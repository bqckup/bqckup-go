// Package backend abstracts repository storage. Two implementations:
// the local filesystem (local.go) and S3-compatible object storage
// (s3.go, L3).
package backend

import (
	"context"
	"io"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

// FileInfo describes a stored file.
type FileInfo struct {
	Name string // storage name (hex ID; "config" for the config file)
	Size int64
}

// Backend is the storage contract all repository operations use.
type Backend interface {
	// Save stores rd under h atomically (tmp file, fsync, rename).
	Save(ctx context.Context, h restic.Handle, rd io.Reader) error
	// Load reads up to length bytes from offset and calls fn with the reader.
	// A length of 0 reads to the end of the file.
	Load(ctx context.Context, h restic.Handle, length int, offset int64, fn func(rd io.Reader) error) error
	// Stat returns file information, or an error for which IsNotExist is true.
	Stat(ctx context.Context, h restic.Handle) (FileInfo, error)
	// List calls fn for every file of the given type.
	List(ctx context.Context, t restic.FileType, fn func(h restic.Handle, size int64) error) error
	// Remove deletes one file. Removing a missing file is not an error.
	Remove(ctx context.Context, h restic.Handle) error
	// IsNotExist reports whether err means "no such file".
	IsNotExist(err error) bool
}
