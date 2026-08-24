package restic

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
func RepositoryURL(storage config.Storage, siteName string) (string, error) {
	if siteName == "" {
		return "", errors.New("site name is required to build repository URL")
	}

	switch storage.Type {
	case "local":
		if storage.Directory == "" {
			return "", errors.New("local storage directory is required")
		}
		return filepath.Join(storage.Directory, "restic", siteName), nil
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

		subpath := "restic/" + siteName
		if storage.Prefix != "" {
			subpath = path.Join(storage.Prefix, subpath)
		}
		return "s3:" + base + "/" + subpath, nil
	default:
		return "", fmt.Errorf("unsupported storage type %q for restic", storage.Type)
	}
}
