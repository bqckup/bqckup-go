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

// Finding is one defect a repository check reported. Type is one of the
// finding kinds and ID names the object (its full hex storage ID, or the
// literal "config" for the config file). The optional fields appear only
// where the finding type defines them: missing and corrupt blobs carry the
// snapshot or the pack they were found under, a missing pack carries its
// index entry count, and detail holds a safe message only — never errors,
// paths, or secrets.
type Finding struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	PackID     string `json:"pack_id,omitempty"`
	BlobCount  int    `json:"blob_count,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// CheckResult is the outcome of one repository check. Status is "healthy"
// or "problems"; the counts report the objects examined and findings are
// always complete (never capped).
type CheckResult struct {
	ReadData        bool      `json:"read_data"`
	Status          string    `json:"status"`
	DurationSeconds float64   `json:"duration_seconds"`
	Indexes         int       `json:"indexes"`
	Snapshots       int       `json:"snapshots"`
	Packs           int       `json:"packs"`
	Blobs           int       `json:"blobs"`
	Findings        []Finding `json:"findings"`
}
