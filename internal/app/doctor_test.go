package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/storage/s3compat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenDoctorRecordsStorageConstructionFailures: a storage whose
// directory cannot be created must become a per-storage error, never a hard
// failure, so doctor reports the rest of the picture.
func TestOpenDoctorRecordsStorageConstructionFailures(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	roParent := filepath.Join(root, "ro-parent")
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "config"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sites"), 0o700))
	require.NoError(t, os.MkdirAll(roParent, 0o700))
	require.NoError(t, os.Chmod(roParent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roParent, 0o700) })

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "bqckup.yaml"), []byte("version: 2\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config", "storages.yaml"), fmt.Appendf(nil, `storages:
  local-primary:
    type: local
    directory: %s
`, filepath.Join(roParent, "child")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", "example.yaml"), []byte(`version: 2
site:
  name: example
  enabled: true
  sources:
    files:
      include: [/tmp]
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1h
    keep_last: 3
`), 0o600))

	checker, err := OpenDoctor(context.Background(), configDir)
	require.NoError(t, err)
	assert.Contains(t, checker.StoreErrs, "local-primary")
	assert.NotContains(t, checker.Stores, "local-primary")
}

// writeRemoteStorageFixture writes a config tree whose only storage is a
// remote S3 entry (empty bucket/keys: an unresolved remote entry must have
// those fields empty or config.Load rejects it) and whose site sends backups
// to it.
func writeRemoteStorageFixture(t *testing.T, providerURL string) (string, string) {
	t.Helper()
	configDir, backupRoot := writeApplicationConfig(t)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config", "storages.yaml"), fmt.Appendf(nil, `storages:
  remote:
    type: s3
    credentials:
      source: remote
      url: %s
`, providerURL), 0o600))
	sitePath := filepath.Join(configDir, "sites", "example.yaml")
	siteBody, err := os.ReadFile(sitePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sitePath, []byte(strings.Replace(string(siteBody), "local-primary", "remote", 1)), 0o600))
	return configDir, backupRoot
}

func TestOpenDoctorResolvesRemoteStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"bucket":"remote-bucket","access_key_id":"remote-key","secret_access_key":"remote-secret","endpoint":"https://s3.example.com","region":"us-east-1"}`))
	}))
	t.Cleanup(server.Close)
	configDir, _ := writeRemoteStorageFixture(t, server.URL)

	checker, err := OpenDoctor(t.Context(), configDir)
	require.NoError(t, err)
	storage := checker.Cfg.Storages["remote"]
	assert.Equal(t, "remote-bucket", storage.Bucket)
	assert.Equal(t, "remote-key", storage.AccessKeyID)
	assert.Equal(t, "remote-secret", storage.SecretAccessKey)
	assert.Empty(t, storage.Credentials) // resolver clears credentials
	assert.NotContains(t, checker.StoreErrs, "remote")
	assert.IsType(t, &s3compat.Store{}, checker.Stores["remote"]) // s3compat.New is network-free
}

func TestOpenDoctorRemoteProviderUnavailable(t *testing.T) {
	t.Run("unavailable provider", func(t *testing.T) {
		configDir, _ := writeRemoteStorageFixture(t, "https://127.0.0.1:1/storage")

		checker, err := OpenDoctor(t.Context(), configDir)
		require.NoError(t, err)
		require.Error(t, checker.LoadErr)
		assert.Contains(t, checker.LoadErr.Error(), "credentials.url")
	})
	t.Run("invalid url never leaks", func(t *testing.T) {
		configDir, _ := writeRemoteStorageFixture(t, "ht!tp://bad url")

		checker, err := OpenDoctor(t.Context(), configDir)
		require.NoError(t, err)
		require.Error(t, checker.LoadErr)
		assert.NotContains(t, checker.LoadErr.Error(), "bad url")
	})
}

func TestOpenDoctorRemoteProviderGarbage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Response missing bucket → validation must fail.
		_, _ = w.Write([]byte(`{"access_key_id":"garbage-key","secret_access_key":"garbage-secret","region":"us-east-1"}`))
	}))
	t.Cleanup(server.Close)
	configDir, _ := writeRemoteStorageFixture(t, server.URL)

	checker, err := OpenDoctor(t.Context(), configDir)
	require.NoError(t, err)
	errMsg := checker.StoreErrs["remote"].Error()
	assert.Contains(t, errMsg, "remote storage configuration is invalid")
	assert.Contains(t, errMsg, "bucket is required") // from validateRemoteRequiredFields
	assert.NotContains(t, errMsg, "garbage-key")
	assert.NotContains(t, errMsg, "garbage-secret")
}

func TestOpenDoctorMixedLocalAndRemote(t *testing.T) {
	configDir, backupRoot := writeApplicationConfig(t)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config", "storages.yaml"), fmt.Appendf(nil, `storages:
  local-primary:
    type: local
    directory: %s
  remote:
    type: s3
    credentials:
      source: remote
      url: https://127.0.0.1:1/storage
`, backupRoot), 0o600))
	sitePath := filepath.Join(configDir, "sites", "example.yaml")
	siteBody, err := os.ReadFile(sitePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sitePath, []byte(strings.Replace(string(siteBody), "    - storage: local-primary", "    - storage: local-primary\n    - storage: remote", 1)), 0o600))

	checker, err := OpenDoctor(t.Context(), configDir)
	require.NoError(t, err)
	assert.Contains(t, checker.Stores, "local-primary")
	require.Contains(t, checker.StoreErrs, "remote")
	assert.Equal(t, "remote storage configuration is unavailable", checker.StoreErrs["remote"].Error())

	report, err := checker.Run(t.Context(), "")
	require.NoError(t, err)
	assert.False(t, report.Passed)
	localOK, remoteFail := false, false
	for _, check := range report.Checks {
		if check.Name == "storage:local-primary" {
			localOK = check.Status == "ok"
		}
		if check.Name == "storage:remote" {
			remoteFail = check.Status == "fail" && check.Message == "remote storage configuration is unavailable"
		}
	}
	assert.True(t, localOK, "local storage must still probe")
	assert.True(t, remoteFail, "remote storage must fail with the safe message")
}
