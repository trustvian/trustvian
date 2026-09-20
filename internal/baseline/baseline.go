// Package baseline is the statistical model of an actor's expected
// behavior: for each Fingerprint an actor has produced, how often it's
// been seen, and — when volatile signals are present — its typical latency
// and error rate. Anomaly detection (internal/anomaly) compares a new
// event's Fingerprint and VolatileFeatures against this model.
//
// Baseline is a pure, immutable value type: Observe never mutates the
// receiver, it returns a new Baseline reflecting the update. This makes a
// Baseline value returned to a caller (e.g. by internal/store) a safe,
// permanently-valid snapshot — no lock needs to be held while it's read.
// Concurrency-safe storage and mutation of the "current" Baseline for a
// given Key is internal/store's job, not this package's.
package baseline

import (
	"fmt"
	"maps"
	"math"
	"strings"
	"time"

	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
)

// emaAlpha is the exponential-moving-average smoothing factor used for
// latency mean/variance and error rate. It reconciles two requirements:
// O(1)-memory streaming statistics (no raw samples retained, in the spirit
// of Welford's online algorithm) and decay, so that older observations
// progressively lose influence and legitimate behavioral drift is absorbed
// without an unbounded all-time window or a manual reset.
//
// 0.2 favors the last ~5-10 observations, a reasonable default until
// production data justifies tuning it.
const emaAlpha = 0.2

// hourActivityAlpha is the EWMA smoothing factor for HourActivity —
// deliberately much slower than emaAlpha, not an arbitrary second
// constant. Every observation updates all 24 HourActivity buckets (see
// observe), but only one of them matches the current hour; the other
// 23 only ever decay. With emaAlpha's 0.2, a bucket that is hit exactly
// once every 24 observations (uniform hour-of-day traffic — the
// textbook "no real time pattern" case) decays to ~0.2% of its peak
// value between hits and rebounds to ~20% right after one, a >200x
// swing depending purely on *when* it happens to be read relative to
// its own last hit — which would make even genuinely patternless
// traffic look sharply time-anomalous, purely as a measurement-phase
// artifact, not a real behavioral signal
// (TestFingerprintStatsHourActivityUniformTraffic pins this down: it
// failed under emaAlpha before this constant was introduced).
// hourActivityAlpha=0.02 keeps that same 24-step round-trip decay to
// within about ±0.01 of the true 1/24 uniform share — small enough
// that the anomaly signal built on it (see internal/anomaly) reads as
// only mildly, not severely, anomalous for uniform traffic, and small
// enough in absolute terms to be a non-issue given the signal ships
// with a zero default weight (see anomaly.Config.TimePatternWeight)
// until an operator has calibrated it against real traffic anyway.
// The tradeoff is slower adaptation to genuine hour-of-day drift
// (effective memory of roughly 100 observations, vs. emaAlpha's ~9) —
// appropriate here, since a real hour-of-day pattern is a weeks-scale
// phenomenon, not something that should shift on the last 5-10 calls.
const hourActivityAlpha = 0.02

// maxPredecessors bounds FingerprintStats.PredecessorCounts: the number
// of distinct predecessor Fingerprint.IDs tracked for one destination
// fingerprint. This is a resource-exhaustion bound, not a statistical
// one — Fingerprints itself (the map this bounds a per-entry map
// within) is already unbounded by design (see
// TestObserveUnboundedFingerprintsDoesNotPanic in engine_test.go and
// docs/SECURITY.md § Resource exhaustion), but a per-destination
// predecessor map compounds that: an attacker who can vary the
// *previous* fingerprint on every call (trivial — Fingerprint.ID is
// derived from Event fields the caller controls) could otherwise grow
// one FingerprintStats entry's PredecessorCounts without bound. 64 is
// generous for real traffic — a real actor's distinct predecessor
// actions are typically a handful to a few dozen (an actor's own
// behavioral repertoire, not per-event entropy) — while keeping the
// worst case (64 entries × a small fixed key/value size) trivial
// memory, matching the "bounded, not unlimited" mandate for any new
// sequence-aware state (see docs/adr/0010-bounded-process-local-sequence-state.md).
const maxPredecessors = 64

// maxTrigramPredecessors independently bounds FingerprintStats.TrigramCounts
// (distinct (First,Second) predecessor pairs tracked per destination)
// and FingerprintStats.TrigramContinuationTotal (distinct First
// grandparent entries tracked per immediate-predecessor fingerprint).
//
// This cannot simply inherit maxPredecessors's existing bound "for
// free": every key ever added to either new map corresponds to some
// distinct grandparent Fingerprint.ID that validly preceded a
// *predecessor* fingerprint at some point in the actor's history — a
// caller-controlled value (Fingerprint.ID derives from Event fields the
// caller controls), and the set of *distinct grandparents* that have
// ever led into one particular predecessor is not bounded by
// PredecessorCounts's own cap on that predecessor's *own* incoming
// entries (recordPredecessor's bound rejection still lets
// Baseline.Observe's PreviousFingerprintID/LastFingerprintID shift
// proceed — see Observe — so a grandparent can become "Previous" and
// later drive a TrigramContinuationTotal/TrigramCounts update even on a
// call where recordPredecessor itself rejected adding that same value
// to PredecessorCounts for being past the cap). Both new maps need
// their own explicit, independently-enforced bound for exactly the
// resource-exhaustion reason maxPredecessors exists at all — see
// docs/adr/0012-bounded-trigram-behavioral-context.md.
//
// 64 (matching maxPredecessors's own value) is reused deliberately, not
// coincidentally: it is the same "generous for real traffic, trivial
// worst-case memory" reasoning maxPredecessors already established,
// applied to a structurally analogous bound rather than an
// independently invented number.
const maxTrigramPredecessors = 64

// maxDelegators bounds Baseline.DelegatorCounts: the number of
// distinct immediate-delegator Actor.IDs tracked per actor+environment
// Baseline. This is a resource-exhaustion bound, the identical role
// maxPredecessors plays for PredecessorCounts, applied one level up —
// Baseline itself, not a single FingerprintStats entry, since
// delegation provenance is a property of the actor being delegated
// to, not of any one operation it performs (see
// docs/tasks/031-delegation-behavioral-semantics.md's orientation
// decision). An attacker who controls Context.DelegatedFrom (it is
// self-reported, unauthenticated input — see ADR 0016) could otherwise
// grow this map without bound by varying it on every event. 64 reuses
// maxPredecessors's own "generous for real traffic, trivial worst-case
// memory" value deliberately, not coincidentally — a real actor's
// distinct delegators are typically a handful, not per-event entropy —
// but is its own independent constant, not a shared one, for the same
// reason maxTrigramPredecessors is its own constant rather than
// reusing maxPredecessors directly: one bound must never be assumed to
// imply another.
const maxDelegators = 64

// maxFingerprints bounds Baseline.Fingerprints: the number of distinct
// fingerprint identities one actor's baseline will ever learn. Unlike
// the three bounds above — which cap a map *inside* one FingerprintStats
// entry — this caps the outer map, and it is the bound whose absence
// made every figure derived from the others incomplete: a baseline with
// unbounded fingerprints has unbounded size no matter how tightly each
// entry is capped.
//
// Fingerprint.ID is derived from Event fields the caller supplies, so
// fingerprint cardinality is an untrusted-input dimension exactly like
// PredecessorCounts's keys are. Without this bound, one actor emitting a
// distinct operation name per call grows its baseline — and, through
// internal/store/postgres's one-jsonb-row-per-actor layout, its database
// row — without limit.
//
// 512, from the one measurement this repository actually has: a baseline
// driven past every other cap with 120 distinct fingerprints serializes
// to ~13 KB (TestLargeBoundedBaselineRoundTrips), which is ~110 bytes of
// serialized state per fingerprint. 512 therefore bounds a baseline at
// roughly 56 KB — large enough for a service with a genuinely wide route
// surface to be learned in full, small enough that even a pathological
// actor population stays predictable. It is deliberately much larger
// than the 64 above: those bound an actor's *repertoire relative to one
// other action*, while this bounds its entire distinct behavior set, and
// a real API service has far more distinct routes than it has distinct
// predecessors for any single one of them.
//
// Admission, not eviction: see Observe. Nothing learned is ever removed
// to make room, because a fingerprint absent from Fingerprints scores as
// maximally novel with zero Confidence (internal/anomaly.Score), which
// contributes nothing to Trust — so evicting learned state would let a
// flood of manufactured fingerprints *suppress* detection for an actor
// rather than merely cost memory. See
// docs/adr/0019-bounded-fingerprint-admission.md.
const maxFingerprints = 512

// TrigramKey identifies the two-fingerprint predecessor pair leading
// into one destination FingerprintStats' TrigramCounts entry — the
// destination itself is implicit (whichever FingerprintStats.TrigramCounts
// map holds this key), exactly like PredecessorCounts's single-predecessor
// key already leaves its destination implicit. First is the fingerprint
// two steps back in the actor's observed order (Baseline.PreviousFingerprintID
// at the time this trigram was recorded); Second is the fingerprint one
// step back (Baseline.LastFingerprintID) — see
// docs/adr/0012-bounded-trigram-behavioral-context.md for why a
// two-field comparable struct is used here instead of a delimited
// string: constructing and comparing a TrigramKey value on the
// anomaly-scoring hot path allocates nothing, where a
// concatenated-string key would allocate on every single lookup.
type TrigramKey struct {
	First  string
	Second string
}

// trigramKeySeparator joins TrigramKey's two fields for
// store.FileStore's encoding/json-based persistence only — encoding/json
// requires a map's key type to be a string/integer kind or implement
// encoding.TextMarshaler; a plain struct key does not qualify on its
// own. "|" is safe as a separator because Fingerprint.ID is always a
// lowercase hex string (strconv.FormatUint(hash, 16) — see
// internal/fingerprint.Compute), which structurally cannot contain "|".
// This encoding is a persistence-format detail; TrigramKey's in-memory
// identity (used for every map lookup/construction in
// internal/anomaly) is the plain two-field struct, never this string.
const trigramKeySeparator = "|"

// MarshalText implements encoding.TextMarshaler, letting
// encoding/json use TrigramKey as a map key (see trigramKeySeparator's
// own doc comment for why this is needed at all).
func (k TrigramKey) MarshalText() ([]byte, error) {
	return []byte(k.First + trigramKeySeparator + k.Second), nil
}

// UnmarshalText implements encoding.TextUnmarshaler, the inverse of
// MarshalText.
func (k *TrigramKey) UnmarshalText(text []byte) error {
	first, second, ok := strings.Cut(string(text), trigramKeySeparator)
	if !ok {
		return fmt.Errorf("baseline: invalid TrigramKey encoding %q", text)
	}
	k.First, k.Second = first, second
	return nil
}

// Key identifies exactly one learned behavioral history: a single actor,
// within a single deployment environment, within a single learning
// scope. Scoping by environment from the start — even though the OSS
// engine is single-tenant — keeps the data model tenant-shaped, so a
// future multi-tenant Enterprise feature is an access-control addition,
// not a data migration.
//
// Scope is the outermost partition: an opaque namespace that lets one
// actor in one environment hold several independent histories. Nothing
// here parses it, and it carries no meaning the engine can act on — a
// caller chooses it, and identical events in different scopes produce
// identical Fingerprints and differ only in the learned evidence they
// are compared against. The empty string is the default scope, which is
// what every Key deserialized from a pre-scope snapshot or database row
// already is.
//
// Scope describes *which history an observation belongs to*, never
// *what the actor did*. That separation is the whole of
// docs/adr/0024-learning-scope-is-a-baseline-key-dimension.md: a scope
// that leaked into behavioral identity would make an actor's entire
// repertoire look brand new every time a caller opened a new one.
//
// It also never comes from an Event. Scope is Engine configuration, so
// whoever emits telemetry cannot choose which learned profile they
// train — see docs/SECURITY.md § Learning scope is a trust boundary.
type Key struct {
	Scope       string
	ActorID     string
	Environment string
}

// FingerprintStats is the learned statistical profile for one Fingerprint:
// how many times it's been observed, and — when available — its latency
// and error-rate behavior.
type FingerprintStats struct {
	// Count is the maturity counter: the number of times this Fingerprint
	// has been observed. Callers (anomaly detection) decide what count
	// constitutes "known" or "mature" — this package only counts.
	Count uint64

	FirstObserved time.Time

	// LastObserved is the latest observation timestamp seen for this
	// Fingerprint. It advances monotonically: an observation whose
	// timestamp precedes it (clock skew, out-of-order delivery) is still
	// counted, but does not pull LastObserved backwards. See observe.
	LastObserved time.Time

	// LatencyObservations is the subset of Count that carried latency
	// data (VolatileFeatures.HasLatency). LatencyMean/LatencyVariance are
	// meaningless when this is zero.
	LatencyObservations uint64
	LatencyMean         float64 // EWMA mean latency, in nanoseconds
	LatencyVariance     float64 // EWMA variance, in nanoseconds^2

	// IntervalObservations is the number of times an inter-observation
	// interval has been recorded for this Fingerprint. It is at most
	// Count-1: the first observation has no prior LastObserved to measure
	// from, and observations that do not strictly follow LastObserved are
	// skipped entirely (see observe). IntervalMean/IntervalVariance are
	// meaningless when this is zero.
	IntervalObservations uint64
	IntervalMean         float64 // EWMA mean inter-observation interval, in nanoseconds
	IntervalVariance     float64 // EWMA variance, in nanoseconds^2

	// ErrorRate is the EWMA-smoothed proportion of observations that
	// carried an error, in [0,1].
	ErrorRate float64

	// TimePatternObservations counts every observation this
	// FingerprintStats has recorded an hour-of-day sample for — always
	// equal to Count going forward, but tracked separately so a
	// FingerprintStats loaded from a persisted file written before this
	// field existed (HourActivity unmarshals to its zero value) is
	// correctly treated as immature for this specific signal, exactly
	// like a brand-new fingerprint, rather than as fully mature with a
	// suspiciously empty distribution. See internal/anomaly's
	// time-pattern signal, gated on this field rather than on Count.
	TimePatternObservations uint64
	// HourActivity is an EWMA-smoothed distribution over UTC
	// hour-of-day (index 0-23): each bucket estimates the fraction of
	// this Fingerprint's traffic that historically falls in that hour.
	// A fingerprint with no time-of-day pattern converges toward
	// 1/24 in every bucket; one concentrated at a specific hour
	// converges toward 1.0 there and 0.0 elsewhere.
	HourActivity [24]float64

	// Stable is the shape this Fingerprint represents, retained for
	// explainability so a consumer never has to recompute it.
	Stable features.StableFeatures

	// PredecessorCounts records, for this destination Fingerprint, how
	// many times each distinct predecessor Fingerprint.ID has
	// immediately preceded it in this actor's observed event order —
	// the minimum state a transition-deviation signal needs (see
	// internal/anomaly's transition_deviation) without yet computing a
	// Markov transition probability (deliberately deferred; see
	// docs/ROADMAP.md § v0.6). A nil map (the zero value, exactly like
	// a persisted file written before this field existed) means "no
	// transition into this fingerprint observed yet" — the correct,
	// safe cold-start default, not a distinguishable error state.
	//
	// Bounded at maxPredecessors distinct entries: once at that bound,
	// a genuinely new predecessor is not added (see recordPredecessor)
	// — existing entries keep accumulating normally. This is a
	// deliberately simple "first N distinct predecessors win" policy,
	// not LRU: the failure mode at the bound is that a not-yet-tracked
	// predecessor always reads as "unseen" (maximally novel), which is
	// the conservative, safe direction to fail in — it never causes an
	// already-legitimate, tracked transition to be silently forgotten.
	PredecessorCounts map[string]uint64

	// OutgoingTransitionTotal counts every valid transition where this
	// Fingerprint was the *predecessor* — the number of times any
	// destination fingerprint immediately followed it, regardless of
	// which one. This is the denominator internal/anomaly's
	// transition_rarity signal needs to estimate
	// P(destination | this fingerprint) = PredecessorCounts_destination[this]
	// / OutgoingTransitionTotal, without scanning every other
	// FingerprintStats entry to compute it — see
	// docs/adr/0011-transition-rarity-statistic-and-orientation.md for
	// the full reasoning behind this specific field, its placement on
	// the predecessor's own stats (not the destination's), and why a
	// plain, unguarded increment (matching Count's own precedent) is
	// used rather than a saturating counter.
	OutgoingTransitionTotal uint64

	// TrigramCounts records, for this destination Fingerprint, how many
	// times each distinct (two-steps-back, one-step-back) predecessor
	// pair has immediately preceded it — the 3-gram analogue of
	// PredecessorCounts (task 025's 2-gram state), the minimum
	// additional state ngram_deviation/ngram_rarity need (task 027) to
	// evaluate a 3-gram without a full sequence-history structure. A nil
	// map (the zero value, exactly like a persisted file written before
	// this field existed) means "no 3-gram into this fingerprint
	// observed yet" — the correct, safe cold-start default.
	//
	// Bounded at maxTrigramPredecessors distinct pairs, independently
	// of PredecessorCounts's own bound — see maxTrigramPredecessors's
	// doc comment and
	// docs/adr/0012-bounded-trigram-behavioral-context.md for why one
	// bound cannot be assumed to imply the other.
	TrigramCounts map[TrigramKey]uint64

	// TrigramContinuationTotal counts, for this Fingerprint acting as
	// the *immediate* (one-step-back) predecessor of a 3-gram, how many
	// valid 3-gram completions have followed each distinct
	// two-steps-back grandparent Fingerprint.ID, to any destination —
	// the 3-gram analogue of OutgoingTransitionTotal (task 026),
	// keyed by grandparent because a single fingerprint can be the
	// "Second" element of many distinct (grandparent, this) pairs, each
	// needing its own continuation total; a plain scalar (as
	// OutgoingTransitionTotal is) would silently answer the wrong
	// question — see
	// docs/adr/0012-bounded-trigram-behavioral-context.md for the full
	// orientation proof, mirroring ADR 0011's for the 2-gram case.
	// Bounded at maxTrigramPredecessors distinct grandparent entries,
	// independently of PredecessorCounts's own bound.
	TrigramContinuationTotal map[string]uint64
}

// recordPredecessor increments counts[predecessor], creating counts if
// nil, but never grows counts past maxPredecessors distinct keys — see
// PredecessorCounts's own doc comment for why this bound exists and why
// this specific eviction policy (deterministic refusal to add a new key
// past the bound, not LRU) was chosen.
//
// Like Baseline.Observe/FingerprintStats.observe, this never mutates
// counts in place: a map, unlike HourActivity's fixed-size array, is a
// reference type, and a shallow maps.Copy of a Fingerprints map (as
// Baseline.Observe performs) only copies the FingerprintStats struct
// values, not the maps a value like PredecessorCounts points into — an
// in-place counts[predecessor]++ here would silently corrupt whatever
// earlier Baseline snapshot (e.g. one a concurrent Store.Get caller is
// still holding) shares that same underlying map.
func recordPredecessor(counts map[string]uint64, predecessor string) map[string]uint64 {
	if _, tracked := counts[predecessor]; !tracked && len(counts) >= maxPredecessors {
		return counts
	}

	next := make(map[string]uint64, len(counts)+1)
	maps.Copy(next, counts)
	next[predecessor]++
	return next
}

// recordTrigram increments counts[key], with the identical
// bounded-copy-on-write discipline recordPredecessor already
// established — see recordPredecessor's own doc comment for why
// neither in-place mutation nor unbounded growth is acceptable here.
// Deliberately a second, independent function rather than a shared
// generic helper with recordPredecessor: three small, near-identical
// bounded-map functions (this, recordTrigramContinuation, and the
// pre-existing recordPredecessor) are simpler to read and audit than a
// generic abstraction introduced for exactly three call sites — see
// CLAUDE.md's "three similar lines is better than a premature
// abstraction."
func recordTrigram(counts map[TrigramKey]uint64, key TrigramKey) map[TrigramKey]uint64 {
	if _, tracked := counts[key]; !tracked && len(counts) >= maxTrigramPredecessors {
		return counts
	}

	next := make(map[TrigramKey]uint64, len(counts)+1)
	maps.Copy(next, counts)
	next[key]++
	return next
}

// recordTrigramContinuation increments counts[grandparent], with the
// identical bounded-copy-on-write discipline recordPredecessor and
// recordTrigram already establish. See maxTrigramPredecessors's own
// doc comment for why this needs its own explicit bound rather than
// inheriting one from PredecessorCounts's or TrigramCounts's.
func recordTrigramContinuation(counts map[string]uint64, grandparent string) map[string]uint64 {
	if _, tracked := counts[grandparent]; !tracked && len(counts) >= maxTrigramPredecessors {
		return counts
	}

	next := make(map[string]uint64, len(counts)+1)
	maps.Copy(next, counts)
	next[grandparent]++
	return next
}

// recordDelegator increments counts[delegator], with the identical
// bounded-copy-on-write discipline recordPredecessor/recordTrigram/
// recordTrigramContinuation already establish — see recordPredecessor's
// own doc comment for why neither in-place mutation nor unbounded
// growth is acceptable here. A deliberately separate function, not a
// shared generic helper, for the same "three/four small, near-identical
// bounded-map functions are simpler to audit than a premature
// abstraction" reasoning recordTrigram's own doc comment already gives.
func recordDelegator(counts map[string]uint64, delegator string) map[string]uint64 {
	if _, tracked := counts[delegator]; !tracked && len(counts) >= maxDelegators {
		return counts
	}

	next := make(map[string]uint64, len(counts)+1)
	maps.Copy(next, counts)
	next[delegator]++
	return next
}

// observeOutgoingTransition increments s.OutgoingTransitionTotal by
// one, for a valid transition where s is the predecessor. A plain,
// unguarded increment — matching Count's own existing precedent (see
// docs/adr/0011-transition-rarity-statistic-and-orientation.md's
// "Consequences" for why this counter deliberately does not saturate).
func (s FingerprintStats) observeOutgoingTransition() FingerprintStats {
	s.OutgoingTransitionTotal++
	return s
}

// observeTrigramContinuation records one valid 3-gram completion where
// s is the *immediate* (one-step-back) predecessor and grandparent is
// the two-steps-back fingerprint — the bounded, copy-on-write update to
// TrigramContinuationTotal (see its own doc comment for why this is
// keyed by grandparent rather than a plain scalar).
func (s FingerprintStats) observeTrigramContinuation(grandparent string) FingerprintStats {
	s.TrigramContinuationTotal = recordTrigramContinuation(s.TrigramContinuationTotal, grandparent)
	return s
}

// LatencyMeanDuration returns LatencyMean as a time.Duration.
func (s FingerprintStats) LatencyMeanDuration() time.Duration {
	return time.Duration(s.LatencyMean)
}

// LatencyStdDevDuration returns the standard deviation implied by
// LatencyVariance, as a time.Duration. It is zero when no latency
// observation has been recorded.
func (s FingerprintStats) LatencyStdDevDuration() time.Duration {
	if s.LatencyObservations == 0 {
		return 0
	}
	return time.Duration(math.Sqrt(s.LatencyVariance))
}

// IsStale reports whether s has not been observed within maxAge of now.
// A FingerprintStats with no observations at all (Count == 0) is never
// stale — that's cold start, a different concept from staleness (see
// internal/anomaly's Confidence for cold start): staleness is about a
// fingerprint that *was* known and hasn't been seen in a while, not one
// that was never known at all.
//
// This package only reports staleness; it does not act on it (no
// automatic expiration or deletion) — deciding what to do with a stale
// entry, if anything, is a caller's policy decision, matching the
// existing "Baseline only counts" design (see Count's doc comment).
func (s FingerprintStats) IsStale(now time.Time, maxAge time.Duration) bool {
	if s.Count == 0 {
		return false
	}
	return now.Sub(s.LastObserved) > maxAge
}

// predecessor is the caller-supplied Fingerprint.ID that immediately
// preceded this observation for the same actor, or "" if there was
// none (the actor's first-ever observation) or the ordering guard in
// Baseline.Observe determined this observation does not validly follow
// one (see there for why). grandparent is the Fingerprint.ID two steps
// back — "" whenever predecessor is "" (no 2-gram, so no 3-gram either)
// or when the actor has not yet produced a second-ever observation (a
// valid 2-gram exists, but no 3-gram can complete from it yet — see
// docs/adr/0012-bounded-trigram-behavioral-context.md's cold-start
// section). observe itself performs no ordering check of its own — by
// the time predecessor/grandparent reach here, that decision has
// already been made once, by the one caller (Baseline.Observe).
func (s FingerprintStats) observe(stable features.StableFeatures, vol features.VolatileFeatures, now time.Time, predecessor, grandparent string) FingerprintStats {
	if s.Count == 0 {
		s.FirstObserved = now
	} else if now.After(s.LastObserved) {
		// Only a strictly forward-moving timestamp carries interval
		// information. An event that does not follow the last one —
		// clock skew, out-of-order delivery, or a deliberately
		// backdated event from an untrusted source — would otherwise
		// fold a negative (or zero) interval into the EWMA, which is a
		// baseline-poisoning primitive: such an event is typically
		// decided observe_only and so is eligible for learning, and one
		// of them is enough to drag IntervalMean below zero and make
		// every subsequent on-time event look anomalous. Skipping it
		// records "no interval information this time" rather than the
		// misleading data point a zero-or-negative interval would be.
		intervalNS := float64(now.Sub(s.LastObserved))
		if s.IntervalObservations == 0 {
			s.IntervalMean = intervalNS
			s.IntervalVariance = 0
		} else {
			delta := intervalNS - s.IntervalMean
			s.IntervalMean += emaAlpha * delta
			s.IntervalVariance = (1 - emaAlpha) * (s.IntervalVariance + emaAlpha*delta*delta)
		}
		s.IntervalObservations++
	}
	s.Count++
	// LastObserved is the latest timestamp seen for this Fingerprint, not
	// the timestamp of the most recent Observe call: it never regresses.
	// Letting an out-of-order event pull it backwards would corrupt the
	// *next* interval too (the following in-order event would measure
	// from a timestamp that is not actually the most recent observation),
	// which would defeat the guard above. It also keeps IsStale honest —
	// a backdated arrival must not make a fingerprint look staler than
	// the freshest evidence we hold for it.
	if now.After(s.LastObserved) {
		s.LastObserved = now
	}
	s.Stable = stable

	// HourActivity tracks the same EWMA-of-indicator shape ErrorRate
	// already uses, applied per hour-of-day bucket instead of a single
	// scalar: every observation nudges the bucket matching now's UTC
	// hour toward 1 and every other bucket toward 0. Unlike the
	// interval/latency EWMAs above, this uses now (the event's own
	// timestamp), not a derived value, and is unaffected by the
	// out-of-order guard above — an out-of-order event's hour is still
	// real information about when this fingerprint fires, even though
	// its *interval* isn't trustworthy.
	hour := now.UTC().Hour()
	if s.TimePatternObservations == 0 {
		for h := range s.HourActivity {
			s.HourActivity[h] = 0
		}
		s.HourActivity[hour] = 1
	} else {
		for h := range s.HourActivity {
			sample := 0.0
			if h == hour {
				sample = 1.0
			}
			s.HourActivity[h] += hourActivityAlpha * (sample - s.HourActivity[h])
		}
	}
	s.TimePatternObservations++

	if vol.HasLatency {
		latencyNS := float64(vol.Latency)
		if s.LatencyObservations == 0 {
			s.LatencyMean = latencyNS
			s.LatencyVariance = 0
		} else {
			delta := latencyNS - s.LatencyMean
			s.LatencyMean += emaAlpha * delta
			s.LatencyVariance = (1 - emaAlpha) * (s.LatencyVariance + emaAlpha*delta*delta)
		}
		s.LatencyObservations++
	}

	errSample := 0.0
	if vol.Error {
		errSample = 1.0
	}
	if s.Count == 1 {
		s.ErrorRate = errSample
	} else {
		s.ErrorRate += emaAlpha * (errSample - s.ErrorRate)
	}

	if predecessor != "" {
		s.PredecessorCounts = recordPredecessor(s.PredecessorCounts, predecessor)
		if grandparent != "" {
			s.TrigramCounts = recordTrigram(s.TrigramCounts, TrigramKey{First: grandparent, Second: predecessor})
		}
	}

	return s
}

// Baseline is the statistical model for a single Key: one FingerprintStats
// entry per distinct Fingerprint the actor has produced.
type Baseline struct {
	Key          Key
	Fingerprints map[string]FingerprintStats

	// LastObserved is when this Baseline was last written — the `now` of
	// the most recent Observe call, whatever its ordering. This is
	// deliberately *not* the same rule as FingerprintStats.LastObserved,
	// which tracks the latest evidence for one Fingerprint and never
	// regresses because interval statistics are measured from it.
	// Nothing measures anything from this field; it records write
	// recency for the whole Baseline.
	LastObserved time.Time

	// LastFingerprintID is the Fingerprint.ID of this actor's most
	// recently observed event — the "previous action" a transition
	// (LastFingerprintID -> the next Fingerprint.ID observed) is formed
	// from. "" means no prior observation exists yet to form a
	// transition from (this actor's first-ever event, or a freshly
	// constructed Baseline) — the correct, safe default, not an error
	// state.
	//
	// Unlike LastObserved above, this *does* follow an ordering guard
	// (paired with LastFingerprintTime below), for the identical reason
	// FingerprintStats.LastObserved's own guard exists: an out-of-order
	// or backdated event must not be allowed to silently rewrite "what
	// the previous action was" for the *next* legitimate event's
	// transition to be measured against — see Observe.
	LastFingerprintID string

	// LastFingerprintTime is the timestamp associated with
	// LastFingerprintID — never regresses; see Observe for the guard
	// that enforces this. Zero (time.Time{}) exactly when
	// LastFingerprintID is "".
	LastFingerprintTime time.Time

	// PreviousFingerprintID is the Fingerprint.ID two steps back in this
	// actor's observed order — together with LastFingerprintID, the
	// minimal two-element history a 3-gram
	// (PreviousFingerprintID -> LastFingerprintID -> the next
	// Fingerprint.ID observed) is formed from. "" means no such
	// fingerprint exists yet: either this actor has fewer than two
	// prior observations, or every intervening advance was itself
	// out-of-order (see Observe) — either way, the correct, safe
	// "insufficient history for a 3-gram" default, not an error state.
	//
	// No separate PreviousFingerprintTime field exists: unlike
	// LastFingerprintID (validated against the *current* observation's
	// timestamp on every read, via LastFingerprintTime),
	// PreviousFingerprintID's own ordering validity is established
	// transitively, once, at the moment it is assigned (see Observe) —
	// it only ever takes a value that was itself a validly-ordered
	// LastFingerprintID at some earlier point, so re-validating it
	// against a second timestamp on every subsequent read would be
	// redundant. See
	// docs/adr/0012-bounded-trigram-behavioral-context.md.
	PreviousFingerprintID string

	// DelegatorCounts records, for this actor+environment, how many
	// times each distinct immediate delegator (Actor.ID, from
	// features.VolatileFeatures.DelegatedFrom) has been observed
	// delegating an event to this actor — the minimum state a
	// delegation-deviation signal needs (see internal/anomaly's
	// delegation_deviation), mirroring FingerprintStats.PredecessorCounts's
	// exact shape, one level up: this is actor-level state, not
	// per-Fingerprint, because "who normally delegates to this actor"
	// is a property of the actor, not of any one operation it
	// performs. A nil map (the zero value) means "no delegation
	// observed yet for this actor" — the correct, safe cold-start
	// default. Bounded at maxDelegators distinct entries, with the
	// identical "first N distinct delegators win, fail toward
	// maximal novelty past the bound" policy recordPredecessor already
	// established. See docs/tasks/031-delegation-behavioral-semantics.md
	// and ADR 0016.
	DelegatorCounts map[string]uint64
}

// New returns an empty Baseline for key, ready to be passed to Observe.
func New(key Key) Baseline {
	return Baseline{Key: key}
}

// Observe returns a new Baseline reflecting one additional observation of
// fp with volatile signals vol at time now. b is not modified: the
// returned value has its own Fingerprints map, so any Baseline value a
// caller already holds (e.g. from a prior Store.Get) remains a valid,
// unaffected snapshot.
//
// This is also where LastFingerprintID/LastFingerprintTime (and, one
// step further back, PreviousFingerprintID) advance, under the same
// ordering guard FingerprintStats.observe already applies to interval
// statistics: now must strictly follow LastFingerprintTime for this
// observation to (a) be treated as a valid transition *from*
// b.LastFingerprintID at all, (b) be treated as a valid 3-gram
// completion from (b.PreviousFingerprintID, b.LastFingerprintID) when
// the former is also non-empty, and (c) shift the two-element history
// window forward (the old LastFingerprintID becomes the new
// PreviousFingerprintID) for whatever observation follows it. An
// out-of-order or backdated now is folded into fp's own
// FingerprintStats (Count, etc.) as usual, but contributes no
// transition or 3-gram information in either direction, and does not
// shift the history window — exactly the same "absence of information,
// not a misleading data point" stance FingerprintStats.observe's own
// interval guard takes, now extended coherently one step further back.
//
// Fingerprint admission is bounded by maxFingerprints. A fingerprint
// already present keeps learning normally however full the baseline is;
// an unknown one is learned only while the baseline holds fewer than
// maxFingerprints identities. At capacity the observation still returns
// a valid Baseline and still advances LastObserved and DelegatorCounts —
// it simply does not create a new FingerprintStats entry, does not
// evict an existing one, and does not move the sequence history window
// to a fingerprint it did not learn. A baseline that already holds more
// than maxFingerprints entries (learned before this bound existed) keeps
// every one of them and keeps updating them; it just admits no more.
//
// vol.DelegatedFrom, when non-empty, updates DelegatorCounts
// unconditionally — unlike the sequence state above, delegation
// provenance carries no ordering dependency, so it is not subject to
// the same now-must-strictly-follow guard.
//
// The returned admitted bool reports whether fp's own FingerprintStats
// were updated — that is, whether this baseline actually learned
// anything about the behavior it was handed. It is false exactly when
// admission control refused an unknown fingerprint at capacity. This is
// deliberately narrower than "did any field change": an observation at
// capacity still advances LastObserved and DelegatorCounts, but a
// caller asking whether the behavior was learned is not asking about
// those. Reporting it is what lets Engine.Observe stop claiming
// learning that did not happen — see
// docs/adr/0024-learning-scope-is-a-baseline-key-dimension.md.
func (b Baseline) Observe(fp fingerprint.Fingerprint, vol features.VolatileFeatures, now time.Time) (updated Baseline, admitted bool) {
	advances := b.LastFingerprintID == "" || now.After(b.LastFingerprintTime)
	validTransition := b.LastFingerprintID != "" && now.After(b.LastFingerprintTime)
	predecessor := ""
	grandparent := ""
	if validTransition {
		predecessor = b.LastFingerprintID
		grandparent = b.PreviousFingerprintID
	}

	// Admission control. A fingerprint this baseline already knows always
	// keeps learning; an unknown one is admitted only while there is room
	// under maxFingerprints. At capacity the observation is not an error
	// and does not stop the pipeline — it simply teaches this baseline
	// nothing about a fingerprint identity it has no room to hold, which
	// leaves anomaly scoring to treat that fingerprint as unknown through
	// the ordinary path rather than through a special case.
	_, known := b.Fingerprints[fp.ID]
	admit := known || len(b.Fingerprints) < maxFingerprints

	next := make(map[string]FingerprintStats, len(b.Fingerprints)+1)
	maps.Copy(next, b.Fingerprints)
	if admit {
		next[fp.ID] = next[fp.ID].observe(fp.Stable, vol, now, predecessor, grandparent)
	}

	// The predecessor's own OutgoingTransitionTotal (and, when a
	// grandparent exists, TrigramContinuationTotal) advance separately
	// from the destination update above — a different map entry,
	// unless predecessor == fp.ID (a self-transition), in which case
	// this reads the value the observe() call just wrote and applies
	// these further updates on top of it, so no update is lost. See
	// docs/adr/0011-transition-rarity-statistic-and-orientation.md and
	// docs/adr/0012-bounded-trigram-behavioral-context.md for why these
	// counters exist and live on the predecessor's stats, not the
	// destination's.
	//
	// The `tracked` guard is what keeps maxFingerprints airtight: this is
	// the one other place an entry can enter the map, and writing to
	// next[predecessor] for an untracked ID would admit a fingerprint
	// through the side door. It cannot normally fire — the history window
	// below only advances to an admitted fingerprint — but it costs one
	// lookup and removes the possibility entirely, including for a
	// baseline deserialized with inconsistent state.
	if predecessor != "" {
		if _, tracked := next[predecessor]; tracked {
			next[predecessor] = next[predecessor].observeOutgoingTransition()
			if grandparent != "" {
				next[predecessor] = next[predecessor].observeTrigramContinuation(grandparent)
			}
		}
	}

	lastFingerprintID := b.LastFingerprintID
	lastFingerprintTime := b.LastFingerprintTime
	previousFingerprintID := b.PreviousFingerprintID
	// `admit` joins the ordering guard here on purpose: a fingerprint that
	// was not learned must not become the predecessor a later observation
	// records a transition from, because that ID has no FingerprintStats
	// to carry the transition's other half. Rejection leaves the window
	// pointing at the last fingerprint this baseline actually knows.
	if advances && admit {
		previousFingerprintID = b.LastFingerprintID // "" on the actor's first-ever observation — correct
		lastFingerprintID = fp.ID
		lastFingerprintTime = now
	}

	// DelegatorCounts records vol.DelegatedFrom unconditionally on
	// every valid observation (no ordering guard, unlike the
	// transition/3-gram state above): delegation provenance is a fact
	// about *this* event alone, not something whose validity depends
	// on strictly following a prior timestamp the way a transition or
	// interval measurement does. An out-of-order event's own delegator
	// claim is exactly as real as an in-order one's.
	delegatorCounts := b.DelegatorCounts
	if vol.DelegatedFrom != "" {
		delegatorCounts = recordDelegator(delegatorCounts, vol.DelegatedFrom)
	}

	return Baseline{
		Key:                   b.Key,
		Fingerprints:          next,
		LastObserved:          now,
		LastFingerprintID:     lastFingerprintID,
		LastFingerprintTime:   lastFingerprintTime,
		PreviousFingerprintID: previousFingerprintID,
		DelegatorCounts:       delegatorCounts,
	}, admit
}
