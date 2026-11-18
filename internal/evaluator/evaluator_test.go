package evaluator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/evaluator"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/testutil"
)

func TestParseResults(t *testing.T) {
	valid := []string{
		`{"schema_version":1,"status":"passed","summary":"ok","checks":[{"id":"a","status":"passed"}]}`,
		`{"schema_version":1,"status":"failed","summary":"no","checks":[{"id":"a","status":"failed"}]}`,
		`{"schema_version":1,"status":"error","summary":"boom","checks":[]}`,
		`{"schema_version":1,"status":"passed","summary":"ok","score":0.5,"checks":[{"id":"a","status":"passed"}]}`,
	}
	for _, input := range valid {
		if _, err := evaluator.Parse([]byte(input)); err != nil {
			t.Errorf("Parse(%s): %v", input, err)
		}
	}

	invalid := []struct {
		name    string
		input   string
		wantSub string
	}{
		{"not json", `{`, "not valid JSON"},
		{"unknown field", `{"schema_version":1,"status":"passed","summary":"ok","checks":[{"id":"a","status":"passed"}],"extra":1}`, "extra"},
		{"bad schema", `{"schema_version":9,"status":"passed","summary":"ok","checks":[{"id":"a","status":"passed"}]}`, "schema_version"},
		{"bad status", `{"schema_version":1,"status":"success","summary":"ok","checks":[]}`, "status"},
		{"missing summary", `{"schema_version":1,"status":"passed","checks":[{"id":"a","status":"passed"}]}`, "summary"},
		{"no checks on passed", `{"schema_version":1,"status":"passed","summary":"ok","checks":[]}`, "no checks"},
		{"passed with failed check", `{"schema_version":1,"status":"passed","summary":"ok","checks":[{"id":"a","status":"failed"}]}`, "failed or error"},
		{"failed with all passed checks", `{"schema_version":1,"status":"failed","summary":"no","checks":[{"id":"a","status":"passed"}]}`, "every reported check passed"},
		{"duplicate check ids", `{"schema_version":1,"status":"failed","summary":"no","checks":[{"id":"a","status":"failed"},{"id":"a","status":"passed"}]}`, "duplicated"},
		{"score out of range", `{"schema_version":1,"status":"passed","summary":"ok","score":2,"checks":[{"id":"a","status":"passed"}]}`, "between 0 and 1"},
		{"trailing content", `{"schema_version":1,"status":"passed","summary":"ok","checks":[{"id":"a","status":"passed"}]} {}`, "single JSON object"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := evaluator.Parse([]byte(tc.input))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func newTask(t *testing.T, script string, timeout time.Duration) (*task.Task, string) {
	t.Helper()
	dir := t.TempDir()
	assets := filepath.Join(dir, "evaluator")
	testutil.WriteFile(t, filepath.Join(assets, "evaluate.sh"), script, 0o755)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	return &task.Task{
		ID:           "eval-test",
		EvaluatorDir: assets,
		Evaluation: task.Evaluation{
			Path:    assets,
			Command: []string{"./evaluate.sh"},
			Timeout: task.NewDuration(timeout),
		},
	}, workspace
}

func TestRunPassAndFail(t *testing.T) {
	pass := `#!/bin/sh
test -f "$1/answer.txt" || { printf '%s' '{"schema_version":1,"status":"failed","summary":"missing","checks":[{"id":"a","status":"failed"}]}'; exit 0; }
printf '%s' '{"schema_version":1,"status":"passed","summary":"ok","checks":[{"id":"a","status":"passed"}]}'
`
	taskDef, workspace := newTask(t, pass, 10*time.Second)
	outcome := evaluator.Run(context.Background(), evaluator.Request{
		AttemptID:  "attempt",
		Task:       taskDef,
		Workspace:  workspace,
		ScratchDir: filepath.Join(filepath.Dir(workspace), "scratch"),
	})
	if outcome.Err != nil {
		t.Fatalf("run: %v (stderr %s)", outcome.Err, outcome.Stderr)
	}
	if outcome.Result == nil || outcome.Result.Status != evaluator.Failed {
		t.Fatalf("result = %+v", outcome.Result)
	}

	testutil.WriteFile(t, filepath.Join(workspace, "answer.txt"), "42\n", 0o644)
	outcome = evaluator.Run(context.Background(), evaluator.Request{
		AttemptID:  "attempt",
		Task:       taskDef,
		Workspace:  workspace,
		ScratchDir: filepath.Join(filepath.Dir(workspace), "scratch"),
	})
	if outcome.Err != nil {
		t.Fatalf("run: %v (stderr %s)", outcome.Err, outcome.Stderr)
	}
	if outcome.Result == nil || outcome.Result.Status != evaluator.Passed {
		t.Fatalf("result = %+v", outcome.Result)
	}
	if outcome.ExitCode == nil || *outcome.ExitCode != 0 {
		t.Errorf("exit code = %v", outcome.ExitCode)
	}
}

func TestRunInfrastructureFailures(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantSub string
	}{
		{"malformed JSON", `#!/bin/sh
printf 'not json'`, "invalid evaluator output"},
		{"non-zero exit", `#!/bin/sh
printf '%s' '{"schema_version":1,"status":"passed","summary":"ok","checks":[{"id":"a","status":"passed"}]}'
exit 7`, "command failed"},
		{"missing executable", `#!/bin/sh
exit 0`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			taskDef, workspace := newTask(t, tc.script, 5*time.Second)
			if tc.name == "missing executable" {
				taskDef.Evaluation.Command = []string{"./does-not-exist.sh"}
			}
			outcome := evaluator.Run(context.Background(), evaluator.Request{
				AttemptID:  "attempt",
				Task:       taskDef,
				Workspace:  workspace,
				ScratchDir: filepath.Join(filepath.Dir(workspace), "scratch"),
			})
			if outcome.Err == nil {
				t.Fatalf("expected infrastructure error, got result %+v", outcome.Result)
			}
			if tc.wantSub != "" && !strings.Contains(outcome.Err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", outcome.Err, tc.wantSub)
			}
			if outcome.Result != nil {
				t.Errorf("result should be nil on infrastructure error, got %+v", outcome.Result)
			}
		})
	}
}

func TestRunTimeout(t *testing.T) {
	taskDef, workspace := newTask(t, "#!/bin/sh\nsleep 5\n", 100*time.Millisecond)
	start := time.Now()
	outcome := evaluator.Run(context.Background(), evaluator.Request{
		AttemptID:  "attempt",
		Task:       taskDef,
		Workspace:  workspace,
		ScratchDir: filepath.Join(filepath.Dir(workspace), "scratch"),
	})
	if outcome.Err == nil {
		t.Fatal("expected timeout error")
	}
	if !outcome.TimedOut {
		t.Errorf("TimedOut = false (err %v)", outcome.Err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("evaluator did not stop promptly: %s", elapsed)
	}
}

func TestRunControlledEnvironment(t *testing.T) {
	script := `#!/bin/sh
if [ -z "$GAUGE_WORKSPACE" ] || [ -z "$GAUGE_TASK_ID" ] || [ -z "$GAUGE_SCRATCH_DIR" ]; then
  printf '%s' '{"schema_version":1,"status":"error","summary":"missing env","checks":[]}'
  exit 0
fi
if [ "$GAUGE_WORKSPACE" != "$1" ] || [ "$GAUGE_SCRATCH_DIR" != "$3" ]; then
  printf '%s' '{"schema_version":1,"status":"error","summary":"env mismatch","checks":[]}'
  exit 0
fi
printf '%s' '{"schema_version":1,"status":"passed","summary":"env ok","checks":[{"id":"env","status":"passed"}]}'
`
	taskDef, workspace := newTask(t, script, 10*time.Second)
	scratch := filepath.Join(filepath.Dir(workspace), "scratch")
	outcome := evaluator.Run(context.Background(), evaluator.Request{
		AttemptID:  "attempt",
		Task:       taskDef,
		Workspace:  workspace,
		ScratchDir: scratch,
	})
	if outcome.Err != nil {
		t.Fatalf("run: %v (stderr %s)", outcome.Err, outcome.Stderr)
	}
	if outcome.Result == nil || outcome.Result.Status != evaluator.Passed {
		t.Fatalf("result = %+v; stderr=%s", outcome.Result, outcome.Stderr)
	}
}

func TestAssetsPathPointsOutsideWorkspace(t *testing.T) {
	script := `#!/bin/sh
case "$2" in
  "$1"/*) printf '%s' '{"schema_version":1,"status":"failed","summary":"assets inside workspace","checks":[{"id":"isolation","status":"failed"}]}' ;;
  *) printf '%s' '{"schema_version":1,"status":"passed","summary":"assets separate","checks":[{"id":"isolation","status":"passed"}]}' ;;
esac
`
	taskDef, workspace := newTask(t, script, 10*time.Second)
	outcome := evaluator.Run(context.Background(), evaluator.Request{
		AttemptID:  "attempt",
		Task:       taskDef,
		Workspace:  workspace,
		ScratchDir: filepath.Join(filepath.Dir(workspace), "scratch"),
	})
	if outcome.Err != nil {
		t.Fatalf("run: %v", outcome.Err)
	}
	if outcome.Result.Status != evaluator.Passed {
		t.Fatalf("evaluator assets leaked into the workspace: %+v", outcome.Result)
	}
}
