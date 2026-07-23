package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/bqckup/bqckup-go/internal/buildinfo"
	"github.com/spf13/cobra"
)

// NewRoot constructs the command tree without reading process-global state.
func NewRoot(info buildinfo.Info) *cobra.Command {
	root := &cobra.Command{
		Use:           "bqckup",
		Short:         "Reliable, self-hosted backups",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "bqckup %s (%s)\n", info.Version, info.Commit)
			return err
		},
	})

	return root
}

// Execute runs the root command and returns the process exit code.
func Execute(ctx context.Context, stdout, stderr io.Writer) int {
	root := NewRoot(buildinfo.Current())
	root.SetContext(ctx)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	return 0
}
