// Package proc prepares external commands for reliable timeout handling.
//
// Gauge runs agents and evaluators as subprocesses. On Unix it puts them in
// their own process group so a timeout kills the whole tree instead of leaving
// orphaned build or test processes behind. Other platforms fall back to killing
// the direct child, which is weaker; the limitation is documented.
package proc

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WaitDelay bounds how long Wait blocks on inherited stdout/stderr pipes after
// the process has been killed.
const WaitDelay = 5 * time.Second

// ResolveExecutable returns the absolute path of the executable named by
// command, consulting PATH for bare names. It does not run the program, so a
// missing dependency can be reported before an attempt starts.
func ResolveExecutable(command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command is empty")
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("cannot use command %q: %w", command, err)
	}
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve command %q: %w", command, err)
		}
		path = absolute
	}
	return path, nil
}

// Configure attaches cancellation behavior to cmd. It must be called after the
// command is created with exec.CommandContext and before it is started.
func Configure(cmd *exec.Cmd) {
	configure(cmd)
}
