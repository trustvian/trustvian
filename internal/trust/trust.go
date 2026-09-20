// Package trust combines an Anomaly result, an actor's IdentityConfidence,
// and a deterministic ContextRisk penalty into a single TrustScore and a
// qualitative RiskLevel — the inputs to policy evaluation.
//
// Trust is never collapsed into an opaque number: every component that
// fed into it is retained on the result, so a Decision can always be
// traced back to why trust was low.
package trust

import (
	"fmt"
	"math"

	"github.com/trustvian/trustvian/internal/anomaly"
)

// Config holds the RiskLevel bucket thresholds, so they stay documented
// and tunable rather than hardcoded.
type Config struct {
	// MediumThreshold, HighThreshold, and CriticalThreshold are ascending
	// cutoffs on (1 - TrustScore): the residual distrust. A residual at
	// or above CriticalThreshold is RiskCritical, at or above
	// HighThreshold (but below CriticalThreshold) is RiskHigh, and so on
	// down to RiskLow.
	MediumThreshold   float64
	HighThreshold     float64
	CriticalThreshold float64
}

// DefaultConfig returns reasonable MVP thresholds: residual distrust
// below 0.25 is Low, [0.25,0.5) is Medium, [0.5,0.75) is High, and 0.75+
// is Critical.
func DefaultConfig() Config {
	return Config{
		MediumThreshold:   0.25,
		HighThreshold:     0.5,
		CriticalThreshold: 0.75,
	}
}

// RiskLevel is a qualitative bucket derived from TrustScore, used by
// policy evaluation.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

var riskRank = map[RiskLevel]int{
	RiskLow:      0,
	RiskMedium:   1,
	RiskHigh:     2,
	RiskCritical: 3,
}

// AtLeast reports whether r is at least as severe as min. Both must be
// one of the four recognized RiskLevel constants; an unrecognized value
// ranks below RiskLow.
func (r RiskLevel) AtLeast(min RiskLevel) bool {
	return riskRank[r] >= riskRank[min]
}

// Trust is the result of combining an Anomaly with identity and context
// signals. Score and Risk are derived; the other fields are the inputs
// that produced them, retained for explainability.
type Trust struct {
	Score float64
	Risk  RiskLevel

	IdentityConfidence float64
	AnomalyScore       float64
	AnomalyConfidence  float64
	ContextRisk        float64
}

// Compute derives Trust from an anomaly.Anomaly, identityConfidence
// (sourced from upstream auth, not computed by Trustvian), and
// contextRisk (a deterministic penalty for operation sensitivity or
// environment, supplied by the caller — not learned from the baseline).
// identityConfidence and contextRisk are expected in [0,1]; out-of-range
// values are clamped defensively rather than producing an out-of-range or
// NaN score. Every field of the returned Trust is therefore finite and in
// [0,1] for any input whatsoever — which is what lets a Result be
// serialized without a caller's arithmetic mistake becoming an encoding
// failure downstream.
//
// A non-finite input is treated as invalid rather than as a value on the
// scale, and resolved to whichever end trusts least: identityConfidence to
// 0, contextRisk and the anomaly inputs to 1. Notably contextRisk, which a
// WithContextRisk callback supplies and nothing else validates — an
// unusable risk reading must never read as "no risk".
//
// The combination is multiplicative, not averaged:
//
//	effectiveAnomaly = an.Score * an.Confidence
//	TrustScore        = IdentityConfidence * (1 - effectiveAnomaly) * (1 - ContextRisk)
//
// Trust is capped by its weakest factor: a single severe, confidently-
// measured anomaly or a high-risk context can drive TrustScore near zero
// even when the other inputs are high — the same way a chain is only as
// strong as its weakest link. This is a deliberate departure from an
// averaged formula, which would let two good signals dilute one bad one.
//
// The anomaly score is scaled by its own Confidence before entering the
// formula. This is what makes cold start safe: internal/anomaly
// deliberately does not suppress Score for a never-before-seen
// Fingerprint (Score can be near 1), because collapsing "how different is
// this" and "how much should you trust that reading" into one number
// would destroy information a security decision needs. Here is where
// the two are recombined — a maximally novel but zero-confidence anomaly
// contributes effectiveAnomaly=0, so TrustScore falls back to Identity
// and ContextRisk alone rather than a false low-trust reading driven
// entirely by having no history yet.
//
// Note: an earlier illustrative example in the project spec (Identity
// 0.95, Anomaly 0.91, Context Risk 0.63 -> Trust 0.31) does not fall out
// of this or any other principled combination of those numbers (this
// formula, at full confidence, yields ~0.03 for that input); it appears
// to be a mockup figure chosen for narrative effect rather than a
// computed test fixture, and reverse-fitting a formula to match it would
// itself be the "arbitrary scoring formula" CLAUDE.md says to avoid.
// Tests here verify this formula against fixtures derived from the
// formula itself.
func Compute(an anomaly.Anomaly, identityConfidence, contextRisk float64, cfg Config) Trust {
	identityConfidence = clampFavorable(identityConfidence)
	contextRisk = clampAdverse(contextRisk)
	anomalyScore := clampAdverse(an.Score)
	confidence := clampAdverse(an.Confidence)

	effectiveAnomaly := anomalyScore * confidence
	score := identityConfidence * (1 - effectiveAnomaly) * (1 - contextRisk)

	return Trust{
		Score:              score,
		Risk:               riskLevel(1-score, cfg),
		IdentityConfidence: identityConfidence,
		AnomalyScore:       anomalyScore,
		AnomalyConfidence:  confidence,
		ContextRisk:        contextRisk,
	}
}

// Explain renders t as a short, human-readable sentence summarizing its
// score, risk bucket, and the components that produced it. It is pure
// formatting over t's existing fields — no new computation, no I/O, and
// deterministic for a given Trust value.
func (t Trust) Explain() string {
	confidence := "full confidence"
	if t.AnomalyConfidence < 1 {
		confidence = fmt.Sprintf("%.0f%% confidence", t.AnomalyConfidence*100)
	}
	return fmt.Sprintf(
		"trust %.2f (%s): identity confidence %.2f, anomaly %.2f at %s, context risk %.2f",
		t.Score, t.Risk, t.IdentityConfidence, t.AnomalyScore, confidence, t.ContextRisk,
	)
}

func riskLevel(residualDistrust float64, cfg Config) RiskLevel {
	switch {
	case residualDistrust >= cfg.CriticalThreshold:
		return RiskCritical
	case residualDistrust >= cfg.HighThreshold:
		return RiskHigh
	case residualDistrust >= cfg.MediumThreshold:
		return RiskMedium
	default:
		return RiskLow
	}
}

func clamp01(v float64) float64 {
	return max(min(v, 1), 0)
}

// clampFavorable and clampAdverse sanitize one Compute input each, and
// differ only in what they do with a non-finite value. Both fail closed;
// "closed" simply points in opposite directions depending on which way the
// input moves trust.
//
// Go's min/max propagate NaN, so clamp01 alone returns NaN for a NaN input —
// which would flow into Trust.Score and out through every consumer of it,
// including a DecisionRecord that encoding/json then refuses to marshal.
// ±Inf clamps correctly by comparison, but -Inf clamping to 0 is its own
// problem for an adverse input: it reads an unusable value as "no risk".
//
// So non-finite is treated as invalid input rather than as a position on the
// scale, and resolved to whichever end of that scale trusts least.
//
// clampFavorable is for inputs where higher means more trust —
// identityConfidence. A non-finite value becomes 0: an unusable reading must
// not be credited as confidence nobody established.
func clampFavorable(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return clamp01(v)
}

// clampAdverse is for inputs where higher means less trust — contextRisk, and
// the anomaly score and confidence that combine into the anomaly penalty. A
// non-finite value becomes 1: an unusable risk reading must never be read as
// safe, which is the one outcome that would turn a caller's bug into a
// silently permissive decision.
func clampAdverse(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 1
	}
	return clamp01(v)
}
