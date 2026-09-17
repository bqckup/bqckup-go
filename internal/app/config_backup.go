package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/ctxcopy"
	"github.com/bqckup/bqckup-go/internal/storage"
)

func syncSiteConfigs(ctx context.Context, configuration config.Config, stores map[string]storage.Store) error {
	destinationSet := make(map[string]struct{})
	for _, site := range configuration.Sites {
		for _, destination := range site.Destinations {
			destinationSet[destination.Storage] = struct{}{}
		}
	}
	destinations := make([]string, 0, len(destinationSet))
	for destination := range destinationSet {
		destinations = append(destinations, destination)
	}
	sort.Strings(destinations)

	var syncErr error
	for _, site := range configuration.Sites {
		if err := ctx.Err(); err != nil {
			return errors.Join(syncErr, err)
		}
		name := filepath.Base(site.SourceFile)
		pkg, err := siteConfigPackage(ctx, site.SourceFile, name)
		if err != nil {
			syncErr = errors.Join(syncErr, err)
			continue
		}
		key := path.Join("bqckup", configuration.BackupNamespace(), "config", name)
		for _, destination := range destinations {
			store, ok := stores[destination]
			if !ok {
				syncErr = errors.Join(syncErr, fmt.Errorf("configuration destination %q is unavailable", destination))
				continue
			}
			replacer, ok := store.(storage.Replacer)
			if !ok {
				syncErr = errors.Join(syncErr, fmt.Errorf("configuration destination %q does not support replacement", destination))
				continue
			}
			if _, err := replacer.Replace(ctx, pkg, key); err != nil {
				syncErr = errors.Join(syncErr, apperror.Hide(fmt.Sprintf("could not upload site configuration %q to %q", name, destination), err))
			}
		}
	}
	return syncErr
}

func siteConfigPackage(ctx context.Context, source, name string) (storage.Package, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return storage.Package{}, apperror.Hide(fmt.Sprintf("could not inspect site configuration %q", name), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return storage.Package{}, fmt.Errorf("site configuration %q must be a regular non-symlink file", name)
	}
	file, err := os.Open(source)
	if err != nil {
		return storage.Package{}, apperror.Hide(fmt.Sprintf("could not open site configuration %q", name), err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := ctxcopy.Copy(ctx, hash, file)
	if err != nil {
		return storage.Package{}, apperror.Hide(fmt.Sprintf("could not read site configuration %q", name), err)
	}
	if err := file.Close(); err != nil {
		return storage.Package{}, apperror.Hide(fmt.Sprintf("could not close site configuration %q", name), err)
	}
	return storage.Package{Path: source, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
