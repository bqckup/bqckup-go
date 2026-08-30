package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
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

func TestFormatErrorMessageKeepsCleanMessageAndCauseChain(t *testing.T) {
	err := apperror.Wrap(apperror.CategoryExecution, "could not create incremental file backup", errors.New("restic: snapshot failed: exit status 1"))
	assert.Equal(t, "could not create incremental file backup: restic: snapshot failed: exit status 1", formatErrorMessage(err))
}

func TestFormatErrorMessageSkipsEmptyAndDuplicateCauseText(t *testing.T) {
	cause := errors.New("repo: lock held by another process")
	err := apperror.Wrap(apperror.CategoryStorage, "could not unlock the incremental repository",
		apperror.Wrap(apperror.CategoryStorage, "repo: lock held by another process", cause))
	assert.Equal(t, "could not unlock the incremental repository: repo: lock held by another process", formatErrorMessage(err))
}

func TestFormatErrorMessageDescendsIntoJoinedErrors(t *testing.T) {
	operationErr := apperror.Wrap(apperror.CategoryExecution, "could not export database", errors.New("mysql: connection refused"))
	err := errors.Join(operationErr, apperror.Wrap(apperror.CategoryPersistence, "could not finalize backup history", errors.New("database unavailable")))
	assert.Equal(t, "could not export database: mysql: connection refused: could not finalize backup history: database unavailable", formatErrorMessage(err))
}

func TestFormatErrorMessageDescendsIntoJoinNestedInCause(t *testing.T) {
	inner := errors.Join(errors.New("storage: quota exceeded"), errors.New("cleanup: partial key left"))
	err := apperror.Wrap(apperror.CategoryStorage, "could not store backup package", inner)
	assert.Equal(t, "could not store backup package: storage: quota exceeded: cleanup: partial key left", formatErrorMessage(err))
}

func TestFormatErrorMessagePassesThroughNonAppErrors(t *testing.T) {
	assert.Equal(t, "plain failure", formatErrorMessage(errors.New("plain failure")))
}

func TestFormatErrorMessageAvoidsRepeatedWrappedPathErrors(t *testing.T) {
	cause := fmt.Errorf("inspect archive source /missing: %w", os.ErrNotExist)
	err := apperror.Wrap(apperror.CategoryExecution, "could not create the file archive", cause)
	assert.Equal(t, "could not create the file archive: inspect archive source /missing: file does not exist", formatErrorMessage(err))
}
