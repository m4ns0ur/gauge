package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *App) versionCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print Gauge version and build identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), a.Version)
			}
			fmt.Fprintln(cmd.OutOrStdout(), a.Version.String())
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}
