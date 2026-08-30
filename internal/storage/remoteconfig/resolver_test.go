package remoteconfig

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolverLoadsRemoteStorageConfigurationIntoMemory(t *testing.T) {
	var method, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, accept = r.Method, r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"bucket":"remote-bucket","access_key_id":"remote-key","secret_access_key":"remote-secret","endpoint":"https://objects.example.invalid","region":"us-east-1"}`))
	}))
	t.Cleanup(server.Close)
	resolved, err := New().Resolve(context.Background(), map[string]config.Storage{
		"remote": {
			Type: "s3", Prefix: "tenant", Primary: true,
			Credentials: config.StorageCredentials{Source: "remote", URL: server.URL},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, method)
	assert.Equal(t, "application/json", accept)
	assert.Equal(t, "remote-bucket", resolved["remote"].Bucket)
	assert.Equal(t, "remote-key", resolved["remote"].AccessKeyID)
	assert.Equal(t, "remote-secret", resolved["remote"].SecretAccessKey)
	assert.Equal(t, "https://objects.example.invalid", resolved["remote"].Endpoint)
	assert.Equal(t, "us-east-1", resolved["remote"].Region)
	assert.Equal(t, "tenant", resolved["remote"].Prefix)
	assert.True(t, resolved["remote"].Primary)
	assert.Empty(t, resolved["remote"].Credentials)
}

func TestResolverLeavesInlineAndLocalStoragesUnchanged(t *testing.T) {
	configured := map[string]config.Storage{
		"local":  {Type: "local", Directory: "/var/backups"},
		"inline": {Type: "s3", Bucket: "bucket", AccessKeyID: "key", SecretAccessKey: "secret", Region: "region"},
	}
	resolved, err := New().Resolve(context.Background(), configured)
	require.NoError(t, err)
	assert.Equal(t, configured, resolved)
}

func TestResolverHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := New().Resolve(ctx, remoteStorage(server.URL))
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), server.URL)
}

func TestResolverAppliesHTTPTimeoutWithoutLeakingURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	resolver := New()
	resolver.client.Timeout = time.Millisecond

	_, err := resolver.Resolve(context.Background(), remoteStorage(server.URL))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, "remote storage configuration request canceled", err.Error())
	assert.NotContains(t, err.Error(), server.URL)
}

func TestResolverRejectsUnsafeOrUnavailableProviderWithoutLeakingIt(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "empty URL", url: ""},
		{name: "non HTTPS remote", url: "http://provider.example.invalid/credentials?token=leaked"},
		{name: "URL user information", url: "https://user:password@provider.example.invalid/credentials"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New().Resolve(context.Background(), remoteStorage(test.url))
			require.Error(t, err)
			assert.Equal(t, "remote storage configuration is unavailable", err.Error())
			if test.url != "" {
				assert.NotContains(t, err.Error(), test.url)
			}
		})
	}
}

func TestResolverRedactsProviderFailuresAndRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "not found response", status: http.StatusNotFound, body: `{"error":"Not Found","message":"Storage not found! secret-body"}`},
		{name: "unknown field", status: http.StatusOK, body: `{"bucket":"bucket","access_key_id":"key","secret_access_key":"secret-body","region":"region","unexpected":true}`},
		{name: "trailing JSON", status: http.StatusOK, body: `{"bucket":"bucket"} {"secret":"secret-body"}`},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("secret-body", maxResponseBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			_, err := New().Resolve(context.Background(), remoteStorage(server.URL))
			require.Error(t, err)
			assert.Equal(t, "remote storage configuration is unavailable", err.Error())
			assert.NotContains(t, err.Error(), server.URL)
			assert.NotContains(t, err.Error(), "secret-body")
			assert.NotContains(t, fmt.Sprintf("%v", err), test.body)
		})
	}
}

func remoteStorage(providerURL string) map[string]config.Storage {
	return map[string]config.Storage{
		"remote": {Type: "s3", Credentials: config.StorageCredentials{Source: "remote", URL: providerURL}},
	}
}
