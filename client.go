package jev

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrInvalidInput identifies a request rejected before reaching the API.
	ErrInvalidInput = errors.New("jev: invalid input")
	// ErrInvalidResponse identifies a successful HTTP response with invalid data.
	ErrInvalidResponse = errors.New("jev: invalid response")
)

const (
	defaultBaseURL    = "https://api.typesafe.ai"
	defaultModel      = "jev-latest"
	defaultMaxRetries = 2

	responseLimit  = 4 << 20
	errorLimit     = 64 << 10
	requestTimeout = 30 * time.Second
)

// Client evaluates questions. Its configuration is fixed at construction.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	maxRetries int
}

// ClientOption configures a Client before it is constructed.
type ClientOption func(*clientConfig) error

type clientConfig Client

// WithHTTPClient uses a copy of client, with redirects disabled. Its transport
// is shared; configure that transport before constructing the Jev client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *clientConfig) error {
		if client == nil {
			return fmt.Errorf("%w: nil HTTP client", ErrInvalidInput)
		}
		c.httpClient = new(*client)
		return nil
	}
}

// WithBaseURL selects an API origin or proxy prefix, without query or fragment.
func WithBaseURL(baseURL string) ClientOption {
	return func(c *clientConfig) error {
		u, err := url.Parse(baseURL)
		if err != nil {
			return withCause(ErrInvalidInput, "invalid base URL", err)
		}
		if u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("%w: invalid base URL", ErrInvalidInput)
		}
		c.baseURL = strings.TrimRight(baseURL, "/")
		return nil
	}
}

// WithModel selects a model; the default is jev-latest.
func WithModel(model string) ClientOption {
	return func(c *clientConfig) error {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("%w: empty model", ErrInvalidInput)
		}
		c.model = model
		return nil
	}
}

// WithMaxRetries sets retries after explicit transient HTTP failures. The
// default is two. Zero disables retries; transport errors are never retried.
func WithMaxRetries(n int) ClientOption {
	return func(c *clientConfig) error {
		if n < 0 {
			return fmt.Errorf("%w: negative retry count", ErrInvalidInput)
		}
		c.maxRetries = n
		return nil
	}
}

// New creates a reusable client. Each evaluation has a total 30-second timeout,
// including retries, or the caller's deadline if it is earlier.
// By default it retries 429, 500, 502, 503, 504, and 529 responses up to twice.
// A retry repeats the evaluation and may incur another charge; use
// WithMaxRetries(0) to disable these status retries.
func New(apiKey string, options ...ClientOption) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, fmt.Errorf("%w: missing or invalid API key", ErrInvalidInput)
	}
	c := &clientConfig{
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		model:      defaultModel,
		httpClient: &http.Client{},
		maxRetries: defaultMaxRetries,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: nil client option", ErrInvalidInput)
		}
		if err := option(c); err != nil {
			return nil, err
		}
	}
	c.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return new(Client(*c)), nil
}

// Usage records the token counts returned by the API.
type Usage struct {
	// InputTokens is the number of input tokens reported by the API.
	InputTokens int `json:"input_tokens"`
	// OutputTokens is the number of output tokens reported by the API.
	OutputTokens int `json:"output_tokens"`
}

// Metadata describes a valid API response envelope. Evaluate may return it
// alongside an error when an individual answer fails validation.
type Metadata struct {
	// Model is the model identifier returned by the API.
	Model string
	// Usage contains the token counts for the evaluation.
	Usage Usage
	// RequestID is the X-Request-ID response header, or empty if absent.
	RequestID string
}

// APIError describes an HTTP failure. Body contains at most 64 KiB and may
// contain sensitive data. Error intentionally excludes the response body.
type APIError struct {
	// StatusCode is the unsuccessful HTTP response status code.
	StatusCode int
	// RequestID is the X-Request-ID response header, or empty if absent.
	RequestID string
	// RetryAfter is the delay parsed from the server's Retry-After header.
	// Zero means the header was absent, invalid, zero, or a date in the past.
	// It does not include the client's fallback exponential backoff.
	RetryAfter time.Duration
	// Body contains at most the first 64 KiB of the response body and may
	// contain sensitive data. Error omits it.
	Body []byte
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("jev: API returned HTTP %d (request %s)", e.StatusCode, e.RequestID)
	}
	return fmt.Sprintf("jev: API returned HTTP %d", e.StatusCode)
}

func (c *Client) evaluate(ctx context.Context, state any, questions map[string]jsontext.Value) (map[string]jsontext.Value, Metadata, error) {
	if c == nil || c.httpClient == nil || ctx == nil {
		return nil, Metadata{}, fmt.Errorf("%w: nil client or context", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, Metadata{}, err
	}
	rawState, err := marshalJSON(state)
	if err != nil {
		return nil, Metadata{}, withCause(ErrInvalidInput, "cannot encode state", err)
	}
	if !isTextKind(jsontext.Value(rawState).Kind()) {
		return nil, Metadata{}, fmt.Errorf("%w: state must be a string, object, or array", ErrInvalidInput)
	}
	if len(questions) == 0 {
		return nil, Metadata{}, fmt.Errorf("%w: no questions", ErrInvalidInput)
	}
	payload, err := marshalJSON(struct {
		Model     string                    `json:"model"`
		State     jsontext.Value            `json:"state"`
		Questions map[string]jsontext.Value `json:"questions"`
	}{c.model, rawState, questions})
	if err != nil {
		return nil, Metadata{}, withCause(ErrInvalidInput, "cannot encode questions", err)
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(payload))
		if err != nil {
			return nil, Metadata{}, withCause(ErrInvalidInput, "cannot construct request", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, Metadata{}, fmt.Errorf("jev: request failed: %w", err)
		}
		limit := int64(responseLimit)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			limit = errorLimit
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()
		if err := ctx.Err(); err != nil {
			return nil, Metadata{}, err
		}
		requestID := resp.Header.Get("X-Request-ID")
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if readErr != nil {
				return nil, Metadata{}, responseError(requestID, withCause(ErrInvalidResponse, "cannot read response", readErr))
			}
			if int64(len(body)) > limit {
				return nil, Metadata{}, responseError(requestID, badResponse("response exceeds 4 MiB"))
			}
			answers, metadata, err := decodeEnvelope(body, requestID)
			return answers, metadata, responseError(requestID, err)
		}
		if int64(len(body)) > limit {
			body = body[:limit]
		}
		body = slices.Clip(body)
		delay, explicitDelay := retryAfter(resp.Header.Get("Retry-After"), time.Now())
		apiErr := &APIError{StatusCode: resp.StatusCode, RequestID: requestID, RetryAfter: delay, Body: body}
		if attempt >= c.maxRetries || !retryable(resp.StatusCode) {
			return nil, Metadata{}, apiErr
		}
		if !explicitDelay {
			delay = retryDelay(attempt)
		}
		if err := waitRetry(ctx, delay); err != nil {
			return nil, Metadata{}, err
		}
	}
}

func decodeEnvelope(body []byte, requestID string) (map[string]jsontext.Value, Metadata, error) {
	fields, err := object(body)
	if err != nil {
		return nil, Metadata{}, err
	}
	var model string
	if err := required(fields, "model", &model); err != nil {
		return nil, Metadata{}, err
	}
	answers, err := object(fields["answers"])
	if err != nil {
		return nil, Metadata{}, withCause(ErrInvalidResponse, "missing or invalid answers", err)
	}
	if len(answers) == 0 {
		return nil, Metadata{}, badResponse("missing or invalid answers")
	}
	usageFields, err := object(fields["usage"])
	if err != nil {
		return nil, Metadata{}, withCause(ErrInvalidResponse, "missing or invalid usage", err)
	}
	var usage Usage
	if err := required(usageFields, "input_tokens", &usage.InputTokens); err != nil {
		return nil, Metadata{}, err
	}
	if err := required(usageFields, "output_tokens", &usage.OutputTokens); err != nil {
		return nil, Metadata{}, err
	}
	if strings.TrimSpace(model) == "" || usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return nil, Metadata{}, badResponse("invalid model or usage")
	}
	return answers, Metadata{Model: model, RequestID: requestID, Usage: usage}, nil
}

func retryable(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504, 529:
		return true
	default:
		return false
	}
}

func retryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		const maxDuration = time.Duration(math.MaxInt64)
		if seconds > uint64(maxDuration/time.Second) {
			return maxDuration, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := max(date.Sub(now), 0)
		return delay, true
	}
	return 0, false
}

func retryDelay(attempt int) time.Duration {
	if attempt >= 5 {
		return 2 * time.Second
	}
	return 100 * time.Millisecond * time.Duration(1<<attempt)
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
