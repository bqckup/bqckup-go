package incremental

import (
	"testing"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryURL(t *testing.T) {
	t.Run("s3 storage uses server and incremental backup path", func(t *testing.T) {
		storage := config.Storage{Type: "s3", Bucket: "backups", Prefix: "objtbackup"}
		url, err := RepositoryURL(storage, "website", "207.180.252.231")
		require.NoError(t, err)
		assert.Equal(t, "s3:s3.amazonaws.com/backups/objtbackup/bqckup/207.180.252.231/website/incremental-backup", url)
	})

	t.Run("s3 storage supports a prefixed server namespace", func(t *testing.T) {
		storage := config.Storage{Type: "s3", Bucket: "backups"}
		url, err := RepositoryURL(storage, "website", "hosting_client/194.233.87.182")
		require.NoError(t, err)
		assert.Equal(t, "s3:s3.amazonaws.com/backups/bqckup/hosting_client/194.233.87.182/website/incremental-backup", url)
	})

	t.Run("s3 storage supports a nested backup prefix", func(t *testing.T) {
		storage := config.Storage{Type: "s3", Bucket: "backups"}
		url, err := RepositoryURL(storage, "website", "hosting_client/production/45.146.6.26_7bjym")
		require.NoError(t, err)
		assert.Equal(t, "s3:s3.amazonaws.com/backups/bqckup/hosting_client/production/45.146.6.26_7bjym/website/incremental-backup", url)
	})

	t.Run("local storage", func(t *testing.T) {
		storage := config.Storage{Type: "local", Directory: "/var/backups/bqckup"}
		url, err := RepositoryURL(storage, "my-site")
		require.NoError(t, err)
		assert.Equal(t, "/var/backups/bqckup/restic/my-site", url)
	})

	t.Run("s3 storage with custom endpoint and prefix", func(t *testing.T) {
		storage := config.Storage{
			Type:     "s3",
			Endpoint: "https://minio.example.com",
			Bucket:   "backup-bucket",
			Prefix:   "prod",
		}
		url, err := RepositoryURL(storage, "my-site")
		require.NoError(t, err)
		assert.Equal(t, "s3:https://minio.example.com/backup-bucket/prod/restic/my-site", url)
	})

	t.Run("s3 storage with default endpoint and no prefix", func(t *testing.T) {
		storage := config.Storage{Type: "s3", Bucket: "aws-bucket"}
		url, err := RepositoryURL(storage, "my-site")
		require.NoError(t, err)
		assert.Equal(t, "s3:s3.amazonaws.com/aws-bucket/restic/my-site", url)
	})

	t.Run("r2 storage", func(t *testing.T) {
		storage := config.Storage{
			Type:     "r2",
			Endpoint: "https://account-id.r2.cloudflarestorage.com",
			Bucket:   "media-vault",
			Prefix:   "servers/node1",
		}
		url, err := RepositoryURL(storage, "wordpress")
		require.NoError(t, err)
		assert.Equal(t, "s3:https://account-id.r2.cloudflarestorage.com/media-vault/servers/node1/restic/wordpress", url)
	})
}
