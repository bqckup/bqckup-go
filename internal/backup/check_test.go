package backup

import (
	"context"
	"errors"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepositoryChecker struct {
	gotRepo restic.RepoConfig
	gotRead bool
	result  restic.CheckResult
	err     error
}

func (f *fakeRepositoryChecker) CheckRepository(_ context.Context, repo restic.RepoConfig, readData bool) (restic.CheckResult, error) {
	f.gotRepo = repo
	f.gotRead = readData
	return f.result, f.err
}

func checkerEnvLookup(key string) (string, bool) {
	if key == "RESTIC_PASSWORD" {
		return "secret", true
	}
	return "", false
}

func checkerSite() config.Site {
	site := restoreSite()
	site.Name = "site-a"
	return site
}

func TestCheckerWiresEngineResultThrough(t *testing.T) {
	engine := &fakeRepositoryChecker{result: restic.CheckResult{
		ReadData: true, Status: "problems", Indexes: 2, Snapshots: 1,
		Findings: []restic.Finding{{Type: "broken_index", ID: "ab"}},
	}}
	checker := &Checker{Engine: engine, EnvLookup: checkerEnvLookup}
	outcome, err := checker.CheckSite(context.Background(), "s3-primary", true, checkerSite(), config.Storage{
		Type: "s3", Bucket: "backups", Prefix: "company",
	})
	require.NoError(t, err)
	assert.Equal(t, "site-a", outcome.Site)
	assert.Equal(t, "incremental", outcome.Mode)
	assert.True(t, engine.gotRead)
	assert.Equal(t, "secret", engine.gotRepo.Password)
	assert.Equal(t, "company/restic/site-a", engine.gotRepo.Prefix)
	assert.Equal(t, "problems", outcome.Result.Status)
	require.Len(t, outcome.Result.Findings, 1)
}

func TestCheckerFullModeSitePointsAtHistory(t *testing.T) {
	site := checkerSite()
	site.BackupMode = "full"
	checker := &Checker{Engine: &fakeRepositoryChecker{}}
	_, err := checker.CheckSite(context.Background(), "local", false, site, config.Storage{Type: "local"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
}

func TestCheckerNilEngineIsInternalError(t *testing.T) {
	checker := &Checker{}
	_, err := checker.CheckSite(context.Background(), "s3-primary", false, checkerSite(), config.Storage{Type: "s3"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryInternal, apperror.CategoryOf(err))
}

func TestCheckerEngineErrorIsStorageCategory(t *testing.T) {
	engine := &fakeRepositoryChecker{err: errors.New("backend exploded")}
	checker := &Checker{Engine: engine, EnvLookup: checkerEnvLookup}
	_, err := checker.CheckSite(context.Background(), "s3-primary", false, checkerSite(), config.Storage{Type: "s3", Bucket: "backups", Prefix: "company"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}
