package platform

// Platform persistence capabilities.
//
// Narrow domain capabilities, not a database. There is deliberately no
// Database interface with Query/Exec/Put(any)/Get(any): that is SQL with the
// type system removed, and it would move persistence decisions into every
// call site. ADR 0023 says a store is an adapter expressed in domain terms,
// and these are the terms.
//
// See docs/adr/0030-local-persistence-stores-authoritative-bounded-state.md.

import (
	"context"
	"errors"
)

var (
	// ErrStoreNotFound reports a requested identity that does not exist.
	//
	// Also reported when a create names a parent that does not exist: an
	// agent under an unknown project is a missing project, not a malformed
	// agent.
	ErrStoreNotFound = errors.New("platform: stored value not found")

	// ErrStoreAlreadyExists reports a create for an identity already stored.
	//
	// Create means create. The stored value is left exactly as it was, even
	// when the incoming value carries different data — that is the case this
	// error exists for.
	ErrStoreAlreadyExists = errors.New("platform: stored value already exists")

	// ErrStoreConflict reports a stale lifecycle update or an incompatible
	// evidence replacement.
	//
	// Not corruption, and not a caller mistake in the usual sense: the value
	// supplied was valid and the store's current state moved on.
	ErrStoreConflict = errors.New("platform: stored value changed")

	// ErrStoreCorrupt reports stored rows that cannot reconstruct a valid
	// platform value.
	//
	// Nothing is repaired, clamped, or normalized on the way out. A restored
	// value either satisfies every invariant a live one would, or it is not
	// returned.
	ErrStoreCorrupt = errors.New("platform: stored data is corrupt")

	// ErrStoreSchemaVersion reports a schema that is unknown, unsupported,
	// or ambiguous.
	//
	// Ambiguous is the interesting one: recognized tables with no version
	// metadata is refused rather than adopted, because the safe reading is
	// "something else wrote here", not "empty".
	ErrStoreSchemaVersion = errors.New("platform: unsupported persistence schema")
)

// ControlStore persists the control-plane entities a local platform owns.
//
// Create means create: an existing identity returns ErrStoreAlreadyExists and
// nothing is overwritten. This matters most for Candidate — the same
// CandidateID arriving with a different artifact digest must not rewrite what
// a finished run was evaluated against. Evaluating a different artifact means
// a different caller-owned CandidateID.
//
// No list, search, filter or pagination method. Task 058 has not specified
// sort order, cursor semantics, limits or parent scoping, and freezing any of
// them here would decide them by accident. No delete method either: nothing
// in this milestone deletes, and a delete API implies a retention model that
// does not exist.
type ControlStore interface {
	CreateProject(ctx context.Context, project Project) error
	Project(ctx context.Context, id ProjectID) (Project, error)

	CreateAgent(ctx context.Context, agent Agent) error
	Agent(ctx context.Context, id AgentID) (Agent, error)

	CreateCandidate(ctx context.Context, candidate Candidate) error
	Candidate(ctx context.Context, id CandidateID) (Candidate, error)
}

// EvaluationStore persists evaluation runs and their evidence.
//
// Separate from ControlStore because the two have different lifetimes, write
// patterns and consumers: control entities are created once and read many
// times, evidence is rewritten as a run progresses and is always read as a
// pair. A later backend may reasonably implement one and not the other.
type EvaluationStore interface {
	CreateEvaluationRun(ctx context.Context, run EvaluationRun) error
	EvaluationRun(ctx context.Context, id EvaluationRunID) (EvaluationRun, error)

	// UpdateEvaluationRun replaces previous with next, and fails with
	// ErrStoreConflict if the stored value is no longer previous.
	//
	// Compare-and-swap rather than a blind write, so a caller holding a
	// stale run cannot overwrite a terminal state somebody else recorded.
	// next must also be a legitimate domain transition from previous, which
	// is checked by invoking the domain's own transition methods rather than
	// by re-implementing the state machine here.
	UpdateEvaluationRun(ctx context.Context, previous, next EvaluationRun) error

	// SaveEvaluationEvidence replaces one run's latest aggregate and
	// behavioral snapshot, atomically.
	//
	// Both together, never separately. They are two views of the same run,
	// and written independently an aggregate from observation N could commit
	// beside a snapshot from N-1 and become the durable truth — a decision
	// distribution and a behavioral summary describing different moments,
	// with nothing recording that they disagree.
	//
	// Evidence never moves backwards: a lower observation count is a
	// conflict, an identical rewrite at the same count is idempotent, and a
	// divergent one at the same count is a conflict rather than a silent
	// choice between two views.
	SaveEvaluationEvidence(ctx context.Context, aggregate EvaluationAggregate, snapshot BehaviorSnapshot) error

	// EvaluationEvidence loads one run's latest aggregate and snapshot.
	//
	// Both are validated before their private bound markers are set. A
	// restored value that cannot satisfy every invariant a live one would is
	// ErrStoreCorrupt, not a partially trusted value.
	EvaluationEvidence(ctx context.Context, id EvaluationRunID) (EvaluationAggregate, BehaviorSnapshot, error)
}
