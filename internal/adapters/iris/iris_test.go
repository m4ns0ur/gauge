package iris

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
)

// sampleStream is a successful Iris JSONL run, shaped like the records produced
// by `iris jsonl` (see docs/adapters.md). It is deliberately recorded data, not
// a live model transcript: the adapter tests must not need Iris or an endpoint.
const sampleStream = `{"type":"agent.started","event":{"at":"2026-01-01T00:00:00Z","input":"add GreetAll"}}
{"type":"agent.turn.started","event":{"at":"2026-01-01T00:00:00.010Z","turn":1}}
{"type":"assistant.delta","event":{"at":"2026-01-01T00:00:00.020Z","text":"Adding "}}
{"type":"assistant.delta","event":{"at":"2026-01-01T00:00:00.030Z","text":"GreetAll."}}
{"type":"tool.started","event":{"at":"2026-01-01T00:00:00.040Z","tool_call_id":"call-1","tool_name":"read","args":{"path":"greeting.go"}}}
{"type":"tool.completed","event":{"at":"2026-01-01T00:00:00.050Z","tool_call_id":"call-1","tool_name":"read","result":{"content":"package greeting"},"duration":1000}}
{"type":"usage","event":{"at":"2026-01-01T00:00:00.060Z","prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}
{"type":"agent.turn.finished","event":{"at":"2026-01-01T00:00:00.070Z","turn":1,"finish_reason":"stop"}}
{"type":"agent.finished","event":{"at":"2026-01-01T00:00:00.080Z","summary":{"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:00.080Z","duration_ms":80,"llm_requests":1,"tool_executions":1,"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14},"git_repository":true,"files_changed":[{"status":"M","path":"greeting.go"}]}}}
{"type":"result","event":{"content":"Adding GreetAll.","session_id":"sess-1","usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14},"summary":{"llm_requests":1,"tool_executions":1,"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}}}
`

func TestParseSuccessfulStream(t *testing.T) {
	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	var raw bytes.Buffer

	if err := state.parse(strings.NewReader(sampleStream), &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	state.finish()

	if raw.String() != sampleStream {
		t.Errorf("raw output was not preserved verbatim")
	}
	if !state.succeeded() {
		t.Errorf("successful stream was not recognized; errRecord=%q finishedErr=%q agentError=%q", state.errRecord, state.finishedErr, state.agentError)
	}
	if state.result == nil || state.result.SessionID != "sess-1" {
		t.Fatalf("result record = %+v", state.result)
	}

	messages := filterEvents(emitted, events.TypeAgentMessage)
	if len(messages) != 1 || messages[0].Text != "Adding GreetAll." {
		t.Errorf("agent messages = %+v", messages)
	}

	toolCalls := filterEvents(emitted, events.TypeToolCall)
	if len(toolCalls) != 1 || toolCalls[0].Tool == nil {
		t.Fatalf("tool calls = %+v", toolCalls)
	}
	if toolCalls[0].Tool.CallID != "call-1" || toolCalls[0].Tool.Name != "read" {
		t.Errorf("tool call = %+v", toolCalls[0].Tool)
	}
	if !strings.Contains(string(toolCalls[0].Tool.Input), `"path":"greeting.go"`) {
		t.Errorf("tool input = %s", toolCalls[0].Tool.Input)
	}

	toolResults := filterEvents(emitted, events.TypeToolResult)
	if len(toolResults) != 1 || toolResults[0].Tool == nil || toolResults[0].Tool.Failed {
		t.Fatalf("tool results = %+v", toolResults)
	}
	if !strings.Contains(toolResults[0].Tool.Output, "package greeting") {
		t.Errorf("tool output = %q", toolResults[0].Tool.Output)
	}

	usageEvents := filterEvents(emitted, events.TypeUsage)
	if len(usageEvents) != 1 {
		t.Fatalf("usage events = %d", len(usageEvents))
	}
	usage := usageEvents[0].Usage
	if usage == nil || deref(usage.InputTokens) != 11 || deref(usage.OutputTokens) != 3 || deref(usage.TotalTokens) != 14 {
		t.Errorf("usage event = %+v", usage)
	}

	if state.toolCalls != 1 || state.toolFailures != 0 {
		t.Errorf("tool counters = %d calls, %d failures", state.toolCalls, state.toolFailures)
	}
	runUsage := state.runUsage()
	if runUsage == nil || deref(runUsage.TotalTokens) != 14 {
		t.Errorf("run usage = %+v", runUsage)
	}

	meta := state.metadata("iris", "http://127.0.0.1:8080/v1")
	if !meta.ResultRecord || meta.SessionID != "sess-1" {
		t.Errorf("metadata = %+v", meta)
	}
	if meta.LLMRequests != 1 || meta.ToolExecutions != 1 || meta.ToolCalls != 1 || meta.FilesChanged != 1 {
		t.Errorf("metadata counters = %+v", meta)
	}
	if meta.ParseErrors != 0 || len(meta.UnrecognizedEvents) != 0 {
		t.Errorf("metadata reported stream problems: %+v", meta)
	}
	if meta.Records != 10 {
		t.Errorf("metadata records = %d, want 10", meta.Records)
	}
}

func TestParseDiscardsRetriedTurnOutput(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant.delta","event":{"text":"partial "}}`,
		`{"type":"agent.turn.retried","event":{"turn":1,"reason":"context_overflow","error":"prompt too long","next_attempt":2}}`,
		`{"type":"assistant.delta","event":{"text":"final"}}`,
		`{"type":"agent.turn.finished","event":{"turn":1}}`,
		`{"type":"agent.finished","event":{}}`,
		`{"type":"result","event":{"content":"final"}}`,
	}, "\n") + "\n"

	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	state.finish()

	messages := filterEvents(emitted, events.TypeAgentMessage)
	if len(messages) != 1 || messages[0].Text != "final" {
		t.Errorf("messages after retry = %+v", messages)
	}
	warnings := filterEvents(emitted, events.TypeWarning)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Text, "context_overflow") {
		t.Errorf("warnings = %+v", warnings)
	}
}

func TestParseFlushesPartialMessageOnTimeout(t *testing.T) {
	stream := `{"type":"assistant.delta","event":{"text":"half an answer"}}` + "\n"
	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	state.finish()

	messages := filterEvents(emitted, events.TypeAgentMessage)
	if len(messages) != 1 || messages[0].Text != "half an answer" {
		t.Errorf("partial message was not flushed: %+v", messages)
	}
	if state.succeeded() {
		t.Error("a stream without a result record must not be considered successful")
	}
}

func TestParseCountsUnknownAndMalformedRecords(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"future.event","event":{}}`,
		`this is not json`,
		`{"type":"assistant.delta","event":{"text":"ok"}}`,
		`{"type":"agent.turn.finished","event":{"turn":1}}`,
		`{"type":"result","event":{"content":"ok"}}`,
	}, "\n") + "\n"

	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	state.finish()

	if !state.succeeded() {
		t.Error("a complete result should not be rejected because of an unknown record")
	}
	if state.unrecognized["future.event"] != 1 {
		t.Errorf("unrecognized = %+v", state.unrecognized)
	}
	if state.parseErrors != 1 || state.firstParseErr == "" {
		t.Errorf("parse errors = %d, first = %q", state.parseErrors, state.firstParseErr)
	}
	meta := state.metadata("iris", "")
	if len(meta.UnrecognizedEvents) != 1 || meta.UnrecognizedEvents[0] != "future.event" {
		t.Errorf("unrecognized metadata = %+v", meta.UnrecognizedEvents)
	}
	if meta.ParseErrors != 1 {
		t.Errorf("parse error metadata = %d", meta.ParseErrors)
	}
}

func TestParseErrorRecordFailsRun(t *testing.T) {
	stream := `{"type":"error","error":"model endpoint unreachable"}` + "\n"
	state := newStreamState(func(events.Event) {})
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if state.succeeded() {
		t.Error("an error record must fail the run")
	}
	if got := state.failureReason(nil, nil, nil, ""); !strings.Contains(got, "model endpoint unreachable") {
		t.Errorf("failure reason = %q", got)
	}
}

func TestParseContinuesWhenRawCopyFails(t *testing.T) {
	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	if err := state.parse(strings.NewReader(sampleStream), failingWriter{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	state.finish()

	if !state.succeeded() {
		t.Error("a raw-copy failure must not change the stream outcome")
	}
	if state.rawErr == nil {
		t.Fatal("raw write failure was not recorded")
	}
	warnings := filterEvents(emitted, events.TypeWarning)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error, "write raw stdout") {
		t.Errorf("warnings = %+v", warnings)
	}
	if meta := state.metadata("iris", ""); meta.RawOutputError == "" {
		t.Error("metadata does not report the raw output error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestDecodeConfig(t *testing.T) {
	for _, raw := range []string{"", "null", "{}"} {
		cfg, err := decodeConfig(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("decodeConfig(%q): %v", raw, err)
		}
		if cfg.Command != defaultCommand {
			t.Errorf("decodeConfig(%q).Command = %q, want %q", raw, cfg.Command, defaultCommand)
		}
	}

	cfg, err := decodeConfig(json.RawMessage(`{"command":" /opt/iris/bin/iris ","model":"m","base_url":"http://127.0.0.1:8080/v1","debug":true}`))
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if cfg.Command != "/opt/iris/bin/iris" || cfg.Model != "m" || !cfg.Debug {
		t.Errorf("decoded config = %+v", cfg)
	}

	if _, err := decodeConfig(json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Error("unknown configuration field was accepted")
	}
}

func TestValidate(t *testing.T) {
	impl := New()
	valid := []string{
		`{}`,
		`{"command":` + quote(os.Args[0]) + `}`,
		`{"command":` + quote(os.Args[0]) + `,"base_url":"https://example.invalid/v1"}`,
	}
	for _, raw := range valid {
		if err := impl.Validate(context.Background(), json.RawMessage(raw)); err != nil {
			t.Errorf("Validate(%s) = %v", raw, err)
		}
	}

	invalid := []string{
		`{"base_url":"ftp://example.invalid/v1"}`,
		`{"base_url":"/v1"}`,
		`{"base_url":"http://"}`,
		`{"extra":"value"}`,
	}
	for _, raw := range invalid {
		if err := impl.Validate(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("Validate(%s) succeeded, want a configuration error", raw)
		}
	}

	err := impl.Validate(context.Background(), json.RawMessage(`{"command":"gauge-iris-missing-executable"}`))
	if err == nil {
		t.Fatal("Validate accepted a missing executable")
	}
	var unavailable *adapter.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Errorf("error %v is not an UnavailableError", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v", err)
	}

	// A missing settings file is a configuration error, not a missing agent.
	missing := filepath.Join(t.TempDir(), "missing.toml")
	err = impl.Validate(context.Background(), json.RawMessage(`{"command":`+quote(os.Args[0])+`,"settings_file":`+quote(missing)+`}`))
	if err == nil {
		t.Fatal("Validate accepted a missing settings_file")
	}
	if errors.As(err, &unavailable) {
		t.Errorf("missing settings_file was reported as an unavailable adapter: %v", err)
	}
}

func TestReadSessionInfo(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSession := func(name string, at time.Time, model, provider string) {
		t.Helper()
		content := `{"type":"session","version":9,"session":{"id":"` + name + `","model":"` + model + `","model_metadata":{"provider":"` + provider + `","base_url":"http://127.0.0.1:8080/v1"}}}` + "\n"
		path := filepath.Join(sessions, name+".jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	writeSession("older", time.Now().Add(-time.Hour), "old-model", "openai-compatible")
	writeSession("newer", time.Now(), "qwen3.5-9b", "openai-compatible")

	info := readSessionInfo(home)
	if info == nil {
		t.Fatal("no session info found")
	}
	if info.ID != "newer" || info.Model != "qwen3.5-9b" || info.Provider != "openai-compatible" {
		t.Errorf("session info = %+v", info)
	}
	if info.BaseURL != "http://127.0.0.1:8080/v1" {
		t.Errorf("session base URL = %q", info.BaseURL)
	}

	if info := readSessionInfo(t.TempDir()); info != nil {
		t.Errorf("session info from an empty home = %+v", info)
	}
}

func TestTruncationIsUTF8Safe(t *testing.T) {
	input := strings.Repeat("é", 100)
	got := truncateText(input, 51)
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Fatalf("truncation marker missing: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("truncation split a multi-byte rune: %q", got)
	}
}

func filterEvents(all []events.Event, want events.Type) []events.Event {
	var out []events.Event
	for _, event := range all {
		if event.Type == want {
			out = append(out, event)
		}
	}
	return out
}

func deref(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
