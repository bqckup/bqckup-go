package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
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

	var site string
	summary := &cobra.Command{
		Use:   "summary",
		Short: "Show a per-site backup report from configuration and history",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				if site != "" {
					if _, ok := application.Configuration().Site(site); !ok {
						return apperror.Wrap(apperror.CategoryConfig,
							fmt.Sprintf("site %q was not found", site), nil)
					}
				}
				runs, err := application.ListRuns(cmd.Context(), "", 0)
				if err != nil {
					return err
				}
				summaries := buildSummaries(application.Configuration(), runs, site)
				if opts.output == "json" {
					if site != "" {
						return writeJSON(cmd, summaries[0])
					}
					return writeJSON(cmd, summaries)
				}
				return writeSummaryText(cmd.OutOrStdout(), summaries)
			})
		},
	}
	summary.Flags().StringVar(&site, "site", "", "show only this site")
	command.AddCommand(summary)

	var force bool
	run := &cobra.Command{
		Use:   "run [site]",
		Short: "Run one backup site or every enabled site",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("%w: backup run accepts at most one site", ErrInvalidInput)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				if len(args) == 1 {
					result, err := application.RunBackup(cmd.Context(), args[0], force)
					if err != nil {
						return err
					}
					if opts.output == "json" {
						return writeJSON(cmd, result)
					}
					return writeRunResultText(cmd.OutOrStdout(), result)
				}

				results, err := application.RunEnabledBackups(cmd.Context(), force)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeJSON(cmd, results)
				}
				for _, result := range results {
					if err := writeRunResultText(cmd.OutOrStdout(), result); err != nil {
						return err
					}
				}
				return nil
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

	var snapshot, target string
	var quiet bool
	restore := &cobra.Command{
		Use:   "restore <site>",
		Short: "Restore one snapshot of an incremental site into a directory",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: backup restore requires exactly one site", ErrInvalidInput)
			}
			return nil
		},
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if destination == "" {
				return fmt.Errorf("%w: --destination is required", ErrInvalidInput)
			}
			if target == "" {
				return fmt.Errorf("%w: --target is required", ErrInvalidInput)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				confirm := resticRestoreOverwrite{force: force, in: cmd.InOrStdin(), out: cmd.ErrOrStderr()}.confirm
				result, err := application.RestoreSnapshot(cmd.Context(), args[0], destination, snapshot, target, confirm)
				if err != nil {
					return err
				}
				if quiet {
					return nil
				}
				if opts.output == "json" {
					return writeRestoreJSON(cmd, result)
				}
				return writeRestoreText(cmd.OutOrStdout(), result)
			})
		},
	}
	restore.Flags().StringVar(&destination, "destination", "", "storage destination of the site to restore from (required)")
	restore.Flags().StringVar(&snapshot, "snapshot", "latest", "snapshot id or prefix, or 'latest'")
	restore.Flags().StringVar(&target, "target", "", "directory to restore into (required)")
	restore.Flags().BoolVar(&force, "force", false, "overwrite existing files without asking")
	restore.Flags().BoolVar(&quiet, "quiet", false, "print nothing on success")
	command.AddCommand(restore)
	return command
}

func writeRunResultText(out io.Writer, result backup.RunResult) error {
	var err error
	if result.Status == backup.StatusSkipped {
		_, err = fmt.Fprintf(out, "%s: skipped (%s)\n", result.SiteName, result.SkipReason)
	} else {
		_, err = fmt.Fprintf(out, "%s: %s (run %s)\n", result.SiteName, result.Status, result.RunID)
	}
	if err != nil {
		return err
	}
	if result.ReclaimedBytes > 0 {
		_, err = fmt.Fprintf(out, "%s: reclaimed %s\n", result.SiteName, humanBytes(result.ReclaimedBytes))
	}
	return err
}

// resticRestoreOverwrite implements the engine's conflict confirmation: it
// lists every conflict, prompts once on stderr, and maps the outcome to
// the established error categories (preflight for non-terminal stdin,
// cancellation for a declined prompt).
type resticRestoreOverwrite struct {
	force bool
	in    io.Reader
	out   io.Writer
	tty   func(io.Reader) bool // injectable for tests; defaults to isTerminalReader
}

func (c resticRestoreOverwrite) confirm(conflicts []string) error {
	if c.force {
		return nil
	}
	for _, path := range conflicts {
		fmt.Fprintf(c.out, "  %s\n", path)
	}
	fmt.Fprintf(c.out, "Overwrite %d files? [y/N]\n", len(conflicts))
	check := c.tty
	if check == nil {
		check = isTerminalReader
	}
	if !check(c.in) {
		return apperror.Wrap(apperror.CategoryPreflight, fmt.Sprintf("restore would overwrite %d files; re-run with --force to overwrite them", len(conflicts)), nil)
	}
	line, _ := bufio.NewReader(c.in).ReadString('\n')
	if answer := strings.TrimSpace(line); answer != "y" && answer != "Y" {
		return apperror.Wrap(apperror.CategoryCancellation, "restore cancelled by user", nil)
	}
	return nil
}

// isTerminalReader reports whether r is a character device (a TTY).
func isTerminalReader(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// writeRestoreText renders the restore summary: the 8-character snapshot
// ID like the listing, then one line per skipped path.
func writeRestoreText(w io.Writer, result backup.RestoreResult) error {
	id := result.SnapshotID
	if len(id) > 8 {
		id = id[:8]
	}
	if _, err := fmt.Fprintf(w, "restored snapshot %s to %s (%d files, %s, %.1fs)\n", id, result.Target, result.FilesRestored, humanBytes(result.BytesRestored), result.DurationSeconds); err != nil {
		return err
	}
	for _, skipped := range result.SkippedPaths {
		if _, err := fmt.Fprintf(w, "skipped %s (not in this snapshot)\n", skipped); err != nil {
			return err
		}
	}
	return nil
}

// writeRestoreJSON renders the restore summary in the JSON schema, with
// the full snapshot ID.
func writeRestoreJSON(cmd *cobra.Command, result backup.RestoreResult) error {
	return writeJSON(cmd, result)
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
