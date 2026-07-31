package history

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateIsIdempotent(t *testing.T) {
	db, closeDB, err := Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB()) })

	require.NoError(t, Migrate(context.Background(), db))
	require.NoError(t, Migrate(context.Background(), db))

	var count int64
	require.NoError(t, db.Table("schema_migrations").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestOpenUsesSQLiteSafetyPragmas(t *testing.T) {
	db, closeDB, err := Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB()) })

	var journalMode string
	require.NoError(t, db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error)
	assert.Equal(t, "wal", journalMode)

	var foreignKeys int
	require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	assert.Equal(t, 1, foreignKeys)
}
