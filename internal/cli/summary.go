package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
)

type summaryView struct {
	Name                     string               `json:"name"`
	Enabled                  bool                 `json:"enabled"`
	BackupMode               string               `json:"backup_mode"`
	Status                   string               `json:"status"`
	LastBackupAt             *time.Time           `json:"last_backup_at"`
	LastBackupStatus         *string              `json:"last_backup_status"`
	LastBackupDurationMillis *int64               `json:"last_backup_duration_millis"`
	LastBackupSize           *int64               `json:"last_backup_size"`
	SuccessfulBackups        int                  `json:"successful_backups"`
	TotalRecordedSize        int64                `json:"total_recorded_size"`
	Destinations             []summaryDestination `json:"destinations"`
	Retention                summaryRetention     `json:"retention"`
}

type summaryDestination struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Primary bool   `json:"primary"`
}

type summaryRetention struct {
	KeepLast int `json:"keep_last"`
}

// buildSummaries renders one view per configured site, sorted by name.
// runs must be ordered by started_at DESC (ListRuns contract); the first
// match per site is therefore its latest run. Runs for sites that are no
// longer configured are ignored entirely.
// # ponytail: whole-table scan of runs, add per-site SQL aggregates in
// internal/history if the history table grows
func buildSummaries(cfg config.Config, runs []history.BackupRun, filter string) []summaryView {
	views := make([]summaryView, 0, len(cfg.Sites))
	for _, site := range cfg.Sites {
		if filter != "" && site.Name != filter {
			continue
		}
		view := summaryView{
			Name:         site.Name,
			Enabled:      site.Enabled,
			BackupMode:   site.BackupMode,
			Status:       "idle",
			Destinations: make([]summaryDestination, 0, len(site.Destinations)),
			Retention:    summaryRetention{KeepLast: site.Policy.KeepLast},
		}
		if !site.Enabled {
			view.Status = "disabled"
		}
		for _, destination := range site.Destinations {
			storage := cfg.Storages[destination.Storage]
			view.Destinations = append(view.Destinations, summaryDestination{
				Name:    destination.Storage,
				Type:    storage.Type,
				Primary: storage.Primary,
			})
		}
		var latest *history.BackupRun
		for i := range runs {
			if runs[i].SiteName != site.Name {
				continue
			}
			if latest == nil {
				latest = &runs[i]
				if view.Status != "disabled" && runs[i].Status == history.StatusRunning {
					view.Status = "running"
				}
			}
			if runs[i].Status == history.StatusSuccess {
				view.SuccessfulBackups++
				view.TotalRecordedSize += summarizePackages(runs[i].Packages).logicalSize
			}
		}
		if latest != nil {
			at := latest.StartedAt.UTC()
			view.LastBackupAt = &at
			status := string(latest.Status)
			view.LastBackupStatus = &status
			if latest.FinishedAt != nil {
				millis := latest.DurationMillis
				view.LastBackupDurationMillis = &millis
			}
			if latest.Status == history.StatusSuccess {
				size := summarizePackages(latest.Packages).logicalSize
				view.LastBackupSize = &size
			}
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

func writeSummaryText(output io.Writer, views []summaryView) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(output, "No backup sites configured.")
		return err
	}
	color := ansiColor{on: isTerminalWriter(output)}
	for index, view := range views {
		if _, err := fmt.Fprintf(output, "%s\n", color.bold(view.Name)); err != nil {
			return err
		}
		lines := []struct {
			label string
			value string
		}{
			{"Status", color.status(view.Status)},
			{"Enabled", map[bool]string{true: "yes", false: "no"}[view.Enabled]},
			{"Backup Mode", view.BackupMode},
			{"Last Backup", view.lastBackupText()},
			{"Last Backup Status", view.lastStatusText()},
			{"Last Backup Duration", view.lastDurationText()},
			{"Last Backup Size", view.lastSizeText()},
			{"Successful Backups", fmt.Sprintf("%d", view.SuccessfulBackups)},
			{"Total Recorded Size", humanBytes(view.TotalRecordedSize)},
			{"Destinations", view.destinationsText()},
			{"Retention", fmt.Sprintf("keep last %d", view.Retention.KeepLast)},
		}
		for _, line := range lines {
			if _, err := fmt.Fprintf(output, "%-20s: %s\n", line.label, line.value); err != nil {
				return err
			}
		}
		if index < len(views)-1 {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
	}
	return nil
}

// ansiColor wraps values in terminal escape codes only when the output is a
// character device; piped or redirected output stays plain text.
type ansiColor struct{ on bool }

func (c ansiColor) bold(value string) string   { return c.wrap("1", value) }
func (c ansiColor) dim(value string) string    { return c.wrap("2", value) }
func (c ansiColor) green(value string) string  { return c.wrap("32", value) }
func (c ansiColor) yellow(value string) string { return c.wrap("33", value) }
func (c ansiColor) red(value string) string    { return c.wrap("31", value) }

func (c ansiColor) status(value string) string {
	switch value {
	case "disabled":
		return c.dim(value)
	case "running", "no_change":
		return c.yellow(value)
	case "failed", "cancelled":
		return c.red(value)
	default:
		return c.green(value)
	}
}

func (c ansiColor) wrap(code, value string) string {
	if !c.on {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

// isTerminalWriter reports whether w is a character device (a TTY).
func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (v summaryView) lastBackupText() string {
	if v.LastBackupAt == nil {
		return "-"
	}
	return v.LastBackupAt.Local().Format("02 Jan 2006, 15:04 MST")
}

func (v summaryView) lastStatusText() string {
	if v.LastBackupStatus == nil {
		return "-"
	}
	return *v.LastBackupStatus
}

func (v summaryView) lastDurationText() string {
	if v.LastBackupDurationMillis != nil {
		return (time.Duration(*v.LastBackupDurationMillis) * time.Millisecond).String()
	}
	if v.LastBackupStatus != nil && *v.LastBackupStatus == string(history.StatusRunning) {
		return "in progress"
	}
	return "-"
}

func (v summaryView) lastSizeText() string {
	if v.LastBackupSize == nil {
		return "-"
	}
	return humanBytes(*v.LastBackupSize)
}

func (v summaryView) destinationsText() string {
	labels := make([]string, 0, len(v.Destinations))
	for _, destination := range v.Destinations {
		label := fmt.Sprintf("%s (%s)", destination.Name, destination.Type)
		if destination.Primary {
			label += " [primary]"
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, ", ")
}
