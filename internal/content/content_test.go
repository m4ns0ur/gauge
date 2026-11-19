package content_test

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/m4ns0ur/gauge/internal/suite"
	"github.com/m4ns0ur/gauge/internal/task"
)

// TestBundledContent loads every task and suite shipped with Gauge. It catches
// broken YAML, missing fixtures or evaluator assets, moving revisions, and a
// task that is missing from the full suite before anyone runs a benchmark.
func TestBundledContent(t *testing.T) {
	root := repositoryRoot(t)
	ctx := context.Background()

	tasks, err := task.LoadAll(ctx, filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatalf("load bundled tasks: %v", err)
	}
	if len(tasks) < 15 {
		t.Errorf("loaded %d bundled tasks, want at least 15", len(tasks))
	}
	byID := make(map[string]bool, len(tasks))
	for _, bundled := range tasks {
		if byID[bundled.ID] {
			t.Errorf("duplicate task id %q", bundled.ID)
		}
		byID[bundled.ID] = true
		if bundled.Provenance == nil {
			t.Errorf("task %q has no provenance", bundled.ID)
		}
	}
	for _, id := range []string{
		"greeting-all",
		"text-reverse-unicode",
		"text-wordcount",
		"text-truncate",
		"text-dedupe",
		"text-normalize-space",
		"text-snake-case",
		"notes-cli-exit-codes",
		"notes-delete",
		"notes-cli-tag-filter",
		"text-title-case",
		"text-join-nonempty",
		"text-is-palindrome",
		"notes-json-persistence",
		"notes-search",
		"ops-build-recovery",
		"ops-test-recovery",
	} {
		if !byID[id] {
			t.Errorf("bundled task %q is missing", id)
		}
	}

	suites, err := suite.LoadAll(filepath.Join(root, "suites"))
	if err != nil {
		t.Fatalf("load bundled suites: %v", err)
	}
	suiteIDs := make(map[string]bool, len(suites))
	for _, bundled := range suites {
		suiteIDs[bundled.ID] = true
		if _, err := bundled.Resolve(tasks); err != nil {
			t.Errorf("suite %q does not resolve: %v", bundled.ID, err)
		}
	}
	for _, id := range []string{"smoke", "coding", "repository", "operational", "full"} {
		if !suiteIDs[id] {
			t.Errorf("bundled suite %q is missing", id)
		}
	}

	full, err := suite.Find(suites, "full")
	if err != nil {
		t.Fatalf("find full suite: %v", err)
	}
	selected := make(map[string]bool, len(full.Tasks))
	for _, ref := range full.Tasks {
		selected[ref.ID] = true
	}
	var missing []string
	for id := range byID {
		if !selected[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("full suite does not include every bundled task; missing: %v", missing)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
