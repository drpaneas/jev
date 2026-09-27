package jev

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const clientValidResponse = `{"model":"jev-test","answers":{"q":{"type":"noul","noul":0.9}},"usage":{"input_tokens":12,"output_tokens":3}}`

var clientQuestions = map[string]jsontext.Value{"q": jsontext.Value(`{"type":"noul","instructions":"urgent?"}`)}

func clientForServer(t *testing.T, handler http.HandlerFunc, options ...ClientOption) *Client {
	t.Helper()
	server := httptest.NewTestServer(t, handler)
	httpClient := server.Client()
	options = append([]ClientOption{WithBaseURL(server.URL), WithHTTPClient(httpClient)}, options...)
	c, err := New("test-key", options...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeClientResponse(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write test response: %v", err)
	}
}

func TestClientWireContract(t *testing.T) {
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s, headers %v", r.Method, r.URL.Path, r.Header)
		}
		var wire struct {
			Model     string                    `json:"model"`
			State     map[string]string         `json:"state"`
			Questions map[string]jsontext.Value `json:"questions"`
		}
		if err := json.UnmarshalRead(r.Body, &wire); err != nil {
			t.Error(err)
		}
		if wire.Model != "custom-model" || wire.State["message"] != "hello" || string(wire.Questions["q"]) != string(clientQuestions["q"]) {
			t.Errorf("unexpected wire payload: %+v", wire)
		}
		w.Header().Set("X-Request-ID", "request-123")
		writeClientResponse(t, w, clientValidResponse)
	}, WithModel("custom-model"))
	answers, meta, err := c.evaluate(t.Context(), map[string]string{"message": "hello"}, clientQuestions)
	if err != nil || len(answers) != 1 || meta.Model != "jev-test" || meta.RequestID != "request-123" || meta.Usage != (Usage{12, 3}) {
		t.Fatalf("answers=%v metadata=%+v error=%v", answers, meta, err)
	}
}

func TestClientValidStates(t *testing.T) {
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { writeClientResponse(t, w, clientValidResponse) })
	for _, state := range []any{"", "text", []string{}, []any{"a", 1}, map[string]any{}, map[string]any{"elapsed": time.Second}, jsontext.Value(` {"x":1} `)} {
		if _, _, err := c.evaluate(t.Context(), state, clientQuestions); err != nil {
			t.Errorf("state %#v: %v", state, err)
		}
	}
}

func TestClientRejectsInvalidInputLocally(t *testing.T) {
	var calls atomic.Int32
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeClientResponse(t, w, clientValidResponse)
	})
	for _, state := range []any{nil, true, false, 42, 1.5, []string(nil), map[string]string(nil), jsontext.Value(`null`), jsontext.Value(`bad`), make(chan int)} {
		if _, _, err := c.evaluate(t.Context(), state, clientQuestions); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("state %#v: %v", state, err)
		}
	}
	if _, _, err := c.evaluate(t.Context(), "text", nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("no questions: %v", err)
	}
	//nolint:staticcheck // SA1012: Verify nil-context rejection before making a request.
	if _, _, err := c.evaluate(nil, "text", clientQuestions); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("nil context: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs made %d calls", calls.Load())
	}
	for _, option := range []ClientOption{nil, WithHTTPClient(nil), WithModel("  "), WithMaxRetries(-1), WithBaseURL("file:///tmp/a"), WithBaseURL("http://user:pass@example.test"), WithBaseURL("http://example.test?token=x"), WithBaseURL("://bad")} {
		if _, err := New("key", option); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("invalid option: %v", err)
		}
	}
	for _, key := range []string{"", "  ", "a\nb"} {
		if _, err := New(key); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("invalid API key accepted")
		}
	}
}

func TestClientResponseValidation(t *testing.T) {
	for name, body := range map[string]string{
		"malformed": `broken`, "null": `null`, "trailing": clientValidResponse + `{}`,
		"duplicate model":  `{"model":"x","model":"y","answers":{"q":{}},"usage":{"input_tokens":0,"output_tokens":0}}`,
		"duplicate answer": `{"model":"x","answers":{"q":{},"q":{}},"usage":{"input_tokens":0,"output_tokens":0}}`,
		"duplicate usage":  `{"model":"x","answers":{"q":{}},"usage":{"input_tokens":0,"input_tokens":1,"output_tokens":0}}`,
		"model absent":     `{"answers":{"q":{}},"usage":{"input_tokens":0,"output_tokens":0}}`,
		"answers empty":    `{"model":"x","answers":{},"usage":{"input_tokens":0,"output_tokens":0}}`,
		"answers array":    `{"model":"x","answers":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"usage absent":     `{"model":"x","answers":{"q":{}}}`,
		"input absent":     `{"model":"x","answers":{"q":{}},"usage":{"output_tokens":0}}`,
		"output null":      `{"model":"x","answers":{"q":{}},"usage":{"input_tokens":0,"output_tokens":null}}`,
		"negative":         `{"model":"x","answers":{"q":{}},"usage":{"input_tokens":-1,"output_tokens":0}}`,
		"fractional":       `{"model":"x","answers":{"q":{}},"usage":{"input_tokens":0,"output_tokens":1.5}}`,
		"oversize":         strings.Repeat(" ", responseLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { writeClientResponse(t, w, body) })
			if _, _, err := c.evaluate(t.Context(), "text", clientQuestions); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, meta, err := decodeEnvelope([]byte(`{"model":"x","answers":{"q":{}},"usage":{"input_tokens":0,"output_tokens":0}}`), ""); err != nil || meta.Usage != (Usage{}) {
		t.Fatalf("zero usage must be accepted: %v", err)
	}
}

func TestClientRetriesExplicitResponses(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504, 529, 400, 401, 403, 404, 408} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			var firstBody atomic.Value
			c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if calls.Add(1) == 1 {
					firstBody.Store(string(body))
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					return
				}
				if string(body) != firstBody.Load() {
					t.Error("retry changed request body")
				}
				writeClientResponse(t, w, clientValidResponse)
			})
			_, _, err := c.evaluate(t.Context(), "text", clientQuestions)
			if retryable(status) {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("calls=%d err=%v", calls.Load(), err)
				}
			} else {
				if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != status || calls.Load() != 1 {
					t.Fatalf("calls=%d err=%v", calls.Load(), err)
				}
			}
		})
	}
}

func TestClientRetryLimitAndAPIError(t *testing.T) {
	for _, retries := range []int{0, 2} {
		var calls atomic.Int32
		options := []ClientOption{}
		if retries == 0 {
			options = append(options, WithMaxRetries(0))
		}
		c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "0")
			w.Header().Set("X-Request-ID", "failed-123")
			w.WriteHeader(503)
			// The client deliberately stops reading at its response limit.
			_, _ = io.WriteString(w, strings.Repeat("private error body ", errorLimit))
		}, options...)
		_, _, err := c.evaluate(t.Context(), "text", clientQuestions)
		if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 503 || apiErr.RequestID != "failed-123" || len(apiErr.Body) != errorLimit || strings.Contains(err.Error(), "private") || int(calls.Load()) != retries+1 {
			t.Fatalf("calls=%d error=%v", calls.Load(), err)
		}
	}
}

type clientRoundTripper func(*http.Request) (*http.Response, error)

func (f clientRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientTransportErrorNotRetriedAndTotalTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		problem := errors.New("transport failed")
		start := time.Now()
		h := &http.Client{Transport: clientRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			deadline, ok := r.Context().Deadline()
			if !ok || deadline != start.Add(30*time.Second) {
				t.Errorf("deadline = %v, present = %v; want total 30-second budget", deadline, ok)
			}
			return nil, problem
		})}
		c, err := New("key", WithHTTPClient(h))
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = c.evaluate(t.Context(), "text", clientQuestions)
		if !errors.Is(err, problem) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestClientCancellation(t *testing.T) {
	started := make(chan struct{})
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read test request: %v", err)
		}
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := c.evaluate(ctx, "text", clientQuestions); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestClientCancellationDuringRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(429)
		})
		ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
		defer cancel()
		start := time.Now()
		_, _, err := c.evaluate(ctx, "text", clientQuestions)
		if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 || time.Since(start) != 7*time.Second {
			t.Fatalf("calls=%d elapsed=%v err=%v", calls.Load(), time.Since(start), err)
		}
	})
}

func TestClientWaitsUntilRetryIsDue(t *testing.T) {
	for _, tc := range []struct {
		name, retryAfter string
		delay            time.Duration
	}{
		{"seconds", "2", 2 * time.Second},
		{"HTTP date", "date", 2 * time.Second},
		{"exponential backoff", "", 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				start := time.Now()
				c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 {
						header := tc.retryAfter
						if header == "date" {
							header = start.Add(tc.delay).UTC().Format(http.TimeFormat)
						}
						if header != "" {
							w.Header().Set("Retry-After", header)
						}
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					writeClientResponse(t, w, clientValidResponse)
				})
				done := make(chan error, 1)
				go func() {
					_, _, err := c.evaluate(t.Context(), "text", clientQuestions)
					done <- err
				}()
				synctest.Wait()
				if calls.Load() != 1 {
					t.Fatalf("initial calls=%d", calls.Load())
				}
				synctest.Sleep(tc.delay - time.Nanosecond)
				if calls.Load() != 1 {
					t.Fatal("retried before the requested delay")
				}
				synctest.Sleep(time.Nanosecond)
				if err := <-done; err != nil || calls.Load() != 2 || time.Since(start) != tc.delay {
					t.Fatalf("calls=%d elapsed=%v err=%v", calls.Load(), time.Since(start), err)
				}
			})
		})
	}
}

func TestClientBudgetCoversAllAttemptsAndBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusServiceUnavailable)
		}, WithMaxRetries(10))
		start := time.Now()
		_, _, err := c.evaluate(t.Context(), "text", clientQuestions)
		if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 2 || time.Since(start) != 30*time.Second {
			t.Fatalf("calls=%d elapsed=%v err=%v; want two attempts in total 30 seconds", calls.Load(), time.Since(start), err)
		}
	})
}

func TestRetryAfterParsingAndBackoff(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"2", 2 * time.Second, true}, {"0", 0, true}, {"-1", 0, false}, {"garbage", 0, false}, {"", 0, false},
		{now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
	} {
		got, valid := retryAfter(tc.value, now)
		if got != tc.want || valid != tc.valid {
			t.Errorf("%q: got %s/%v want %s/%v", tc.value, got, valid, tc.want, tc.valid)
		}
	}
	if retryDelay(0) != 100*time.Millisecond || retryDelay(1) != 200*time.Millisecond || retryDelay(100) != 2*time.Second {
		t.Error("invalid exponential backoff")
	}
}

func TestClientDisablesRedirectsWithoutMutatingCaller(t *testing.T) {
	var targetCalls atomic.Int32
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The in-memory transport routes every hostname to this handler. A
		// followed redirect therefore reaches this path and remains observable.
		if r.URL.Path == "/redirect-target" {
			targetCalls.Add(1)
			return
		}
		http.Redirect(w, r, "https://redirected.example.test/redirect-target", http.StatusTemporaryRedirect)
	}))
	customPolicy := errors.New("original redirect policy")
	original := server.Client()
	original.Timeout = 7 * time.Second
	original.CheckRedirect = func(*http.Request, []*http.Request) error { return customPolicy }
	c, err := New("test-key", WithHTTPClient(original))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.evaluate(t.Context(), "text", clientQuestions)
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 307 || targetCalls.Load() != 0 {
		t.Fatalf("redirect reached target or wrong error: %v", err)
	}
	if c.httpClient == original || original.Timeout != 7*time.Second || original.CheckRedirect(nil, nil) != customPolicy {
		t.Fatal("caller HTTP client was mutated")
	}
	original.CheckRedirect = nil
	original.Timeout = 0
	if c.httpClient.Timeout != 7*time.Second || c.httpClient.CheckRedirect == nil {
		t.Fatal("later caller mutation changed client")
	}
}

func TestClientMalformedSuccessRetainsRequestID(t *testing.T) {
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "invalid-response-123")
		writeClientResponse(t, w, `{"model":`)
	})
	_, err := c.Ask(t.Context(), "state", Noul("?"))
	if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "invalid-response-123") {
		t.Fatalf("response failure lost request ID: %v", err)
	}
	if response, ok := errors.AsType[*ResponseError](err); !ok || response.RequestID != "invalid-response-123" {
		t.Fatalf("missing typed response error: %v", err)
	}
}

func TestClientErrorBodyCapacityIsBounded(t *testing.T) {
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// The client deliberately stops reading at its response limit.
		_, _ = io.WriteString(w, strings.Repeat("a", errorLimit)+"discarded")
	})
	_, err := c.Ask(t.Context(), "state", Noul("?"))
	api, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("want APIError: %v", err)
	}
	if len(api.Body) != errorLimit || cap(api.Body) != len(api.Body) {
		t.Fatalf("body len=%d cap=%d", len(api.Body), cap(api.Body))
	}
}

func TestClientInputFailuresPreserveCauses(t *testing.T) {
	c, err := New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Ask(t.Context(), make(chan int), Noul("?"))
	if _, ok := errors.AsType[*json.SemanticError](err); !ok || !errors.Is(err, ErrInvalidInput) {
		t.Errorf("state encode cause is missing: %v", err)
	}
	_, err = New("test-key", WithBaseURL("https://private.example/%secret"))
	if _, ok := errors.AsType[*url.Error](err); !ok || !errors.Is(err, ErrInvalidInput) {
		t.Errorf("URL parse cause is missing: %v", err)
	}
	if strings.Contains(err.Error(), "private.example") || strings.Contains(err.Error(), "secret") {
		t.Errorf("URL input leaked in error: %v", err)
	}
	_, _, err = c.evaluate(t.Context(), "state", map[string]jsontext.Value{"q": jsontext.Value(`{"private-question":]}`)})
	if _, ok := errors.AsType[*jsontext.SyntacticError](err); !ok || !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), "private-question") {
		t.Errorf("question encoding lost cause or leaked input: %v", err)
	}
	// Exercise the request builder independently of constructor URL validation.
	c.baseURL = "https://private.example/%secret"
	_, _, err = c.evaluate(t.Context(), "state", clientQuestions)
	if _, ok := errors.AsType[*url.Error](err); !ok || !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), "secret") {
		t.Errorf("request construction lost cause or leaked URL: %v", err)
	}
}

type clientFailureBody struct {
	problem error
	closed  bool
	cancel  context.CancelFunc
}

func (b *clientFailureBody) Read([]byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return 0, b.problem
}

func (b *clientFailureBody) Close() error {
	b.closed = true
	return nil
}

func TestClientResponseFailuresPreserveDiagnostics(t *testing.T) {
	readFailure := errors.New("private-read-content")
	for name, body := range map[string]io.ReadCloser{
		"syntax":   io.NopCloser(strings.NewReader(`{"model":"private-input",]}`)),
		"semantic": io.NopCloser(strings.NewReader(`{"model":["private-input"],"answers":{"q":{}},"usage":{"input_tokens":0,"output_tokens":0}}`)),
		"oversize": io.NopCloser(strings.NewReader(strings.Repeat("x", responseLimit+1))),
		"read":     &clientFailureBody{problem: readFailure},
	} {
		t.Run(name, func(t *testing.T) {
			var calls int
			c, err := New("test-key", WithHTTPClient(&http.Client{Transport: clientRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": {"diagnostic-123"}}, Body: body}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Ask(t.Context(), "state", Noul("?"))
			response, ok := errors.AsType[*ResponseError](err)
			if !ok || response.RequestID != "diagnostic-123" || !errors.Is(err, ErrInvalidResponse) || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatalf("response details leaked: %v", err)
			}
			switch name {
			case "syntax":
				if _, ok := errors.AsType[*jsontext.SyntacticError](err); !ok {
					t.Fatalf("syntax cause missing: %v", err)
				}
			case "semantic":
				if _, ok := errors.AsType[*json.SemanticError](err); !ok {
					t.Fatalf("semantic cause missing: %v", err)
				}
			case "read":
				if !errors.Is(err, readFailure) || !body.(*clientFailureBody).closed {
					t.Fatalf("read cause missing or body not closed: %v", err)
				}
			}
		})
	}
}

func TestClientCancellationWhileReadingRemainsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := &clientFailureBody{problem: errors.New("read stopped"), cancel: cancel}
	c, err := New("test-key", WithHTTPClient(&http.Client{Transport: clientRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": {"cancelled-123"}}, Body: body}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Ask(ctx, "state", Noul("?"))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrInvalidResponse) || !body.closed {
		t.Fatalf("cancellation misclassified or body not closed: %v", err)
	}
}

func TestWaitRetryPreservesCancellationWhenTimerIsReady(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 100 {
		if err := waitRetry(ctx, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("ready timer hid cancellation: %v", err)
		}
	}
}
