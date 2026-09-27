package jev

import "fmt"

// ResponseError describes a successful HTTP response that could not be read
// or validated. It preserves the request ID even when no Metadata is available.
// Errors returned by this package omit raw parser and response-body details
// from Error; use errors.As or errors.Is to inspect the underlying failure.
type ResponseError struct {
	// RequestID is the X-Request-ID response header, or empty if absent.
	RequestID string
	// Err is the validation or read failure. Errors produced by this package
	// match ErrInvalidResponse and retain any lower-level cause for inspection.
	Err error
}

func (e *ResponseError) Error() string {
	message := ErrInvalidResponse.Error()
	if e.Err != nil {
		message = e.Err.Error()
	}
	if e.RequestID != "" {
		return fmt.Sprintf("%s (request %s)", message, e.RequestID)
	}
	return message
}

// Unwrap returns the underlying validation or read failure.
func (e *ResponseError) Unwrap() error { return e.Err }

func responseError(requestID string, err error) error {
	if err == nil {
		return nil
	}
	return &ResponseError{RequestID: requestID, Err: err}
}

// causeError separates the message safe to log from a potentially sensitive
// underlying error while retaining both the category and cause in the chain.
type causeError struct {
	category error
	message  string
	cause    error
}

func (e *causeError) Error() string {
	return fmt.Sprintf("%s: %s", e.category, e.message)
}

func (e *causeError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.category}
	}
	return []error{e.category, e.cause}
}

func withCause(category error, message string, cause error) error {
	return &causeError{category: category, message: message, cause: cause}
}
