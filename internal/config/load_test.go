package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAcceptsUnversionedStorageYAMLAndResolvesPrimary(t *testing.T) {
	site := strings.Replace(validSiteYAML(t), `  destinations:
    - storage: local-primary
`, "", 1)
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, `storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true
`, site)

	cfg, err := Load(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, []Destination{{Storage: "local-primary"}}, cfg.Sites[0].Destinations)
}

func TestLoadDefaultsMissingSchemaVersionsToV2(t *testing.T) {
	site := strings.TrimPrefix(validSiteYAML(t), "version: 2\n")
	dir := writeConfigTree(t, `app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, localStorageYAML, site)

	cfg, err := Load(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, cfg.Version)
	assert.Equal(t, SchemaVersion, cfg.Sites[0].SchemaVersion)
}

func TestLoadAcceptsStorageYML(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, `storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
`, validSiteYAML(t))
	require.NoError(t, os.Rename(
		filepath.Join(dir, "config", "storages.yaml"),
		filepath.Join(dir, "config", "storages.yml"),
	))

	_, err := Load(context.Background(), dir)
	require.NoError(t, err)
}

func TestLoadRejectsAmbiguousStorageExtensions(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, localStorageYAML, validSiteYAML(t))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "storages.yml"), []byte(localStorageYAML), 0o600))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both storages.yaml and storages.yml")
}

func TestLoadRejectsStorageDocumentVersion(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, `version: 2
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
`, validSiteYAML(t))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

const localStorageYAML = `storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
`

func TestLoadRejectsUnknownFieldWithFileAndPath(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
  mystery: true
`, localStorageYAML, validSiteYAML(t))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bqckup.yaml")
	assert.Contains(t, err.Error(), "mystery")
}

func TestLoadUsesOneSitePerFileAndResolvesRootPaths(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
  log_level: info
`, localStorageYAML, validSiteYAML(t))

	cfg, err := Load(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, cfg.Sites, 1)
	assert.Equal(t, filepath.Join(dir, "data/bqckup.db"), cfg.App.StateDatabase)
	assert.Equal(t, filepath.Join(dir, "tmp"), cfg.App.TemporaryDirectory)
	assert.Equal(t, filepath.Join(dir, "locks"), cfg.App.LockDirectory)
}

func writeConfigTree(t *testing.T, root, storages, site string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sites"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bqckup.yaml"), []byte(root), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "storages.yaml"), []byte(storages), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sites", "example.yaml"), []byte(site), 0o600))
	return dir
}

func validSiteYAML(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o700))
	return `version: 2
site:
  name: example
  enabled: true
  sources:
    files:
      include:
        - ` + source + `
      exclude: []
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
`
}
