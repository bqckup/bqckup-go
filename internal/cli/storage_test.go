package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteStorageTextFullMode(t *testing.T) {
	created := time.Date(2026, 11, 10, 3, 0, 12, 0, time.UTC)
	listing := backup.Listing{
		Mode:        "full",
		Destination: "s3-primary",
		Site:        "site-a",
		Artifacts: []backup.ArtifactRow{
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
	assert.Equal(t, "No archive artifacts found for site \"site-a\" on \"s3-primary\".\n", fullOutput.String())

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
		Artifacts: []backup.ArtifactRow{
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
