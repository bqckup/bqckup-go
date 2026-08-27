// Package notify delivers terminal backup-run notifications over SMTP,
// generic webhooks, and Discord webhooks. The concrete dispatcher implements
// backup.Notifier; channels are constructed in internal/app. Delivery is best
// effort: errors are returned for the caller to warn about, and never alter
// run status or history.
package notify

import (
	"fmt"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
)

// Event is a notification event name. Values are the config contract strings.
type Event string

const (
	EventBackupSucceeded Event = config.EventBackupSucceeded
	EventBackupFailed    Event = config.EventBackupFailed
	EventBackupCancelled Event = config.EventBackupCancelled
)

// Payload is the shared notification payload for every channel. The JSON
// shape is the spec contract; error fields appear only for failed and
// cancelled runs.
type Payload struct {
	Event           Event  `json:"event"`
	RunID           string `json:"run_id"`
	Site            string `json:"site"`
	Status          string `json:"status"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
	DurationSeconds int64  `json:"duration_seconds"`
	ArtifactCount   int    `json:"artifact_count"`
	SizeBytes       int64  `json:"size_bytes"`
	ErrorCategory   string `json:"error_category,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
}

// NewPayload builds the shared payload from a run's facts. ErrorCategory and
// ErrorMessage must already be redacted (apperror.UserMessage).
func NewPayload(input backup.NotifyInput) Payload {
	started := input.StartedAt.UTC()
	finished := input.FinishedAt.UTC()
	duration := finished.Sub(started)
	if duration < 0 {
		duration = 0
	}
	payload := Payload{
		Event:           Event(input.Event),
		RunID:           input.RunID,
		Site:            input.SiteName,
		Status:          string(input.Status),
		StartedAt:       started.Format(time.RFC3339),
		FinishedAt:      finished.Format(time.RFC3339),
		DurationSeconds: int64(duration.Seconds()),
		ErrorCategory:   input.ErrorCategory,
		ErrorMessage:    input.ErrorMessage,
	}
	payload.ArtifactCount, payload.SizeBytes = AggregateStats(input.Artifacts)
	return payload
}

// AggregateStats counts stored artifacts once per distinct source and sums
// their sizes, so one artifact stored to several destinations is not
// double-counted.
func AggregateStats(artifacts []history.Artifact) (count int, size int64) {
	seen := make(map[[2]string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		key := [2]string{artifact.SourceKind, artifact.SourceName}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		count++
		size += artifact.Size
	}
	return count, size
}

// statusColor returns the channel color for a payload status.
func statusColor(status string) int {
	switch status {
	case string(backup.StatusSuccess):
		return 0x2ECC71
	case string(backup.StatusCancelled):
		return 0xF1C40F
	default:
		return 0xE74C3C
	}
}

// formatBytes renders a byte count as a short human string.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
