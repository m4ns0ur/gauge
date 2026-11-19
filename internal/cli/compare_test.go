package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m4ns0ur/gauge/internal/cli"
	"github.com/m4ns0ur/gauge/internal/report"
	"github.com/m4ns0ur/gauge/internal/testutil"
)

const comparePassingConfig = `
[adapters.fake.options]
mode = "write"

[adapters.fake.options.files]
"answer.txt" = "42"

[[adapters.fake.options.events]]
type = "tool.call"

[adapters.fake.options.events.tool]
call_id = "call-1"
name = "read"

[[adapters.fake.options.events]]
type = "tool.result"

[adapters.fake.options.events.tool]
call_id = "call-1"
name = "read"
output = "ok"
`

const compareFailingConfig = `
[adapters.fake.options]
mode = "noop"
`

func runFake(t *testing.T, bench *testutil.Bench, config, suite, model string) string {
	t.Helper()
	bench.WriteConfig(config)
	stdout, _, err := execCLI(t, bench, "run", "--suite", suite, "--agent", "fake", "--model", model)
	if err != nil && cli.ExitCode(err) != cli.ExitTaskFailed {
		t.Fatalf("run %s: %v\n%s", suite, err, stdout)
	}
	return extractExperimentID(t, stdout)
}

func TestCompareExperiments(t *testing.T) {
	bench, _ := newBench(t)
	passingID := runFake(t, bench, comparePassingConfig, "smoke", "fake/a")
	failingID := runFake(t, bench, compareFailingConfig, "smoke", "fake/b")

	stdout, _, err := execCLI(t, bench, "compare", passingID, failingID, "--format", "json")
	if err != nil {
		t.Fatalf("compare: %v\n%s", err, stdout)
	}
	var comparison report.Comparison
	if err := json.Unmarshal([]byte(stdout), &comparison); err != nil {
		t.Fatalf("parse comparison: %v\n%s", err, stdout)
	}
	if !comparison.Compatible {
		t.Fatalf("comparison not compatible: %v", comparison.Incompatibilities)
	}
	if comparison.A.Summary.Passed != 1 || comparison.B.Summary.Failed != 1 {
		t.Errorf("summaries: A passed %d, B failed %d", comparison.A.Summary.Passed, comparison.B.Summary.Failed)
	}
	if comparison.A.Summary.Behavior == nil || comparison.A.Summary.Behavior.ToolCalls != 1 {
		t.Errorf("A behavior = %+v", comparison.A.Summary.Behavior)
	}
	if comparison.B.Summary.Behavior == nil || comparison.B.Summary.Behavior.ToolCalls != 0 {
		t.Errorf("B behavior = %+v", comparison.B.Summary.Behavior)
	}
	if comparison.Summary.ToolCallsDelta == nil || *comparison.Summary.ToolCallsDelta != -1 {
		t.Errorf("tool delta = %v", comparison.Summary.ToolCallsDelta)
	}
	if len(comparison.Tasks) != 1 || comparison.Tasks[0].Change != "regressed" {
		t.Fatalf("tasks = %+v", comparison.Tasks)
	}
	if comparison.Summary.PassRateDeltaPP == nil || *comparison.Summary.PassRateDeltaPP != -100 {
		t.Errorf("pass rate delta = %v", comparison.Summary.PassRateDeltaPP)
	}

	fields := map[string]bool{}
	for _, difference := range comparison.Differences {
		fields[difference.Field] = true
	}
	for _, want := range []string{"model", "adapter config"} {
		if !fields[want] {
			t.Errorf("missing difference %q: %+v", want, comparison.Differences)
		}
	}

	stdout, _, err = execCLI(t, bench, "compare", passingID, failingID)
	if err != nil {
		t.Fatalf("compare text: %v", err)
	}
	for _, want := range []string{"Comparison:", "Configuration differences:", "Per-task outcomes:", "regressed", "Tool calls"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("terminal comparison missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = execCLI(t, bench, "compare", passingID, failingID, "--format", "markdown")
	if err != nil {
		t.Fatalf("compare markdown: %v", err)
	}
	for _, want := range []string{"# Experiment comparison", "## Per-task outcomes", "| Pass rate |", "regressed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("markdown comparison missing %q:\n%s", want, stdout)
		}
	}

	outputFile := filepath.Join(t.TempDir(), "comparison.json")
	stdout, _, err = execCLI(t, bench, "compare", passingID, failingID, "--format", "json", "--output", outputFile)
	if err != nil {
		t.Fatalf("compare to file: %v", err)
	}
	if !strings.Contains(stdout, "wrote "+outputFile) {
		t.Errorf("output = %q", stdout)
	}
	if data := testutil.ReadFile(t, outputFile); !json.Valid([]byte(data)) {
		t.Errorf("written comparison is not valid JSON:\n%s", data)
	}
}

func TestCompareRejectsDifferentTaskSelections(t *testing.T) {
	bench, _ := newBench(t)
	smokeID := runFake(t, bench, comparePassingConfig, "smoke", "fake/a")

	repo, revision := bench.AddFixture("other", map[string]string{"README.md": "other\n"})
	bench.AddTask(testutil.TaskOptions{ID: "other-task", FixtureRepo: repo, Revision: revision})
	bench.AddSuite("other", "other-task")
	otherID := runFake(t, bench, comparePassingConfig, "other", "fake/a")

	_, _, err := execCLI(t, bench, "compare", smokeID, otherID)
	if err == nil {
		t.Fatal("expected incompatible selection error")
	}
	if code := cli.ExitCode(err); code != cli.ExitUsage {
		t.Errorf("exit code = %d, want %d (%v)", code, cli.ExitUsage, err)
	}
	for _, want := range []string{"not comparable", "only in"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	if _, _, err := execCLI(t, bench, "compare", smokeID, "missing"); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("missing experiment: %v", err)
	}
	if _, _, err := execCLI(t, bench, "compare", smokeID, otherID, "--format", "yaml"); err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("invalid format: %v", err)
	}
}
