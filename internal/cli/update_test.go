package cli

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/bqckup/bqckup-go/internal/update"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateCommandShowsProgressBeforeRunningUpdate(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	opts := &options{output: "text"}
	runner := func(_ context.Context, options update.Options) error {
		assert.Contains(t, stderr.String(), "[>] update: downloading and installing latest release")
		_, err := fmt.Fprintln(options.Output, "updated bqckup")
		return err
	}
	cmd := newUpdateCommandWithRunner(opts, runner)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	require.NoError(t, cmd.Execute())
	assert.Equal(t, "[>] update: downloading and installing latest release\n", stderr.String())
	assert.Equal(t, "updated bqckup\n", stdout.String())
}

func TestUpdateCommandSuppressesProgressForJSONOutput(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	opts := &options{output: "json"}
	runner := func(_ context.Context, options update.Options) error {
		_, err := fmt.Fprintln(options.Output, "updated bqckup")
		return err
	}
	cmd := newUpdateCommandWithRunner(opts, runner)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	require.NoError(t, cmd.Execute())
	assert.Empty(t, stderr.String())
	assert.Equal(t, "updated bqckup\n", stdout.String())
}
