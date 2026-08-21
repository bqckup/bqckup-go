package restic

type RepoConfig struct {
	URL             string
	Password        string
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	// Endpoint, Bucket and Prefix carry the S3-compatible connection for
	// the builtin engine (in memory only). URL remains the canonical
	// location string for the process adapter.
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
