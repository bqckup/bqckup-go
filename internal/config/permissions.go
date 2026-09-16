package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FixCredentialFilePermissions sets mode 0600 on regular configuration files
// that contain inline credentials. It never follows symlinks or changes files
// without credentials.
func FixCredentialFilePermissions(ctx context.Context, dir string) ([]string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve config directory: %w", err)
	}
	changed := make([]string, 0, 3)
	rootPath := filepath.Join(dir, "bqckup.yaml")
	var root rootDocument
	if err := decode(rootPath, &root, nil); err != nil {
		return nil, err
	}
	if hasNotificationCredentials(root.Notifications) {
		if changedFile, err := tightenCredentialFile(rootPath); err != nil {
			return nil, err
		} else if changedFile {
			changed = append(changed, rootPath)
		}
	}
	storageFile, err := storagePath(dir)
	if err != nil {
		return nil, err
	}
	var stores storageDocument
	if err := decode(storageFile, &stores, nil); err != nil {
		return nil, err
	}
	if hasStorageCredentials(stores.Storages) {
		if changedFile, err := tightenCredentialFile(storageFile); err != nil {
			return nil, err
		} else if changedFile {
			changed = append(changed, storageFile)
		}
	}
	sitePaths, err := filepath.Glob(filepath.Join(dir, "sites", "*.yaml"))
	if err != nil {
		return nil, &Error{File: filepath.Join(dir, "sites"), Kind: ErrorRead, Err: err}
	}
	sort.Strings(sitePaths)
	for _, sitePath := range sitePaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var document siteDocument
		if err := decode(sitePath, &document, nil); err != nil {
			return nil, err
		}
		if !hasSiteCredentials(document.Site) {
			continue
		}
		if changedFile, err := tightenCredentialFile(sitePath); err != nil {
			return nil, err
		} else if changedFile {
			changed = append(changed, sitePath)
		}
	}
	return changed, nil
}

func tightenCredentialFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, &Error{File: path, Kind: ErrorRead, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, validationError(path, "permissions", "credential-bearing file must be a regular non-symlink file")
	}
	if info.Mode().Perm() == 0o600 {
		return false, nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return false, &Error{File: path, Kind: ErrorRead, Err: fmt.Errorf("set mode 0600: %w", err)}
	}
	return true, nil
}

func hasNotificationCredentials(notifications Notifications) bool {
	for _, channel := range notifications.Channels {
		if channel.Username != "" || channel.Password != "" || channel.URL != "" || channel.WebhookURL != "" {
			return true
		}
	}
	return false
}

func hasStorageCredentials(storages map[string]Storage) bool {
	for _, storage := range storages {
		if storage.AccessKeyID != "" || storage.SecretAccessKey != "" || storage.Credentials.URL != "" {
			return true
		}
	}
	return false
}

func hasSiteCredentials(site Site) bool {
	if site.Incremental.Password != "" {
		return true
	}
	for _, database := range site.Sources.Databases {
		if database.Password != "" {
			return true
		}
	}
	return false
}
