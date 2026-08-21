package lock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBackend(t *testing.T) backend.Backend {
	t.Helper()
	return backend.NewLocal(t.TempDir())
}

func testKey(t *testing.T) *crypto.MasterKey {
	t.Helper()
	key, err := crypto.NewRandomMasterKey()
	require.NoError(t, err)
	return key
}

// writeLock plants a lock file directly with a fixed name and time.
func writeLock(t *testing.T, b backend.Backend, key *crypto.MasterKey, name string, l Lock) {
	t.Helper()
	if name == "" {
		name = strings.Repeat("f", 64)
	}
	l.handle = restic.Handle{Type: restic.LockFile, Name: name}
	require.NoError(t, saveFixture(t, b, key, &l))
}

func saveFixture(t *testing.T, b backend.Backend, key *crypto.MasterKey, l *Lock) error {
	t.Helper()
	blob, err := Seal(key, l)
	if err != nil {
		t.Fatal(err)
	}
	return b.Save(context.Background(), l.handle, strings.NewReader(string(blob)))
}

func listLockNames(t *testing.T, b backend.Backend) []string {
	t.Helper()
	var names []string
	err := b.List(context.Background(), restic.LockFile, func(h restic.Handle, _ int64) error {
		names = append(names, h.Name)
		return nil
	})
	require.NoError(t, err)
	return names
}

func TestLockJSONFormatMatchesRestic(t *testing.T) {
	doc, err := json.Marshal(Lock{
		Time:      time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		Exclusive: true,
		Hostname:  "backup-host",
		Username:  "operator",
		PID:       4242,
		UID:       1000,
		GID:       1000,
	})
	require.NoError(t, err)
	// restic's lock JSON: time, exclusive, hostname, username, pid, uid, gid.
	assert.JSONEq(t, `{
		"time": "2026-08-20T12:00:00Z",
		"exclusive": true,
		"hostname": "backup-host",
		"username": "operator",
		"pid": 4242,
		"uid": 1000,
		"gid": 1000
	}`, string(doc))

	var round Lock
	require.NoError(t, json.Unmarshal(doc, &round))
	assert.Equal(t, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), round.Time)
	assert.True(t, round.Exclusive)
	assert.Equal(t, "backup-host", round.Hostname)
	assert.Equal(t, "operator", round.Username)
	assert.Equal(t, 4242, round.PID)
	assert.Equal(t, uint32(1000), round.UID)
	assert.Equal(t, uint32(1000), round.GID)
}

func TestLockJSONOmitsZeroUIDGID(t *testing.T) {
	doc, err := json.Marshal(Lock{Time: time.Now(), PID: 1})
	require.NoError(t, err)
	assert.NotContains(t, string(doc), "uid")
	assert.NotContains(t, string(doc), "gid")
}

func TestStaleness(t *testing.T) {
	fresh := Lock{Time: time.Now()}
	assert.False(t, fresh.Stale())
	stale := Lock{Time: time.Now().Add(-StaleTimeout - time.Minute)}
	assert.True(t, stale.Stale())
	future := Lock{Time: time.Now().Add(time.Hour)}
	assert.False(t, future.Stale())
}

func TestLockBlobRoundTrip(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	l := &Lock{Time: time.Now(), Exclusive: true, Hostname: "h", Username: "u", PID: 1}
	blob, err := Seal(key, l)
	require.NoError(t, err)

	// the blob must be sealed: it cannot be parsed as JSON, and a wrong
	// key must not open it
	assert.NotContains(t, string(blob), `"exclusive"`)
	h := restic.Handle{Type: restic.LockFile, Name: restic.Hash(blob).String()}
	require.NoError(t, b.Save(context.Background(), h, strings.NewReader(string(blob))))
	_, invalid, err := loadLockTyped(context.Background(), b, testKey(t), h)
	require.Error(t, err)
	assert.True(t, invalid, "a wrong key must be classified as invalid data")

	// the name is the content hash, like restic's SaveUnpacked
	assert.Equal(t, restic.Hash(blob).String(), h.Name)

	// round-trip through the backend
	other, err := loadLock(context.Background(), b, key, h)
	require.NoError(t, err)
	assert.True(t, l.Time.Equal(other.Time))
	assert.True(t, other.Exclusive)
	assert.Equal(t, "h", other.Hostname)
}

func TestNewWritesContentAddressedLockFile(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	l, err := New(context.Background(), b, key, true)
	require.NoError(t, err)
	names := listLockNames(t, b)
	require.Len(t, names, 1)
	assert.Equal(t, 64, len(names[0]))
	assert.Equal(t, names[0], l.handle.Name)
	assert.True(t, l.Exclusive)
}

func TestExclusiveConflictsWithAnyLock(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	first, err := New(context.Background(), b, key, true)
	require.NoError(t, err)

	_, err = New(context.Background(), b, key, true)
	require.Error(t, err)
	var locked *ErrLocked
	require.ErrorAs(t, err, &locked)
	assert.Equal(t, first.Hostname, locked.Lock.Hostname)
	assert.Equal(t, first.PID, locked.Lock.PID)

	// the failed acquisition removed its own lock file
	require.Len(t, listLockNames(t, b), 1)
}

func TestNonExclusiveJoinsReadersButNotWriters(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	first, err := New(context.Background(), b, key, false)
	require.NoError(t, err)
	defer first.Unlock(context.Background(), b)

	second, err := New(context.Background(), b, key, false)
	require.NoError(t, err)
	defer second.Unlock(context.Background(), b)

	_, err = New(context.Background(), b, key, true)
	var locked *ErrLocked
	require.ErrorAs(t, err, &locked)
}

func TestStaleNonExclusiveLockIsAutoRemoved(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	writeLock(t, b, key, "", Lock{Time: time.Now().Add(-StaleTimeout - time.Minute), Exclusive: false, Hostname: "ghost"})

	l, err := New(context.Background(), b, key, true)
	require.NoError(t, err)
	defer l.Unlock(context.Background(), b)
	require.Len(t, listLockNames(t, b), 1, "stale non-exclusive lock must be removed")
}

func TestStaleExclusiveLockBlocksAndSurvives(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	writeLock(t, b, key, "", Lock{Time: time.Now().Add(-StaleTimeout - time.Minute), Exclusive: true, Hostname: "ghost", PID: 7})

	_, err := New(context.Background(), b, key, true)
	var stale *ErrStaleExclusive
	require.ErrorAs(t, err, &stale)
	assert.Contains(t, err.Error(), "unlock")

	// never removed automatically
	require.Len(t, listLockNames(t, b), 1)
}

func TestFutureLocksAreIgnored(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	writeLock(t, b, key, "", Lock{Time: time.Now().Add(time.Hour), Exclusive: true, Hostname: "clock-skewed"})

	l, err := New(context.Background(), b, key, true)
	require.NoError(t, err)
	defer l.Unlock(context.Background(), b)
}

func TestInvalidLockFileBlocks(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	require.NoError(t, b.Save(context.Background(), restic.Handle{Type: restic.LockFile, Name: strings.Repeat("e", 64)}, strings.NewReader("not a sealed lock")))

	_, err := New(context.Background(), b, key, true)
	var invalid *ErrInvalidLock
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, err.Error(), "unlock")
}

func TestUnlockAndRefresh(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	l, err := New(context.Background(), b, key, true)
	require.NoError(t, err)
	l.Time = time.Now().Add(-time.Hour)
	require.NoError(t, l.Refresh(context.Background(), b, key))
	require.False(t, l.Stale(), "refresh must renew the timestamp")
	// refresh moves to a new content-addressed name and drops the old one
	require.Len(t, listLockNames(t, b), 1)

	require.NoError(t, l.Unlock(context.Background(), b))
	require.Empty(t, listLockNames(t, b))
	// removing again is not an error
	require.NoError(t, l.Unlock(context.Background(), b))
}

// failRemoveBackend fails the next N Remove calls with an injected error.
type failRemoveBackend struct {
	backend.Backend
	failures int
}

func (b *failRemoveBackend) Remove(ctx context.Context, h restic.Handle) error {
	if b.failures > 0 {
		b.failures--
		return errors.New("injected remove failure")
	}
	return b.Backend.Remove(ctx, h)
}

// TestRefreshRemoveFailureDoesNotLeakLock: a refresh whose old-file removal
// fails must not leave a lock behind — Unlock removes every file this
// process created, and a fresh lock must be acquirable afterwards.
func TestRefreshRemoveFailureDoesNotLeakLock(t *testing.T) {
	b := &failRemoveBackend{Backend: testBackend(t)}
	key := testKey(t)
	l, err := New(context.Background(), b, key, true)
	require.NoError(t, err)

	b.failures = 1 // the old lock file survives the refresh
	require.Error(t, l.Refresh(context.Background(), b, key))

	require.NoError(t, l.Unlock(context.Background(), b))
	require.Empty(t, listLockNames(t, b), "every lock file created by this process must be removed")

	// the leaked file would block this acquisition
	other, err := New(context.Background(), b, key, true)
	require.NoError(t, err)
	require.NoError(t, other.Unlock(context.Background(), b))
}

// cancelAfterSaveBackend cancels the shared context after the next Save.
type cancelAfterSaveBackend struct {
	backend.Backend
	cancel context.CancelFunc
	armed  bool
}

func (b *cancelAfterSaveBackend) Save(ctx context.Context, h restic.Handle, rd io.Reader) error {
	if err := b.Backend.Save(ctx, h, rd); err != nil {
		return err
	}
	if b.armed {
		b.cancel()
	}
	return nil
}

// TestRefreshOldLockRemovalSurvivesCancellation: the old lock file must be
// removed even when the caller's context is cancelled right after the new
// lock was created (restic removes it with context.TODO() for the same
// reason); otherwise the old file is leaked with a fresh timestamp and
// blocks the next run.
func TestRefreshOldLockRemovalSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &cancelAfterSaveBackend{Backend: testBackend(t), cancel: cancel}
	key := testKey(t)
	l, err := New(ctx, b, key, true)
	require.NoError(t, err)

	b.armed = true // the refresh's Save cancels ctx; the old-file removal must still succeed
	require.NoError(t, l.Refresh(ctx, b, key))
	require.Len(t, listLockNames(t, b), 1, "old lock removed, only the refreshed one remains")

	require.NoError(t, l.Unlock(context.Background(), b))
	require.Empty(t, listLockNames(t, b))
}

func TestRemoveStaleRemovesOnlyStaleLocks(t *testing.T) {
	b := testBackend(t)
	key := testKey(t)
	writeLock(t, b, key, "aaaa", Lock{Time: time.Now().Add(-StaleTimeout - time.Minute), Exclusive: false, Hostname: "old"})
	writeLock(t, b, key, "bbbb", Lock{Time: time.Now(), Exclusive: true, Hostname: "fresh"})
	require.NoError(t, b.Save(context.Background(), restic.Handle{Type: restic.LockFile, Name: strings.Repeat("c", 64)}, strings.NewReader("garbage")))

	removed, err := RemoveStale(context.Background(), b, key)
	require.NoError(t, err)
	assert.Equal(t, 2, removed, "stale and invalid locks removed, live lock kept")
	assert.Equal(t, []string{"bbbb"}, listLockNames(t, b))
}

func TestNewHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(ctx, testBackend(t), testKey(t), true)
	require.ErrorIs(t, err, context.Canceled)
}

func TestLockErrorMessageHasNoSecrets(t *testing.T) {
	err := (&ErrLocked{Lock: Lock{Hostname: "host-a", Username: "alice", PID: 9, UID: 1000, GID: 1000}}).Error()
	assert.NotContains(t, err, "password")
	assert.Contains(t, err, "host-a")
	assert.Contains(t, err, "alice")
}
