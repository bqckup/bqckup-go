package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	"github.com/bqckup/bqckup-go/internal/backup/files"
	restic "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/clock"
	"github.com/bqckup/bqckup-go/internal/config"
	resticfacade "github.com/bqckup/bqckup-go/internal/engine/restic/facade"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/notify"
	"github.com/bqckup/bqckup-go/internal/platform/lock"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/bqckup/bqckup-go/internal/retention"
	"github.com/bqckup/bqckup-go/internal/storage"
	localstorage "github.com/bqckup/bqckup-go/internal/storage/local"
	"github.com/bqckup/bqckup-go/internal/storage/remoteconfig"
	"github.com/bqckup/bqckup-go/internal/storage/s3compat"
)

type remoteStorageResolver interface {
	Resolve(context.Context, map[string]config.Storage) (map[string]config.Storage, error)
}

type App struct {
	configuration config.Config
	runner        *backup.Runner
	repository    *history.Repository
	stores        map[string]storage.Store
	snapshots     backup.SnapshotLister
	restorer      backup.SnapshotRestorer
	closeOnce     sync.Once
	closeErr      error
	closeDatabase func() error
}

func Open(ctx context.Context, configDir string) (*App, error) {
	configuration, err := config.Load(ctx, configDir)
	if err != nil {
		return nil, err
	}
	configuration, err = resolveRemoteStorageConfiguration(ctx, configuration, remoteconfig.New())
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

	stores, err := buildStores(ctx, configuration.Storages)
	if err != nil {
		_ = closeDatabase()
		return nil, err
	}
	databaseExporters, err := buildDatabaseExporters(ctx, configuration, process.NewProcessRunner())
	if err != nil {
		_ = closeDatabase()
		return nil, err
	}

	repository := history.NewRepository(database)
	engine := resticfacade.NewEngine()
	runner := backup.NewRunner(backup.Dependencies{
		Repository:         repository,
		Archiver:           files.New(),
		IncrementalEngine:  engine,
		DatabaseExporters:  databaseExporters,
		Stores:             stores,
		Storages:           configuration.Storages,
		Retainer:           retentionAdapter{},
		Locker:             lock.New(configuration.App.LockDirectory),
		Notifier:           buildNotifier(configuration.Notifications, os.LookupEnv),
		Clock:              clock.System{},
		TemporaryDirectory: configuration.App.TemporaryDirectory,
	})
	return &App{
		configuration: configuration,
		runner:        runner,
		repository:    repository,
		stores:        stores,
		snapshots:     engine,
		restorer:      engine,
		closeDatabase: closeDatabase,
	}, nil
}

// buildNotifier constructs the notification dispatcher from the configured
// channels and routes. A config without a notifications section yields nil,
// which the runner treats as a no-op.
func buildNotifier(notifications config.Notifications, lookupEnv func(string) (string, bool)) backup.Notifier {
	if len(notifications.Channels) == 0 {
		return nil
	}
	channels := make(map[string]notify.Channel, len(notifications.Channels))
	for name, channel := range notifications.Channels {
		switch channel.Type {
		case "smtp":
			channels[name] = notify.NewSMTP(name, channel, lookupEnv, nil)
		case "webhook":
			channels[name] = notify.NewWebhook(name, channel.URLEnv, lookupEnv)
		case "discord":
			channels[name] = notify.NewDiscord(name, channel.WebhookURLEnv, lookupEnv)
		}
	}
	return notify.NewDispatcher(channels, notifications.Routes)
}

func resolveRemoteStorageConfiguration(ctx context.Context, configuration config.Config, resolver remoteStorageResolver) (config.Config, error) {
	storages, err := resolver.Resolve(ctx, configuration.Storages)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return config.Config{}, err
		}
		return config.Config{}, apperror.Wrap(apperror.CategoryPreflight, "could not load remote storage configuration", err)
	}
	configuration.Storages = storages
	if err := configuration.Validate(); err != nil {
		return config.Config{}, apperror.Wrap(apperror.CategoryConfig, "remote storage configuration is invalid", err)
	}
	return configuration, nil
}

// buildDatabaseExporters constructs one exporter per configured database
// engine that is enabled somewhere.
func buildDatabaseExporters(ctx context.Context, configuration config.Config, process process.ProcessRunner) (map[string]backup.Exporter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exporters := make(map[string]backup.Exporter)
	for _, site := range configuration.Sites {
		if !site.Enabled {
			continue
		}
		for _, source := range site.Sources.Databases {
			if !source.Enabled {
				continue
			}
			if _, exists := exporters[source.Engine]; exists {
				continue
			}
			var exporter *databaseexporter.ProcessExporter
			switch source.Engine {
			case "mysql":
				exporter = databaseexporter.NewMySQL(process)
			case "postgres":
				exporter = databaseexporter.NewPostgres(process)
			default:
				return nil, apperror.Wrap(apperror.CategoryConfig, "unsupported database exporter", nil)
			}
			if err := exporter.Preflight(); err != nil {
				return nil, apperror.Wrap(apperror.CategoryPreflight, "required database exporter is unavailable", err)
			}
			exporters[source.Engine] = exporter
		}
	}
	return exporters, nil
}

func buildStores(ctx context.Context, configured map[string]config.Storage) (map[string]storage.Store, error) {
	stores := make(map[string]storage.Store, len(configured))
	for name, value := range configured {
		var store storage.Store
		var err error
		switch value.Type {
		case "local":
			store, err = localstorage.New(value.Directory)
		case "s3", "r2":
			store, err = s3compat.New(ctx, s3compat.Options{
				Provider:        s3compat.Provider(value.Type),
				Bucket:          value.Bucket,
				Region:          value.Region,
				Endpoint:        value.Endpoint,
				Prefix:          value.Prefix,
				AccessKeyID:     value.AccessKeyID,
				SecretAccessKey: value.SecretAccessKey,
			})
		default:
			err = errors.New("unsupported storage type")
		}
		if err != nil {
			return nil, apperror.Wrap(apperror.CategoryPreflight, "could not prepare a storage destination", err)
		}
		stores[name] = store
	}
	return stores, nil
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

// RunEnabledBackups runs every enabled site in deterministic configuration
// order. It stops at the first failure so the existing error category and
// process exit-code contract remain intact.
func (a *App) RunEnabledBackups(ctx context.Context, force bool) ([]backup.RunResult, error) {
	results := make([]backup.RunResult, 0, len(a.configuration.Sites))
	for _, site := range a.configuration.Sites {
		if !site.Enabled {
			continue
		}
		result, err := a.RunBackup(ctx, site.Name, force)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (a *App) LastSuccessful(ctx context.Context, siteName string) (*history.BackupRun, error) {
	return a.repository.LastSuccessful(ctx, siteName)
}

// UnlockRepository removes stale repository locks for a site.
func (a *App) UnlockRepository(ctx context.Context, siteName string) error {
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	return a.runner.Unlock(ctx, site)
}

func (a *App) ListRuns(ctx context.Context, siteName string, limit int) ([]history.BackupRun, error) {
	return a.repository.ListRuns(ctx, history.RunFilter{Site: siteName, Limit: limit})
}

// ListRemoteContents lists the live remote contents of one destination of
// one site. The site must exist and be enabled (mirrors RunBackup), the
// destination must exist and be one the site actually uses; local
// destinations are rejected inside the use case with a pointer to history.
func (a *App) ListRemoteContents(ctx context.Context, siteName, destinationName string) (backup.Listing, error) {
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	storageConfig, ok := a.configuration.Storages[destinationName]
	if !ok {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("storage destination %q was not found", destinationName), nil)
	}
	if !siteUsesDestination(site, destinationName) {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q does not send backups to destination %q", siteName, destinationName), nil)
	}
	store, ok := a.stores[destinationName]
	if !ok || store == nil {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryInternal, "a configured storage destination is unavailable", nil)
	}
	return (&backup.Lister{Snapshots: a.snapshots}).List(ctx, destinationName, site, storageConfig, store)
}

// ListSiteSnapshots lists the live snapshots of one incremental site on
// one of its destinations, read directly from the repository. The site
// must exist, be enabled, use incremental mode, and actually send backups
// to the destination. No storage.Store is resolved: the lister only needs
// the storage document to build the repository configuration.
func (a *App) ListSiteSnapshots(ctx context.Context, siteName, destinationName string) (backup.Listing, error) {
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	if site.BackupMode != "incremental" {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; use 'bqckup history list --site %s --details' to inspect stored archives",
			siteName, siteName), nil)
	}
	storageConfig, ok := a.configuration.Storages[destinationName]
	if !ok {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("storage destination %q was not found", destinationName), nil)
	}
	if !siteUsesDestination(site, destinationName) {
		return backup.Listing{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q does not send backups to destination %q", siteName, destinationName), nil)
	}
	return (&backup.Lister{Snapshots: a.snapshots}).ListSiteSnapshots(ctx, destinationName, site, storageConfig)
}

func siteUsesDestination(site config.Site, destination string) bool {
	for _, configured := range site.Destinations {
		if configured.Storage == destination {
			return true
		}
	}
	return false
}

// RestoreSnapshot restores one snapshot of one incremental site into the
// target directory. Validation mirrors ListSiteSnapshots; the confirm
// callback is passed through to the engine unchanged.
func (a *App) RestoreSnapshot(ctx context.Context, siteName, destinationName, snapshotRef, target string, confirm restic.RestoreOverwrite) (backup.RestoreResult, error) {
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return backup.RestoreResult{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return backup.RestoreResult{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	if site.BackupMode != "incremental" {
		return backup.RestoreResult{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; use 'bqckup history list --site %s --details' to inspect stored archives",
			siteName, siteName), nil)
	}
	storageConfig, ok := a.configuration.Storages[destinationName]
	if !ok {
		return backup.RestoreResult{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("storage destination %q was not found", destinationName), nil)
	}
	if !siteUsesDestination(site, destinationName) {
		return backup.RestoreResult{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q does not send backups to destination %q", siteName, destinationName), nil)
	}
	return (&backup.Restorer{Snapshots: a.snapshots, Engine: a.restorer}).RestoreSiteSnapshot(ctx, destinationName, snapshotRef, target, site, storageConfig, confirm)
}

// parseSiteFromKey extracts the site name from a download-link key. Valid
// keys start with bqckup/<site>/ and carry at least one segment after the
// site.
func parseSiteFromKey(key string) (string, error) {
	parts := strings.Split(key, "/")
	if len(parts) < 3 || parts[0] != "bqckup" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("key %q must start with bqckup/<site>/", key)
	}
	return parts[1], nil
}

// Link creates a temporary download link for one artifact of a remote
// destination. The site is parsed from the key; it must exist, be enabled,
// use full mode, and send backups to the destination. Nothing is written to
// history and the remote only receives one existence check.
func (a *App) Link(ctx context.Context, destinationName, key string, expires time.Duration) (storage.DownloadLink, error) {
	siteName, err := parseSiteFromKey(key)
	if err != nil {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, err.Error(), nil)
	}
	if err := storage.ValidateKey(key); err != nil {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, "unsafe storage key", nil)
	}
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	if _, ok := a.configuration.Storages[destinationName]; !ok {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("storage destination %q was not found", destinationName), nil)
	}
	if !siteUsesDestination(site, destinationName) {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q does not send backups to destination %q", siteName, destinationName), nil)
	}
	store, ok := a.stores[destinationName]
	if !ok || store == nil {
		return storage.DownloadLink{}, apperror.Wrap(apperror.CategoryInternal, "a configured storage destination is unavailable", nil)
	}
	return (&backup.Linker{}).Link(ctx, destinationName, site, store, key, expires)
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
