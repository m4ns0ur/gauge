package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/m4ns0ur/gauge/internal/metrics"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/version"
)

// CompareSchemaVersion is the comparison document schema version.
const CompareSchemaVersion = 1

// Comparison is the exportable comparison of two experiments.
type Comparison struct {
	SchemaVersion     int               `json:"schema_version"`
	GeneratedAt       time.Time         `json:"generated_at"`
	Gauge             version.Info      `json:"gauge"`
	A                 ComparisonSide    `json:"a"`
	B                 ComparisonSide    `json:"b"`
	Compatible        bool              `json:"compatible"`
	Incompatibilities []string          `json:"incompatibilities,omitempty"`
	Differences       []Difference      `json:"differences"`
	Tasks             []GroupComparison `json:"tasks"`
	Categories        []GroupComparison `json:"categories"`
	Summary           SummaryDelta      `json:"summary"`
}

// ComparisonSide is one experiment in a comparison.
type ComparisonSide struct {
	Experiment *storage.Experiment `json:"experiment"`
	Summary    metrics.Summary     `json:"summary"`
}

// Difference is one recorded configuration or environment difference.
type Difference struct {
	Field string `json:"field"`
	A     string `json:"a"`
	B     string `json:"b"`
}

// GroupComparison compares one task or category between the two experiments.
type GroupComparison struct {
	Key             string        `json:"key"`
	Category        string        `json:"category,omitempty"`
	A               metrics.Group `json:"a"`
	B               metrics.Group `json:"b"`
	PassRateDeltaPP *float64      `json:"pass_rate_delta_pp,omitempty"`
	Change          string        `json:"change"`
}

// SummaryDelta is the aggregate difference for the headline metrics.
type SummaryDelta struct {
	PassRateDeltaPP       *float64 `json:"pass_rate_delta_pp,omitempty"`
	EvaluatedDelta        int      `json:"evaluated_delta"`
	EvaluatorErrorsDelta  int      `json:"evaluator_errors_delta"`
	NotEvaluatedDelta     int      `json:"not_evaluated_delta"`
	TimeoutsDelta         int      `json:"timeouts_delta"`
	DurationMedianDeltaMS *float64 `json:"duration_median_delta_ms,omitempty"`
	ToolCallsDelta        *int     `json:"tool_calls_delta,omitempty"`
}

// BuildComparison aggregates both experiments and checks that their task
// selections (IDs, content fingerprints, fixture revisions) match. Configuration
// differences are reported, not rejected: comparing different agents or models
// is the point of the command. The behavior arguments may be nil when event logs
// are unavailable.
func BuildComparison(
	a, b *storage.Experiment,
	aAttempts, bAttempts []*storage.Attempt,
	aBehavior, bBehavior *metrics.Behavior,
) Comparison {
	comparison := Comparison{
		SchemaVersion: CompareSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Gauge:         version.Get(),
	}
	if a != nil {
		comparison.A = ComparisonSide{Experiment: a, Summary: metrics.Aggregate(a.ID, aAttempts)}
	}
	if b != nil {
		comparison.B = ComparisonSide{Experiment: b, Summary: metrics.Aggregate(b.ID, bAttempts)}
	}
	comparison.A.Summary.Behavior = aBehavior
	comparison.B.Summary.Behavior = bBehavior
	comparison.Incompatibilities = incompatibilityReasons(a, b)
	comparison.Compatible = len(comparison.Incompatibilities) == 0
	comparison.Differences = configurationDifferences(a, b)
	comparison.Tasks = compareGroups(comparison.A.Summary.PerTask, comparison.B.Summary.PerTask)
	comparison.Categories = compareGroups(comparison.A.Summary.PerCategory, comparison.B.Summary.PerCategory)
	comparison.Summary = summarizeComparison(comparison.A.Summary, comparison.B.Summary)
	return comparison
}

// incompatibilityReasons refuses comparisons whose benchmark content differs.
// Suite labels may differ (for example an ad-hoc run versus a named suite) as
// long as the actual task content matches.
func incompatibilityReasons(a, b *storage.Experiment) []string {
	if a == nil || b == nil {
		return []string{"both experiments are required"}
	}
	left := make(map[string]storage.TaskSelection, len(a.Selection))
	for _, selection := range a.Selection {
		left[selection.ID] = selection
	}
	right := make(map[string]storage.TaskSelection, len(b.Selection))
	for _, selection := range b.Selection {
		right[selection.ID] = selection
	}
	ids := make([]string, 0, len(left)+len(right))
	seen := make(map[string]bool, len(left)+len(right))
	for _, selections := range []map[string]storage.TaskSelection{left, right} {
		for id := range selections {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)

	var reasons []string
	for _, id := range ids {
		leftSelection, inLeft := left[id]
		rightSelection, inRight := right[id]
		switch {
		case !inLeft:
			reasons = append(reasons, fmt.Sprintf("task %q is only in B", id))
		case !inRight:
			reasons = append(reasons, fmt.Sprintf("task %q is only in A", id))
		default:
			if leftSelection.Fingerprint != rightSelection.Fingerprint {
				reasons = append(reasons, fmt.Sprintf(
					"task %q has a different content fingerprint (A %s, B %s)",
					id, leftSelection.Fingerprint, rightSelection.Fingerprint,
				))
			}
			if leftSelection.Fixture != rightSelection.Fixture {
				reasons = append(reasons, fmt.Sprintf(
					"task %q has a different fixture revision (A %s, B %s)",
					id, shortSHA(leftSelection.Fixture), shortSHA(rightSelection.Fixture),
				))
			}
		}
	}
	return reasons
}

// configurationDifferences lists recorded differences that matter for
// interpreting a comparison. Only differences are returned.
func configurationDifferences(a, b *storage.Experiment) []Difference {
	if a == nil || b == nil {
		return nil
	}
	var differences []Difference
	add := func(field, left, right string) {
		if left != right {
			differences = append(differences, Difference{Field: field, A: left, B: right})
		}
	}
	add("agent", a.Agent.ID, b.Agent.ID)
	add("agent version", valueOrUnavailable(a.Agent.Version), valueOrUnavailable(b.Agent.Version))
	add("model", valueOrUnavailable(a.Agent.Model), valueOrUnavailable(b.Agent.Model))
	add("provider", valueOrUnavailable(a.Agent.Provider), valueOrUnavailable(b.Agent.Provider))
	add("adapter config", a.Agent.ConfigFingerprint, b.Agent.ConfigFingerprint)
	add("suite", suiteLabel(a.Suite), suiteLabel(b.Suite))
	add("selection", selectionLabel(a.Selection), selectionLabel(b.Selection))
	add("repetitions", strconv.Itoa(a.Repetitions), strconv.Itoa(b.Repetitions))
	add("gauge", fmt.Sprintf("%s (%s)", a.Gauge.Version, a.Gauge.Commit), fmt.Sprintf("%s (%s)", b.Gauge.Version, b.Gauge.Commit))
	add("backend", valueOrUnavailable(a.Environment.Backend), valueOrUnavailable(b.Environment.Backend))
	add("platform", platformLabel(a.Environment), platformLabel(b.Environment))
	return differences
}

func compareGroups(left, right []metrics.Group) []GroupComparison {
	leftGroups := make(map[string]metrics.Group, len(left))
	for _, group := range left {
		leftGroups[group.Key] = group
	}
	rightGroups := make(map[string]metrics.Group, len(right))
	for _, group := range right {
		rightGroups[group.Key] = group
	}
	keys := make([]string, 0, len(leftGroups)+len(rightGroups))
	seen := make(map[string]bool, len(leftGroups)+len(rightGroups))
	for _, groups := range []map[string]metrics.Group{leftGroups, rightGroups} {
		for key := range groups {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)

	comparisons := make([]GroupComparison, 0, len(keys))
	for _, key := range keys {
		leftGroup := leftGroups[key]
		rightGroup := rightGroups[key]
		comparison := GroupComparison{
			Key:      key,
			Category: firstNonEmpty(leftGroup.Category, rightGroup.Category),
			A:        leftGroup,
			B:        rightGroup,
			Change:   "unknown",
		}
		if leftGroup.PassRate != nil && rightGroup.PassRate != nil {
			delta := (*rightGroup.PassRate - *leftGroup.PassRate) * 100
			comparison.PassRateDeltaPP = &delta
			comparison.Change = changeLabel(delta)
		}
		comparisons = append(comparisons, comparison)
	}
	return comparisons
}

func summarizeComparison(a, b metrics.Summary) SummaryDelta {
	delta := SummaryDelta{
		EvaluatedDelta:       b.Evaluated - a.Evaluated,
		EvaluatorErrorsDelta: b.EvaluatorErrors - a.EvaluatorErrors,
		NotEvaluatedDelta:    b.NotEvaluated - a.NotEvaluated,
		TimeoutsDelta:        b.Timeouts - a.Timeouts,
	}
	if a.PassRate != nil && b.PassRate != nil {
		value := (*b.PassRate - *a.PassRate) * 100
		delta.PassRateDeltaPP = &value
	}
	if a.Duration != nil && b.Duration != nil && a.Duration.Median != nil && b.Duration.Median != nil {
		value := *b.Duration.Median - *a.Duration.Median
		delta.DurationMedianDeltaMS = &value
	}
	if a.Behavior != nil && b.Behavior != nil {
		value := b.Behavior.ToolCalls - a.Behavior.ToolCalls
		delta.ToolCallsDelta = &value
	}
	return delta
}

func changeLabel(delta float64) string {
	switch {
	case math.Abs(delta) < 1e-9:
		return "unchanged"
	case delta > 0:
		return "improved"
	default:
		return "regressed"
	}
}

func valueOrUnavailable(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unavailable"
	}
	return value
}

func suiteLabel(suite *storage.SuiteInfo) string {
	if suite == nil {
		return "ad-hoc task selection"
	}
	return fmt.Sprintf("%s (%s)", suite.ID, suite.Fingerprint)
}

func selectionLabel(selection []storage.TaskSelection) string {
	data, err := json.Marshal(selection)
	if err != nil {
		return fmt.Sprintf("%d task(s)", len(selection))
	}
	return fmt.Sprintf("%d task(s) (%s)", len(selection), storage.FingerprintJSON(data))
}

func platformLabel(environment storage.EnvironmentInfo) string {
	return fmt.Sprintf("%s/%s, %s", environment.OS, environment.Arch, environment.GoVersion)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func shortSHA(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

// WriteComparisonJSON writes the comparison document as indented JSON.
func WriteComparisonJSON(w io.Writer, comparison Comparison) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(comparison); err != nil {
		return fmt.Errorf("report: encode comparison JSON: %w", err)
	}
	return nil
}

// WriteComparisonTerminal writes a concise comparison for a terminal.
func WriteComparisonTerminal(w io.Writer, comparison Comparison) error {
	if comparison.A.Experiment == nil || comparison.B.Experiment == nil {
		return fmt.Errorf("report: comparison requires two experiments")
	}
	fmt.Fprintf(w, "Comparison: %s  vs  %s\n", comparison.A.Experiment.ID, comparison.B.Experiment.ID)
	fmt.Fprintf(w, "A: %s\n", sideLabel(comparison.A))
	fmt.Fprintf(w, "B: %s\n", sideLabel(comparison.B))
	if comparison.Compatible {
		fmt.Fprintf(w, "Selection: compatible (%d task(s); fingerprints and fixture revisions match)\n",
			len(comparison.A.Experiment.Selection))
	} else {
		fmt.Fprintln(w, "Selection: INCOMPATIBLE")
		for _, reason := range comparison.Incompatibilities {
			fmt.Fprintf(w, "  - %s\n", reason)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Configuration differences:")
	if len(comparison.Differences) == 0 {
		fmt.Fprintln(w, "  none recorded")
	} else {
		tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  FIELD\tA\tB")
		for _, difference := range comparison.Differences {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", difference.Field, difference.A, difference.B)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Per-task outcomes:")
	if err := writeGroupTable(w, comparison.Tasks); err != nil {
		return err
	}
	fmt.Fprintln(w)

	if len(comparison.Categories) > 0 {
		fmt.Fprintln(w, "Per-category outcomes:")
		if err := writeGroupTable(w, comparison.Categories); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "Summary:")
	aSummary, bSummary := comparison.A.Summary, comparison.B.Summary
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  METRIC\tA\tB\tDELTA")
	fmt.Fprintf(tw, "  Pass rate\t%s\t%s\t%s\n",
		passRateText(aSummary), passRateText(bSummary), deltaPPText(comparison.Summary.PassRateDeltaPP))
	fmt.Fprintf(tw, "  Evaluated\t%d\t%d\t%s\n",
		aSummary.Evaluated, bSummary.Evaluated, signedInt(comparison.Summary.EvaluatedDelta))
	fmt.Fprintf(tw, "  Evaluator errors\t%d\t%d\t%s\n",
		aSummary.EvaluatorErrors, bSummary.EvaluatorErrors, signedInt(comparison.Summary.EvaluatorErrorsDelta))
	fmt.Fprintf(tw, "  Not evaluated\t%d\t%d\t%s\n",
		aSummary.NotEvaluated, bSummary.NotEvaluated, signedInt(comparison.Summary.NotEvaluatedDelta))
	fmt.Fprintf(tw, "  Timeouts\t%d\t%d\t%s\n",
		aSummary.Timeouts, bSummary.Timeouts, signedInt(comparison.Summary.TimeoutsDelta))
	fmt.Fprintf(tw, "  Duration (median)\t%s\t%s\t%s\n",
		comparisonDurationText(aSummary.Duration), comparisonDurationText(bSummary.Duration),
		signedDurationMS(comparison.Summary.DurationMedianDeltaMS))
	fmt.Fprintf(tw, "  Tool calls\t%s\t%s\t%s\n",
		comparisonBehaviorText(aSummary.Behavior), comparisonBehaviorText(bSummary.Behavior), signedIntPtr(comparison.Summary.ToolCallsDelta))
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "Tokens A: %s\n", usageText(aSummary.Usage))
	fmt.Fprintf(w, "Tokens B: %s\n", usageText(bSummary.Usage))
	return nil
}

func writeGroupTable(w io.Writer, groups []GroupComparison) error {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  KEY\tA PASS\tB PASS\tDELTA\tCHANGE")
	for _, group := range groups {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			group.Key, rateText(group.A.PassRate), rateText(group.B.PassRate),
			deltaPPText(group.PassRateDeltaPP), group.Change)
	}
	return tw.Flush()
}

func comparisonDurationText(stats *metrics.Stats) string {
	if stats == nil || stats.Median == nil {
		return "unavailable"
	}
	if stats.Count > 1 && stats.Min != nil && stats.Max != nil {
		return fmt.Sprintf("%s (%s–%s, n=%d)",
			metrics.FormatDurationMS(stats.Median),
			metrics.FormatDurationMS(stats.Min),
			metrics.FormatDurationMS(stats.Max),
			stats.Count)
	}
	return metrics.FormatDurationMS(stats.Median)
}

func comparisonBehaviorText(behavior *metrics.Behavior) string {
	if behavior == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d calls, %d failed (%d/%d logs)",
		behavior.ToolCalls, behavior.ToolFailures, behavior.AttemptsWithEvents, behavior.Attempts)
}

// WriteComparisonMarkdown writes the comparison as Markdown.
func WriteComparisonMarkdown(w io.Writer, comparison Comparison) error {
	if comparison.A.Experiment == nil || comparison.B.Experiment == nil {
		return fmt.Errorf("report: comparison requires two experiments")
	}
	b := &errWriter{w: w}
	b.printf("# Experiment comparison\n\n")
	b.printf("| Field | A | B |\n|---|---|---|\n")
	b.printf("| Experiment | `%s` | `%s` |\n", comparison.A.Experiment.ID, comparison.B.Experiment.ID)
	b.printf("| Agent | %s | %s |\n", md(sideAgent(comparison.A)), md(sideAgent(comparison.B)))
	b.printf("| Model | %s | %s |\n", md(valueOrUnavailable(comparison.A.Experiment.Agent.Model)), md(valueOrUnavailable(comparison.B.Experiment.Agent.Model)))
	b.printf("| Suite | %s | %s |\n", md(suiteLabel(comparison.A.Experiment.Suite)), md(suiteLabel(comparison.B.Experiment.Suite)))
	b.printf("| Repetitions | %d | %d |\n", comparison.A.Experiment.Repetitions, comparison.B.Experiment.Repetitions)
	b.printf("| Created | %s | %s |\n",
		comparison.A.Experiment.CreatedAt.UTC().Format(time.RFC3339),
		comparison.B.Experiment.CreatedAt.UTC().Format(time.RFC3339))
	b.printf("\n")

	b.printf("## Compatibility\n\n")
	if comparison.Compatible {
		b.printf("Task selections match (%d task(s); content fingerprints and fixture revisions are identical).\n\n", len(comparison.A.Experiment.Selection))
	} else {
		b.printf("**Not comparable.**\n\n")
		for _, reason := range comparison.Incompatibilities {
			b.printf("- %s\n", md(reason))
		}
		b.printf("\n")
	}

	b.printf("## Configuration and environment differences\n\n")
	if len(comparison.Differences) == 0 {
		b.printf("No recorded differences.\n\n")
	} else {
		b.printf("| Field | A | B |\n|---|---|---|\n")
		for _, difference := range comparison.Differences {
			b.printf("| %s | %s | %s |\n", md(difference.Field), md(difference.A), md(difference.B))
		}
		b.printf("\n")
	}

	b.printf("## Per-task outcomes\n\n")
	b.printf("| Task | A pass rate | B pass rate | Delta | Change |\n|---|---:|---:|---:|---|\n")
	for _, group := range comparison.Tasks {
		b.printf("| %s | %s | %s | %s | %s |\n",
			md(group.Key), rateText(group.A.PassRate), rateText(group.B.PassRate),
			deltaPPText(group.PassRateDeltaPP), group.Change)
	}
	b.printf("\n")

	if len(comparison.Categories) > 0 {
		b.printf("## Per-category outcomes\n\n")
		b.printf("| Category | A pass rate | B pass rate | Delta | Change |\n|---|---:|---:|---:|---|\n")
		for _, group := range comparison.Categories {
			b.printf("| %s | %s | %s | %s | %s |\n",
				md(group.Key), rateText(group.A.PassRate), rateText(group.B.PassRate),
				deltaPPText(group.PassRateDeltaPP), group.Change)
		}
		b.printf("\n")
	}

	aSummary, bSummary := comparison.A.Summary, comparison.B.Summary
	b.printf("## Summary\n\n")
	b.printf("| Metric | A | B | Delta |\n|---|---|---|---|\n")
	b.printf("| Pass rate | %s | %s | %s |\n", passRateText(aSummary), passRateText(bSummary), deltaPPText(comparison.Summary.PassRateDeltaPP))
	b.printf("| Evaluated | %d | %d | %s |\n", aSummary.Evaluated, bSummary.Evaluated, signedInt(comparison.Summary.EvaluatedDelta))
	b.printf("| Evaluator errors | %d | %d | %s |\n", aSummary.EvaluatorErrors, bSummary.EvaluatorErrors, signedInt(comparison.Summary.EvaluatorErrorsDelta))
	b.printf("| Not evaluated | %d | %d | %s |\n", aSummary.NotEvaluated, bSummary.NotEvaluated, signedInt(comparison.Summary.NotEvaluatedDelta))
	b.printf("| Timeouts | %d | %d | %s |\n", aSummary.Timeouts, bSummary.Timeouts, signedInt(comparison.Summary.TimeoutsDelta))
	b.printf("| Duration (median) | %s | %s | %s |\n",
		comparisonDurationText(aSummary.Duration), comparisonDurationText(bSummary.Duration), signedDurationMS(comparison.Summary.DurationMedianDeltaMS))
	b.printf("| Tool calls | %s | %s | %s |\n",
		comparisonBehaviorText(aSummary.Behavior), comparisonBehaviorText(bSummary.Behavior), signedIntPtr(comparison.Summary.ToolCallsDelta))
	b.printf("| Tokens | %s | %s | — |\n", usageText(aSummary.Usage), usageText(bSummary.Usage))
	b.printf("\n")
	b.printf("Missing metrics are reported as unavailable, never as zero. A comparison is only valid when the task selections match; use a holdout suite for final comparisons.\n")
	return b.err
}

func sideLabel(side ComparisonSide) string {
	exp := side.Experiment
	agent := exp.Agent.ID
	if exp.Agent.Version != "" {
		agent += " " + exp.Agent.Version
	}
	model := valueOrUnavailable(exp.Agent.Model)
	return fmt.Sprintf("%s (%s)  %s  model %s", agent, exp.Status, displayComparison(side.Summary), model)
}

func sideAgent(side ComparisonSide) string {
	agent := side.Experiment.Agent.ID
	if side.Experiment.Agent.Version != "" {
		agent += " " + side.Experiment.Agent.Version
	}
	return agent
}

func displayComparison(summary metrics.Summary) string {
	return passRateText(summary)
}

func deltaPPText(delta *float64) string {
	if delta == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%+.1fpp", *delta)
}

func signedInt(value int) string {
	if value == 0 {
		return "0"
	}
	return fmt.Sprintf("%+d", value)
}

func signedIntPtr(value *int) string {
	if value == nil {
		return "unavailable"
	}
	return signedInt(*value)
}

func signedDurationMS(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	sign := ""
	absolute := *value
	switch {
	case absolute > 0:
		sign = "+"
	case absolute < 0:
		sign = "-"
		absolute = -absolute
	}
	return sign + metrics.FormatDurationMS(&absolute)
}
