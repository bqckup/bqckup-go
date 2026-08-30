package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWriteProgressCleanupDoesNotAddBlankLine(t *testing.T) {
	var output bytes.Buffer

	writeProgressCleanup(&output, true)

	assert.Equal(t, "\r\033[2K", output.String())
}

func TestWriteProgressCleanupSkipsOutputWithoutRenderedFrame(t *testing.T) {
	var output bytes.Buffer

	writeProgressCleanup(&output, false)

	assert.Empty(t, output.String())
}
