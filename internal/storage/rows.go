package storage

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/evaluator"
	"github.com/m4ns0ur/gauge/internal/events"
)

// timeLayout is a fixed-width UTC timestamp. Fixed width keeps TEXT timestamps
// correctly ordered by SQLite, which RFC3339 with optional fractional seconds
// does not guarantee.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

var experimentColumns = []string{
	"id", "schema_version", "status", "created_at", "started_at", "ended_at",
	"gauge_version", "gauge_commit",
	"agent_id", "agent_description", "agent_version", "agent_provider", "agent_model",
	"agent_config", "agent_config_fingerprint", "agent_config_redacted",
	"suite_id", "suite_name", "suite_fingerprint", "suite_file", "suite_tasks",
	"selection", "repetitions", "environment", "notes",
}

var attemptColumns = []string{
	"id", "experiment_id", "schema_version", "task_id", "task_name", "task_category",
	"task_fingerprint", "repetition", "agent_id", "agent_version", "model", "provider",
	"started_at", "ended_at", "duration_ms",
	"execution_completion", "execution_exit_code", "execution_signal", "execution_error",
	"execution_adapter_error", "execution_timed_out", "execution_cancelled",
	"setup",
	"usage_input_tokens", "usage_output_tokens", "usage_total_tokens", "usage_cost_usd",
	"diff",
	"evaluation_status", "evaluation_summary", "evaluation_score", "evaluation_error",
	"evaluation_timed_out", "evaluation_exit_code", "evaluation_duration_ms",
	"adapter_metadata", "workspace_kept", "error", "outcome",
}

var (
	upsertExperimentSQL = upsertSQL("experiments", experimentColumns)
	upsertAttemptSQL    = upsertSQL("attempts", attemptColumns)

	selectExperimentSQL = "SELECT " + strings.Join(experimentColumns, ", ") + " FROM experiments"
	selectAttemptSQL    = "SELECT " + strings.Join(attemptColumns, ", ") + " FROM attempts"
)

func upsertSQL(table string, columns []string) string {
	updates := make([]string, 0, len(columns)-1)
	for _, column := range columns[1:] {
		updates = append(updates, column+" = excluded."+column)
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(id) DO UPDATE SET %s",
		table,
		strings.Join(columns, ", "),
		placeholders(len(columns)),
		strings.Join(updates, ", "),
	)
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ", ")
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// --- encoding ---------------------------------------------------------------

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(value string) (time.Time, error) {
	return time.Parse(timeLayout, value)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableRaw(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	// Store raw JSON compactly so a record re-encoded by an older writer (or by
	// json.MarshalIndent) still round-trips byte-for-byte.
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return string(value)
	}
	return compact.String()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func intPtrValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64PtrValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func floatPtrValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func experimentArgs(exp *Experiment) ([]any, error) {
	selection, err := json.Marshal(exp.Selection)
	if err != nil {
		return nil, fmt.Errorf("storage: encode experiment selection: %w", err)
	}
	environment, err := json.Marshal(exp.Environment)
	if err != nil {
		return nil, fmt.Errorf("storage: encode experiment environment: %w", err)
	}
	var suiteID, suiteName, suiteFingerprint, suiteFile, suiteTasks any
	if exp.Suite != nil {
		tasks, err := json.Marshal(exp.Suite.Tasks)
		if err != nil {
			return nil, fmt.Errorf("storage: encode experiment suite tasks: %w", err)
		}
		suiteID = nullableString(exp.Suite.ID)
		suiteName = nullableString(exp.Suite.Name)
		suiteFingerprint = nullableString(exp.Suite.Fingerprint)
		suiteFile = nullableString(exp.Suite.File)
		suiteTasks = string(tasks)
	}
	return []any{
		exp.ID, exp.SchemaVersion, exp.Status, formatTime(exp.CreatedAt), formatTime(exp.StartedAt), nullableTime(exp.EndedAt),
		exp.Gauge.Version, exp.Gauge.Commit,
		exp.Agent.ID, nullableString(exp.Agent.Description), nullableString(exp.Agent.Version),
		nullableString(exp.Agent.Provider), nullableString(exp.Agent.Model),
		nullableRaw(exp.Agent.Config), exp.Agent.ConfigFingerprint, boolInt(exp.Agent.ConfigRedacted),
		suiteID, suiteName, suiteFingerprint, suiteFile, suiteTasks,
		string(selection), exp.Repetitions, string(environment), nullableString(exp.Notes),
	}, nil
}

func attemptArgs(a *Attempt) ([]any, error) {
	diff, err := json.Marshal(a.Diff)
	if err != nil {
		return nil, fmt.Errorf("storage: encode attempt diff: %w", err)
	}
	var setup any
	if a.Setup != nil {
		data, err := json.Marshal(a.Setup)
		if err != nil {
			return nil, fmt.Errorf("storage: encode attempt setup: %w", err)
		}
		setup = string(data)
	}
	execution := a.Execution
	args := []any{
		a.ID, a.ExperimentID, a.SchemaVersion, a.TaskID,
		nullableString(a.TaskName), nullableString(a.TaskCategory), nullableString(a.TaskFingerprint),
		a.Repetition, a.AgentID, nullableString(a.AgentVersion), nullableString(a.Model), nullableString(a.Provider),
		formatTime(a.StartedAt), formatTime(a.EndedAt), a.DurationMS,
		string(execution.Completion), intPtrValue(execution.ExitCode), nullableString(execution.Signal),
		nullableString(execution.Error), nullableString(execution.AdapterError),
		boolInt(execution.TimedOut), boolInt(execution.Cancelled),
		setup,
		nil, nil, nil, nil, // usage
		string(diff),
		nil, nil, nil, nil, nil, nil, nil, // evaluation
		nullableRaw(a.AdapterMetadata),
		boolInt(a.WorkspaceKept),
		nullableString(a.Error),
		string(a.Outcome),
	}
	if a.Usage != nil {
		args[23] = int64PtrValue(a.Usage.InputTokens)
		args[24] = int64PtrValue(a.Usage.OutputTokens)
		args[25] = int64PtrValue(a.Usage.TotalTokens)
		args[26] = floatPtrValue(a.Usage.CostUSD)
	}
	if a.Evaluation != nil {
		args[28] = string(a.Evaluation.Status)
		args[29] = nullableString(a.Evaluation.Summary)
		args[30] = floatPtrValue(a.Evaluation.Score)
		args[31] = nullableString(a.Evaluation.Error)
		args[32] = boolInt(a.Evaluation.TimedOut)
		args[33] = intPtrValue(a.Evaluation.ExitCode)
		args[34] = a.Evaluation.DurationMS
	}
	return args, nil
}

// --- decoding ---------------------------------------------------------------

func scanExperiment(row rowScanner) (*Experiment, error) {
	var (
		exp                                             Experiment
		createdAt, startedAt                            string
		endedAt                                         sql.NullString
		agentDescription, agentVersion, agentProvider   sql.NullString
		agentModel, agentConfig                         sql.NullString
		agentConfigRedacted                             int
		suiteID, suiteName, suiteFingerprint, suiteFile sql.NullString
		suiteTasks                                      sql.NullString
		selection, environment                          string
		notes                                           sql.NullString
	)
	err := row.Scan(
		&exp.ID, &exp.SchemaVersion, &exp.Status, &createdAt, &startedAt, &endedAt,
		&exp.Gauge.Version, &exp.Gauge.Commit,
		&exp.Agent.ID, &agentDescription, &agentVersion, &agentProvider, &agentModel,
		&agentConfig, &exp.Agent.ConfigFingerprint, &agentConfigRedacted,
		&suiteID, &suiteName, &suiteFingerprint, &suiteFile, &suiteTasks,
		&selection, &exp.Repetitions, &environment, &notes,
	)
	if err != nil {
		return nil, err
	}
	if exp.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("experiment %q created_at: %w", exp.ID, err)
	}
	if exp.StartedAt, err = parseTime(startedAt); err != nil {
		return nil, fmt.Errorf("experiment %q started_at: %w", exp.ID, err)
	}
	if endedAt.Valid {
		parsed, err := parseTime(endedAt.String)
		if err != nil {
			return nil, fmt.Errorf("experiment %q ended_at: %w", exp.ID, err)
		}
		exp.EndedAt = &parsed
	}
	exp.Agent.Description = agentDescription.String
	exp.Agent.Version = agentVersion.String
	exp.Agent.Provider = agentProvider.String
	exp.Agent.Model = agentModel.String
	if agentConfig.Valid {
		exp.Agent.Config = json.RawMessage(agentConfig.String)
	}
	exp.Agent.ConfigRedacted = agentConfigRedacted != 0
	if suiteID.Valid {
		exp.Suite = &SuiteInfo{
			ID:          suiteID.String,
			Name:        suiteName.String,
			Fingerprint: suiteFingerprint.String,
			File:        suiteFile.String,
		}
		if suiteTasks.Valid {
			if err := json.Unmarshal([]byte(suiteTasks.String), &exp.Suite.Tasks); err != nil {
				return nil, fmt.Errorf("experiment %q suite tasks: %w", exp.ID, err)
			}
		}
	}
	if err := json.Unmarshal([]byte(selection), &exp.Selection); err != nil {
		return nil, fmt.Errorf("experiment %q selection: %w", exp.ID, err)
	}
	if err := json.Unmarshal([]byte(environment), &exp.Environment); err != nil {
		return nil, fmt.Errorf("experiment %q environment: %w", exp.ID, err)
	}
	exp.Notes = notes.String
	return &exp, nil
}

func scanAttempt(row rowScanner) (*Attempt, error) {
	var (
		a                                       Attempt
		taskName, taskCategory, taskFingerprint sql.NullString
		agentVersion, model, provider           sql.NullString
		startedAt, endedAt                      string
		executionCompletion, executionSignal    sql.NullString
		executionError, executionAdapterError   sql.NullString
		executionExitCode                       sql.NullInt64
		executionTimedOut, executionCancelled   int
		setup                                   sql.NullString
		usageInput, usageOutput, usageTotal     sql.NullInt64
		usageCost                               sql.NullFloat64
		diff                                    string
		evaluationStatus, evaluationSummary     sql.NullString
		evaluationScore                         sql.NullFloat64
		evaluationError                         sql.NullString
		evaluationTimedOut                      sql.NullInt64
		evaluationExitCode                      sql.NullInt64
		evaluationDurationMS                    sql.NullInt64
		adapterMetadata                         sql.NullString
		workspaceKept                           int
		errorText, outcome                      sql.NullString
	)
	err := row.Scan(
		&a.ID, &a.ExperimentID, &a.SchemaVersion, &a.TaskID, &taskName, &taskCategory,
		&taskFingerprint, &a.Repetition, &a.AgentID, &agentVersion, &model, &provider,
		&startedAt, &endedAt, &a.DurationMS,
		&executionCompletion, &executionExitCode, &executionSignal, &executionError,
		&executionAdapterError, &executionTimedOut, &executionCancelled,
		&setup,
		&usageInput, &usageOutput, &usageTotal, &usageCost,
		&diff,
		&evaluationStatus, &evaluationSummary, &evaluationScore, &evaluationError,
		&evaluationTimedOut, &evaluationExitCode, &evaluationDurationMS,
		&adapterMetadata, &workspaceKept, &errorText, &outcome,
	)
	if err != nil {
		return nil, err
	}
	a.TaskName = taskName.String
	a.TaskCategory = taskCategory.String
	a.TaskFingerprint = taskFingerprint.String
	a.AgentVersion = agentVersion.String
	a.Model = model.String
	a.Provider = provider.String
	if a.StartedAt, err = parseTime(startedAt); err != nil {
		return nil, fmt.Errorf("attempt %q started_at: %w", a.ID, err)
	}
	if a.EndedAt, err = parseTime(endedAt); err != nil {
		return nil, fmt.Errorf("attempt %q ended_at: %w", a.ID, err)
	}
	a.Execution = ExecutionInfo{
		Completion:   adapter.Completion(executionCompletion.String),
		Signal:       executionSignal.String,
		Error:        executionError.String,
		AdapterError: executionAdapterError.String,
		TimedOut:     executionTimedOut != 0,
		Cancelled:    executionCancelled != 0,
	}
	if executionExitCode.Valid {
		code := int(executionExitCode.Int64)
		a.Execution.ExitCode = &code
	}
	if setup.Valid {
		var info SetupInfo
		if err := json.Unmarshal([]byte(setup.String), &info); err != nil {
			return nil, fmt.Errorf("attempt %q setup: %w", a.ID, err)
		}
		a.Setup = &info
	}
	if usageInput.Valid || usageOutput.Valid || usageTotal.Valid || usageCost.Valid {
		usage := &events.Usage{}
		if usageInput.Valid {
			value := usageInput.Int64
			usage.InputTokens = &value
		}
		if usageOutput.Valid {
			value := usageOutput.Int64
			usage.OutputTokens = &value
		}
		if usageTotal.Valid {
			value := usageTotal.Int64
			usage.TotalTokens = &value
		}
		if usageCost.Valid {
			value := usageCost.Float64
			usage.CostUSD = &value
		}
		a.Usage = usage
	}
	if err := json.Unmarshal([]byte(diff), &a.Diff); err != nil {
		return nil, fmt.Errorf("attempt %q diff: %w", a.ID, err)
	}
	if evaluationStatus.Valid {
		evaluation := &EvaluationInfo{
			Status:     evaluator.Status(evaluationStatus.String),
			Summary:    evaluationSummary.String,
			Error:      evaluationError.String,
			DurationMS: evaluationDurationMS.Int64,
		}
		if evaluationScore.Valid {
			value := evaluationScore.Float64
			evaluation.Score = &value
		}
		if evaluationTimedOut.Valid {
			evaluation.TimedOut = evaluationTimedOut.Int64 != 0
		}
		if evaluationExitCode.Valid {
			code := int(evaluationExitCode.Int64)
			evaluation.ExitCode = &code
		}
		a.Evaluation = evaluation
	}
	if adapterMetadata.Valid {
		a.AdapterMetadata = json.RawMessage(adapterMetadata.String)
	}
	a.WorkspaceKept = workspaceKept != 0
	a.Error = errorText.String
	a.Outcome = Outcome(outcome.String)
	return &a, nil
}

// --- child rows -------------------------------------------------------------

func loadChecks(q querier, attemptID string) ([]evaluator.Check, error) {
	rows, err := q.Query(
		`SELECT id, status, message FROM checks WHERE attempt_id = ? ORDER BY position`,
		attemptID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: read checks for %q: %w", attemptID, err)
	}
	defer rows.Close()

	var checks []evaluator.Check
	for rows.Next() {
		var (
			check   evaluator.Check
			status  string
			message sql.NullString
		)
		if err := rows.Scan(&check.ID, &status, &message); err != nil {
			return nil, fmt.Errorf("storage: scan check for %q: %w", attemptID, err)
		}
		check.Status = evaluator.Status(status)
		check.Message = message.String
		checks = append(checks, check)
	}
	return checks, rows.Err()
}

func loadArtifacts(q querier, attemptID string) (map[string]Artifact, error) {
	rows, err := q.Query(
		`SELECT name, path, size, sha256 FROM artifacts WHERE attempt_id = ? ORDER BY name`,
		attemptID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: read artifacts for %q: %w", attemptID, err)
	}
	defer rows.Close()

	artifacts := make(map[string]Artifact)
	for rows.Next() {
		var (
			name     string
			artifact Artifact
		)
		if err := rows.Scan(&name, &artifact.Path, &artifact.Size, &artifact.SHA256); err != nil {
			return nil, fmt.Errorf("storage: scan artifact for %q: %w", attemptID, err)
		}
		artifacts[name] = artifact
	}
	return artifacts, rows.Err()
}

func saveAttemptRows(tx *sql.Tx, a *Attempt) error {
	args, err := attemptArgs(a)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(upsertAttemptSQL, args...); err != nil {
		return fmt.Errorf("storage: write attempt %q: %w", a.ID, err)
	}
	for _, table := range []string{"checks", "artifacts", "metrics"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE attempt_id = ?", a.ID); err != nil {
			return fmt.Errorf("storage: clear %s for %q: %w", table, a.ID, err)
		}
	}
	if a.Evaluation != nil {
		for position, check := range a.Evaluation.Checks {
			if _, err := tx.Exec(
				`INSERT INTO checks (attempt_id, position, id, status, message) VALUES (?, ?, ?, ?, ?)`,
				a.ID, position, check.ID, string(check.Status), nullableString(check.Message),
			); err != nil {
				return fmt.Errorf("storage: write check %q for %q: %w", check.ID, a.ID, err)
			}
		}
	}
	names := make([]string, 0, len(a.Artifacts))
	for name := range a.Artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		artifact := a.Artifacts[name]
		if _, err := tx.Exec(
			`INSERT INTO artifacts (attempt_id, name, path, size, sha256) VALUES (?, ?, ?, ?, ?)`,
			a.ID, name, artifact.Path, artifact.Size, artifact.SHA256,
		); err != nil {
			return fmt.Errorf("storage: write artifact %q for %q: %w", name, a.ID, err)
		}
	}
	for _, metric := range attemptMetrics(a) {
		if _, err := tx.Exec(
			`INSERT INTO metrics (attempt_id, name, value, unit, source) VALUES (?, ?, ?, ?, ?)`,
			a.ID, metric.name, metric.value, metric.unit, metric.source,
		); err != nil {
			return fmt.Errorf("storage: write metric %q for %q: %w", metric.name, a.ID, err)
		}
	}
	return nil
}

// metricRow is one typed measurement with its unit and provenance.
type metricRow struct {
	name   string
	value  float64
	unit   string
	source string
}

// attemptMetrics derives the typed metric rows persisted for one attempt. Usage
// and cost are recorded only when the adapter reported them; duration is always
// observed by Gauge.
func attemptMetrics(a *Attempt) []metricRow {
	metrics := []metricRow{
		{name: "duration_ms", value: float64(a.DurationMS), unit: "ms", source: "gauge-observed"},
	}
	if a.Usage != nil {
		if a.Usage.InputTokens != nil {
			metrics = append(metrics, metricRow{name: "usage.input_tokens", value: float64(*a.Usage.InputTokens), unit: "tokens", source: "adapter-reported"})
		}
		if a.Usage.OutputTokens != nil {
			metrics = append(metrics, metricRow{name: "usage.output_tokens", value: float64(*a.Usage.OutputTokens), unit: "tokens", source: "adapter-reported"})
		}
		if a.Usage.TotalTokens != nil {
			metrics = append(metrics, metricRow{name: "usage.total_tokens", value: float64(*a.Usage.TotalTokens), unit: "tokens", source: "adapter-reported"})
		}
		if a.Usage.CostUSD != nil {
			metrics = append(metrics, metricRow{name: "usage.cost_usd", value: *a.Usage.CostUSD, unit: "usd", source: "adapter-reported"})
		}
	}
	if a.Evaluation != nil && a.Evaluation.DurationMS > 0 {
		metrics = append(metrics, metricRow{name: "evaluation.duration_ms", value: float64(a.Evaluation.DurationMS), unit: "ms", source: "gauge-observed"})
	}
	return metrics
}
