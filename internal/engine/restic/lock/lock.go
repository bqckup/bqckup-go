// Package lock implements restic-compatible repository locks: the same
// lock files in locks/, the same 30-minute staleness rule, and the same
// list-based conflict detection (object stores have no compare-and-swap,
// so lock files are content-addressed blobs and conflicts are found by
// listing — exactly like restic).
//
// Lock file format (verified against restic internal/restic/lock.go and
// repository.go, versions 0.16 and 0.19): the Lock struct is marshalled
// to JSON, compressed as 0x02 || zstd(JSON), and sealed with the
// repository master key (nonce || ciphertext || MAC) — the same "unpacked"
// blob format restic's SaveUnpacked produces. The file name is the SHA-256
// of the sealed blob. Restic decrypts these files with the repo key, so
// locks and repos interoperate in both directions.
//
// Policy (L4 decisions, tasks/plan-l3-l4-l2.md D5): exclusive locks for
// backup and retention, non-exclusive for listing; stale non-exclusive
// locks are removed automatically; a stale exclusive lock is reported as
// ErrStaleExclusive instead of being silently dropped — the user runs
// `bqckup backup unlock` to remove it. (Restic itself auto-removes stale
// locks of any kind; the deviation is deliberate and documented in the
// L4 design note.)
package lock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
	"github.com/klauspost/compress/zstd"
)

// StaleTimeout matches restic's staleness window.
const StaleTimeout = 30 * time.Minute

// waitBeforeLockCheck matches restic: after creating our own lock, wait
// briefly so concurrent writers' files are visible before the re-check
// (important on eventually-consistent object stores).
const waitBeforeLockCheck = 200 * time.Millisecond

// maxLockSize bounds lock file reads (locks are a few hundred bytes).
const maxLockSize = 1 << 20

// Lock is the restic lock file format.
type Lock struct {
	Time      time.Time `json:"time"`
	Exclusive bool      `json:"exclusive"`
	Hostname  string    `json:"hostname"`
	Username  string    `json:"username"`
	PID       int       `json:"pid"`
	UID       uint32    `json:"uid,omitempty"`
	GID       uint32    `json:"gid,omitempty"`

	handle restic.Handle // own lock file, set by New/Refresh

	// handles are every lock file this Lock ever created, in order. A
	// refresh whose old-file removal failed leaves a file behind; Unlock
	// removes them all so one failed refresh cannot leak a lock that
	// blocks the repository for every later run. Accessed by New/Refresh/
	// Unlock only; the facade's refresh goroutine and release() are
	// synchronized with a done channel, so no locking is needed here.
	handles []restic.Handle
}

// ErrLocked reports a conflicting, non-stale lock held by another process.
type ErrLocked struct{ Lock Lock }

func (e *ErrLocked) Error() string {
	return fmt.Sprintf("repository is already locked %sby %s (%s, UID %d, GID %d) PID %d since %s",
		exclusiveWord(e.Lock.Exclusive), e.Lock.Hostname, e.Lock.Username, e.Lock.UID, e.Lock.GID,
		e.Lock.PID, e.Lock.Time.UTC().Format(time.RFC3339))
}

// ErrStaleExclusive reports a stale exclusive lock. It is never removed
// automatically (unlike restic, which drops stale locks of any type): the
// user must run `bqckup backup unlock` to remove it.
type ErrStaleExclusive struct{ Lock Lock }

func (e *ErrStaleExclusive) Error() string {
	return fmt.Sprintf("repository is locked by a stale exclusive lock (hostname %s, PID %d, since %s); run 'bqckup backup unlock <site>' to remove it",
		e.Lock.Hostname, e.Lock.PID, e.Lock.Time.UTC().Format(time.RFC3339))
}

// ErrInvalidLock reports a lock file that cannot be decrypted or parsed.
// It blocks operations because it might be a real lock; `bqckup backup
// unlock` removes it (like restic's `unlock --remove-all` for invalid
// files, but scoped to unreadable locks only).
type ErrInvalidLock struct{ Handle restic.Handle }

func (e *ErrInvalidLock) Error() string {
	return fmt.Sprintf("invalid lock file %q blocks the repository; run 'bqckup backup unlock <site>' to remove it", e.Handle.Name)
}

func exclusiveWord(exclusive bool) string {
	if exclusive {
		return "exclusively "
	}
	return ""
}

// Stale reports whether the lock is older than restic's staleness window.
func (l *Lock) Stale() bool { return time.Since(l.Time) > StaleTimeout }

// New acquires a lock. Like restic: check existing locks first, then
// create our own (content-addressed name), wait briefly, then check again
// so two concurrent writers cannot both pass the check. On conflict the
// own lock file is removed again before returning the error.
func New(ctx context.Context, b backend.Backend, key *crypto.MasterKey, exclusive bool) (*Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	username, hostname := repository.CurrentIdentity()
	l := &Lock{
		Time:      time.Now(),
		Exclusive: exclusive,
		Hostname:  hostname,
		Username:  username,
		PID:       os.Getpid(),
		UID:       uint32(os.Getuid()),
		GID:       uint32(os.Getgid()),
	}
	if err := l.checkOthers(ctx, b, key, restic.Handle{}); err != nil {
		return nil, err
	}
	if err := l.create(ctx, b, key); err != nil {
		return nil, err
	}
	time.Sleep(waitBeforeLockCheck)
	if err := l.checkOthers(ctx, b, key, l.handle); err != nil {
		_ = b.Remove(context.WithoutCancel(ctx), l.handle)
		return nil, err
	}
	return l, nil
}

// create seals the lock JSON and stores it under its content hash.
func (l *Lock) create(ctx context.Context, b backend.Backend, key *crypto.MasterKey) error {
	blob, err := Seal(key, l)
	if err != nil {
		return err
	}
	h := restic.Handle{Type: restic.LockFile, Name: restic.Hash(blob).String()}
	l.handle = h
	l.handles = append(l.handles, h)
	return b.Save(ctx, h, bytes.NewReader(blob))
}

// checkOthers looks for locks that would prevent this lock: any lock
// conflicts with an exclusive request, an exclusive lock conflicts with
// any request. Stale non-exclusive locks are removed; stale exclusive
// locks block with ErrStaleExclusive; unreadable lock files block with
// ErrInvalidLock. Locks with a timestamp in the future are ignored.
// Transient load errors are retried (restic behavior).
func (l *Lock) checkOthers(ctx context.Context, b backend.Backend, key *crypto.MasterKey, own restic.Handle) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = b.List(ctx, restic.LockFile, func(h restic.Handle, _ int64) error {
			if h.Name == own.Name {
				return nil // own lock
			}
			other, invalid, loadErr := loadLockTyped(ctx, b, key, h)
			if loadErr != nil {
				if invalid {
					return &ErrInvalidLock{Handle: h}
				}
				return loadErr // unclear whether it can be ignored: retry, then fail
			}
			if other.Time.After(time.Now()) {
				return nil // ignore future locks
			}
			if !other.Exclusive && !l.Exclusive {
				return nil // readers may join
			}
			if other.Stale() {
				if other.Exclusive {
					return &ErrStaleExclusive{Lock: *other}
				}
				return b.Remove(ctx, h) // stale non-exclusive: auto-remove
			}
			return &ErrLocked{Lock: *other}
		})
		if err == nil {
			return nil
		}
		var locked *ErrLocked
		var stale *ErrStaleExclusive
		var invalid *ErrInvalidLock
		if errors.As(err, &locked) || errors.As(err, &stale) || errors.As(err, &invalid) {
			return err // permanent conflicts
		}
	}
	return err
}

// Refresh renews the lock timestamp so a long operation never looks stale
// (restic renews its locks every ~5 minutes too). The refreshed lock gets
// a new file name (the blob changed); the old file is removed.
func (l *Lock) Refresh(ctx context.Context, b backend.Backend, key *crypto.MasterKey) error {
	old := l.handle
	l.Time = time.Now()
	if err := l.create(ctx, b, key); err != nil {
		return err
	}
	if old.Name != "" {
		// Remove the old file with a context that survives cancellation
		// (restic's Refresh uses context.TODO() for the same reason): if
		// the caller's ctx dies here, the old lock file would be left
		// behind with a fresh timestamp and block every later run.
		return b.Remove(context.WithoutCancel(ctx), old)
	}
	return nil
}

// Unlock removes every lock file this process created. A missing file is
// not an error. Removing all handles (not just the current one) cleans up
// files a failed refresh left behind.
func (l *Lock) Unlock(ctx context.Context, b backend.Backend) error {
	var firstErr error
	for _, h := range l.handles {
		if err := b.Remove(ctx, h); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RemoveStale removes every stale or invalid lock file and returns how
// many were removed — the `bqckup backup unlock` / `restic unlock`
// semantics. Transient load errors are skipped, never deleted.
func RemoveStale(ctx context.Context, b backend.Backend, key *crypto.MasterKey) (int, error) {
	removed := 0
	err := b.List(ctx, restic.LockFile, func(h restic.Handle, _ int64) error {
		other, invalid, loadErr := loadLockTyped(ctx, b, key, h)
		if loadErr != nil && !invalid {
			return nil // transient (network): leave the lock alone
		}
		if invalid || other.Stale() {
			if err := b.Remove(ctx, h); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}

// Seal produces the restic unpacked blob restic's SaveUnpacked writes for
// lock files: 0x02 || zstd(JSON), sealed with the master key.
func Seal(key *crypto.MasterKey, l *Lock) ([]byte, error) {
	doc, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer encoder.Close()
	return key.Seal(nil, encoder.EncodeAll(doc, []byte{2}))
}

// loadLock decrypts and parses a lock blob. Errors are categorized:
// invalid = the blob cannot be decrypted or parsed (real corruption);
// otherwise the error is transient (backend read failure).
func loadLock(ctx context.Context, b backend.Backend, key *crypto.MasterKey, h restic.Handle) (*Lock, error) {
	l, _, err := loadLockTyped(ctx, b, key, h)
	return l, err
}

func loadLockTyped(ctx context.Context, b backend.Backend, key *crypto.MasterKey, h restic.Handle) (*Lock, bool, error) {
	var raw []byte
	err := b.Load(ctx, h, 0, 0, func(rd io.Reader) error {
		var readErr error
		raw, readErr = io.ReadAll(io.LimitReader(rd, maxLockSize))
		return readErr
	})
	if err != nil {
		return nil, false, err
	}
	plain, err := key.Open(nil, raw)
	if err != nil {
		return nil, true, fmt.Errorf("lock file %s: %w", h.Name, err)
	}
	payload := plain
	if len(payload) > 0 && payload[0] == 2 {
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, true, err
		}
		defer decoder.Close()
		payload, err = decoder.DecodeAll(payload[1:], nil)
		if err != nil {
			return nil, true, fmt.Errorf("lock file %s: %w", h.Name, err)
		}
	}
	var l Lock
	if err := json.Unmarshal(payload, &l); err != nil {
		return nil, true, fmt.Errorf("lock file %s: %w", h.Name, err)
	}
	return &l, false, nil
}
