// Package pi adapts the Pi coding-agent harness to Gauge's adapter contract.
//
// Pi is driven through its documented one-shot JSON interface:
//
//	pi --mode json --no-session [--provider <p>] [--model <m>] \
//	   [--thinking <level>] [--tools <list>]        # prompt arrives on stdin
//
// Each attempt gets a private agent directory (PI_CODING_AGENT_DIR), so model
// catalogs, credentials, and trust state never leak between attempts or into
// the user's ~/.pi. The JSONL event stream is translated into Gauge's
// normalized events, and raw stdout/stderr are kept as attempt artifacts.
//
// Pi's exit code is not a run-success signal: a failed assistant response still
// exits 0. The adapter derives completion from the final assistant message's
// stop reason and the terminal agent_settled event, never from the process exit
// code alone. It never decides whether the task passed.
package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/proc"
)

// ID is the registry identifier for the Pi adapter.
const ID = "pi"

const (
	defaultCommand = "pi"
	// maxToolInput and maxToolOutput bound what is copied into normalized
	// events. Complete payloads remain in stdout.log.
	maxToolInput  = 4 << 10
	maxToolOutput = 8 << 10
)

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// thinkingLevels are the values Pi accepts for --thinking.
var thinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

// Config is the JSON configuration accepted by the Pi adapter. It comes from
// [adapters.pi.options] in Gauge's TOML configuration.
type Config struct {
	// Command is the Pi executable name or path (default "pi").
	Command string `json:"command,omitempty"`
	// Provider restricts model lookup to one Pi provider (--provider).
	Provider string `json:"provider,omitempty"`
	// Model selects the model (--model). A per-run --model override wins.
	Model string `json:"model,omitempty"`
	// Thinking passes --thinking (off, minimal, low, medium, high, xhigh, max).
	Thinking string `json:"thinking,omitempty"`
	// Tools passes --tools with a comma-joined allowlist.
	Tools []string `json:"tools,omitempty"`
	// ModelsFile is copied to <pi-home>/models.json before the run. It is how
	// custom OpenAI-compatible endpoints, model metadata, and pricing are
	// configured, because Pi has no --base-url flag.
	ModelsFile string `json:"models_file,omitempty"`
	// AuthFile is copied to <pi-home>/auth.json before the run.
	AuthFile string `json:"auth_file,omitempty"`
	// ForwardEnv lists environment variable names copied from Gauge's process
	// into the agent process (for example OPENAI_API_KEY). Only the names are
	// persisted; values never are.
	ForwardEnv []string `json:"forward_env,omitempty"`
}

// Adapter implements adapter.Adapter for Pi.
type Adapter struct{}

// New returns the Pi adapter.
func New() *Adapter { return &Adapter{} }

// ID implements adapter.Adapter.
func (a *Adapter) ID() string { return ID }

// Description implements adapter.Describer.
func (a *Adapter) Description() string {
	return "Pi coding agent over its JSON event-stream subprocess interface"
}

// Validate implements adapter.Adapter. A missing Pi executable is reported as
// adapter.UnavailableError so it is not confused with a configuration mistake.
func (a *Adapter) Validate(_ context.Context, raw json.RawMessage) error {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if _, err := resolveExecutable(cfg.Command); err != nil {
		return &adapter.UnavailableError{Adapter: ID, Err: err}
	}
	return nil
}

func decodeConfig(raw json.RawMessage) (Config, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("pi adapter config: %w", err)
	}
	cfg.Command = strings.TrimSpace(cfg.Command)
	if cfg.Command == "" {
		cfg.Command = defaultCommand
	}
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.Thinking = strings.TrimSpace(cfg.Thinking)
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.Thinking != "" && !thinkingLevels[cfg.Thinking] {
		return fmt.Errorf("pi adapter config: thinking %q must be one of off, minimal, low, medium, high, xhigh, max", cfg.Thinking)
	}
	for i, tool := range cfg.Tools {
		if strings.TrimSpace(tool) == "" || strings.ContainsAny(tool, ", \t") {
			return fmt.Errorf("pi adapter config: tools[%d] %q must be a non-empty tool name or pattern without commas or spaces", i, tool)
		}
	}
	for i, name := range cfg.ForwardEnv {
		if !envNameRE.MatchString(name) {
			return fmt.Errorf("pi adapter config: forward_env[%d] %q is not a valid environment variable name", i, name)
		}
	}
	if _, err := resolveFile(cfg.ModelsFile, "models_file"); err != nil {
		return err
	}
	if _, err := resolveFile(cfg.AuthFile, "auth_file"); err != nil {
		return err
	}
	return nil
}

// resolveFile validates an optional configuration file and returns its absolute
// path. An empty value is not an error.
func resolveFile(value, field string) (string, error) {
	if value == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("pi adapter config: resolve %s %q: %w", field, value, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("pi adapter config: %s %q: %w", field, value, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("pi adapter config: %s %q is a directory", field, value)
	}
	return absolute, nil
}

func resolveExecutable(command string) (string, error) {
	path, err := proc.ResolveExecutable(command)
	if err != nil {
		return "", fmt.Errorf("%w; install Pi or set adapters.%s.options.command to its path", err, ID)
	}
	return path, nil
}

// Run implements adapter.Adapter.
func (a *Adapter) Run(ctx context.Context, req adapter.RunRequest, emit func(events.Event)) (adapter.RunResult, error) {
	notify := emit
	if notify == nil {
		notify = func(events.Event) {}
	}

	result := adapter.RunResult{StartedAt: time.Now().UTC()}

	cfg, err := decodeConfig(req.Config)
	if err != nil {
		return fail(result, err, notify), nil
	}
	if err := validateConfig(cfg); err != nil {
		return fail(result, err, notify), nil
	}
	command, err := resolveExecutable(cfg.Command)
	if err != nil {
		return fail(result, err, notify), nil
	}
	modelsFile, err := resolveFile(cfg.ModelsFile, "models_file")
	if err != nil {
		return fail(result, err, notify), nil
	}
	authFile, err := resolveFile(cfg.AuthFile, "auth_file")
	if err != nil {
		return fail(result, err, notify), nil
	}

	model := strings.TrimSpace(cfg.Model)
	if override := strings.TrimSpace(req.Model); override != "" {
		model = override
	}
	if cfg.Provider != "" && model == "" {
		return fail(result, fmt.Errorf("pi adapter config: a provider requires a model; set model or pass --model"), notify), nil
	}

	home, cleanupHome, err := makeHome(req.ArtifactsDir)
	if err != nil {
		return fail(result, err, notify), nil
	}
	defer cleanupHome()

	modelsHash := ""
	if modelsFile != "" {
		modelsHash, err = copyFile(modelsFile, filepath.Join(home, "models.json"))
		if err != nil {
			return fail(result, fmt.Errorf("pi: install models.json: %w", err), notify), nil
		}
	}
	authInstalled := false
	if authFile != "" {
		if _, err := copyFile(authFile, filepath.Join(home, "auth.json")); err != nil {
			return fail(result, fmt.Errorf("pi: install auth.json: %w", err), notify), nil
		}
		authInstalled = true
	}

	rawOut, stdoutLog, _, err := createArtifact(req.ArtifactsDir, "stdout.log")
	if err != nil {
		return fail(result, err, notify), nil
	}
	defer closeFile(stdoutLog)

	stderrTarget, stderrLog, stderrPath, err := createArtifact(req.ArtifactsDir, "stderr.log")
	if err != nil {
		return fail(result, err, notify), nil
	}
	defer closeFile(stderrLog)

	environment := buildEnvironment(req.Environment, home, cfg.ForwardEnv)
	result.AgentVersion = probeVersion(ctx, command, environment)

	args := []string{"--mode", "json", "--no-session"}
	if cfg.Provider != "" {
		args = append(args, "--provider", cfg.Provider)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if cfg.Thinking != "" {
		args = append(args, "--thinking", cfg.Thinking)
	}
	if len(cfg.Tools) > 0 {
		args = append(args, "--tools", strings.Join(cfg.Tools, ","))
	}

	cmd := exec.CommandContext(ctx, command, args...)
	proc.Configure(cmd)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	cmd.Env = environment
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Stderr = stderrTarget

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(result, fmt.Errorf("pi: open stdout pipe: %w", err), notify), nil
	}
	if err := cmd.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return interrupted(result, ctxErr, notify), nil
		}
		return fail(result, fmt.Errorf("pi: start %s: %w", command, err), notify), nil
	}
	notify(events.Event{Type: events.TypeLifecycle, State: "agent_started"})

	state := newStreamState(notify)
	parseErr := state.parse(stdout, rawOut)
	if parseErr != nil && ctx.Err() == nil && cmd.Cancel != nil {
		// The reader stopped before EOF. Terminate the agent so Wait cannot
		// block on a pipe that nobody is draining.
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()

	result.EndedAt = time.Now().UTC()
	result.Duration = result.EndedAt.Sub(result.StartedAt)

	if state.model != "" {
		result.Model = state.model
	} else if model != "" {
		result.Model = model
	}
	if state.provider != "" {
		result.Provider = state.provider
	} else if cfg.Provider != "" {
		result.Provider = cfg.Provider
	}
	result.Usage = state.runUsage()
	meta := state.metadata(command, cfg)
	meta.ModelsFileSHA256 = modelsHash
	meta.AuthFileInstalled = authInstalled
	if data, marshalErr := json.Marshal(meta); marshalErr == nil {
		result.Metadata = data
	}
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			result.ExitCode = &code
		}
	}

	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Completion = adapter.TimedOut
	case ctx.Err() != nil:
		result.Completion = adapter.Cancelled
	case waitErr == nil && parseErr == nil && state.succeeded():
		result.Completion = adapter.Completed
	default:
		result.Completion = adapter.Failed
	}

	if result.Completion == adapter.Completed {
		if state.truncated() {
			notify(events.Event{
				Type:  events.TypeWarning,
				Error: "pi: the final response hit the model output limit; the run is recorded as truncated",
			})
		}
		notify(events.Event{Type: events.TypeLifecycle, State: "agent_finished"})
		return result, nil
	}

	result.Error = state.failureReason(waitErr, ctx.Err(), parseErr, stderrPath)
	if result.Completion == adapter.Failed {
		notify(events.Event{Type: events.TypeError, Error: result.Error})
	}
	notify(events.Event{Type: events.TypeLifecycle, State: "agent_stopped", Error: result.Error})
	return result, nil
}

// makeHome returns the private agent directory for this attempt. Pi stores
// model catalogs, credentials, and trust state there, so a fresh directory is
// the only way to keep attempts from sharing state.
func makeHome(artifactsDir string) (string, func(), error) {
	if artifactsDir != "" {
		home := filepath.Join(artifactsDir, "pi-home")
		if err := os.MkdirAll(home, 0o700); err != nil {
			return "", nil, fmt.Errorf("pi: create agent directory: %w", err)
		}
		return home, func() {}, nil
	}
	home, err := os.MkdirTemp("", "gauge-pi-home-")
	if err != nil {
		return "", nil, fmt.Errorf("pi: create temporary agent directory: %w", err)
	}
	return home, func() { _ = os.RemoveAll(home) }, nil
}

// copyFile installs a configuration file into the attempt's agent directory and
// returns the SHA-256 of its content, which is recorded so a later change to the
// file is visible in the experiment record.
func copyFile(source, target string) (string, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// buildEnvironment merges the controlled attempt environment with adapter-owned
// variables. Configuration that Pi reads at startup is pinned so a run cannot
// pick up the host's agent directory or make unexpected network calls.
func buildEnvironment(base map[string]string, home string, forward []string) []string {
	env := make(map[string]string, len(base)+len(forward)+4)
	if len(base) == 0 {
		for _, entry := range os.Environ() {
			if key, value, ok := strings.Cut(entry, "="); ok {
				env[key] = value
			}
		}
	} else {
		for key, value := range base {
			env[key] = value
		}
	}
	env["PI_CODING_AGENT_DIR"] = home
	env["PI_OFFLINE"] = "1"
	env["PI_SKIP_VERSION_CHECK"] = "1"
	env["PI_TELEMETRY"] = "0"
	for _, name := range forward {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
}

// probeVersion asks Pi for its version with the same environment as the run.
// Version discovery is best-effort: an older or wrapped binary may not support
// --version, in which case Gauge records no version rather than guessing.
func probeVersion(ctx context.Context, command string, environment []string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, command, "--version")
	proc.Configure(cmd)
	cmd.Env = environment
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(out))
	if first, _, found := strings.Cut(version, "\n"); found {
		version = strings.TrimSpace(first)
	}
	if version == "" {
		return ""
	}
	return truncateText(version, 120)
}

func createArtifact(dir, name string) (io.Writer, *os.File, string, error) {
	if dir == "" {
		return io.Discard, nil, "", nil
	}
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("pi: create %s: %w", name, err)
	}
	return file, file, path, nil
}

func closeFile(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}

// firstNonEmptyLine returns a short diagnostic from a captured log file. It is
// used when Pi fails before emitting useful events, for example when no model
// credentials are available and stderr is the only explanation.
func firstNonEmptyLine(path string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 16<<10))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateText(trimmed, 400)
		}
	}
	return ""
}

func fail(result adapter.RunResult, err error, notify func(events.Event)) adapter.RunResult {
	notify(events.Event{Type: events.TypeError, Error: err.Error()})
	result.Completion = adapter.Failed
	result.Error = err.Error()
	result.EndedAt = time.Now().UTC()
	result.Duration = result.EndedAt.Sub(result.StartedAt)
	return result
}

func interrupted(result adapter.RunResult, ctxErr error, notify func(events.Event)) adapter.RunResult {
	result.EndedAt = time.Now().UTC()
	result.Duration = result.EndedAt.Sub(result.StartedAt)
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		result.Completion = adapter.TimedOut
		result.Error = "pi: attempt deadline exceeded"
	} else {
		result.Completion = adapter.Cancelled
		result.Error = "pi: run cancelled"
	}
	notify(events.Event{Type: events.TypeLifecycle, State: "agent_stopped", Error: result.Error})
	return result
}
