// Package fake provides a deterministic adapter used to test the runner,
// evaluator lifecycle, storage, and reports without a model or network.
//
// It is a built-in adapter so integration tests can exercise the same code
// path as a real agent: the CLI loads it by ID and passes JSON configuration.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
)

// ID is the registry identifier for the fake adapter.
const ID = "fake"

// Mode selects the fake adapter's behavior.
type Mode string

const (
	// ModeNoop emits configured events and leaves the workspace untouched.
	ModeNoop Mode = "noop"
	// ModeWrite writes the configured files into the workspace.
	ModeWrite Mode = "write"
	// ModeFail exits unsuccessfully after emitting configured events.
	ModeFail Mode = "fail"
	// ModeSleep waits for the configured duration before finishing.
	ModeSleep Mode = "sleep"
)

// Event is a canned event the fake adapter emits.
type Event struct {
	Type  events.Type     `json:"type"`
	Text  string          `json:"text,omitempty"`
	State string          `json:"state,omitempty"`
	Tool  *events.Tool    `json:"tool,omitempty"`
	Usage *events.Usage   `json:"usage,omitempty"`
	Error string          `json:"error,omitempty"`
	Raw   json.RawMessage `json:"raw,omitempty"`
}

// Config is the JSON configuration accepted by the fake adapter.
type Config struct {
	Mode         Mode              `json:"mode,omitempty"`
	Files        map[string]string `json:"files,omitempty"`
	Events       []Event           `json:"events,omitempty"`
	Sleep        string            `json:"sleep,omitempty"`
	ExitCode     int               `json:"exit_code,omitempty"`
	ErrorMessage string            `json:"error,omitempty"`
	AgentVersion string            `json:"agent_version,omitempty"`
	Model        string            `json:"model,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	Usage        *events.Usage     `json:"usage,omitempty"`
	Metadata     json.RawMessage   `json:"metadata,omitempty"`
}

// Adapter implements adapter.Adapter.
type Adapter struct{}

// New returns the fake adapter.
func New() *Adapter { return &Adapter{} }

// ID implements adapter.Adapter.
func (a *Adapter) ID() string { return ID }

// Description implements adapter.Describer.
func (a *Adapter) Description() string {
	return "deterministic test agent (no model, no network)"
}

// Validate implements adapter.Adapter.
func (a *Adapter) Validate(_ context.Context, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	cfg, err := decode(raw)
	if err != nil {
		return err
	}
	return validate(cfg)
}

func decode(raw json.RawMessage) (Config, error) {
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("fake adapter config: %w", err)
	}
	return cfg, nil
}

func validate(cfg Config) error {
	if cfg.Mode == "" {
		cfg.Mode = ModeNoop
	}
	switch cfg.Mode {
	case ModeNoop, ModeWrite, ModeFail, ModeSleep:
	default:
		return fmt.Errorf("fake adapter config: unknown mode %q (want noop, write, fail, or sleep)", cfg.Mode)
	}
	if cfg.Sleep != "" {
		d, err := time.ParseDuration(cfg.Sleep)
		if err != nil {
			return fmt.Errorf("fake adapter config: invalid sleep %q: %w", cfg.Sleep, err)
		}
		if d < 0 {
			return fmt.Errorf("fake adapter config: sleep must not be negative")
		}
	}
	if cfg.ExitCode < 0 || cfg.ExitCode > 255 {
		return fmt.Errorf("fake adapter config: exit_code %d outside 0..255", cfg.ExitCode)
	}
	names := make([]string, 0, len(cfg.Files))
	for name := range cfg.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := checkRelPath(name); err != nil {
			return fmt.Errorf("fake adapter config: file %q: %w", name, err)
		}
	}
	for i, ev := range cfg.Events {
		if !ev.Type.Valid() {
			return fmt.Errorf("fake adapter config: events[%d]: invalid type %q", i, ev.Type)
		}
	}
	return nil
}

func checkRelPath(name string) error {
	if name == "" {
		return fmt.Errorf("empty path")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return fmt.Errorf("must be relative to the workspace")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("escapes the workspace")
	}
	return nil
}

// Run implements adapter.Adapter.
func (a *Adapter) Run(ctx context.Context, req adapter.RunRequest, emit func(events.Event)) (adapter.RunResult, error) {
	started := time.Now().UTC()
	result := adapter.RunResult{
		StartedAt:    started,
		AgentVersion: "fake-0.0.0",
		Provider:     "fake",
	}

	cfg := Config{Mode: ModeNoop}
	if len(req.Config) > 0 && string(req.Config) != "null" {
		parsed, err := decode(req.Config)
		if err != nil {
			return failedResult(result, 2, err), nil
		}
		if err := validate(parsed); err != nil {
			return failedResult(result, 2, err), nil
		}
		cfg = parsed
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeNoop
	}
	if cfg.AgentVersion != "" {
		result.AgentVersion = cfg.AgentVersion
	}
	result.Model = cfg.Model
	if req.Model != "" {
		result.Model = req.Model
	}
	if cfg.Provider != "" {
		result.Provider = cfg.Provider
	}
	result.Usage = cfg.Usage
	if len(cfg.Metadata) > 0 {
		result.Metadata = cfg.Metadata
	}

	emit(events.Event{Type: events.TypeLifecycle, State: "agent_started"})

	var stdout strings.Builder
	for _, ev := range cfg.Events {
		out := events.Event{
			Type:  ev.Type,
			Text:  ev.Text,
			State: ev.State,
			Tool:  ev.Tool,
			Usage: ev.Usage,
			Error: ev.Error,
			Raw:   ev.Raw,
		}
		emit(out)
		if ev.Type == events.TypeAgentMessage {
			stdout.WriteString(ev.Text)
			stdout.WriteString("\n")
		}
	}

	if cfg.Sleep != "" {
		d, err := time.ParseDuration(cfg.Sleep)
		if err != nil {
			return failedResult(result, 2, err), nil
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			emit(events.Event{Type: events.TypeLifecycle, State: "agent_stopped", Error: ctx.Err().Error()})
			return interruptedResult(result, ctx.Err()), nil
		case <-timer.C:
		}
	}

	if cfg.Mode == ModeFail {
		msg := cfg.ErrorMessage
		if msg == "" {
			msg = "fake adapter configured to fail"
		}
		emit(events.Event{Type: events.TypeError, Error: msg})
		code := cfg.ExitCode
		if code == 0 {
			code = 1
		}
		result.Completion = adapter.Failed
		result.ExitCode = &code
		result.Error = msg
		result.EndedAt = time.Now().UTC()
		result.Duration = result.EndedAt.Sub(result.StartedAt)
		return result, nil
	}

	applied := make([]string, 0, len(cfg.Files))
	names := make([]string, 0, len(cfg.Files))
	for name := range cfg.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return interruptedResult(result, err), nil
		}
		if err := writeFile(req.WorkDir, name, cfg.Files[name]); err != nil {
			emit(events.Event{Type: events.TypeError, Error: err.Error()})
			return failedResult(result, 1, err), nil
		}
		applied = append(applied, name)
	}
	if cfg.Mode == ModeWrite || len(cfg.Files) > 0 {
		result.Metadata = mergeMetadata(result.Metadata, map[string]any{"applied_files": applied})
	}

	if len(stdout.String()) > 0 && req.ArtifactsDir != "" {
		_ = os.WriteFile(filepath.Join(req.ArtifactsDir, "stdout.log"), []byte(stdout.String()), 0o644)
	}

	emit(events.Event{Type: events.TypeLifecycle, State: "agent_finished"})
	result.Completion = adapter.Completed
	code := 0
	result.ExitCode = &code
	if cfg.ExitCode != 0 {
		result.Completion = adapter.Failed
		result.ExitCode = &cfg.ExitCode
		result.Error = fmt.Sprintf("fake adapter configured exit code %d", cfg.ExitCode)
	}
	result.EndedAt = time.Now().UTC()
	result.Duration = result.EndedAt.Sub(result.StartedAt)
	return result, nil
}

func writeFile(root, name, content string) error {
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("fake adapter: create parent of %s: %w", name, err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return fmt.Errorf("fake adapter: write %s: %w", name, err)
	}
	return nil
}

func mergeMetadata(existing json.RawMessage, extra map[string]any) json.RawMessage {
	merged := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &merged); err != nil {
			merged = map[string]any{"adapter_metadata": string(existing)}
		}
	}
	for k, v := range extra {
		merged[k] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return existing
	}
	return out
}

func failedResult(r adapter.RunResult, code int, err error) adapter.RunResult {
	r.Completion = adapter.Failed
	r.ExitCode = &code
	r.Error = err.Error()
	r.EndedAt = time.Now().UTC()
	r.Duration = r.EndedAt.Sub(r.StartedAt)
	return r
}

func interruptedResult(r adapter.RunResult, ctxErr error) adapter.RunResult {
	r.EndedAt = time.Now().UTC()
	r.Duration = r.EndedAt.Sub(r.StartedAt)
	if ctxErr == context.DeadlineExceeded {
		r.Completion = adapter.TimedOut
		r.Error = "attempt deadline exceeded"
		return r
	}
	r.Completion = adapter.Cancelled
	r.Error = "run cancelled"
	return r
}
