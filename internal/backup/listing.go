package backup

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
)

// RemoteLister is the narrow interface remote stores satisfy for listing.
// Only s3compat.Store implements it; a failed assertion on a local store
// is exactly the "local destinations are not listed" error.
type RemoteLister interface {
	ListBackupSets(ctx context.Context, sitePrefix string) ([]storage.BackupSet, error)
	ListArtifacts(ctx context.Context, setPrefix string) ([]storage.RemoteArtifact, error)
}

// SnapshotLister lists snapshots through the incremental engine.
type SnapshotLister interface {
	ListSnapshots(ctx context.Context, repo restic.RepoConfig) ([]restic.Snapshot, error)
}

// ArtifactRow is one stored object in a full-mode listing.
type ArtifactRow struct {
	Destination string
	Key         string
	Size        int64
	CreatedAt   time.Time
}

// SnapshotRow is one snapshot in an incremental listing. ID is truncated
// to 8 characters for display.
type SnapshotRow struct {
	ID        string
	Paths     []string
	Size      int64
	CreatedAt time.Time
}

// Listing is the result of listing one remote destination.
type Listing struct {
	Mode        string
	Site        string
	Destination string
	Artifacts   []ArtifactRow
	Snapshots   []SnapshotRow
}

// Lister lists the live remote contents of one destination. It never
// writes, deletes, or locks anything beyond the engine's short-lived
// non-exclusive snapshot lock.
type Lister struct {
	Snapshots SnapshotLister
	EnvLookup func(string) (string, bool)
}

func (l *Lister) lookupEnv(key string) (string, bool) {
	if l.EnvLookup != nil {
		return l.EnvLookup(key)
	}
	return os.LookupEnv(key)
}

// List branches on the site's backup mode: full lists archive artifacts
// under every UTC backup set, incremental lists snapshots through the
// engine. Local destinations fail as config errors: this command exists
// for remote destinations, local-only users have history.
func (l *Lister) List(ctx context.Context, destination string, site config.Site, storageConfig config.Storage, store storage.Store) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	remote, ok := store.(RemoteLister)
	if !ok {
		return Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"destination %q is local-only; use 'bqckup history list --site %s --details' to inspect stored archives",
			destination, site.Name), nil)
	}
	switch site.BackupMode {
	case "full":
		return l.listArtifacts(ctx, destination, site, remote)
	case "incremental":
		return l.listSnapshots(ctx, destination, site, storageConfig)
	default:
		return Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q has an unsupported backup mode %q", site.Name, site.BackupMode), nil)
	}
}

func (l *Lister) listArtifacts(ctx context.Context, destination string, site config.Site, remote RemoteLister) (Listing, error) {
	sets, err := remote.ListBackupSets(ctx, path.Join("bqckup", site.Name))
	if err != nil {
		return Listing{}, apperror.Wrap(apperror.CategoryStorage, "could not list remote backup sets", err)
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].CreatedAt.After(sets[j].CreatedAt) })
	listing := Listing{Mode: "full", Site: site.Name, Destination: destination}
	for _, set := range sets {
		artifacts, err := remote.ListArtifacts(ctx, set.Key)
		if err != nil {
			return Listing{}, apperror.Wrap(apperror.CategoryStorage, "could not list remote backup artifacts", err)
		}
		for _, artifact := range artifacts {
			listing.Artifacts = append(listing.Artifacts, ArtifactRow{
				Destination: destination,
				Key:         artifact.Key,
				Size:        artifact.Size,
				CreatedAt:   artifact.CreatedAt,
			})
		}
	}
	return listing, nil
}

func (l *Lister) listSnapshots(ctx context.Context, destination string, site config.Site, storageConfig config.Storage) (Listing, error) {
	if l.Snapshots == nil {
		return Listing{}, apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil)
	}
	repo, err := buildRepoConfig(site, storageConfig, l.lookupEnv, true)
	if err != nil {
		return Listing{}, apperror.Wrap(apperror.CategoryPreflight, "could not build repository configuration", err)
	}
	snapshots, err := l.Snapshots.ListSnapshots(ctx, repo)
	if err != nil {
		return Listing{}, apperror.Wrap(apperror.CategoryStorage, "could not list the incremental snapshots", err)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt) })
	listing := Listing{Mode: "incremental", Site: site.Name, Destination: destination}
	for _, snapshot := range snapshots {
		id := snapshot.ID
		if len(id) > 8 {
			id = id[:8]
		}
		listing.Snapshots = append(listing.Snapshots, SnapshotRow{
			ID:        id,
			Paths:     snapshot.Paths,
			Size:      snapshot.Size,
			CreatedAt: snapshot.CreatedAt,
		})
	}
	return listing, nil
}
