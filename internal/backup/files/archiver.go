package files

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/fileexclude"
)

type Archiver struct{}

const (
	missingRetryAttempts = 3
	missingRetryDelay    = 100 * time.Millisecond
)

func (a *Archiver) Create(ctx context.Context, source backup.FileSource, destination string) (backup.Package, error) {
	if err := ctx.Err(); err != nil {
		return backup.Package{}, err
	}
	if len(source.Include) == 0 {
		return backup.Package{}, errors.New("archive requires at least one source path")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return backup.Package{}, fmt.Errorf("create archive directory: %w", err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backup.Package{}, fmt.Errorf("create archive: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = output.Close()
			_ = os.Remove(destination)
		}
	}()

	digest := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(output, digest))
	tw := tar.NewWriter(gz)
	state := &archiveState{ctx: ctx, writer: tw, source: source}
	rootNames := map[string]struct{}{}
	preserveSourcePaths := len(source.Include) > 1
	for _, include := range source.Include {
		clean := filepath.Clean(include)
		rootName := filepath.Base(clean)
		if rootName == "." || rootName == string(filepath.Separator) || rootName == "" {
			return backup.Package{}, fmt.Errorf("cannot archive source root %q", include)
		}
		rootName = archiveRootName(include, rootName, rootNames, preserveSourcePaths)
		rootNames[rootName] = struct{}{}
		if err := state.add(clean, rootName, map[string]bool{}, false); err != nil {
			return backup.Package{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return backup.Package{}, fmt.Errorf("finish tar archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return backup.Package{}, fmt.Errorf("finish gzip archive: %w", err)
	}
	if err := output.Sync(); err != nil {
		return backup.Package{}, fmt.Errorf("sync archive: %w", err)
	}
	info, err := output.Stat()
	if err != nil {
		return backup.Package{}, fmt.Errorf("stat archive: %w", err)
	}
	if err := output.Close(); err != nil {
		return backup.Package{}, fmt.Errorf("close archive: %w", err)
	}

	success = true
	return backup.Package{
		Path: destination, Size: info.Size(), SHA256: hex.EncodeToString(digest.Sum(nil)),
		SourceKind: "files", SourceName: "files", FilesSkipped: state.filesSkipped,
	}, nil
}

func retryMissing[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; attempt < missingRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := operation()
		if err == nil || !errors.Is(err, os.ErrNotExist) || attempt == missingRetryAttempts-1 {
			return value, err
		}
		timer := time.NewTimer(missingRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, ctx.Err()
}

// archiveRootName gives multi-root archives descriptive, non-overlapping
// names. A single source keeps its basename for compatibility; multiple
// sources preserve their path below the filesystem root, e.g. etc/crowdsec.
func archiveRootName(include, base string, used map[string]struct{}, preservePath bool) string {
	if !preservePath {
		return base
	}
	candidate := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(include)), "/")
	if candidate != "" && candidate != "." {
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	// The exact same path was included more than once. Keep the archive
	// valid without silently overwriting an earlier member.
	return base + "-duplicate"
}

type archiveState struct {
	ctx          context.Context
	writer       *tar.Writer
	source       backup.FileSource
	filesSkipped int
}

func (s *archiveState) add(realPath, archivePath string, active map[string]bool, optional bool) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	realPath = filepath.Clean(realPath)
	if s.excluded(realPath) {
		return nil
	}
	info, err := retryMissing(s.ctx, func() (os.FileInfo, error) {
		return os.Lstat(realPath)
	})
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			s.filesSkipped++
			return nil
		}
		return fmt.Errorf("inspect archive source %s: %w", realPath, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := retryMissing(s.ctx, func() (string, error) {
			return os.Readlink(realPath)
		})
		if err != nil {
			if optional && errors.Is(err, os.ErrNotExist) {
				s.filesSkipped++
				return nil
			}
			return fmt.Errorf("read symlink %s: %w", realPath, err)
		}
		if !s.source.FollowSymlinks {
			return s.writeHeader(info, archivePath, target)
		}
		resolved, err := retryMissing(s.ctx, func() (string, error) {
			return filepath.EvalSymlinks(realPath)
		})
		if err != nil {
			if optional && errors.Is(err, os.ErrNotExist) {
				s.filesSkipped++
				return nil
			}
			return fmt.Errorf("resolve symlink %s: %w", realPath, err)
		}
		return s.add(resolved, archivePath, active, optional)
	}

	if info.IsDir() {
		canonical, err := retryMissing(s.ctx, func() (string, error) {
			return filepath.EvalSymlinks(realPath)
		})
		if err != nil {
			if optional && errors.Is(err, os.ErrNotExist) {
				s.filesSkipped++
				return nil
			}
			return fmt.Errorf("resolve directory %s: %w", realPath, err)
		}
		if active[canonical] {
			return fmt.Errorf("symlink directory cycle at %s", realPath)
		}
		active[canonical] = true
		defer delete(active, canonical)
		entries, err := retryMissing(s.ctx, func() ([]os.DirEntry, error) {
			return os.ReadDir(realPath)
		})
		if err != nil {
			if optional && errors.Is(err, os.ErrNotExist) {
				s.filesSkipped++
				return nil
			}
			return fmt.Errorf("read archive directory %s: %w", realPath, err)
		}
		if err := s.writeHeader(info, archivePath+"/", ""); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := s.add(filepath.Join(realPath, entry.Name()), path.Join(archivePath, entry.Name()), active, true); err != nil {
				return err
			}
		}
		return nil
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported archive source type at %s", realPath)
	}
	file, err := retryMissing(s.ctx, func() (*os.File, error) {
		return os.Open(realPath)
	})
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			s.filesSkipped++
			return nil
		}
		return fmt.Errorf("open archive source %s: %w", realPath, err)
	}
	if err := s.writeHeader(info, archivePath, ""); err != nil {
		_ = file.Close()
		return err
	}
	copyErr := copyArchiveFile(s.writer, file, info.Size())
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("archive file %s: %w", realPath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close archive source %s: %w", realPath, closeErr)
	}
	return nil
}

func copyArchiveFile(destination io.Writer, source io.Reader, size int64) error {
	_, err := io.CopyN(destination, source, size)
	return err
}

func (s archiveState) writeHeader(info os.FileInfo, archivePath, link string) error {
	name := path.Clean(filepath.ToSlash(archivePath))
	if strings.HasPrefix(name, "../") || name == ".." || path.IsAbs(name) {
		return fmt.Errorf("unsafe archive member path %q", archivePath)
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return fmt.Errorf("create archive header for %s: %w", archivePath, err)
	}
	header.Name = name
	if info.IsDir() && !strings.HasSuffix(header.Name, "/") {
		header.Name += "/"
	}
	if err := s.writer.WriteHeader(header); err != nil {
		return fmt.Errorf("write archive header for %s: %w", archivePath, err)
	}
	return nil
}

func (s archiveState) excluded(candidate string) bool {
	return fileexclude.MatchAny(s.source.Exclude, candidate, s.source.Include)
}
