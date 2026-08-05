package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newInitCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a schema-v2 configuration tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := initializeConfig(opts.configDir); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "initialized schema-v2 configuration in %s\n", opts.configDir)
			return err
		},
	}
}

func initializeConfig(directory string) error {
	files := map[string]string{
		filepath.Join(directory, "bqckup.yaml"): `app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
`,
		filepath.Join(directory, "config", "storages.yaml"): `storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true
`,
		filepath.Join(directory, "sites", "example.yaml"): `site:
  name: example
  enabled: false
  sources:
    files:
      include:
        - /srv/example/data
      exclude:
        - /srv/example/data/cache
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
`,
	}
	for filename := range files {
		if _, err := os.Lstat(filename); err == nil {
			return fmt.Errorf("%w: refusing to overwrite %s", ErrInvalidInput, filename)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect initialization target %s: %w", filename, err)
		}
	}
	for filename, contents := range files {
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			return fmt.Errorf("create configuration directory: %w", err)
		}
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create configuration file %s: %w", filename, err)
		}
		if _, err := file.WriteString(contents); err != nil {
			_ = file.Close()
			return fmt.Errorf("write configuration file %s: %w", filename, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close configuration file %s: %w", filename, err)
		}
	}
	return nil
}
