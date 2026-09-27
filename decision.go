package jev

import (
	"encoding/json/jsontext"
	"maps"
	"math"
	"slices"
)

// Decision is Jev's Choice answer with a string-backed Go type. Its zero value
// is unavailable, not a decision for T's zero value. Results are immutable.
type Decision[T ~string] struct {
	value         T
	confidence    float64
	probabilities map[T]float64
	valid         bool
}

// Value returns the selected option and whether an answer is available.
// It deliberately does not apply a confidence policy.
func (d Decision[T]) Value() (value T, ok bool) { return d.value, d.valid }

// Resolve returns the selected option only when an answer exists and Jev's
// confidence meets minConfidence. An invalid threshold (outside [0,1], NaN,
// or infinity) is never accepted. Confidence is not a correctness guarantee.
func (d Decision[T]) Resolve(minConfidence float64) (value T, ok bool) {
	if d.valid && unit(minConfidence) && d.confidence >= minConfidence {
		return d.value, true
	}
	var zero T
	return zero, false
}

// Confidence is Jev's distribution-derived statistic, not the probability
// that the selected option is correct. An unavailable answer returns zero.
func (d Decision[T]) Confidence() float64 { return d.confidence }

// Probability returns the probability assigned to an option. The boolean is
// false if the answer is unavailable or the option was not in the question.
func (d Decision[T]) Probability(option T) (probability float64, ok bool) {
	p, ok := d.probabilities[option]
	return p, ok && d.valid
}

// Probabilities returns an independent copy of the option distribution.
func (d Decision[T]) Probabilities() map[T]float64 {
	if !d.valid {
		return nil
	}
	return maps.Clone(d.probabilities)
}

// Probability is a Noul answer: the estimated probability of yes. Noul has
// no separate confidence field. The zero value is unavailable, not a strong no.
type Probability struct {
	value float64
	valid bool
}

// Value returns P(yes), and false when no answer is available.
func (p Probability) Value() (probability float64, ok bool) { return p.value, p.valid }

// Resolve makes a symmetric yes/no decision. It returns (yes, ok), where ok
// reports whether either outcome clears the threshold: (true,true) when
// P(yes) >= minProbability, (false,true) when P(no) >= minProbability, and
// (false,false) when neither clears the threshold. minProbability must be in
// (0.5,1]; invalid thresholds and unavailable answers always return false,false.
func (p Probability) Resolve(minProbability float64) (yes, ok bool) {
	if !p.valid || !unit(minProbability) || minProbability <= 0.5 {
		return false, false
	}
	if p.value >= minProbability {
		return true, true
	}
	if 1-p.value >= minProbability {
		return false, true
	}
	return false, false
}

// Rating is a Score answer. Its value is the probability-weighted mean of the
// zero-based rubric levels, not an exact measurement. The zero value is unavailable.
type Rating struct {
	value         float64
	confidence    float64
	probabilities []float64
	legend        []jsontext.Value
	valid         bool
}

// Value returns the score and whether an answer is available.
func (r Rating) Value() (score float64, ok bool) { return r.value, r.valid }

// Resolve returns the score only when confidence meets a finite [0,1] threshold.
func (r Rating) Resolve(minConfidence float64) (score float64, ok bool) {
	if r.valid && unit(minConfidence) && r.confidence >= minConfidence {
		return r.value, true
	}
	return 0, false
}

// Confidence returns Jev's confidence statistic, or zero if unavailable.
func (r Rating) Confidence() float64 { return r.confidence }

// Probabilities returns a copy of the probabilities in rubric order.
func (r Rating) Probabilities() []float64 {
	return slices.Clone(r.probabilities)
}

// Legend returns copies of the server's JSON level descriptions, in rubric
// order. Raw JSON preserves structured descriptions as well as plain strings.
// An unavailable result returns nil.
func (r Rating) Legend() []jsontext.Value {
	out := slices.Clone(r.legend)
	for i, level := range out {
		out[i] = level.Clone()
	}
	return out
}

func unit(n float64) bool { return !math.IsNaN(n) && n >= 0 && n <= 1 }
