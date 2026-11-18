// Package suite defines Gauge's versioned suite specification: a named,
// ordered selection of task IDs.
package suite

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/m4ns0ur/gauge/internal/task"
)

// SchemaVersion is the supported suite schema version.
const SchemaVersion = 1

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Suite is a versioned selection of tasks.
type Suite struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	ID            string    `yaml:"id" json:"id"`
	Name          string    `yaml:"name" json:"name"`
	Description   string    `yaml:"description,omitempty" json:"description,omitempty"`
	Tasks         []TaskRef `yaml:"tasks" json:"tasks"`

	// File is the absolute path suite.yaml was loaded from.
	File string `yaml:"-" json:"-"`
}

// TaskRef is one task entry. Reason is required when the same task appears
// more than once; conditional repetition like that must be explicit.
type TaskRef struct {
	ID     string `yaml:"id" json:"id"`
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// ValidationError identifies the suite file and field that failed validation.
type ValidationError struct {
	File  string
	Field string
	Msg   string
}

// Error implements error.
func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.File, e.Msg)
	}
	return fmt.Sprintf("%s: field %q: %s", e.File, e.Field, e.Msg)
}

func verr(file, field, format string, args ...any) error {
	return &ValidationError{File: file, Field: field, Msg: fmt.Sprintf(format, args...)}
}

// LoadFile reads and validates a suite YAML document. Task existence is checked
// by Resolve, which requires the discovered task set.
func LoadFile(file string) (*Suite, error) {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("resolve suite file %q: %w", file, err)
	}
	info, err := os.Stat(absFile)
	if err != nil {
		return nil, fmt.Errorf("access suite file %q: %w", absFile, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("suite path %q is a directory, want a YAML file", absFile)
	}
	data, err := os.ReadFile(absFile)
	if err != nil {
		return nil, fmt.Errorf("read suite %s: %w", absFile, err)
	}

	var s Suite
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, verr(absFile, "", "invalid YAML: %v", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != nil && !errors.Is(err, io.EOF) {
		return nil, verr(absFile, "", "invalid trailing YAML content: %v", err)
	} else if err == nil {
		return nil, verr(absFile, "", "a suite file must contain exactly one YAML document")
	}
	s.File = absFile

	if err := validate(&s, absFile); err != nil {
		return nil, err
	}
	return &s, nil
}

func validate(s *Suite, file string) error {
	if s.SchemaVersion != SchemaVersion {
		return verr(file, "schema_version", "unsupported version %d (this Gauge supports %d)", s.SchemaVersion, SchemaVersion)
	}
	if !idRE.MatchString(s.ID) {
		return verr(file, "id", "must match %s", idRE)
	}
	if strings.TrimSpace(s.Name) == "" {
		return verr(file, "name", "is required")
	}
	if len(s.Name) > 120 {
		return verr(file, "name", "must be at most 120 characters")
	}
	if len(s.Description) > 1000 {
		return verr(file, "description", "must be at most 1000 characters")
	}
	if len(s.Tasks) == 0 {
		return verr(file, "tasks", "must select at least one task")
	}
	if len(s.Tasks) > 500 {
		return verr(file, "tasks", "must contain at most 500 entries")
	}
	firstSeen := make(map[string]int, len(s.Tasks))
	for i, ref := range s.Tasks {
		field := fmt.Sprintf("tasks[%d]", i)
		if !idRE.MatchString(ref.ID) {
			return verr(file, field+".id", "must be a task id matching %s", idRE)
		}
		if first, dup := firstSeen[ref.ID]; dup {
			if strings.TrimSpace(ref.Reason) == "" {
				return verr(file, field+".reason", "task %q already listed at tasks[%d]; repeat an entry only with an explicit reason", ref.ID, first)
			}
		} else {
			firstSeen[ref.ID] = i
			if strings.TrimSpace(ref.Reason) != "" {
				return verr(file, field+".reason", "reason is only meaningful on repeated entries")
			}
		}
	}
	return nil
}

// Resolve verifies that every referenced task exists. It returns the selected
// tasks in suite order, including repetitions.
func (s *Suite) Resolve(tasks []*task.Task) ([]*task.Task, error) {
	byID := make(map[string]*task.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	resolved := make([]*task.Task, 0, len(s.Tasks))
	for i, ref := range s.Tasks {
		t, ok := byID[ref.ID]
		if !ok {
			return nil, verr(s.File, fmt.Sprintf("tasks[%d].id", i), "task %q does not exist in the configured task directories", ref.ID)
		}
		resolved = append(resolved, t)
	}
	return resolved, nil
}

// LoadAll discovers suites beneath root, sorted by ID.
func LoadAll(root string) ([]*Suite, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve suites root %q: %w", root, err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("access suites root %q: %w", absRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("suites root %q is not a directory", absRoot)
	}

	var files []string
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover suites under %q: %w", absRoot, err)
	}
	sort.Strings(files)

	byID := make(map[string]string, len(files))
	suites := make([]*Suite, 0, len(files))
	for _, file := range files {
		s, err := LoadFile(file)
		if err != nil {
			return nil, err
		}
		if prev, dup := byID[s.ID]; dup {
			return nil, fmt.Errorf("duplicate suite id %q in %s and %s", s.ID, prev, file)
		}
		byID[s.ID] = file
		suites = append(suites, s)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].ID < suites[j].ID })
	return suites, nil
}

// Find returns the suite with id, or a helpful error.
func Find(suites []*Suite, id string) (*Suite, error) {
	for _, s := range suites {
		if s.ID == id {
			return s, nil
		}
	}
	ids := make([]string, 0, len(suites))
	for _, s := range suites {
		ids = append(ids, s.ID)
	}
	return nil, fmt.Errorf("suite %q not found (available: %s)", id, strings.Join(ids, ", "))
}

// Fingerprint is a stable hash of the suite definition (ordered task IDs and
// reasons). Task content hashes are recorded separately per attempt.
func (s *Suite) Fingerprint() string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema=%d\nid=%s\n", s.SchemaVersion, s.ID)
	for _, ref := range s.Tasks {
		fmt.Fprintf(&b, "task=%s\nreason=%s\n", ref.ID, ref.Reason)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("sha256:%x", sum)
}
