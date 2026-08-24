package facade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backuprestic "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/lock"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
)

const testPassword = "facade-test-password"

func testRepo(t *testing.T) backuprestic.RepoConfig {
	t.Helper()
	return backuprestic.RepoConfig{URL: t.TempDir(), Password: testPassword}
}

// listSnapshots opens the repository below the facade and lists snapshots.
func listSnapshots(t *testing.T, repo backuprestic.RepoConfig) []repository.SnapshotWithID {
	t.Helper()
	r, err := repository.Open(context.Background(), backend.NewLocal(repo.URL), repo.Password)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshots
}

func TestEnsureAndBackupAndList(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)

	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	// idempotent
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("facade data"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
		SiteName: "testsite",
		Include:  []string{source},
		Tags:     []string{"bqckup", "site:testsite"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.SnapshotID == "" || summary.TotalFilesProcessed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	snapshots := listSnapshots(t, repo)
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
}

func TestBackupNeedsNoResticBinary(t *testing.T) {
	// The facade path spawns no processes: a full run must work with a PATH
	// that contains only a directory without restic.
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", t.TempDir()) // empty PATH: no binaries at all
	defer os.Setenv("PATH", oldPath)

	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{Include: []string{source}, Tags: []string{"t"}}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRetentionKeepsNewest(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// three backups: newest must survive, oldest two are deleted (keep 2
	// only deletes 1; use keep 1 to delete 2) — use keepLast=2, delete 1
	source := t.TempDir()
	for i := 0; i < 3; i++ {
		file := filepath.Join(source, "f.txt")
		if err := os.WriteFile(file, []byte(strings.Repeat("x", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
			Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.ApplyRetention(ctx, repo, 2, "testsite"); err != nil {
		t.Fatal(err)
	}
	snapshots := listSnapshots(t, repo)
	if len(snapshots) != 2 {
		t.Fatalf("after retention: %d snapshots, want 2", len(snapshots))
	}
}

func TestApplyRetentionIgnoresOtherSites(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// one snapshot for another site
	if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:othersite"},
	}); err != nil {
		t.Fatal(err)
	}
	// retention for THIS site must not touch the foreign snapshot
	if _, err := engine.ApplyRetention(ctx, repo, 1, "testsite"); err != nil {
		t.Fatal(err)
	}
	snapshots := listSnapshots(t, repo)
	if len(snapshots) != 1 {
		t.Fatalf("foreign snapshot was deleted: %d remain", len(snapshots))
	}
}

func TestRejectsRemoteURLs(t *testing.T) {
	engine := NewEngine()
	repo := backuprestic.RepoConfig{URL: "rest:https://user:secret-key@example.com/repo", Password: "x"}
	err := engine.EnsureRepository(context.Background(), repo)
	if err == nil {
		t.Fatal("want error for rest URL")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("error leaks URL credentials")
	}
}

func TestUnlockSucceedsOnCleanRepository(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := engine.Unlock(ctx, repo); err != nil {
		t.Fatalf("Unlock on a repository without locks must succeed, got %v", err)
	}
}

// compile-time check: the facade satisfies the runner's interface.
var _ interface {
	EnsureRepository(context.Context, backuprestic.RepoConfig) error
	BackupFiles(context.Context, backuprestic.RepoConfig, backuprestic.BackupSpec) (backuprestic.SnapshotSummary, error)
	ApplyRetention(context.Context, backuprestic.RepoConfig, int, string) (int64, error)
	Unlock(context.Context, backuprestic.RepoConfig) error
} = (*Engine)(nil)

func TestListSnapshotsListsRepositorySnapshotsAndReleasesLock(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// Empty repository: empty slice, not an error.
	snapshots, err := engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatalf("ListSnapshots on empty repository: %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("empty repository returned %d snapshots", len(snapshots))
	}

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("snapshot data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
	}); err != nil {
		t.Fatal(err)
	}

	snapshots, err = engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
	listed := snapshots[0]
	if len(listed.ID) != 64 {
		t.Fatalf("snapshot ID %q is not a full hash", listed.ID)
	}
	if len(listed.Paths) != 1 || listed.Paths[0] != source {
		t.Fatalf("paths = %v, want [%s]", listed.Paths, source)
	}
	if listed.Size <= 0 {
		t.Fatalf("size = %d, want > 0", listed.Size)
	}
	if time.Since(listed.CreatedAt) > time.Minute {
		t.Fatalf("created at %v is not recent", listed.CreatedAt)
	}

	// The non-exclusive lock must be gone after listing.
	b := backend.NewLocal(repo.URL)
	var locks []restic.Handle
	if err := b.List(ctx, restic.LockFile, func(handle restic.Handle, _ int64) error {
		locks = append(locks, handle)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(locks) != 0 {
		t.Fatalf("listing left %d lock files behind", len(locks))
	}
}

func TestListSnapshotsTakesANonExclusiveLockDuringListing(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	b := backend.NewLocal(repo.URL)

	// Hold a non-exclusive lock, then list: listing must not conflict.
	opened, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := lock.New(ctx, b, opened.MasterKey(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ListSnapshots(ctx, repo); err != nil {
		t.Fatalf("listing alongside a non-exclusive lock: %v", err)
	}
	if err := existing.Unlock(ctx, b); err != nil {
		t.Fatal(err)
	}
}
