package backup

import (
	"context"

	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
)

type FileSource struct {
	Include        []string
	Exclude        []string
	FollowSymlinks bool
}

type Package struct {
	Path       string
	Size       int64
	SHA256     string
	SourceKind string
	SourceName string
}

type Exporter interface {
	Export(ctx context.Context, source config.DatabaseSource, destination string) (Package, error)
}

type IncrementalEngine interface {
	EnsureRepository(ctx context.Context, repo incremental.RepoConfig) error
	BackupFiles(ctx context.Context, repo incremental.RepoConfig, spec incremental.BackupSpec) (incremental.SnapshotSummary, error)
	// ApplyRetention forgets snapshots beyond keepLast for the site and
	// prunes unreachable data; it returns the reclaimed bytes.
	ApplyRetention(ctx context.Context, repo incremental.RepoConfig, keepLast int, siteName string) (int64, error)
	// Unlock removes stale repository locks (never a live one).
	Unlock(ctx context.Context, repo incremental.RepoConfig) error
}
