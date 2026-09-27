package jev

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type diagnosticCause struct{}

func (*diagnosticCause) Error() string { return "private-input-value" }

func TestWithCausePreservesClassificationWithoutLoggingCause(t *testing.T) {
	cause := &diagnosticCause{}
	err := withCause(ErrInvalidInput, "cannot encode state", cause)
	if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, cause) {
		t.Fatalf("classification or cause missing: %v", err)
	}
	if got, ok := errors.AsType[*diagnosticCause](err); !ok || got != cause {
		t.Fatalf("typed cause missing: %v", err)
	}
	if err.Error() != "jev: invalid input: cannot encode state" || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("unsafe or incomplete message: %v", err)
	}
}

func TestResponseErrorRetainsSafeContextAndCause(t *testing.T) {
	cause := &diagnosticCause{}
	inner := fmt.Errorf("question %q: %w", "urgent", withCause(ErrInvalidResponse, "invalid noul", cause))
	err := responseError("request-123", inner)
	response, ok := errors.AsType[*ResponseError](err)
	if !ok || response.RequestID != "request-123" || response.Unwrap() != inner || !errors.Is(err, cause) || !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("response diagnostics missing: %v", err)
	}
	if !strings.Contains(err.Error(), `question "urgent"`) || !strings.Contains(err.Error(), "request-123") || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("unsafe or incomplete response message: %v", err)
	}
	if responseError("request-123", nil) != nil {
		t.Fatal("successful response produced an error")
	}
	if err := responseError("", inner); err.Error() != inner.Error() {
		t.Fatalf("empty request ID changed context: %v", err)
	}
	if err := new(ResponseError); err.Error() != ErrInvalidResponse.Error() || err.Unwrap() != nil {
		t.Fatalf("zero response error is unusable: %v", err)
	}
}
