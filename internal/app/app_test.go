package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage/s3compat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
