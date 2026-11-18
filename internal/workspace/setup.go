package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/proc"
)

// DefaultSetupLimit bounds captured setup stdout/stderr per stream.
const DefaultSetupLimit = 4 << 20

// SetupStep is one executed setup command.
type SetupStep struct {
	Index     int       `json:"index"`
	Command   []string  `json:"command"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Stdout    string    `json:"-"`
	Stderr    string    `json:"-"`
	Truncated bool      `json:"truncated,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// SetupOutcome reports the setup phase result. A non-empty Err means setup did
// not complete and the agent must not run.
type SetupOutcome struct {
	Steps     []SetupStep `json:"steps,omitempty"`
	Err       string      `json:"error,omitempty"`
	TimedOut  bool        `json:"timed_out,omitempty"`
	Cancelled bool        `json:"cancelled,omitempty"`
}

// RunSetup executes argv-style setup commands sequentially in the workspace.
// Each command runs directly (no shell) with a minimal environment and its own
// timeout; the first failure stops the sequence.
func RunSetup(ctx context.Context, workspaceDir, artifactsDir string, steps [][]string, timeout time.Duration) SetupOutcome {
	var out SetupOutcome
	if len(steps) == 0 {
		return out
	}
	if timeout <= 0 {
		timeout = DefaultGitTimeout
	}
	homeDir := filepath.Join(artifactsDir, "setup-home")
	tmpDir := filepath.Join(artifactsDir, "setup-tmp")
	for _, dir := range []string{artifactsDir, homeDir, tmpDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			out.Err = fmt.Sprintf("setup: create %s: %v", dir, err)
			return out
		}
	}

	for i, argv := range steps {
		step := SetupStep{Index: i, Command: argv, StartedAt: time.Now().UTC()}
		stepDir := filepath.Join(artifactsDir, fmt.Sprintf("setup-%02d", i+1))
		if err := os.MkdirAll(stepDir, 0o755); err != nil {
			out.Err = fmt.Sprintf("setup step %d: create artifact dir: %v", i+1, err)
			out.Steps = append(out.Steps, step)
			return out
		}
		if len(argv) == 0 {
			step.Error = "empty command"
			out.Steps = append(out.Steps, step)
			out.Err = fmt.Sprintf("setup step %d: empty command", i+1)
			return out
		}

		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(stepCtx, argv[0], argv[1:]...)
		cmd.Dir = workspaceDir
		cmd.Env = []string{
			"PATH=" + pathOrDefault(),
			"HOME=" + homeDir,
			"TMPDIR=" + tmpDir,
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
			"GAUGE_WORKSPACE=" + workspaceDir,
		}
		proc.Configure(cmd)
		var stdout, stderr limitedBuffer
		stdout.limit = DefaultSetupLimit
		stderr.limit = DefaultSetupLimit
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		runErr := cmd.Run()
		step.EndedAt = time.Now().UTC()
		step.Stdout = stdout.String()
		step.Stderr = stderr.String()
		step.Truncated = stdout.truncated || stderr.truncated
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			step.ExitCode = &code
		}
		if runErr != nil {
			switch {
			case stepCtx.Err() == context.DeadlineExceeded:
				step.Error = fmt.Sprintf("timed out after %s", timeout)
				out.TimedOut = true
			case ctx.Err() != nil:
				step.Error = "cancelled"
				out.Cancelled = true
			default:
				step.Error = runErr.Error()
			}
			out.Steps = append(out.Steps, step)
			out.Err = fmt.Sprintf("setup step %d (%s): %s", i+1, strings.Join(argv, " "), step.Error)
			writeStepLogs(stepDir, step)
			cancel()
			return out
		}
		writeStepLogs(stepDir, step)
		cancel()
		out.Steps = append(out.Steps, step)
	}
	return out
}

func writeStepLogs(dir string, step SetupStep) {
	if step.Stdout != "" {
		_ = os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(step.Stdout), 0o644)
	}
	if step.Stderr != "" {
		_ = os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(step.Stderr), 0o644)
	}
	_ = os.WriteFile(filepath.Join(dir, "command.txt"), []byte(strings.Join(step.Command, " ")+"\n"), 0o644)
}

func pathOrDefault() string {
	if p := os.Getenv("PATH"); p != "" {
		return p
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

// limitedBuffer captures up to limit bytes and records truncation.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
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
