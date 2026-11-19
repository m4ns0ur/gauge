package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/metrics"
	"github.com/m4ns0ur/gauge/internal/report"
	"github.com/m4ns0ur/gauge/internal/storage"
)

func comparableExperiment(id, agent, model, taskFingerprint, fixture string, repetitions int) *storage.Experiment {
	return &storage.Experiment{
		SchemaVersion: 1,
		ID:            id,
		Status:        storage.StatusCompleted,
		CreatedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		StartedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Gauge:         storage.GaugeInfo{Version: "0.1.0", Commit: "abc123"},
		Agent: storage.AgentInfo{
			ID:                agent,
			Version:           agent + "-1.0.0",
			Model:             model,
			ConfigFingerprint: "sha256:config-" + agent,
		},
		Suite: &storage.SuiteInfo{ID: "smoke", Name: "Smoke", Fingerprint: "sha256:suite", Tasks: []string{"task-one"}},
		Selection: []storage.TaskSelection{{
			ID:          "task-one",
			Category:    "repository-engineering",
			Fingerprint: taskFingerprint,
			Fixture:     fixture,
		}},
		Repetitions: repetitions,
		Environment: storage.EnvironmentInfo{OS: "test", Arch: "test", GoVersion: "go1.x", Backend: "local"},
	}
}

func comparableAttempt(expID string, outcome storage.Outcome, durationMS int64) *storage.Attempt {
	start := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
	return &storage.Attempt{
		SchemaVersion: storage.SchemaVersion,
		ID:            expID + "-" + string(outcome) + "-" + time.Duration(durationMS*int64(time.Millisecond)).String(),
		ExperimentID:  expID,
		TaskID:        "task-one",
		TaskCategory:  "repository-engineering",
		StartedAt:     start,
		EndedAt:       start.Add(time.Duration(durationMS) * time.Millisecond),
		DurationMS:    durationMS,
		Execution:     storage.ExecutionInfo{Completion: adapter.Completed},
		Outcome:       outcome,
	}
}

func TestBuildComparison(t *testing.T) {
	a := comparableExperiment("exp-a", "iris", "model-a", "sha256:task", strings.Repeat("a", 40), 1)
	b := comparableExperiment("exp-b", "pi", "model-b", "sha256:task", strings.Repeat("a", 40), 3)
	aAttempts := []*storage.Attempt{
		comparableAttempt("exp-a", storage.OutcomePassed, 1000),
		comparableAttempt("exp-a", storage.OutcomeFailed, 2000),
	}
	bAttempts := []*storage.Attempt{
		comparableAttempt("exp-b", storage.OutcomePassed, 1500),
		comparableAttempt("exp-b", storage.OutcomePassed, 1500),
	}
	aBehavior := &metrics.Behavior{ToolCalls: 2, ToolResults: 2, ToolFailures: 1, AttemptsWithEvents: 2, Attempts: 2}
	bBehavior := &metrics.Behavior{ToolCalls: 5, ToolResults: 5, AttemptsWithEvents: 2, Attempts: 2}

	comparison := report.BuildComparison(a, b, aAttempts, bAttempts, aBehavior, bBehavior)
	if !comparison.Compatible {
		t.Fatalf("compatible = false: %v", comparison.Incompatibilities)
	}
	if len(comparison.Tasks) != 1 {
		t.Fatalf("tasks = %+v", comparison.Tasks)
	}
	task := comparison.Tasks[0]
	if task.Key != "task-one" || task.Change != "improved" {
		t.Errorf("task comparison = %+v", task)
	}
	if task.PassRateDeltaPP == nil || *task.PassRateDeltaPP != 50 {
		t.Errorf("task delta = %v", task.PassRateDeltaPP)
	}
	if comparison.Summary.PassRateDeltaPP == nil || *comparison.Summary.PassRateDeltaPP != 50 {
		t.Errorf("summary delta = %v", comparison.Summary.PassRateDeltaPP)
	}
	if comparison.Summary.ToolCallsDelta == nil || *comparison.Summary.ToolCallsDelta != 3 {
		t.Errorf("tool delta = %v", comparison.Summary.ToolCallsDelta)
	}
	if comparison.A.Summary.Behavior == nil || comparison.A.Summary.Behavior.ToolCalls != 2 {
		t.Errorf("A behavior = %+v", comparison.A.Summary.Behavior)
	}

	fields := map[string]bool{}
	for _, difference := range comparison.Differences {
		fields[difference.Field] = true
	}
	for _, want := range []string{"agent", "model", "adapter config", "repetitions"} {
		if !fields[want] {
			t.Errorf("missing difference %q: %+v", want, comparison.Differences)
		}
	}
}

func TestBuildComparisonRejectsDifferentTaskContent(t *testing.T) {
	a := comparableExperiment("exp-a", "iris", "model-a", "sha256:task-a", strings.Repeat("a", 40), 1)
	b := comparableExperiment("exp-b", "iris", "model-a", "sha256:task-b", strings.Repeat("b", 40), 1)
	comparison := report.BuildComparison(a, b, nil, nil, nil, nil)
	if comparison.Compatible {
		t.Fatal("different task content was accepted as comparable")
	}
	reasons := strings.Join(comparison.Incompatibilities, "\n")
	if !strings.Contains(reasons, "fingerprint") || !strings.Contains(reasons, "fixture revision") {
		t.Errorf("reasons = %q", reasons)
	}

	c := comparableExperiment("exp-c", "iris", "model-a", "sha256:task-a", strings.Repeat("a", 40), 1)
	c.Selection = []storage.TaskSelection{{ID: "task-two", Fingerprint: "sha256:task-a", Fixture: strings.Repeat("a", 40)}}
	comparison = report.BuildComparison(a, c, nil, nil, nil, nil)
	if comparison.Compatible {
		t.Fatal("different task ids were accepted as comparable")
	}
	reasons = strings.Join(comparison.Incompatibilities, "\n")
	if !strings.Contains(reasons, `only in A`) || !strings.Contains(reasons, `only in B`) {
		t.Errorf("reasons = %q", reasons)
	}
}

func TestComparisonRenderings(t *testing.T) {
	a := comparableExperiment("exp-a", "iris", "model-a", "sha256:task", strings.Repeat("a", 40), 1)
	b := comparableExperiment("exp-b", "pi", "model-b", "sha256:task", strings.Repeat("a", 40), 1)
	aAttempts := []*storage.Attempt{comparableAttempt("exp-a", storage.OutcomePassed, 1000)}
	bAttempts := []*storage.Attempt{comparableAttempt("exp-b", storage.OutcomeFailed, 2000)}
	comparison := report.BuildComparison(a, b, aAttempts, bAttempts, nil, nil)

	var terminal bytes.Buffer
	if err := report.WriteComparisonTerminal(&terminal, comparison); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	for _, want := range []string{"Comparison:", "Configuration differences:", "Per-task outcomes:", "regressed", "Pass rate"} {
		if !strings.Contains(terminal.String(), want) {
			t.Errorf("terminal output missing %q:\n%s", want, terminal.String())
		}
	}

	var markdown bytes.Buffer
	if err := report.WriteComparisonMarkdown(&markdown, comparison); err != nil {
		t.Fatalf("markdown: %v", err)
	}
	for _, want := range []string{"# Experiment comparison", "## Per-task outcomes", "| Pass rate |", "regressed"} {
		if !strings.Contains(markdown.String(), want) {
			t.Errorf("markdown output missing %q:\n%s", want, markdown.String())
		}
	}

	var encoded bytes.Buffer
	if err := report.WriteComparisonJSON(&encoded, comparison); err != nil {
		t.Fatalf("json: %v", err)
	}
	var decoded report.Comparison
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.SchemaVersion != report.CompareSchemaVersion || decoded.Compatible != true {
		t.Errorf("decoded comparison = %+v", decoded)
	}
}

func TestComparisonTerminalShowsIncompatibility(t *testing.T) {
	a := comparableExperiment("exp-a", "iris", "model-a", "sha256:task-a", strings.Repeat("a", 40), 1)
	b := comparableExperiment("exp-b", "iris", "model-a", "sha256:task-b", strings.Repeat("a", 40), 1)
	comparison := report.BuildComparison(a, b, nil, nil, nil, nil)
	var buffer bytes.Buffer
	if err := report.WriteComparisonTerminal(&buffer, comparison); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"INCOMPATIBLE", "fingerprint"} {
		if !strings.Contains(buffer.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buffer.String())
		}
	}
}
