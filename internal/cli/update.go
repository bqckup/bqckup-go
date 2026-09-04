package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bqckup/bqckup-go/internal/update"
	"github.com/spf13/cobra"
)

type updateRunner func(context.Context, update.Options) error

func updateVersionLabel(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || version == "latest" {
		return "latest"
	}
	return version
}

func warnIfNeedsSudo(out io.Writer, target string) {
	if out == nil || os.Geteuid() == 0 {
		return
	}
	dir := filepath.Dir(target)
	if !isSystemDirectory(dir) {
		return
	}
	checkPath := filepath.Join(dir, ".bqckup-write-check")
	file, err := os.OpenFile(checkPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_ = file.Close()
		_ = os.Remove(checkPath)
		return
	}
	color := ansiColor{on: isTerminalWriter(out)}
	_, _ = fmt.Fprintf(out, "%s this binary is installed in %s; run with sudo to update it\n", color.yellow("[WARN]"), dir)
}

func isSystemDirectory(path string) bool {
	switch path {
	case "/usr/local/bin", "/usr/bin", "/bin", "/opt/bin":
		return true
	default:
		return filepath.Clean(path) == "/usr/local/bin" || filepath.Clean(path) == "/usr/bin" || filepath.Clean(path) == "/bin"
	}
}

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
			if opts.output != "json" {
				warnIfNeedsSudo(cmd.ErrOrStderr(), os.Args[0])
			}

			var progress *CLIProgress
			if opts.output != "json" {
				color := ansiColor{on: isTerminalWriter(cmd.ErrOrStderr())}
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s update: downloading and installing %s release\n", color.yellow("[>]"), version); err != nil {
					return err
				}
				progress = NewCLIProgress(cmd.ErrOrStderr())
			}

			var result bytes.Buffer
			var updateProgress update.Progress
			if progress != nil {
				updateProgress = progress
			}
			err := run(cmd.Context(), update.Options{
				Version: version, Repository: repository, Output: &result, Progress: updateProgress,
			})
			if progress != nil {
				progress.Done()
			}
			if err != nil {
				return err
			}
			if progress != nil {
				if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "%s update completed: %s\n", ansiColor{on: isTerminalWriter(cmd.ErrOrStderr())}.green("[✓]"), updateVersionLabel(version)); writeErr != nil {
					return writeErr
				}
			}
			_, err = io.Copy(cmd.OutOrStdout(), &result)
			return err
		},
	}
	cmd.Flags().StringVar(&version, "version", "latest", "release version to install")
	cmd.Flags().StringVar(&repository, "repository", "bqckup/bqckup-go", "GitHub repository")
	return cmd
}
