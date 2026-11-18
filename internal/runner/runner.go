// Package runner executes one benchmark attempt.
//
// The runner owns the lifecycle around an agent: it creates a fresh workspace
// from the pinned fixture, runs declared setup steps, invokes the adapter,
// captures the candidate diff, and only then runs the independent evaluator.
// Adapters never see evaluator assets or decide whether a task passed.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/evaluator"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/workspace"
)

// Runner executes attempts for one experiment.
type Runner struct {
	Store     *storage.Store
	Workspace *workspace.Manager
	Retention Retention
	// Now overrides the clock in tests. It must be safe for concurrent use.
	Now func() time.Time
}

// Request describes one attempt.
type Request struct {
	Experiment *storage.Experiment
	Task       *task.Task
	Repetition int
	Adapter    adapter.Adapter
	// Config is validated adapter configuration as JSON.
	Config json.RawMessage
	// Model optionally overrides the adapter's configured model.
	Model string
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// NewAttemptID returns a unique, filesystem-safe attempt identifier.
func NewAttemptID(taskID string, repetition int, at time.Time) string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// Fall back to a timestamp-derived suffix; uniqueness is still
		// effectively guaranteed by the attempt directory checks.
		copy(suffix[:], fmt.Sprintf("%08x", at.UnixNano()&0xffffffff))
	}
	return fmt.Sprintf("%s-r%02d-%s-%s", taskID, repetition+1, at.Format("20060102T150405Z"), hex.EncodeToString(suffix[:]))
}

// Run executes one attempt. It returns an attempt record for any outcome that
// could be persisted; a returned error means Gauge could not record the
// attempt at all.
func (r *Runner) Run(ctx context.Context, req Request) (*storage.Attempt, error) {
	if req.Experiment == nil || req.Task == nil || req.Adapter == nil {
		return nil, fmt.Errorf("runner: experiment, task, and adapter are required")
	}
	if req.Repetition < 0 {
		return nil, fmt.Errorf("runner: repetition must be >= 0")
	}
	startedAt := r.now()
	attemptID := NewAttemptID(req.Task.ID, req.Repetition, startedAt)
	attemptDir, err := r.Store.CreateAttemptDir(req.Experiment.ID, attemptID)
	if err != nil {
		return nil, err
	}

	eventsFile, err := os.Create(filepath.Join(attemptDir, storage.ArtifactEvents))
	if err != nil {
		return nil, fmt.Errorf("runner: create event log: %w", err)
	}
	recorder := events.NewRecorder(eventsFile, "gauge")
	emitGauge := func(state string) {
		recorder.Emit(events.Event{Type: events.TypeLifecycle, Source: "gauge", State: state})
	}
	emitAdapter := func(ev events.Event) {
		if ev.Source == "" {
			ev.Source = req.Adapter.ID()
		}
		recorder.Emit(ev)
	}

	attempt := &storage.Attempt{
		SchemaVersion:   storage.SchemaVersion,
		ID:              attemptID,
		ExperimentID:    req.Experiment.ID,
		TaskID:          req.Task.ID,
		TaskName:        req.Task.Name,
		TaskCategory:    req.Task.Category,
		TaskFingerprint: taskFingerprint(req.Task),
		Repetition:      req.Repetition,
		AgentID:         req.Adapter.ID(),
		StartedAt:       startedAt,
		Outcome:         storage.OutcomeNotEvaluated,
		Artifacts:       map[string]storage.Artifact{},
	}

	finish := func(runErr string) (*storage.Attempt, error) {
		attempt.EndedAt = r.now()
		attempt.DurationMS = attempt.EndedAt.Sub(attempt.StartedAt).Milliseconds()
		if runErr != "" {
			attempt.Error = runErr
		}
		emitGauge("attempt_finished")
		closeErr := recorder.Close()
		artifacts, artifactErr := storage.CollectArtifacts(attemptDir,
			storage.ArtifactEvents,
			storage.ArtifactStdout,
			storage.ArtifactStderr,
			storage.ArtifactDiff,
			storage.ArtifactGitStatus,
			storage.ArtifactEvaluation,
			storage.ArtifactEvaluatorStdout,
			storage.ArtifactEvaluatorStderr,
		)
		for name, artifact := range artifacts {
			attempt.Artifacts[name] = artifact
		}
		if extra, err := collectAdapterArtifacts(attemptDir); err == nil {
			for name, artifact := range extra {
				attempt.Artifacts[name] = artifact
			}
		}
		if extra, err := collectSetupArtifacts(attemptDir); err == nil {
			for name, artifact := range extra {
				attempt.Artifacts[name] = artifact
			}
		}
		if closeErr != nil {
			attempt.Error = appendError(attempt.Error, fmt.Sprintf("record events: %v", closeErr))
		}
		if artifactErr != nil {
			attempt.Error = appendError(attempt.Error, fmt.Sprintf("hash artifacts: %v", artifactErr))
		}
		if err := r.Store.SaveAttempt(req.Experiment.ID, attempt); err != nil {
			return attempt, fmt.Errorf("runner: save attempt: %w", err)
		}
		return attempt, nil
	}

	// 1. Fresh workspace from the pinned fixture revision.
	emitGauge("workspace_preparing")
	wsInfo, err := r.Workspace.Prepare(ctx, workspace.PrepareRequest{
		FixtureDir: req.Task.FixtureDir,
		Revision:   req.Task.Fixture.Revision,
		Dest:       filepath.Join(attemptDir, "workspace"),
	})
	if err != nil {
		attempt.Execution = storage.ExecutionInfo{
			Completion: adapter.Failed,
			Error:      err.Error(),
		}
		emitGauge("workspace_failed")
		return finish(err.Error())
	}
	wsPath := wsInfo.Path

	// 2. Setup commands run in the fresh workspace, before the agent.
	setup := workspace.RunSetup(ctx, wsPath, attemptDir, req.Task.Execution.Setup, req.Task.Execution.Timeout.Duration())
	if len(req.Task.Execution.Setup) > 0 {
		attempt.Setup = &storage.SetupInfo{
			Ran:       true,
			Steps:     len(setup.Steps),
			Error:     setup.Err,
			TimedOut:  setup.TimedOut,
			Cancelled: setup.Cancelled,
		}
	}
	if setup.Err != "" {
		attempt.Execution = storage.ExecutionInfo{
			Completion: adapter.Failed,
			Error:      setup.Err,
			TimedOut:   setup.TimedOut,
			Cancelled:  setup.Cancelled,
		}
		emitGauge("setup_failed")
		return finish(setup.Err)
	}
	emitGauge("workspace_ready")

	// 3. Agent execution under its own deadline.
	agentCtx, cancelAgent := context.WithTimeout(ctx, req.Task.Execution.Timeout.Duration())
	emitGauge("agent_starting")
	runResult, adapterErr := req.Adapter.Run(agentCtx, adapter.RunRequest{
		RunID:        attemptID,
		TaskID:       req.Task.ID,
		Prompt:       req.Task.Prompt,
		WorkDir:      wsPath,
		ArtifactsDir: attemptDir,
		Timeout:      req.Task.Execution.Timeout.Duration(),
		Config:       req.Config,
		Model:        req.Model,
		Environment:  agentEnvironment(attemptDir, wsPath, req.Task.ID),
	}, emitAdapter)
	cancelAgent()
	agentTimedOut := errors.Is(agentCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	agentCancelled := agentCtx.Err() != nil && ctx.Err() != nil

	attempt.Execution = executionInfo(runResult, adapterErr, agentTimedOut, agentCancelled)
	attempt.AgentVersion = runResult.AgentVersion
	attempt.Model = runResult.Model
	attempt.Provider = runResult.Provider
	attempt.Usage = runResult.Usage
	attempt.AdapterMetadata = runResult.Metadata
	emitGauge("agent_finished")

	// 4. Capture the candidate diff before evaluation touches anything.
	diff, diffErr := r.Workspace.CaptureDiff(context.WithoutCancel(ctx), wsPath, attemptDir)
	if diffErr != nil {
		attempt.Error = appendError(attempt.Error, fmt.Sprintf("capture diff: %v", diffErr))
		recorder.Emit(events.Event{Type: events.TypeWarning, Source: "gauge", Error: diffErr.Error()})
	} else {
		attempt.Diff = storage.DiffInfo{
			Baseline:  diff.Baseline,
			Files:     diff.Files,
			Changed:   diff.HasChanges(),
			Truncated: diff.Truncated,
		}
		if len(diff.Patch) > 0 {
			if err := os.WriteFile(filepath.Join(attemptDir, storage.ArtifactDiff), diff.Patch, 0o644); err != nil {
				attempt.Error = appendError(attempt.Error, fmt.Sprintf("write diff: %v", err))
			}
		}
	}
	if err := os.WriteFile(filepath.Join(attemptDir, storage.ArtifactGitStatus), []byte(diff.Status), 0o644); err != nil {
		attempt.Error = appendError(attempt.Error, fmt.Sprintf("write git status: %v", err))
	}

	// 5. Independent evaluation, outside the agent process.
	emitGauge("evaluation_starting")
	evalOutcome := evaluator.Run(ctx, evaluator.Request{
		AttemptID:  attemptID,
		Task:       req.Task,
		Workspace:  wsPath,
		ScratchDir: filepath.Join(attemptDir, "evaluation-scratch"),
		Timeout:    req.Task.Evaluation.Timeout.Duration(),
	})
	applyEvaluation(attempt, evalOutcome)
	writeEvaluationArtifacts(attemptDir, evalOutcome)
	emitGauge("evaluation_finished")

	// 6. Retention. Logs, events, diffs, and evaluation output outlive the
	// workspace. Tool caches and the evaluator scratch directory are removed
	// unless the policy explicitly preserves everything.
	if r.Retention != RetainKeepAll {
		if err := workspace.CleanScratch(attemptDir); err != nil {
			attempt.Error = appendError(attempt.Error, fmt.Sprintf("clean scratch: %v", err))
		}
	}
	keep := r.Retention.KeepWorkspace(attempt.Outcome == storage.OutcomePassed)
	attempt.WorkspaceKept = keep
	if !keep {
		if err := workspace.Remove(attemptDir); err != nil {
			attempt.Error = appendError(attempt.Error, fmt.Sprintf("remove workspace: %v", err))
		} else {
			recorder.Emit(events.Event{Type: events.TypeLifecycle, Source: "gauge", State: "workspace_removed"})
		}
	}
	if evalOutcome.Err != nil && attempt.Error == "" {
		attempt.Error = evalOutcome.Err.Error()
	}
	return finish(attempt.Error)
}

func executionInfo(res adapter.RunResult, adapterErr error, timedOut, cancelled bool) storage.ExecutionInfo {
	info := storage.ExecutionInfo{
		Completion: res.Completion,
		ExitCode:   res.ExitCode,
		Signal:     res.Signal,
		Error:      res.Error,
		TimedOut:   timedOut || res.Completion == adapter.TimedOut,
		Cancelled:  cancelled || res.Completion == adapter.Cancelled,
	}
	if adapterErr != nil {
		info.AdapterError = adapterErr.Error()
	}
	if info.Completion == "" {
		switch {
		case info.TimedOut:
			info.Completion = adapter.TimedOut
		case info.Cancelled:
			info.Completion = adapter.Cancelled
		default:
			info.Completion = adapter.Failed
		}
	}
	if !info.Completion.Valid() {
		info.Completion = adapter.Failed
	}
	return info
}

func applyEvaluation(attempt *storage.Attempt, outcome evaluator.Outcome) {
	duration := outcome.Duration().Milliseconds()
	if outcome.Result != nil {
		attempt.Evaluation = &storage.EvaluationInfo{
			Status:     outcome.Result.Status,
			Summary:    outcome.Result.Summary,
			Score:      outcome.Result.Score,
			Checks:     outcome.Result.Checks,
			ExitCode:   outcome.ExitCode,
			DurationMS: duration,
		}
		switch outcome.Result.Status {
		case evaluator.Passed:
			attempt.Outcome = storage.OutcomePassed
		case evaluator.Failed:
			attempt.Outcome = storage.OutcomeFailed
		default:
			attempt.Outcome = storage.OutcomeError
			attempt.Evaluation.Error = outcome.Result.Summary
		}
		return
	}
	attempt.Outcome = storage.OutcomeError
	attempt.Evaluation = &storage.EvaluationInfo{
		Status:     evaluator.Error,
		Summary:    "Evaluator did not produce a valid result.",
		Error:      errorText(outcome.Err),
		TimedOut:   outcome.TimedOut,
		ExitCode:   outcome.ExitCode,
		DurationMS: duration,
	}
}

func writeEvaluationArtifacts(dir string, outcome evaluator.Outcome) {
	if outcome.Result != nil {
		if data, err := json.MarshalIndent(outcome.Result, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(dir, storage.ArtifactEvaluation), append(data, '\n'), 0o644)
		}
	}
	if outcome.Stdout != "" {
		_ = os.WriteFile(filepath.Join(dir, storage.ArtifactEvaluatorStdout), []byte(outcome.Stdout), 0o644)
	}
	if outcome.Stderr != "" {
		_ = os.WriteFile(filepath.Join(dir, storage.ArtifactEvaluatorStderr), []byte(outcome.Stderr), 0o644)
	}
}

func collectAdapterArtifacts(attemptDir string) (map[string]storage.Artifact, error) {
	entries, err := os.ReadDir(attemptDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), storage.ArtifactAdapterRawPrefix) {
			names = append(names, entry.Name())
		}
	}
	return storage.CollectArtifacts(attemptDir, names...)
}

func collectSetupArtifacts(attemptDir string) (map[string]storage.Artifact, error) {
	entries, err := os.ReadDir(attemptDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || !isSetupStepDir(entry.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(attemptDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			names = append(names, entry.Name()+"/"+file.Name())
		}
	}
	return storage.CollectArtifacts(attemptDir, names...)
}

// isSetupStepDir matches "setup-01" while excluding setup-home and setup-tmp,
// whose contents are tool caches rather than recorded artifacts.
func isSetupStepDir(name string) bool {
	const prefix = "setup-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := name[len(prefix):]
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func agentEnvironment(attemptDir, wsPath, taskID string) map[string]string {
	home := filepath.Join(attemptDir, "agent-home")
	tmp := filepath.Join(attemptDir, "agent-tmp")
	_ = os.MkdirAll(home, 0o755)
	_ = os.MkdirAll(tmp, 0o755)
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	return map[string]string{
		"PATH":                 path,
		"HOME":                 home,
		"TMPDIR":               tmp,
		"LANG":                 "C.UTF-8",
		"LC_ALL":               "C.UTF-8",
		"GAUGE_WORKSPACE":      wsPath,
		"GAUGE_TASK_ID":        taskID,
		"GAUGE_SCHEMA_VERSION": "1",
	}
}

func taskFingerprint(t *task.Task) string {
	fp, err := t.Fingerprint()
	if err != nil {
		return ""
	}
	return fp
}

func appendError(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
