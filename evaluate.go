package jev

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Binding associates a typed question with a destination. Create one with Into.
type Binding struct {
	name    string
	wire    jsontext.Value
	target  any
	prepare func(jsontext.Value) (func(), error)
	err     error
}

// Into binds a question to a non-nil destination. Evaluate updates destinations
// only after every answer has been validated. An error leaves all unchanged.
// Destinations must be distinct and must not be accessed concurrently.
func Into[A any](name string, question Question[A], destination *A) Binding {
	b := Binding{name: name, wire: question.wire, target: destination, err: question.err}
	if destination == nil {
		b.err = fmt.Errorf("%w: nil destination for %q", ErrInvalidInput, name)
	} else if question.decode == nil || len(question.wire) == 0 {
		if b.err == nil {
			b.err = fmt.Errorf("%w: uninitialized question %q", ErrInvalidInput, name)
		}
	}
	b.prepare = func(raw jsontext.Value) (func(), error) {
		value, err := question.decode(raw)
		if err != nil {
			return nil, err
		}
		return func() { *destination = value }, nil
	}
	return b
}

// Ask is a convenience wrapper for Client.Ask. Prefer calling the method when
// a client is available.
func Ask[A any](ctx context.Context, client *Client, state any, question Question[A]) (A, error) {
	return client.Ask(ctx, state, question)
}

// Ask evaluates one question and infers the answer type from it. Use Evaluate
// with Into for multiple questions in a single call and request metadata.
func (c *Client) Ask[A any](ctx context.Context, state any, question Question[A]) (A, error) {
	var answer A
	_, err := c.Evaluate(ctx, state, Into("answer", question, &answer))
	return answer, err
}

// Evaluate asks all bound questions about the same state in one Jev call.
// It returns model/usage metadata and never partially updates destinations.
// When the response envelope is valid but an answer fails validation, the
// returned metadata remains available and the error is a *ResponseError.
// Use a request context with a deadline appropriate for your application.
func (c *Client) Evaluate(ctx context.Context, state any, bindings ...Binding) (Metadata, error) {
	if c == nil || ctx == nil || len(bindings) == 0 {
		return Metadata{}, fmt.Errorf("%w: client, context and questions are required", ErrInvalidInput)
	}
	questions := make(map[string]jsontext.Value, len(bindings))
	targets := make(map[any]bool, len(bindings))
	for _, binding := range bindings {
		if binding.err != nil {
			return Metadata{}, fmt.Errorf("question %q: %w", binding.name, binding.err)
		}
		if strings.TrimSpace(binding.name) == "" || !utf8.ValidString(binding.name) || binding.prepare == nil || len(binding.wire) == 0 {
			return Metadata{}, fmt.Errorf("%w: uninitialized binding or empty question name", ErrInvalidInput)
		}
		if _, duplicate := questions[binding.name]; duplicate {
			return Metadata{}, fmt.Errorf("%w: duplicate question name %q", ErrInvalidInput, binding.name)
		}
		if targets[binding.target] {
			return Metadata{}, fmt.Errorf("%w: duplicate destination for %q", ErrInvalidInput, binding.name)
		}
		targets[binding.target] = true
		questions[binding.name] = binding.wire
	}
	answers, metadata, err := c.evaluate(ctx, state, questions)
	if err != nil {
		return metadata, err
	}
	if len(answers) != len(questions) {
		return metadata, responseError(metadata.RequestID, badResponse("answer IDs do not match question IDs"))
	}
	commits := make([]func(), 0, len(bindings))
	for _, binding := range bindings {
		raw, ok := answers[binding.name]
		if !ok {
			return metadata, responseError(metadata.RequestID, badResponse("missing answer for "+binding.name))
		}
		commit, err := binding.prepare(raw)
		if err != nil {
			return metadata, responseError(metadata.RequestID, fmt.Errorf("question %q: %w", binding.name, err))
		}
		commits = append(commits, commit)
	}
	for _, commit := range commits {
		commit()
	}
	return metadata, nil
}
