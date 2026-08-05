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

func (s *Store) Put(ctx context.Context, artifact storage.Artifact, key string) (stored storage.StoredArtifact, err error) {
	if err := ctx.Err(); err != nil {
		return stored, err
	}
	finalPath, err := s.resolve(key)
	if err != nil {
		return stored, err
	}
	if artifact.Size < 0 || len(artifact.SHA256) != sha256.Size*2 {
		return stored, errors.New("artifact size and SHA-256 are required")
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil {
		return stored, fmt.Errorf("artifact SHA-256 is invalid: %w", err)
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

	source, err := os.Open(artifact.Path)
	if err != nil {
		return stored, fmt.Errorf("open artifact: %w", err)
	}
	defer source.Close()
	hash := sha256.New()
	size, err := copyWithContext(ctx, io.MultiWriter(staging, hash), source)
	if err != nil {
		return stored, err
	}
	if err := source.Close(); err != nil {
		return stored, fmt.Errorf("close artifact: %w", err)
	}
	actualSHA := hex.EncodeToString(hash.Sum(nil))
	if size != artifact.Size || !strings.EqualFold(actualSHA, artifact.SHA256) {
		return stored, fmt.Errorf("artifact verification failed: expected size %d SHA-256 %s", artifact.Size, artifact.SHA256)
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
	return storage.StoredArtifact{Key: key, Size: size, SHA256: actualSHA}, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("delete storage object %q: %w", key, err)
	}
	return syncDirectory(filepath.Dir(target))
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
	sets := make([]storage.BackupSet, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		createdAt, err := time.Parse(storage.TimestampLayout, entry.Name())
		if err != nil || createdAt.Location() != time.UTC {
			continue
		}
		sets = append(sets, storage.BackupSet{
			Key:       path.Join(sitePrefix, entry.Name()),
			CreatedAt: createdAt,
		})
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

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := destination.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, fmt.Errorf("write storage staging file: %w", writeErr)
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, fmt.Errorf("read artifact: %w", readErr)
		}
	}
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
