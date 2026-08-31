package history

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryRecordsRunLifecycleAndPackages(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	started := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)
	run := &BackupRun{SiteName: "example", Status: StatusRunning, StartedAt: started, Forced: true}

	require.NoError(t, repo.CreateRun(ctx, run))
	require.NotEmpty(t, run.ID)
	require.NoError(t, repo.CreatePackage(ctx, &Package{
		RunID: run.ID, SourceKind: "files", SourceName: "files", Destination: "local-primary",
		ObjectKey: "bqckup/example/set/files.tar.gz", Size: 42, SHA256: "abc", Status: PackageStored,
	}))
	finished := started.Add(2 * time.Second)
	require.NoError(t, repo.FinishRun(ctx, run.ID, StatusSuccess, finished, "", ""))

	last, err := repo.LastSuccessful(ctx, "example", time.Time{})
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Equal(t, StatusSuccess, last.Status)
	assert.EqualValues(t, 2000, last.DurationMillis)

	runs, err := repo.ListRuns(ctx, RunFilter{Site: "example", Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Len(t, runs[0].Packages, 1)
	assert.Equal(t, "abc", runs[0].Packages[0].SHA256)
}

func TestReportDeliveryDeduplicatesByTypeAndPeriod(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	when := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)

	require.NoError(t, repo.RecordDelivery(ctx, "daily", "2026-08-01", when))
	require.NoError(t, repo.RecordDelivery(ctx, "daily", "2026-08-01", when.Add(5*time.Minute)))

	delivered, err := repo.ReportDelivered(ctx, "daily", "2026-08-01")
	require.NoError(t, err)
	assert.True(t, delivered)

	var count int64
	require.NoError(t, repo.db.WithContext(ctx).Model(&ReportDelivery{}).
		Where("report_type = ? AND period = ?", "daily", "2026-08-01").
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestLastSuccessfulReturnsNilWhenMissing(t *testing.T) {
	repo := testRepository(t)
	run, err := repo.LastSuccessful(context.Background(), "missing", time.Time{})
	require.NoError(t, err)
	assert.Nil(t, run)
}

func TestLastSuccessfulMatchesSuccessAndNoChange(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	t1 := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	// Run 1: success at 10:00
	require.NoError(t, repo.CreateRun(ctx, &BackupRun{SiteName: "example", Status: StatusSuccess, StartedAt: t1}))
	// Run 2: no_change at 11:00
	require.NoError(t, repo.CreateRun(ctx, &BackupRun{SiteName: "example", Status: StatusNoChange, StartedAt: t2}))
	// Run 3: failed at 12:00
	require.NoError(t, repo.CreateRun(ctx, &BackupRun{SiteName: "example", Status: StatusFailed, StartedAt: t3}))

	// Without cutoff, returns the most recent healthy run (the no_change run at 11:00)
	last, err := repo.LastSuccessful(ctx, "example", time.Time{})
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Equal(t, StatusNoChange, last.Status)
	assert.True(t, last.StartedAt.Equal(t2))

	// With cutoff before 11:00 (e.g. at 11:00), excludes 11:00 and returns the success run at 10:00
	lastBefore11, err := repo.LastSuccessful(ctx, "example", t2)
	require.NoError(t, err)
	require.NotNil(t, lastBefore11)
	assert.Equal(t, StatusSuccess, lastBefore11.Status)
	assert.True(t, lastBefore11.StartedAt.Equal(t1))

	// With cutoff before 10:00, returns nil
	lastBefore10, err := repo.LastSuccessful(ctx, "example", t1)
	require.NoError(t, err)
	assert.Nil(t, lastBefore10)
}

func TestRunPackagesReturnsOnlyStoredPackages(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	run := &BackupRun{SiteName: "example", Status: StatusRunning, StartedAt: time.Now()}
	require.NoError(t, repo.CreateRun(ctx, run))

	stored := []Package{
		{RunID: run.ID, SourceKind: "files", SourceName: "files", Destination: "local-primary", ObjectKey: "k1", Size: 10, SHA256: "a", Status: PackageStored},
		{RunID: run.ID, SourceKind: "files", SourceName: "files", Destination: "s3-primary", ObjectKey: "k2", Size: 10, SHA256: "b", Status: PackageStored},
		{RunID: run.ID, SourceKind: "database", SourceName: "app", Destination: "local-primary", ObjectKey: "k3", Size: 20, SHA256: "c", Status: PackageStored},
	}
	for _, pkg := range stored {
		require.NoError(t, repo.CreatePackage(ctx, &pkg))
	}
	// A failed package of the same run must not be returned.
	require.NoError(t, repo.CreatePackage(ctx, &Package{
		RunID: run.ID, SourceKind: "database", SourceName: "broken", Destination: "local-primary",
		ObjectKey: "k4", Size: 99, SHA256: "d", Status: PackageFailed,
	}))
	// An package of another run must not be returned.
	other := &BackupRun{SiteName: "example", Status: StatusRunning, StartedAt: time.Now()}
	require.NoError(t, repo.CreateRun(ctx, other))
	require.NoError(t, repo.CreatePackage(ctx, &Package{
		RunID: other.ID, SourceKind: "files", SourceName: "files", Destination: "local-primary",
		ObjectKey: "k5", Size: 99, SHA256: "e", Status: PackageStored,
	}))

	packages, err := repo.RunPackages(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, packages, 3)
	for _, pkg := range packages {
		assert.Equal(t, run.ID, pkg.RunID)
		assert.Equal(t, PackageStored, pkg.Status)
	}
}

func TestConsecutiveWithoutSuccess(t *testing.T) {
	ctx := context.Background()

	t.Run("empty history returns 0", func(t *testing.T) {
		repo := testRepository(t)
		streak, err := repo.ConsecutiveWithoutSuccess(ctx, "site1", time.Now())
		require.NoError(t, err)
		assert.Equal(t, 0, streak)
	})

	t.Run("counts failed cancelled and running rows with current row included", func(t *testing.T) {
		repo := testRepository(t)
		anchor := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

		// Create runs in reverse chronological order:
		// 1. Current run (anchor): failed
		// 2. 2h ago: cancelled
		// 3. 4h ago: running
		// 4. 6h ago: failed
		runs := []struct {
			started time.Time
			status  RunStatus
		}{
			{anchor.Add(-6 * time.Hour), StatusFailed},
			{anchor.Add(-4 * time.Hour), StatusRunning},
			{anchor.Add(-2 * time.Hour), StatusCancelled},
			{anchor, StatusFailed},
		}
		for _, r := range runs {
			require.NoError(t, repo.CreateRun(ctx, &BackupRun{
				SiteName:  "example",
				Status:    r.status,
				StartedAt: r.started,
			}))
		}

		streak, err := repo.ConsecutiveWithoutSuccess(ctx, "example", anchor)
		require.NoError(t, err)
		assert.Equal(t, 4, streak)
	})

	t.Run("stops at first success row and does not count it", func(t *testing.T) {
		repo := testRepository(t)
		anchor := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

		// 1. Older failed run (6h ago) - should not be counted
		// 2. Success run (4h ago) - break
		// 3. Cancelled run (2h ago)
		// 4. Current failed run (anchor)
		runs := []struct {
			started time.Time
			status  RunStatus
		}{
			{anchor.Add(-6 * time.Hour), StatusFailed},
			{anchor.Add(-4 * time.Hour), StatusSuccess},
			{anchor.Add(-2 * time.Hour), StatusCancelled},
			{anchor, StatusFailed},
		}
		for _, r := range runs {
			require.NoError(t, repo.CreateRun(ctx, &BackupRun{
				SiteName:  "example",
				Status:    r.status,
				StartedAt: r.started,
			}))
		}

		streak, err := repo.ConsecutiveWithoutSuccess(ctx, "example", anchor)
		require.NoError(t, err)
		assert.Equal(t, 2, streak)
	})

	t.Run("stops at first no_change row and does not count it", func(t *testing.T) {
		repo := testRepository(t)
		anchor := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

		// 1. Older failed run (6h ago) - should not be counted
		// 2. No_change run (4h ago) - break
		// 3. Cancelled run (2h ago)
		// 4. Current failed run (anchor)
		runs := []struct {
			started time.Time
			status  RunStatus
		}{
			{anchor.Add(-6 * time.Hour), StatusFailed},
			{anchor.Add(-4 * time.Hour), StatusNoChange},
			{anchor.Add(-2 * time.Hour), StatusCancelled},
			{anchor, StatusFailed},
		}
		for _, r := range runs {
			require.NoError(t, repo.CreateRun(ctx, &BackupRun{
				SiteName:  "example",
				Status:    r.status,
				StartedAt: r.started,
			}))
		}

		streak, err := repo.ConsecutiveWithoutSuccess(ctx, "example", anchor)
		require.NoError(t, err)
		assert.Equal(t, 2, streak)
	})

	t.Run("stops when gap between consecutive rows exceeds 24h", func(t *testing.T) {
		repo := testRepository(t)
		anchor := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

		// Single run at anchor, previous run 25h earlier -> gap > 24h
		require.NoError(t, repo.CreateRun(ctx, &BackupRun{
			SiteName:  "example",
			Status:    StatusFailed,
			StartedAt: anchor.Add(-25 * time.Hour),
		}))
		require.NoError(t, repo.CreateRun(ctx, &BackupRun{
			SiteName:  "example",
			Status:    StatusFailed,
			StartedAt: anchor,
		}))

		streak, err := repo.ConsecutiveWithoutSuccess(ctx, "example", anchor)
		require.NoError(t, err)
		assert.Equal(t, 1, streak)
	})

	t.Run("excludes rows older than startedAt minus 24h", func(t *testing.T) {
		repo := testRepository(t)
		anchor := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

		// Run 30h ago (outside query window)
		require.NoError(t, repo.CreateRun(ctx, &BackupRun{
			SiteName:  "example",
			Status:    StatusFailed,
			StartedAt: anchor.Add(-30 * time.Hour),
		}))
		// Run at anchor
		require.NoError(t, repo.CreateRun(ctx, &BackupRun{
			SiteName:  "example",
			Status:    StatusFailed,
			StartedAt: anchor,
		}))

		streak, err := repo.ConsecutiveWithoutSuccess(ctx, "example", anchor)
		require.NoError(t, err)
		assert.Equal(t, 1, streak)
	})
}

func testRepository(t *testing.T) *Repository {
	t.Helper()
	db, closeDB, err := Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB()) })
	require.NoError(t, Migrate(context.Background(), db))
	return NewRepository(db)
}
