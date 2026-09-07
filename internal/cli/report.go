package cli

import (
	"fmt"
	"time"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/spf13/cobra"
)

func newReportCommand(opts *options) *cobra.Command {
	command := &cobra.Command{
		Use:   "report",
		Short: "Send scheduled backup summary reports",
	}
	command.AddCommand(newReportSendCommand(opts))
	return command
}

func newReportSendCommand(opts *options) *cobra.Command {
	command := &cobra.Command{
		Use:   "send <daily|monthly>",
		Short: "Send a daily or monthly backup report",
	}
	command.AddCommand(newReportSendDailyCommand(opts))
	command.AddCommand(newReportSendMonthlyCommand(opts))
	return command
}

func newReportSendDailyCommand(opts *options) *cobra.Command {
	var dateStr string
	cmd := &cobra.Command{
		Use:     "daily",
		Short:   "Send the daily backup summary report",
		Example: "  bqckup report send daily\n  bqckup report send daily --date 2025-01-15",
		Args:    cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if dateStr != "" {
				if _, err := time.Parse("2006-01-02", dateStr); err != nil {
					return usageError(cmd, "--date must be in YYYY-MM-DD format")
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				t := time.Now()
				if dateStr != "" {
					parsed, _ := time.Parse("2006-01-02", dateStr)
					t = parsed
				}
				if err := application.SendDailyReport(cmd.Context(), t); err != nil {
					return err
				}
				if opts.output != "json" {
					period := t.Format("2006-01-02")
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "[OK] daily report sent for %s\n", period)
					return err
				}
				return writeJSON(cmd, map[string]string{
					"report_type": "daily",
					"period":      t.Format("2006-01-02"),
					"status":      "sent",
				})
			})
		},
	}
	cmd.Flags().StringVar(&dateStr, "date", "", "calendar day to report on (YYYY-MM-DD, default: today)")
	return cmd
}

func newReportSendMonthlyCommand(opts *options) *cobra.Command {
	var monthStr string
	cmd := &cobra.Command{
		Use:     "monthly",
		Short:   "Send the monthly consolidated backup report",
		Example: "  bqckup report send monthly\n  bqckup report send monthly --month 2025-01",
		Args:    cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if monthStr != "" {
				if _, err := time.Parse("2006-01", monthStr); err != nil {
					return usageError(cmd, "--month must be in YYYY-MM format")
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				t := time.Now()
				if monthStr != "" {
					parsed, _ := time.Parse("2006-01", monthStr)
					t = parsed
				}
				if err := application.SendMonthlyReport(cmd.Context(), t); err != nil {
					return err
				}
				if opts.output != "json" {
					period := t.Format("2006-01")
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "[OK] monthly report sent for %s\n", period)
					return err
				}
				return writeJSON(cmd, map[string]string{
					"report_type": "monthly",
					"period":      t.Format("2006-01"),
					"status":      "sent",
				})
			})
		},
	}
	cmd.Flags().StringVar(&monthStr, "month", "", "calendar month to report on (YYYY-MM, default: current month)")
	return cmd
}
