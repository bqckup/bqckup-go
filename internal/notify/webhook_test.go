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

	webhook := NewWebhook("webhook", server.URL)
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

	webhook := NewWebhook("webhook", server.URL)
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestWebhookReturnsNetworkErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := server.URL
	server.Close() // connection refused from here on

	webhook := NewWebhook("webhook", url)
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	require.Error(t, err)
}

func TestWebhookEmptyURLIsAnError(t *testing.T) {
	webhook := NewWebhook("webhook", "")
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported protocol scheme")
}

func TestWebhookTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	webhook := NewWebhook("webhook", server.URL)
	webhook.client.Timeout = 50 * time.Millisecond
	started := time.Now()
	err := webhook.Send(context.Background(), NewPayload(notifyInput(config.EventBackupFailed)))
	assert.Error(t, err)
	assert.Less(t, time.Since(started), 2*time.Second)
}
