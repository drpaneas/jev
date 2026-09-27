package jev

import (
	"encoding/json/jsontext"
	"math"
	"testing"
)

func TestUnavailableResultsNeverResolve(t *testing.T) {
	var d Decision[testTeam]
	var p Probability
	var r Rating
	for _, threshold := range []float64{0, 0.5, 0.9, 1, -1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, ok := d.Resolve(threshold); ok {
			t.Fatalf("zero decision resolved at %v", threshold)
		}
		if _, ok := p.Resolve(threshold); ok {
			t.Fatalf("zero probability resolved at %v", threshold)
		}
		if _, ok := r.Resolve(threshold); ok {
			t.Fatalf("zero rating resolved at %v", threshold)
		}
	}
	if _, ok := d.Value(); ok {
		t.Fatal("zero decision available")
	}
	if _, ok := p.Value(); ok {
		t.Fatal("zero probability available")
	}
	if _, ok := r.Value(); ok {
		t.Fatal("zero rating available")
	}
	if _, ok := d.Probability(testBilling); ok {
		t.Fatal("zero decision has probability")
	}
	if d.Probabilities() != nil || r.Probabilities() != nil || r.Legend() != nil {
		t.Fatal("unavailable results must return nil distributions and legend")
	}
}

func TestDecisionAndRatingThresholdPolicy(t *testing.T) {
	d := decodeFixture(t, testChoiceQuestion(), choiceFixture)
	r := decodeFixture(t, Score("Urgency?", "Can wait", "This week", "Today"), scoreFixture)
	for _, threshold := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, ok := d.Resolve(threshold); ok {
			t.Fatalf("decision accepted threshold %v", threshold)
		}
		if _, ok := r.Resolve(threshold); ok {
			t.Fatalf("rating accepted threshold %v", threshold)
		}
	}
	if value, ok := d.Resolve(0.7); !ok || value != testBilling {
		t.Fatalf("threshold equality: %v %v", value, ok)
	}
	if _, ok := d.Resolve(0.8); ok {
		t.Fatal("used selected probability (0.8) as confidence (0.7)")
	}
	if _, ok := d.Resolve(0); !ok {
		t.Fatal("zero threshold should accept an available choice")
	}
	if _, ok := r.Resolve(0.9); !ok {
		t.Fatal("rating did not accept confidence equality")
	}
	if _, ok := r.Resolve(0.95); ok {
		t.Fatal("rating accepted low confidence")
	}
}

func TestNoulResolvesYesNoAndUncertaintySymmetrically(t *testing.T) {
	for _, tc := range []struct {
		raw             string
		threshold       float64
		value, accepted bool
	}{
		{"0.875", 0.875, true, true}, {"0.125", 0.875, false, true},
		{"0.6", 0.875, false, false}, {"0.4", 0.875, false, false},
		{"0.5", 0.875, false, false}, {"1", 1, true, true}, {"0", 1, false, true},
	} {
		p := decodeFixture(t, Noul("Urgent?"), `{"type":"noul","noul":`+tc.raw+`}`)
		value, accepted := p.Resolve(tc.threshold)
		if value != tc.value || accepted != tc.accepted {
			t.Fatalf("P(yes)=%s: got %v,%v; want %v,%v", tc.raw, value, accepted, tc.value, tc.accepted)
		}
		for _, threshold := range []float64{0, 0.5, -1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
			if value, ok := p.Resolve(threshold); value || ok {
				t.Fatalf("accepted invalid threshold %v", threshold)
			}
		}
	}
}

func TestResultAccessorsReturnIndependentCopies(t *testing.T) {
	d := decodeFixture(t, testChoiceQuestion(), choiceFixture)
	probabilities := d.Probabilities()
	probabilities[testBilling] = 0
	delete(probabilities, testSupport)
	if p, ok := d.Probability(testBilling); !ok || p != 0.8 {
		t.Fatalf("caller mutated choice result: %v %v", p, ok)
	}
	if _, ok := d.Probability("unoffered"); ok {
		t.Fatal("unknown option reported present")
	}
	if len(d.Probabilities()) != 2 {
		t.Fatal("caller deleted an option from result")
	}
	r := decodeFixture(t, Score("Urgency?", "Can wait", "This week", "Today"), scoreFixture)
	scoreProbabilities, legend := r.Probabilities(), r.Legend()
	scoreProbabilities[0] = 1
	legend[0][1] = 'X'
	legend[1] = jsontext.Value(`"replaced"`)
	if r.Probabilities()[0] != 0.1 || string(r.Legend()[0]) != `"Can wait"` || string(r.Legend()[1]) != `"This week"` {
		t.Fatal("caller mutated score result")
	}
}
