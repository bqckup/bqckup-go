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

	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/bqckup/bqckup-go/internal/storage/local"
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
	assert.Equal(t, "bqckup/example/23-July-2026/03-45-00.000000000/files.tar.gz", deps.repository.artifacts[0].ObjectKey)
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

func TestRunnerExportsEnabledDatabasesToEveryDestination(t *testing.T) {
	deps := successfulDependencies(t)
	store := deps.stores["local-primary"].(*fakeStore)
	site := validSite()
	site.Sources.Databases = []config.DatabaseSource{
		{Name: "application-mysql", Enabled: true, Engine: "mysql"},
		{Name: "application-postgres", Enabled: true, Engine: "postgres"},
	}
	deps.databaseExporters = map[string]Exporter{
		"mysql":    &fakeExporter{sourceKind: "database"},
		"postgres": &fakeExporter{sourceKind: "database"},
	}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), site, false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Len(t, store.keys, 3)
	assert.Contains(t, store.keys, "bqckup/example/23-July-2026/03-45-00.000000000/databases/application-mysql.sql.gz")
	assert.Contains(t, store.keys, "bqckup/example/23-July-2026/03-45-00.000000000/databases/application-postgres.sql.gz")
	assert.Len(t, deps.repository.artifacts, 3)
}

func TestRunnerDatabaseExporterFailurePreventsRetention(t *testing.T) {
	deps := successfulDependencies(t)
	deps.databaseExporters = map[string]Exporter{
		"mysql": &fakeExporter{err: errors.New("database-secret process output")},
	}
	site := validSite()
	site.Sources.Databases = []config.DatabaseSource{{Name: "application-mysql", Enabled: true, Engine: "mysql"}}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), site, false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, 0, deps.retainer.calls)
	assert.NotContains(t, deps.repository.errorMessage, "database-secret")
	require.Len(t, deps.repository.artifacts, 2)
	assert.Equal(t, history.ArtifactFailed, deps.repository.artifacts[1].Status)
	assert.Equal(t, "database", deps.repository.artifacts[1].SourceKind)
}

type dependencyFakes struct {
	repository        *fakeRepository
	archiver          *fakeArchiver
	incremental       *fakeIncrementalEngine
	stores            map[string]storage.Store
	storages          map[string]config.Storage
	retainer          *fakeRetainer
	lock              *fakeLocker
	clock             fakeClock
	tempRoot          string
	databaseExporters map[string]Exporter
	envLookup         func(string) (string, bool)
	notifier          *fakeNotifier
}

func successfulDependencies(t *testing.T) *dependencyFakes {
	t.Helper()
	return &dependencyFakes{
		repository:  &fakeRepository{},
		archiver:    &fakeArchiver{},
		incremental: &fakeIncrementalEngine{summary: restic.SnapshotSummary{SnapshotID: "snap-001", DataAdded: 2048, TotalBytesProcessed: 5_000_000}},
		stores:      map[string]storage.Store{"local-primary": &fakeStore{}},
		storages:    map[string]config.Storage{"local-primary": {Type: "local", Directory: "/var/backups/bqckup"}},
		retainer:    &fakeRetainer{},
		lock:        &fakeLocker{acquired: true},
		clock:       fakeClock{now: time.Date(2026, 7, 23, 3, 45, 0, 0, time.UTC)},
		tempRoot:    t.TempDir(),
		envLookup: func(key string) (string, bool) {
			if key == "RESTIC_PASSWORD" {
				return "test-secret-password", true
			}
			return "", false
		},
	}
}

func (d *dependencyFakes) dependencies() Dependencies {
	// A nil *fakeNotifier must stay a nil interface, or the runner's
	// nil-notifier check misses it and calls a nil pointer.
	var notifier Notifier
	if d.notifier != nil {
		notifier = d.notifier
	}
	return Dependencies{
		Repository:         d.repository,
		Archiver:           d.archiver,
		IncrementalEngine:  d.incremental,
		Stores:             d.stores,
		Storages:           d.storages,
		Retainer:           d.retainer,
		Locker:             d.lock,
		Notifier:           notifier,
		Clock:              d.clock,
		TemporaryDirectory: d.tempRoot,
		DatabaseExporters:  d.databaseExporters,
		EnvLookup:          d.envLookup,
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
	createdRuns     []history.BackupRun
	artifacts       []history.Artifact
	lastSuccessful  *history.BackupRun
	finishedStatus  history.RunStatus
	finishCtxErr    error
	errorCategory   string
	errorMessage    string
	createErr       error
	artifactErr     error
	finishErr       error
	finishCalls     int
	runArtifactsErr error
}

func (f *fakeRepository) CreateRun(_ context.Context, run *history.BackupRun) error {
	if f.createErr != nil {
		return f.createErr
	}
	run.ID = "run-1"
	f.createdRuns = append(f.createdRuns, *run)
	return nil
}

func (f *fakeRepository) FinishRun(ctx context.Context, _ string, status history.RunStatus, _ time.Time, category, message string) error {
	f.finishCalls++
	f.finishedStatus = status
	f.finishCtxErr = ctx.Err()
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

func (f *fakeRepository) RunArtifacts(_ context.Context, runID string) ([]history.Artifact, error) {
	if f.runArtifactsErr != nil {
		return nil, f.runArtifactsErr
	}
	var artifacts []history.Artifact
	for _, artifact := range f.artifacts {
		if artifact.RunID == runID && artifact.Status == history.ArtifactStored {
			artifacts = append(artifacts, artifact)
		}
	}
	return artifacts, nil
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
	keys     []string
}

func (f *fakeStore) Put(_ context.Context, artifact storage.Artifact, key string) (storage.StoredArtifact, error) {
	f.putCalls++
	f.keys = append(f.keys, key)
	if f.putErr != nil {
		return storage.StoredArtifact{}, f.putErr
	}
	return storage.StoredArtifact{Key: key, Size: artifact.Size, SHA256: artifact.SHA256}, nil
}

func (f *fakeStore) Probe(context.Context) error { return nil }

type fakeExporter struct {
	err        error
	sourceKind string
}

func (f *fakeExporter) Export(_ context.Context, source config.DatabaseSource, destination string) (Artifact, error) {
	if f.err != nil {
		return Artifact{}, f.err
	}
	contents := []byte(source.Name)
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(contents)
	return Artifact{Path: destination, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:]), SourceKind: f.sourceKind, SourceName: source.Name}, nil
}
func (*fakeStore) Delete(context.Context, string) error { return nil }
func (*fakeStore) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return nil, nil
}

type fakeRetainer struct {
	calls          int
	lastSitePrefix string
}

func (f *fakeRetainer) Apply(_ context.Context, _ storage.Store, sitePrefix string, _ int) error {
	f.calls++
	f.lastSitePrefix = sitePrefix
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

type fakeIncrementalEngine struct {
	ensureCalls    int
	ensureErr      error
	backupCalls    int
	backupErr      error
	retentionCalls int
	retentionErr   error
	summary        restic.SnapshotSummary
	lastSpec       restic.BackupSpec
	lastRepo       restic.RepoConfig
}

func (f *fakeIncrementalEngine) EnsureRepository(_ context.Context, repo restic.RepoConfig) error {
	f.ensureCalls++
	f.lastRepo = repo
	return f.ensureErr
}

func (f *fakeIncrementalEngine) BackupFiles(_ context.Context, repo restic.RepoConfig, spec restic.BackupSpec) (restic.SnapshotSummary, error) {
	f.backupCalls++
	f.lastRepo = repo
	f.lastSpec = spec
	if f.backupErr != nil {
		return restic.SnapshotSummary{}, f.backupErr
	}
	return f.summary, nil
}

func (f *fakeIncrementalEngine) ApplyRetention(_ context.Context, repo restic.RepoConfig, _ int, _ string) (int64, error) {
	f.retentionCalls++
	f.lastRepo = repo
	return 0, f.retentionErr
}

func (f *fakeIncrementalEngine) Unlock(_ context.Context, _ restic.RepoConfig) error {
	return nil
}

// TestRunnerIncrementalBackupRetainsDatabaseArtifacts: incremental sites
// store database dumps under bqckup/<site>/<timestamp>/databases/ on every
// run, but retention only ran for full mode, so those sets grew without
// bound. The run must apply set retention to the bqckup/<site> prefix in
// incremental mode too.
func TestRunnerIncrementalBackupRetainsDatabaseArtifacts(t *testing.T) {
	deps := successfulDependencies(t)
	store := deps.stores["local-primary"].(*fakeStore)
	deps.databaseExporters = map[string]Exporter{
		"mysql": &fakeExporter{sourceKind: "database"},
	}

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{PasswordEnv: "RESTIC_PASSWORD"}
	site.Sources.Databases = []config.DatabaseSource{
		{Name: "application-mysql", Enabled: true, Engine: "mysql"},
	}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), site, false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Contains(t, store.keys, "bqckup/example/23-July-2026/03-45-00.000000000/databases/application-mysql.sql.gz")
	assert.Equal(t, 1, deps.retainer.calls, "incremental runs must retain the bqckup/<site> database artifact sets")
	assert.Equal(t, "bqckup/example", deps.retainer.lastSitePrefix)
}

// TestRunnerTwoForcedRunsInSameSecond: two forced runs started within the
// same wall-clock second must both succeed. The second run used to get the
// same backup-set key as the first (bqckup/<site>/<date>/<hh-mm-ss>/...) and
// the storage collision rejected the overwrite, failing the whole run.
func TestRunnerTwoForcedRunsInSameSecond(t *testing.T) {
	deps := successfulDependencies(t)
	store, err := local.New(t.TempDir())
	require.NoError(t, err)
	deps.stores["local-primary"] = store
	site := validSite()

	first, err := NewRunner(deps.dependencies()).Run(context.Background(), site, true)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, first.Status)

	deps.clock.now = deps.clock.now.Add(500 * time.Millisecond) // still the same second
	second, err := NewRunner(deps.dependencies()).Run(context.Background(), site, true)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, second.Status)
	require.Len(t, deps.repository.artifacts, 2)
	assert.NotEqual(t, deps.repository.artifacts[0].ObjectKey, deps.repository.artifacts[1].ObjectKey)
}

// TestRunnerIncrementalArtifactRecordsSnapshotSize: the incremental artifact
// row must carry the snapshot's logical size, not the dedup delta (0 on a
// fully deduplicated run), and must not claim a SHA-256 it does not have.
func TestRunnerIncrementalArtifactRecordsSnapshotSize(t *testing.T) {
	deps := successfulDependencies(t)
	deps.incremental.summary = restic.SnapshotSummary{SnapshotID: "snap-001", TotalBytesProcessed: 5_000_000, DataAdded: 2048}
	runner := NewRunner(deps.dependencies())

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{PasswordEnv: "RESTIC_PASSWORD"}

	result, err := runner.Run(context.Background(), site, false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	require.Len(t, deps.repository.artifacts, 1)
	artifact := deps.repository.artifacts[0]
	assert.Equal(t, "snap-001", artifact.ObjectKey)
	assert.Equal(t, int64(5_000_000), artifact.Size)
	assert.Empty(t, artifact.SHA256)
}

func TestRunnerIncrementalBackupSuccess(t *testing.T) {
	deps := successfulDependencies(t)
	runner := NewRunner(deps.dependencies())

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{
		PasswordEnv: "RESTIC_PASSWORD",
	}

	result, err := runner.Run(context.Background(), site, false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, 1, deps.incremental.ensureCalls)
	assert.Equal(t, 1, deps.incremental.backupCalls)
	assert.Equal(t, 1, deps.incremental.retentionCalls)
	assert.Equal(t, 0, deps.archiver.calls) // classic archiver not called

	require.Len(t, deps.repository.artifacts, 1)
	assert.Equal(t, "snap-001", deps.repository.artifacts[0].ObjectKey)
	assert.Equal(t, int64(5_000_000), deps.repository.artifacts[0].Size)
	assert.Empty(t, deps.repository.artifacts[0].SHA256)
}

func TestRunnerIncrementalBackupMissingPasswordEnv(t *testing.T) {
	deps := successfulDependencies(t)
	deps.envLookup = func(string) (string, bool) { return "", false }
	runner := NewRunner(deps.dependencies())

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{
		PasswordEnv: "UNSET_VAR",
	}

	result, err := runner.Run(context.Background(), site, false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, 0, deps.incremental.backupCalls)
	assert.Equal(t, 0, deps.incremental.retentionCalls)
}

func TestRunnerIncrementalBackupFailureDoesNotRetain(t *testing.T) {
	deps := successfulDependencies(t)
	deps.incremental.backupErr = errors.New("restic failed to snapshot")
	runner := NewRunner(deps.dependencies())

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{
		PasswordEnv: "RESTIC_PASSWORD",
	}

	result, err := runner.Run(context.Background(), site, false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, 1, deps.incremental.backupCalls)
	assert.Equal(t, 0, deps.incremental.retentionCalls) // retention must NOT run
}

// cancelAfterPutStore cancels the shared context right after a successful
// Put, simulating a cancellation arriving just after the last storage
// write of an otherwise successful run.
type cancelAfterPutStore struct {
	storage.Store
	cancel context.CancelFunc
}

func (s *cancelAfterPutStore) Put(ctx context.Context, artifact storage.Artifact, key string) (storage.StoredArtifact, error) {
	stored, err := s.Store.Put(ctx, artifact, key)
	if err != nil {
		return storage.StoredArtifact{}, err
	}
	s.cancel()
	return stored, nil
}

func (s *cancelAfterPutStore) Probe(context.Context) error { return nil }

// TestRunnerSuccessFinishRunSurvivesLateCancellation: a cancellation that
// arrives after the last storage write must not abort the success-path
// FinishRun (the failure path already uses context.WithoutCancel; the
// success path must too), or the run stays in history status "running"
// forever.
func TestRunnerSuccessFinishRunSurvivesLateCancellation(t *testing.T) {
	deps := successfulDependencies(t)
	ctx, cancel := context.WithCancel(context.Background())
	deps.stores["local-primary"] = &cancelAfterPutStore{Store: deps.stores["local-primary"], cancel: cancel}

	result, err := NewRunner(deps.dependencies()).Run(ctx, validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, history.StatusSuccess, deps.repository.finishedStatus)
	assert.NoError(t, deps.repository.finishCtxErr, "FinishRun must not observe the cancelled context")
}

func TestRunnerNotifiesSuccessAfterTerminalRecord(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, config.EventBackupSucceeded, call.Event)
	assert.Equal(t, StatusSuccess, call.Status)
	assert.Equal(t, "run-1", call.RunID)
	assert.Equal(t, "example", call.SiteName)
	assert.Equal(t, deps.clock.now, call.StartedAt)
	assert.Equal(t, deps.clock.now, call.FinishedAt)
	assert.Empty(t, call.ErrorCategory)
	require.Len(t, call.Artifacts, 1, "success notification carries the run's stored artifacts")
}

func TestRunnerNotifiesFailureWithCategoryAndRedactedMessage(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.archiver.err = errors.New("source vanished")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, config.EventBackupFailed, call.Event)
	assert.Equal(t, "execution", call.ErrorCategory)
	assert.Equal(t, "could not create the file archive", call.ErrorMessage)
	assert.NotContains(t, call.ErrorMessage, "source vanished")
}

func TestRunnerNotifiesCancellation(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.archiver.err = context.Canceled

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, StatusCancelled, result.Status)

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, config.EventBackupCancelled, call.Event)
	assert.Equal(t, "cancellation", call.ErrorCategory)
}

func TestRunnerNotifiesPersistenceWhenSuccessFinishRunFails(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.repository.finishErr = errors.New("database unavailable")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.ErrorIs(t, err, deps.repository.finishErr)
	assert.Equal(t, StatusFailed, result.Status)

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, config.EventBackupFailed, call.Event)
	assert.Equal(t, "persistence", call.ErrorCategory)
	assert.Equal(t, "could not finalize backup history", call.ErrorMessage)
}

func TestRunnerDoesNotNotifyForSkippedRuns(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.repository.lastSuccessful = &history.BackupRun{StartedAt: deps.clock.now.Add(-30 * time.Minute)}

	_, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Empty(t, deps.notifier.calls)

	deps.repository.lastSuccessful = nil
	deps.lock.acquired = false
	_, err = NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Empty(t, deps.notifier.calls)
}

func TestRunnerDoesNotNotifyWhenRunIsNeverCreated(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.repository.createErr = errors.New("database unavailable")

	_, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.Error(t, err)
	assert.Empty(t, deps.notifier.calls)
}

func TestRunnerNotifierErrorDoesNotChangeRunResult(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{err: errors.New("webhook down")}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, history.StatusSuccess, deps.repository.finishedStatus)
}

func TestRunnerNotifierWithoutWiringIsANoOp(t *testing.T) {
	deps := successfulDependencies(t)

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
}

type fakeNotifier struct {
	calls []NotifyInput
	err   error
}

func (f *fakeNotifier) Notify(_ context.Context, input NotifyInput) error {
	f.calls = append(f.calls, input)
	return f.err
}
