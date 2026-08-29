package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookPostsExactPayload(t *testing.T) {
	var received struct {
		method string
		ctype  string
		body   map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.method = r.Method
		received.ctype = r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received.body))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	webhook := NewWebhook("webhook", "BQCKUP_WEBHOOK_URL", func(key string) (string, bool) {
		if key == "BQCKUP_WEBHOOK_URL" {
			return server.URL, true
		}
		return "", false
	})
	payload := NewPayload(notifyInput(config.EventBackupFailed))
	require.NoError(t, webhook.Send(context.Background(), payload))

	assert.Equal(t, http.MethodPost, received.method)
	assert.Equal(t, "application/json", received.ctype)
	assert.Equal(t, "backup_failed", received.body["event"])
	assert.Equal(t, "example.org", received.body["site"])
	assert.Equal(t, "failed", received.body["status"])
}

func TestWebhookReturnsNon2xxStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	webhook := NewWebhook("webhook", "BQCKUP_WEBHOOK_URL", func(string) (string, bool) { return server.URL, true })
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestWebhookReturnsNetworkErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := server.URL
	server.Close() // connection refused from here on

	webhook := NewWebhook("webhook", "BQCKUP_WEBHOOK_URL", func(string) (string, bool) { return url, true })
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	require.Error(t, err)
}

func TestWebhookMissingEnvIsAnError(t *testing.T) {
	webhook := NewWebhook("webhook", "BQCKUP_WEBHOOK_URL", func(string) (string, bool) { return "", false })
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BQCKUP_WEBHOOK_URL")
}

func TestWebhookTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	webhook := NewWebhook("webhook", "BQCKUP_WEBHOOK_URL", func(string) (string, bool) { return server.URL, true })
	webhook.client.Timeout = 50 * time.Millisecond
	started := time.Now()
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	assert.Error(t, err)
	assert.Less(t, time.Since(started), 2*time.Second)
}
