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
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/spf13/cobra"
)

type siteView struct {
	Name             string     `json:"name"`
	Enabled          bool       `json:"enabled"`
	BackupMode       string     `json:"-"`
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
					view := siteView{Name: site.Name, Enabled: site.Enabled, BackupMode: site.BackupMode, FileSources: len(site.Sources.Files.Include), Destinations: destinations}
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
				return writeBackupListText(cmd.OutOrStdout(), views)
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
		Use:     "run [site]",
		Short:   "Run one backup site or every enabled site",
		Example: "  bqckup backup run incremental-test --force",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageError(cmd, "backup run accepts at most one site")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				if len(args) == 1 {
					var heartbeat *progressHeartbeat
					if opts.output != "json" {
						site, ok := application.Configuration().Site(args[0])
						if ok {
							progress := backupProgressForSite(site)
							if err := writeBackupStartText(cmd.ErrOrStderr(), progress); err != nil {
								return err
							}
							heartbeat = startProgressHeartbeat(cmd.ErrOrStderr(), "backup", progress.SiteName, "running")
						}
					}
					result, err := application.RunBackup(cmd.Context(), args[0], force)
					if heartbeat != nil {
						heartbeat.Stop()
					}
					if err != nil {
						return err
					}
					if opts.output == "json" {
						if err := writeJSON(cmd, result); err != nil {
							return err
						}
					} else {
						if err := writeRunResultText(cmd.OutOrStdout(), result); err != nil {
							return err
						}
					}
					return nil
				}

				var progressErr error
				var observer app.BackupRunObserver
				var heartbeat *progressHeartbeat
				if opts.output != "json" {
					observer = func(progress app.BackupRunProgress) {
						if progressErr != nil {
							return
						}
						if progress.Result == nil {
							progressErr = writeBackupStartText(cmd.ErrOrStderr(), progress)
							if progressErr == nil {
								heartbeat = startProgressHeartbeat(cmd.ErrOrStderr(), "backup", progress.SiteName, "running")
							}
							return
						}
						if heartbeat != nil {
							heartbeat.Stop()
							heartbeat = nil
						}
						progressErr = writeRunResultText(cmd.OutOrStdout(), *progress.Result)
						if progressErr == nil && progress.Error != nil {
							progressErr = writeFailureReason(cmd.ErrOrStderr(), progress.Error)
						}
					}
				}
				results, runErr := application.RunEnabledBackups(cmd.Context(), force, observer)
				if progressErr != nil {
					return progressErr
				}
				if progressErr != nil {
					return progressErr
				}
				if opts.output == "json" {
					if err := writeJSON(cmd, results); err != nil {
						return err
					}
				}
				if runErr != nil {
					return apperror.Wrap(apperror.CategoryOf(runErr), "one or more backups failed", nil)
				}
				return nil
			})
		},
	}
	run.Flags().BoolVar(&force, "force", false, "ignore the minimum backup interval")
	command.AddCommand(run)
	command.AddCommand(&cobra.Command{
		Use:     "unlock <site>",
		Short:   "Remove stale repository locks for one site",
		Example: "  bqckup backup unlock incremental-test",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, "backup unlock requires exactly one site")
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
		Use:     "snapshots <site> --destination <destination>",
		Short:   "List the live snapshots of one incremental site",
		Example: "  bqckup backup snapshots incremental-test --destination testing",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, "backup snapshots requires exactly one site")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if destination == "" {
				return usageError(cmd, "--destination is required")
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
		Use:     "restore <site> --destination <destination> --target <directory>",
		Short:   "Restore one snapshot of an incremental site into a directory",
		Example: "  bqckup backup restore incremental-test --destination testing --target /tmp/restore",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, "backup restore requires exactly one site")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if destination == "" {
				return usageError(cmd, "--destination is required")
			}
			if target == "" {
				return usageError(cmd, "--target is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				var heartbeat *progressHeartbeat
				if opts.output != "json" && !quiet {
					if err := writeRestoreStartText(cmd.ErrOrStderr(), args[0], destination, snapshot, target); err != nil {
						return err
					}
					heartbeat = startProgressHeartbeat(cmd.ErrOrStderr(), "restore", args[0], "restoring")
				}
				var onPrompt, onAnswer func()
				if heartbeat != nil {
					onPrompt = heartbeat.Pause
					onAnswer = heartbeat.Resume
				}
				confirm := resticRestoreOverwrite{
					force:    force,
					in:       cmd.InOrStdin(),
					out:      cmd.ErrOrStderr(),
					onPrompt: onPrompt,
					onAnswer: onAnswer,
				}.confirm
				result, err := application.RestoreSnapshot(cmd.Context(), args[0], destination, snapshot, target, confirm)
				if heartbeat != nil {
					heartbeat.Stop()
				}
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
	command.AddCommand(newCheckCommand(opts))
	command.AddCommand(newRepairIndexCommand(opts))
	return command
}

func writeBackupListText(output io.Writer, views []siteView) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(output, "No backup sites configured.")
		return err
	}
	siteWidth, modeWidth, destinationWidth := len("SITE"), len("MODE"), len("DESTINATIONS")
	for _, view := range views {
		mode := view.BackupMode
		if mode == "" {
			mode = "full"
		}
		destinations := strings.Join(view.Destinations, ", ")
		if len(view.Name) > siteWidth {
			siteWidth = len(view.Name)
		}
		if len(mode) > modeWidth {
			modeWidth = len(mode)
		}
		if len(destinations) > destinationWidth {
			destinationWidth = len(destinations)
		}
	}
	if _, err := fmt.Fprintf(output, "%-*s  %-7s  %-*s  %5s  %-*s\n", siteWidth, "SITE", "ENABLED", modeWidth, "MODE", "FILES", destinationWidth, "DESTINATIONS"); err != nil {
		return err
	}
	color := ansiColor{on: isTerminalWriter(output)}
	for _, view := range views {
		mode := view.BackupMode
		if mode == "" {
			mode = "full"
		}
		enabled := "no"
		if view.Enabled {
			enabled = "yes"
		}
		enabledField := fmt.Sprintf("%-7s", strings.ToUpper(enabled))
		if view.Enabled {
			enabledField = color.green(enabledField)
		} else {
			enabledField = color.dim(enabledField)
		}
		destinations := strings.Join(view.Destinations, ", ")
		if _, err := fmt.Fprintf(output, "%-*s  %s  %-*s  %5d  %-*s\n", siteWidth, view.Name, enabledField, modeWidth, mode, view.FileSources, destinationWidth, destinations); err != nil {
			return err
		}
	}
	return nil
}

func writeRunResultText(out io.Writer, result backup.RunResult) error {
	color := ansiColor{on: isTerminalWriter(out)}
	var err error
	if result.Status == backup.StatusSkipped {
		_, err = fmt.Fprintf(out, "%s %s: %s (%s)\n", color.yellow("[WARN]"), result.SiteName, color.status("skipped"), result.SkipReason)
	} else {
		_, err = fmt.Fprintf(out, "%s %s: %s (run %s)\n", colorResultSymbol(color, result.Status), result.SiteName, color.status(string(result.Status)), shortRunID(result.RunID))
	}
	if err != nil {
		return err
	}
	if result.ReclaimedBytes > 0 {
		_, err = fmt.Fprintf(out, "%s: reclaimed %s\n", result.SiteName, humanBytes(result.ReclaimedBytes))
	}
	return err
}

func shortRunID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func resultSymbol(status backup.Status) string {
	switch status {
	case backup.StatusSuccess:
		return "[OK]"
	case backup.StatusNoChange, backup.StatusCancelled:
		return "[WARN]"
	case backup.StatusFailed:
		return "[FAIL]"
	default:
		return "[.]"
	}
}

func colorResultSymbol(color ansiColor, status backup.Status) string {
	symbol := resultSymbol(status)
	switch status {
	case backup.StatusSuccess:
		return color.green(symbol)
	case backup.StatusNoChange, backup.StatusCancelled:
		return color.yellow(symbol)
	case backup.StatusFailed:
		return color.red(symbol)
	default:
		return color.cyan(symbol)
	}
}

func writeFailureReason(out io.Writer, err error) error {
	const width = 96
	words := strings.Fields(formatErrorMessage(err))
	line := "  Reason:"
	for _, word := range words {
		if len(line)+1+len(word) > width && line != "  Reason:" {
			if _, writeErr := fmt.Fprintln(out, line); writeErr != nil {
				return writeErr
			}
			line = "           " + word
			continue
		}
		line += " " + word
	}
	_, writeErr := fmt.Fprintln(out, line)
	return writeErr
}

func backupProgressForSite(site config.Site) app.BackupRunProgress {
	destinations := make([]string, 0, len(site.Destinations))
	for _, destination := range site.Destinations {
		destinations = append(destinations, destination.Storage)
	}
	return app.BackupRunProgress{
		SiteName:     site.Name,
		BackupMode:   site.BackupMode,
		Destinations: destinations,
	}
}

func writeBackupStartText(out io.Writer, progress app.BackupRunProgress) error {
	mode := progress.BackupMode
	if mode == "" {
		mode = "full"
	}
	destinations := strings.Join(progress.Destinations, ", ")
	color := ansiColor{on: isTerminalWriter(out)}
	_, err := fmt.Fprintf(out, "%s backup:%s: starting %s backup to %s\n", color.yellow("[>]"), progress.SiteName, mode, destinations)
	return err
}

// resticRestoreOverwrite implements the engine's conflict confirmation: it
// lists every conflict, prompts once on stderr, and maps the outcome to
// the established error categories (preflight for non-terminal stdin,
// cancellation for a declined prompt).
type resticRestoreOverwrite struct {
	force    bool
	in       io.Reader
	out      io.Writer
	tty      func(io.Reader) bool // injectable for tests; defaults to isTerminalReader
	onPrompt func()
	onAnswer func()
}

func (c resticRestoreOverwrite) confirm(conflicts []string) error {
	if c.force {
		return nil
	}
	if c.onPrompt != nil {
		c.onPrompt()
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
	if c.onAnswer != nil {
		c.onAnswer()
	}
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
	color := ansiColor{on: isTerminalWriter(w)}
	if _, err := fmt.Fprintf(w, "%s restored snapshot %s to %s (%d files, %s, %.1fs)\n", color.green("[OK]"), id, result.Target, result.FilesRestored, humanBytes(result.BytesRestored), result.DurationSeconds); err != nil {
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
