package storage

import (
	"context"
	"errors"
	"time"
)

const (
	// BackupDateLayout is the human-readable UTC date directory. Go's fixed
	// reference-time month token always formats and parses English month names.
	BackupDateLayout = "02-January-2006"
	// BackupRunLayout is the compact UTC run directory within one date.
	// Nanosecond resolution: two runs of one site within the same UTC
	// second must not share a backup-set key, or the second run's store
	// write is rejected as a collision (stores are write-once) and the
	// whole run fails.
	BackupRunLayout = "15-04-05.000000000"
	// TimestampLayout is the complete directory path used for new archive sets.
	TimestampLayout = BackupDateLayout + "/" + BackupRunLayout

	// ReadableSecondsLayout is the previous readable layout (second
	// resolution). It remains readable so listing and retention continue to
	// manage archive sets created before run times gained nanoseconds.
	ReadableSecondsLayout = BackupDateLayout + "/15-04-05"
)

// LegacyTimestampLayout remains readable so listing and retention continue to
// manage archive sets created before the human-readable layout was introduced.
const LegacyTimestampLayout = "2006-01-02T15-04-05.000000000Z"

// FormatBackupSet returns the canonical directory path for a new archive set.
func FormatBackupSet(createdAt time.Time) string {
	return createdAt.UTC().Format(TimestampLayout)
}

// ParseBackupSet accepts the canonical layout, the previous second-resolution
// readable layout, and the previous flat UTC timestamp layout. Exact round
// trips reject ambiguous or non-UTC names.
func ParseBackupSet(value string) (time.Time, error) {
	for _, layout := range []string{TimestampLayout, ReadableSecondsLayout, LegacyTimestampLayout} {
		createdAt, err := time.Parse(layout, value)
		if err == nil && createdAt.Location() == time.UTC && createdAt.Format(layout) == value {
			return createdAt, nil
		}
	}
	return time.Time{}, errors.New("invalid backup set timestamp")
}

type StoredArtifact struct {
	Key    string
	Size   int64
	SHA256 string
}

// Artifact is the verified local file handed to a storage adapter.
type Artifact struct {
	Path   string
	Size   int64
	SHA256 string
}

type BackupSet struct {
	Key       string
	CreatedAt time.Time
}

// DownloadLink is a temporary signed URL for one stored object. Key is
// relative to the storage document prefix, exactly as storage list prints it.
type DownloadLink struct {
	URL       string
	Key       string
	ExpiresAt time.Time
}

// RemoteArtifact is one stored object listed from a remote destination.
// Key is relative to the storage document prefix (bqckup/<site>/<set>/<name>).
type RemoteArtifact struct {
	Key       string
	Size      int64
	CreatedAt time.Time
}

type Store interface {
	Put(ctx context.Context, artifact Artifact, key string) (StoredArtifact, error)
	Delete(ctx context.Context, key string) error
	ListBackupSets(ctx context.Context, sitePrefix string) ([]BackupSet, error)
	Probe(ctx context.Context) error
}
