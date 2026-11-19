package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/config"
	"github.com/m4ns0ur/gauge/internal/experiment"
	"github.com/m4ns0ur/gauge/internal/report"
	"github.com/m4ns0ur/gauge/internal/runner"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/suite"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/workspace"
)

func (a *App) runCommand() *cobra.Command {
	var (
		agentID     string
		suiteID     string
		taskIDs     []string
		repetitions int
		model       string
		notes       string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a task selection against one agent",
		Long: `Run a suite or one or more tasks against a single agent adapter.

Every attempt gets a fresh workspace at the task's pinned fixture revision. The
task evaluator runs only after the agent process ends, outside that process, and
determines pass/fail.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			impl, options, err := a.adapterConfig(cfg, agentID)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("repetitions") {
				repetitions = cfg.Defaults.Repetitions
			}
			if repetitions < 1 || repetitions > 100 {
				return Exitf(ExitUsage, "--repetitions must be between 1 and 100")
			}
			retention, err := runner.ParseRetention(cfg.Retention.Workspace)
			if err != nil {
				return Exitf(ExitUsage, "%v", err)
			}

			tasks, err := a.tasks(a.ctx(), cfg)
			if err != nil {
				return err
			}
			selected, selectedSuite, err := a.resolveSelection(tasks, suiteID, taskIDs, cfg)
			if err != nil {
				return err
			}

			store, err := a.store(cfg)
			if err != nil {
				return err
			}
			defer store.Close()
			run := &runner.Runner{
				Store:     store,
				Workspace: &workspace.Manager{WorkRoot: cfg.DataDir},
				Retention: retention,
				Now:       a.Now,
			}
			coordinator, err := experiment.New(experiment.Request{
				Store:            store,
				Runner:           run,
				Agent:            impl,
				AgentDescription: describe(impl),
				Config:           options,
				Model:            model,
				Suite:            selectedSuite,
				Tasks:            selected,
				Repetitions:      repetitions,
				Notes:            notes,
				DataDir:          cfg.DataDir,
				Progress:         func(p experiment.Progress) { a.printProgress(p) },
			})
			if err != nil {
				return Exitf(ExitUsage, "%v", err)
			}

			if a.verbose {
				configFile := cfg.File
				if configFile == "" {
					configFile = "(built-in defaults)"
				}
				tasksDir, _ := cfg.ResolveTasksDir()
				suitesDir, _ := cfg.ResolveSuitesDir()
				fmt.Fprintf(a.Err, "config:    %s\n", configFile)
				fmt.Fprintf(a.Err, "data:      %s\n", cfg.DataDir)
				fmt.Fprintf(a.Err, "tasks:     %s\n", tasksDir)
				fmt.Fprintf(a.Err, "suites:    %s\n", suitesDir)
				fmt.Fprintf(a.Err, "retention: %s\n", retention)
			}
			fmt.Fprintf(a.Err, "Running %d task selection(s) x %d repetition(s) with agent %q\n",
				len(selected), repetitions, impl.ID())

			ctx, stop := signal.NotifyContext(a.ctx(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			exp, attempts, runErr := coordinator.Run(ctx)
			if exp != nil {
				doc := report.Build(exp, attempts)
				if err := report.WriteTerminal(a.Out, doc); err != nil {
					return Exitf(ExitInfrastructure, "%v", err)
				}
				fmt.Fprintf(a.Out, "\nInspect with: %s\n", inspectHint(cfg.DataDir, exp.ID))
			}
			if runErr != nil {
				if errors.Is(runErr, context.Canceled) {
					return Exitf(ExitInterrupted, "run interrupted")
				}
				return Exitf(ExitInfrastructure, "%v", runErr)
			}
			return exitForAttempts(attempts)
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "agent adapter ID (see gauge agents list)")
	cmd.Flags().StringVar(&suiteID, "suite", "", "run all tasks in this suite")
	cmd.Flags().StringArrayVar(&taskIDs, "task", nil, "run this task (repeatable)")
	cmd.Flags().IntVar(&repetitions, "repetitions", 0, "attempts per task (default: configuration defaults.repetitions)")
	cmd.Flags().StringVar(&model, "model", "", "model override, applied only when the adapter supports it")
	cmd.Flags().StringVar(&notes, "notes", "", "free-form note stored with the experiment")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func (a *App) resolveSelection(tasks []*task.Task, suiteID string, taskIDs []string, cfg *config.Config) ([]*task.Task, *suite.Suite, error) {
	if suiteID != "" && len(taskIDs) > 0 {
		return nil, nil, Exitf(ExitUsage, "choose either --suite or --task, not both")
	}
	if suiteID == "" && len(taskIDs) == 0 {
		return nil, nil, Exitf(ExitUsage, "select tasks with --suite or at least one --task")
	}
	if suiteID != "" {
		suites, err := a.suites(cfg)
		if err != nil {
			return nil, nil, err
		}
		s, err := suite.Find(suites, suiteID)
		if err != nil {
			return nil, nil, Exitf(ExitUsage, "%v", err)
		}
		resolved, err := s.Resolve(tasks)
		if err != nil {
			return nil, nil, Exitf(ExitUsage, "%v", err)
		}
		return resolved, s, nil
	}
	seen := make(map[string]struct{}, len(taskIDs))
	selected := make([]*task.Task, 0, len(taskIDs))
	for _, id := range taskIDs {
		if _, dup := seen[id]; dup {
			return nil, nil, Exitf(ExitUsage, "task %q selected more than once", id)
		}
		seen[id] = struct{}{}
		t, err := task.Find(tasks, id)
		if err != nil {
			return nil, nil, Exitf(ExitUsage, "%v", err)
		}
		selected = append(selected, t)
	}
	return selected, nil, nil
}

func (a *App) printProgress(p experiment.Progress) {
	switch p.Phase {
	case experiment.PhaseAttemptStarted:
		fmt.Fprintf(a.Err, "[%d/%d] %s (rep %d) starting\n", p.Index, p.Total, p.Task.ID, p.Repetition+1)
	case experiment.PhaseAttemptFinished:
		attempt := p.Attempt
		detail := ""
		if attempt.Evaluation != nil && len(attempt.Evaluation.Checks) > 0 {
			c := attempt.Evaluation.ChecksSummary()
			detail = fmt.Sprintf(", %d/%d checks", c.Passed, c.Total)
		}
		fmt.Fprintf(a.Err, "[%d/%d] %s (rep %d) %s in %s (execution %s%s)\n",
			p.Index, p.Total, attempt.TaskID, attempt.Repetition+1, attempt.Outcome,
			formatDurationMS(attempt.DurationMS), attempt.Execution.Completion, detail)
	}
}

func exitForAttempts(attempts []*storage.Attempt) error {
	passed, failed, errored, notEvaluated := 0, 0, 0, 0
	for _, attempt := range attempts {
		switch attempt.Outcome {
		case storage.OutcomePassed:
			passed++
		case storage.OutcomeFailed:
			failed++
		case storage.OutcomeError:
			errored++
		default:
			notEvaluated++
		}
	}
	switch {
	case errored > 0 || notEvaluated > 0:
		return Exitf(ExitInfrastructure,
			"%d evaluator error(s) and %d unevaluated attempt(s); %d passed, %d failed",
			errored, notEvaluated, passed, failed)
	case failed > 0:
		return Exitf(ExitTaskFailed, "%d of %d evaluated attempt(s) failed", failed, failed+passed)
	case passed == 0:
		return Exitf(ExitInfrastructure, "no attempt produced a task result")
	default:
		return nil
	}
}

func formatDurationMS(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String()
}

// inspectHint prints a copy-pasteable command that resolves the same data
// directory even when the user no longer has the original config on hand.
func inspectHint(dataDir, expID string) string {
	return fmt.Sprintf("gauge --data-dir %s runs show %s", shellQuote(dataDir), shellQuote(expID))
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._/:@%+=", r) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
	}
	return value
}

func describe(a adapter.Adapter) string {
	if d, ok := a.(adapter.Describer); ok {
		return d.Description()
	}
	return ""
}
