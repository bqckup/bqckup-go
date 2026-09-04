package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/spf13/cobra"
)

func newRepairIndexCommand(opts *options) *cobra.Command {
	var destination string
	command := &cobra.Command{
		Use:   "repair-index <site>",
		Short: "Repair the index of one incremental site",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: backup repair-index requires exactly one site", ErrInvalidInput)
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
				var heartbeat *progressHeartbeat
				if opts.output != "json" {
					if err := writeRepairIndexStartText(cmd.ErrOrStderr(), args[0], destination); err != nil {
						return err
					}
					heartbeat = startProgressHeartbeat(cmd.ErrOrStderr(), "repair-index", args[0], "repairing")
				}
				outcome, err := application.RepairIndex(cmd.Context(), args[0], destination)
				if heartbeat != nil {
					heartbeat.Stop()
				}
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeRepairJSON(cmd, outcome)
				}
				return writeRepairText(cmd.OutOrStdout(), outcome)
			})
		},
	}
	command.Flags().StringVar(&destination, "destination", "", "storage destination of the site to repair (required)")
	return command
}

// repairEnvelope is the flat JSON repair report.
type repairEnvelope struct {
	Site              string  `json:"site"`
	Destination       string  `json:"destination"`
	Mode              string  `json:"mode"`
	Status            string  `json:"status"`
	DurationSeconds   float64 `json:"duration_seconds"`
	PacksProcessed    int     `json:"packs_processed"`
	BlobsIndexed      int     `json:"blobs_indexed"`
	OldIndexesRemoved int     `json:"old_indexes_removed"`
	NewIndexesWritten int     `json:"new_indexes_written"`
}

// writeRepairJSON renders the full repair outcome in JSON format.
func writeRepairJSON(cmd *cobra.Command, outcome backup.RepairOutcome) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(repairEnvelope{
		Site:              outcome.Site,
		Destination:       outcome.Destination,
		Mode:              outcome.Mode,
		Status:            "repaired",
		DurationSeconds:   outcome.Result.DurationSeconds,
		PacksProcessed:    outcome.Result.PacksProcessed,
		BlobsIndexed:      outcome.Result.BlobsIndexed,
		OldIndexesRemoved: outcome.Result.OldIndexesRemoved,
		NewIndexesWritten: outcome.Result.NewIndexesWritten,
	})
}

// writeRepairText renders the repair summary line in text format.
func writeRepairText(output io.Writer, outcome backup.RepairOutcome) error {
	newIndexesWord := "new index written"
	if outcome.Result.NewIndexesWritten != 1 {
		newIndexesWord = "new indexes written"
	}
	oldIndexesWord := "old indexes removed"
	if outcome.Result.OldIndexesRemoved == 1 {
		oldIndexesWord = "old index removed"
	}
	_, err := fmt.Fprintf(output, "repair-index %s/%s: %d packs processed, %d blobs indexed, %d %s, %d %s\n",
		outcome.Site,
		outcome.Destination,
		outcome.Result.PacksProcessed,
		outcome.Result.BlobsIndexed,
		outcome.Result.OldIndexesRemoved,
		oldIndexesWord,
		outcome.Result.NewIndexesWritten,
		newIndexesWord,
	)
	return err
}
