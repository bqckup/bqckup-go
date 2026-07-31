package config

import "fmt"

type ErrorKind string

const (
	ErrorRead       ErrorKind = "read"
	ErrorDecode     ErrorKind = "decode"
	ErrorValidation ErrorKind = "validation"
)

// Error preserves source context without exposing configuration values.
type Error struct {
	File  string
	Field string
	Kind  ErrorKind
	Err   error
}

func (e *Error) Error() string {
	location := e.File
	if e.Field != "" {
		location += ":" + e.Field
	}
	return fmt.Sprintf("config %s error in %s: %v", e.Kind, location, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func validationError(file, field, format string, args ...any) error {
	return &Error{
		File:  file,
		Field: field,
		Kind:  ErrorValidation,
		Err:   fmt.Errorf(format, args...),
	}
}
