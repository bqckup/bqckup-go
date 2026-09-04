package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
)

// discordPayload is the Discord webhook JSON body: one embed with a static
// sender name. Field values come from the shared payload and are JSON-escaped
// on marshal, so error messages can never break the structure.
type discordPayload struct {
	Username string         `json:"username"`
	Content  string         `json:"content,omitempty"`
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
    if r == nil {
        return fmt.Errorf("report data missing")
    }

    storageName := reportStorageLine(r.Overall.Destinations)
    if storageName == "" {
        storageName = "No storage activity"
    }

    firstSite := "{domain_name}"
    if len(r.Sites) > 0 {
        firstSite = r.Sites[0].SiteName
    }

    var description string
    switch {
    case r.Overall.TotalRuns == 0:
        description = "We have not detected any backup runs for this period.\n\n**Recommended steps:**\n1. Verify that your application schedule or cron job is actively running.\n2. Ensure the Bqckup history database is accessible."
	case r.Overall.Failed > 0:
        description = fmt.Sprintf(
            "Backup activity was recorded, but we detected **%d failed run(s)** during this period.\n\n**Recommended steps:**\n1. Check your storage configuration destination (`%s`).\n2. Inspect the failed backup logs and attempt to force a backup by running `bqckup backup run %s --force` to ensure the backup process is functioning correctly.",
            r.Overall.Failed,
            storageName,
            firstSite,
        )
    default:
        description = "All scheduled backup activities were recorded successfully for this period. No further action is required."
    }

    siteBlock := "```text\n"
    if len(r.Sites) == 0 {
        siteBlock += "No sites available in storage.\n"
    } else {
        siteBlock += fmt.Sprintf("%-21s | %-11s | %-6s | %-5s\n", "Site", "Last Status", "Status", "False")
        siteBlock += strings.Repeat("-", 52) + "\n"

        for _, site := range r.Sites {
            status := site.LastStatus
            if status == "" {
                status = "N/A"
            }

            siteBlock += fmt.Sprintf("%-21s | %-11s | %-6d | %-5d\n",
                site.SiteName,
                status,
                site.Successful,
                site.Failed,
            )
        }
    }
    siteBlock += "```"

    fields := []discordField{
        {Name: "Server IP", Value: payload.ServerIP, Inline: true},
        {Name: "Total Site on config", Value: strconv.Itoa(len(r.Sites)), Inline: true},
        {Name: "List site in storage", Value: siteBlock, Inline: false},
    }

    body := discordPayload{
        Username: "Bqckup",
        Embeds: []discordEmbed{{
            Title:       reportHeadline(r),
            Description: description,
            Color:       reportColor(),
            Fields:      fields,
            Footer:      &discordFooter{Text: "If this was a mistake, please create issue here: https://github.com/bqckup/bqckup-go"},
        }},
    }

    raw, err := json.Marshal(body)
    if err != nil {
        return fmt.Errorf("encode report embed: %w", err)
    }
    return postJSON(ctx, d.client, d.webhookURL, raw)
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