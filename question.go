package jev

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Question is an immutable question whose answer has type A. Use Choice, Noul,
// or Score to construct one. Input is snapshotted when constructed. Invalid
// questions are reported by Client.Ask/Client.Evaluate before any network request.
type Question[A any] struct {
	wire   jsontext.Value
	decode func(jsontext.Value) (A, error)
	err    error
}

// Option pairs a string-backed Go value with a description.
type Option[T ~string] struct {
	// Value identifies the option. It must be nonblank valid UTF-8 and unique
	// among the options passed to Choice.
	Value T
	// Description may be text, a JSON object/array, or nil when Value is sufficient.
	Description any
}

// Opt creates an option, inferring its Go type from value.
func Opt[T ~string](value T, description any) Option[T] {
	return Option[T]{Value: value, Description: description}
}

// Choice asks Jev to choose among 1–255 distinct options. A named string type
// preserves domain types in the resulting Decision[T]. Instructions accept
// text or a JSON object/array; each option may also have a nil description.
func Choice[T ~string](instructions any, options ...Option[T]) Question[Decision[T]] {
	q := Question[Decision[T]]{}
	if len(options) < 1 || len(options) > 255 {
		q.err = fmt.Errorf("%w: choice needs 1–255 options", ErrInvalidInput)
		return q
	}
	criteria := make(map[string]jsontext.Value, len(options))
	for _, option := range options {
		key := string(option.Value)
		if strings.TrimSpace(key) == "" || !utf8.ValidString(key) {
			q.err = fmt.Errorf("%w: empty or invalid UTF-8 choice option", ErrInvalidInput)
			return q
		}
		if _, exists := criteria[key]; exists {
			q.err = fmt.Errorf("%w: duplicate choice option %q", ErrInvalidInput, key)
			return q
		}
		raw, err := textJSON(option.Description, true)
		if err != nil {
			q.err = err
			return q
		}
		criteria[key] = raw
	}
	q.wire, q.err = questionJSON("choice", instructions, criteria)
	q.decode = func(raw jsontext.Value) (Decision[T], error) {
		var result Decision[T]
		fields, err := answerFields(raw, "choice")
		if err != nil {
			return result, err
		}
		var selected string
		if err := required(fields, "choice", &selected); err != nil {
			return result, err
		}
		if _, ok := criteria[selected]; !ok {
			return result, badResponse("choice is not an offered option")
		}
		confidence, err := confidenceField(fields)
		if err != nil {
			return result, err
		}
		probabilities, err := distribution(fields["probabilities"], criteria)
		if err != nil {
			return result, err
		}
		for _, p := range probabilities {
			if p > probabilities[selected]+distributionTolerance {
				return result, badResponse("selected choice is not a highest-probability option")
			}
		}
		typed := make(map[T]float64, len(probabilities))
		for key, p := range probabilities {
			typed[T(key)] = p
		}
		return Decision[T]{value: T(selected), confidence: confidence, probabilities: typed, valid: true}, nil
	}
	return q
}

// Noul asks a yes/no question and returns P(yes), without inventing a confidence
// statistic. Optional criteria explicitly describe true and false outcomes;
// if provided, both keys are required and no other keys are accepted.
func Noul(instructions any, criteria ...map[bool]any) Question[Probability] {
	q := Question[Probability]{}
	if len(criteria) > 1 {
		q.err = fmt.Errorf("%w: supply at most one noul criteria map", ErrInvalidInput)
		return q
	}
	var wireCriteria any
	if len(criteria) == 1 {
		if len(criteria[0]) != 2 {
			q.err = fmt.Errorf("%w: noul criteria need true and false descriptions", ErrInvalidInput)
			return q
		}
		out := make(map[string]jsontext.Value, 2)
		for value, desc := range criteria[0] {
			raw, err := textJSON(desc, false)
			if err != nil {
				q.err = err
				return q
			}
			out[strconv.FormatBool(value)] = raw
		}
		wireCriteria = out
	}
	q.wire, q.err = questionJSON("noul", instructions, wireCriteria)
	q.decode = func(raw jsontext.Value) (Probability, error) {
		fields, err := answerFields(raw, "noul")
		if err != nil {
			return Probability{}, err
		}
		var p float64
		if err := required(fields, "noul", &p); err != nil {
			return Probability{}, err
		}
		if !unit(p) {
			return Probability{}, badResponse("noul is outside [0,1]")
		}
		return Probability{value: p, valid: true}, nil
	}
	return q
}

// Score rates input against 2–10 ordered levels. Each level accepts text or a
// JSON object/array. The returned Rating may lie between integer level indices.
func Score(instructions any, levels ...any) Question[Rating] {
	q := Question[Rating]{}
	if len(levels) < 2 || len(levels) > 10 {
		q.err = fmt.Errorf("%w: score needs 2–10 levels", ErrInvalidInput)
		return q
	}
	criteria := make([]jsontext.Value, len(levels))
	keys := make(map[string]jsontext.Value, len(levels))
	for i, level := range levels {
		raw, err := textJSON(level, false)
		if err != nil {
			q.err = err
			return q
		}
		criteria[i] = raw
		keys[strconv.Itoa(i)] = raw
	}
	q.wire, q.err = questionJSON("score", instructions, criteria)
	q.decode = func(raw jsontext.Value) (Rating, error) {
		fields, err := answerFields(raw, "score")
		if err != nil {
			return Rating{}, err
		}
		var value float64
		if err := required(fields, "score", &value); err != nil {
			return Rating{}, err
		}
		confidence, err := confidenceField(fields)
		if err != nil {
			return Rating{}, err
		}
		probabilities, err := distribution(fields["probabilities"], keys)
		if err != nil {
			return Rating{}, err
		}
		legend, err := object(fields["legend"])
		if err != nil {
			return Rating{}, withCause(ErrInvalidResponse, "invalid score legend", err)
		}
		if len(legend) != len(keys) {
			return Rating{}, badResponse("score legend must contain every rubric level")
		}
		result := Rating{value: value, confidence: confidence, valid: true,
			probabilities: make([]float64, len(keys)), legend: make([]jsontext.Value, len(keys))}
		var mean float64
		for i := range criteria {
			key := strconv.Itoa(i)
			level, err := marshalJSON(legend[key])
			if err != nil {
				return Rating{}, withCause(ErrInvalidResponse, "invalid score legend level", err)
			}
			if !isTextKind(jsontext.Value(level).Kind()) {
				return Rating{}, badResponse("invalid or missing score legend level")
			}
			result.legend[i] = level
			result.probabilities[i] = probabilities[key]
			mean += float64(i) * probabilities[key]
		}
		if math.IsNaN(value) || value < 0 || value > float64(len(keys)-1) || math.Abs(value-mean) > distributionTolerance*float64(len(keys)) {
			return Rating{}, badResponse("score disagrees with its probability-weighted levels")
		}
		return result, nil
	}
	return q
}

func questionJSON(kind string, instructions, criteria any) (jsontext.Value, error) {
	instruction, err := textJSON(instructions, false)
	if err != nil {
		return nil, err
	}
	raw, err := marshalJSON(struct {
		Type         string         `json:"type"`
		Instructions jsontext.Value `json:"instructions"`
		Criteria     any            `json:"criteria,omitzero"`
	}{kind, instruction, criteria})
	if err != nil {
		return nil, withCause(ErrInvalidInput, "cannot encode question", err)
	}
	return raw, nil
}

// marshalJSON keeps map ordering deterministic and nil containers as null.
// The latter preserves the distinction between missing content and an empty
// object or array, including inside structured descriptions and state. Durations
// retain their existing representation as a number of nanoseconds.
func marshalJSON(value any) ([]byte, error) {
	return json.Marshal(value, json.Deterministic(true),
		json.FormatNilMapAsNull(true), json.FormatNilSliceAsNull(true),
		jsonv1.FormatDurationAsNano(true))
}

func textJSON(value any, allowNull bool) (jsontext.Value, error) {
	raw, err := marshalJSON(value)
	if err != nil {
		return nil, withCause(ErrInvalidInput, "cannot encode text or structured description", err)
	}
	kind := jsontext.Value(raw).Kind()
	if isTextKind(kind) || (allowNull && kind == 'n') {
		return raw, nil
	}
	return nil, fmt.Errorf("%w: expected text, object or array", ErrInvalidInput)
}

// isTextKind identifies text and structured content accepted for state and
// descriptions. Whether null is allowed is decided by the caller.
func isTextKind(kind jsontext.Kind) bool {
	return kind == '"' || kind == '{' || kind == '['
}

func answerFields(raw jsontext.Value, kind string) (map[string]jsontext.Value, error) {
	fields, err := object(raw)
	if err != nil {
		return nil, err
	}
	var actual string
	if err := required(fields, "type", &actual); err != nil {
		return nil, err
	}
	if actual != kind {
		return nil, badResponse("answer type does not match question")
	}
	return fields, nil
}

func required(fields map[string]jsontext.Value, key string, out any) error {
	raw, ok := fields[key]
	if !ok || raw.Kind() == 'n' {
		return badResponse("missing or null " + key)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return withCause(ErrInvalidResponse, "invalid "+key, err)
	}
	return nil
}

func confidenceField(fields map[string]jsontext.Value) (float64, error) {
	var confidence float64
	if err := required(fields, "confidence", &confidence); err != nil {
		return 0, err
	}
	if !unit(confidence) {
		return 0, badResponse("confidence is outside [0,1]")
	}
	return confidence, nil
}

const distributionTolerance = 1e-6

func distribution(raw jsontext.Value, expected map[string]jsontext.Value) (map[string]float64, error) {
	fields, err := object(raw)
	if err != nil {
		return nil, withCause(ErrInvalidResponse, "invalid probabilities", err)
	}
	if len(fields) != len(expected) {
		return nil, badResponse("probabilities must contain exactly the offered options or levels")
	}
	result := make(map[string]float64, len(fields))
	var sum float64
	for key := range expected {
		var p float64
		if err := required(fields, key, &p); err != nil {
			return nil, err
		}
		if !unit(p) {
			return nil, badResponse("probability is outside [0,1]")
		}
		result[key] = p
		sum += p
	}
	if math.Abs(sum-1) > distributionTolerance {
		return nil, badResponse("probabilities do not sum to 1")
	}
	return result, nil
}

// object uses v2's strict defaults to reject duplicate names and invalid UTF-8,
// including within nested values, and still rejects null as a missing object.
func object(raw jsontext.Value) (map[string]jsontext.Value, error) {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, withCause(ErrInvalidResponse, "invalid JSON object", err)
	}
	if fields == nil {
		return nil, badResponse("expected a JSON object")
	}
	return fields, nil
}

func badResponse(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidResponse, message)
}
