package iris

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/events"
)

// record is one line of Iris's JSONL stream. Agent events carry a typed
// payload in "event"; the terminal "result" record carries the run result and
// a failed run ends with {"type":"error","error":"…"}.
type record struct {
	Type  string          `json:"type"`
	Event json.RawMessage `json:"event"`
	Error string          `json:"error"`
}

// usagePayload mirrors Iris's token accounting. Iris reports prompt tokens and
// completion tokens; Gauge calls those input and output tokens.
type usagePayload struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u usagePayload) zero() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0
}

func (u usagePayload) event() *events.Usage {
	return &events.Usage{
		InputTokens:  int64Ptr(int64(u.PromptTokens)),
		OutputTokens: int64Ptr(int64(u.CompletionTokens)),
		TotalTokens:  int64Ptr(int64(u.TotalTokens)),
	}
}

func int64Ptr(value int64) *int64 { return &value }

// fileChange is one working-tree entry from Iris's run summary.
type fileChange struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// runSummary is the runtime-observed execution metadata Iris attaches to its
// terminal agent.finished event.
type runSummary struct {
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     time.Time    `json:"finished_at"`
	DurationMS     int64        `json:"duration_ms"`
	LLMRequests    int          `json:"llm_requests"`
	ToolExecutions int          `json:"tool_executions"`
	Usage          usagePayload `json:"usage"`
	GitRepository  bool         `json:"git_repository"`
	FilesChanged   []fileChange `json:"files_changed"`
}

// runResult is Iris's terminal "result" record.
type runResult struct {
	Content   string       `json:"content"`
	SessionID string       `json:"session_id"`
	Usage     usagePayload `json:"usage"`
	Canceled  bool         `json:"canceled"`
	Truncated bool         `json:"truncated"`
	Summary   *runSummary  `json:"summary"`
}

type usageTotals struct {
	prompt     int64
	completion int64
	total      int64
	seen       bool
}

// streamState translates one Iris JSONL stream into normalized events and the
// facts Gauge persists about the run.
type streamState struct {
	emit func(events.Event)

	records       int
	parseErrors   int
	firstParseErr string
	unrecognized  map[string]int

	message         strings.Builder
	emittedMessages int

	usage   usageTotals
	summary *runSummary
	result  *runResult

	errRecord   string
	finishedErr string
	agentError  string
	canceled    bool

	// errorEmitted avoids a duplicate normalized error when the agent stream
	// already reported one and the adapter then records the process failure.
	errorEmitted bool

	sessionID      string
	sessionBaseURL string

	toolCalls    int
	toolFailures int

	// rawErr records a failure to copy raw stdout to its artifact. It is a
	// telemetry problem, not a run outcome, and must not stop stream parsing.
	rawErr error
}

func newStreamState(emit func(events.Event)) *streamState {
	return &streamState{
		emit:         emit,
		unrecognized: make(map[string]int),
	}
}

// parse reads the stream line by line, copying every raw line to raw and
// translating recognized records. Unparsable lines are counted, not fatal:
// partial output after a timeout is still evidence. A raw-copy failure is
// reported once as a warning and stops raw copying, but parsing continues so a
// broken log file cannot change the recorded run outcome.
func (s *streamState) parse(r io.Reader, raw io.Writer) error {
	reader := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if raw != nil && s.rawErr == nil {
				if _, writeErr := raw.Write(line); writeErr != nil {
					s.rawErr = fmt.Errorf("write raw stdout: %w", writeErr)
					s.emit(events.Event{Type: events.TypeWarning, Error: s.rawErr.Error()})
				}
			}
			s.handleLine(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read stdout: %w", err)
		}
	}
}

func (s *streamState) handleLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return
	}
	s.records++

	var env record
	if err := json.Unmarshal(trimmed, &env); err != nil {
		s.parseFailure(err)
		return
	}
	switch env.Type {
	case "":
		s.parseFailure(fmt.Errorf("record without a type"))
	case "result":
		if len(env.Event) == 0 || string(env.Event) == "null" {
			s.parseFailure(fmt.Errorf("result record has no payload"))
			return
		}
		var result runResult
		if err := json.Unmarshal(env.Event, &result); err != nil {
			s.parseFailure(err)
			return
		}
		s.result = &result
		if result.SessionID != "" {
			s.sessionID = result.SessionID
		}
		if result.Canceled {
			s.canceled = true
		}
	case "error":
		s.errRecord = strings.TrimSpace(env.Error)
		if s.errRecord == "" {
			s.errRecord = "iris reported an error without a message"
		}
	default:
		s.handleEvent(env)
	}
}

func (s *streamState) handleEvent(env record) {
	switch env.Type {
	case "agent.started":
		// The adapter emits its own lifecycle event once the process has
		// actually started; the payload remains in stdout.log.

	case "agent.finished":
		var event struct {
			Err      string      `json:"error"`
			Canceled bool        `json:"canceled"`
			Summary  *runSummary `json:"summary"`
		}
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		if event.Err != "" {
			s.finishedErr = event.Err
		}
		if event.Canceled {
			s.canceled = true
		}
		if event.Summary != nil {
			s.summary = event.Summary
		}
		s.flushMessage()
		s.emit(events.Event{Type: events.TypeLifecycle, State: "agent_settled", Error: event.Err})

	case "agent.turn.started":
		s.emit(events.Event{Type: events.TypeAgentState, State: "turn_started", Raw: env.Event})

	case "agent.turn.finished":
		s.flushMessage()
		s.emit(events.Event{Type: events.TypeAgentState, State: "turn_finished", Raw: env.Event})

	case "agent.turn.retried":
		var event struct {
			Reason string `json:"reason"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		// Iris documents that partial output for a retried turn must be
		// discarded before the retry's deltas are processed.
		s.message.Reset()
		text := "turn retried"
		if event.Reason != "" {
			text += ": " + event.Reason
		}
		if event.Error != "" {
			text += " (" + truncateText(event.Error, 400) + ")"
		}
		s.emit(events.Event{Type: events.TypeWarning, Text: text, Raw: env.Event})

	case "assistant.delta":
		var event struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.message.WriteString(event.Text)

	case "tool.started":
		var event struct {
			ToolCallID string         `json:"tool_call_id"`
			ToolName   string         `json:"tool_name"`
			Args       map[string]any `json:"args"`
		}
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.toolCalls++
		s.emit(events.Event{
			Type: events.TypeToolCall,
			Tool: &events.Tool{
				CallID: event.ToolCallID,
				Name:   event.ToolName,
				Input:  truncatedJSON(event.Args, maxToolInput),
			},
		})

	case "tool.completed":
		var event struct {
			ToolCallID string         `json:"tool_call_id"`
			ToolName   string         `json:"tool_name"`
			Result     map[string]any `json:"result"`
		}
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		output := ""
		if len(event.Result) > 0 {
			if data, err := json.Marshal(event.Result); err == nil {
				output = truncateText(string(data), maxToolOutput)
			}
		}
		s.emit(events.Event{
			Type: events.TypeToolResult,
			Tool: &events.Tool{
				CallID: event.ToolCallID,
				Name:   event.ToolName,
				Output: output,
			},
		})

	case "tool.failed":
		var event struct {
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
			Message    string `json:"message"`
		}
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.toolFailures++
		s.emit(events.Event{
			Type: events.TypeToolResult,
			Tool: &events.Tool{
				CallID: event.ToolCallID,
				Name:   event.ToolName,
				Output: truncateText(event.Message, maxToolOutput),
				Failed: true,
			},
		})

	case "usage":
		var event usagePayload
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		if event.zero() {
			return
		}
		s.usage.prompt += int64(event.PromptTokens)
		s.usage.completion += int64(event.CompletionTokens)
		s.usage.total += int64(event.TotalTokens)
		s.usage.seen = true
		s.emit(events.Event{Type: events.TypeUsage, Usage: event.event()})

	case "agent.error":
		var event struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(env.Event, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.agentError = strings.TrimSpace(event.Message)
		s.emit(events.Event{Type: events.TypeError, Error: event.Message})
		s.errorEmitted = true

	case "context.compaction.started", "context.compaction.finished":
		state := "context_compaction_started"
		if env.Type == "context.compaction.finished" {
			state = "context_compaction_finished"
		}
		s.emit(events.Event{Type: events.TypeAgentState, State: state, Raw: env.Event})

	case "reasoning.delta", "tool.progress", "llm.request.started", "llm.response.started",
		"agent.queue.updated", "agent.instruction.delivered":
		// Preserved in stdout.log but omitted from normalized events: Gauge has
		// no vocabulary for streaming deltas or request timing, and one-shot
		// jsonl runs never deliver queued instructions.

	default:
		s.unrecognized[env.Type]++
	}
}

func (s *streamState) parseFailure(err error) {
	s.parseErrors++
	if s.firstParseErr == "" {
		s.firstParseErr = err.Error()
	}
}

// finish flushes assistant text that was still buffered when the process
// ended. This matters for timed-out runs, where the final turn never ends.
func (s *streamState) finish() {
	s.flushMessage()
	if s.emittedMessages == 0 && s.result != nil && strings.TrimSpace(s.result.Content) != "" {
		s.emit(events.Event{Type: events.TypeAgentMessage, Text: s.result.Content})
		s.emittedMessages++
	}
}

func (s *streamState) flushMessage() {
	if strings.TrimSpace(s.message.String()) != "" {
		s.emit(events.Event{Type: events.TypeAgentMessage, Text: s.message.String()})
		s.emittedMessages++
	}
	s.message.Reset()
}

// succeeded reports whether the stream itself shows a clean run. The process
// exit code and the evaluator are checked separately; neither this method nor
// the adapter decides whether the task passed.
func (s *streamState) succeeded() bool {
	if s.result == nil {
		return false
	}
	if s.errRecord != "" || s.finishedErr != "" || s.agentError != "" {
		return false
	}
	return !s.canceled
}

// runUsage prefers Iris's aggregate summary (which includes compaction and
// retries), then the result record, then the sum of per-call usage events.
// Unavailable usage stays nil rather than becoming zero.
func (s *streamState) runUsage() *events.Usage {
	if s.summary != nil && !s.summary.Usage.zero() {
		return s.summary.Usage.event()
	}
	if s.result != nil && !s.result.Usage.zero() {
		return s.result.Usage.event()
	}
	if s.usage.seen {
		return &events.Usage{
			InputTokens:  int64Ptr(s.usage.prompt),
			OutputTokens: int64Ptr(s.usage.completion),
			TotalTokens:  int64Ptr(s.usage.total),
		}
	}
	return nil
}

func (s *streamState) metadata(command, baseURL string) adapterMetadata {
	meta := adapterMetadata{
		Command:        command,
		BaseURL:        baseURL,
		SessionID:      s.sessionID,
		SessionBaseURL: s.sessionBaseURL,
		Records:        s.records,
		ParseErrors:    s.parseErrors,
		ResultRecord:   s.result != nil,
		ToolCalls:      s.toolCalls,
		ToolFailures:   s.toolFailures,
	}
	if s.summary != nil {
		meta.LLMRequests = s.summary.LLMRequests
		meta.ToolExecutions = s.summary.ToolExecutions
		meta.FilesChanged = len(s.summary.FilesChanged)
	}
	if s.result != nil {
		meta.Truncated = s.result.Truncated
	}
	if s.rawErr != nil {
		meta.RawOutputError = s.rawErr.Error()
	}
	if len(s.unrecognized) > 0 {
		names := make([]string, 0, len(s.unrecognized))
		for name := range s.unrecognized {
			names = append(names, name)
		}
		sort.Strings(names)
		meta.UnrecognizedEvents = names
	}
	return meta
}

func (s *streamState) failureReason(waitErr, ctxErr, parseErr error, stderrPath string) string {
	switch {
	case ctxErr != nil && errors.Is(ctxErr, context.DeadlineExceeded):
		return "iris: attempt deadline exceeded"
	case ctxErr != nil:
		return "iris: run cancelled"
	case s.errRecord != "":
		return "iris: " + s.errRecord
	case s.finishedErr != "":
		return "iris: " + s.finishedErr
	case s.agentError != "":
		return "iris: " + s.agentError
	case s.canceled:
		return "iris: run reported cancellation"
	case parseErr != nil:
		return "iris: " + parseErr.Error()
	case s.result == nil:
		message := "iris: no result record on stdout"
		if first := firstNonEmptyLine(stderrPath); first != "" {
			message += "; stderr: " + first
		}
		if s.parseErrors > 0 {
			message += fmt.Sprintf(" (%d unparsable line(s); first: %s)", s.parseErrors, s.firstParseErr)
		}
		return message
	case waitErr != nil:
		return "iris: " + waitErr.Error()
	default:
		return "iris: run did not produce a successful result"
	}
}
