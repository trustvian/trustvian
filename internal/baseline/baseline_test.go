package baseline_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
)

var testKey = baseline.Key{ActorID: "svc-payment", Environment: "production"}

func testFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryHTTP,
		OperationName:     "POST /payment",
		TargetName:        "payment-db",
		Environment:       "production",
	})
}

func TestBaselineObserveTracksMaturityCount(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	const observations = 25
	const maturityThreshold = 20

	for i := 1; i <= observations; i++ {
		b, _ = b.Observe(fp, features.VolatileFeatures{}, time.Now())

		stats, ok := b.Fingerprints[fp.ID]
		if !ok {
			t.Fatalf("observation %d: fingerprint missing from baseline", i)
		}
		if stats.Count != uint64(i) {
			t.Fatalf("observation %d: Count = %d, want %d", i, stats.Count, i)
		}

		wantMature := i >= maturityThreshold
		gotMature := stats.Count >= maturityThreshold
		if gotMature != wantMature {
			t.Fatalf("observation %d: maturity (Count>=%d) = %v, want %v", i, maturityThreshold, gotMature, wantMature)
		}
	}
}

func TestBaselineObserveIsImmutable(t *testing.T) {
	fp := testFingerprint()
	before := baseline.New(testKey)

	after, _ := before.Observe(fp, features.VolatileFeatures{}, time.Now())

	if len(before.Fingerprints) != 0 {
		t.Fatalf("Observe mutated the receiver: before.Fingerprints = %v, want empty", before.Fingerprints)
	}
	if len(after.Fingerprints) != 1 {
		t.Fatalf("after.Fingerprints has %d entries, want 1", len(after.Fingerprints))
	}

	// A second Observe on `after` must not reach back and affect the
	// first snapshot either.
	again, _ := after.Observe(fp, features.VolatileFeatures{}, time.Now())
	if after.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("second Observe mutated an earlier snapshot: Count = %d, want 1", after.Fingerprints[fp.ID].Count)
	}
	if again.Fingerprints[fp.ID].Count != 2 {
		t.Fatalf("again.Fingerprints[fp.ID].Count = %d, want 2", again.Fingerprints[fp.ID].Count)
	}
}

func TestFingerprintStatsColdStart(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	b, _ = b.Observe(fp, features.VolatileFeatures{HasLatency: true, Latency: 100 * time.Millisecond, Error: true}, time.Now())

	stats := b.Fingerprints[fp.ID]
	if stats.Count != 1 {
		t.Fatalf("Count = %d, want 1", stats.Count)
	}
	if stats.LatencyObservations != 1 {
		t.Fatalf("LatencyObservations = %d, want 1", stats.LatencyObservations)
	}
	if stats.LatencyMeanDuration() != 100*time.Millisecond {
		t.Fatalf("LatencyMeanDuration() = %v, want 100ms", stats.LatencyMeanDuration())
	}
	if stats.LatencyStdDevDuration() != 0 {
		t.Fatalf("LatencyStdDevDuration() = %v, want 0 on first observation", stats.LatencyStdDevDuration())
	}
	if stats.ErrorRate != 1.0 {
		t.Fatalf("ErrorRate = %v, want 1.0 on first (errored) observation", stats.ErrorRate)
	}
}

func TestFingerprintStatsLatencyConvergesToStableValue(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	const target = 100 * time.Millisecond
	for range 200 {
		b, _ = b.Observe(fp, features.VolatileFeatures{HasLatency: true, Latency: target}, time.Now())
	}

	stats := b.Fingerprints[fp.ID]
	gotMean := stats.LatencyMeanDuration()
	if diff := gotMean - target; diff > time.Microsecond || diff < -time.Microsecond {
		t.Fatalf("LatencyMeanDuration() = %v, want ~%v after repeated identical observations", gotMean, target)
	}
	if stats.LatencyVariance > 1 {
		t.Fatalf("LatencyVariance = %v, want ~0 after repeated identical observations", stats.LatencyVariance)
	}
}

func TestFingerprintStatsIntervalConvergesToStableValue(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	const interval = 10 * time.Second
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	for range 50 {
		b, _ = b.Observe(fp, features.VolatileFeatures{}, now)
		now = now.Add(interval)
	}

	stats := b.Fingerprints[fp.ID]
	if stats.IntervalObservations == 0 {
		t.Fatal("IntervalObservations = 0 after 50 observations, want > 0")
	}
	gotMean := time.Duration(stats.IntervalMean)
	if diff := gotMean - interval; diff < -500*time.Millisecond || diff > 500*time.Millisecond {
		t.Fatalf("IntervalMean = %s after convergence, want ~%s", gotMean, interval)
	}
	if stats.IntervalVariance > float64(time.Second*time.Second) {
		t.Fatalf("IntervalVariance = %v, want ~0 after repeated identical intervals", stats.IntervalVariance)
	}
}

func TestFingerprintStatsIntervalObservationsIsCountMinusOne(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b, _ = b.Observe(fp, features.VolatileFeatures{}, now)

	stats := b.Fingerprints[fp.ID]
	if stats.IntervalObservations != 0 {
		t.Fatalf("IntervalObservations = %d after a single observation, want 0 (no prior LastObserved to measure from)", stats.IntervalObservations)
	}

	now = now.Add(5 * time.Second)
	b, _ = b.Observe(fp, features.VolatileFeatures{}, now)
	stats = b.Fingerprints[fp.ID]
	if stats.IntervalObservations != 1 {
		t.Fatalf("IntervalObservations = %d after a second observation, want 1", stats.IntervalObservations)
	}
	if got := time.Duration(stats.IntervalMean); got != 5*time.Second {
		t.Fatalf("IntervalMean = %s after exactly one interval, want exactly 5s", got)
	}
}

// observeStableCadence returns a Baseline where fp has been observed
// count times, exactly interval apart, starting at start, plus the
// timestamp of the last observation.
func observeStableCadence(fp fingerprint.Fingerprint, start time.Time, interval time.Duration, count int) (baseline.Baseline, time.Time) {
	b := baseline.New(testKey)
	now := start
	for i := range count {
		b, _ = b.Observe(fp, features.VolatileFeatures{}, now)
		if i < count-1 {
			now = now.Add(interval)
		}
	}
	return b, now
}

// TestFingerprintStatsIgnoresNonPositiveInterval pins the ordering guard
// in observe: an observation whose timestamp does not strictly follow
// LastObserved carries no usable interval information, so it must not
// reach the interval EWMA at all. Without the guard, a backdated event
// folds a large *negative* interval into IntervalMean/IntervalVariance,
// which is a baseline-poisoning primitive: such an event is typically
// decided observe_only, and therefore eligible for learning.
func TestFingerprintStatsIgnoresNonPositiveInterval(t *testing.T) {
	const cadence = 10 * time.Second
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		offset time.Duration // relative to LastObserved
	}{
		{"backdated observation", -5 * time.Second},
		{"far-backdated observation", -30 * 24 * time.Hour},
		{"duplicate timestamp", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp := testFingerprint()
			b, last := observeStableCadence(fp, start, cadence, 20)
			before := b.Fingerprints[fp.ID]

			b, _ = b.Observe(fp, features.VolatileFeatures{}, last.Add(tt.offset))
			after := b.Fingerprints[fp.ID]

			if after.IntervalMean != before.IntervalMean {
				t.Errorf("IntervalMean = %s after an out-of-order observation, want it unchanged at %s",
					time.Duration(after.IntervalMean), time.Duration(before.IntervalMean))
			}
			if after.IntervalVariance != before.IntervalVariance {
				t.Errorf("IntervalVariance = %v after an out-of-order observation, want it unchanged at %v",
					after.IntervalVariance, before.IntervalVariance)
			}
			if after.IntervalObservations != before.IntervalObservations {
				t.Errorf("IntervalObservations = %d after an out-of-order observation, want it unchanged at %d",
					after.IntervalObservations, before.IntervalObservations)
			}

			// The observation itself is still recorded: only its
			// interval is unusable, not its existence.
			if after.Count != before.Count+1 {
				t.Errorf("Count = %d, want %d — an out-of-order event is still an observation", after.Count, before.Count+1)
			}
			// LastObserved tracks the latest timestamp seen, never
			// regressing, so the *next* in-order event still measures
			// its interval from the true most-recent observation.
			if !after.LastObserved.Equal(last) {
				t.Errorf("LastObserved = %s, want it to stay at the latest timestamp seen (%s)", after.LastObserved, last)
			}
		})
	}
}

// TestFingerprintStatsOutOfOrderObservationDoesNotDistortNextInterval is
// the consequence test for the guard above: after a backdated event, the
// very next perfectly on-cadence event must still look on-cadence.
func TestFingerprintStatsOutOfOrderObservationDoesNotDistortNextInterval(t *testing.T) {
	const cadence = 10 * time.Second
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	fp := testFingerprint()
	b, last := observeStableCadence(fp, start, cadence, 20)

	b, _ = b.Observe(fp, features.VolatileFeatures{}, last.Add(-5*time.Second)) // out of order
	b, _ = b.Observe(fp, features.VolatileFeatures{}, last.Add(cadence))        // back on cadence

	stats := b.Fingerprints[fp.ID]
	gotMean := time.Duration(stats.IntervalMean)
	if diff := gotMean - cadence; diff < -100*time.Millisecond || diff > 100*time.Millisecond {
		t.Fatalf("IntervalMean = %s after an out-of-order event followed by an on-cadence one, want ~%s", gotMean, cadence)
	}
	if stddev := time.Duration(math.Sqrt(stats.IntervalVariance)); stddev > 100*time.Millisecond {
		t.Fatalf("interval stddev = %s, want ~0 — a backdated event must not inflate the interval variance", stddev)
	}
}

func TestFingerprintStatsSkipsLatencyWhenAbsent(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	b, _ = b.Observe(fp, features.VolatileFeatures{HasLatency: true, Latency: 50 * time.Millisecond}, time.Now())
	b, _ = b.Observe(fp, features.VolatileFeatures{HasLatency: false}, time.Now())

	stats := b.Fingerprints[fp.ID]
	if stats.Count != 2 {
		t.Fatalf("Count = %d, want 2", stats.Count)
	}
	if stats.LatencyObservations != 1 {
		t.Fatalf("LatencyObservations = %d, want 1 (second observation had no latency)", stats.LatencyObservations)
	}
	if stats.LatencyMeanDuration() != 50*time.Millisecond {
		t.Fatalf("LatencyMeanDuration() = %v, want unaffected 50ms", stats.LatencyMeanDuration())
	}
}

func TestFingerprintStatsErrorRateTracksDirection(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	for range 50 {
		b, _ = b.Observe(fp, features.VolatileFeatures{Error: false}, time.Now())
	}
	if rate := b.Fingerprints[fp.ID].ErrorRate; rate > 0.01 {
		t.Fatalf("ErrorRate = %v after 50 clean observations, want ~0", rate)
	}

	for range 50 {
		b, _ = b.Observe(fp, features.VolatileFeatures{Error: true}, time.Now())
	}
	if rate := b.Fingerprints[fp.ID].ErrorRate; rate < 0.99 {
		t.Fatalf("ErrorRate = %v after 50 errored observations, want ~1", rate)
	}
}

func TestBaselineObserveDistinctFingerprintsDoNotInterfere(t *testing.T) {
	fpA := fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
		OperationName: "POST /payment", TargetName: "payment-db", Environment: "production",
	})
	fpB := fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryExternal,
		OperationName: "webhook.send", TargetName: "notify-service", Environment: "production",
	})

	b := baseline.New(testKey)
	b, _ = b.Observe(fpA, features.VolatileFeatures{}, time.Now())
	b, _ = b.Observe(fpA, features.VolatileFeatures{}, time.Now())
	b, _ = b.Observe(fpB, features.VolatileFeatures{}, time.Now())

	if got := b.Fingerprints[fpA.ID].Count; got != 2 {
		t.Fatalf("fpA Count = %d, want 2", got)
	}
	if got := b.Fingerprints[fpB.ID].Count; got != 1 {
		t.Fatalf("fpB Count = %d, want 1", got)
	}
	if len(b.Fingerprints) != 2 {
		t.Fatalf("len(Fingerprints) = %d, want 2", len(b.Fingerprints))
	}
}

func TestFingerprintStatsLatencyStdDevIsNonNegative(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	latencies := []time.Duration{10 * time.Millisecond, 200 * time.Millisecond, 15 * time.Millisecond, 5 * time.Millisecond}
	for _, l := range latencies {
		b, _ = b.Observe(fp, features.VolatileFeatures{HasLatency: true, Latency: l}, time.Now())
	}

	stats := b.Fingerprints[fp.ID]
	if stats.LatencyVariance < 0 {
		t.Fatalf("LatencyVariance = %v, must be non-negative", stats.LatencyVariance)
	}
	if math.IsNaN(stats.LatencyVariance) {
		t.Fatalf("LatencyVariance is NaN")
	}

	stdDev := stats.LatencyStdDevDuration()
	if stdDev < 0 {
		t.Fatalf("LatencyStdDevDuration() = %v, must be non-negative", stdDev)
	}
}

func TestFingerprintStatsIsStale(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)

	observedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	b, _ = b.Observe(fp, features.VolatileFeatures{}, observedAt)
	stats := b.Fingerprints[fp.ID]

	tests := []struct {
		name   string
		now    time.Time
		maxAge time.Duration
		want   bool
	}{
		{"well within threshold", observedAt.Add(time.Minute), time.Hour, false},
		{"exactly at threshold", observedAt.Add(time.Hour), time.Hour, false},
		{"just beyond threshold", observedAt.Add(time.Hour + time.Nanosecond), time.Hour, true},
		{"far beyond threshold", observedAt.Add(30 * 24 * time.Hour), time.Hour, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stats.IsStale(tt.now, tt.maxAge); got != tt.want {
				t.Errorf("IsStale() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFingerprintStatsIsStaleNeverObserved(t *testing.T) {
	var stats baseline.FingerprintStats // zero value: Count == 0

	if stats.IsStale(time.Now(), time.Nanosecond) {
		t.Fatalf("IsStale() = true for a never-observed FingerprintStats, want false (cold start, not staleness)")
	}
}

// baselineAtHour observes fp count times, every observation timestamped
// at the same UTC hour-of-day (different days, so only the hour
// repeats) — the fixed input TestFingerprintStatsHourActivityConverges
// and the anomaly package's own time-pattern tests build against.
func baselineAtHour(fp fingerprint.Fingerprint, hour, count int) baseline.Baseline {
	b := baseline.New(testKey)
	day := time.Date(2026, 1, 1, hour, 0, 0, 0, time.UTC)
	for i := range count {
		b, _ = b.Observe(fp, features.VolatileFeatures{}, day.AddDate(0, 0, i))
	}
	return b
}

func TestFingerprintStatsHourActivityConverges(t *testing.T) {
	fp := testFingerprint()
	const hour = 9
	// hourActivityAlpha (0.02) is deliberately much slower than
	// emaAlpha (0.2) — see its doc comment in baseline.go — so
	// convergence here needs far more observations than the
	// latency/interval EWMA convergence tests do at the package's
	// faster rate; 300 observations at hourActivityAlpha=0.02 reaches
	// 1-(0.98)^300 ≈ 0.9977, comfortably inside a 0.01 tolerance.
	const observations = 300
	b := baselineAtHour(fp, hour, observations)

	stats := b.Fingerprints[fp.ID]
	if stats.TimePatternObservations != observations {
		t.Fatalf("TimePatternObservations = %d, want %d", stats.TimePatternObservations, observations)
	}
	if got := stats.HourActivity[hour]; math.Abs(got-1.0) > 0.01 {
		t.Errorf("HourActivity[%d] = %v, want ~1.0 (every observation at this hour)", hour, got)
	}
	for h := range 24 {
		if h == hour {
			continue
		}
		if got := stats.HourActivity[h]; math.Abs(got) > 1e-9 {
			t.Errorf("HourActivity[%d] = %v, want ~0.0 (never observed at this hour)", h, got)
		}
	}
}

func TestFingerprintStatsHourActivityFirstObservationIsOneHot(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)
	b, _ = b.Observe(fp, features.VolatileFeatures{}, time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC))

	stats := b.Fingerprints[fp.ID]
	if stats.TimePatternObservations != 1 {
		t.Fatalf("TimePatternObservations = %d, want 1", stats.TimePatternObservations)
	}
	for h := range 24 {
		want := 0.0
		if h == 14 {
			want = 1.0
		}
		if got := stats.HourActivity[h]; got != want {
			t.Errorf("HourActivity[%d] = %v, want %v after a single observation", h, got, want)
		}
	}
}

func TestFingerprintStatsHourActivityUniformTraffic(t *testing.T) {
	fp := testFingerprint()
	b := baseline.New(testKey)
	// Ten full days, one observation per hour each day.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for day := range 10 {
		for hour := range 24 {
			b, _ = b.Observe(fp, features.VolatileFeatures{}, start.AddDate(0, 0, day).Add(time.Duration(hour)*time.Hour))
		}
	}

	stats := b.Fingerprints[fp.ID]
	const uniformShare = 1.0 / 24.0
	for h := range 24 {
		if got := stats.HourActivity[h]; math.Abs(got-uniformShare) > 0.02 {
			t.Errorf("HourActivity[%d] = %v, want ~%v (uniform traffic across all hours)", h, got, uniformShare)
		}
	}
}

func readFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
		OperationName: "GET /customer", TargetName: "customer-db", Environment: "production",
	})
}

func updateFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
		OperationName: "PATCH /customer", TargetName: "customer-db", Environment: "production",
	})
}

func deleteFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
		OperationName: "DELETE /customer", TargetName: "customer-db", Environment: "production",
	})
}

func TestBaselineObserveFirstEventHasNoPredecessor(t *testing.T) {
	fpRead := readFingerprint()
	b := baseline.New(testKey)

	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, time.Now())

	if b.LastFingerprintID != fpRead.ID {
		t.Fatalf("LastFingerprintID = %q, want %q", b.LastFingerprintID, fpRead.ID)
	}
	if counts := b.Fingerprints[fpRead.ID].PredecessorCounts; counts != nil {
		t.Fatalf("PredecessorCounts = %v, want nil — the first-ever event has no predecessor to record", counts)
	}
}

func TestBaselineObserveRecordsTransitionBetweenDistinctFingerprints(t *testing.T) {
	fpRead, fpUpdate := readFingerprint(), updateFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	b, _ = b.Observe(fpUpdate, features.VolatileFeatures{}, now.Add(time.Second))

	if b.LastFingerprintID != fpUpdate.ID {
		t.Fatalf("LastFingerprintID = %q, want %q", b.LastFingerprintID, fpUpdate.ID)
	}
	if got := b.Fingerprints[fpUpdate.ID].PredecessorCounts[fpRead.ID]; got != 1 {
		t.Fatalf("PredecessorCounts[read] for update = %d, want 1", got)
	}
}

func TestBaselineObserveRecordsRepeatedSelfTransition(t *testing.T) {
	fpRead := readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	// read -> read -> read: a fingerprint can validly be its own
	// predecessor (a repeated action), and each transition into it
	// should count.
	for i := range 3 {
		b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second))
	}

	if got := b.Fingerprints[fpRead.ID].PredecessorCounts[fpRead.ID]; got != 2 {
		t.Fatalf("PredecessorCounts[read] for read = %d, want 2 (the 2nd and 3rd observations each follow a read)", got)
	}
}

func TestBaselineObserveOutOfOrderEventDoesNotRecordOrCorruptTransition(t *testing.T) {
	fpRead, fpUpdate, fpDelete := readFingerprint(), updateFingerprint(), deleteFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	b, _ = b.Observe(fpUpdate, features.VolatileFeatures{}, now.Add(2*time.Second))

	// A backdated event, timestamped before fpUpdate's own arrival,
	// must not be treated as following fpUpdate (it doesn't, in real
	// time), and must not overwrite LastFingerprintID/Time with its
	// own, earlier timestamp — that would corrupt the ordering for
	// whatever legitimately follows next.
	b, _ = b.Observe(fpDelete, features.VolatileFeatures{}, now.Add(1*time.Second))

	if got := b.Fingerprints[fpDelete.ID].PredecessorCounts; got != nil {
		t.Fatalf("out-of-order delete recorded a predecessor: %v, want nil", got)
	}
	if b.LastFingerprintID != fpUpdate.ID {
		t.Fatalf("LastFingerprintID = %q, want %q (out-of-order event must not overwrite it)", b.LastFingerprintID, fpUpdate.ID)
	}

	// The next legitimate, forward-timestamped event must still form
	// its transition from fpUpdate (the real predecessor), not fpDelete.
	fpRead2 := readFingerprint()
	b, _ = b.Observe(fpRead2, features.VolatileFeatures{}, now.Add(3*time.Second))
	if got := b.Fingerprints[fpRead2.ID].PredecessorCounts[fpUpdate.ID]; got != 1 {
		t.Fatalf("PredecessorCounts[update] for read = %d, want 1 (must follow the real predecessor, not the out-of-order delete)", got)
	}
}

// TestBaselineObservePredecessorCountsIsImmutable proves the copy-on-write
// discipline PredecessorCounts must uphold, the same way
// TestBaselineObserveIsImmutable already proves it for Fingerprints
// itself: a later Observe call must never retroactively change what an
// earlier Baseline snapshot (e.g. one a concurrent Store.Get caller
// still holds) reports.
func TestBaselineObservePredecessorCountsIsImmutable(t *testing.T) {
	fpRead, fpUpdate := readFingerprint(), updateFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	snapshot, _ := b.Observe(fpUpdate, features.VolatileFeatures{}, now.Add(time.Second))

	if got := snapshot.Fingerprints[fpUpdate.ID].PredecessorCounts[fpRead.ID]; got != 1 {
		t.Fatalf("snapshot PredecessorCounts[read] = %d, want 1", got)
	}

	// A further Observe on top of snapshot must not reach back and
	// mutate snapshot's own PredecessorCounts map.
	_, _ = snapshot.Observe(fpUpdate, features.VolatileFeatures{}, now.Add(2*time.Second))
	if got := snapshot.Fingerprints[fpUpdate.ID].PredecessorCounts[fpRead.ID]; got != 1 {
		t.Fatalf("a later Observe mutated an earlier snapshot's PredecessorCounts: got %d, want 1", got)
	}
}

func TestBaselineObservePredecessorCountsIsBounded(t *testing.T) {
	fpDest := deleteFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	// One more distinct predecessor than the bound allows.
	const overBound = 65
	for i := range overBound {
		pred := fingerprint.Compute(features.StableFeatures{
			ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
			OperationName: fmt.Sprintf("predecessor-%d", i), TargetName: "customer-db", Environment: "production",
		})
		b, _ = b.Observe(pred, features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second))
		b, _ = b.Observe(fpDest, features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second+time.Millisecond))
	}

	if got := len(b.Fingerprints[fpDest.ID].PredecessorCounts); got != 64 {
		t.Fatalf("len(PredecessorCounts) = %d, want 64 (bounded, one predecessor never tracked)", got)
	}
}

// TestBaselineObserveTracksDelegatorCounts is task 031's own signal
// foundation test, mirroring TestBaselineObserveTracksOutgoingTransitionTotal's
// shape.
func TestBaselineObserveTracksDelegatorCounts(t *testing.T) {
	fpRead := readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{DelegatedFrom: "agent-a"}, now)
	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{DelegatedFrom: "agent-a"}, now)
	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{DelegatedFrom: "agent-b"}, now)

	if got := b.DelegatorCounts["agent-a"]; got != 2 {
		t.Fatalf("DelegatorCounts[agent-a] = %d, want 2", got)
	}
	if got := b.DelegatorCounts["agent-b"]; got != 1 {
		t.Fatalf("DelegatorCounts[agent-b] = %d, want 1", got)
	}
	if got := len(b.DelegatorCounts); got != 2 {
		t.Fatalf("len(DelegatorCounts) = %d, want 2", got)
	}
}

// TestBaselineObserveMissingDelegationDoesNotUpdateDelegatorCounts is
// task 031's mandatory missing-delegation regression (§23/§48 of the
// task brief): an event with no DelegatedFrom must leave DelegatorCounts
// completely untouched — preserving non-agent/direct-action behavior.
func TestBaselineObserveMissingDelegationDoesNotUpdateDelegatorCounts(t *testing.T) {
	fpRead := readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)

	if got := len(b.DelegatorCounts); got != 0 {
		t.Fatalf("len(DelegatorCounts) = %d, want 0 for events with no DelegatedFrom", got)
	}
}

func TestBaselineObserveDelegatorCountsIsImmutable(t *testing.T) {
	fpRead := readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{DelegatedFrom: "agent-a"}, now)
	snapshot, _ := b.Observe(fpRead, features.VolatileFeatures{DelegatedFrom: "agent-a"}, now.Add(time.Second))

	if got := snapshot.DelegatorCounts["agent-a"]; got != 2 {
		t.Fatalf("snapshot DelegatorCounts[agent-a] = %d, want 2", got)
	}

	// A further Observe on top of snapshot must not reach back and
	// mutate snapshot's own DelegatorCounts map.
	_, _ = snapshot.Observe(fpRead, features.VolatileFeatures{DelegatedFrom: "agent-a"}, now.Add(2*time.Second))
	if got := snapshot.DelegatorCounts["agent-a"]; got != 2 {
		t.Fatalf("a later Observe mutated an earlier snapshot's DelegatorCounts: got %d, want 2", got)
	}
}

// TestBaselineObserveDelegatorCountsIsBounded is task 031's mandatory
// cardinality-attack regression (§16/§51 of the task brief): far more
// than maxDelegators distinct delegators must never grow DelegatorCounts
// past its bound, mirroring TestBaselineObservePredecessorCountsIsBounded's
// exact shape and scale.
func TestBaselineObserveDelegatorCountsIsBounded(t *testing.T) {
	fpRead := readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	// Far more distinct delegators than the bound allows — well beyond
	// the illustrative "one over the bound" case
	// TestBaselineObservePredecessorCountsIsBounded uses, matching this
	// task's own explicit request for a cardinality-attack-shaped test
	// (a reasonable size, not 100,000 events, per the brief's own
	// guidance).
	const distinctDelegators = 200
	for i := range distinctDelegators {
		b, _ = b.Observe(fpRead, features.VolatileFeatures{DelegatedFrom: fmt.Sprintf("delegator-%d", i)}, now.Add(time.Duration(i)*time.Second))
	}

	if got := len(b.DelegatorCounts); got != 64 {
		t.Fatalf("len(DelegatorCounts) = %d, want 64 (bounded, even with %d distinct delegators observed)", got, distinctDelegators)
	}
}

func TestBaselineObserveTracksOutgoingTransitionTotal(t *testing.T) {
	fpRead, fpUpdate, fpDelete := readFingerprint(), updateFingerprint(), deleteFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	// read -> update, read -> update, read -> delete: read is the
	// predecessor in all three transitions of interest. Note that
	// update also becomes a predecessor itself twice, implicitly,
	// every time the sequence returns to read — this is real,
	// correct behavior (update -> read is just as much a valid
	// transition as read -> update), not a test artifact to avoid; the
	// assertions below account for it explicitly rather than assuming
	// update is never a predecessor.
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	b, _ = b.Observe(fpUpdate, features.VolatileFeatures{}, now) // read -> update
	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now) // update -> read
	now = now.Add(time.Second)
	b, _ = b.Observe(fpUpdate, features.VolatileFeatures{}, now) // read -> update
	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now) // update -> read
	now = now.Add(time.Second)
	b, _ = b.Observe(fpDelete, features.VolatileFeatures{}, now) // read -> delete

	if got := b.Fingerprints[fpRead.ID].OutgoingTransitionTotal; got != 3 {
		t.Fatalf("read.OutgoingTransitionTotal = %d, want 3 (read -> update twice, read -> delete once)", got)
	}
	if got := b.Fingerprints[fpUpdate.ID].PredecessorCounts[fpRead.ID]; got != 2 {
		t.Fatalf("PredecessorCounts[read] for update = %d, want 2", got)
	}
	if got := b.Fingerprints[fpDelete.ID].PredecessorCounts[fpRead.ID]; got != 1 {
		t.Fatalf("PredecessorCounts[read] for delete = %d, want 1", got)
	}
	// update -> read happened twice, so update genuinely is a
	// predecessor twice — not zero.
	if got := b.Fingerprints[fpUpdate.ID].OutgoingTransitionTotal; got != 2 {
		t.Fatalf("update.OutgoingTransitionTotal = %d, want 2 (update -> read happened twice)", got)
	}
	// delete was never a predecessor: it's the last event observed.
	if got := b.Fingerprints[fpDelete.ID].OutgoingTransitionTotal; got != 0 {
		t.Fatalf("delete.OutgoingTransitionTotal = %d, want 0 (delete is the last event; nothing follows it)", got)
	}
}

func TestBaselineObserveOutgoingTransitionTotalSelfTransition(t *testing.T) {
	fpRead := readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	// read -> read -> read: two valid self-transitions.
	for i := range 3 {
		b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second))
	}

	stats := b.Fingerprints[fpRead.ID]
	if stats.OutgoingTransitionTotal != 2 {
		t.Errorf("OutgoingTransitionTotal = %d, want 2", stats.OutgoingTransitionTotal)
	}
	if stats.PredecessorCounts[fpRead.ID] != 2 {
		t.Errorf("PredecessorCounts[read] = %d, want 2", stats.PredecessorCounts[fpRead.ID])
	}
}

func TestBaselineObserveOutOfOrderEventDoesNotIncrementOutgoingTotal(t *testing.T) {
	fpRead, fpUpdate, fpDelete := readFingerprint(), updateFingerprint(), deleteFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	b, _ = b.Observe(fpUpdate, features.VolatileFeatures{}, now.Add(2*time.Second))

	// Backdated relative to fpUpdate's own arrival: not a valid
	// transition from fpUpdate.
	b, _ = b.Observe(fpDelete, features.VolatileFeatures{}, now.Add(1*time.Second))

	if got := b.Fingerprints[fpUpdate.ID].OutgoingTransitionTotal; got != 0 {
		t.Fatalf("update.OutgoingTransitionTotal = %d, want 0 (the out-of-order delete must not count as a valid outgoing transition from update)", got)
	}
}

// TestBaselineObserveOutgoingTransitionTotalIsImmutable proves a later
// Observe call never retroactively changes an earlier Baseline
// snapshot's OutgoingTransitionTotal — the same guarantee
// TestBaselineObservePredecessorCountsIsImmutable already proves for
// PredecessorCounts, restated here for the new counter (a plain
// uint64 field is copied by value on every struct assignment, so this
// mainly documents the guarantee rather than catching a map-aliasing
// bug the way the PredecessorCounts test did — see
// observeOutgoingTransition's own doc comment).
func TestBaselineObserveOutgoingTransitionTotalIsImmutable(t *testing.T) {
	fpRead, fpUpdate := readFingerprint(), updateFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	snapshot, _ := b.Observe(fpUpdate, features.VolatileFeatures{}, now.Add(time.Second))

	if got := snapshot.Fingerprints[fpRead.ID].OutgoingTransitionTotal; got != 1 {
		t.Fatalf("snapshot read.OutgoingTransitionTotal = %d, want 1", got)
	}

	_, _ = snapshot.Observe(fpRead, features.VolatileFeatures{}, now.Add(2*time.Second))
	if got := snapshot.Fingerprints[fpRead.ID].OutgoingTransitionTotal; got != 1 {
		t.Fatalf("a later Observe mutated an earlier snapshot's OutgoingTransitionTotal: got %d, want 1", got)
	}
}

// TestBaselineObserveManyDistinctTransitionsStayBounded is task 026's
// own large-cardinality proof for the transition counters specifically:
// many distinct predecessors, all
// transitioning to one shared destination, must not panic, and the
// destination's own PredecessorCounts must stay at exactly
// maxPredecessors (64) — the bound established by task 025 is
// unaffected by, and sufficient for, task 026's new
// OutgoingTransitionTotal counter, which introduces no new
// cardinality-sensitive structure of its own (it is one scalar per
// existing FingerprintStats entry, not a new map).
func TestBaselineObserveManyDistinctTransitionsStayBounded(t *testing.T) {
	fpDest := deleteFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	// 5,000 matches this codebase's existing scale precedent for this
	// kind of test (TestObserveUnboundedFingerprintsDoesNotPanic in
	// engine_test.go) — comfortably beyond maxPredecessors (64), and
	// enough to prove boundedness, without the O(N^2) copy-on-write
	// cost this style of test carries (see Baseline.Observe's own doc
	// comment) making the suite unreasonably slow for a "10,000" figure
	// that was illustrative in task 026's own brief, not a hard
	// requirement.
	const distinctPredecessors = 5000
	admittedAll := true
	for i := range distinctPredecessors {
		pred := fingerprint.Compute(features.StableFeatures{
			ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
			OperationName: fmt.Sprintf("predecessor-%d", i), TargetName: "customer-db", Environment: "production",
		})
		b, _ = b.Observe(pred, features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second))
		b, _ = b.Observe(fpDest, features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second+time.Millisecond))

		// Every predecessor this baseline actually admitted has
		// OutgoingTransitionTotal exactly 1, regardless of whether the
		// destination's PredecessorCounts had room to track it — the
		// destination-side cap (maxPredecessors) never suppresses the
		// predecessor-side counter. Predecessors beyond the fingerprint
		// admission bound (task 046) are not learned at all, which is a
		// different mechanism and is asserted separately below.
		stats, admitted := b.Fingerprints[pred.ID]
		if admitted && stats.OutgoingTransitionTotal != 1 {
			t.Fatalf("predecessor %d: OutgoingTransitionTotal = %d, want 1", i, stats.OutgoingTransitionTotal)
		}
		if !admitted {
			admittedAll = false
		}
	}

	if got := len(b.Fingerprints[fpDest.ID].PredecessorCounts); got != 64 {
		t.Fatalf("len(PredecessorCounts) = %d, want 64 (bounded even at %d distinct predecessors)", got, distinctPredecessors)
	}
	// Fingerprints is itself bounded as of task 046: this stream is far
	// past that bound, so admission stopped long before the loop ended.
	if admittedAll {
		t.Fatalf("every one of %d distinct predecessors was admitted; fingerprint admission is supposed to be bounded", distinctPredecessors)
	}
	if got := len(b.Fingerprints); got != 512 {
		t.Fatalf("len(Fingerprints) = %d, want 512 (bounded at %d distinct predecessors)", got, distinctPredecessors)
	}
}

// --- Task 027: bounded 3-gram behavioral context ---

// TestBaselineObserveTracksPreviousFingerprintIDWindow proves the
// two-element history window shifts forward by exactly one fingerprint
// on every valid advance. Note carefully what "insufficient history"
// (task 027 §17's cold-start example) actually refers to: it is a
// property of what state is *read* when scoring the Nth event — and
// Analyze always reads the Baseline as it stood after the (N-1)th
// Observe call. So after this test's 2nd raw Observe call, Previous is
// already populated (fpAuth) — that is precisely the state a 3rd
// Analyze call will read, which is what makes the 3rd event's 3-gram
// complete. "Insufficient history" describes events #1 and #2
// themselves (what gets *read* when scoring them: Previous=="" in
// both cases — see TestBaselineObserveNoTrigramBeforeThirdObservation),
// not the Baseline state immediately following any particular Observe
// call.
func TestBaselineObserveTracksPreviousFingerprintIDWindow(t *testing.T) {
	fpAuth, fpRead, fpExport := authenticateFingerprint(), readFingerprint(), exportFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
	if b.PreviousFingerprintID != "" {
		t.Fatalf("after 1st observation: PreviousFingerprintID = %q, want \"\" (this actor's first-ever observation)", b.PreviousFingerprintID)
	}
	if b.LastFingerprintID != fpAuth.ID {
		t.Fatalf("after 1st observation: LastFingerprintID = %q, want %q", b.LastFingerprintID, fpAuth.ID)
	}

	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	if b.PreviousFingerprintID != fpAuth.ID {
		t.Fatalf("after 2nd observation: PreviousFingerprintID = %q, want %q (the window shifted: old Last becomes the new Previous)", b.PreviousFingerprintID, fpAuth.ID)
	}
	if b.LastFingerprintID != fpRead.ID {
		t.Fatalf("after 2nd observation: LastFingerprintID = %q, want %q", b.LastFingerprintID, fpRead.ID)
	}

	now = now.Add(time.Second)
	b, _ = b.Observe(fpExport, features.VolatileFeatures{}, now)
	if b.PreviousFingerprintID != fpRead.ID {
		t.Fatalf("after 3rd observation: PreviousFingerprintID = %q, want %q (window shifted forward again)", b.PreviousFingerprintID, fpRead.ID)
	}
	if b.LastFingerprintID != fpExport.ID {
		t.Fatalf("after 3rd observation: LastFingerprintID = %q, want %q", b.LastFingerprintID, fpExport.ID)
	}
}

func TestBaselineObserveNoTrigramBeforeThirdObservation(t *testing.T) {
	fpAuth, fpRead := authenticateFingerprint(), readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)

	if got := b.Fingerprints[fpRead.ID].TrigramCounts; got != nil {
		t.Fatalf("TrigramCounts after only 2 observations = %v, want nil (not enough history for a 3-gram yet)", got)
	}
	if got := b.Fingerprints[fpAuth.ID].TrigramContinuationTotal; got != nil {
		t.Fatalf("TrigramContinuationTotal after only 2 observations = %v, want nil", got)
	}
}

func TestBaselineObserveRecordsTrigram(t *testing.T) {
	fpAuth, fpRead, fpExport := authenticateFingerprint(), readFingerprint(), exportFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
	now = now.Add(time.Second)
	b, _ = b.Observe(fpExport, features.VolatileFeatures{}, now)

	key := baseline.TrigramKey{First: fpAuth.ID, Second: fpRead.ID}
	if got := b.Fingerprints[fpExport.ID].TrigramCounts[key]; got != 1 {
		t.Fatalf("TrigramCounts[{auth,read}] for export = %d, want 1", got)
	}
	if got := b.Fingerprints[fpRead.ID].TrigramContinuationTotal[fpAuth.ID]; got != 1 {
		t.Fatalf("TrigramContinuationTotal[auth] for read = %d, want 1", got)
	}
}

func TestBaselineObserveRecordsRepeatedTrigram(t *testing.T) {
	fpAuth, fpRead, fpExport := authenticateFingerprint(), readFingerprint(), exportFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	for range 5 {
		b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		b, _ = b.Observe(fpExport, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}

	key := baseline.TrigramKey{First: fpAuth.ID, Second: fpRead.ID}
	if got := b.Fingerprints[fpExport.ID].TrigramCounts[key]; got != 5 {
		t.Fatalf("TrigramCounts[{auth,read}] for export = %d, want 5", got)
	}
	if got := b.Fingerprints[fpRead.ID].TrigramContinuationTotal[fpAuth.ID]; got != 5 {
		t.Fatalf("TrigramContinuationTotal[auth] for read = %d, want 5", got)
	}
}

// TestBaselineObserveRepeatedFingerprintTrigram proves A -> A -> A (and
// A -> A -> B) are handled correctly: a fingerprint validly repeating
// as its own predecessor and grandparent is not deduplicated or
// special-cased away.
func TestBaselineObserveRepeatedFingerprintTrigram(t *testing.T) {
	fpA, fpB := readFingerprint(), updateFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	// A -> A -> A
	for range 3 {
		b, _ = b.Observe(fpA, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}
	selfKey := baseline.TrigramKey{First: fpA.ID, Second: fpA.ID}
	if got := b.Fingerprints[fpA.ID].TrigramCounts[selfKey]; got != 1 {
		t.Fatalf("A->A->A: TrigramCounts[{A,A}] for A = %d, want 1 (the 3rd observation is the only complete A,A->A 3-gram)", got)
	}

	// Continue: A -> A -> A -> B (the 4th observation completes a
	// second, different 3-gram: A,A -> B).
	b, _ = b.Observe(fpB, features.VolatileFeatures{}, now)
	abKey := baseline.TrigramKey{First: fpA.ID, Second: fpA.ID}
	if got := b.Fingerprints[fpB.ID].TrigramCounts[abKey]; got != 1 {
		t.Fatalf("A->A->A->B: TrigramCounts[{A,A}] for B = %d, want 1", got)
	}
	if got := b.Fingerprints[fpA.ID].TrigramContinuationTotal[fpA.ID]; got != 2 {
		t.Fatalf("TrigramContinuationTotal[A] for A = %d, want 2 (A,A->A and A,A->B)", got)
	}
}

// TestBaselineObserveOutOfOrderEventDoesNotRecordOrCorruptTrigram
// mirrors TestBaselineObserveOutOfOrderEventDoesNotRecordOrCorruptTransition
// one level up: a backdated event must not shift the
// Previous/LastFingerprintID window, must not be scored as completing
// a 3-gram, and must not corrupt what the *next* legitimate event's
// 3-gram is measured against.
func TestBaselineObserveOutOfOrderEventDoesNotRecordOrCorruptTrigram(t *testing.T) {
	fpAuth, fpRead, fpExport, fpDelete := authenticateFingerprint(), readFingerprint(), exportFingerprint(), deleteFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now.Add(2*time.Second))

	// Backdated relative to fpRead's own arrival.
	b, _ = b.Observe(fpDelete, features.VolatileFeatures{}, now.Add(1*time.Second))

	if b.PreviousFingerprintID != fpAuth.ID || b.LastFingerprintID != fpRead.ID {
		t.Fatalf("out-of-order event corrupted the history window: Previous=%q, Last=%q, want Previous=%q, Last=%q",
			b.PreviousFingerprintID, b.LastFingerprintID, fpAuth.ID, fpRead.ID)
	}
	if got := b.Fingerprints[fpDelete.ID].TrigramCounts; got != nil {
		t.Fatalf("out-of-order delete recorded a 3-gram: %v, want nil", got)
	}

	// The next legitimate event must complete its 3-gram from
	// (fpAuth, fpRead) — the real history — not from the out-of-order
	// fpDelete.
	b, _ = b.Observe(fpExport, features.VolatileFeatures{}, now.Add(3*time.Second))
	key := baseline.TrigramKey{First: fpAuth.ID, Second: fpRead.ID}
	if got := b.Fingerprints[fpExport.ID].TrigramCounts[key]; got != 1 {
		t.Fatalf("TrigramCounts[{auth,read}] for export = %d, want 1 (must follow the real history, not the out-of-order delete)", got)
	}
}

// TestBaselineObserveEqualTimestampsDoNotAdvanceHistory pins down
// deterministic behavior for equal timestamps (task 027 §23): the
// existing ordering guard is now.After(LastFingerprintTime), which is
// false for an equal timestamp, so an equal-timestamp event is treated
// identically to an out-of-order one — deterministically, not as a
// special/nondeterministic case.
func TestBaselineObserveEqualTimestampsDoNotAdvanceHistory(t *testing.T) {
	fpAuth, fpRead := authenticateFingerprint(), readFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now) // same timestamp as fpAuth

	if b.LastFingerprintID != fpAuth.ID {
		t.Fatalf("LastFingerprintID = %q, want %q (an equal timestamp must not advance the window)", b.LastFingerprintID, fpAuth.ID)
	}
	if got := b.Fingerprints[fpRead.ID].PredecessorCounts; got != nil {
		t.Fatalf("PredecessorCounts for the equal-timestamp event = %v, want nil", got)
	}
}

// TestBaselineObserveTrigramCountsIsImmutable proves the copy-on-write
// discipline TrigramCounts must uphold, mirroring
// TestBaselineObservePredecessorCountsIsImmutable one level up.
func TestBaselineObserveTrigramCountsIsImmutable(t *testing.T) {
	fpAuth, fpRead, fpExport := authenticateFingerprint(), readFingerprint(), exportFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now.Add(time.Second))
	snapshot, _ := b.Observe(fpExport, features.VolatileFeatures{}, now.Add(2*time.Second))

	key := baseline.TrigramKey{First: fpAuth.ID, Second: fpRead.ID}
	if got := snapshot.Fingerprints[fpExport.ID].TrigramCounts[key]; got != 1 {
		t.Fatalf("snapshot TrigramCounts[{auth,read}] = %d, want 1", got)
	}

	// A further Observe on top of snapshot must not reach back and
	// mutate snapshot's own TrigramCounts map.
	_, _ = snapshot.Observe(fpExport, features.VolatileFeatures{}, now.Add(3*time.Second))
	if got := snapshot.Fingerprints[fpExport.ID].TrigramCounts[key]; got != 1 {
		t.Fatalf("a later Observe mutated an earlier snapshot's TrigramCounts: got %d, want 1", got)
	}
}

// TestBaselineObserveTrigramContinuationTotalIsImmutable mirrors the
// above for TrigramContinuationTotal.
func TestBaselineObserveTrigramContinuationTotalIsImmutable(t *testing.T) {
	fpAuth, fpRead, fpExport := authenticateFingerprint(), readFingerprint(), exportFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	b, _ = b.Observe(fpAuth, features.VolatileFeatures{}, now)
	b, _ = b.Observe(fpRead, features.VolatileFeatures{}, now.Add(time.Second))
	snapshot, _ := b.Observe(fpExport, features.VolatileFeatures{}, now.Add(2*time.Second))

	if got := snapshot.Fingerprints[fpRead.ID].TrigramContinuationTotal[fpAuth.ID]; got != 1 {
		t.Fatalf("snapshot TrigramContinuationTotal[auth] = %d, want 1", got)
	}

	_, _ = snapshot.Observe(fpExport, features.VolatileFeatures{}, now.Add(3*time.Second))
	if got := snapshot.Fingerprints[fpRead.ID].TrigramContinuationTotal[fpAuth.ID]; got != 1 {
		t.Fatalf("a later Observe mutated an earlier snapshot's TrigramContinuationTotal: got %d, want 1", got)
	}
}

// TestBaselineObserveTrigramCountsIsBounded proves TrigramCounts is
// bounded independently of PredecessorCounts's own cap — see
// maxTrigramPredecessors's doc comment for why this cannot be assumed
// "for free." Many distinct (grandparent, predecessor) pairs, sharing
// the same immediate predecessor (so PredecessorCounts itself stays
// tiny — one entry), all feeding into one shared destination.
func TestBaselineObserveTrigramCountsIsBounded(t *testing.T) {
	fpDest := deleteFingerprint()
	predecessor := updateFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	const overBound = 65
	for i := range overBound {
		grandparent := fingerprint.Compute(features.StableFeatures{
			ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
			OperationName: fmt.Sprintf("grandparent-%d", i), TargetName: "customer-db", Environment: "production",
		})
		b, _ = b.Observe(grandparent, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		b, _ = b.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		b, _ = b.Observe(fpDest, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}

	if got := len(b.Fingerprints[fpDest.ID].TrigramCounts); got != 64 {
		t.Fatalf("len(TrigramCounts) = %d, want 64 (bounded, one pair never tracked)", got)
	}
	// PredecessorCounts for the destination stays at exactly 1 (always
	// the same immediate predecessor), proving TrigramCounts's bound is
	// independent, not inherited.
	if got := len(b.Fingerprints[fpDest.ID].PredecessorCounts); got != 1 {
		t.Fatalf("len(PredecessorCounts) = %d, want 1 (same predecessor every time)", got)
	}
}

// TestBaselineObserveTrigramContinuationTotalIsBounded mirrors the
// above for TrigramContinuationTotal: many distinct grandparents,
// sharing the same immediate predecessor, must not grow that
// predecessor's TrigramContinuationTotal map without bound.
func TestBaselineObserveTrigramContinuationTotalIsBounded(t *testing.T) {
	predecessor := updateFingerprint()
	dest := deleteFingerprint()
	b := baseline.New(testKey)
	now := time.Now()

	const overBound = 65
	for i := range overBound {
		grandparent := fingerprint.Compute(features.StableFeatures{
			ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
			OperationName: fmt.Sprintf("grandparent-%d", i), TargetName: "customer-db", Environment: "production",
		})
		b, _ = b.Observe(grandparent, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		b, _ = b.Observe(predecessor, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
		b, _ = b.Observe(dest, features.VolatileFeatures{}, now)
		now = now.Add(time.Second)
	}

	if got := len(b.Fingerprints[predecessor.ID].TrigramContinuationTotal); got != 64 {
		t.Fatalf("len(TrigramContinuationTotal) = %d, want 64 (bounded, one grandparent never tracked)", got)
	}
}

// TestTrigramKeyTextRoundTrip proves baseline.TrigramKey's
// MarshalText/UnmarshalText — needed purely so store.FileStore's
// encoding/json-based persistence can use it as a map key — correctly
// round-trips, including a value whose fields are themselves real
// Fingerprint.ID hex strings.
func TestTrigramKeyTextRoundTrip(t *testing.T) {
	want := baseline.TrigramKey{First: authenticateFingerprint().ID, Second: readFingerprint().ID}

	text, err := want.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}

	var got baseline.TrigramKey
	if err := got.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText(%q) error = %v", text, err)
	}
	if got != want {
		t.Fatalf("round-tripped TrigramKey = %+v, want %+v", got, want)
	}
}

func authenticateFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
		OperationName: "POST /authenticate", TargetName: "auth-service", Environment: "production",
	})
}

func exportFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
		OperationName: "GET /customer/export", TargetName: "customer-db", Environment: "production",
	})
}
