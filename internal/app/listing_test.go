package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type appRemoteLister struct {
	appFakeStore
	sets     []storage.BackupSet
	packages []storage.RemotePackage
}

type appFakeStore struct{}

func (appFakeStore) Put(context.Context, storage.Package, string) (storage.StoredPackage, error) {
	return storage.StoredPackage{}, errors.New("unused")
}
func (appFakeStore) Delete(context.Context, string) error { return errors.New("unused") }
func (appFakeStore) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return nil, errors.New("unused")
}
func (appFakeStore) Probe(context.Context) error { return nil }

func (a *appRemoteLister) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return a.sets, nil
}
func (a *appRemoteLister) ListPackages(context.Context, string) ([]storage.RemotePackage, error) {
	return a.packages, nil
}

type appSnapshotLister struct {
	snapshots []restic.Snapshot
	gotRepo   restic.RepoConfig
}

func (a *appSnapshotLister) ListSnapshots(_ context.Context, repo restic.RepoConfig) ([]restic.Snapshot, error) {
	a.gotRepo = repo
	return a.snapshots, nil
}

func listingApp(t *testing.T, site config.Site, storages map[string]config.Storage, stores map[string]storage.Store) *App {
	t.Helper()
	return &App{
		configuration: config.Config{Sites: []config.Site{site}, Storages: storages},
		stores:        stores,
	}
}

func remoteSite() config.Site {
	return config.Site{
		Name: "site-a", Enabled: true, BackupMode: "full",
		Destinations: []config.Destination{{Storage: "s3-primary"}},
	}
}

func TestListRemoteContentsUnknownSiteFailsAsConfigError(t *testing.T) {
	application := listingApp(t, remoteSite(), map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": &appRemoteLister{}})
	_, err := application.ListRemoteContents(context.Background(), "missing", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestListRemoteContentsDisabledSiteFailsAsConfigError(t *testing.T) {
	site := remoteSite()
	site.Enabled = false
	application := listingApp(t, site, map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": &appRemoteLister{}})
	_, err := application.ListRemoteContents(context.Background(), "site-a", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestListRemoteContentsUnknownDestinationFailsAsConfigError(t *testing.T) {
	application := listingApp(t, remoteSite(), map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": &appRemoteLister{}})
	_, err := application.ListRemoteContents(context.Background(), "site-a", "missing-destination")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestListRemoteContentsDestinationNotUsedBySiteFailsAsConfigError(t *testing.T) {
	application := listingApp(t, remoteSite(), map[string]config.Storage{
		"s3-primary": {Type: "s3"},
		"s3-other":   {Type: "s3"},
	}, map[string]storage.Store{
		"s3-primary": &appRemoteLister{},
		"s3-other":   &appRemoteLister{},
	})
	_, err := application.ListRemoteContents(context.Background(), "site-a", "s3-other")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestListRemoteContentsLocalDestinationPointsToHistory(t *testing.T) {
	site := remoteSite()
	site.Destinations = []config.Destination{{Storage: "local-primary"}}
	application := listingApp(t, site, map[string]config.Storage{
		"local-primary": {Type: "local"},
	}, map[string]storage.Store{"local-primary": appFakeStore{}})
	_, err := application.ListRemoteContents(context.Background(), "site-a", "local-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
	assert.Contains(t, err.Error(), "--details")
}

func TestListRemoteContentsFullModeListsPackages(t *testing.T) {
	created := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	store := &appRemoteLister{
		sets: []storage.BackupSet{{Key: "bqckup/site-a/2026-11-10T03-00-00.000000000Z", CreatedAt: created}},
		packages: []storage.RemotePackage{
			{Key: "bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz", Size: 42, CreatedAt: created},
		},
	}
	application := listingApp(t, remoteSite(), map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": store})

	listing, err := application.ListRemoteContents(context.Background(), "site-a", "s3-primary")
	require.NoError(t, err)
	require.Len(t, listing.Packages, 1)
	assert.Equal(t, "s3-primary", listing.Packages[0].Destination)
	assert.Equal(t, int64(42), listing.Packages[0].Size)
}

func TestListRemoteContentsIncrementalModeWiresRepositoryConfig(t *testing.T) {
	site := remoteSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{PasswordEnv: "TEST_REPO_PASSWORD"}
	t.Setenv("TEST_REPO_PASSWORD", "secret")
	snapshots := &appSnapshotLister{snapshots: []restic.Snapshot{
		{ID: "0123456789abcdef", Paths: []string{"/var/www"}, Size: 7, CreatedAt: time.Now()},
	}}
	application := listingApp(t, site, map[string]config.Storage{
		"s3-primary": {Type: "s3", Bucket: "backups", Prefix: "company"},
	}, map[string]storage.Store{"s3-primary": &appRemoteLister{}})
	application.snapshots = snapshots

	listing, err := application.ListRemoteContents(context.Background(), "site-a", "s3-primary")
	require.NoError(t, err)
	require.Len(t, listing.Snapshots, 1)
	assert.Equal(t, "01234567", listing.Snapshots[0].ID)
	assert.Equal(t, "secret", snapshots.gotRepo.Password)
	assert.Equal(t, "backups", snapshots.gotRepo.Bucket)
	assert.Equal(t, "company/restic/site-a", snapshots.gotRepo.Prefix)
}

var _ backup.RemoteLister = (*appRemoteLister)(nil)
var _ backup.SnapshotLister = (*appSnapshotLister)(nil)
