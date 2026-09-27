package jev

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type testTeam string

const (
	testBilling   testTeam = "billing"
	testSupport   testTeam = "support"
	choiceFixture          = `{"type":"choice","choice":"billing","confidence":0.7,"probabilities":{"billing":0.8,"support":0.2}}`
	scoreFixture           = `{"type":"score","score":1.7,"confidence":0.9,"legend":{"0":"Can wait","1":"This week","2":"Today"},"probabilities":{"0":0.1,"1":0.1,"2":0.8}}`
)

func testChoiceQuestion() Question[Decision[testTeam]] {
	return Choice("Which team?", Opt(testBilling, "Payments"), Opt(testSupport, "Technical help"))
}

func decodeFixture[A any](t *testing.T, q Question[A], raw string) A {
	t.Helper()
	if q.err != nil {
		t.Fatal(q.err)
	}
	answer, err := q.decode(jsontext.Value(raw))
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

func changedAnswer(t *testing.T, raw, field, value string) string {
	t.Helper()
	var fields map[string]jsontext.Value
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	if value == "" {
		delete(fields, field)
	} else {
		fields[field] = jsontext.Value(value)
	}
	// Malformed raw fields are deliberate inputs to the response validator.
	encoded, err := json.Marshal(fields, jsontext.AllowDuplicateNames(true))
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestQuestionRejectsInvalidConstruction(t *testing.T) {
	tooManyOptions := make([]Option[string], 256)
	for i := range tooManyOptions {
		tooManyOptions[i] = Opt(fmt.Sprint(i), nil)
	}
	tooManyLevels := make([]any, 11)
	for i := range tooManyLevels {
		tooManyLevels[i] = "level"
	}
	cases := map[string]error{
		"no options":                 Choice[string]("? ").err,
		"too many options":           Choice("?", tooManyOptions...).err,
		"duplicate option":           Choice("?", Opt("a", nil), Opt("a", "other")).err,
		"blank option":               Choice("?", Opt("  ", nil)).err,
		"numeric option description": Choice("?", Opt("a", 12)).err,
		"unencodable instruction":    Noul(make(chan int)).err,
		"null instruction":           Noul(nil).err,
		"numeric instruction":        Noul(42).err,
		"partial noul criteria":      Noul("?", map[bool]any{true: "yes"}).err,
		"null noul description":      Noul("?", map[bool]any{true: nil, false: "no"}).err,
		"multiple noul criteria":     Noul("?", map[bool]any{}, map[bool]any{}).err,
		"one score level":            Score("?", "only").err,
		"too many score levels":      Score("?", tooManyLevels...).err,
		"null score level":           Score("?", "low", nil).err,
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v; want ErrInvalidInput", err)
			}
		})
	}
	if q := Choice("?", Opt("only", nil)); q.err != nil {
		t.Fatalf("single choice: %v", q.err)
	}
	if q := Noul("?", map[bool]any{true: map[string]any{"meaning": "yes"}, false: "no"}); q.err != nil {
		t.Fatal(q.err)
	}
}

func TestQuestionSnapshotsMutableInputs(t *testing.T) {
	instructions := map[string]any{"task": []string{"Original question"}}
	description := jsontext.Value(`{"meaning":"payments"}`)
	options := []Option[testTeam]{Opt(testBilling, description), Opt(testSupport, nil)}
	q := Choice(instructions, options...)
	if q.err != nil {
		t.Fatal(q.err)
	}
	before := bytes.Clone(q.wire)
	instructions["task"].([]string)[0] = "Changed question"
	description[12] = 'X'
	options[0].Value = "other"
	if !bytes.Equal(q.wire, before) {
		t.Fatalf("question changed after caller mutation: %s", q.wire)
	}
	d := decodeFixture(t, q, choiceFixture)
	if got, ok := d.Value(); !ok || got != testBilling {
		t.Fatalf("snapshot lost original choice: %q, %v", got, ok)
	}
	levels := []any{map[string]any{"meaning": "low"}, "high"}
	score := Score("?", levels...)
	before = bytes.Clone(score.wire)
	levels[0].(map[string]any)["meaning"] = "changed"
	levels[1] = "changed"
	if !bytes.Equal(score.wire, before) {
		t.Fatalf("score rubric changed: %s", score.wire)
	}
}

func TestDecodeDocumentedAnswerShapes(t *testing.T) {
	d := decodeFixture(t, testChoiceQuestion(), choiceFixture)
	var value testTeam
	value, valid := d.Value()
	if !valid || value != testBilling || d.Confidence() != 0.7 {
		t.Fatalf("choice: %+v", d)
	}
	p := decodeFixture(t, Noul("Spam?"), `{"type":"noul","noul":0.98}`)
	if value, ok := p.Value(); !ok || value != 0.98 {
		t.Fatalf("noul: %v %v", value, ok)
	}
	r := decodeFixture(t, Score("Urgency?", "Can wait", "This week", "Today"), scoreFixture)
	if value, ok := r.Value(); !ok || value != 1.7 {
		t.Fatalf("score: %v %v", value, ok)
	}
	structured := `{"meaning":"Can wait","examples":["newsletter",{"note":null}]}`
	r = decodeFixture(t, Score("Urgency?", jsontext.Value(structured), "Today"),
		`{"type":"score","score":0.4,"confidence":0.7,"legend":{"0":`+structured+`,"1":"Today"},"probabilities":{"0":0.6,"1":0.4}}`)
	if string(r.Legend()[0]) != structured {
		t.Fatalf("structured legend was lost: %s", r.Legend()[0])
	}
}

func TestChoiceRejectsMalformedOrInconsistentAnswers(t *testing.T) {
	cases := map[string]string{
		"not object": "null", "array": "[]", "trailing JSON": choiceFixture + ` {}`,
		"duplicate answer key": `{"type":"choice","choice":"billing","choice":"support","confidence":0.7,"probabilities":{"billing":0.8,"support":0.2}}`,
	}
	for _, field := range []string{"type", "choice", "confidence", "probabilities"} {
		cases["missing "+field] = changedAnswer(t, choiceFixture, field, "")
		cases["null "+field] = changedAnswer(t, choiceFixture, field, "null")
	}
	for name, pair := range map[string][2]string{
		"wrong kind": {"type", `"noul"`}, "wrong type": {"choice", `42`},
		"unoffered choice": {"choice", `"other"`}, "not maximum": {"choice", `"support"`},
		"negative confidence": {"confidence", `-0.1`}, "excess confidence": {"confidence", `1.1`},
		"string confidence": {"confidence", `"0.7"`}, "overflow confidence": {"confidence", `1e999`},
		"missing probability":    {"probabilities", `{"billing":1}`},
		"wrong probability key":  {"probabilities", `{"billing":0.8,"other":0.2}`},
		"excess probability key": {"probabilities", `{"billing":0.8,"support":0.2,"other":0}`},
		"sum mismatch":           {"probabilities", `{"billing":0.8,"support":0.3}`},
		"out of range":           {"probabilities", `{"billing":1.1,"support":-0.1}`},
		"null probability":       {"probabilities", `{"billing":null,"support":1}`},
		"wrong probability type": {"probabilities", `{"billing":"0.8","support":0.2}`},
		"duplicate probability":  {"probabilities", `{"billing":0.8,"billing":0.7,"support":0.3}`},
	} {
		cases[name] = changedAnswer(t, choiceFixture, pair[0], pair[1])
	}
	q := testChoiceQuestion()
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := q.decode(jsontext.Value(raw))
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("got %v for %s", err, raw)
			}
			if _, ok := d.Resolve(0); ok {
				t.Fatal("invalid response produced usable decision")
			}
		})
	}
}

func TestNoulRejectsMissingNullAndInvalidProbabilities(t *testing.T) {
	q := Noul("Urgent?")
	for _, raw := range []string{`{}`, `{"type":"noul"}`, `{"type":"noul","noul":null}`, `{"type":"noul","noul":"0.8"}`, `{"type":"noul","noul":1.5}`, `{"type":"noul","noul":-0.1}`, `{"type":"noul","noul":1e999}`, `{"type":"noul","noul":0,"noul":1}`, `{"type":"choice","noul":0.8}`} {
		if _, err := q.decode(jsontext.Value(raw)); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
	for _, number := range []string{"0", "1"} {
		decodeFixture(t, q, `{"type":"noul","noul":`+number+`}`)
	}
}

func TestScoreRejectsMalformedOrInconsistentRubric(t *testing.T) {
	cases := map[string]string{}
	for _, field := range []string{"score", "confidence", "legend", "probabilities"} {
		cases["missing "+field] = changedAnswer(t, scoreFixture, field, "")
		cases["null "+field] = changedAnswer(t, scoreFixture, field, "null")
	}
	for name, pair := range map[string][2]string{
		"wrong score type": {"score", `"1.7"`}, "negative score": {"score", `-1`},
		"excess score": {"score", `3`}, "mean mismatch": {"score", `1.5`},
		"wrong legend type":      {"legend", `["Can wait","This week","Today"]`},
		"wrong legend key":       {"legend", `{"0":"Can wait","1":"This week","3":"Today"}`},
		"null legend entry":      {"legend", `{"0":"Can wait","1":null,"2":"Today"}`},
		"numeric legend entry":   {"legend", `{"0":"Can wait","1":12,"2":"Today"}`},
		"duplicate legend entry": {"legend", `{"0":"Can wait","0":"other","1":"This week","2":"Today"}`},
		"distribution sum":       {"probabilities", `{"0":0.1,"1":0.2,"2":0.8}`},
		"distribution keys":      {"probabilities", `{"0":0.1,"1":0.1,"3":0.8}`},
	} {
		cases[name] = changedAnswer(t, scoreFixture, pair[0], pair[1])
	}
	q := Score("Urgency?", "Can wait", "This week", "Today")
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := q.decode(jsontext.Value(raw))
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("got %v for %s", err, raw)
			}
			if _, ok := r.Resolve(0); ok {
				t.Fatal("invalid response produced usable rating")
			}
		})
	}
}

func TestQuestionRejectsAmbiguousJSONAndInvalidUTF8(t *testing.T) {
	for name, instructions := range map[string]any{
		"duplicate nested name":  jsontext.Value(`{"context":{"status":"open","status":"closed"}}`),
		"escaped duplicate name": jsontext.Value(`{"context":{"status":"open","st\u0061tus":"closed"}}`),
		"invalid text":           "question\xff",
		"invalid nested text":    map[string]any{"context": []string{"state\xff"}},
		"invalid raw text":       jsontext.Value("{\"context\":\"state\xff\"}"),
	} {
		t.Run(name, func(t *testing.T) {
			if q := Noul(instructions); !errors.Is(q.err, ErrInvalidInput) {
				t.Fatalf("got %v; want ErrInvalidInput", q.err)
			}
		})
	}
}

func TestQuestionPreservesNullContainersAndWireText(t *testing.T) {
	var nilMap map[string]any
	var nilSlice []string
	for name, value := range map[string]any{"nil map": nilMap, "nil slice": nilSlice} {
		t.Run(name, func(t *testing.T) {
			if q := Noul(value); !errors.Is(q.err, ErrInvalidInput) {
				t.Fatalf("nil instructions became a structured value: %v", q.err)
			}
			q := Choice("?", Opt("only", value))
			if q.err != nil {
				t.Fatal(q.err)
			}
			if !bytes.Contains(q.wire, []byte(`"criteria":{"only":null}`)) {
				t.Fatalf("nil description lost null: %s", q.wire)
			}
		})
	}
	q := Choice(map[string]any{"z": "<state> & answer", "a": nilMap},
		Opt("z", map[string]any{"z": nilSlice, "a": "<option> & text"}), Opt("a", nil))
	if q.err != nil {
		t.Fatal(q.err)
	}
	want := `{"type":"choice","instructions":{"a":null,"z":"<state> & answer"},"criteria":{"a":null,"z":{"a":"<option> & text","z":null}}}`
	if string(q.wire) != want {
		t.Fatalf("wire JSON = %s; want %s", q.wire, want)
	}
	if q := Noul("?"); q.err != nil || bytes.Contains(q.wire, []byte(`"criteria"`)) {
		t.Fatalf("absent criteria were not omitted: %s, %v", q.wire, q.err)
	}
}

func TestResponseRejectsNestedAmbiguityAndInvalidUTF8(t *testing.T) {
	q := testChoiceQuestion()
	for name, extra := range map[string]string{
		"nested duplicate":         `{"context":{"x":1,"x":2}}`,
		"escaped nested duplicate": `{"context":{"x":1,"\u0078":2}}`,
		"nested invalid text":      "{\"context\":[\"value\xff\"]}",
		"nested invalid name":      "{\"context\":{\"key\xff\":1}}",
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.TrimSuffix(choiceFixture, "}") + `,"extra":` + extra + `}`
			if _, err := q.decode(jsontext.Value(raw)); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("accepted invalid nested JSON: %v", err)
			}
		})
	}
	// Valid unknown content and nested nulls remain forward-compatible.
	decodeFixture(t, q, strings.TrimSuffix(choiceFixture, "}")+`,"extra":{"context":null}}`)
}

func TestQuestionPreservesStructuredDurations(t *testing.T) {
	q := Noul(map[string]any{"elapsed": time.Second, "nested": []time.Duration{-time.Millisecond}})
	if q.err != nil {
		t.Fatal(q.err)
	}
	want := `{"type":"noul","instructions":{"elapsed":1000000000,"nested":[-1000000]}}`
	if string(q.wire) != want {
		t.Fatalf("wire JSON = %s; want %s", q.wire, want)
	}
}

type questionFailingJSON struct{ cause error }

func (value questionFailingJSON) MarshalJSON() ([]byte, error) { return nil, value.cause }

func TestQuestionEncodingErrorsPreserveCauseWithoutContent(t *testing.T) {
	private := errors.New("private-instruction-secret")
	q := Noul(questionFailingJSON{cause: private})
	var semantic *json.SemanticError
	if !errors.Is(q.err, ErrInvalidInput) || !errors.Is(q.err, private) || !errors.As(q.err, &semantic) {
		t.Fatalf("encoding cause was lost: %v", q.err)
	}
	if strings.Contains(q.err.Error(), private.Error()) {
		t.Fatalf("encoding error exposed input: %v", q.err)
	}
	q = Noul(jsontext.Value(`{"private-instruction-secret":`))
	var syntax *jsontext.SyntacticError
	if !errors.Is(q.err, ErrInvalidInput) || !errors.As(q.err, &syntax) {
		t.Fatalf("JSON syntax cause was lost: %v", q.err)
	}
	if strings.Contains(q.err.Error(), "private-instruction-secret") {
		t.Fatalf("syntax error exposed input: %v", q.err)
	}
}

func TestAnswerDecodingErrorsPreserveNestedJSONCauses(t *testing.T) {
	const secret = "private-model-response"
	choice := testChoiceQuestion()
	score := Score("Urgency?", "Can wait", "This week", "Today")
	_, noulErr := Noul("Urgent?").decode(jsontext.Value(`{"type":"noul","noul":"` + secret + `"}`))
	_, probabilitiesErr := choice.decode(jsontext.Value(changedAnswer(t, choiceFixture, "probabilities", `["`+secret+`"]`)))
	_, legendErr := score.decode(jsontext.Value(changedAnswer(t, scoreFixture, "legend", `["`+secret+`"]`)))
	for name, err := range map[string]error{"noul field": noulErr, "probabilities object": probabilitiesErr, "legend object": legendErr} {
		t.Run(name, func(t *testing.T) {
			var semantic *json.SemanticError
			if !errors.Is(err, ErrInvalidResponse) || !errors.As(err, &semantic) {
				t.Fatalf("JSON semantic cause was lost: %v", err)
			}
			if errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), secret) {
				t.Fatalf("response error misclassified or exposed content: %v", err)
			}
		})
	}
	_, err := Noul("Urgent?").decode(jsontext.Value(`{"type":"noul","private-model-response":`))
	var syntax *jsontext.SyntacticError
	if !errors.Is(err, ErrInvalidResponse) || !errors.As(err, &syntax) || strings.Contains(err.Error(), secret) {
		t.Fatalf("JSON syntax cause lost or content exposed: %v", err)
	}
	_, err = score.decode(jsontext.Value(changedAnswer(t, scoreFixture, "legend", `{"0":42,"1":"This week","2":"Today"}`)))
	if !errors.Is(err, ErrInvalidResponse) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid server legend was classified as caller input: %v", err)
	}
}
