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
				if result.ReclaimedBytes > 0 {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: reclaimed %s\n", result.SiteName, humanBytes(result.ReclaimedBytes))
				}
				return err
			})
		},
	}
	run.Flags().BoolVar(&force, "force", false, "ignore the minimum backup interval")
	command.AddCommand(run)
	command.AddCommand(&cobra.Command{
		Use:   "unlock <site>",
		Short: "Remove stale repository locks for one site",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: backup unlock requires exactly one site", ErrInvalidInput)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				return application.UnlockRepository(cmd.Context(), args[0])
			})
		},
	})

	var destination string
	snapshots := &cobra.Command{
		Use:   "snapshots <site>",
		Short: "List the live snapshots of one incremental site",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: backup snapshots requires exactly one site", ErrInvalidInput)
			}
			return nil
		},
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if destination == "" {
				return fmt.Errorf("%w: --destination is required", ErrInvalidInput)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				listing, err := application.ListSiteSnapshots(cmd.Context(), args[0], destination)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeStorageJSON(cmd, listing)
				}
				return writeSnapshotText(cmd.OutOrStdout(), listing)
			})
		},
	}
	snapshots.Flags().StringVar(&destination, "destination", "", "storage destination of the site to list (required)")
	command.AddCommand(snapshots)
	return command
}

// humanBytes renders a byte count for run output.
func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
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
