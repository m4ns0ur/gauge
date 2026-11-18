package task_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/testutil"
)

func TestRepoContentLoads(t *testing.T) {
	ctx := context.Background()
	tasks, err := task.LoadAll(ctx, "../../tasks")
	if err != nil {
		t.Fatalf("load bundled tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("no bundled tasks discovered")
	}
	found := false
	for _, candidate := range tasks {
		if candidate.ID == "greeting-all" {
			found = true
			if len(candidate.Prompt) == 0 {
				t.Error("greeting-all prompt is empty")
			}
			if candidate.Fixture.Revision == "" {
				t.Error("greeting-all fixture revision is empty")
			}
			if candidate.EvaluatorDir == "" {
				t.Error("greeting-all evaluator dir is empty")
			}
			if !strings.HasSuffix(candidate.FixtureDir, "greeting-go.git") {
				t.Errorf("fixture dir = %q", candidate.FixtureDir)
			}
		}
	}
	if !found {
		t.Fatalf("greeting-all not found in %d tasks", len(tasks))
	}
}

func TestLoadValidTask(t *testing.T) {
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{
		"README.md": "demo\n",
	})
	dir := bench.AddTask(testutil.TaskOptions{
		ID:          "write-answer",
		FixtureRepo: repo,
		Revision:    revision,
	})

	loaded, err := task.Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ID != "write-answer" {
		t.Errorf("ID = %q", loaded.ID)
	}
	if !strings.Contains(loaded.Prompt, "answer.txt") {
		t.Errorf("prompt = %q", loaded.Prompt)
	}
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	realEval, err := filepath.EvalSymlinks(filepath.Join(dir, "evaluator"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FixtureDir != realRepo {
		t.Errorf("FixtureDir = %q, want %q", loaded.FixtureDir, realRepo)
	}
	if loaded.EvaluatorDir != realEval {
		t.Errorf("EvaluatorDir = %q, want %q", loaded.EvaluatorDir, realEval)
	}

	fp, err := loaded.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if !strings.HasPrefix(fp, "sha256:") {
		t.Errorf("fingerprint = %q", fp)
	}
	again, err := task.Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	fpAgain, err := again.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint again: %v", err)
	}
	if fp != fpAgain {
		t.Error("fingerprint is not stable across loads")
	}

	// Changing evaluator assets must change the fingerprint.
	script := filepath.Join(dir, "evaluator", "evaluate.sh")
	testutil.WriteFile(t, script, testutil.DefaultEvaluator+"\n# changed\n", 0o755)
	changed, err := task.Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("reload after change: %v", err)
	}
	fpChanged, err := changed.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint after change: %v", err)
	}
	if fp == fpChanged {
		t.Error("fingerprint did not change when evaluator assets changed")
	}
}

func TestLoadValidationErrors(t *testing.T) {
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{"main.go": "package main\n"})
	dir := bench.AddTask(testutil.TaskOptions{FixtureRepo: repo, Revision: revision})
	baseline := testutil.ReadFile(t, filepath.Join(dir, "task.yaml"))
	replace := func(old, new string) func(*testing.T, string) string {
		return func(t *testing.T, _ string) string {
			t.Helper()
			if !strings.Contains(baseline, old) {
				t.Fatalf("baseline does not contain %q", old)
			}
			return strings.Replace(baseline, old, new, 1)
		}
	}

	cases := []struct {
		name    string
		mutate  func(t *testing.T, dir string) string
		wantSub string
	}{
		{
			name:    "unsupported schema",
			mutate:  replace("schema_version: 1\n", "schema_version: 2\n"),
			wantSub: "schema_version",
		},
		{
			name:    "unknown field",
			mutate:  func(_ *testing.T, _ string) string { return baseline + "unexpected: true\n" },
			wantSub: "unexpected",
		},
		{
			name:    "invalid id",
			mutate:  replace("id: write-answer\n", "id: Write Answer\n"),
			wantSub: "id",
		},
		{
			name: "missing prompt file",
			mutate: func(t *testing.T, dir string) string {
				if err := os.Remove(filepath.Join(dir, "prompt.md")); err != nil {
					t.Fatal(err)
				}
				return baseline
			},
			wantSub: "prompt",
		},
		{
			name:    "prompt traversal",
			mutate:  replace("prompt: prompt.md\n", "prompt: ../prompt.md\n"),
			wantSub: "prompt",
		},
		{
			name: "absolute fixture path",
			mutate: func(_ *testing.T, _ string) string {
				return strings.Replace(baseline, "path: ", "path: /tmp/", 1)
			},
			wantSub: "fixture.path",
		},
		{
			name:    "short revision",
			mutate:  replace("revision: "+revision, "revision: main"),
			wantSub: "40-character",
		},
		{
			name:    "unknown revision",
			mutate:  replace("revision: "+revision, "revision: "+strings.Repeat("0", 40)),
			wantSub: "does not exist",
		},
		{
			name:    "invalid execution timeout",
			mutate:  replace("timeout: 30s", "timeout: soon"),
			wantSub: "execution.timeout",
		},
		{
			name:    "zero evaluation timeout",
			mutate:  replace("timeout: 10s", "timeout: 0s"),
			wantSub: "evaluation.timeout",
		},
		{
			name:    "evaluator command absolute",
			mutate:  replace("    - ./evaluate.sh\n", "    - /bin/sh\n"),
			wantSub: "evaluation.command[0]",
		},
		{
			name:    "evaluator traversal",
			mutate:  replace("  path: evaluator\n", "  path: ../evaluator\n"),
			wantSub: "evaluation.path",
		},
		{
			name: "missing evaluator",
			mutate: func(t *testing.T, dir string) string {
				if err := os.RemoveAll(filepath.Join(dir, "evaluator")); err != nil {
					t.Fatal(err)
				}
				return baseline
			},
			wantSub: "evaluation.path",
		},
		{
			name:    "empty setup command",
			mutate:  replace("  network: disabled\n", "  network: disabled\n  setup:\n    - []\n"),
			wantSub: "execution.setup[0]",
		},
		{
			name:    "duplicate tag",
			mutate:  replace("description: Synthetic task for tests.\n", "description: Synthetic task for tests.\ntags:\n  - go\n  - go\n"),
			wantSub: "duplicate tag",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := filepath.Join(bench.TasksDir, "case-"+strings.ReplaceAll(tc.name, " ", "-"))
			if err := os.MkdirAll(caseDir, 0o755); err != nil {
				t.Fatal(err)
			}
			copyTree(t, dir, caseDir)
			testutil.WriteFile(t, filepath.Join(caseDir, "task.yaml"), tc.mutate(t, caseDir), 0o644)

			_, err := task.Load(context.Background(), caseDir)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), "task.yaml") {
				t.Errorf("error %q does not name task.yaml", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestLoadAllDuplicateIDs(t *testing.T) {
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{"a.txt": "a\n"})
	bench.AddTask(testutil.TaskOptions{ID: "dup", FixtureRepo: repo, Revision: revision})

	second := filepath.Join(bench.TasksDir, "second")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join(bench.TasksDir, "dup"), second)
	// Make the second copy loadable on its own: it already points at the same
	// fixture through the same relative path depth.
	_, err := task.LoadAll(context.Background(), bench.TasksDir)
	if err == nil || !strings.Contains(err.Error(), "duplicate task id") {
		t.Fatalf("expected duplicate id error, got %v", err)
	}
}

func TestLoadAllMissingRoot(t *testing.T) {
	_, err := task.LoadAll(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestFingerprintIsStableAcrossPaths(t *testing.T) {
	files := map[string]string{"a.txt": "a\n"}
	firstBench := testutil.NewBench(t)
	firstRepo, firstRev := firstBench.AddFixture("demo", files)
	firstDir := firstBench.AddTask(testutil.TaskOptions{FixtureRepo: firstRepo, Revision: firstRev})

	secondBench := testutil.NewBench(t)
	secondRepo, secondRev := secondBench.AddFixture("demo", files)
	secondDir := secondBench.AddTask(testutil.TaskOptions{FixtureRepo: secondRepo, Revision: secondRev})
	if firstRev != secondRev {
		t.Fatalf("fixture revisions differ: %s vs %s", firstRev, secondRev)
	}

	first, err := task.Load(context.Background(), firstDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := task.Load(context.Background(), secondDir)
	if err != nil {
		t.Fatal(err)
	}
	fp1, err := first.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := second.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Error("fingerprint changed when only absolute paths changed")
	}
}

func TestDurationParsing(t *testing.T) {
	var d task.Duration
	if err := d.UnmarshalYAML(yamlScalar("90s")); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Duration() != 90*time.Second {
		t.Errorf("duration = %s", d.Duration())
	}
	if err := d.Err(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	var bad task.Duration
	if err := bad.UnmarshalYAML(yamlScalar("eventually")); err != nil {
		t.Fatalf("parse should defer errors to validation: %v", err)
	}
	if bad.Err() == nil {
		t.Error("expected a deferred parse error")
	}

	encoded, err := json.Marshal(task.NewDuration(90 * time.Second))
	if err != nil {
		t.Fatalf("marshal duration: %v", err)
	}
	if string(encoded) != `"1m30s"` {
		t.Errorf("duration JSON = %s", encoded)
	}
}

// yamlScalar builds a minimal *yaml.Node for duration parsing tests.
func yamlScalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy tree: %v", err)
	}
}
