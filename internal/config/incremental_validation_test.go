package config

import (
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

	t.Run("accepts valid incremental restic configuration", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "incremental"
		cfg.Sites[0].Incremental = Incremental{
			Engine:      "restic",
			PasswordEnv: "RESTIC_PASSWORD",
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

	t.Run("rejects incremental mode with unsupported engine", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "incremental"
		cfg.Sites[0].Incremental = Incremental{
			Engine:      "borg",
			PasswordEnv: "BORG_PASSPHRASE",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "incremental.engine")
		assert.Contains(t, err.Error(), "must be 'restic'")
	})

	t.Run("rejects incremental mode with missing password_env", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "incremental"
		cfg.Sites[0].Incremental = Incremental{
			Engine: "restic",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "incremental.password_env")
		assert.Contains(t, err.Error(), "is required")
	})

	t.Run("rejects incremental mode with invalid password_env name", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Sites[0].BackupMode = "incremental"
		cfg.Sites[0].Incremental = Incremental{
			Engine:      "restic",
			PasswordEnv: "invalid-env-name!",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "incremental.password_env")
		assert.Contains(t, err.Error(), "valid environment variable name")
	})
}

func TestLoadIncrementalSiteYAML(t *testing.T) {
	siteYAML := `version: 2
site:
  name: example
  enabled: true
  backup_mode: incremental
  incremental:
    engine: restic
    password_env: RESTIC_PASSWORD
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
	assert.Equal(t, "restic", cfg.Sites[0].Incremental.Engine)
	assert.Equal(t, "RESTIC_PASSWORD", cfg.Sites[0].Incremental.PasswordEnv)
}
