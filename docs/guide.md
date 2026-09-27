# Guide

[Back to README](../README.md)

## Question and result types

| Constructor | Result | Meaning |
| --- | --- | --- |
| `Choice(instructions, Opt(value, description), ...)` | `Decision[T]` | An offered option, its distribution and Jev's confidence |
| `Noul(instructions)` | `Probability` | Probability of **yes** |
| `Score(instructions, level0, level1, ...)` | `Rating` | Probability-weighted zero-based rubric position, distribution and confidence |

Choice supports string and named string types (including ordinary Go string
enums), 1–255 distinct options. Score supports 2–10 levels. Instructions and
descriptions can be strings or JSON-serializable objects/arrays; Choice
descriptions can also be nil. Optional Noul criteria use
`jev.Noul(question, map[bool]any{true: "...", false: "..."})`.

Questions snapshot inputs when constructed and can be reused concurrently.
Constructor validation errors are returned by `Client.Ask` or `Client.Evaluate`, before
sending a request. A variable declaration does not call the model.

Results have private fields. `Value()` returns `(value, available)` without a
policy decision. Their **zero values are unavailable**, including at threshold
zero. Distribution accessors return copies. Results can be read concurrently.

`Decision.Resolve(minConfidence)` and `Rating.Resolve(minConfidence)` apply
Jev's confidence statistic. `Probability.Resolve(minProbability)` requires a
threshold in `(0.5,1]` and distinguishes a strong yes `(true,true)`, a strong no
`(false,true)`, and uncertainty `(false,false)`. Invalid/NaN thresholds are never
accepted. `Resolve` never calls the network.

**Confidence is not a guarantee of correctness.** It is supplied by Jev and
derived from the returned distribution. This package does not recompute it,
invent it for Noul, or label it a probability of correctness. Thresholds in
examples are illustrative and need evaluation against your own data.

## Batch without losing types

Using `client` and `question` from the [README](../README.md):

```go
var team jev.Decision[Team]
var urgent jev.Probability

meta, err := client.Evaluate(context.Background(), "I was charged twice.",
	jev.Into("team", question, &team),
	jev.Into("urgent", jev.Noul("Is this time-sensitive?"), &urgent),
)
if err != nil {
	log.Fatal(err)
}
fmt.Println(meta.Model, meta.Usage.InputTokens)
```

The questions are sent together in one HTTP request per attempt. Types are
checked when each `Into` binding is constructed. Every answer is decoded and
validated before **any** destination is written. On error, destinations retain
their previous values; handle the error rather than using a stale decision.
Destination pointers must be non-nil and distinct, and must not be used
concurrently. Clients and questions themselves are reusable concurrently.

## HTTP behavior

`New` requires an explicit API key; it does not implicitly read environment
variables. Defaults are `https://api.typesafe.ai`, `jev-latest`, a 30-second total
request budget, and up to two retries for transient HTTP responses. Use
`WithModel`, `WithBaseURL`, `WithHTTPClient` and `WithMaxRetries` to configure it.
Use `WithMaxRetries(0)` to disable automatic retries.

- A shorter `context` deadline wins and cancellation interrupts retry waits.
- `Retry-After` seconds or dates are honored; retries never outrun the context.
- Ambiguous transport failures are not retried automatically, since the first
  request may already have been processed and billed. HTTP retries themselves
  are not an exactly-once execution guarantee.
- Response sizes are bounded. Redirects are rejected, and an injected HTTP
  client's fields are never changed by the package.
- Required fields, answer IDs and kinds, selected-option membership, finite
  probability/confidence ranges, distribution keys/sums, and Score's weighted
  value are validated. Unknown additional fields are allowed for forward
  compatibility. Structured Score legends are preserved as `jsontext.Value`.
- JSON uses `encoding/json/v2`: invalid UTF-8 and duplicate object names are
  rejected, including inside nested data. Outbound map ordering is deterministic.
  Nil maps/slices encode as null and are rejected as top-level state or required
  descriptions. Structured `time.Duration` values encode as numeric nanoseconds.
  HTML characters in text are not escaped.

Use `errors.Is(err, jev.ErrInvalidInput)` for local validation and
`errors.Is(err, jev.ErrInvalidResponse)` for malformed API responses.
`errors.AsType[*jev.APIError](err)` provides the HTTP status and request ID for
HTTP failures. `errors.AsType[*jev.ResponseError](err)` provides the request ID
and wrapped `Err` for invalid API responses, including errors returned by
`Client.Ask`. Request IDs are empty when the server does not supply one.

`ResponseError` unwraps to `Err`, preserving `ErrInvalidResponse` and any
available JSON parser cause for `errors.Is` and `errors.AsType`.
Its `Error()` text omits raw cause details; `APIError.Error()` omits response
bodies. Inspect `ResponseError.Err` or `APIError.Body` deliberately when needed.
Standard context cancellation errors remain discoverable through `errors.Is`.

When an envelope is valid but an answer fails validation, `Client.Evaluate`
may return its metadata alongside the error. Destinations remain unchanged.
Use the error to decide whether answers are usable; metadata alone does not
indicate success.

## Typed API

The generic method `client.Ask(ctx, state, question)` uses Go 1.27's method
type parameters: the result type is inferred from the question. Prefer the
method; `jev.Ask(ctx, client, state, question)` is also available as a convenience
wrapper. Generics preserve an application's string enum through request and
response without code generation or reflection-based binding.
Go type safety keeps domains distinct; it does not make Go switches exhaustive
or force callers to check errors/confidence.

## Go 1.27.1

The module's `go 1.27.1` directive sets the minimum toolchain version. It does
not prevent a newer Go toolchain from building the package. To use exactly the
version validated here, set `GOTOOLCHAIN=go1.27.1` for your build or test command.

The package uses [Go 1.27](https://go.dev/doc/go1.27) generic methods and JSON v2.
Standard-library cloning helpers keep result copies independent.
Client construction uses `new(value)`, introduced in
[Go 1.26](https://go.dev/doc/go1.26#language), to allocate a pointer to a copy.
In `new(*client)`, that is a shallow copy of the HTTP client: its transport remains
shared, as documented by `WithHTTPClient`.
HTTP tests use the new in-memory `httptest.NewTestServer`, and retry/deadline
tests use `testing/synctest` to check elapsed virtual time without wall-clock
delays. No experimental Go features or `GOEXPERIMENT` flags are required.

## Run

```sh
GOTOOLCHAIN=go1.27.1 go test -race ./...
GOTOOLCHAIN=go1.27.1 go vet ./...
GOTOOLCHAIN=go1.27.1 go run ./examples/triage
```

Tests use in-memory HTTP fixtures, require **no key or listening ports**, and make no requests to
TypeSafe. The example program requires `TYPESAFE_API_KEY` in its environment and
makes one live evaluation. Its HTTP attempts, including retries, may be billed.

The implementation has been tested against the documented HTTP contract using
local fixtures. A successful live call and application-specific model accuracy
are separate checks; neither is claimed by the offline suite.

## Sources

- [Official HTTP contract](https://docs.typesafe.ai/api)
- [Official confidence semantics](https://docs.typesafe.ai/confidence)
- [Official SDK list](https://docs.typesafe.ai/sdk) — Python and JavaScript/TypeScript

This is an independent implementation, unaffiliated with TypeSafe.

## License

MIT. See [LICENSE](../LICENSE).
