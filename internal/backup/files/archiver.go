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

	"github.com/bqckup/bqckup-go/internal/backup"
)

type Archiver struct{}

func New() *Archiver { return &Archiver{} }

func (a *Archiver) Create(ctx context.Context, source backup.FileSource, destination string) (artifact backup.Artifact, err error) {
	if err := ctx.Err(); err != nil {
		return backup.Artifact{}, err
	}
	if len(source.Include) == 0 {
		return backup.Artifact{}, errors.New("archive requires at least one source path")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return backup.Artifact{}, fmt.Errorf("create archive directory: %w", err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backup.Artifact{}, fmt.Errorf("create archive: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = output.Close()
			_ = os.Remove(destination)
		}
	}()

	gz := gzip.NewWriter(output)
	tw := tar.NewWriter(gz)
	state := archiveState{ctx: ctx, writer: tw, source: source}
	rootNames := map[string]struct{}{}
	for _, include := range source.Include {
		clean := filepath.Clean(include)
		rootName := filepath.Base(clean)
		if rootName == "." || rootName == string(filepath.Separator) || rootName == "" {
			return backup.Artifact{}, fmt.Errorf("cannot archive source root %q", include)
		}
		if _, exists := rootNames[rootName]; exists {
			return backup.Artifact{}, fmt.Errorf("source archive name %q is duplicated", rootName)
		}
		rootNames[rootName] = struct{}{}
		if err := state.add(clean, rootName, map[string]bool{}); err != nil {
			return backup.Artifact{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return backup.Artifact{}, fmt.Errorf("finish tar archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return backup.Artifact{}, fmt.Errorf("finish gzip archive: %w", err)
	}
	if err := output.Sync(); err != nil {
		return backup.Artifact{}, fmt.Errorf("sync archive: %w", err)
	}
	if err := output.Close(); err != nil {
		return backup.Artifact{}, fmt.Errorf("close archive: %w", err)
	}

	checksum, size, err := checksumFile(destination)
	if err != nil {
		return backup.Artifact{}, err
	}
	success = true
	return backup.Artifact{
		Path: destination, Size: size, SHA256: checksum,
		SourceKind: "files", SourceName: "files",
	}, nil
}

type archiveState struct {
	ctx    context.Context
	writer *tar.Writer
	source backup.FileSource
}

func (s archiveState) add(realPath, archivePath string, active map[string]bool) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	realPath = filepath.Clean(realPath)
	if s.excluded(realPath) {
		return nil
	}
	info, err := os.Lstat(realPath)
	if err != nil {
		return fmt.Errorf("inspect archive source %s: %w", realPath, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(realPath)
		if err != nil {
			return fmt.Errorf("read symlink %s: %w", realPath, err)
		}
		if !s.source.FollowSymlinks {
			return s.writeHeader(info, archivePath, target)
		}
		resolved, err := filepath.EvalSymlinks(realPath)
		if err != nil {
			return fmt.Errorf("resolve symlink %s: %w", realPath, err)
		}
		return s.add(resolved, archivePath, active)
	}

	if info.IsDir() {
		canonical, err := filepath.EvalSymlinks(realPath)
		if err != nil {
			return fmt.Errorf("resolve directory %s: %w", realPath, err)
		}
		if active[canonical] {
			return fmt.Errorf("symlink directory cycle at %s", realPath)
		}
		active[canonical] = true
		defer delete(active, canonical)
		if err := s.writeHeader(info, archivePath+"/", ""); err != nil {
			return err
		}
		entries, err := os.ReadDir(realPath)
		if err != nil {
			return fmt.Errorf("read archive directory %s: %w", realPath, err)
		}
		for _, entry := range entries {
			if err := s.add(filepath.Join(realPath, entry.Name()), path.Join(archivePath, entry.Name()), active); err != nil {
				return err
			}
		}
		return nil
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported archive source type at %s", realPath)
	}
	if err := s.writeHeader(info, archivePath, ""); err != nil {
		return err
	}
	file, err := os.Open(realPath)
	if err != nil {
		return fmt.Errorf("open archive source %s: %w", realPath, err)
	}
	_, copyErr := io.Copy(s.writer, file)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("archive file %s: %w", realPath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close archive source %s: %w", realPath, closeErr)
	}
	return nil
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
	for _, excluded := range s.source.Exclude {
		excluded = filepath.Clean(excluded)
		if candidate == excluded || strings.HasPrefix(candidate, excluded+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func checksumFile(filename string) (string, int64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", 0, fmt.Errorf("open completed archive: %w", err)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("checksum archive: %w", copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("close completed archive: %w", closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
