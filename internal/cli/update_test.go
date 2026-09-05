package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/bqckup/bqckup-go/internal/update"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWarnIfNeedsSudoShowsWarningForSystemBinary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root does not need sudo warning")
	}
	var stderr bytes.Buffer
	warnIfNeedsSudo(&stderr, "/usr/local/bin/bqckup")
	assert.Contains(t, stderr.String(), "[WARN]")
	assert.Contains(t, stderr.String(), "sudo")
	assert.Contains(t, stderr.String(), "/usr/local/bin")
}

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
	assert.Contains(t, stderr.String(), "[>] update: downloading and installing latest release")
	assert.Contains(t, stderr.String(), "[✓] update completed: latest")
	assert.Equal(t, "updated bqckup\n", stdout.String())
}

func TestUpdateCommandShowsFinalCompletionStatus(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	opts := &options{output: "text"}
	runner := func(_ context.Context, options update.Options) error {
		_, err := fmt.Fprintln(options.Output, "updated bqckup")
		return err
	}
	cmd := newUpdateCommandWithRunner(opts, runner)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	require.NoError(t, cmd.Execute())
	assert.Contains(t, stderr.String(), "[>] update: downloading and installing latest release")
	assert.Contains(t, stderr.String(), "[✓] update completed: latest")
	assert.Equal(t, "updated bqckup\n", stdout.String())
}

func TestUpdateCommandDoesNotLeakCredentialsOrPathsToStderr(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	opts := &options{output: "text"}
	runner := func(_ context.Context, options update.Options) error {
		_, err := fmt.Fprintln(options.Output, "updated bqckup")
		return err
	}
	cmd := newUpdateCommandWithRunner(opts, runner)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--version", "v1.2.3", "--repository", "https://token:super-secret@example.invalid/private/repo"})

	require.NoError(t, cmd.Execute())
	assert.NotContains(t, stderr.String(), "super-secret")
	assert.NotContains(t, stderr.String(), "example.invalid")
	assert.NotContains(t, stderr.String(), "/private/repo")
	assert.Contains(t, stderr.String(), "[✓] update completed: v1.2.3")
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
