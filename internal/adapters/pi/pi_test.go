package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/events"
)

// sampleStream is a successful Pi JSON-mode run, shaped like the records Pi
// 1.5.2 emits. It is recorded data, not a live transcript: adapter tests must
// not need Pi or a model endpoint.
const sampleStream = `{"type":"session","version":3,"id":"sess-1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}
{"type":"agent_start"}
{"type":"turn_start"}
{"type":"message_end","message":{"role":"system","content":"","timestamp":1}}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"add GreetAll"}],"timestamp":2}}
{"type":"message_start","message":{"role":"assistant","content":[],"stopReason":"pending"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Let me look."},{"type":"toolCall","id":"call-1","name":"read","arguments":{"path":"greeting.go"}}],"api":"openai-completions","provider":"local","model":"m1","usage":{"input":100,"output":20,"cacheRead":10,"cacheWrite":0,"totalTokens":130,"cost":{"total":0.001}},"stopReason":"toolUse","timestamp":3}}
{"type":"tool_execution_start","toolCallId":"call-1","toolName":"read","args":{"path":"greeting.go"}}
{"type":"tool_execution_end","toolCallId":"call-1","toolName":"read","result":{"content":[{"type":"text","text":"package greeting"}],"details":{}},"isError":false,"durationMs":3}
{"type":"turn_end","message":{"role":"assistant","content":[]},"toolResults":[]}
{"type":"agent_end","messages":[],"willRetry":false}
{"type":"agent_start"}
{"type":"turn_start"}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Added GreetAll."}],"provider":"local","model":"m1","responseModel":"m1-2026","responseId":"resp-1","usage":{"input":50,"output":10,"cacheRead":0,"cacheWrite":0,"totalTokens":60,"cost":{"total":0.0005}},"stopReason":"stop","timestamp":4}}
{"type":"turn_end","message":{"role":"assistant","content":[]},"toolResults":[]}
{"type":"agent_end","messages":[],"willRetry":false}
{"type":"agent_settled","aborted":false}
`

func TestParseSuccessfulStream(t *testing.T) {
	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	var raw bytes.Buffer

	if err := state.parse(strings.NewReader(sampleStream), &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if raw.String() != sampleStream {
		t.Error("raw output was not preserved verbatim")
	}
	if !state.succeeded() {
		t.Errorf("successful stream was not recognized: stop=%q settled=%v aborted=%v", state.stopReason, state.settled, state.aborted)
	}
	if state.truncated() {
		t.Error("a stop response must not be marked truncated")
	}

	messages := filterEvents(emitted, events.TypeAgentMessage)
	if len(messages) != 2 || messages[0].Text != "Let me look." || messages[1].Text != "Added GreetAll." {
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
	if len(usageEvents) != 2 {
		t.Fatalf("usage events = %d", len(usageEvents))
	}
	usage := state.runUsage()
	if usage == nil {
		t.Fatal("run usage is nil")
	}
	if deref(usage.InputTokens) != 150 || deref(usage.OutputTokens) != 30 || deref(usage.TotalTokens) != 190 {
		t.Errorf("run usage = %+v", usage)
	}
	if usage.CostUSD == nil || *usage.CostUSD < 0.0014 || *usage.CostUSD > 0.0016 {
		t.Errorf("cost = %v, want ~0.0015", usage.CostUSD)
	}

	if state.sessionID != "sess-1" || state.sessionVersion != 3 {
		t.Errorf("session = %q v%d", state.sessionID, state.sessionVersion)
	}
	if state.provider != "local" || state.model != "m1-2026" {
		t.Errorf("provider/model = %q/%q, want the response model", state.provider, state.model)
	}
	if state.responseID != "resp-1" {
		t.Errorf("response ID = %q", state.responseID)
	}
	if state.toolCalls != 1 || state.toolFailures != 0 {
		t.Errorf("tool counters = %d calls, %d failures", state.toolCalls, state.toolFailures)
	}

	meta := state.metadata("pi", Config{})
	if !meta.Settled || meta.StopReason != "stop" || meta.SessionID != "sess-1" {
		t.Errorf("metadata = %+v", meta)
	}
	if meta.ToolCalls != 1 || meta.ToolFailures != 0 || meta.Truncated {
		t.Errorf("metadata counters = %+v", meta)
	}
	if meta.ParseErrors != 0 || len(meta.UnrecognizedEvents) != 0 {
		t.Errorf("metadata reported stream problems: %+v", meta)
	}
}

func TestParseErrorStopFailsEvenWithZeroExit(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"session","version":3,"id":"sess-2","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[],"provider":"local","model":"m1","usage":{"input":0,"output":0,"totalTokens":0},"stopReason":"error","errorMessage":"Connection error.: connect ECONNREFUSED 127.0.0.1:8080"}}`,
		`{"type":"agent_end","messages":[],"willRetry":true}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"connection refused"}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[],"provider":"local","model":"m1","usage":{"input":0,"output":0,"totalTokens":0},"stopReason":"error","errorMessage":"Connection error.: connect ECONNREFUSED 127.0.0.1:8080"}}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
		`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"connection refused"}`,
		`{"type":"agent_settled","aborted":false}`,
	}, "\n") + "\n"

	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if state.succeeded() {
		t.Error("a stream whose final assistant response failed must not succeed")
	}
	if reason := state.failureReason(nil, nil, nil, ""); !strings.Contains(reason, "ECONNREFUSED") {
		t.Errorf("failure reason = %q", reason)
	}
	if state.autoRetries != 1 {
		t.Errorf("auto retries = %d", state.autoRetries)
	}
	if len(filterEvents(emitted, events.TypeWarning)) == 0 {
		t.Error("no warning was emitted for the retry")
	}
	if state.runUsage() != nil {
		t.Errorf("zero usage was reported as available: %+v", state.runUsage())
	}
}

func TestParseAbortedStream(t *testing.T) {
	stream := `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"aborted","errorMessage":"user aborted"}}` + "\n" +
		`{"type":"agent_settled","aborted":true}` + "\n"
	state := newStreamState(func(events.Event) {})
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if state.succeeded() {
		t.Error("an aborted run must not succeed")
	}
	if reason := state.failureReason(nil, nil, nil, ""); !strings.Contains(reason, "aborted") {
		t.Errorf("failure reason = %q", reason)
	}
}

func TestParseTruncatedStream(t *testing.T) {
	stream := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"length"}}` + "\n" +
		`{"type":"agent_settled","aborted":false}` + "\n"
	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !state.succeeded() {
		t.Error("a length stop is a completed (truncated) run")
	}
	if !state.truncated() {
		t.Error("length stop was not marked truncated")
	}
	if len(filterEvents(emitted, events.TypeAgentMessage)) != 1 {
		t.Errorf("partial text was not emitted: %+v", filterEvents(emitted, events.TypeAgentMessage))
	}
}

func TestParseCountsUnknownAndMalformedRecords(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"future.event"}`,
		`not json`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop"}}`,
		`{"type":"agent_settled","aborted":false}`,
	}, "\n") + "\n"

	state := newStreamState(func(events.Event) {})
	if err := state.parse(strings.NewReader(stream), nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !state.succeeded() {
		t.Error("a complete result should not be rejected because of an unknown record")
	}
	if state.unrecognized["future.event"] != 1 {
		t.Errorf("unrecognized = %+v", state.unrecognized)
	}
	if state.parseErrors != 1 || state.firstParseErr == "" {
		t.Errorf("parse errors = %d, first = %q", state.parseErrors, state.firstParseErr)
	}
	meta := state.metadata("pi", Config{})
	if len(meta.UnrecognizedEvents) != 1 || meta.UnrecognizedEvents[0] != "future.event" {
		t.Errorf("unrecognized metadata = %+v", meta.UnrecognizedEvents)
	}
}

func TestParseContinuesWhenRawCopyFails(t *testing.T) {
	var emitted []events.Event
	state := newStreamState(func(event events.Event) { emitted = append(emitted, event) })
	if err := state.parse(strings.NewReader(sampleStream), failingWriter{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
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
	if meta := state.metadata("pi", Config{}); meta.RawOutputError == "" {
		t.Error("metadata does not report the raw output error")
	}
}

func TestContentTextHandlesStringsAndBlocks(t *testing.T) {
	if got := contentText(json.RawMessage(`"a string"`)); got != "a string" {
		t.Errorf("string content = %q", got)
	}
	if got := contentText(json.RawMessage(`[{"type":"text","text":"one"},{"type":"thinking","thinking":"hidden"},{"type":"text","text":"two"}]`)); got != "onetwo" {
		t.Errorf("block content = %q", got)
	}
	if got := contentText(nil); got != "" {
		t.Errorf("nil content = %q", got)
	}
}

func TestToolResultTextFallsBackToJSON(t *testing.T) {
	if got := toolResultText(json.RawMessage(`{"content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}`)); got != "line one\nline two" {
		t.Errorf("text result = %q", got)
	}
	if got := toolResultText(json.RawMessage(`{"details":{"exitCode":1}}`)); !strings.Contains(got, "exitCode") {
		t.Errorf("non-text result = %q", got)
	}
}

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

	cfg, err := decodeConfig(json.RawMessage(`{"command":" /opt/pi/bin/pi ","provider":"p","model":"m","thinking":"high","tools":["read","bash"],"forward_env":["OPENAI_API_KEY"]}`))
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if cfg.Command != "/opt/pi/bin/pi" || cfg.Provider != "p" || cfg.Model != "m" || cfg.Thinking != "high" {
		t.Errorf("decoded config = %+v", cfg)
	}
	if len(cfg.Tools) != 2 || len(cfg.ForwardEnv) != 1 {
		t.Errorf("decoded lists = %+v", cfg)
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
		`{"command":` + quote(os.Args[0]) + `,"provider":"local","model":"m","thinking":"off","tools":["read","bash"]}`,
		`{"command":` + quote(os.Args[0]) + `,"forward_env":["OPENAI_API_KEY"]}`,
	}
	for _, raw := range valid {
		if err := impl.Validate(context.Background(), json.RawMessage(raw)); err != nil {
			t.Errorf("Validate(%s) = %v", raw, err)
		}
	}

	invalid := []string{
		`{"thinking":"extreme"}`,
		`{"tools":["read,bash"]}`,
		`{"tools":[""]}`,
		`{"forward_env":["NOT-A-NAME"]}`,
		`{"extra":"value"}`,
	}
	for _, raw := range invalid {
		if err := impl.Validate(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("Validate(%s) succeeded, want a configuration error", raw)
		}
	}

	err := impl.Validate(context.Background(), json.RawMessage(`{"command":"gauge-pi-missing-executable"}`))
	if err == nil {
		t.Fatal("Validate accepted a missing executable")
	}
	var unavailable *adapter.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Errorf("error %v is not an UnavailableError", err)
	}

	// A missing models file is a configuration error, not a missing agent.
	missing := filepath.Join(t.TempDir(), "models.json")
	err = impl.Validate(context.Background(), json.RawMessage(`{"command":`+quote(os.Args[0])+`,"models_file":`+quote(missing)+`}`))
	if err == nil {
		t.Fatal("Validate accepted a missing models_file")
	}
	if errors.As(err, &unavailable) {
		t.Errorf("missing models_file was reported as an unavailable adapter: %v", err)
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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
