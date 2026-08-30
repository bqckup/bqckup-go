package cli

import (
	"fmt"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/spf13/cobra"
)

func newDoctorCommand(opts *options) *cobra.Command {
	var siteFilter string
	command := &cobra.Command{
		Use:     "doctor",
		Short:   "Preflight diagnostics and dependency checks",
		Example: "  bqckup doctor --site incremental-test",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checker, err := app.OpenDoctor(cmd.Context(), opts.configDir)
			if err != nil {
				return err
			}
			report, err := checker.Run(cmd.Context(), siteFilter)
			if err != nil {
				return usageError(cmd, err.Error())
			}
			if opts.output == "json" {
				if err := writeJSON(cmd, report); err != nil {
					return err
				}
			} else {
				color := ansiColor{on: isTerminalWriter(cmd.OutOrStdout())}
				for _, check := range report.Checks {
					statusSymbol := "[OK]"
					statusColor := color.green(statusSymbol)
					if check.Status == "fail" {
						statusSymbol = "[FAIL]"
						statusColor = color.red(statusSymbol)
					} else if check.Status == "skipped" {
						statusSymbol = "[WARN]"
						statusColor = color.yellow(statusSymbol)
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", statusColor, check.Name, check.Message)
				}
			}

			if !report.Passed {
				return apperror.Wrap(apperror.CategoryPreflight, "doctor diagnostic checks failed", nil)
			}
			return nil
		},
	}
	command.Flags().StringVar(&siteFilter, "site", "", "filter diagnostic checks for a specific site")
	return command
}
