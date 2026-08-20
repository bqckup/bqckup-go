// Package snapshot implements the snapshot document verified in
// restic-format-verification.md §2.9. Stored under snapshots/<sha256 of the
// encrypted bytes>.
package snapshot

import (
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

// Snapshot is one backup point in time.
type Snapshot struct {
	Time           time.Time  `json:"time"`
	Parent         *restic.ID `json:"parent,omitempty"`
	Tree           *restic.ID `json:"tree"`
	Paths          []string   `json:"paths"`
	Hostname       string     `json:"hostname,omitempty"`
	Username       string     `json:"username,omitempty"`
	UID            uint32     `json:"uid,omitempty"`
	GID            uint32     `json:"gid,omitempty"`
	Excludes       []string   `json:"excludes,omitempty"`
	Tags           []string   `json:"tags,omitempty"`
	Original       *restic.ID `json:"original,omitempty"`
	ProgramVersion string     `json:"program_version,omitempty"`
	Summary        *Summary   `json:"summary,omitempty"`
}

// Summary carries the backup statistics restic reports per snapshot. Only
// the fields this engine produces are modeled; unknown fields from upstream
// snapshots are dropped when parsing (we never reserialize parsed files).
type Summary struct {
	FilesNew            int     `json:"files_new"`
	FilesChanged        int     `json:"files_changed"`
	FilesUnmodified     int     `json:"files_unmodified"`
	DataBlobs           int     `json:"data_blobs"`
	TreeBlobs           int     `json:"tree_blobs"`
	DataAdded           uint64  `json:"data_added"`
	TotalFilesProcessed int     `json:"total_files_processed"`
	TotalBytesProcessed uint64  `json:"total_bytes_processed"`
	TotalDuration       float64 `json:"total_duration"`
	SnapshotID          string  `json:"snapshot_id,omitempty"`
}
