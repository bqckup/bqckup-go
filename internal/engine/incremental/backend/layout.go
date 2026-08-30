package backend

import (
	"errors"
	"path/filepath"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
)

// Layout resolves repository paths. Verified against the official layout
// (restic-format-verification.md §2.1):
//
//	<repo>/config
//	<repo>/keys/<64hex>
//	<repo>/data/<xx>/<64hex>    (xx = first two hex chars)
//	<repo>/index/<64hex>
//	<repo>/snapshots/<64hex>
//	<repo>/locks/<64hex>
//	<repo>/tmp/
type Layout struct {
	Dir string
}

// Path returns the file path for a handle, or an error for an invalid name.
func (l Layout) Path(h incremental.Handle) (string, error) {
	if l.Dir == "" {
		return "", errors.New("backend layout: repository directory is empty")
	}
	// The config file is the only file stored under its own name at the root.
	if h.Type == incremental.ConfigFile {
		return filepath.Join(l.Dir, "config"), nil
	}
	dir, err := l.Dirname(h)
	if err != nil {
		return "", err
	}
	return filepath.Join(l.Dir, dir, h.Name), nil
}

// Dirname returns the subdirectory for a file type.
func (l Layout) Dirname(h incremental.Handle) (string, error) {
	if h.Name == "" && h.Type != incremental.ConfigFile {
		return "", errors.New("backend layout: file name must not be empty")
	}
	switch h.Type {
	case incremental.ConfigFile:
		return ".", nil
	case incremental.KeyFileType:
		return "keys", nil
	case incremental.IndexFile:
		return "index", nil
	case incremental.SnapshotFile:
		return "snapshots", nil
	case incremental.LockFile:
		return "locks", nil
	case incremental.DataFile:
		if len(h.Name) != 64 {
			return "", errors.New("backend layout: data file name must be 64 hex characters")
		}
		return filepath.Join("data", h.Name[:2]), nil
	default:
		return "", errors.New("backend layout: unknown file type")
	}
}
