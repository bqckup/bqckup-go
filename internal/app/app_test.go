package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	restic "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/bqckup/bqckup-go/internal/storage/s3compat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDatabaseExportersPreflightsEnabledEngines(t *testing.T) {
	process := &fakeDatabaseProcessRunner{}
	configuration := config.Config{Sites: []config.Site{{Enabled: true, Sources: config.Sources{Databases: []config.DatabaseSource{
		{Enabled: true, Engine: "mysql", Name: "mysql-db"},
		{Enabled: true, Engine: "postgres", Name: "postgres-db"},
	}}}}}

	exporters, err := buildDatabaseExporters(context.Background(), configuration, process)
	require.NoError(t, err)
	assert.IsType(t, &databaseexporter.ProcessExporter{}, exporters["mysql"])
	assert.IsType(t, &databaseexporter.ProcessExporter{}, exporters["postgres"])
	assert.ElementsMatch(t, []string{"mysqldump", "pg_dump"}, process.lookups)
}

func TestBuildDatabaseExportersReturnsPreflightError(t *testing.T) {
	process := &fakeDatabaseProcessRunner{lookupErr: errors.New("binary not found")}
	configuration := config.Config{Sites: []config.Site{{Enabled: true, Sources: config.Sources{Databases: []config.DatabaseSource{
		{Enabled: true, Engine: "mysql", Name: "mysql-db"},
	}}}}}

	_, err := buildDatabaseExporters(context.Background(), configuration, process)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(err))
	assert.NotContains(t, err.Error(), "binary not found")
}

type fakeDatabaseProcessRunner struct {
	lookupErr error
	lookups   []string
}

func (f *fakeDatabaseProcessRunner) LookPath(command string) (string, error) {
	f.lookups = append(f.lookups, command)
	if f.lookupErr != nil {
		return "", f.lookupErr
	}
	return command, nil
}

func (*fakeDatabaseProcessRunner) Run(context.Context, process.ProcessSpec) error {
	return nil
}

func TestBuildStoresConstructsS3AndR2WithoutNetworkIO(t *testing.T) {
	stores, err := buildStores(context.Background(), map[string]config.Storage{
		"s3": {
			Type: "s3", Bucket: "example", Region: "us-east-1",
			AccessKeyID: "EXAMPLE_ACCESS", SecretAccessKey: "EXAMPLE_SECRET",
		},
		"r2": {
			Type: "r2", Bucket: "example", Region: "auto",
			Endpoint:    "https://example.r2.cloudflarestorage.com",
			AccessKeyID: "EXAMPLE_ACCESS", SecretAccessKey: "EXAMPLE_SECRET",
		},
	})
	require.NoError(t, err)
	assert.IsType(t, &s3compat.Store{}, stores["s3"])
	assert.IsType(t, &s3compat.Store{}, stores["r2"])
}

type fakeSnapshotLister struct {
	snapshots []restic.Snapshot
	err       error
	gotRepo   restic.RepoConfig
}

func (f *fakeSnapshotLister) ListSnapshots(_ context.Context, repo restic.RepoConfig) ([]restic.Snapshot, error) {
	f.gotRepo = repo
	return f.snapshots, f.err
}

func appWithSite(site config.Site) *App {
	return &App{
		configuration: config.Config{
			Sites: []config.Site{site},
			Storages: map[string]config.Storage{
				"local-primary": {Type: "local", Directory: "/srv/backups"},
			},
		},
		snapshots: &fakeSnapshotLister{},
	}
}

func TestListSiteSnapshotsUnknownSiteFails(t *testing.T) {
	_, err := appWithSite(config.Site{Name: "example", Enabled: true, BackupMode: "incremental"}).ListSiteSnapshots(context.Background(), "missing", "local-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "was not found")
}

func TestListSiteSnapshotsDisabledSiteFails(t *testing.T) {
	site := config.Site{Name: "example", Enabled: false, BackupMode: "incremental"}
	_, err := appWithSite(site).ListSiteSnapshots(context.Background(), "example", "local-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "disabled")
}

func TestListSiteSnapshotsFullModeFails(t *testing.T) {
	site := config.Site{Name: "example", Enabled: true, BackupMode: "full"}
	_, err := appWithSite(site).ListSiteSnapshots(context.Background(), "example", "local-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
}

func TestListSiteSnapshotsUnknownDestinationFails(t *testing.T) {
	site := config.Site{Name: "example", Enabled: true, BackupMode: "incremental", Destinations: []config.Destination{{Storage: "local-primary"}}}
	_, err := appWithSite(site).ListSiteSnapshots(context.Background(), "example", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "was not found")
}

func TestListSiteSnapshotsUnusedDestinationFails(t *testing.T) {
	site := config.Site{Name: "example", Enabled: true, BackupMode: "incremental", Destinations: []config.Destination{{Storage: "other-primary"}}}
	_, err := appWithSite(site).ListSiteSnapshots(context.Background(), "example", "local-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "does not send backups to destination")
}

func TestListSiteSnapshotsSucceedsWithLocalStorageDocument(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "secret")
	site := config.Site{
		Name: "example", Enabled: true, BackupMode: "incremental",
		Incremental:  config.Incremental{PasswordEnv: "RESTIC_PASSWORD"},
		Destinations: []config.Destination{{Storage: "local-primary"}},
	}
	lister := &fakeSnapshotLister{snapshots: []restic.Snapshot{{
		ID: "33e25d78", Paths: []string{"/var/www/html"}, Size: 2147483648,
	}}}
	application := &App{
		configuration: config.Config{
			Sites: []config.Site{site},
			Storages: map[string]config.Storage{
				"local-primary": {Type: "local", Directory: "/srv/backups"},
			},
		},
		snapshots: lister,
	}

	listing, err := application.ListSiteSnapshots(context.Background(), "example", "local-primary")
	require.NoError(t, err)
	assert.Equal(t, "incremental", listing.Mode)
	assert.Equal(t, "local-primary", listing.Destination)
	require.Len(t, listing.Snapshots, 1)
	assert.Equal(t, "/srv/backups/restic/example", lister.gotRepo.URL)
	assert.Equal(t, "secret", lister.gotRepo.Password)
}

func TestOpenWiresAWorkingLocalBackupApplication(t *testing.T) {
	configDir, backupRoot := writeApplicationConfig(t)
	application, err := Open(context.Background(), configDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, application.Close()) })

	result, err := application.RunBackup(context.Background(), "example", true)
	require.NoError(t, err)
	assert.Equal(t, "success", string(result.Status))

	matches, err := filepath.Glob(filepath.Join(backupRoot, "bqckup", "example", "*", "files.tar.gz"))
	require.NoError(t, err)
	assert.Len(t, matches, 1)
	runs, err := application.ListRuns(context.Background(), "example", 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Len(t, runs[0].Artifacts, 1)
}

func writeApplicationConfig(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	source := filepath.Join(root, "source")
	backupRoot := filepath.Join(root, "backups")
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "config"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sites"), 0o700))
	require.NoError(t, os.MkdirAll(source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "data.txt"), []byte("important"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "bqckup.yaml"), []byte(`version: 2
app:
  state_database: data/state.db
  temporary_directory: tmp
  lock_directory: locks
  log_level: info
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config", "storages.yaml"), fmt.Appendf(nil, `storages:
  local-primary:
    type: local
    directory: %s
`, backupRoot), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", "example.yaml"), fmt.Appendf(nil, `version: 2
site:
  name: example
  enabled: true
  sources:
    files:
      include: [%s]
      exclude: []
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1h
    keep_last: 3
`, source), 0o600))
	return configDir, backupRoot
}

type fakeSnapshotRestorer struct {
	snapshotID string
	paths      []string
	target     string
	confirm    restic.RestoreOverwrite
	summary    restic.RestoreSummary
	err        error
}

func (f *fakeSnapshotRestorer) RestoreSnapshot(_ context.Context, _ restic.RepoConfig, snapshotID string, paths []string, target string, confirm restic.RestoreOverwrite) (restic.RestoreSummary, error) {
	f.snapshotID, f.paths, f.target, f.confirm = snapshotID, paths, target, confirm
	return f.summary, f.err
}

func restoreSite() config.Site {
	return config.Site{
		Name: "example", Enabled: true, BackupMode: "incremental",
		Incremental:  config.Incremental{PasswordEnv: "RESTIC_PASSWORD"},
		Sources:      config.Sources{Files: config.FileSource{Include: []string{"/var/www/html"}}},
		Destinations: []config.Destination{{Storage: "local-primary"}},
	}
}

func appWithRestoreSite(site config.Site) *App {
	return &App{
		configuration: config.Config{
			Sites: []config.Site{site},
			Storages: map[string]config.Storage{
				"local-primary": {Type: "local", Directory: "/srv/backups"},
			},
		},
		snapshots: &fakeSnapshotLister{},
		restorer:  &fakeSnapshotRestorer{},
	}
}

func TestRestoreUnknownSiteFails(t *testing.T) {
	_, err := appWithRestoreSite(restoreSite()).RestoreSnapshot(context.Background(), "missing", "local-primary", "latest", "/tmp/restore", nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "was not found")
}

func TestRestoreDisabledSiteFails(t *testing.T) {
	site := restoreSite()
	site.Enabled = false
	_, err := appWithRestoreSite(site).RestoreSnapshot(context.Background(), "example", "local-primary", "latest", "/tmp/restore", nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "disabled")
}

func TestRestoreFullModeFails(t *testing.T) {
	site := restoreSite()
	site.BackupMode = "full"
	_, err := appWithRestoreSite(site).RestoreSnapshot(context.Background(), "example", "local-primary", "latest", "/tmp/restore", nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
}

func TestRestoreUnknownDestinationFails(t *testing.T) {
	_, err := appWithRestoreSite(restoreSite()).RestoreSnapshot(context.Background(), "example", "s3-primary", "latest", "/tmp/restore", nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "was not found")
}

func TestRestoreUnusedDestinationFails(t *testing.T) {
	site := restoreSite()
	site.Destinations = []config.Destination{{Storage: "other-primary"}}
	_, err := appWithRestoreSite(site).RestoreSnapshot(context.Background(), "example", "local-primary", "latest", "/tmp/restore", nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "does not send backups to destination")
}

func TestRestoreSucceedsThroughEngine(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "secret")
	site := restoreSite()
	lister := &fakeSnapshotLister{snapshots: []restic.Snapshot{{
		ID: strings.Repeat("a", 64), Paths: []string{"/var/www/html"}, Size: 100,
		Tags: []string{"site:example"},
	}}}
	engine := &fakeSnapshotRestorer{summary: restic.RestoreSummary{
		SnapshotID: strings.Repeat("a", 64), Target: "/tmp/restore", FilesRestored: 3,
	}}
	application := &App{
		configuration: config.Config{
			Sites: []config.Site{site},
			Storages: map[string]config.Storage{
				"local-primary": {Type: "local", Directory: "/srv/backups"},
			},
		},
		snapshots: lister,
		restorer:  engine,
	}

	confirm := func([]string) error { return nil }
	result, err := application.RestoreSnapshot(context.Background(), "example", "local-primary", "latest", "/tmp/restore", confirm)
	require.NoError(t, err)
	assert.Equal(t, 3, result.FilesRestored)
	assert.Equal(t, strings.Repeat("a", 64), engine.snapshotID)
	assert.Equal(t, "/tmp/restore", engine.target)
	assert.NotNil(t, engine.confirm)
}
