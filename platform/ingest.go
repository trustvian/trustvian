package platform

// Evaluation ingest: the durable retry contract for DecisionRecord evidence.
//
// Task 053 made a duplicate record count twice, because dedup inside the
// reducer would need unbounded retention. That is still right, and it means
// an HTTP retry — the most ordinary thing a network client does — would
// corrupt the evidence unless something above the reducer decides what a
// repeated request means.
//
// This is that something, and it spends O(1) durable state per run doing it:
// a monotonic sequence and the digest of the last accepted record. No EventID
// set, no idempotency table, no stored records. Every ambiguous case fails
// closed.
//
// See docs/adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	trustvian "github.com/trustvian/trustvian"
)

// ErrIngestSequence reports a sequence that cannot be accepted: a gap, a
// stale number, or a retry of the last record with different content.
//
// Deliberately distinct from ErrStoreConflict. This is a protocol outcome a
// caller can reason about and act on, not a race against durable state.
var ErrIngestSequence = errors.New("platform: evaluation ingest sequence conflict")

// EvaluationIngestState is one run's durable ingest cursor.
//
// Fixed-shape and O(1): two values, whatever the run has ingested. That bound
// is what separates a retry mechanism from an archive.
type EvaluationIngestState struct {
	nextSequence uint64
	lastDigest   string
}

// NextSequence is the sequence the next accepted record must carry.
func (s EvaluationIngestState) NextSequence() uint64 { return s.nextSequence }

// LastDigest is the digest of the most recently accepted record, or empty
// when none is known.
//
// Empty is a real state, not a missing value: a run migrated from a task 057
// database has evidence but no recorded digest, and nothing fabricates one.
func (s EvaluationIngestState) LastDigest() string { return s.lastDigest }

// EvaluationIngestCommit is one atomic advance of a run's evidence and cursor.
type EvaluationIngestCommit struct {
	// PreviousNextSequence is the cursor this commit was computed against.
	// The store refuses the write if durable state has moved, so two racing
	// requests cannot both apply the same sequence.
	PreviousNextSequence uint64

	Sequence     uint64
	RecordDigest string

	Aggregate EvaluationAggregate
	Snapshot  BehaviorSnapshot
}

// EvaluationIngestStore persists the ingest cursor alongside the evidence it
// describes.
//
// One capability, not a widening of EvaluationStore: the cursor has its own
// lifetime and its own consistency rule, and a backend could reasonably
// implement evidence without it.
type EvaluationIngestStore interface {
	// EvaluationIngestState returns a run's cursor.
	//
	// A run with evidence but no cursor row — a task 057 database — reports
	// the sequence derived from its record count, with no digest. That is the
	// migration edge, and it is why the digest can legitimately be empty.
	EvaluationIngestState(ctx context.Context, id EvaluationRunID) (EvaluationIngestState, error)

	// CommitEvaluationIngest writes evidence and cursor in one transaction.
	//
	// Split, the two failure modes are silent and both bad: evidence without
	// the cursor makes the next retry double-count, and the cursor without
	// evidence loses a record the protocol believes arrived. Neither surfaces
	// as an error — they surface later as wrong numbers.
	CommitEvaluationIngest(ctx context.Context, commit EvaluationIngestCommit) error
}

// ---------------------------------------------------------------------
// Sequence and digest encoding
// ---------------------------------------------------------------------

// FormatSequence renders a sequence for the wire.
//
// Canonical decimal text rather than a JSON number: uint64 exceeds what a
// JSON double represents exactly, so a browser client would silently round
// large values. The same reasoning task 057 applied to stored counters.
func FormatSequence(sequence uint64) string { return strconv.FormatUint(sequence, 10) }

// ParseSequence decodes a wire sequence, rejecting anything non-canonical.
//
// "01", "+1", "-1", "1.0", whitespace and empty are all refused rather than
// coerced: a value needing coercion was not produced by a client following
// this contract, and guessing its intent is how a retry becomes a new record.
func ParseSequence(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: sequence %q is not a canonical uint64", ErrIngestSequence, preview(s))
	}
	if strconv.FormatUint(v, 10) != s {
		return 0, fmt.Errorf("%w: sequence %q is not canonical", ErrIngestSequence, preview(s))
	}
	if v == 0 {
		return 0, fmt.Errorf("%w: sequences start at 1", ErrIngestSequence)
	}
	return v, nil
}

// RecordDigest is the replay identity of one decoded record.
//
// Taken over the marshalled *decoded* record rather than the raw request
// bytes, so whitespace and key ordering do not make a genuine retry look like
// a different record. Go's encoder emits struct fields in declaration order,
// which makes this deterministic for a fixed-shape type.
//
// Storage metadata, not behavioral identity: not FingerprintID, not EventID.
// Those answer what a behavior is; this answers whether two requests carried
// the same bytes.
func RecordDigest(record trustvian.DecisionRecord) (string, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("platform: digest decision record: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// validateDigest refuses stored or supplied digests that are not the shape
// this code writes.
func validateDigest(field, digest string) error {
	if digest == "" {
		return nil // legitimately unknown; see EvaluationIngestState.LastDigest
	}
	if len(digest) != sha256.Size*2 {
		return fmt.Errorf("%w: %s is not a sha-256 digest", ErrStoreCorrupt, field)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("%w: %s is not hex", ErrStoreCorrupt, field)
	}
	return nil
}

// nextSequenceAfter advances a cursor, refusing to wrap.
//
// Operationally unreachable and trivially testable. A wrapped cursor would
// silently reset the protocol to "expecting sequence 0", which every
// subsequent request would then conflict against for reasons nobody could
// diagnose.
func nextSequenceAfter(sequence uint64) (uint64, error) {
	if sequence == math.MaxUint64 {
		return 0, fmt.Errorf("%w: sequence %d is the last representable value",
			ErrIngestSequence, sequence)
	}
	return sequence + 1, nil
}

// initialSequenceFor derives a starting cursor for a run that has none.
//
// One ingest is one aggregate record, so evidence of N records means the next
// sequence is N+1. A task 057 database reaches this path: it has evidence and
// no cursor, and the arithmetic is what lets ingest continue against it
// rather than restarting the count and double-aggregating.
func initialSequenceFor(recordCount uint64) (uint64, error) {
	next, err := nextSequenceAfter(recordCount)
	if err != nil {
		return 0, fmt.Errorf("%w: run already holds %d records", ErrIngestSequence, recordCount)
	}
	return next, nil
}

// ---------------------------------------------------------------------
// Collector restoration
// ---------------------------------------------------------------------

// behaviorCollectorFromSnapshot rebuilds a collector from trusted evidence.
//
// Deliberately unexported, and deliberately not offered as a public
// constructor. A caller able to build a collector from arbitrary snapshot
// fields could forge collector state — the bound marker tasks 053-057 rely on
// would become a suggestion. The snapshot reaching here has already been
// validated by the store, which is the only reason rebuilding from it is safe.
//
// This is what lets a restarted process continue a running evaluation instead
// of starting its behavioral evidence over.
func behaviorCollectorFromSnapshot(snapshot BehaviorSnapshot) (*BehaviorCollector, error) {
	if !snapshot.bound {
		return nil, fmt.Errorf("%w: snapshot did not come from a BehaviorCollector", ErrUnboundCollector)
	}

	entries := make(map[string]BehaviorEntry, len(snapshot.entries))
	byBehavior := make(map[trustvian.StableFeatures]string, len(snapshot.entries))
	for _, entry := range snapshot.entries {
		if _, duplicate := entries[entry.FingerprintID]; duplicate {
			return nil, fmt.Errorf("%w: snapshot holds fingerprint %s twice",
				ErrStoreCorrupt, preview(entry.FingerprintID))
		}
		if other, duplicate := byBehavior[entry.Behavior]; duplicate {
			return nil, fmt.Errorf("%w: fingerprints %s and %s describe the same behavior",
				ErrStoreCorrupt, preview(other), preview(entry.FingerprintID))
		}
		entries[entry.FingerprintID] = entry
		byBehavior[entry.Behavior] = entry.FingerprintID
	}

	return &BehaviorCollector{
		bound:        true,
		runID:        snapshot.runID,
		candidateID:  snapshot.candidateID,
		environment:  snapshot.environment,
		profile:      snapshot.profile,
		observations: snapshot.observations,
		entries:      entries,
		byBehavior:   byBehavior,
		complete:     snapshot.complete,
	}, nil
}
