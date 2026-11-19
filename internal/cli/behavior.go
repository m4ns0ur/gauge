package cli

import (
	"path/filepath"

	"github.com/m4ns0ur/gauge/internal/metrics"
	"github.com/m4ns0ur/gauge/internal/storage"
)

// loadBehavior aggregates tool activity from the attempts' normalized event
// logs. It returns nil when no log can be read, so reports say unavailable
// instead of claiming zero tool calls.
func loadBehavior(store *storage.Store, attempts []*storage.Attempt) *metrics.Behavior {
	if len(attempts) == 0 {
		return nil
	}
	aggregate := &metrics.Behavior{Attempts: len(attempts)}
	for _, attempt := range attempts {
		if _, ok := attempt.Artifacts[storage.ArtifactEvents]; !ok {
			continue
		}
		dir, err := store.AttemptDir(attempt.ExperimentID, attempt.ID)
		if err != nil {
			continue
		}
		behavior, err := metrics.ReadBehavior(filepath.Join(dir, storage.ArtifactEvents))
		if err != nil {
			continue
		}
		aggregate.ToolCalls += behavior.ToolCalls
		aggregate.ToolResults += behavior.ToolResults
		aggregate.ToolFailures += behavior.ToolFailures
		aggregate.Events += behavior.Events
		aggregate.AttemptsWithEvents++
	}
	if aggregate.AttemptsWithEvents == 0 {
		return nil
	}
	return aggregate
}
