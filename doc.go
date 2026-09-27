// Package jev evaluates typed questions with TypeSafe AI's Jev model.
//
// Choice produces a Decision[T] for string-backed Go types, Noul produces a
// Probability, and Score produces a Rating. Client.Ask evaluates one question;
// Client.Evaluate and Into batch heterogeneous questions in one HTTP request.
//
// All inference is explicit and happens at run time. Declaring a variable or
// constructing a question never makes a network request. Questions and clients
// can be reused concurrently. Destination pointers passed to Into must not be
// shared between concurrent evaluations.
//
// APIError reports HTTP failures. ResponseError reports invalid API responses
// and preserves the server's request ID when available. Use errors.Is with
// ErrInvalidInput and ErrInvalidResponse to identify validation failures.
//
// This is an unofficial, Jev-specific package. Model probabilities and confidence
// are estimates, not proofs that a decision is correct.
package jev
