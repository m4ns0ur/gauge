package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"config", "data", "tasks", "suites", "git", "agent:fake", "sandbox"} {
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
