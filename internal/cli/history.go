package cli

import (
	"fmt"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/spf13/cobra"
)

func newHistoryCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "history", Short: "Inspect backup history"}
	var site string
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List completed and running backup records",
		Args:  cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if limit < 1 {
				return fmt.Errorf("%w: --limit must be at least 1", ErrInvalidInput)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				runs, err := application.ListRuns(cmd.Context(), site, limit)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeJSON(cmd, runs)
				}
				for _, run := range runs {
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%d artifact(s)\n", run.StartedAt.UTC().Format("2006-01-02T15:04:05Z"), run.SiteName, run.Status, len(run.Artifacts))
					if err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	list.Flags().StringVar(&site, "site", "", "filter by site name")
	list.Flags().IntVar(&limit, "limit", 20, "maximum records to return")
	command.AddCommand(list)
	return command
}
