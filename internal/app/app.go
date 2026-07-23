package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/backup/files"
	"github.com/bqckup/bqckup-go/internal/clock"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/platform/lock"
	"github.com/bqckup/bqckup-go/internal/retention"
	"github.com/bqckup/bqckup-go/internal/storage"
	localstorage "github.com/bqckup/bqckup-go/internal/storage/local"
)

type App struct {
	configuration config.Config
	runner        *backup.Runner
	repository    *history.Repository
	closeOnce     sync.Once
	closeErr      error
	closeDatabase func() error
}

func Open(ctx context.Context, configDir string) (*App, error) {
	configuration, err := config.Load(ctx, configDir)
	if err != nil {
		return nil, err
	}
	database, closeDatabase, err := history.Open(configuration.App.StateDatabase)
	if err != nil {
		return nil, apperror.Wrap(apperror.CategoryPersistence, "could not open the backup history database", err)
	}
	if err := history.Migrate(ctx, database); err != nil {
		_ = closeDatabase()
		return nil, apperror.Wrap(apperror.CategoryPersistence, "could not migrate the backup history database", err)
	}

	stores := make(map[string]storage.Store, len(configuration.Storages))
	for name, storageConfig := range configuration.Storages {
		if storageConfig.Type != "local" {
			_ = closeDatabase()
			return nil, apperror.Wrap(apperror.CategoryConfig, "the configured storage type is not supported", nil)
		}
		store, err := localstorage.New(storageConfig.Directory)
		if err != nil {
			_ = closeDatabase()
			return nil, apperror.Wrap(apperror.CategoryPreflight, "could not prepare a local storage destination", err)
		}
		stores[name] = store
	}

	repository := history.NewRepository(database)
	runner := backup.NewRunner(backup.Dependencies{
		Repository:         repository,
		Archiver:           files.New(),
		Stores:             stores,
		Retainer:           retentionAdapter{},
		Locker:             lock.New(configuration.App.LockDirectory),
		Clock:              clock.System{},
		TemporaryDirectory: configuration.App.TemporaryDirectory,
	})
	return &App{
		configuration: configuration,
		runner:        runner,
		repository:    repository,
		closeDatabase: closeDatabase,
	}, nil
}

func (a *App) Configuration() config.Config { return a.configuration }

func (a *App) RunBackup(ctx context.Context, siteName string, force bool) (backup.RunResult, error) {
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return backup.RunResult{SiteName: siteName, Status: backup.StatusFailed}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return backup.RunResult{SiteName: siteName, Status: backup.StatusFailed}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	return a.runner.Run(ctx, site, force)
}

func (a *App) LastSuccessful(ctx context.Context, siteName string) (*history.BackupRun, error) {
	return a.repository.LastSuccessful(ctx, siteName)
}

func (a *App) ListRuns(ctx context.Context, siteName string, limit int) ([]history.BackupRun, error) {
	return a.repository.ListRuns(ctx, history.RunFilter{Site: siteName, Limit: limit})
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		if a.closeDatabase != nil {
			a.closeErr = a.closeDatabase()
		}
	})
	return a.closeErr
}

type retentionAdapter struct{}

func (retentionAdapter) Apply(ctx context.Context, store storage.Store, sitePrefix string, keepLast int) error {
	return retention.Apply(ctx, store, sitePrefix, keepLast)
}
