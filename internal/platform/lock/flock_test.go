package lock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryLockDoesNotBlockAndCanBeReacquired(t *testing.T) {
	locker := New(t.TempDir())
	unlockFirst, acquired, err := locker.TryLock(context.Background(), "example")
	require.NoError(t, err)
	require.True(t, acquired)

	_, acquired, err = locker.TryLock(context.Background(), "example")
	require.NoError(t, err)
	assert.False(t, acquired)

	require.NoError(t, unlockFirst())
	unlockThird, acquired, err := locker.TryLock(context.Background(), "example")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, unlockThird())
}

func TestTryLockRejectsUnsafeSiteName(t *testing.T) {
	_, _, err := New(t.TempDir()).TryLock(context.Background(), "../escape")
	require.Error(t, err)
}
