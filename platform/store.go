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
// Task 065 added the first list method, for one entity, because that was the
// milestone with a concrete need for it: a caller cannot open an environment
// by ref when knowing the refs is the question. Task 074 added the rest of the
// hierarchy for the same reason and no sooner — a browser that reloads with no
// live traffic has to find what exists, and every alternative (browser
// storage, a database handed to a static client, pretending realtime replays)
// is refused on its own grounds. Every collection shares one scope, order,
// cursor and limit design; see ProjectEnvironments for the long form.
//
// No delete method: nothing in this milestone deletes, and a delete API
// implies a retention model that does not exist. An environment is archived
// instead, which keeps every historical reference resolvable.
type ControlStore interface {
	CreateProject(ctx context.Context, project Project) error
	Project(ctx context.Context, id ProjectID) (Project, error)

	// Projects returns at most limit projects whose id sorts after `after`,
	// in id byte order. An empty `after` starts at the beginning.
	//
	// The one unscoped collection in this API, and deliberately so: it is the
	// root of the hierarchy, and without it there is no entry point that does
	// not require already knowing an identifier. It is bounded by the same
	// MaxListPage as every scoped one — being the root buys no exemption.
	//
	// limit outside 1..MaxListPage is ErrInvalidID. There is no parent to be
	// missing, so there is no ErrStoreNotFound here; an empty platform returns
	// an empty slice.
	Projects(ctx context.Context, after ProjectID, limit int) ([]Project, error)

	CreateAgent(ctx context.Context, agent Agent) error
	Agent(ctx context.Context, id AgentID) (Agent, error)

	// ProjectAgents returns at most limit of one project's agents whose id
	// sorts after `after`, in id byte order.
	//
	// Traversal is by id because id is immutable and an agent's name is not.
	// Ordering by a mutable column lets a rename move a row between pages
	// mid-traversal, which silently skips and repeats entries. The same
	// reasoning ProjectEnvironments gives for ref over rank.
	//
	// limit outside 1..MaxListPage is ErrInvalidID. A project with no agents
	// returns an empty slice; a project that does not exist returns
	// ErrStoreNotFound, because "there is nothing here" and "there is no here"
	// are different answers and a caller acts differently on each.
	ProjectAgents(
		ctx context.Context, projectID ProjectID, after AgentID, limit int,
	) ([]Agent, error)

	CreateCandidate(ctx context.Context, candidate Candidate) error
	Candidate(ctx context.Context, id CandidateID) (Candidate, error)

	// AgentCandidates returns at most limit of one agent's candidates whose id
	// sorts after `after`, in id byte order.
	//
	// Not by created time: candidate metadata carries no timestamp, and the
	// schema's timestamps are RFC3339Nano text, which is not lexically
	// ordered. Newest-first over an unbounded history is task 067's subject.
	//
	// limit outside 1..MaxListPage is ErrInvalidID; a missing agent is
	// ErrStoreNotFound; an agent with no candidates is an empty slice.
	AgentCandidates(
		ctx context.Context, agentID AgentID, after CandidateID, limit int,
	) ([]Candidate, error)

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

	// CreatePromotion stores one decision, in one transaction that first
	// revalidates the environment state the decision was built on.
	//
	// A promotion is immutable, so this is the only write: there is no update
	// and no delete at any layer. A promotion whose ID already exists is
	// ErrStoreAlreadyExists, whatever the rest of the promotion says — the
	// identifier names a decision that was already recorded.
	//
	// The revalidation is not a courtesy. A promotion carries the revision of
	// each environment it was decided against, and those revisions must still
	// be the authoritative ones at the serialization point of this insert, or
	// the row would record a decision about a configuration that had already
	// been replaced. A changed revision is ErrStoreConflict.
	//
	// The atomicity is the contract; how each backend obtains it is not.
	// PostgreSQL takes FOR UPDATE row locks on the two environments in
	// (project_id, ref) byte order, which buys independent progress for
	// unrelated promotions. SQLite enters an explicit write transaction and
	// serializes writers database-wide, which is accepted for the local
	// backend. Neither is required to run unrelated promotions in parallel.
	CreatePromotion(ctx context.Context, promotion Promotion) error

	// Promotion loads one recorded decision by identifier. Identity is
	// global, like a project, agent, candidate or run identifier and unlike
	// an environment ref.
	Promotion(ctx context.Context, id PromotionID) (Promotion, error)

	// ProjectPromotions returns at most limit of one project's promotions, in
	// identifier byte order, whose ID sorts after `after`. An empty `after`
	// starts at the beginning.
	//
	// limit is 1 to MaxPromotionPage inclusive; anything outside that is
	// ErrInvalidID. Traversal is by identifier rather than by decision time
	// because timestamps are stored as RFC3339Nano text, which is not
	// lexically ordered — a cursor over that column would silently skip and
	// repeat rows. A project with no promotions returns an empty slice; one
	// that does not exist returns ErrStoreNotFound.
	ProjectPromotions(
		ctx context.Context, projectID ProjectID, after PromotionID, limit int,
	) ([]Promotion, error)
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

	// CandidateEvaluationRuns returns at most limit of one candidate's runs
	// whose id sorts after `after`, in id byte order.
	//
	// On this interface rather than ControlStore, and that placement is the
	// point rather than an accident of where browsing is convenient. Task 057
	// split the two capabilities because "a later backend may reasonably
	// implement one and not the other"; putting the run collection on
	// ControlStore would oblige a backend implementing only the control
	// capability to serve evaluation runs it does not store. A list method
	// does not move an entity between capabilities.
	//
	// limit outside 1..MaxListPage is ErrInvalidID; a missing candidate is
	// ErrStoreNotFound; a candidate with no runs is an empty slice. A run is
	// restored by replaying its lifecycle transitions, exactly as a by-id read
	// does, so a listed run is never less validated than a singly-read one.
	CandidateEvaluationRuns(
		ctx context.Context, candidateID CandidateID, after EvaluationRunID, limit int,
	) ([]EvaluationRun, error)

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
