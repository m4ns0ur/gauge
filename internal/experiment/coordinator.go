// Package experiment owns the coordinator that runs a task selection against
// one agent, sequentially, with fresh workspaces and explicit repetitions.
package experiment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/runner"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/suite"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/version"
)

// Request configures one experiment.
type Request struct {
	Store            *storage.Store
	Runner           *runner.Runner
	Agent            adapter.Adapter
	AgentDescription string
	Config           json.RawMessage
	Model            string
	Suite            *suite.Suite
	Tasks            []*task.Task
	Repetitions      int
	Notes            string
	DataDir          string
	Progress         func(Progress)
}

// Progress reports coordinator milestones to the CLI.
type Progress struct {
	Phase      string
	Task       *task.Task
	Attempt    *storage.Attempt
	Index      int
	Total      int
	Repetition int
}

// Progress phases.
const (
	PhaseAttemptStarted  = "attempt_started"
	PhaseAttemptFinished = "attempt_finished"
)

// NewID builds a filesystem-safe, sortable experiment identifier.
func NewID(prefix, agentID string, at time.Time) string {
	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	safe := sanitize(prefix)
	if safe == "" {
		safe = "tasks"
	}
	return fmt.Sprintf("%s-%s-%s-%s", safe, sanitize(agentID), at.UTC().Format("20060102T150405Z"), hex.EncodeToString(suffix[:]))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// Coordinator runs an experiment sequentially.
type Coordinator struct {
	Request
	now func() time.Time
}

// New validates the request and returns a coordinator.
func New(req Request) (*Coordinator, error) {
	if req.Store == nil || req.Runner == nil || req.Agent == nil {
		return nil, fmt.Errorf("experiment: store, runner, and agent are required")
	}
	if len(req.Tasks) == 0 {
		return nil, fmt.Errorf("experiment: no tasks selected")
	}
	if req.Repetitions < 1 {
		return nil, fmt.Errorf("experiment: repetitions must be at least 1")
	}
	if req.Repetitions > 100 {
		return nil, fmt.Errorf("experiment: repetitions must be at most 100")
	}
	return &Coordinator{Request: req, now: time.Now}, nil
}

// Run executes all attempts and returns the persisted experiment.
func (c *Coordinator) Run(ctx context.Context) (*storage.Experiment, []*storage.Attempt, error) {
	if err := c.Agent.Validate(ctx, c.Config); err != nil {
		return nil, nil, fmt.Errorf("agent %q configuration is invalid: %w", c.Agent.ID(), err)
	}
	exp, err := c.buildExperiment()
	if err != nil {
		return nil, nil, err
	}
	if err := c.Store.CreateExperiment(exp); err != nil {
		return nil, nil, err
	}

	var attempts []*storage.Attempt
	total := len(c.Tasks) * c.Repetitions
	index := 0
	interrupted := false

	for _, t := range c.Tasks {
		if ctx.Err() != nil {
			interrupted = true
			break
		}
		for rep := 0; rep < c.Repetitions; rep++ {
			if ctx.Err() != nil {
				interrupted = true
				break
			}
			index++
			if c.Progress != nil {
				c.Progress(Progress{Phase: PhaseAttemptStarted, Task: t, Index: index, Total: total, Repetition: rep})
			}
			attempt, runErr := c.Runner.Run(ctx, runner.Request{
				Experiment: exp,
				Task:       t,
				Repetition: rep,
				Adapter:    c.Agent,
				Config:     c.Config,
				Model:      c.Model,
			})
			if attempt != nil {
				attempts = append(attempts, attempt)
				c.absorbAgentInfo(exp, attempt)
			}
			if c.Progress != nil && attempt != nil {
				c.Progress(Progress{Phase: PhaseAttemptFinished, Task: t, Attempt: attempt, Index: index, Total: total, Repetition: rep})
			}
			// Persist progress after every attempt so an interruption leaves a
			// truthful experiment record.
			exp.Status = storage.StatusRunning
			if saveErr := c.Store.SaveExperiment(exp); saveErr != nil && runErr == nil {
				runErr = saveErr
			}
			if runErr != nil {
				exp.Status = storage.StatusFailed
				c.finishExperiment(exp)
				return exp, attempts, runErr
			}
			if ctx.Err() != nil {
				interrupted = true
				break
			}
		}
		if interrupted {
			break
		}
	}

	if interrupted {
		exp.Status = storage.StatusInterrupted
	} else {
		exp.Status = storage.StatusCompleted
	}
	c.finishExperiment(exp)
	if err := c.Store.SaveExperiment(exp); err != nil {
		return exp, attempts, err
	}
	if interrupted {
		return exp, attempts, context.Canceled
	}
	return exp, attempts, nil
}

func (c *Coordinator) finishExperiment(exp *storage.Experiment) {
	now := c.now().UTC()
	exp.EndedAt = &now
}

func (c *Coordinator) absorbAgentInfo(exp *storage.Experiment, attempt *storage.Attempt) {
	if exp.Agent.Version == "" && attempt.AgentVersion != "" {
		exp.Agent.Version = attempt.AgentVersion
	}
	if exp.Agent.Model == "" && attempt.Model != "" {
		exp.Agent.Model = attempt.Model
	}
	if exp.Agent.Provider == "" && attempt.Provider != "" {
		exp.Agent.Provider = attempt.Provider
	}
}

func (c *Coordinator) buildExperiment() (*storage.Experiment, error) {
	now := c.now().UTC()
	redacted, changed := storage.RedactJSON(c.Config)
	prefix := "tasks"
	var suiteInfo *storage.SuiteInfo
	if c.Suite != nil {
		prefix = c.Suite.ID
		suiteInfo = &storage.SuiteInfo{
			ID:          c.Suite.ID,
			Name:        c.Suite.Name,
			Fingerprint: c.Suite.Fingerprint(),
			File:        c.Suite.File,
			Tasks:       make([]string, 0, len(c.Suite.Tasks)),
		}
		for _, ref := range c.Suite.Tasks {
			suiteInfo.Tasks = append(suiteInfo.Tasks, ref.ID)
		}
	}

	selection := make([]storage.TaskSelection, 0, len(c.Tasks))
	for _, t := range c.Tasks {
		fp, err := t.Fingerprint()
		if err != nil {
			return nil, fmt.Errorf("experiment: fingerprint task %q: %w", t.ID, err)
		}
		selection = append(selection, storage.TaskSelection{
			ID:          t.ID,
			Category:    t.Category,
			Fingerprint: fp,
			Fixture:     t.Fixture.Revision,
		})
	}

	exp := &storage.Experiment{
		SchemaVersion: storage.SchemaVersion,
		ID:            NewID(prefix, c.Agent.ID(), now),
		Status:        storage.StatusRunning,
		CreatedAt:     now,
		StartedAt:     now,
		Gauge: storage.GaugeInfo{
			Version: version.Version,
			Commit:  version.Commit,
		},
		Agent: storage.AgentInfo{
			ID:                c.Agent.ID(),
			Description:       c.AgentDescription,
			Model:             c.Model,
			Config:            redacted,
			ConfigFingerprint: storage.FingerprintJSON(redacted),
			ConfigRedacted:    changed,
		},
		Suite:       suiteInfo,
		Selection:   dedupeSelections(selection),
		Repetitions: c.Repetitions,
		Environment: storage.EnvironmentInfo{
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			GoVersion:  runtime.Version(),
			Backend:    "local",
			DataDir:    c.DataDir,
			GitVersion: gitVersion(),
		},
		Notes: c.Notes,
	}
	return exp, nil
}

func dedupeSelections(in []storage.TaskSelection) []storage.TaskSelection {
	seen := make(map[string]struct{}, len(in))
	out := make([]storage.TaskSelection, 0, len(in))
	for _, sel := range in {
		if _, ok := seen[sel.ID]; ok {
			continue
		}
		seen[sel.ID] = struct{}{}
		out = append(out, sel)
	}
	return out
}

func gitVersion() string {
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
