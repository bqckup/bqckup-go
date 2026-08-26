package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutRejectsUnsafeKeys(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	artifact := sourceArtifact(t, []byte("backup"))

	for _, key := range []string{
		"../escape.tar.gz",
		"/absolute.tar.gz",
		"safe/../../escape.tar.gz",
		`safe\escape.tar.gz`,
		"safe//empty.tar.gz",
		"",
	} {
		t.Run(key, func(t *testing.T) {
			_, putErr := store.Put(context.Background(), artifact, key)
			require.Error(t, putErr)
		})
	}
}

func TestPutCancelledRemovesStagingFiles(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	require.NoError(t, err)
	artifact := sourceArtifact(t, []byte("backup"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.Put(ctx, artifact, "bqckup/site/2026-07-23T03-45-00.000000000Z/files.tar.gz")
	require.ErrorIs(t, err, context.Canceled)

	var files []string
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	}))
	assert.Empty(t, files)
}

// TestPutSameSecondRunsDoNotCollide: two runs in the same second (--force
// rerun, cron plus manual overlap) must not collide on the same object
// key; the timestamp layout carries nanosecond resolution.
func TestPutSameSecondRunsDoNotCollide(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)

	for _, nsec := range []int{0, 123_456_789} {
		timestamp := time.Date(2026, 7, 23, 3, 45, 0, nsec, time.UTC).Format(storage.TimestampLayout)
		key := path.Join("bqckup", "site", timestamp, "files.tar.gz")
		stored, putErr := store.Put(context.Background(), sourceArtifact(t, []byte("archive")), key)
		require.NoError(t, putErr, "run at %s must be storable", timestamp)
		assert.Equal(t, key, stored.Key)
	}
}

func TestPutDoesNotOverwriteExistingObject(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	require.NoError(t, err)
	artifact := sourceArtifact(t, []byte("new"))
	key := "bqckup/site/2026-07-23T03-45-00.000000000Z/files.tar.gz"
	finalPath := filepath.Join(root, filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(filepath.Dir(finalPath), 0o700))
	require.NoError(t, os.WriteFile(finalPath, []byte("existing"), 0o600))

	_, err = store.Put(context.Background(), artifact, key)
	require.Error(t, err)
	contents, readErr := os.ReadFile(finalPath)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("existing"), contents)
}

func TestPutPersistsVerifiedArtifactWithPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	require.NoError(t, err)
	contents := []byte("verified backup")
	artifact := sourceArtifact(t, contents)
	key := "bqckup/site/2026-07-23T03-45-00.000000000Z/files.tar.gz"

	stored, err := store.Put(context.Background(), artifact, key)
	require.NoError(t, err)
	assert.Equal(t, key, stored.Key)
	assert.Equal(t, artifact.SHA256, stored.SHA256)
	assert.EqualValues(t, len(contents), stored.Size)

	finalPath := filepath.Join(root, filepath.FromSlash(key))
	actual, err := os.ReadFile(finalPath)
	require.NoError(t, err)
	assert.Equal(t, contents, actual)
	info, err := os.Stat(finalPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestListBackupSetsRecognizesOnlyUTCApplicationTimestamps(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	require.NoError(t, err)
	for _, name := range []string{
		"2026-07-22T03-45-00.000000000Z",
		"2026-07-23T03-45-00.000000000Z",
		"2026-07-23T03:45:00Z",
		"notes",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "bqckup", "site", name), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "bqckup", "site", "2026-07-24T03-45-00.000000000Z"), []byte("file"), 0o600))

	sets, err := store.ListBackupSets(context.Background(), "bqckup/site")
	require.NoError(t, err)
	require.Len(t, sets, 2)
	assert.Equal(t, "bqckup/site/2026-07-22T03-45-00.000000000Z", sets[0].Key)
	assert.Equal(t, "bqckup/site/2026-07-23T03-45-00.000000000Z", sets[1].Key)
}

func sourceArtifact(t *testing.T, contents []byte) storage.Artifact {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "artifact.tar.gz")
	require.NoError(t, os.WriteFile(filename, contents, 0o600))
	sum := sha256.Sum256(contents)
	return storage.Artifact{
		Path:   filename,
		Size:   int64(len(contents)),
		SHA256: hex.EncodeToString(sum[:]),
	}
}

func TestLocalPathJoinsKeyUnderRoot(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)

	resolved, err := store.LocalPath("bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(store.root, "bqckup", "site-a", "2026-08-05T00-00-00Z", "files.tar.gz"), resolved)
}

func TestLocalPathRejectsUnsafeKeys(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)

	_, err = store.LocalPath("../outside/files.tar.gz")
	require.Error(t, err)
}
