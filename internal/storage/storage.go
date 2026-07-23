package storage

import (
	"context"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
)

// TimestampLayout is the portable UTC directory name used for backup sets.
const TimestampLayout = "2006-01-02T15-04-05Z"

type StoredArtifact struct {
	Key    string
	Size   int64
	SHA256 string
}

type BackupSet struct {
	Key       string
	CreatedAt time.Time
}

type Store interface {
	Put(ctx context.Context, artifact backup.Artifact, key string) (StoredArtifact, error)
	Delete(ctx context.Context, key string) error
	ListBackupSets(ctx context.Context, sitePrefix string) ([]BackupSet, error)
}
