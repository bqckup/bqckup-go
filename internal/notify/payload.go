// Package notify delivers terminal backup-run notifications over SMTP,
// generic webhooks, and Discord webhooks. The concrete dispatcher implements
// backup.Notifier; channels are constructed in internal/app. Delivery is best
// effort: errors are returned for the caller to warn about, and never alter
// run status or history.
package notify

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
)

// Event is a notification event name. Values are the config contract strings.
type Event string

const (
	EventBackupFailed    Event = config.EventBackupFailed
	EventBackupCancelled Event = config.EventBackupCancelled
	EventBackupNoChange  Event = config.EventBackupNoChange
)

// maxPackageKeys caps how many package keys a notification carries, so a
// site with many sources keeps a readable payload and embed.
const maxPackageKeys = 10

// DestinationInfo describes one configured destination in notification payloads.
type DestinationInfo struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket,omitempty"`
	Path   string `json:"path,omitempty"`
}

// SiteReportSummary holds per-site statistics for a scheduled report payload.
type SiteReportSummary struct {
	SiteName               string                     `json:"site_name"`
	TotalRuns              int                        `json:"total_runs"`
	Successful             int                        `json:"successful"`
	Failed                 int                        `json:"failed"`
	Cancelled              int                        `json:"cancelled"`
	Skipped                int                        `json:"skipped"`
	NoChange               int                        `json:"no_change"`
	DurationSeconds        int64                      `json:"duration_seconds"`
	AverageDurationSeconds int64                      `json:"average_duration_seconds"`
	TotalBytes             int64                      `json:"total_bytes"`
	Destinations           []ReportDestinationSummary `json:"destinations,omitempty"`
	LastStatus             string                     `json:"last_status,omitempty"`
	LastRunAt              string                     `json:"last_run_at,omitempty"`
}

// ReportDestinationSummary holds package state for one destination in a report.
type ReportDestinationSummary struct {
	Name          string `json:"name"`
	TotalPackages int    `json:"total_packages"`
	Stored        int    `json:"stored"`
	Failed        int    `json:"failed"`
	TotalBytes    int64  `json:"total_bytes"`
}

// ReportPeriodSummary holds overall statistics for a day or month.
type ReportPeriodSummary struct {
	TotalRuns              int                        `json:"total_runs"`
	Successful             int                        `json:"successful"`
	Failed                 int                        `json:"failed"`
	Cancelled              int                        `json:"cancelled"`
	Skipped                int                        `json:"skipped"`
	NoChange               int                        `json:"no_change"`
	DurationSeconds        int64                      `json:"duration_seconds"`
	AverageDurationSeconds int64                      `json:"average_duration_seconds"`
	TotalBytes             int64                      `json:"total_bytes"`
	Destinations           []ReportDestinationSummary `json:"destinations,omitempty"`
}

// ReportDaySummary holds one calendar day in a monthly report.
type ReportDaySummary struct {
	Date    string              `json:"date"`
	HasRuns bool                `json:"has_runs"`
	Overall ReportPeriodSummary `json:"overall"`
	Sites   []SiteReportSummary `json:"sites,omitempty"`
}

// ReportData carries the structured summary for daily and monthly report
// payloads. It is nil for regular per-run notifications.
type ReportData struct {
	ReportType string              `json:"report_type"`
	Period     string              `json:"period"`
	Overall    ReportPeriodSummary `json:"overall"`
	Days       []ReportDaySummary  `json:"days,omitempty"`
	Sites      []SiteReportSummary `json:"sites"`
}

// Payload is the shared notification payload for every channel. The JSON
// shape is the spec contract; error fields appear only for failed and
// cancelled runs. Hostname and ServerIP identify the machine that ran the
// backup and are filled by the dispatcher when it is built.
type Payload struct {
	Event              Event             `json:"event"`
	RunID              string            `json:"run_id"`
	Site               string            `json:"site"`
	Hostname           string            `json:"hostname"`
	ServerIP           string            `json:"server_ip"`
	Status             string            `json:"status"`
	StartedAt          string            `json:"started_at"`
	FinishedAt         string            `json:"finished_at"`
	DurationSeconds    int64             `json:"duration_seconds"`
	LastSuccessfulAt   string            `json:"last_successful_at,omitempty"`
	FailureStreak      int               `json:"failure_streak"`
	PackageCount       int               `json:"package_count"`
	SizeBytes          int64             `json:"size_bytes"`
	Packages           []string          `json:"packages,omitempty"`
	Destinations       []DestinationInfo `json:"destinations,omitempty"`
	HasDatabaseSources bool              `json:"-"`
	ErrorCategory      string            `json:"error_category,omitempty"`
	ErrorMessage       string            `json:"error_message,omitempty"`
	ReportData         *ReportData       `json:"report,omitempty"`
}

// NewPayload builds the shared payload from a run's facts. ErrorCategory and
// ErrorMessage must already be redacted (apperror.UserMessage).
func NewPayload(input backup.NotifyInput) Payload {
	started := input.StartedAt.UTC()
	finished := input.FinishedAt.UTC()
	duration := finished.Sub(started)
	if duration < 0 {
		duration = 0
	}
	var lastSuccessfulAt string
	if !input.LastSuccessfulAt.IsZero() {
		lastSuccessfulAt = input.LastSuccessfulAt.UTC().Format(time.RFC3339)
	}
	payload := Payload{
		Event:              Event(input.Event),
		RunID:              input.RunID,
		Site:               input.SiteName,
		Status:             string(input.Status),
		StartedAt:          started.Format(time.RFC3339),
		FinishedAt:         finished.Format(time.RFC3339),
		DurationSeconds:    int64(duration.Seconds()),
		LastSuccessfulAt:   lastSuccessfulAt,
		FailureStreak:      input.FailureStreak,
		HasDatabaseSources: input.HasDatabaseSources,
		ErrorCategory:      input.ErrorCategory,
		ErrorMessage:       input.ErrorMessage,
	}
	if len(input.Destinations) > 0 {
		destinations := make([]DestinationInfo, len(input.Destinations))
		for i, d := range input.Destinations {
			destinations[i] = DestinationInfo{
				Name:   d.Name,
				Bucket: d.Bucket,
				Path:   d.Path,
			}
		}
		payload.Destinations = destinations
	}
	payload.PackageCount, payload.SizeBytes, payload.Packages = summarize(input.Packages)
	return payload
}

// summarize counts stored packages once per distinct source and sums their
// sizes, so one package stored to several destinations is not double-counted.
// It also lists their object keys, capped at maxPackageKeys.
func summarize(packages []history.Package) (count int, size int64, keys []string) {
	seen := make(map[[2]string]struct{}, len(packages))
	keys = make([]string, 0, len(packages))
	for _, pkg := range packages {
		key := [2]string{pkg.SourceKind, pkg.SourceName}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		count++
		size += pkg.Size
		if len(keys) < maxPackageKeys {
			keys = append(keys, pkg.ObjectKey)
		}
	}
	return count, size, keys
}

// ServerIdentity reports the machine identity attached to notifications: the
// hostname and the address the machine uses for its default route. Both are
// resolved locally without sending packets; inside a container they describe
// the container, not the host. An empty IP means no route exists.
func ServerIdentity() (hostname, serverIP string) {
	return serverIdentity()
}

func serverIdentity() (hostname, serverIP string) {
	hostname = "unknown"
	if name, err := os.Hostname(); err == nil && name != "" {
		hostname = name
	}
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return hostname, ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		serverIP = addr.IP.String()
	}
	return hostname, serverIP
}

// statusColor returns the channel color for a payload status.
func statusColor(status string) int {
	switch status {
	case string(backup.StatusCancelled), string(backup.StatusNoChange):
		return 0xF1C40F
	default:
		return 0xE74C3C
	}
}

// formatBytes renders a byte count as a short human string.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// humanStatus renders a backup status as a headline verb for non-IT readers.
func humanStatus(status string) string {
	switch backup.Status(status) {
	case backup.StatusFailed:
		return "Backup failed"
	case backup.StatusCancelled:
		return "Backup cancelled"
	case backup.StatusNoChange:
		return "No changes detected"
	default:
		return status
	}
}

// headline renders the lead sentence shared by every human channel.
func headline(payload Payload) string {
	if payload.ReportData != nil {
		return reportHeadline(payload.ReportData)
	}
	if payload.Status == string(backup.StatusNoChange) {
		return "No changes detected for " + payload.Site
	}
	return humanStatus(payload.Status) + " for " + payload.Site
}

// reportHeadline returns the subject/title for a scheduled report.
func reportHeadline(r *ReportData) string {
	switch r.ReportType {
	case "daily":
		return "Daily Backup Report · " + r.Period
	case "monthly":
		return "Monthly Backup Report · " + r.Period
	default:
		return "Backup Report · " + r.Period
	}
}

// reportColor returns a neutral blue accent for report payloads.
func reportColor() int { return 0x2563EB }

// lastSuccessfulLine formats a time as "02 Jan 2006, 15:04" local.
func lastSuccessfulLine(at time.Time) string {
	return at.Local().Format("02 Jan 2006, 15:04")
}

// durationHuman renders seconds as "45 s", "4 min", or "1 h 2 min".
func durationHuman(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d min", seconds/60)
	}
	hours, minutes := seconds/3600, seconds%3600/60
	if minutes == 0 {
		return fmt.Sprintf("%d h", hours)
	}
	return fmt.Sprintf("%d h %d min", hours, minutes)
}

// serverLine renders the machine identity for humans, omitting missing
// halves: "mynas (192.168.1.10)", "192.168.1.10", or "mynas".
func serverLine(hostname, ip string) string {
	switch {
	case hostname != "" && ip != "":
		return hostname + " (" + ip + ")"
	case ip != "":
		return ip
	default:
		return hostname
	}
}

// itemsLine renders the item count in the human word for a Package.
func itemsLine(count int) string {
	if count == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", count)
}

// itemsSizeLine renders stored items and formatted size, e.g. "3 items (2.1 GiB)".
// Returns "" when count is 0.
func itemsSizeLine(count int, size int64) string {
	if count <= 0 {
		return ""
	}
	return itemsLine(count) + " (" + formatBytes(size) + ")"
}

// description returns the human explanation paragraph for the run.
func description(payload Payload) string {
	if payload.Status == string(backup.StatusNoChange) {
		var anchorPart string
		if payload.LastSuccessfulAt != "" {
			if t, err := time.Parse(time.RFC3339, payload.LastSuccessfulAt); err == nil {
				anchorPart = " (unchanged since " + t.Local().Format("02 Jan 15:04") + ")"
			}
		}
		desc := "The new backup is identical to the last one" + anchorPart + "."
		if payload.HasDatabaseSources {
			desc += " Likely an idle app, or the database dump silently failed."
		}
		return desc
	}

	var base string
	if payload.Status == string(backup.StatusCancelled) {
		base = "The backup was stopped before it finished."
	} else {
		switch payload.ErrorCategory {
		case string(apperror.CategoryConfig):
			base = "A setting needs attention. The backup configuration was rejected, so the backup did not run."
		case string(apperror.CategoryPreflight):
			base = "The backup did not start. A check before the backup failed."
		case string(apperror.CategoryExecution):
			startedStr := payload.StartedAt
			if t, err := time.Parse(time.RFC3339, payload.StartedAt); err == nil {
				startedStr = t.Local().Format("02 Jan 15:04")
			}
			base = "Started " + startedStr + " but never finished. It likely timed out or the process crashed."
		case string(apperror.CategoryStorage):
			base = "The backup ran but could not be saved to its destination."
		case string(apperror.CategoryPersistence):
			base = "The backup finished but its result could not be recorded in the history database."
		case string(apperror.CategoryInternal):
			base = "An unexpected problem stopped the backup."
		default:
			base = "An unexpected problem stopped the backup."
		}
	}

	if payload.PackageCount > 0 {
		items := itemsSizeLine(payload.PackageCount, payload.SizeBytes)
		if payload.PackageCount == 1 {
			base += " " + items + " was prepared."
		} else {
			base += " " + items + " were prepared."
		}
	}
	return base
}

// formatDestinationTargets formats the list of storage targets (bucket for s3/r2, path for local, or name).
func formatDestinationTargets(destinations []DestinationInfo) string {
	if len(destinations) == 0 {
		return ""
	}
	parts := make([]string, len(destinations))
	for i, d := range destinations {
		if d.Bucket != "" {
			parts[i] = d.Bucket
		} else if d.Path != "" {
			parts[i] = d.Path
		} else {
			parts[i] = d.Name
		}
	}
	return strings.Join(parts, ", ")
}

// tryThis returns the actionable numbered fix suggestions for a run.
func tryThis(payload Payload) string {
	bucket := formatDestinationTargets(payload.Destinations)
	site := payload.Site

	var template string
	switch {
	case payload.Status == string(backup.StatusNoChange) || payload.ErrorCategory == "no_change":
		if payload.HasDatabaseSources {
			template = "1. Check the storage bucket {bucket}. If the database size is less than 1 KB or looks unusual, the backup likely did not finish correctly.\n2. Run `bqckup backup run {site} --force` to make sure the backup process works."
		} else {
			template = "1. Check the storage bucket {bucket}. If the backup size looks unusual, the backup likely did not finish correctly.\n2. Run `bqckup backup run {site} --force` to make sure the backup process works."
		}
	case payload.ErrorCategory == string(apperror.CategoryConfig):
		template = "1. Check the site's settings in bqckup.yaml. If the configuration was rejected, the backup did not run.\n2. Run `bqckup config validate` to see the problem."
	case payload.ErrorCategory == string(apperror.CategoryPreflight):
		template = "1. Check the database host and credentials. If a check before the backup failed, the backup did not start.\n2. Run `bqckup backup run {site} --force` to try again."
	case payload.ErrorCategory == string(apperror.CategoryExecution):
		template = "1. Check the site's data and logs. If the backup started but never finished, confirm that no backup process for the site is still running.\n2. Once no backup is active, run `bqckup backup run {site} --force` and watch the output."
	case payload.ErrorCategory == string(apperror.CategoryStorage):
		template = "1. Check the storage credentials and endpoint. If the backup ran but could not be saved, the storage is the likely cause.\n2. Run `bqckup doctor` to check the storage."
	case payload.ErrorCategory == string(apperror.CategoryPersistence):
		template = "1. Check disk space and permissions for the state database.\n2. Run `bqckup backup run {site} --force` and watch for the same error."
	case payload.ErrorCategory == string(apperror.CategoryInternal):
		template = "1. Note the error message above.\n2. Report the problem at github.com/bqckup/bqckup-go/issues."
	default:
		template = "1. Note the error message above.\n2. Report the problem at github.com/bqckup/bqckup-go/issues."
	}

	result := strings.ReplaceAll(template, "{site}", site)
	result = strings.ReplaceAll(result, "{bucket}", bucket)
	return result
}

// monitoringFooter renders the standard footer text for human channels.
func monitoringFooter(now time.Time) string {
	return "Bqckup Backup Monitoring · " + now.Local().Format("15:04 MST · 02 Jan 2006")
}

// categoryPhrase softens an error category into a phrase a non-IT reader
// can act on. Unknown or empty categories fall back to a non-empty phrase.
func categoryPhrase(category string) string {
	switch category {
	case "no_change":
		return "No changes detected"
	case string(apperror.CategoryConfig):
		return "A setting needs attention"
	case string(apperror.CategoryPreflight):
		return "The backup did not start"
	case string(apperror.CategoryExecution):
		return "Something went wrong"
	case string(apperror.CategoryStorage):
		return "The backup could not be saved"
	case string(apperror.CategoryPersistence):
		return "The backup history could not be saved"
	case string(apperror.CategoryInternal):
		return "Unexpected problem"
	default:
		return "Something went wrong"
	}
}

// failureBlock returns the failure block's label and text: the category
// phrase as label and the sanitized message as body. Success and cancelled
// runs get "" for both.
func failureBlock(payload Payload) (label, message string) {
	if payload.Status != string(backup.StatusFailed) && payload.Status != string(backup.StatusNoChange) {
		return "", ""
	}
	return categoryPhrase(payload.ErrorCategory), payload.ErrorMessage
}
