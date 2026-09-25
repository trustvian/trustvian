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
	"io"
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

// Store is every persistence capability a control plane needs, plus the
// lifecycle the thing that opened it owns.
//
// For composition only. No service takes a Store: ControlPlane takes the three
// narrow interfaces separately, which is what stops a service reaching a
// capability it has no business with. This exists so a composition root can
// hold "the store" without naming a concrete backend — see
// docs/adr/0037-postgresql-is-the-shared-platform-persistence-backend.md.
type Store interface {
	ControlStore
	EvaluationStore
	EvaluationIngestStore
	io.Closer
}

// ControlStore persists the control-plane entities a local platform owns.
//
// Create means create: an existing identity returns ErrStoreAlreadyExists and
// nothing is overwritten. This matters most for Candidate — the same
// CandidateID arriving with a different artifact digest must not rewrite what
// a finished run was evaluated against. Evaluating a different artifact means
// a different caller-owned CandidateID.
//
// One list method, for one entity, added by task 065 because that is the
// milestone with a concrete need for it: a caller cannot open an environment
// by ref when knowing the refs is the question. Its scope, order, cursor and
// limit are all decided — see ProjectEnvironments. Projects, agents,
// candidates and runs still have none, and freezing those four decisions for
// them by accident is what task 058 was avoiding.
//
// No delete method: nothing in this milestone deletes, and a delete API
// implies a retention model that does not exist. An environment is archived
// instead, which keeps every historical reference resolvable.
type ControlStore interface {
	CreateProject(ctx context.Context, project Project) error
	Project(ctx context.Context, id ProjectID) (Project, error)

	CreateAgent(ctx context.Context, agent Agent) error
	Agent(ctx context.Context, id AgentID) (Agent, error)

	CreateCandidate(ctx context.Context, candidate Candidate) error
	Candidate(ctx context.Context, id CandidateID) (Candidate, error)

	// CreateEnvironment stores one environment, with its checks ordered: a
	// missing project is ErrStoreNotFound, an existing (project, ref) is
	// ErrStoreAlreadyExists whatever the project's count, and only then a new
	// ref into a project at or over maxProjectEnvironments is
	// ErrEnvironmentLimit.
	//
	// That order matters. A caller re-sending a ref that already exists is
	// adding nothing, so the cap is irrelevant to it — reporting a limit
	// there would be misleading, and which of two racing callers saw it would
	// depend on arrival order.
	//
	// The cap is a cross-row invariant, so the whole sequence runs in one
	// transaction serialized on the owning project row. Two creates of
	// different refs into a project at 63 cannot both succeed.
	CreateEnvironment(ctx context.Context, env Environment) error

	// Environment loads one by (project, ref). Identity is the pair: the same
	// ref in two projects is two environments.
	Environment(ctx context.Context, projectID ProjectID, ref EnvironmentRef) (Environment, error)

	// UpdateEnvironment replaces previous with next, and fails with
	// ErrStoreConflict if the stored revision is no longer previous's.
	//
	// Compare-and-swap rather than a blind write, so two operators editing
	// one environment cannot silently overwrite each other. next must carry
	// the same identity as previous — moving a ref or a project is
	// ErrInvalidID — and must advance the revision by exactly one.
	UpdateEnvironment(ctx context.Context, previous, next Environment) error

	// ProjectEnvironments returns at most limit of one project's
	// environments, active and archived, whose ref sorts after `after`, in
	// ref byte order. An empty `after` starts at the beginning.
	//
	// limit is 1 to MaxEnvironmentPage inclusive; anything outside that is
	// ErrInvalidID. The range is the whole range — there is no wider limit a
	// privileged caller may pass, and no row returned beyond the one asked
	// for. A caller that needs to know whether a further page exists asks a
	// second bounded question from the last ref it received, which is what
	// the HTTP list route does.
	//
	// Traversal is by ref because ref is immutable: rank is mutable, and
	// re-ranking is one of the two operations an environment exists for, so a
	// cursor over rank could move a row between pages mid-traversal. A
	// project with none returns an empty slice; one that does not exist
	// returns ErrStoreNotFound.
	ProjectEnvironments(
		ctx context.Context, projectID ProjectID, after EnvironmentRef, limit int,
	) ([]Environment, error)
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

// Compile-time proof that every backend satisfies the composite.
//
// Cheap, and it fails at build time rather than when a composition root is
// wired, which is where the mistake would otherwise surface. PostgresStore's
// own assertion lives beside it in postgres.go, so neither backend can fall
// behind the interface without the build saying so.
var _ Store = (*SQLiteStore)(nil)
