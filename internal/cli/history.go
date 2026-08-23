package cli

import (
	"fmt"
	"io"
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
				return writeHistoryText(cmd.OutOrStdout(), runs)
			})
		},
	}
	list.Flags().StringVar(&site, "site", "", "filter by site name")
	list.Flags().IntVar(&limit, "limit", 20, "maximum records to return")
	command.AddCommand(list)
	return command
}

func writeHistoryText(output io.Writer, runs []history.BackupRun) error {
	if len(runs) == 0 {
		_, err := fmt.Fprintln(output, "No backup history found.")
		return err
	}

	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "STATUS\tSITE\tSTARTED\tDURATION\tARTIFACTS\tSIZE\tRUN ID"); err != nil {
		return err
	}

	totalArtifacts := 0
	var totalSize int64
	for _, run := range runs {
		runSize := artifactBytes(run.Artifacts)
		totalArtifacts += len(run.Artifacts)
		totalSize += runSize
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			strings.ToUpper(string(run.Status)),
			run.SiteName,
			run.StartedAt.Local().Format("02 Jan 2006, 15:04 MST"),
			formatRunDuration(run),
			len(run.Artifacts),
			humanBytes(runSize),
			run.ID,
		); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}

	for _, run := range runs {
		if run.ErrorMessage == "" {
			continue
		}
		message := strings.Join(strings.Fields(run.ErrorMessage), " ")
		if run.ErrorCategory != "" {
			if _, err := fmt.Fprintf(output, "Error [%s]: %s\n", run.ErrorCategory, message); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(output, "Error: %s\n", message); err != nil {
			return err
		}
	}

	_, err := fmt.Fprintf(
		output,
		"\n%d %s, %d %s, %s total\n",
		len(runs), plural(len(runs), "run", "runs"),
		totalArtifacts, plural(totalArtifacts, "artifact", "artifacts"),
		humanBytes(totalSize),
	)
	return err
}

func artifactBytes(artifacts []history.Artifact) int64 {
	var total int64
	for _, artifact := range artifacts {
		total += artifact.Size
	}
	return total
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
