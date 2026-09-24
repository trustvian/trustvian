package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	trustvian "github.com/trustvian/trustvian"
)

// LearnFunc applies the local half of one record's delivery.
//
// The sink calls it exactly once per record, and only after the control
// plane has confirmed that record — never for one it declined, and never for
// one whose outcome is still unknown. That ordering is the whole point:
// learning that runs ahead of confirmation outlives the process in a durable
// store, and nothing afterwards can tell it apart from learning the run's
// evidence actually accounts for.
//
// The payload is whatever the caller handed to Record beside the record.
// This package never interprets it, which is what keeps the engine out of
// the sink — see processor.go for the half that decodes it back into a
// Result and observes it.
type LearnFunc func(ctx context.Context, learning []byte) error

// Sink posts DecisionRecords to one evaluation run, in order, and gates
// their learning on the control plane having accepted them.
//
// The producer owns the sequence (ADR 0031 §8), so this owns a cursor. The
// cursor is seeded from the server by Initialize and thereafter advances
// only to the number the server returned — nothing is derived locally, so a
// client-side increment can never disagree with durable state.
//
// Delivery is not assumed, and neither is its absence. A POST whose outcome
// is unknown binds its sequence to its record — durably, before the request
// is sent — until the server settles it. That is what makes a lost response
// recoverable rather than corrupting, both within this process (Record) and
// across a restart (Initialize).
type Sink struct {
	client  *client
	runID   string
	profile string

	// journal is the durable half of pending, below. Written before a
	// request can be delivered and released only once its record's fate is
	// settled, so a process that dies in between leaves behind the one fact
	// its successor cannot otherwise obtain.
	journal *journal

	// learn applies a confirmed record's learning. Held here rather than
	// left to the caller after Record returns, because the ordering between
	// confirmation, learning and releasing the sequence is exactly what must
	// not depend on a caller getting it right.
	learn LearnFunc

	// mu covers allocate → record intent → POST → learn → advance as one
	// critical section.
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

	// pending is the one record whose delivery is not settled, or nil. It
	// mirrors the journal entry: both are written before the POST and both
	// are released together.
	//
	// Bounded at one by construction — mu serializes the whole cycle, so
	// there is never a second record in flight to become unsettled. It is a
	// held position, not a queue: nothing is buffered for later delivery and
	// nothing is dropped.
	pending *pendingRecord
}

// pendingRecord is a sequence bound to the exact record that claimed it, and
// to the learning that record owes.
//
// All three parts matter. The sequence must not be handed to another record,
// because the server may already have committed this one there. The record
// must be kept byte-identical, because the server's replay rule recognizes a
// retry by digest: the same sequence carrying different content is two
// records claiming one position, and the control plane rejects it —
// correctly — as a conflict. The learning must be kept because it is the
// only thing that can complete the local half afterwards, and it cannot be
// recomputed: analyzing again produces a different Result, and rebuilding
// one from the record is the reconstruction ADR 0038 §2 rejects.
//
// The behavioral profile is the fourth part of the logical request and is
// not copied here: it is fixed for the sink's lifetime, so a re-presented
// record necessarily carries the same one.
type pendingRecord struct {
	sequence uint64
	record   trustvian.DecisionRecord
	learning json.RawMessage

	// state mirrors the durable entry's own, because the two mean different
	// things to the next call. posting is recoverable in place: the record
	// can be re-presented and, when the control plane confirms it, learned
	// from. confirmed is not: the record is in the run and its learning
	// either happened or did not, so re-presenting it would risk a second
	// observation and abandoning it would risk none at all. Only a restart
	// resolves that, deliberately, and this field is what stops a later
	// record from stepping past it in the meantime.
	state pendingState
}

// ErrUnresolved reports that a record's fate is still unknown: the control
// plane may hold it durably, and the sink could not confirm either way.
//
// The sequence stays bound to that record, on disk as well as in memory, so
// the next call — or the next process — finishes it rather than guessing.
var ErrUnresolved = errors.New("evaluation ingest outcome is unresolved")

// ErrLearningIndeterminate reports the other half of the same problem: the
// control plane definitely holds the record, and whether its learning
// reached the Engine's store cannot be established.
//
// It is distinct from ErrUnresolved because the two describe opposite
// certainties — one is "the record may not be there", the other is "the
// record is there" — and because only one of them is recoverable in this
// process. A sink in this state accepts no further records: re-learning
// could double an observation and dropping the entry could lose one, and
// neither is a choice the next span gets to make on its own. Restarting the
// Collector resolves it (ADR 0038 §10).
var ErrLearningIndeterminate = errors.New("evaluation record learning is indeterminate")

// RecoveryOutcome is what became of a record a previous process left
// pending.
type RecoveryOutcome string

const (
	// RecoveryNone means nothing was pending: an ordinary start.
	RecoveryNone RecoveryOutcome = ""

	// RecoveryCompleted means the control plane held the record and its
	// learning was applied during startup — exactly once, because the
	// pending state proved the previous process had not applied it.
	RecoveryCompleted RecoveryOutcome = "completed"

	// RecoveryDiscarded means the record never reached the control plane:
	// the run still expects that very sequence. Nothing was learned from it,
	// nothing is posted for it now, and the sequence goes to the next
	// record. The span it came from was dropped with its batch, and its
	// record follows it rather than arriving alone after a restart.
	RecoveryDiscarded RecoveryOutcome = "discarded"

	// RecoveryIndeterminate is the one case a restart cannot settle: the
	// control plane holds the record, and whether the previous process
	// applied its learning before dying is unknowable. It is not applied
	// again.
	RecoveryIndeterminate RecoveryOutcome = "indeterminate"
)

// Recovery describes what Initialize found left over from a previous
// process, so the caller can report it. The zero value means a clean start.
type Recovery struct {
	// Outcome is what became of the pending record, if there was one.
	Outcome RecoveryOutcome

	// Sequence is the record found pending, or 0 when there was none.
	Sequence uint64

	// Disposition is what the control plane said when the record was
	// re-presented, for RecoveryCompleted: replayed when it already held the
	// record, applied in the narrow race where the cursor moved between
	// reading it and re-presenting.
	Disposition string
}

// New validates the configuration and returns an uninitialized Sink.
//
// Construction performs no network I/O, so an unreachable control plane
// fails at Start with a clear message rather than inside pipeline graph
// construction — while a pending state path that cannot work fails before a
// single span depends on it.
func New(apiURL, runID, behavioralProfile, pendingStatePath string, learn LearnFunc) (*Sink, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	if behavioralProfile == "" {
		return nil, errors.New("behavioral_profile is required")
	}
	if learn == nil {
		return nil, errors.New("a learn function is required")
	}
	c, err := newClient(apiURL, requestTimeout)
	if err != nil {
		return nil, err
	}
	j, err := newJournal(pendingStatePath)
	if err != nil {
		return nil, err
	}
	return &Sink{client: c, runID: runID, profile: behavioralProfile, journal: j, learn: learn}, nil
}

// RunID is the run this sink feeds.
func (s *Sink) RunID() string { return s.runID }

// Initialize confirms the run is running, finishes whatever a previous
// process left pending, and seeds the cursor.
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
//
// Recovery is the part that makes a durable baseline safe across a restart.
// A pending entry is a record whose two halves — the run's evidence and this
// Collector's learning — the previous process could not be sure it had
// joined. The state it is in, and the run's own cursor, say which:
//
//	confirmed, cursor N+1  The run holds the record; whether its learning was
//	                     applied before the process died cannot be known. It
//	                     is not applied again: an observation that is missing
//	                     makes a fingerprint look less familiar, which fails
//	                     safe, while one applied twice makes it look more
//	                     familiar than the evidence supports, which is a
//	                     silent weakening. The caller is told, so it is not
//	                     silent.
//
//	confirmed, any other cursor  A process that died holding sequence N
//	                     cannot have produced N+1, so a run expecting
//	                     anything but N+1 was written by something else —
//	                     below N+1 it does not hold a record it accepted,
//	                     above it there is evidence this Collector never
//	                     analyzed. Refuse.
//
//	posting, cursor N    The run still expects that record's own sequence, so
//	                     the record never reached it. The previous process had
//	                     not learned from it either — a posting entry is
//	                     replaced the instant the control plane confirms a
//	                     record — so both halves agree at nothing. The entry
//	                     is discarded and the sequence goes to the next
//	                     record. Nothing is posted for a span whose batch was
//	                     already dropped.
//
//	posting, cursor N+1  Something occupies that sequence. The record is
//	                     re-presented there, which is the only proof
//	                     available that the something is this record: an
//	                     identical one replays, anything else conflicts. The
//	                     replay changes no evidence, and only then is the
//	                     learning applied — exactly once, here, before the
//	                     first new span is analyzed.
//
//	anything else        Another writer has been in this run. Refuse.
func (s *Sink) Initialize(ctx context.Context) (Recovery, error) {
	status, err := s.client.runStatus(ctx, s.runID)
	if err != nil {
		return Recovery{}, fmt.Errorf("reading run status for run %s: %w", s.runID, err)
	}
	if status != runStatusRunning {
		return Recovery{}, fmt.Errorf(
			"run %s is %s, not running; evaluation ingest requires a run that has already been started",
			s.runID, status)
	}

	entry, found, err := s.journal.load()
	if err != nil {
		return Recovery{}, fmt.Errorf("reading the pending ingest state: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if found {
		if entry.RunID != s.runID {
			// Fail closed rather than discard it. The entry describes a
			// record whose fate is unsettled for *that* run, which this
			// process cannot settle; deleting it would destroy the only
			// evidence that it was ever in flight.
			return Recovery{}, fmt.Errorf(
				"the pending ingest state belongs to run %s, but this Collector is configured for run %s; "+
					"point pending_state_path at this run's own file, or settle that run before reusing it",
				entry.RunID, s.runID)
		}
		sequence, err := parseSequence(entry.Sequence)
		if err != nil {
			return Recovery{}, fmt.Errorf("reading the pending ingest state: %w", err)
		}

		next, err := s.client.ingestState(ctx, s.runID)
		if err != nil {
			return Recovery{}, fmt.Errorf("reading ingest state for run %s: %w", s.runID, err)
		}

		switch {
		case entry.State == stateConfirmed && next != sequence+1:
			// A confirmed entry says the control plane accepted that record
			// and the process then died before releasing it. Such a process
			// cannot have produced the record after it, so the run can only
			// be expecting exactly the next sequence.
			//
			// Anything lower is a contradiction: the record was accepted, yet
			// the run does not hold it — a restored backup, or a run
			// identifier something else is using. Anything higher means a
			// second writer added records this Collector never analyzed, and
			// resuming would step over evidence whose learning is nobody's
			// to account for. The single-writer assumption is what the whole
			// sequence contract rests on, so this refuses rather than
			// guessing, and leaves the entry in place.
			return Recovery{}, fmt.Errorf(
				"the pending ingest state records sequence %d as accepted, so run %s should expect %d, "+
					"but it expects %d; this run has been written by something else "+
					"and cannot be resumed safely",
				sequence, s.runID, sequence+1, next)

		case entry.State == stateConfirmed:
			if err := s.journal.clear(); err != nil {
				return Recovery{}, fmt.Errorf("releasing the pending ingest state: %w", err)
			}
			s.next = next
			s.initialized = true
			return Recovery{Outcome: RecoveryIndeterminate, Sequence: sequence}, nil

		case next == sequence:
			// The run still expects this very sequence, so the record never
			// reached it. Nothing was applied and nothing may be learned
			// from it — the two halves agree, at nothing — and the sequence
			// goes to the next record.
			if err := s.journal.clear(); err != nil {
				return Recovery{}, fmt.Errorf("releasing the pending ingest state: %w", err)
			}
			s.next = next
			s.initialized = true
			return Recovery{Outcome: RecoveryDiscarded, Sequence: sequence}, nil

		case next == sequence+1:
			// Something occupies that sequence. Re-presenting the record is
			// the only proof available that the something is this record:
			// the control plane replays an identical one and conflicts on
			// anything else. A replay changes no evidence, and only then is
			// the learning applied — exactly once, because a posting entry
			// proves the previous process never got that far.
			s.pending = &pendingRecord{
				sequence: sequence, record: entry.Record,
				learning: entry.Learning, state: statePosting}
			s.next = sequence
			disposition, err := s.reconcile(ctx)
			if err != nil {
				return Recovery{}, fmt.Errorf(
					"finishing the record left pending at sequence %d: %w", sequence, err)
			}
			s.initialized = true
			return Recovery{
				Outcome: RecoveryCompleted, Sequence: sequence, Disposition: disposition}, nil

		default:
			// The run is somewhere this record cannot account for: behind it,
			// or more than one record ahead. Either way another writer has
			// been here, and guessing which records are whose is how evidence
			// and learning diverge for good.
			return Recovery{}, fmt.Errorf(
				"the pending ingest state holds sequence %d but run %s expects %d; "+
					"this run has been written by something else and cannot be resumed safely",
				sequence, s.runID, next)
		}
	}

	next, err := s.client.ingestState(ctx, s.runID)
	if err != nil {
		return Recovery{}, fmt.Errorf("reading ingest state for run %s: %w", s.runID, err)
	}
	s.next = next
	s.initialized = true
	return Recovery{}, nil
}

// Record delivers one record and, once the control plane confirms it,
// applies its learning — exactly once, in that order, never the reverse.
//
// Every call leaves the sink in one of four states, each of them durable:
//
//	idle           no sequence is held; the next record takes the cursor
//	posting        one sequence is bound to one record whose delivery is
//	               unknown; nothing has been learned from it
//	confirmed      the control plane holds the record and its learning is
//	               not established; no further record is accepted, because
//	               only a restart can settle that (see ErrLearningIndeterminate)
//	settled        the run holds the record, its learning has been applied,
//	               and the sequence has been released
//
// The intent is written to disk *before* the request is sent. That ordering
// is what a restart depends on: a record the control plane may hold can
// never be one this Collector has forgotten, and a record it has not
// confirmed can never be one this Collector has already learned from.
//
// A record whose outcome is unknown is not forgotten and its sequence is not
// reused. It is re-presented, unchanged, at the same sequence: the control
// plane replays an identical record at the previous sequence rather than
// conflicting on it, so a lost response costs one redundant POST and nothing
// else. That reconciliation happens inside this call, before it returns, so
// the ordinary response-loss case ends in success.
//
// Nothing retries on a timer, in the background, or with a fresh context.
// There is exactly one extra attempt per call, made synchronously under the
// same ctx; a sink that still cannot settle the record reports ErrUnresolved
// and refuses to accept another one until it can.
func (s *Sink) Record(
	ctx context.Context, record trustvian.DecisionRecord, learning []byte,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return "", errors.New("evaluation sink used before its ingest state was read")
	}
	if len(learning) == 0 {
		// Without it a confirmed record could not be learned from — here or
		// after a restart — which is the divergence this design prevents.
		return "", errors.New("a record must carry its learning payload")
	}

	// Anything left unsettled by an earlier call is finished first. Until it
	// is, the cursor still points at its sequence, and handing that sequence
	// to this record would either conflict against the committed one or
	// silently take its place.
	if s.pending != nil {
		if s.pending.state == stateConfirmed {
			// The control plane holds that record and its learning is
			// indeterminate. Nothing this call can do settles that: applying
			// the learning again could double an observation, and stepping
			// over it could lose one. Both are decisions about a record this
			// span has nothing to do with, so the sink stops instead.
			return "", fmt.Errorf(
				"%w: sequence %d is in the run but its learning was never confirmed; "+
					"restart the Collector to settle it before recording another span",
				ErrLearningIndeterminate, s.pending.sequence)
		}
		if _, err := s.reconcile(ctx); err != nil {
			return "", err
		}
	}

	sequence := s.next
	held := &pendingRecord{
		sequence: sequence, record: record, learning: learning, state: statePosting}
	if err := s.journal.write(entryFor(s.runID, statePosting, held)); err != nil {
		// Nothing has been sent, so the sequence is untouched and this
		// record simply did not happen. Refusing here is what keeps the
		// promise the write-ahead makes: no request is ever in flight
		// without a durable record of it.
		return "", fmt.Errorf("recording the pending ingest state: %w", err)
	}
	s.pending = held

	disposition, err := s.attempt(ctx, sequence, record)
	if err == nil {
		if settleErr := s.settle(ctx); settleErr != nil {
			return "", settleErr
		}
		return disposition, nil
	}
	if !outcomeUnknown(err) {
		// The server declined it, so the record is not there, the sequence
		// was never consumed, and nothing may be learned from it.
		s.pending = nil
		if clearErr := s.journal.clear(); clearErr != nil {
			return "", errors.Join(err, fmt.Errorf("releasing the pending ingest state: %w", clearErr))
		}
		return "", err
	}

	// The outcome is unknown: this record may already be durable at this
	// sequence. It stays bound to it — in memory and on disk — and is
	// settled in this same call if the control plane can still be reached.
	return s.reconcile(ctx)
}

// reconcile re-presents the pending record at its own sequence and settles
// it.
//
// Success — applied or replayed, the server's choice — is what proves the
// record's fate, applies its learning and releases the sequence. Any
// failure, definitive or not, leaves it pending: once an attempt's outcome
// was unknown, nothing a later attempt reports can prove the record is
// absent, so its sequence stays bound to it rather than being handed on.
func (s *Sink) reconcile(ctx context.Context) (string, error) {
	held := s.pending
	disposition, err := s.attempt(ctx, held.sequence, held.record)
	if err != nil {
		return "", fmt.Errorf(
			"%w: sequence %d holds a record the control plane may already have; "+
				"it must be reconciled before another record can use that sequence: %w",
			ErrUnresolved, held.sequence, err)
	}
	if err := s.settle(ctx); err != nil {
		return "", err
	}
	return disposition, nil
}

// settle applies the local half of a delivery the control plane has
// confirmed, then releases the sequence.
//
// The order is the contract. Marking the entry confirmed before learning is
// what makes a posting entry mean "this was definitely not learned from",
// which is the fact a restart completes exactly once. Releasing only after
// learning is what stops a crash in between from dropping the learning for a
// record the run holds.
func (s *Sink) settle(ctx context.Context) error {
	held := s.pending
	if err := s.journal.write(entryFor(s.runID, stateConfirmed, held)); err != nil {
		// The record is in the run, but nothing may be learned from it until
		// that fact is durable: a crash now must re-present the record rather
		// than assume the learning happened. The entry stays posting in
		// memory, so that is exactly what the next attempt — or the next
		// process — does. If the failure was the directory sync, the disk may
		// already say confirmed; both readings are safe, because one
		// re-presents and learns once and the other reports an indeterminate
		// state and learns nothing.
		return fmt.Errorf(
			"%w: sequence %d was accepted but its pending state could not be updated: %w",
			ErrUnresolved, held.sequence, err)
	}
	held.state = stateConfirmed

	if err := s.learn(ctx, held.learning); err != nil {
		// The record is in the run and the learning failed. Whether anything
		// reached the store cannot be known from here — a FileStore updates
		// its in-memory baseline before the flush that failed, and a database
		// error can arrive on either side of a commit — so the entry stays,
		// confirmed, as the note that says exactly that. Deleting it would
		// leave the run holding a record with nothing anywhere to say its
		// learning was never established, which is the divergence this whole
		// mechanism exists to prevent.
		//
		// It is not retried either: a second attempt against a store that may
		// have applied the first is how one observation becomes two.
		return fmt.Errorf(
			"%w: sequence %d is in the run but applying its learning failed: %w",
			ErrLearningIndeterminate, held.sequence, err)
	}

	s.pending = nil
	if err := s.journal.clear(); err != nil {
		// Both halves are done; only the release is unproven. That is safe in
		// either direction — an entry that survives tells the next process
		// the learning is indeterminate, and it will not be applied twice —
		// but a filesystem that cannot delete durably is about to fail a
		// write too, so it is reported rather than swallowed.
		return fmt.Errorf("releasing the pending ingest state: %w", err)
	}
	return nil
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

// entryFor renders the durable form of a held record.
func entryFor(runID string, state pendingState, held *pendingRecord) pendingEntry {
	return pendingEntry{
		Version:  pendingVersion,
		RunID:    runID,
		Sequence: formatSequence(held.sequence),
		State:    state,
		Record:   held.record,
		Learning: held.learning,
	}
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
