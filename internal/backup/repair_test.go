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

type fakeIndexRepairer struct {
	gotRepo restic.RepoConfig
	result  restic.RepairResult
	err     error
}

func (f *fakeIndexRepairer) RepairIndex(_ context.Context, repo restic.RepoConfig) (restic.RepairResult, error) {
	f.gotRepo = repo
	return f.result, f.err
}

func repairEnvLookup(key string) (string, bool) {
	if key == "RESTIC_PASSWORD" {
		return "secret", true
	}
	return "", false
}

func repairSite() config.Site {
	site := restoreSite()
	site.Name = "site-a"
	return site
}

func TestRepairerWiresEngineResultThrough(t *testing.T) {
	engine := &fakeIndexRepairer{result: restic.RepairResult{
		DurationSeconds:   1.5,
		PacksProcessed:    5,
		BlobsIndexed:      42,
		OldIndexesRemoved: 2,
		NewIndexesWritten: 1,
	}}
	repairer := &Repairer{Engine: engine, EnvLookup: repairEnvLookup}
	outcome, err := repairer.RepairSite(context.Background(), "s3-primary", repairSite(), config.Storage{
		Type: "s3", Bucket: "backups", Prefix: "company",
	})
	require.NoError(t, err)
	assert.Equal(t, "site-a", outcome.Site)
	assert.Equal(t, "s3-primary", outcome.Destination)
	assert.Equal(t, "incremental", outcome.Mode)
	assert.Equal(t, "secret", engine.gotRepo.Password)
	assert.Equal(t, "company/restic/site-a", engine.gotRepo.Prefix)
	assert.Equal(t, 5, outcome.Result.PacksProcessed)
	assert.Equal(t, 42, outcome.Result.BlobsIndexed)
	assert.Equal(t, 2, outcome.Result.OldIndexesRemoved)
	assert.Equal(t, 1, outcome.Result.NewIndexesWritten)
}

func TestRepairerFullModeSiteFailsAsConfigError(t *testing.T) {
	site := repairSite()
	site.BackupMode = "full"
	repairer := &Repairer{Engine: &fakeIndexRepairer{}}
	_, err := repairer.RepairSite(context.Background(), "local", site, config.Storage{Type: "local"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "history list")
}

func TestRepairerNilEngineIsInternalError(t *testing.T) {
	repairer := &Repairer{}
	_, err := repairer.RepairSite(context.Background(), "s3-primary", repairSite(), config.Storage{Type: "s3"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryInternal, apperror.CategoryOf(err))
}

func TestRepairerEngineErrorIsStorageCategory(t *testing.T) {
	engine := &fakeIndexRepairer{err: errors.New("backend exploded")}
	repairer := &Repairer{Engine: engine, EnvLookup: repairEnvLookup}
	_, err := repairer.RepairSite(context.Background(), "s3-primary", repairSite(), config.Storage{Type: "s3", Bucket: "backups", Prefix: "company"})
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}
