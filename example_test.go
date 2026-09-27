package jev_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/drpaneas/jev"
)

type exampleTransport func(*http.Request) (*http.Response, error)

func (f exampleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This executable example uses an in-memory fixture, not a live model prediction.
func ExampleClient_Ask() {
	type Team string
	const (
		Billing   Team = "billing"
		Technical Team = "technical"
	)
	httpClient := &http.Client{Transport: exampleTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"model":"jev-1.13.0","answers":{"answer":{"type":"choice","choice":"billing","confidence":0.93,"probabilities":{"billing":0.97,"technical":0.03}}},"usage":{"input_tokens":100,"output_tokens":10}}`)),
			Request:    r,
		}, nil
	})}
	client, err := jev.New("test-key", jev.WithHTTPClient(httpClient))
	if err != nil {
		panic(err)
	}
	question := jev.Choice("Which team should handle this ticket?",
		jev.Opt(Billing, "Charges, payments, invoices"),
		jev.Opt(Technical, "Software bugs and outages"),
	)
	answer, err := client.Ask(context.Background(), "I was charged twice.", question)
	if err != nil {
		panic(err)
	}
	team, ok := answer.Resolve(0.90)
	fmt.Println(team, ok)
	// Output: billing true
}
