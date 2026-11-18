// Package adapter defines Gauge's agent-neutral adapter contract.
//
// An adapter invokes exactly one agent for exactly one benchmark attempt. It
// receives the task prompt and a working directory, streams observable events,
// and returns a process-level outcome. An adapter never evaluates correctness
// and never reports whether the task passed.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/m4ns0ur/gauge/internal/events"
)

// Compatibility identifies how an adapter is configured. It is recorded with
// experiment metadata so runs remain interpretable later.
type Compatibility struct {
	ID          string
	Description string
}

// Adapter drives one agent through one benchmark attempt.
type Adapter interface {
	// ID is the stable identifier used by --agent and configuration keys.
	ID() string
	// Validate reports whether config is acceptable before any run starts.
	Validate(ctx context.Context, config json.RawMessage) error
	// Run executes the agent. It must honor ctx cancellation, must not
	// evaluate the task, and must return a RunResult even when execution fails.
	Run(ctx context.Context, request RunRequest, emit func(events.Event)) (RunResult, error)
}

// Describer is an optional interface for human-readable adapter metadata.
type Describer interface {
	Description() string
}

// RunRequest is everything an adapter may use for one attempt.
//
// It deliberately contains no evaluator assets, hidden tests, or expected
// outputs. Prompt is the only task instruction the agent may see.
type RunRequest struct {
	// RunID uniquely identifies this attempt.
	RunID string
	// TaskID identifies the benchmark task, for logging and telemetry only.
	TaskID string
	// Prompt is the task prompt text.
	Prompt string
	// WorkDir is the absolute path of the prepared candidate workspace.
	WorkDir string
	// ArtifactsDir is an absolute path where the adapter may write raw
	// process output (for example stdout.log and stderr.log).
	ArtifactsDir string
	// Timeout is the per-attempt budget. The adapter should also honor the
	// context deadline, which the runner sets to the same value.
	Timeout time.Duration
	// Config is validated adapter-specific configuration as JSON.
	Config json.RawMessage
	// Model is an optional model override, applied only when the adapter can
	// honor it. Empty means "use the adapter's own configuration".
	Model string
	// Environment is the controlled environment for the agent process.
	// Secret values are redacted before being persisted.
	Environment map[string]string
}

// Completion describes how an agent process ended. It says nothing about
// whether the task passed.
type Completion string

const (
	// Completed means the agent process finished on its own.
	Completed Completion = "completed"
	// Failed means the process exited with an error or could not be started.
	Failed Completion = "failed"
	// TimedOut means the attempt deadline expired.
	TimedOut Completion = "timed_out"
	// Cancelled means the run was cancelled by the user or runner.
	Cancelled Completion = "cancelled"
)

// Valid reports whether c is a known completion reason.
func (c Completion) Valid() bool {
	switch c {
	case Completed, Failed, TimedOut, Cancelled:
		return true
	}
	return false
}

// RunResult describes execution, not correctness.
type RunResult struct {
	Completion   Completion
	ExitCode     *int
	Signal       string
	StartedAt    time.Time
	EndedAt      time.Time
	Duration     time.Duration
	AgentVersion string
	Model        string
	Provider     string
	Usage        *events.Usage
	Metadata     json.RawMessage
	Error        string
}

// Validate checks the structural invariants of a result.
func (r RunResult) Validate() error {
	if !r.Completion.Valid() {
		return fmt.Errorf("adapter: invalid completion reason %q", r.Completion)
	}
	if !r.StartedAt.IsZero() && !r.EndedAt.IsZero() && r.EndedAt.Before(r.StartedAt) {
		return fmt.Errorf("adapter: EndAt %s precedes StartAt %s", r.EndedAt, r.StartedAt)
	}
	return nil
}

// Info is the public description of a registered adapter.
type Info struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Registry holds the built-in adapters for one Gauge process.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
	order    []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register adds an adapter. It panics on an empty or duplicate ID because
// registration happens at startup and indicates a programming error.
func (r *Registry) Register(a Adapter) {
	if a == nil {
		panic("adapter: cannot register nil adapter")
	}
	id := a.ID()
	if id == "" {
		panic("adapter: cannot register adapter with empty ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[id]; exists {
		panic(fmt.Sprintf("adapter: adapter %q already registered", id))
	}
	r.adapters[id] = a
	r.order = append(r.order, id)
}

// Get returns an adapter by ID.
func (r *Registry) Get(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[id]
	return a, ok
}

// List returns registered adapters sorted by ID.
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]Info, 0, len(r.adapters))
	for id, a := range r.adapters {
		desc := ""
		if d, ok := a.(Describer); ok {
			desc = d.Description()
		}
		// Stable ordering makes CLI output and tests deterministic.
		infos = append(infos, Info{ID: id, Description: desc})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}
