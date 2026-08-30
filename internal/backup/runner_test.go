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

	"github.com/bqckup/bqckup-go/internal/backup/incremental"
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
	require.Len(t, deps.repository.packages, 1)
	assert.Equal(t, "bqckup/example/23-July-2026/03-45-00.000000000/files.tar.gz", deps.repository.packages[0].ObjectKey)
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
	assert.Len(t, deps.repository.packages, 2)
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
	assert.Len(t, deps.repository.packages, 3)
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
	require.Len(t, deps.repository.packages, 2)
	assert.Equal(t, history.PackageFailed, deps.repository.packages[1].Status)
	assert.Equal(t, "database", deps.repository.packages[1].SourceKind)
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
		incremental: &fakeIncrementalEngine{summary: incremental.SnapshotSummary{SnapshotID: "snap-001", DataAdded: 2048, TotalBytesProcessed: 5_000_000}},
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
	createdRuns          []history.BackupRun
	packages             []history.Package
	lastSuccessful       *history.BackupRun
	lastSuccessErr       error
	notifyLastSuccessErr error
	detectLastSuccessErr error
	lastSuccessCalls     int
	streak               int
	streakErr            error
	finishedStatus       history.RunStatus
	finishCtxErr         error
	errorCategory        string
	errorMessage         string
	createErr            error
	packageErr           error
	finishErr            error
	finishCalls          int
	runPackagesErr       error
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

func (f *fakeRepository) CreatePackage(_ context.Context, pkg *history.Package) error {
	if f.packageErr != nil {
		return f.packageErr
	}
	f.packages = append(f.packages, *pkg)
	return nil
}

func (f *fakeRepository) LastSuccessful(_ context.Context, _ string, _ time.Time) (*history.BackupRun, error) {
	f.lastSuccessCalls++
	if f.lastSuccessCalls == 2 && f.detectLastSuccessErr != nil {
		return nil, f.detectLastSuccessErr
	}
	if f.lastSuccessCalls > 2 && f.notifyLastSuccessErr != nil {
		return nil, f.notifyLastSuccessErr
	}
	if f.lastSuccessErr != nil {
		return nil, f.lastSuccessErr
	}
	return f.lastSuccessful, nil
}

func (f *fakeRepository) ConsecutiveWithoutSuccess(context.Context, string, time.Time) (int, error) {
	if f.streakErr != nil {
		return 0, f.streakErr
	}
	return f.streak, nil
}

func (f *fakeRepository) RunPackages(_ context.Context, runID string) ([]history.Package, error) {
	if f.runPackagesErr != nil {
		return nil, f.runPackagesErr
	}
	var packages []history.Package
	for _, pkg := range f.packages {
		if pkg.RunID == runID && pkg.Status == history.PackageStored {
			packages = append(packages, pkg)
		}
	}
	return packages, nil
}

type fakeArchiver struct {
	calls     int
	err       error
	workspace string
}

func (f *fakeArchiver) Create(_ context.Context, _ FileSource, destination string) (Package, error) {
	f.calls++
	f.workspace = filepath.Dir(destination)
	if f.err != nil {
		return Package{}, f.err
	}
	contents := []byte("archive")
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		return Package{}, err
	}
	sum := sha256.Sum256(contents)
	return Package{Path: destination, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:]), SourceKind: "files", SourceName: "files"}, nil
}

type fakeStore struct {
	putCalls int
	putErr   error
	keys     []string
}

func (f *fakeStore) Put(_ context.Context, pkg storage.Package, key string) (storage.StoredPackage, error) {
	f.putCalls++
	f.keys = append(f.keys, key)
	if f.putErr != nil {
		return storage.StoredPackage{}, f.putErr
	}
	return storage.StoredPackage{Key: key, Size: pkg.Size, SHA256: pkg.SHA256}, nil
}

func (f *fakeStore) Probe(context.Context) error { return nil }

type fakeExporter struct {
	err        error
	sourceKind string
}

func (f *fakeExporter) Export(_ context.Context, source config.DatabaseSource, destination string) (Package, error) {
	if f.err != nil {
		return Package{}, f.err
	}
	contents := []byte(source.Name)
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		return Package{}, err
	}
	sum := sha256.Sum256(contents)
	return Package{Path: destination, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:]), SourceKind: f.sourceKind, SourceName: source.Name}, nil
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
	summary        incremental.SnapshotSummary
	lastSpec       incremental.BackupSpec
	lastRepo       incremental.RepoConfig
}

func (f *fakeIncrementalEngine) EnsureRepository(_ context.Context, repo incremental.RepoConfig) error {
	f.ensureCalls++
	f.lastRepo = repo
	return f.ensureErr
}

func (f *fakeIncrementalEngine) BackupFiles(_ context.Context, repo incremental.RepoConfig, spec incremental.BackupSpec) (incremental.SnapshotSummary, error) {
	f.backupCalls++
	f.lastRepo = repo
	f.lastSpec = spec
	if f.backupErr != nil {
		return incremental.SnapshotSummary{}, f.backupErr
	}
	return f.summary, nil
}

func (f *fakeIncrementalEngine) ApplyRetention(_ context.Context, repo incremental.RepoConfig, _ int, _ string) (int64, error) {
	f.retentionCalls++
	f.lastRepo = repo
	return 0, f.retentionErr
}

func (f *fakeIncrementalEngine) Unlock(_ context.Context, _ incremental.RepoConfig) error {
	return nil
}

// TestRunnerIncrementalBackupRetainsDatabasePackages: incremental sites
// store database dumps under bqckup/<site>/<timestamp>/databases/ on every
// run, but retention only ran for full mode, so those sets grew without
// bound. The run must apply set retention to the bqckup/<site> prefix in
// incremental mode too.
func TestRunnerIncrementalBackupRetainsDatabasePackages(t *testing.T) {
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
	assert.Equal(t, 1, deps.retainer.calls, "incremental runs must retain the bqckup/<site> database package sets")
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
	require.Len(t, deps.repository.packages, 2)
	assert.NotEqual(t, deps.repository.packages[0].ObjectKey, deps.repository.packages[1].ObjectKey)
}

// TestRunnerIncrementalPackageRecordsSnapshotSize: the incremental package
// row must carry the snapshot's logical size, not the dedup delta (0 on a
// fully deduplicated run), and must not claim a SHA-256 it does not have.
func TestRunnerIncrementalPackageRecordsSnapshotSize(t *testing.T) {
	deps := successfulDependencies(t)
	deps.incremental.summary = incremental.SnapshotSummary{SnapshotID: "snap-001", TotalBytesProcessed: 5_000_000, DataAdded: 2048}
	runner := NewRunner(deps.dependencies())

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{PasswordEnv: "RESTIC_PASSWORD"}

	result, err := runner.Run(context.Background(), site, false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	require.Len(t, deps.repository.packages, 1)
	pkg := deps.repository.packages[0]
	assert.Equal(t, "snap-001", pkg.ObjectKey)
	assert.Equal(t, int64(5_000_000), pkg.Size)
	assert.Empty(t, pkg.SHA256)
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

	require.Len(t, deps.repository.packages, 1)
	assert.Equal(t, "snap-001", deps.repository.packages[0].ObjectKey)
	assert.Equal(t, int64(5_000_000), deps.repository.packages[0].Size)
	assert.Empty(t, deps.repository.packages[0].SHA256)
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

// TestRunnerIncrementalFailureNotifiesCleanMessage: the top-level apperror
// message stays hand-written; engine text and paths live only in the cause
// chain, so the notification payload is clean by construction.
func TestRunnerIncrementalFailureNotifiesCleanMessage(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.incremental.backupErr = errors.New("restic: snapshot failed: /srv/example/secret.txt")
	runner := NewRunner(deps.dependencies())

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{PasswordEnv: "RESTIC_PASSWORD"}

	result, err := runner.Run(context.Background(), site, false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, "could not create incremental file backup", err.Error())
	require.NotNil(t, errors.Unwrap(err), "engine error must stay the wrapped cause")
	assert.Contains(t, errors.Unwrap(err).Error(), "restic")

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, "could not create incremental file backup", call.ErrorMessage)
	assert.NotContains(t, call.ErrorMessage, "restic")
	assert.NotContains(t, call.ErrorMessage, "secret.txt")
}

// cancelAfterPutStore cancels the shared context right after a successful
// Put, simulating a cancellation arriving just after the last storage
// write of an otherwise successful run.
type cancelAfterPutStore struct {
	storage.Store
	cancel context.CancelFunc
}

func (s *cancelAfterPutStore) Put(ctx context.Context, pkg storage.Package, key string) (storage.StoredPackage, error) {
	stored, err := s.Store.Put(ctx, pkg, key)
	if err != nil {
		return storage.StoredPackage{}, err
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

func TestRunnerNeverNotifiesOnSuccess(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Empty(t, deps.notifier.calls, "success runs must never notify")
}

func TestRunnerNotifiesFailureWithStreakAndLastSuccessful(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	lastSuccessTime := deps.clock.now.Add(-2 * time.Hour)
	deps.repository.lastSuccessful = &history.BackupRun{StartedAt: lastSuccessTime}
	deps.repository.streak = 3
	deps.archiver.err = errors.New("disk full")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), true)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, config.EventBackupFailed, call.Event)
	assert.Equal(t, 3, call.FailureStreak)
	assert.Equal(t, lastSuccessTime, call.LastSuccessfulAt)
}

func TestRunnerNotifiesFailureWhenRepositoryQueriesFail(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.repository.notifyLastSuccessErr = errors.New("db error")
	deps.repository.streakErr = errors.New("db error")
	deps.archiver.err = errors.New("disk full")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), true)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, config.EventBackupFailed, call.Event)
	assert.Equal(t, 0, call.FailureStreak)
	assert.True(t, call.LastSuccessfulAt.IsZero())
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

func TestUnchangedSizes(t *testing.T) {
	tests := []struct {
		name     string
		current  []history.Package
		previous []history.Package
		expected bool
	}{
		{
			name:     "empty current",
			current:  nil,
			previous: []history.Package{{SourceKind: "files", SourceName: "files", Size: 100}},
			expected: false,
		},
		{
			name:     "empty previous",
			current:  []history.Package{{SourceKind: "files", SourceName: "files", Size: 100}},
			previous: nil,
			expected: false,
		},
		{
			name:     "both empty",
			current:  nil,
			previous: nil,
			expected: false,
		},
		{
			name: "missing key in current",
			current: []history.Package{
				{SourceKind: "files", SourceName: "files", Size: 100},
			},
			previous: []history.Package{
				{SourceKind: "files", SourceName: "files", Size: 100},
				{SourceKind: "database", SourceName: "app", Size: 200},
			},
			expected: false,
		},
		{
			name: "added key in current",
			current: []history.Package{
				{SourceKind: "files", SourceName: "files", Size: 100},
				{SourceKind: "database", SourceName: "app", Size: 200},
			},
			previous: []history.Package{
				{SourceKind: "files", SourceName: "files", Size: 100},
			},
			expected: false,
		},
		{
			name: "size difference",
			current: []history.Package{
				{SourceKind: "files", SourceName: "files", Size: 101},
			},
			previous: []history.Package{
				{SourceKind: "files", SourceName: "files", Size: 100},
			},
			expected: false,
		},
		{
			name: "multi destination dedupe same size",
			current: []history.Package{
				{SourceKind: "files", SourceName: "files", Destination: "local-primary", Size: 100},
				{SourceKind: "files", SourceName: "files", Destination: "s3-primary", Size: 100},
			},
			previous: []history.Package{
				{SourceKind: "files", SourceName: "files", Destination: "local-primary", Size: 100},
			},
			expected: true,
		},
		{
			name: "exact equality multi package",
			current: []history.Package{
				{SourceKind: "files", SourceName: "files", Destination: "dest1", Size: 500},
				{SourceKind: "database", SourceName: "db1", Destination: "dest1", Size: 250},
			},
			previous: []history.Package{
				{SourceKind: "database", SourceName: "db1", Destination: "dest2", Size: 250},
				{SourceKind: "files", SourceName: "files", Destination: "dest2", Size: 500},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, unchangedSizes(tt.current, tt.previous))
		})
	}
}

func TestRunnerDetectsNoChangeRun(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}

	anchorID := "anchor-run-1"
	deps.repository.lastSuccessful = &history.BackupRun{
		ID:        anchorID,
		SiteName:  "example",
		Status:    history.StatusSuccess,
		StartedAt: deps.clock.now.Add(-2 * time.Hour),
	}
	// Previous package size is 7 bytes (created by fakeArchiver "archive")
	deps.repository.packages = []history.Package{
		{
			RunID:       anchorID,
			SourceKind:  "files",
			SourceName:  "files",
			Destination: "local-primary",
			ObjectKey:   "bqckup/example/23-July-2026/01-45-00.000000000/files.tar.gz",
			Size:        7,
			Status:      history.PackageStored,
		},
	}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), true)
	require.NoError(t, err)
	assert.Equal(t, StatusNoChange, result.Status)
	assert.Equal(t, history.StatusNoChange, deps.repository.finishedStatus)
	assert.Equal(t, "no_change", deps.repository.errorCategory)
	assert.Equal(t, "1 item is unchanged from the previous run.", deps.repository.errorMessage)

	require.Len(t, deps.notifier.calls, 1)
	call := deps.notifier.calls[0]
	assert.Equal(t, config.EventBackupNoChange, call.Event)
	assert.Equal(t, StatusNoChange, call.Status)
	assert.Equal(t, "no_change", call.ErrorCategory)
	assert.Equal(t, "1 item is unchanged from the previous run.", call.ErrorMessage)
	assert.False(t, call.HasDatabaseSources)
	require.Len(t, call.Destinations, 1)
	assert.Equal(t, "local-primary", call.Destinations[0].Name)
	assert.Equal(t, "/var/backups/bqckup", call.Destinations[0].Path)
}

func TestRunnerNoChangeMultipleSourcesMessage(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.databaseExporters = map[string]Exporter{
		"mysql": &fakeExporter{sourceKind: "database"},
	}

	site := validSite()
	site.Sources.Databases = []config.DatabaseSource{
		{Name: "app-mysql", Enabled: true, Engine: "mysql"},
	}

	anchorID := "anchor-run-1"
	deps.repository.lastSuccessful = &history.BackupRun{
		ID:        anchorID,
		SiteName:  "example",
		Status:    history.StatusSuccess,
		StartedAt: deps.clock.now.Add(-2 * time.Hour),
	}
	// Previous packages: files (7 bytes) and app-mysql (9 bytes)
	deps.repository.packages = []history.Package{
		{
			RunID:       anchorID,
			SourceKind:  "files",
			SourceName:  "files",
			Destination: "local-primary",
			ObjectKey:   "bqckup/example/prev/files.tar.gz",
			Size:        7,
			Status:      history.PackageStored,
		},
		{
			RunID:       anchorID,
			SourceKind:  "database",
			SourceName:  "app-mysql",
			Destination: "local-primary",
			ObjectKey:   "bqckup/example/prev/databases/app-mysql.sql.gz",
			Size:        9, // len("app-mysql")
			Status:      history.PackageStored,
		},
	}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), site, true)
	require.NoError(t, err)
	assert.Equal(t, StatusNoChange, result.Status)
	assert.Equal(t, history.StatusNoChange, deps.repository.finishedStatus)
	assert.Equal(t, "2 items are unchanged from the previous run.", deps.repository.errorMessage)

	require.Len(t, deps.notifier.calls, 1)
	assert.True(t, deps.notifier.calls[0].HasDatabaseSources)
}

func TestRunnerDegradesToSuccessWhenAnchorQueryFails(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.repository.detectLastSuccessErr = errors.New("query error")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), true)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, history.StatusSuccess, deps.repository.finishedStatus)
	assert.Empty(t, deps.notifier.calls)
}

func TestRunnerDegradesToSuccessWhenRunPackagesQueryFails(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}
	deps.repository.lastSuccessful = &history.BackupRun{
		ID:        "anchor-1",
		SiteName:  "example",
		Status:    history.StatusSuccess,
		StartedAt: deps.clock.now.Add(-2 * time.Hour),
	}
	deps.repository.runPackagesErr = errors.New("packages query error")

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), true)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, history.StatusSuccess, deps.repository.finishedStatus)
	assert.Empty(t, deps.notifier.calls)
}

func TestRunnerIncrementalNeverClassifiesAsNoChange(t *testing.T) {
	deps := successfulDependencies(t)
	deps.notifier = &fakeNotifier{}

	site := validSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{PasswordEnv: "RESTIC_PASSWORD"}

	anchorID := "anchor-run-1"
	deps.repository.lastSuccessful = &history.BackupRun{
		ID:        anchorID,
		SiteName:  "example",
		Status:    history.StatusSuccess,
		StartedAt: deps.clock.now.Add(-2 * time.Hour),
	}
	deps.repository.packages = []history.Package{
		{
			RunID:       anchorID,
			SourceKind:  "files",
			SourceName:  "files",
			Destination: "local-primary",
			ObjectKey:   "snap-001",
			Size:        5_000_000,
			Status:      history.PackageStored,
		},
	}

	result, err := NewRunner(deps.dependencies()).Run(context.Background(), site, true)
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, history.StatusSuccess, deps.repository.finishedStatus)
	assert.Empty(t, deps.notifier.calls)
}

type fakeNotifier struct {
	calls []NotifyInput
	err   error
}

func (f *fakeNotifier) Notify(_ context.Context, input NotifyInput) error {
	f.calls = append(f.calls, input)
	return f.err
}
