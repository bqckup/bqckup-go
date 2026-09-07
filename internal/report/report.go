// Package report builds and delivers scheduled backup summary reports.
// A Builder aggregates history runs into DailyReportData or MonthlyReportData;
// a Dispatcher sends them through the configured notification channels.
package report

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bqckup/bqckup-go/internal/history"
)

// DestinationSummary holds the observed package state for one destination.
type DestinationSummary struct {
	Name          string
	TotalPackages int
	Stored        int
	Failed        int
	TotalBytes    int64
}

// PeriodSummary holds aggregate backup statistics for a day or month.
type PeriodSummary struct {
	TotalRuns              int
	Successful             int
	Failed                 int
	Cancelled              int
	Skipped                int
	NoChange               int
	DurationSeconds        int64
	AverageDurationSeconds int64
	TotalBytes             int64
	Destinations           []DestinationSummary
}

// SiteSummary holds the aggregated backup statistics for one site within a
// report period.
type SiteSummary struct {
	SiteName               string
	TotalRuns              int
	Successful             int
	Failed                 int
	Cancelled              int
	Skipped                int
	NoChange               int
	DurationSeconds        int64
	AverageDurationSeconds int64
	TotalBytes             int64
	Destinations           []DestinationSummary
	LastStatus             string
	LastRunAt              *time.Time
}

// DailyReportData is the payload for a daily backup summary report.
type DailyReportData struct {
	// Date is the calendar day this report covers, in the report timezone.
	Date     time.Time
	Hostname string
	ServerIP string
	HasRuns  bool
	Overall  PeriodSummary
	Sites    []SiteSummary
}

// MonthlyReportData is the payload for a monthly consolidated backup report.
type MonthlyReportData struct {
	// Month is the first day of the calendar month this report covers.
	Month    time.Time
	Hostname string
	ServerIP string
	// Days holds one DailyReportData per calendar day that had at least one
	// run (or all days when IncludeEmptyDays is true).
	Days []DailyReportData
	// Overall is the month-level rollup across all days and sites.
	Overall PeriodSummary
	// Sites is the month-level rollup across all days.
	Sites []SiteSummary
}

// historyRepository is the subset of history.Repository used by Builder.
type historyRepository interface {
	ListRunsInRange(ctx context.Context, from, to time.Time) ([]history.BackupRun, error)
}

// Builder aggregates history runs into report data structures.
type Builder struct {
	repo historyRepository
}

// NewBuilder creates a Builder backed by the given repository.
func NewBuilder(repo historyRepository) *Builder {
	return &Builder{repo: repo}
}

// BuildDailyReport returns a DailyReportData for the calendar day containing
// t in the given timezone. IncludeEmptyDays controls whether sites with zero
// runs are included.
func (b *Builder) BuildDailyReport(ctx context.Context, t time.Time, tz *time.Location, includeEmpty bool) (DailyReportData, error) {
	local := t.In(tz)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, tz)
	dayEnd := dayStart.Add(24 * time.Hour)

	runs, err := b.repo.ListRunsInRange(ctx, dayStart, dayEnd)
	if err != nil {
		return DailyReportData{}, fmt.Errorf("build daily report: %w", err)
	}

	return DailyReportData{
		Date:    dayStart,
		HasRuns: len(runs) > 0,
		Overall: aggregatePeriod(runs),
		Sites:   aggregateSites(runs, includeEmpty),
	}, nil
}

// BuildMonthlyReport returns a MonthlyReportData for the calendar month
// containing t in the given timezone. It builds one DailyReportData per day
// in the month and rolls them up into a month-level Sites summary.
func (b *Builder) BuildMonthlyReport(ctx context.Context, t time.Time, tz *time.Location, includeEmpty bool) (MonthlyReportData, error) {
	local := t.In(tz)
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, tz)
	monthEnd := monthStart.AddDate(0, 1, 0)

	runs, err := b.repo.ListRunsInRange(ctx, monthStart, monthEnd)
	if err != nil {
		return MonthlyReportData{}, fmt.Errorf("build monthly report: %w", err)
	}

	// Group runs by calendar day.
	byDay := make(map[string][]history.BackupRun)
	daysOrder := make([]string, 0)
	for _, run := range runs {
		key := run.StartedAt.In(tz).Format("2006-01-02")
		if _, exists := byDay[key]; !exists {
			daysOrder = append(daysOrder, key)
		}
		byDay[key] = append(byDay[key], run)
	}

	// Build per-day reports. When includeEmpty, emit every day in the month.
	var days []DailyReportData
	if includeEmpty {
		for d := monthStart; d.Before(monthEnd); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			days = append(days, DailyReportData{
				Date:    d,
				HasRuns: len(byDay[key]) > 0,
				Overall: aggregatePeriod(byDay[key]),
				Sites:   aggregateSites(byDay[key], true),
			})
		}
	} else {
		for _, key := range daysOrder {
			d, _ := time.ParseInLocation("2006-01-02", key, tz)
			days = append(days, DailyReportData{
				Date:    d,
				HasRuns: len(byDay[key]) > 0,
				Overall: aggregatePeriod(byDay[key]),
				Sites:   aggregateSites(byDay[key], false),
			})
		}
	}

	return MonthlyReportData{
		Month:   monthStart,
		Days:    days,
		Overall: aggregatePeriod(runs),
		Sites:   aggregateSites(runs, includeEmpty),
	}, nil
}

// aggregateSites groups runs by site name and returns one SiteSummary per
// site. When includeEmpty is false, sites with zero runs are omitted (only
// relevant when the caller passes an empty slice for a day).
func aggregateSites(runs []history.BackupRun, includeEmpty bool) []SiteSummary {
	order := make([]string, 0)
	index := make(map[string]*SiteSummary)

	for i := range runs {
		run := &runs[i]
		s, exists := index[run.SiteName]
		if !exists {
			order = append(order, run.SiteName)
			index[run.SiteName] = &SiteSummary{SiteName: run.SiteName}
			s = index[run.SiteName]
		}
		s.TotalRuns++
		addStatus(&s.Successful, &s.Failed, &s.Cancelled, &s.Skipped, &s.NoChange, run.Status)
		s.DurationSeconds += run.DurationMillis / 1000
		if s.TotalRuns > 0 {
			s.AverageDurationSeconds = s.DurationSeconds / int64(s.TotalRuns)
		}
		s.TotalBytes += logicalPackageBytes(*run)
		s.Destinations = mergeDestinationSummaries(s.Destinations, destinationSummaries([]history.BackupRun{*run}))
		if s.LastRunAt == nil || run.StartedAt.After(*s.LastRunAt) {
			t := run.StartedAt
			s.LastRunAt = &t
			s.LastStatus = string(run.Status)
		}
	}

	summaries := make([]SiteSummary, 0, len(order))
	for _, name := range order {
		summaries = append(summaries, *index[name])
	}
	if includeEmpty && len(summaries) == 0 {
		return []SiteSummary{}
	}
	return summaries
}

func aggregatePeriod(runs []history.BackupRun) PeriodSummary {
	var summary PeriodSummary
	for i := range runs {
		run := &runs[i]
		summary.TotalRuns++
		addStatus(&summary.Successful, &summary.Failed, &summary.Cancelled, &summary.Skipped, &summary.NoChange, run.Status)
		summary.DurationSeconds += run.DurationMillis / 1000
		summary.TotalBytes += logicalPackageBytes(*run)
	}
	if summary.TotalRuns > 0 {
		summary.AverageDurationSeconds = summary.DurationSeconds / int64(summary.TotalRuns)
	}
	summary.Destinations = destinationSummaries(runs)
	return summary
}

func addStatus(successful, failed, cancelled, skipped, noChange *int, status history.RunStatus) {
	switch status {
	case history.StatusSuccess:
		(*successful)++
	case history.StatusFailed:
		(*failed)++
	case history.StatusCancelled:
		(*cancelled)++
	case history.StatusSkipped:
		(*skipped)++
	case history.StatusNoChange:
		(*noChange)++
	}
}

func logicalPackageBytes(run history.BackupRun) int64 {
	seen := make(map[[3]string]struct{}, len(run.Packages))
	var total int64
	for _, pkg := range run.Packages {
		if pkg.Status != history.PackageStored {
			continue
		}
		key := [3]string{pkg.RunID, pkg.SourceKind, pkg.SourceName}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		total += pkg.Size
	}
	return total
}

func destinationSummaries(runs []history.BackupRun) []DestinationSummary {
	index := make(map[string]*DestinationSummary)
	for _, run := range runs {
		for _, pkg := range run.Packages {
			if _, ok := index[pkg.Destination]; !ok {
				index[pkg.Destination] = &DestinationSummary{Name: pkg.Destination}
			}
			destination := index[pkg.Destination]
			destination.TotalPackages++
			switch pkg.Status {
			case history.PackageStored:
				destination.Stored++
				destination.TotalBytes += pkg.Size
			case history.PackageFailed:
				destination.Failed++
			}
		}
	}
	names := make([]string, 0, len(index))
	for name := range index {
		names = append(names, name)
	}
	sort.Strings(names)
	summaries := make([]DestinationSummary, 0, len(names))
	for _, name := range names {
		summaries = append(summaries, *index[name])
	}
	return summaries
}

func mergeDestinationSummaries(left, right []DestinationSummary) []DestinationSummary {
	index := make(map[string]*DestinationSummary, len(left)+len(right))
	for _, summary := range left {
		value := summary
		index[value.Name] = &value
	}
	for _, summary := range right {
		current, ok := index[summary.Name]
		if !ok {
			value := summary
			index[value.Name] = &value
			continue
		}
		current.TotalPackages += summary.TotalPackages
		current.Stored += summary.Stored
		current.Failed += summary.Failed
		current.TotalBytes += summary.TotalBytes
	}
	names := make([]string, 0, len(index))
	for name := range index {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]DestinationSummary, 0, len(names))
	for _, name := range names {
		out = append(out, *index[name])
	}
	return out
}
