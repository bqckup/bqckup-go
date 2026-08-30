package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
)

// RemoteLister is the narrow interface remote stores satisfy for listing.
// Only s3compat.Store implements it; a failed assertion on a local store
// is exactly the "local destinations are not listed" error.
type RemoteLister interface {
	ListBackupSets(ctx context.Context, sitePrefix string) ([]storage.BackupSet, error)
	ListPackages(ctx context.Context, setPrefix string) ([]storage.RemotePackage, error)
}

// SnapshotLister lists snapshots through the incremental engine.
type SnapshotLister interface {
	ListSnapshots(ctx context.Context, repo incremental.RepoConfig) ([]incremental.Snapshot, error)
}

// PackageRow is one stored object in a full-mode listing.
type PackageRow struct {
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
	Mode             string
	Site             string
	Destination      string
	Packages         []PackageRow
	Snapshots        []SnapshotRow
	DatabasePackages []PackageRow
}

// Lister lists the live remote contents of one destination. It never
// writes, deletes, or locks anything beyond the engine's short-lived
// non-exclusive snapshot lock.
type Lister struct {
	ServerID  string
	Snapshots SnapshotLister
}

// List branches on the site's backup mode: full lists stored packages
// under every UTC backup set, incremental lists snapshots through the
// engine. Local destinations fail as config errors: this command exists
// for remote destinations, local-only users have history.
func (l *Lister) List(ctx context.Context, destination string, site config.Site, storageConfig config.Storage, store storage.Store) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	remote, ok := store.(RemoteLister)
	if !ok {
		if site.BackupMode == "incremental" {
			return Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
				"destination %q is local-only; use 'bqckup backup snapshots %s --destination %q' to list snapshots",
				destination, site.Name, destination), nil)
		}
		return Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"destination %q is local-only; use 'bqckup history list --site %s --details' to inspect stored archives",
			destination, site.Name), nil)
	}
	switch site.BackupMode {
	case "full":
		return l.listPackages(ctx, destination, site, remote)
	case "incremental":
		return l.listSnapshots(ctx, destination, site, storageConfig, remote)
	default:
		return Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q has an unsupported backup mode %q", site.Name, site.BackupMode), nil)
	}
}

// ListSiteSnapshots lists the live snapshots of an incremental site
// directly from its repository, for any destination type. Unlike List it
// never asserts a RemoteLister: local repositories are listed too. Non-
// incremental sites (full mode, or the unset default that runs as full)
// are a config error pointing at history, which stays the run ledger.
func (l *Lister) ListSiteSnapshots(ctx context.Context, destination string, site config.Site, storageConfig config.Storage) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	if site.BackupMode != "incremental" {
		return Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; use 'bqckup history list --site %s --details' to inspect stored archives",
			site.Name, site.Name), nil)
	}
	return l.listSnapshots(ctx, destination, site, storageConfig, nil)
}

func (l *Lister) listPackages(ctx context.Context, destination string, site config.Site, remote RemoteLister) (Listing, error) {
	sets, err := remote.ListBackupSets(ctx, backupSitePrefix(site.Name, l.ServerID))
	if err != nil {
		return Listing{}, apperror.Wrap(apperror.CategoryStorage, "could not list remote backup sets", err)
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].CreatedAt.After(sets[j].CreatedAt) })
	listing := Listing{Mode: "full", Site: site.Name, Destination: destination}
	for _, set := range sets {
		packages, err := remote.ListPackages(ctx, set.Key)
		if err != nil {
			return Listing{}, apperror.Wrap(apperror.CategoryStorage, "could not list remote backup packages", err)
		}
		for _, pkg := range packages {
			listing.Packages = append(listing.Packages, PackageRow{
				Destination: destination,
				Key:         pkg.Key,
				Size:        pkg.Size,
				CreatedAt:   pkg.CreatedAt,
			})
		}
	}
	return listing, nil
}

func (l *Lister) listSnapshots(ctx context.Context, destination string, site config.Site, storageConfig config.Storage, remote RemoteLister) (Listing, error) {
	if l.Snapshots == nil {
		return Listing{}, apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil)
	}
	repo, err := buildRepoConfig(site, storageConfig, true, l.ServerID)
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
	if remote != nil && hasEnabledDatabaseSources(site) {
		packages, err := l.listDatabasePackages(ctx, destination, site, remote)
		if err != nil {
			return Listing{}, err
		}
		listing.DatabasePackages = packages
	}
	return listing, nil
}

func (l *Lister) listDatabasePackages(ctx context.Context, destination string, site config.Site, remote RemoteLister) ([]PackageRow, error) {
	sets, err := remote.ListBackupSets(ctx, backupSitePrefix(site.Name, l.ServerID))
	if err != nil {
		return nil, apperror.Wrap(apperror.CategoryStorage, "could not list remote database backup sets", err)
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].CreatedAt.After(sets[j].CreatedAt) })
	packages := make([]PackageRow, 0)
	for _, set := range sets {
		rows, err := remote.ListPackages(ctx, set.Key)
		if err != nil {
			return nil, apperror.Wrap(apperror.CategoryStorage, "could not list remote database packages", err)
		}
		for _, pkg := range rows {
			if !strings.HasSuffix(pkg.Key, ".sql.gz") {
				continue
			}
			packages = append(packages, PackageRow{Destination: destination, Key: pkg.Key, Size: pkg.Size, CreatedAt: pkg.CreatedAt})
		}
	}
	return packages, nil
}
