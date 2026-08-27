package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// discordPayload is the Discord webhook JSON body: one embed with a static
// sender name. Field values come from the shared payload and are JSON-escaped
// on marshal, so error messages can never break the structure.
type discordPayload struct {
	Username string         `json:"username"`
	Embeds   []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title  string         `json:"title"`
	Color  int            `json:"color"`
	Fields []discordField `json:"fields"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
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
	body := discordPayload{
		Username: "Bqckup",
		Embeds: []discordEmbed{{
			Title: fmt.Sprintf("[bqckup] %s: %s", payload.Event, payload.Site),
			Color: statusColor(payload.Status),
			Fields: []discordField{
				{Name: "site", Value: payload.Site, Inline: true},
				{Name: "status", Value: payload.Status, Inline: true},
				{Name: "duration", Value: fmt.Sprintf("%ds", payload.DurationSeconds), Inline: true},
				{Name: "artifacts", Value: fmt.Sprintf("%d", payload.ArtifactCount), Inline: true},
				{Name: "size", Value: formatBytes(payload.SizeBytes), Inline: true},
			},
		}},
	}
	if payload.ErrorCategory != "" {
		body.Embeds[0].Fields = append(body.Embeds[0].Fields,
			discordField{Name: "error_category", Value: payload.ErrorCategory, Inline: true},
			discordField{Name: "error_message", Value: payload.ErrorMessage},
		)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode embed: %w", err)
	}
	return postJSON(ctx, d.client, url, raw)
}
