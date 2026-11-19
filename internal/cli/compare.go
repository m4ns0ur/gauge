package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/report"
)

func (a *App) compareCommand() *cobra.Command {
	var (
		format string
		output string
	)
	cmd := &cobra.Command{
		Use:   "compare <experiment-a> <experiment-b>",
		Short: "Compare two experiments with compatibility guards",
		Long: `Compare two recorded experiments and show per-task regressions and improvements.

Both experiments must use the same task selection: same task IDs, content
fingerprints, and fixture revisions. Recorded differences in agent, model,
adapter configuration, repetitions, Gauge version, and environment are reported
but do not block the comparison. A comparison with different benchmark content
is refused rather than presented as if it were meaningful.

Use a holdout suite for final comparisons: tune on the public suites, then
compare the holdout results separately.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			format = strings.ToLower(strings.TrimSpace(format))
			switch format {
			case "text", "markdown", "json":
			default:
				return Exitf(ExitUsage, "--format must be text, markdown, or json")
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

			left, err := store.GetExperiment(args[0])
			if err != nil {
				return notFoundError(err, store, "experiment", args[0])
			}
			right, err := store.GetExperiment(args[1])
			if err != nil {
				return notFoundError(err, store, "experiment", args[1])
			}
			leftAttempts, leftErr := store.ListAttempts(left.ID)
			if leftErr != nil {
				fmt.Fprintf(a.Err, "warning: %v\n", leftErr)
			}
			rightAttempts, rightErr := store.ListAttempts(right.ID)
			if rightErr != nil {
				fmt.Fprintf(a.Err, "warning: %v\n", rightErr)
			}

			comparison := report.BuildComparison(
				left, right,
				leftAttempts, rightAttempts,
				loadBehavior(store, leftAttempts),
				loadBehavior(store, rightAttempts),
			)
			if !comparison.Compatible {
				return Exitf(ExitUsage, "experiments are not comparable:\n  - %s",
					strings.Join(comparison.Incompatibilities, "\n  - "))
			}

			w, closeOutput, path, err := writeTarget(cmd, output)
			if err != nil {
				return err
			}
			var writeErr error
			switch format {
			case "text":
				writeErr = report.WriteComparisonTerminal(w, comparison)
			case "markdown":
				writeErr = report.WriteComparisonMarkdown(w, comparison)
			case "json":
				writeErr = report.WriteComparisonJSON(w, comparison)
			}
			if closeErr := closeOutput(); writeErr == nil {
				writeErr = closeErr
			}
			if writeErr != nil {
				return Exitf(ExitInfrastructure, "write comparison: %v", writeErr)
			}
			if path != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, markdown, or json")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to a file instead of stdout")
	return cmd
}

// writeTarget resolves --output for report commands. It returns stdout when no
// file is requested and a close function for a created file.
func writeTarget(cmd *cobra.Command, output string) (io.Writer, func() error, string, error) {
	noop := func() error { return nil }
	if output == "" || output == "-" {
		return cmd.OutOrStdout(), noop, "", nil
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return nil, nil, "", Exitf(ExitUsage, "resolve output path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, nil, "", Exitf(ExitInfrastructure, "create output directory: %v", err)
	}
	file, err := os.Create(absolute)
	if err != nil {
		return nil, nil, "", Exitf(ExitInfrastructure, "create %s: %v", absolute, err)
	}
	return file, file.Close, absolute, nil
}
