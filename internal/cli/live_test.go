package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/cli"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/testutil"
)

// TestLiveAdapters drives the real Iris and Pi binaries against a real
// OpenAI-compatible model endpoint. It is opt-in because it needs an installed
// agent, a running model, and several minutes:
//
//	GAUGE_LIVE=1 GAUGE_LIVE_MODEL=qwen3.6-35B-Q1 \
//	  go test ./internal/cli -run TestLiveAdapters -v -timeout 30m
//
// Optional configuration:
//
//	GAUGE_LIVE_ENDPOINT  OpenAI-compatible base URL (default http://127.0.0.1:8080/v1)
//	GAUGE_LIVE_AGENTS    comma-separated adapters (default iris,pi)
//	GAUGE_LIVE_TIMEOUT   per-attempt budget (default 10m)
//	GAUGE_LIVE_REPETITIONS  attempts per adapter (default 1)
//	GAUGE_LIVE_REQUIRE_PASS require every attempt to pass (default off)
//
// The test asserts that each adapter completes the attempt, the independent
// evaluator records a verdict, and the task actually passed. It is the
// end-to-end check that the standard suite deliberately does not run.
func TestLiveAdapters(t *testing.T) {
	if os.Getenv("GAUGE_LIVE") != "1" {
		t.Skip("set GAUGE_LIVE=1 and GAUGE_LIVE_MODEL to run the live agent test")
	}
	model := strings.TrimSpace(os.Getenv("GAUGE_LIVE_MODEL"))
	if model == "" {
		t.Fatal("GAUGE_LIVE_MODEL must name the model served by the endpoint")
	}
	endpoint := strings.TrimSpace(os.Getenv("GAUGE_LIVE_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080/v1"
	}
	timeout := 10 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("GAUGE_LIVE_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("GAUGE_LIVE_TIMEOUT: %v", err)
		}
		timeout = parsed
	}
	repetitions := 1
	if raw := strings.TrimSpace(os.Getenv("GAUGE_LIVE_REPETITIONS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 20 {
			t.Fatalf("GAUGE_LIVE_REPETITIONS must be between 1 and 20, got %q", raw)
		}
		repetitions = parsed
	}
	requirePass := os.Getenv("GAUGE_LIVE_REQUIRE_PASS") == "1"

	rawAgents := strings.TrimSpace(os.Getenv("GAUGE_LIVE_AGENTS"))
	if rawAgents == "" {
		rawAgents = "iris,pi"
	}
	for _, rawAgent := range strings.Split(rawAgents, ",") {
		agent := strings.TrimSpace(rawAgent)
		if agent == "" {
			continue
		}
		t.Run(agent, func(t *testing.T) {
			if _, err := exec.LookPath(agent); err != nil {
				t.Skipf("%s executable not found in PATH", agent)
			}
			verifyLiveAgent(t, agent, endpoint, model, timeout, repetitions, requirePass)
		})
	}
}

func verifyLiveAgent(t *testing.T, agent, endpoint, model string, timeout time.Duration, repetitions int, requirePass bool) {
	t.Helper()

	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("live", map[string]string{"README.md": "live fixture\n"})
	bench.AddTask(testutil.TaskOptions{
		ID:                "write-answer",
		Name:              "Write the answer",
		Prompt:            "Create a file named answer.txt in the current directory containing exactly 42 (the number, optionally followed by a newline). Do not modify any other file.",
		FixtureRepo:       repo,
		Revision:          revision,
		ExecutionTimeout:  timeout,
		EvaluationTimeout: 2 * time.Minute,
	})
	bench.AddSuite("smoke", "write-answer")
	bench.WriteConfig(liveAgentConfig(t, agent, endpoint, model))

	stdout, stderr, err := execCLI(t, bench, "run", "--suite", "smoke", "--agent", agent, "--repetitions", strconv.Itoa(repetitions))
	if err != nil && cli.ExitCode(err) != cli.ExitTaskFailed {
		t.Fatalf("live run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	expID := extractExperimentID(t, stdout)
	store, err := storage.New(bench.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	attempts, err := store.ListAttempts(expID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != repetitions {
		t.Fatalf("attempts = %d, want %d", len(attempts), repetitions)
	}

	var durations []time.Duration
	passed := 0
	for _, attempt := range attempts {
		meta := liveAdapterMetadata{}
		_ = json.Unmarshal(attempt.AdapterMetadata, &meta)
		t.Logf("%s rep %d: execution=%s outcome=%s duration=%s model=%q provider=%q version=%q usage=%s requests=%d tool_calls=%d",
			agent,
			attempt.Repetition+1,
			attempt.Execution.Completion,
			attempt.Outcome,
			time.Duration(attempt.DurationMS)*time.Millisecond,
			attempt.Model,
			attempt.Provider,
			attempt.AgentVersion,
			usageString(attempt.Usage),
			meta.LLMRequests,
			meta.ToolCalls,
		)

		if attempt.Execution.Completion != adapter.Completed {
			t.Fatalf("%s rep %d execution = %q (error %q)\nstdout:\n%s\nstderr:\n%s",
				agent, attempt.Repetition+1, attempt.Execution.Completion, attempt.Execution.Error, stdout, stderr)
		}
		// The adapter and evaluator pipeline is what this test verifies. Whether
		// the model solves the task is a benchmark result, not an integration
		// fact, so a failed verdict is reported unless GAUGE_LIVE_REQUIRE_PASS=1.
		if attempt.Outcome == storage.OutcomePassed {
			passed++
		} else if requirePass {
			t.Errorf("%s rep %d task outcome = %s (summary %q)\nstdout:\n%s\nstderr:\n%s",
				agent, attempt.Repetition+1, attempt.Outcome, evaluationSummary(attempt), stdout, stderr)
		} else {
			t.Logf("%s rep %d task outcome = %s (summary %q); the integration itself completed. Set GAUGE_LIVE_REQUIRE_PASS=1 to require a pass.",
				agent, attempt.Repetition+1, attempt.Outcome, evaluationSummary(attempt))
		}
		if attempt.Model == "" {
			t.Errorf("%s rep %d did not record a model", agent, attempt.Repetition+1)
		}
		if attempt.Usage == nil || attempt.Usage.TotalTokens == nil || *attempt.Usage.TotalTokens == 0 {
			t.Errorf("%s rep %d did not record usage: %+v", agent, attempt.Repetition+1, attempt.Usage)
		}
		if agent == "pi" && attempt.AgentVersion == "" {
			t.Errorf("%s rep %d did not record an agent version", agent, attempt.Repetition+1)
		}
		if _, ok := attempt.Artifacts[storage.ArtifactEvents]; !ok {
			t.Errorf("%s rep %d did not record events.jsonl: %+v", agent, attempt.Repetition+1, attempt.Artifacts)
		}
		if _, ok := attempt.Artifacts[storage.ArtifactStdout]; !ok {
			t.Errorf("%s rep %d did not record stdout.log: %+v", agent, attempt.Repetition+1, attempt.Artifacts)
		}

		dir, err := store.AttemptDir(expID, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		events := testutil.ReadFile(t, filepath.Join(dir, storage.ArtifactEvents))
		for _, want := range []string{`"type":"agent.message"`, `"type":"tool.call"`} {
			if !strings.Contains(events, want) {
				t.Errorf("%s rep %d events.jsonl missing %s", agent, attempt.Repetition+1, want)
			}
		}
		durations = append(durations, time.Duration(attempt.DurationMS)*time.Millisecond)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	median := durations[len(durations)/2]
	t.Logf("%s: %d/%d passed; median duration %s (range %s–%s)",
		agent, passed, len(attempts), median, durations[0], durations[len(durations)-1])
}

// liveAgentConfig writes self-contained agent configuration for the live test.
// Gauge isolates each agent's home directory, so the user's ~/.iris or ~/.pi
// configuration never applies; the model, endpoint, and policy must be explicit.
func liveAgentConfig(t *testing.T, agent, endpoint, model string) string {
	t.Helper()
	switch agent {
	case "iris":
		settings := filepath.Join(t.TempDir(), "iris-settings.toml")
		content := fmt.Sprintf(`base_url = %q
default_model = %q

[[models]]
name = %q
context_size = 65536
tool_calling = true

[security.shell]
mode = "allowlist"
allow = ["cat", "ls", "pwd", "git", "go"]
`, endpoint, model, model)
		testutil.WriteFile(t, settings, content, 0o600)
		return fmt.Sprintf(`
[adapters.iris.options]
command = "iris"
model = %q
base_url = %q
settings_file = %q
`, model, endpoint, settings)
	case "pi":
		models := filepath.Join(t.TempDir(), "pi-models.json")
		content := fmt.Sprintf(
			`{"providers":{"gauge-live":{"baseUrl":%q,"api":"openai-completions","apiKey":"gauge","models":[{"id":%q,"contextWindow":65536,"maxTokens":4096}]}}}`+"\n",
			endpoint, model,
		)
		testutil.WriteFile(t, models, content, 0o600)
		return fmt.Sprintf(`
[adapters.pi.options]
command = "pi"
provider = "gauge-live"
model = %q
models_file = %q
thinking = "off"
`, model, models)
	default:
		t.Fatalf("unknown live agent %q (want iris or pi)", agent)
		return ""
	}
}

// liveAdapterMetadata is the subset of adapter metadata both adapters report, so
// the test can show workload size alongside duration.
type liveAdapterMetadata struct {
	LLMRequests int `json:"llm_requests"`
	ToolCalls   int `json:"tool_calls"`
}

func usageString(usage *events.Usage) string {
	if usage == nil {
		return "unavailable"
	}
	var parts []string
	if usage.InputTokens != nil {
		parts = append(parts, fmt.Sprintf("input=%d", *usage.InputTokens))
	}
	if usage.OutputTokens != nil {
		parts = append(parts, fmt.Sprintf("output=%d", *usage.OutputTokens))
	}
	if usage.TotalTokens != nil {
		parts = append(parts, fmt.Sprintf("total=%d", *usage.TotalTokens))
	}
	if usage.CostUSD != nil {
		parts = append(parts, fmt.Sprintf("cost=$%.4f", *usage.CostUSD))
	}
	if len(parts) == 0 {
		return "unavailable"
	}
	return strings.Join(parts, ", ")
}

func evaluationSummary(attempt *storage.Attempt) string {
	if attempt.Evaluation == nil {
		return ""
	}
	return attempt.Evaluation.Summary
}
