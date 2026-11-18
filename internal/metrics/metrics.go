// Package metrics aggregates attempt outcomes without collapsing execution
// failures into task failures or inventing unavailable measurements.
package metrics

import (
	"sort"
	"time"

	"github.com/m4ns0ur/gauge/internal/storage"
)

// Summary is the aggregate view of one experiment.
type Summary struct {
	ExperimentID string `json:"experiment_id"`
	Attempts     int    `json:"attempts"`
	// Evaluated counts attempts with a valid evaluator verdict (passed or
	// failed). It is the denominator for PassRate.
	Evaluated int `json:"evaluated"`
	Passed    int `json:"passed"`
	Failed    int `json:"failed"`
	// EvaluatorErrors counts attempts whose evaluation infrastructure failed.
	EvaluatorErrors int `json:"evaluator_errors"`
	// NotEvaluated counts attempts that produced no evaluator verdict because
	// setup or workspace preparation failed.
	NotEvaluated int `json:"not_evaluated"`
	// Timeouts counts attempts whose agent or setup timed out, independently
	// of the evaluator's task result.
	Timeouts int `json:"timeouts"`
	// Cancelled counts attempts cancelled by the user or runner.
	Cancelled int `json:"cancelled"`
	// ExecutionFailures counts attempts whose agent process failed.
	ExecutionFailures int `json:"execution_failures"`
	// SetupFailures counts attempts whose setup commands failed.
	SetupFailures int `json:"setup_failures"`

	PassRate    *float64 `json:"pass_rate,omitempty"`
	Duration    *Stats   `json:"duration,omitempty"`
	Usage       *Usage   `json:"usage,omitempty"`
	PerTask     []Group  `json:"per_task"`
	PerCategory []Group  `json:"per_category"`
}

// Group is an outcome breakdown for a task or category.
type Group struct {
	Key          string   `json:"key"`
	Category     string   `json:"category,omitempty"`
	Attempts     int      `json:"attempts"`
	Evaluated    int      `json:"evaluated"`
	Passed       int      `json:"passed"`
	Failed       int      `json:"failed"`
	Errors       int      `json:"errors"`
	NotEvaluated int      `json:"not_evaluated"`
	PassRate     *float64 `json:"pass_rate,omitempty"`
	Duration     *Stats   `json:"duration,omitempty"`
}

// Stats describes a numeric distribution across attempts that reported it.
type Stats struct {
	Count  int      `json:"count"`
	Median *float64 `json:"median,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// Usage aggregates only values adapters actually reported, with coverage so a
// partial sum is not mistaken for a complete one.
type Usage struct {
	InputTokens       *int64   `json:"input_tokens,omitempty"`
	OutputTokens      *int64   `json:"output_tokens,omitempty"`
	TotalTokens       *int64   `json:"total_tokens,omitempty"`
	CostUSD           *float64 `json:"cost_usd,omitempty"`
	AttemptsWithUsage int      `json:"attempts_with_usage"`
	Attempts          int      `json:"attempts"`
}

// Aggregate computes the experiment summary.
func Aggregate(experimentID string, attempts []*storage.Attempt) Summary {
	s := Summary{ExperimentID: experimentID, Attempts: len(attempts)}
	durations := make([]float64, 0, len(attempts))
	var usage Usage
	usage.Attempts = len(attempts)
	tasks := map[string]*Group{}
	categories := map[string]*Group{}
	taskDurations := map[string][]float64{}
	categoryDurations := map[string][]float64{}

	for _, a := range attempts {
		group := tasks[a.TaskID]
		if group == nil {
			group = &Group{Key: a.TaskID, Category: a.TaskCategory}
			tasks[a.TaskID] = group
		}
		cat := categories[a.TaskCategory]
		if cat == nil {
			cat = &Group{Key: a.TaskCategory}
			categories[a.TaskCategory] = cat
		}
		for _, g := range []*Group{group, cat} {
			g.Attempts++
		}

		if a.DurationMS > 0 {
			durations = append(durations, float64(a.DurationMS))
			taskDurations[a.TaskID] = append(taskDurations[a.TaskID], float64(a.DurationMS))
			categoryDurations[a.TaskCategory] = append(categoryDurations[a.TaskCategory], float64(a.DurationMS))
		}
		switch a.Outcome {
		case storage.OutcomePassed:
			s.Passed++
			s.Evaluated++
			group.Passed++
			group.Evaluated++
			cat.Passed++
			cat.Evaluated++
		case storage.OutcomeFailed:
			s.Failed++
			s.Evaluated++
			group.Failed++
			group.Evaluated++
			cat.Failed++
			cat.Evaluated++
		case storage.OutcomeError:
			s.EvaluatorErrors++
			group.Errors++
			cat.Errors++
		default:
			s.NotEvaluated++
			group.NotEvaluated++
			cat.NotEvaluated++
		}
		if a.Execution.TimedOut {
			s.Timeouts++
		}
		if a.Execution.Cancelled {
			s.Cancelled++
		}
		if a.Setup != nil && a.Setup.Error != "" {
			// Setup problems are reported separately from agent failures so a
			// broken fixture does not look like a weak agent.
			s.SetupFailures++
		} else if a.Execution.Completion == "failed" || a.Execution.AdapterError != "" {
			s.ExecutionFailures++
		}
		if a.Usage != nil {
			reported := a.Usage.InputTokens != nil || a.Usage.OutputTokens != nil ||
				a.Usage.TotalTokens != nil || a.Usage.CostUSD != nil
			if reported {
				usage.AttemptsWithUsage++
			}
			usage.InputTokens = addInt64(usage.InputTokens, a.Usage.InputTokens)
			usage.OutputTokens = addInt64(usage.OutputTokens, a.Usage.OutputTokens)
			usage.TotalTokens = addInt64(usage.TotalTokens, a.Usage.TotalTokens)
			usage.CostUSD = addFloat64(usage.CostUSD, a.Usage.CostUSD)
		}
	}

	if s.Evaluated > 0 {
		rate := float64(s.Passed) / float64(s.Evaluated)
		s.PassRate = &rate
	}
	s.Duration = summarize(durations)
	if usage.AttemptsWithUsage > 0 {
		s.Usage = &usage
	}
	for key, group := range tasks {
		group.Duration = summarize(taskDurations[key])
	}
	for key, group := range categories {
		group.Duration = summarize(categoryDurations[key])
	}
	s.PerTask = groupList(tasks, func(g *Group) string { return g.Key })
	s.PerCategory = groupList(categories, func(g *Group) string { return g.Key })
	return s
}

func groupList(groups map[string]*Group, key func(*Group) string) []Group {
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		if g.Evaluated > 0 {
			rate := float64(g.Passed) / float64(g.Evaluated)
			g.PassRate = &rate
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return key(&out[i]) < key(&out[j]) })
	return out
}

func summarize(values []float64) *Stats {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	median := medianOf(sorted)
	min, max := sorted[0], sorted[len(sorted)-1]
	return &Stats{Count: len(sorted), Median: &median, Min: &min, Max: &max}
}

func medianOf(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func addInt64(existing, value *int64) *int64 {
	if value == nil {
		return existing
	}
	if existing == nil {
		v := *value
		return &v
	}
	v := *existing + *value
	return &v
}

func addFloat64(existing, value *float64) *float64 {
	if value == nil {
		return existing
	}
	if existing == nil {
		v := *value
		return &v
	}
	v := *existing + *value
	return &v
}

// FormatDurationMS renders a millisecond value compactly, or "unavailable".
func FormatDurationMS(ms *float64) string {
	if ms == nil {
		return "unavailable"
	}
	return time.Duration(*ms * float64(time.Millisecond)).Round(time.Millisecond).String()
}
