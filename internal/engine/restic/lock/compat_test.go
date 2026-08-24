//go:build restic_compat

// Package lock_test verifies the lock files interoperate with the official
// restic binary in both directions:
//   - restic blocks on our exclusive lock (restic check fails "already locked")
//   - we block on a lock restic itself holds (restic backup --stdin holds an
//     append/non-exclusive lock while stdin stays open; our exclusive lock
//     must conflict with it)
//
// Note: restic >= 0.17 lets backups run concurrently (append locks);
// bqckup deliberately keeps backup exclusive (plan D5) — strictly safer.
//
// Locks are stored encrypted with the repository master key, so these
// tests need restic >= 0.17.0 (format v2) — the same gate as the engine
// compat suite — and a repository the engine can open.
//
// Run: go test -race -tags=restic_compat ./internal/engine/restic/lock/...
package lock_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/lock"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
	"github.com/stretchr/testify/require"
)

const compatPassword = "compat-test-password"

func resticBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic binary not found in PATH (install restic >= 0.17.0 to run compat tests)")
	}
	return path
}

// resticVersionOK skips below 0.17.0 (format v1 repos the engine cannot open).
func resticVersionOK(t *testing.T) {
	t.Helper()
	out, err := exec.Command(resticBinary(t), "version").Output()
	if err != nil {
		t.Fatalf("restic version failed: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		t.Fatalf("unexpected restic version output: %q", out)
	}
	parts := strings.SplitN(fields[1], ".", 3)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		t.Fatalf("unexpected restic version %q", fields[1])
	}
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if major < 0 || (major == 0 && minor < 17) {
		t.Skipf("restic %s is repository format v1 only; need >= 0.17.0", fields[1])
	}
}

func runRestic(t *testing.T, repo string, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{"--repo", repo, "--no-cache"}, args...)
	command := exec.Command(resticBinary(t), fullArgs...)
	command.Env = append(os.Environ(), "RESTIC_PASSWORD="+compatPassword)
	output, err := command.CombinedOutput()
	return string(output), err
}

// openRepo opens a restic-made repo with the engine and returns the master
// key (lock blobs are encrypted with it).
func openRepo(t *testing.T, repoDir string) *repository.Repository {
	t.Helper()
	r, err := repository.Open(context.Background(), backend.NewLocal(repoDir), compatPassword)
	require.NoError(t, err)
	return r
}

func TestResticBlocksOnOurExclusiveLock(t *testing.T) {
	resticVersionOK(t)
	repoDir := t.TempDir()
	_, err := runRestic(t, repoDir, "init")
	require.NoError(t, err)

	b := backend.NewLocal(repoDir)
	key := openRepo(t, repoDir).MasterKey()
	holder, err := lock.New(context.Background(), b, key, true)
	require.NoError(t, err)
	defer holder.Unlock(context.Background(), b)

	// the official binary must refuse to work while our lock is held
	output, err := runRestic(t, repoDir, "check")
	require.Error(t, err)
	require.Contains(t, output, "already locked")

	// after unlocking, restic works again
	require.NoError(t, holder.Unlock(context.Background(), b))
	_, err = runRestic(t, repoDir, "check")
	require.NoError(t, err)
}

// TestWeBlockOnResticLock verifies both directions of the lock conflict
// with a running official backup. Restic >= 0.17 holds an APPEND
// (non-exclusive) lock while backing up, so: our exclusive lock must
// conflict with it, while a non-exclusive reader may join.
func TestWeBlockOnResticExclusiveLock(t *testing.T) {
	resticVersionOK(t)
	repoDir := t.TempDir()
	_, err := runRestic(t, repoDir, "init")
	require.NoError(t, err)

	// restic backup --stdin holds an exclusive lock while stdin stays open
	command := exec.Command(resticBinary(t), "--repo", repoDir, "--no-cache", "backup", "--stdin", "--stdin-filename", "backup.tar")
	command.Env = append(os.Environ(), "RESTIC_PASSWORD="+compatPassword)
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	defer func() {
		_ = stdin.Close()
		_ = command.Wait()
	}()

	// wait for restic to write its lock file
	deadline := time.Now().Add(20 * time.Second)
	for {
		entries, readErr := os.ReadDir(filepath.Join(repoDir, "locks"))
		if readErr == nil && len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restic did not create a lock file in time")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// our engine must refuse an exclusive lock while restic is running
	b := backend.NewLocal(repoDir)
	key := openRepo(t, repoDir).MasterKey()
	_, err = lock.New(context.Background(), b, key, true)
	var locked *lock.ErrLocked
	require.ErrorAs(t, err, &locked)
	require.Contains(t, err.Error(), "already locked")

	// readers may join a running backup (restic >= 0.17 append locks)
	reader, err := lock.New(context.Background(), b, key, false)
	require.NoError(t, err)
	require.NoError(t, reader.Unlock(context.Background(), b))
}
