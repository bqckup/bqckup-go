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
	t.Run("passes with password in protected site config", func(t *testing.T) {
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
    password: my-secret-password
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

		var stdout, stderr bytes.Buffer
		root := NewRoot(buildinfo.Info{})
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{"doctor", "--config-dir", tempDir, "--output", "json"})

		_ = root.Execute()
		assert.Contains(t, stdout.String(), `"name":"config"`)
		assert.Contains(t, stdout.String(), `"secret:test-site:incremental"`)
	})

	t.Run("does not interpret password as environment variable", func(t *testing.T) {
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
    password: UNSET_DOCTOR_PASS_VAR
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

		require.NoError(t, root.Execute())
		assert.Contains(t, stdout.String(), "[✓] secret:test-site:incremental")
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

func TestDoctorIncrementalEngineNeedsNoResticBinary(t *testing.T) {
	t.Setenv("TEST_RESTIC_PASS", "secret-value")
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "source")
	backupDir := filepath.Join(tempDir, "backups")
	require.NoError(t, os.MkdirAll(sourceDir, 0o700))
	require.NoError(t, os.MkdirAll(backupDir, 0o700))

	writeFile(t, filepath.Join(tempDir, "config", "bqckup.yaml"), fmt.Sprintf(`app:
  state_database: %s
  temporary_directory: %s
  lock_directory: %s
`, filepath.Join(tempDir, "state.db"), filepath.Join(tempDir, "tmp"), filepath.Join(tempDir, "locks")))
	writeFile(t, filepath.Join(tempDir, "config", "config", "storages.yaml"), fmt.Sprintf(`storages:
  local-primary:
    type: local
    directory: %s
`, backupDir))
	writeFile(t, filepath.Join(tempDir, "config", "sites", "test-site.yaml"), fmt.Sprintf(`version: 2
site:
  name: test-site
  enabled: true
  backup_mode: incremental
  incremental:
    password: TEST_RESTIC_PASS
  sources:
    files:
      include:
        - %s
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
`, sourceDir))

	var stdout bytes.Buffer
	root := NewRoot(buildinfo.Info{})
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"doctor", "--config-dir", filepath.Join(tempDir, "config")})
	require.NoError(t, root.Execute())
	// Incremental backups use the built-in engine and never probe for incremental.
	assert.NotContains(t, stdout.String(), `"name":"binary:restic"`)
	assert.Contains(t, stdout.String(), "built-in incremental engine")
}

func TestDoctorStorageProbes(t *testing.T) {
	// Fixture: one enabled incremental site, one disabled site, one local
	// storage. Used by every subtest; the storage directory is created by
	// the test so the probe passes.
	writeFixture := func(t *testing.T) string {
		t.Helper()
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
    password: TEST_RESTIC_PASS
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
		writeFile(t, filepath.Join(tempDir, "sites", "disabled-site.yaml"), `version: 2
site:
  name: disabled-site
  enabled: false
`)
		t.Setenv("TEST_RESTIC_PASS", "my-secret-password")
		return tempDir
	}

	runDoctor := func(t *testing.T, configDir string, args ...string) (string, error) {
		t.Helper()
		var stdout bytes.Buffer
		root := NewRoot(buildinfo.Info{})
		root.SetOut(&stdout)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"doctor", "--config-dir", configDir}, args...))
		err := root.Execute()
		return stdout.String(), err
	}

	t.Run("local storage check passes", func(t *testing.T) {
		configDir := writeFixture(t)

		output, err := runDoctor(t, configDir, "--output", "json")
		require.NoError(t, err)
		assert.Equal(t, 0, ExitCode(err))
		assert.Contains(t, output, `"name":"storage:local-primary"`)
		assert.Contains(t, output, `"status":"ok"`)
		assert.NotContains(t, output, "\x1b", "JSON output must not contain ANSI escapes")

		text, err := runDoctor(t, configDir)
		require.NoError(t, err)
		assert.Contains(t, text, "storage:local-primary: access ok")
	})

	t.Run("unwritable local storage fails with exit code 3", func(t *testing.T) {
		tempDir := t.TempDir()
		roParent := filepath.Join(tempDir, "ro-parent")
		require.NoError(t, os.MkdirAll(roParent, 0o700))
		require.NoError(t, os.Chmod(roParent, 0o500))
		t.Cleanup(func() { _ = os.Chmod(roParent, 0o700) })

		sourceDir := filepath.Join(tempDir, "source")
		require.NoError(t, os.MkdirAll(sourceDir, 0o700))
		siteYAML := fmt.Sprintf(`version: 2
site:
  name: test-site
  enabled: true
  backup_mode: incremental
  incremental:
    password: TEST_RESTIC_PASS
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
`, filepath.Join(roParent, "child")), siteYAML)
		t.Setenv("TEST_RESTIC_PASS", "my-secret-password")

		output, err := runDoctor(t, tempDir)
		require.Error(t, err)
		assert.Equal(t, 3, ExitCode(err))
		assert.Contains(t, output, "storage:local-primary")
	})

	t.Run("--site selects a site or exits 2", func(t *testing.T) {
		configDir := writeFixture(t)

		_, err := runDoctor(t, configDir, "--site", "test-site")
		require.NoError(t, err)

		_, err = runDoctor(t, configDir, "--site", "unknown-site")
		require.Error(t, err)
		assert.Equal(t, 2, ExitCode(err))

		_, err = runDoctor(t, configDir, "--site", "disabled-site")
		require.Error(t, err)
		assert.Equal(t, 2, ExitCode(err))
	})
}

func TestDoctorRemoteStorageProviderUnavailable(t *testing.T) {
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "source")
	require.NoError(t, os.MkdirAll(sourceDir, 0o700))

	writeConfigTree(t, tempDir, fmt.Sprintf(`version: 2
app:
  state_database: %s
  temporary_directory: %s
  lock_directory: %s
`, filepath.Join(tempDir, "data", "state.db"), filepath.Join(tempDir, "tmp"), filepath.Join(tempDir, "locks")),
		`storages:
  remote-x:
    type: s3
    credentials:
      source: remote
      url: https://127.0.0.1:1/storage
`, fmt.Sprintf(`version: 2
site:
  name: test-site
  enabled: true
  sources:
    files:
      include: [%s]
  destinations:
    - storage: remote-x
  policy:
    minimum_interval: 24h
    keep_last: 7
`, sourceDir))

	var stdout, stderr bytes.Buffer
	root := NewRoot(buildinfo.Info{})
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"doctor", "--config-dir", tempDir})

	err := root.Execute()
	assert.Equal(t, 3, ExitCode(err))
	assert.Contains(t, stdout.String(), "[✗] storage:remote-x: remote storage configuration is unavailable")
	assert.NotContains(t, stdout.String(), "request failed")
	assert.NotContains(t, stdout.String(), "http")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
