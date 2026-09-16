package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/platform/lock"
)

// BackupActivity is a stable local process-control view. Times are UTC when
// serialized and elapsed is calculated at display time.
type BackupActivity struct {
	Site           string    `json:"site"`
	Mode           string    `json:"mode,omitempty"`
	State          string    `json:"state"`
	PID            int       `json:"pid,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	ElapsedSeconds int64     `json:"elapsed_seconds"`
}

// BackupControl loads only config, local locks and the application logger.
// Unlike Open it never resolves remote storage, creates storage clients, or
// prepares exporters. History is opened only while building an activity view.
type BackupControl struct {
	configuration config.Config
	locks         *lock.Locker
	logger        *appLogger
	closeLogger   func() error
}

func OpenBackupControl(ctx context.Context, configDir string) (*BackupControl, error) {
	configuration, err := config.Load(ctx, configDir)
	if err != nil {
		return nil, err
	}
	logger, closeLogger, err := openAppLogger(configuration.App)
	if err != nil {
		return nil, apperror.Wrap(apperror.CategoryPreflight, "could not open the application log", err)
	}
	return &BackupControl{configuration: configuration, locks: lock.New(configuration.App.LockDirectory), logger: logger, closeLogger: closeLogger}, nil
}

func (c *BackupControl) Close() error { return c.closeLogger() }

func (c *BackupControl) Active(ctx context.Context, site string) ([]BackupActivity, error) {
	runs, err := c.runningRuns(ctx)
	if err != nil {
		return nil, err
	}
	modes := make(map[string]string, len(c.configuration.Sites))
	sites := make(map[string]struct{}, len(c.configuration.Sites)+len(runs))
	for _, configured := range c.configuration.Sites {
		modes[configured.Name] = configured.BackupMode
		sites[configured.Name] = struct{}{}
	}
	for _, run := range runs {
		sites[run.SiteName] = struct{}{}
	}
	lockSites, err := c.locks.Sites()
	if err != nil {
		return nil, err
	}
	for _, name := range lockSites {
		sites[name] = struct{}{}
	}
	if site != "" {
		sites = map[string]struct{}{site: {}}
	}

	var active []BackupActivity
	matchedRuns := make(map[string]bool)
	now := time.Now().UTC()
	for name := range sites {
		inspection, err := c.locks.Inspect(ctx, name)
		if err != nil {
			return nil, err
		}
		if !inspection.Held {
			continue
		}
		entry := BackupActivity{Site: name, Mode: modes[name], State: "unknown", StartedAt: now}
		if inspection.Owner != nil {
			entry.State, entry.PID, entry.StartedAt = "running", inspection.Owner.PID, inspection.Owner.AcquiredAt.UTC()
			for _, run := range runs {
				if run.SiteName == name && !run.StartedAt.Before(entry.StartedAt) {
					entry.RunID, matchedRuns[run.ID] = run.ID, true
					break
				}
			}
		}
		entry.ElapsedSeconds = elapsedSeconds(now, entry.StartedAt)
		active = append(active, entry)
	}
	for _, run := range runs {
		if site != "" && run.SiteName != site || matchedRuns[run.ID] {
			continue
		}
		active = append(active, BackupActivity{Site: run.SiteName, Mode: modes[run.SiteName], State: "stale", RunID: run.ID, StartedAt: run.StartedAt.UTC(), ElapsedSeconds: elapsedSeconds(now, run.StartedAt)})
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Site < active[j].Site })
	return active, nil
}

func (c *BackupControl) runningRuns(ctx context.Context) ([]history.BackupRun, error) {
	database, closeDatabase, err := history.Open(c.configuration.App.StateDatabase)
	if err != nil {
		return nil, apperror.Wrap(apperror.CategoryPersistence, "could not open the backup history database", err)
	}
	defer closeDatabase()
	if err := history.Migrate(ctx, database); err != nil {
		return nil, apperror.Wrap(apperror.CategoryPersistence, "could not migrate the backup history database", err)
	}
	return history.NewRepository(database).ListRunning(ctx)
}

// Stop verifies all targets first, signals each unique owner once, then waits
// for every affected lock. No database mutation, unlock, retry, or SIGKILL is
// performed here; the target process records its own cancelled result.
func (c *BackupControl) Stop(ctx context.Context, site string, all bool, timeout time.Duration) ([]BackupActivity, error) {
	if all == (site != "") {
		return nil, fmt.Errorf("specify exactly one site or --all")
	}
	activities, err := c.Active(ctx, "")
	if err != nil {
		return nil, err
	}
	valid := make(map[int][]BackupActivity)
	for _, activity := range activities {
		if activity.State == "running" {
			valid[activity.PID] = append(valid[activity.PID], activity)
		}
	}
	var targets []BackupActivity
	if all {
		if len(activities) == 0 {
			return nil, fmt.Errorf("no active backup process found")
		}
		for _, activity := range activities {
			if activity.State != "running" {
				return nil, fmt.Errorf("cannot stop %s backup for site %q automatically", activity.State, activity.Site)
			}
		}
		for _, group := range valid {
			targets = append(targets, group...)
		}
	} else {
		var chosen *BackupActivity
		for i := range activities {
			if activities[i].Site == site {
				chosen = &activities[i]
				break
			}
		}
		if chosen == nil {
			return nil, fmt.Errorf("no live backup holds site lock %q", site)
		}
		if chosen.State != "running" {
			return nil, fmt.Errorf("cannot stop %s backup for site %q automatically", chosen.State, site)
		}
		if shared := valid[chosen.PID]; len(shared) > 1 {
			names := make([]string, 0, len(shared))
			for _, item := range shared {
				names = append(names, item.Site)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("backup process %d also holds site locks %v; use backup stop --all", chosen.PID, names)
		}
		targets = []BackupActivity{*chosen}
	}
	for pid, group := range valid {
		selected := false
		for _, target := range targets {
			if target.PID == pid {
				selected = true
				break
			}
		}
		if !selected {
			continue
		}
		inspection, err := c.locks.Inspect(ctx, group[0].Site)
		if err != nil || inspection.Owner == nil {
			return nil, fmt.Errorf("reverify backup process %d: %w", pid, err)
		}
		c.logger.write(logInfo, "backup_stop_requested", "pid", pid, "sites", siteNames(group))
		if err := c.locks.SignalTerm(*inspection.Owner); err != nil {
			c.logger.write(logError, "backup_stop_failed", "pid", pid, "sites", siteNames(group))
			return nil, err
		}
	}
	for _, target := range targets {
		if err := c.locks.WaitReleased(ctx, target.Site, timeout); err != nil {
			c.logger.write(logError, "backup_stop_failed", "pid", target.PID, "site", target.Site)
			return nil, err
		}
	}
	c.logger.write(logInfo, "backup_stopped", "sites", siteNames(targets))
	return targets, nil
}

func siteNames(activities []BackupActivity) []string {
	names := make([]string, 0, len(activities))
	for _, activity := range activities {
		names = append(names, activity.Site)
	}
	sort.Strings(names)
	return names
}

func elapsedSeconds(now, started time.Time) int64 {
	if started.After(now) {
		return 0
	}
	return int64(now.Sub(started).Seconds())
}
