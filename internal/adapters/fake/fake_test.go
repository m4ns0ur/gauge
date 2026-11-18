package fake_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/adapters/fake"
	"github.com/m4ns0ur/gauge/internal/events"
)

func request(t *testing.T, cfg fake.Config) adapter.RunRequest {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return adapter.RunRequest{
		RunID:        "run-1",
		TaskID:       "task-1",
		Prompt:       "do the thing",
		WorkDir:      t.TempDir(),
		ArtifactsDir: t.TempDir(),
		Timeout:      time.Second,
		Config:       raw,
	}
}

func TestValidate(t *testing.T) {
	impl := fake.New()
	valid := []string{
		`{}`,
		`{"mode":"noop"}`,
		`{"mode":"write","files":{"a.txt":"x"},"events":[{"type":"agent.message","text":"hi"}]}`,
		`{"mode":"sleep","sleep":"10ms","exit_code":2}`,
	}
	for _, cfg := range valid {
		if err := impl.Validate(context.Background(), json.RawMessage(cfg)); err != nil {
			t.Errorf("Validate(%s) = %v", cfg, err)
		}
	}
	invalid := []string{
		`{"mode":"teleport"}`,
		`{"unknown":true}`,
		`{"files":{"../escape.txt":"x"}}`,
		`{"files":{"/tmp/escape.txt":"x"}}`,
		`{"sleep":"soon"}`,
		`{"sleep":"-1s"}`,
		`{"exit_code":999}`,
		`{"events":[{"type":"nonsense"}]}`,
	}
	for _, cfg := range invalid {
		if err := impl.Validate(context.Background(), json.RawMessage(cfg)); err == nil {
			t.Errorf("Validate(%s) succeeded, want error", cfg)
		}
	}
}

func TestRunWritesFilesAndEmitsEvents(t *testing.T) {
	req := request(t, fake.Config{
		Mode:  fake.ModeWrite,
		Files: map[string]string{"nested/answer.txt": "42"},
		Events: []fake.Event{
			{Type: events.TypeAgentMessage, Text: "working"},
			{Type: events.TypeToolCall, Tool: &events.Tool{Name: "write", CallID: "1"}},
		},
		Usage: &events.Usage{InputTokens: int64ptr(3)},
	})
	var recorded []events.Event
	result, err := fake.New().Run(context.Background(), req, func(ev events.Event) {
		recorded = append(recorded, ev)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Completion != adapter.Completed || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("result = %+v", result)
	}
	if got := readFile(t, filepath.Join(req.WorkDir, "nested", "answer.txt")); got != "42" {
		t.Errorf("written file = %q", got)
	}
	if len(recorded) < 4 {
		t.Errorf("recorded %d events: %+v", len(recorded), recorded)
	}
	types := make(map[events.Type]bool)
	for _, ev := range recorded {
		types[ev.Type] = true
	}
	if !types[events.TypeAgentMessage] || !types[events.TypeToolCall] || !types[events.TypeLifecycle] {
		t.Errorf("event types = %v", types)
	}
	if result.Usage == nil || *result.Usage.InputTokens != 3 {
		t.Errorf("usage = %+v", result.Usage)
	}
	if _, err := os.Stat(filepath.Join(req.ArtifactsDir, "stdout.log")); err != nil {
		t.Errorf("stdout artifact missing: %v", err)
	}
}

func TestRunFailureModes(t *testing.T) {
	req := request(t, fake.Config{Mode: fake.ModeFail, ErrorMessage: "cannot proceed"})
	result, err := fake.New().Run(context.Background(), req, func(events.Event) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Completion != adapter.Failed {
		t.Errorf("completion = %s", result.Completion)
	}
	if result.Error != "cannot proceed" {
		t.Errorf("error = %q", result.Error)
	}
	if result.ExitCode == nil || *result.ExitCode != 1 {
		t.Errorf("exit code = %v", result.ExitCode)
	}

	invalid := adapter.RunRequest{Config: json.RawMessage(`{"mode":"nope"}`), WorkDir: t.TempDir()}
	result, err = fake.New().Run(context.Background(), invalid, func(events.Event) {})
	if err != nil {
		t.Fatalf("invalid config should be a failed result, not an error: %v", err)
	}
	if result.Completion != adapter.Failed || result.Error == "" {
		t.Errorf("result = %+v", result)
	}
}

func TestRunHonorsCancellationAndDeadline(t *testing.T) {
	req := request(t, fake.Config{Mode: fake.ModeSleep, Sleep: "30s"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := fake.New().Run(ctx, req, func(events.Event) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Completion != adapter.TimedOut {
		t.Errorf("completion = %s", result.Completion)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("fake adapter ignored the deadline")
	}

	req = request(t, fake.Config{Mode: fake.ModeSleep, Sleep: "30s"})
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	result, err = fake.New().Run(ctx, req, func(events.Event) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Completion != adapter.Cancelled {
		t.Errorf("completion = %s", result.Completion)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func int64ptr(v int64) *int64 { return &v }

func TestDescription(t *testing.T) {
	if desc := fake.New().Description(); !strings.Contains(desc, "deterministic") {
		t.Errorf("description = %q", desc)
	}
}
