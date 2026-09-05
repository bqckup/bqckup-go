package report

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/bqckup/bqckup-go/internal/notify"
)

// reportRepository is the subset of history.Repository used by Dispatcher.
type reportRepository interface {
	ReportDelivered(ctx context.Context, reportType, period string) (bool, error)
	RecordDelivery(ctx context.Context, reportType, period string, deliveredAt time.Time) error
}

// Dispatcher sends a built report through the channels of a named route,
// recording delivery so the same period is never sent twice.
type Dispatcher struct {
	channels map[string]notify.Channel
	routes   []config.Route
	repo     reportRepository
	hostname string
	serverIP string
}

// NewDispatcher creates a Dispatcher. channels and routes come from the same
// notification configuration used by the backup notifier.
func NewDispatcher(channels map[string]notify.Channel, routes []config.Route, repo reportRepository) *Dispatcher {
	hostname, serverIP := notify.ServerIdentity()
	return &Dispatcher{
		channels: channels,
		routes:   routes,
		repo:     repo,
		hostname: hostname,
		serverIP: serverIP,
	}
}

// SendDaily sends a daily report if it has not already been delivered for the
// given date. routeName must match a route's name field in the configuration.
func (d *Dispatcher) SendDaily(ctx context.Context, data DailyReportData, routeName string) error {
	period := data.Date.Format("2006-01-02")
	return d.send(ctx, "daily", period, routeName, dailyPayload(data, d.hostname, d.serverIP))
}

// SendMonthly sends a monthly report if it has not already been delivered for
// the given month. routeName must match a route's name field in the configuration.
func (d *Dispatcher) SendMonthly(ctx context.Context, data MonthlyReportData, routeName string) error {
	period := data.Month.Format("2006-01")
	return d.send(ctx, "monthly", period, routeName, monthlyPayload(data, d.hostname, d.serverIP))
}

func (d *Dispatcher) send(ctx context.Context, reportType, period, routeName string, payload notify.Payload) error {
	delivered, err := d.repo.ReportDelivered(ctx, reportType, period)
	if err != nil {
		return fmt.Errorf("check delivery: %w", err)
	}
	if delivered {
		return nil
	}

	channels := d.channelsFor(routeName)
	if len(channels) == 0 {
		return fmt.Errorf("no channels found for route %q", routeName)
	}

	var errs []error
	for _, ch := range channels {
		if err := ch.Send(ctx, payload); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch.Name(), err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	return d.repo.RecordDelivery(ctx, reportType, period, time.Now())
}

// channelsFor returns the channels of the named route, deduplicated.
func (d *Dispatcher) channelsFor(routeName string) []notify.Channel {
	seen := make(map[string]struct{})
	var matched []notify.Channel
	for _, route := range d.routes {
		if route.Name != routeName {
			continue
		}
		for _, name := range route.Channels {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			if ch, ok := d.channels[name]; ok {
				matched = append(matched, ch)
			}
		}
	}
	return matched
}

// dailyPayload converts DailyReportData into a notify.Payload suitable for
// delivery through existing channels. The event field uses config.EventDailyReport.
func dailyPayload(data DailyReportData, hostname, serverIP string) notify.Payload {
	return notify.Payload{
		Event:    notify.Event(config.EventDailyReport),
		Hostname: hostname,
		ServerIP: serverIP,
		Status:   "daily_report",
		Site:     data.Date.Format("2006-01-02"),
		ReportData: &notify.ReportData{
			ReportType: "daily",
			Period:     data.Date.Format("2006-01-02"),
			Overall:    periodSummaryToPayload(data.Overall),
			Sites:      siteSummariesToPayload(data.Sites),
		},
	}
}

// monthlyPayload converts MonthlyReportData into a notify.Payload.
func monthlyPayload(data MonthlyReportData, hostname, serverIP string) notify.Payload {
	return notify.Payload{
		Event:    notify.Event(config.EventMonthlyReport),
		Hostname: hostname,
		ServerIP: serverIP,
		Status:   "monthly_report",
		Site:     data.Month.Format("2006-01"),
		ReportData: &notify.ReportData{
			ReportType: "monthly",
			Period:     data.Month.Format("2006-01"),
			Overall:    periodSummaryToPayload(data.Overall),
			Days:       daysToPayload(data.Days),
			Sites:      siteSummariesToPayload(data.Sites),
		},
	}
}

func siteSummariesToPayload(sites []SiteSummary) []notify.SiteReportSummary {
	out := make([]notify.SiteReportSummary, len(sites))
	for i, s := range sites {
		out[i] = notify.SiteReportSummary{
			SiteName:               s.SiteName,
			TotalRuns:              s.TotalRuns,
			Successful:             s.Successful,
			Failed:                 s.Failed,
			Cancelled:              s.Cancelled,
			Skipped:                s.Skipped,
			NoChange:               s.NoChange,
			DurationSeconds:        s.DurationSeconds,
			AverageDurationSeconds: s.AverageDurationSeconds,
			TotalBytes:             s.TotalBytes,
			Destinations:           destinationSummariesToPayload(s.Destinations),
			LastStatus:             s.LastStatus,
			LastRunAt:              lastRunAtStr(s.LastRunAt),
		}
	}
	return out
}

func daysToPayload(days []DailyReportData) []notify.ReportDaySummary {
	out := make([]notify.ReportDaySummary, len(days))
	for i, day := range days {
		out[i] = notify.ReportDaySummary{
			Date:    day.Date.Format("2006-01-02"),
			HasRuns: day.HasRuns,
			Overall: periodSummaryToPayload(day.Overall),
			Sites:   siteSummariesToPayload(day.Sites),
		}
	}
	return out
}

func periodSummaryToPayload(summary PeriodSummary) notify.ReportPeriodSummary {
	return notify.ReportPeriodSummary{
		TotalRuns:              summary.TotalRuns,
		Successful:             summary.Successful,
		Failed:                 summary.Failed,
		Cancelled:              summary.Cancelled,
		Skipped:                summary.Skipped,
		NoChange:               summary.NoChange,
		DurationSeconds:        summary.DurationSeconds,
		AverageDurationSeconds: summary.AverageDurationSeconds,
		TotalBytes:             summary.TotalBytes,
		Destinations:           destinationSummariesToPayload(summary.Destinations),
	}
}

func destinationSummariesToPayload(summaries []DestinationSummary) []notify.ReportDestinationSummary {
	out := make([]notify.ReportDestinationSummary, len(summaries))
	for i, summary := range summaries {
		out[i] = notify.ReportDestinationSummary{
			Name:          summary.Name,
			TotalPackages: summary.TotalPackages,
			Stored:        summary.Stored,
			Failed:        summary.Failed,
			TotalBytes:    summary.TotalBytes,
		}
	}
	return out
}

func lastRunAtStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Ensure history.Repository satisfies reportRepository at compile time.
var _ reportRepository = (*history.Repository)(nil)
