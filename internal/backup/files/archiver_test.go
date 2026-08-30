package files

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateExcludesConfiguredSubtree(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "keep"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "cache"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "keep", "a.txt"), []byte("keep"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "cache", "b.txt"), []byte("drop"), 0o600))
	out := filepath.Join(t.TempDir(), "files.tar.gz")

	pkg, err := New().Create(context.Background(), backup.FileSource{
		Include: []string{source},
		Exclude: []string{filepath.Join(source, "cache")},
	}, out)

	require.NoError(t, err)
	assert.Equal(t, []string{"source/keep/a.txt"}, archiveMembers(t, out))
	assert.Len(t, pkg.SHA256, 64)
	assert.Positive(t, pkg.Size)
}

func TestCreateSupportsRelativeExcludePatterns(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "keep.txt"), []byte("keep"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "skip.tmp"), []byte("skip"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "cache", "deep"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "cache", "deep", "secret"), []byte("secret"), 0o600))

	destination := filepath.Join(t.TempDir(), "archive.tar.gz")
	_, err := New().Create(ctx, backup.FileSource{
		Include: []string{source},
		Exclude: []string{"*.tmp", "cache/**"},
	}, destination)
	require.NoError(t, err)

	names := archiveMembers(t, destination)
	assert.Contains(t, names, filepath.Base(source)+"/keep.txt")
	assert.NotContains(t, names, filepath.Base(source)+"/skip.tmp")
	assert.NotContains(t, names, filepath.Base(source)+"/cache/deep/secret")
}

func TestCreateDisambiguatesDuplicateSourceBasenames(t *testing.T) {
	parent := t.TempDir()
	first := filepath.Join(parent, "one", "crowdsec")
	second := filepath.Join(parent, "two", "crowdsec")
	require.NoError(t, os.MkdirAll(first, 0o700))
	require.NoError(t, os.MkdirAll(second, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(first, "first.txt"), []byte("first"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(second, "second.txt"), []byte("second"), 0o600))
	out := filepath.Join(t.TempDir(), "files.tar.gz")

	_, err := New().Create(context.Background(), backup.FileSource{
		Include: []string{first, second},
	}, out)
	require.NoError(t, err)

	assert.Equal(t, []string{
		filepath.ToSlash(strings.TrimPrefix(first, "/")) + "/first.txt",
		filepath.ToSlash(strings.TrimPrefix(second, "/")) + "/second.txt",
	}, archiveMembers(t, out))
}

func TestCreateStoresSymlinkWithoutFollowingByDefault(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "target.txt"), []byte("value"), 0o600))
	require.NoError(t, os.Symlink("target.txt", filepath.Join(source, "link.txt")))
	out := filepath.Join(t.TempDir(), "files.tar.gz")

	_, err := New().Create(context.Background(), backup.FileSource{Include: []string{source}}, out)
	require.NoError(t, err)

	entries := archiveEntries(t, out)
	assert.Equal(t, byte(tar.TypeSymlink), entries["source/link.txt"])
}

func TestCreateRemovesPartialOutputOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(t.TempDir(), "files.tar.gz")
	_, err := New().Create(ctx, backup.FileSource{Include: []string{t.TempDir()}}, out)
	require.ErrorIs(t, err, context.Canceled)
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func archiveMembers(t *testing.T, filename string) []string {
	t.Helper()
	entries := archiveEntries(t, filename)
	names := make([]string, 0, len(entries))
	for name, kind := range entries {
		if kind == tar.TypeReg || kind == tar.TypeRegA {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func archiveEntries(t *testing.T, filename string) map[string]byte {
	t.Helper()
	file, err := os.Open(filename)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	gz, err := gzip.NewReader(file)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gz.Close()) })
	reader := tar.NewReader(gz)
	entries := map[string]byte{}
	for {
		header, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
		}
		entries[header.Name] = header.Typeflag
	}
	return entries
}
