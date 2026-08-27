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
		Use:   "doctor",
		Short: "Preflight diagnostics and dependency checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checker, err := app.OpenDoctor(cmd.Context(), opts.configDir)
			if err != nil {
				return err
			}
			report, err := checker.Run(cmd.Context(), siteFilter)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrInvalidInput, err)
			}
			if opts.output == "json" {
				if err := writeJSON(cmd, report); err != nil {
					return err
				}
			} else {
				for _, check := range report.Checks {
					statusSymbol := "[✓]"
					if check.Status == "fail" {
						statusSymbol = "[✗]"
					} else if check.Status == "skipped" {
						statusSymbol = "[-]"
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", statusSymbol, check.Name, check.Message)
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
