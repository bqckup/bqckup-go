package apperror

import "errors"

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
