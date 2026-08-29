package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// postJSON sends one JSON body with an explicit content type and fails on
// any non-2xx response. The caller owns the client and its timeout.
func postJSON(ctx context.Context, client *http.Client, url string, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("returned status %s", response.Status)
	}
	return nil
}

// Webhook posts the shared payload as JSON to a URL taken from an
// environment variable at send time. Missing or empty env is a per-channel
// error; the caller warns and moves on.
type Webhook struct {
	name      string
	urlEnv    string
	lookupEnv func(string) (string, bool)
	client    *http.Client
}

func NewWebhook(name, urlEnv string, lookupEnv func(string) (string, bool)) *Webhook {
	return &Webhook{
		name:      name,
		urlEnv:    urlEnv,
		lookupEnv: lookupEnv,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *Webhook) Name() string { return w.name }

func (w *Webhook) Send(ctx context.Context, payload Payload) error {
	url, ok := w.lookupEnv(w.urlEnv)
	if !ok || url == "" {
		return fmt.Errorf("environment variable %q is not set", w.urlEnv)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	return postJSON(ctx, w.client, url, body)
}
