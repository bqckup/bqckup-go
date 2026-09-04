package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/bqckup/bqckup-go/internal/update"
	"github.com/spf13/cobra"
)

type updateRunner func(context.Context, update.Options) error

func newUpdateCommand(opts *options) *cobra.Command {
	return newUpdateCommandWithRunner(opts, update.Run)
}

func newUpdateCommandWithRunner(opts *options, run updateRunner) *cobra.Command {
	var version string
	var repository string
	cmd := &cobra.Command{
		Use:     "update",
		Short:   "Update bqckup to the latest release",
		Example: "  sudo bqckup update\n  sudo bqckup update --version v0.0.5",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var heartbeat *progressHeartbeat
			if opts.output != "json" {
				color := ansiColor{on: isTerminalWriter(cmd.ErrOrStderr())}
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s update: downloading and installing %s release\n", color.yellow("[>]"), version); err != nil {
					return err
				}
				heartbeat = startProgressHeartbeat(cmd.ErrOrStderr(), "update")
			}

			var result bytes.Buffer
			err := run(cmd.Context(), update.Options{
				Version: version, Repository: repository, Output: &result,
			})
			if heartbeat != nil {
				heartbeat.Stop()
			}
			if err != nil {
				return err
			}
			_, err = io.Copy(cmd.OutOrStdout(), &result)
			return err
		},
	}
	cmd.Flags().StringVar(&version, "version", "latest", "release version to install")
	cmd.Flags().StringVar(&repository, "repository", "bqckup/bqckup-go", "GitHub repository")
	return cmd
}
