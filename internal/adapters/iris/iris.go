// Package iris adapts the Iris agent harness to Gauge's adapter contract.
//
// Iris is driven through its documented one-shot JSONL interface:
//
//	iris jsonl --home <dir> --workdir <dir> [--model <m>] [--base-url <u>]
//
// The task prompt is written to stdin, so it never appears on the process
// command line. Typed Iris events are translated into Gauge's normalized event
// vocabulary, and raw stdout/stderr are kept as attempt artifacts. The adapter
// never evaluates the task and never treats Iris's own completion claim as
// evidence of correctness.
package iris

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/proc"
)

// ID is the registry identifier for the Iris adapter.
const ID = "iris"

const (
	defaultCommand = "iris"
	// maxToolInput and maxToolOutput bound what is copied into normalized
	// events. The complete payload remains in stdout.log.
	maxToolInput  = 4 << 10
	maxToolOutput = 8 << 10
)

// Config is the JSON configuration accepted by the Iris adapter. It comes from
// [adapters.iris.options] in Gauge's TOML configuration.
type Config struct {
	// Command is the Iris executable name or path (default "iris").
	Command string `json:"command,omitempty"`
	// Model is passed as --model. A per-run --model override wins.
	Model string `json:"model,omitempty"`
	// BaseURL is passed as --base-url when set.
	BaseURL string `json:"base_url,omitempty"`
	// SettingsFile is an Iris settings.toml copied into the attempt's isolated
	// home before the run. It is the way to configure Iris security policy,
	// model settings, and context limits without sharing the user's ~/.iris.
	// A relative path resolves against Gauge's working directory.
	SettingsFile string `json:"settings_file,omitempty"`
	// Debug passes --debug and leaves verbose Iris logs in stderr.log.
	Debug bool `json:"debug,omitempty"`
}

// Adapter implements adapter.Adapter for Iris.
type Adapter struct{}

// New returns the Iris adapter.
func New() *Adapter { return &Adapter{} }

// ID implements adapter.Adapter.
func (a *Adapter) ID() string { return ID }

// Description implements adapter.Describer.
func (a *Adapter) Description() string {
	return "Iris agent harness over its JSONL subprocess interface"
}

// Validate implements adapter.Adapter. A missing Iris executable is reported as
// adapter.UnavailableError so it is not confused with a configuration mistake.
func (a *Adapter) Validate(_ context.Context, raw json.RawMessage) error {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return err
	}
	if _, err := resolveSettingsFile(cfg.SettingsFile); err != nil {
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
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("iris adapter config: %w", err)
	}
	cfg.Command = strings.TrimSpace(cfg.Command)
	if cfg.Command == "" {
		cfg.Command = defaultCommand
	}
	return cfg, nil
}

func validateBaseURL(value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("iris adapter config: base_url %q: %w", value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("iris adapter config: base_url %q must use http or https", value)
	}
	if parsed.Host == "" {
		return fmt.Errorf("iris adapter config: base_url %q must be an absolute URL", value)
	}
	return nil
}

// resolveSettingsFile validates the optional Iris settings file and returns its
// absolute path. An empty value is not an error.
func resolveSettingsFile(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("iris adapter config: resolve settings_file %q: %w", value, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("iris adapter config: settings_file %q: %w", value, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("iris adapter config: settings_file %q is a directory", value)
	}
	return absolute, nil
}

// resolveExecutable finds the Iris binary and returns an absolute path so the
// process can be started from any working directory.
func resolveExecutable(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf(
			"executable %q not found: install Iris or set adapters.%s.options.command to its path",
			command, ID,
		)
	}
	if !filepath.IsAbs(path) {
		absolute, absErr := filepath.Abs(path)
		if absErr != nil {
			return "", fmt.Errorf("resolve executable %q: %w", command, absErr)
		}
		path = absolute
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
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return fail(result, err, notify), nil
	}
	command, err := resolveExecutable(cfg.Command)
	if err != nil {
		return fail(result, err, notify), nil
	}
	settingsFile, err := resolveSettingsFile(cfg.SettingsFile)
	if err != nil {
		return fail(result, err, notify), nil
	}

	home, cleanupHome, err := makeHome(req.ArtifactsDir)
	if err != nil {
		return fail(result, err, notify), nil
	}
	defer cleanupHome()

	if settingsFile != "" {
		if err := copySettings(settingsFile, home); err != nil {
			return fail(result, err, notify), nil
		}
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

	model := strings.TrimSpace(cfg.Model)
	if override := strings.TrimSpace(req.Model); override != "" {
		model = override
	}

	args := []string{"jsonl", "--home", home}
	if req.WorkDir != "" {
		args = append(args, "--workdir", req.WorkDir)
	}
	if cfg.BaseURL != "" {
		args = append(args, "--base-url", cfg.BaseURL)
	}
	if cfg.Debug {
		args = append(args, "--debug")
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	proc.Configure(cmd)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	cmd.Env = processEnv(req.Environment)
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Stderr = stderrTarget

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(result, fmt.Errorf("iris: open stdout pipe: %w", err), notify), nil
	}
	if err := cmd.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return interrupted(result, ctxErr, notify), nil
		}
		return fail(result, fmt.Errorf("iris: start %s: %w", command, err), notify), nil
	}
	notify(events.Event{Type: events.TypeLifecycle, State: "agent_started"})

	state := newStreamState(notify)
	parseErr := state.parse(stdout, rawOut)
	state.finish()
	if parseErr != nil && ctx.Err() == nil && cmd.Cancel != nil {
		// The reader stopped before EOF. Terminate the agent so Wait cannot
		// block on a pipe that nobody is draining.
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()

	result.EndedAt = time.Now().UTC()
	result.Duration = result.EndedAt.Sub(result.StartedAt)

	if model != "" {
		result.Model = model
	}
	if session := readSessionInfo(home); session != nil {
		// The session header records the model Iris actually used, which is
		// more trustworthy than the flag Gauge passed.
		if session.Model != "" {
			result.Model = session.Model
		}
		if result.Provider == "" {
			result.Provider = session.Provider
		}
		if state.sessionID == "" {
			state.sessionID = session.ID
		}
		state.sessionBaseURL = session.BaseURL
	}
	result.Usage = state.runUsage()
	if meta, marshalErr := json.Marshal(state.metadata(command, cfg.BaseURL)); marshalErr == nil {
		result.Metadata = meta
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
		notify(events.Event{Type: events.TypeLifecycle, State: "agent_finished"})
		return result, nil
	}

	result.Error = state.failureReason(waitErr, ctx.Err(), parseErr, stderrPath)
	if result.Completion == adapter.Failed && !state.errorEmitted {
		notify(events.Event{Type: events.TypeError, Error: result.Error})
	}
	notify(events.Event{Type: events.TypeLifecycle, State: "agent_stopped", Error: result.Error})
	return result, nil
}

// copySettings installs an Iris settings.toml into the attempt's isolated home.
// The copy is per attempt, so an agent that edits its own settings cannot change
// the configuration a later repetition starts from.
func copySettings(source, home string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("iris: read settings_file: %w", err)
	}
	target := filepath.Join(home, "settings.toml")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return fmt.Errorf("iris: write settings.toml: %w", err)
	}
	return nil
}

// makeHome returns an isolated Iris home for this attempt. Iris always persists
// settings, sessions, and logs under its home directory, so a fresh home is the
// only way to keep attempts from sharing state.
func makeHome(artifactsDir string) (string, func(), error) {
	if artifactsDir != "" {
		home := filepath.Join(artifactsDir, "iris-home")
		if err := os.MkdirAll(home, 0o700); err != nil {
			return "", nil, fmt.Errorf("iris: create home directory: %w", err)
		}
		return home, func() {}, nil
	}
	home, err := os.MkdirTemp("", "gauge-iris-home-")
	if err != nil {
		return "", nil, fmt.Errorf("iris: create temporary home: %w", err)
	}
	return home, func() { _ = os.RemoveAll(home) }, nil
}

// createArtifact opens a per-attempt log file. The returned writer is
// io.Discard when the request has no artifact directory, which keeps direct
// adapter use (tests, embedding) working without a runner.
func createArtifact(dir, name string) (io.Writer, *os.File, string, error) {
	if dir == "" {
		return io.Discard, nil, "", nil
	}
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("iris: create %s: %w", name, err)
	}
	return file, file, path, nil
}

func closeFile(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}

// processEnv builds the child environment. Gauge's runner supplies a controlled
// environment so host configuration cannot leak into a benchmark attempt; an
// empty map means the adapter is being used directly and inherits the process
// environment.
func processEnv(environment map[string]string) []string {
	if len(environment) == 0 {
		return os.Environ()
	}
	env := make([]string, 0, len(environment))
	for key, value := range environment {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
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
		result.Error = "iris: attempt deadline exceeded"
	} else {
		result.Completion = adapter.Cancelled
		result.Error = "iris: run cancelled"
	}
	notify(events.Event{Type: events.TypeLifecycle, State: "agent_stopped", Error: result.Error})
	return result
}

// adapterMetadata is the machine-readable record of what the adapter observed.
// Unavailable signals are omitted rather than zero-filled.
type adapterMetadata struct {
	Command            string   `json:"command"`
	BaseURL            string   `json:"base_url,omitempty"`
	SessionID          string   `json:"session_id,omitempty"`
	SessionBaseURL     string   `json:"session_base_url,omitempty"`
	Records            int      `json:"records,omitempty"`
	UnrecognizedEvents []string `json:"unrecognized_events,omitempty"`
	ParseErrors        int      `json:"parse_errors,omitempty"`
	ResultRecord       bool     `json:"result_record"`
	LLMRequests        int      `json:"llm_requests,omitempty"`
	ToolExecutions     int      `json:"tool_executions,omitempty"`
	ToolCalls          int      `json:"tool_calls,omitempty"`
	ToolFailures       int      `json:"tool_failures,omitempty"`
	FilesChanged       int      `json:"files_changed,omitempty"`
	Truncated          bool     `json:"truncated,omitempty"`
	RawOutputError     string   `json:"raw_output_error,omitempty"`
}

// sessionInfo is the subset of Iris's session header Gauge can use.
type sessionInfo struct {
	ID       string
	Model    string
	Provider string
	BaseURL  string
}

// readSessionInfo reads the newest Iris session header from the attempt's
// isolated home. It is best-effort: Iris does not put the model in its event
// stream, and a run that fails before creating a session simply has no header.
func readSessionInfo(home string) *sessionInfo {
	matches, err := filepath.Glob(filepath.Join(home, "sessions", "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftErr := os.Stat(matches[i])
		right, rightErr := os.Stat(matches[j])
		if leftErr != nil || rightErr != nil {
			return matches[i] < matches[j]
		}
		return left.ModTime().After(right.ModTime())
	})
	for _, path := range matches {
		if info := readSessionHeader(path); info != nil {
			return info
		}
	}
	return nil
}

func readSessionHeader(path string) *sessionInfo {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	reader := bufio.NewReader(io.LimitReader(file, 1<<20))
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil
	}
	var header struct {
		Type    string `json:"type"`
		Session struct {
			ID            string            `json:"id"`
			Model         string            `json:"model"`
			ModelMetadata map[string]string `json:"model_metadata"`
		} `json:"session"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &header); err != nil || header.Type != "session" {
		return nil
	}
	info := &sessionInfo{
		ID:    header.Session.ID,
		Model: header.Session.Model,
	}
	if metadata := header.Session.ModelMetadata; metadata != nil {
		info.Provider = metadata["provider"]
		info.BaseURL = metadata["base_url"]
	}
	return info
}

// firstNonEmptyLine returns a short diagnostic from a captured log file. It is
// used when Iris fails before emitting any JSON record, where stderr is the
// only explanation the user gets.
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

// truncateText shortens s without splitting a UTF-8 rune.
func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(truncated)"
}

// truncatedJSON marshals an arbitrary adapter payload for a normalized event,
// bounding its size. Truncated payloads become a JSON string that ends in
// "…(truncated)"; the raw payload stays in stdout.log.
func truncatedJSON(value any, limit int) json.RawMessage {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil || string(data) == "null" {
		return nil
	}
	if len(data) > limit {
		data, err = json.Marshal(truncateText(string(data), limit))
		if err != nil {
			return nil
		}
	}
	return json.RawMessage(data)
}
