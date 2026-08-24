package retention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyKeepsNewestSuccessfulSets(t *testing.T) {
	store := &fakeStore{sets: backupSets(
		"2026-01-03T00-00-00.000000000Z",
		"2026-01-01T00-00-00.000000000Z",
		"2026-01-02T00-00-00.000000000Z",
	)}

	require.NoError(t, Apply(context.Background(), store, "bqckup/site", 2))
	assert.Equal(t, []string{"bqckup/site/2026-01-01T00-00-00.000000000Z"}, store.deleted)
}

func TestApplyRejectsInvalidKeepLast(t *testing.T) {
	err := Apply(context.Background(), &fakeStore{}, "bqckup/site", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keep_last")
}

func TestApplyStopsAtFirstDeletionFailure(t *testing.T) {
	deleteErr := errors.New("disk unavailable")
	store := &fakeStore{
		sets:      backupSets("2026-01-01T00-00-00.000000000Z", "2026-01-02T00-00-00.000000000Z", "2026-01-03T00-00-00.000000000Z"),
		deleteErr: deleteErr,
	}

	err := Apply(context.Background(), store, "bqckup/site", 1)
	require.ErrorIs(t, err, deleteErr)
	assert.Equal(t, []string{"bqckup/site/2026-01-01T00-00-00.000000000Z"}, store.deleted)
}

type fakeStore struct {
	sets      []storage.BackupSet
	deleted   []string
	deleteErr error
}

func (f *fakeStore) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return append([]storage.BackupSet(nil), f.sets...), nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return f.deleteErr
}

func backupSets(names ...string) []storage.BackupSet {
	sets := make([]storage.BackupSet, 0, len(names))
	for _, name := range names {
		parsed, _ := time.Parse(storage.TimestampLayout, name)
		sets = append(sets, storage.BackupSet{Key: "bqckup/site/" + name, CreatedAt: parsed})
	}
	return sets
}
