package notify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/require"
)

type notificationSample struct {
	name  string
	input backup.NotifyInput
}

func notificationSamples(now time.Time, siteName string) []notificationSample {
	return []notificationSample{
		{
			name: "failure",
			input: backup.NotifyInput{
				Event:            config.EventBackupFailed,
				SiteName:         siteName,
				Status:           backup.StatusFailed,
				StartedAt:        now.Add(-4*time.Minute - 30*time.Second),
				FinishedAt:       now,
				LastSuccessfulAt: now.Add(-24 * time.Hour),
				FailureStreak:    2,
				ErrorCategory:    "execution",
				ErrorMessage:     "could not export database: connection to 127.0.0.1:3306 failed",
				Packages: []history.Package{
					{SourceKind: "files", SourceName: "files", Size: 18038862643},
					{SourceKind: "database", SourceName: "app", Size: 2048},
				},
			},
		},
		{
			name: "cancelled",
			input: backup.NotifyInput{
				Event:            config.EventBackupCancelled,
				SiteName:         siteName,
				Status:           backup.StatusCancelled,
				StartedAt:        now.Add(-75 * time.Second),
				FinishedAt:       now,
				LastSuccessfulAt: now.Add(-24 * time.Hour),
				ErrorCategory:    "cancellation",
				ErrorMessage:     "backup was cancelled",
				Packages: []history.Package{
					{SourceKind: "files", SourceName: "files", Size: 18038862643},
				},
			},
		},
		{
			name: "no_change",
			input: backup.NotifyInput{
				Event:              config.EventBackupNoChange,
				SiteName:           siteName,
				Status:             backup.StatusNoChange,
				StartedAt:          now.Add(-time.Minute),
				FinishedAt:         now,
				LastSuccessfulAt:   now.Add(-24 * time.Hour),
				ErrorCategory:      "no_change",
				ErrorMessage:       "2 items are unchanged from the previous run.",
				HasDatabaseSources: true,
				Destinations: []backup.NotifyDestination{
					{Name: "s3-primary", Bucket: "my-backups"},
				},
				Packages: []history.Package{
					{SourceKind: "files", SourceName: "files", Size: 18038862643},
					{SourceKind: "database", SourceName: "app", Size: 2048},
				},
			},
		},
	}
}

// TestSampleOutput writes deterministic email previews and prints the Discord
// payloads without contacting a real notification endpoint.
//
//	go test ./internal/notify -run TestSampleOutput -v
func TestSampleOutput(t *testing.T) {
	now := time.Date(2026, 8, 26, 14, 9, 30, 0, time.UTC)
	for _, sample := range notificationSamples(now, "example.org") {
		t.Run(sample.name, func(t *testing.T) {
			payload := NewPayload(sample.input)
			payload.Hostname = "mynas"
			payload.ServerIP = "192.168.1.10"

			var embed discordPayload
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&embed))
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			require.NoError(t, NewDiscord("discord", server.URL).Send(t.Context(), payload))

			raw, err := json.MarshalIndent(embed, "", "  ")
			require.NoError(t, err)
			fmt.Printf("=== DISCORD (%s) ===\n%s\n", sample.name, raw)

			smtp := &SMTP{from: "bqckup@example.com", to: []string{"ops@example.com"}}
			previewPath := filepath.Join(os.TempDir(), "bqckup-notify-"+sample.name+"-preview.html")
			require.NoError(t, os.WriteFile(previewPath, []byte(smtp.renderHTML(headline(payload), payload, logoDataURI)), 0o600))
			fmt.Printf("preview tersimpan: %s\n", previewPath)
		})
	}
}

func sendLiveNotification(t *testing.T, input backup.NotifyInput) {
	t.Helper()
	ctx := t.Context()

	configDir := os.Getenv("BQCKUP_CONFIG_DIR")
	if configDir == "" {
		if _, err := os.Stat("/etc/bqckup/bqckup.yaml"); err == nil {
			configDir = "/etc/bqckup"
		}
	}

	if configDir != "" {
		cfg, err := config.Load(ctx, configDir)
		if err != nil {
			t.Fatalf("failed to load configuration from %s: %v", configDir, err)
		}
		if len(cfg.Notifications.Channels) == 0 {
			t.Fatalf("no notification channels found in %s/bqckup.yaml", configDir)
		}
		if len(cfg.Notifications.Routes) == 0 {
			t.Fatalf("no notification routes found in %s/bqckup.yaml", configDir)
		}

		channels := make(map[string]Channel, len(cfg.Notifications.Channels))
		for name, channel := range cfg.Notifications.Channels {
			switch channel.Type {
			case "smtp":
				channels[name] = NewSMTP(name, channel, nil)
			case "webhook":
				channels[name] = NewWebhook(name, channel.URL)
			case "discord":
				channels[name] = NewDiscord(name, channel.WebhookURL)
			}
		}

		if err := NewDispatcher(channels, cfg.Notifications.Routes).Notify(ctx, input); err != nil {
			t.Fatalf("failed to deliver notification: %v", err)
		}
		t.Logf("live %s notification dispatched using %s", input.Event, configDir)
		return
	}

	webhookURL := os.Getenv("BQCKUP_DISCORD_WEBHOOK_URL")
	if webhookURL == "" {
		webhookURL = os.Getenv("BQCKUP_SAMPLE_WEBHOOK")
	}
	if webhookURL == "" {
		t.Skip("set BQCKUP_CONFIG_DIR or BQCKUP_DISCORD_WEBHOOK_URL to send a live notification")
	}
	require.NoError(t, NewDiscord("discord", webhookURL).Send(ctx, NewPayload(input)))
}

// TestSendLiveNotification sends all sample events only when explicitly enabled.
//
//	BQCKUP_RUN_LIVE_NOTIFICATION_TESTS=1 go test ./internal/notify -run TestSendLiveNotification -v
func TestSendLiveNotification(t *testing.T) {
	if os.Getenv("BQCKUP_RUN_LIVE_NOTIFICATION_TESTS") != "1" {
		t.Skip("set BQCKUP_RUN_LIVE_NOTIFICATION_TESTS=1 to send live notifications")
	}
	siteName := os.Getenv("BQCKUP_SITE")
	if siteName == "" {
		siteName = "example.org"
	}
	for _, sample := range notificationSamples(time.Now(), siteName) {
		t.Run(sample.name, func(t *testing.T) {
			sendLiveNotification(t, sample.input)
		})
	}
}
