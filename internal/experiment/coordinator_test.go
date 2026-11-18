package experiment_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapters/fake"
	"github.com/m4ns0ur/gauge/internal/experiment"
	"github.com/m4ns0ur/gauge/internal/runner"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/testutil"
	"github.com/m4ns0ur/gauge/internal/workspace"
)

func setup(t *testing.T) (*storage.Store, *runner.Runner, *task.Task, *storage.Experiment) {
	t.Helper()
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{"README.md": "demo\n"})
	dir := bench.AddTask(testutil.TaskOptions{ID: "write-answer", FixtureRepo: repo, Revision: revision})
	loaded, err := task.Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	store, err := storage.New(bench.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	return store, &runner.Runner{
		Store:     store,
		Workspace: &workspace.Manager{WorkRoot: bench.DataDir},
		Retention: runner.RetainKeepFailed,
	}, loaded, nil
}

func config(t *testing.T, mode fake.Mode) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fake.Config{
		Mode:  mode,
		Files: map[string]string{"answer.txt": "42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRunCompletesWithRepetitions(t *testing.T) {
	store, run, taskDef, _ := setup(t)
	var progress []string
	coordinator, err := experiment.New(experiment.Request{
		Store:       store,
		Runner:      run,
		Agent:       fake.New(),
		Config:      config(t, fake.ModeWrite),
		Tasks:       []*task.Task{taskDef},
		Repetitions: 2,
		DataDir:     store.Root(),
		Progress: func(p experiment.Progress) {
			progress = append(progress, p.Phase)
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	exp, attempts, err := coordinator.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exp.Status != storage.StatusCompleted {
		t.Errorf("status = %s", exp.Status)
	}
	if exp.EndedAt == nil {
		t.Error("experiment has no end time")
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	for _, attempt := range attempts {
		if attempt.Outcome != storage.OutcomePassed {
			t.Errorf("attempt %s outcome = %s", attempt.ID, attempt.Outcome)
		}
	}
	if len(progress) != 4 {
		t.Errorf("progress events = %d: %v", len(progress), progress)
	}
	if exp.Agent.Version == "" {
		t.Error("experiment did not absorb the agent version")
	}
	if len(exp.Selection) != 1 {
		t.Errorf("selection should be deduplicated: %+v", exp.Selection)
	}

	reloaded, err := store.GetExperiment(exp.ID)
	if err != nil {
		t.Fatalf("reload experiment: %v", err)
	}
	if reloaded.Status != storage.StatusCompleted || reloaded.EndedAt == nil {
		t.Errorf("persisted experiment = %+v", reloaded)
	}
	persistedAttempts, err := store.ListAttempts(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persistedAttempts) != 2 {
		t.Errorf("persisted attempts = %d", len(persistedAttempts))
	}
}

func TestInterruptedRunIsRecorded(t *testing.T) {
	store, run, taskDef, _ := setup(t)
	coordinator, err := experiment.New(experiment.Request{
		Store:       store,
		Runner:      run,
		Agent:       fake.New(),
		Config:      config(t, fake.ModeWrite),
		Tasks:       []*task.Task{taskDef},
		Repetitions: 1,
		DataDir:     store.Root(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exp, attempts, err := coordinator.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if exp == nil || exp.Status != storage.StatusInterrupted {
		t.Fatalf("experiment = %+v", exp)
	}
	if len(attempts) != 0 {
		t.Errorf("attempts = %d", len(attempts))
	}
	reloaded, err := store.GetExperiment(exp.ID)
	if err != nil {
		t.Fatalf("interrupted experiment was not persisted: %v", err)
	}
	if reloaded.Status != storage.StatusInterrupted {
		t.Errorf("persisted status = %s", reloaded.Status)
	}
}

func TestValidation(t *testing.T) {
	store, run, taskDef, _ := setup(t)
	cases := []struct {
		name string
		req  experiment.Request
	}{
		{"no tasks", experiment.Request{Store: store, Runner: run, Agent: fake.New(), Repetitions: 1}},
		{"zero repetitions", experiment.Request{Store: store, Runner: run, Agent: fake.New(), Tasks: []*task.Task{taskDef}}},
		{"too many repetitions", experiment.Request{Store: store, Runner: run, Agent: fake.New(), Tasks: []*task.Task{taskDef}, Repetitions: 1000}},
		{"missing agent", experiment.Request{Store: store, Runner: run, Tasks: []*task.Task{taskDef}, Repetitions: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := experiment.New(tc.req); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestInvalidAgentConfigFailsBeforeExperimentCreation(t *testing.T) {
	store, run, taskDef, _ := setup(t)
	coordinator, err := experiment.New(experiment.Request{
		Store:       store,
		Runner:      run,
		Agent:       fake.New(),
		Config:      json.RawMessage(`{"mode":"teleport"}`),
		Tasks:       []*task.Task{taskDef},
		Repetitions: 1,
		DataDir:     store.Root(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, runErr := coordinator.Run(context.Background())
	if runErr == nil || !strings.Contains(runErr.Error(), "configuration is invalid") {
		t.Fatalf("expected config error, got %v", runErr)
	}
	runsDir := filepath.Join(store.Root(), "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("experiment was created despite invalid config: %v", entries)
	}
}

func TestNewIDIsFilesystemSafe(t *testing.T) {
	id := experiment.NewID("My Suite / v2", "agent:fake", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	valid := true
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			valid = false
			break
		}
	}
	if !valid || strings.Contains(id, " ") {
		t.Errorf("id = %q", id)
	}
	if !strings.Contains(id, "my-suite---v2") {
		t.Errorf("id = %q", id)
	}
}
