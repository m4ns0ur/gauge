package pi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
)

const minimalStream = `{"type":"session","version":3,"id":"sess-min","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"totalTokens":2},"stopReason":"stop"}}
{"type":"agent_settled","aborted":false}
`

const errorStream = `{"type":"session","version":3,"id":"sess-err","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}
{"type":"message_end","message":{"role":"assistant","content":[],"provider":"local","model":"m1","usage":{"input":0,"output":0,"totalTokens":0},"stopReason":"error","errorMessage":"Connection error.: connect ECONNREFUSED 127.0.0.1:8080"}}
{"type":"agent_settled","aborted":false}
`

// writeStub creates a POSIX shell executable that stands in for `pi`. The
// adapter tests invoke real subprocesses, but never a model or the real agent.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub agent executables require a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "pi-stub")
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

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
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
	stub := writeStub(t, `if [ "$1" = "--version" ]; then
  printf '9.9.9-stub\n'
  exit 0
fi
printf '%s\n' "$@" > "$STUB_ARGS"
cat > "$STUB_STDIN"
printf '%s' "$PI_CODING_AGENT_DIR" > "$STUB_HOME"
printf '%s' "$PI_TEST_KEY" > "$STUB_FORWARDED"
test -f "$PI_CODING_AGENT_DIR/models.json" || exit 8
test -f "$PI_CODING_AGENT_DIR/auth.json" || exit 9
cat "$STUB_STREAM"`)

	streamPath := writeFile(t, t.TempDir(), "stream.jsonl", sampleStream)
	credentials := t.TempDir()
	modelsPath := writeFile(t, credentials, "models.json", `{"providers":{"local":{"baseUrl":"http://127.0.0.1:8080/v1","api":"openai-completions","apiKey":"local","models":[{"id":"m1"}]}}}`)
	authPath := writeFile(t, credentials, "auth.json", `{"local":{"type":"apiKey","key":"local"}}`)
	t.Setenv("PI_TEST_KEY", "secret-value")

	artifacts := t.TempDir()
	workspace := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	homePath := filepath.Join(t.TempDir(), "home.txt")
	forwardedPath := filepath.Join(t.TempDir(), "forwarded.txt")

	result, emitted := runAdapter(t, context.Background(), Config{
		Command:    stub,
		Provider:   "cfg-provider",
		Model:      "cfg-model",
		Thinking:   "high",
		Tools:      []string{"read", "bash"},
		ModelsFile: modelsPath,
		AuthFile:   authPath,
		ForwardEnv: []string{"PI_TEST_KEY"},
	}, "run-model", baseEnv(map[string]string{
		"STUB_ARGS":      argsPath,
		"STUB_STDIN":     stdinPath,
		"STUB_HOME":      homePath,
		"STUB_FORWARDED": forwardedPath,
		"STUB_STREAM":    streamPath,
	}), artifacts, workspace)

	if result.Completion != adapter.Completed {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("exit code = %v", result.ExitCode)
	}
	if result.AgentVersion != "9.9.9-stub" {
		t.Errorf("agent version = %q", result.AgentVersion)
	}
	if result.Model != "m1-2026" || result.Provider != "local" {
		t.Errorf("provider/model = %q/%q, want the response values", result.Provider, result.Model)
	}
	if result.Usage == nil || deref(result.Usage.TotalTokens) != 190 {
		t.Errorf("usage = %+v", result.Usage)
	}

	if got := readFile(t, stdinPath); got != "add GreetAll" {
		t.Errorf("prompt on stdin = %q", got)
	}
	wantArgs := []string{
		"--mode", "json",
		"--no-session",
		"--provider", "cfg-provider",
		"--model", "run-model",
		"--thinking", "high",
		"--tools", "read,bash",
	}
	if got := strings.Split(strings.TrimSpace(readFile(t, argsPath)), "\n"); strings.Join(got, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("argv = %q, want %q", got, wantArgs)
	}

	home := readFile(t, homePath)
	if want := filepath.Join(artifacts, "pi-home"); home != want {
		t.Errorf("PI_CODING_AGENT_DIR = %q, want %q", home, want)
	}
	if got := readFile(t, forwardedPath); got != "secret-value" {
		t.Errorf("forwarded variable = %q", got)
	}
	if got := readFile(t, filepath.Join(artifacts, "pi-home", "models.json")); got != readFile(t, modelsPath) {
		t.Errorf("models.json copy = %q", got)
	}
	if got := readFile(t, filepath.Join(artifacts, "pi-home", "auth.json")); got != readFile(t, authPath) {
		t.Errorf("auth.json copy = %q", got)
	}
	if got := readFile(t, filepath.Join(artifacts, "stdout.log")); got != sampleStream {
		t.Error("raw stdout was not preserved verbatim in stdout.log")
	}

	var meta adapterMetadata
	if err := json.Unmarshal(result.Metadata, &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if !meta.Settled || meta.StopReason != "stop" || meta.SessionID != "sess-1" {
		t.Errorf("metadata = %+v", meta)
	}
	if meta.ToolCalls != 1 || meta.Tools == nil || meta.Thinking != "high" {
		t.Errorf("metadata config = %+v", meta)
	}
	if !meta.AuthFileInstalled {
		t.Error("metadata does not record that auth.json was installed")
	}
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(readFile(t, modelsPath)))); meta.ModelsFileSHA256 != want {
		t.Errorf("models hash = %q, want %q", meta.ModelsFileSHA256, want)
	}
	if len(filterEvents(emitted, events.TypeAgentMessage)) != 2 {
		t.Errorf("agent messages = %+v", filterEvents(emitted, events.TypeAgentMessage))
	}
}

func TestRunFailsDespiteZeroExit(t *testing.T) {
	stub := writeStub(t, `if [ "$1" = "--version" ]; then printf '9.9.9-stub\n'; exit 0; fi
cat > /dev/null
cat "$STUB_STREAM"`)
	streamPath := writeFile(t, t.TempDir(), "stream.jsonl", errorStream)

	result, _ := runAdapter(t, context.Background(), Config{Command: stub}, "", baseEnv(map[string]string{
		"STUB_STREAM": streamPath,
	}), t.TempDir(), t.TempDir())

	if result.Completion != adapter.Failed {
		t.Fatalf("completion = %q; Pi exits 0 after an error response, so the adapter must fail it", result.Completion)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("exit code = %v, want the real 0", result.ExitCode)
	}
	if !strings.Contains(result.Error, "ECONNREFUSED") {
		t.Errorf("error = %q", result.Error)
	}
}

func TestRunReportsStartupFailure(t *testing.T) {
	stub := writeStub(t, `if [ "$1" = "--version" ]; then printf '9.9.9-stub\n'; exit 0; fi
cat > /dev/null
echo "No API key found for the selected model." >&2
exit 1`)

	result, _ := runAdapter(t, context.Background(), Config{Command: stub}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.Failed {
		t.Fatalf("completion = %q", result.Completion)
	}
	if result.ExitCode == nil || *result.ExitCode != 1 {
		t.Errorf("exit code = %v", result.ExitCode)
	}
	for _, want := range []string{"agent_settled was not observed", "No API key found"} {
		if !strings.Contains(result.Error, want) {
			t.Errorf("error %q missing %q", result.Error, want)
		}
	}
}

func TestRunRequiresModelWithProvider(t *testing.T) {
	stub := writeStub(t, `if [ "$1" = "--version" ]; then printf '9.9.9-stub\n'; exit 0; fi
exit 0`)

	result, _ := runAdapter(t, context.Background(), Config{Command: stub, Provider: "local"}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.Failed {
		t.Fatalf("completion = %q", result.Completion)
	}
	if !strings.Contains(result.Error, "provider requires a model") {
		t.Errorf("error = %q", result.Error)
	}
}

func TestRunMissingExecutable(t *testing.T) {
	result, _ := runAdapter(t, context.Background(), Config{Command: "gauge-pi-missing-executable"}, "", baseEnv(nil), t.TempDir(), t.TempDir())
	if result.Completion != adapter.Failed {
		t.Fatalf("completion = %q", result.Completion)
	}
	if !strings.Contains(result.Error, "cannot use command") {
		t.Errorf("error = %q", result.Error)
	}
}

func TestRunTimesOut(t *testing.T) {
	stub := writeStub(t, `if [ "$1" = "--version" ]; then printf '9.9.9-stub\n'; exit 0; fi
cat > /dev/null
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
	stub := writeStub(t, `if [ "$1" = "--version" ]; then printf '9.9.9-stub\n'; exit 0; fi
cat > /dev/null
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
	stub := writeStub(t, `if [ "$1" = "--version" ]; then printf '9.9.9-stub\n'; exit 0; fi
cat > /dev/null
cat "$STUB_STREAM"`)
	streamPath := writeFile(t, t.TempDir(), "stream.jsonl", minimalStream)

	result, _ := runAdapter(t, context.Background(), Config{Command: stub}, "", baseEnv(map[string]string{
		"STUB_STREAM": streamPath,
	}), "", "")
	if result.Completion != adapter.Completed {
		t.Fatalf("completion = %q (error %q)", result.Completion, result.Error)
	}
	if result.Usage == nil || deref(result.Usage.TotalTokens) != 2 {
		t.Errorf("usage = %+v", result.Usage)
	}
}
