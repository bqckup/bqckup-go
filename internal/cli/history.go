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
	list.Flags().BoolVar(&details, "details", false, "show artifact details for every run")
	command.AddCommand(list)
	return command
}

func writeHistoryText(output io.Writer, runs []history.BackupRun, details bool) error {
	if len(runs) == 0 {
		_, err := fmt.Fprintln(output, "No backup history found for the selected filters.")
		return err
	}

	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "STATUS\tSITE\tSTARTED\tDURATION\tARTIFACTS\tDESTINATIONS\tLOGICAL SIZE\tRUN ID"); err != nil {
		return err
	}

	totalArtifacts := 0
	allDestinations := make(map[string]struct{})
	var totalSize int64
	for _, run := range runs {
		summary := summarizeArtifacts(run.Artifacts)
		totalArtifacts += summary.logicalCount
		totalSize += summary.logicalSize
		for destination := range summary.destinations {
			allDestinations[destination] = struct{}{}
		}
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			safeHistoryField(strings.ToUpper(string(run.Status))),
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
			if err := writeArtifactDetails(output, run); err != nil {
				return err
			}
		}
	}

	_, err := fmt.Fprintf(
		output,
		"\n%d %s, %d logical %s, %d %s, %s logical total\n",
		len(runs), plural(len(runs), "run", "runs"),
		totalArtifacts, plural(totalArtifacts, "artifact", "artifacts"),
		len(allDestinations), plural(len(allDestinations), "destination", "destinations"),
		humanBytes(totalSize),
	)
	return err
}

type artifactSummary struct {
	logicalCount int
	logicalSize  int64
	destinations map[string]struct{}
}

// summarizeArtifacts treats one source as one logical artifact. The runner
// records one copy of that artifact per destination, so summing every history
// row would inflate both artifact count and logical size.
func summarizeArtifacts(artifacts []history.Artifact) artifactSummary {
	logicalSizes := make(map[string]int64)
	destinations := make(map[string]struct{})
	for _, artifact := range artifacts {
		key := artifact.SourceKind + "\x00" + artifact.SourceName
		if current, exists := logicalSizes[key]; !exists || artifact.Size > current {
			logicalSizes[key] = artifact.Size
		}
		if artifact.Destination != "" {
			destinations[artifact.Destination] = struct{}{}
		}
	}
	var logicalSize int64
	for _, size := range logicalSizes {
		logicalSize += size
	}
	return artifactSummary{
		logicalCount: len(logicalSizes),
		logicalSize:  logicalSize,
		destinations: destinations,
	}
}

func writeArtifactDetails(output io.Writer, run history.BackupRun) error {
	if _, err := fmt.Fprintf(output, "\nArtifacts for run %s:\n", safeHistoryField(run.ID)); err != nil {
		return err
	}
	if len(run.Artifacts) == 0 {
		_, err := fmt.Fprintln(output, "  No artifacts recorded.")
		return err
	}

	artifacts := append([]history.Artifact(nil), run.Artifacts...)
	sort.SliceStable(artifacts, func(i, j int) bool {
		left := artifacts[i].SourceKind + "\x00" + artifacts[i].SourceName + "\x00" + artifacts[i].Destination + "\x00" + artifacts[i].ObjectKey
		right := artifacts[j].SourceKind + "\x00" + artifacts[j].SourceName + "\x00" + artifacts[j].Destination + "\x00" + artifacts[j].ObjectKey
		return left < right
	})

	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "  SOURCE\tDESTINATION\tSTATUS\tSIZE\tOBJECT KEY"); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		source := safeHistoryField(artifact.SourceKind) + "/" + safeHistoryField(artifact.SourceName)
		if _, err := fmt.Fprintf(
			table,
			"  %s\t%s\t%s\t%s\t%s\n",
			source,
			safeHistoryField(artifact.Destination),
			safeHistoryField(strings.ToUpper(string(artifact.Status))),
			humanBytes(artifact.Size),
			safeHistoryField(artifact.ObjectKey),
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
