package jev

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEvaluateMixedBatchContract(t *testing.T) {
	type team string
	const billing team = "billing"
	var calls atomic.Int32
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			State     map[string]string `json:"state"`
			Questions map[string]struct {
				Type         string         `json:"type"`
				Instructions string         `json:"instructions"`
				Criteria     jsontext.Value `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.UnmarshalRead(r.Body, &request); err != nil {
			t.Error(err)
		}
		if len(request.Questions) != 3 || request.State["text"] != "<invoice> & payment" {
			t.Errorf("incorrect request: %+v", request)
		}
		if request.Questions["team"].Type != "choice" || request.Questions["team"].Instructions != "Team?" || string(request.Questions["team"].Criteria) != `{"billing":"Charges","technical":null}` {
			t.Error("incorrect choice wire shape")
		}
		if request.Questions["urgent"].Type != "noul" || len(request.Questions["urgent"].Criteria) != 0 {
			t.Error("incorrect noul wire shape")
		}
		if request.Questions["impact"].Type != "score" || string(request.Questions["impact"].Criteria) != `["Low","Medium","High"]` {
			t.Error("incorrect score wire shape")
		}
		w.Header().Set("X-Request-ID", "batch-id")
		writeClientResponse(t, w, `{"model":"jev-1.13.0","usage":{"input_tokens":123,"output_tokens":30},"answers":{
			"team":{"type":"choice","choice":"billing","probabilities":{"billing":0.97,"technical":0.03},"confidence":0.93},
			"urgent":{"type":"noul","noul":0.02},
			"impact":{"type":"score","score":1.2,"probabilities":{"0":0,"1":0.8,"2":0.2},"confidence":0.6,"legend":{"0":"Low","1":"Medium","2":"High"}}
		}}`)
	}))
	httpClient := server.Client()
	client, err := New("test-key", WithBaseURL(server.URL), WithHTTPClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	var department Decision[team]
	var urgent Probability
	var impact Rating
	meta, err := client.Evaluate(t.Context(), map[string]string{"text": "<invoice> & payment"},
		Into("team", Choice("Team?", Opt(billing, "Charges"), Opt(team("technical"), nil)), &department),
		Into("urgent", Noul("Urgent?"), &urgent),
		Into("impact", Score("Impact?", "Low", "Medium", "High"), &impact),
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || meta.RequestID != "batch-id" || meta.Usage.InputTokens != 123 {
		t.Fatalf("calls=%d metadata=%+v", calls.Load(), meta)
	}
	if value, ok := department.Resolve(.9); !ok || value != billing {
		t.Fatalf("department=%v/%v", value, ok)
	}
	if value, ok := urgent.Resolve(.9); !ok || value {
		t.Fatalf("urgent=%v/%v", value, ok)
	}
	if value, ok := impact.Value(); !ok || value != 1.2 {
		t.Fatalf("impact=%v/%v", value, ok)
	}
}

func TestEvaluateNeverPartiallyWritesDestinations(t *testing.T) {
	var first, second Probability
	first = Probability{value: .8, valid: true}
	second = Probability{value: .7, valid: true}
	for name, answers := range map[string]string{
		"missing":        `{"first":{"type":"noul","noul":0.1}}`,
		"different ID":   `{"first":{"type":"noul","noul":0.1},"unexpected":{"type":"noul","noul":0.9}}`,
		"invalid second": `{"first":{"type":"noul","noul":0.1},"second":{"type":"noul","noul":2}}`,
		"JSON type":      `{"first":{"type":"noul","noul":0.1},"second":{"type":"noul","noul":"private-batch-content"}}`,
		"wrong kind":     `{"first":{"type":"noul","noul":0.1},"second":{"type":"choice","choice":"yes"}}`,
		"extra":          `{"first":{"type":"noul","noul":0.1},"second":{"type":"noul","noul":0.9},"extra":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-ID", "answer-validation-id")
				writeClientResponse(t, w, `{"model":"jev-test","usage":{"input_tokens":1,"output_tokens":1},"answers":`+answers+`}`)
			})
			meta, err := c.Evaluate(t.Context(), "state", Into("first", Noul("One?"), &first), Into("second", Noul("Two?"), &second))
			var responseErr *ResponseError
			if !errors.Is(err, ErrInvalidResponse) || !errors.As(err, &responseErr) || responseErr.RequestID != "answer-validation-id" {
				t.Fatalf("error=%v", err)
			}
			if meta.RequestID != responseErr.RequestID || meta.Model != "jev-test" || meta.Usage != (Usage{InputTokens: 1, OutputTokens: 1}) {
				t.Fatalf("valid envelope metadata lost on answer failure: %+v", meta)
			}
			if name == "JSON type" {
				var semantic *json.SemanticError
				if !errors.As(err, &semantic) || strings.Contains(err.Error(), "private-batch-content") {
					t.Fatalf("batch JSON cause lost or response content exposed: %v", err)
				}
			}
			if first.value != .8 || second.value != .7 {
				t.Fatal("failed batch modified destinations")
			}
		})
	}
}

func TestEvaluatePreflight(t *testing.T) {
	var calls atomic.Int32
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	var p, other Probability
	for name, bindings := range map[string][]Binding{
		"none":                  nil,
		"zero":                  {{}},
		"nil destination":       {Into("q", Noul("Q?"), nil)},
		"zero question":         {Into("q", Question[Probability]{}, &p)},
		"empty name":            {Into(" ", Noul("Q?"), &p)},
		"invalid UTF-8":         {Into(string([]byte{0xff}), Noul("Q?"), &p)},
		"duplicate name":        {Into("q", Noul("Q?"), &p), Into("q", Noul("Q?"), &other)},
		"duplicate destination": {Into("q", Noul("Q?"), &p), Into("other", Noul("Q?"), &p)},
		"bad instructions":      {Into("q", Noul(123), &p)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Evaluate(t.Context(), "state", bindings...); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	var nilClient *Client
	for _, client := range []*Client{nilClient, &Client{}} {
		if _, err := client.Ask(t.Context(), "state", Noul("Q?")); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error=%v", err)
		}
	}
	//nolint:staticcheck // SA1012: Verify nil-context rejection before making a request.
	if _, err := c.Ask(nil, "state", Noul("Q?")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs made %d calls", calls.Load())
	}
}

func TestConcurrentClientAndQuestionReuse(t *testing.T) {
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeClientResponse(t, w, `{"model":"jev-test","usage":{"input_tokens":1,"output_tokens":1},"answers":{"answer":{"type":"choice","choice":"yes","probabilities":{"yes":0.95,"no":0.05},"confidence":0.9}}}`)
	})
	q := Choice("Does this fit?", Opt("yes", nil), Opt("no", nil))
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			d, err := c.Ask(t.Context(), "state", q)
			if err != nil {
				t.Error(err)
				return
			}
			if value, ok := d.Resolve(.9); !ok || value != "yes" {
				t.Errorf("result=%v/%v", value, ok)
			}
			d.Probabilities()["yes"] = 0 // Mutating a returned copy must not race.
		})
	}
	wg.Wait()
}

func TestEvaluateConstructorErrorNamesQuestionAndPreservesCause(t *testing.T) {
	var calls atomic.Int32
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	private := errors.New("private-constructor-content")
	var result Probability
	_, err := c.Evaluate(t.Context(), "state", Into("urgency", Noul(questionFailingJSON{cause: private}), &result))
	var semantic *json.SemanticError
	if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, private) || !errors.As(err, &semantic) {
		t.Fatalf("constructor diagnostics lost: %v", err)
	}
	if !strings.Contains(err.Error(), `question "urgency"`) || strings.Contains(err.Error(), private.Error()) {
		t.Fatalf("question context absent or private input exposed: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("constructor error made %d requests", calls.Load())
	}
}

func TestAskResponseErrorRetainsRequestIDAndJSONCause(t *testing.T) {
	c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "single-answer-id")
		writeClientResponse(t, w, `{"model":"jev-test","usage":{"input_tokens":1,"output_tokens":1},"answers":{"answer":{"type":"noul","noul":"private-response-content"}}}`)
	})
	answer, err := c.Ask(t.Context(), "state", Noul("Urgent?"))
	var responseErr *ResponseError
	var semantic *json.SemanticError
	if !errors.Is(err, ErrInvalidResponse) || !errors.As(err, &responseErr) || !errors.As(err, &semantic) {
		t.Fatalf("answer diagnostics lost: %v", err)
	}
	if responseErr.RequestID != "single-answer-id" || !strings.Contains(err.Error(), `question "answer"`) || strings.Contains(err.Error(), "private-response-content") {
		t.Fatalf("request/question context lost or content exposed: %v", err)
	}
	if _, ok := answer.Value(); ok {
		t.Fatal("invalid response returned a usable answer")
	}
}
