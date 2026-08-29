package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/buildinfo"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitCreatesSchemaV2TreeWithoutOverwriting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bqckup")
	root, stdout, _ := commandForTest(t, "--config-dir", dir, "init")
	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "initialized")
	rootFile := filepath.Join(dir, "bqckup.yaml")
	original, err := os.ReadFile(rootFile)
	require.NoError(t, err)
	assert.NotContains(t, string(original), "version:")
	storageContents, err := os.ReadFile(filepath.Join(dir, "config", "storages.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(storageContents), "version:")
	assert.Contains(t, string(storageContents), "primary: true")

	require.NoError(t, os.WriteFile(rootFile, []byte("custom"), 0o600))
	root, _, _ = commandForTest(t, "--config-dir", dir, "init")
	require.Error(t, root.Execute())
	actual, err := os.ReadFile(rootFile)
	require.NoError(t, err)
	assert.Equal(t, "custom", string(actual))
}

func TestConfigValidateAndBackupList(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "config", "validate")
	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "1 site")
	assert.Contains(t, stdout.String(), "1 storage")

	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "list")
	require.NoError(t, root.Execute())
	var sites []map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &sites))
	require.Len(t, sites, 1)
	assert.Equal(t, "example", sites[0]["name"])
}

func TestConfigValidateFlagsMissingNotificationEnv(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	rootPath := filepath.Join(configDir, "bqckup.yaml")
	root, err := os.ReadFile(rootPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rootPath, append(root, []byte(`notifications:
  channels:
    webhook:
      type: webhook
      url_env: BQCKUP_WEBHOOK_URL
    discord:
      type: discord
      webhook_url_env: BQCKUP_DISCORD_WEBHOOK_URL
  routes:
    - events: [backup_failed]
      channels: [webhook, discord]
`)...), 0o600))

	rootCmd, _, stderr := commandForTest(t, "--config-dir", configDir, "config", "validate")
	err = rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BQCKUP_WEBHOOK_URL")
	assert.Contains(t, err.Error(), "BQCKUP_DISCORD_WEBHOOK_URL")

	// Both set: validate passes and the output shape is unchanged.
	t.Setenv("BQCKUP_WEBHOOK_URL", "https://example.invalid/hook")
	t.Setenv("BQCKUP_DISCORD_WEBHOOK_URL", "https://discord.invalid/hook")
	rootCmd, stdout, _ := commandForTest(t, "--config-dir", configDir, "config", "validate")
	require.NoError(t, rootCmd.Execute())
	assert.Contains(t, stdout.String(), "1 site")
	assert.Contains(t, stdout.String(), "1 storage")
	assert.NotContains(t, stderr.String(), "not set")
}

func TestBackupRunSucceedsWithUnsetNotificationEnv(t *testing.T) {
	configDir, backupRoot := writeCLIConfig(t)
	rootPath := filepath.Join(configDir, "bqckup.yaml")
	root, err := os.ReadFile(rootPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rootPath, append(root, []byte(`notifications:
  channels:
    webhook:
      type: webhook
      url_env: BQCKUP_UNSET_WEBHOOK_URL
  routes:
    - events: [backup_failed]
      channels: [webhook]
`)...), 0o600))

	rootCmd, stdout, _ := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "run", "example", "--force")
	require.NoError(t, rootCmd.Execute())
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, "success", result["status"], "a missing notification env must never fail a backup run")

	matches, err := filepath.Glob(filepath.Join(backupRoot, "bqckup", "example", "*", "*", "files.tar.gz"))
	require.NoError(t, err)
	assert.Len(t, matches, 1)
}

func TestBackupRunAndHistoryListEndToEnd(t *testing.T) {
	configDir, backupRoot := writeCLIConfig(t)
	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "run", "example", "--force")
	require.NoError(t, root.Execute())
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, "success", result["status"])

	matches, err := filepath.Glob(filepath.Join(backupRoot, "bqckup", "example", "*", "*", "files.tar.gz"))
	require.NoError(t, err)
	assert.Len(t, matches, 1)

	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "--output", "json", "history", "list", "--site", "example", "--limit", "1")
	require.NoError(t, root.Execute())
	rawJSON := stdout.String()
	var runs []history.BackupRun
	require.NoError(t, json.Unmarshal([]byte(rawJSON), &runs))
	require.Len(t, runs, 1)
	assert.Equal(t, history.StatusSuccess, runs[0].Status)
	assert.Len(t, runs[0].Packages, 1)

	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "--output", "json", "history", "list", "--details", "--site", "example", "--limit", "1")
	require.NoError(t, root.Execute())
	assert.JSONEq(t, rawJSON, stdout.String(), "--details must not change raw JSON history output")

	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "history", "list", "--details", "--site", "example", "--limit", "1")
	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "Packages for run "+runs[0].ID)
	assert.Contains(t, stdout.String(), runs[0].Packages[0].ObjectKey)
}

func TestBackupRunWithoutSiteRunsEveryEnabledSite(t *testing.T) {
	configDir, backupRoot := writeCLIConfig(t)
	writeCLISite(t, configDir, "site-b", true)
	writeCLISite(t, configDir, "site-disabled", false)

	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "run", "--force")
	require.NoError(t, root.Execute())

	var results []backup.RunResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &results))
	require.Len(t, results, 2)
	assert.Equal(t, []string{"example", "site-b"}, []string{results[0].SiteName, results[1].SiteName})
	assert.Equal(t, backup.StatusSuccess, results[0].Status)
	assert.Equal(t, backup.StatusSuccess, results[1].Status)

	for _, siteName := range []string{"example", "site-b"} {
		matches, err := filepath.Glob(filepath.Join(backupRoot, "bqckup", siteName, "*", "*", "files.tar.gz"))
		require.NoError(t, err)
		assert.Len(t, matches, 1)
	}
	disabledMatches, err := filepath.Glob(filepath.Join(backupRoot, "bqckup", "site-disabled", "*", "files.tar.gz"))
	require.NoError(t, err)
	assert.Empty(t, disabledMatches)
}

func TestExitCodeMapping(t *testing.T) {
	assert.Equal(t, 2, ExitCode(fmt.Errorf("bad flag: %w", ErrInvalidInput)))
}

func commandForTest(t *testing.T, args ...string) (*cobraCommand, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := NewRoot(buildinfo.Info{Version: "test", Commit: "test"})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	return (*cobraCommand)(root), stdout, stderr
}

// cobraCommand keeps the helper return concise while retaining Cobra's methods.
type cobraCommand = cobra.Command

func writeCLIConfig(t *testing.T) (string, string) {
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

func writeCLISite(t *testing.T, configDir, siteName string, enabled bool) {
	t.Helper()
	source := filepath.Join(filepath.Dir(configDir), "source-"+siteName)
	require.NoError(t, os.MkdirAll(source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "data.txt"), []byte("important"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", siteName+".yaml"), []byte(fmt.Sprintf(`version: 2
site:
  name: %s
  enabled: %t
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
`, siteName, enabled, source)), 0o600))
}

func TestBackupRunSingleSiteNoChangeExitCode5(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	// First run: success
	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "backup", "run", "example", "--force")
	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "example: success")

	// Second run (source unchanged): no_change, exits with code 5
	stdout = new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := Execute(t.Context(), stdout, stderr)
	assert.Equal(t, 0, code) // without args, Execute runs help / version / root without args

	// Run with args via root command execution
	rootCmd, stdout, stderr := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "run", "example", "--force")
	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Equal(t, 5, ExitCode(err))
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, "no_change", result["status"])

	// Text output format
	rootCmd, stdout, _ = commandForTest(t, "--config-dir", configDir, "backup", "run", "example", "--force")
	err = rootCmd.Execute()
	require.Error(t, err)
	assert.Equal(t, 5, ExitCode(err))
	assert.Contains(t, stdout.String(), "example: no_change")
}

func TestBackupRunMultiSiteNoChangeExitCode5(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	writeCLISite(t, configDir, "site-b", true)

	// First run: all succeed
	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "backup", "run", "--force")
	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "example: success")
	assert.Contains(t, stdout.String(), "site-b: success")

	// Second run (all unchanged): exits 5
	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "run", "--force")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 5, ExitCode(err))

	var results []backup.RunResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &results))
	require.Len(t, results, 2)
	assert.Equal(t, backup.StatusNoChange, results[0].Status)
	assert.Equal(t, backup.StatusNoChange, results[1].Status)
}
