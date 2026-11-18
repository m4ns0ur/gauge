package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/adapters/fake"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/runner"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/testutil"
	"github.com/m4ns0ur/gauge/internal/workspace"
)

type harness struct {
	bench  *testutil.Bench
	store  *storage.Store
	task   *task.Task
	exp    *storage.Experiment
	runner *runner.Runner
}

func newHarness(t *testing.T, opts testutil.TaskOptions, retention runner.Retention) *harness {
	t.Helper()
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{
		"README.md": "demo\n",
	})
	opts.FixtureRepo = repo
	opts.Revision = revision
	dir := bench.AddTask(opts)
	loaded, err := task.Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	store, err := storage.New(bench.DataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	expID := "exp-" + sanitize(t.Name())
	now := time.Now().UTC()
	exp := &storage.Experiment{
		SchemaVersion: storage.SchemaVersion,
		ID:            expID,
		Status:        storage.StatusRunning,
		CreatedAt:     now,
		StartedAt:     now,
		Gauge:         storage.GaugeInfo{Version: "test", Commit: "test"},
		Agent: storage.AgentInfo{
			ID:                fake.ID,
			ConfigFingerprint: storage.FingerprintJSON(nil),
		},
		Selection: []storage.TaskSelection{{
			ID:          loaded.ID,
			Category:    loaded.Category,
			Fingerprint: "sha256:test",
			Fixture:     loaded.Fixture.Revision,
		}},
		Repetitions: 1,
		Environment: storage.EnvironmentInfo{OS: "test", Arch: "test", Backend: "local"},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("create experiment: %v", err)
	}
	return &harness{
		bench: bench,
		store: store,
		task:  loaded,
		exp:   exp,
		runner: &runner.Runner{
			Store:     store,
			Workspace: &workspace.Manager{WorkRoot: bench.DataDir},
			Retention: retention,
		},
	}
}

func sanitize(name string) string {
	re := regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	return strings.ToLower(re.ReplaceAllString(name, "-"))
}

func (h *harness) run(t *testing.T, cfg fake.Config) *storage.Attempt {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := h.runner.Run(context.Background(), runner.Request{
		Experiment: h.exp,
		Task:       h.task,
		Adapter:    fake.New(),
		Config:     raw,
	})
	if err != nil {
		t.Fatalf("run attempt: %v", err)
	}
	return attempt
}

func attemptDir(t *testing.T, store *storage.Store, attempt *storage.Attempt) string {
	t.Helper()
	dir, err := store.AttemptDir(attempt.ExperimentID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunPassingAttempt(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{ID: "write-answer"}, runner.RetainKeepFailed)
	attempt := h.run(t, fake.Config{
		Mode:  fake.ModeWrite,
		Files: map[string]string{"answer.txt": "42\n"},
		Events: []fake.Event{
			{Type: events.TypeAgentMessage, Text: "I wrote the answer."},
		},
		Usage: &events.Usage{InputTokens: int64ptr(12), OutputTokens: int64ptr(7)},
	})

	if attempt.Outcome != storage.OutcomePassed {
		t.Fatalf("outcome = %s, evaluation = %+v", attempt.Outcome, attempt.Evaluation)
	}
	if attempt.Execution.Completion != adapter.Completed {
		t.Errorf("completion = %s", attempt.Execution.Completion)
	}
	if !attempt.Diff.Changed {
		t.Error("diff should report the new answer.txt")
	}
	if attempt.WorkspaceKept {
		t.Error("workspace should be removed after a passing evaluation")
	}
	if attempt.Usage == nil || *attempt.Usage.InputTokens != 12 {
		t.Errorf("usage = %+v", attempt.Usage)
	}
	if attempt.Evaluation == nil || len(attempt.Evaluation.Checks) != 1 {
		t.Fatalf("evaluation = %+v", attempt.Evaluation)
	}

	dir := attemptDir(t, h.store, attempt)
	if _, err := os.Stat(filepath.Join(dir, "workspace")); !os.IsNotExist(err) {
		t.Error("workspace directory was not removed")
	}
	for _, name := range []string{"events.jsonl", "evaluation.json", "diff.patch", "git-status.txt", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("artifact %s missing: %v", name, err)
		}
	}
	for _, name := range []string{"evaluation-scratch", "agent-home", "setup-home"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("scratch directory %s was not cleaned up", name)
		}
	}
	if _, ok := attempt.Artifacts[storage.ArtifactEvents]; !ok {
		t.Error("events artifact not recorded in metadata")
	}
	eventsData := testutil.ReadFile(t, filepath.Join(dir, "events.jsonl"))
	lines := strings.Split(strings.TrimSpace(eventsData), "\n")
	if len(lines) < 3 {
		t.Fatalf("events log has %d lines:\n%s", len(lines), eventsData)
	}
	first := lines[0]
	var event map[string]any
	if err := json.Unmarshal([]byte(first), &event); err != nil {
		t.Fatalf("invalid event line %q: %v", first, err)
	}
	if event["schema_version"].(float64) != 1 {
		t.Errorf("event schema = %v", event["schema_version"])
	}

	persisted, err := h.store.GetAttempt(h.exp.ID, attempt.ID)
	if err != nil {
		t.Fatalf("reload attempt: %v", err)
	}
	if persisted.Outcome != storage.OutcomePassed {
		t.Errorf("persisted outcome = %s", persisted.Outcome)
	}
}

func TestRunFailingAttemptKeepsWorkspace(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{ID: "noop"}, runner.RetainKeepFailed)
	attempt := h.run(t, fake.Config{
		Events: []fake.Event{{Type: events.TypeAgentMessage, Text: "The task is definitely complete and passed."}},
	})
	if attempt.Outcome != storage.OutcomeFailed {
		t.Fatalf("outcome = %s", attempt.Outcome)
	}
	if !attempt.WorkspaceKept {
		t.Error("failed attempt should preserve its workspace")
	}
	dir := attemptDir(t, h.store, attempt)
	if _, err := os.Stat(filepath.Join(dir, "workspace")); err != nil {
		t.Errorf("workspace missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "evaluation-scratch")); !os.IsNotExist(err) {
		t.Error("evaluator scratch cache was retained for a failed attempt")
	}
	if attempt.Diff.Changed {
		t.Errorf("noop agent should not change the workspace: %+v", attempt.Diff)
	}
}

func TestRunAgentTimeout(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{
		ID:               "slow",
		ExecutionTimeout: 400 * time.Millisecond,
	}, runner.RetainKeepFailed)
	start := time.Now()
	attempt := h.run(t, fake.Config{Mode: fake.ModeSleep, Sleep: "10s"})
	elapsed := time.Since(start)
	if !attempt.Execution.TimedOut {
		t.Errorf("TimedOut = false (completion %s)", attempt.Execution.Completion)
	}
	if attempt.Execution.Completion != adapter.TimedOut {
		t.Errorf("completion = %s, want timed_out", attempt.Execution.Completion)
	}
	if attempt.Outcome != storage.OutcomeFailed {
		t.Errorf("outcome after timeout = %s", attempt.Outcome)
	}
	if elapsed > 5*time.Second {
		t.Errorf("timeout took %s, prompt termination failed", elapsed)
	}
	if !attempt.WorkspaceKept {
		t.Error("timed-out attempt should preserve its workspace")
	}
}

func TestEvaluatorErrorIsNotATaskFailure(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{
		ID:             "broken-evaluator",
		EvaluateScript: "#!/bin/sh\nprintf 'this is not json'\n",
	}, runner.RetainKeepFailed)
	attempt := h.run(t, fake.Config{
		Mode:  fake.ModeWrite,
		Files: map[string]string{"answer.txt": "42"},
	})
	if attempt.Outcome != storage.OutcomeError {
		t.Fatalf("outcome = %s, want error", attempt.Outcome)
	}
	if attempt.Evaluation == nil || attempt.Evaluation.Error == "" {
		t.Errorf("evaluation = %+v", attempt.Evaluation)
	}
	if !attempt.Evaluation.TimedOut {
		// Not a timeout; the evaluator produced invalid JSON.
	}
	if _, err := os.Stat(filepath.Join(attemptDir(t, h.store, attempt), "evaluator-stdout.log")); err != nil {
		t.Errorf("evaluator output log missing: %v", err)
	}
}

func TestEvaluatorFailureExitCodeIsAnError(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{
		ID:             "evaluator-crash",
		EvaluateScript: "#!/bin/sh\necho boom >&2\nexit 9\n",
	}, runner.RetainKeepFailed)
	attempt := h.run(t, fake.Config{Mode: fake.ModeWrite, Files: map[string]string{"answer.txt": "42"}})
	if attempt.Outcome != storage.OutcomeError {
		t.Fatalf("outcome = %s, want error", attempt.Outcome)
	}
	if attempt.Evaluation == nil || attempt.Evaluation.ExitCode == nil || *attempt.Evaluation.ExitCode != 9 {
		t.Errorf("evaluation exit code = %+v", attempt.Evaluation)
	}
}

func TestSetupFailureSkipsAgentAndEvaluation(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{
		ID:    "setup-fails",
		Setup: [][]string{{"sh", "-c", "echo cannot prepare >&2; exit 4"}},
	}, runner.RetainKeepFailed)
	attempt := h.run(t, fake.Config{
		Mode:  fake.ModeWrite,
		Files: map[string]string{"answer.txt": "42"},
	})
	if attempt.Outcome != storage.OutcomeNotEvaluated {
		t.Fatalf("outcome = %s, want not_evaluated", attempt.Outcome)
	}
	if attempt.Setup == nil || attempt.Setup.Error == "" {
		t.Errorf("setup = %+v", attempt.Setup)
	}
	if attempt.Evaluation != nil {
		t.Error("evaluator must not run after a setup failure")
	}
	if attempt.Execution.Completion != adapter.Failed {
		t.Errorf("completion = %s", attempt.Execution.Completion)
	}
	dir := attemptDir(t, h.store, attempt)
	if _, err := os.Stat(filepath.Join(dir, "evaluation.json")); !os.IsNotExist(err) {
		t.Error("evaluation.json exists after setup failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "workspace", "answer.txt")); !os.IsNotExist(err) {
		t.Error("agent ran despite setup failure")
	}
	if _, ok := attempt.Artifacts["setup-01/stderr.log"]; !ok {
		t.Errorf("setup log artifact not recorded: %v", attempt.Artifacts)
	}
}

func TestDiffCapturedBeforeEvaluation(t *testing.T) {
	script := `#!/bin/sh
set -u
printf 'tampered\n' > "$1/tampered.txt"
printf '%s' '{"schema_version":1,"status":"passed","summary":"ok","checks":[{"id":"ok","status":"passed"}]}'
`
	h := newHarness(t, testutil.TaskOptions{
		ID:             "evaluator-mutates",
		EvaluateScript: script,
	}, runner.RetainKeepAll)
	attempt := h.run(t, fake.Config{Mode: fake.ModeWrite, Files: map[string]string{"answer.txt": "42"}})
	if attempt.Outcome != storage.OutcomePassed {
		t.Fatalf("outcome = %s", attempt.Outcome)
	}
	for _, file := range attempt.Diff.Files {
		if file.Path == "tampered.txt" {
			t.Error("diff was captured after the evaluator ran")
		}
	}
	// The evaluator is allowed to write inside its scratch copy/workspace; the
	// recorded diff must remain the candidate change set.
	diffPatch := testutil.ReadFile(t, filepath.Join(attemptDir(t, h.store, attempt), "diff.patch"))
	if strings.Contains(diffPatch, "tampered") {
		t.Errorf("recorded diff includes evaluator mutation:\n%s", diffPatch)
	}
}

func TestFreshWorkspacePerAttempt(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{ID: "isolation"}, runner.RetainKeepAll)
	first := h.run(t, fake.Config{Mode: fake.ModeWrite, Files: map[string]string{"secret.txt": "first attempt"}})
	second := h.run(t, fake.Config{Mode: fake.ModeNoop})

	if first.Diff.Baseline != second.Diff.Baseline {
		t.Errorf("baselines differ: %s vs %s", first.Diff.Baseline, second.Diff.Baseline)
	}
	firstDir := attemptDir(t, h.store, first)
	secondDir := attemptDir(t, h.store, second)
	if firstDir == secondDir {
		t.Fatal("attempts share a directory")
	}
	if _, err := os.Stat(filepath.Join(firstDir, "workspace", "secret.txt")); err != nil {
		t.Errorf("first workspace missing its file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(secondDir, "workspace", "secret.txt")); !os.IsNotExist(err) {
		t.Error("second attempt inherited a file from the first workspace")
	}
}

func TestKeepAllRetainsScratch(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{ID: "keep-all"}, runner.RetainKeepAll)
	attempt := h.run(t, fake.Config{Mode: fake.ModeWrite, Files: map[string]string{"answer.txt": "42"}})
	if attempt.Outcome != storage.OutcomePassed {
		t.Fatalf("outcome = %s", attempt.Outcome)
	}
	dir := attemptDir(t, h.store, attempt)
	for _, name := range []string{"workspace", "evaluation-scratch", "agent-home"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("keep_all did not retain %s: %v", name, err)
		}
	}
}

func TestCancelledAttemptIsRecorded(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{ID: "cancelled"}, runner.RetainKeepAll)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(600 * time.Millisecond)
		cancel()
	}()
	raw, err := json.Marshal(fake.Config{Mode: fake.ModeSleep, Sleep: "30s"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := h.runner.Run(ctx, runner.Request{
		Experiment: h.exp,
		Task:       h.task,
		Adapter:    fake.New(),
		Config:     raw,
	})
	if err != nil {
		t.Fatalf("cancelled run should still be recorded: %v", err)
	}
	if !attempt.Execution.Cancelled {
		t.Fatalf("execution = %+v", attempt.Execution)
	}
	if attempt.Execution.Completion != adapter.Cancelled {
		t.Errorf("completion = %s", attempt.Execution.Completion)
	}
	if _, err := h.store.GetAttempt(h.exp.ID, attempt.ID); err != nil {
		t.Errorf("cancelled attempt was not persisted: %v", err)
	}
}

func TestAgentVersionAndModelRecorded(t *testing.T) {
	h := newHarness(t, testutil.TaskOptions{ID: "metadata"}, runner.RetainKeepFailed)
	attempt := h.run(t, fake.Config{
		Mode:         fake.ModeNoop,
		AgentVersion: "fake-9.9.9",
		Model:        "fake/model",
		Provider:     "fake-provider",
	})
	if attempt.AgentVersion != "fake-9.9.9" {
		t.Errorf("AgentVersion = %q", attempt.AgentVersion)
	}
	if attempt.Model != "fake/model" {
		t.Errorf("Model = %q", attempt.Model)
	}
	if attempt.Provider != "fake-provider" {
		t.Errorf("Provider = %q", attempt.Provider)
	}
}

func int64ptr(v int64) *int64 { return &v }
