package history

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryRecordsRunLifecycleAndArtifacts(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	started := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)
	run := &BackupRun{SiteName: "example", Status: StatusRunning, StartedAt: started, Forced: true}

	require.NoError(t, repo.CreateRun(ctx, run))
	require.NotEmpty(t, run.ID)
	require.NoError(t, repo.CreateArtifact(ctx, &Artifact{
		RunID: run.ID, SourceKind: "files", SourceName: "files", Destination: "local-primary",
		ObjectKey: "bqckup/example/set/files.tar.gz", Size: 42, SHA256: "abc", Status: ArtifactStored,
	}))
	finished := started.Add(2 * time.Second)
	require.NoError(t, repo.FinishRun(ctx, run.ID, StatusSuccess, finished, "", ""))

	last, err := repo.LastSuccessful(ctx, "example")
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Equal(t, StatusSuccess, last.Status)
	assert.EqualValues(t, 2000, last.DurationMillis)

	runs, err := repo.ListRuns(ctx, RunFilter{Site: "example", Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Len(t, runs[0].Artifacts, 1)
	assert.Equal(t, "abc", runs[0].Artifacts[0].SHA256)
}

func TestLastSuccessfulReturnsNilWhenMissing(t *testing.T) {
	repo := testRepository(t)
	run, err := repo.LastSuccessful(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, run)
}

func testRepository(t *testing.T) *Repository {
	t.Helper()
	db, closeDB, err := Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB()) })
	require.NoError(t, Migrate(context.Background(), db))
	return NewRepository(db)
}
