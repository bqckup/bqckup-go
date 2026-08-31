package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscordEmbedFailed(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", server.URL)
	lastSuccess := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	input := backup.NotifyInput{
		Event:            config.EventBackupFailed,
		SiteName:         "example.org",
		Status:           backup.StatusFailed,
		StartedAt:        time.Date(2026, 8, 23, 1, 46, 56, 0, time.UTC),
		FinishedAt:       time.Date(2026, 8, 23, 1, 48, 38, 0, time.UTC),
		LastSuccessfulAt: lastSuccess,
		FailureStreak:    3,
		ErrorCategory:    "execution",
		ErrorMessage:     "could not create the file archive",
	}
	payload := NewPayload(input)
	payload.Hostname = "web-01"
	payload.ServerIP = "203.0.113.7"
	require.NoError(t, discord.Send(context.Background(), payload))

	body := <-received
	assert.Equal(t, "Bqckup", body.Username)
	require.Len(t, body.Embeds, 1)
	embed := body.Embeds[0]
	assert.Equal(t, "Backup failed for example.org", embed.Title)
	assert.Equal(t, description(payload), embed.Description)
	assert.Equal(t, 0xE74C3C, embed.Color)
	require.NotNil(t, embed.Footer)
	assert.True(t, strings.HasPrefix(embed.Footer.Text, "Bqckup Backup Monitoring · "))

	require.Len(t, embed.Fields, 8)

	// Row 1 (grid inline)
	assert.Equal(t, "Server", embed.Fields[0].Name)
	assert.Equal(t, "web-01 (203.0.113.7)", embed.Fields[0].Value)
	assert.True(t, embed.Fields[0].Inline)

	assert.Equal(t, "Last Successful Backup", embed.Fields[1].Name)
	assert.Equal(t, lastSuccess.Local().Format("02 Jan 2006, 15:04"), embed.Fields[1].Value)
	assert.True(t, embed.Fields[1].Inline)

	assert.Equal(t, "Duration", embed.Fields[2].Name)
	assert.Equal(t, "1 min", embed.Fields[2].Value)
	assert.True(t, embed.Fields[2].Inline)

	// Row 2 (grid inline: 2 + filler)
	assert.Equal(t, "Consecutive Failures", embed.Fields[3].Name)
	assert.Equal(t, "3", embed.Fields[3].Value)
	assert.True(t, embed.Fields[3].Inline)

	assert.Equal(t, "Problem faced", embed.Fields[4].Name)
	assert.Equal(t, "Something went wrong", embed.Fields[4].Value)
	assert.True(t, embed.Fields[4].Inline)

	assert.Equal(t, "\u200b", embed.Fields[5].Name)
	assert.Equal(t, "\u200b", embed.Fields[5].Value)
	assert.True(t, embed.Fields[5].Inline)

	// Full-width fields
	assert.Equal(t, "What went wrong", embed.Fields[6].Name)
	assert.Equal(t, "could not create the file archive", embed.Fields[6].Value)
	assert.False(t, embed.Fields[6].Inline)

	assert.Equal(t, "Try this", embed.Fields[7].Name)
	assert.Equal(t, "1. Check the site's data and logs. If the backup started but never finished, confirm that no backup process for the site is still running.\n2. Once no backup is active, run `bqckup backup run example.org --force` and watch the output.", embed.Fields[7].Value)
	assert.False(t, embed.Fields[7].Inline)
}

func TestDiscordNoChangeEmbed(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", server.URL)
	anchor := time.Date(2026, 8, 25, 6, 12, 0, 0, time.UTC)
	input := backup.NotifyInput{
		Event:              config.EventBackupNoChange,
		SiteName:           "example.org",
		Status:             backup.StatusNoChange,
		StartedAt:          time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC),
		FinishedAt:         time.Date(2026, 8, 26, 1, 1, 0, 0, time.UTC),
		LastSuccessfulAt:   anchor,
		FailureStreak:      0,
		ErrorCategory:      "no_change",
		ErrorMessage:       "2 items are unchanged from the previous run.",
		HasDatabaseSources: true,
		Destinations:       []backup.NotifyDestination{{Name: "s3-primary", Bucket: "my-backups"}},
	}
	payload := NewPayload(input)
	payload.Hostname = "web-01"
	payload.ServerIP = "203.0.113.7"
	require.NoError(t, discord.Send(context.Background(), payload))

	body := <-received
	require.Len(t, body.Embeds, 1)
	embed := body.Embeds[0]
	assert.Equal(t, "No changes detected for example.org", embed.Title)
	assert.Equal(t, 0xF1C40F, embed.Color)
	assert.Contains(t, embed.Description, "The new backup is identical to the last one")
	assert.Contains(t, embed.Description, "Likely an idle app")

	require.Len(t, embed.Fields, 8)
	assert.Equal(t, "Problem faced", embed.Fields[4].Name)
	assert.Equal(t, "No changes detected", embed.Fields[4].Value)
	assert.Equal(t, "What went wrong", embed.Fields[6].Name)
	assert.Equal(t, "2 items are unchanged from the previous run.", embed.Fields[6].Value)
	assert.Equal(t, "Try this", embed.Fields[7].Name)
	assert.Contains(t, embed.Fields[7].Value, "1. Check the storage bucket my-backups.")
}

func TestDiscordEmbedFailedWithoutErrorMessage(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", server.URL)
	input := backup.NotifyInput{
		Event:         config.EventBackupFailed,
		SiteName:      "example.org",
		Status:        backup.StatusFailed,
		StartedAt:     time.Date(2026, 8, 23, 1, 46, 56, 0, time.UTC),
		FinishedAt:    time.Date(2026, 8, 23, 1, 48, 38, 0, time.UTC),
		FailureStreak: 1,
		ErrorCategory: "storage",
	}
	payload := NewPayload(input)
	require.NoError(t, discord.Send(context.Background(), payload))

	body := <-received
	require.Len(t, body.Embeds, 1)
	embed := body.Embeds[0]

	// When LastSuccessfulAt is zero:
	assert.Equal(t, "Last Successful Backup", embed.Fields[1].Name)
	assert.Equal(t, "No successful backup yet", embed.Fields[1].Value)

	// Problem faced still shows phrase
	assert.Equal(t, "Problem faced", embed.Fields[4].Name)
	assert.Equal(t, "The backup could not be saved", embed.Fields[4].Value)

	// What went wrong must be omitted
	for _, field := range embed.Fields {
		assert.NotEqual(t, "What went wrong", field.Name)
	}
	require.Len(t, embed.Fields, 7)
}

func TestDiscordCancelledEmbedHasNoFailureBlock(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", server.URL)
	input := backup.NotifyInput{
		Event:         config.EventBackupCancelled,
		SiteName:      "example.org",
		Status:        backup.StatusCancelled,
		StartedAt:     time.Date(2026, 8, 23, 1, 46, 56, 0, time.UTC),
		FinishedAt:    time.Date(2026, 8, 23, 1, 48, 38, 0, time.UTC),
		ErrorCategory: "cancellation",
		ErrorMessage:  "backup was cancelled",
	}
	payload := NewPayload(input)
	require.NoError(t, discord.Send(context.Background(), payload))

	body := <-received
	require.Len(t, body.Embeds, 1)
	embed := body.Embeds[0]
	assert.Equal(t, "Backup cancelled for example.org", embed.Title)
	assert.Equal(t, "The backup was stopped before it finished.", embed.Description)
	assert.Equal(t, 0xF1C40F, embed.Color)
	require.NotNil(t, embed.Footer)
	assert.True(t, strings.HasPrefix(embed.Footer.Text, "Bqckup Backup Monitoring · "))

	// Cancelled has row 1 only (3 fields)
	require.Len(t, embed.Fields, 3)
	assert.Equal(t, "Server", embed.Fields[0].Name)
	assert.Equal(t, "Last Successful Backup", embed.Fields[1].Name)
	assert.Equal(t, "Duration", embed.Fields[2].Name)

	for _, field := range embed.Fields {
		assert.NotEqual(t, "Consecutive Failures", field.Name)
		assert.NotEqual(t, "Error Category", field.Name)
		assert.NotEqual(t, "What went wrong", field.Name)
		assert.NotEqual(t, "Try this", field.Name)
	}
}

func TestDiscordEmbedShowsHumanRowsOnly(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", server.URL)
	input := backup.NotifyInput{
		Event:         config.EventBackupFailed,
		SiteName:      "example.org",
		Status:        backup.StatusFailed,
		StartedAt:     time.Date(2026, 8, 23, 1, 46, 56, 0, time.UTC),
		FinishedAt:    time.Date(2026, 8, 23, 1, 48, 38, 0, time.UTC),
		FailureStreak: 1,
		ErrorCategory: "config",
		ErrorMessage:  "invalid yaml",
	}
	payload := NewPayload(input)
	payload.Hostname = "web-01"
	payload.ServerIP = "203.0.113.7"
	payload.RunID = "c699eaba-4928-48e8-a9db-6e3d6121d07f"
	require.NoError(t, discord.Send(context.Background(), payload))

	body := <-received
	names := make(map[string]bool)
	for _, field := range body.Embeds[0].Fields {
		names[field.Name] = true
	}
	for _, forbidden := range []string{"server_ip", "hostname", "run_id", "started_at", "status", "error_category"} {
		assert.False(t, names[forbidden], "field %q must not appear", forbidden)
	}
}

func TestDiscordReturnsErrorsLikeWebhook(t *testing.T) {
	discord := NewDiscord("discord", "")
	err := discord.Send(context.Background(), NewPayload(backup.NotifyInput{
		Event:    config.EventBackupFailed,
		SiteName: "example.org",
		Status:   backup.StatusFailed,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported protocol scheme")
}

func TestDiscordReportEmbedUsesOperationalSummary(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", server.URL)
	payload := Payload{
		Event:    Event(config.EventDailyReport),
		Hostname: "backup-host",
		ServerIP: "192.168.1.129",
		ReportData: &ReportData{
			ReportType: "daily",
			Period:     "2026-08-31",
			Overall: ReportPeriodSummary{
				TotalRuns:    2,
				Successful:   1,
				Failed:       1,
				TotalBytes:   1536,
				Destinations: []ReportDestinationSummary{{Name: "local-primary"}},
			},
			Sites: []SiteReportSummary{{SiteName: "example", TotalRuns: 2, Successful: 1, Failed: 1, LastStatus: "success"}},
		},
	}
	require.NoError(t, discord.Send(context.Background(), payload))

	embed := (<-received).Embeds[0]
	assert.Equal(t, "Daily Backup Report · 2026-08-31", embed.Title)
	assert.Contains(t, embed.Description, "1 run(s) failed")
	assert.Equal(t, "Server IP", embed.Fields[0].Name)
	assert.Equal(t, "local-primary", embed.Fields[1].Value)
	assert.Equal(t, "2 (OK: 1 | Failed: 1)", embed.Fields[2].Value)
	assert.Equal(t, "1.5 KiB", embed.Fields[3].Value)
	assert.Equal(t, "example", embed.Fields[4].Name)
}
