// Package report renders experiment results as terminal text, Markdown, or
// JSON. Reports preserve per-task outcomes and label unavailable metrics as
// unavailable instead of zero.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/m4ns0ur/gauge/internal/metrics"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/version"
)

// SchemaVersion is the export document schema version.
const SchemaVersion = 1

// Document is the exportable representation of an experiment.
type Document struct {
	SchemaVersion int                 `json:"schema_version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Gauge         version.Info        `json:"gauge"`
	Experiment    *storage.Experiment `json:"experiment"`
	Summary       metrics.Summary     `json:"summary"`
	Attempts      []*storage.Attempt  `json:"attempts"`
}

// Build assembles a report document.
func Build(exp *storage.Experiment, attempts []*storage.Attempt) Document {
	ordered := append([]*storage.Attempt(nil), attempts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].StartedAt.Equal(ordered[j].StartedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].StartedAt.Before(ordered[j].StartedAt)
	})
	id := ""
	if exp != nil {
		id = exp.ID
	}
	return Document{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Gauge:         version.Get(),
		Experiment:    exp,
		Summary:       metrics.Aggregate(id, ordered),
		Attempts:      ordered,
	}
}

// WriteJSON writes the document as indented JSON.
func WriteJSON(w io.Writer, doc Document) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("report: encode JSON: %w", err)
	}
	return nil
}

// WriteMarkdown writes a human-readable Markdown report.
func WriteMarkdown(w io.Writer, doc Document) error {
	b := &errWriter{w: w}
	exp := doc.Experiment
	if exp == nil {
		return fmt.Errorf("report: experiment is nil")
	}

	b.printf("# Experiment %s\n\n", exp.ID)
	b.printf("| Field | Value |\n|---|---|\n")
	b.printf("| Status | %s |\n", exp.Status)
	b.printf("| Agent | %s |\n", md(exp.Agent.ID))
	if exp.Agent.Version != "" {
		b.printf("| Agent version | %s |\n", md(exp.Agent.Version))
	}
	if exp.Agent.Model != "" {
		b.printf("| Model | %s |\n", md(exp.Agent.Model))
	} else {
		b.printf("| Model | unavailable |\n")
	}
	if exp.Agent.Provider != "" {
		b.printf("| Provider | %s |\n", md(exp.Agent.Provider))
	}
	if exp.Suite != nil {
		b.printf("| Suite | %s (`%s`) |\n", md(exp.Suite.Name), exp.Suite.Fingerprint)
	}
	b.printf("| Repetitions | %d |\n", exp.Repetitions)
	b.printf("| Created | %s |\n", exp.CreatedAt.UTC().Format(time.RFC3339))
	if exp.EndedAt != nil {
		b.printf("| Ended | %s |\n", exp.EndedAt.UTC().Format(time.RFC3339))
	}
	b.printf("| Gauge | %s (%s) |\n", md(exp.Gauge.Version), exp.Gauge.Commit)
	b.printf("| Backend | %s (%s/%s) |\n", exp.Environment.Backend, exp.Environment.OS, exp.Environment.Arch)
	b.printf("\n")

	writeMarkdownSummary(b, doc.Summary)

	b.printf("## Per-task outcomes\n\n")
	b.printf("| Task | Category | Attempts | Passed | Failed | Evaluator errors | Not evaluated | Pass rate | Median duration |\n")
	b.printf("|---|---|---:|---:|---:|---:|---:|---:|---|\n")
	for _, g := range doc.Summary.PerTask {
		b.printf("| %s | %s | %d | %d | %d | %d | %d | %s | %s |\n",
			md(g.Key), md(g.Category), g.Attempts, g.Passed, g.Failed, g.Errors, g.NotEvaluated,
			rateText(g.PassRate), durationText(g.Duration))
	}
	b.printf("\n")

	b.printf("## Per-category outcomes\n\n")
	b.printf("| Category | Attempts | Passed | Failed | Pass rate |\n")
	b.printf("|---|---:|---:|---:|---|\n")
	for _, g := range doc.Summary.PerCategory {
		b.printf("| %s | %d | %d | %d | %s |\n", md(g.Key), g.Attempts, g.Passed, g.Failed, rateText(g.PassRate))
	}
	b.printf("\n")

	b.printf("## Attempts\n\n")
	b.printf("| Attempt | Task | Rep | Outcome | Execution | Duration | Evaluation |\n")
	b.printf("|---|---|---:|---|---|---:|---|\n")
	for _, a := range doc.Attempts {
		b.printf("| %s | %s | %d | %s | %s | %s | %s |\n",
			md(a.ID), md(a.TaskID), a.Repetition+1, string(a.Outcome), md(executionText(a)),
			formatMS(a.DurationMS), evaluationText(a))
	}
	b.printf("\n")

	checks := attemptsWithChecks(doc.Attempts)
	if len(checks) > 0 {
		b.printf("## Evaluation checks\n\n")
		for _, a := range checks {
			b.printf("### %s (%s)\n\n", a.ID, a.TaskID)
			for _, c := range a.Evaluation.Checks {
				b.printf("- %s: **%s**%s\n", md(c.ID), c.Status, messageSuffix(c.Message))
			}
			b.printf("\n")
		}
	}

	b.printf("## Reproducibility details\n\n")
	b.printf("- Gauge: %s (commit `%s`)\n", doc.Gauge.Version, doc.Gauge.Commit)
	b.printf("- Agent: `%s`", exp.Agent.ID)
	if exp.Agent.Version != "" {
		b.printf(" version %s", exp.Agent.Version)
	}
	b.printf("\n")
	b.printf("- Adapter config: `%s`", exp.Agent.ConfigFingerprint)
	if exp.Agent.ConfigRedacted {
		b.printf(" (secret values redacted)")
	}
	b.printf("\n")
	if exp.Suite != nil {
		b.printf("- Suite: `%s` fingerprint `%s`\n", exp.Suite.ID, exp.Suite.Fingerprint)
	}
	b.printf("- Task fingerprints:\n")
	for _, sel := range exp.Selection {
		b.printf("  - `%s`: `%s` (fixture `%s`)\n", sel.ID, sel.Fingerprint, sel.Fixture)
	}
	b.printf("- Environment: %s/%s, %s, backend `%s`\n", exp.Environment.OS, exp.Environment.Arch, exp.Environment.GoVersion, exp.Environment.Backend)
	if exp.Environment.GitVersion != "" {
		b.printf("- Git: %s\n", exp.Environment.GitVersion)
	}
	b.printf("- Data directory: `%s`\n", exp.Environment.DataDir)
	b.printf("\n")
	b.printf("Metrics unavailable from the agent or adapter are reported as `unavailable`, never as zero.\n")

	return b.err
}

func writeMarkdownSummary(b *errWriter, s metrics.Summary) {
	b.printf("## Summary\n\n")
	b.printf("- Task pass rate: **%s**\n", passRateText(s))
	b.printf("- Evaluated attempts: %d of %d\n", s.Evaluated, s.Attempts)
	b.printf("- Evaluator errors: %d\n", s.EvaluatorErrors)
	b.printf("- Attempts not evaluated (setup/workspace failure): %d\n", s.NotEvaluated)
	b.printf("- Timeouts: %d\n", s.Timeouts)
	b.printf("- Cancelled: %d\n", s.Cancelled)
	b.printf("- Agent execution failures: %d\n", s.ExecutionFailures)
	b.printf("- Setup failures: %d\n", s.SetupFailures)
	b.printf("- Duration (all attempts): %s\n", durationText(s.Duration))
	b.printf("- Token usage: %s\n", usageText(s.Usage))
	b.printf("\n")
}

// WriteTerminal writes a concise report suitable for a terminal.
func WriteTerminal(w io.Writer, doc Document) error {
	exp := doc.Experiment
	if exp == nil {
		return fmt.Errorf("report: experiment is nil")
	}
	fmt.Fprintf(w, "Experiment: %s (%s)\n", exp.ID, exp.Status)
	agent := exp.Agent.ID
	if exp.Agent.Version != "" {
		agent += " " + exp.Agent.Version
	}
	model := exp.Agent.Model
	if model == "" {
		model = "unavailable"
	}
	fmt.Fprintf(w, "Agent:      %s   Model: %s\n", agent, model)
	if exp.Suite != nil {
		fmt.Fprintf(w, "Suite:      %s (%s)\n", exp.Suite.ID, exp.Suite.Fingerprint)
	}
	s := doc.Summary
	fmt.Fprintf(w, "Attempts:   %d   Passed: %s   Evaluator errors: %d   Not evaluated: %d\n",
		s.Attempts, passRateText(s), s.EvaluatorErrors, s.NotEvaluated)
	fmt.Fprintf(w, "Reliability: timeouts %d, cancelled %d, execution failures %d, setup failures %d\n",
		s.Timeouts, s.Cancelled, s.ExecutionFailures, s.SetupFailures)
	fmt.Fprintf(w, "Duration:   %s\n", durationText(s.Duration))
	fmt.Fprintf(w, "Tokens:     %s\n", usageText(s.Usage))
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tCATEGORY\tATTEMPTS\tPASS\tFAIL\tERROR\tNOT EVALUATED\tPASS RATE")
	for _, g := range s.PerTask {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			g.Key, g.Category, g.Attempts, g.Passed, g.Failed, g.Errors, g.NotEvaluated, rateText(g.PassRate))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return nil
}

func attemptsWithChecks(attempts []*storage.Attempt) []*storage.Attempt {
	var out []*storage.Attempt
	for _, a := range attempts {
		if a.Evaluation != nil && len(a.Evaluation.Checks) > 0 {
			out = append(out, a)
		}
	}
	return out
}

func passRateText(s metrics.Summary) string {
	if s.PassRate == nil {
		return fmt.Sprintf("unavailable (0 evaluated of %d attempts)", s.Attempts)
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", s.Passed, s.Evaluated, *s.PassRate*100)
}

func rateText(rate *float64) string {
	if rate == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.1f%%", *rate*100)
}

func durationText(stats *metrics.Stats) string {
	if stats == nil || stats.Median == nil {
		return "unavailable"
	}
	text := "median " + metrics.FormatDurationMS(stats.Median)
	if stats.Min != nil && stats.Max != nil {
		text += fmt.Sprintf(" (range %s–%s, n=%d)",
			metrics.FormatDurationMS(stats.Min), metrics.FormatDurationMS(stats.Max), stats.Count)
	}
	return text
}

func usageText(u *metrics.Usage) string {
	if u == nil {
		return "unavailable (no adapter-reported usage)"
	}
	var parts []string
	if u.InputTokens != nil {
		parts = append(parts, fmt.Sprintf("input %d", *u.InputTokens))
	}
	if u.OutputTokens != nil {
		parts = append(parts, fmt.Sprintf("output %d", *u.OutputTokens))
	}
	if u.TotalTokens != nil {
		parts = append(parts, fmt.Sprintf("total %d", *u.TotalTokens))
	}
	if u.CostUSD != nil {
		parts = append(parts, fmt.Sprintf("cost $%.4f (adapter-reported)", *u.CostUSD))
	}
	if len(parts) == 0 {
		return "unavailable (adapter reported nothing)"
	}
	return fmt.Sprintf("%s [reported for %d/%d attempts]", strings.Join(parts, ", "), u.AttemptsWithUsage, u.Attempts)
}

func executionText(a *storage.Attempt) string {
	text := string(a.Execution.Completion)
	if a.Execution.TimedOut {
		text = "timed_out"
	}
	if a.Execution.Cancelled {
		text = "cancelled"
	}
	if a.Execution.AdapterError != "" {
		text += " (adapter error)"
	}
	if a.Setup != nil && a.Setup.Error != "" {
		text = "setup_failed"
	}
	return text
}

func evaluationText(a *storage.Attempt) string {
	if a.Evaluation == nil {
		return "not run"
	}
	if a.Evaluation.Error != "" && a.Evaluation.Status == "error" {
		return "error"
	}
	c := a.Evaluation.ChecksSummary()
	return fmt.Sprintf("%s (%d/%d checks)", a.Evaluation.Status, c.Passed, c.Total)
}

func messageSuffix(msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	return " — " + strings.TrimSpace(msg)
}

func md(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func formatMS(ms int64) string {
	return time.Duration(ms * int64(time.Millisecond)).Round(time.Millisecond).String()
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
