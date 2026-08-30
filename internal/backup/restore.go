package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
)

// SnapshotRestorer restores one snapshot through the incremental engine.
type SnapshotRestorer interface {
	RestoreSnapshot(ctx context.Context, repo incremental.RepoConfig, snapshotID string, paths []string, target string, confirm incremental.RestoreOverwrite) (incremental.RestoreSummary, error)
}

// RestoreResult is the use-case view of one restore.
type RestoreResult struct {
	SnapshotID      string   `json:"snapshot_id"`
	Target          string   `json:"target"`
	FilesRestored   int      `json:"files_restored"`
	BytesRestored   int64    `json:"bytes_restored"`
	SkippedPaths    []string `json:"skipped_paths,omitempty"`
	DurationSeconds float64  `json:"duration_seconds"`
}

// Restorer restores one site's files from one snapshot. It never writes
// history and resolves snapshot references against the site's tag only.
type Restorer struct {
	ServerID  string
	Snapshots SnapshotLister
	Engine    SnapshotRestorer
}

// RestoreSiteSnapshot restores the configured file paths of one snapshot
// into the target directory. The confirm callback is passed through to the
// engine unchanged; its error keeps its apperror category so the CLI can
// map declined prompts and non-terminal stdin to their exit codes.
func (r *Restorer) RestoreSiteSnapshot(ctx context.Context, destination string, snapshotRef, target string, site config.Site, storageConfig config.Storage, confirm incremental.RestoreOverwrite) (RestoreResult, error) {
	if err := ctx.Err(); err != nil {
		return RestoreResult{}, err
	}
	if site.BackupMode != "incremental" {
		return RestoreResult{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; use 'bqckup history list --site %s --details' to inspect stored archives",
			site.Name, site.Name), nil)
	}
	if r.Snapshots == nil || r.Engine == nil {
		return RestoreResult{}, apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil)
	}
	repo, err := buildRepoConfig(site, storageConfig, true, r.ServerID)
	if err != nil {
		return RestoreResult{}, apperror.Wrap(apperror.CategoryPreflight, "could not build repository configuration", err)
	}
	// Lstat: a symlink to a directory is still a symlink, and the spec
	// rejects both regular files and symlinks as targets.
	if info, err := os.Lstat(target); err == nil && !info.IsDir() {
		return RestoreResult{}, apperror.Wrap(apperror.CategoryPreflight, fmt.Sprintf("target %q exists and is not a directory", target), nil)
	}
	snapshots, err := r.Snapshots.ListSnapshots(ctx, repo)
	if err != nil {
		return RestoreResult{}, apperror.Wrap(apperror.CategoryStorage, "could not list the incremental snapshots", err)
	}
	tag := "site:" + site.Name
	var tagged []incremental.Snapshot
	for _, snapshot := range snapshots {
		if slices.Contains(snapshot.Tags, tag) {
			tagged = append(tagged, snapshot)
		}
	}
	chosen, ok := resolveSnapshotRef(snapshotRef, tagged)
	if !ok {
		return RestoreResult{}, apperror.Wrap(apperror.CategoryStorage, fmt.Sprintf("snapshot %q was not found for site %q", snapshotRef, site.Name), nil)
	}
	paths := make([]string, len(site.Sources.Files.Include))
	for i, p := range site.Sources.Files.Include {
		paths[i] = filepath.Clean(p)
	}
	summary, err := r.Engine.RestoreSnapshot(ctx, repo, chosen.ID, paths, target, confirm)
	if err != nil {
		// Pass through errors that already carry a category (the confirm
		// callback's preflight/cancellation); everything else is storage.
		if apperror.CategoryOf(err) != apperror.CategoryInternal {
			return RestoreResult{}, err
		}
		return RestoreResult{}, apperror.Wrap(apperror.CategoryStorage, "could not restore the snapshot", err)
	}
	return RestoreResult{
		SnapshotID:      summary.SnapshotID,
		Target:          summary.Target,
		FilesRestored:   summary.FilesRestored,
		BytesRestored:   summary.BytesRestored,
		SkippedPaths:    summary.SkippedPaths,
		DurationSeconds: summary.DurationSeconds,
	}, nil
}

// resolveSnapshotRef resolves "latest" (newest creation time) or an ID
// prefix against the site-tagged snapshots. Zero or ambiguous matches are
// reported through ok=false.
func resolveSnapshotRef(ref string, tagged []incremental.Snapshot) (*incremental.Snapshot, bool) {
	if ref == "latest" {
		var newest *incremental.Snapshot
		for i := range tagged {
			if newest == nil || tagged[i].CreatedAt.After(newest.CreatedAt) {
				newest = &tagged[i]
			}
		}
		return newest, newest != nil
	}
	var match *incremental.Snapshot
	for i := range tagged {
		if !strings.HasPrefix(tagged[i].ID, ref) {
			continue
		}
		if match != nil {
			return nil, false
		}
		match = &tagged[i]
	}
	return match, match != nil
}
