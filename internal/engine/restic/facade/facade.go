// The facade: implements the existing backup.IncrementalEngine interface
// with the pure-Go engine, so the runner and history code need no changes
// (P0-T5 option (a)). ListSnapshots is facade-only, used by ApplyRetention
// and future diagnostics.
package facade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"sort"
	"strings"

	adaptertypes "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/archiver"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
)

// Engine is the pure-Go incremental engine. It never spawns processes and
// needs no restic binary.
type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

// EnsureRepository initializes a local repository (idempotent).
func (e *Engine) EnsureRepository(ctx context.Context, repo adaptertypes.RepoConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.rejectRemoteURL(repo.URL); err != nil {
		return err
	}
	if _, err := repository.Init(ctx, backend.NewLocal(repo.URL), repo.Password); err != nil {
		return &restic.RedactedError{Category: "repository", Message: "could not initialize the local repository", Err: err}
	}
	return nil
}

// BackupFiles walks the include paths and writes one snapshot.
func (e *Engine) BackupFiles(ctx context.Context, repo adaptertypes.RepoConfig, spec adaptertypes.BackupSpec) (adaptertypes.SnapshotSummary, error) {
	if err := ctx.Err(); err != nil {
		return adaptertypes.SnapshotSummary{}, err
	}
	if len(spec.Include) == 0 {
		return adaptertypes.SnapshotSummary{}, errors.New("at least one include path is required for backup")
	}
	if err := e.rejectRemoteURL(repo.URL); err != nil {
		return adaptertypes.SnapshotSummary{}, err
	}
	r, err := repository.Open(ctx, backend.NewLocal(repo.URL), repo.Password)
	if err != nil {
		return adaptertypes.SnapshotSummary{}, &restic.RedactedError{Category: "repository", Message: "could not open the local repository", Err: err}
	}
	snapID, summary, err := archiver.New(r).Backup(ctx, archiver.BackupSpec{
		Paths:    spec.Include,
		Excludes: spec.Exclude,
		Tags:     spec.Tags,
		Hostname: hostname(),
		Username: username(),
	})
	if err != nil {
		return adaptertypes.SnapshotSummary{}, &restic.RedactedError{Category: "repository", Message: "could not create the incremental backup", Err: err}
	}
	return adaptertypes.SnapshotSummary{
		SnapshotID:          snapID.String(),
		MessageType:         "summary",
		FilesNew:            summary.FilesNew,
		FilesChanged:        summary.FilesChanged,
		FilesUnmodified:     summary.FilesUnmodified,
		TotalFilesProcessed: summary.TotalFilesProcessed,
		TotalBytesProcessed: summary.TotalBytesProcessed,
		DataAdded:           summary.DataAdded,
		TotalDuration:       summary.TotalDuration,
	}, nil
}

// ApplyRetention keeps the newest keepLast snapshots of the site and
// deletes the snapshot files of the rest. No prune: data blobs stay until
// L2. Never silently skips a configured policy.
func (e *Engine) ApplyRetention(ctx context.Context, repo adaptertypes.RepoConfig, keepLast int, siteName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if keepLast < 1 {
		return errors.New("keep_last must be at least 1")
	}
	if err := e.rejectRemoteURL(repo.URL); err != nil {
		return err
	}
	r, err := repository.Open(ctx, backend.NewLocal(repo.URL), repo.Password)
	if err != nil {
		return &restic.RedactedError{Category: "repository", Message: "could not open the local repository", Err: err}
	}
	snapshots, err := r.ListSnapshots(ctx)
	if err != nil {
		return &restic.RedactedError{Category: "repository", Message: "could not list snapshots for retention", Err: err}
	}
	tag := "site:" + siteName
	mine := make([]repository.SnapshotWithID, 0, len(snapshots))
	for _, entry := range snapshots {
		for _, candidate := range entry.Snapshot.Tags {
			if candidate == tag {
				mine = append(mine, entry)
				break
			}
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Snapshot.Time.Before(mine[j].Snapshot.Time) })
	for i := 0; i < len(mine)-keepLast; i++ {
		if err := r.DeleteSnapshot(ctx, mine[i].ID); err != nil {
			return &restic.RedactedError{Category: "repository", Message: "could not apply snapshot retention", Err: err}
		}
	}
	return nil
}

// Unlock is a documented no-op: the builtin engine writes no lock files
// and never consults them (stale-lock handling is L4). The method exists
// for interface compatibility with the process adapter.
func (e *Engine) Unlock(context.Context, adaptertypes.RepoConfig) error { return nil }

// ListSnapshots returns every snapshot in the repository (facade-only; the
// runner interface is unchanged).
func (e *Engine) ListSnapshots(ctx context.Context, repo adaptertypes.RepoConfig) ([]repository.SnapshotWithID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.rejectRemoteURL(repo.URL); err != nil {
		return nil, err
	}
	r, err := repository.Open(ctx, backend.NewLocal(repo.URL), repo.Password)
	if err != nil {
		return nil, &restic.RedactedError{Category: "repository", Message: "could not open the local repository", Err: err}
	}
	return r.ListSnapshots(ctx)
}

// rejectRemoteURL refuses any non-local repository location. Only the
// scheme is reported (URLs can embed credentials).
func (e *Engine) rejectRemoteURL(url string) error {
	for _, prefix := range []string{"s3:", "r2:", "sftp:", "rest:", "b2:", "azure:", "gs:"} {
		if strings.HasPrefix(url, prefix) {
			return fmt.Errorf("the builtin engine supports local repositories only (%s storage needs engine: restic until L3)", prefix)
		}
	}
	return nil
}

func hostname() string {
	if name, err := os.Hostname(); err == nil && name != "" {
		return name
	}
	return "unknown"
}

func username() string {
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	return "unknown"
}
