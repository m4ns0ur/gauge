package suite_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m4ns0ur/gauge/internal/suite"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/testutil"
)

func TestRepoSuiteLoadsAndResolves(t *testing.T) {
	suites, err := suite.LoadAll("../../suites")
	if err != nil {
		t.Fatalf("load bundled suites: %v", err)
	}
	if len(suites) == 0 {
		t.Fatal("no bundled suites discovered")
	}
	tasks, err := task.LoadAll(context.Background(), "../../tasks")
	if err != nil {
		t.Fatalf("load bundled tasks: %v", err)
	}
	smoke, err := suite.Find(suites, "smoke")
	if err != nil {
		t.Fatalf("find smoke: %v", err)
	}
	resolved, err := smoke.Resolve(tasks)
	if err != nil {
		t.Fatalf("resolve smoke: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != "greeting-all" {
		t.Fatalf("resolved = %v", resolved)
	}
	if !strings.HasPrefix(smoke.Fingerprint(), "sha256:") {
		t.Errorf("fingerprint = %q", smoke.Fingerprint())
	}
}

func TestValidSuite(t *testing.T) {
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{"a.txt": "a\n"})
	bench.AddTask(testutil.TaskOptions{ID: "one", FixtureRepo: repo, Revision: revision})
	file := bench.AddSuite("mini", "one")

	loaded, err := suite.LoadFile(file)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ID != "mini" {
		t.Errorf("ID = %q", loaded.ID)
	}
	if loaded.File != file {
		t.Errorf("File = %q, want %q", loaded.File, file)
	}
}

func TestDuplicateEntries(t *testing.T) {
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{"a.txt": "a\n"})
	bench.AddTask(testutil.TaskOptions{ID: "one", FixtureRepo: repo, Revision: revision})

	withoutReason := filepath.Join(bench.SuitesDir, "bad.yaml")
	testutil.WriteFile(t, withoutReason, `schema_version: 1
id: bad
name: Bad
tasks:
  - id: one
  - id: one
`, 0o644)
	if _, err := suite.LoadFile(withoutReason); err == nil || !strings.Contains(err.Error(), "explicit reason") {
		t.Fatalf("expected duplicate reason error, got %v", err)
	}

	withReason := filepath.Join(bench.SuitesDir, "repeated.yaml")
	testutil.WriteFile(t, withReason, `schema_version: 1
id: repeated
name: Repeated
tasks:
  - id: one
  - id: one
    reason: run twice in this suite
`, 0o644)
	loaded, err := suite.LoadFile(withReason)
	if err != nil {
		t.Fatalf("load with reason: %v", err)
	}
	tasks, err := task.LoadAll(context.Background(), bench.TasksDir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := loaded.Resolve(tasks)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved %d tasks, want 2", len(resolved))
	}
}

func TestSuiteValidationErrors(t *testing.T) {
	bench := testutil.NewBench(t)
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{
			name:    "unsupported schema",
			content: "schema_version: 2\nid: x\nname: X\ntasks:\n  - id: a\n",
			wantSub: "schema_version",
		},
		{
			name:    "no tasks",
			content: "schema_version: 1\nid: x\nname: X\ntasks: []\n",
			wantSub: "tasks",
		},
		{
			name:    "bad id",
			content: "schema_version: 1\nid: Not Valid\nname: X\ntasks:\n  - id: a\n",
			wantSub: "id",
		},
		{
			name:    "unknown field",
			content: "schema_version: 1\nid: x\nname: X\ntasks:\n  - id: a\nretries: 3\n",
			wantSub: "retries",
		},
		{
			name:    "bad task id",
			content: "schema_version: 1\nid: x\nname: X\ntasks:\n  - id: Bad Task\n",
			wantSub: "tasks[0].id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(bench.SuitesDir, strings.ReplaceAll(tc.name, " ", "-")+".yaml")
			testutil.WriteFile(t, file, tc.content, 0o644)
			_, err := suite.LoadFile(file)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestResolveMissingTask(t *testing.T) {
	bench := testutil.NewBench(t)
	file := bench.AddSuite("broken", "does-not-exist")
	loaded, err := suite.LoadFile(file)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err = loaded.Resolve(nil)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected missing task error, got %v", err)
	}
}

func TestLoadAllDetectsDuplicateSuiteIDs(t *testing.T) {
	bench := testutil.NewBench(t)
	first := bench.AddSuite("same", "a")
	second := filepath.Join(bench.SuitesDir, "other.yaml")
	testutil.WriteFile(t, second, `schema_version: 1
id: same
name: Same
tasks:
  - id: a
`, 0o644)
	if _, err := suite.LoadAll(bench.SuitesDir); err == nil || !strings.Contains(err.Error(), "duplicate suite id") {
		t.Fatalf("expected duplicate suite error, got %v (first %s)", err, first)
	}
}

func TestLoadAllMissingRoot(t *testing.T) {
	if _, err := suite.LoadAll(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error for missing suites root")
	}
}
