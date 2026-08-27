package cli

import (
	"bytes"
	"testing"

	"github.com/bqckup/bqckup-go/internal/buildinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCommandWritesStableText(t *testing.T) {
	root := NewRoot(buildinfo.Info{Version: "0.1.0", Commit: "abc123"})
	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"version"})

	require.NoError(t, root.Execute())
	assert.Equal(t, "bqckup 0.1.0 (abc123)\n", out.String())
}

func TestVersionCommandOmitsEmptyCommit(t *testing.T) {
	root := NewRoot(buildinfo.Info{Version: "v0.0.3"})
	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"version"})

	require.NoError(t, root.Execute())
	assert.Equal(t, "bqckup v0.0.3\n", out.String())
}

func TestRootRejectsUnknownCommand(t *testing.T) {
	root := NewRoot(buildinfo.Info{Version: "dev", Commit: "unknown"})
	root.SetArgs([]string{"missing"})
	assert.Error(t, root.Execute())
}
