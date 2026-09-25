package platform

// The historical-evidence contract, tested from inside the package because it
// is about values only this package can build.
//
// The rule these defend: a stored promotion carries what the platform relied
// on, not what today's build would compute. A restore that re-derived a Passed
// flag or a verdict would hand back a value that existed at no point in time.

import (
	"errors"
	"math"
	"testing"
	"time"
)

var restoreEpoch = time.Date(2026, 3, 1, 9, 14, 22, 481_000_321, time.UTC)

// storedGateRow is a plausible stored gate result, as scalars.
func storedGateRow() promotionRow {
	return promotionRow{
		id: "promo-1", projectID: "proj-1",
		candidateID: "cand-can", referenceCandidateID: "cand-ref",
		referenceRunID: "run-ref", candidateRunID: "run-can",

		sourceRef: "staging", sourceRank: 30, sourceRevision: "7",
		targetRef: "production", targetRank: 40, targetRevision: "3",

		maxAdded: "3", maxBlock: "0", maxCritical: "0",

		refEvidenceActual: "412", refEvidenceMinimum: "1", refEvidencePassed: 1,
		candEvidenceActual: "388", candEvidenceMin: "1", candEvidencePassed: 1,
		addedActual: "5", addedPassed: 0,
		blockActual: "0", blockPassed: 1,
		criticalActual: "0", criticalPassed: 1,

		verdict: "fail", outcome: "rejected",
		decidedAt: restoreEpoch.Format(time.RFC3339Nano),
	}
}

func TestRestorePromotionRowRoundTripsEveryGateField(t *testing.T) {
	promotion, err := restorePromotionRow(storedGateRow())
	if err != nil {
		t.Fatalf("restorePromotionRow() error = %v", err)
	}

	gate := promotion.GateResult()
	checks := []struct {
		name    string
		actual  uint64
		bound   uint64
		passed  bool
		wantAct uint64
		wantBnd uint64
		wantPas bool
	}{
		{"reference evidence", gate.ReferenceEvidence().Actual,
			gate.ReferenceEvidence().Minimum, gate.ReferenceEvidence().Passed, 412, 1, true},
		{"candidate evidence", gate.CandidateEvidence().Actual,
			gate.CandidateEvidence().Minimum, gate.CandidateEvidence().Passed, 388, 1, true},
		{"added behaviors", gate.AddedBehaviors().Actual,
			gate.AddedBehaviors().Maximum, gate.AddedBehaviors().Passed, 5, 3, false},
		{"block decisions", gate.BlockDecisions().Actual,
			gate.BlockDecisions().Maximum, gate.BlockDecisions().Passed, 0, 0, true},
		{"critical risk", gate.CriticalRiskObservations().Actual,
			gate.CriticalRiskObservations().Maximum, gate.CriticalRiskObservations().Passed, 0, 0, true},
	}
	for _, c := range checks {
		if c.actual != c.wantAct || c.bound != c.wantBnd || c.passed != c.wantPas {
			t.Errorf("%s = (%d, %d, %t), want (%d, %d, %t)",
				c.name, c.actual, c.bound, c.passed, c.wantAct, c.wantBnd, c.wantPas)
		}
	}
	if gate.Verdict() != GateVerdictFail {
		t.Errorf("verdict = %q, want fail", gate.Verdict())
	}
	if gate.ReferenceRunID() != "run-ref" || gate.CandidateRunID() != "run-can" ||
		gate.ReferenceCandidateID() != "cand-ref" || gate.CandidateCandidateID() != "cand-can" ||
		gate.Environment() != "staging" {
		t.Error("gate identity was not restored from the promotion's own columns")
	}
	if !promotion.DecidedAt().Equal(restoreEpoch) {
		t.Errorf("decided at = %v, want %v with nanoseconds intact",
			promotion.DecidedAt(), restoreEpoch)
	}
}

// The regression this whole design exists for.
//
// `actual = 1, minimum = 1, passed = false` is impossible under today's
// helpers. It is exactly what an older build with a defect would have written,
// and the promotion it produced was recorded as rejected because that is what
// the platform decided. Restoring it must hand back what was stored — a build
// that recomputed the flag would return `passed = true` beside the stored fail
// verdict, which is neither the historical result nor a current one.
func TestRestorePreservesAHistoricallyInconsistentCheck(t *testing.T) {
	row := storedGateRow()
	row.refEvidenceActual = "1"
	row.refEvidenceMinimum = "1"
	row.refEvidencePassed = 0 // arithmetic says true; the old build wrote false

	promotion, err := restorePromotionRow(row)
	if err != nil {
		t.Fatalf("a historically inconsistent check must restore, not fail: %v", err)
	}
	check := promotion.GateResult().ReferenceEvidence()
	if check.Actual != 1 || check.Minimum != 1 || check.Passed {
		t.Errorf("reference evidence = %+v, want actual 1, minimum 1, passed false "+
			"exactly as stored — the flag was recomputed", check)
	}
}

// And the same at the verdict level: five passing flags beside a stored fail
// verdict restores as five passing flags and a fail verdict.
func TestRestoreDoesNotRecomputeTheVerdictFromTheFlags(t *testing.T) {
	row := storedGateRow()
	row.addedActual = "0"
	row.addedPassed = 1 // every check now passes …
	// … while the stored verdict and outcome still say the decision failed.

	promotion, err := restorePromotionRow(row)
	if err != nil {
		t.Fatalf("restorePromotionRow() error = %v", err)
	}
	if promotion.GateResult().Verdict() != GateVerdictFail {
		t.Errorf("verdict = %q, want the stored fail — it was recomputed from the flags",
			promotion.GateResult().Verdict())
	}
	if promotion.Outcome() != PromotionRejected {
		t.Errorf("outcome = %q, want rejected", promotion.Outcome())
	}
}

// What *is* corruption: a value this code could not have written.
func TestRestorePromotionRowFailsClosedOnCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*promotionRow)
	}{
		{"unknown outcome", func(r *promotionRow) { r.outcome = "approved" }},
		{"unknown verdict", func(r *promotionRow) { r.verdict = "maybe" }},
		{"passed flag is neither 0 nor 1", func(r *promotionRow) { r.addedPassed = 2 }},
		{"non-canonical uint64", func(r *promotionRow) { r.maxAdded = "007" }},
		{"unparseable uint64", func(r *promotionRow) { r.criticalActual = "many" }},
		{"unparseable timestamp", func(r *promotionRow) { r.decidedAt = "yesterday" }},
		{"rank above the bound", func(r *promotionRow) { r.targetRank = 10_000 }},
		{"negative rank", func(r *promotionRow) { r.sourceRank = -1 }},
		{"inverted ordering", func(r *promotionRow) { r.sourceRank, r.targetRank = 40, 30 }},
		{"empty identifier", func(r *promotionRow) { r.id = "" }},
		// Task 066's own invariant: accepted with a stored fail verdict was
		// not written by this code.
		{"outcome disagrees with verdict", func(r *promotionRow) { r.outcome = "accepted" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := storedGateRow()
			tt.mutate(&row)
			if _, err := restorePromotionRow(row); !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("restorePromotionRow() error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// The full unsigned range survives, which a native integer column would not
// preserve and a JSON number would round.
func TestRestorePromotionRowPreservesMaxUint64(t *testing.T) {
	row := storedGateRow()
	max := "18446744073709551615"
	row.maxAdded, row.maxBlock, row.maxCritical = max, max, max
	row.addedActual, row.blockActual, row.criticalActual = max, max, max
	row.sourceRevision, row.targetRevision = max, max
	row.addedPassed, row.blockPassed, row.criticalPassed = 1, 1, 1

	promotion, err := restorePromotionRow(row)
	if err != nil {
		t.Fatalf("restorePromotionRow() error = %v", err)
	}
	if promotion.GateLimits().MaxAddedBehaviors != math.MaxUint64 ||
		promotion.Source().Revision != math.MaxUint64 ||
		promotion.GateResult().CriticalRiskObservations().Actual != math.MaxUint64 {
		t.Error("a MaxUint64 value did not survive the round trip")
	}
}

// promotionEnvironmentOrder is deterministic and independent of direction,
// which is what keeps two promotions over an overlapping pair from acquiring
// the shared row in opposite sequences.
func TestPromotionEnvironmentOrderIsByteAscending(t *testing.T) {
	forward, err := restorePromotionRow(storedGateRow())
	if err != nil {
		t.Fatalf("restorePromotionRow() error = %v", err)
	}
	order := promotionEnvironmentOrder(forward)
	if order[0] != "production" || order[1] != "staging" {
		t.Errorf("order = %v, want production before staging by byte value", order)
	}

	// A promotion whose refs sort the other way round still yields ascending
	// order, so direction cannot change the sequence.
	reversed := storedGateRow()
	reversed.sourceRef, reversed.targetRef = "alpha", "beta"
	reversedPromotion, err := restorePromotionRow(reversed)
	if err != nil {
		t.Fatalf("restorePromotionRow() error = %v", err)
	}
	if got := promotionEnvironmentOrder(reversedPromotion); got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("order = %v, want alpha before beta", got)
	}
}
