package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/buildinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoctorCommand(t *testing.T) {
	t.Run("passes on valid configuration and available secrets", func(t *testing.T) {
		tempDir := t.TempDir()
		sourceDir := filepath.Join(tempDir, "source")
		backupDir := filepath.Join(tempDir, "backups")
		require.NoError(t, os.MkdirAll(sourceDir, 0o700))
		require.NoError(t, os.MkdirAll(backupDir, 0o700))

		siteYAML := fmt.Sprintf(`version: 2
site:
  name: test-site
  enabled: true
  backup_mode: incremental
  incremental:
    engine: restic
    password_env: TEST_RESTIC_PASS
  sources:
    files:
      include:
        - %s
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
`, sourceDir)

		writeConfigTree(t, tempDir, fmt.Sprintf(`version: 2
app:
  state_database: %s
  temporary_directory: %s
  lock_directory: %s
`, filepath.Join(tempDir, "data", "state.db"), filepath.Join(tempDir, "tmp"), filepath.Join(tempDir, "locks")),
			fmt.Sprintf(`storages:
  local-primary:
    type: local
    directory: %s
`, backupDir), siteYAML)

		t.Setenv("TEST_RESTIC_PASS", "my-secret-password")

		var stdout, stderr bytes.Buffer
		root := NewRoot(buildinfo.Info{})
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{"doctor", "--config-dir", tempDir, "--output", "json"})

		_ = root.Execute()
		assert.Contains(t, stdout.String(), `"name":"config"`)
		assert.Contains(t, stdout.String(), `"secret:test-site:TEST_RESTIC_PASS"`)
	})

	t.Run("fails when password_env is missing for incremental site", func(t *testing.T) {
		tempDir := t.TempDir()
		sourceDir := filepath.Join(tempDir, "source")
		backupDir := filepath.Join(tempDir, "backups")
		require.NoError(t, os.MkdirAll(sourceDir, 0o700))
		require.NoError(t, os.MkdirAll(backupDir, 0o700))

		siteYAML := fmt.Sprintf(`version: 2
site:
  name: test-site
  enabled: true
  backup_mode: incremental
  incremental:
    engine: restic
    password_env: UNSET_DOCTOR_PASS_VAR
  sources:
    files:
      include:
        - %s
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
`, sourceDir)

		writeConfigTree(t, tempDir, fmt.Sprintf(`version: 2
app:
  state_database: %s
  temporary_directory: %s
  lock_directory: %s
`, filepath.Join(tempDir, "data", "state.db"), filepath.Join(tempDir, "tmp"), filepath.Join(tempDir, "locks")),
			fmt.Sprintf(`storages:
  local-primary:
    type: local
    directory: %s
`, backupDir), siteYAML)

		_ = os.Unsetenv("UNSET_DOCTOR_PASS_VAR")

		var stdout, stderr bytes.Buffer
		root := NewRoot(buildinfo.Info{})
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{"doctor", "--config-dir", tempDir})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, stdout.String(), "[✗]")
		assert.Contains(t, stdout.String(), "secret:test-site:UNSET_DOCTOR_PASS_VAR")
	})
}

func writeConfigTree(t *testing.T, dir, rootYAML, storageYAML, siteYAML string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bqckup.yaml"), []byte(rootYAML), 0o600))
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "storages.yaml"), []byte(storageYAML), 0o600))
	sitesDir := filepath.Join(dir, "sites")
	require.NoError(t, os.MkdirAll(sitesDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sitesDir, "test-site.yaml"), []byte(siteYAML), 0o600))
}
