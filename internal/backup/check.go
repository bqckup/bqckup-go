package backup

import (
	"context"
	"fmt"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
)

// RepositoryChecker checks one incremental repository through the engine.
type RepositoryChecker interface {
	CheckRepository(ctx context.Context, repo incremental.RepoConfig, readData bool) (incremental.CheckResult, error)
}

// CheckOutcome is the use-case view of one repository check.
type CheckOutcome struct {
	Site        string
	Destination string
	Mode        string
	Result      incremental.CheckResult
}

// Checker runs the read-only repository check for one site's destination.
// It never writes history; findings travel inside the result, errors are
// command failures only.
type Checker struct {
	ServerID string
	Engine   RepositoryChecker
}

// CheckSite validates the repository of an incremental site on one of its
// destinations. Full-mode sites are a config error pointing at history,
// exactly like ListSiteSnapshots.
func (c *Checker) CheckSite(ctx context.Context, destination string, readData bool, site config.Site, storageConfig config.Storage) (CheckOutcome, error) {
	if err := ctx.Err(); err != nil {
		return CheckOutcome{}, err
	}
	if site.BackupMode != "incremental" {
		return CheckOutcome{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; use 'bqckup history list --site %s --details' to inspect stored archives",
			site.Name, site.Name), nil)
	}
	if c.Engine == nil {
		return CheckOutcome{}, apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil)
	}
	repo, err := buildRepoConfig(site, storageConfig, true, c.ServerID)
	if err != nil {
		return CheckOutcome{}, apperror.Wrap(apperror.CategoryPreflight, "could not build repository configuration", err)
	}
	result, err := c.Engine.CheckRepository(ctx, repo, readData)
	if err != nil {
		return CheckOutcome{}, apperror.Wrap(apperror.CategoryStorage, "could not check the incremental repository", err)
	}
	return CheckOutcome{
		Site:        site.Name,
		Destination: destination,
		Mode:        "incremental",
		Result:      result,
	}, nil
}
