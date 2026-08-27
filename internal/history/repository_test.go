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

func TestRunArtifactsReturnsOnlyStoredArtifacts(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	run := &BackupRun{SiteName: "example", Status: StatusRunning, StartedAt: time.Now()}
	require.NoError(t, repo.CreateRun(ctx, run))

	stored := []Artifact{
		{RunID: run.ID, SourceKind: "files", SourceName: "files", Destination: "local-primary", ObjectKey: "k1", Size: 10, SHA256: "a", Status: ArtifactStored},
		{RunID: run.ID, SourceKind: "files", SourceName: "files", Destination: "s3-primary", ObjectKey: "k2", Size: 10, SHA256: "b", Status: ArtifactStored},
		{RunID: run.ID, SourceKind: "database", SourceName: "app", Destination: "local-primary", ObjectKey: "k3", Size: 20, SHA256: "c", Status: ArtifactStored},
	}
	for _, artifact := range stored {
		require.NoError(t, repo.CreateArtifact(ctx, &artifact))
	}
	// A failed artifact of the same run must not be returned.
	require.NoError(t, repo.CreateArtifact(ctx, &Artifact{
		RunID: run.ID, SourceKind: "database", SourceName: "broken", Destination: "local-primary",
		ObjectKey: "k4", Size: 99, SHA256: "d", Status: ArtifactFailed,
	}))
	// An artifact of another run must not be returned.
	other := &BackupRun{SiteName: "example", Status: StatusRunning, StartedAt: time.Now()}
	require.NoError(t, repo.CreateRun(ctx, other))
	require.NoError(t, repo.CreateArtifact(ctx, &Artifact{
		RunID: other.ID, SourceKind: "files", SourceName: "files", Destination: "local-primary",
		ObjectKey: "k5", Size: 99, SHA256: "e", Status: ArtifactStored,
	}))

	artifacts, err := repo.RunArtifacts(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, artifacts, 3)
	for _, artifact := range artifacts {
		assert.Equal(t, run.ID, artifact.RunID)
		assert.Equal(t, ArtifactStored, artifact.Status)
	}
}

func testRepository(t *testing.T) *Repository {
	t.Helper()
	db, closeDB, err := Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB()) })
	require.NoError(t, Migrate(context.Background(), db))
	return NewRepository(db)
}
