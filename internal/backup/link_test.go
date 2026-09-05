package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLinkGenerator struct {
	link   storage.DownloadLink
	err    error
	gotKey string
	gotExp time.Duration
}

func (f *fakeLinkGenerator) PresignLink(_ context.Context, key string, expires time.Duration) (storage.DownloadLink, error) {
	f.gotKey = key
	f.gotExp = expires
	return f.link, f.err
}

func (f *fakeLinkGenerator) Put(context.Context, storage.Package, string) (storage.StoredPackage, error) {
	return storage.StoredPackage{}, errors.New("unused")
}
func (f *fakeLinkGenerator) Delete(context.Context, string) error { return errors.New("unused") }
func (f *fakeLinkGenerator) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return nil, errors.New("unused")
}
func (f *fakeLinkGenerator) Probe(context.Context) error { return nil }

func TestLinkFullModeReturnsTheLink(t *testing.T) {
	generator := &fakeLinkGenerator{link: storage.DownloadLink{URL: "https://example.test/signed", Key: "k", ExpiresAt: time.Now()}}
	linker := &Linker{}

	link, err := linker.Link(context.Background(), "s3-primary", fullSite(), generator, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "https://example.test/signed", link.URL)
	assert.Equal(t, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", generator.gotKey)
	assert.Equal(t, time.Hour, generator.gotExp)
}

func TestLinkIncrementalModeIsConfigError(t *testing.T) {
	generator := &fakeLinkGenerator{link: storage.DownloadLink{URL: "https://example.test/signed"}}
	site := fullSite()
	site.BackupMode = "incremental"

	_, err := (&Linker{}).Link(context.Background(), "s3-primary", site, generator, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "restore")
}

func TestLinkLocalDestinationShowsLocalPath(t *testing.T) {
	_, err := (&Linker{}).Link(context.Background(), "local-disk", fullSite(), fakeLocalStore{}, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryConfig, apperror.CategoryOf(err))
	assert.Contains(t, err.Error(), "/srv/backups/bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz")
}

func TestLinkGeneratorFailureKeepsStorageCategory(t *testing.T) {
	generator := &fakeLinkGenerator{err: errors.New("provider secret response")}

	_, err := (&Linker{}).Link(context.Background(), "s3-primary", fullSite(), generator, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryStorage, apperror.CategoryOf(err))
}

func TestLinkPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (&Linker{}).Link(ctx, "s3-primary", fullSite(), &fakeLinkGenerator{}, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.ErrorIs(t, err, context.Canceled)
}
