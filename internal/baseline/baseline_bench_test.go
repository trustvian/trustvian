package baseline_test

import (
	"testing"
	"time"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
)

// BenchmarkObserve measures the copy-on-write update cost: every call
// allocates a new Fingerprints map (see Baseline.Observe's doc comment),
// so this is the cost that matters as an actor's known-fingerprint set
// grows, not just the EWMA arithmetic itself.
//
// This benchmark's fixed now means Baseline.Observe's ordering guard
// (now.After(LastFingerprintTime)) is only satisfied on the very first
// call — every steady-state call here takes the "no valid transition"
// path, so it does not exercise recordPredecessor's own map
// copy-on-write cost at all. See BenchmarkObserveTransition below for
// that.
func BenchmarkObserve(b *testing.B) {
	fp := testFingerprint()
	vol := features.VolatileFeatures{HasLatency: true, Latency: 10 * time.Millisecond}
	bl := baseline.New(testKey)
	now := time.Now()

	b.ReportAllocs()
	for b.Loop() {
		bl, _ = bl.Observe(fp, vol, now)
	}
}

// BenchmarkObserveTransition measures Observe's real, worst-case v0.6
// foundation cost: a strictly-advancing clock and an alternating
// fingerprint pair, so every single call takes the "valid transition"
// path and actually exercises recordPredecessor's copy-on-write
// (see docs/tasks/025-sequence-analysis-foundation.md § Benchmarks for
// the before/after comparison this measures against BenchmarkObserve).
func BenchmarkObserveTransition(b *testing.B) {
	fpA, fpB := readFingerprint(), updateFingerprint() // defined in baseline_test.go
	vol := features.VolatileFeatures{HasLatency: true, Latency: 10 * time.Millisecond}
	bl := baseline.New(testKey)
	now := time.Now()

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		fp := fpA
		if i%2 == 1 {
			fp = fpB
		}
		bl, _ = bl.Observe(fp, vol, now)
		now = now.Add(time.Millisecond)
	}
}

// BenchmarkObserveTrigram measures task 027's own added cost on top of
// BenchmarkObserveTransition: a strictly-advancing clock cycling
// through 3 distinct fingerprints, so every call (after the first two)
// completes a valid 3-gram and exercises recordTrigram/
// observeTrigramContinuation's copy-on-write, not just
// recordPredecessor's — see docs/tasks/027-bounded-ngram-detection.md
// § Benchmarks for the before/after comparison this measures against
// BenchmarkObserveTransition.
func BenchmarkObserveTrigram(b *testing.B) {
	fpA, fpB, fpC := readFingerprint(), updateFingerprint(), deleteFingerprint() // defined in baseline_test.go
	vol := features.VolatileFeatures{HasLatency: true, Latency: 10 * time.Millisecond}
	bl := baseline.New(testKey)
	now := time.Now()

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		var fp = fpA
		switch i % 3 {
		case 1:
			fp = fpB
		case 2:
			fp = fpC
		}
		bl, _ = bl.Observe(fp, vol, now)
		now = now.Add(time.Millisecond)
	}
}
