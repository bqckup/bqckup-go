package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
)

// RepositoryChecker checks one incremental repository through the engine.
type RepositoryChecker interface {
	CheckRepository(ctx context.Context, repo incremental.RepoConfig, readData bool) (incremental.CheckResult, error)
}

type CheckHistory interface {
	LastSuccessful(ctx context.Context, site string, before time.Time) (*history.BackupRun, error)
	RunPackages(ctx context.Context, runID string) ([]history.Package, error)
}

type PackageVerifier interface {
	VerifyPackage(ctx context.Context, key string, expectedSize int64, expectedSHA256 string, readData bool) error
}

// CheckOutcome is the use-case view of one repository check.
type CheckOutcome struct {
	Site        string
	Destination string
	Mode        string
	Result      incremental.CheckResult
}

// Checker runs the read-only integrity check for one site's destination.
// It never writes history; findings travel inside the result, errors are
// command failures only.
type Checker struct {
	ServerID string
	Engine   RepositoryChecker
	History  CheckHistory
	Verifier PackageVerifier
}

// CheckSite validates either an incremental repository or the packages from
// the latest successful full backup.
func (c *Checker) CheckSite(ctx context.Context, destination string, readData bool, site config.Site, storageConfig config.Storage) (CheckOutcome, error) {
	if err := ctx.Err(); err != nil {
		return CheckOutcome{}, err
	}
	if site.BackupMode != "incremental" {
		return c.checkFull(ctx, destination, readData, site)
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

func (c *Checker) checkFull(ctx context.Context, destination string, readData bool, site config.Site) (CheckOutcome, error) {
	if c.History == nil || c.Verifier == nil {
		return CheckOutcome{}, apperror.Wrap(apperror.CategoryInternal, "full backup checker dependencies are unavailable", nil)
	}
	started := time.Now()
	result := incremental.CheckResult{ReadData: readData, Status: "healthy"}
	run, err := c.History.LastSuccessful(ctx, site.Name, time.Time{})
	if err != nil {
		return CheckOutcome{}, apperror.Wrap(apperror.CategoryPersistence, "could not load the latest successful backup", err)
	}
	if run == nil {
		result.Status = "problems"
		result.Findings = append(result.Findings, incremental.Finding{Type: "missing_backup", ID: site.Name, Detail: "no successful backup is recorded"})
		result.DurationSeconds = time.Since(started).Seconds()
		return CheckOutcome{Site: site.Name, Destination: destination, Mode: "full", Result: result}, nil
	}
	packages, err := c.History.RunPackages(ctx, run.ID)
	if err != nil {
		return CheckOutcome{}, apperror.Wrap(apperror.CategoryPersistence, "could not load packages for the latest successful backup", err)
	}
	for _, pkg := range packages {
		if pkg.Destination != destination {
			continue
		}
		result.Packs++
		if err := c.Verifier.VerifyPackage(ctx, pkg.ObjectKey, pkg.Size, pkg.SHA256, readData); err != nil {
			result.Status = "problems"
			result.Findings = append(result.Findings, incremental.Finding{Type: "package_verification", ID: pkg.ObjectKey, Detail: err.Error()})
		}
	}
	if result.Packs == 0 {
		result.Status = "problems"
		result.Findings = append(result.Findings, incremental.Finding{Type: "missing_package_history", ID: run.ID, Detail: fmt.Sprintf("no stored packages are recorded for destination %q", destination)})
	}
	result.DurationSeconds = time.Since(started).Seconds()
	return CheckOutcome{Site: site.Name, Destination: destination, Mode: "full", Result: result}, nil
}
