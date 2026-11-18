package metrics_test

import (
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/metrics"
	"github.com/m4ns0ur/gauge/internal/storage"
)

func attempt(taskID, category string, outcome storage.Outcome, durationMS int64, usage *events.Usage) *storage.Attempt {
	return &storage.Attempt{
		ID:           "a",
		ExperimentID: "e",
		TaskID:       taskID,
		TaskCategory: category,
		Outcome:      outcome,
		DurationMS:   durationMS,
		Execution:    storage.ExecutionInfo{Completion: adapter.Completed},
		Usage:        usage,
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestAggregateOutcomes(t *testing.T) {
	attempts := []*storage.Attempt{
		attempt("t1", "cat-a", storage.OutcomePassed, 1000, &events.Usage{InputTokens: int64Ptr(100), OutputTokens: int64Ptr(50)}),
		attempt("t1", "cat-a", storage.OutcomeFailed, 3000, nil),
		attempt("t2", "cat-b", storage.OutcomeError, 500, nil),
		attempt("t2", "cat-b", storage.OutcomeNotEvaluated, 0, nil),
	}
	attempts[1].Execution.Completion = adapter.TimedOut
	attempts[1].Execution.TimedOut = true

	summary := metrics.Aggregate("e", attempts)
	if summary.Attempts != 4 {
		t.Errorf("Attempts = %d", summary.Attempts)
	}
	if summary.Evaluated != 2 || summary.Passed != 1 || summary.Failed != 1 {
		t.Errorf("evaluated=%d passed=%d failed=%d", summary.Evaluated, summary.Passed, summary.Failed)
	}
	if summary.PassRate == nil || *summary.PassRate != 0.5 {
		t.Errorf("PassRate = %v", summary.PassRate)
	}
	if summary.EvaluatorErrors != 1 || summary.NotEvaluated != 1 {
		t.Errorf("errors=%d notEvaluated=%d", summary.EvaluatorErrors, summary.NotEvaluated)
	}
	if summary.Timeouts != 1 {
		t.Errorf("Timeouts = %d", summary.Timeouts)
	}
	if summary.Duration == nil || summary.Duration.Count != 3 {
		t.Fatalf("Duration = %+v", summary.Duration)
	}
	if *summary.Duration.Median != 1000 {
		t.Errorf("median = %v, want 1000", *summary.Duration.Median)
	}
	if *summary.Duration.Min != 500 || *summary.Duration.Max != 3000 {
		t.Errorf("range = %v..%v", *summary.Duration.Min, *summary.Duration.Max)
	}

	if summary.Usage == nil {
		t.Fatal("Usage is nil despite reported tokens")
	}
	if *summary.Usage.InputTokens != 100 || *summary.Usage.OutputTokens != 50 {
		t.Errorf("usage = %+v", summary.Usage)
	}
	if summary.Usage.AttemptsWithUsage != 1 || summary.Usage.Attempts != 4 {
		t.Errorf("usage coverage = %d/%d", summary.Usage.AttemptsWithUsage, summary.Usage.Attempts)
	}
	if summary.Usage.TotalTokens != nil {
		t.Errorf("total tokens should be unavailable, got %v", *summary.Usage.TotalTokens)
	}

	if len(summary.PerTask) != 2 {
		t.Fatalf("per-task groups = %d", len(summary.PerTask))
	}
	if summary.PerTask[0].Key != "t1" || *summary.PerTask[0].PassRate != 0.5 {
		t.Errorf("t1 = %+v", summary.PerTask[0])
	}
	if summary.PerTask[0].Duration == nil || *summary.PerTask[0].Duration.Median != 2000 {
		t.Errorf("t1 duration = %+v", summary.PerTask[0].Duration)
	}
	if summary.PerTask[1].Errors != 1 || summary.PerTask[1].PassRate != nil {
		t.Errorf("t2 = %+v", summary.PerTask[1])
	}
	if len(summary.PerCategory) != 2 || summary.PerCategory[0].Key != "cat-a" {
		t.Errorf("categories = %+v", summary.PerCategory)
	}
}

func TestAggregateWithoutEvaluations(t *testing.T) {
	summary := metrics.Aggregate("e", []*storage.Attempt{
		attempt("t", "c", storage.OutcomeError, 10, nil),
	})
	if summary.PassRate != nil {
		t.Errorf("PassRate = %v, want nil", *summary.PassRate)
	}
	if summary.Usage != nil {
		t.Errorf("Usage = %+v, want nil", summary.Usage)
	}
	if summary.Duration == nil || *summary.Duration.Median != 10 {
		t.Errorf("Duration = %+v", summary.Duration)
	}
}

func TestAggregateEmpty(t *testing.T) {
	summary := metrics.Aggregate("e", nil)
	if summary.Attempts != 0 || summary.PassRate != nil || summary.Duration != nil || summary.Usage != nil {
		t.Errorf("summary = %+v", summary)
	}
}

func TestMedianEvenCount(t *testing.T) {
	attempts := []*storage.Attempt{
		attempt("t", "c", storage.OutcomePassed, 100, nil),
		attempt("t", "c", storage.OutcomePassed, 200, nil),
	}
	summary := metrics.Aggregate("e", attempts)
	if summary.Duration == nil || *summary.Duration.Median != 150 {
		t.Errorf("median = %+v", summary.Duration)
	}
}

func TestFormatDurationMS(t *testing.T) {
	value := 1500.0
	if got := metrics.FormatDurationMS(&value); got != "1.5s" {
		t.Errorf("FormatDurationMS = %q", got)
	}
	if got := metrics.FormatDurationMS(nil); got != "unavailable" {
		t.Errorf("FormatDurationMS(nil) = %q", got)
	}
	if got := metrics.FormatDurationMS(ptr(0)); got != "0s" {
		t.Errorf("FormatDurationMS(0) = %q", got)
	}
	_ = time.Millisecond
}

func ptr(v float64) *float64 { return &v }
