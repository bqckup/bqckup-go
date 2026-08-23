package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteHistoryTextRendersReadableSummary(t *testing.T) {
	started := time.Date(2026, 8, 23, 12, 56, 11, 0, time.UTC)
	finished := started.Add(1250 * time.Millisecond)
	runs := []history.BackupRun{{
		ID:             "c699eaba-4928-48e8-a9db-6e3d6121d07f",
		SiteName:       "mysql-test",
		Status:         history.StatusSuccess,
		StartedAt:      started,
		FinishedAt:     &finished,
		DurationMillis: 1250,
		Artifacts: []history.Artifact{
			{Size: 196},
			{Size: 904},
		},
	}}

	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, runs))

	text := output.String()
	assert.Contains(t, text, "STATUS")
	assert.Contains(t, text, "SITE")
	assert.Contains(t, text, "STARTED")
	assert.Contains(t, text, "DURATION")
	assert.Contains(t, text, "ARTIFACTS")
	assert.Contains(t, text, "SIZE")
	assert.Contains(t, text, "RUN ID")
	assert.Contains(t, text, "SUCCESS")
	assert.Contains(t, text, "mysql-test")
	assert.Contains(t, text, started.Local().Format("02 Jan 2006, 15:04 MST"))
	assert.Contains(t, text, "1.25s")
	assert.Contains(t, text, "1.1 KiB")
	assert.Contains(t, text, runs[0].ID)
	assert.Contains(t, text, "1 run, 2 artifacts, 1.1 KiB total")
}

func TestWriteHistoryTextShowsFailureDetails(t *testing.T) {
	runs := []history.BackupRun{{
		ID:             "failed-run",
		SiteName:       "database",
		Status:         history.StatusFailed,
		StartedAt:      time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
		DurationMillis: 500,
		ErrorCategory:  "execution",
		ErrorMessage:   "could not export database",
	}}

	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, runs))
	assert.Contains(t, output.String(), "Error [execution]: could not export database")
}

func TestWriteHistoryTextExplainsEmptyHistory(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, nil))
	assert.Equal(t, "No backup history found.\n", output.String())
}
