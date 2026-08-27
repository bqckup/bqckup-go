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

func TestAggregateStatsCountsDistinctSourcesOnce(t *testing.T) {
	artifacts := []history.Artifact{
		{SourceKind: "files", SourceName: "files", Destination: "local-primary", Size: 100},
		{SourceKind: "files", SourceName: "files", Destination: "s3-primary", Size: 100},
		{SourceKind: "database", SourceName: "app-mysql", Destination: "local-primary", Size: 50},
		{SourceKind: "database", SourceName: "app-mysql", Destination: "s3-primary", Size: 50},
	}
	count, size := AggregateStats(artifacts)
	assert.Equal(t, 2, count)
	assert.EqualValues(t, 150, size)
}

func TestAggregateStatsEmpty(t *testing.T) {
	count, size := AggregateStats(nil)
	assert.Equal(t, 0, count)
	assert.EqualValues(t, 0, size)
}

func TestNewPayloadMarshalsToExactSpecSchema(t *testing.T) {
	input := backup.NotifyInput{
		Event:      config.EventBackupSucceeded,
		RunID:      "c699eaba-4928-48e8-a9db-6e3d6121d07f",
		SiteName:   "example.org",
		Status:     backup.StatusSuccess,
		StartedAt:  time.Date(2026, 8, 23, 1, 46, 56, 0, time.UTC),
		FinishedAt: time.Date(2026, 8, 23, 1, 48, 38, 0, time.UTC),
		Artifacts: []history.Artifact{
			{SourceKind: "files", SourceName: "files", Size: 18038862643},
			{SourceKind: "files", SourceName: "files", Size: 18038862643},
			{SourceKind: "database", SourceName: "app", Size: 2048},
		},
	}

	payload := NewPayload(input)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"event": "backup_succeeded",
		"run_id": "c699eaba-4928-48e8-a9db-6e3d6121d07f",
		"site": "example.org",
		"status": "success",
		"started_at": "2026-08-23T01:46:56Z",
		"finished_at": "2026-08-23T01:48:38Z",
		"duration_seconds": 102,
		"artifact_count": 2,
		"size_bytes": 18038864691
	}`, string(raw))
}

func TestNewPayloadAddsErrorFieldsOnlyForFailedAndCancelled(t *testing.T) {
	for _, test := range []struct {
		event    string
		status   backup.Status
		category string
		message  string
		wantErr  bool
	}{
		{event: config.EventBackupFailed, status: backup.StatusFailed, category: "storage", message: "could not store backup artifact", wantErr: true},
		{event: config.EventBackupCancelled, status: backup.StatusCancelled, category: "cancellation", message: "backup was cancelled", wantErr: true},
		{event: config.EventBackupSucceeded, status: backup.StatusSuccess},
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
		Event: config.EventBackupSucceeded, Status: backup.StatusSuccess,
		StartedAt:  time.Date(2026, 8, 23, 1, 48, 38, 0, time.FixedZone("x", 7*3600)),
		FinishedAt: time.Date(2026, 8, 22, 18, 0, 0, 0, time.UTC),
	}
	payload := NewPayload(input)
	assert.EqualValues(t, 0, payload.DurationSeconds)
	assert.Equal(t, "2026-08-22T18:48:38Z", payload.StartedAt)
}
