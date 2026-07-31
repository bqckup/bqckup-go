package cli

import (
	"fmt"
	"sort"
	"time"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/spf13/cobra"
)

type siteView struct {
	Name             string     `json:"name"`
	Enabled          bool       `json:"enabled"`
	FileSources      int        `json:"file_sources"`
	Destinations     []string   `json:"destinations"`
	LastSuccessfulAt *time.Time `json:"last_successful_at,omitempty"`
}

func newBackupCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "backup", Short: "List or run backups"}
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured backup sites",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				configuration := application.Configuration()
				views := make([]siteView, 0, len(configuration.Sites))
				for _, site := range configuration.Sites {
					destinations := make([]string, 0, len(site.Destinations))
					for _, destination := range site.Destinations {
						destinations = append(destinations, destination.Storage)
					}
					last, err := application.LastSuccessful(cmd.Context(), site.Name)
					if err != nil {
						return err
					}
					view := siteView{Name: site.Name, Enabled: site.Enabled, FileSources: len(site.Sources.Files.Include), Destinations: destinations}
					if last != nil {
						lastAt := last.StartedAt.UTC()
						view.LastSuccessfulAt = &lastAt
					}
					views = append(views, view)
				}
				sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
				if opts.output == "json" {
					return writeJSON(cmd, views)
				}
				for _, view := range views {
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\tenabled=%t\tfiles=%d\tdestinations=%v\n", view.Name, view.Enabled, view.FileSources, view.Destinations)
					if err != nil {
						return err
					}
				}
				return nil
			})
		},
	})

	var force bool
	run := &cobra.Command{
		Use:   "run <site>",
		Short: "Run one configured backup site",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: backup run requires exactly one site", ErrInvalidInput)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				result, err := application.RunBackup(cmd.Context(), args[0], force)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeJSON(cmd, result)
				}
				if result.Status == "skipped" {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped (%s)\n", result.SiteName, result.SkipReason)
				} else {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (run %s)\n", result.SiteName, result.Status, result.RunID)
				}
				return err
			})
		},
	}
	run.Flags().BoolVar(&force, "force", false, "ignore the minimum backup interval")
	command.AddCommand(run)
	return command
}

func withApplication(cmd *cobra.Command, configDir string, operation func(*app.App) error) error {
	application, err := app.Open(cmd.Context(), configDir)
	if err != nil {
		return err
	}
	operationErr := operation(application)
	closeErr := application.Close()
	if operationErr != nil {
		return operationErr
	}
	return closeErr
}
