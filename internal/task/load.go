package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxPromptBytes = 64 << 10
	maxSetupSteps  = 32
	maxArgs        = 64
)

var (
	idRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	categoryRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	tagRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]{0,31}$`)
	revisionRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func verr(file, field, format string, args ...any) error {
	return &ValidationError{File: file, Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Load reads, validates, and resolves one task directory.
func Load(ctx context.Context, dir string) (*Task, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve task directory %q: %w", dir, err)
	}
	if err := requireDir(absDir); err != nil {
		return nil, err
	}
	file := File(absDir)
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read task %s: %w", file, err)
	}

	var t Task
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, verr(file, "", "invalid YAML: %v", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != nil && !errors.Is(err, io.EOF) {
		return nil, verr(file, "", "invalid trailing YAML content: %v", err)
	} else if err == nil {
		return nil, verr(file, "", "task.yaml must contain exactly one YAML document")
	}

	t.Dir = absDir
	if err := validateSchema(&t, file); err != nil {
		return nil, err
	}
	if err := resolve(ctx, &t, file); err != nil {
		return nil, err
	}
	return &t, nil
}

func validateSchema(t *Task, file string) error {
	if t.SchemaVersion != SchemaVersion {
		return verr(file, "schema_version", "unsupported version %d (this Gauge supports %d)", t.SchemaVersion, SchemaVersion)
	}
	if !idRE.MatchString(t.ID) {
		return verr(file, "id", "must match %s (lowercase letters, digits, and hyphens)", idRE)
	}
	if strings.TrimSpace(t.Name) == "" {
		return verr(file, "name", "is required")
	}
	if len(t.Name) > 120 {
		return verr(file, "name", "must be at most 120 characters")
	}
	if !categoryRE.MatchString(t.Category) {
		return verr(file, "category", "must match %s (for example repository-engineering)", categoryRE)
	}
	switch t.Difficulty {
	case DifficultyEasy, DifficultyMedium, DifficultyHard:
	case "":
		return verr(file, "difficulty", "is required (easy, medium, or hard)")
	default:
		return verr(file, "difficulty", "must be easy, medium, or hard, got %q", t.Difficulty)
	}
	if len(t.Description) > 1000 {
		return verr(file, "description", "must be at most 1000 characters")
	}
	if len(t.Tags) > 20 {
		return verr(file, "tags", "must contain at most 20 tags")
	}
	seenTags := make(map[string]struct{}, len(t.Tags))
	for i, tag := range t.Tags {
		if !tagRE.MatchString(tag) {
			return verr(file, fmt.Sprintf("tags[%d]", i), "invalid tag %q", tag)
		}
		if _, dup := seenTags[tag]; dup {
			return verr(file, "tags", "duplicate tag %q", tag)
		}
		seenTags[tag] = struct{}{}
	}

	if strings.TrimSpace(t.Fixture.Path) == "" {
		return verr(file, "fixture.path", "is required")
	}
	if filepath.IsAbs(t.Fixture.Path) || strings.HasPrefix(t.Fixture.Path, "/") {
		return verr(file, "fixture.path", "must be relative to the task directory so tasks stay portable")
	}
	if !revisionRE.MatchString(t.Fixture.Revision) {
		return verr(file, "fixture.revision", "must be a full 40-character lowercase commit SHA, got %q", t.Fixture.Revision)
	}

	if strings.TrimSpace(t.PromptFile) == "" {
		return verr(file, "prompt", "is required")
	}

	if err := t.Execution.Timeout.Err(); err != nil {
		return verr(file, "execution.timeout", "%v", err)
	}
	if t.Execution.Timeout.Duration() <= 0 {
		return verr(file, "execution.timeout", "is required and must be positive (for example 10m)")
	}
	if t.Execution.Timeout.Duration() > 6*time.Hour {
		return verr(file, "execution.timeout", "must be at most 6h")
	}
	if t.Execution.Network == "" {
		t.Execution.Network = NetworkDisabled
	}
	switch t.Execution.Network {
	case NetworkDisabled, NetworkAllowed:
	default:
		return verr(file, "execution.network", "must be %q or %q, got %q", NetworkDisabled, NetworkAllowed, t.Execution.Network)
	}
	if len(t.Execution.Setup) > maxSetupSteps {
		return verr(file, "execution.setup", "must contain at most %d commands", maxSetupSteps)
	}
	for i, argv := range t.Execution.Setup {
		field := fmt.Sprintf("execution.setup[%d]", i)
		if len(argv) == 0 {
			return verr(file, field, "command must not be empty")
		}
		if len(argv) > maxArgs {
			return verr(file, field, "command must have at most %d arguments", maxArgs)
		}
		for j, arg := range argv {
			if strings.TrimSpace(arg) == "" {
				return verr(file, fmt.Sprintf("%s[%d]", field, j), "argument must not be empty")
			}
			if strings.ContainsRune(arg, 0) {
				return verr(file, fmt.Sprintf("%s[%d]", field, j), "argument must not contain NUL")
			}
		}
	}

	if strings.TrimSpace(t.Evaluation.Path) == "" {
		return verr(file, "evaluation.path", "is required")
	}
	if len(t.Evaluation.Command) == 0 {
		return verr(file, "evaluation.command", "is required (for example [./evaluate.sh])")
	}
	if len(t.Evaluation.Command) > maxArgs {
		return verr(file, "evaluation.command", "must contain at most %d elements", maxArgs)
	}
	for i, arg := range t.Evaluation.Command {
		if strings.TrimSpace(arg) == "" {
			return verr(file, fmt.Sprintf("evaluation.command[%d]", i), "must not be empty")
		}
		if strings.ContainsRune(arg, 0) {
			return verr(file, fmt.Sprintf("evaluation.command[%d]", i), "must not contain NUL")
		}
	}
	if filepath.IsAbs(t.Evaluation.Command[0]) || strings.HasPrefix(t.Evaluation.Command[0], "/") {
		return verr(file, "evaluation.command[0]", "must not be an absolute path; resolve relative to evaluation.path")
	}
	if err := t.Evaluation.Timeout.Err(); err != nil {
		return verr(file, "evaluation.timeout", "%v", err)
	}
	if t.Evaluation.Timeout.Duration() <= 0 {
		return verr(file, "evaluation.timeout", "is required and must be positive (for example 2m)")
	}
	if t.Evaluation.Timeout.Duration() > time.Hour {
		return verr(file, "evaluation.timeout", "must be at most 1h")
	}

	if t.Provenance != nil {
		if len(t.Provenance.Origin) > 500 || len(t.Provenance.License) > 200 || len(t.Provenance.Notes) > 2000 {
			return verr(file, "provenance", "values are too long")
		}
	}
	return nil
}

func resolve(ctx context.Context, t *Task, file string) error {
	promptPath, err := resolveWithin(t.Dir, t.PromptFile)
	if err != nil {
		return verr(file, "prompt", "%v", err)
	}
	info, err := os.Stat(promptPath)
	if err != nil {
		return verr(file, "prompt", "cannot read %q: %v", t.PromptFile, err)
	}
	if info.IsDir() {
		return verr(file, "prompt", "%q is a directory, want a file", t.PromptFile)
	}
	if info.Size() > maxPromptBytes {
		return verr(file, "prompt", "%q is larger than %d bytes", t.PromptFile, maxPromptBytes)
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return verr(file, "prompt", "cannot read %q: %v", t.PromptFile, err)
	}
	if strings.TrimSpace(string(prompt)) == "" {
		return verr(file, "prompt", "%q is empty", t.PromptFile)
	}
	t.Prompt = string(prompt)

	evalDir, err := resolveWithin(t.Dir, t.Evaluation.Path)
	if err != nil {
		return verr(file, "evaluation.path", "%v", err)
	}
	evalInfo, err := os.Stat(evalDir)
	if err != nil {
		return verr(file, "evaluation.path", "cannot access %q: %v", t.Evaluation.Path, err)
	}
	if !evalInfo.IsDir() {
		return verr(file, "evaluation.path", "%q must be a directory", t.Evaluation.Path)
	}
	t.EvaluatorDir = evalDir
	if err := resolveCommand(t, evalDir, file); err != nil {
		return err
	}

	fixturePath := filepath.Join(t.Dir, filepath.FromSlash(t.Fixture.Path))
	fixturePath, err = filepath.Abs(fixturePath)
	if err != nil {
		return verr(file, "fixture.path", "cannot resolve %q: %v", t.Fixture.Path, err)
	}
	if err := requireDir(fixturePath); err != nil {
		return verr(file, "fixture.path", "%v", err)
	}
	realFixture, err := filepath.EvalSymlinks(fixturePath)
	if err != nil {
		return verr(file, "fixture.path", "cannot resolve symlinks: %v", err)
	}
	t.FixtureDir = realFixture
	if err := checkGitRevision(ctx, realFixture, t.Fixture.Revision); err != nil {
		return verr(file, "fixture.revision", "%v", err)
	}
	return nil
}

func resolveCommand(t *Task, evalDir, file string) error {
	first := t.Evaluation.Command[0]
	if !strings.Contains(first, "/") && !strings.Contains(first, string(filepath.Separator)) && first != "." && first != ".." {
		// Bare command name: resolved from PATH at evaluation time.
		return nil
	}
	resolved, err := resolveWithin(evalDir, first)
	if err != nil {
		return verr(file, "evaluation.command[0]", "%v", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return verr(file, "evaluation.command[0]", "cannot access %q: %v", first, err)
	}
	if info.IsDir() {
		return verr(file, "evaluation.command[0]", "%q is a directory", first)
	}
	return nil
}

// resolveWithin joins rel to base and rejects absolute paths, traversal, and
// symlink escapes. The returned path is absolute and symlink-resolved.
func resolveWithin(base, rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%q must be relative", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes its root directory", rel)
	}
	joined := filepath.Join(base, clean)
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", base, err)
	}
	realPath, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", rel, err)
	}
	if !within(realBase, realPath) {
		return "", fmt.Errorf("%q resolves outside %q", rel, base)
	}
	return realPath, nil
}

func within(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func requireDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	return nil
}

func checkGitRevision(ctx context.Context, repo, revision string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git executable not found in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--git-dir")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%q is not a Git repository: %s", repo, strings.TrimSpace(string(out)))
	}
	cmd = exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("revision %s does not exist in %q", revision, repo)
	}
	resolved := strings.TrimSpace(string(out))
	if resolved != revision {
		return fmt.Errorf("revision %s resolves to %s; tasks must pin the full commit SHA", revision, resolved)
	}
	return nil
}

// LoadAll discovers and loads every task beneath root, sorted by ID.
func LoadAll(ctx context.Context, root string) ([]*Task, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve tasks root %q: %w", root, err)
	}
	if err := requireDir(absRoot); err != nil {
		return nil, fmt.Errorf("tasks root: %w", err)
	}

	var dirs []string
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" || name == "evaluator" || name == "fixtures" || name == "node_modules" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		depth := 0
		if rel != "." {
			depth = len(strings.Split(rel, string(filepath.Separator)))
		}
		if depth > 3 {
			return filepath.SkipDir
		}
		if _, err := os.Stat(File(path)); err == nil {
			dirs = append(dirs, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover tasks under %q: %w", absRoot, err)
	}

	byID := make(map[string]string, len(dirs))
	tasks := make([]*Task, 0, len(dirs))
	for _, dir := range dirs {
		t, err := Load(ctx, dir)
		if err != nil {
			return nil, err
		}
		if prev, dup := byID[t.ID]; dup {
			return nil, fmt.Errorf("duplicate task id %q in %s and %s", t.ID, prev, dir)
		}
		byID[t.ID] = dir
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// Find returns the task with id, or a helpful error listing known IDs.
func Find(tasks []*Task, id string) (*Task, error) {
	for _, t := range tasks {
		if t.ID == id {
			return t, nil
		}
	}
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	return nil, fmt.Errorf("task %q not found (available: %s)", id, strings.Join(ids, ", "))
}
