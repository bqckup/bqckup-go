package restic

import (
	"testing"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryURL(t *testing.T) {
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
