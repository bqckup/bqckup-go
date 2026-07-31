package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRejectsRelativeSourcePath(t *testing.T) {
	cfg := validConfig(t)
	cfg.Sites[0].Sources.Files.Include = []string{"relative/path"}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sites.example.sources.files.include[0]")
}

func TestValidateRejectsUnsupportedDatabaseMilestone(t *testing.T) {
	cfg := validConfig(t)
	cfg.Sites[0].Sources.Databases = []DatabaseSource{{
		Name:     "application",
		Enabled:  true,
		Engine:   "mysql",
		Database: "app",
	}}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database exporters are not available")
}

func TestValidateAcceptsLocalFileBackup(t *testing.T) {
	cfg := validConfig(t)
	require.NoError(t, cfg.Validate())
}

func validConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		Version:        2,
		StorageVersion: 2,
		App: App{
			StateDatabase:      filepath.Join(root, "state.db"),
			TemporaryDirectory: filepath.Join(root, "tmp"),
			LockDirectory:      filepath.Join(root, "locks"),
			LogLevel:           "info",
		},
		Storages: map[string]Storage{
			"local-primary": {Type: "local", Directory: filepath.Join(root, "backups")},
		},
		Sites: []Site{{
			SchemaVersion: 2,
			SourceFile:    filepath.Join(root, "sites", "example.yaml"),
			Name:          "example",
			Enabled:       true,
			Sources: Sources{Files: FileSource{
				Include: []string{root},
			}},
			Destinations: []Destination{{Storage: "local-primary"}},
			Policy:       Policy{MinimumInterval: 24 * time.Hour, KeepLast: 7},
		}},
	}
}
