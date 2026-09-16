package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	"github.com/bqckup/bqckup-go/internal/backup/files"
	incremental "github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/clock"
	"github.com/bqckup/bqckup-go/internal/config"
	incrementalfacade "github.com/bqckup/bqckup-go/internal/engine/incremental/facade"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/notify"
	"github.com/bqckup/bqckup-go/internal/platform/lock"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/bqckup/bqckup-go/internal/report"
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
	configuration    config.Config
	runner           *backup.Runner
	repository       *history.Repository
	stores           map[string]storage.Store
	snapshots        backup.SnapshotLister
	restorer         backup.SnapshotRestorer
	checker          backup.RepositoryChecker
	repairer         backup.IndexRepairer
	reportBuilder    *report.Builder
	reportDispatcher *report.Dispatcher
	closeOnce        sync.Once
	closeErr         error
	closeDatabase    func() error
	logger           *appLogger
	backupProgress   backup.Progress
	closeLogger      func() error
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
	logger, closeLogger, err := openAppLogger(configuration.App)
	if err != nil {
		_ = closeDatabase()
		return nil, apperror.Wrap(apperror.CategoryPreflight, "could not open the application log", err)
	}

	repository := history.NewRepository(database)
	engine := &incrementalfacade.Engine{}
	runner := backup.NewRunner(backup.Dependencies{
		ServerID:           configuration.BackupNamespace(),
		Repository:         repository,
		Archiver:           &files.Archiver{},
		IncrementalEngine:  engine,
		DatabaseExporters:  databaseExporters,
		Stores:             stores,
		Storages:           configuration.Storages,
		Retainer:           retentionAdapter{},
		Locker:             lock.New(configuration.App.LockDirectory),
		Notifier:           buildNotifier(configuration.Notifications),
		Clock:              clock.System{},
		TemporaryDirectory: configuration.App.TemporaryDirectory,
	})
	reportBuilder := report.NewBuilder(repository)
	reportDispatcher := buildReportDispatcher(configuration.Notifications, repository)
	return &App{
		configuration:    configuration,
		runner:           runner,
		repository:       repository,
		stores:           stores,
		snapshots:        engine,
		restorer:         engine,
		checker:          engine,
		repairer:         engine,
		reportBuilder:    reportBuilder,
		reportDispatcher: reportDispatcher,
		closeDatabase:    closeDatabase,
		logger:           logger,
		closeLogger:      closeLogger,
	}, nil
}

// buildReportDispatcher constructs the report dispatcher from the configured
// channels and routes. It shares the same channel map as the backup notifier.
func buildReportDispatcher(notifications config.Notifications, repo *history.Repository) *report.Dispatcher {
	return report.NewDispatcher(buildNotificationChannels(notifications.Channels), notifications.Routes, repo)
}

// buildNotifier constructs the notification dispatcher from the configured
// channels and routes. A config without a notifications section yields nil,
// which the runner treats as a no-op.
func buildNotifier(notifications config.Notifications) backup.Notifier {
	if len(notifications.Channels) == 0 {
		return nil
	}
	return notify.NewDispatcher(buildNotificationChannels(notifications.Channels), notifications.Routes)
}

func buildNotificationChannels(configured map[string]config.Channel) map[string]notify.Channel {
	channels := make(map[string]notify.Channel, len(configured))
	for name, channel := range configured {
		switch channel.Type {
		case "smtp":
			channels[name] = notify.NewSMTP(name, channel, nil)
		case "webhook":
			channels[name] = notify.NewWebhook(name, channel.URL)
		case "discord":
			channels[name] = notify.NewDiscord(name, channel.WebhookURL)
		}
	}
	return channels
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

// SetBackupProgress configures the optional progress reporter used by
// subsequent backup runs in this application instance.
func (a *App) SetBackupProgress(progress backup.Progress) {
	a.backupProgress = progress
}

func (a *App) RunBackup(ctx context.Context, siteName string, force bool) (backup.RunResult, error) {
	return a.runBackup(ctx, siteName, force, nil)
}

func (a *App) runBackup(ctx context.Context, siteName string, force bool, progress backup.Progress) (backup.RunResult, error) {
	started := time.Now()
	a.logger.write(logInfo, fmt.Sprintf("event=backup_start site=%q force=%t", siteName, force))
	site, ok := a.configuration.Site(siteName)
	if !ok {
		err := apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
		a.logBackupFinished(siteName, backup.RunResult{SiteName: siteName, Status: backup.StatusFailed}, started, err)
		return backup.RunResult{SiteName: siteName, Status: backup.StatusFailed}, err
	}
	if !site.Enabled {
		err := apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
		a.logBackupFinished(siteName, backup.RunResult{SiteName: siteName, Status: backup.StatusFailed}, started, err)
		return backup.RunResult{SiteName: siteName, Status: backup.StatusFailed}, err
	}
	destinations := make([]string, 0, len(site.Destinations))
	for _, destination := range site.Destinations {
		destinations = append(destinations, destination.Storage)
	}
	databaseNames := make([]string, 0, len(site.Sources.Databases))
	for _, source := range site.Sources.Databases {
		if source.Enabled {
			databaseNames = append(databaseNames, source.Name)
		}
	}
	a.logger.write(logInfo, fmt.Sprintf(
		"event=backup_plan site=%q mode=%q namespace=%q destinations=%q file_sources=%d database_sources=%d keep_last=%d minimum_interval=%q",
		siteName, site.BackupMode, a.configuration.BackupNamespace(), strings.Join(destinations, ","), len(site.Sources.Files.Include), len(databaseNames), site.Policy.KeepLast, site.Policy.MinimumInterval,
	))
	a.logger.write(logDebug, fmt.Sprintf(
		"event=backup_plan_detail site=%q database_names=%q file_excludes=%d follow_symlinks=%t",
		siteName, strings.Join(databaseNames, ","), len(site.Sources.Files.Exclude), site.Sources.Files.FollowSymlinks,
	))
	if progress == nil {
		progress = a.backupProgress
	}
	result, err := a.runner.RunWithProgress(ctx, site, force, newLoggingProgress(a.logger, siteName, progress))
	a.logStoredPackages(ctx, result)
	a.logBackupFinished(siteName, result, started, err)
	if err == nil {
		for _, warning := range result.Warnings {
			a.logger.write(logWarn, fmt.Sprintf("event=backup_warning site=%q run_id=%q category=%q warning=%q", siteName, result.RunID, "retention", warning))
		}
	}
	return result, err
}

func (a *App) logStoredPackages(ctx context.Context, result backup.RunResult) {
	if result.RunID == "" || a.repository == nil {
		return
	}
	packages, err := a.repository.RunPackages(ctx, result.RunID)
	if err != nil {
		a.logger.write(logDebug, fmt.Sprintf("event=backup_packages_unavailable site=%q run_id=%q error=%q", result.SiteName, result.RunID, apperror.DiagnosticMessage(err)))
		return
	}
	for _, pkg := range packages {
		a.logger.write(logInfo, fmt.Sprintf(
			"event=package_stored site=%q run_id=%q source_kind=%q source_name=%q destination=%q object_key=%q size_bytes=%d status=%q",
			result.SiteName, result.RunID, pkg.SourceKind, pkg.SourceName, pkg.Destination, pkg.ObjectKey, pkg.Size, pkg.Status,
		))
	}
}

func (a *App) logBackupFinished(siteName string, result backup.RunResult, started time.Time, err error) {
	message := fmt.Sprintf(
		"event=backup_finished site=%q run_id=%q status=%q duration_ms=%d files_skipped=%d reclaimed_bytes=%d warning_count=%d skip_reason=%q",
		siteName, result.RunID, result.Status, time.Since(started).Milliseconds(), result.FilesSkipped, result.ReclaimedBytes, len(result.Warnings), result.SkipReason,
	)
	if err != nil {
		a.logger.write(logError, fmt.Sprintf("%s category=%q error=%q", message, apperror.CategoryOf(err), apperror.DiagnosticMessage(err)))
		return
	}
	a.logger.write(logInfo, message)
}

// BackupRunProgress contains only the non-sensitive configuration needed to
// report progress while a batch of enabled sites is running. Result is nil
// when the site starts and populated when it finishes.
type BackupRunProgress struct {
	SiteName     string
	BackupMode   string
	Destinations []string
	Result       *backup.RunResult
	Error        error
}

type BackupRunObserver func(BackupRunProgress)

// RunEnabledBackups runs enabled sites concurrently in two bounded lanes: one
// incremental site and one full site at a time. A failure is collected and
// does not prevent other sites from running; the combined error is returned
// after all active work finishes. Context cancellation stops queued work and
// is passed to active runs. The optional observer is called synchronously with
// sanitized site progress.
func (a *App) RunEnabledBackups(ctx context.Context, force bool, observer BackupRunObserver) ([]backup.RunResult, error) {
	return runEnabledBackups(ctx, a.configuration.Sites, force, observer,
		func(ctx context.Context, siteName string, force bool) (backup.RunResult, error) {
			return a.runBackup(ctx, siteName, force, backup.NoopProgress{})
		})
}

type batchRunFunc func(context.Context, string, bool) (backup.RunResult, error)

type batchJob struct {
	index int
	site  config.Site
}

type batchOutcome struct {
	result backup.RunResult
	err    error
	done   bool
}

func runEnabledBackups(ctx context.Context, sites []config.Site, force bool, observer BackupRunObserver, run batchRunFunc) ([]backup.RunResult, error) {
	jobs := make([]batchJob, 0, len(sites))
	for _, site := range sites {
		if !site.Enabled {
			continue
		}
		job := batchJob{index: len(jobs), site: site}
		jobs = append(jobs, job)
		if observer != nil {
			observer(batchProgressForSite(site))
		}
	}

	outcomes := make([]batchOutcome, len(jobs))
	var outcomesMu sync.Mutex
	var observerMu sync.Mutex
	var workers sync.WaitGroup
	for _, mode := range []string{"full", "incremental"} {
		mode := mode
		workers.Add(1)
		go func() {
			defer workers.Done()
			for _, job := range jobs {
				jobMode := job.site.BackupMode
				if jobMode == "" {
					jobMode = "full"
				}
				if jobMode != mode {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				result, err := run(ctx, job.site.Name, force)
				outcomesMu.Lock()
				outcomes[job.index] = batchOutcome{result: result, err: err, done: true}
				outcomesMu.Unlock()
				if observer != nil {
					progress := batchProgressForSite(job.site)
					progress.Result = &result
					progress.Error = err
					observerMu.Lock()
					observer(progress)
					observerMu.Unlock()
				}
			}
		}()
	}
	workers.Wait()

	results := make([]backup.RunResult, 0, len(jobs))
	var runErr error
	for _, outcome := range outcomes {
		if !outcome.done {
			continue
		}
		results = append(results, outcome.result)
		if outcome.err != nil {
			runErr = errors.Join(runErr, outcome.err)
		}
	}
	if ctx.Err() != nil {
		runErr = errors.Join(runErr, ctx.Err())
	}
	return results, runErr
}

func batchProgressForSite(site config.Site) BackupRunProgress {
	progress := BackupRunProgress{
		SiteName:     site.Name,
		BackupMode:   site.BackupMode,
		Destinations: make([]string, 0, len(site.Destinations)),
	}
	for _, destination := range site.Destinations {
		progress.Destinations = append(progress.Destinations, destination.Storage)
	}
	return progress
}

func (a *App) LastSuccessful(ctx context.Context, siteName string) (*history.BackupRun, error) {
	return a.repository.LastSuccessful(ctx, siteName, time.Time{})
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
	return (&backup.Lister{ServerID: a.configuration.BackupNamespace(), Snapshots: a.snapshots}).List(ctx, destinationName, site, storageConfig, store)
}

// ListSiteSnapshots lists the live snapshots of one incremental site on
// one of its destinations, read directly from the repository. The site
// must exist, be enabled, use incremental mode, and actually send backups
// to the destination. No storage.Store is resolved: the lister only needs
// the storage document to build the repository configuration.
func (a *App) ListSiteSnapshots(ctx context.Context, siteName, destinationName string) (backup.Listing, error) {
	site, storageConfig, err := a.incrementalSiteDestination(siteName, destinationName)
	if err != nil {
		return backup.Listing{}, err
	}
	return (&backup.Lister{ServerID: a.configuration.BackupNamespace(), Snapshots: a.snapshots}).ListSiteSnapshots(ctx, destinationName, site, storageConfig)
}

func siteUsesDestination(site config.Site, destination string) bool {
	for _, configured := range site.Destinations {
		if configured.Storage == destination {
			return true
		}
	}
	return false
}

// CheckRepository runs the read-only integrity check of one incremental
// site's repository on one of its destinations. Validation mirrors
// ListSiteSnapshots: the site must exist, be enabled, use incremental
// mode, and actually send backups to the destination. Nothing is written
// to history and no storage.Store is resolved.
func (a *App) CheckRepository(ctx context.Context, siteName, destinationName string, readData bool) (backup.CheckOutcome, error) {
	site, storageConfig, err := a.incrementalSiteDestination(siteName, destinationName)
	if err != nil {
		return backup.CheckOutcome{}, err
	}
	return (&backup.Checker{ServerID: a.configuration.BackupNamespace(), Engine: a.checker}).CheckSite(ctx, destinationName, readData, site, storageConfig)
}

// RepairIndex rebuilds the index files of one incremental site's repository
// on one of its destinations. Validation mirrors CheckRepository: the site
// must exist, be enabled, use incremental mode, and actually send backups to
// the destination. Nothing is written to history and no storage.Store is resolved.
func (a *App) RepairIndex(ctx context.Context, siteName, destinationName string) (backup.RepairOutcome, error) {
	site, storageConfig, err := a.incrementalSiteDestination(siteName, destinationName)
	if err != nil {
		return backup.RepairOutcome{}, err
	}
	return (&backup.Repairer{ServerID: a.configuration.BackupNamespace(), Engine: a.repairer}).RepairSite(ctx, destinationName, site, storageConfig)
}

// RestoreSnapshot restores one snapshot of one incremental site into the
// target directory. Validation mirrors ListSiteSnapshots; the confirm
// callback is passed through to the engine unchanged.
func (a *App) RestoreSnapshot(ctx context.Context, siteName, destinationName, snapshotRef, target string, confirm incremental.RestoreOverwrite) (backup.RestoreResult, error) {
	site, storageConfig, err := a.incrementalSiteDestination(siteName, destinationName)
	if err != nil {
		return backup.RestoreResult{}, err
	}
	return (&backup.Restorer{ServerID: a.configuration.BackupNamespace(), Snapshots: a.snapshots, Engine: a.restorer}).RestoreSiteSnapshot(ctx, destinationName, snapshotRef, target, site, storageConfig, confirm)
}

func (a *App) incrementalSiteDestination(siteName, destinationName string) (config.Site, config.Storage, error) {
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return config.Site{}, config.Storage{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return config.Site{}, config.Storage{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	if site.BackupMode != "incremental" {
		return config.Site{}, config.Storage{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf(
			"site %q uses full backup mode; use 'bqckup history list --site %s --details' to inspect stored archives",
			siteName, siteName), nil)
	}
	storageConfig, ok := a.configuration.Storages[destinationName]
	if !ok {
		return config.Site{}, config.Storage{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("storage destination %q was not found", destinationName), nil)
	}
	if !siteUsesDestination(site, destinationName) {
		return config.Site{}, config.Storage{}, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q does not send backups to destination %q", siteName, destinationName), nil)
	}
	return site, storageConfig, nil
}

// parseSiteFromKey extracts the site name from a download-link key. Current
// keys are namespaced as bqckup/<backup_prefix>/<server_id>/<site>/... . The
// legacy bqckup/<site>/... form remains valid when neither value is configured.
func parseSiteFromKey(key, namespace string) (string, error) {
	prefix := path.Join("bqckup", namespace) + "/"
	remainder, ok := strings.CutPrefix(key, prefix)
	site, suffix, cut := strings.Cut(remainder, "/")
	if !ok || !cut || site == "" || suffix == "" {
		return "", fmt.Errorf("key %q must start with %s<site>/", key, prefix)
	}
	return site, nil
}

// Link creates a temporary download link for one package of a remote
// destination. The site is parsed from the key; it must exist, be enabled,
// use full mode, and send backups to the destination. Nothing is written to
// history and the remote only receives one existence check.
func (a *App) Link(ctx context.Context, destinationName, key string, expires time.Duration) (storage.DownloadLink, error) {
	siteName, err := parseSiteFromKey(key, a.configuration.BackupNamespace())
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

// SendDailyReport builds and delivers the daily backup summary report for the
// calendar day containing t in the configured timezone. It is a no-op when
// daily reports are disabled or the report for that day has already been sent.
func (a *App) SendDailyReport(ctx context.Context, t time.Time) error {
	cfg := a.configuration.Reports.Daily
	if !cfg.Enabled {
		return nil
	}
	tz, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("daily report timezone: %w", err)
	}
	data, err := a.reportBuilder.BuildDailyReport(ctx, t, tz, cfg.IncludeEmptyDays)
	if err != nil {
		return err
	}
	return a.reportDispatcher.SendDaily(ctx, data, cfg.NotificationRoute)
}

// SendMonthlyReport builds and delivers the monthly consolidated backup report
// for the calendar month containing t in the configured timezone. It is a
// no-op when monthly reports are disabled or the report for that month has
// already been sent.
func (a *App) SendMonthlyReport(ctx context.Context, t time.Time) error {
	cfg := a.configuration.Reports.Monthly
	if !cfg.Enabled {
		return nil
	}
	tz, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("monthly report timezone: %w", err)
	}
	data, err := a.reportBuilder.BuildMonthlyReport(ctx, t, tz, cfg.IncludeEmptyDays)
	if err != nil {
		return err
	}
	return a.reportDispatcher.SendMonthly(ctx, data, cfg.NotificationRoute)
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		if a.closeDatabase != nil {
			a.closeErr = a.closeDatabase()
		}
		if a.closeLogger != nil {
			a.closeErr = errors.Join(a.closeErr, a.closeLogger())
		}
	})
	return a.closeErr
}

type retentionAdapter struct{}

func (retentionAdapter) Apply(ctx context.Context, store storage.Store, sitePrefix string, keepLast int) error {
	return retention.Apply(ctx, store, sitePrefix, keepLast)
}
