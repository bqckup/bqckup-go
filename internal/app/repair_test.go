package app

import (
	"context"
	"errors"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type appIndexRepairer struct {
	gotRepo     restic.RepoConfig
	repairError error
	result      restic.RepairResult
}

func (r *appIndexRepairer) RepairIndex(_ context.Context, repo restic.RepoConfig) (restic.RepairResult, error) {
	r.gotRepo = repo
	return r.result, r.repairError
}

var _ backup.IndexRepairer = (*appIndexRepairer)(nil)

func repairApp(t *testing.T, site config.Site, repairer *appIndexRepairer) *App {
	t.Helper()
	application := &App{
		configuration: config.Config{
			Sites:    []config.Site{site},
			Storages: map[string]config.Storage{"s3-primary": {Type: "s3", Bucket: "backups", Prefix: "company"}},
		},
	}
	application.repairer = repairer
	return application
}

func TestRepairIndexUnknownSiteFailsAsConfigError(t *testing.T) {
	application := repairApp(t, remoteSite(), &appIndexRepairer{})
	_, err := application.RepairIndex(context.Background(), "missing", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestRepairIndexDisabledSiteFailsAsConfigError(t *testing.T) {
	site := incrementalSite()
	site.Enabled = false
	application := repairApp(t, site, &appIndexRepairer{})
	_, err := application.RepairIndex(context.Background(), "site-a", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestRepairIndexFullModeSiteFailsAsConfigError(t *testing.T) {
	application := repairApp(t, remoteSite(), &appIndexRepairer{})
	_, err := application.RepairIndex(context.Background(), "site-a", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "full backup mode")
	assert.Contains(t, err.Error(), "history list")
}

func TestRepairIndexUnknownDestinationFailsAsConfigError(t *testing.T) {
	application := repairApp(t, incrementalSite(), &appIndexRepairer{})
	_, err := application.RepairIndex(context.Background(), "site-a", "missing-destination")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestRepairIndexDestinationNotUsedBySiteFailsAsConfigError(t *testing.T) {
	site := incrementalSite()
	site.Destinations = []config.Destination{{Storage: "s3-other"}}
	application := &App{
		configuration: config.Config{
			Sites:    []config.Site{site},
			Storages: map[string]config.Storage{"s3-primary": {}, "s3-other": {}},
		},
		repairer: &appIndexRepairer{},
	}
	_, err := application.RepairIndex(context.Background(), "site-a", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestRepairIndexWiresRepositoryConfig(t *testing.T) {
	t.Setenv("TEST_REPO_PASSWORD", "secret")
	site := incrementalSite()
	repairer := &appIndexRepairer{result: restic.RepairResult{
		DurationSeconds:   2.5,
		PacksProcessed:    10,
		BlobsIndexed:      50,
		OldIndexesRemoved: 3,
		NewIndexesWritten: 1,
	}}
	application := repairApp(t, site, repairer)

	outcome, err := application.RepairIndex(context.Background(), "site-a", "s3-primary")
	require.NoError(t, err)
	assert.Equal(t, "site-a", outcome.Site)
	assert.Equal(t, "s3-primary", outcome.Destination)
	assert.Equal(t, "incremental", outcome.Mode)
	assert.Equal(t, "secret", repairer.gotRepo.Password)
	assert.Equal(t, "backups", repairer.gotRepo.Bucket)
	assert.Equal(t, "company/restic/site-a", repairer.gotRepo.Prefix)
	assert.Equal(t, 10, outcome.Result.PacksProcessed)
	assert.Equal(t, 50, outcome.Result.BlobsIndexed)
	assert.Equal(t, 3, outcome.Result.OldIndexesRemoved)
	assert.Equal(t, 1, outcome.Result.NewIndexesWritten)
}

func TestRepairIndexEngineErrorSurfaces(t *testing.T) {
	t.Setenv("TEST_REPO_PASSWORD", "secret")
	repairer := &appIndexRepairer{repairError: errors.New("engine down")}
	application := repairApp(t, incrementalSite(), repairer)
	_, err := application.RepairIndex(context.Background(), "site-a", "s3-primary")
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}
