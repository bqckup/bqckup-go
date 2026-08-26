package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeIncrementalSiteConfig adds an incremental site "site-b" on
// "local-primary" to the config directory created by writeCLIConfig.
// passwordEnv is the repository password environment variable name.
func writeIncrementalSiteConfig(t *testing.T, configDir, passwordEnv string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", "site-b.yaml"), []byte(`version: 2
site:
  name: site-b
  enabled: true
  backup_mode: incremental
  incremental:
    password_env: `+passwordEnv+`
  sources:
    files:
      include: [/srv/example/data]
      exclude: []
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1h
`), 0o600))
}

func TestBackupSnapshotsRequiresDestination(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "snapshots", "example")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestBackupSnapshotsRequiresSite(t *testing.T) {
	root, _, _ := commandForTest(t, "backup", "snapshots")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestBackupSnapshotsFullModeSiteFails(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "snapshots", "example", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	message := apperror.UserMessage(err)
	assert.Contains(t, message, "history list")
	assert.Contains(t, message, "--details")
}

func TestBackupSnapshotsMissingPasswordEnvFails(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	writeIncrementalSiteConfig(t, configDir, "MISSING_PASSWORD_ENV")
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "snapshots", "site-b", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 3, ExitCode(err))
}

func TestBackupSnapshotsBrokenRepositoryFailsRedacted(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "supersecret")
	configDir, _ := writeCLIConfig(t)
	writeIncrementalSiteConfig(t, configDir, "RESTIC_PASSWORD")
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "snapshots", "site-b", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 4, ExitCode(err))
	assert.NotContains(t, apperror.UserMessage(err), "supersecret")
}

// Table shape, JSON schema, and empty states need no new writer tests:
// the command renders through writeSnapshotText and writeStorageJSON,
// whose output is pinned by TestWriteStorageTextIncrementalMode and
// TestWriteStorageJSONSchemas in storage_test.go.
