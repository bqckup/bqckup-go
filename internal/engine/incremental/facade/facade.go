// The facade: implements the existing backup.IncrementalEngine interface
// with the pure-Go engine, so the runner and history code need no changes
// (P0-T5 option (a)). ListSnapshots is facade-only, used by ApplyRetention
// and future diagnostics.
package facade

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	backupincremental "github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/archiver"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/lock"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/repository"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/restorer"
)

// Engine is the pure-Go incremental engine. It never spawns processes and
// needs no restic binary.
type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

// EnsureRepository initializes a repository (idempotent), local or
// S3-compatible depending on the URL.
func (e *Engine) EnsureRepository(ctx context.Context, repo backupincremental.RepoConfig) error {
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
	// First-run initialization is stat-then-write: two machines starting
	// the first backup of the same site simultaneously would each write a
	// config with its own random master key, and the loser's data would be
	// undecryptable forever. Serialize initialization with an exclusive
	// lock while no config exists. Master-key locks cannot be used here —
	// the master key only exists after Init — and an init lock cannot read
	// master-key locks, so once a config exists this path skips locking
	// entirely and the normal master-key lock (taken by BackupFiles)
	// governs. Lock errors are returned unwrapped, like BackupFiles: their
	// message (hostname, PID) must reach the user.
	if _, err := b.Stat(ctx, incremental.Handle{Type: incremental.ConfigFile}); b.IsNotExist(err) {
		initLock, lockErr := lock.New(ctx, b, initLockKey(repo.Password), true)
		if lockErr != nil {
			return lockErr
		}
		defer func() { _ = initLock.Unlock(context.WithoutCancel(ctx), b) }()
	} else if err != nil {
		return err
	}
	if _, err := repository.Init(ctx, b, repo.Password); err != nil {
		return &incremental.RedactedError{Category: "repository", Message: "could not initialize the repository", Err: err}
	}
	return nil
}

// initLockKey derives the key sealing the initialization lock. Before the
// first Init there is no repository master key, so the lock is sealed with
// a password-derived key — the one secret every machine that may initialize
// the repository shares. The domain-separated derivation never produces a
// real master key, and the lock only protects non-secret metadata
// (hostname, PID, timestamps), so a fast hash suffices.
func initLockKey(password string) *crypto.MasterKey {
	encrypt := sha256.Sum256([]byte("bqckup init-lock encrypt" + password))
	mack := sha256.Sum256([]byte("bqckup init-lock mack" + password))
	macr := sha256.Sum256([]byte("bqckup init-lock macr" + password))
	return &crypto.MasterKey{
		Encrypt: encrypt,
		MACK:    [16]byte(mack[:16]),
		MACR:    [16]byte(macr[:16]),
	}
}

// BackupFiles walks the include paths and writes one snapshot.
func (e *Engine) BackupFiles(ctx context.Context, repo backupincremental.RepoConfig, spec backupincremental.BackupSpec) (backupincremental.SnapshotSummary, error) {
	if err := ctx.Err(); err != nil {
		return backupincremental.SnapshotSummary{}, err
	}
	if len(spec.Include) == 0 {
		return backupincremental.SnapshotSummary{}, errors.New("at least one include path is required for backup")
	}
	if err := e.rejectUnsupportedURL(repo.URL); err != nil {
		return backupincremental.SnapshotSummary{}, err
	}
	b, err := e.openBackend(ctx, repo)
	if err != nil {
		return backupincremental.SnapshotSummary{}, err
	}
	r, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		return backupincremental.SnapshotSummary{}, &incremental.RedactedError{Category: "repository", Message: "could not open the repository", Err: err}
	}
	// Exclusive lock; lock errors are returned unwrapped: their message
	// (hostname/pid, never secrets) must reach the user.
	_, release, err := acquireExclusiveLock(ctx, b, r.MasterKey())
	if err != nil {
		return backupincremental.SnapshotSummary{}, err
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
		return backupincremental.SnapshotSummary{}, &incremental.RedactedError{Category: "repository", Message: "could not create the incremental backup", Err: err}
	}
	return backupincremental.SnapshotSummary{
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
func (e *Engine) ApplyRetention(ctx context.Context, repo backupincremental.RepoConfig, keepLast int, siteName string) (int64, error) {
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
		return 0, &incremental.RedactedError{Category: "repository", Message: "could not open the repository", Err: err}
	}
	_, release, err := acquireExclusiveLock(ctx, b, r.MasterKey())
	if err != nil {
		return 0, err
	}
	defer release()
	result, err := r.ForgetAndPrune(ctx, keepLast, "site:"+siteName)
	if err != nil {
		return 0, &incremental.RedactedError{Category: "repository", Message: "could not apply snapshot retention", Err: err}
	}
	return result.BytesReclaimed, nil
}

// ListSnapshots lists the repository's snapshots under a non-exclusive
// lock (policy L4 reserves non-exclusive locks for listing). The lock is
// removed on every return path. Size comes from the snapshot summary.
func (e *Engine) ListSnapshots(ctx context.Context, repo backupincremental.RepoConfig) ([]backupincremental.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.rejectUnsupportedURL(repo.URL); err != nil {
		return nil, err
	}
	b, err := e.openBackend(ctx, repo)
	if err != nil {
		return nil, err
	}
	r, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		return nil, &incremental.RedactedError{Category: "repository", Message: "could not open the repository", Err: err}
	}
	listingLock, err := lock.New(ctx, b, r.MasterKey(), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = listingLock.Unlock(context.WithoutCancel(ctx), b) }()

	stored, err := r.ListSnapshots(ctx)
	if err != nil {
		return nil, &incremental.RedactedError{Category: "repository", Message: "could not list the repository snapshots", Err: err}
	}
	snapshots := make([]backupincremental.Snapshot, 0, len(stored))
	for _, entry := range stored {
		size := int64(0)
		if entry.Snapshot.Summary != nil {
			size = int64(entry.Snapshot.Summary.TotalBytesProcessed)
		}
		snapshots = append(snapshots, backupincremental.Snapshot{
			ID:        entry.ID.String(),
			Paths:     entry.Snapshot.Paths,
			Size:      size,
			CreatedAt: entry.Snapshot.Time,
			Tags:      entry.Snapshot.Tags,
		})
	}
	return snapshots, nil
}

// RestoreSnapshot restores one snapshot's configured paths into the
// target directory under a non-exclusive lock. The confirm callback is
// invoked once, before anything is written, with every conflicting path;
// its error aborts the restore.
func (e *Engine) RestoreSnapshot(ctx context.Context, repo backupincremental.RepoConfig, snapshotID string, paths []string, target string, confirm backupincremental.RestoreOverwrite) (backupincremental.RestoreSummary, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return backupincremental.RestoreSummary{}, err
	}
	if err := e.rejectUnsupportedURL(repo.URL); err != nil {
		return backupincremental.RestoreSummary{}, err
	}
	b, err := e.openBackend(ctx, repo)
	if err != nil {
		return backupincremental.RestoreSummary{}, err
	}
	r, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		return backupincremental.RestoreSummary{}, &incremental.RedactedError{Category: "repository", Message: "could not open the repository", Err: err}
	}
	restoreLock, err := lock.New(ctx, b, r.MasterKey(), false)
	if err != nil {
		return backupincremental.RestoreSummary{}, err
	}
	defer func() { _ = restoreLock.Unlock(context.WithoutCancel(ctx), b) }()

	id, err := incremental.ParseID(snapshotID)
	if err != nil {
		return backupincremental.RestoreSummary{}, err
	}
	entry, err := r.LoadSnapshot(ctx, id)
	if err != nil {
		return backupincremental.RestoreSummary{}, &incremental.RedactedError{Category: "repository", Message: "could not load the snapshot", Err: err}
	}
	summary, err := restorer.New(r).Restore(ctx, entry.Snapshot, paths, target, restorer.Overwrite(confirm))
	if err != nil {
		// The confirm callback's own error (with its apperror category)
		// stays reachable through the unwrap chain.
		return backupincremental.RestoreSummary{}, &incremental.RedactedError{Category: "repository", Message: "could not restore the snapshot", Err: err}
	}
	return backupincremental.RestoreSummary{
		SnapshotID:      snapshotID,
		Target:          target,
		FilesRestored:   summary.FilesRestored,
		BytesRestored:   summary.BytesRestored,
		SkippedPaths:    summary.SkippedPaths,
		DurationSeconds: time.Since(started).Seconds(),
	}, nil
}

// Unlock removes stale repository locks (restic unlock semantics: stale
// locks only, never a live one). Locks are encrypted with the repository
// key, so the repository must be opened first.
func (e *Engine) Unlock(ctx context.Context, repo backupincremental.RepoConfig) error {
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
		return &incremental.RedactedError{Category: "repository", Message: "could not open the repository to unlock it", Err: err}
	}
	if _, err := lock.RemoveStale(ctx, b, r.MasterKey()); err != nil {
		return &incremental.RedactedError{Category: "repository", Message: "could not unlock the repository", Err: err}
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
func (e *Engine) openBackend(ctx context.Context, repo backupincremental.RepoConfig) (backend.Backend, error) {
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
