package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepositoryChecker struct {
	gotRepo incremental.RepoConfig
	gotRead bool
	result  incremental.CheckResult
	err     error
}

func (f *fakeRepositoryChecker) CheckRepository(_ context.Context, repo incremental.RepoConfig, readData bool) (incremental.CheckResult, error) {
	f.gotRepo = repo
	f.gotRead = readData
	return f.result, f.err
}

func checkerSite() config.Site {
	site := restoreSite()
	site.Name = "site-a"
	return site
}

func TestCheckerWiresEngineResultThrough(t *testing.T) {
	engine := &fakeRepositoryChecker{result: incremental.CheckResult{
		ReadData: true, Status: "problems", Indexes: 2, Snapshots: 1,
		Findings: []incremental.Finding{{Type: "broken_index", ID: "ab"}},
	}}
	checker := &Checker{Engine: engine}
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

type fakeCheckHistory struct {
	run      *history.BackupRun
	packages []history.Package
}

func (f *fakeCheckHistory) LastSuccessful(context.Context, string, time.Time) (*history.BackupRun, error) {
	return f.run, nil
}

func (f *fakeCheckHistory) RunPackages(context.Context, string) ([]history.Package, error) {
	return f.packages, nil
}

type fakePackageVerifier struct {
	keys     []string
	readData []bool
	err      error
}

func (f *fakePackageVerifier) VerifyPackage(_ context.Context, key string, _ int64, _ string, readData bool) error {
	f.keys = append(f.keys, key)
	f.readData = append(f.readData, readData)
	return f.err
}

func TestCheckerFullModeVerifiesLatestSuccessfulPackages(t *testing.T) {
	site := checkerSite()
	site.BackupMode = "full"
	verifier := &fakePackageVerifier{}
	checker := &Checker{
		History: &fakeCheckHistory{
			run: &history.BackupRun{ID: "run-1"},
			packages: []history.Package{
				{Destination: "local", ObjectKey: "bqckup/site-a/files.tar.gz", Size: 42, SHA256: "abcd"},
				{Destination: "other", ObjectKey: "ignored", Size: 1, SHA256: "efgh"},
			},
		},
		Verifier: verifier,
	}
	outcome, err := checker.CheckSite(context.Background(), "local", true, site, config.Storage{Type: "local"})
	require.NoError(t, err)
	assert.Equal(t, "full", outcome.Mode)
	assert.Equal(t, "healthy", outcome.Result.Status)
	assert.Equal(t, 1, outcome.Result.Packs)
	assert.Equal(t, []string{"bqckup/site-a/files.tar.gz"}, verifier.keys)
	assert.Equal(t, []bool{true}, verifier.readData)
}

func TestCheckerFullModeReportsPackageVerificationProblems(t *testing.T) {
	site := checkerSite()
	site.BackupMode = "full"
	checker := &Checker{
		History: &fakeCheckHistory{
			run:      &history.BackupRun{ID: "run-1"},
			packages: []history.Package{{Destination: "local", ObjectKey: "bqckup/site-a/files.tar.gz"}},
		},
		Verifier: &fakePackageVerifier{err: errors.New("size mismatch")},
	}
	outcome, err := checker.CheckSite(context.Background(), "local", false, site, config.Storage{Type: "local"})
	require.NoError(t, err)
	assert.Equal(t, "problems", outcome.Result.Status)
	require.Len(t, outcome.Result.Findings, 1)
	assert.Equal(t, "package_verification", outcome.Result.Findings[0].Type)
	assert.Contains(t, outcome.Result.Findings[0].Detail, "size mismatch")
}

func TestCheckerNilEngineIsInternalError(t *testing.T) {
	checker := &Checker{}
	_, err := checker.CheckSite(context.Background(), "s3-primary", false, checkerSite(), config.Storage{Type: "s3"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryInternal, apperror.CategoryOf(err))
}

func TestCheckerEngineErrorIsStorageCategory(t *testing.T) {
	engine := &fakeRepositoryChecker{err: errors.New("backend exploded")}
	checker := &Checker{Engine: engine}
	_, err := checker.CheckSite(context.Background(), "s3-primary", false, checkerSite(), config.Storage{Type: "s3", Bucket: "backups", Prefix: "company"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}
