package metrics

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/m4ns0ur/gauge/internal/events"
)

// Behavior summarizes agent activity reconstructed from normalized event logs.
// Fields that no log can answer stay absent: Gauge does not infer behavior for
// agents that do not expose events.
type Behavior struct {
	ToolCalls    int `json:"tool_calls"`
	ToolResults  int `json:"tool_results"`
	ToolFailures int `json:"tool_failures"`
	Events       int `json:"events"`
	// AttemptsWithEvents is the number of attempts whose event log could be
	// read; Attempts is the number of attempts considered. Together they are the
	// coverage denominator for the tool counters.
	AttemptsWithEvents int `json:"attempts_with_events"`
	Attempts           int `json:"attempts"`
}

// ReadBehavior counts tool activity in one normalized event log.
func ReadBehavior(path string) (Behavior, error) {
	file, err := os.Open(path)
	if err != nil {
		return Behavior{}, err
	}
	defer file.Close()
	return readBehavior(file)
}

func readBehavior(r io.Reader) (Behavior, error) {
	var behavior Behavior
	reader := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && strings.TrimSpace(string(line)) != "" {
			var event events.Event
			if err := json.Unmarshal(line, &event); err != nil {
				return behavior, fmt.Errorf("metrics: parse event log: %w", err)
			}
			behavior.Events++
			switch event.Type {
			case events.TypeToolCall:
				behavior.ToolCalls++
			case events.TypeToolResult:
				behavior.ToolResults++
				if event.Tool != nil && event.Tool.Failed {
					behavior.ToolFailures++
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return behavior, nil
			}
			return behavior, fmt.Errorf("metrics: read event log: %w", err)
		}
	}
}
