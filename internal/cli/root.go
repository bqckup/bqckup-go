package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/buildinfo"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/spf13/cobra"
)

var ErrInvalidInput = errors.New("invalid command input")

type options struct {
	configDir string
	output    string
}

// NewRoot constructs the command tree without reading configuration or opening state.
func NewRoot(info buildinfo.Info) *cobra.Command {
	defaultConfigDir := "/etc/bqckup"
	if value := os.Getenv("BQCKUP_CONFIG_DIR"); value != "" {
		defaultConfigDir = value
	}
	opts := &options{}
	root := &cobra.Command{
		Use:           "bqckup",
		Short:         "Reliable, self-hosted backups",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.output != "text" && opts.output != "json" {
				return fmt.Errorf("%w: --output must be text or json", ErrInvalidInput)
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&opts.configDir, "config-dir", defaultConfigDir, "configuration directory")
	root.PersistentFlags().StringVar(&opts.output, "output", "text", "output format: text or json")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	})

	root.AddCommand(newInitCommand(opts))
	root.AddCommand(newConfigCommand(opts))
	root.AddCommand(newBackupCommand(opts))
	root.AddCommand(newDoctorCommand(opts))
	root.AddCommand(newHistoryCommand(opts))
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

// Execute runs the root command and returns the stable process exit code.
func Execute(ctx context.Context, stdout, stderr io.Writer) int {
	root := NewRoot(buildinfo.Current())
	root.SetContext(ctx)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		message := err.Error()
		var applicationError *apperror.Error
		if errors.As(err, &applicationError) {
			message = apperror.UserMessage(err)
		}
		_, _ = fmt.Fprintln(stderr, message)
		return ExitCode(err)
	}
	return 0
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var configError *config.Error
	if errors.Is(err, ErrInvalidInput) || errors.As(err, &configError) || strings.Contains(err.Error(), "unknown command") {
		return 2
	}
	switch apperror.CategoryOf(err) {
	case apperror.CategoryConfig:
		return 2
	case apperror.CategoryPreflight:
		return 3
	case apperror.CategoryExecution, apperror.CategoryStorage, apperror.CategoryCancellation:
		return 4
	default:
		return 1
	}
}
