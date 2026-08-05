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

func TestValidateEnabledDatabaseSources(t *testing.T) {
	tests := []struct {
		name    string
		db      DatabaseSource
		wantErr string
	}{
		{name: "mysql", db: validDatabase("mysql")},
		{name: "postgres", db: validDatabase("postgres")},
		{name: "unsupported engine", db: validDatabase("sqlite"), wantErr: "engine"},
		{name: "missing name", db: func() DatabaseSource { db := validDatabase("mysql"); db.Name = ""; return db }(), wantErr: "name"},
		{name: "missing host", db: func() DatabaseSource { db := validDatabase("mysql"); db.Host = ""; return db }(), wantErr: "host"},
		{name: "missing database", db: func() DatabaseSource { db := validDatabase("mysql"); db.Database = ""; return db }(), wantErr: "database"},
		{name: "missing username", db: func() DatabaseSource { db := validDatabase("mysql"); db.Username = ""; return db }(), wantErr: "username"},
		{name: "missing password", db: func() DatabaseSource { db := validDatabase("mysql"); db.Password = ""; return db }(), wantErr: "password"},
		{name: "invalid low port", db: func() DatabaseSource { db := validDatabase("mysql"); db.Port = 0; return db }(), wantErr: "port"},
		{name: "invalid high port", db: func() DatabaseSource { db := validDatabase("mysql"); db.Port = 65536; return db }(), wantErr: "port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.Sites[0].Sources.Databases = []DatabaseSource{test.db}
			err := cfg.Validate()
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
			assert.NotContains(t, err.Error(), "database-secret")
		})
	}
}

func TestValidateAllowsDisabledIncompleteDatabaseSource(t *testing.T) {
	cfg := validConfig(t)
	cfg.Sites[0].Sources.Databases = []DatabaseSource{{Enabled: false}}
	require.NoError(t, cfg.Validate())
}

func TestValidateRejectsDuplicateEnabledDatabaseNames(t *testing.T) {
	cfg := validConfig(t)
	first := validDatabase("mysql")
	second := validDatabase("postgres")
	second.Name = first.Name
	cfg.Sites[0].Sources.Databases = []DatabaseSource{first, second}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate database source name")
}

func TestLoadRejectsDatabaseCredentialFileWithoutMode0600(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, localStorageYAML, databaseSiteYAML(t, "database-secret"))
	sitePath := filepath.Join(dir, "sites", "example.yaml")
	require.NoError(t, os.Chmod(sitePath, 0o640))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode 0600")
	assert.NotContains(t, err.Error(), "database-secret")
}

func TestLoadRejectsDatabaseCredentialFileSymlink(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, localStorageYAML, databaseSiteYAML(t, "database-secret"))
	sitePath := filepath.Join(dir, "sites", "example.yaml")
	target := filepath.Join(dir, "private-example.yaml")
	require.NoError(t, os.Rename(sitePath, target))
	require.NoError(t, os.Symlink(target, sitePath))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be a symlink")
	assert.NotContains(t, err.Error(), "database-secret")
}

func validDatabase(engine string) DatabaseSource {
	return DatabaseSource{
		Name:     "application-" + engine,
		Enabled:  true,
		Engine:   engine,
		Host:     "localhost",
		Port:     3306,
		Database: "application",
		Username: "backup-user",
		Password: "database-secret",
	}
}

func databaseSiteYAML(t *testing.T, password string) string {
	t.Helper()
	site := strings.Replace(validSiteYAML(t), "    databases: []", `    databases:
      - name: application-mysql
        enabled: true
        engine: mysql
        host: localhost
        port: 3306
        database: application
        username: backup-user
        password: `+password, 1)
	return site
}
