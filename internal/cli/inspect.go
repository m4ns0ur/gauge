package cli

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/storage"
)

func (a *App) inspectCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inspect <attempt-id>",
		Short: "Show one attempt, its evaluation, and artifact paths",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			store, err := a.store(cfg)
			if err != nil {
				return err
			}
			attempt, err := store.FindAttempt(args[0])
			if err != nil {
				return notFoundError(err, store, "attempt", args[0])
			}
			attemptDir, err := store.AttemptDir(attempt.ExperimentID, attempt.ID)
			if err != nil {
				return Exitf(ExitInfrastructure, "%v", err)
			}
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), struct {
					Attempt    *storage.Attempt  `json:"attempt"`
					AttemptDir string            `json:"attempt_dir"`
					Artifacts  map[string]string `json:"artifact_paths"`
					Workspace  string            `json:"workspace,omitempty"`
				}{
					Attempt:    attempt,
					AttemptDir: attemptDir,
					Artifacts:  artifactPaths(attemptDir, attempt),
					Workspace:  workspacePath(attemptDir, attempt),
				})
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Attempt:    %s\n", attempt.ID)
			fmt.Fprintf(w, "Experiment: %s\n", attempt.ExperimentID)
			fmt.Fprintf(w, "Task:       %s (%s, %s)\n", attempt.TaskID, attempt.TaskName, attempt.TaskCategory)
			fmt.Fprintf(w, "Agent:      %s", attempt.AgentID)
			if attempt.AgentVersion != "" {
				fmt.Fprintf(w, " %s", attempt.AgentVersion)
			}
			fmt.Fprintf(w, "\nModel:      %s\n", display(attempt.Model))
			fmt.Fprintf(w, "Outcome:    %s\n", attempt.Outcome)
			fmt.Fprintf(w, "Execution:  %s", attempt.Execution.Completion)
			if attempt.Execution.ExitCode != nil {
				fmt.Fprintf(w, " (exit %d)", *attempt.Execution.ExitCode)
			}
			if attempt.Execution.TimedOut {
				fmt.Fprintf(w, " [timed out]")
			}
			if attempt.Execution.Cancelled {
				fmt.Fprintf(w, " [cancelled]")
			}
			if attempt.Execution.Error != "" {
				fmt.Fprintf(w, "\n            error: %s", attempt.Execution.Error)
			}
			if attempt.Execution.AdapterError != "" {
				fmt.Fprintf(w, "\n            adapter error: %s", attempt.Execution.AdapterError)
			}
			fmt.Fprintf(w, "\nStarted:    %s\n", attempt.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
			fmt.Fprintf(w, "Duration:   %s\n", formatDurationMS(attempt.DurationMS))
			if attempt.Setup != nil && attempt.Setup.Ran {
				fmt.Fprintf(w, "Setup:      %d step(s)", attempt.Setup.Steps)
				if attempt.Setup.Error != "" {
					fmt.Fprintf(w, ", failed: %s", attempt.Setup.Error)
				}
				fmt.Fprintf(w, "\n")
			}
			if attempt.Usage != nil {
				fmt.Fprintf(w, "Usage:      %s\n", usageLine(attempt.Usage))
			} else {
				fmt.Fprintf(w, "Usage:      unavailable\n")
			}

			fmt.Fprintf(w, "\nDiff: ")
			if !attempt.Diff.Changed {
				fmt.Fprintf(w, "no changes detected\n")
			} else {
				fmt.Fprintf(w, "%d changed file(s)\n", len(attempt.Diff.Files))
				for _, f := range attempt.Diff.Files {
					fmt.Fprintf(w, "  %s%s\n", f.Index+f.Worktree, f.Path)
				}
			}
			if attempt.Diff.Truncated {
				fmt.Fprintf(w, "  (captured patch was truncated)\n")
			}

			if attempt.Evaluation != nil {
				fmt.Fprintf(w, "\nEvaluation: %s", attempt.Evaluation.Status)
				if attempt.Evaluation.Score != nil {
					fmt.Fprintf(w, " (score %.2f)", *attempt.Evaluation.Score)
				}
				fmt.Fprintf(w, "\n  %s\n", attempt.Evaluation.Summary)
				if attempt.Evaluation.Error != "" {
					fmt.Fprintf(w, "  error: %s\n", attempt.Evaluation.Error)
				}
				for _, check := range attempt.Evaluation.Checks {
					fmt.Fprintf(w, "  [%s] %s: %s\n", check.Status, check.ID, check.Message)
				}
			} else {
				fmt.Fprintf(w, "\nEvaluation: not run\n")
			}

			if attempt.Error != "" {
				fmt.Fprintf(w, "\nRecorded error: %s\n", attempt.Error)
			}

			fmt.Fprintf(w, "\nArtifacts:\n")
			paths := artifactPaths(attemptDir, attempt)
			if len(paths) == 0 {
				fmt.Fprintf(w, "  (none)\n")
			}
			for _, name := range sortedKeys(paths) {
				fmt.Fprintf(w, "  %-24s %s\n", name, paths[name])
			}
			if ws := workspacePath(attemptDir, attempt); ws != "" {
				fmt.Fprintf(w, "\nWorkspace: %s\n", ws)
			} else {
				fmt.Fprintf(w, "\nWorkspace: removed (retention policy)\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func artifactPaths(attemptDir string, attempt *storage.Attempt) map[string]string {
	paths := make(map[string]string, len(attempt.Artifacts))
	for name, artifact := range attempt.Artifacts {
		paths[name] = filepath.Join(attemptDir, filepath.FromSlash(artifact.Path))
	}
	return paths
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func workspacePath(attemptDir string, attempt *storage.Attempt) string {
	if !attempt.WorkspaceKept {
		return ""
	}
	return filepath.Join(attemptDir, "workspace")
}

func usageLine(u *events.Usage) string {
	if u == nil {
		return "unavailable"
	}
	lines := fmt.Sprintf("input %s, output %s, total %s",
		intPtrText(u.InputTokens), intPtrText(u.OutputTokens), intPtrText(u.TotalTokens))
	if u.CostUSD != nil {
		lines += fmt.Sprintf(", cost $%.4f", *u.CostUSD)
	}
	return lines
}

func intPtrText(v *int64) string {
	if v == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d", *v)
}
