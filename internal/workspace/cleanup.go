package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// attemptScratchDirs are per-attempt scratch and tool-cache directories. They
// are not benchmark evidence: the evaluator's verdict and logs are captured
// separately, and keeping dependency/build caches would consume hundreds of
// megabytes per attempt.
var attemptScratchDirs = []string{
	"evaluation-scratch",
	"setup-home",
	"setup-tmp",
	"agent-home",
	"agent-tmp",
	"iris-home",
}

// CleanScratch removes tool caches, agent home directories, and the evaluator
// scratch directory from a finished attempt. It leaves logs, diffs, events,
// evaluation output, setup step logs, and (if retained) the workspace intact.
func CleanScratch(attemptDir string) error {
	base, err := filepath.Abs(attemptDir)
	if err != nil {
		return fmt.Errorf("workspace: resolve attempt directory: %w", err)
	}
	var problems []error
	for _, name := range attemptScratchDirs {
		target := filepath.Join(base, name)
		if _, statErr := os.Lstat(target); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			problems = append(problems, fmt.Errorf("inspect %s: %w", target, statErr))
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			problems = append(problems, fmt.Errorf("remove %s: %w", target, err))
		}
	}
	return errors.Join(problems...)
}
