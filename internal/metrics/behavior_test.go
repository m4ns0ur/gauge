package metrics_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/m4ns0ur/gauge/internal/metrics"
)

func TestReadBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	content := `{"schema_version":1,"type":"tool.call","tool":{"call_id":"c1","name":"read"}}
{"schema_version":1,"type":"tool.result","tool":{"call_id":"c1","name":"read"}}
{"schema_version":1,"type":"tool.call","tool":{"call_id":"c2","name":"bash"}}
{"schema_version":1,"type":"tool.result","tool":{"call_id":"c2","name":"bash","failed":true}}
{"schema_version":1,"type":"agent.message","text":"hi"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	behavior, err := metrics.ReadBehavior(path)
	if err != nil {
		t.Fatalf("read behavior: %v", err)
	}
	if behavior.ToolCalls != 2 || behavior.ToolResults != 2 || behavior.ToolFailures != 1 {
		t.Errorf("tool counters = %+v", behavior)
	}
	if behavior.Events != 5 {
		t.Errorf("events = %d", behavior.Events)
	}
}

func TestReadBehaviorErrors(t *testing.T) {
	if _, err := metrics.ReadBehavior(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("expected missing-file error")
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := metrics.ReadBehavior(path); err == nil {
		t.Fatal("expected parse error")
	}
}
