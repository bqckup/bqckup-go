package backup

import (
	"context"

	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
)

type FileSource struct {
	Include        []string
	Exclude        []string
	FollowSymlinks bool
}

type Artifact struct {
	Path       string
	Size       int64
	SHA256     string
	SourceKind string
	SourceName string
}

type Exporter interface {
	Export(ctx context.Context, source config.DatabaseSource, destination string) (Artifact, error)
}

type IncrementalEngine interface {
	EnsureRepository(ctx context.Context, repo restic.RepoConfig) error
	BackupFiles(ctx context.Context, repo restic.RepoConfig, spec restic.BackupSpec) (restic.SnapshotSummary, error)
	ApplyRetention(ctx context.Context, repo restic.RepoConfig, keepLast int, siteName string) error
	Unlock(ctx context.Context, repo restic.RepoConfig) error
}
