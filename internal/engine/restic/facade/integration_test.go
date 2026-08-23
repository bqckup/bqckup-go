//go:build integration

package facade_test

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"

	backuprestic "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/facade"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDisposableIncrementalBackupS3Compatible exercises the complete native
// incremental path against an explicitly selected disposable S3-compatible
// storage. It is excluded from the default test suite and never embeds
// credentials. Set BQCKUP_S3_INTEGRATION_KEEP=1 to retain the dummy repository
// for manual inspection; otherwise every object created by the test is removed.
func TestDisposableIncrementalBackupS3Compatible(t *testing.T) {
	configDir := os.Getenv("BQCKUP_S3_INTEGRATION_CONFIG")
	storageName := os.Getenv("BQCKUP_S3_INTEGRATION_STORAGE")
	if configDir == "" || storageName == "" {
		t.Skip("S3 integration config is not selected")
	}

	ctx := context.Background()
	cfg, err := config.Load(ctx, configDir)
	require.NoError(t, err)
	selected, ok := cfg.Storages[storageName]
	require.True(t, ok, "selected integration storage does not exist")
	require.Contains(t, []string{"s3", "r2"}, selected.Type)
	keepRepository := os.Getenv("BQCKUP_S3_INTEGRATION_KEEP") == "1"
	repositoryPassword := os.Getenv("BQCKUP_RESTIC_INTEGRATION_PASSWORD")
	if repositoryPassword == "" {
		require.False(t, keepRepository, "BQCKUP_RESTIC_INTEGRATION_PASSWORD is required when retaining the dummy repository")
		repositoryPassword = uuid.NewString() + uuid.NewString()
	}

	siteName := "integration-" + uuid.NewString()
	prefix := path.Join(selected.Prefix, "integration", "incremental", siteName)
	repoConfig := backuprestic.RepoConfig{
		URL:             "s3:integration/" + selected.Bucket + "/" + prefix,
		Password:        repositoryPassword,
		AccessKeyID:     selected.AccessKeyID,
		SecretAccessKey: selected.SecretAccessKey,
		Region:          selected.Region,
		Endpoint:        selected.Endpoint,
		Bucket:          selected.Bucket,
		Prefix:          prefix,
	}
	repoBackend, err := backend.NewS3(ctx, backend.S3Options{
		Bucket: selected.Bucket, Endpoint: selected.Endpoint, Prefix: prefix,
		Region: selected.Region, AccessKeyID: selected.AccessKeyID,
		SecretAccessKey: selected.SecretAccessKey,
	})
	require.NoError(t, err)
	if keepRepository {
		t.Logf("retaining dummy repository at object prefix %s", prefix)
	} else {
		t.Cleanup(func() { require.NoError(t, removeRepository(context.Background(), repoBackend)) })
	}

	engine := facade.NewEngine()
	require.NoError(t, engine.EnsureRepository(ctx, repoConfig))
	require.NoError(t, engine.EnsureRepository(ctx, repoConfig), "repository init must be idempotent")

	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "keep.txt"), []byte("version one"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "skip.tmp"), []byte("excluded"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "cache", "deep"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "cache", "deep", "secret"), []byte("excluded"), 0o600))

	spec := backuprestic.BackupSpec{
		SiteName: siteName,
		Include:  []string{source},
		Exclude:  []string{"*.tmp", "cache/**"},
		Tags:     []string{"bqckup", "site:" + siteName},
	}
	first, err := engine.BackupFiles(ctx, repoConfig, spec)
	require.NoError(t, err)
	assert.NotEmpty(t, first.SnapshotID)
	assert.Equal(t, 1, first.TotalFilesProcessed)

	require.NoError(t, os.WriteFile(filepath.Join(source, "keep.txt"), []byte("version two"), 0o600))
	second, err := engine.BackupFiles(ctx, repoConfig, spec)
	require.NoError(t, err)
	assert.NotEqual(t, first.SnapshotID, second.SnapshotID)
	assert.Equal(t, 1, second.TotalFilesProcessed)

	opened, err := repository.Open(ctx, repoBackend, repoConfig.Password)
	require.NoError(t, err)
	snapshots, err := opened.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 2)

	_, err = engine.ApplyRetention(ctx, repoConfig, 1, siteName)
	require.NoError(t, err)
	opened, err = repository.Open(ctx, repoBackend, repoConfig.Password)
	require.NoError(t, err)
	snapshots, err = opened.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
}

func removeRepository(ctx context.Context, repoBackend backend.Backend) error {
	for _, fileType := range []restic.FileType{
		restic.LockFile,
		restic.SnapshotFile,
		restic.IndexFile,
		restic.DataFile,
		restic.KeyFileType,
		restic.ConfigFile,
	} {
		var handles []restic.Handle
		if err := repoBackend.List(ctx, fileType, func(handle restic.Handle, _ int64) error {
			handles = append(handles, handle)
			return nil
		}); err != nil {
			return err
		}
		for _, handle := range handles {
			if err := repoBackend.Remove(ctx, handle); err != nil {
				return err
			}
		}
	}
	return nil
}
