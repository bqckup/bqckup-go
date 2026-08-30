package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/buildinfo"
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
    password: `+passwordEnv+`
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

func TestBackupSnapshotsMissingPasswordFails(t *testing.T) {
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

func TestRestoreRequiresDestination(t *testing.T) {
	root, _, _ := commandForTest(t, "backup", "restore", "example", "--target", "/tmp/restore")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestRestoreRequiresTarget(t *testing.T) {
	root, _, _ := commandForTest(t, "backup", "restore", "example", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestRestoreRequiresSite(t *testing.T) {
	root, _, _ := commandForTest(t, "backup", "restore")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestRestoreFullModeSiteFails(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "restore", "example", "--destination", "local-primary", "--target", "/tmp/restore")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	message := apperror.UserMessage(err)
	assert.Contains(t, message, "history list")
	assert.Contains(t, message, "--details")
}

func TestRestoreMissingPasswordFails(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	writeIncrementalSiteConfig(t, configDir, "MISSING_PASSWORD_ENV")
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "restore", "site-b", "--destination", "local-primary", "--target", "/tmp/restore")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 3, ExitCode(err))
}

func TestRestoreUnknownSnapshotFails(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "supersecret")
	configDir, _ := writeCLIConfig(t)
	writeIncrementalSiteConfig(t, configDir, "RESTIC_PASSWORD")
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "restore", "site-b", "--destination", "local-primary", "--target", "/tmp/restore")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 4, ExitCode(err))
	assert.NotContains(t, apperror.UserMessage(err), "supersecret")
}

func TestRestoreSummaryText(t *testing.T) {
	var out bytes.Buffer
	result := backup.RestoreResult{
		SnapshotID:      strings.Repeat("a", 64),
		Target:          "/tmp/restore",
		FilesRestored:   12,
		BytesRestored:   1288490188,
		SkippedPaths:    []string{"/var/www/blog"},
		DurationSeconds: 3.1,
	}
	require.NoError(t, writeRestoreText(&out, result))
	text := out.String()
	assert.Contains(t, text, "restored snapshot "+strings.Repeat("a", 8)+" to /tmp/restore (12 files, 1.2 GiB, 3.1s)")
	assert.Contains(t, text, "skipped /var/www/blog (not in this snapshot)")
}

func TestRestoreSummaryJSON(t *testing.T) {
	root, stdout, _ := commandForTest(t, "version")
	result := backup.RestoreResult{
		SnapshotID:    strings.Repeat("b", 64),
		Target:        "/tmp/restore",
		FilesRestored: 5,
		BytesRestored: 100,
		SkippedPaths:  []string{"/var/www/blog"},
	}
	require.NoError(t, writeRestoreJSON(root, result))
	var got map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, strings.Repeat("b", 64), got["snapshot_id"])
	assert.Equal(t, "/tmp/restore", got["target"])
	assert.Equal(t, float64(5), got["files_restored"])
	skipped := got["skipped_paths"].([]any)
	assert.Len(t, skipped, 1)
	assert.Equal(t, "/var/www/blog", skipped[0])
}

func TestRestoreConfirmNonTerminalFails(t *testing.T) {
	var out bytes.Buffer
	confirm := resticRestoreOverwrite{in: strings.NewReader(""), out: &out}.confirm
	err := confirm([]string{"/tmp/restore/a.txt", "/tmp/restore/b.txt"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(err))
	assert.Contains(t, apperror.UserMessage(err), "--force")
	assert.Contains(t, out.String(), "/tmp/restore/a.txt")
	assert.Contains(t, out.String(), "Overwrite 2 files? [y/N]")
}

func TestRestoreConfirmDeclinedFails(t *testing.T) {
	var out bytes.Buffer
	confirm := resticRestoreOverwrite{in: strings.NewReader("n\n"), out: &out, tty: func(io.Reader) bool { return true }}.confirm
	err := confirm([]string{"/x"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryCancellation, apperror.CategoryOf(err))
	assert.Equal(t, "restore cancelled by user", apperror.UserMessage(err))
}

func TestRestoreConfirmAccepted(t *testing.T) {
	var out bytes.Buffer
	confirm := resticRestoreOverwrite{in: strings.NewReader("y\n"), out: &out, tty: func(io.Reader) bool { return true }}.confirm
	require.NoError(t, confirm([]string{"/x"}))
	assert.Contains(t, out.String(), "Overwrite 1 files? [y/N]")
}

func TestRestoreConfirmForceProceeds(t *testing.T) {
	var out bytes.Buffer
	confirm := resticRestoreOverwrite{force: true, in: strings.NewReader(""), out: &out}.confirm
	require.NoError(t, confirm([]string{"/x"}))
	assert.Empty(t, out.String())
}

func TestIsTerminalReaderFile(t *testing.T) {
	assert.False(t, isTerminalReader(strings.NewReader("")))
	file, err := os.CreateTemp(t.TempDir(), "tty-*")
	require.NoError(t, err)
	defer file.Close()
	assert.False(t, isTerminalReader(file))
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		assert.True(t, isTerminalReader(os.Stdin))
	}
}

func TestRestoreSnapshotDefaultsLatest(t *testing.T) {
	root := NewRoot(buildinfo.Info{Version: "test", Commit: "test"})
	restore, _, err := root.Find([]string{"backup", "restore"})
	require.NoError(t, err)
	flag := restore.Flags().Lookup("snapshot")
	require.NotNil(t, flag)
	assert.Equal(t, "latest", flag.DefValue)
}
