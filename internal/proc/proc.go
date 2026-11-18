// Package proc prepares external commands for reliable timeout handling.
//
// Gauge runs agents and evaluators as subprocesses. On Unix it puts them in
// their own process group so a timeout kills the whole tree instead of leaving
// orphaned build or test processes behind. Other platforms fall back to killing
// the direct child, which is weaker; the limitation is documented.
package proc

import (
	"os/exec"
	"time"
)

// WaitDelay bounds how long Wait blocks on inherited stdout/stderr pipes after
// the process has been killed.
const WaitDelay = 5 * time.Second

// Configure attaches cancellation behavior to cmd. It must be called after the
// command is created with exec.CommandContext and before it is started.
func Configure(cmd *exec.Cmd) {
	configure(cmd)
}
