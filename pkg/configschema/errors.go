package configschema

import "errors"

// FieldError is a validation error for a specific configuration option. Path holds the keys
// leading to it (list indexes as decimal strings), e.g. ["feeds", "show", "url"].
type FieldError struct {
	Path []string
	Err  error
}

func (e *FieldError) Error() string { return e.Err.Error() }

func (e *FieldError) Unwrap() error { return e.Err }

// NewFieldError attaches an option path to err. It returns nil for a nil err.
func NewFieldError(err error, path ...string) error {
	if err == nil {
		return nil
	}
	return &FieldError{Path: path, Err: err}
}

// ErrorPath returns the option path attached to err, or nil.
func ErrorPath(err error) []string {
	var fieldErr *FieldError
	if errors.As(err, &fieldErr) {
		return fieldErr.Path
	}
	return nil
}
