package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// Discord posts one embed to a Discord webhook URL taken from an environment
// variable at send time.
type Discord struct {
	name          string
	webhookURLEnv string
	lookupEnv     func(string) (string, bool)
	client        *http.Client
}

func NewDiscord(name, webhookURLEnv string, lookupEnv func(string) (string, bool)) *Discord {
	return &Discord{
		name:          name,
		webhookURLEnv: webhookURLEnv,
		lookupEnv:     lookupEnv,
		client:        &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *Discord) Name() string { return d.name }

func (d *Discord) Send(ctx context.Context, payload Payload) error {
	url, ok := d.lookupEnv(d.webhookURLEnv)
	if !ok || url == "" {
		return fmt.Errorf("environment variable %q is not set", d.webhookURLEnv)
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
	return postJSON(ctx, d.client, url, raw)
}
