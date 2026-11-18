package iris

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
)

const minimalStream = `{"type":"assistant.delta","event":{"text":"ok"}}
{"type":"agent.turn.finished","event":{"turn":1}}
{"type":"agent.finished","event":{}}
{"type":"result","event":{"content":"ok"}}
`

// writeStub creates a POSIX shell executable that stands in for `iris`. The
// adapter tests invoke real subprocesses, but never a model or the real agent.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub agent executables require a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "iris-stub")
	content := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func baseEnv(extra map[string]string) map[string]string {
	env := map[string]string{"PATH": os.Getenv("PATH")}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func runAdapter(t *testing.T, ctx context.Context, cfg Config, requestModel string, env map[string]string, artifacts, workspace string) (adapter.RunResult, []events.Event) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	var emitted []events.Event
	result, err := New().Run(ctx, adapter.RunRequest{
		RunID:        "run-1",
		TaskID:       "task-1",
		Prompt:       "add GreetAll",
		WorkDir:      workspace,
		ArtifactsDir: artifacts,
		Timeout:      5 * time.Second,
		Config:       raw,
		Model:        requestModel,
		Environment:  env,
	}, func(event events.Event) { emitted = append(emitted, event) })
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	return result, emitted
}

func TestRunCompletesWithRecordedStream(t *testing.T) {
	stub := writeStub(t, `printf '%s\n' "$@" > "$STUB_ARGS"
cat > "$STUB_STDIN"
cat "$STUB_STREAM"`)

	streamPath := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(streamPath, []byte(sampleStream), 0o644); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")

	artifacts := t.TempDir()
	workspace := t.TempDir()
	home := filepath.Join(artifacts, "iris-home", "sessions")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	session := `{"type":"session","version":9,"session":{"id":"sess-1","model":"request-model","model_metadata":{"provider":"openai-compatible","base_url":"http://127.0.0.1:8080/v1"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "s.jsonl"), []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}

	result, emitted := runAdapter(t, context.Background(), Config{
		Command: stub,
		Model:   "config-model",
		BaseURL: "http://127.0.0.1:8080/v1",
	}, "request-model", baseEnv(map[string]string{
		"STUB_ARGS":   argsPath,
		"STUB_STDIN":  stdinPath,
		"STUB_STREAM": streamPath,
	}), artifacts, workspace)

	if result.Completion != adapter.Completed {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("exit code = %v", result.ExitCode)
	}
	if result.Model != "request-model" {
		t.Errorf("model = %q, want the per-run override", result.Model)
	}
	if result.Provider != "openai-compatible" {
		t.Errorf("provider = %q, want the session header value", result.Provider)
	}
	if result.Usage == nil || result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 14 {
		t.Errorf("usage = %+v", result.Usage)
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != "add GreetAll" {
		t.Errorf("prompt on stdin = %q", stdin)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"jsonl",
		"--home", filepath.Join(artifacts, "iris-home"),
		"--workdir", workspace,
		"--base-url", "http://127.0.0.1:8080/v1",
		"--model", "request-model",
	}
	if got := strings.Split(strings.TrimSpace(string(args)), "\n"); strings.Join(got, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("argv = %q, want %q", got, wantArgs)
	}

	stdoutLog, err := os.ReadFile(filepath.Join(artifacts, "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdoutLog) != sampleStream {
		t.Error("raw stdout was not preserved verbatim in stdout.log")
	}

	var meta adapterMetadata
	if err := json.Unmarshal(result.Metadata, &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if !meta.ResultRecord || meta.SessionID != "sess-1" || meta.SessionBaseURL != "http://127.0.0.1:8080/v1" {
		t.Errorf("metadata = %+v", meta)
	}
	if meta.LLMRequests != 1 || meta.ToolExecutions != 1 || meta.ToolCalls != 1 {
		t.Errorf("metadata counters = %+v", meta)
	}
	if len(filterEvents(emitted, events.TypeAgentMessage)) != 1 {
		t.Errorf("agent messages = %+v", filterEvents(emitted, events.TypeAgentMessage))
	}
	if len(filterEvents(emitted, events.TypeToolCall)) != 1 || len(filterEvents(emitted, events.TypeToolResult)) != 1 {
		t.Errorf("tool events missing: %+v", emitted)
	}
}

func TestRunUsesSessionModelWhenNotConfigured(t *testing.T) {
	stub := writeStub(t, `cat > /dev/null
printf '%s\n' '{"type":"result","event":{"content":"ok"}}'`)

	artifacts := t.TempDir()
	sessions := filepath.Join(artifacts, "iris-home", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	session := `{"type":"session","version":9,"session":{"id":"sess-2","model":"qwen3.5-9b","model_metadata":{"provider":"openai-compatible"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "s.jsonl"), []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}

	result, _ := runAdapter(t, context.Background(), Config{Command: stub}, "", baseEnv(nil), artifacts, t.TempDir())
	if result.Completion != adapter.Completed {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
	if result.Model != "qwen3.5-9b" {
		t.Errorf("model = %q, want the session header model", result.Model)
	}
}

func TestRunReportsRequestedModelWithoutSession(t *testing.T) {
	stub := writeStub(t, `cat > /dev/null
cat "$STUB_STREAM"`)
	streamPath := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(streamPath, []byte(minimalStream), 0o644); err != nil {
		t.Fatal(err)
	}

	result, _ := runAdapter(t, context.Background(), Config{Command: stub}, "requested-model", baseEnv(map[string]string{
		"STUB_STREAM": streamPath,
	}), t.TempDir(), t.TempDir())
	if result.Model != "requested-model" {
		t.Errorf("model = %q, want the requested model as a fallback", result.Model)
	}
}

func TestRunCopiesSettingsFile(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "iris-settings.toml")
	settings := "[security.shell]\nmode = \"allowlist\"\nallow = [\"go\", \"git\"]\n"
	if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	streamPath := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(streamPath, []byte(minimalStream), 0o644); err != nil {
		t.Fatal(err)
	}

	artifacts := t.TempDir()
	// The stub fails the run if the settings file is not in place before it
	// starts, which pins down the copy ordering.
	stub := writeStub(t, `cat > /dev/null
test -f "$STUB_SETTINGS" || exit 7
cat "$STUB_STREAM"`)

	result, _ := runAdapter(t, context.Background(), Config{
		Command:      stub,
		SettingsFile: settingsPath,
	}, "", baseEnv(map[string]string{
		"STUB_SETTINGS": filepath.Join(artifacts, "iris-home", "settings.toml"),
		"STUB_STREAM":   streamPath,
	}), artifacts, t.TempDir())
	if result.Completion != adapter.Completed {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}

	copied, err := os.ReadFile(filepath.Join(artifacts, "iris-home", "settings.toml"))
	if err != nil {
		t.Fatalf("settings were not copied into the Iris home: %v", err)
	}
	if string(copied) != settings {
		t.Errorf("copied settings = %q", copied)
	}
}

func TestRunReportsStreamError(t *testing.T) {
	stub := writeStub(t, `cat > /dev/null
printf '%s\n' '{"type":"error","error":"model endpoint unreachable"}'
exit 2`)

	artifacts := t.TempDir()
	result, emitted := runAdapter(t, context.Background(), Config{Command: stub}, "", baseEnv(nil), artifacts, t.TempDir())
	if result.Completion != adapter.Failed {
		t.Fatalf("completion = %q", result.Completion)
	}
	if result.ExitCode == nil || *result.ExitCode != 2 {
		t.Errorf("exit code = %v", result.ExitCode)
	}
	if !strings.Contains(result.Error, "model endpoint unreachable") {
		t.Errorf("error = %q", result.Error)
	}
	if len(filterEvents(emitted, events.TypeError)) == 0 {
		t.Error("no normalized error event was emitted")
	}
	logged, err := os.ReadFile(filepath.Join(artifacts, "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "model endpoint unreachable") {
		t.Errorf("stdout.log = %q", logged)
	}
}

func TestRunFailsWithoutResultRecord(t *testing.T) {
	stub := writeStub(t, `cat > /dev/null
echo "iris: cannot reach the model endpoint" >&2
exit 0`)

	result, _ := runAdapter(t, context.Background(), Config{Command: stub}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.Failed {
		t.Fatalf("completion = %q", result.Completion)
	}
	if !strings.Contains(result.Error, "no result record") {
		t.Errorf("error = %q", result.Error)
	}
	if !strings.Contains(result.Error, "cannot reach the model endpoint") {
		t.Errorf("error should quote stderr, got %q", result.Error)
	}
}

func TestRunMissingExecutable(t *testing.T) {
	result, _ := runAdapter(t, context.Background(), Config{Command: "gauge-iris-missing-executable"}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.Failed {
		t.Fatalf("completion = %q", result.Completion)
	}
	if !strings.Contains(result.Error, "not found") {
		t.Errorf("error = %q", result.Error)
	}
}

func TestRunTimesOut(t *testing.T) {
	stub := writeStub(t, `cat > /dev/null
sleep 30`)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	result, _ := runAdapter(t, ctx, Config{Command: stub}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.TimedOut {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("timeout took %s; the process tree was not terminated promptly", elapsed)
	}
	if !strings.Contains(result.Error, "deadline") {
		t.Errorf("error = %q", result.Error)
	}
}

func TestRunCancelled(t *testing.T) {
	stub := writeStub(t, `cat > /dev/null
sleep 30`)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	result, _ := runAdapter(t, ctx, Config{Command: stub}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.Cancelled {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
	if !strings.Contains(result.Error, "cancelled") {
		t.Errorf("error = %q", result.Error)
	}
}

func TestRunWithoutArtifactsDirectory(t *testing.T) {
	stub := writeStub(t, `cat > /dev/null
cat "$STUB_STREAM"`)
	streamPath := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(streamPath, []byte(minimalStream), 0o644); err != nil {
		t.Fatal(err)
	}

	// Direct adapter use (no runner, no artifacts) must still work and must
	// clean up the temporary Iris home it creates.
	result, _ := runAdapter(t, context.Background(), Config{Command: stub}, "", baseEnv(map[string]string{
		"STUB_STREAM": streamPath,
	}), "", "")
	if result.Completion != adapter.Completed {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
	if result.Usage != nil {
		t.Errorf("usage should be unavailable when the stream reports none: %+v", result.Usage)
	}
}

func TestRunStartFailureOnExpiredContext(t *testing.T) {
	stub := writeStub(t, `exit 0`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, _ := runAdapter(t, ctx, Config{Command: stub}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.Cancelled {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
}
