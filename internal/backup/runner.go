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
	RunID      string
	SiteName   string
	Status     Status
	SkipReason SkipReason
	StartedAt  time.Time
	FinishedAt time.Time
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
	Stores             map[string]storage.Store
	Retainer           Retainer
	Locker             Locker
	Clock              clock.Clock
	TemporaryDirectory string
}

type Runner struct{ dependencies Dependencies }

func NewRunner(dependencies Dependencies) *Runner { return &Runner{dependencies: dependencies} }

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

	workspace, err := os.MkdirTemp(r.dependencies.TemporaryDirectory, site.Name+"-*")
	if err != nil {
		return fail(apperror.Wrap(apperror.CategoryExecution, "could not create a temporary backup workspace", err))
	}
	defer os.RemoveAll(workspace)

	archive, err := r.dependencies.Archiver.Create(ctx, FileSource{
		Include:        site.Sources.Files.Include,
		Exclude:        site.Sources.Files.Exclude,
		FollowSymlinks: site.Sources.Files.FollowSymlinks,
	}, filepath.Join(workspace, "files.tar.gz"))
	if err != nil {
		return fail(apperror.Wrap(apperror.CategoryExecution, "could not create the file archive", err))
	}

	timestamp := now.Format(storage.TimestampLayout)
	sitePrefix := path.Join("bqckup", site.Name)
	objectKey := path.Join(sitePrefix, timestamp, "files.tar.gz")
	for _, destination := range site.Destinations {
		store, ok := r.dependencies.Stores[destination.Storage]
		if !ok || store == nil {
			return fail(apperror.Wrap(apperror.CategoryInternal, "a configured storage destination is unavailable", nil))
		}
		stored, putErr := store.Put(ctx, storage.Artifact{Path: archive.Path, Size: archive.Size, SHA256: archive.SHA256}, objectKey)
		if putErr != nil {
			recordErr := r.dependencies.Repository.CreateArtifact(context.WithoutCancel(ctx), &history.Artifact{
				RunID: run.ID, SourceKind: archive.SourceKind, SourceName: archive.SourceName,
				Destination: destination.Storage, ObjectKey: objectKey, Size: archive.Size,
				SHA256: archive.SHA256, Status: history.ArtifactFailed, ErrorMessage: "could not store backup artifact",
			})
			operationErr := apperror.Wrap(apperror.CategoryStorage, "could not store backup artifact", putErr)
			if recordErr != nil {
				operationErr = errors.Join(operationErr, apperror.Wrap(apperror.CategoryPersistence, "could not record failed backup artifact", recordErr))
			}
			return fail(operationErr)
		}
		if err := r.dependencies.Repository.CreateArtifact(ctx, &history.Artifact{
			RunID: run.ID, SourceKind: archive.SourceKind, SourceName: archive.SourceName,
			Destination: destination.Storage, ObjectKey: stored.Key, Size: stored.Size,
			SHA256: stored.SHA256, Status: history.ArtifactStored,
		}); err != nil {
			return fail(apperror.Wrap(apperror.CategoryPersistence, "could not record stored backup artifact", err))
		}
	}

	for _, destination := range site.Destinations {
		store := r.dependencies.Stores[destination.Storage]
		if err := r.dependencies.Retainer.Apply(ctx, store, sitePrefix, site.Policy.KeepLast); err != nil {
			return fail(apperror.Wrap(apperror.CategoryStorage, "backup completed but retention could not be applied", err))
		}
	}

	finished := r.dependencies.Clock.Now().UTC()
	if err := r.dependencies.Repository.FinishRun(ctx, run.ID, history.StatusSuccess, finished, "", ""); err != nil {
		result.Status = StatusFailed
		result.FinishedAt = finished
		return result, apperror.Wrap(apperror.CategoryPersistence, "could not finalize backup history", err)
	}
	result.Status = StatusSuccess
	result.FinishedAt = finished
	return result, nil
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
