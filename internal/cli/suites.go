package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/suite"
)

type suiteRow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Tasks       int      `json:"tasks"`
	Fingerprint string   `json:"fingerprint"`
	TaskIDs     []string `json:"task_ids,omitempty"`
	File        string   `json:"file,omitempty"`
}

func (a *App) suitesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suites",
		Short: "Discover and inspect benchmark suites",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(a.suitesListCommand(), a.suitesShowCommand())
	return cmd
}

func (a *App) suitesListCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List and validate all suites",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			suites, err := a.suites(cfg)
			if err != nil {
				return err
			}
			tasks, err := a.tasks(a.ctx(), cfg)
			if err != nil {
				return err
			}
			rows := make([]suiteRow, 0, len(suites))
			for _, s := range suites {
				if _, err := s.Resolve(tasks); err != nil {
					return Exitf(ExitUsage, "%v", err)
				}
				ids := make([]string, 0, len(s.Tasks))
				for _, ref := range s.Tasks {
					ids = append(ids, ref.ID)
				}
				rows = append(rows, suiteRow{
					ID:          s.ID,
					Name:        s.Name,
					Tasks:       len(s.Tasks),
					Fingerprint: s.Fingerprint(),
					TaskIDs:     ids,
					File:        s.File,
				})
			}
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No suites found.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTASKS\tNAME")
			for _, row := range rows {
				fmt.Fprintf(tw, "%s\t%d\t%s\n", row.ID, row.Tasks, row.Name)
			}
			if err := tw.Flush(); err != nil {
				return Exitf(ExitInfrastructure, "write output: %v", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func (a *App) suitesShowCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <suite-id>",
		Short: "Show one suite and resolve its tasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			suites, err := a.suites(cfg)
			if err != nil {
				return err
			}
			s, err := suite.Find(suites, args[0])
			if err != nil {
				return Exitf(ExitUsage, "%v", err)
			}
			tasks, err := a.tasks(a.ctx(), cfg)
			if err != nil {
				return err
			}
			resolved, err := s.Resolve(tasks)
			if err != nil {
				return Exitf(ExitUsage, "%v", err)
			}
			type resolvedTask struct {
				ID          string `json:"id"`
				Category    string `json:"category"`
				Fingerprint string `json:"fingerprint"`
			}
			items := make([]resolvedTask, 0, len(resolved))
			for _, t := range resolved {
				fp, err := t.Fingerprint()
				if err != nil {
					return Exitf(ExitInfrastructure, "%v", err)
				}
				items = append(items, resolvedTask{ID: t.ID, Category: t.Category, Fingerprint: fp})
			}
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), struct {
					Suite       *suite.Suite   `json:"suite"`
					Tasks       []resolvedTask `json:"tasks"`
					Fingerprint string         `json:"fingerprint"`
				}{s, items, s.Fingerprint()})
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s — %s\n\n", s.ID, s.Name)
			if s.Description != "" {
				fmt.Fprintf(w, "%s\n\n", s.Description)
			}
			fmt.Fprintf(w, "Fingerprint: %s\n", s.Fingerprint())
			fmt.Fprintf(w, "File:        %s\n\n", s.File)
			tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TASK\tCATEGORY\tFINGERPRINT")
			for _, item := range items {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", item.ID, item.Category, item.Fingerprint)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}
