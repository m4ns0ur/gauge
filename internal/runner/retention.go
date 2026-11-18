package runner

import "fmt"

// Retention controls what happens to a candidate workspace after an attempt.
type Retention string

const (
	// RetainKeepFailed deletes the workspace after a passed evaluation and
	// preserves it for failed, errored, or incomplete attempts. This is the
	// default.
	RetainKeepFailed Retention = "keep_failed"
	// RetainKeepAll always preserves the workspace.
	RetainKeepAll Retention = "keep_all"
	// RetainDeleteAll always deletes the workspace after evaluation.
	RetainDeleteAll Retention = "delete_all"
)

// ParseRetention validates a configured retention policy.
func ParseRetention(value string) (Retention, error) {
	switch Retention(value) {
	case "", RetainKeepFailed:
		return RetainKeepFailed, nil
	case RetainKeepAll:
		return RetainKeepAll, nil
	case RetainDeleteAll:
		return RetainDeleteAll, nil
	default:
		return "", fmt.Errorf("invalid workspace retention %q (want keep_failed, keep_all, or delete_all)", value)
	}
}

// KeepWorkspace reports whether the workspace should be preserved.
func (r Retention) KeepWorkspace(outcomePassed bool) bool {
	switch r {
	case RetainKeepAll:
		return true
	case RetainDeleteAll:
		return false
	default:
		return !outcomePassed
	}
}
