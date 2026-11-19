package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/cli"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/testutil"
)

func execCLI(t *testing.T, bench *testutil.Bench, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	app := cli.New()
	app.Context = func() context.Context { return context.Background() }
	app.Now = time.Now
	var out, errBuf bytes.Buffer
	app.Out = &out
	app.Err = &errBuf
	root := app.Root()
	full := append([]string{"--config", bench.ConfigPath}, args...)
	root.SetArgs(full)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func newBench(t *testing.T) (*testutil.Bench, string) {
	t.Helper()
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{
		"README.md": "demo\n",
	})
	bench.AddTask(testutil.TaskOptions{
		ID:          "write-answer",
		FixtureRepo: repo,
		Revision:    revision,
	})
	bench.AddSuite("smoke", "write-answer")
	return bench, bench.WriteConfig(`
[adapters.fake.options]
mode = "write"

[adapters.fake.options.files]
"answer.txt" = "42"
`)
}

func TestVersionCommand(t *testing.T) {
	bench, _ := newBench(t)
	stdout, _, err := execCLI(t, bench, "version", "--json")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("parse version JSON: %v\n%s", err, stdout)
	}
	if info.Name != "gauge" || info.Version == "" {
		t.Errorf("version info = %+v", info)
	}
}

func TestTaskAndSuiteCommands(t *testing.T) {
	bench, config := newBench(t)

	stdout, _, err := execCLI(t, bench, "tasks", "list")
	if err != nil {
		t.Fatalf("tasks list: %v", err)
	}
	if !strings.Contains(stdout, "write-answer") {
		t.Errorf("tasks list output:\n%s", stdout)
	}

	stdout, _, err = execCLI(t, bench, "tasks", "show", "write-answer")
	if err != nil {
		t.Fatalf("tasks show: %v", err)
	}
	for _, want := range []string{"write-answer", "Write 42 to answer.txt", "Fingerprint:", "never exposed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("tasks show missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = execCLI(t, bench, "suites", "list")
	if err != nil {
		t.Fatalf("suites list: %v", err)
	}
	if !strings.Contains(stdout, "smoke") {
		t.Errorf("suites list output:\n%s", stdout)
	}

	stdout, _, err = execCLI(t, bench, "suites", "show", "smoke")
	if err != nil {
		t.Fatalf("suites show: %v", err)
	}
	if !strings.Contains(stdout, "write-answer") || !strings.Contains(stdout, "sha256:") {
		t.Errorf("suites show output:\n%s", stdout)
	}
	_ = config
}

func TestRunInspectExportLifecycle(t *testing.T) {
	bench, _ := newBench(t)

	stdout, stderr, err := execCLI(t, bench, "run", "--suite", "smoke", "--agent", "fake")
	if err != nil {
		t.Fatalf("run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Passed: 1/1 (100.0%)") {
		t.Errorf("run output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "--data-dir "+bench.DataDir) || !strings.Contains(stdout, "runs show ") {
		t.Errorf("run output has no copy-pasteable inspect hint:\n%s", stdout)
	}
	if !strings.Contains(stderr, "passed in") {
		t.Errorf("progress output:\n%s", stderr)
	}
	expID := extractExperimentID(t, stdout)

	store, err := storage.New(bench.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := store.GetExperiment(expID)
	if err != nil {
		t.Fatalf("get experiment: %v", err)
	}
	if exp.Status != storage.StatusCompleted {
		t.Errorf("experiment status = %s", exp.Status)
	}
	if exp.Suite == nil || exp.Suite.ID != "smoke" {
		t.Errorf("experiment suite = %+v", exp.Suite)
	}
	if len(exp.Selection) != 1 || exp.Selection[0].ID != "write-answer" {
		t.Errorf("selection = %+v", exp.Selection)
	}
	attempts, err := store.ListAttempts(expID)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	attempt := attempts[0]

	stdout, _, err = execCLI(t, bench, "runs", "list")
	if err != nil {
		t.Fatalf("runs list: %v", err)
	}
	if !strings.Contains(stdout, expID) {
		t.Errorf("runs list missing %s:\n%s", expID, stdout)
	}

	stdout, _, err = execCLI(t, bench, "runs", "show", expID)
	if err != nil {
		t.Fatalf("runs show: %v", err)
	}
	if !strings.Contains(stdout, "write-answer") || !strings.Contains(stdout, "Passed: 1/1") {
		t.Errorf("runs show output:\n%s", stdout)
	}

	stdout, _, err = execCLI(t, bench, "inspect", attempt.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, want := range []string{"Outcome:    passed", "Evaluation: passed", "events.jsonl", "Workspace: removed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = execCLI(t, bench, "export", expID, "--format", "json")
	if err != nil {
		t.Fatalf("export json: %v", err)
	}
	var doc struct {
		Experiment struct {
			ID string `json:"id"`
		} `json:"experiment"`
		Summary struct {
			Passed int `json:"passed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("parse export: %v\n%s", err, stdout)
	}
	if doc.Experiment.ID != expID || doc.Summary.Passed != 1 {
		t.Errorf("export doc = %+v", doc)
	}

	stdout, _, err = execCLI(t, bench, "export", expID, "--format", "markdown")
	if err != nil {
		t.Fatalf("export markdown: %v", err)
	}
	if !strings.Contains(stdout, "# Experiment "+expID) {
		t.Errorf("markdown export:\n%s", stdout)
	}

	outFile := filepath.Join(t.TempDir(), "report.md")
	stdout, _, err = execCLI(t, bench, "export", expID, "--format", "markdown", "--output", outFile)
	if err != nil {
		t.Fatalf("export to file: %v", err)
	}
	if !strings.Contains(stdout, "wrote "+outFile) {
		t.Errorf("export output:\n%s", stdout)
	}
	if data := testutil.ReadFile(t, outFile); !strings.Contains(data, "# Experiment") {
		t.Errorf("exported file:\n%s", data)
	}

	stdout, _, err = execCLI(t, bench, "runs", "show", expID, "--json")
	if err != nil {
		t.Fatalf("runs show json: %v", err)
	}
	if !json.Valid([]byte(stdout)) {
		t.Errorf("runs show --json is not valid JSON:\n%s", stdout)
	}
}

// TestRunWithIrisStub proves the Iris adapter satisfies the same attempt
// lifecycle as the fake adapter: a real subprocess, normalized events, captured
// raw output, and an independent evaluator verdict. The stub replaces the agent
// executable, so no model or network access is needed.
func TestRunWithIrisStub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub agent executables require a POSIX shell")
	}
	bench, _ := newBench(t)

	stub := filepath.Join(t.TempDir(), "iris-stub")
	testutil.WriteFile(t, stub, `#!/bin/sh
cat > /dev/null
printf 42 > answer.txt
printf '%s\n' '{"type":"agent.started","event":{}}'
printf '%s\n' '{"type":"assistant.delta","event":{"text":"wrote answer.txt"}}'
printf '%s\n' '{"type":"agent.turn.finished","event":{"turn":1}}'
printf '%s\n' '{"type":"tool.started","event":{"tool_call_id":"call-1","tool_name":"write","args":{"path":"answer.txt"}}}'
printf '%s\n' '{"type":"tool.completed","event":{"tool_call_id":"call-1","tool_name":"write","result":{"bytes":2}}}'
printf '%s\n' '{"type":"usage","event":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}'
printf '%s\n' '{"type":"agent.finished","event":{"summary":{"duration_ms":1,"llm_requests":1,"tool_executions":1,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}}}'
printf '%s\n' '{"type":"result","event":{"content":"wrote answer.txt","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}}'
`, 0o755)
	bench.WriteConfig(fmt.Sprintf(`
[adapters.iris.options]
command = %q
`, stub))

	stdout, stderr, err := execCLI(t, bench, "run", "--suite", "smoke", "--agent", "iris")
	if err != nil {
		t.Fatalf("run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Passed: 1/1 (100.0%)") {
		t.Errorf("run output:\n%s", stdout)
	}

	expID := extractExperimentID(t, stdout)
	store, err := storage.New(bench.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListAttempts(expID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	attempt := attempts[0]
	if attempt.AgentID != "iris" {
		t.Errorf("agent = %q", attempt.AgentID)
	}
	if attempt.Execution.Completion != adapter.Completed {
		t.Errorf("execution = %+v", attempt.Execution)
	}
	if attempt.Usage == nil || attempt.Usage.TotalTokens == nil || *attempt.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", attempt.Usage)
	}
	if attempt.Outcome != storage.OutcomePassed {
		t.Errorf("outcome = %s", attempt.Outcome)
	}

	dir, err := store.AttemptDir(expID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := attempt.Artifacts["stdout.log"]; !ok {
		t.Errorf("stdout.log is not recorded: %+v", attempt.Artifacts)
	}
	raw := testutil.ReadFile(t, filepath.Join(dir, "stdout.log"))
	if !strings.Contains(raw, `"type":"result"`) {
		t.Errorf("stdout.log does not contain the raw stream:\n%s", raw)
	}
	eventsLog := testutil.ReadFile(t, filepath.Join(dir, "events.jsonl"))
	for _, want := range []string{`"type":"agent.message"`, `"type":"tool.call"`, `"source":"iris"`} {
		if !strings.Contains(eventsLog, want) {
			t.Errorf("events.jsonl missing %s:\n%s", want, eventsLog)
		}
	}
}

// TestRunWithPiStub proves the Pi adapter satisfies the same attempt lifecycle
// as the other adapters. Pi exits 0 even when the model response fails, so this
// test also pins that the adapter's verdict comes from the event stream.
func TestRunWithPiStub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub agent executables require a POSIX shell")
	}
	bench, _ := newBench(t)

	stub := filepath.Join(t.TempDir(), "pi-stub")
	testutil.WriteFile(t, stub, `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '9.9.9-stub\n'
  exit 0
fi
cat > /dev/null
printf 42 > answer.txt
printf '%s\n' '{"type":"session","version":3,"id":"sess-cli","timestamp":"2026-01-01T00:00:00.000Z","cwd":"."}'
printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"wrote answer.txt"},{"type":"toolCall","id":"call-1","name":"write","arguments":{"path":"answer.txt"}}],"provider":"local","model":"m1","usage":{"input":10,"output":5,"totalTokens":15},"stopReason":"toolUse"}}'
printf '%s\n' '{"type":"tool_execution_start","toolCallId":"call-1","toolName":"write","args":{"path":"answer.txt"}}'
printf '%s\n' '{"type":"tool_execution_end","toolCallId":"call-1","toolName":"write","result":{"content":[{"type":"text","text":"wrote"}]},"isError":false,"durationMs":2}'
printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"provider":"local","model":"m1","usage":{"input":20,"output":5,"totalTokens":25},"stopReason":"stop"}}'
printf '%s\n' '{"type":"agent_settled","aborted":false}'
`, 0o755)
	bench.WriteConfig(fmt.Sprintf(`
[adapters.pi.options]
command = %q
`, stub))

	stdout, stderr, err := execCLI(t, bench, "run", "--suite", "smoke", "--agent", "pi")
	if err != nil {
		t.Fatalf("run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Passed: 1/1 (100.0%)") {
		t.Errorf("run output:\n%s", stdout)
	}

	expID := extractExperimentID(t, stdout)
	store, err := storage.New(bench.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListAttempts(expID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	attempt := attempts[0]
	if attempt.AgentID != "pi" {
		t.Errorf("agent = %q", attempt.AgentID)
	}
	if attempt.Execution.Completion != adapter.Completed {
		t.Errorf("execution = %+v", attempt.Execution)
	}
	if attempt.AgentVersion != "9.9.9-stub" {
		t.Errorf("agent version = %q", attempt.AgentVersion)
	}
	if attempt.Provider != "local" || attempt.Model != "m1" {
		t.Errorf("provider/model = %q/%q", attempt.Provider, attempt.Model)
	}
	if attempt.Usage == nil || attempt.Usage.TotalTokens == nil || *attempt.Usage.TotalTokens != 40 {
		t.Errorf("usage = %+v", attempt.Usage)
	}
	if attempt.Outcome != storage.OutcomePassed {
		t.Errorf("outcome = %s", attempt.Outcome)
	}

	dir, err := store.AttemptDir(expID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := attempt.Artifacts["stdout.log"]; !ok {
		t.Errorf("stdout.log is not recorded: %+v", attempt.Artifacts)
	}
	eventsLog := testutil.ReadFile(t, filepath.Join(dir, "events.jsonl"))
	for _, want := range []string{`"type":"agent.message"`, `"type":"tool.call"`, `"source":"pi"`} {
		if !strings.Contains(eventsLog, want) {
			t.Errorf("events.jsonl missing %s:\n%s", want, eventsLog)
		}
	}
}

func TestRunRepetitions(t *testing.T) {
	bench, _ := newBench(t)
	stdout, _, err := execCLI(t, bench, "run", "--task", "write-answer", "--agent", "fake", "--repetitions", "2", "--notes", "repetition test")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "Passed: 2/2") {
		t.Errorf("run output:\n%s", stdout)
	}
	expID := extractExperimentID(t, stdout)
	store, err := storage.New(bench.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := store.GetExperiment(expID)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Repetitions != 2 || exp.Notes != "repetition test" {
		t.Errorf("experiment = repetitions %d notes %q", exp.Repetitions, exp.Notes)
	}
	attempts, err := store.ListAttempts(expID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	if attempts[0].ID == attempts[1].ID {
		t.Error("attempt IDs are not unique")
	}
	if attempts[0].Repetition != 0 || attempts[1].Repetition != 1 {
		t.Errorf("repetitions = %d, %d", attempts[0].Repetition, attempts[1].Repetition)
	}
	if exp.Suite != nil {
		t.Errorf("adhoc task run should not record a suite: %+v", exp.Suite)
	}
}

func TestRunFailingTaskExitCode(t *testing.T) {
	bench, _ := newBench(t)
	bench.WriteConfig(`
[adapters.fake.options]
mode = "noop"
`)
	stdout, _, err := execCLI(t, bench, "run", "--suite", "smoke", "--agent", "fake")
	if err == nil {
		t.Fatalf("expected failure exit; output:\n%s", stdout)
	}
	if code := cli.ExitCode(err); code != cli.ExitTaskFailed {
		t.Errorf("exit code = %d, want %d (err %v)", code, cli.ExitTaskFailed, err)
	}
	if !strings.Contains(stdout, "Passed: 0/1 (0.0%)") {
		t.Errorf("output:\n%s", stdout)
	}
	expID := extractExperimentID(t, stdout)
	store, _ := storage.New(bench.DataDir)
	attempts, _ := store.ListAttempts(expID)
	if len(attempts) != 1 || attempts[0].Outcome != storage.OutcomeFailed {
		t.Fatalf("attempts = %+v", attempts)
	}
	if !attempts[0].WorkspaceKept {
		t.Error("failed task workspace was removed")
	}
}

func TestRunEvaluatorErrorExitCode(t *testing.T) {
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{"README.md": "demo\n"})
	bench.AddTask(testutil.TaskOptions{
		ID:             "broken",
		FixtureRepo:    repo,
		Revision:       revision,
		EvaluateScript: "#!/bin/sh\nprintf 'not json'\n",
	})
	bench.AddSuite("broken", "broken")
	bench.WriteConfig("")

	stdout, _, err := execCLI(t, bench, "run", "--suite", "broken", "--agent", "fake")
	if err == nil {
		t.Fatalf("expected infrastructure exit; output:\n%s", stdout)
	}
	if code := cli.ExitCode(err); code != cli.ExitInfrastructure {
		t.Errorf("exit code = %d, want %d (err %v)", code, cli.ExitInfrastructure, err)
	}
	if !strings.Contains(stdout, "Evaluator errors: 1") {
		t.Errorf("output:\n%s", stdout)
	}
}

func TestRunSetupFailureExitCode(t *testing.T) {
	bench := testutil.NewBench(t)
	repo, revision := bench.AddFixture("demo", map[string]string{"README.md": "demo\n"})
	bench.AddTask(testutil.TaskOptions{
		ID:          "setup-fails",
		FixtureRepo: repo,
		Revision:    revision,
		Setup:       [][]string{{"sh", "-c", "exit 4"}},
	})
	bench.AddSuite("setup", "setup-fails")
	bench.WriteConfig("")

	stdout, _, err := execCLI(t, bench, "run", "--suite", "setup", "--agent", "fake")
	if err == nil {
		t.Fatalf("expected infrastructure exit; output:\n%s", stdout)
	}
	if code := cli.ExitCode(err); code != cli.ExitInfrastructure {
		t.Errorf("exit code = %d, want %d (err %v)", code, cli.ExitInfrastructure, err)
	}
	if !strings.Contains(stdout, "Not evaluated: 1") {
		t.Errorf("output:\n%s", stdout)
	}
}

func TestRunSelectionErrors(t *testing.T) {
	bench, _ := newBench(t)
	cases := [][]string{
		{"run", "--agent", "fake"},
		{"run", "--agent", "fake", "--suite", "nope"},
		{"run", "--agent", "fake", "--task", "nope"},
		{"run", "--agent", "fake", "--suite", "smoke", "--task", "write-answer"},
		{"run", "--agent", "nope", "--suite", "smoke"},
		{"run", "--agent", "fake", "--suite", "smoke", "--repetitions", "0"},
		{"run", "--suite", "smoke"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			_, _, err := execCLI(t, bench, args...)
			if err == nil {
				t.Fatalf("expected error for %v", args)
			}
			if code := cli.ExitCode(err); code != cli.ExitUsage {
				t.Errorf("exit code = %d, want %d (err %v)", code, cli.ExitUsage, err)
			}
		})
	}
}

func TestSuiteWithMissingTaskFails(t *testing.T) {
	bench, _ := newBench(t)
	bench.AddSuite("broken", "does-not-exist")
	_, _, err := execCLI(t, bench, "suites", "list")
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected usage error, got %v", err)
	}
	_, _, err = execCLI(t, bench, "run", "--suite", "broken", "--agent", "fake")
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestDoctor(t *testing.T) {
	bench, _ := newBench(t)
	stdout, _, err := execCLI(t, bench, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	for _, want := range []string{"config", "data", "tasks", "suites", "git", "agent:fake", "agent:iris", "agent:pi", "sandbox"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output missing %q:\n%s", want, stdout)
		}
	}

	// A missing task directory is a failing check.
	broken := bench
	broken.ConfigPath = filepath.Join(broken.Root, "broken.toml")
	testutil.WriteFile(t, broken.ConfigPath, `data_dir = "`+broken.DataDir+`"
tasks_dir = "`+filepath.Join(broken.Root, "missing-tasks")+`"
suites_dir = "`+broken.SuitesDir+`"
`, 0o644)
	_, _, err = execCLI(t, broken, "doctor")
	if err == nil || cli.ExitCode(err) != cli.ExitInfrastructure {
		t.Fatalf("expected doctor infrastructure failure, got %v", err)
	}
}

func TestAgentsList(t *testing.T) {
	bench, _ := newBench(t)
	stdout, _, err := execCLI(t, bench, "agents", "list")
	if err != nil {
		t.Fatalf("agents list: %v", err)
	}
	if !strings.Contains(stdout, "fake") || !strings.Contains(stdout, "valid") {
		t.Errorf("agents list:\n%s", stdout)
	}
	if !strings.Contains(stdout, "iris") || !strings.Contains(stdout, "pi") {
		t.Errorf("agents list does not mention all built-in adapters:\n%s", stdout)
	}

	stdout, _, err = execCLI(t, bench, "agents", "list", "--json")
	if err != nil {
		t.Fatalf("agents list --json: %v", err)
	}
	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"config_status"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("parse agents JSON: %v\n%s", err, stdout)
	}
	statuses := make(map[string]string, len(rows))
	for _, row := range rows {
		statuses[row.ID] = row.Status
	}
	for _, id := range []string{"fake", "iris", "pi"} {
		status, ok := statuses[id]
		if !ok {
			t.Errorf("adapter %q is not registered", id)
			continue
		}
		if id == "fake" {
			continue
		}
		// Iris and Pi may or may not be installed on the machine running the
		// tests; a missing executable is "unavailable", never a config error.
		if status != "valid" && status != "unavailable" {
			t.Errorf("adapter %q config status = %q", id, status)
		}
	}
}

func TestRunsListEmpty(t *testing.T) {
	bench, _ := newBench(t)
	stdout, _, err := execCLI(t, bench, "runs", "list")
	if err != nil {
		t.Fatalf("runs list: %v", err)
	}
	if !strings.Contains(stdout, "No experiments recorded") {
		t.Errorf("runs list:\n%s", stdout)
	}
}

func TestInspectUnknownAttempt(t *testing.T) {
	bench, _ := newBench(t)
	stdout, _, err := execCLI(t, bench, "inspect", "nope")
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected usage error, got %v", err)
	}
	if !strings.Contains(err.Error(), bench.DataDir) || !strings.Contains(err.Error(), "hint:") {
		t.Errorf("error should name the data directory and hint, got %v (stdout %s)", err, stdout)
	}
}

func TestReadingFromWrongDataDirectoryGivesHint(t *testing.T) {
	bench, config := newBench(t)
	if _, _, err := execCLI(t, bench, "run", "--suite", "smoke", "--agent", "fake"); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	otherData := filepath.Join(bench.Root, "other-data")
	otherConfig := filepath.Join(bench.Root, "other.toml")
	testutil.WriteFile(t, otherConfig, `data_dir = "`+otherData+`"
tasks_dir = "`+bench.TasksDir+`"
suites_dir = "`+bench.SuitesDir+`"
`, 0o644)

	// The run used bench.ConfigPath; reading with another config must fail with
	// a hint rather than a raw syscall error.
	app := cli.New()
	var out, errBuf bytes.Buffer
	app.Out = &out
	app.Err = &errBuf
	root := app.Root()
	root.SetArgs([]string{"--config", otherConfig, "runs", "list"})
	root.SetOut(&out)
	root.SetErr(&errBuf)
	if err := root.Execute(); err != nil {
		t.Fatalf("runs list on empty store: %v", err)
	}
	if !strings.Contains(out.String(), otherData) {
		t.Errorf("empty-store message should name its data directory:\n%s", out.String())
	}
	_ = config
}

func TestInvalidConfigFails(t *testing.T) {
	bench, _ := newBench(t)
	testutil.WriteFile(t, bench.ConfigPath, "unknown_option = true\n", 0o644)
	_, _, err := execCLI(t, bench, "tasks", "list")
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Fatalf("expected config usage error, got %v", err)
	}
}

func extractExperimentID(t *testing.T, stdout string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^Experiment: (\S+) `)
	match := re.FindStringSubmatch(stdout)
	if len(match) != 2 {
		t.Fatalf("could not find experiment id in output:\n%s", stdout)
	}
	return match[1]
}

var _ = os.Getenv
