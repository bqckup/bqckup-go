package cli

import (
	"encoding/json"
	"fmt"

	appconfig "github.com/bqckup/bqckup-go/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Inspect configuration"}
	command.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Strictly validate configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			configuration, err := appconfig.Load(cmd.Context(), opts.configDir)
			if err != nil {
				return err
			}
			if opts.output == "json" {
				return writeJSON(cmd, map[string]any{
					"valid": true, "version": configuration.Version,
					"config_directory": opts.configDir, "sites": len(configuration.Sites), "storages": len(configuration.Storages),
				})
			}
			color := ansiColor{on: isTerminalWriter(cmd.OutOrStdout())}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s config: schema v%d valid (%d site(s), %d storage(s))\n", color.green("[OK]"), configuration.Version, len(configuration.Sites), len(configuration.Storages))
			return err
		},
	})
	return command
}

func writeJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
