package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/spf13/cobra"
)

func newRekeyCommand(opts *options) *cobra.Command {
	var destination, newPasswordFile string
	command := &cobra.Command{
		Use:   "rekey <site>",
		Short: "Replace the password of one incremental site's repository",
		Long: `Replace the password of one incremental site's repository without touching the data.

The new password is read from --new-password-file. The old password is the one
in the site YAML (incremental.password). The new key is verified before the old
key is removed. After a successful rekey, update incremental.password in the
site YAML to the new password; until then backups of this site fail.`,
		Example: "  bqckup backup rekey ojt.site --destination sgbucket --new-password-file /root/new.pass",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: backup rekey requires exactly one site", ErrInvalidInput)
			}
			return nil
		},
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if destination == "" {
				return fmt.Errorf("%w: --destination is required", ErrInvalidInput)
			}
			if newPasswordFile == "" {
				return fmt.Errorf("%w: --new-password-file is required", ErrInvalidInput)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			newPassword, err := readPasswordFile(newPasswordFile)
			if err != nil {
				return err
			}
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				outcome, err := application.RekeySite(cmd.Context(), args[0], destination, newPassword)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeRekeyJSON(cmd, outcome)
				}
				return writeRekeyText(cmd.OutOrStdout(), outcome)
			})
		},
	}
	command.Flags().StringVar(&destination, "destination", "", "storage destination of the site to rekey (required)")
	command.Flags().StringVar(&newPasswordFile, "new-password-file", "", "file containing the new repository password (required)")
	return command
}

// readPasswordFile returns the first line of the file without its line ending.
// The password never comes from a command-line argument.
func readPasswordFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: could not read --new-password-file: %v", ErrInvalidInput, err)
	}
	password := strings.TrimRight(string(data), "\r\n")
	if password == "" {
		return "", fmt.Errorf("%w: the new password file is empty", ErrInvalidInput)
	}
	return password, nil
}

type rekeyEnvelope struct {
	Site            string `json:"site"`
	Destination     string `json:"destination"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	KeyFileAdded    int    `json:"key_file_added"`
	KeyFilesRemoved int    `json:"key_files_removed"`
}

func writeRekeyJSON(cmd *cobra.Command, outcome backup.RekeyOutcome) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(rekeyEnvelope{
		Site:            outcome.Site,
		Destination:     outcome.Destination,
		Mode:            outcome.Mode,
		Status:          "rekeyed",
		KeyFileAdded:    outcome.Result.KeyFileAdded,
		KeyFilesRemoved: outcome.Result.KeyFilesRemoved,
	})
}

func writeRekeyText(output io.Writer, outcome backup.RekeyOutcome) error {
	_, err := fmt.Fprintf(output, "rekey %s/%s: added %d key file, removed %d old key file(s); update incremental.password in the site YAML now\n",
		outcome.Site,
		outcome.Destination,
		outcome.Result.KeyFileAdded,
		outcome.Result.KeyFilesRemoved,
	)
	return err
}
