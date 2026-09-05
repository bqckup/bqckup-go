package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	backupincremental "github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/spf13/cobra"
)

// errCheckProblems reports a check that ran but found problems; ExitCode
// maps it to 1, distinct from the command-failure codes (2 config, 3
// preflight, 4 storage) and from errNoChange's 5.
var errCheckProblems = errors.New("backup check found problems")

// textFindingCap is how many finding lines text stdout prints before
// pointing at --findings-file.
const textFindingCap = 100

func newCheckCommand(opts *options) *cobra.Command {
	var destination, findingsFile string
	var readData bool
	command := &cobra.Command{
		Use:   "check <site>",
		Short: "Check the repository of one incremental site",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: backup check requires exactly one site", ErrInvalidInput)
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
				outcome, err := application.CheckRepository(cmd.Context(), args[0], destination, readData)
				if err != nil {
					return err
				}
				if findingsFile != "" {
					if err := writeFindingsFile(findingsFile, opts.output, outcome.Result.Findings); err != nil {
						return apperror.Wrap(apperror.CategoryPreflight, "could not write the findings file", err)
					}
				}
				if opts.output == "json" {
					if err := writeCheckJSON(cmd, outcome); err != nil {
						return err
					}
				} else if err := writeCheckText(cmd.OutOrStdout(), outcome); err != nil {
					return err
				}
				if outcome.Result.Status == "problems" {
					return errCheckProblems
				}
				return nil
			})
		},
	}
	command.Flags().StringVar(&destination, "destination", "", "storage destination of the site to check (required)")
	command.Flags().BoolVar(&readData, "read-data", false, "also read and authenticate every stored blob")
	command.Flags().StringVar(&findingsFile, "findings-file", "", "write the complete finding list to this file")
	return command
}

// checkEnvelope is the flat JSON check report.
type checkEnvelope struct {
	Site            string                      `json:"site"`
	Destination     string                      `json:"destination"`
	Mode            string                      `json:"mode"`
	ReadData        bool                        `json:"read_data"`
	Status          string                      `json:"status"`
	DurationSeconds float64                     `json:"duration_seconds"`
	Indexes         int                         `json:"indexes"`
	Snapshots       int                         `json:"snapshots"`
	Packs           int                         `json:"packs"`
	Blobs           int                         `json:"blobs"`
	Findings        []backupincremental.Finding `json:"findings"`
}

// writeCheckJSON renders the full check result, uncapped.
func writeCheckJSON(cmd *cobra.Command, outcome backup.CheckOutcome) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(checkEnvelope{
		Site:            outcome.Site,
		Destination:     outcome.Destination,
		Mode:            outcome.Mode,
		ReadData:        outcome.Result.ReadData,
		Status:          outcome.Result.Status,
		DurationSeconds: outcome.Result.DurationSeconds,
		Indexes:         outcome.Result.Indexes,
		Snapshots:       outcome.Result.Snapshots,
		Packs:           outcome.Result.Packs,
		Blobs:           outcome.Result.Blobs,
		Findings:        outcome.Result.Findings,
	})
}

// writeCheckText renders the check summary and one line per finding,
// capped at textFindingCap lines with a pointer to --findings-file.
func writeCheckText(output io.Writer, outcome backup.CheckOutcome) error {
	if outcome.Result.Status == "healthy" {
		_, err := fmt.Fprintf(output, "check %s/%s: healthy\n", outcome.Site, outcome.Destination)
		return err
	}
	summary := fmt.Sprintf("check %s/%s: problems found (", outcome.Site, outcome.Destination)
	findings := outcome.Result.Findings
	for i := 0; i < len(findings); {
		j := i
		for j < len(findings) && findings[j].Type == findings[i].Type {
			j++
		}
		if i > 0 {
			summary += ", "
		}
		summary += fmt.Sprintf("%d %s", j-i, findings[i].Type)
		i = j
	}
	if _, err := fmt.Fprintln(output, summary+")"); err != nil {
		return err
	}
	limit := len(findings)
	if limit > textFindingCap {
		limit = textFindingCap
	}
	for _, finding := range findings[:limit] {
		if _, err := fmt.Fprintln(output, checkFindingLine(finding)); err != nil {
			return err
		}
	}
	if extra := len(findings) - limit; extra > 0 {
		_, err := fmt.Fprintf(output, "and %d more findings (see --findings-file)\n", extra)
		return err
	}
	return nil
}

// checkFindingLine renders one finding in the text format: "<type> <id>"
// plus the fields the type carries.
func checkFindingLine(finding backupincremental.Finding) string {
	switch finding.Type {
	case "broken_config":
		return "broken_config config"
	case "missing_blob":
		return fmt.Sprintf("missing_blob %s (snapshot %s)", finding.ID, finding.SnapshotID)
	case "corrupt_blob":
		if finding.PackID != "" {
			return fmt.Sprintf("corrupt_blob %s (pack %s)", finding.ID, finding.PackID)
		}
		return fmt.Sprintf("corrupt_blob %s (snapshot %s)", finding.ID, finding.SnapshotID)
	case "missing_pack":
		return fmt.Sprintf("missing_pack %s (%d blobs)", finding.ID, finding.BlobCount)
	default:
		return fmt.Sprintf("%s %s", finding.Type, finding.ID)
	}
}

// writeFindingsFile writes the complete finding list to path: one finding
// per line in text mode, the findings array in JSON mode. The file is
// always written when requested, even with zero findings.
func writeFindingsFile(path, output string, findings []backupincremental.Finding) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if output == "json" {
		if findings == nil {
			findings = []backupincremental.Finding{}
		}
		encoder := json.NewEncoder(file)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(findings)
	}
	for _, finding := range findings {
		if _, err := fmt.Fprintln(file, checkFindingLine(finding)); err != nil {
			return err
		}
	}
	return nil
}
