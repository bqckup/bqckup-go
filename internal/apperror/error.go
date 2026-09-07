package apperror

import (
	"errors"
	"regexp"
	"strings"
)

type Category string

const (
	CategoryConfig       Category = "config"
	CategoryPreflight    Category = "preflight"
	CategoryExecution    Category = "execution"
	CategoryStorage      Category = "storage"
	CategoryPersistence  Category = "persistence"
	CategoryCancellation Category = "cancellation"
	CategoryInternal     Category = "internal"
)

type Error struct {
	Category Category
	Message  string
	Cause    error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func Wrap(category Category, message string, cause error) error {
	if cause == nil {
		return &Error{Category: category, Message: message}
	}
	return &Error{Category: category, Message: message, Cause: cause}
}

func CategoryOf(err error) Category {
	var applicationError *Error
	if errors.As(err, &applicationError) {
		return applicationError.Category
	}
	return CategoryInternal
}

func UserMessage(err error) string {
	var applicationError *Error
	if errors.As(err, &applicationError) {
		return applicationError.Message
	}
	return "an internal error occurred"
}

var (
	diagnosticURL    = regexp.MustCompile(`https?://[^\s]+`)
	diagnosticSecret = regexp.MustCompile(`(?i)(password|secret|access[_-]?key|webhook[_-]?url)=([^\s,;]+)`)
)

// DiagnosticMessage returns a deduplicated, redacted error chain suitable for
// operational logs. User-facing messages remain controlled by UserMessage.
func DiagnosticMessage(err error) string {
	if err == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	seen := make(map[string]struct{})
	var walk func(error)
	walk = func(current error) {
		if current == nil {
			return
		}
		message := strings.TrimSpace(current.Error())
		if message != "" {
			message = diagnosticURL.ReplaceAllString(message, "<redacted-url>")
			message = diagnosticSecret.ReplaceAllString(message, "$1=<redacted>")
			if _, ok := seen[message]; !ok {
				seen[message] = struct{}{}
				parts = append(parts, message)
			}
		}
		switch unwrapped := current.(type) {
		case interface{ Unwrap() []error }:
			for _, cause := range unwrapped.Unwrap() {
				walk(cause)
			}
		case interface{ Unwrap() error }:
			walk(unwrapped.Unwrap())
		}
	}
	walk(err)
	return strings.Join(parts, ": ")
}

// Hidden wraps an error with a public message. The cause is never shown
// in Error(), but errors.Is and errors.As still reach it through Unwrap.
type Hidden struct {
	Public string
	Cause  error
}

func (h *Hidden) Error() string { return h.Public }
func (h *Hidden) Unwrap() error { return h.Cause }

// Hide returns an error that shows public instead of the cause text.
func Hide(public string, cause error) error {
	return &Hidden{Public: public, Cause: cause}
}
