package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRemoteLister struct {
	sets          []storage.BackupSet
	packages      map[string][]storage.RemotePackage
	setsErr       error
	packagesErr   error
	listedSetKeys []string
}

func (f *fakeRemoteLister) ListBackupSets(_ context.Context, _ string) ([]storage.BackupSet, error) {
	return f.sets, f.setsErr
}

func (f *fakeRemoteLister) ListPackages(_ context.Context, setPrefix string) ([]storage.RemotePackage, error) {
	f.listedSetKeys = append(f.listedSetKeys, setPrefix)
	return f.packages[setPrefix], f.packagesErr
}

func (f *fakeRemoteLister) Put(context.Context, storage.Package, string) (storage.StoredPackage, error) {
	return storage.StoredPackage{}, errors.New("unused")
}

func (f *fakeRemoteLister) Delete(context.Context, string) error { return errors.New("unused") }

func (f *fakeRemoteLister) Probe(context.Context) error { return nil }

type fakeSnapshotLister struct {
	snapshots []incremental.Snapshot
	err       error
	gotRepo   incremental.RepoConfig
}

func (f *fakeSnapshotLister) ListSnapshots(_ context.Context, repo incremental.RepoConfig) ([]incremental.Snapshot, error) {
	f.gotRepo = repo
	return f.snapshots, f.err
}

// fakeLocalStore satisfies storage.Store but not RemoteLister, like
// internal/storage/local's store.
type fakeLocalStore struct{}

func (fakeLocalStore) Put(context.Context, storage.Package, string) (storage.StoredPackage, error) {
	return storage.StoredPackage{}, errors.New("unused")
}
func (fakeLocalStore) Delete(context.Context, string) error { return errors.New("unused") }
func (fakeLocalStore) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return nil, errors.New("unused")
}
func (fakeLocalStore) Probe(context.Context) error { return nil }
func (fakeLocalStore) LocalPath(key string) (string, error) {
	return "/srv/backups/" + key, nil
}

func fullSite() config.Site {
	return config.Site{Name: "site-a", Enabled: true, BackupMode: "full"}
}

func TestListFullModeReturnsNewestSetFirst(t *testing.T) {
	older := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC)
	remote := &fakeRemoteLister{
		sets: []storage.BackupSet{
			{Key: "bqckup/site-a/2026-11-10T03-00-00.000000000Z", CreatedAt: older},
			{Key: "bqckup/site-a/2026-11-11T04-00-00.000000000Z", CreatedAt: newer},
		},
		packages: map[string][]storage.RemotePackage{
			"bqckup/site-a/2026-11-10T03-00-00.000000000Z": {
				{Key: "bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz", Size: 100, CreatedAt: older.Add(time.Second)},
			},
			"bqckup/site-a/2026-11-11T04-00-00.000000000Z": {
				{Key: "bqckup/site-a/2026-11-11T04-00-00.000000000Z/files.tar.gz", Size: 200, CreatedAt: newer.Add(time.Second)},
			},
		},
	}
	lister := &Lister{}

	listing, err := lister.List(context.Background(), "s3-primary", fullSite(), config.Storage{Type: "s3"}, remote)
	require.NoError(t, err)
	require.Equal(t, "full", listing.Mode)
	require.Len(t, listing.Packages, 2)
	assert.Equal(t, "bqckup/site-a/2026-11-11T04-00-00.000000000Z/files.tar.gz", listing.Packages[0].Key)
	assert.Equal(t, int64(200), listing.Packages[0].Size)
	assert.Equal(t, "s3-primary", listing.Packages[0].Destination)
	assert.Equal(t, "bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz", listing.Packages[1].Key)
	// Newest set listed first.
	assert.Equal(t, []string{
		"bqckup/site-a/2026-11-11T04-00-00.000000000Z",
		"bqckup/site-a/2026-11-10T03-00-00.000000000Z",
	}, remote.listedSetKeys)
}

func TestListIncrementalModeTruncatesIDsAndNewestFirst(t *testing.T) {
	older := time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 12, 11, 6, 55, 47, 0, time.UTC)
	snapshots := &fakeSnapshotLister{snapshots: []incremental.Snapshot{
		{ID: strings.Repeat("a", 64), Paths: []string{"/old"}, Size: 10, CreatedAt: older},
		{ID: strings.Repeat("b", 64), Paths: []string{"/new", "/extra"}, Size: 20, CreatedAt: newer},
	}}
	envLookup := func(key string) (string, bool) {
		assert.Equal(t, "RESTIC_PASSWORD", key)
		return "secret", true
	}
	site := config.Site{
		Name: "site-b", Enabled: true, BackupMode: "incremental",
		Incremental: config.Incremental{Password: "RESTIC_PASSWORD"},
	}
	lister := &Lister{Snapshots: snapshots, EnvLookup: envLookup}

	listing, err := lister.List(context.Background(), "s3-primary", site, config.Storage{
		Type: "s3", Bucket: "backups", Prefix: "company",
	}, &fakeRemoteLister{})
	require.NoError(t, err)
	require.Equal(t, "incremental", listing.Mode)
	require.Len(t, listing.Snapshots, 2)
	assert.Equal(t, strings.Repeat("b", 8), listing.Snapshots[0].ID)
	assert.Equal(t, []string{"/new", "/extra"}, listing.Snapshots[0].Paths)
	assert.Equal(t, int64(20), listing.Snapshots[0].Size)
	assert.Equal(t, strings.Repeat("a", 8), listing.Snapshots[1].ID)
	assert.Equal(t, "secret", snapshots.gotRepo.Password)
	assert.Equal(t, "backups", snapshots.gotRepo.Bucket)
}

func TestListIncrementalRequiresPassword(t *testing.T) {
	site := config.Site{
		Name: "site-b", Enabled: true, BackupMode: "incremental",
		Incremental: config.Incremental{Password: "MISSING_PASSWORD_ENV"},
	}
	lister := &Lister{Snapshots: &fakeSnapshotLister{}, EnvLookup: func(string) (string, bool) { return "", false }}

	_, err := lister.List(context.Background(), "s3-primary", site, config.Storage{Type: "s3", Bucket: "backups"}, &fakeRemoteLister{})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(err))
}

func TestListRejectsLocalStoreIncrementalPointsToBackupSnapshots(t *testing.T) {
	lister := &Lister{Snapshots: &fakeSnapshotLister{}}

	_, err := lister.List(context.Background(), "local-primary", incrementalSite(), config.Storage{Type: "local"}, fakeLocalStore{})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "backup snapshots")
	assert.Contains(t, err.Error(), "--destination")
}

func TestListRejectsLocalStoreFullModeKeepsHistoryPointer(t *testing.T) {
	lister := &Lister{Snapshots: &fakeSnapshotLister{}}

	_, err := lister.List(context.Background(), "local-primary", fullSite(), config.Storage{Type: "local"}, fakeLocalStore{})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
	assert.Contains(t, err.Error(), "--details")
}

func TestListRejectsUnknownBackupMode(t *testing.T) {
	site := fullSite()
	site.BackupMode = "weird"
	_, err := (&Lister{}).List(context.Background(), "s3-primary", site, config.Storage{Type: "s3"}, &fakeRemoteLister{})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func incrementalSite() config.Site {
	return config.Site{
		Name: "site-b", Enabled: true, BackupMode: "incremental",
		Incremental: config.Incremental{Password: "RESTIC_PASSWORD"},
	}
}

func TestListSiteSnapshotsLocalDestinationSucceeds(t *testing.T) {
	snapshots := &fakeSnapshotLister{snapshots: []incremental.Snapshot{
		{ID: strings.Repeat("a", 64), Paths: []string{"/old"}, Size: 10, CreatedAt: time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)},
	}}
	envLookup := func(key string) (string, bool) {
		assert.Equal(t, "RESTIC_PASSWORD", key)
		return "secret", true
	}
	lister := &Lister{Snapshots: snapshots, EnvLookup: envLookup}

	listing, err := lister.ListSiteSnapshots(context.Background(), "local-primary", incrementalSite(), config.Storage{Type: "local", Directory: "/srv/repos"})
	require.NoError(t, err)
	require.Equal(t, "incremental", listing.Mode)
	require.Len(t, listing.Snapshots, 1)
	assert.Equal(t, strings.Repeat("a", 8), listing.Snapshots[0].ID)
	assert.Equal(t, "/srv/repos/restic/site-b", snapshots.gotRepo.URL)
	assert.Equal(t, "secret", snapshots.gotRepo.Password)
}

func TestListSiteSnapshotsRejectsFullMode(t *testing.T) {
	site := fullSite()
	_, err := (&Lister{Snapshots: &fakeSnapshotLister{}}).ListSiteSnapshots(context.Background(), "local-primary", site, config.Storage{Type: "local"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
	assert.Contains(t, err.Error(), "--details")
}

func TestListSiteSnapshotsRejectsUnknownMode(t *testing.T) {
	site := incrementalSite()
	site.BackupMode = "weird"
	_, err := (&Lister{Snapshots: &fakeSnapshotLister{}}).ListSiteSnapshots(context.Background(), "local-primary", site, config.Storage{Type: "local"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestListSiteSnapshotsRequiresPassword(t *testing.T) {
	site := incrementalSite()
	site.Incremental.Password = "MISSING_PASSWORD_ENV"
	lister := &Lister{Snapshots: &fakeSnapshotLister{}, EnvLookup: func(string) (string, bool) { return "", false }}

	_, err := lister.ListSiteSnapshots(context.Background(), "local-primary", site, config.Storage{Type: "local", Directory: "/srv/repos"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(err))
}

func TestListSiteSnapshotsKeepsEngineFailureRedacted(t *testing.T) {
	cause := errors.New("endpoint secret leaked here")
	lister := &Lister{Snapshots: &fakeSnapshotLister{err: apperror.Hide("engine failure", cause)}, EnvLookup: func(string) (string, bool) { return "secret", true }}

	_, err := lister.ListSiteSnapshots(context.Background(), "local-primary", incrementalSite(), config.Storage{Type: "local", Directory: "/srv/repos"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
	assert.NotContains(t, err.Error(), "secret")
	assert.ErrorIs(t, err, cause)
}

func TestListSiteSnapshotsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Lister{Snapshots: &fakeSnapshotLister{}}).ListSiteSnapshots(ctx, "local-primary", incrementalSite(), config.Storage{Type: "local"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestListKeepsStorageFailureRedacted(t *testing.T) {
	cause := errors.New("endpoint secret leaked here")
	remote := &fakeRemoteLister{setsErr: apperror.Hide("could not list remote backup sets", cause)}
	_, err := (&Lister{}).List(context.Background(), "s3-primary", fullSite(), config.Storage{Type: "s3"}, remote)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
	assert.NotContains(t, err.Error(), "secret")
	assert.ErrorIs(t, err, cause)
}
