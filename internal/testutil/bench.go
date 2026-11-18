// Package testutil builds small, isolated benchmark repositories for tests.
//
// Tests use it to exercise the real loader, workspace, runner, and CLI paths
// with a real Git fixture and real evaluator subprocesses, without a model or
// network access.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Bench is a temporary benchmark content tree.
type Bench struct {
	t          *testing.T
	Root       string
	Fixtures   string
	TasksDir   string
	SuitesDir  string
	DataDir    string
	ConfigPath string
}

// NewBench creates the directory layout.
func NewBench(t *testing.T) *Bench {
	t.Helper()
	root := t.TempDir()
	b := &Bench{
		t:          t,
		Root:       root,
		Fixtures:   filepath.Join(root, "fixtures"),
		TasksDir:   filepath.Join(root, "tasks"),
		SuitesDir:  filepath.Join(root, "suites"),
		DataDir:    filepath.Join(root, "data"),
		ConfigPath: filepath.Join(root, "gauge.toml"),
	}
	for _, dir := range []string{b.Fixtures, b.TasksDir, b.SuitesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	return b
}

// AddFixture commits files in a fresh Git repository and returns a bare clone
// plus its revision, mirroring how bundled fixtures are stored.
func (b *Bench) AddFixture(name string, files map[string]string) (repo string, revision string) {
	b.t.Helper()
	work := filepath.Join(b.Root, "fixture-src", name)
	if err := os.MkdirAll(work, 0o755); err != nil {
		b.t.Fatalf("create fixture source: %v", err)
	}
	for rel, content := range files {
		WriteFile(b.t, filepath.Join(work, filepath.FromSlash(rel)), content, 0o644)
	}
	runGit(b.t, work, "init", "--quiet", "--initial-branch=main")
	runGit(b.t, work, "add", "-A")
	runGitCommit(b.t, work, "fixture "+name)
	revision = strings.TrimSpace(runGit(b.t, work, "rev-parse", "HEAD"))

	bare := filepath.Join(b.Fixtures, name+".git")
	runGit(b.t, b.Root, "clone", "--quiet", "--bare", work, bare)
	runGit(b.t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return bare, revision
}

// TaskOptions describes a synthetic task.
type TaskOptions struct {
	ID                string
	Category          string
	Difficulty        string
	Name              string
	Prompt            string
	FixtureRepo       string
	Revision          string
	EvaluateScript    string
	ExecutionTimeout  time.Duration
	EvaluationTimeout time.Duration
	Setup             [][]string
	Network           string
}

// DefaultEvaluator checks that the candidate wrote 42 to answer.txt.
const DefaultEvaluator = `#!/bin/sh
set -u
workspace=$1
if [ -f "$workspace/answer.txt" ] && [ "$(cat "$workspace/answer.txt")" = "42" ]; then
  printf '%s' '{"schema_version":1,"status":"passed","summary":"answer.txt contains 42.","checks":[{"id":"answer-written","status":"passed","message":"answer.txt contains 42."}]}'
else
  printf '%s' '{"schema_version":1,"status":"failed","summary":"answer.txt does not contain 42.","checks":[{"id":"answer-written","status":"failed","message":"answer.txt is missing or wrong."}]}'
fi
`

// AddTask writes a task directory and returns its path.
func (b *Bench) AddTask(opts TaskOptions) string {
	b.t.Helper()
	if opts.ID == "" {
		opts.ID = "write-answer"
	}
	if opts.Category == "" {
		opts.Category = "repository-engineering"
	}
	if opts.Difficulty == "" {
		opts.Difficulty = "easy"
	}
	if opts.Name == "" {
		opts.Name = "Write the answer"
	}
	if opts.Prompt == "" {
		opts.Prompt = "Write 42 to answer.txt in the repository."
	}
	if opts.EvaluateScript == "" {
		opts.EvaluateScript = DefaultEvaluator
	}
	if opts.ExecutionTimeout <= 0 {
		opts.ExecutionTimeout = 30 * time.Second
	}
	if opts.EvaluationTimeout <= 0 {
		opts.EvaluationTimeout = 10 * time.Second
	}
	if opts.Network == "" {
		opts.Network = "disabled"
	}

	dir := filepath.Join(b.TasksDir, opts.ID)
	if err := os.MkdirAll(filepath.Join(dir, "evaluator"), 0o755); err != nil {
		b.t.Fatalf("create task dir: %v", err)
	}
	relFixture, err := filepath.Rel(dir, opts.FixtureRepo)
	if err != nil {
		b.t.Fatalf("relative fixture path: %v", err)
	}
	WriteFile(b.t, filepath.Join(dir, "prompt.md"), opts.Prompt+"\n", 0o644)
	WriteFile(b.t, filepath.Join(dir, "evaluator", "evaluate.sh"), opts.EvaluateScript, 0o755)
	WriteFile(b.t, filepath.Join(dir, "evaluator", "checks.md"), "hidden evaluator assets\n", 0o644)

	var setup strings.Builder
	if len(opts.Setup) > 0 {
		setup.WriteString("  setup:\n")
		for _, argv := range opts.Setup {
			setup.WriteString("    - [")
			for i, arg := range argv {
				if i > 0 {
					setup.WriteString(", ")
				}
				fmt.Fprintf(&setup, "%q", arg)
			}
			setup.WriteString("]\n")
		}
	}

	yaml := fmt.Sprintf(`schema_version: 1
id: %s
name: %s
category: %s
difficulty: %s
description: Synthetic task for tests.
fixture:
  path: %s
  revision: %s
prompt: prompt.md
execution:
  timeout: %s
  network: %s
%s
evaluation:
  path: evaluator
  command:
    - ./evaluate.sh
  timeout: %s
`, opts.ID, opts.Name, opts.Category, opts.Difficulty, filepath.ToSlash(relFixture), opts.Revision,
		formatDuration(opts.ExecutionTimeout), opts.Network, setup.String(), formatDuration(opts.EvaluationTimeout))
	WriteFile(b.t, filepath.Join(dir, "task.yaml"), yaml, 0o644)
	return dir
}

// AddSuite writes a suite file and returns its path.
func (b *Bench) AddSuite(id string, taskIDs ...string) string {
	b.t.Helper()
	var tasks strings.Builder
	for _, taskID := range taskIDs {
		fmt.Fprintf(&tasks, "  - id: %s\n", taskID)
	}
	content := fmt.Sprintf(`schema_version: 1
id: %s
name: %s
description: Synthetic suite for tests.
tasks:
%s`, id, id, tasks.String())
	file := filepath.Join(b.SuitesDir, id+".yaml")
	WriteFile(b.t, file, content, 0o644)
	return file
}

// WriteConfig writes a config file with absolute content and data paths plus
// extra TOML (for adapter options).
func (b *Bench) WriteConfig(extra string) string {
	b.t.Helper()
	content := fmt.Sprintf(`data_dir = %q
tasks_dir = %q
suites_dir = %q

[retention]
workspace = "keep_failed"

[defaults]
repetitions = 1
%s`, b.DataDir, b.TasksDir, b.SuitesDir, extra)
	WriteFile(b.t, b.ConfigPath, content, 0o644)
	return b.ConfigPath
}

// WriteFile writes a file after creating its parent directory.
func WriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent of %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ReadFile reads a file or fails the test.
func ReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func formatDuration(d time.Duration) string {
	return d.String()
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Gauge Test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Gauge Test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runGitCommit(t *testing.T, dir, message string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "--quiet", "-m", message)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Gauge Test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Gauge Test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}
