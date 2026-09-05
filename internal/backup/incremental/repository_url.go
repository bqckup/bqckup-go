package incremental

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/bqckup/bqckup-go/internal/config"
)

// RepositoryURL constructs the canonical repository location for a storage
// target. The built-in engine uses this value for local repositories and the
// explicit connection fields in RepoConfig for S3-compatible repositories.
func RepositoryPrefix(siteName, serverID string) string {
	if serverID == "" {
		return path.Join("restic", siteName)
	}
	return path.Join("bqckup", serverID, siteName, "incremental-backup")
}

func RepositoryURL(storage config.Storage, siteName string, serverID ...string) (string, error) {
	if siteName == "" {
		return "", errors.New("site name is required to build repository URL")
	}

	switch storage.Type {
	case "local":
		if storage.Directory == "" {
			return "", errors.New("local storage directory is required")
		}
		return filepath.Join(storage.Directory, RepositoryPrefix(siteName, serverIDValue(serverID))), nil
	case "s3", "r2":
		if storage.Bucket == "" {
			return "", errors.New("s3/r2 storage bucket is required")
		}
		var base string
		if storage.Endpoint != "" {
			endpoint := strings.TrimRight(storage.Endpoint, "/")
			if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
				endpoint = "https://" + endpoint
			}
			base = endpoint + "/" + storage.Bucket
		} else {
			base = "s3.amazonaws.com/" + storage.Bucket
		}

		subpath := RepositoryPrefix(siteName, serverIDValue(serverID))
		if storage.Prefix != "" {
			subpath = path.Join(storage.Prefix, subpath)
		}
		return "s3:" + base + "/" + subpath, nil
	default:
		return "", fmt.Errorf("unsupported storage type %q for restic", storage.Type)
	}
}

func serverIDValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
