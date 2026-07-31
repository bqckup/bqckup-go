package backup

type FileSource struct {
	Include        []string
	Exclude        []string
	FollowSymlinks bool
}

type Artifact struct {
	Path       string
	Size       int64
	SHA256     string
	SourceKind string
	SourceName string
}
