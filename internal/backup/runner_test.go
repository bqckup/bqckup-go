package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerCompletesBackupLifecycle(t *testing.T) {
	deps := successfulDependencies(t)
	runner := NewRunner(deps.dependencies())

	result, err := runner.Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, history.StatusSuccess, deps.repository.finishedStatus)
	require.Len(t, deps.repository.artifacts, 1)
	assert.Equal(t, "bqckup/example/2026-07-23T03-45-00Z/files.tar.gz", deps.repository.artifacts[0].ObjectKey)
	assert.Equal(t, 1, deps.retainer.calls)
	assert.Equal(t, 1, deps.lock.unlockCalls)
	_, statErr := os.Stat(deps.archiver.workspace)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestRunnerSkipsInsideMinimumInterval(t *testing.T) {
	deps := successfulDependencies(t)
	deps.repository.lastSuccessful = &history.BackupRun{StartedAt: deps.clock.now.Add(-30 * time.Minute)}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSkipped, result.Status)
	assert.Equal(t, SkipMinimumInterval, result.SkipReason)
	assert.Empty(t, deps.repository.createdRuns)
	assert.Equal(t, 0, deps.archiver.calls)
}

func TestRunnerForceOverridesMinimumInterval(t *testing.T) {
	deps := successfulDependencies(t)
	deps.repository.lastSuccessful = &history.BackupRun{StartedAt: deps.clock.now.Add(-30 * time.Minute)}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), true)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	require.Len(t, deps.repository.createdRuns, 1)
	assert.True(t, deps.repository.createdRuns[0].Forced)
}

func TestRunnerSkipsWhenSiteLockIsHeld(t *testing.T) {
	deps := successfulDependencies(t)
	deps.lock.acquired = false

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSkipped, result.Status)
	assert.Equal(t, SkipAlreadyRunning, result.SkipReason)
	assert.Empty(t, deps.repository.createdRuns)
}

func TestRunnerDoesNotApplyRetentionAfterArchiveFailure(t *testing.T) {
	deps := successfulDependencies(t)
	deps.archiver.err = errors.New("source vanished")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, history.StatusFailed, deps.repository.finishedStatus)
	assert.Equal(t, 0, deps.retainer.calls)
	assert.Equal(t, "execution", deps.repository.errorCategory)
	assert.NotContains(t, deps.repository.errorMessage, "source vanished")
}

func TestRunnerDoesNotApplyRetentionAfterStorageFailure(t *testing.T) {
	deps := successfulDependencies(t)
	deps.stores["local-primary"].(*fakeStore).putErr = errors.New("disk full")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, history.StatusFailed, deps.repository.finishedStatus)
	assert.Equal(t, 0, deps.retainer.calls)
	assert.Equal(t, "storage", deps.repository.errorCategory)
}

func TestRunnerMarksCancellation(t *testing.T) {
	deps := successfulDependencies(t)
	deps.archiver.err = context.Canceled

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, StatusCancelled, result.Status)
	assert.Equal(t, history.StatusCancelled, deps.repository.finishedStatus)
	assert.Equal(t, "cancellation", deps.repository.errorCategory)
}

func TestRunnerRequiresEveryDestination(t *testing.T) {
	deps := successfulDependencies(t)
	deps.stores["secondary"] = &fakeStore{}
	site := validSite()
	site.Destinations = append(site.Destinations, config.Destination{Storage: "secondary"})

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), site, false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, 1, deps.stores["local-primary"].(*fakeStore).putCalls)
	assert.Equal(t, 1, deps.stores["secondary"].(*fakeStore).putCalls)
	assert.Len(t, deps.repository.artifacts, 2)
	assert.Equal(t, 2, deps.retainer.calls)
}

func TestRunnerFailsWhenDestinationIsNotWired(t *testing.T) {
	deps := successfulDependencies(t)
	site := validSite()
	site.Destinations = []config.Destination{{Storage: "missing"}}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), site, false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, history.StatusFailed, deps.repository.finishedStatus)
}

func TestRunnerReturnsPersistenceFailureWhenTerminalUpdateFails(t *testing.T) {
	deps := successfulDependencies(t)
	finishErr := errors.New("database unavailable")
	deps.repository.finishErr = finishErr

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.ErrorIs(t, err, finishErr)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, 1, deps.repository.finishCalls)
}

type dependencyFakes struct {
	repository *fakeRepository
	archiver   *fakeArchiver
	stores     map[string]storage.Store
	retainer   *fakeRetainer
	lock       *fakeLocker
	clock      fakeClock
	tempRoot   string
}

func successfulDependencies(t *testing.T) *dependencyFakes {
	t.Helper()
	return &dependencyFakes{
		repository: &fakeRepository{},
		archiver:   &fakeArchiver{},
		stores:     map[string]storage.Store{"local-primary": &fakeStore{}},
		retainer:   &fakeRetainer{},
		lock:       &fakeLocker{acquired: true},
		clock:      fakeClock{now: time.Date(2026, 7, 23, 3, 45, 0, 0, time.UTC)},
		tempRoot:   t.TempDir(),
	}
}

func (d *dependencyFakes) dependencies() Dependencies {
	return Dependencies{
		Repository:         d.repository,
		Archiver:           d.archiver,
		Stores:             d.stores,
		Retainer:           d.retainer,
		Locker:             d.lock,
		Clock:              d.clock,
		TemporaryDirectory: d.tempRoot,
	}
}

func validSite() config.Site {
	return config.Site{
		Name:    "example",
		Enabled: true,
		Sources: config.Sources{Files: config.FileSource{Include: []string{"/srv/example"}}},
		Destinations: []config.Destination{
			{Storage: "local-primary"},
		},
		Policy: config.Policy{MinimumInterval: time.Hour, KeepLast: 3},
	}
}

type fakeRepository struct {
	createdRuns    []history.BackupRun
	artifacts      []history.Artifact
	lastSuccessful *history.BackupRun
	finishedStatus history.RunStatus
	errorCategory  string
	errorMessage   string
	createErr      error
	artifactErr    error
	finishErr      error
	finishCalls    int
}

func (f *fakeRepository) CreateRun(_ context.Context, run *history.BackupRun) error {
	if f.createErr != nil {
		return f.createErr
	}
	run.ID = "run-1"
	f.createdRuns = append(f.createdRuns, *run)
	return nil
}

func (f *fakeRepository) FinishRun(_ context.Context, _ string, status history.RunStatus, _ time.Time, category, message string) error {
	f.finishCalls++
	f.finishedStatus = status
	f.errorCategory = category
	f.errorMessage = message
	return f.finishErr
}

func (f *fakeRepository) CreateArtifact(_ context.Context, artifact *history.Artifact) error {
	if f.artifactErr != nil {
		return f.artifactErr
	}
	f.artifacts = append(f.artifacts, *artifact)
	return nil
}

func (f *fakeRepository) LastSuccessful(context.Context, string) (*history.BackupRun, error) {
	return f.lastSuccessful, nil
}

type fakeArchiver struct {
	calls     int
	err       error
	workspace string
}

func (f *fakeArchiver) Create(_ context.Context, _ FileSource, destination string) (Artifact, error) {
	f.calls++
	f.workspace = filepath.Dir(destination)
	if f.err != nil {
		return Artifact{}, f.err
	}
	contents := []byte("archive")
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(contents)
	return Artifact{Path: destination, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:]), SourceKind: "files", SourceName: "files"}, nil
}

type fakeStore struct {
	putCalls int
	putErr   error
}

func (f *fakeStore) Put(_ context.Context, artifact storage.Artifact, key string) (storage.StoredArtifact, error) {
	f.putCalls++
	if f.putErr != nil {
		return storage.StoredArtifact{}, f.putErr
	}
	return storage.StoredArtifact{Key: key, Size: artifact.Size, SHA256: artifact.SHA256}, nil
}
func (*fakeStore) Delete(context.Context, string) error { return nil }
func (*fakeStore) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return nil, nil
}

type fakeRetainer struct{ calls int }

func (f *fakeRetainer) Apply(_ context.Context, _ storage.Store, _ string, _ int) error {
	f.calls++
	return nil
}

type fakeLocker struct {
	acquired    bool
	err         error
	unlockCalls int
}

func (f *fakeLocker) TryLock(context.Context, string) (func() error, bool, error) {
	return func() error { f.unlockCalls++; return nil }, f.acquired, f.err
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }
