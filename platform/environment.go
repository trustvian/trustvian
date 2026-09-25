package platform

// The environment registry: what an EvaluationRun's EnvironmentRef resolves
// to, and the ordering a promotion workflow will ask about.
//
//	Project
//	  ├─ Agent ──▶ Candidate ──▶ EvaluationRun ──▶ EnvironmentRef
//	  └─ Environment ◀───────────────────────────────┘
//
// The arrow that already existed does not change. A run still records an
// EnvironmentRef and nothing else; this gives that reference something to
// resolve against inside the same project. See
// docs/tasks/v1.0/065-environment-model.md and
// docs/adr/0039-environments-are-project-owned-ranked-references.md.

import (
	"errors"
	"fmt"
)

// maxEnvironmentRank bounds a rank.
//
// Small on purpose: a rank is a stage an operator chose, not a counter, and
// four digits is more room than a promotion order anybody reasons about will
// ever need. Bounding it keeps the column an ordinary integer on both
// backends and keeps the value human-sized.
const maxEnvironmentRank = 9999

// maxProjectEnvironments caps how many environments may be *created* in one
// project.
//
// A bound on growth, not a claim about what a project already contains: a
// database migrated from schema 2 may hold more, because schema 2 had no
// registry and no cap, and discarding historical environments to satisfy a
// new limit would orphan the evidence that names them. See
// migrateEnvironmentsFromRuns.
const maxProjectEnvironments = 64

// MaxEnvironmentPage bounds one page of ProjectEnvironments.
//
// The response is bounded separately from the entity for exactly the reason
// above: the creation cap says nothing about a migrated project, so the
// transport cannot rely on it. Exported because the HTTP layer must reject
// the same number this one enforces, and two copies of a limit is how two
// layers come to disagree about it.
//
// Defined as MaxListPage since task 074, which gave every collection in this
// API one shared bound. The name stays because it is published compatibility
// surface; the value has one definition.
const MaxEnvironmentPage = MaxListPage

var (
	// ErrEnvironmentLimit reports a create that would take a project past
	// maxProjectEnvironments.
	//
	// Only ever returned for a ref the project does not already have: an
	// identity that exists is reported as ErrStoreAlreadyExists whatever the
	// count, because that request adds nothing and consumes no room.
	ErrEnvironmentLimit = errors.New("platform: project has reached its environment limit")

	// ErrEnvironmentUnavailable reports an environment that exists but is not
	// open to new work — today, an archived one.
	//
	// Distinct from ErrStoreNotFound because the two are different operator
	// problems: one is a typo or a missing setup step, the other is a
	// deliberate configuration choice somebody made.
	ErrEnvironmentUnavailable = errors.New("platform: environment is not available")
)

// EnvironmentStatus is whether an environment is open to new work.
//
// Two states and no state machine: Archive and Activate are total and
// mutually inverse, because archiving destroys nothing and there is no
// invariant a transition could violate. RunStatus has a transition table
// because a completed run is historical evidence; an environment is
// configuration.
type EnvironmentStatus string

const (
	EnvironmentActive   EnvironmentStatus = "active"
	EnvironmentArchived EnvironmentStatus = "archived"
)

func (s EnvironmentStatus) valid() bool {
	switch s {
	case EnvironmentActive, EnvironmentArchived:
		return true
	default:
		return false
	}
}

// Environment is one deployment stage a project owns, and the thing a run's
// EnvironmentRef names.
//
// Identity is (ProjectID, EnvironmentRef). There is deliberately no
// EnvironmentID: the ref is already what every run persists, what
// EvaluationAggregate.AddRecord compares a DecisionRecord's environment
// against byte for byte, and what every snapshot, diff, scorecard and gate
// result is bounded by. A second identifier would need a translation on the
// per-record ingest path, and a translation is something that can disagree.
//
// It carries no URL, endpoint, credential, secret, label map or policy
// reference. Nothing in Trustvian would connect to an address stored here,
// and a field nothing reads is a field every backend must still agree about.
//
// Its state is unexported and reachable only through the accessors below;
// transitions return a new value rather than mutating the receiver, which is
// what makes "ref is immutable" a property rather than a convention.
type Environment struct {
	ref       EnvironmentRef
	projectID ProjectID
	name      string

	// rank is a position in the project's promotion order, and ranked says
	// whether it has one. Two fields rather than a sentinel because rank 0 is
	// a legitimate rank and "no position" is a different statement from
	// "position zero".
	rank   uint16
	ranked bool

	status EnvironmentStatus

	// revision changes with every mutation and is what makes a configure,
	// archive or activate a compare-and-swap rather than a blind write. See
	// ControlStore.UpdateEnvironment.
	revision uint64
}

// NewEnvironment validates and returns an active, unranked environment at
// revision 1.
//
// Unranked on purpose: an ordering nobody chose is worse than no ordering,
// so a rank is applied deliberately by WithRank rather than defaulted here.
//
// The owning project is recorded, not verified — this constructor cannot see
// other entities, and checking existence needs both loaded at once. The store
// enforces it with a foreign key, exactly as it does for an agent.
func NewEnvironment(ref EnvironmentRef, projectID ProjectID, name string) (Environment, error) {
	if err := validateID("environment ref", string(ref)); err != nil {
		return Environment{}, err
	}
	if err := validateID("environment project id", string(projectID)); err != nil {
		return Environment{}, err
	}
	if err := validateName("environment name", name); err != nil {
		return Environment{}, err
	}
	return Environment{
		ref:       ref,
		projectID: projectID,
		name:      name,
		status:    EnvironmentActive,
		revision:  1,
	}, nil
}

// NewRankedEnvironment is NewEnvironment for a caller that already knows the
// environment's position.
//
// Exists because a create is one act: an environment created with a rank is
// still revision 1, and building it by calling WithRank on a new value would
// hand a create a revision of 2 and make the transport compensate. Ranking an
// existing environment stays WithRank's job.
func NewRankedEnvironment(
	ref EnvironmentRef, projectID ProjectID, name string, rank uint16,
) (Environment, error) {
	env, err := NewEnvironment(ref, projectID, name)
	if err != nil {
		return Environment{}, err
	}
	ranked, err := env.WithRank(rank)
	if err != nil {
		return Environment{}, err
	}
	ranked.revision = env.revision
	return ranked, nil
}

// Ref returns the environment's identifier within its project — the same
// value an EvaluationRun records.
func (e Environment) Ref() EnvironmentRef { return e.ref }

// ProjectID returns the project this environment belongs to.
func (e Environment) ProjectID() ProjectID { return e.projectID }

// Name returns the human-facing name.
func (e Environment) Name() string { return e.name }

// Rank returns the promotion rank and whether one is set. A false second
// result means the environment has no position in the project's order, which
// is not an error: hosting runs and participating in promotion are separate
// capabilities.
func (e Environment) Rank() (uint16, bool) { return e.rank, e.ranked }

// Status returns whether the environment is open to new work.
func (e Environment) Status() EnvironmentStatus { return e.status }

// Revision returns the value a mutation must present to be accepted.
func (e Environment) Revision() uint64 { return e.revision }

// Rename changes the display name.
func (e Environment) Rename(name string) (Environment, error) {
	if err := validateName("environment name", name); err != nil {
		return e, err
	}
	// e is a value receiver: these assignments mutate this function's own
	// copy, and the caller's environment is untouched.
	e.name = name
	e.revision++
	return e, nil
}

// WithRank gives the environment a position in its project's promotion
// order.
func (e Environment) WithRank(rank uint16) (Environment, error) {
	if rank > maxEnvironmentRank {
		return e, fmt.Errorf("%w: environment rank %d is above the maximum of %d",
			ErrInvalidID, rank, maxEnvironmentRank)
	}
	e.rank = rank
	e.ranked = true
	e.revision++
	return e, nil
}

// WithoutRank removes the environment's position.
//
// Returns a new value even when the environment was already unranked: a
// mutation that produced an identical revision would let a compare-and-swap
// succeed without anything having changed.
func (e Environment) WithoutRank() Environment {
	e.rank = 0
	e.ranked = false
	e.revision++
	return e
}

// Archive closes the environment to new work.
//
// Existing runs are untouched: an archived environment refuses new runs,
// keeps every historical reference resolvable, and remains readable,
// configurable and re-activatable. There is deliberately no delete anywhere
// in this package — a hard delete would leave a completed run's
// EnvironmentRef resolving to nothing.
func (e Environment) Archive() (Environment, error) {
	e.status = EnvironmentArchived
	e.revision++
	return e, nil
}

// Activate reopens an archived environment.
func (e Environment) Activate() (Environment, error) {
	e.status = EnvironmentActive
	e.revision++
	return e, nil
}

// atRevision returns e with an explicit revision.
//
// One store mutation advances the stored revision by exactly one, and a
// configure may apply two domain transitions — a rename and a re-rank — to
// express it. Each transition increments on its own, which is the rule the
// domain keeps; composing them into a single write is the service's job, and
// this is where the composed value's revision is stated rather than counted.
//
// Unexported: a revision is the store's compare-and-swap token, not something
// a caller outside this package may choose.
func (e Environment) atRevision(revision uint64) Environment {
	e.revision = revision
	return e
}

// CanPromote reports whether a candidate evaluated in from may be considered
// for promotion toward to.
//
// An ordering question and nothing else. A true answer is a *precondition* of
// promotion, never an authorization of one: whether a candidate may actually
// move, on what evidence, and with whose approval is task 066's, and a gate
// PASS is not a promotion either.
//
// True iff both environments belong to one project, both are active, both are
// ranked, and to's rank is strictly greater. Every other combination is
// false, including:
//
//   - the same environment, and any two environments sharing a rank — equal
//     ranks mean peers, and a peer is not a next stage;
//   - a backward move, which is a rollback and has its own evidence rules;
//   - an unranked environment on either side, which has no position at all;
//   - an archived environment on either side;
//   - two environments in different projects.
//
// A forward jump over an intermediate rank is true: forward is forward, and
// whether a workflow *permits* the jump is a promotion policy computed from
// these same ranks rather than a second ordering model.
//
// This is the only implementation of promotion precedence in the repository.
// No transport re-derives it from ranks.
func CanPromote(from, to Environment) bool {
	if from.projectID != to.projectID {
		return false
	}
	if from.status != EnvironmentActive || to.status != EnvironmentActive {
		return false
	}
	if !from.ranked || !to.ranked {
		return false
	}
	return to.rank > from.rank
}

// restoreEnvironment rebuilds a stored environment, applying every check a
// live value faced.
//
// Shared by both backends so neither can accept a row the other would refuse.
// An unrecognized status is corruption rather than a value coerced to active:
// a row saying "disabled" means something wrote it that this code did not,
// and guessing which of two states it meant is how an archived environment
// silently reopens.
func restoreEnvironment(
	ref EnvironmentRef, projectID ProjectID, name string,
	rank uint16, ranked bool, status EnvironmentStatus, revision uint64,
) (Environment, error) {
	env, err := NewEnvironment(ref, projectID, name)
	if err != nil {
		return Environment{}, err
	}
	if !status.valid() {
		return Environment{}, fmt.Errorf("environment status %q is not recognized", status)
	}
	if ranked && rank > maxEnvironmentRank {
		return Environment{}, fmt.Errorf("environment rank %d is above the maximum of %d",
			rank, maxEnvironmentRank)
	}
	if revision == 0 {
		return Environment{}, errors.New("environment revision starts at 1")
	}
	env.rank = rank
	env.ranked = ranked
	env.status = status
	env.revision = revision
	return env, nil
}

// validateEnvironmentIdentity checks a (project, ref) pair a caller supplied
// as two loose strings.
//
// The service calls this before it touches a store, so a malformed ref is a
// caller error rather than a lookup that happens to miss. Without it, an
// over-length or control-character-bearing ref from a URL path would reach
// the database and come back as "not found", which tells the caller the wrong
// thing about what they got wrong.
func validateEnvironmentIdentity(projectID ProjectID, ref EnvironmentRef) error {
	if err := validateID("environment project id", string(projectID)); err != nil {
		return err
	}
	return validateID("environment ref", string(ref))
}

// validateEnvironmentPage bounds a list request before it reaches a query.
func validateEnvironmentPage(after EnvironmentRef, limit int) error {
	if limit < 1 || limit > MaxEnvironmentPage {
		return fmt.Errorf("%w: environment page limit %d is outside 1..%d",
			ErrInvalidID, limit, MaxEnvironmentPage)
	}
	// An empty cursor starts at the beginning; a non-empty one faces the same
	// rules as any other ref, because it is one.
	if after != "" {
		if err := validateID("environment page cursor", string(after)); err != nil {
			return err
		}
	}
	return nil
}
