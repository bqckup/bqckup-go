package apperror

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorPreservesCauseAndExposesRedactedMessage(t *testing.T) {
	cause := errors.New("password=secret")
	err := Wrap(CategoryStorage, "could not store backup artifact", cause)

	require.ErrorIs(t, err, cause)
	assert.Equal(t, CategoryStorage, CategoryOf(err))
	assert.Equal(t, "could not store backup artifact", UserMessage(err))
	assert.NotContains(t, UserMessage(err), "secret")
}
