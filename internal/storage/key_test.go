package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateKey(t *testing.T) {
	for _, key := range []string{
		"bqckup/site/2026-08-05T00-00-00Z/files.tar.gz",
		"company/bqckup/site",
	} {
		t.Run("accepts "+key, func(t *testing.T) {
			require.NoError(t, ValidateKey(key))
		})
	}
	for _, key := range []string{
		"",
		"/absolute",
		"../escape",
		"safe/../escape",
		`safe\escape`,
		"safe//empty",
		"safe/./dot",
		"trailing/",
	} {
		t.Run("rejects "+key, func(t *testing.T) {
			require.Error(t, ValidateKey(key))
		})
	}
}

func TestJoinPrefix(t *testing.T) {
	key, err := JoinPrefix("company", "bqckup/site/file")
	require.NoError(t, err)
	assert.Equal(t, "company/bqckup/site/file", key)

	key, err = JoinPrefix("", "bqckup/site/file")
	require.NoError(t, err)
	assert.Equal(t, "bqckup/site/file", key)

	_, err = JoinPrefix("../escape", "bqckup/site/file")
	require.Error(t, err)
}
