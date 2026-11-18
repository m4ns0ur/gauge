// Package evaluator defines Gauge's independent evaluation contract.
//
// Evaluation runs after the agent process ends and never inside it. The
// evaluator receives a candidate workspace, its own read-only assets, and a
// scratch directory, and reports a versioned JSON result on stdout. The agent's
// own claims are never accepted as evidence.
package evaluator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// SchemaVersion is the supported evaluator result schema version.
const SchemaVersion = 1

// Status is a task or check outcome. It is deliberately coarse: the primary
// task result is pass/fail, with infrastructure problems kept separate.
type Status string

const (
	// Passed means the criteria were satisfied.
	Passed Status = "passed"
	// Failed means the candidate did not satisfy the criteria.
	Failed Status = "failed"
	// Error means the evaluator could not determine an outcome.
	Error Status = "error"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case Passed, Failed, Error:
		return true
	}
	return false
}

// Check is one individual criterion reported by an evaluator.
type Check struct {
	ID      string `json:"id"`
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

// Result is the evaluator's versioned verdict.
type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Status        Status   `json:"status"`
	Score         *float64 `json:"score,omitempty"`
	Summary       string   `json:"summary"`
	Checks        []Check  `json:"checks"`
}

var checkIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Parse decodes a single JSON evaluator result and validates it.
func Parse(data []byte) (*Result, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Result
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("evaluator result is not valid JSON: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != nil && err != io.EOF {
		return nil, fmt.Errorf("evaluator result has trailing content: %w", err)
	} else if err == nil {
		return nil, fmt.Errorf("evaluator result must be a single JSON object")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate checks schema and internal consistency.
func (r *Result) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("evaluator schema_version %d is unsupported (this Gauge supports %d)", r.SchemaVersion, SchemaVersion)
	}
	if !r.Status.Valid() {
		return fmt.Errorf("evaluator status %q must be passed, failed, or error", r.Status)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("evaluator summary is required")
	}
	if len(r.Summary) > 4000 {
		return fmt.Errorf("evaluator summary is too long (max 4000 bytes)")
	}
	if r.Score != nil && (*r.Score < 0 || *r.Score > 1) {
		return fmt.Errorf("evaluator score %.3f must be between 0 and 1", *r.Score)
	}
	if len(r.Checks) > 1000 {
		return fmt.Errorf("evaluator reported %d checks (max 1000)", len(r.Checks))
	}

	seen := make(map[string]struct{}, len(r.Checks))
	failed, errored, passed := 0, 0, 0
	for i, c := range r.Checks {
		if !checkIDRE.MatchString(c.ID) {
			return fmt.Errorf("checks[%d].id %q must match %s", i, c.ID, checkIDRE)
		}
		if _, dup := seen[c.ID]; dup {
			return fmt.Errorf("checks[%d].id %q is duplicated", i, c.ID)
		}
		seen[c.ID] = struct{}{}
		if !c.Status.Valid() {
			return fmt.Errorf("checks[%d].status %q must be passed, failed, or error", i, c.Status)
		}
		if len(c.Message) > 2000 {
			return fmt.Errorf("checks[%d].message is too long (max 2000 bytes)", i)
		}
		switch c.Status {
		case Passed:
			passed++
		case Failed:
			failed++
		case Error:
			errored++
		}
	}

	switch r.Status {
	case Passed:
		if len(r.Checks) == 0 {
			return fmt.Errorf("evaluator status is passed but no checks were reported")
		}
		if failed > 0 || errored > 0 {
			return fmt.Errorf("evaluator status is passed but %d check(s) are failed or error", failed+errored)
		}
	case Failed:
		if len(r.Checks) > 0 && failed == 0 && errored == 0 {
			return fmt.Errorf("evaluator status is failed but every reported check passed")
		}
	}
	return nil
}

// CheckStatuses summarizes checks for reports.
type CheckStatuses struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
}

// CountChecks summarizes the checks in a result.
func (r *Result) CountChecks() CheckStatuses {
	var c CheckStatuses
	for _, check := range r.Checks {
		c.Total++
		switch check.Status {
		case Passed:
			c.Passed++
		case Failed:
			c.Failed++
		case Error:
			c.Errored++
		}
	}
	return c
}
