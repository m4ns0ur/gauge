// Package storage defines Gauge's persisted domain records: experiments,
// attempts, evaluator checks, artifacts, usage, and typed metrics.
//
// Metadata lives in SQLite; large artifacts stay as files. The Store interface
// is deliberately narrow so the runner, reports, and CLI do not depend on the
// database layout.
package storage

import (
	"encoding/json"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/evaluator"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/workspace"
)

// SchemaVersion is the supported persisted metadata schema version.
const SchemaVersion = 1

// Outcome is the task-level result of one attempt. It is derived from the
// independent evaluator, never from the agent.
type Outcome string

const (
	// OutcomePassed means the evaluator reported passed.
	OutcomePassed Outcome = "passed"
	// OutcomeFailed means the evaluator reported failed.
	OutcomeFailed Outcome = "failed"
	// OutcomeError means the evaluator could not produce a valid verdict.
	OutcomeError Outcome = "error"
	// OutcomeNotEvaluated means the evaluator never ran (setup or adapter
	// failure), so the attempt carries no task result.
	OutcomeNotEvaluated Outcome = "not_evaluated"
)

// Valid reports whether o is a known outcome.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomePassed, OutcomeFailed, OutcomeError, OutcomeNotEvaluated:
		return true
	}
	return false
}

// Artifact names recorded for an attempt. Only files that exist are recorded.
const (
	ArtifactEvents           = "events.jsonl"
	ArtifactStdout           = "stdout.log"
	ArtifactStderr           = "stderr.log"
	ArtifactDiff             = "diff.patch"
	ArtifactGitStatus        = "git-status.txt"
	ArtifactEvaluation       = "evaluation.json"
	ArtifactEvaluatorStdout  = "evaluator-stdout.log"
	ArtifactEvaluatorStderr  = "evaluator-stderr.log"
	ArtifactAdapterRawPrefix = "adapter-raw-"
)

// Artifact is a persisted file with integrity metadata.
type Artifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Experiment is one invocation of one agent configuration against a task
// selection.
type Experiment struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     time.Time       `json:"started_at"`
	EndedAt       *time.Time      `json:"ended_at,omitempty"`
	Gauge         GaugeInfo       `json:"gauge"`
	Agent         AgentInfo       `json:"agent"`
	Suite         *SuiteInfo      `json:"suite,omitempty"`
	Selection     []TaskSelection `json:"selection"`
	Repetitions   int             `json:"repetitions"`
	Environment   EnvironmentInfo `json:"environment"`
	Notes         string          `json:"notes,omitempty"`
}

// Experiment statuses.
const (
	StatusRunning     = "running"
	StatusCompleted   = "completed"
	StatusInterrupted = "interrupted"
	StatusFailed      = "failed"
)

// GaugeInfo identifies the binary that produced the results.
type GaugeInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// AgentInfo records the agent system under test.
type AgentInfo struct {
	ID                string          `json:"id"`
	Description       string          `json:"description,omitempty"`
	Version           string          `json:"version,omitempty"`
	Provider          string          `json:"provider,omitempty"`
	Model             string          `json:"model,omitempty"`
	Config            json.RawMessage `json:"config,omitempty"`
	ConfigFingerprint string          `json:"config_fingerprint"`
	ConfigRedacted    bool            `json:"config_redacted"`
}

// SuiteInfo identifies the exact suite selection used.
type SuiteInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Fingerprint string   `json:"fingerprint"`
	File        string   `json:"file,omitempty"`
	Tasks       []string `json:"tasks"`
}

// TaskSelection pins one selected task.
type TaskSelection struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Fingerprint string `json:"fingerprint"`
	Fixture     string `json:"fixture_revision"`
}

// EnvironmentInfo captures the execution environment.
type EnvironmentInfo struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	GoVersion  string `json:"go_version"`
	Backend    string `json:"backend"`
	DataDir    string `json:"data_dir"`
	GitVersion string `json:"git_version,omitempty"`
}

// Attempt is one execution of one task within an experiment.
type Attempt struct {
	SchemaVersion   int                 `json:"schema_version"`
	ID              string              `json:"id"`
	ExperimentID    string              `json:"experiment_id"`
	TaskID          string              `json:"task_id"`
	TaskName        string              `json:"task_name"`
	TaskCategory    string              `json:"task_category"`
	TaskFingerprint string              `json:"task_fingerprint"`
	Repetition      int                 `json:"repetition"`
	AgentID         string              `json:"agent_id"`
	AgentVersion    string              `json:"agent_version,omitempty"`
	Model           string              `json:"model,omitempty"`
	Provider        string              `json:"provider,omitempty"`
	StartedAt       time.Time           `json:"started_at"`
	EndedAt         time.Time           `json:"ended_at"`
	DurationMS      int64               `json:"duration_ms"`
	Execution       ExecutionInfo       `json:"execution"`
	Setup           *SetupInfo          `json:"setup,omitempty"`
	Usage           *events.Usage       `json:"usage,omitempty"`
	Diff            DiffInfo            `json:"diff"`
	Evaluation      *EvaluationInfo     `json:"evaluation,omitempty"`
	Outcome         Outcome             `json:"outcome"`
	Artifacts       map[string]Artifact `json:"artifacts"`
	AdapterMetadata json.RawMessage     `json:"adapter_metadata,omitempty"`
	WorkspaceKept   bool                `json:"workspace_kept"`
	Error           string              `json:"error,omitempty"`
}

// ExecutionInfo describes the agent process, not task correctness.
type ExecutionInfo struct {
	Completion   adapter.Completion `json:"completion"`
	ExitCode     *int               `json:"exit_code,omitempty"`
	Signal       string             `json:"signal,omitempty"`
	Error        string             `json:"error,omitempty"`
	AdapterError string             `json:"adapter_error,omitempty"`
	TimedOut     bool               `json:"timed_out"`
	Cancelled    bool               `json:"cancelled"`
}

// SetupInfo summarizes pre-agent setup commands.
type SetupInfo struct {
	Ran       bool   `json:"ran"`
	Steps     int    `json:"steps"`
	Error     string `json:"error,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// DiffInfo is the candidate change summary captured before evaluation.
type DiffInfo struct {
	Baseline  string                 `json:"baseline,omitempty"`
	Files     []workspace.FileStatus `json:"files,omitempty"`
	Changed   bool                   `json:"changed"`
	Truncated bool                   `json:"truncated,omitempty"`
}

// EvaluationInfo is the independent evaluator's verdict.
type EvaluationInfo struct {
	Status     evaluator.Status  `json:"status"`
	Summary    string            `json:"summary"`
	Score      *float64          `json:"score,omitempty"`
	Checks     []evaluator.Check `json:"checks,omitempty"`
	Error      string            `json:"error,omitempty"`
	TimedOut   bool              `json:"timed_out,omitempty"`
	ExitCode   *int              `json:"exit_code,omitempty"`
	DurationMS int64             `json:"duration_ms"`
}

// Checks summarizes evaluator checks.
func (e *EvaluationInfo) ChecksSummary() evaluator.CheckStatuses {
	if e == nil {
		return evaluator.CheckStatuses{}
	}
	r := evaluator.Result{Checks: e.Checks}
	return r.CountChecks()
}
