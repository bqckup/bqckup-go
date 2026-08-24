package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteHistoryTextRendersInvestigationSummary(t *testing.T) {
	started := time.Date(2026, 8, 23, 12, 56, 11, 0, time.UTC)
	finished := started.Add(1250 * time.Millisecond)
	runs := []history.BackupRun{
		{
			ID: "success-run", SiteName: "multi-destination", Status: history.StatusSuccess,
			StartedAt: started, FinishedAt: &finished, DurationMillis: 1250,
			Artifacts: []history.Artifact{
				artifact("files", "files", "local-primary", 1024),
				artifact("files", "files", "remote-primary", 1024),
				artifact("database", "application", "local-primary", 2048),
				artifact("database", "application", "remote-primary", 2048),
			},
		},
		{
			ID: "failed-run", SiteName: "failed-site", Status: history.StatusFailed,
			StartedAt: started.Add(-time.Hour), DurationMillis: 500,
			ErrorCategory: "execution", ErrorMessage: "could not export database",
		},
		{
			ID: "cancelled-run", SiteName: "cancelled-site", Status: history.StatusCancelled,
			StartedAt: started.Add(-2 * time.Hour), DurationMillis: 250,
			ErrorCategory: "cancellation", ErrorMessage: "backup was cancelled",
		},
		{
			ID: "running-run", SiteName: "running-site", Status: history.StatusRunning,
			StartedAt: started.Add(-3 * time.Hour),
		},
	}

	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, runs, false))

	text := output.String()
	for _, heading := range []string{"STATUS", "SITE", "STARTED", "DURATION", "ARTIFACTS", "DESTINATIONS", "LOGICAL SIZE", "RUN ID"} {
		assert.Contains(t, text, heading)
	}
	for _, status := range []string{"SUCCESS", "FAILED", "CANCELLED", "RUNNING"} {
		assert.Contains(t, text, status)
	}
	assert.Contains(t, text, started.Local().Format("02 Jan 2006, 15:04 MST"))
	assert.Contains(t, text, "1.25s")
	assert.Contains(t, text, "in progress")
	assert.Regexp(t, `SUCCESS\s+multi-destination\s+.+\s+1\.25s\s+2\s+2\s+3\.0 KiB\s+success-run`, text)
	assert.Regexp(t, `RUNNING\s+running-site\s+.+\s+in progress\s+0\s+0\s+0 B\s+running-run`, text)
	assert.Contains(t, text, "3.0 KiB")
	assert.NotContains(t, text, "6.0 KiB")
	assert.Contains(t, text, "Run failed-run error [execution]: could not export database")
	assert.Contains(t, text, "Run cancelled-run error [cancellation]: backup was cancelled")
	assert.Contains(t, text, "4 runs, 2 logical artifacts, 2 destinations, 3.0 KiB logical total")
}

func TestWriteHistoryTextCountsSingleDestination(t *testing.T) {
	runs := []history.BackupRun{{
		ID: "single-run", SiteName: "single-site", Status: history.StatusSuccess,
		StartedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
		Artifacts: []history.Artifact{
			artifact("files", "files", "local-primary", 512),
			artifact("database", "application", "local-primary", 1024),
		},
	}}

	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, runs, false))
	assert.Regexp(t, `SUCCESS\s+single-site\s+.+\s+0s\s+2\s+1\s+1\.5 KiB\s+single-run`, output.String())
	assert.Contains(t, output.String(), "1 run, 2 logical artifacts, 1 destination, 1.5 KiB logical total")
}

func TestWriteHistoryTextDetailsListsEveryStoredCopy(t *testing.T) {
	runs := []history.BackupRun{{
		ID: "details-run", SiteName: "example", Status: history.StatusSuccess,
		StartedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
		Artifacts: []history.Artifact{
			{SourceKind: "files", SourceName: "files", Destination: "remote-primary", Status: history.ArtifactStored, Size: 1024, ObjectKey: "bqckup/example/timestamp/files.tar.gz"},
			{SourceKind: "files", SourceName: "files", Destination: "local-primary", Status: history.ArtifactStored, Size: 1024, ObjectKey: "bqckup/example/timestamp/files.tar.gz"},
		},
	}}

	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, runs, true))
	text := output.String()
	assert.Contains(t, text, "Artifacts for run details-run")
	for _, heading := range []string{"SOURCE", "DESTINATION", "STATUS", "SIZE", "OBJECT KEY"} {
		assert.Contains(t, text, heading)
	}
	assert.Contains(t, text, "files/files")
	assert.Contains(t, text, "local-primary")
	assert.Contains(t, text, "remote-primary")
	assert.Contains(t, text, "STORED")
	assert.Equal(t, 2, strings.Count(text, "bqckup/example/timestamp/files.tar.gz"))
}

func TestWriteHistoryTextHidesDetailsByDefault(t *testing.T) {
	runs := []history.BackupRun{{
		ID: "summary-run", SiteName: "example", Status: history.StatusSuccess,
		StartedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
		Artifacts: []history.Artifact{artifact("files", "files", "local-primary", 512)},
	}}

	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, runs, false))
	assert.NotContains(t, output.String(), "Artifacts for run")
}

func TestWriteHistoryTextRedactsSensitiveHistoryFields(t *testing.T) {
	privateURL := strings.Join([]string{"https", "://", "storage.invalid", "/private"}, "")
	privatePath := strings.Join([]string{"/srv", "/customer-data"}, "")
	passwordAssignment := strings.Join([]string{"PASS", "WORD", "=not-a-real-secret"}, "")
	runs := []history.BackupRun{{
		ID: "sensitive-run", SiteName: "example", Status: history.StatusFailed,
		StartedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC), ErrorCategory: "storage",
		ErrorMessage: "provider response from " + privateURL + " " + passwordAssignment,
		Artifacts: []history.Artifact{{
			SourceKind: "files", SourceName: privatePath, Destination: privateURL,
			Status: history.ArtifactFailed, ObjectKey: privateURL,
		}},
	}}

	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, runs, true))
	text := output.String()
	assert.NotContains(t, text, privateURL)
	assert.NotContains(t, text, privatePath)
	assert.NotContains(t, text, passwordAssignment)
	assert.Contains(t, text, "Run sensitive-run error [storage]: [redacted]")
	assert.Contains(t, text, "[redacted]")
}

func TestWriteHistoryTextExplainsEmptyHistory(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, writeHistoryText(&output, nil, false))
	assert.Equal(t, "No backup history found for the selected filters.\n", output.String())
}

func artifact(sourceKind, sourceName, destination string, size int64) history.Artifact {
	return history.Artifact{
		SourceKind: sourceKind, SourceName: sourceName, Destination: destination,
		Status: history.ArtifactStored, Size: size, ObjectKey: "bqckup/example/timestamp/artifact",
	}
}
