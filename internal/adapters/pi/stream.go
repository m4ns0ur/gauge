package pi

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
	"unicode/utf8"

	"github.com/m4ns0ur/gauge/internal/events"
)

// usagePayload mirrors Pi's assistant-message usage. Pi reports cache reads and
// writes separately; totalTokens includes them, while input is the uncached
// prompt side.
type usagePayload struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
	Cost        *struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

func (u *usagePayload) zero() bool {
	if u == nil {
		return true
	}
	return u.Input == 0 && u.Output == 0 && u.TotalTokens == 0 && (u.Cost == nil || u.Cost.Total == 0)
}

func (u *usagePayload) event() *events.Usage {
	out := &events.Usage{
		InputTokens:  int64Ptr(u.Input),
		OutputTokens: int64Ptr(u.Output),
		TotalTokens:  int64Ptr(u.TotalTokens),
	}
	if u.Cost != nil && u.Cost.Total > 0 {
		out.CostUSD = float64Ptr(u.Cost.Total)
	}
	return out
}

// contentBlock is one entry of a message's content array.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// wireMessage is the subset of Pi's AgentMessage shape Gauge needs. Content is
// raw because system messages use a string while user and assistant messages
// use a block array.
type wireMessage struct {
	Role          string          `json:"role"`
	Content       json.RawMessage `json:"content"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	ResponseModel string          `json:"responseModel"`
	ResponseID    string          `json:"responseId"`
	StopReason    string          `json:"stopReason"`
	ErrorMessage  string          `json:"errorMessage"`
	Usage         *usagePayload   `json:"usage"`
}

// adapterMetadata is the machine-readable record of what the adapter observed.
// Unavailable signals are omitted rather than zero-filled.
type adapterMetadata struct {
	Command            string   `json:"command"`
	Provider           string   `json:"provider,omitempty"`
	Model              string   `json:"model,omitempty"`
	Thinking           string   `json:"thinking,omitempty"`
	Tools              []string `json:"tools,omitempty"`
	SessionID          string   `json:"session_id,omitempty"`
	SessionVersion     int      `json:"session_version,omitempty"`
	StopReason         string   `json:"stop_reason,omitempty"`
	Settled            bool     `json:"settled"`
	Aborted            bool     `json:"aborted,omitempty"`
	Truncated          bool     `json:"truncated,omitempty"`
	ResponseID         string   `json:"response_id,omitempty"`
	Records            int      `json:"records,omitempty"`
	ParseErrors        int      `json:"parse_errors,omitempty"`
	UnrecognizedEvents []string `json:"unrecognized_events,omitempty"`
	RawOutputError     string   `json:"raw_output_error,omitempty"`
	AutoRetries        int      `json:"auto_retries,omitempty"`
	Compactions        int      `json:"compactions,omitempty"`
	ToolCalls          int      `json:"tool_calls,omitempty"`
	ToolFailures       int      `json:"tool_failures,omitempty"`
	ModelsFileSHA256   string   `json:"models_file_sha256,omitempty"`
	AuthFileInstalled  bool     `json:"auth_file_installed,omitempty"`
}

type usageTotals struct {
	input  int64
	output int64
	total  int64
	cost   float64
	seen   bool
}

// streamState translates one Pi JSONL stream into normalized events and the
// facts Gauge persists about the run.
type streamState struct {
	emit func(events.Event)

	records       int
	parseErrors   int
	firstParseErr string
	unrecognized  map[string]int

	sessionID      string
	sessionVersion int

	settled         bool
	aborted         bool
	stopReason      string
	errorMessage    string
	retryFinalError string
	provider        string
	model           string
	responseID      string

	autoRetries  int
	compactions  int
	toolCalls    int
	toolFailures int
	usage        usageTotals

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
// translating known records. Pi framing is LF-delimited: Unicode line
// separators may appear inside JSON strings, so lines are split only on LF.
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

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &head); err != nil {
		s.parseFailure(err)
		return
	}

	switch head.Type {
	case "":
		s.parseFailure(fmt.Errorf("record without a type"))

	case "session":
		var event struct {
			Version int    `json:"version"`
			ID      string `json:"id"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.sessionID = event.ID
		s.sessionVersion = event.Version

	case "message_end":
		var event struct {
			Message wireMessage `json:"message"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.handleMessage(event.Message)

	case "tool_execution_start":
		var event struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Args       json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.toolCalls++
		s.emit(events.Event{
			Type: events.TypeToolCall,
			Tool: &events.Tool{
				CallID: event.ToolCallID,
				Name:   event.ToolName,
				Input:  truncateJSON(event.Args, maxToolInput),
			},
		})

	case "tool_execution_end":
		var event struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Result     json.RawMessage `json:"result"`
			IsError    bool            `json:"isError"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		if event.IsError {
			s.toolFailures++
		}
		s.emit(events.Event{
			Type: events.TypeToolResult,
			Tool: &events.Tool{
				CallID: event.ToolCallID,
				Name:   event.ToolName,
				Output: truncateText(toolResultText(event.Result), maxToolOutput),
				Failed: event.IsError,
			},
		})

	case "turn_start":
		s.emit(events.Event{Type: events.TypeAgentState, State: "turn_started"})

	case "turn_end":
		s.emit(events.Event{Type: events.TypeAgentState, State: "turn_finished"})

	case "auto_retry_start":
		var event struct {
			Attempt      int    `json:"attempt"`
			MaxAttempts  int    `json:"maxAttempts"`
			ErrorMessage string `json:"errorMessage"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.autoRetries = max(s.autoRetries, event.Attempt)
		text := fmt.Sprintf("auto retry %d/%d", event.Attempt, event.MaxAttempts)
		if event.ErrorMessage != "" {
			text += ": " + truncateText(event.ErrorMessage, 300)
		}
		s.emit(events.Event{Type: events.TypeWarning, Text: text})

	case "auto_retry_end":
		var event struct {
			Success    bool   `json:"success"`
			FinalError string `json:"finalError"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		if !event.Success && event.FinalError != "" {
			s.retryFinalError = event.FinalError
		}

	case "compaction_start":
		s.compactions++
		s.emit(events.Event{Type: events.TypeAgentState, State: "context_compaction_started"})

	case "compaction_end":
		s.emit(events.Event{Type: events.TypeAgentState, State: "context_compaction_finished"})

	case "agent_settled":
		var event struct {
			Aborted bool `json:"aborted"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.settled = true
		s.aborted = event.Aborted
		lifecycle := events.Event{Type: events.TypeLifecycle, State: "agent_settled"}
		if event.Aborted {
			lifecycle.Error = "pi reported an aborted run"
		}
		s.emit(lifecycle)

	case "extension_error":
		var event struct {
			ExtensionPath string `json:"extensionPath"`
			Event         string `json:"event"`
			Error         string `json:"error"`
		}
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.parseFailure(err)
			return
		}
		s.emit(events.Event{
			Type:  events.TypeWarning,
			Text:  fmt.Sprintf("extension %s failed handling %s: %s", event.ExtensionPath, event.Event, event.Error),
			Error: event.Error,
		})

	case "agent_start", "agent_end", "message_start", "message_update",
		"tool_execution_update", "queue_update", "entry_appended",
		"session_info_changed", "thinking_level_changed",
		"summarization_retry_scheduled", "summarization_retry_attempt_start",
		"summarization_retry_finished", "bash_execution_update":
		// Preserved in stdout.log but omitted from normalized events: these are
		// streaming deltas, queue bookkeeping, or per-message progress that
		// message_end and tool events already summarize.

	default:
		s.unrecognized[head.Type]++
	}
}

// handleMessage records an assistant message. Its content is authoritative
// (streaming deltas are not used), and its usage and stop reason are the only
// reliable source for run-level telemetry in JSON mode.
func (s *streamState) handleMessage(msg wireMessage) {
	if msg.Role != "assistant" {
		return
	}
	if text := contentText(msg.Content); strings.TrimSpace(text) != "" {
		s.emit(events.Event{Type: events.TypeAgentMessage, Text: text})
	}
	if msg.StopReason != "" {
		s.stopReason = msg.StopReason
	}
	s.errorMessage = strings.TrimSpace(msg.ErrorMessage)
	if msg.Provider != "" {
		s.provider = msg.Provider
	}
	if msg.ResponseModel != "" {
		s.model = msg.ResponseModel
	} else if msg.Model != "" {
		s.model = msg.Model
	}
	if msg.ResponseID != "" {
		s.responseID = msg.ResponseID
	}
	if !msg.Usage.zero() {
		s.usage.input += msg.Usage.Input
		s.usage.output += msg.Usage.Output
		s.usage.total += msg.Usage.TotalTokens
		if msg.Usage.Cost != nil {
			s.usage.cost += msg.Usage.Cost.Total
		}
		s.usage.seen = true
		s.emit(events.Event{Type: events.TypeUsage, Usage: msg.Usage.event()})
	}
}

func (s *streamState) parseFailure(err error) {
	s.parseErrors++
	if s.firstParseErr == "" {
		s.firstParseErr = err.Error()
	}
}

// succeeded reports whether the stream itself shows a clean run. Pi can exit 0
// after an error response, so the exit code alone is not evidence. Whether the
// task passed is still the evaluator's decision.
func (s *streamState) succeeded() bool {
	if !s.settled || s.aborted {
		return false
	}
	return s.stopReason == "stop" || s.stopReason == "length"
}

func (s *streamState) truncated() bool { return s.stopReason == "length" }

// runUsage sums assistant-message usage. Cache tokens are part of Pi's
// totalTokens but not of the uncached input count, so total may exceed
// input+output. Cost is only recorded when Pi computes a non-zero estimate.
func (s *streamState) runUsage() *events.Usage {
	if !s.usage.seen {
		return nil
	}
	out := &events.Usage{
		InputTokens:  int64Ptr(s.usage.input),
		OutputTokens: int64Ptr(s.usage.output),
		TotalTokens:  int64Ptr(s.usage.total),
	}
	if s.usage.cost > 0 {
		out.CostUSD = float64Ptr(s.usage.cost)
	}
	return out
}

func (s *streamState) metadata(command string, cfg Config) adapterMetadata {
	meta := adapterMetadata{
		Command:        command,
		Provider:       s.provider,
		Model:          s.model,
		Thinking:       cfg.Thinking,
		Tools:          cfg.Tools,
		SessionID:      s.sessionID,
		SessionVersion: s.sessionVersion,
		StopReason:     s.stopReason,
		Settled:        s.settled,
		Aborted:        s.aborted,
		Truncated:      s.truncated(),
		ResponseID:     s.responseID,
		Records:        s.records,
		ParseErrors:    s.parseErrors,
		AutoRetries:    s.autoRetries,
		Compactions:    s.compactions,
		ToolCalls:      s.toolCalls,
		ToolFailures:   s.toolFailures,
	}
	if meta.Provider == "" {
		meta.Provider = cfg.Provider
	}
	if meta.Model == "" {
		meta.Model = cfg.Model
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
		return "pi: attempt deadline exceeded"
	case ctxErr != nil:
		return "pi: run cancelled"
	case parseErr != nil:
		return "pi: " + parseErr.Error()
	case s.stopReason == "error":
		return "pi: " + s.errorText("the provider reported an error")
	case s.stopReason == "aborted":
		return "pi: the response was aborted" + s.errorSuffix()
	case s.stopReason == "deferred":
		return "pi: the provider returned a deferred response that Gauge cannot resume"
	case s.stopReason == "toolUse" || s.stopReason == "pending":
		return fmt.Sprintf("pi: run ended with stop reason %q before a final response", s.stopReason)
	case s.aborted:
		return "pi: run reported aborted"
	case !s.settled:
		message := "pi: agent_settled was not observed"
		if first := firstNonEmptyLine(stderrPath); first != "" {
			message += "; stderr: " + first
		}
		if s.retryFinalError != "" {
			message += "; " + truncateText(s.retryFinalError, 300)
		}
		return message
	case waitErr != nil:
		return "pi: " + waitErr.Error()
	default:
		return "pi: run did not produce a successful result"
	}
}

func (s *streamState) errorText(fallback string) string {
	for _, candidate := range []string{s.errorMessage, s.retryFinalError} {
		if strings.TrimSpace(candidate) != "" {
			return truncateText(candidate, 1000)
		}
	}
	return fallback
}

func (s *streamState) errorSuffix() string {
	if strings.TrimSpace(s.errorMessage) == "" {
		return ""
	}
	return ": " + truncateText(s.errorMessage, 1000)
}

// contentText extracts assistant text from a message content value. System
// messages carry a string; user and assistant messages carry a block array.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

// toolResultText renders a tool result's content blocks, falling back to the
// raw JSON when the result has no text content.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var result struct {
		Content []contentBlock `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err == nil && len(result.Content) > 0 {
		var builder strings.Builder
		for _, block := range result.Content {
			if block.Type != "text" || block.Text == "" {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(block.Text)
		}
		if builder.Len() > 0 {
			return builder.String()
		}
	}
	return string(raw)
}

func int64Ptr(value int64) *int64 { return &value }

func float64Ptr(value float64) *float64 { return &value }

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

// truncateJSON bounds a JSON payload copied into a normalized event. A
// truncated payload becomes a JSON string ending in "…(truncated)"; the raw
// payload stays in stdout.log.
func truncateJSON(raw json.RawMessage, limit int) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if len(raw) <= limit {
		return raw
	}
	quoted, err := json.Marshal(truncateText(string(raw), limit))
	if err != nil {
		return nil
	}
	return quoted
}
