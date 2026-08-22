package backup

import (
	"context"

	"github.com/bqckup/bqckup-go/internal/config"
)

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

type Exporter interface {
	Export(ctx context.Context, source config.DatabaseSource, destination string) (Artifact, error)
}
