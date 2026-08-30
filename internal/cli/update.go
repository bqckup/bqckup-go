package cli

import (
	"github.com/bqckup/bqckup-go/internal/update"
	"github.com/spf13/cobra"
)

func newUpdateCommand(_ *options) *cobra.Command {
	var version string
	var repository string
	cmd := &cobra.Command{
		Use:     "update",
		Short:   "Update bqckup to the latest release",
		Example: "  sudo bqckup update\n  sudo bqckup update --version v0.0.5",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return update.Run(cmd.Context(), update.Options{
				Version: version, Repository: repository, Output: cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().StringVar(&version, "version", "latest", "release version to install")
	cmd.Flags().StringVar(&repository, "repository", "bqckup/bqckup-go", "GitHub repository")
	return cmd
}
