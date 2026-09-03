package app

import (
	"context"
	"errors"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type appRepositoryChecker struct {
	gotRepo    incremental.RepoConfig
	gotRead    bool
	checkError error
	result     incremental.CheckResult
}

func incrementalSite() config.Site {
	site := remoteSite()
	site.BackupMode = "incremental"
	site.Incremental = config.Incremental{Password: "secret"}
	return site
}

func (c *appRepositoryChecker) CheckRepository(_ context.Context, repo incremental.RepoConfig, readData bool) (incremental.CheckResult, error) {
	c.gotRepo = repo
	c.gotRead = readData
	return c.result, c.checkError
}

var _ backup.RepositoryChecker = (*appRepositoryChecker)(nil)

func checkApp(t *testing.T, site config.Site, checker *appRepositoryChecker) *App {
	t.Helper()
	application := &App{
		configuration: config.Config{
			Sites:    []config.Site{site},
			Storages: map[string]config.Storage{"s3-primary": {Type: "s3", Bucket: "backups", Prefix: "company"}},
		},
	}
	application.checker = checker
	return application
}

func TestCheckRepositoryUnknownSiteFailsAsConfigError(t *testing.T) {
	application := checkApp(t, remoteSite(), &appRepositoryChecker{})
	_, err := application.CheckRepository(context.Background(), "missing", "s3-primary", false)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestCheckRepositoryDisabledSiteFailsAsConfigError(t *testing.T) {
	site := incrementalSite()
	site.Enabled = false
	application := checkApp(t, site, &appRepositoryChecker{})
	_, err := application.CheckRepository(context.Background(), "site-a", "s3-primary", false)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestCheckRepositoryFullModeSiteFailsAsConfigErrorPointingAtHistory(t *testing.T) {
	application := checkApp(t, remoteSite(), &appRepositoryChecker{})
	_, err := application.CheckRepository(context.Background(), "site-a", "s3-primary", false)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "full backup mode")
	assert.Contains(t, err.Error(), "history list")
}

func TestCheckRepositoryUnknownDestinationFailsAsConfigError(t *testing.T) {
	application := checkApp(t, incrementalSite(), &appRepositoryChecker{})
	_, err := application.CheckRepository(context.Background(), "site-a", "missing-destination", false)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestCheckRepositoryDestinationNotUsedBySiteFailsAsConfigError(t *testing.T) {
	site := incrementalSite()
	site.Destinations = []config.Destination{{Storage: "s3-other"}}
	application := &App{
		configuration: config.Config{
			Sites:    []config.Site{site},
			Storages: map[string]config.Storage{"s3-primary": {}, "s3-other": {}},
		},
		checker: &appRepositoryChecker{},
	}
	_, err := application.CheckRepository(context.Background(), "site-a", "s3-primary", false)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestCheckRepositoryWiresRepositoryConfigAndReadData(t *testing.T) {
	site := incrementalSite()
	checker := &appRepositoryChecker{result: incremental.CheckResult{Status: "problems", Findings: []incremental.Finding{
		{Type: "orphaned_pack", ID: "abcd"},
	}}}
	application := checkApp(t, site, checker)

	outcome, err := application.CheckRepository(context.Background(), "site-a", "s3-primary", true)
	require.NoError(t, err)
	assert.Equal(t, "site-a", outcome.Site)
	assert.Equal(t, "s3-primary", outcome.Destination)
	assert.Equal(t, "incremental", outcome.Mode)
	assert.True(t, checker.gotRead)
	assert.Equal(t, "secret", checker.gotRepo.Password)
	assert.Equal(t, "backups", checker.gotRepo.Bucket)
	assert.Equal(t, "company/restic/site-a", checker.gotRepo.Prefix)
	require.Len(t, outcome.Result.Findings, 1)
	assert.Equal(t, "orphaned_pack", outcome.Result.Findings[0].Type)
}

func TestCheckRepositoryEngineErrorSurfaces(t *testing.T) {
	checker := &appRepositoryChecker{checkError: errors.New("engine down")}
	application := checkApp(t, incrementalSite(), checker)
	_, err := application.CheckRepository(context.Background(), "site-a", "s3-primary", false)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}
