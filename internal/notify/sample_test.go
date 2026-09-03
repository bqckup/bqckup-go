package notify

import (
	"context"
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

// TestSampleFailureOutput previews the rendered Discord embed and email body
// for a failed run, exactly as Send would post/deliver them.
//
//	go test ./internal/notify/ -run TestSampleFailureOutput -v
//
// The email body is written to a temp HTML file (open it in a browser; the
// path is printed). The Discord embed is posted to a real webhook when
// BQCKUP_SAMPLE_WEBHOOK is set (no config change needed); otherwise the
// embed JSON is printed for pasting into
// an embed visualizer. Tweak ErrorCategory/ErrorMessage/Status to preview
// other variants.
func TestSampleFailureOutput(t *testing.T) {
	input := notifyInput(config.EventBackupFailed)
	input.Status = backup.StatusFailed
	input.StartedAt = time.Date(2026, 8, 26, 14, 5, 0, 0, time.UTC)
	input.FinishedAt = time.Date(2026, 8, 26, 14, 9, 30, 0, time.UTC)
	input.ErrorCategory = "execution"
	input.ErrorMessage = "could not export database"
	input.Packages = []history.Package{
		{SourceKind: "files", SourceName: "files", Size: 18038862643},
		{SourceKind: "database", SourceName: "app", Size: 2048},
	}
	payload := NewPayload(input)
	payload.Hostname = "mynas"
	payload.ServerIP = "192.168.1.10"

	fmt.Println("=== DISCORD ===")
	if url, ok := os.LookupEnv("BQCKUP_SAMPLE_WEBHOOK"); ok && url != "" {
		discord := NewDiscord("discord", url)
		require.NoError(t, discord.Send(context.Background(), payload))
		fmt.Println("posted to webhook — lihat di Discord server kamu")
	} else {
		var embed discordPayload
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&embed))
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)
		discord := NewDiscord("discord", server.URL)
		require.NoError(t, discord.Send(context.Background(), payload))
		raw, err := json.MarshalIndent(embed, "", "  ")
		require.NoError(t, err)
		fmt.Println(string(raw))
		fmt.Println("set BQCKUP_SAMPLE_WEBHOOK=<url> untuk kirim ke Discord sungguhan, atau paste JSON di https://discohook.org")
	}

	fmt.Println("=== EMAIL ===")
	smtp := &SMTP{from: "bqckup@example.com", to: []string{"ops@example.com"}}
	path := filepath.Join(os.TempDir(), "bqckup-notify-preview.html")
	require.NoError(t, os.WriteFile(path, []byte(smtp.renderHTML(headline(payload), payload, logoDataURI)), 0o644))
	fmt.Printf("preview tersimpan: %s\nxdg-open %s\n", path, path)
}

// TestSampleCancelledOutput previews the rendered Discord embed and email body for a cancelled run.
//
//	go test ./internal/notify/ -run TestSampleCancelledOutput -v
func TestSampleCancelledOutput(t *testing.T) {
	input := notifyInput(config.EventBackupCancelled)
	input.Status = backup.StatusCancelled
	input.StartedAt = time.Date(2026, 8, 26, 14, 5, 0, 0, time.UTC)
	input.FinishedAt = time.Date(2026, 8, 26, 14, 6, 15, 0, time.UTC)
	input.LastSuccessfulAt = time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	input.ErrorCategory = "cancellation"
	input.ErrorMessage = "backup was cancelled"
	input.Packages = []history.Package{
		{SourceKind: "files", SourceName: "files", Size: 18038862643},
	}
	payload := NewPayload(input)
	payload.Hostname = "mynas"
	payload.ServerIP = "192.168.1.10"

	fmt.Println("=== DISCORD (CANCELLED) ===")
	var embed discordPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&embed))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	discord := NewDiscord("discord", server.URL)
	require.NoError(t, discord.Send(context.Background(), payload))
	raw, err := json.MarshalIndent(embed, "", "  ")
	require.NoError(t, err)
	fmt.Println(string(raw))

	fmt.Println("=== EMAIL (CANCELLED) ===")
	smtp := &SMTP{from: "bqckup@example.com", to: []string{"ops@example.com"}}
	path := filepath.Join(os.TempDir(), "bqckup-notify-cancelled-preview.html")
	require.NoError(t, os.WriteFile(path, []byte(smtp.renderHTML(headline(payload), payload, logoDataURI)), 0o644))
	fmt.Printf("preview tersimpan: %s\nxdg-open %s\n", path, path)
}

// TestSampleNoChangeOutput previews the rendered Discord embed and email body for an unchanged run.
//
//	go test ./internal/notify/ -run TestSampleNoChangeOutput -v
func TestSampleNoChangeOutput(t *testing.T) {
	input := notifyInput(config.EventBackupNoChange)
	input.Status = backup.StatusNoChange
	input.StartedAt = time.Date(2026, 8, 26, 14, 5, 0, 0, time.UTC)
	input.FinishedAt = time.Date(2026, 8, 26, 14, 6, 0, 0, time.UTC)
	input.LastSuccessfulAt = time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	input.ErrorCategory = "no_change"
	input.ErrorMessage = "2 items are unchanged from the previous run."
	input.HasDatabaseSources = true
	input.Destinations = []backup.NotifyDestination{
		{Name: "s3-primary", Bucket: "my-backups"},
	}
	input.Packages = []history.Package{
		{SourceKind: "files", SourceName: "files", Size: 18038862643},
		{SourceKind: "database", SourceName: "app", Size: 2048},
	}
	payload := NewPayload(input)
	payload.Hostname = "mynas"
	payload.ServerIP = "192.168.1.10"

	fmt.Println("=== DISCORD (NO_CHANGE) ===")
	var embed discordPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&embed))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	discord := NewDiscord("discord", server.URL)
	require.NoError(t, discord.Send(context.Background(), payload))
	raw, err := json.MarshalIndent(embed, "", "  ")
	require.NoError(t, err)
	fmt.Println(string(raw))

	fmt.Println("=== EMAIL (NO_CHANGE) ===")
	smtp := &SMTP{from: "bqckup@example.com", to: []string{"ops@example.com"}}
	path := filepath.Join(os.TempDir(), "bqckup-notify-no-change-preview.html")
	require.NoError(t, os.WriteFile(path, []byte(smtp.renderHTML(headline(payload), payload, logoDataURI)), 0o644))
	fmt.Printf("preview tersimpan: %s\nxdg-open %s\n", path, path)
}

func sendLiveNotification(t *testing.T, input backup.NotifyInput) {
	t.Helper()
	if os.Getenv("BQCKUP_RUN_LIVE_NOTIFICATION_TESTS") != "1" {
		t.Skip("set BQCKUP_RUN_LIVE_NOTIFICATION_TESTS=1 to send live notifications")
	}
	ctx := context.Background()

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
		for name, ch := range cfg.Notifications.Channels {
			switch ch.Type {
			case "smtp":
				channels[name] = NewSMTP(name, ch, nil)
			case "webhook":
<<<<<<< HEAD
				channels[name] = NewWebhook(name, ch.URL)
			case "discord":
				channels[name] = NewDiscord(name, ch.WebhookURL)
=======
				channels[name] = NewWebhook(name, ch.URL, os.LookupEnv)
			case "discord":
				channels[name] = NewDiscord(name, ch.WebhookURL, os.LookupEnv)
>>>>>>> 3e1e8c2 (refactor: simplify secret reference config keys)
			}
		}

		dispatcher := NewDispatcher(channels, cfg.Notifications.Routes)
		err = dispatcher.Notify(ctx, input)
		if err != nil {
			t.Fatalf("failed to deliver notification: %v", err)
		}
		t.Logf("Success! Live %s notification dispatched to all configured routes from %s", input.Event, configDir)
		return
	}

	// Fallback to direct environment variables if no config directory is available.
	delivered := 0
	payload := NewPayload(input)

	if webhookURL, ok := os.LookupEnv("BQCKUP_DISCORD_WEBHOOK_URL"); ok && webhookURL != "" {
		discord := NewDiscord("discord", webhookURL)
		require.NoError(t, discord.Send(ctx, payload))
		t.Logf("Discord %s notification sent successfully via BQCKUP_DISCORD_WEBHOOK_URL.", input.Event)
		delivered++
	} else if sampleURL, ok := os.LookupEnv("BQCKUP_SAMPLE_WEBHOOK"); ok && sampleURL != "" {
		discord := NewDiscord("discord", sampleURL)
		require.NoError(t, discord.Send(ctx, payload))
		t.Logf("Discord %s notification sent successfully via BQCKUP_SAMPLE_WEBHOOK.", input.Event)
		delivered++
	}

	if delivered == 0 {
		t.Skip("No configuration directory (/etc/bqckup or $BQCKUP_CONFIG_DIR) or environment variables found. Set BQCKUP_CONFIG_DIR=/etc/bqckup or export BQCKUP_DISCORD_WEBHOOK_URL to send a live test notification.")
	}
}

// TestSendLiveFailureNotification sends a simulated failure notification
// to all channels configured in your configuration (e.g. /etc/bqckup or $BQCKUP_CONFIG_DIR),
// or configured via environment variables.
//
// Usage:
//
//	sudo -E go test -v ./internal/notify/ -run TestSendLiveFailureNotification
func TestSendLiveFailureNotification(t *testing.T) {
	siteName := os.Getenv("BQCKUP_SITE")
	if siteName == "" {
		siteName = "example.org"
	}

	input := backup.NotifyInput{
		Event:            config.EventBackupFailed,
		SiteName:         siteName,
		Status:           backup.StatusFailed,
		StartedAt:        time.Now().Add(-4 * time.Minute),
		FinishedAt:       time.Now(),
		LastSuccessfulAt: time.Now().Add(-24 * time.Hour),
		FailureStreak:    2,
		ErrorCategory:    "execution",
		ErrorMessage:     "could not export database: connection to 127.0.0.1:3306 failed",
		Packages: []history.Package{
			{SourceKind: "files", SourceName: "files", Size: 18038862643},
			{SourceKind: "database", SourceName: "app", Size: 2048},
		},
	}

	sendLiveNotification(t, input)
}

// TestSendLiveCancelledNotification sends a simulated cancellation notification
// to all configured channels.
//
// Usage:
//
//	sudo -E go test -v ./internal/notify/ -run TestSendLiveCancelledNotification
func TestSendLiveCancelledNotification(t *testing.T) {
	siteName := os.Getenv("BQCKUP_SITE")
	if siteName == "" {
		siteName = "example.org"
	}

	input := backup.NotifyInput{
		Event:            config.EventBackupCancelled,
		SiteName:         siteName,
		Status:           backup.StatusCancelled,
		StartedAt:        time.Now().Add(-1 * time.Minute),
		FinishedAt:       time.Now(),
		LastSuccessfulAt: time.Now().Add(-24 * time.Hour),
		ErrorCategory:    "cancellation",
		ErrorMessage:     "backup was cancelled",
		Packages: []history.Package{
			{SourceKind: "files", SourceName: "files", Size: 18038862643},
		},
	}

	sendLiveNotification(t, input)
}

// TestSendLiveNoChangeNotification sends a simulated no_change notification
// to all configured channels.
//
// Usage:
//
//	sudo -E go test -v ./internal/notify/ -run TestSendLiveNoChangeNotification
func TestSendLiveNoChangeNotification(t *testing.T) {
	siteName := os.Getenv("BQCKUP_SITE")
	if siteName == "" {
		siteName = "example.org"
	}

	input := backup.NotifyInput{
		Event:              config.EventBackupNoChange,
		SiteName:           siteName,
		Status:             backup.StatusNoChange,
		StartedAt:          time.Now().Add(-1 * time.Minute),
		FinishedAt:         time.Now(),
		LastSuccessfulAt:   time.Now().Add(-24 * time.Hour),
		FailureStreak:      0,
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
	}

	sendLiveNotification(t, input)
}
