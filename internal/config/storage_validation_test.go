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

func TestValidateStorageTypes(t *testing.T) {
	tests := []struct {
		name    string
		storage Storage
		wantErr string
	}{
		{name: "local", storage: Storage{Type: "local", Directory: "/var/backups"}},
		{name: "s3", storage: Storage{Type: "s3", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "us-east-1"}},
		{name: "r2", storage: Storage{Type: "r2", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "auto", Endpoint: "https://account.r2.cloudflarestorage.com"}},
		{name: "local requires directory", storage: Storage{Type: "local"}, wantErr: "directory is required"},
		{name: "local rejects bucket", storage: Storage{Type: "local", Directory: "/var/backups", Bucket: "unexpected"}, wantErr: "bucket is not valid for local storage"},
		{name: "s3 requires bucket", storage: Storage{Type: "s3", AccessKeyID: "example", SecretAccessKey: "example", Region: "us-east-1"}, wantErr: "bucket is required"},
		{name: "s3 requires access key", storage: Storage{Type: "s3", Bucket: "backups", SecretAccessKey: "example", Region: "us-east-1"}, wantErr: "access_key_id is required"},
		{name: "s3 requires secret key", storage: Storage{Type: "s3", Bucket: "backups", AccessKeyID: "example", Region: "us-east-1"}, wantErr: "secret_access_key is required"},
		{name: "s3 requires region", storage: Storage{Type: "s3", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example"}, wantErr: "region is required"},
		{name: "s3 rejects directory", storage: Storage{Type: "s3", Directory: "/var/backups", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "us-east-1"}, wantErr: "directory is not valid for s3 storage"},
		{name: "r2 requires endpoint", storage: Storage{Type: "r2", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "auto"}, wantErr: "endpoint is required"},
		{name: "r2 requires auto region", storage: Storage{Type: "r2", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "us-east-1", Endpoint: "https://account.r2.cloudflarestorage.com"}, wantErr: "region must be auto"},
		{name: "unknown type", storage: Storage{Type: "ftp"}, wantErr: "type must be one of local, s3, or r2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.Storages = map[string]Storage{"testing": test.storage}
			cfg.Sites[0].Destinations = []Destination{{Storage: "testing"}}

			err := cfg.Validate()
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestValidateStorageEndpointsAndPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		prefix   string
		wantErr  string
	}{
		{name: "https endpoint", endpoint: "https://objects.example.invalid", prefix: "company/backups"},
		{name: "loopback http", endpoint: "http://127.0.0.1:9000", prefix: "company"},
		{name: "localhost http", endpoint: "http://localhost:9000"},
		{name: "rejects remote http", endpoint: "http://objects.example.invalid", wantErr: "endpoint must use HTTPS"},
		{name: "rejects credentials", endpoint: "https://user:password@objects.example.invalid", wantErr: "endpoint must not contain user information"},
		{name: "rejects query", endpoint: "https://objects.example.invalid?token=secret", wantErr: "endpoint must not contain a query or fragment"},
		{name: "rejects fragment", endpoint: "https://objects.example.invalid#secret", wantErr: "endpoint must not contain a query or fragment"},
		{name: "rejects malformed endpoint", endpoint: "://not-a-url", wantErr: "endpoint must be an absolute URL"},
		{name: "rejects absolute prefix", endpoint: "https://objects.example.invalid", prefix: "/company", wantErr: "prefix must be a safe relative object prefix"},
		{name: "rejects escaping prefix", endpoint: "https://objects.example.invalid", prefix: "company/../other", wantErr: "prefix must be a safe relative object prefix"},
		{name: "rejects backslash prefix", endpoint: "https://objects.example.invalid", prefix: `company\other`, wantErr: "prefix must be a safe relative object prefix"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.Storages = map[string]Storage{"testing": {
				Type:            "s3",
				Bucket:          "backups",
				AccessKeyID:     "example-access",
				SecretAccessKey: "example-secret",
				Region:          "us-east-1",
				Endpoint:        test.endpoint,
				Prefix:          test.prefix,
			}}
			cfg.Sites[0].Destinations = []Destination{{Storage: "testing"}}

			err := cfg.Validate()
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
			assert.NotContains(t, err.Error(), test.endpoint)
			assert.NotContains(t, err.Error(), "example-access")
			assert.NotContains(t, err.Error(), "example-secret")
		})
	}
}

func TestValidateRejectsMultiplePrimaryStorages(t *testing.T) {
	cfg := validConfig(t)
	cfg.Storages["local-primary"] = Storage{Type: "local", Directory: "/var/backups/one", Primary: true}
	cfg.Storages["local-secondary"] = Storage{Type: "local", Directory: "/var/backups/two", Primary: true}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most one storage may be primary")
}

func TestLoadRejectsCredentialFileWithoutMode0600(t *testing.T) {
	dir := writeRemoteConfigTree(t)
	storageFile := filepath.Join(dir, "config", "storages.yaml")
	require.NoError(t, os.Chmod(storageFile, 0o640))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must have mode 0600")
	assert.NotContains(t, err.Error(), "EXAMPLE_ACCESS_KEY")
	assert.NotContains(t, err.Error(), "EXAMPLE_SECRET_KEY")
}

func TestLoadRejectsCredentialFileSymlink(t *testing.T) {
	dir := writeRemoteConfigTree(t)
	storageFile := filepath.Join(dir, "config", "storages.yaml")
	target := filepath.Join(dir, "private-storages.yaml")
	require.NoError(t, os.Rename(storageFile, target))
	require.NoError(t, os.Symlink(target, storageFile))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be a symlink")
}

func TestLoadDefaultsR2RegionAndAcceptsLegacyPrimaryBoolean(t *testing.T) {
	dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, `storages:
  remote:
    type: r2
    bucket: example-bucket
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    endpoint: https://account.r2.cloudflarestorage.com
    primary: yes
`, siteYAMLWithDestination(t, "remote"))

	cfg, err := Load(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, "auto", cfg.Storages["remote"].Region)
	assert.True(t, cfg.Storages["remote"].Primary)
}

func writeRemoteConfigTree(t *testing.T) string {
	t.Helper()
	return writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
`, `storages:
  remote:
    type: s3
    bucket: example-bucket
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    region: us-east-1
`, siteYAMLWithDestination(t, "remote"))
}

func siteYAMLWithDestination(t *testing.T, storage string) string {
	t.Helper()
	return strings.Replace(validSiteYAML(t), "storage: local-primary", "storage: "+storage, 1)
}
