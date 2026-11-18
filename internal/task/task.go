// Package task defines Gauge's versioned, agent-neutral task specification.
//
// A task is immutable benchmark content: a prompt shown to the agent, a pinned
// fixture revision that becomes the candidate workspace, and an evaluator that
// never enters that workspace. Loading a task validates the document and all
// referenced paths before any agent process can start.
package task

import (
	"fmt"
	"path/filepath"
)

// SchemaVersion is the supported task schema version.
const SchemaVersion = 1

// Difficulty is a coarse, optional task label.
type Difficulty string

const (
	// DifficultyEasy is a small, localized change.
	DifficultyEasy Difficulty = "easy"
	// DifficultyMedium is a multi-file or behaviorally subtle change.
	DifficultyMedium Difficulty = "medium"
	// DifficultyHard is a cross-cutting, migration, or debugging task.
	DifficultyHard Difficulty = "hard"
)

// NetworkPolicy states whether a task is expected to need network access.
type NetworkPolicy string

const (
	// NetworkDisabled is the default: tasks must not require internet access.
	NetworkDisabled NetworkPolicy = "disabled"
	// NetworkAllowed marks an explicit, justified exception.
	NetworkAllowed NetworkPolicy = "allowed"
)

// Task is one immutable benchmark item.
type Task struct {
	SchemaVersion int         `yaml:"schema_version" json:"schema_version"`
	ID            string      `yaml:"id" json:"id"`
	Name          string      `yaml:"name" json:"name"`
	Category      string      `yaml:"category" json:"category"`
	Difficulty    Difficulty  `yaml:"difficulty" json:"difficulty"`
	Description   string      `yaml:"description,omitempty" json:"description,omitempty"`
	Tags          []string    `yaml:"tags,omitempty" json:"tags,omitempty"`
	Fixture       Fixture     `yaml:"fixture" json:"fixture"`
	PromptFile    string      `yaml:"prompt" json:"prompt"`
	Execution     Execution   `yaml:"execution" json:"execution"`
	Evaluation    Evaluation  `yaml:"evaluation" json:"evaluation"`
	Provenance    *Provenance `yaml:"provenance,omitempty" json:"provenance,omitempty"`

	// Derived, not part of the on-disk schema.

	// Dir is the absolute task directory.
	Dir string `yaml:"-" json:"-"`
	// Prompt is the loaded prompt text.
	Prompt string `yaml:"-" json:"-"`
	// FixtureDir is the absolute fixture repository path.
	FixtureDir string `yaml:"-" json:"-"`
	// EvaluatorDir is the absolute evaluator assets directory.
	EvaluatorDir string `yaml:"-" json:"-"`
}

// Fixture pins the repository state a candidate workspace starts from.
type Fixture struct {
	// Path is relative to the task directory. It may point outside the task
	// directory (for example ../../../fixtures/x.git) but must exist and be a
	// Git repository.
	Path string `yaml:"path" json:"path"`
	// Revision is a full 40-character commit SHA. Branches and tags are
	// rejected because they move.
	Revision string `yaml:"revision" json:"revision"`
}

// Execution constrains the agent attempt.
type Execution struct {
	Timeout Duration      `yaml:"timeout" json:"timeout"`
	Network NetworkPolicy `yaml:"network,omitempty" json:"network,omitempty"`
	// Setup is a list of argv-style commands run in the workspace before the
	// agent starts. Commands are executed directly (no shell).
	Setup [][]string `yaml:"setup,omitempty" json:"setup,omitempty"`
}

// Evaluation invokes the independent evaluator after the agent stops.
type Evaluation struct {
	Path string `yaml:"path" json:"path"`
	// Command is an argv-style evaluator invocation resolved relative to Path.
	// Gauge appends the workspace, assets, and scratch directory arguments.
	Command []string `yaml:"command" json:"command"`
	Timeout Duration `yaml:"timeout" json:"timeout"`
}

// Provenance records where task content came from.
type Provenance struct {
	Origin  string `yaml:"origin,omitempty" json:"origin,omitempty"`
	License string `yaml:"license,omitempty" json:"license,omitempty"`
	Notes   string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// ValidationError identifies the file and field that failed validation.
type ValidationError struct {
	File  string
	Field string
	Msg   string
}

// Error implements error.
func (e *ValidationError) Error() string {
	if e.File == "" && e.Field == "" {
		return e.Msg
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.File, e.Msg)
	}
	return fmt.Sprintf("%s: field %q: %s", e.File, e.Field, e.Msg)
}

// File returns the task.yaml path for a task directory.
func File(dir string) string { return filepath.Join(dir, "task.yaml") }
