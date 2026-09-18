package notify

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeCountsDistinctSourcesOnce(t *testing.T) {
	packages := []history.Package{
		{SourceKind: "files", SourceName: "files", Destination: "local-primary", Size: 100, ObjectKey: "bqckup/site/ts/files.tar.gz"},
		{SourceKind: "files", SourceName: "files", Destination: "s3-primary", Size: 100, ObjectKey: "bqckup/site/ts/files.tar.gz"},
		{SourceKind: "database", SourceName: "app-mysql", Destination: "local-primary", Size: 50, ObjectKey: "bqckup/site/ts/app-mysql.sql.gz"},
		{SourceKind: "database", SourceName: "app-mysql", Destination: "s3-primary", Size: 50, ObjectKey: "bqckup/site/ts/app-mysql.sql.gz"},
	}
	count, size, keys := summarize(packages)
	assert.Equal(t, 2, count)
	assert.EqualValues(t, 150, size)
	assert.Equal(t, []string{"bqckup/site/ts/files.tar.gz", "bqckup/site/ts/app-mysql.sql.gz"}, keys)
}

func TestSummarizeEmpty(t *testing.T) {
	count, size, keys := summarize(nil)
	assert.Equal(t, 0, count)
	assert.EqualValues(t, 0, size)
	assert.Empty(t, keys)
}

func TestSummarizeCapsKeysButKeepsCount(t *testing.T) {
	packages := make([]history.Package, 0, 12)
	for i := 0; i < 12; i++ {
		packages = append(packages, history.Package{
			SourceKind: "database",
			SourceName: string(rune('a' + i)),
			Size:       1,
			ObjectKey:  "bqckup/site/ts/db.sql.gz",
		})
	}
	count, size, keys := summarize(packages)
	assert.Equal(t, 12, count)
	assert.EqualValues(t, 12, size)
	assert.Len(t, keys, 10)
}

func TestNewPayloadMarshalsToExactSpecSchema(t *testing.T) {
	lastSuccess := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	input := backup.NotifyInput{
		Event:            config.EventBackupFailed,
		RunID:            "c699eaba-4928-48e8-a9db-6e3d6121d07f",
		SiteName:         "example.org",
		Status:           backup.StatusFailed,
		StartedAt:        time.Date(2026, 8, 23, 1, 46, 56, 0, time.UTC),
		FinishedAt:       time.Date(2026, 8, 23, 1, 48, 38, 0, time.UTC),
		LastSuccessfulAt: lastSuccess,
		FailureStreak:    3,
		ErrorCategory:    "storage",
		ErrorMessage:     "could not store backup package",
		Packages: []history.Package{
			{SourceKind: "files", SourceName: "files", Size: 18038862643, ObjectKey: "bqckup/example.org/2026-08-23T01-46-56Z/files.tar.gz"},
			{SourceKind: "files", SourceName: "files", Size: 18038862643, ObjectKey: "bqckup/example.org/2026-08-23T01-46-56Z/files.tar.gz"},
			{SourceKind: "database", SourceName: "app", Size: 2048, ObjectKey: "bqckup/example.org/2026-08-23T01-46-56Z/app.sql.gz"},
		},
	}

	payload := NewPayload(input)
	payload.ServerID = "ojt-zaku"
	payload.Hostname = "web-01"
	payload.ServerIP = "203.0.113.7"
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"event": "backup_failed",
		"run_id": "c699eaba-4928-48e8-a9db-6e3d6121d07f",
		"server_id": "ojt-zaku",
		"site": "example.org",
		"hostname": "web-01",
		"server_ip": "203.0.113.7",
		"status": "failed",
		"started_at": "2026-08-23T01:46:56Z",
		"finished_at": "2026-08-23T01:48:38Z",
		"duration_seconds": 102,
		"last_successful_at": "2026-08-22T01:00:00Z",
		"failure_streak": 3,
		"package_count": 2,
		"size_bytes": 18038864691,
		"packages": [
			"bqckup/example.org/2026-08-23T01-46-56Z/files.tar.gz",
			"bqckup/example.org/2026-08-23T01-46-56Z/app.sql.gz"
		],
		"error_category": "storage",
		"error_message": "could not store backup package"
	}`, string(raw))
}

func TestNewPayloadOmitsLastSuccessfulAtWhenZero(t *testing.T) {
	input := backup.NotifyInput{
		Event:  config.EventBackupFailed,
		Status: backup.StatusFailed,
	}
	raw, err := json.Marshal(NewPayload(input))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "last_successful_at")
	assert.Contains(t, string(raw), `"failure_streak":0`)
}

func TestNewPayloadOmitsPackagesWhenRunHasNone(t *testing.T) {
	input := backup.NotifyInput{
		Event: config.EventBackupFailed, Status: backup.StatusFailed,
		ErrorCategory: "storage", ErrorMessage: "could not store backup package",
	}
	raw, err := json.Marshal(NewPayload(input))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "packages")
	assert.Contains(t, string(raw), `"package_count":0`)
}

func TestPartialPayloadReportsIncompleteSnapshotWithoutPaths(t *testing.T) {
	input := backup.NotifyInput{
		Event:         "backup_partial",
		SiteName:      "example.org",
		Status:        backup.Status("partial"),
		FilesSkipped:  2,
		ErrorCategory: "source",
		ErrorMessage:  "2 source entries could not be read; an incomplete snapshot was saved",
	}
	payload := NewPayload(input)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"files_skipped":2`)
	assert.NotContains(t, string(raw), "/srv/")
	assert.Equal(t, "Backup incomplete for example.org", headline(payload))
	assert.Contains(t, description(payload), "2 source entries")
	assert.Equal(t, 0xF1C40F, statusColor(payload.Status))
}

func TestNewPayloadAddsDiagnosticFieldsForTerminalWarningsAndFailures(t *testing.T) {
	for _, test := range []struct {
		event    string
		status   backup.Status
		category string
		message  string
		wantErr  bool
	}{
		{event: config.EventBackupFailed, status: backup.StatusFailed, category: "storage", message: "could not store backup package", wantErr: true},
		{event: config.EventBackupCancelled, status: backup.StatusCancelled, category: "cancellation", message: "backup was cancelled", wantErr: true},
	} {
		input := backup.NotifyInput{
			Event: test.event, Status: test.status,
			ErrorCategory: test.category, ErrorMessage: test.message,
		}
		payload := NewPayload(input)
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		if test.wantErr {
			assert.Contains(t, string(raw), `"error_category":"`+test.category+`"`)
			assert.Contains(t, string(raw), `"error_message":"`+test.message+`"`)
		} else {
			assert.NotContains(t, string(raw), "error_category")
			assert.NotContains(t, string(raw), "error_message")
		}
	}
}

func TestNewPayloadClampsNegativeDurationAndUsesUTC(t *testing.T) {
	input := backup.NotifyInput{
		Event: config.EventBackupFailed, Status: backup.StatusFailed,
		StartedAt:  time.Date(2026, 8, 23, 1, 48, 38, 0, time.FixedZone("x", 7*3600)),
		FinishedAt: time.Date(2026, 8, 22, 18, 0, 0, 0, time.UTC),
	}
	payload := NewPayload(input)
	assert.EqualValues(t, 0, payload.DurationSeconds)
	assert.Equal(t, "2026-08-22T18:48:38Z", payload.StartedAt)
}

func TestHumanStatus(t *testing.T) {
	assert.Equal(t, "Backup failed", humanStatus("failed"))
	assert.Equal(t, "Backup cancelled", humanStatus("cancelled"))
	assert.Equal(t, "mystery", humanStatus("mystery"))
}

func TestStatusColor(t *testing.T) {
	assert.Equal(t, 0xF1C40F, statusColor("cancelled"))
	assert.Equal(t, 0xF1C40F, statusColor("no_change"))
	assert.Equal(t, 0xE74C3C, statusColor("failed"))
	assert.Equal(t, 0xE74C3C, statusColor("unknown"))
}

func TestHeadline(t *testing.T) {
	assert.Equal(t, "Backup failed for example.org", headline(Payload{Status: "failed", Site: "example.org"}))
	assert.Equal(t, "Backup cancelled for example.org", headline(Payload{Status: "cancelled", Site: "example.org"}))
	assert.Equal(t, "No changes detected for example.org", headline(Payload{Status: "no_change", Site: "example.org"}))
}

func TestLastSuccessfulLine(t *testing.T) {
	dt := time.Date(2026, 8, 24, 6, 12, 0, 0, time.UTC)
	assert.Equal(t, dt.Local().Format("02 Jan 2006, 15:04"), lastSuccessfulLine(dt))
}

func TestDurationHuman(t *testing.T) {
	assert.Equal(t, "0 s", durationHuman(0))
	assert.Equal(t, "45 s", durationHuman(45))
	assert.Equal(t, "1 min", durationHuman(60))
	assert.Equal(t, "4 min", durationHuman(240))
	assert.Equal(t, "1 h", durationHuman(3600))
	assert.Equal(t, "1 h 2 min", durationHuman(3720))
}

func TestServerLine(t *testing.T) {
	assert.Equal(t, "mynas (192.168.1.10)", serverLine("mynas", "192.168.1.10"))
	assert.Equal(t, "192.168.1.10", serverLine("", "192.168.1.10"))
	assert.Equal(t, "mynas", serverLine("mynas", ""))
	assert.Equal(t, "", serverLine("", ""))
}

func TestItemsLine(t *testing.T) {
	assert.Equal(t, "1 item", itemsLine(1))
	assert.Equal(t, "3 items", itemsLine(3))
	assert.Equal(t, "0 items", itemsLine(0))
}

func TestItemsSizeLine(t *testing.T) {
	assert.Equal(t, "", itemsSizeLine(0, 0))
	assert.Equal(t, "1 item (812.0 KiB)", itemsSizeLine(1, 831488))
	assert.Equal(t, "3 items (2.1 GiB)", itemsSizeLine(3, 2254857830))
}

func TestDescription(t *testing.T) {
	t.Run("failed config without packages", func(t *testing.T) {
		p := Payload{Status: "failed", ErrorCategory: "config"}
		assert.Equal(t, "A setting needs attention. The backup configuration was rejected, so the backup did not run.", description(p))
	})

	t.Run("failed preflight with 1 package", func(t *testing.T) {
		p := Payload{Status: "failed", ErrorCategory: "preflight", PackageCount: 1, SizeBytes: 831488}
		assert.Equal(t, "The backup did not start. A check before the backup failed. 1 item (812.0 KiB) was prepared.", description(p))
	})

	t.Run("failed execution parses startedAt", func(t *testing.T) {
		started := time.Date(2026, 8, 23, 0, 6, 0, 0, time.UTC)
		p := Payload{
			Status:        "failed",
			ErrorCategory: "execution",
			StartedAt:     started.Format(time.RFC3339),
			PackageCount:  3,
			SizeBytes:     2254857830,
		}
		assert.Equal(t, "The backup ended with an execution error. 3 items (2.1 GiB) were prepared.", description(p))
	})

	t.Run("failed execution unparseable startedAt falls back to raw", func(t *testing.T) {
		p := Payload{
			Status:        "failed",
			ErrorCategory: "execution",
			StartedAt:     "invalid-timestamp",
		}
		assert.Equal(t, "The backup ended with an execution error.", description(p))
	})

	t.Run("failed storage", func(t *testing.T) {
		p := Payload{Status: "failed", ErrorCategory: "storage"}
		assert.Equal(t, "The backup ran but could not be saved to its destination.", description(p))
	})

	t.Run("failed persistence", func(t *testing.T) {
		p := Payload{Status: "failed", ErrorCategory: "persistence"}
		assert.Equal(t, "The backup finished but its result could not be recorded in the history database.", description(p))
	})

	t.Run("failed internal", func(t *testing.T) {
		p := Payload{Status: "failed", ErrorCategory: "internal"}
		assert.Equal(t, "An unexpected problem stopped the backup.", description(p))
	})

	t.Run("failed unknown category", func(t *testing.T) {
		p := Payload{Status: "failed", ErrorCategory: "unknown_category"}
		assert.Equal(t, "An unexpected problem stopped the backup.", description(p))
	})

	t.Run("cancelled run", func(t *testing.T) {
		p := Payload{Status: "cancelled", ErrorCategory: "cancellation", PackageCount: 1, SizeBytes: 1024}
		assert.Equal(t, "The backup was stopped before it finished. 1 item (1.0 KiB) was prepared.", description(p))
	})

	t.Run("no_change with anchor and databases", func(t *testing.T) {
		anchorTime := time.Date(2026, 8, 25, 6, 12, 0, 0, time.UTC)
		p := Payload{
			Status:             "no_change",
			LastSuccessfulAt:   anchorTime.Format(time.RFC3339),
			HasDatabaseSources: true,
		}
		expectedTime := anchorTime.Local().Format("02 Jan 15:04")
		assert.Equal(t, "The new backup is identical to the last one (unchanged since "+expectedTime+"). Likely an idle app, or the database dump silently failed.", description(p))
	})

	t.Run("no_change with anchor without databases", func(t *testing.T) {
		anchorTime := time.Date(2026, 8, 25, 6, 12, 0, 0, time.UTC)
		p := Payload{
			Status:             "no_change",
			LastSuccessfulAt:   anchorTime.Format(time.RFC3339),
			HasDatabaseSources: false,
		}
		expectedTime := anchorTime.Local().Format("02 Jan 15:04")
		assert.Equal(t, "The new backup is identical to the last one (unchanged since "+expectedTime+").", description(p))
	})

	t.Run("no_change without anchor", func(t *testing.T) {
		p := Payload{
			Status:             "no_change",
			HasDatabaseSources: false,
		}
		assert.Equal(t, "The new backup is identical to the last one.", description(p))
	})
}

func TestMonitoringFooter(t *testing.T) {
	now := time.Date(2026, 8, 26, 14, 5, 0, 0, time.UTC)
	expected := "Bqckup Backup Monitoring · " + now.Local().Format("15:04 MST · 02 Jan 2006")
	assert.Equal(t, expected, monitoringFooter(now))
}

func TestFailureMessage(t *testing.T) {
	assert.Equal(t, "could not export database", failureMessage(Payload{Status: "failed", ErrorMessage: "could not export database"}))
	assert.Equal(t, "1 item is unchanged from the previous run.", failureMessage(Payload{Status: "no_change", ErrorMessage: "1 item is unchanged from the previous run."}))
	assert.Empty(t, failureMessage(Payload{Status: "success", ErrorMessage: "unused"}))
	assert.Empty(t, failureMessage(Payload{Status: "cancelled", ErrorMessage: "backup was cancelled"}))
}
