//go:build integration

package s3compat_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/bqckup/bqckup-go/internal/storage/s3compat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisposableS3CompatibleStorage(t *testing.T) {
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
	store, err := s3compat.New(ctx, s3compat.Options{
		Provider: s3compat.Provider(selected.Type), Bucket: selected.Bucket,
		Region: selected.Region, Endpoint: selected.Endpoint, Prefix: selected.Prefix,
		AccessKeyID: selected.AccessKeyID, SecretAccessKey: selected.SecretAccessKey,
	})
	require.NoError(t, err)

	site := "integration-" + uuid.NewString()
	setKey := path.Join("bqckup", site, time.Now().UTC().Format(storage.TimestampLayout))
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			require.NoError(t, store.Delete(context.Background(), setKey))
		}
	})
	contents := []byte("bqckup S3 integration probe")
	filename := filepath.Join(t.TempDir(), "probe.txt")
	require.NoError(t, os.WriteFile(filename, contents, 0o600))
	sum := sha256.Sum256(contents)
	artifact := storage.Artifact{Path: filename, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:])}

	stored, err := store.Put(ctx, artifact, path.Join(setKey, "probe.txt"))
	require.NoError(t, err)
	assert.Equal(t, artifact.SHA256, stored.SHA256)
	sets, err := store.ListBackupSets(ctx, path.Join("bqckup", site))
	require.NoError(t, err)
	require.Len(t, sets, 1)
	require.NoError(t, store.Delete(ctx, setKey))
	deleted = true
}
