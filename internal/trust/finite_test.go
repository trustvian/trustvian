package trust_test

// Compute's documented contract says out-of-range input is clamped "rather
// than producing an out-of-range or NaN score". The implementation did not
// honour that for NaN: Go's min/max propagate it, so clamp01(NaN) returned
// NaN, and it flowed into Trust.Score.
//
// That mattered beyond arithmetic. A DecisionRecord built from such a Trust
// cannot be marshalled — encoding/json refuses non-finite floats — so a
// successful Analyze could produce a result the public boundary could not
// serialize. These tests pin the contract that closes it.

import (
	"math"
	"testing"

	"github.com/trustvian/trustvian/internal/anomaly"
	"github.com/trustvian/trustvian/internal/trust"
)

var nonFinite = map[string]float64{
	"NaN":  math.NaN(),
	"+Inf": math.Inf(1),
	"-Inf": math.Inf(-1),
}

func finiteIn01(t *testing.T, name string, v float64) {
	t.Helper()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		t.Errorf("%s = %v, want a finite value", name, v)
		return
	}
	if v < 0 || v > 1 {
		t.Errorf("%s = %v, want a value in [0,1]", name, v)
	}
}

func assertUsable(t *testing.T, tr trust.Trust) {
	t.Helper()
	finiteIn01(t, "Score", tr.Score)
	finiteIn01(t, "IdentityConfidence", tr.IdentityConfidence)
	finiteIn01(t, "AnomalyScore", tr.AnomalyScore)
	finiteIn01(t, "AnomalyConfidence", tr.AnomalyConfidence)
	finiteIn01(t, "ContextRisk", tr.ContextRisk)
	if tr.Risk == "" {
		t.Error("Risk is empty")
	}
}

// TestNonFiniteContextRiskIsMaximallyAdverse is the security half of the
// contract. contextRisk is the one Compute input nothing validates upstream —
// it comes straight from a WithContextRisk callback — so an unusable reading
// must resolve to maximum risk. Resolving it to 0 would turn a caller's
// arithmetic bug into a silently permissive decision.
func TestNonFiniteContextRiskIsMaximallyAdverse(t *testing.T) {
	for name, v := range nonFinite {
		t.Run(name, func(t *testing.T) {
			got := trust.Compute(anomaly.Anomaly{}, 1, v, trust.DefaultConfig())

			assertUsable(t, got)
			if got.ContextRisk != 1 {
				t.Fatalf("ContextRisk = %v for %s input, want 1 (maximum risk)", got.ContextRisk, name)
			}
			// Maximum context risk drives trust to zero through the
			// multiplicative formula, which is the point.
			if got.Score != 0 {
				t.Fatalf("Score = %v, want 0 when context risk is maximal", got.Score)
			}
		})
	}
}

// TestNonFiniteIdentityConfidenceIsMaximallyUntrusting is the other
// direction. Higher identity confidence means more trust, so an unusable
// reading must resolve to 0 — crediting confidence nobody established would
// be the permissive failure here.
func TestNonFiniteIdentityConfidenceIsMaximallyUntrusting(t *testing.T) {
	for name, v := range nonFinite {
		t.Run(name, func(t *testing.T) {
			got := trust.Compute(anomaly.Anomaly{}, v, 0, trust.DefaultConfig())

			assertUsable(t, got)
			if got.IdentityConfidence != 0 {
				t.Fatalf("IdentityConfidence = %v for %s input, want 0", got.IdentityConfidence, name)
			}
			if got.Score != 0 {
				t.Fatalf("Score = %v, want 0 with no identity confidence", got.Score)
			}
		})
	}
}

// TestNonFiniteAnomalyInputsStayUsable covers the remaining two inputs. They
// are engine-computed rather than caller-supplied, so this is defence in
// depth: Compute's guarantee is unconditional, not contingent on its caller.
func TestNonFiniteAnomalyInputsStayUsable(t *testing.T) {
	for name, v := range nonFinite {
		t.Run(name, func(t *testing.T) {
			assertUsable(t, trust.Compute(anomaly.Anomaly{Score: v, Confidence: 1}, 1, 0, trust.DefaultConfig()))
			assertUsable(t, trust.Compute(anomaly.Anomaly{Score: 1, Confidence: v}, 1, 0, trust.DefaultConfig()))
		})
	}
}

// TestFiniteOutOfRangeStillClamps: the existing documented behavior for
// ordinary out-of-range numbers is unchanged. Only non-finite input is
// treated as invalid rather than as a position on the scale.
func TestFiniteOutOfRangeStillClamps(t *testing.T) {
	tests := []struct {
		name                            string
		identityConfidence, contextRisk float64
		wantIdentity, wantRisk          float64
	}{
		{"above one", 5, 5, 1, 1},
		{"below zero", -5, -5, 0, 0},
		{"in range", 0.75, 0.25, 0.75, 0.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trust.Compute(anomaly.Anomaly{}, tt.identityConfidence, tt.contextRisk, trust.DefaultConfig())
			if got.IdentityConfidence != tt.wantIdentity {
				t.Errorf("IdentityConfidence = %v, want %v", got.IdentityConfidence, tt.wantIdentity)
			}
			if got.ContextRisk != tt.wantRisk {
				t.Errorf("ContextRisk = %v, want %v", got.ContextRisk, tt.wantRisk)
			}
		})
	}
}

// TestComputeIsDeterministicForNonFinite: sanitizing must not introduce any
// input-dependent wobble. The same pathological input yields the same Trust.
func TestComputeIsDeterministicForNonFinite(t *testing.T) {
	for name, v := range nonFinite {
		t.Run(name, func(t *testing.T) {
			first := trust.Compute(anomaly.Anomaly{Score: v, Confidence: v}, v, v, trust.DefaultConfig())
			second := trust.Compute(anomaly.Anomaly{Score: v, Confidence: v}, v, v, trust.DefaultConfig())
			if first != second {
				t.Fatalf("Compute is not deterministic: %+v vs %+v", first, second)
			}
		})
	}
}
