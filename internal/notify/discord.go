package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
)

// discordPayload is the Discord webhook JSON body: one embed with a static
// sender name. Field values come from the shared payload and are JSON-escaped
// on marshal, so error messages can never break the structure.
type discordPayload struct {
	Username string         `json:"username"`
	Embeds   []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
	Footer      *discordFooter `json:"footer,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordFooter struct {
	Text string `json:"text"`
}

// Discord posts one embed to its configured Discord webhook URL.
type Discord struct {
	name       string
	webhookURL string
	client     *http.Client
}

func NewDiscord(name, webhookURL string) *Discord {
	return &Discord{
		name:       name,
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *Discord) Name() string { return d.name }

func (d *Discord) Send(ctx context.Context, payload Payload) error {
	if payload.ReportData != nil {
		return d.sendReport(ctx, payload)
	}

	lastSuccess := "No successful backup yet"
	if payload.LastSuccessfulAt != "" {
		if t, err := time.Parse(time.RFC3339, payload.LastSuccessfulAt); err == nil {
			lastSuccess = lastSuccessfulLine(t)
		} else {
			lastSuccess = payload.LastSuccessfulAt
		}
	}

	fields := []discordField{
		{Name: "Server", Value: serverLine(payload.Hostname, payload.ServerIP), Inline: true},
		{Name: "Last Successful Backup", Value: lastSuccess, Inline: true},
		{Name: "Duration", Value: durationHuman(payload.DurationSeconds), Inline: true},
	}

	if payload.Status == string(backup.StatusFailed) || payload.Status == string(backup.StatusNoChange) {
		label, message := failureBlock(payload)
		fields = append(fields,
			discordField{Name: "Consecutive Failures", Value: fmt.Sprintf("%d", payload.FailureStreak), Inline: true},
			discordField{Name: "Problem faced", Value: label, Inline: true},
			discordField{Name: "\u200b", Value: "\u200b", Inline: true},
		)
		if message != "" {
			fields = append(fields, discordField{Name: "What went wrong", Value: message})
		}
		fields = append(fields,
			discordField{Name: "Try this", Value: tryThis(payload)},
		)
	}

	body := discordPayload{
		Username: "Bqckup",
		Embeds: []discordEmbed{{
			Title:       headline(payload),
			Description: description(payload),
			Color:       statusColor(payload.Status),
			Fields:      fields,
			Footer:      &discordFooter{Text: monitoringFooter(time.Now())},
		}},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode embed: %w", err)
	}
	return postJSON(ctx, d.client, d.webhookURL, raw)
}

// sendReport posts a Discord embed for a daily or monthly report payload.
func (d *Discord) sendReport(ctx context.Context, payload Payload) error {
	r := payload.ReportData
	fields := []discordField{
		{Name: "Server IP", Value: serverLine(payload.Hostname, payload.ServerIP), Inline: true},
		{Name: "Storage", Value: reportStorageLine(r.Overall.Destinations), Inline: true},
		{Name: "Total Runs", Value: fmt.Sprintf("%d (OK: %d | Failed: %d)", r.Overall.TotalRuns, r.Overall.Successful, r.Overall.Failed), Inline: true},
		{Name: "Total Size", Value: formatBytes(r.Overall.TotalBytes), Inline: true},
	}
	if len(r.Sites) == 0 {
		fields = append(fields, discordField{Name: "Sites", Value: "No backup runs recorded for this period."})
	}
	for _, site := range r.Sites {
		value := fmt.Sprintf("Runs: %d | OK: %d | Failed: %d | Last: %s",
			site.TotalRuns, site.Successful, site.Failed, site.LastStatus)
		fields = append(fields, discordField{Name: site.SiteName, Value: value})
	}
	body := discordPayload{
		Username: "Bqckup",
		Embeds: []discordEmbed{{
			Title:       reportHeadline(r),
			Description: reportDescription(r),
			Color:       reportColor(),
			Fields:      fields,
			Footer:      &discordFooter{Text: monitoringFooter(time.Now())},
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode report embed: %w", err)
	}
	return postJSON(ctx, d.client, d.webhookURL, raw)
}

func reportDescription(r *ReportData) string {
	if r.Overall.TotalRuns == 0 {
		return "We have not detected any backup runs for this period.\n\nRecommended: verify that the backup schedule is running and that the history database is accessible."
	}
	if r.Overall.Failed > 0 {
		return fmt.Sprintf("Backup activity was recorded, but %d run(s) failed.\n\nRecommended: inspect the failed backup logs and run the affected site manually.", r.Overall.Failed)
	}
	return "Backup activity was recorded successfully for this period."
}

func reportStorageLine(destinations []ReportDestinationSummary) string {
	if len(destinations) == 0 {
		return "No storage activity"
	}
	names := make([]string, 0, len(destinations))
	for _, destination := range destinations {
		names = append(names, destination.Name)
	}
	return strings.Join(names, ", ")
}
