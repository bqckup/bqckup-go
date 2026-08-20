package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

// Local stores repository files on the local filesystem with atomic writes:
// stage in <repo>/tmp/, fsync, rename. Directories 0700, files 0600.
type Local struct {
	layout Layout
}

// NewLocal returns a backend for the repository at dir.
func NewLocal(dir string) *Local {
	return &Local{layout: Layout{Dir: dir}}
}

// CreateLayout creates all repository directories (0700), including all
// 256 data/<xx> subdirectories, like restic does at init.
func (b *Local) CreateLayout() error {
	dirs := []string{".", "keys", "index", "snapshots", "locks", "tmp", "data"}
	for i := 0; i < 256; i++ {
		dirs = append(dirs, filepath.Join("data", fmt.Sprintf("%02x", i)))
	}
	for _, dir := range dirs {
		if err := b.mkdirAll(dir); err != nil {
			return err
		}
	}
	return nil
}

// mkdirAll creates each missing component with mode 0700 (umask-proof).
func (b *Local) mkdirAll(dir string) error {
	path := filepath.Join(b.layout.Dir, filepath.FromSlash(dir))
	missing := []string{}
	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		if _, err := os.Stat(current); os.IsNotExist(err) {
			missing = append(missing, current)
			continue
		}
		break
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create directory: %w", err)
		}
		if err := os.Chmod(missing[i], 0o700); err != nil {
			return fmt.Errorf("secure directory: %w", err)
		}
	}
	return nil
}

func (b *Local) Save(ctx context.Context, h restic.Handle, rd io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := b.layout.Dirname(h)
	if err != nil {
		return err
	}
	if err := b.mkdirAll(dir); err != nil {
		return err
	}
	if err := b.mkdirAll("tmp"); err != nil {
		return err
	}
	finalPath, err := b.layout.Path(h)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Join(b.layout.Dir, "tmp"), "save-*")
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("secure staging file: %w", err)
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	if _, err := copyContext(ctx, tmp, rd); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync staging file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close staging file: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("move staged file into place: %w", err)
	}
	return nil
}

// copyContext copies rd to w, checking ctx between chunks.
func copyContext(ctx context.Context, w io.Writer, rd io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := rd.Read(buffer)
		if read > 0 {
			count, writeErr := w.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, fmt.Errorf("write staging file: %w", writeErr)
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func (b *Local) Load(ctx context.Context, h restic.Handle, length int, offset int64, fn func(rd io.Reader) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := b.layout.Path(h)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}
	var rd io.Reader = file
	if length > 0 {
		rd = io.LimitReader(file, int64(length))
	}
	return fn(rd)
}

func (b *Local) Stat(ctx context.Context, h restic.Handle) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	path, err := b.layout.Path(h)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	if !info.Mode().IsRegular() {
		return FileInfo{}, fmt.Errorf("%s is not a regular file", h.Name)
	}
	return FileInfo{Name: h.Name, Size: info.Size()}, nil
}

func (b *Local) List(ctx context.Context, t restic.FileType, fn func(h restic.Handle, size int64) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := b.layout.Dirname(restic.Handle{Type: t, Name: placeholder(t)})
	if err != nil {
		return err
	}
	base := filepath.Join(b.layout.Dir, dir)
	if t == restic.DataFile {
		for i := 0; i < 256; i++ {
			if err := b.listDir(ctx, filepath.Join(base, fmt.Sprintf("%02x", i)), t, fn); err != nil {
				return err
			}
		}
		return nil
	}
	return b.listDir(ctx, base, t, fn)
}

// placeholder returns a valid name so Dirname works for every type.
func placeholder(t restic.FileType) string {
	if t == restic.DataFile {
		return "0000000000000000000000000000000000000000000000000000000000000000"
	}
	return "x"
}

func (b *Local) listDir(ctx context.Context, dir string, t restic.FileType, fn func(h restic.Handle, size int64) error) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := fn(restic.Handle{Type: t, Name: entry.Name()}, info.Size()); err != nil {
			return err
		}
	}
	return nil
}

func (b *Local) Remove(ctx context.Context, h restic.Handle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := b.layout.Path(h)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (b *Local) IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
