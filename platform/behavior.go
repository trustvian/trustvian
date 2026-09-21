package platform

// Behavioral diff: which behavioral shapes changed between two evaluations.
//
//	DecisionRecord ──▶ BehaviorCollector ──▶ BehaviorSnapshot ──▶ BehaviorDiff
//	                   bounded, mutable      detached, sorted     bounded, factual
//
// EvaluationAggregate is untouched by all of this. Task 053 answers *what
// happened* in fixed shape; this answers *which behaviors happened*, which
// genuinely needs per-fingerprint state. They consume the same record stream
// and share none of it, so a caller can run either, both, or neither — and an
// evaluation that will never be compared pays nothing for the bookkeeping.
//
// See docs/adr/0027-behavioral-diff-compares-bounded-snapshots.md.

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// maxBehaviorEntries bounds the distinct fingerprints one collector admits.
//
// The set is keyed by Fingerprint.ID, which the core derives from Event fields
// the caller supplies — the same hazard ADR 0019 found in the engine's own
// baseline, so it carries the same kind of cap and the same number. An
// evaluation observing more distinct shapes than an actor's entire learned
// repertoire is past the point where a bounded behavioral diff can answer
// truthfully.
//
// Declared here rather than imported: the platform cannot reach
// internal/baseline, and should not compile against a core constant even if it
// could. The two bounds answer different questions — what one actor's baseline
// will learn, versus what one evaluation will compare — and must be free to
// move apart.
const maxBehaviorEntries = 512

// maxBehaviorDeltas bounds a diff: two snapshots of at most maxBehaviorEntries
// each, with no overlap.
const maxBehaviorDeltas = 2 * maxBehaviorEntries

// Sentinel errors, wrapped with fmt.Errorf and matched with errors.Is.
//
// Seven categories because a caller can act differently on each: fix the
// record, send it to the right collector, distrust the evidence, accept that
// the evaluation is too wide to diff, re-run to get complete evidence,
// construct the collector properly, or stop.
var (
	// ErrInvalidBehaviorRecord reports a record whose behavioral fields are
	// missing, unrecognized, or over-long.
	ErrInvalidBehaviorRecord = errors.New("platform: invalid behavioral evidence")

	// ErrBehaviorEnvironmentMismatch reports evidence from a different
	// environment than the collector's, or a comparison across environments.
	ErrBehaviorEnvironmentMismatch = errors.New("platform: behavioral evidence environment does not match")

	// ErrFingerprintConflict reports evidence where fingerprint identity and
	// stable behavior descriptor disagree, in either direction: one
	// fingerprint claiming two shapes, or one shape claiming two
	// fingerprints.
	ErrFingerprintConflict = errors.New("platform: fingerprint identity and behavior descriptor are inconsistent")

	// ErrBehaviorCapacity reports a distinct behavior beyond the collector's
	// capacity. The collector is incomplete from this point on.
	ErrBehaviorCapacity = errors.New("platform: behavioral capacity exceeded")

	// ErrIncompleteSnapshot reports an attempt to compare saturated evidence.
	ErrIncompleteSnapshot = errors.New("platform: behavioral snapshot is incomplete")

	// ErrUnboundCollector reports a collector that did not come from
	// NewBehaviorCollector and is bound to no evaluation run.
	ErrUnboundCollector = errors.New("platform: behavior collector is not bound to an evaluation run")

	// ErrBehaviorOverflow reports an observation counter that would wrap.
	ErrBehaviorOverflow = errors.New("platform: behavioral observation counter overflow")
)

// BehaviorEntry is one behavioral shape and how often an evaluation observed
// it.
//
// Returned by value from a snapshot, so the exported fields are a snapshot's
// snapshot: mutating one cannot reach the collector or the snapshot it came
// from.
type BehaviorEntry struct {
	// FingerprintID is the core's stable behavioral-shape identity, and the
	// only key a diff compares on.
	FingerprintID string

	// Behavior is the shape behind that fingerprint, retained so a consumer
	// can say "tool / shell.execute → build-host" instead of printing a hash.
	Behavior trustvian.StableFeatures

	// Observations counts successful Observe calls for this fingerprint.
	Observations uint64
}

// BehaviorCollector reduces a stream of DecisionRecords into the bounded set
// of behaviors one evaluation run observed.
//
// Deliberately mutable, and deliberately the only mutable type in this
// package. EvaluationRun, EvaluationAggregate, BehaviorSnapshot and
// BehaviorDiff are all values; this is an ephemeral builder that receives one
// record at a time and can hold up to 512 entries. Copying that map on every
// observation to imitate value semantics would be real work spent on an
// appearance. Immutability arrives at the snapshot boundary, which is where it
// matters — that is what a diff consumes.
//
// Not safe for concurrent use. It is a builder, not a service: a caller that
// needs concurrency owns the synchronization, and there is no mutex here to
// suggest otherwise.
type BehaviorCollector struct {
	// bound marks a collector that came from NewBehaviorCollector with a
	// valid run. Unexported fields prevent mutation, not
	// `platform.BehaviorCollector{}` — which any package can write, and which
	// would otherwise accept evidence into a collector belonging to no run.
	// Same guard, same reasoning as EvaluationAggregate.
	bound bool

	runID       EvaluationRunID
	candidateID CandidateID
	environment EnvironmentRef
	profile     BehavioralProfileRef

	observations uint64
	entries      map[string]BehaviorEntry

	// byBehavior is the reverse index: descriptor -> fingerprint. It exists
	// so the one-to-one identity relation can be checked in both directions
	// without scanning.
	//
	// A bounded linear scan over entries was tried first and measured: it
	// cost 3.2 us per newly admitted fingerprint against 189 ns, because the
	// scan averages half of a 512-entry map per admission. This index makes
	// it a single lookup at the cost of one more bounded map — the same 512
	// ceiling, and written in exactly one place, so the two cannot drift.
	byBehavior map[trustvian.StableFeatures]string

	// complete goes false the first time a distinct behavior is refused for
	// capacity, and never goes back. See Observe.
	complete bool
}

// NewBehaviorCollector returns a collector bound to run, or an error if the
// run could not have come from NewEvaluationRun and its transitions.
//
// The same run validation NewEvaluationAggregate applies, for the same reason:
// a zero-value EvaluationRun is constructible from any package, and evidence
// bound to no run is evidence about nothing.
func NewBehaviorCollector(run EvaluationRun) (*BehaviorCollector, error) {
	if err := validateRunBinding(run); err != nil {
		return nil, err
	}
	return &BehaviorCollector{
		bound:       true,
		runID:       run.ID(),
		candidateID: run.CandidateID(),
		environment: run.Environment(),
		profile:     run.BehavioralProfile(),
		entries:     make(map[string]BehaviorEntry),
		byBehavior:  make(map[trustvian.StableFeatures]string),
		complete:    true,
	}, nil
}

// Observe folds one DecisionRecord into the collector.
//
// Every consumed field is validated as untrusted input before any state
// changes, so a rejected record leaves the collector exactly as it was. The
// one deliberate exception is capacity: refusing a 513th distinct behavior
// *does* change the collector, by marking it permanently incomplete. That is
// the point — see below.
//
// Fields this task does not consume — Decision, RiskLevel, ApprovalStatus, the
// numeric signals, PolicyRule, Contributors — are not validated. Task 053
// already checks them for the aggregate, and rejecting a record over a field
// nothing here reads would fail evaluations that are perfectly usable for a
// diff.
//
// Duplicates count twice. One successful Observe is one observation, and
// detecting a repeat would need every identifier remembered — the unbounded
// structure this design exists to exclude.
func (c *BehaviorCollector) Observe(record trustvian.DecisionRecord) error {
	if c == nil || !c.bound {
		return fmt.Errorf("%w: use NewBehaviorCollector", ErrUnboundCollector)
	}
	// Once saturated, the collector stops accepting rather than continuing to
	// accumulate counts that would look complete. A caller that ignored the
	// capacity error should not be able to keep building a partial picture
	// that reads like a whole one.
	if !c.complete {
		return fmt.Errorf("%w: collector saturated at %d distinct behaviors", ErrBehaviorCapacity, maxBehaviorEntries)
	}
	if c.observations == math.MaxUint64 {
		return fmt.Errorf("%w: already at %d observations", ErrBehaviorOverflow, c.observations)
	}

	if record.EventID == "" {
		return fmt.Errorf("%w: event id is empty", ErrInvalidBehaviorRecord)
	}
	if record.Timestamp.IsZero() {
		return fmt.Errorf("%w: event %s has no timestamp", ErrInvalidBehaviorRecord, preview(record.EventID))
	}

	// Three-way environment agreement. The record's own two values are the
	// same in genuine engine output, so a disagreement means the record was
	// assembled rather than produced.
	if record.Environment != string(c.environment) {
		return fmt.Errorf("%w: event %s is from %s, collector is %s",
			ErrBehaviorEnvironmentMismatch, preview(record.EventID),
			preview(record.Environment), preview(string(c.environment)))
	}
	if record.Behavior.Environment != record.Environment {
		return fmt.Errorf("%w: event %s reports environment %s but its behavior says %s",
			ErrInvalidBehaviorRecord, preview(record.EventID),
			preview(record.Environment), preview(record.Behavior.Environment))
	}

	if err := validateBehaviorIdentity(record.FingerprintID, record.Behavior, record.EventID); err != nil {
		return err
	}

	// One fingerprint identifies exactly one shape. Overwriting the
	// descriptor, or merging the counts, would report two different behaviors
	// as one — and the realistic causes (a tampered record, corruption, a
	// collision) all deserve refusal rather than a plausible-looking merge.
	existing, known := c.entries[record.FingerprintID]
	if known {
		if existing.Behavior != record.Behavior {
			return fmt.Errorf("%w: fingerprint %s was %s/%s, event %s says %s/%s",
				ErrFingerprintConflict, preview(record.FingerprintID),
				preview(string(existing.Behavior.OperationCategory)), preview(existing.Behavior.OperationName),
				preview(record.EventID),
				preview(string(record.Behavior.OperationCategory)), preview(record.Behavior.OperationName))
		}
		if existing.Observations == math.MaxUint64 {
			return fmt.Errorf("%w: fingerprint %s already at %d observations",
				ErrBehaviorOverflow, preview(record.FingerprintID), existing.Observations)
		}
	} else {
		// The reverse direction, and the one a single-direction check
		// misses entirely: this fingerprint is new, but its *shape* may
		// already be here under a different identity.
		//
		// Left unchecked, two snapshots built from such evidence report the
		// same behavior as Removed under the old id and Added under the new
		// one — a diff claiming behavior changed when only its identity
		// encoding did. That is the most damaging output this type can
		// produce, because it is specific, confident, and wrong.
		//
		// One lookup in the reverse index, which exists for exactly this
		// check. A bounded linear scan over entries was implemented first
		// and measured: it cost 3.2 us per newly admitted fingerprint
		// against 189 ns, because it averages half of a 512-entry map. The
		// index is one lookup instead, for a second map under the same
		// maxBehaviorEntries ceiling.
		//
		// Both indexes are written together, once, after every check below
		// has passed, so neither can carry an entry the other lacks.
		//
		// Repeat observations — the common path — never reach here at all;
		// they take the known-fingerprint branch above.
		if id, clash := c.byBehavior[record.Behavior]; clash && id != record.FingerprintID {
			return fmt.Errorf("%w: behavior %s/%s is already fingerprint %s, event %s calls it %s",
				ErrFingerprintConflict,
				preview(string(record.Behavior.OperationCategory)), preview(record.Behavior.OperationName),
				preview(id), preview(record.EventID), preview(record.FingerprintID))
		}
		if len(c.entries) >= maxBehaviorEntries {
			// The one state change on a failure path, and the reason it exists:
			// a diff's headline output is which behaviors are new, and evidence
			// that stopped collecting at 512 yields a confident, specific, wrong
			// answer to exactly that question. Marking the collector incomplete
			// is what makes a later comparison refuse rather than under-report.
			c.complete = false
			return fmt.Errorf("%w: %d distinct behaviors already admitted, event %s brought another",
				ErrBehaviorCapacity, maxBehaviorEntries, preview(record.EventID))
		}
	}

	// Past this point nothing can fail.
	c.observations++
	if known {
		existing.Observations++
		c.entries[record.FingerprintID] = existing
	} else {
		// The only place an entry is created, so the two indexes are written
		// together and cannot fall out of step. Both writes happen after
		// every check above has passed, so no failure path leaves one
		// updated without the other.
		c.entries[record.FingerprintID] = BehaviorEntry{
			FingerprintID: record.FingerprintID,
			Behavior:      record.Behavior,
			Observations:  1,
		}
		c.byBehavior[record.Behavior] = record.FingerprintID
	}
	return nil
}

// Snapshot returns a detached, deterministically ordered view of what the
// collector has seen.
//
// Later Observe calls cannot change a snapshot already taken: the entries are
// copied out, not shared.
//
// A saturated collector still produces a snapshot, reporting Complete() ==
// false. Knowing that an evaluation saturated is useful; deriving a comparison
// from it is not, which is why CompareBehaviorSnapshots refuses one.
func (c *BehaviorCollector) Snapshot() BehaviorSnapshot {
	if c == nil || !c.bound {
		return BehaviorSnapshot{}
	}

	entries := slices.SortedFunc(maps.Values(c.entries), func(a, b BehaviorEntry) int {
		return cmp.Compare(a.FingerprintID, b.FingerprintID)
	})

	return BehaviorSnapshot{
		bound:        true,
		runID:        c.runID,
		candidateID:  c.candidateID,
		environment:  c.environment,
		profile:      c.profile,
		observations: c.observations,
		entries:      entries,
		complete:     c.complete,
	}
}

// BehaviorSnapshot is the bounded, immutable behavioral evidence of one
// evaluation run: which shapes it observed and how often.
//
// Entries are sorted by FingerprintID, so two snapshots built from the same
// records in different arrival orders are identical. Go randomizes map
// iteration deliberately; depending on it would make a diff's output vary
// between runs of the same comparison.
type BehaviorSnapshot struct {
	bound bool

	runID       EvaluationRunID
	candidateID CandidateID
	environment EnvironmentRef
	profile     BehavioralProfileRef

	observations uint64
	entries      []BehaviorEntry
	complete     bool
}

// Run identity, captured from the collector's run.
func (s BehaviorSnapshot) RunID() EvaluationRunID                  { return s.runID }
func (s BehaviorSnapshot) CandidateID() CandidateID                { return s.candidateID }
func (s BehaviorSnapshot) Environment() EnvironmentRef             { return s.environment }
func (s BehaviorSnapshot) BehavioralProfile() BehavioralProfileRef { return s.profile }

// ObservationCount is how many records were folded in — duplicates included.
func (s BehaviorSnapshot) ObservationCount() uint64 { return s.observations }

// DistinctBehaviorCount is how many distinct fingerprints were observed, at
// most maxBehaviorEntries.
func (s BehaviorSnapshot) DistinctBehaviorCount() int { return len(s.entries) }

// Complete reports whether this snapshot describes the whole run.
//
// False means the collector saturated: more distinct behaviors occurred than
// it could hold, so the entries below are a prefix of the truth and not the
// truth. CompareBehaviorSnapshots refuses such a snapshot.
func (s BehaviorSnapshot) Complete() bool { return s.complete }

// Entries returns the observed behaviors, sorted by FingerprintID.
//
// A defensive copy: a caller sorting, truncating, or rewriting the result
// cannot reach the snapshot's own state.
func (s BehaviorSnapshot) Entries() []BehaviorEntry {
	return slices.Clone(s.entries)
}

// BehaviorPresence says where a behavior occurred.
//
// Factual names on purpose. Not "unsafe", "violation", "drift" or
// "regression": an added behavior is frequently the feature the candidate was
// built to add, and deciding whether a change is acceptable needs thresholds
// that belong to the gate task.
type BehaviorPresence string

const (
	// BehaviorAdded occurred in the candidate and not in the reference.
	BehaviorAdded BehaviorPresence = "added"
	// BehaviorRemoved occurred in the reference and not in the candidate.
	BehaviorRemoved BehaviorPresence = "removed"
	// BehaviorShared occurred in both.
	BehaviorShared BehaviorPresence = "shared"
)

// BehaviorDelta is one behavior's presence and frequency across a comparison.
//
// Rates are each snapshot's share of its own observations, which is what makes
// a 900-record reference comparable to a 90-record candidate. RateDelta is
// signed and uninterpreted: no threshold here decides that some magnitude is
// meaningful.
type BehaviorDelta struct {
	FingerprintID string
	Behavior      trustvian.StableFeatures

	Presence BehaviorPresence

	ReferenceCount uint64
	CandidateCount uint64

	ReferenceRate float64
	CandidateRate float64
	RateDelta     float64
}

// BehaviorDiff is the factual comparison of two behavioral snapshots.
//
// It reports presence, counts, and frequency movement. It reports no
// DriftScore, Severity, Passed, or Promotable, and applies no threshold —
// including no threshold deciding that a frequency shift counts as "changed".
// Interpretation is the scorecard task's; gating is the gate task's. A verdict
// here would become the field people read instead of the gate.
//
// AddedCount is factual evidence a later gate may build
// `new_behavior_count <= threshold` on. The threshold is not here.
type BehaviorDiff struct {
	referenceRunID         EvaluationRunID
	referenceCandidateID   CandidateID
	referenceProfile       BehavioralProfileRef
	referenceObservations  uint64
	referenceDistinctCount int

	candidateRunID         EvaluationRunID
	candidateCandidateID   CandidateID
	candidateProfile       BehavioralProfileRef
	candidateObservations  uint64
	candidateDistinctCount int

	environment EnvironmentRef

	deltas  []BehaviorDelta
	added   int
	removed int
	shared  int
}

// Comparison identity, so a diff is self-describing.
func (d BehaviorDiff) ReferenceRunID() EvaluationRunID                  { return d.referenceRunID }
func (d BehaviorDiff) ReferenceCandidateID() CandidateID                { return d.referenceCandidateID }
func (d BehaviorDiff) ReferenceBehavioralProfile() BehavioralProfileRef { return d.referenceProfile }
func (d BehaviorDiff) CandidateRunID() EvaluationRunID                  { return d.candidateRunID }
func (d BehaviorDiff) CandidateCandidateID() CandidateID                { return d.candidateCandidateID }
func (d BehaviorDiff) CandidateBehavioralProfile() BehavioralProfileRef { return d.candidateProfile }

// Environment is the environment both sides were observed in; a comparison
// across environments is refused.
func (d BehaviorDiff) Environment() EnvironmentRef { return d.environment }

// Factual summary counts.
func (d BehaviorDiff) ReferenceObservationCount() uint64 { return d.referenceObservations }
func (d BehaviorDiff) CandidateObservationCount() uint64 { return d.candidateObservations }
func (d BehaviorDiff) ReferenceDistinctCount() int       { return d.referenceDistinctCount }
func (d BehaviorDiff) CandidateDistinctCount() int       { return d.candidateDistinctCount }
func (d BehaviorDiff) AddedCount() int                   { return d.added }
func (d BehaviorDiff) RemovedCount() int                 { return d.removed }
func (d BehaviorDiff) SharedCount() int                  { return d.shared }

// Deltas returns every behavior in the union, sorted by FingerprintID.
//
// A defensive copy, and bounded: at most maxBehaviorDeltas entries, because
// each snapshot holds at most maxBehaviorEntries.
func (d BehaviorDiff) Deltas() []BehaviorDelta {
	return slices.Clone(d.deltas)
}

// CompareBehaviorSnapshots reports which behaviors changed from reference to
// candidate.
//
// The direction matters and the function is not symmetric.
//
// Both snapshots must be constructor-produced and **complete**. Comparing
// saturated evidence is refused rather than approximated: the headline output
// is which behaviors are new, and evidence that stopped collecting early
// produces a confident, specific, wrong answer to that question. An error a
// caller must handle beats a plausible number nobody questions.
//
// Environments must match, because Environment is a stable fingerprint
// dimension: comparing staging against production would classify every
// behavior as simultaneously added and removed.
//
// CandidateID, RunID and BehavioralProfileRef may all differ, and usually
// will. Comparing two candidates is the point, and task 051 kept the learning
// scope out of behavioral identity precisely so two runs under different
// profiles that observe the same behavior produce the same fingerprint.
func CompareBehaviorSnapshots(reference, candidate BehaviorSnapshot) (BehaviorDiff, error) {
	for _, s := range []struct {
		name string
		snap BehaviorSnapshot
	}{{"reference", reference}, {"candidate", candidate}} {
		if !s.snap.bound {
			return BehaviorDiff{}, fmt.Errorf("%w: %s snapshot did not come from a collector",
				ErrUnboundCollector, s.name)
		}
		if !s.snap.complete {
			return BehaviorDiff{}, fmt.Errorf("%w: %s run observed more than %d distinct behaviors; "+
				"its evidence is a prefix, not a whole",
				ErrIncompleteSnapshot, s.name, maxBehaviorEntries)
		}
	}

	if reference.environment != candidate.environment {
		return BehaviorDiff{}, fmt.Errorf("%w: reference is %s, candidate is %s; "+
			"environment is a fingerprint dimension, so every behavior would differ",
			ErrBehaviorEnvironmentMismatch,
			preview(string(reference.environment)), preview(string(candidate.environment)))
	}

	refByID := make(map[string]BehaviorEntry, len(reference.entries))
	refByBehavior := make(map[trustvian.StableFeatures]string, len(reference.entries))
	for _, e := range reference.entries {
		refByID[e.FingerprintID] = e
		refByBehavior[e.Behavior] = e.FingerprintID
	}

	// Identity consistency across the combined evidence, both directions,
	// before anything is classified. Each snapshot is internally consistent
	// by construction, but two built independently can still disagree — and
	// no partial diff is returned when they do.
	if err := assertConsistentIdentity(refByID, refByBehavior, candidate.entries); err != nil {
		return BehaviorDiff{}, err
	}

	diff := BehaviorDiff{
		referenceRunID:         reference.runID,
		referenceCandidateID:   reference.candidateID,
		referenceProfile:       reference.profile,
		referenceObservations:  reference.observations,
		referenceDistinctCount: len(reference.entries),

		candidateRunID:         candidate.runID,
		candidateCandidateID:   candidate.candidateID,
		candidateProfile:       candidate.profile,
		candidateObservations:  candidate.observations,
		candidateDistinctCount: len(candidate.entries),

		environment: reference.environment,
	}

	deltas := make([]BehaviorDelta, 0, len(reference.entries)+len(candidate.entries))

	// Candidate side: Shared when the reference knows the fingerprint, Added
	// otherwise.
	for _, cand := range candidate.entries {
		delta := BehaviorDelta{
			FingerprintID:  cand.FingerprintID,
			Behavior:       cand.Behavior,
			Presence:       BehaviorAdded,
			CandidateCount: cand.Observations,
		}
		// Consistency was established above, so a shared fingerprint is
		// known to describe the same behavior on both sides.
		if ref, ok := refByID[cand.FingerprintID]; ok {
			delta.Presence = BehaviorShared
			delta.ReferenceCount = ref.Observations
		}
		deltas = append(deltas, delta)
	}

	// Reference side: whatever the candidate did not observe at all.
	candidateIDs := make(map[string]struct{}, len(candidate.entries))
	for _, e := range candidate.entries {
		candidateIDs[e.FingerprintID] = struct{}{}
	}
	for _, ref := range reference.entries {
		if _, ok := candidateIDs[ref.FingerprintID]; ok {
			continue
		}
		deltas = append(deltas, BehaviorDelta{
			FingerprintID:  ref.FingerprintID,
			Behavior:       ref.Behavior,
			Presence:       BehaviorRemoved,
			ReferenceCount: ref.Observations,
		})
	}

	// Rates from integer counts, at comparison time. Accumulating floats while
	// observing would make the result depend on arrival order; deriving them
	// here means equal counts give bit-identical rates however the records
	// arrived.
	for i := range deltas {
		deltas[i].ReferenceRate = rate(deltas[i].ReferenceCount, reference.observations)
		deltas[i].CandidateRate = rate(deltas[i].CandidateCount, candidate.observations)
		deltas[i].RateDelta = deltas[i].CandidateRate - deltas[i].ReferenceRate

		switch deltas[i].Presence {
		case BehaviorAdded:
			diff.added++
		case BehaviorRemoved:
			diff.removed++
		case BehaviorShared:
			diff.shared++
		}
	}

	slices.SortFunc(deltas, func(a, b BehaviorDelta) int {
		return cmp.Compare(a.FingerprintID, b.FingerprintID)
	})
	diff.deltas = deltas

	return diff, nil
}

// rate is a behavior's share of its snapshot's observations. Zero observations
// means zero, not a division by zero: an empty run observed nothing, and every
// behavior's share of nothing is nothing.
func rate(count, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total)
}

// validateBehaviorIdentity checks every field a snapshot retains.
//
// Identity strings are rejected rather than truncated. Two different behaviors
// must not become one because a display string was shortened — a diff that
// merged them would under-report exactly the thing it exists to report.
// Truncation is for diagnostics; identity gets refusal.
func validateBehaviorIdentity(fingerprintID string, b trustvian.StableFeatures, eventID string) error {
	// Opaque and bounded, deliberately not validated as a hash of any
	// particular shape: the platform should not freeze the core's internal
	// fingerprint algorithm.
	if fingerprintID == "" {
		return fmt.Errorf("%w: event %s has no fingerprint id", ErrInvalidBehaviorRecord, preview(eventID))
	}
	if err := validateText(ErrInvalidBehaviorRecord, "fingerprint id", fingerprintID); err != nil {
		return err
	}

	if !validActorType(b.ActorType) {
		return fmt.Errorf("%w: event %s has unrecognized actor type %s",
			ErrInvalidBehaviorRecord, preview(eventID), preview(string(b.ActorType)))
	}
	if !validOperationCategory(b.OperationCategory) {
		return fmt.Errorf("%w: event %s has unrecognized operation category %s",
			ErrInvalidBehaviorRecord, preview(eventID), preview(string(b.OperationCategory)))
	}
	if !validTargetCategory(b.TargetCategory) {
		return fmt.Errorf("%w: event %s has unrecognized target category %s",
			ErrInvalidBehaviorRecord, preview(eventID), preview(string(b.TargetCategory)))
	}

	// Required: Event.Validate rejects a missing operation name, so genuine
	// engine output always carries one.
	if b.OperationName == "" {
		return fmt.Errorf("%w: event %s has no operation name", ErrInvalidBehaviorRecord, preview(eventID))
	}
	if err := validateText(ErrInvalidBehaviorRecord, "operation name", b.OperationName); err != nil {
		return err
	}
	// Optional: a target is not always present.
	if b.TargetName != "" {
		if err := validateText(ErrInvalidBehaviorRecord, "target name", b.TargetName); err != nil {
			return err
		}
	}
	return nil
}

// The three enumerated behavioral dimensions, validated against the public
// event constants.
//
// The types and constants are public but their valid() methods are not, so the
// switch is restated rather than called — a smaller cost than asking the core
// to widen its surface, and the same trade task 053 made for the decision and
// risk strings.
func validActorType(t event.ActorType) bool {
	switch t {
	case event.ActorTypeService, event.ActorTypeUser, event.ActorTypeServiceAccount,
		event.ActorTypeAIAgent, event.ActorTypeDevice, event.ActorTypeUnknown:
		return true
	default:
		return false
	}
}

func validOperationCategory(c event.OperationCategory) bool {
	switch c {
	case event.OperationCategoryHTTP, event.OperationCategoryDB, event.OperationCategoryRPC,
		event.OperationCategoryTool, event.OperationCategoryExternal:
		return true
	default:
		return false
	}
}

func validTargetCategory(c event.TargetCategory) bool {
	switch c {
	// Unspecified is the zero value and a valid state: not every operation
	// has a categorized target.
	case event.TargetCategoryUnspecified, event.TargetCategoryInternal,
		event.TargetCategoryExternal, event.TargetCategoryDatabase:
		return true
	default:
		return false
	}
}

// assertConsistentIdentity checks the one-to-one relation between fingerprint
// identity and behavior descriptor across two snapshots.
//
//	same fingerprint, different descriptor  → conflict
//	same descriptor, different fingerprint  → conflict
//
// The second direction is the one a naive check misses, and it is the more
// damaging of the two. Two snapshots whose fingerprints were produced under
// different identity encodings — a changed hash, mismatched namespaces,
// assembled evidence — describe the same behavior under different ids, and a
// diff would report it as Removed under the old id and Added under the new
// one. That is a specific, confident claim that behavior changed when only
// its encoding did, and "added behavior" is the output most likely to be
// acted on.
//
// This deliberately does *not* recompute the core's hash. The platform must
// not freeze the choice of algorithm, its version prefix, or its
// serialization — it checks that the evidence it was handed is
// self-consistent, and the real-Engine test is what shows healthy core output
// satisfies the relation naturally.
func assertConsistentIdentity(
	refByID map[string]BehaviorEntry,
	refByBehavior map[trustvian.StableFeatures]string,
	candidateEntries []BehaviorEntry,
) error {
	for _, cand := range candidateEntries {
		if ref, ok := refByID[cand.FingerprintID]; ok && ref.Behavior != cand.Behavior {
			return fmt.Errorf("%w: fingerprint %s is %s/%s in the reference and %s/%s in the candidate",
				ErrFingerprintConflict, preview(cand.FingerprintID),
				preview(string(ref.Behavior.OperationCategory)), preview(ref.Behavior.OperationName),
				preview(string(cand.Behavior.OperationCategory)), preview(cand.Behavior.OperationName))
		}
		if id, ok := refByBehavior[cand.Behavior]; ok && id != cand.FingerprintID {
			return fmt.Errorf("%w: behavior %s/%s is fingerprint %s in the reference and %s in the candidate; "+
				"reporting this as removed and added would claim behavior changed when only its identity did",
				ErrFingerprintConflict,
				preview(string(cand.Behavior.OperationCategory)), preview(cand.Behavior.OperationName),
				preview(id), preview(cand.FingerprintID))
		}
	}
	return nil
}
