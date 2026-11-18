package evaluator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/proc"
	"github.com/m4ns0ur/gauge/internal/task"
)

// DefaultOutputLimit bounds captured evaluator stdout and stderr. The
// evaluator's own files in the scratch directory are not affected.
const DefaultOutputLimit = 8 << 20

// Request is one evaluator invocation.
type Request struct {
	AttemptID  string
	Task       *task.Task
	Workspace  string
	ScratchDir string
	Timeout    time.Duration
	// OutputLimit bounds captured stdout/stderr per stream. Zero uses
	// DefaultOutputLimit.
	OutputLimit int64
}

// Outcome reports what happened, distinguishing evaluator infrastructure
// failure (Err != nil) from a candidate task failure (Result.Status == failed).
type Outcome struct {
	Result    *Result
	Err       error
	ExitCode  *int
	TimedOut  bool
	Command   []string
	StartedAt time.Time
	EndedAt   time.Time
	Stdout    string
	Stderr    string
	Truncated bool
}

// Duration returns the evaluator wall-clock duration.
func (o Outcome) Duration() time.Duration {
	if o.StartedAt.IsZero() || o.EndedAt.IsZero() {
		return 0
	}
	return o.EndedAt.Sub(o.StartedAt)
}

// Run invokes the task evaluator in a controlled environment. It always
// returns an Outcome; infrastructure problems are reported through Err.
func Run(ctx context.Context, req Request) Outcome {
	out := Outcome{StartedAt: time.Now().UTC()}
	defer func() { out.EndedAt = time.Now().UTC() }()

	if req.Task == nil {
		out.Err = fmt.Errorf("evaluator: task is nil")
		return out
	}
	if req.Workspace == "" || req.ScratchDir == "" {
		out.Err = fmt.Errorf("evaluator: workspace and scratch directory are required")
		return out
	}
	if req.Timeout <= 0 {
		req.Timeout = req.Task.Evaluation.Timeout.Duration()
	}
	if req.Timeout <= 0 {
		out.Err = fmt.Errorf("evaluator: timeout must be positive")
		return out
	}
	if req.OutputLimit <= 0 {
		req.OutputLimit = DefaultOutputLimit
	}

	argv, err := command(req.Task)
	if err != nil {
		out.Err = err
		return out
	}
	argv = append(argv, req.Workspace, req.Task.EvaluatorDir, req.ScratchDir)
	out.Command = argv

	if err := os.MkdirAll(req.ScratchDir, 0o755); err != nil {
		out.Err = fmt.Errorf("evaluator: create scratch directory: %w", err)
		return out
	}
	tmpDir := filepath.Join(req.ScratchDir, "tmp")
	homeDir := filepath.Join(req.ScratchDir, "home")
	for _, dir := range []string{tmpDir, homeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			out.Err = fmt.Errorf("evaluator: create %s: %w", dir, err)
			return out
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = req.ScratchDir
	cmd.Env = environment(req, tmpDir, homeDir)
	proc.Configure(cmd)

	var stdout, stderr limitedBuffer
	stdout.limit = req.OutputLimit
	stderr.limit = req.OutputLimit
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	out.Stdout = stdout.String()
	out.Stderr = stderr.String()
	out.Truncated = stdout.truncated || stderr.truncated
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		out.ExitCode = &code
	}

	if runCtx.Err() == context.DeadlineExceeded {
		out.TimedOut = true
		out.Err = fmt.Errorf("evaluator timed out after %s", req.Timeout)
		return out
	}
	if ctx.Err() != nil {
		out.Err = fmt.Errorf("evaluator cancelled: %w", ctx.Err())
		return out
	}
	if runErr != nil {
		out.Err = fmt.Errorf("evaluator command failed: %w", runErr)
		return out
	}

	result, err := Parse([]byte(strings.TrimSpace(out.Stdout)))
	if err != nil {
		out.Err = fmt.Errorf("invalid evaluator output: %w", err)
		return out
	}
	out.Result = result
	return out
}

// command builds the configured argv. Relative executables resolve inside the
// evaluator assets dir. The positional workspace, assets, and scratch paths are
// appended by Run.
func command(t *task.Task) ([]string, error) {
	if len(t.Evaluation.Command) == 0 {
		return nil, fmt.Errorf("evaluator: task %q has no evaluation command", t.ID)
	}
	argv := make([]string, 0, len(t.Evaluation.Command))
	first := t.Evaluation.Command[0]
	if strings.ContainsAny(first, `/\`) {
		first = filepath.Join(t.EvaluatorDir, filepath.FromSlash(first))
	} else {
		resolved, err := exec.LookPath(first)
		if err != nil {
			return nil, fmt.Errorf("evaluator: command %q not found in PATH: %w", first, err)
		}
		first = resolved
	}
	argv = append(argv, first)
	argv = append(argv, t.Evaluation.Command[1:]...)
	return argv, nil
}

// environment builds a small, explicit environment. It inherits PATH so the
// evaluator can find the repository's toolchain, and deliberately does not
// inherit credentials or the caller's HOME.
func environment(req Request, tmpDir, homeDir string) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + homeDir,
		"TMPDIR=" + tmpDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"GAUGE_SCHEMA_VERSION=1",
		"GAUGE_ATTEMPT_ID=" + req.AttemptID,
		"GAUGE_TASK_ID=" + req.Task.ID,
		"GAUGE_WORKSPACE=" + req.Workspace,
		"GAUGE_EVALUATOR_ASSETS=" + req.Task.EvaluatorDir,
		"GAUGE_SCRATCH_DIR=" + req.ScratchDir,
		"GOOS=" + runtime.GOOS,
		"GOARCH=" + runtime.GOARCH,
	}
	return env
}

// limitedBuffer captures up to limit bytes and records truncation.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
