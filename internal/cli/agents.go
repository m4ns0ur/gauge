package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/adapter"
)

type agentRow struct {
	ID           string `json:"id"`
	Description  string `json:"description,omitempty"`
	Enabled      bool   `json:"enabled"`
	ConfigStatus string `json:"config_status"`
	ConfigError  string `json:"config_error,omitempty"`
}

func (a *App) agentsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Inspect built-in agent adapters",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(a.agentsListCommand())
	return cmd
}

func (a *App) agentsListCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent adapters built into this Gauge",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			reg := a.registry()
			rows := make([]agentRow, 0, 2)
			for _, info := range reg.List() {
				impl, ok := reg.Get(info.ID)
				if !ok {
					continue
				}
				row := agentRow{ID: info.ID, Description: info.Description, Enabled: cfg.AdapterEnabled(info.ID)}
				options, optErr := cfg.AdapterOptions(info.ID)
				switch {
				case optErr != nil:
					row.ConfigStatus = "invalid"
					row.ConfigError = optErr.Error()
				default:
					validateErr := impl.Validate(a.ctx(), options)
					var unavailable *adapter.UnavailableError
					switch {
					case validateErr == nil:
						row.ConfigStatus = "valid"
					case errors.As(validateErr, &unavailable):
						// Valid configuration, missing external agent.
						row.ConfigStatus = "unavailable"
						row.ConfigError = validateErr.Error()
					default:
						row.ConfigStatus = "invalid"
						row.ConfigError = validateErr.Error()
					}
				}
				rows = append(rows, row)
			}
			if asJSON {
				return jsonOut(cmd.OutOrStdout(), rows)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tENABLED\tCONFIG\tDESCRIPTION")
			for _, row := range rows {
				enabled := "yes"
				if !row.Enabled {
					enabled = "no"
				}
				status := row.ConfigStatus
				if row.ConfigError != "" {
					status = fmt.Sprintf("%s (%s)", row.ConfigStatus, row.ConfigError)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.ID, enabled, status, row.Description)
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
