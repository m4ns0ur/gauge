package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/testutil"
	"github.com/m4ns0ur/gauge/internal/workspace"
)

func setup(t *testing.T) (*workspace.Manager, string, string, string) {
	t.Helper()
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{
		"README.md": "demo\n",
		"main.go":   "package main\n\nfunc main() {}\n",
	})
	attempt := filepath.Join(bench.Root, "attempt")
	if err := os.MkdirAll(attempt, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &workspace.Manager{WorkRoot: bench.Root}
	return manager, repo, revision, attempt
}

func TestPrepare(t *testing.T) {
	manager, repo, revision, attempt := setup(t)
	info, err := manager.Prepare(context.Background(), workspace.PrepareRequest{
		FixtureDir: repo,
		Revision:   revision,
		Dest:       filepath.Join(attempt, "workspace"),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if info.Revision != revision {
		t.Errorf("Revision = %q, want %q", info.Revision, revision)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err != nil {
		t.Errorf("workspace is missing fixture files: %v", err)
	}
	out := git(t, info.Path, "remote")
	if strings.Contains(out, "origin") {
		t.Errorf("workspace still has an origin remote:\n%s", out)
	}
	out = git(t, info.Path, "status", "--porcelain")
	if strings.TrimSpace(out) != "" {
		t.Errorf("fresh workspace is dirty:\n%s", out)
	}
}

func TestPrepareRejectsReuse(t *testing.T) {
	manager, repo, revision, attempt := setup(t)
	dest := filepath.Join(attempt, "workspace")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(dest, "leftover.txt"), "x", 0o644)
	_, err := manager.Prepare(context.Background(), workspace.PrepareRequest{
		FixtureDir: repo,
		Revision:   revision,
		Dest:       dest,
	})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("expected refusal to reuse workspace, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "leftover.txt")); statErr != nil {
		t.Error("refusal must not delete the existing directory")
	}
}

func TestPrepareBadRevision(t *testing.T) {
	manager, repo, _, attempt := setup(t)
	dest := filepath.Join(attempt, "workspace")
	_, err := manager.Prepare(context.Background(), workspace.PrepareRequest{
		FixtureDir: repo,
		Revision:   strings.Repeat("0", 40),
		Dest:       dest,
	})
	if err == nil {
		t.Fatal("expected checkout error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("failed prepare left a directory behind: %v", statErr)
	}
}

func TestCaptureDiff(t *testing.T) {
	manager, repo, revision, attempt := setup(t)
	ws := filepath.Join(attempt, "workspace")
	if _, err := manager.Prepare(context.Background(), workspace.PrepareRequest{
		FixtureDir: repo,
		Revision:   revision,
		Dest:       ws,
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	testutil.WriteFile(t, filepath.Join(ws, "README.md"), "changed\n", 0o644)
	testutil.WriteFile(t, filepath.Join(ws, "new.txt"), "new file\n", 0o644)
	if err := os.Remove(filepath.Join(ws, "main.go")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(ws, "staged.txt"), "staged\n", 0o644)
	git(t, ws, "add", "staged.txt")

	diff, err := manager.CaptureDiff(context.Background(), ws, attempt)
	if err != nil {
		t.Fatalf("capture diff: %v", err)
	}
	if !diff.HasChanges() {
		t.Fatal("diff reports no changes")
	}
	if diff.Baseline != revision {
		t.Errorf("Baseline = %q, want %q", diff.Baseline, revision)
	}
	for _, want := range []string{"README.md", "new.txt", "main.go", "staged.txt"} {
		if !strings.Contains(diff.Status, want) {
			t.Errorf("status does not mention %s:\n%s", want, diff.Status)
		}
	}
	patch := string(diff.Patch)
	for _, want := range []string{"changed", "new file", "staged", "deleted file mode"} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch does not contain %q:\n%s", want, patch)
		}
	}
	// The candidate's real index must be untouched: staged.txt stays staged,
	// new.txt stays untracked.
	status := git(t, ws, "status", "--porcelain")
	if !strings.Contains(status, "A  staged.txt") {
		t.Errorf("staging state was modified:\n%s", status)
	}
	if !strings.Contains(status, "?? new.txt") {
		t.Errorf("untracked state was modified:\n%s", status)
	}
}

func TestCaptureDiffEmpty(t *testing.T) {
	manager, repo, revision, attempt := setup(t)
	ws := filepath.Join(attempt, "workspace")
	if _, err := manager.Prepare(context.Background(), workspace.PrepareRequest{
		FixtureDir: repo,
		Revision:   revision,
		Dest:       ws,
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	diff, err := manager.CaptureDiff(context.Background(), ws, attempt)
	if err != nil {
		t.Fatalf("capture diff: %v", err)
	}
	if diff.HasChanges() {
		t.Errorf("expected no changes, status:\n%s", diff.Status)
	}
	if len(diff.Patch) != 0 {
		t.Errorf("expected empty patch, got %d bytes", len(diff.Patch))
	}
}

func TestSetup(t *testing.T) {
	_, _, _, attempt := setup(t)
	ws := filepath.Join(attempt, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	outcome := workspace.RunSetup(context.Background(), ws, attempt, [][]string{
		{"sh", "-c", "printf 'setup ran' > setup-output.txt; echo done"},
	}, 5*time.Second)
	if outcome.Err != "" {
		t.Fatalf("setup failed: %s", outcome.Err)
	}
	if len(outcome.Steps) != 1 {
		t.Fatalf("steps = %d", len(outcome.Steps))
	}
	if got := testutil.ReadFile(t, filepath.Join(ws, "setup-output.txt")); got != "setup ran" {
		t.Errorf("setup output = %q", got)
	}
	if _, err := os.Stat(filepath.Join(attempt, "setup-01", "stdout.log")); err != nil {
		t.Errorf("setup stdout artifact missing: %v", err)
	}
}

func TestSetupFailure(t *testing.T) {
	_, _, _, attempt := setup(t)
	ws := filepath.Join(attempt, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	outcome := workspace.RunSetup(context.Background(), ws, attempt, [][]string{
		{"sh", "-c", "echo broken >&2; exit 3"},
		{"sh", "-c", "touch should-not-run.txt"},
	}, 5*time.Second)
	if outcome.Err == "" {
		t.Fatal("expected setup failure")
	}
	if len(outcome.Steps) != 1 {
		t.Errorf("setup continued after failure: %d steps", len(outcome.Steps))
	}
	if outcome.Steps[0].ExitCode == nil || *outcome.Steps[0].ExitCode != 3 {
		t.Errorf("exit code = %v", outcome.Steps[0].ExitCode)
	}
	if _, err := os.Stat(filepath.Join(ws, "should-not-run.txt")); !os.IsNotExist(err) {
		t.Error("later setup steps ran after a failure")
	}
	if _, err := os.Stat(filepath.Join(attempt, "setup-01", "stderr.log")); err != nil {
		t.Errorf("setup stderr artifact missing: %v", err)
	}
}

func TestSetupTimeout(t *testing.T) {
	_, _, _, attempt := setup(t)
	ws := filepath.Join(attempt, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	outcome := workspace.RunSetup(context.Background(), ws, attempt, [][]string{
		{"sh", "-c", "sleep 5"},
	}, 150*time.Millisecond)
	if !outcome.TimedOut {
		t.Fatalf("expected timeout, got err %q", outcome.Err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("setup did not stop promptly: %s", elapsed)
	}
}

func TestSetupEnvironmentIsControlled(t *testing.T) {
	_, _, _, attempt := setup(t)
	ws := filepath.Join(attempt, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(attempt, "env.sh")
	testutil.WriteFile(t, script, "#!/bin/sh\nprintf '%s\\n%s\\n' \"$HOME\" \"$GAUGE_WORKSPACE\" > env.txt\n", 0o755)
	outcome := workspace.RunSetup(context.Background(), ws, attempt, [][]string{{script}}, 5*time.Second)
	if outcome.Err != "" {
		t.Fatalf("setup failed: %s", outcome.Err)
	}
	lines := strings.Split(strings.TrimSpace(testutil.ReadFile(t, filepath.Join(ws, "env.txt"))), "\n")
	if len(lines) != 2 {
		t.Fatalf("env output = %q", lines)
	}
	hostHome, _ := os.UserHomeDir()
	if lines[0] == hostHome {
		t.Errorf("setup inherited the host HOME: %q", lines[0])
	}
	if lines[1] != ws {
		t.Errorf("GAUGE_WORKSPACE = %q, want %q", lines[1], ws)
	}
}

func TestRemoveOnlyDeletesWorkspace(t *testing.T) {
	_, _, _, attempt := setup(t)
	ws := filepath.Join(attempt, "workspace")
	if err := os.MkdirAll(filepath.Join(ws, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(attempt, "events.jsonl"), "{}\n", 0o644)
	if err := workspace.Remove(attempt); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Error("workspace was not removed")
	}
	if _, err := os.Stat(filepath.Join(attempt, "events.jsonl")); err != nil {
		t.Error("logs were removed with the workspace")
	}
	if err := workspace.Remove(attempt); err != nil {
		t.Errorf("remove should be idempotent: %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
