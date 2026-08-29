package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteStorageTextFullMode(t *testing.T) {
	created := time.Date(2026, 11, 10, 3, 0, 12, 0, time.UTC)
	listing := backup.Listing{
		Mode:        "full",
		Destination: "s3-primary",
		Site:        "site-a",
		Packages: []backup.PackageRow{
			{Destination: "s3-primary", Key: "bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz", Size: 2254857830, CreatedAt: created},
			{Destination: "s3-primary", Key: "bqckup/site-a/2026-11-10T03-00-00.000000000Z/databases/db.sql.gz", Size: 84 * 1024 * 1024, CreatedAt: created},
		},
	}
	var output bytes.Buffer
	require.NoError(t, writeStorageText(&output, listing))
	text := output.String()
	for _, heading := range []string{"DESTINATION", "KEY", "SIZE", "CREATED AT"} {
		assert.Contains(t, text, heading)
	}
	assert.Regexp(t, `s3-primary\s+bqckup/site-a/2026-11-10T03-00-00\.000000000Z/files\.tar\.gz\s+2\.1 GiB\s+10 Nov 2026 03:00`, text)
	assert.Contains(t, text, "84.0 MiB")
}

func TestWriteStorageTextIncrementalMode(t *testing.T) {
	created := time.Date(2026, 12, 11, 6, 55, 47, 0, time.UTC)
	listing := backup.Listing{
		Mode:        "incremental",
		Destination: "s3-primary",
		Site:        "site-b",
		Snapshots: []backup.SnapshotRow{
			{ID: "33e25d78", Paths: []string{"/var/www/html"}, Size: 2147483648, CreatedAt: created},
			{ID: "aabbccdd", Paths: []string{"/etc", "/opt"}, Size: 0, CreatedAt: created.Add(-time.Hour)},
		},
	}
	var output bytes.Buffer
	require.NoError(t, writeStorageText(&output, listing))
	text := output.String()
	for _, heading := range []string{"ID", "PATHS", "SIZE", "CREATED AT"} {
		assert.Contains(t, text, heading)
	}
	assert.Regexp(t, `33e25d78\s+/var/www/html\s+2\.0 GiB\s+11 Dec 2026 06:55`, text)
	assert.Contains(t, text, "/etc, /opt")
	assert.Contains(t, text, "-") // nil summary renders a dash
}

func TestWriteStorageTextEmptyStates(t *testing.T) {
	full := backup.Listing{Mode: "full", Destination: "s3-primary", Site: "site-a"}
	var fullOutput bytes.Buffer
	require.NoError(t, writeStorageText(&fullOutput, full))
	assert.Equal(t, "No packages found for site \"site-a\" on \"s3-primary\".\n", fullOutput.String())

	incremental := backup.Listing{Mode: "incremental", Destination: "s3-primary", Site: "site-b"}
	var incrementalOutput bytes.Buffer
	require.NoError(t, writeStorageText(&incrementalOutput, incremental))
	assert.Equal(t, "No snapshots found for site \"site-b\" on \"s3-primary\".\n", incrementalOutput.String())
}

func TestWriteStorageJSONSchemas(t *testing.T) {
	created := time.Date(2026, 11, 10, 3, 0, 12, 0, time.UTC)
	full := backup.Listing{
		Mode:        "full",
		Destination: "s3-primary",
		Packages: []backup.PackageRow{
			{Destination: "s3-primary", Key: "bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz", Size: 2254857830, CreatedAt: created},
		},
	}
	assert.Equal(t, `[{"destination":"s3-primary","key":"bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz","size":2254857830,"created_at":"2026-11-10T03:00:12Z"}]`+"\n", encodeStorageJSONForTest(t, full))

	incremental := backup.Listing{
		Mode:        "incremental",
		Destination: "s3-primary",
		Snapshots: []backup.SnapshotRow{
			{ID: "33e25d78", Paths: []string{"/var/www/html"}, Size: 2147483648, CreatedAt: created},
		},
	}
	assert.Equal(t, `[{"id":"33e25d78","paths":["/var/www/html"],"size":2147483648,"created_at":"2026-11-10T03:00:12Z"}]`+"\n", encodeStorageJSONForTest(t, incremental))

	empty := encodeStorageJSONForTest(t, backup.Listing{Mode: "full", Destination: "s3-primary"})
	assert.Equal(t, "[]\n", empty)
	assert.NotContains(t, empty, "\x1b")
}

func encodeStorageJSONForTest(t *testing.T, listing backup.Listing) string {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	require.NoError(t, encodeStorageJSON(encoder, listing))
	return output.String()
}

func TestStorageListCommandRejectsMissingSite(t *testing.T) {
	root, _, _ := commandForTest(t, "storage", "list", "s3-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestStorageListCommandRejectsMissingDestination(t *testing.T) {
	root, _, _ := commandForTest(t, "storage", "list", "--site", "example")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestStorageListLocalDestinationFailsWithHistoryPointer(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "storage", "list", "local-primary", "--site", "example")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	message := apperror.UserMessage(err)
	assert.Contains(t, message, "history list")
	assert.Contains(t, message, "--details")
}

func TestStorageListLocalDestinationIncrementalPointsToBackupSnapshots(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", "site-b.yaml"), []byte(`version: 2
site:
  name: site-b
  enabled: true
  backup_mode: incremental
  incremental:
    password_env: RESTIC_PASSWORD
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
	root, _, _ := commandForTest(t, "--config-dir", configDir, "storage", "list", "local-primary", "--site", "site-b")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	message := apperror.UserMessage(err)
	assert.Contains(t, message, "backup snapshots")
	assert.Contains(t, message, "--destination")
}

func TestStorageListUnknownSiteFailsWithConfigError(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "storage", "list", "local-primary", "--site", "missing")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	assert.Contains(t, apperror.UserMessage(err), "was not found")
}

func TestStorageFailureExitsFourRedacted(t *testing.T) {
	err := apperror.Wrap(apperror.CategoryStorage, "could not list remote backup sets", apperror.Hide("provider secret", nil))
	assert.Equal(t, 4, ExitCode(err))
	assert.NotContains(t, apperror.UserMessage(err), "secret")
}

func TestWriteLinkTextSplitsURLAndInfo(t *testing.T) {
	link := storage.DownloadLink{
		URL:       "https://example.test/signed",
		Key:       "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz",
		ExpiresAt: time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC),
	}
	var stdout, stderr bytes.Buffer
	require.NoError(t, writeLinkText(&stdout, &stderr, link))
	assert.Equal(t, "https://example.test/signed\n", stdout.String())
	assert.Contains(t, stderr.String(), "Link expires at 2026-08-06T03:00:00Z.")
	assert.Contains(t, stderr.String(), "Anyone with this link can download the file.")
}

func TestWriteLinkJSONSchema(t *testing.T) {
	link := storage.DownloadLink{
		URL:       "https://example.test/signed",
		Key:       "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz",
		ExpiresAt: time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC),
	}
	var output bytes.Buffer
	require.NoError(t, encodeLinkJSON(json.NewEncoder(&output), "s3-primary", link))
	assert.Equal(t, `{"url":"https://example.test/signed","key":"bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz","destination":"s3-primary","expires_at":"2026-08-06T03:00:00Z"}`+"\n", output.String())
}

func TestStorageLinkRejectsMissingKey(t *testing.T) {
	root, _, _ := commandForTest(t, "storage", "link", "s3-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestStorageLinkRejectsMissingDestination(t *testing.T) {
	root, _, _ := commandForTest(t, "storage", "link", "--key", "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestStorageLinkRejectsInvalidExpiryValues(t *testing.T) {
	for _, expiry := range []string{"15m", "0h", "25h", "6", "2h30m"} {
		root, _, _ := commandForTest(t, "storage", "link", "s3-primary", "--key", "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", "--expires", expiry)
		err := root.Execute()
		require.Error(t, err, expiry)
		assert.ErrorIs(t, err, ErrInvalidInput, expiry)
		assert.Equal(t, 2, ExitCode(err), expiry)
		assert.Contains(t, err.Error(), "whole number of hours", expiry)
	}
}

func TestStorageLinkLocalDestinationShowsLocalPath(t *testing.T) {
	configDir, backupRoot := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "storage", "link", "local-primary", "--key", "bqckup/example/2026-08-05T00-00-00Z/files.tar.gz", "--expires", "6h")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	message := apperror.UserMessage(err)
	assert.Contains(t, message, "local")
	assert.Contains(t, message, "has no download link")
	assert.Contains(t, message, backupRoot)
}
