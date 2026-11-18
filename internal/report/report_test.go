package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/evaluator"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/report"
	"github.com/m4ns0ur/gauge/internal/storage"
)

func fixture(t *testing.T) (*storage.Experiment, []*storage.Attempt) {
	t.Helper()
	exp := &storage.Experiment{
		SchemaVersion: 1,
		ID:            "exp-one",
		Status:        storage.StatusCompleted,
		CreatedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		StartedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Gauge:         storage.GaugeInfo{Version: "0.1.0", Commit: "abc123"},
		Agent: storage.AgentInfo{
			ID:                "fake",
			Version:           "fake-1.0.0",
			Model:             "fake/deterministic",
			ConfigFingerprint: "sha256:config",
			ConfigRedacted:    true,
		},
		Suite:       &storage.SuiteInfo{ID: "smoke", Name: "Smoke", Fingerprint: "sha256:suite", Tasks: []string{"task-one"}},
		Selection:   []storage.TaskSelection{{ID: "task-one", Category: "repository-engineering", Fingerprint: "sha256:task", Fixture: "abc"}},
		Repetitions: 1,
		Environment: storage.EnvironmentInfo{OS: "test", Arch: "test", GoVersion: "go1.x", Backend: "local", DataDir: "/tmp/data", GitVersion: "git version 2"},
	}
	start := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
	attempts := []*storage.Attempt{
		{
			SchemaVersion: 1,
			ID:            "attempt-one",
			ExperimentID:  "exp-one",
			TaskID:        "task-one",
			TaskName:      "Task One",
			TaskCategory:  "repository-engineering",
			Repetition:    0,
			AgentID:       "fake",
			AgentVersion:  "fake-1.0.0",
			Model:         "fake/deterministic",
			StartedAt:     start,
			EndedAt:       start.Add(2 * time.Second),
			DurationMS:    2000,
			Outcome:       storage.OutcomePassed,
			Execution:     storage.ExecutionInfo{Completion: adapter.Completed},
			Diff:          storage.DiffInfo{Changed: true},
			Evaluation: &storage.EvaluationInfo{
				Status:  evaluator.Passed,
				Summary: "All checks passed.",
				Checks: []evaluator.Check{
					{ID: "answer", Status: evaluator.Passed, Message: "file exists"},
				},
				DurationMS: 100,
			},
		},
		{
			SchemaVersion: 1,
			ID:            "attempt-two",
			ExperimentID:  "exp-one",
			TaskID:        "task-one",
			TaskName:      "Task One",
			TaskCategory:  "repository-engineering",
			Repetition:    1,
			AgentID:       "fake",
			StartedAt:     start.Add(3 * time.Second),
			EndedAt:       start.Add(5 * time.Second),
			DurationMS:    2000,
			Outcome:       storage.OutcomeFailed,
			Execution:     storage.ExecutionInfo{Completion: adapter.Completed},
			Evaluation: &storage.EvaluationInfo{
				Status:  evaluator.Failed,
				Summary: "Missing answer.",
				Checks: []evaluator.Check{
					{ID: "answer", Status: evaluator.Failed, Message: "file missing"},
				},
			},
			Usage: &events.Usage{InputTokens: ptrInt(10)},
		},
	}
	return exp, attempts
}

func ptrInt(v int64) *int64 { return &v }

func TestBuildSummary(t *testing.T) {
	exp, attempts := fixture(t)
	doc := report.Build(exp, attempts)
	if doc.Summary.Attempts != 2 || doc.Summary.Passed != 1 || doc.Summary.Failed != 1 {
		t.Errorf("summary = %+v", doc.Summary)
	}
	if doc.Summary.PassRate == nil || *doc.Summary.PassRate != 0.5 {
		t.Errorf("pass rate = %v", doc.Summary.PassRate)
	}
	if len(doc.Attempts) != 2 || doc.Attempts[0].ID != "attempt-one" {
		t.Errorf("attempt order = %+v", doc.Attempts)
	}
}

func TestMarkdownReport(t *testing.T) {
	exp, attempts := fixture(t)
	doc := report.Build(exp, attempts)
	var buf bytes.Buffer
	if err := report.WriteMarkdown(&buf, doc); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"# Experiment exp-one",
		"1/2 (50.0%)",
		"| task-one |",
		"### attempt-one",
		"sha256:suite",
		"sha256:config",
		"secret values redacted",
		"unavailable",
		"never as zero",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("markdown does not contain %q:\n%s", want, text)
		}
	}
}

func TestTerminalReport(t *testing.T) {
	exp, attempts := fixture(t)
	var buf bytes.Buffer
	if err := report.WriteTerminal(&buf, report.Build(exp, attempts)); err != nil {
		t.Fatalf("write terminal: %v", err)
	}
	text := buf.String()
	for _, want := range []string{"Experiment: exp-one", "Passed: 1/2 (50.0%)", "task-one", "Tokens:"} {
		if !strings.Contains(text, want) {
			t.Errorf("terminal output does not contain %q:\n%s", want, text)
		}
	}
}

func TestJSONReport(t *testing.T) {
	exp, attempts := fixture(t)
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, report.Build(exp, attempts)); err != nil {
		t.Fatalf("write json: %v", err)
	}
	var doc struct {
		SchemaVersion int `json:"schema_version"`
		Summary       struct {
			Attempts int     `json:"attempts"`
			PassRate float64 `json:"pass_rate"`
		} `json:"summary"`
		Attempts []map[string]any `json:"attempts"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if doc.SchemaVersion != 1 || doc.Summary.Attempts != 2 || doc.Summary.PassRate != 0.5 {
		t.Errorf("document = %+v", doc)
	}
	if len(doc.Attempts) != 2 {
		t.Errorf("attempts = %d", len(doc.Attempts))
	}
}

func TestEvaluationErrorsNotCountedAsFailures(t *testing.T) {
	exp, attempts := fixture(t)
	attempts[1].Outcome = storage.OutcomeError
	attempts[1].Evaluation = &storage.EvaluationInfo{Status: evaluator.Error, Summary: "broken", Error: "invalid JSON"}
	doc := report.Build(exp, attempts)
	if doc.Summary.EvaluatorErrors != 1 {
		t.Errorf("evaluator errors = %d", doc.Summary.EvaluatorErrors)
	}
	if doc.Summary.Passed != 1 || doc.Summary.Failed != 0 {
		t.Errorf("passed=%d failed=%d", doc.Summary.Passed, doc.Summary.Failed)
	}
	if doc.Summary.PassRate == nil || *doc.Summary.PassRate != 1 {
		t.Errorf("pass rate should be 1/1, got %v", doc.Summary.PassRate)
	}
}
