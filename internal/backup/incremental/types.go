package incremental

import "time"

type RepoConfig struct {
	URL             string
	Password        string
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	// Endpoint, Bucket and Prefix carry the S3-compatible connection in
	// memory only. URL remains the canonical repository location.
	Endpoint string
	Bucket   string
	Prefix   string // repository object-key prefix inside the bucket
}

type BackupSpec struct {
	SiteName string
	Include  []string
	Exclude  []string
	Tags     []string
}

type SnapshotSummary struct {
	SnapshotID          string  `json:"snapshot_id"`
	MessageType         string  `json:"message_type"`
	FilesNew            int     `json:"files_new"`
	FilesChanged        int     `json:"files_changed"`
	FilesUnmodified     int     `json:"files_unmodified"`
	TotalFilesProcessed int     `json:"total_files_processed"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	DataAdded           int64   `json:"data_added"`
	TotalDuration       float64 `json:"total_duration"`
}

// Snapshot is one snapshot listed for a repository. Size comes from the
// snapshot summary (TotalBytesProcessed); a snapshot without a summary has
// size 0 and renders as "-" in text output.
type Snapshot struct {
	ID        string
	Paths     []string
	Size      int64
	CreatedAt time.Time
	Tags      []string
}

// RestoreSummary is the result of one restore.
type RestoreSummary struct {
	SnapshotID      string   `json:"snapshot_id"`
	Target          string   `json:"target"`
	FilesRestored   int      `json:"files_restored"`
	BytesRestored   int64    `json:"bytes_restored"`
	SkippedPaths    []string `json:"skipped_paths,omitempty"`
	DurationSeconds float64  `json:"duration_seconds"`
}

// RestoreOverwrite is called once, before anything is written, with every
// existing path the restore would replace. A nil return means proceed; a
// non-nil error aborts the restore and is propagated unchanged.
type RestoreOverwrite func(conflicts []string) error
