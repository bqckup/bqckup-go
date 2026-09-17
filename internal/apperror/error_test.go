package apperror

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorPreservesCauseAndExposesRedactedMessage(t *testing.T) {
	cause := errors.New("password=secret")
	err := Wrap(CategoryStorage, "could not store backup package", cause)

	require.ErrorIs(t, err, cause)
	assert.Equal(t, CategoryStorage, CategoryOf(err))
	assert.Equal(t, "could not store backup package", UserMessage(err))
	assert.NotContains(t, UserMessage(err), "secret")
}

func TestDiagnosticMessageIncludesRedactedCauseChain(t *testing.T) {
	err := Wrap(CategoryStorage, "could not store backup package", errors.New("upload failed: exceeded total allowed MaxUploadParts; endpoint=https://s3.example.test/bucket?secret=topsecret"))
	assert.Equal(t, "could not store backup package: upload failed: exceeded total allowed MaxUploadParts; endpoint=<redacted-url>", DiagnosticMessage(err))
}

func TestNotificationMessageIncludesRedactedCauseChain(t *testing.T) {
	err := Wrap(CategoryExecution, "could not create incremental file backup",
		fmt.Errorf("repository: could not create the incremental backup: %w",
			errors.New(`archiver: combine roots: tree: nodes are not sorted by name: "eprints" after "mysql"`)))

	assert.Equal(t,
		`could not create incremental file backup: repository: could not create the incremental backup: archiver: combine roots: tree: nodes are not sorted by name: "eprints" after "mysql"`,
		NotificationMessage(err),
	)
}

func TestNotificationMessageDoesNotExposeHiddenCause(t *testing.T) {
	err := Wrap(CategoryStorage, "could not store backup package",
		Hide("S3-compatible upload failed", errors.New("provider body password=supersecret")))

	assert.Equal(t, "could not store backup package: S3-compatible upload failed", NotificationMessage(err))
	assert.NotContains(t, NotificationMessage(err), "supersecret")
}

func TestNotificationMessageRedactsAbsolutePaths(t *testing.T) {
	err := Wrap(CategoryExecution, "could not create the file archive",
		errors.New("inspect archive source /srv/private/customer.sql: permission denied"))

	assert.Equal(t,
		"could not create the file archive: inspect archive source <redacted-path>: permission denied",
		NotificationMessage(err),
	)
	assert.NotContains(t, NotificationMessage(err), "customer.sql")
}
