package backup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func taggedSnapshot(id, tag string, createdAt time.Time) restic.Snapshot {
	return restic.Snapshot{ID: id, Paths: []string{"/data"}, Size: 10, CreatedAt: createdAt, Tags: []string{tag}}
}

func restoreSite() config.Site {
	return config.Site{
		Name: "site-b", Enabled: true, BackupMode: "incremental",
		Incremental: config.Incremental{PasswordEnv: "RESTIC_PASSWORD"},
		Sources:     config.Sources{Files: config.FileSource{Include: []string{"/srv/example/data"}}},
	}
}

func restoreEnvLookup() func(string) (string, bool) {
	return func(key string) (string, bool) {
		if key == "RESTIC_PASSWORD" {
			return "secret", true
		}
		return "", false
	}
}

func TestRestoreResolvesLatestBySiteTag(t *testing.T) {
	older := time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 12, 11, 6, 0, 0, 0, time.UTC)
	snapshots := &fakeSnapshotLister{snapshots: []restic.Snapshot{
		taggedSnapshot(strings.Repeat("a", 64), "site:site-b", older),
		taggedSnapshot(strings.Repeat("b", 64), "site:site-b", newer),
		taggedSnapshot(strings.Repeat("c", 64), "site:site-c", newer.Add(time.Hour)),
	}}
	engine := &fakeSnapshotRestorer{summary: restic.RestoreSummary{SnapshotID: strings.Repeat("b", 64)}}
	restorer := &Restorer{Snapshots: snapshots, Engine: engine, EnvLookup: restoreEnvLookup()}

	result, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "latest", "/tmp/restore", restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("b", 64), result.SnapshotID)
	assert.Equal(t, strings.Repeat("b", 64), engine.snapshotID)
}

func TestRestoreResolvesIDPrefix(t *testing.T) {
	full := "abcd1234" + strings.Repeat("f", 56)
	snapshots := &fakeSnapshotLister{snapshots: []restic.Snapshot{
		taggedSnapshot(full, "site:site-b", time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)),
		taggedSnapshot(strings.Repeat("e", 64), "site:site-b", time.Date(2026, 12, 11, 6, 0, 0, 0, time.UTC)),
	}}
	engine := &fakeSnapshotRestorer{summary: restic.RestoreSummary{SnapshotID: full}}
	restorer := &Restorer{Snapshots: snapshots, Engine: engine, EnvLookup: restoreEnvLookup()}

	_, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "abcd1234", "/tmp/restore", restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.NoError(t, err)
	assert.Equal(t, full, engine.snapshotID)
}

func TestRestoreUnknownSnapshotFails(t *testing.T) {
	snapshots := &fakeSnapshotLister{snapshots: []restic.Snapshot{
		taggedSnapshot(strings.Repeat("a", 64), "site:site-b", time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)),
	}}
	restorer := &Restorer{Snapshots: snapshots, Engine: &fakeSnapshotRestorer{}, EnvLookup: restoreEnvLookup()}

	_, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "ffff", "/tmp/restore", restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}

func TestRestoreAmbiguousPrefixFails(t *testing.T) {
	snapshots := &fakeSnapshotLister{snapshots: []restic.Snapshot{
		taggedSnapshot(strings.Repeat("a", 64), "site:site-b", time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)),
		taggedSnapshot("aaaa"+strings.Repeat("b", 60), "site:site-b", time.Date(2026, 12, 11, 6, 0, 0, 0, time.UTC)),
	}}
	restorer := &Restorer{Snapshots: snapshots, Engine: &fakeSnapshotRestorer{}, EnvLookup: restoreEnvLookup()}

	_, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "aaaa", "/tmp/restore", restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}

func TestRestoreRejectsFullMode(t *testing.T) {
	_, err := (&Restorer{Snapshots: &fakeSnapshotLister{}, Engine: &fakeSnapshotRestorer{}}).RestoreSiteSnapshot(context.Background(), "local-primary", "latest", "/tmp/restore", fullSite(), config.Storage{Type: "local"}, nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
	assert.Contains(t, err.Error(), "--details")
}

func TestRestoreRequiresPasswordEnv(t *testing.T) {
	site := restoreSite()
	site.Incremental.PasswordEnv = "MISSING_PASSWORD_ENV"
	restorer := &Restorer{Snapshots: &fakeSnapshotLister{}, Engine: &fakeSnapshotRestorer{}, EnvLookup: func(string) (string, bool) { return "", false }}

	_, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "latest", "/tmp/restore", site, config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(err))
}

func TestRestoreRejectsFileTarget(t *testing.T) {
	targetFile, err := os.CreateTemp(t.TempDir(), "target-*")
	require.NoError(t, err)
	require.NoError(t, targetFile.Close())

	restorer := &Restorer{Snapshots: &fakeSnapshotLister{}, Engine: &fakeSnapshotRestorer{}, EnvLookup: restoreEnvLookup()}
	_, err = restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "latest", targetFile.Name(), restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(err))
}

func TestRestorePassesConfiguredPaths(t *testing.T) {
	snapshots := &fakeSnapshotLister{snapshots: []restic.Snapshot{
		taggedSnapshot(strings.Repeat("a", 64), "site:site-b", time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)),
	}}
	engine := &fakeSnapshotRestorer{summary: restic.RestoreSummary{SnapshotID: strings.Repeat("a", 64)}}
	confirm := func([]string) error { return nil }
	restorer := &Restorer{Snapshots: snapshots, Engine: engine, EnvLookup: restoreEnvLookup()}

	_, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "latest", "/tmp/restore", restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, confirm)
	require.NoError(t, err)
	assert.Equal(t, []string{"/srv/example/data"}, engine.paths)
	assert.Equal(t, "/tmp/restore", engine.target)
	assert.NotNil(t, engine.confirm)
}

func TestRestorePassesConfirmErrorThrough(t *testing.T) {
	snapshots := &fakeSnapshotLister{snapshots: []restic.Snapshot{
		taggedSnapshot(strings.Repeat("a", 64), "site:site-b", time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)),
	}}
	engine := &fakeSnapshotRestorer{err: apperror.Wrap(apperror.CategoryCancellation, "restore cancelled by user", nil)}
	restorer := &Restorer{Snapshots: snapshots, Engine: engine, EnvLookup: restoreEnvLookup()}

	_, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "latest", "/tmp/restore", restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryCancellation, apperror.CategoryOf(err))
	assert.Equal(t, "restore cancelled by user", apperror.UserMessage(err))
}

func TestRestoreKeepsEngineFailureRedacted(t *testing.T) {
	cause := errors.New("endpoint secret leaked here")
	snapshots := &fakeSnapshotLister{snapshots: []restic.Snapshot{
		taggedSnapshot(strings.Repeat("a", 64), "site:site-b", time.Date(2026, 12, 10, 6, 0, 0, 0, time.UTC)),
	}}
	engine := &fakeSnapshotRestorer{err: apperror.Hide("engine failure", cause)}
	restorer := &Restorer{Snapshots: snapshots, Engine: engine, EnvLookup: restoreEnvLookup()}

	_, err := restorer.RestoreSiteSnapshot(context.Background(), "local-primary", "latest", "/tmp/restore", restoreSite(), config.Storage{Type: "local", Directory: "/srv/repos"}, nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
	assert.Equal(t, "could not restore the snapshot", apperror.UserMessage(err))
	assert.NotContains(t, err.Error(), "secret")
	assert.ErrorIs(t, err, cause)
}

func TestRestoreCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := &fakeSnapshotRestorer{}
	_, err := (&Restorer{Snapshots: &fakeSnapshotLister{}, Engine: engine, EnvLookup: restoreEnvLookup()}).RestoreSiteSnapshot(ctx, "local-primary", "latest", "/tmp/restore", restoreSite(), config.Storage{Type: "local"}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, engine.snapshotID)
}
