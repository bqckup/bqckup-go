package facade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptertypes "github.com/bqckup/bqckup-go/internal/backup/restic"
)

const testPassword = "facade-test-password"

func testRepo(t *testing.T) adaptertypes.RepoConfig {
	t.Helper()
	return adaptertypes.RepoConfig{URL: t.TempDir(), Password: testPassword}
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
	summary, err := engine.BackupFiles(ctx, repo, adaptertypes.BackupSpec{
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

	snapshots, err := engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := engine.BackupFiles(ctx, repo, adaptertypes.BackupSpec{Include: []string{source}, Tags: []string{"t"}}); err != nil {
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
		if _, err := engine.BackupFiles(ctx, repo, adaptertypes.BackupSpec{
			Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.ApplyRetention(ctx, repo, 2, "testsite"); err != nil {
		t.Fatal(err)
	}
	snapshots, err := engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := engine.BackupFiles(ctx, repo, adaptertypes.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:othersite"},
	}); err != nil {
		t.Fatal(err)
	}
	// retention for THIS site must not touch the foreign snapshot
	if err := engine.ApplyRetention(ctx, repo, 1, "testsite"); err != nil {
		t.Fatal(err)
	}
	snapshots, err := engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("foreign snapshot was deleted: %d remain", len(snapshots))
	}
}

func TestRejectsRemoteURLs(t *testing.T) {
	engine := NewEngine()
	repo := adaptertypes.RepoConfig{URL: "s3:https://secret-key:secret@example.com/bucket/path", Password: "x"}
	err := engine.EnsureRepository(context.Background(), repo)
	if err == nil {
		t.Fatal("want error for s3 URL")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("error leaks URL credentials")
	}
}

func TestUnlockIsNoOp(t *testing.T) {
	engine := NewEngine()
	if err := engine.Unlock(context.Background(), adaptertypes.RepoConfig{}); err != nil {
		t.Fatalf("Unlock must be a no-op, got %v", err)
	}
}

// compile-time check: the facade satisfies the runner's interface.
var _ interface {
	EnsureRepository(context.Context, adaptertypes.RepoConfig) error
	BackupFiles(context.Context, adaptertypes.RepoConfig, adaptertypes.BackupSpec) (adaptertypes.SnapshotSummary, error)
	ApplyRetention(context.Context, adaptertypes.RepoConfig, int, string) error
	Unlock(context.Context, adaptertypes.RepoConfig) error
} = (*Engine)(nil)
