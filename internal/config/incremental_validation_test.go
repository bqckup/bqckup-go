package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateIncrementalBackupMode(t *testing.T) {
	t.Run("accepts full backup mode explicitly", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "full"
		require.NoError(t, cfg.Validate())
	})

	t.Run("accepts default empty backup mode as full", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = ""
		require.NoError(t, cfg.Validate())
	})

	t.Run("accepts incremental configuration", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "incremental"
		cfg.Sites[0].Incremental = Incremental{
			Password: "test-secret-password",
		}
		require.NoError(t, cfg.Validate())
	})

	t.Run("rejects unknown backup mode", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "differential"
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backup_mode")
		assert.Contains(t, err.Error(), "must be 'full' or 'incremental'")
	})

	t.Run("rejects incremental mode with missing password", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "incremental"
		cfg.Sites[0].Incremental = Incremental{}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "incremental.password")
		assert.Contains(t, err.Error(), "is required")
	})

	t.Run("accepts literal password characters", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "incremental"
		cfg.Sites[0].Incremental = Incremental{
			Password: "correct horse battery staple!",
		}
		require.NoError(t, cfg.Validate())
	})
}

func TestLoadIncrementalSiteYAML(t *testing.T) {
	siteYAML := `version: 2
site:
  name: example
  enabled: true
  backup_mode: incremental
  incremental:
    password: "test-secret-password"
  sources:
    files:
      include:
        - /var/www/html
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
`
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, `storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
`, siteYAML)

	cfg, err := Load(t.Context(), dir)
	require.NoError(t, err)
	require.Len(t, cfg.Sites, 1)
	assert.Equal(t, "incremental", cfg.Sites[0].BackupMode)
	assert.Equal(t, "test-secret-password", cfg.Sites[0].Incremental.Password)
}

func TestLoadRequires0600ForIncrementalPassword(t *testing.T) {
	dir := writeConfigTree(t, `app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, localStorageYAML, `site:
  name: example
  enabled: true
  backup_mode: incremental
  incremental:
    password: "literal-secret"
  sources:
    files:
      include: [/var/www/html]
  destinations:
    - storage: local-primary
`)
	sitePath := filepath.Join(dir, "sites", "example.yaml")
	require.NoError(t, os.Chmod(sitePath, 0o644))

	_, err := Load(t.Context(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential-bearing site file must have mode 0600")
}

func TestLoadRejectsRemovedIncrementalEngineField(t *testing.T) {
	siteYAML := `version: 2
site:
  name: example
  enabled: true
  backup_mode: incremental
  incremental:
    engine: restic
    password: "test-secret-password"
  sources:
    files:
      include:
        - /var/www/html
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
`
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, `storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
`, siteYAML)

	_, err := Load(t.Context(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "engine")
}
