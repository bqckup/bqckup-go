package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
)

// linkGenerator is the narrow interface remote stores satisfy for download
// links. Only s3compat.Store implements it; a failed assertion on a local
// store is exactly the "local destinations have no download link" error.
type linkGenerator interface {
	PresignLink(ctx context.Context, key string, expires time.Duration) (storage.DownloadLink, error)
}

// localPathProvider resolves an object key to a local filesystem path, used
// only for the local-destination error message.
type localPathProvider interface {
	LocalPath(key string) (string, error)
}

// Linker creates download links for stored packages.
type Linker struct{}

func (l *Linker) Link(ctx context.Context, destination string, site config.Site, store storage.Store, key string, expires time.Duration) (storage.DownloadLink, error) {
	if err := ctx.Err(); err != nil {
		return storage.DownloadLink{}, err
	}
	generator, ok := store.(linkGenerator)
	if !ok {
		if provider, ok := store.(localPathProvider); ok {
			if localPath, err := provider.LocalPath(key); err == nil {
				return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
					"destination %q is local; the file is at %s and has no download link",
					destination, localPath), nil)
			}
		}
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"destination %q is local; local files have no download link", destination), nil)
	}
	if site.BackupMode != "full" {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses incremental backups; download links only apply to full-mode archive packages, use restore instead",
			site.Name), nil)
	}
	link, err := generator.PresignLink(ctx, key, expires)
	if err != nil {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryStorage, "could not create the download link", err)
	}
	return link, nil
}
