package report

import (
	"context"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHistoryRepository struct {
	runs []history.BackupRun
}

func (f fakeHistoryRepository) ListRunsInRange(_ context.Context, from, to time.Time) ([]history.BackupRun, error) {
	var out []history.BackupRun
	for _, run := range f.runs {
		if !run.StartedAt.Before(from) && run.StartedAt.Before(to) {
			out = append(out, run)
		}
	}
	return out, nil
}

func TestBuildMonthlyReportDefinesExplicitDailyAndDestinationContract(t *testing.T) {
	tz := time.FixedZone("WITA", 8*60*60)
	start := func(day int, hour int) time.Time {
		return time.Date(2026, 8, day, hour, 0, 0, 0, tz)
	}
	repo := fakeHistoryRepository{runs: []history.BackupRun{
		{
			ID:             "run-1",
			SiteName:       "site-a",
			Status:         history.StatusSuccess,
			StartedAt:      start(1, 1),
			DurationMillis: 90_000,
			Packages: []history.Package{
				{RunID: "run-1", SourceKind: "files", SourceName: "files", Destination: "local", Size: 100, Status: history.PackageStored},
				{RunID: "run-1", SourceKind: "files", SourceName: "files", Destination: "s3", Size: 100, Status: history.PackageStored},
				{RunID: "run-1", SourceKind: "database", SourceName: "mysql", Destination: "s3", Size: 50, Status: history.PackageFailed},
			},
		},
		{
			ID:             "run-2",
			SiteName:       "site-a",
			Status:         history.StatusSkipped,
			StartedAt:      start(3, 2),
			DurationMillis: 10_000,
		},
		{
			ID:             "run-3",
			SiteName:       "site-b",
			Status:         history.StatusNoChange,
			StartedAt:      start(3, 3),
			DurationMillis: 20_000,
			Packages: []history.Package{
				{RunID: "run-3", SourceKind: "files", SourceName: "files", Destination: "local", Size: 25, Status: history.PackageStored},
			},
		},
	}}

	report, err := NewBuilder(repo).BuildMonthlyReport(context.Background(), start(15, 12), tz, true)
	require.NoError(t, err)

	require.Len(t, report.Days, 31)
	assert.Equal(t, "2026-08-01", report.Days[0].Date.Format("2006-01-02"))
	assert.True(t, report.Days[0].HasRuns)
	assert.False(t, report.Days[1].HasRuns)
	assert.Empty(t, report.Days[1].Sites)
	assert.Equal(t, 1, report.Days[0].Overall.TotalRuns)
	assert.Equal(t, int64(90), report.Days[0].Overall.DurationSeconds)
	assert.Equal(t, int64(100), report.Days[0].Overall.TotalBytes, "same source stored to multiple destinations counts once logically")

	assert.Equal(t, 3, report.Overall.TotalRuns)
	assert.Equal(t, 1, report.Overall.Successful)
	assert.Equal(t, 1, report.Overall.Skipped)
	assert.Equal(t, 1, report.Overall.NoChange)
	assert.Equal(t, int64(120), report.Overall.DurationSeconds)
	assert.Equal(t, int64(40), report.Overall.AverageDurationSeconds)
	assert.Equal(t, int64(125), report.Overall.TotalBytes)

	require.Len(t, report.Overall.Destinations, 2)
	assert.Equal(t, DestinationSummary{Name: "local", TotalPackages: 2, Stored: 2, TotalBytes: 125}, report.Overall.Destinations[0])
	assert.Equal(t, DestinationSummary{Name: "s3", TotalPackages: 2, Stored: 1, Failed: 1, TotalBytes: 100}, report.Overall.Destinations[1])

	require.Len(t, report.Sites, 2)
	assert.Equal(t, "site-a", report.Sites[0].SiteName)
	assert.Equal(t, 2, report.Sites[0].TotalRuns)
	assert.Equal(t, 1, report.Sites[0].Skipped)
	assert.Equal(t, int64(100), report.Sites[0].TotalBytes)
}
