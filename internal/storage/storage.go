package storage

import (
	"context"
	"time"
)

// TimestampLayout is the portable UTC directory name used for backup sets.
// Nanosecond resolution: two runs in the same second (--force twice in a
// row, cron and a manual run overlapping) must never collide on the same
// object key — the stores are write-once and reject overwrites.
const TimestampLayout = "2006-01-02T15-04-05.000000000Z"

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
}
