package incremental

import (
	"errors"
	"fmt"

	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
)

var (
	// ErrInvalidPassword reports that a password could not decrypt the
	// repository key file (re-exported from crypto for callers of this package).
	ErrInvalidPassword = crypto.ErrInvalidPassword
	// ErrCorrupted reports unreadable or tampered repository data.
	ErrCorrupted = errors.New("repository data is corrupted")
	// ErrRepoNotFound reports that no repository exists at the location.
	ErrRepoNotFound = errors.New("repository not found")
)

// RedactedError carries a safe public message while hiding the cause from
// output. Secrets (passwords, key bytes, credentials) never appear in
// Message; they may exist only in the wrapped cause, which is not printed.
type RedactedError struct {
	Category string
	Message  string
	Err      error
}

func (e *RedactedError) Error() string {
	if e.Category == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Category, e.Message)
}

func (e *RedactedError) Unwrap() error { return e.Err }
