package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup/incremental"
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
	StatusNoChange  Status = "no_change"
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
	Create(ctx context.Context, source FileSource, destination string) (Package, error)
}

type RunRepository interface {
	CreateRun(ctx context.Context, run *history.BackupRun) error
	FinishRun(ctx context.Context, id string, status history.RunStatus, finished time.Time, errorCategory, errorMessage string) error
	CreatePackage(ctx context.Context, pkg *history.Package) error
	LastSuccessful(ctx context.Context, site string, before time.Time) (*history.BackupRun, error)
	RunPackages(ctx context.Context, runID string) ([]history.Package, error)
	ConsecutiveWithoutSuccess(ctx context.Context, site string, startedAt time.Time) (int, error)
}

// NotifyDestination describes one configured destination for notifications.
type NotifyDestination struct {
	Name   string
	Bucket string
	Path   string
}

// NotifyInput carries one terminal run's facts to the notifier. The event
// name is one of the config notification events; payload stats are computed
// from Packages by the notifier, so RunResult stays untouched.
type NotifyInput struct {
	Event              string
	RunID              string
	SiteName           string
	Status             Status
	StartedAt          time.Time
	FinishedAt         time.Time
	LastSuccessfulAt   time.Time
	FailureStreak      int
	ErrorCategory      string
	ErrorMessage       string
	Packages           []history.Package
	Destinations       []NotifyDestination
	HasDatabaseSources bool
}

// Notifier delivers terminal run notifications. It is consumer-owned: the
// concrete dispatcher lives in internal/notify and is wired in internal/app.
// Delivery is best effort; an error is a warning, never a run failure.
type Notifier interface {
	Notify(ctx context.Context, input NotifyInput) error
}

type Retainer interface {
	Apply(ctx context.Context, store storage.Store, sitePrefix string, keepLast int) error
}

type Locker interface {
	TryLock(ctx context.Context, site string) (unlock func() error, acquired bool, err error)
}

type Dependencies struct {
	ServerID           string
	Repository         RunRepository
	Archiver           Archiver
	IncrementalEngine  IncrementalEngine
	DatabaseExporters  map[string]Exporter
	Stores             map[string]storage.Store
	Storages           map[string]config.Storage
	Retainer           Retainer
	Locker             Locker
	Notifier           Notifier
	Clock              clock.Clock
	TemporaryDirectory string
	EnvLookup          func(string) (string, bool)
}

type Runner struct{ dependencies Dependencies }

func backupSitePrefix(siteName, serverID string) string {
	if serverID == "" {
		return path.Join("bqckup", siteName)
	}
	return path.Join("bqckup", serverID, siteName)
}

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
func (r *Runner) buildRepo(site config.Site, storageConfig config.Storage, requirePassword bool) (incremental.RepoConfig, error) {
	return buildRepoConfig(site, storageConfig, r.lookupEnv, requirePassword, r.dependencies.ServerID)
}

// buildRepoConfig constructs the engine repository configuration for one
// destination. requirePassword mirrors the run-time rule that the
// repository password environment variable must be set.
func buildRepoConfig(site config.Site, storageConfig config.Storage, lookupEnv func(string) (string, bool), requirePassword bool, serverID ...string) (incremental.RepoConfig, error) {
	server := ""
	if len(serverID) > 0 {
		server = serverID[0]
	}
	repoURL, err := incremental.RepositoryURL(storageConfig, site.Name, server)
	if err != nil {
		return incremental.RepoConfig{}, err
	}
	password, ok := lookupEnv(site.Incremental.PasswordEnv)
	if requirePassword && (!ok || password == "") {
		return incremental.RepoConfig{}, apperror.Wrap(apperror.CategoryPreflight, fmt.Sprintf("environment variable %q for incremental repository password is not set or empty", site.Incremental.PasswordEnv), nil)
	}
	return incremental.RepoConfig{
		URL:             repoURL,
		Password:        password,
		AccessKeyID:     storageConfig.AccessKeyID,
		SecretAccessKey: storageConfig.SecretAccessKey,
		Region:          storageConfig.Region,
		Endpoint:        storageConfig.Endpoint,
		Bucket:          storageConfig.Bucket,
		Prefix:          path.Join(storageConfig.Prefix, incremental.RepositoryPrefix(site.Name, server)),
	}, nil
}

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

	now := r.dependencies.Clock.Now().UTC()
	lastSuccessful, err := r.dependencies.Repository.LastSuccessful(ctx, site.Name, now)
	if err != nil {
		result.Status = StatusFailed
		return result, apperror.Wrap(apperror.CategoryPersistence, "could not read backup history", err)
	}
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
		event := config.EventBackupFailed
		result.Status = StatusFailed
		if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
			category = apperror.CategoryCancellation
			status = history.StatusCancelled
			event = config.EventBackupCancelled
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
		r.notify(context.WithoutCancel(ctx), NotifyInput{
			Event: event, RunID: run.ID, SiteName: site.Name, Status: result.Status,
			StartedAt: now, FinishedAt: finished,
			ErrorCategory: string(category), ErrorMessage: apperror.UserMessage(operationErr),
			Destinations:       buildNotifyDestinations(site, r.dependencies.Storages),
			HasDatabaseSources: hasEnabledDatabaseSources(site),
		})
		return result, operationErr
	}

	backupSet := storage.FormatBackupSet(now)
	sitePrefix := backupSitePrefix(site.Name, r.dependencies.ServerID)

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
				return fail(apperror.Wrap(apperror.CategoryStorage, "could not ensure incremental repository", err))
			}
			spec := incremental.BackupSpec{
				SiteName: site.Name,
				Include:  []string(site.Sources.Files.Include),
				Exclude:  []string(site.Sources.Files.Exclude),
				Tags:     []string{"bqckup", "site:" + site.Name},
			}
			summary, err := engine.BackupFiles(ctx, repo, spec)
			if err != nil {
				return fail(apperror.Wrap(apperror.CategoryExecution, "could not create incremental file backup", err))
			}
			if err := r.dependencies.Repository.CreatePackage(ctx, &history.Package{
				RunID:       run.ID,
				SourceKind:  "files",
				SourceName:  "files",
				Destination: destination.Storage,
				ObjectKey:   summary.SnapshotID,
				// The snapshot's logical size, not the dedup delta: a fully
				// deduplicated run adds 0 bytes but still holds data. There
				// is no single package file to hash, so SHA256 stays empty
				// rather than claiming the snapshot ID is a content hash.
				Size:   summary.TotalBytesProcessed,
				SHA256: "",
				Status: history.PackageStored,
			}); err != nil {
				return fail(apperror.Wrap(apperror.CategoryPersistence, "could not record incremental backup package", err))
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

		objectKey := path.Join(sitePrefix, backupSet, "files.tar.gz")
		if err := r.storePackage(ctx, run.ID, archive, objectKey, site.Destinations); err != nil {
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
			databasePackage, exportErr := exporter.Export(ctx, source, destination)
			databaseKey := path.Join(sitePrefix, backupSet, "databases", source.Name+".sql.gz")
			if exportErr != nil {
				recordErr := r.recordFailedPackage(ctx, run.ID, Package{SourceKind: "database", SourceName: source.Name}, databaseKey, site.Destinations, "could not export database")
				operationErr := apperror.Wrap(apperror.CategoryExecution, "could not export database", exportErr)
				if recordErr != nil {
					operationErr = errors.Join(operationErr, recordErr)
				}
				return fail(operationErr)
			}
			if err := r.storePackage(ctx, run.ID, databasePackage, databaseKey, site.Destinations); err != nil {
				return fail(err)
			}
		}
	}

	for _, destination := range site.Destinations {
		store, ok := r.dependencies.Stores[destination.Storage]
		if !ok || store == nil {
			return fail(apperror.Wrap(apperror.CategoryInternal, "a configured storage destination is unavailable", nil))
		}
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
				return fail(apperror.Wrap(apperror.CategoryStorage, "backup completed but incremental retention could not be applied", err))
			}
			result.ReclaimedBytes += reclaimed
		}
		// Set retention covers every mode: full sites store the file
		// archive here, and incremental sites store their database dumps
		// here. Without this, incremental runs would grow
		// bqckup/<site>/<timestamp>/databases/ without bound.
		if err := r.dependencies.Retainer.Apply(ctx, store, sitePrefix, site.Policy.KeepLast); err != nil {
			return fail(apperror.Wrap(apperror.CategoryStorage, "backup completed but retention could not be applied", err))
		}
	}

	if site.BackupMode != "incremental" {
		anchor, err := r.dependencies.Repository.LastSuccessful(ctx, site.Name, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not load previous backup for change detection: %v\n", err)
		} else if anchor != nil {
			prevPkgs, prevErr := r.dependencies.Repository.RunPackages(ctx, anchor.ID)
			currPkgs, currErr := r.dependencies.Repository.RunPackages(ctx, run.ID)
			if prevErr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not load previous backup packages: %v\n", prevErr)
			} else if currErr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not load current backup packages: %v\n", currErr)
			} else if unchangedSizes(currPkgs, prevPkgs) {
				seenSources := make(map[[2]string]struct{})
				var details []string
				for _, p := range currPkgs {
					k := [2]string{p.SourceKind, p.SourceName}
					if _, seen := seenSources[k]; seen {
						continue
					}
					seenSources[k] = struct{}{}
					name := path.Base(p.ObjectKey)
					details = append(details, fmt.Sprintf("%s (%d B)", name, p.Size))
				}
				fmt.Fprintf(os.Stderr, "warning: backup for %s is unchanged: %s — same sizes as the previous run\n", site.Name, strings.Join(details, ", "))

				count := len(seenSources)
				var msg string
				if count == 1 {
					msg = "1 item is unchanged from the previous run."
				} else {
					msg = fmt.Sprintf("%d items are unchanged from the previous run.", count)
				}

				finished := r.dependencies.Clock.Now().UTC()
				if err := r.dependencies.Repository.FinishRun(context.WithoutCancel(ctx), run.ID, history.StatusNoChange, finished, "no_change", msg); err != nil {
					result.Status = StatusFailed
					result.FinishedAt = finished
					r.notify(context.WithoutCancel(ctx), NotifyInput{
						Event: config.EventBackupFailed, RunID: run.ID, SiteName: site.Name, Status: result.Status,
						StartedAt: now, FinishedAt: finished,
						ErrorCategory: string(apperror.CategoryPersistence), ErrorMessage: "could not finalize backup history",
						Destinations:       buildNotifyDestinations(site, r.dependencies.Storages),
						HasDatabaseSources: hasEnabledDatabaseSources(site),
					})
					return result, apperror.Wrap(apperror.CategoryPersistence, "could not finalize backup history", err)
				}
				result.Status = StatusNoChange
				result.FinishedAt = finished
				r.notify(context.WithoutCancel(ctx), NotifyInput{
					Event: config.EventBackupNoChange, RunID: run.ID, SiteName: site.Name, Status: result.Status,
					StartedAt: now, FinishedAt: finished,
					ErrorCategory: "no_change", ErrorMessage: msg,
					Destinations:       buildNotifyDestinations(site, r.dependencies.Storages),
					HasDatabaseSources: hasEnabledDatabaseSources(site),
				})
				return result, nil
			}
		}
	}

	finished := r.dependencies.Clock.Now().UTC()
	if err := r.dependencies.Repository.FinishRun(context.WithoutCancel(ctx), run.ID, history.StatusSuccess, finished, "", ""); err != nil {
		result.Status = StatusFailed
		result.FinishedAt = finished
		r.notify(context.WithoutCancel(ctx), NotifyInput{
			Event: config.EventBackupFailed, RunID: run.ID, SiteName: site.Name, Status: result.Status,
			StartedAt: now, FinishedAt: finished,
			ErrorCategory: string(apperror.CategoryPersistence), ErrorMessage: "could not finalize backup history",
			Destinations:       buildNotifyDestinations(site, r.dependencies.Storages),
			HasDatabaseSources: hasEnabledDatabaseSources(site),
		})
		return result, apperror.Wrap(apperror.CategoryPersistence, "could not finalize backup history", err)
	}
	result.Status = StatusSuccess
	result.FinishedAt = finished
	return result, nil
}

// unchangedSizes reports whether every stored package in current has a
// same-sized counterpart in previous, deduped by source kind and name.
func unchangedSizes(current, previous []history.Package) bool {
	if len(current) == 0 || len(previous) == 0 {
		return false
	}
	currentSizes := make(map[[2]string]int64)
	for _, p := range current {
		key := [2]string{p.SourceKind, p.SourceName}
		if _, exists := currentSizes[key]; !exists {
			currentSizes[key] = p.Size
		}
	}
	previousSizes := make(map[[2]string]int64)
	for _, p := range previous {
		key := [2]string{p.SourceKind, p.SourceName}
		if _, exists := previousSizes[key]; !exists {
			previousSizes[key] = p.Size
		}
	}
	if len(currentSizes) == 0 || len(currentSizes) != len(previousSizes) {
		return false
	}
	for key, currentSize := range currentSizes {
		prevSize, exists := previousSizes[key]
		if !exists || currentSize != prevSize {
			return false
		}
	}
	return true
}

func buildNotifyDestinations(site config.Site, storages map[string]config.Storage) []NotifyDestination {
	destinations := make([]NotifyDestination, 0, len(site.Destinations))
	for _, d := range site.Destinations {
		nd := NotifyDestination{Name: d.Storage}
		if s, ok := storages[d.Storage]; ok {
			switch s.Type {
			case "local":
				nd.Path = s.Directory
			case "s3", "r2":
				nd.Bucket = s.Bucket
			}
		}
		destinations = append(destinations, nd)
	}
	return destinations
}

func hasEnabledDatabaseSources(site config.Site) bool {
	for _, db := range site.Sources.Databases {
		if db.Enabled {
			return true
		}
	}
	return false
}

// notify delivers one terminal notification after the run is recorded in
// history. Delivery is best effort: errors are warnings on stderr and never
// alter the run result. Runs without a notifier skip the work entirely.
func (r *Runner) notify(ctx context.Context, input NotifyInput) {
	if r.dependencies.Notifier == nil {
		return
	}
	lastSuccessful, err := r.dependencies.Repository.LastSuccessful(ctx, input.SiteName, input.StartedAt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load last successful run for notification: %v\n", err)
	} else if lastSuccessful != nil {
		input.LastSuccessfulAt = lastSuccessful.StartedAt
	}
	streak, err := r.dependencies.Repository.ConsecutiveWithoutSuccess(ctx, input.SiteName, input.StartedAt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not compute failure streak for notification: %v\n", err)
	} else {
		input.FailureStreak = streak
	}
	packages, err := r.dependencies.Repository.RunPackages(ctx, input.RunID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load run packages for notification: %v\n", err)
	}
	input.Packages = packages
	if err := r.dependencies.Notifier.Notify(ctx, input); err != nil {
		fmt.Fprintf(os.Stderr, "warning: notification delivery failed: %v\n", err)
	}
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
			return apperror.Wrap(apperror.CategoryStorage, "could not unlock the incremental repository", err)
		}
	}
	return nil
}

func (r *Runner) storePackage(ctx context.Context, runID string, pkg Package, objectKey string, destinations []config.Destination) error {
	for _, destination := range destinations {
		store, ok := r.dependencies.Stores[destination.Storage]
		if !ok || store == nil {
			return apperror.Wrap(apperror.CategoryInternal, "a configured storage destination is unavailable", nil)
		}
		stored, putErr := store.Put(ctx, storage.Package{Path: pkg.Path, Size: pkg.Size, SHA256: pkg.SHA256}, objectKey)
		if putErr != nil {
			recordErr := r.recordFailedPackage(ctx, runID, pkg, objectKey, []config.Destination{destination}, "could not store backup package")
			operationErr := apperror.Wrap(apperror.CategoryStorage, "could not store backup package", putErr)
			if recordErr != nil {
				operationErr = errors.Join(operationErr, recordErr)
			}
			return operationErr
		}
		if err := r.dependencies.Repository.CreatePackage(ctx, &history.Package{
			RunID: runID, SourceKind: pkg.SourceKind, SourceName: pkg.SourceName,
			Destination: destination.Storage, ObjectKey: stored.Key, Size: stored.Size,
			SHA256: stored.SHA256, Status: history.PackageStored,
		}); err != nil {
			return apperror.Wrap(apperror.CategoryPersistence, "could not record stored backup package", err)
		}
	}
	return nil
}

func (r *Runner) recordFailedPackage(ctx context.Context, runID string, pkg Package, objectKey string, destinations []config.Destination, message string) error {
	for _, destination := range destinations {
		if err := r.dependencies.Repository.CreatePackage(context.WithoutCancel(ctx), &history.Package{
			RunID: runID, SourceKind: pkg.SourceKind, SourceName: pkg.SourceName,
			Destination: destination.Storage, ObjectKey: objectKey, Size: pkg.Size,
			SHA256: pkg.SHA256, Status: history.PackageFailed, ErrorMessage: message,
		}); err != nil {
			return apperror.Wrap(apperror.CategoryPersistence, "could not record failed backup package", err)
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
