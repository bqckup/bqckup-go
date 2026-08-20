package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage/s3compat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDatabaseExportersPreflightsEnabledEngines(t *testing.T) {
	process := &fakeDatabaseProcessRunner{}
	configuration := config.Config{Sites: []config.Site{{Enabled: true, Sources: config.Sources{Databases: []config.DatabaseSource{
		{Enabled: true, Engine: "mysql", Name: "mysql-db"},
		{Enabled: true, Engine: "postgres", Name: "postgres-db"},
	}}}}}

	exporters, err := buildDatabaseExporters(context.Background(), configuration, process)
	require.NoError(t, err)
	assert.IsType(t, &databaseexporter.ProcessExporter{}, exporters["mysql"])
	assert.IsType(t, &databaseexporter.ProcessExporter{}, exporters["postgres"])
	assert.ElementsMatch(t, []string{"mysqldump", "pg_dump"}, process.lookups)
}

func TestBuildDatabaseExportersReturnsPreflightError(t *testing.T) {
	process := &fakeDatabaseProcessRunner{lookupErr: errors.New("binary not found")}
	configuration := config.Config{Sites: []config.Site{{Enabled: true, Sources: config.Sources{Databases: []config.DatabaseSource{
		{Enabled: true, Engine: "mysql", Name: "mysql-db"},
	}}}}}

	_, err := buildDatabaseExporters(context.Background(), configuration, process)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(err))
	assert.NotContains(t, err.Error(), "binary not found")
}

type fakeDatabaseProcessRunner struct {
	lookupErr error
	lookups   []string
}

func (f *fakeDatabaseProcessRunner) LookPath(command string) (string, error) {
	f.lookups = append(f.lookups, command)
	if f.lookupErr != nil {
		return "", f.lookupErr
	}
	return command, nil
}

func (*fakeDatabaseProcessRunner) Run(context.Context, databaseexporter.ProcessSpec) error {
	return nil
}

func TestBuildStoresConstructsS3AndR2WithoutNetworkIO(t *testing.T) {
	stores, err := buildStores(context.Background(), map[string]config.Storage{
		"s3": {
			Type: "s3", Bucket: "example", Region: "us-east-1",
			AccessKeyID: "EXAMPLE_ACCESS", SecretAccessKey: "EXAMPLE_SECRET",
		},
		"r2": {
			Type: "r2", Bucket: "example", Region: "auto",
			Endpoint:    "https://example.r2.cloudflarestorage.com",
			AccessKeyID: "EXAMPLE_ACCESS", SecretAccessKey: "EXAMPLE_SECRET",
		},
	})
	require.NoError(t, err)
	assert.IsType(t, &s3compat.Store{}, stores["s3"])
	assert.IsType(t, &s3compat.Store{}, stores["r2"])
}

func TestOpenWiresAWorkingLocalBackupApplication(t *testing.T) {
	configDir, backupRoot := writeApplicationConfig(t)
	application, err := Open(context.Background(), configDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, application.Close()) })

	result, err := application.RunBackup(context.Background(), "example", true)
	require.NoError(t, err)
	assert.Equal(t, "success", string(result.Status))

	matches, err := filepath.Glob(filepath.Join(backupRoot, "bqckup", "example", "*", "files.tar.gz"))
	require.NoError(t, err)
	assert.Len(t, matches, 1)
	runs, err := application.ListRuns(context.Background(), "example", 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Len(t, runs[0].Artifacts, 1)
}

func writeApplicationConfig(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	source := filepath.Join(root, "source")
	backupRoot := filepath.Join(root, "backups")
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "config"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sites"), 0o700))
	require.NoError(t, os.MkdirAll(source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "data.txt"), []byte("important"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "bqckup.yaml"), []byte(`version: 2
app:
  state_database: data/state.db
  temporary_directory: tmp
  lock_directory: locks
  log_level: info
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config", "storages.yaml"), []byte(fmt.Sprintf(`storages:
  local-primary:
    type: local
    directory: %s
`, backupRoot)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", "example.yaml"), []byte(fmt.Sprintf(`version: 2
site:
  name: example
  enabled: true
  sources:
    files:
      include: [%s]
      exclude: []
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1h
    keep_last: 3
`, source)), 0o600))
	return configDir, backupRoot
}

func TestValidateBuiltinEngineStorages(t *testing.T) {
	local := config.Storage{Type: "local", Directory: "/var/backups"}
	s3 := config.Storage{Type: "s3", Bucket: "bucket"}

	builtinSite := func(destinations []config.Destination) config.Site {
		return config.Site{
			Name: "site-a", Enabled: true, BackupMode: "incremental",
			Incremental:  config.Incremental{Engine: "builtin", PasswordEnv: "PW"},
			Destinations: destinations,
		}
	}

	t.Run("builtin with local destinations is fine", func(t *testing.T) {
		cfg := config.Config{
			Storages: map[string]config.Storage{"local-primary": local},
			Sites:    []config.Site{builtinSite([]config.Destination{{Storage: "local-primary"}})},
		}
		require.NoError(t, validateBuiltinEngineStorages(cfg))
	})

	t.Run("builtin with s3 destination is rejected", func(t *testing.T) {
		cfg := config.Config{
			Storages: map[string]config.Storage{"s3-primary": s3},
			Sites:    []config.Site{builtinSite([]config.Destination{{Storage: "s3-primary"}})},
		}
		err := validateBuiltinEngineStorages(cfg)
		require.Error(t, err)
		assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	})

	t.Run("process adapter sites are not restricted", func(t *testing.T) {
		site := builtinSite([]config.Destination{{Storage: "s3-primary"}})
		site.Incremental.Engine = "restic"
		cfg := config.Config{
			Storages: map[string]config.Storage{"s3-primary": s3},
			Sites:    []config.Site{site},
		}
		require.NoError(t, validateBuiltinEngineStorages(cfg))
	})
}
