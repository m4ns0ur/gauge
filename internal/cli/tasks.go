package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/task"
)

type taskRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Difficulty string `json:"difficulty"`
	Tags       string `json:"tags,omitempty"`
	Timeout    string `json:"timeout"`
	Evaluator  string `json:"evaluator"`
}

func (a *App) tasksCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "Discover and inspect benchmark tasks",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(a.tasksListCommand(), a.tasksShowCommand())
	return cmd
}

func (a *App) tasksListCommand() *cobra.Command {
	var (
		asJSON   bool
		category string
		tag      string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List and validate all tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			tasks, err := a.tasks(a.ctx(), cfg)
			if err != nil {
				return err
			}
			rows := make([]taskRow, 0, len(tasks))
			for _, t := range tasks {
				if category != "" && t.Category != category {
					continue
				}
				if tag != "" && !hasTag(t, tag) {
					continue
				}
				rows = append(rows, taskRow{
					ID:         t.ID,
					Name:       t.Name,
					Category:   t.Category,
					Difficulty: string(t.Difficulty),
					Tags:       strings.Join(t.Tags, ","),
					Timeout:    t.Execution.Timeout.String(),
					Evaluator:  strings.Join(t.Evaluation.Command, " "),
				})
			}
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No tasks matched.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCATEGORY\tDIFFICULTY\tTIMEOUT\tTAGS\tNAME")
			for _, row := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					row.ID, row.Category, row.Difficulty, row.Timeout, row.Tags, row.Name)
			}
			if err := tw.Flush(); err != nil {
				return Exitf(ExitInfrastructure, "write output: %v", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	cmd.Flags().StringVar(&category, "category", "", "only show tasks in this category")
	cmd.Flags().StringVar(&tag, "tag", "", "only show tasks carrying this tag")
	return cmd
}

func (a *App) tasksShowCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <task-id>",
		Short: "Show one task definition, prompt, and evaluator",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			tasks, err := a.tasks(a.ctx(), cfg)
			if err != nil {
				return err
			}
			t, err := task.Find(tasks, args[0])
			if err != nil {
				return Exitf(ExitUsage, "%v", err)
			}
			fingerprint, err := t.Fingerprint()
			if err != nil {
				return Exitf(ExitInfrastructure, "%v", err)
			}
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), struct {
					Task        *task.Task `json:"task"`
					Fingerprint string     `json:"fingerprint"`
				}{t, fingerprint})
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s — %s\n\n", t.ID, t.Name)
			fmt.Fprintf(w, "Category:    %s\n", t.Category)
			fmt.Fprintf(w, "Difficulty:  %s\n", t.Difficulty)
			if t.Description != "" {
				fmt.Fprintf(w, "Description: %s\n", t.Description)
			}
			if len(t.Tags) > 0 {
				fmt.Fprintf(w, "Tags:        %s\n", strings.Join(t.Tags, ", "))
			}
			fmt.Fprintf(w, "Fingerprint: %s\n", fingerprint)
			fmt.Fprintf(w, "Directory:   %s\n", t.Dir)
			fmt.Fprintf(w, "Fixture:     %s @ %s\n", t.FixtureDir, t.Fixture.Revision)
			fmt.Fprintf(w, "Network:     %s\n", t.Execution.Network)
			fmt.Fprintf(w, "Timeout:     %s\n", t.Execution.Timeout)
			if len(t.Execution.Setup) > 0 {
				fmt.Fprintf(w, "Setup:\n")
				for _, argv := range t.Execution.Setup {
					fmt.Fprintf(w, "  - %s\n", strings.Join(argv, " "))
				}
			}
			fmt.Fprintf(w, "Evaluator:   %s (timeout %s)\n", strings.Join(t.Evaluation.Command, " "), t.Evaluation.Timeout)
			fmt.Fprintf(w, "Evaluator directory (never exposed to the agent): %s\n", t.EvaluatorDir)
			if t.Provenance != nil {
				if t.Provenance.Origin != "" {
					fmt.Fprintf(w, "Origin:      %s\n", t.Provenance.Origin)
				}
				if t.Provenance.License != "" {
					fmt.Fprintf(w, "License:     %s\n", t.Provenance.License)
				}
			}
			fmt.Fprintf(w, "\nPrompt (the only instruction the agent receives):\n\n%s\n", t.Prompt)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func hasTag(t *task.Task, tag string) bool {
	for _, candidate := range t.Tags {
		if candidate == tag {
			return true
		}
	}
	return false
}
