// The facade: implements the existing backup.IncrementalEngine interface
// with the pure-Go engine, so the runner and history code need no changes
// (P0-T5 option (a)). ListSnapshots is facade-only, used by ApplyRetention
// and future diagnostics.
package facade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	adaptertypes "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/archiver"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
	"github.com/bqckup/bqckup-go/internal/engine/restic/lock"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
)

// Engine is the pure-Go incremental engine. It never spawns processes and
// needs no restic binary.
type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

// EnsureRepository initializes a repository (idempotent), local or
// S3-compatible depending on the URL.
func (e *Engine) EnsureRepository(ctx context.Context, repo adaptertypes.RepoConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.rejectUnsupportedURL(repo.URL); err != nil {
		return err
	}
	b, err := e.openBackend(ctx, repo)
	if err != nil {
		return err
	}
	if _, err := repository.Init(ctx, b, repo.Password); err != nil {
		return &restic.RedactedError{Category: "repository", Message: "could not initialize the repository", Err: err}
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
	if err := e.rejectUnsupportedURL(repo.URL); err != nil {
		return adaptertypes.SnapshotSummary{}, err
	}
	b, err := e.openBackend(ctx, repo)
	if err != nil {
		return adaptertypes.SnapshotSummary{}, err
	}
	r, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		return adaptertypes.SnapshotSummary{}, &restic.RedactedError{Category: "repository", Message: "could not open the repository", Err: err}
	}
	// Exclusive lock; lock errors are returned unwrapped: their message
	// (hostname/pid, never secrets) must reach the user.
	_, release, err := acquireExclusiveLock(ctx, b, r.MasterKey())
	if err != nil {
		return adaptertypes.SnapshotSummary{}, err
	}
	defer release()
	username, hostname := repository.CurrentIdentity()
	snapID, summary, err := archiver.New(r).Backup(ctx, archiver.BackupSpec{
		Paths:    []string(spec.Include),
		Excludes: []string(spec.Exclude),
		Tags:     spec.Tags,
		Hostname: hostname,
		Username: username,
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

// ApplyRetention keeps the newest keepLast snapshots of the site, deletes
// the snapshot files of the rest, and prunes unreachable pack data. It
// returns the number of bytes reclaimed. Never silently skips a configured
// policy.
func (e *Engine) ApplyRetention(ctx context.Context, repo adaptertypes.RepoConfig, keepLast int, siteName string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if keepLast < 1 {
		return 0, errors.New("keep_last must be at least 1")
	}
	if err := e.rejectUnsupportedURL(repo.URL); err != nil {
		return 0, err
	}
	b, err := e.openBackend(ctx, repo)
	if err != nil {
		return 0, err
	}
	r, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		return 0, &restic.RedactedError{Category: "repository", Message: "could not open the repository", Err: err}
	}
	_, release, err := acquireExclusiveLock(ctx, b, r.MasterKey())
	if err != nil {
		return 0, err
	}
	defer release()
	result, err := r.ForgetAndPrune(ctx, keepLast, "site:"+siteName)
	if err != nil {
		return 0, &restic.RedactedError{Category: "repository", Message: "could not apply snapshot retention", Err: err}
	}
	return result.BytesReclaimed, nil
}

// Unlock removes stale repository locks (restic unlock semantics: stale
// locks only, never a live one). Locks are encrypted with the repository
// key, so the repository must be opened first.
func (e *Engine) Unlock(ctx context.Context, repo adaptertypes.RepoConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.rejectUnsupportedURL(repo.URL); err != nil {
		return err
	}
	b, err := e.openBackend(ctx, repo)
	if err != nil {
		return err
	}
	r, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		return &restic.RedactedError{Category: "repository", Message: "could not open the repository to unlock it", Err: err}
	}
	if _, err := lock.RemoveStale(ctx, b, r.MasterKey()); err != nil {
		return &restic.RedactedError{Category: "repository", Message: "could not unlock the repository", Err: err}
	}
	return nil
}

// acquireExclusiveLock takes a restic-compatible exclusive lock and keeps
// it fresh while the operation runs (restic renews its locks every ~5
// minutes too), so a long backup never turns into a stale exclusive lock
// that blocks the next run. release stops refreshing and removes the lock.
func acquireExclusiveLock(ctx context.Context, b backend.Backend, key *crypto.MasterKey) (*lock.Lock, func(), error) {
	l, err := lock.New(ctx, b, key, true)
	if err != nil {
		return nil, nil, err
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// Keep refreshing on failure (restic's refresh loop does the
				// same): a single transient backend error must not stop the
				// refresher, or the lock ages into a stale exclusive lock
				// that blocks every later run until manual unlock.
				_ = l.Refresh(ctx, b, key)
			}
		}
	}()
	release := func() {
		close(stop)
		<-done // no refresh in flight when Unlock runs
		_ = l.Unlock(context.WithoutCancel(ctx), b)
	}
	return l, release, nil
}

// rejectUnsupportedURL refuses repository locations the builtin engine
// cannot serve. Only the scheme is reported (URLs can embed credentials).
func (e *Engine) rejectUnsupportedURL(url string) error {
	for _, prefix := range []string{"sftp:", "rest:", "b2:", "azure:", "gs:"} {
		if strings.HasPrefix(url, prefix) {
			return fmt.Errorf("the builtin engine does not support %s repositories", prefix)
		}
	}
	return nil
}

// openBackend picks the storage backend for a repository. s3: URLs (which
// also cover r2, as RepositoryURL always prefixes s3:) use the S3 backend;
// everything else is a local path.
func (e *Engine) openBackend(ctx context.Context, repo adaptertypes.RepoConfig) (backend.Backend, error) {
	if strings.HasPrefix(repo.URL, "s3:") {
		if repo.Bucket == "" || repo.Prefix == "" {
			return nil, errors.New("s3 repositories require a bucket and object prefix")
		}
		return backend.NewS3(ctx, backend.S3Options{
			Bucket:          repo.Bucket,
			Endpoint:        repo.Endpoint,
			Prefix:          repo.Prefix,
			Region:          repo.Region,
			AccessKeyID:     repo.AccessKeyID,
			SecretAccessKey: repo.SecretAccessKey,
		})
	}
	return backend.NewLocal(repo.URL), nil
}
