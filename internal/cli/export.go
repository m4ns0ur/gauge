package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/report"
)

func (a *App) exportCommand() *cobra.Command {
	var (
		format string
		output string
	)
	cmd := &cobra.Command{
		Use:   "export <experiment-id>",
		Short: "Export an experiment as JSON or Markdown",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format = strings.ToLower(strings.TrimSpace(format))
			if format != "json" && format != "markdown" {
				return Exitf(ExitUsage, "--format must be json or markdown")
			}
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
			doc.Summary.Behavior = loadBehavior(store, attempts)

			w, closeOutput, path, err := writeTarget(cmd, output)
			if err != nil {
				return err
			}

			var writeErr error
			switch format {
			case "json":
				writeErr = report.WriteJSON(w, doc)
			case "markdown":
				writeErr = report.WriteMarkdown(w, doc)
			}
			if closeErr := closeOutput(); writeErr == nil {
				writeErr = closeErr
			}
			if writeErr != nil {
				return Exitf(ExitInfrastructure, "write export: %v", writeErr)
			}
			if path != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "output format: json or markdown")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to a file instead of stdout")
	return cmd
}
