package baseline_test

// Task 046: bounded fingerprint admission.
//
// Baseline.Fingerprints was the one behavioral state dimension with no
// outer bound, and Fingerprint.ID is derived from Event fields a caller
// supplies — so one actor emitting a distinct operation name per call
// grew its baseline, and its database row, without limit.
//
// These tests assert the structural bound itself, not that a large input
// fails to panic. The distinction is the whole point: the test this file
// supersedes drove 5,000 distinct fingerprints and asserted only that
// Observe returned no error, which is exactly as true of an unbounded
// implementation as of a bounded one.
//
// See docs/adr/0019-bounded-fingerprint-admission.md for why admission is
// refused rather than an existing entry evicted.

import (
	"fmt"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
)

// maxFingerprints is unexported in internal/baseline, so these tests
// restate it. A change to the production constant that is not mirrored
// here fails TestAdmissionBoundMatchesImplementation below rather than
// silently weakening every assertion in this file.
const wantMaxFingerprints = 512

// distinctFingerprint returns the i'th of an arbitrarily large family of
// distinct fingerprints, standing in for the caller-controlled operation
// names an untrusted event source can vary per call.
func distinctFingerprint(i int) fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryHTTP,
		OperationName:     fmt.Sprintf("GET /resource/%d", i),
		TargetName:        "api",
		Environment:       "production",
	})
}

// observeN feeds n distinct fingerprints through one baseline, each one
// second apart so every observation is a valid, ordered advance.
func observeN(b baseline.Baseline, n int, start time.Time) baseline.Baseline {
	for i := range n {
		b, _ = b.Observe(distinctFingerprint(i), features.VolatileFeatures{}, start.Add(time.Duration(i)*time.Second))
	}
	return b
}

// TestAdmissionBoundMatchesImplementation pins wantMaxFingerprints to the
// production constant: feeding far more than the bound must leave exactly
// the bound, so if the constant moves this file's premise fails loudly.
func TestAdmissionBoundMatchesImplementation(t *testing.T) {
	b := observeN(baseline.New(testKey), wantMaxFingerprints*2, time.Now())
	if got := len(b.Fingerprints); got != wantMaxFingerprints {
		t.Fatalf("len(Fingerprints) = %d, want %d — the production bound and this file disagree", got, wantMaxFingerprints)
	}
}

// TestAdmitsUpToCapacity is the below-capacity half of the contract:
// nothing is refused while there is room.
func TestAdmitsUpToCapacity(t *testing.T) {
	b := observeN(baseline.New(testKey), wantMaxFingerprints, time.Now())

	if got := len(b.Fingerprints); got != wantMaxFingerprints {
		t.Fatalf("len(Fingerprints) = %d, want %d", got, wantMaxFingerprints)
	}
	for i := range wantMaxFingerprints {
		fp := distinctFingerprint(i)
		if _, ok := b.Fingerprints[fp.ID]; !ok {
			t.Fatalf("fingerprint %d was not admitted below capacity", i)
		}
	}
}

// TestRejectsOneBeyondCapacity is the boundary: the 513th distinct
// fingerprint is refused, and refusal removes nothing.
func TestRejectsOneBeyondCapacity(t *testing.T) {
	now := time.Now()
	b := observeN(baseline.New(testKey), wantMaxFingerprints, now)

	before := make(map[string]baseline.FingerprintStats, len(b.Fingerprints))
	for id, st := range b.Fingerprints {
		before[id] = st
	}

	extra := distinctFingerprint(wantMaxFingerprints)
	b, _ = b.Observe(extra, features.VolatileFeatures{}, now.Add(time.Hour))

	if got := len(b.Fingerprints); got != wantMaxFingerprints {
		t.Fatalf("len(Fingerprints) = %d, want %d after one observation past capacity", got, wantMaxFingerprints)
	}
	if _, ok := b.Fingerprints[extra.ID]; ok {
		t.Fatal("the fingerprint observed past capacity was admitted")
	}
	// Nothing was evicted to make room, and nothing already learned was
	// disturbed — the anti-eviction half of the decision.
	for id, want := range before {
		got, ok := b.Fingerprints[id]
		if !ok {
			t.Fatalf("fingerprint %q was evicted to admit a new one", id)
		}
		if got.Count != want.Count {
			t.Fatalf("fingerprint %q: Count = %d, want %d (untouched by a rejected observation)", id, got.Count, want.Count)
		}
	}
}

// TestAdversarialStreamStaysBounded is the security property stated
// directly: an untrusted source varying the fingerprint on every call
// cannot grow learned state without limit.
func TestAdversarialStreamStaysBounded(t *testing.T) {
	const flood = 5000

	b := observeN(baseline.New(testKey), flood, time.Now())

	if got := len(b.Fingerprints); got != wantMaxFingerprints {
		t.Fatalf("len(Fingerprints) = %d after %d distinct fingerprints, want %d", got, flood, wantMaxFingerprints)
	}
}

// TestKnownFingerprintKeepsLearningAtCapacity is the requirement that
// capacity bounds admission only. A full baseline is not a frozen one:
// everything it already knows keeps accumulating evidence, which is what
// keeps detection working for the behavior an actor actually repeats.
func TestKnownFingerprintKeepsLearningAtCapacity(t *testing.T) {
	now := time.Now()
	b := observeN(baseline.New(testKey), wantMaxFingerprints, now)

	known := distinctFingerprint(0)
	countBefore := b.Fingerprints[known.ID].Count

	// Interleave rejected strangers with the known fingerprint: neither
	// the rejections nor the full map may stop the known one learning.
	at := now.Add(time.Hour)
	for i := range 10 {
		b, _ = b.Observe(distinctFingerprint(wantMaxFingerprints+i), features.VolatileFeatures{}, at)
		at = at.Add(time.Second)
		b, _ = b.Observe(known, features.VolatileFeatures{}, at)
		at = at.Add(time.Second)
	}

	got := b.Fingerprints[known.ID].Count
	if want := countBefore + 10; got != want {
		t.Fatalf("known fingerprint Count = %d, want %d — a full baseline must not freeze existing learning", got, want)
	}
	if n := len(b.Fingerprints); n != wantMaxFingerprints {
		t.Fatalf("len(Fingerprints) = %d, want %d", n, wantMaxFingerprints)
	}
}

// TestRejectionDoesNotMoveTheHistoryWindow covers the side door that
// makes the bound airtight. A rejected fingerprint must not become the
// predecessor a later observation records a transition from: writing the
// other half of that transition would create a FingerprintStats entry for
// an identity that was never admitted, quietly re-opening the map.
func TestRejectionDoesNotMoveTheHistoryWindow(t *testing.T) {
	now := time.Now()
	b := observeN(baseline.New(testKey), wantMaxFingerprints, now)

	lastAdmitted := b.LastFingerprintID

	rejected := distinctFingerprint(wantMaxFingerprints)
	b, _ = b.Observe(rejected, features.VolatileFeatures{}, now.Add(time.Hour))

	if b.LastFingerprintID != lastAdmitted {
		t.Fatalf("LastFingerprintID = %q, want %q — the window advanced to a fingerprint that was never learned", b.LastFingerprintID, lastAdmitted)
	}

	// The following observation must not resurrect the rejected ID as a
	// predecessor entry.
	b, _ = b.Observe(distinctFingerprint(0), features.VolatileFeatures{}, now.Add(2*time.Hour))
	if _, ok := b.Fingerprints[rejected.ID]; ok {
		t.Fatal("a rejected fingerprint was admitted through the predecessor path")
	}
	if got := len(b.Fingerprints); got != wantMaxFingerprints {
		t.Fatalf("len(Fingerprints) = %d, want %d", got, wantMaxFingerprints)
	}
}

// TestAdmissionIsDeterministic proves the bound introduces no ordering or
// randomness dependence: the same event sequence yields the same retained
// identities and the same counts, which is what makes a decision derived
// from a baseline reproducible.
func TestAdmissionIsDeterministic(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()

	run := func() baseline.Baseline {
		return observeN(baseline.New(testKey), wantMaxFingerprints+250, start)
	}
	first, second := run(), run()

	if len(first.Fingerprints) != len(second.Fingerprints) {
		t.Fatalf("cardinality differs across identical runs: %d vs %d", len(first.Fingerprints), len(second.Fingerprints))
	}
	for id, a := range first.Fingerprints {
		bStats, ok := second.Fingerprints[id]
		if !ok {
			t.Fatalf("fingerprint %q retained in one run and not the other", id)
		}
		if a.Count != bStats.Count {
			t.Fatalf("fingerprint %q: Count %d vs %d across identical runs", id, a.Count, bStats.Count)
		}
	}
	if first.LastFingerprintID != second.LastFingerprintID {
		t.Fatal("LastFingerprintID differs across identical runs")
	}
}

// --- Legacy baselines learned before the bound existed ----------------

// oversizedLegacyBaseline builds what a v0.9 deployment could genuinely
// have persisted: more fingerprints than v1.0 will ever admit. It is
// assembled directly rather than through Observe, because the bounded
// implementation can no longer produce one.
func oversizedLegacyBaseline(n int, now time.Time) baseline.Baseline {
	b := baseline.New(testKey)
	fps := make(map[string]baseline.FingerprintStats, n)
	for i := range n {
		fps[distinctFingerprint(i).ID] = baseline.FingerprintStats{
			Count:         7,
			FirstObserved: now.Add(-time.Hour),
			LastObserved:  now.Add(-time.Minute),
		}
	}
	b.Fingerprints = fps
	return b
}

// TestLegacyOversizedBaselineIsNotTruncated is the upgrade guarantee: a
// baseline that already exceeds the bound keeps every identity it learned.
// Nothing is evicted on load, on observation, or at any other point —
// learned detection state survives the upgrade intact.
func TestLegacyOversizedBaselineIsNotTruncated(t *testing.T) {
	now := time.Now()
	const legacySize = wantMaxFingerprints + 300

	b := oversizedLegacyBaseline(legacySize, now)
	if got := len(b.Fingerprints); got != legacySize {
		t.Fatalf("fixture: len(Fingerprints) = %d, want %d", got, legacySize)
	}

	// An ordinary observation of a known fingerprint must not trigger a
	// cleanup pass.
	known := distinctFingerprint(0)
	b, _ = b.Observe(known, features.VolatileFeatures{}, now)

	if got := len(b.Fingerprints); got != legacySize {
		t.Fatalf("len(Fingerprints) = %d after observing a known fingerprint, want %d — legacy state must not be truncated", got, legacySize)
	}
}

// TestLegacyOversizedBaselineKeepsLearningKnownFingerprints: being over
// the bound does not freeze what the actor already does.
func TestLegacyOversizedBaselineKeepsLearningKnownFingerprints(t *testing.T) {
	now := time.Now()
	b := oversizedLegacyBaseline(wantMaxFingerprints+300, now)

	known := distinctFingerprint(5)
	before := b.Fingerprints[known.ID].Count

	b, _ = b.Observe(known, features.VolatileFeatures{}, now.Add(time.Second))

	if got := b.Fingerprints[known.ID].Count; got != before+1 {
		t.Fatalf("known fingerprint Count = %d, want %d", got, before+1)
	}
}

// TestLegacyOversizedBaselineAdmitsNothingNew: an oversized baseline does
// not converge downward, but it does not grow either. Its cardinality is
// frozen at whatever it already was.
func TestLegacyOversizedBaselineAdmitsNothingNew(t *testing.T) {
	now := time.Now()
	const legacySize = wantMaxFingerprints + 300

	b := oversizedLegacyBaseline(legacySize, now)

	at := now
	for i := range 50 {
		at = at.Add(time.Second)
		b, _ = b.Observe(distinctFingerprint(legacySize+i), features.VolatileFeatures{}, at)
	}

	if got := len(b.Fingerprints); got != legacySize {
		t.Fatalf("len(Fingerprints) = %d, want %d — an oversized legacy baseline must neither shrink nor grow", got, legacySize)
	}
}

// TestObserveReportsWhetherItAdmitted is task 051's addition to this
// contract. The bound itself is unchanged — refuse, never evict — but the
// refusal used to be invisible above this package: the store call succeeded,
// so Engine.Observe reported learning that had not happened. Task 049
// recorded that as an open conflict, and a learning scope that exists to be
// measured cannot silently stop learning while reporting that it did.
//
// The bool reports whether *this fingerprint's* statistics were updated,
// which is deliberately narrower than "did any field change": an observation
// at capacity still advances LastObserved and DelegatorCounts, and a caller
// asking whether the behavior was learned is not asking about those.
func TestObserveReportsWhetherItAdmitted(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := baseline.New(baseline.Key{ActorID: "svc-payment", Environment: "production"})

	for i := range wantMaxFingerprints {
		var admitted bool
		b, admitted = b.Observe(distinctFingerprint(i), features.VolatileFeatures{}, start.Add(time.Duration(i)*time.Second))
		if !admitted {
			t.Fatalf("observation %d reported admitted = false below the bound", i)
		}
	}
	if len(b.Fingerprints) != wantMaxFingerprints {
		t.Fatalf("premise broken: %d fingerprints held, want %d", len(b.Fingerprints), wantMaxFingerprints)
	}

	t.Run("unknown fingerprint at capacity", func(t *testing.T) {
		next, admitted := b.Observe(distinctFingerprint(wantMaxFingerprints), features.VolatileFeatures{}, start.Add(time.Hour))
		if admitted {
			t.Error("admitted = true for a fingerprint the bound refused")
		}
		if len(next.Fingerprints) != wantMaxFingerprints {
			t.Errorf("held %d fingerprints after refusal, want %d — the bound must refuse, never evict",
				len(next.Fingerprints), wantMaxFingerprints)
		}
		// Refusal is not an error and does not stop the rest of the
		// observation: whole-baseline recency still advances.
		if !next.LastObserved.Equal(start.Add(time.Hour)) {
			t.Errorf("LastObserved = %v, want the refused observation's timestamp", next.LastObserved)
		}
	})

	t.Run("known fingerprint at capacity", func(t *testing.T) {
		known := distinctFingerprint(0)
		before := b.Fingerprints[known.ID].Count

		next, admitted := b.Observe(known, features.VolatileFeatures{}, start.Add(2*time.Hour))
		if !admitted {
			t.Error("admitted = false for a fingerprint the baseline already knows")
		}
		if got := next.Fingerprints[known.ID].Count; got != before+1 {
			t.Errorf("Count = %d, want %d — a known fingerprint keeps learning at capacity", got, before+1)
		}
	})

	t.Run("fresh baseline admits", func(t *testing.T) {
		fresh := baseline.New(baseline.Key{Scope: "other", ActorID: "svc-payment", Environment: "production"})
		if _, admitted := fresh.Observe(distinctFingerprint(0), features.VolatileFeatures{}, start); !admitted {
			t.Error("admitted = false on an empty baseline")
		}
	})
}
