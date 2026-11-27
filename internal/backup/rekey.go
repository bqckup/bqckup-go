package backup

import (
	"context"
	"fmt"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
)

// PasswordRekeyer replaces one incremental repository's password through the engine.
type PasswordRekeyer interface {
	RekeyRepository(ctx context.Context, repo incremental.RepoConfig, newPassword string) (incremental.RekeyResult, error)
}

// RekeyOutcome is the use-case view of one password replacement.
type RekeyOutcome struct {
	Site        string
	Destination string
	Mode        string
	Result      incremental.RekeyResult
}

// Rekeyer replaces the password of one site's incremental repository.
// It never writes history. The site YAML still holds the old password when
// this runs, so the caller must update it right after a successful rekey.
type Rekeyer struct {
	ServerID string
	Engine   PasswordRekeyer
}

// RekeySite replaces the repository password for one incremental site on one of its destinations.
func (r *Rekeyer) RekeySite(ctx context.Context, destination string, site config.Site, storageConfig config.Storage, newPassword string) (RekeyOutcome, error) {
	if err := ctx.Err(); err != nil {
		return RekeyOutcome{}, err
	}
	if site.BackupMode != "incremental" {
		return RekeyOutcome{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; only incremental repositories have a password", site.Name), nil)
	}
	if newPassword == "" {
		return RekeyOutcome{}, apperror.Wrap(apperror.CategoryConfig, "the new password must not be empty", nil)
	}
	if r.Engine == nil {
		return RekeyOutcome{}, apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil)
	}
	repo, err := buildRepoConfig(site, storageConfig, true, r.ServerID)
	if err != nil {
		return RekeyOutcome{}, apperror.Wrap(apperror.CategoryPreflight, "could not build repository configuration", err)
	}
	result, err := r.Engine.RekeyRepository(ctx, repo, newPassword)
	if err != nil {
		return RekeyOutcome{}, apperror.Wrap(apperror.CategoryStorage, "could not replace the incremental repository password", err)
	}
	return RekeyOutcome{
		Site:        site.Name,
		Destination: destination,
		Mode:        "incremental",
		Result:      result,
	}, nil
}
