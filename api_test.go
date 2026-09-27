package jev

import (
	"context"
	"errors"
	"testing"
)

// Go 1.27 infers a generic method's type arguments from a function assignment.
// This lets applications pass a typed bound method to workers without adapters.
func TestClientAskFunctionValue(t *testing.T) {
	var client *Client
	//nolint:staticcheck // ST1023: The target type is required to infer the generic method's type arguments.
	var ask func(context.Context, any, Question[Probability]) (Probability, error) = client.Ask
	answer, err := ask(t.Context(), "state", Noul("Is this urgent?"))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil client error = %v", err)
	}
	if _, available := answer.Value(); available {
		t.Fatal("failed method call produced an available answer")
	}
}
