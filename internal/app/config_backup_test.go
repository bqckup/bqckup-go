package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/bqckup/bqckup-go/internal/storage/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncSiteConfigsUploadsAllSitesToUsedDestinations(t *testing.T) {
	root := t.TempDir()
	sitesDir := filepath.Join(root, "sites")
	require.NoError(t, os.MkdirAll(sitesDir, 0o700))
	enabledPath := filepath.Join(sitesDir, "enabled.yaml")
	disabledPath := filepath.Join(sitesDir, "disabled.yaml")
	require.NoError(t, os.WriteFile(enabledPath, []byte("site:\n  password: enabled-secret\n"), 0o600))
	require.NoError(t, os.WriteFile(disabledPath, []byte("site:\n  password: disabled-secret\n"), 0o600))

	usedRoot := filepath.Join(root, "used")
	used, err := local.New(usedRoot)
	require.NoError(t, err)
	archiveRoot := filepath.Join(root, "archive")
	archive, err := local.New(archiveRoot)
	require.NoError(t, err)
	unusedRoot := filepath.Join(root, "unused")
	unused, err := local.New(unusedRoot)
	require.NoError(t, err)
	configuration := config.Config{
		ServerID:     "194.233.66.13_all",
		BackupPrefix: "hosting_client",
		Sites: []config.Site{
			{Name: "enabled", Enabled: true, SourceFile: enabledPath, Destinations: []config.Destination{{Storage: "sgbucket"}}},
			{Name: "disabled", Enabled: false, SourceFile: disabledPath, Destinations: []config.Destination{{Storage: "archive"}}},
		},
	}

	err = syncSiteConfigs(context.Background(), configuration, map[string]storage.Store{
		"sgbucket": used,
		"archive":  archive,
		"unused":   unused,
	})
	require.NoError(t, err)

	configDir := filepath.Join(usedRoot, "bqckup", "hosting_client", "194.233.66.13_all", "config")
	enabled, err := os.ReadFile(filepath.Join(configDir, "enabled.yaml"))
	require.NoError(t, err)
	disabled, err := os.ReadFile(filepath.Join(configDir, "disabled.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(enabled), "enabled-secret")
	assert.Contains(t, string(disabled), "disabled-secret")
	for _, name := range []string{"enabled.yaml", "disabled.yaml"} {
		_, err = os.Stat(filepath.Join(archiveRoot, "bqckup", "hosting_client", "194.233.66.13_all", "config", name))
		require.NoError(t, err)
	}
	_, err = os.Stat(filepath.Join(unusedRoot, "bqckup"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, os.WriteFile(enabledPath, []byte("site:\n  password: updated-secret\n"), 0o600))
	require.NoError(t, syncSiteConfigs(context.Background(), configuration, map[string]storage.Store{"sgbucket": used, "archive": archive}))
	enabled, err = os.ReadFile(filepath.Join(configDir, "enabled.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(enabled), "updated-secret")
}
