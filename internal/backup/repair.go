package backup

import (
	"context"
	"fmt"
	"os"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
)

// IndexRepairer rebuilds one incremental repository's index files through the engine.
type IndexRepairer interface {
	RepairIndex(ctx context.Context, repo incremental.RepoConfig) (incremental.RepairResult, error)
}

// RepairOutcome is the use-case view of one index repair operation.
type RepairOutcome struct {
	Site        string
	Destination string
	Mode        string
	Result      incremental.RepairResult
}

// Repairer runs the index repair for one site's destination.
// It never writes history.
type Repairer struct {
	Engine    IndexRepairer
	EnvLookup func(string) (string, bool)
}

func (r *Repairer) lookupEnv(key string) (string, bool) {
	if r.EnvLookup != nil {
		return r.EnvLookup(key)
	}
	return os.LookupEnv(key)
}

// RepairSite rebuilds the index of an incremental site on one of its destinations.
// Full-mode sites return a config error pointing at history.
func (r *Repairer) RepairSite(ctx context.Context, destination string, site config.Site, storageConfig config.Storage) (RepairOutcome, error) {
	if err := ctx.Err(); err != nil {
		return RepairOutcome{}, err
	}
	if site.BackupMode != "incremental" {
		return RepairOutcome{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; use 'bqckup history list --site %s --details' to inspect stored archives",
			site.Name, site.Name), nil)
	}
	if r.Engine == nil {
		return RepairOutcome{}, apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil)
	}
	repo, err := buildRepoConfig(site, storageConfig, r.lookupEnv, true)
	if err != nil {
		return RepairOutcome{}, apperror.Wrap(apperror.CategoryPreflight, "could not build repository configuration", err)
	}
	result, err := r.Engine.RepairIndex(ctx, repo)
	if err != nil {
		return RepairOutcome{}, apperror.Wrap(apperror.CategoryStorage, "could not repair the incremental repository index", err)
	}
	return RepairOutcome{
		Site:        site.Name,
		Destination: destination,
		Mode:        "incremental",
		Result:      result,
	}, nil
}
