package anomaly_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/trustvian/trustvian/internal/anomaly"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
)

// BenchmarkScoreKnownFamiliar is the common case: a mature fingerprint
// behaving exactly as expected, so no signal fires. This is the path that
// runs on every normal event and should be the cheapest.
func BenchmarkScoreKnownFamiliar(b *testing.B) {
	fp := fingerprint.Compute(stable("payment-db"))
	bl := matureBaseline(fp, 100, 10)
	feat := features.Features{
		Stable: stable("payment-db"),
		Volatile: features.VolatileFeatures{
			HasLatency: true, Latency: 10 * time.Millisecond,
			Timestamp: bl.Fingerprints[fp.ID].LastObserved.Add(matureBaselineInterval), // normal rate: frequency_deviation must not fire here
		},
	}
	cfg := anomaly.DefaultConfig()

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, bl, cfg)
	}
}

// BenchmarkScoreNovelWithAllSignals is the worst case: every signal
// fires (novel fingerprint, latency deviation is moot since baseline is
// empty, error against no history, sensitive target floor).
func BenchmarkScoreNovelWithAllSignals(b *testing.B) {
	empty := matureBaseline(fingerprint.Compute(stable("unrelated")), 0, 0)
	feat := features.Features{
		Stable:   stable("secrets-manager"),
		Volatile: features.VolatileFeatures{Error: true},
	}
	fp := fingerprint.Compute(feat.Stable)
	cfg := anomaly.DefaultConfig()
	cfg.SensitiveTargetFloor = map[string]float64{"secrets-manager": 0.7}

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, empty, cfg)
	}
}

// BenchmarkScoreTransitionDeviation measures the v0.6 foundation's own
// added cost on a mature, otherwise-unremarkable fingerprint whose
// PredecessorCounts map holds a realistic number of distinct entries
// (near maxPredecessors) — the worst case for the map lookup
// transitionSignal performs, not the empty-map case
// BenchmarkScoreKnownFamiliar's self-transition already exercises
// cheaply.
func BenchmarkScoreTransitionDeviation(b *testing.B) {
	fp := fingerprint.Compute(stable("payment-db"))
	bl := baseline.New(testKey)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 60 { // near maxPredecessors (64), without exceeding it
		pred := fingerprint.Compute(stable(fmt.Sprintf("predecessor-%d", i)))
		bl, _ = bl.Observe(pred, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(fp, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	// The predecessor for the event under test: never before observed
	// leading to fp, so transition_deviation actually fires.
	unseenPredecessor := fingerprint.Compute(stable("unseen-predecessor"))
	bl, _ = bl.Observe(unseenPredecessor, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	// This benchmark's own loop leaves bl.PreviousFingerprintID
	// populated (task 027 added the field; it now shifts on every
	// valid advance, including this one) — which would also make
	// ngram_deviation fire here, measuring a cost this benchmark was
	// never meant to isolate (it predates task 027 by two tasks; see
	// BenchmarkScoreNGramDeviation for that signal's own dedicated
	// benchmark). Clearing it keeps this benchmark measuring exactly
	// what its own doc comment says: transitionSignal's cost alone.
	bl.PreviousFingerprintID = ""

	feat := features.Features{Stable: stable("payment-db"), Volatile: features.VolatileFeatures{Timestamp: now}}
	cfg := anomaly.DefaultConfig()
	cfg.TransitionWeight = 0.7

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, bl, cfg)
	}
}

// BenchmarkScoreTransitionRarity measures task 026's own added cost on top
// of the task 025 transition foundation: transitionRaritySignal's map
// lookup on PredecessorCounts plus the O(1) frequency division, in the
// worst case where the signal actually fires (a rare-but-seen transition,
// past MinTransitionObservations, so Detail's fmt.Sprintf runs) rather than
// short-circuiting on the cold-start gate or landing on a common
// (Value == 0, no Detail) transition. Compare against
// BenchmarkScoreTransitionDeviation, which measures the same predecessor
// scale with only the task 025 signal enabled.
//
// Since task 028, this benchmark's own B/op and allocs/op also include
// markovSurprisalSignal's cost, unavoidably: markov_surprisal shares
// transition_rarity's exact gate by design (same count/total, same
// MinTransitionObservations — see
// docs/adr/0013-first-order-markov-surprisal-without-duplicate-evidence.md),
// so any fixture with enough history to fire one always has enough to
// fire the other too, regardless of MarkovWeight's own value (Score's
// append condition is Value > 0, weight-independent, the same
// "always compute" precedent every prior signal already established).
// There is no way to construct a fixture that isolates
// transitionRaritySignal's cost alone anymore without also disabling
// the minimum-support gate transitionRaritySignal itself needs to fire
// — unlike BenchmarkScoreTransitionDeviation's own task-027 fixture
// leak (internal/baseline.Baseline.PreviousFingerprintID's mere
// existence), which was a genuine, fixable accident, this is a
// structural consequence of the ADR 0013 design, not a bug to correct
// here. See docs/tasks/028-markov-transition-scoring.md § Benchmarks
// for the measured before/after delta this causes.
func BenchmarkScoreTransitionRarity(b *testing.B) {
	fp := fingerprint.Compute(stable("payment-db"))
	bl := baseline.New(testKey)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	predecessor := fingerprint.Compute(stable("predecessor"))
	// predecessor leads to 50 distinct filler destinations (the common
	// case) and only twice to fp, so predecessor->fp is rare but not
	// unseen: OutgoingTransitionTotal clears MinTransitionObservations
	// (20) while frequency(fp | predecessor) stays low, so the rarity
	// signal fires instead of degenerating to Value == 0.
	for i := range 50 {
		filler := fingerprint.Compute(stable(fmt.Sprintf("filler-%d", i)))
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(filler, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	for range 2 {
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(fp, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	// One final, unpaired predecessor observation: without it,
	// bl.LastFingerprintID would be fp's own ID (the loop's last
	// Observe), making the Score call below a never-seen fp->fp
	// self-transition instead of the intended, well-established
	// predecessor->fp transition.
	bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)

	feat := features.Features{Stable: stable("payment-db"), Volatile: features.VolatileFeatures{Timestamp: now}}
	cfg := anomaly.DefaultConfig()
	cfg.TransitionRarityWeight = 0.7

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, bl, cfg)
	}
}

// BenchmarkScoreMarkovSurprisal measures task 028's own added cost on
// top of the task 026 transition-rarity foundation: markovSurprisalSignal's
// map lookup on PredecessorCounts plus the O(1) surprisal/normalization
// arithmetic (math.Log2 twice, one division), in the worst case where
// the signal actually fires (a rare-but-seen transition, past
// MinTransitionObservations, so Detail's fmt.Sprintf runs) — mirroring
// BenchmarkScoreTransitionRarity's own worst-case shape exactly, since
// both signals share the identical gate and denominator. Compare
// directly against it to see this task's own added cost.
func BenchmarkScoreMarkovSurprisal(b *testing.B) {
	fp := fingerprint.Compute(stable("payment-db"))
	bl := baseline.New(testKey)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	predecessor := fingerprint.Compute(stable("predecessor"))
	for i := range 50 {
		filler := fingerprint.Compute(stable(fmt.Sprintf("filler-%d", i)))
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(filler, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	for range 2 {
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(fp, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)

	feat := features.Features{Stable: stable("payment-db"), Volatile: features.VolatileFeatures{Timestamp: now}}
	cfg := anomaly.DefaultConfig()
	cfg.MarkovWeight = 0.7

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, bl, cfg)
	}
}

// BenchmarkScoreMarkovLookup isolates the cold-start-gated, non-firing
// path — the "lookup" cost §41 of the task brief asks to keep
// approximately O(1) on the common, non-firing case: a predecessor
// below MinTransitionObservations, so markovSurprisalSignal takes its
// early-return path (one map read, one field read, one comparison, no
// math.Log2, no allocation).
func BenchmarkScoreMarkovLookup(b *testing.B) {
	fp := fingerprint.Compute(stable("payment-db"))
	bl := baseline.New(testKey)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	predecessor := fingerprint.Compute(stable("predecessor"))
	// Only 5 outgoing observations: below DefaultConfig's
	// MinTransitionObservations (20), so the cold-start gate — not the
	// arithmetic — is what this benchmark measures.
	for range 5 {
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(fp, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)

	feat := features.Features{Stable: stable("payment-db"), Volatile: features.VolatileFeatures{Timestamp: now}}
	cfg := anomaly.DefaultConfig()
	cfg.MarkovWeight = 0.7

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, bl, cfg)
	}
}

// BenchmarkScoreNGramDeviation measures task 027's own added cost on a
// mature, otherwise-unremarkable fingerprint whose TrigramCounts map
// holds a realistic number of distinct (grandparent,predecessor) pairs
// (near maxTrigramPredecessors) — the worst case for the map lookup
// ngramDeviationSignal performs, mirroring
// BenchmarkScoreTransitionDeviation one level up. Compare against it
// directly to see this task's own added cost on top of the task
// 025/026 foundation.
func BenchmarkScoreNGramDeviation(b *testing.B) {
	fp := fingerprint.Compute(stable("payment-db"))
	bl := baseline.New(testKey)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	predecessor := fingerprint.Compute(stable("predecessor"))
	for i := range 60 { // near maxTrigramPredecessors (64), without exceeding it
		grandparent := fingerprint.Compute(stable(fmt.Sprintf("grandparent-%d", i)))
		bl, _ = bl.Observe(grandparent, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(fp, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	// The (grandparent, predecessor) pair for the event under test:
	// never before observed leading to fp, so ngram_deviation actually
	// fires.
	unseenGrandparent := fingerprint.Compute(stable("unseen-grandparent"))
	bl, _ = bl.Observe(unseenGrandparent, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)

	feat := features.Features{Stable: stable("payment-db"), Volatile: features.VolatileFeatures{Timestamp: now}}
	cfg := anomaly.DefaultConfig()
	cfg.NGramWeight = 0.7

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, bl, cfg)
	}
}

// BenchmarkScoreNGramRarity measures ngramRaritySignal's own cost in
// the worst case where it actually fires (a rare-but-seen 3-gram, past
// MinNGramObservations, so Detail's fmt.Sprintf runs), mirroring
// BenchmarkScoreTransitionRarity one level up.
func BenchmarkScoreNGramRarity(b *testing.B) {
	fp := fingerprint.Compute(stable("payment-db"))
	bl := baseline.New(testKey)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	grandparent := fingerprint.Compute(stable("grandparent"))
	predecessor := fingerprint.Compute(stable("predecessor"))
	// (grandparent, predecessor) leads to 50 distinct filler
	// destinations (the common case) and only twice to fp, so
	// (grandparent,predecessor)->fp is rare but not unseen:
	// TrigramContinuationTotal clears MinNGramObservations (20) while
	// frequency(fp | grandparent, predecessor) stays low, so the rarity
	// signal fires instead of degenerating to Value == 0.
	for i := range 50 {
		filler := fingerprint.Compute(stable(fmt.Sprintf("filler-%d", i)))
		bl, _ = bl.Observe(grandparent, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(filler, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	for range 2 {
		bl, _ = bl.Observe(grandparent, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		bl, _ = bl.Observe(fp, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	// Two final, unpaired observations (grandparent, then predecessor):
	// without them, the history window would not be positioned at
	// (grandparent, predecessor) for the Score call below — see
	// ngramBaseline's identical technique in anomaly_test.go.
	bl, _ = bl.Observe(grandparent, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	bl, _ = bl.Observe(predecessor, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)

	feat := features.Features{Stable: stable("payment-db"), Volatile: features.VolatileFeatures{Timestamp: now}}
	cfg := anomaly.DefaultConfig()
	cfg.NGramRarityWeight = 0.7

	b.ReportAllocs()
	for b.Loop() {
		_ = anomaly.Score(feat, fp, bl, cfg)
	}
}
