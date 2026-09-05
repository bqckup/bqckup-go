// Package remoteconfig resolves S3-compatible storage configuration from a
// constrained HTTPS JSON provider. Provider values exist only in memory.
package remoteconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
)

const (
	requestTimeout   = 10 * time.Second
	maxResponseBytes = 64 * 1024
)

type providerResponse struct {
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
}

// Resolver fetches remote storage documents with bounded network and parsing
// behavior. It is safe to reuse for multiple storage entries.
type Resolver struct {
	client *http.Client
}

func New() *Resolver {
	return &Resolver{client: &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if err := validateProviderURL(request.URL); err != nil {
				return errors.New("unsafe remote storage configuration redirect")
			}
			return nil
		},
	}}
}

// Resolve returns a copy of configured with every remote entry replaced by
// the provider response. The caller must validate the complete configuration
// after resolution.
func (r *Resolver) Resolve(ctx context.Context, configured map[string]config.Storage) (map[string]config.Storage, error) {
	resolved := make(map[string]config.Storage, len(configured))
	for name, storage := range configured {
		resolved[name] = storage
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		storage := configured[name]
		if storage.Credentials.Source != "remote" {
			continue
		}
		providerURL := strings.TrimSpace(storage.Credentials.URL)
		if providerURL == "" {
			return nil, unavailable(errors.New("remote storage configuration URL is empty"))
		}
		parsed, err := url.Parse(providerURL)
		if err != nil || validateProviderURL(parsed) != nil {
			return nil, unavailable(errors.New("remote storage configuration URL is invalid"))
		}
		response, err := r.fetch(ctx, parsed.String())
		if err != nil {
			return nil, err
		}
		storage.Bucket = response.Bucket
		storage.AccessKeyID = response.AccessKeyID
		storage.SecretAccessKey = response.SecretAccessKey
		storage.Endpoint = response.Endpoint
		storage.Region = response.Region
		storage.Credentials = config.StorageCredentials{}
		resolved[name] = storage
	}
	return resolved, nil
}

func (r *Resolver) fetch(ctx context.Context, providerURL string) (providerResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, providerURL, nil)
	if err != nil {
		return providerResponse{}, unavailable(err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return providerResponse{}, apperror.Hide("remote storage configuration request canceled", err)
		}
		return providerResponse{}, unavailable(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return providerResponse{}, unavailable(fmt.Errorf("provider returned HTTP status %d", response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return providerResponse{}, unavailable(err)
	}
	if len(body) > maxResponseBytes {
		return providerResponse{}, unavailable(errors.New("provider response exceeds size limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result providerResponse
	if err := decoder.Decode(&result); err != nil {
		return providerResponse{}, unavailable(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return providerResponse{}, unavailable(errors.New("provider response contains trailing data"))
	}
	return result, nil
}

func unavailable(cause error) error {
	return apperror.Hide("remote storage configuration is unavailable", cause)
}

func validateProviderURL(parsed *url.URL) error {
	if parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return errors.New("provider URL must be absolute")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return errors.New("provider URL contains unsupported components")
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(parsed.Scheme, "http") && isLoopback(parsed.Hostname()) {
		return nil
	}
	return errors.New("provider URL must use HTTPS")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
