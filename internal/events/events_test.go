package events_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/events"
)

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func TestRecorderAssignsEnvelope(t *testing.T) {
	var buf bytes.Buffer
	recorder := events.NewRecorder(nopCloser{&buf}, "fake")
	recorder.Emit(events.Event{Type: events.TypeAgentMessage, Text: "one"})
	recorder.Emit(events.Event{Type: events.TypeToolCall, Tool: &events.Tool{Name: "read"}})
	if err := recorder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	scanner := bufio.NewScanner(&buf)
	var recorded []events.Event
	for scanner.Scan() {
		var ev events.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", scanner.Text(), err)
		}
		recorded = append(recorded, ev)
	}
	if len(recorded) != 2 {
		t.Fatalf("recorded %d events", len(recorded))
	}
	for i, ev := range recorded {
		if ev.SchemaVersion != events.SchemaVersion {
			t.Errorf("event %d schema = %d", i, ev.SchemaVersion)
		}
		if ev.Seq != i+1 {
			t.Errorf("event %d seq = %d", i, ev.Seq)
		}
		if ev.Source != "fake" {
			t.Errorf("event %d source = %q", i, ev.Source)
		}
		if ev.Time.IsZero() {
			t.Errorf("event %d has no timestamp", i)
		}
	}
	if recorded[0].Text != "one" || recorded[1].Tool == nil || recorded[1].Tool.Name != "read" {
		t.Errorf("events = %+v", recorded)
	}
}

func TestRecorderRejectsUnknownTypes(t *testing.T) {
	var buf bytes.Buffer
	recorder := events.NewRecorder(nopCloser{&buf}, "fake")
	recorder.Emit(events.Event{Type: "mystery"})
	if recorder.Err() == nil {
		t.Fatal("expected an error for an unknown event type")
	}
	if buf.Len() != 0 {
		t.Errorf("invalid event was written: %s", buf.String())
	}
	// Emit after an error is a no-op.
	recorder.Emit(events.Event{Type: events.TypeWarning})
	if buf.Len() != 0 {
		t.Errorf("recorder continued after an error: %s", buf.String())
	}
}

func TestRecorderConcurrentEmit(t *testing.T) {
	var buf bytes.Buffer
	recorder := events.NewRecorder(nopCloser{&buf}, "fake")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder.Emit(events.Event{Type: events.TypeAgentMessage, Text: "concurrent"})
		}()
	}
	wg.Wait()
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	lines := 0
	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		var ev events.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("corrupt line: %v", err)
		}
		lines++
	}
	if lines != 20 {
		t.Fatalf("recorded %d lines, want 20", lines)
	}
}

func TestUsageAdd(t *testing.T) {
	v1, v2 := int64(5), int64(7)
	cost1, cost2 := 0.10, 0.25
	first := &events.Usage{InputTokens: &v1, TotalTokens: nil, CostUSD: &cost1}
	second := &events.Usage{InputTokens: &v2, OutputTokens: nil, CostUSD: &cost2}
	sum := first.Add(second)
	if sum.InputTokens == nil || *sum.InputTokens != 12 {
		t.Errorf("input = %v", sum.InputTokens)
	}
	if sum.OutputTokens != nil {
		t.Errorf("output should be unavailable, got %v", *sum.OutputTokens)
	}
	if sum.TotalTokens != nil {
		t.Errorf("total should be unavailable, got %v", *sum.TotalTokens)
	}
	if sum.CostUSD == nil || *sum.CostUSD != 0.35 {
		t.Errorf("cost = %v", sum.CostUSD)
	}
	if nilUsage := (*events.Usage)(nil).Add(nil); nilUsage != nil {
		t.Errorf("nil + nil = %+v", nilUsage)
	}
	_ = time.Now
}
