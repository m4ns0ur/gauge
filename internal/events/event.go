// Package events defines Gauge's normalized agent event vocabulary and the
// per-attempt JSONL recorder used by the runner.
//
// Adapters translate whatever telemetry their agent exposes into these events.
// An adapter that cannot observe a signal must omit it rather than invent it.
package events

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the version of the normalized event envelope.
const SchemaVersion = 1

// Type is a normalized event kind.
type Type string

const (
	// TypeAgentMessage is assistant or agent-authored text.
	TypeAgentMessage Type = "agent.message"
	// TypeAgentState is an assistant/agent state change where available.
	TypeAgentState Type = "agent.state"
	// TypeToolCall reports a tool invocation the agent made.
	TypeToolCall Type = "tool.call"
	// TypeToolResult reports the observed result of a tool call.
	TypeToolResult Type = "tool.result"
	// TypeUsage reports token or cost usage from a trustworthy source.
	TypeUsage Type = "usage"
	// TypeWarning reports a non-fatal problem.
	TypeWarning Type = "warning"
	// TypeError reports an error surfaced by the agent or adapter.
	TypeError Type = "error"
	// TypeLifecycle reports process/attempt lifecycle transitions.
	TypeLifecycle Type = "lifecycle"
)

// Valid reports whether t is a known normalized event type.
func (t Type) Valid() bool {
	switch t {
	case TypeAgentMessage, TypeAgentState, TypeToolCall, TypeToolResult,
		TypeUsage, TypeWarning, TypeError, TypeLifecycle:
		return true
	}
	return false
}

// Event is one normalized observation recorded during an attempt.
type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Seq           int             `json:"seq"`
	Time          time.Time       `json:"time"`
	Source        string          `json:"source"`
	Type          Type            `json:"type"`
	Text          string          `json:"text,omitempty"`
	State         string          `json:"state,omitempty"`
	Tool          *Tool           `json:"tool,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
	Error         string          `json:"error,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

// Tool describes a tool call or its result.
type Tool struct {
	CallID string          `json:"call_id,omitempty"`
	Name   string          `json:"name,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Failed bool            `json:"failed,omitempty"`
}

// Usage holds usage values reported by a trustworthy source. Nil fields are
// unavailable and must not be rendered or aggregated as zero.
type Usage struct {
	InputTokens  *int64   `json:"input_tokens,omitempty"`
	OutputTokens *int64   `json:"output_tokens,omitempty"`
	TotalTokens  *int64   `json:"total_tokens,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
}

// Add returns the element-wise sum of u and o, treating unavailable values as
// unavailable. It is used to aggregate usage across attempts.
func (u *Usage) Add(o *Usage) *Usage {
	if u == nil && o == nil {
		return nil
	}
	out := &Usage{}
	if u != nil {
		out.InputTokens = u.InputTokens
		out.OutputTokens = u.OutputTokens
		out.TotalTokens = u.TotalTokens
		out.CostUSD = u.CostUSD
	}
	if o == nil {
		return out
	}
	out.InputTokens = sumInt64(out.InputTokens, o.InputTokens)
	out.OutputTokens = sumInt64(out.OutputTokens, o.OutputTokens)
	out.TotalTokens = sumInt64(out.TotalTokens, o.TotalTokens)
	out.CostUSD = sumFloat64(out.CostUSD, o.CostUSD)
	return out
}

func sumInt64(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	return &v
}

func sumFloat64(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	return &v
}
