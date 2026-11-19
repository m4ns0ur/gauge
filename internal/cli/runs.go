package cli

import (
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/metrics"
	"github.com/m4ns0ur/gauge/internal/report"
	"github.com/m4ns0ur/gauge/internal/storage"
)

type runListItem struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	Agent           string    `json:"agent"`
	Model           string    `json:"model,omitempty"`
	Suite           string    `json:"suite,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Attempts        int       `json:"attempts"`
	Evaluated       int       `json:"evaluated"`
	Passed          int       `json:"passed"`
	Failed          int       `json:"failed"`
	EvaluatorErrors int       `json:"evaluator_errors"`
	NotEvaluated    int       `json:"not_evaluated"`
	PassRate        *float64  `json:"pass_rate,omitempty"`
}

func (a *App) runsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List and inspect recorded experiments",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(a.runsListCommand(), a.runsShowCommand())
	return cmd
}

func (a *App) runsListCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List experiments, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			store, err := a.store(cfg)
			if err != nil {
				return err
			}
			defer store.Close()
			experiments, listErr := store.ListExperiments()
			// Unreadable experiment directories do not hide the readable ones.
			if listErr != nil {
				fmt.Fprintf(a.Err, "warning: %v\n", listErr)
			}
			items := make([]runListItem, 0, len(experiments))
			for _, exp := range experiments {
				attempts, err := store.ListAttempts(exp.ID)
				if err != nil {
					fmt.Fprintf(a.Err, "warning: %v\n", err)
				}
				summary := metrics.Aggregate(exp.ID, attempts)
				item := runListItem{
					ID:              exp.ID,
					Status:          exp.Status,
					Agent:           exp.Agent.ID,
					Model:           exp.Agent.Model,
					CreatedAt:       exp.CreatedAt,
					Attempts:        summary.Attempts,
					Evaluated:       summary.Evaluated,
					Passed:          summary.Passed,
					Failed:          summary.Failed,
					EvaluatorErrors: summary.EvaluatorErrors,
					NotEvaluated:    summary.NotEvaluated,
					PassRate:        summary.PassRate,
				}
				if exp.Suite != nil {
					item.Suite = exp.Suite.ID
				}
				items = append(items, item)
			}
			sort.SliceStable(items, func(i, j int) bool {
				if items[i].CreatedAt.Equal(items[j].CreatedAt) {
					return items[i].ID > items[j].ID
				}
				return items[i].CreatedAt.After(items[j].CreatedAt)
			})
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), items)
			}
			if len(items) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No experiments recorded in %s. Run `gauge run` first.\n", store.Root())
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "EXPERIMENT\tAGENT\tMODEL\tSTATUS\tCREATED\tPASS RATE\tERRORS")
			for _, item := range items {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
					item.ID, item.Agent, display(item.Model), item.Status,
					item.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"),
					passRate(item), item.EvaluatorErrors+item.NotEvaluated)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func (a *App) runsShowCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <experiment-id>",
		Short: "Show one experiment and its per-task results",
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
			defer store.Close()
			exp, err := store.GetExperiment(args[0])
			if err != nil {
				return notFoundError(err, store, "experiment", args[0])
			}
			attempts, listErr := store.ListAttempts(exp.ID)
			if listErr != nil {
				fmt.Fprintf(a.Err, "warning: %v\n", listErr)
			}
			doc := report.Build(exp, attempts)
			if asJSON {
				return report.WriteJSON(cmd.OutOrStdout(), doc)
			}
			if err := report.WriteTerminal(cmd.OutOrStdout(), doc); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nArtifacts: %s\n", expDirFor(store, exp.ID))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func expDirFor(store *storage.Store, expID string) string {
	dir, err := store.ExperimentDir(expID)
	if err != nil {
		return ""
	}
	return dir
}

func passRate(item runListItem) string {
	if item.PassRate == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", item.Passed, item.Evaluated, *item.PassRate*100)
}

func display(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
