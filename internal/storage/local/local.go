package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/ctxcopy"
	"github.com/bqckup/bqckup-go/internal/storage"
	"golang.org/x/sys/unix"
)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("local storage root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local storage root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create local storage root: %w", err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("secure local storage root: %w", err)
	}
	return &Store{root: absolute}, nil
}

func (s *Store) Put(ctx context.Context, pkg storage.Package, key string) (stored storage.StoredPackage, err error) {
	if err := ctx.Err(); err != nil {
		return stored, err
	}
	finalPath, err := s.resolve(key)
	if err != nil {
		return stored, err
	}
	if pkg.Size < 0 || len(pkg.SHA256) != sha256.Size*2 {
		return stored, errors.New("package size and SHA-256 are required")
	}
	if _, err := hex.DecodeString(pkg.SHA256); err != nil {
		return stored, fmt.Errorf("package SHA-256 is invalid: %w", err)
	}
	if _, err := os.Lstat(finalPath); err == nil {
		return stored, fmt.Errorf("storage object %q already exists", key)
	} else if !errors.Is(err, os.ErrNotExist) {
		return stored, fmt.Errorf("inspect storage object %q: %w", key, err)
	}

	parent := filepath.Dir(finalPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return stored, fmt.Errorf("create storage object directory: %w", err)
	}
	staging, err := os.CreateTemp(parent, ".bqckup-staging-*")
	if err != nil {
		return stored, fmt.Errorf("create storage staging file: %w", err)
	}
	stagingPath := staging.Name()
	defer func() {
		_ = staging.Close()
		_ = os.Remove(stagingPath)
	}()
	if err := staging.Chmod(0o600); err != nil {
		return stored, fmt.Errorf("secure storage staging file: %w", err)
	}

	source, err := os.Open(pkg.Path)
	if err != nil {
		return stored, fmt.Errorf("open package: %w", err)
	}
	defer source.Close()
	hash := sha256.New()
	size, err := ctxcopy.Copy(ctx, io.MultiWriter(staging, hash), source)
	if err != nil {
		return stored, err
	}
	if err := source.Close(); err != nil {
		return stored, fmt.Errorf("close package: %w", err)
	}
	actualSHA := hex.EncodeToString(hash.Sum(nil))
	if size != pkg.Size || !strings.EqualFold(actualSHA, pkg.SHA256) {
		return stored, fmt.Errorf("package verification failed: expected size %d SHA-256 %s", pkg.Size, pkg.SHA256)
	}
	if err := staging.Sync(); err != nil {
		return stored, fmt.Errorf("sync storage staging file: %w", err)
	}
	if err := staging.Close(); err != nil {
		return stored, fmt.Errorf("close storage staging file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return stored, err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stagingPath, unix.AT_FDCWD, finalPath, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return stored, fmt.Errorf("storage object %q already exists", key)
		}
		return stored, fmt.Errorf("finalize storage object %q: %w", key, err)
	}
	if err := syncDirectory(parent); err != nil {
		return stored, err
	}
	return storage.StoredPackage{Key: key, Size: size, SHA256: actualSHA}, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("delete storage object %q: %w", key, err)
		}
		return syncDirectory(filepath.Dir(target))
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect storage object %q: %w", key, statErr)
	}
	// New full-backup keys use a logical set prefix (date/time) while the
	// objects live directly below the date directory.
	dateDirectory := filepath.Dir(target)
	runPrefix := filepath.Base(target) + "-"
	entries, err := os.ReadDir(dateDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list storage objects %q: %w", key, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), runPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dateDirectory, entry.Name())); err != nil {
			return fmt.Errorf("delete storage object %q: %w", key, err)
		}
	}
	return syncDirectory(dateDirectory)
}

// Probe verifies the destination is writable by creating and immediately
// removing a temporary file. The error text is safe to print (local paths
// only).
func (s *Store) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.root, ".bqckup-probe-*")
	if err != nil {
		return fmt.Errorf("destination not writable: %w", err)
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name) // best effort; never return an error from Remove
	return nil
}

// LocalPath resolves an object key to its path on the local filesystem.
// Used by the download-link command to explain where a local file lives.
func (s *Store) LocalPath(key string) (string, error) {
	return s.resolve(key)
}

func (s *Store) ListBackupSets(ctx context.Context, sitePrefix string) ([]storage.BackupSet, error) {
	directory, err := s.resolve(sitePrefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []storage.BackupSet{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list backup sets %q: %w", sitePrefix, err)
	}
	setsByKey := make(map[string]storage.BackupSet)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		if createdAt, parseErr := storage.ParseBackupSet(entry.Name()); parseErr == nil {
			setKey := path.Join(sitePrefix, entry.Name())
			setsByKey[setKey] = storage.BackupSet{Key: setKey, CreatedAt: createdAt}
			continue
		}
		date, dateErr := time.Parse(storage.BackupDateLayout, entry.Name())
		if dateErr != nil || date.Format(storage.BackupDateLayout) != entry.Name() {
			date, dateErr = time.Parse("02-January-2006", entry.Name())
		}
		if dateErr != nil {
			continue
		}
		runs, readErr := os.ReadDir(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("list backup runs %q: %w", path.Join(sitePrefix, entry.Name()), readErr)
		}
		for _, run := range runs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if run.IsDir() {
				setName := path.Join(entry.Name(), run.Name())
				createdAt, parseErr := storage.ParseBackupSet(setName)
				if parseErr == nil {
					setKey := path.Join(sitePrefix, setName)
					setsByKey[setKey] = storage.BackupSet{Key: setKey, CreatedAt: createdAt}
				}
				continue
			}
			setName, createdAt, parseErr := storage.BackupSetForPackage(entry.Name(), run.Name())
			if parseErr != nil {
				continue
			}
			setKey := path.Join(sitePrefix, setName)
			setsByKey[setKey] = storage.BackupSet{Key: setKey, CreatedAt: createdAt}
		}
	}
	sets := make([]storage.BackupSet, 0, len(setsByKey))
	for _, set := range setsByKey {
		sets = append(sets, set)
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].CreatedAt.Before(sets[j].CreatedAt) })
	return sets, nil
}

func (s *Store) resolve(key string) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	resolved := filepath.Join(s.root, filepath.FromSlash(key))
	relative, err := filepath.Rel(s.root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("storage key %q escapes its root", key)
	}
	return resolved, nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open storage directory for sync: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync storage directory: %w", err)
	}
	return nil
}
