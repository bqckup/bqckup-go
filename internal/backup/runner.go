package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/clock"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/storage"
)

type Status string

const (
	StatusSuccess   Status = "success"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusSkipped   Status = "skipped"
)

type SkipReason string

const (
	SkipMinimumInterval SkipReason = "minimum_interval"
	SkipAlreadyRunning  SkipReason = "already_running"
)

type RunResult struct {
	RunID      string     `json:"run_id,omitempty"`
	SiteName   string     `json:"site_name"`
	Status     Status     `json:"status"`
	SkipReason SkipReason `json:"skip_reason,omitempty"`
	StartedAt  time.Time  `json:"started_at,omitempty"`
	FinishedAt time.Time  `json:"finished_at,omitempty"`
	// ReclaimedBytes is the space freed by incremental retention (prune).
	ReclaimedBytes int64 `json:"reclaimed_bytes,omitempty"`
}

type Archiver interface {
	Create(ctx context.Context, source FileSource, destination string) (Artifact, error)
}

type RunRepository interface {
	CreateRun(ctx context.Context, run *history.BackupRun) error
	FinishRun(ctx context.Context, id string, status history.RunStatus, finished time.Time, errorCategory, errorMessage string) error
	CreateArtifact(ctx context.Context, artifact *history.Artifact) error
	LastSuccessful(ctx context.Context, site string) (*history.BackupRun, error)
}

type Retainer interface {
	Apply(ctx context.Context, store storage.Store, sitePrefix string, keepLast int) error
}

type Locker interface {
	TryLock(ctx context.Context, site string) (unlock func() error, acquired bool, err error)
}

type Dependencies struct {
	Repository         RunRepository
	Archiver           Archiver
	IncrementalEngine  IncrementalEngine
	DatabaseExporters  map[string]Exporter
	Stores             map[string]storage.Store
	Storages           map[string]config.Storage
	Retainer           Retainer
	Locker             Locker
	Clock              clock.Clock
	TemporaryDirectory string
	EnvLookup          func(string) (string, bool)
}

type Runner struct{ dependencies Dependencies }

func NewRunner(dependencies Dependencies) *Runner { return &Runner{dependencies: dependencies} }

func (r *Runner) lookupEnv(key string) (string, bool) {
	if r.dependencies.EnvLookup != nil {
		return r.dependencies.EnvLookup(key)
	}
	return os.LookupEnv(key)
}

// buildRepo constructs the engine repository configuration for one
// destination. requirePassword mirrors the run-time rule that the
// repository password environment variable must be set.
func (r *Runner) buildRepo(site config.Site, storageConfig config.Storage, requirePassword bool) (restic.RepoConfig, error) {
	return buildRepoConfig(site, storageConfig, r.lookupEnv, requirePassword)
}

// buildRepoConfig constructs the engine repository configuration for one
// destination. requirePassword mirrors the run-time rule that the
// repository password environment variable must be set.
func buildRepoConfig(site config.Site, storageConfig config.Storage, lookupEnv func(string) (string, bool), requirePassword bool) (restic.RepoConfig, error) {
	repoURL, err := restic.RepositoryURL(storageConfig, site.Name)
	if err != nil {
		return restic.RepoConfig{}, err
	}
	password, ok := lookupEnv(site.Incremental.PasswordEnv)
	if requirePassword && (!ok || password == "") {
		return restic.RepoConfig{}, apperror.Wrap(apperror.CategoryPreflight, fmt.Sprintf("environment variable %q for incremental repository password is not set or empty", site.Incremental.PasswordEnv), nil)
	}
	return restic.RepoConfig{
		URL:             repoURL,
		Password:        password,
		AccessKeyID:     storageConfig.AccessKeyID,
		SecretAccessKey: storageConfig.SecretAccessKey,
		Region:          storageConfig.Region,
		Endpoint:        storageConfig.Endpoint,
		Bucket:          storageConfig.Bucket,
		Prefix:          path.Join(storageConfig.Prefix, "restic", site.Name),
	}, nil
}

// engineDetail surfaces an engine error's public text (engine errors are
// redacted by contract, so this is safe to store and print).
func engineDetail(err error) string { return err.Error() }

func (r *Runner) Run(ctx context.Context, site config.Site, force bool) (result RunResult, returnedErr error) {
	result.SiteName = site.Name
	if err := r.validateDependencies(); err != nil {
		result.Status = StatusFailed
		return result, err
	}
	if err := ctx.Err(); err != nil {
		result.Status = StatusCancelled
		return result, apperror.Wrap(apperror.CategoryCancellation, "backup was cancelled", err)
	}

	unlock, acquired, err := r.dependencies.Locker.TryLock(ctx, site.Name)
	if err != nil {
		result.Status = StatusFailed
		return result, apperror.Wrap(apperror.CategoryPreflight, "could not acquire the site lock", err)
	}
	if !acquired {
		result.Status = StatusSkipped
		result.SkipReason = SkipAlreadyRunning
		return result, nil
	}
	defer func() {
		if unlock == nil {
			return
		}
		if unlockErr := unlock(); unlockErr != nil && returnedErr == nil {
			result.Status = StatusFailed
			returnedErr = apperror.Wrap(apperror.CategoryInternal, "backup completed but the site lock could not be released", unlockErr)
		}
	}()

	lastSuccessful, err := r.dependencies.Repository.LastSuccessful(ctx, site.Name)
	if err != nil {
		result.Status = StatusFailed
		return result, apperror.Wrap(apperror.CategoryPersistence, "could not read backup history", err)
	}
	now := r.dependencies.Clock.Now().UTC()
	if !force && lastSuccessful != nil && now.Sub(lastSuccessful.StartedAt.UTC()) < site.Policy.MinimumInterval {
		result.Status = StatusSkipped
		result.SkipReason = SkipMinimumInterval
		return result, nil
	}

	run := &history.BackupRun{
		SiteName:  site.Name,
		Status:    history.StatusRunning,
		Forced:    force,
		StartedAt: now,
	}
	if err := r.dependencies.Repository.CreateRun(ctx, run); err != nil {
		result.Status = StatusFailed
		return result, apperror.Wrap(apperror.CategoryPersistence, "could not create backup history", err)
	}
	result.RunID = run.ID
	result.StartedAt = now

	fail := func(operationErr error) (RunResult, error) {
		category := apperror.CategoryOf(operationErr)
		status := history.StatusFailed
		result.Status = StatusFailed
		if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
			category = apperror.CategoryCancellation
			status = history.StatusCancelled
			result.Status = StatusCancelled
			operationErr = apperror.Wrap(category, "backup was cancelled", operationErr)
		}
		finished := r.dependencies.Clock.Now().UTC()
		result.FinishedAt = finished
		finishErr := r.dependencies.Repository.FinishRun(
			context.WithoutCancel(ctx), run.ID, status, finished, string(category), apperror.UserMessage(operationErr),
		)
		if finishErr != nil {
			result.Status = StatusFailed
			return result, errors.Join(operationErr, apperror.Wrap(apperror.CategoryPersistence, "could not finalize backup history", finishErr))
		}
		return result, operationErr
	}

	timestamp := now.Format(storage.TimestampLayout)
	sitePrefix := path.Join("bqckup", site.Name)

	if site.BackupMode == "incremental" {
		engine := r.dependencies.IncrementalEngine
		if engine == nil {
			return fail(apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil))
		}
		password, ok := r.lookupEnv(site.Incremental.PasswordEnv)
		if !ok || password == "" {
			return fail(apperror.Wrap(apperror.CategoryPreflight, fmt.Sprintf("environment variable %q for incremental repository password is not set or empty", site.Incremental.PasswordEnv), nil))
		}
		for _, destination := range site.Destinations {
			storageConfig, ok := r.dependencies.Storages[destination.Storage]
			if !ok {
				return fail(apperror.Wrap(apperror.CategoryInternal, fmt.Sprintf("storage configuration %q is unavailable", destination.Storage), nil))
			}
			repo, err := r.buildRepo(site, storageConfig, true)
			if err != nil {
				return fail(err)
			}
			if err := engine.EnsureRepository(ctx, repo); err != nil {
				return fail(apperror.Wrap(apperror.CategoryStorage, "could not ensure incremental repository: "+engineDetail(err), err))
			}
			spec := restic.BackupSpec{
				SiteName: site.Name,
				Include:  []string(site.Sources.Files.Include),
				Exclude:  []string(site.Sources.Files.Exclude),
				Tags:     []string{"bqckup", "site:" + site.Name},
			}
			summary, err := engine.BackupFiles(ctx, repo, spec)
			if err != nil {
				return fail(apperror.Wrap(apperror.CategoryExecution, "could not create incremental file backup: "+engineDetail(err), err))
			}
			if err := r.dependencies.Repository.CreateArtifact(ctx, &history.Artifact{
				RunID:       run.ID,
				SourceKind:  "files",
				SourceName:  "files",
				Destination: destination.Storage,
				ObjectKey:   summary.SnapshotID,
				Size:        summary.DataAdded,
				SHA256:      summary.SnapshotID,
				Status:      history.ArtifactStored,
			}); err != nil {
				return fail(apperror.Wrap(apperror.CategoryPersistence, "could not record incremental backup artifact", err))
			}
		}
	} else {
		workspace, err := os.MkdirTemp(r.dependencies.TemporaryDirectory, site.Name+"-*")
		if err != nil {
			return fail(apperror.Wrap(apperror.CategoryExecution, "could not create a temporary backup workspace", err))
		}
		defer os.RemoveAll(workspace)

		archive, err := r.dependencies.Archiver.Create(ctx, FileSource{
			Include:        []string(site.Sources.Files.Include),
			Exclude:        []string(site.Sources.Files.Exclude),
			FollowSymlinks: site.Sources.Files.FollowSymlinks,
		}, filepath.Join(workspace, "files.tar.gz"))
		if err != nil {
			return fail(apperror.Wrap(apperror.CategoryExecution, "could not create the file archive", err))
		}

		objectKey := path.Join(sitePrefix, timestamp, "files.tar.gz")
		if err := r.storeArtifact(ctx, run.ID, archive, objectKey, site.Destinations); err != nil {
			return fail(err)
		}
	}

	if len(site.Sources.Databases) > 0 {
		workspace, err := os.MkdirTemp(r.dependencies.TemporaryDirectory, site.Name+"-db-*")
		if err != nil {
			return fail(apperror.Wrap(apperror.CategoryExecution, "could not create a temporary database workspace", err))
		}
		defer os.RemoveAll(workspace)

		for _, source := range site.Sources.Databases {
			if !source.Enabled {
				continue
			}
			exporter, ok := r.dependencies.DatabaseExporters[source.Engine]
			if !ok || exporter == nil {
				return fail(apperror.Wrap(apperror.CategoryInternal, "a configured database exporter is unavailable", nil))
			}
			destination := filepath.Join(workspace, "databases", source.Name+".sql.gz")
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return fail(apperror.Wrap(apperror.CategoryExecution, "could not prepare database export workspace", err))
			}
			databaseArtifact, exportErr := exporter.Export(ctx, source, destination)
			databaseKey := path.Join(sitePrefix, timestamp, "databases", source.Name+".sql.gz")
			if exportErr != nil {
				recordErr := r.recordFailedArtifact(ctx, run.ID, Artifact{SourceKind: "database", SourceName: source.Name}, databaseKey, site.Destinations, "could not export database")
				operationErr := apperror.Wrap(apperror.CategoryExecution, "could not export database", exportErr)
				if recordErr != nil {
					operationErr = errors.Join(operationErr, recordErr)
				}
				return fail(operationErr)
			}
			if err := r.storeArtifact(ctx, run.ID, databaseArtifact, databaseKey, site.Destinations); err != nil {
				return fail(err)
			}
		}
	}

	for _, destination := range site.Destinations {
		if site.BackupMode == "incremental" {
			engine := r.dependencies.IncrementalEngine
			storageConfig, ok := r.dependencies.Storages[destination.Storage]
			if !ok {
				return fail(apperror.Wrap(apperror.CategoryInternal, fmt.Sprintf("storage configuration %q is unavailable", destination.Storage), nil))
			}
			repo, err := r.buildRepo(site, storageConfig, false)
			if err != nil {
				return fail(err)
			}
			reclaimed, err := engine.ApplyRetention(ctx, repo, site.Policy.KeepLast, site.Name)
			if err != nil {
				return fail(apperror.Wrap(apperror.CategoryStorage, "backup completed but incremental retention could not be applied: "+engineDetail(err), err))
			}
			result.ReclaimedBytes += reclaimed
		} else {
			store := r.dependencies.Stores[destination.Storage]
			if err := r.dependencies.Retainer.Apply(ctx, store, sitePrefix, site.Policy.KeepLast); err != nil {
				return fail(apperror.Wrap(apperror.CategoryStorage, "backup completed but retention could not be applied", err))
			}
		}
	}

	finished := r.dependencies.Clock.Now().UTC()
	if err := r.dependencies.Repository.FinishRun(context.WithoutCancel(ctx), run.ID, history.StatusSuccess, finished, "", ""); err != nil {
		result.Status = StatusFailed
		result.FinishedAt = finished
		return result, apperror.Wrap(apperror.CategoryPersistence, "could not finalize backup history", err)
	}
	result.Status = StatusSuccess
	result.FinishedAt = finished
	return result, nil
}

// Unlock removes stale repository locks for every destination of a site
// (restic unlock semantics: stale locks only).
func (r *Runner) Unlock(ctx context.Context, site config.Site) error {
	if site.BackupMode != "incremental" {
		return apperror.Wrap(apperror.CategoryConfig, "unlock applies to incremental sites only", nil)
	}
	engine := r.dependencies.IncrementalEngine
	if engine == nil {
		return apperror.Wrap(apperror.CategoryInternal, "incremental backup engine is unavailable", nil)
	}
	for _, destination := range site.Destinations {
		storageConfig, ok := r.dependencies.Storages[destination.Storage]
		if !ok {
			return apperror.Wrap(apperror.CategoryInternal, fmt.Sprintf("storage configuration %q is unavailable", destination.Storage), nil)
		}
		repo, err := r.buildRepo(site, storageConfig, true)
		if err != nil {
			return apperror.Wrap(apperror.CategoryPreflight, "could not build repository configuration", err)
		}
		if err := engine.Unlock(ctx, repo); err != nil {
			return apperror.Wrap(apperror.CategoryStorage, "could not unlock the incremental repository: "+engineDetail(err), err)
		}
	}
	return nil
}

func (r *Runner) storeArtifact(ctx context.Context, runID string, artifact Artifact, objectKey string, destinations []config.Destination) error {
	for _, destination := range destinations {
		store, ok := r.dependencies.Stores[destination.Storage]
		if !ok || store == nil {
			return apperror.Wrap(apperror.CategoryInternal, "a configured storage destination is unavailable", nil)
		}
		stored, putErr := store.Put(ctx, storage.Artifact{Path: artifact.Path, Size: artifact.Size, SHA256: artifact.SHA256}, objectKey)
		if putErr != nil {
			recordErr := r.recordFailedArtifact(ctx, runID, artifact, objectKey, []config.Destination{destination}, "could not store backup artifact")
			operationErr := apperror.Wrap(apperror.CategoryStorage, "could not store backup artifact", putErr)
			if recordErr != nil {
				operationErr = errors.Join(operationErr, recordErr)
			}
			return operationErr
		}
		if err := r.dependencies.Repository.CreateArtifact(ctx, &history.Artifact{
			RunID: runID, SourceKind: artifact.SourceKind, SourceName: artifact.SourceName,
			Destination: destination.Storage, ObjectKey: stored.Key, Size: stored.Size,
			SHA256: stored.SHA256, Status: history.ArtifactStored,
		}); err != nil {
			return apperror.Wrap(apperror.CategoryPersistence, "could not record stored backup artifact", err)
		}
	}
	return nil
}

func (r *Runner) recordFailedArtifact(ctx context.Context, runID string, artifact Artifact, objectKey string, destinations []config.Destination, message string) error {
	for _, destination := range destinations {
		if err := r.dependencies.Repository.CreateArtifact(context.WithoutCancel(ctx), &history.Artifact{
			RunID: runID, SourceKind: artifact.SourceKind, SourceName: artifact.SourceName,
			Destination: destination.Storage, ObjectKey: objectKey, Size: artifact.Size,
			SHA256: artifact.SHA256, Status: history.ArtifactFailed, ErrorMessage: message,
		}); err != nil {
			return apperror.Wrap(apperror.CategoryPersistence, "could not record failed backup artifact", err)
		}
	}
	return nil
}

func (r *Runner) validateDependencies() error {
	d := r.dependencies
	if d.Repository == nil || d.Archiver == nil || d.Retainer == nil || d.Locker == nil || d.Clock == nil || d.TemporaryDirectory == "" {
		return apperror.Wrap(apperror.CategoryInternal, "backup runner dependencies are incomplete", nil)
	}
	if len(d.Stores) == 0 {
		return apperror.Wrap(apperror.CategoryInternal, "no storage destinations are available", nil)
	}
	if err := os.MkdirAll(d.TemporaryDirectory, 0o700); err != nil {
		return apperror.Wrap(apperror.CategoryPreflight, "could not prepare the temporary directory", fmt.Errorf("create temporary directory: %w", err))
	}
	return nil
}
