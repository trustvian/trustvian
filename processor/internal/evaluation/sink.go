package evaluation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	trustvian "github.com/trustvian/trustvian"
)

// Sink posts DecisionRecords to one evaluation run, in order.
//
// The producer owns the sequence (ADR 0031 §8), so this owns a cursor. The
// cursor is seeded from the server by Initialize and thereafter advances
// only to the number the server returned — nothing is derived locally, so a
// client-side increment can never disagree with durable state.
//
// Delivery is not assumed. A POST whose outcome is unknown binds its
// sequence to its record until the server settles it, which is the one thing
// that makes a lost response recoverable rather than corrupting: see Record.
type Sink struct {
	client  *client
	runID   string
	profile string

	// mu covers allocate → POST → advance as one critical section.
	//
	// An atomic counter would be wrong, not merely coarse. The ingest
	// contract is gap-free and strictly monotonic: two goroutines holding 5
	// and 6 race, and if 6 arrives first the server refuses it as a gap —
	// correctly, since it cannot know 5 is in flight. Reordering would have
	// to be reimplemented here, which is the machinery the explicit-sequence
	// contract exists to avoid.
	//
	// The cost is one loopback round trip per analyzed span, serialized. That
	// is the price of an evaluation-configured Collector, which is dedicated
	// to one run; a Collector with no evaluation block takes no lock at all.
	mu          sync.Mutex
	next        uint64
	initialized bool

	// pending is the one record whose fate is unknown, or nil.
	//
	// Bounded at one by construction: mu serializes allocate → POST →
	// advance, so there is never a second record in flight to become
	// unknown. It is a held position, not a queue — nothing is buffered for
	// later delivery, and nothing is dropped.
	pending *pendingRecord
}

// pendingRecord is a sequence bound to the exact record that claimed it.
//
// Both halves matter. The sequence must not be handed to another record,
// because the server may already have committed this one there. The record
// must be kept byte-identical, because the server's replay rule recognizes a
// retry by digest: the same sequence carrying different content is two
// records claiming one position, and the control plane rejects it —
// correctly — as a conflict.
//
// The behavioral profile is the third part of the logical request and is not
// copied here: it is fixed for the sink's lifetime, so a re-presented record
// necessarily carries the same one.
type pendingRecord struct {
	sequence uint64
	record   trustvian.DecisionRecord
}

// ErrUnresolved reports that a record's fate is still unknown: the control
// plane may hold it durably, and the sink could not confirm either way.
//
// It exists so a caller can tell this apart from a definitive refusal
// without inspecting transport details. The two demand opposite handling of
// whatever local bookkeeping accompanies the record — the processor observes
// a Result whose record may be durable, and does not observe one that
// certainly is not (see processor.go).
var ErrUnresolved = errors.New("evaluation ingest outcome is unresolved")

// New validates the configuration and returns an uninitialized Sink.
//
// Construction never performs I/O, so an unreachable control plane fails at
// Start with a clear message rather than inside pipeline graph construction.
func New(apiURL, runID, behavioralProfile string) (*Sink, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	if behavioralProfile == "" {
		return nil, errors.New("behavioral_profile is required")
	}
	c, err := newClient(apiURL, requestTimeout)
	if err != nil {
		return nil, err
	}
	return &Sink{client: c, runID: runID, profile: behavioralProfile}, nil
}

// RunID is the run this sink feeds.
func (s *Sink) RunID() string { return s.runID }

// Initialize confirms the run is actually running, then seeds the cursor
// from the control plane.
//
// Called from Start rather than lazily on the first span, so a Collector
// whose control plane is unreachable refuses to come up instead of enriching
// spans for an unknown period while recording nothing. It is also what lets
// a restarted Collector resume a running evaluation rather than restarting
// the count and double-aggregating.
//
// The status check exists because IngestDecisionRecord (the control plane's
// own guard) refuses every record for a run that is not RunRunning — so
// without this, a Collector pointed at a run that was created but never
// started, or one that already finished, would pass Initialize, log ready,
// serve /readyz 200, and then fail every span from the first one onward.
// Checking once here, against the run's own progress, turns that into one
// clear startup refusal naming the actual status instead.
//
// Called once, from Start, before any Record — so seeding the cursor can
// never discard a held sequence, and a restarted Collector begins from the
// server's own number with nothing pending.
func (s *Sink) Initialize(ctx context.Context) error {
	status, err := s.client.runStatus(ctx, s.runID)
	if err != nil {
		return fmt.Errorf("reading run status for run %s: %w", s.runID, err)
	}
	if status != runStatusRunning {
		return fmt.Errorf(
			"run %s is %s, not running; evaluation ingest requires a run that has already been started",
			s.runID, status)
	}

	next, err := s.client.ingestState(ctx, s.runID)
	if err != nil {
		return fmt.Errorf("reading ingest state for run %s: %w", s.runID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = next
	s.initialized = true
	return nil
}

// Record posts one record and reports the server's disposition.
//
// Three states, and every call leaves the sink in one of them:
//
//	idle      — no sequence is held; the next record takes the cursor
//	pending   — one sequence is bound to one record whose fate is unknown
//	reconciled — that record's fate is settled and the cursor has moved
//
// A record whose outcome is unknown is not forgotten and its sequence is not
// reused. It is re-presented, unchanged, at the same sequence: the control
// plane replays an identical record at the previous sequence rather than
// conflicting on it, so a lost response costs one redundant POST and nothing
// else. That reconciliation happens inside this call, before it returns, so
// the ordinary response-loss case ends in success — which is what keeps a
// caller's own bookkeeping (the Engine.Observe that follows this in
// processor.go) from being skipped for a record the run actually holds.
//
// Nothing retries on a timer, in the background, or with a fresh context.
// There is exactly one extra attempt per call, made synchronously under the
// same ctx; a sink that still cannot resolve the record reports
// ErrUnresolved and refuses to accept another one until it can.
//
// The cursor advances only to the server's number, and only on a recognized
// disposition. Nothing is derived locally.
func (s *Sink) Record(ctx context.Context, record trustvian.DecisionRecord) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return "", errors.New("evaluation sink used before its ingest state was read")
	}

	// Anything left unresolved by an earlier call is settled first. Until it
	// is, the cursor still points at its sequence, and handing that sequence
	// to this record would either conflict against the committed one or
	// silently take its place.
	if s.pending != nil {
		if _, err := s.reconcile(ctx); err != nil {
			return "", err
		}
	}

	sequence := s.next
	disposition, err := s.attempt(ctx, sequence, record)
	if err == nil {
		return disposition, nil
	}
	if !outcomeUnknown(err) {
		// The server declined it, so the record is not there and the
		// sequence was never consumed. The next record takes it.
		return "", err
	}

	// The outcome is unknown: this record may already be durable at this
	// sequence. Bind the two together before anything else can use either,
	// then settle it in this same call.
	s.pending = &pendingRecord{sequence: sequence, record: record}
	return s.reconcile(ctx)
}

// reconcile re-presents the pending record at its own sequence.
//
// Success — applied or replayed, the server's choice — is what proves the
// record's fate and releases the sequence. Any failure, definitive or not,
// leaves it pending: once the first attempt's outcome was unknown, nothing a
// later attempt reports can prove the record is absent, so its sequence
// stays bound to it rather than being handed on.
func (s *Sink) reconcile(ctx context.Context) (string, error) {
	held := s.pending
	disposition, err := s.attempt(ctx, held.sequence, held.record)
	if err != nil {
		return "", fmt.Errorf(
			"%w: sequence %d holds a record the control plane may already have; "+
				"it must be reconciled before another record can use that sequence: %w",
			ErrUnresolved, held.sequence, err)
	}
	s.pending = nil
	return disposition, nil
}

// attempt posts one record at one sequence and advances the cursor on a
// recognized success.
//
// Both refusals below mark the outcome unknown rather than definitive: the
// server answered 2xx, so it accepted the record, and a reply this client
// cannot make sense of hides what it did rather than proving it did nothing.
// Failing closed and holding the sequence are the same decision here.
func (s *Sink) attempt(
	ctx context.Context, sequence uint64, record trustvian.DecisionRecord,
) (string, error) {
	result, err := s.client.ingest(ctx, s.runID, sequence, s.profile, record)
	if err != nil {
		return "", err
	}

	switch result.Disposition {
	case dispositionApplied, dispositionReplayed:
	default:
		// Fail closed. An unrecognized disposition may or may not mean the
		// record landed, and recording it as a success would report evidence
		// that might not exist.
		return "", unknownOutcome(fmt.Errorf(
			"control plane reported unrecognized disposition %q", result.Disposition))
	}

	if result.NextSequence <= sequence {
		// A server claiming success without advancing would freeze the
		// cursor, and every later span would conflict against it forever.
		return "", unknownOutcome(fmt.Errorf(
			"control plane accepted sequence %d but reports %d next; the cursor did not advance",
			sequence, result.NextSequence))
	}
	s.next = result.NextSequence

	return result.Disposition, nil
}

// nextSequence reports the cursor, for tests.
func (s *Sink) nextSequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// pendingSequence reports the held sequence, or 0 when none is held, for
// tests.
func (s *Sink) pendingSequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return 0
	}
	return s.pending.sequence
}
