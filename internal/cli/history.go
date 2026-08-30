package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/spf13/cobra"
)

func newHistoryCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "history", Short: "Inspect backup history"}
	var site string
	var limit int
	var details bool
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
				return writeHistoryText(cmd.OutOrStdout(), runs, details)
			})
		},
	}
	list.Flags().StringVar(&site, "site", "", "filter by site name")
	list.Flags().IntVar(&limit, "limit", 20, "maximum records to return")
	list.Flags().BoolVar(&details, "details", false, "show package details for every run")
	command.AddCommand(list)
	return command
}

func writeHistoryText(output io.Writer, runs []history.BackupRun, details bool) error {
	if len(runs) == 0 {
		_, err := fmt.Fprintln(output, "No backup history found for the selected filters.")
		return err
	}

	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	color := ansiColor{on: isTerminalWriter(output)}
	if _, err := fmt.Fprintln(table, "STATUS\tSITE\tSTARTED\tDURATION\tPACKAGES\tDESTINATIONS\tLOGICAL SIZE\tRUN ID"); err != nil {
		return err
	}

	totalPackages := 0
	allDestinations := make(map[string]struct{})
	var totalSize int64
	for _, run := range runs {
		summary := summarizePackages(run.Packages)
		totalPackages += summary.logicalCount
		totalSize += summary.logicalSize
		for destination := range summary.destinations {
			allDestinations[destination] = struct{}{}
		}
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			color.statusLabel(safeHistoryField(string(run.Status))),
			safeHistoryField(run.SiteName),
			run.StartedAt.Local().Format("02 Jan 2006, 15:04 MST"),
			formatRunDuration(run),
			summary.logicalCount,
			len(summary.destinations),
			humanBytes(summary.logicalSize),
			safeHistoryField(run.ID),
		); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}

	for _, run := range runs {
		if run.Status != history.StatusFailed && run.Status != history.StatusCancelled {
			continue
		}
		category := safeHistoryField(run.ErrorCategory)
		if run.ErrorCategory == "" {
			category = "unknown"
		}
		message := safeHistoryMessage(run.ErrorMessage)
		if run.ErrorMessage == "" {
			message = "message unavailable"
		}
		if _, err := fmt.Fprintf(output, "Run %s error [%s]: %s\n", safeHistoryField(run.ID), category, message); err != nil {
			return err
		}
	}

	if details {
		for _, run := range runs {
			if err := writePackageDetails(output, run); err != nil {
				return err
			}
		}
	}

	_, err := fmt.Fprintf(
		output,
		"\n%d %s, %d logical %s, %d %s, %s logical total\n",
		len(runs), plural(len(runs), "run", "runs"),
		totalPackages, plural(totalPackages, "package", "packages"),
		len(allDestinations), plural(len(allDestinations), "destination", "destinations"),
		humanBytes(totalSize),
	)
	return err
}

type packageSummary struct {
	logicalCount int
	logicalSize  int64
	destinations map[string]struct{}
}

// summarizePackages treats one source as one logical pkg. The runner
// records one copy of that package per destination, so summing every history
// row would inflate both package count and logical size.
func summarizePackages(packages []history.Package) packageSummary {
	logicalSizes := make(map[string]int64)
	destinations := make(map[string]struct{})
	for _, pkg := range packages {
		key := pkg.SourceKind + "\x00" + pkg.SourceName
		if current, exists := logicalSizes[key]; !exists || pkg.Size > current {
			logicalSizes[key] = pkg.Size
		}
		if pkg.Destination != "" {
			destinations[pkg.Destination] = struct{}{}
		}
	}
	var logicalSize int64
	for _, size := range logicalSizes {
		logicalSize += size
	}
	return packageSummary{
		logicalCount: len(logicalSizes),
		logicalSize:  logicalSize,
		destinations: destinations,
	}
}

func writePackageDetails(output io.Writer, run history.BackupRun) error {
	if _, err := fmt.Fprintf(output, "\nPackages for run %s:\n", safeHistoryField(run.ID)); err != nil {
		return err
	}
	if len(run.Packages) == 0 {
		_, err := fmt.Fprintln(output, "  No packages recorded.")
		return err
	}

	packages := append([]history.Package(nil), run.Packages...)
	sort.SliceStable(packages, func(i, j int) bool {
		left := packages[i].SourceKind + "\x00" + packages[i].SourceName + "\x00" + packages[i].Destination + "\x00" + packages[i].ObjectKey
		right := packages[j].SourceKind + "\x00" + packages[j].SourceName + "\x00" + packages[j].Destination + "\x00" + packages[j].ObjectKey
		return left < right
	})

	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	color := ansiColor{on: isTerminalWriter(output)}
	if _, err := fmt.Fprintln(table, "  SOURCE\tDESTINATION\tSTATUS\tSIZE\tOBJECT KEY"); err != nil {
		return err
	}
	for _, pkg := range packages {
		source := safeHistoryField(pkg.SourceKind) + "/" + safeHistoryField(pkg.SourceName)
		if _, err := fmt.Fprintf(
			table,
			"  %s\t%s\t%s\t%s\t%s\n",
			source,
			safeHistoryField(pkg.Destination),
			color.statusLabel(safeHistoryField(string(pkg.Status))),
			humanBytes(pkg.Size),
			safeHistoryField(pkg.ObjectKey),
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func formatRunDuration(run history.BackupRun) string {
	if run.Status == history.StatusRunning && run.FinishedAt == nil {
		return "in progress"
	}
	return (time.Duration(run.DurationMillis) * time.Millisecond).String()
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

var sensitiveHistoryFragments = []string{
	"access-key", "access_key", "authorization", "aws_", "child environment",
	"credential", "endpoint", "mysql_pwd", "password", "pgpassword",
	"provider response", "request id", "response body", "secret", "token",
}

// safeHistoryField is a defense-in-depth boundary for old or externally
// modified history databases. Normal history values are already validated or
// redacted before persistence.
func safeHistoryField(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "-"
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") || strings.Contains(lower, `:\`) ||
		strings.Contains(lower, "../") || strings.Contains(lower, `..\`) {
		return "[redacted]"
	}
	for _, field := range strings.Fields(value) {
		if strings.HasPrefix(field, "/") {
			return "[redacted]"
		}
	}
	for _, fragment := range sensitiveHistoryFragments {
		if strings.Contains(lower, fragment) {
			return "[redacted]"
		}
	}
	return value
}

func safeHistoryMessage(message string) string {
	return safeHistoryField(message)
}
