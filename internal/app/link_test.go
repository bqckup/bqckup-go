package app

import (
	"context"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type appLinkStore struct {
	appFakeStore
	link   storage.DownloadLink
	err    error
	gotKey string
	gotExp time.Duration
}

func (a *appLinkStore) PresignLink(_ context.Context, key string, expires time.Duration) (storage.DownloadLink, error) {
	a.gotKey = key
	a.gotExp = expires
	return a.link, a.err
}

func TestLinkResolvesSiteFromSupportedKeyLayouts(t *testing.T) {
	tests := []struct {
		name, serverID, backupPrefix, key string
	}{
		{name: "legacy", key: "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz"},
		{name: "server", serverID: "127.0.0.1", key: "bqckup/127.0.0.1/site-a/30-August-2026/04-42-59/files.tar.gz"},
		{name: "prefixed server", serverID: "194.233.87.182", backupPrefix: "hosting_client", key: "bqckup/hosting_client/194.233.87.182/site-a/30-August-2026/04-42-59/files.tar.gz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &appLinkStore{link: storage.DownloadLink{URL: "https://example.test/signed"}}
			application := listingApp(t, remoteSite(), map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": store})
			application.configuration.ServerID = test.serverID
			application.configuration.BackupPrefix = test.backupPrefix

			link, err := application.Link(context.Background(), "s3-primary", test.key, time.Hour)
			require.NoError(t, err)
			assert.Equal(t, "https://example.test/signed", link.URL)
			assert.Equal(t, test.key, store.gotKey)
			assert.Equal(t, time.Hour, store.gotExp)
		})
	}
}

func TestLinkRejectsMalformedKeys(t *testing.T) {
	application := listingApp(t, remoteSite(), map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": &appLinkStore{}})
	for _, key := range []string{
		"other/2026-08-05T00-00-00Z/files.tar.gz",
		"bqckup/site-a",
		"bqckup/site-a/",
		"bqckup/site-a/../files.tar.gz",
		"bqckup//files.tar.gz",
	} {
		_, err := application.Link(context.Background(), "s3-primary", key, time.Hour)
		require.Error(t, err, key)
		assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err), key)
	}
}

func TestLinkRejectsUnknownOrDisabledSite(t *testing.T) {
	for _, site := range []config.Site{
		{Name: "other-site", Enabled: true, BackupMode: "full", Destinations: []config.Destination{{Storage: "s3-primary"}}}, // key names another site
		{Name: "site-a", Enabled: false, BackupMode: "full", Destinations: []config.Destination{{Storage: "s3-primary"}}},
	} {
		application := listingApp(t, site, map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": &appLinkStore{}})
		_, err := application.Link(context.Background(), "s3-primary", "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
		require.Error(t, err)
		assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	}
}

func TestLinkRejectsUnknownOrUnusedDestination(t *testing.T) {
	unknown := listingApp(t, remoteSite(), map[string]config.Storage{"s3-primary": {Type: "s3"}}, map[string]storage.Store{"s3-primary": &appLinkStore{}})
	_, err := unknown.Link(context.Background(), "missing", "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))

	unused := listingApp(t, remoteSite(), map[string]config.Storage{
		"s3-primary": {Type: "s3"},
		"s3-other":   {Type: "s3"},
	}, map[string]storage.Store{
		"s3-primary": &appLinkStore{},
		"s3-other":   &appLinkStore{},
	})
	_, err = unused.Link(context.Background(), "s3-other", "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
}

func TestLinkLocalDestinationIncludesLocalPath(t *testing.T) {
	site := remoteSite()
	site.Destinations = []config.Destination{{Storage: "local-primary"}}
	application := listingApp(t, site, map[string]config.Storage{"local-primary": {Type: "local"}}, map[string]storage.Store{"local-primary": appFakeStore{}})

	// appFakeStore has neither PresignLink nor LocalPath: the use case must
	// still produce the local-destination config error.
	_, err := application.Link(context.Background(), "local-primary", "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "local")
}
