package platform

// The control plane: the authoritative platform service layer.
//
// Every rule about what an evaluation is lives here — which lifecycle
// transitions are legal, when evidence may be ingested, what a comparison
// requires. Transports call these methods and translate; they decide nothing.
//
// The alternative works exactly until the second caller arrives. A CLI that
// reimplemented "ingest only while running" would drift from the HTTP
// version, and the drift would stay invisible until the two disagreed about a
// real evaluation.
//
// See docs/adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md.

import (
	"context"
	"errors"
	"fmt"
	"time"

	trustvian "github.com/trustvian/trustvian"
)

// ErrEvaluationState reports an operation that does not fit a run's current
// lifecycle state — ingesting into a pending run, comparing an unfinished
// one.
//
// Not a storage conflict and not corruption: the caller asked for something
// coherent at the wrong moment.
var ErrEvaluationState = errors.New("platform: evaluation is not in the required state")

// ErrComparisonScope reports a comparison between two runs that do not belong
// to the same project.
//
// Task 065 made EnvironmentRef explicitly project-scoped, which turned an
// existing check into a hazard: CompareBehaviorSnapshots requires the two runs
// to share an environment ref, and two projects may each own a "staging". Two
// runs from different projects would pass that check and produce a scorecard
// over unrelated populations, reporting the environment as "staging" and being
// right and misleading at once.
//
// Not a state conflict and not a storage condition: the pairing itself is
// something that can never succeed, so a caller must change the request rather
// than retry it.
var ErrComparisonScope = errors.New("platform: runs belong to different projects")

// ControlPlane serves platform operations over narrow store capabilities.
//
// The dependencies are interfaces, never *SQLiteStore, so task 064 can supply
// PostgreSQL without any of this changing. SQLite happens to implement all
// three; the service must not be able to tell.
type ControlPlane struct {
	control     ControlStore
	evaluations EvaluationStore
	ingest      EvaluationIngestStore

	// realtime is optional infrastructure. Nil means the plane behaves exactly
	// as it did before task 059 — which is why realtime is an option rather
	// than a constructor parameter: existing callers should not have to build
	// a bus in order to ignore it.
	realtime RealtimePublisher
}

// ControlPlaneOption configures a control plane at construction.
type ControlPlaneOption func(*ControlPlane)

// WithRealtimePublisher attaches a realtime publisher.
//
// A publisher, never a full bus: the service must not be able to subscribe,
// and a transport must not be able to publish. There is deliberately no
// post-construction setter — a publisher that could be swapped while requests
// are in flight would make publication ordering impossible to reason about.
func WithRealtimePublisher(publisher RealtimePublisher) ControlPlaneOption {
	return func(c *ControlPlane) {
		if publisher != nil {
			c.realtime = publisher
		}
	}
}

// NewControlPlane wires the service to its capabilities.
func NewControlPlane(
	control ControlStore,
	evaluations EvaluationStore,
	ingest EvaluationIngestStore,
	options ...ControlPlaneOption,
) (*ControlPlane, error) {
	if control == nil || evaluations == nil || ingest == nil {
		return nil, errors.New("platform: control plane requires control, evaluation and ingest stores")
	}
	plane := &ControlPlane{control: control, evaluations: evaluations, ingest: ingest}
	for _, option := range options {
		option(plane)
	}
	return plane, nil
}

// ---------------------------------------------------------------------
// Realtime publication
// ---------------------------------------------------------------------

// publishLifecycle notifies subscribers about a committed run transition.
//
// Called only after the durable mutation succeeded, and its outcome is
// deliberately ignored: the write already landed, and reporting a delivery
// problem as an operation failure would make a client retry a committed
// write. Realtime is advisory.
func (c *ControlPlane) publishLifecycle(
	ctx context.Context, kind RealtimeEventKind, run EvaluationRun,
) {
	if c.realtime == nil {
		return
	}
	scope, ok := c.resolveScope(ctx, run)
	if !ok {
		return
	}
	c.realtime.Publish(RealtimeEvent{
		Kind:  kind,
		Scope: scope,
		Evaluation: RealtimeEvaluation{
			Status:        run.Status(),
			CreatedAt:     run.CreatedAt(),
			StartedAt:     run.StartedAt(),
			FinishedAt:    run.FinishedAt(),
			FailureReason: run.FailureReason(),
		},
	})
}

// resolveScope builds the hierarchy an event belongs to.
//
// Two reads, so subscribers need none: every event carries project, agent,
// candidate and run, and a filter can match without touching the database.
// Sound because the hierarchy is immutable — task 052 offers no ChangeAgent.
//
// A failure here happens *after* the authoritative mutation committed, so it
// degrades to skipping the notification rather than failing the operation.
// Authority lives in the database, and a missing notification is recoverable
// by resync; a committed write reported as failed is not.
// requireUsableEnvironment refuses a run whose environment is missing,
// archived, or owned by another project.
//
// Fails before anything is written and therefore before anything is
// published. A ref that exists in a *different* project is ErrStoreNotFound
// here, which is exactly right: it does not exist in this run's project, and
// identity is the pair.
func (c *ControlPlane) requireUsableEnvironment(ctx context.Context, run EvaluationRun) error {
	projectID, err := c.projectOf(ctx, run)
	if err != nil {
		return err
	}
	environment, err := c.control.Environment(ctx, projectID, run.Environment())
	if err != nil {
		return err
	}
	if environment.Status() != EnvironmentActive {
		return fmt.Errorf("%w: environment %s in project %s is %s and accepts no new runs",
			ErrEnvironmentUnavailable, preview(string(environment.Ref())),
			preview(string(projectID)), environment.Status())
	}
	return nil
}

// projectOf walks a run to the project that owns it.
//
// The same two reads resolveScope performs, kept separate because this one's
// failure is the operation's failure rather than a skipped notification.
func (c *ControlPlane) projectOf(ctx context.Context, run EvaluationRun) (ProjectID, error) {
	candidate, err := c.control.Candidate(ctx, run.CandidateID())
	if err != nil {
		return "", err
	}
	agent, err := c.control.Agent(ctx, candidate.AgentID())
	if err != nil {
		return "", err
	}
	return agent.ProjectID(), nil
}

func (c *ControlPlane) resolveScope(
	ctx context.Context, run EvaluationRun,
) (RealtimeScope, bool) {
	candidate, err := c.control.Candidate(ctx, run.CandidateID())
	if err != nil {
		return RealtimeScope{}, false
	}
	agent, err := c.control.Agent(ctx, candidate.AgentID())
	if err != nil {
		return RealtimeScope{}, false
	}
	return RealtimeScope{
		ProjectID:         agent.ProjectID(),
		AgentID:           agent.ID(),
		CandidateID:       candidate.ID(),
		RunID:             run.ID(),
		Environment:       run.Environment(),
		BehavioralProfile: run.BehavioralProfile(),
	}, true
}

// ---------------------------------------------------------------------
// Control entities
// ---------------------------------------------------------------------

// Identity is caller-owned throughout: nothing here generates an ID, and no
// UUID dependency exists. ADR 0025 is binding.

func (c *ControlPlane) CreateProject(ctx context.Context, project Project) error {
	return c.control.CreateProject(ctx, project)
}

func (c *ControlPlane) Project(ctx context.Context, id ProjectID) (Project, error) {
	return c.control.Project(ctx, id)
}

func (c *ControlPlane) CreateAgent(ctx context.Context, agent Agent) error {
	return c.control.CreateAgent(ctx, agent)
}

func (c *ControlPlane) Agent(ctx context.Context, id AgentID) (Agent, error) {
	return c.control.Agent(ctx, id)
}

func (c *ControlPlane) CreateCandidate(ctx context.Context, candidate Candidate) error {
	return c.control.CreateCandidate(ctx, candidate)
}

func (c *ControlPlane) Candidate(ctx context.Context, id CandidateID) (Candidate, error) {
	return c.control.Candidate(ctx, id)
}

// ---------------------------------------------------------------------
// Hierarchy browsing (task 074)
// ---------------------------------------------------------------------

// The four collections that make the hierarchy discoverable without knowing an
// identifier, composed from both capabilities into one surface.
//
// Three come from ControlStore and one from EvaluationStore, and the split is
// preserved rather than collapsed for browsing convenience: task 057 separated
// the two because a later backend may implement one and not the other, and a
// list method does not move an entity between capabilities. This is the layer
// whose job it is to hold both — the HTTP adapter above never learns that the
// run collection came from somewhere else, and must not need to.
//
// No business logic lives here. Each method validates the caller-owned
// identifier it was given and forwards; the page bound, the ordering, the
// cursor rules and the parent-missing-versus-empty distinction are all the
// store's contract, enforced once at the store edge rather than restated here
// where the two could disagree.

// Projects returns one bounded page of projects, in id byte order.
//
// The root of the hierarchy and the only unscoped collection: there is no
// parent to validate, and an empty platform is an empty page.
func (c *ControlPlane) Projects(
	ctx context.Context, after ProjectID, limit int,
) ([]Project, error) {
	return c.control.Projects(ctx, after, limit)
}

// ProjectAgents returns one bounded page of a project's agents.
func (c *ControlPlane) ProjectAgents(
	ctx context.Context, projectID ProjectID, after AgentID, limit int,
) ([]Agent, error) {
	if err := validateID("agent project id", string(projectID)); err != nil {
		return nil, err
	}
	return c.control.ProjectAgents(ctx, projectID, after, limit)
}

// AgentCandidates returns one bounded page of an agent's candidates.
func (c *ControlPlane) AgentCandidates(
	ctx context.Context, agentID AgentID, after CandidateID, limit int,
) ([]Candidate, error) {
	if err := validateID("candidate agent id", string(agentID)); err != nil {
		return nil, err
	}
	return c.control.AgentCandidates(ctx, agentID, after, limit)
}

// CandidateEvaluationRuns returns one bounded page of a candidate's runs.
//
// The one collection served by EvaluationStore. Composing it here is what lets
// a caller browse project → agent → candidate → run through a single service
// without knowing that the last step crosses a capability boundary.
func (c *ControlPlane) CandidateEvaluationRuns(
	ctx context.Context, candidateID CandidateID, after EvaluationRunID, limit int,
) ([]EvaluationRun, error) {
	if err := validateID("evaluation run candidate id", string(candidateID)); err != nil {
		return nil, err
	}
	return c.evaluations.CandidateEvaluationRuns(ctx, candidateID, after, limit)
}

// ---------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------

// CreateEnvironment records one environment in a project.
//
// The store owns the ordered checks and the per-project cap, because the cap
// is a cross-row invariant that only a transaction can hold. Nothing is
// published: realtime is scoped to evaluation lifecycle and ingest (ADR 0032),
// and an environment rename is not something a live dashboard is watching.
func (c *ControlPlane) CreateEnvironment(ctx context.Context, env Environment) error {
	return c.control.CreateEnvironment(ctx, env)
}

// Environment loads one environment by (project, ref).
func (c *ControlPlane) Environment(
	ctx context.Context, projectID ProjectID, ref EnvironmentRef,
) (Environment, error) {
	// Validated here rather than left to the lookup: a malformed ref that
	// reached the store would come back as "not found", which tells a caller
	// the wrong thing about what they got wrong.
	if err := validateEnvironmentIdentity(projectID, ref); err != nil {
		return Environment{}, err
	}
	return c.control.Environment(ctx, projectID, ref)
}

// ProjectEnvironments returns one bounded page of a project's environments,
// in ref byte order, starting after the given ref.
func (c *ControlPlane) ProjectEnvironments(
	ctx context.Context, projectID ProjectID, after EnvironmentRef, limit int,
) ([]Environment, error) {
	if err := validateID("environment project id", string(projectID)); err != nil {
		return nil, err
	}
	return c.control.ProjectEnvironments(ctx, projectID, after, limit)
}

// ConfigureEnvironmentRequest is one configuration change.
//
// A fixed shape rather than a patch map, and pointers rather than sentinel
// values, because "leave it alone" and "set it to zero" are different
// intentions and rank 0 is a legitimate rank. ClearRank is explicit for the
// same reason.
type ConfigureEnvironmentRequest struct {
	// Revision is the revision the caller read. Required: there is no
	// unconditional write, because two operators reordering ranks would
	// otherwise overwrite each other and the loser would never know.
	Revision uint64

	Name      *string
	Rank      *uint16
	ClearRank bool
}

// ConfigureEnvironment applies a name and/or rank change under
// compare-and-swap.
//
// Load, apply the domain transitions, store with the CAS. Not a transaction:
// the compare-and-swap *is* the serialization point, so a losing writer gets
// ErrStoreConflict rather than a lost update.
func (c *ControlPlane) ConfigureEnvironment(
	ctx context.Context, projectID ProjectID, ref EnvironmentRef,
	request ConfigureEnvironmentRequest,
) (Environment, error) {
	if request.Rank != nil && request.ClearRank {
		return Environment{}, fmt.Errorf(
			"%w: a configure may set a rank or clear it, not both", ErrInvalidID)
	}
	if request.Name == nil && request.Rank == nil && !request.ClearRank {
		return Environment{}, fmt.Errorf(
			"%w: a configure must change the name, the rank, or both", ErrInvalidID)
	}

	return c.mutateEnvironment(ctx, projectID, ref, request.Revision,
		func(current Environment) (Environment, error) {
			next := current
			var err error
			if request.Name != nil {
				if next, err = next.Rename(*request.Name); err != nil {
					return Environment{}, err
				}
			}
			switch {
			case request.Rank != nil:
				if next, err = next.WithRank(*request.Rank); err != nil {
					return Environment{}, err
				}
			case request.ClearRank:
				next = next.WithoutRank()
			}
			return next, nil
		})
}

// ArchiveEnvironment closes an environment to new work.
//
// Existing runs are untouched: archiving refuses the *next* run against this
// environment and invalidates nothing already underway or already finished.
func (c *ControlPlane) ArchiveEnvironment(
	ctx context.Context, projectID ProjectID, ref EnvironmentRef, revision uint64,
) (Environment, error) {
	return c.mutateEnvironment(ctx, projectID, ref, revision,
		func(current Environment) (Environment, error) { return current.Archive() })
}

// ActivateEnvironment reopens an archived environment.
func (c *ControlPlane) ActivateEnvironment(
	ctx context.Context, projectID ProjectID, ref EnvironmentRef, revision uint64,
) (Environment, error) {
	return c.mutateEnvironment(ctx, projectID, ref, revision,
		func(current Environment) (Environment, error) { return current.Activate() })
}

// mutateEnvironment is the load-apply-CAS every environment mutation shares.
//
// The revision is checked here as well as by the store: a caller presenting a
// stale one learns that immediately, without a write attempt, and the store's
// own predicate still decides the race between two callers who both read the
// same revision.
func (c *ControlPlane) mutateEnvironment(
	ctx context.Context, projectID ProjectID, ref EnvironmentRef, revision uint64,
	apply func(Environment) (Environment, error),
) (Environment, error) {
	current, err := c.Environment(ctx, projectID, ref)
	if err != nil {
		return Environment{}, err
	}
	if current.Revision() != revision {
		return Environment{}, fmt.Errorf(
			"%w: environment %s is at revision %d, not %d",
			ErrStoreConflict, preview(string(ref)), current.Revision(), revision)
	}
	next, err := apply(current)
	if err != nil {
		return Environment{}, err
	}
	// One mutation, one revision. A configure changing both the name and the
	// rank applies two domain transitions, and each of those increments on its
	// own; what the store compares against is the single write they add up to.
	next = next.atRevision(current.Revision() + 1)

	if err := c.control.UpdateEnvironment(ctx, current, next); err != nil {
		return Environment{}, err
	}
	return next, nil
}

// PromotionOrder reports whether one environment is forward of another in its
// project's order.
//
// A loading wrapper over CanPromote for transports, which hold refs rather
// than values. It answers an ordering question and authorizes nothing: what a
// promotion requires, and whether one may happen, is task 066's.
func (c *ControlPlane) PromotionOrder(
	ctx context.Context, projectID ProjectID, from, to EnvironmentRef,
) (bool, error) {
	source, err := c.Environment(ctx, projectID, from)
	if err != nil {
		return false, err
	}
	target, err := c.Environment(ctx, projectID, to)
	if err != nil {
		return false, err
	}
	return CanPromote(source, target), nil
}

// ---------------------------------------------------------------------
// Promotions
// ---------------------------------------------------------------------

// PromotionRequest is the fixed-shape input to Promote.
//
// Everything the server can derive is absent by construction: no project,
// agent or candidate identity, no source environment, no ranks, no verdict,
// no timestamp. A caller supplies an identifier, two completed runs, a target,
// and the limits the gate should apply.
type PromotionRequest struct {
	ID PromotionID

	ReferenceRunID EvaluationRunID
	CandidateRunID EvaluationRunID

	TargetEnvironment EnvironmentRef

	GateLimits EvaluationGateLimits
}

// Promote records one promotion decision.
//
// The ordering below is the contract, and it puts every structural
// precondition before any evidence is read: an impossible promotion costs a
// handful of primary-key lookups rather than the evidence reads and three
// derivations a comparison costs.
//
//	 1  validate the request shape                       no reads
//	 2  load candidate run, require completed            1 read
//	 3  load reference run, require completed            1 read
//	 4  resolve both runs to candidates and agents       4 reads
//	 5  require same project                             ErrComparisonScope
//	 6  require same agent                               ErrPromotionScope
//	 7  require same environment ref → the source        ErrPromotionScope
//	 8  load source and target environments              2 reads
//	 9  require CanPromote(source, target)               ErrPromotionOrder
//	10  CompareEvaluations — diff, scorecard, gate       evidence reads
//	11  NewPromotion, which derives the outcome          no reads
//	12  CreatePromotion, which revalidates and inserts   1 transaction
//
// Steps 9 and 12 both check ordering and they are different checks. Step 9
// asks whether the request is structurally possible, on values just read, and
// its failure is the caller's — ErrPromotionOrder. Step 12 asks whether the
// configuration the decision was built on is still authoritative, atomically
// with the insert, and its failure is a race — ErrStoreConflict. Keeping them
// separate is what makes the race observable without pretending the caller
// sent something invalid.
//
// Step 10 reuses CompareEvaluations whole, including its own repeat of
// completedRun and requireSameProject. That redundancy is accepted
// deliberately: an internal variant that skipped the checks would create a
// path where they *can* be skipped, and a future edit routing another caller
// through it would lose them silently.
//
// A structural failure writes nothing. A gate FAIL writes a rejected record,
// because a verdict was reached.
func (c *ControlPlane) Promote(
	ctx context.Context, request PromotionRequest, at time.Time,
) (Promotion, error) {
	for _, field := range []struct{ name, value string }{
		{"promotion id", string(request.ID)},
		{"reference run id", string(request.ReferenceRunID)},
		{"candidate run id", string(request.CandidateRunID)},
		{"target environment", string(request.TargetEnvironment)},
	} {
		if err := validateID(field.name, field.value); err != nil {
			return Promotion{}, err
		}
	}
	if request.ReferenceRunID == request.CandidateRunID {
		return Promotion{}, fmt.Errorf(
			"%w: reference and candidate run are both %s; a promotion compares two runs",
			ErrInvalidID, preview(string(request.CandidateRunID)))
	}

	candidateRun, err := c.completedRun(ctx, "candidate", request.CandidateRunID)
	if err != nil {
		return Promotion{}, err
	}
	referenceRun, err := c.completedRun(ctx, "reference", request.ReferenceRunID)
	if err != nil {
		return Promotion{}, err
	}

	source, target, err := c.promotionEnvironments(
		ctx, referenceRun, candidateRun, request.TargetEnvironment)
	if err != nil {
		return Promotion{}, err
	}

	comparison, err := c.CompareEvaluations(
		ctx, request.ReferenceRunID, request.CandidateRunID, request.GateLimits)
	if err != nil {
		return Promotion{}, err
	}

	promotion, err := NewPromotion(PromotionDecision{
		ID:         request.ID,
		Source:     source,
		Target:     target,
		GateResult: comparison.Gate,
		DecidedAt:  at,
	})
	if err != nil {
		return Promotion{}, err
	}

	if err := c.control.CreatePromotion(ctx, promotion); err != nil {
		return Promotion{}, err
	}
	return promotion, nil
}

// promotionEnvironments resolves the source and target a promotion runs
// between, and refuses every pairing that is not promotion-eligible.
//
// The source is **inferred**, never supplied. Both runs already carry an
// EnvironmentRef, CompareBehaviorSnapshots already refuses two snapshots whose
// environments differ, and the project is already derived from the runs — so
// (project, shared ref) is unambiguously bound into the evidence, and asking
// the caller to restate it would add an input whose only possible effect is to
// disagree with it.
//
// The same-agent rule lives here rather than in CompareEvaluations. A
// comparison between two agents of one project is unusual but coherent; a
// promotion asserts that one candidate's evidence justifies that candidate
// advancing, and evidence drawn from a different agent justifies nothing about
// this one.
func (c *ControlPlane) promotionEnvironments(
	ctx context.Context, reference, candidate EvaluationRun, targetRef EnvironmentRef,
) (Environment, Environment, error) {
	none := func(err error) (Environment, Environment, error) {
		return Environment{}, Environment{}, err
	}

	if err := c.requireSameProject(ctx, reference, candidate); err != nil {
		return none(err)
	}

	referenceAgent, err := c.agentOf(ctx, reference)
	if err != nil {
		return none(err)
	}
	candidateAgent, err := c.agentOf(ctx, candidate)
	if err != nil {
		return none(err)
	}
	if referenceAgent != candidateAgent {
		return none(fmt.Errorf(
			"%w: reference run belongs to agent %s, candidate run to agent %s; "+
				"one agent's evidence does not justify another agent's promotion",
			ErrPromotionScope, preview(string(referenceAgent)), preview(string(candidateAgent))))
	}

	if reference.Environment() != candidate.Environment() {
		return none(fmt.Errorf(
			"%w: reference run is in environment %s, candidate run in %s; "+
				"a promotion advances out of one environment",
			ErrPromotionScope, preview(string(reference.Environment())),
			preview(string(candidate.Environment()))))
	}

	projectID, err := c.projectOf(ctx, candidate)
	if err != nil {
		return none(err)
	}

	source, err := c.control.Environment(ctx, projectID, candidate.Environment())
	if err != nil {
		return none(err)
	}
	target, err := c.control.Environment(ctx, projectID, targetRef)
	if err != nil {
		return none(err)
	}

	if !CanPromote(source, target) {
		return none(fmt.Errorf(
			"%w: %s cannot promote toward %s in project %s",
			ErrPromotionOrder, preview(string(source.Ref())),
			preview(string(target.Ref())), preview(string(projectID))))
	}
	return source, target, nil
}

// agentOf walks a run to the agent that owns it.
func (c *ControlPlane) agentOf(ctx context.Context, run EvaluationRun) (AgentID, error) {
	candidate, err := c.control.Candidate(ctx, run.CandidateID())
	if err != nil {
		return "", err
	}
	return candidate.AgentID(), nil
}

// Promotion loads one recorded decision.
func (c *ControlPlane) Promotion(ctx context.Context, id PromotionID) (Promotion, error) {
	if err := validateID("promotion id", string(id)); err != nil {
		return Promotion{}, err
	}
	return c.control.Promotion(ctx, id)
}

// ProjectPromotions returns one bounded page of a project's promotion history.
func (c *ControlPlane) ProjectPromotions(
	ctx context.Context, projectID ProjectID, after PromotionID, limit int,
) ([]Promotion, error) {
	if err := validateID("project id", string(projectID)); err != nil {
		return nil, err
	}
	return c.control.ProjectPromotions(ctx, projectID, after, limit)
}

// ---------------------------------------------------------------------
// Evaluation run lifecycle
// ---------------------------------------------------------------------

// CreateEvaluationRun records a run after verifying the environment it names.
//
// The environment must exist, be active, and belong to the run's own project
// — resolved through the hierarchy the run already implies:
//
//	run.CandidateID → Candidate.AgentID → Agent.ProjectID → Environment
//
// Before task 065 a run could name any syntactically valid string, so
// "stagin" produced a perfectly valid run against an environment that did not
// exist, and every comparison bounded by that ref silently described a
// population of one.
//
// The check lives here rather than in NewEvaluationRun, which cannot see
// other entities, and rather than in a foreign key, which would need
// project_id denormalized onto runs and would make a historical run
// unloadable if its environment ever became unreachable.
//
// It is a create-time check and nothing else. A run already created keeps
// running, ingesting and completing after its environment is archived:
// environment configuration governs what may start, never what is underway.
// Nothing on the per-record ingest path resolves an environment.
func (c *ControlPlane) CreateEvaluationRun(ctx context.Context, run EvaluationRun) error {
	if err := c.requireUsableEnvironment(ctx, run); err != nil {
		return err
	}
	if err := c.evaluations.CreateEvaluationRun(ctx, run); err != nil {
		return err
	}
	// After the commit, never before: publishing first would let a subscriber
	// observe a run whose creation then failed.
	c.publishLifecycle(ctx, RealtimeEvaluationCreated, run)
	return nil
}

func (c *ControlPlane) EvaluationRun(ctx context.Context, id EvaluationRunID) (EvaluationRun, error) {
	return c.evaluations.EvaluationRun(ctx, id)
}

// StartEvaluationRun moves a run to Running at the given time.
func (c *ControlPlane) StartEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Start(at)
	})
}

// CompleteEvaluationRun ends a run successfully.
//
// Completed means execution ended, not that the candidate passed, is safe, or
// may be promoted. Task 052 drew that line and comparison keeps it.
func (c *ControlPlane) CompleteEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Complete(at)
	})
}

func (c *ControlPlane) FailEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time, reason string,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Fail(at, reason)
	})
}

func (c *ControlPlane) CancelEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Cancel(at)
	})
}

// transition applies one domain transition under the store's compare-and-swap.
//
// There is no second lifecycle implementation here: the domain decides what is
// legal, and task 057's CAS decides whether the caller's view is still current.
func (c *ControlPlane) transition(
	ctx context.Context, id EvaluationRunID,
	apply func(EvaluationRun) (EvaluationRun, error),
) (EvaluationRun, error) {
	current, err := c.evaluations.EvaluationRun(ctx, id)
	if err != nil {
		return EvaluationRun{}, err
	}
	next, err := apply(current)
	if err != nil {
		return EvaluationRun{}, fmt.Errorf("%w: %w", ErrEvaluationState, err)
	}
	if err := c.evaluations.UpdateEvaluationRun(ctx, current, next); err != nil {
		return EvaluationRun{}, err
	}
	if kind, published := lifecycleKindFor(next.Status()); published {
		c.publishLifecycle(ctx, kind, next)
	}
	return next, nil
}

// lifecycleKindFor maps a committed status to its event kind.
//
// Pending has no kind: nothing transitions back to it, so it is only ever the
// creation state, which CreateEvaluationRun publishes directly.
func lifecycleKindFor(status RunStatus) (RealtimeEventKind, bool) {
	switch status {
	case RunRunning:
		return RealtimeEvaluationStarted, true
	case RunCompleted:
		return RealtimeEvaluationCompleted, true
	case RunFailed:
		return RealtimeEvaluationFailed, true
	case RunCancelled:
		return RealtimeEvaluationCancelled, true
	default:
		return "", false
	}
}

// ---------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------

// IngestDisposition is what happened to a submitted record.
//
// Two values, not three: a sequence conflict is an error. Presenting it as a
// success state would let a client treat "your record was not applied" as
// progress.
type IngestDisposition string

const (
	// IngestApplied means the record was folded into the evidence.
	IngestApplied IngestDisposition = "applied"

	// IngestReplayed means this exact record was already applied, and the
	// request changed nothing.
	IngestReplayed IngestDisposition = "replayed"
)

// IngestRequest is one record offered to a running evaluation.
type IngestRequest struct {
	RunID    EvaluationRunID
	Sequence uint64

	// BehavioralProfile travels beside the record rather than inside it.
	// DecisionRecord carries no learning scope — ADR 0024 decided a caller
	// that chose the scope already knows it — and adding the field to the
	// core's public type would push a platform concept into every producer.
	BehavioralProfile BehavioralProfileRef

	Record trustvian.DecisionRecord
}

// IngestResult reports the outcome and the cursor a client should use next.
type IngestResult struct {
	Disposition  IngestDisposition
	NextSequence uint64
	RecordCount  uint64

	// BehaviorComplete is false once the collector saturated at 512 distinct
	// behaviors. Aggregate evidence keeps advancing; behavioral evidence no
	// longer describes the whole run, and a later comparison will refuse it.
	BehaviorComplete bool
}

// IngestDecisionRecord folds one record into a running evaluation.
//
// The sequence is the retry contract. Task 053 made duplicates count twice on
// purpose, so a resubmitted record would otherwise corrupt the evidence; here
// only the expected sequence applies, only an identical retry of the previous
// one replays, and everything else fails closed.
func (c *ControlPlane) IngestDecisionRecord(
	ctx context.Context, request IngestRequest,
) (IngestResult, error) {
	run, err := c.evaluations.EvaluationRun(ctx, request.RunID)
	if err != nil {
		return IngestResult{}, err
	}

	// Evidence belongs to an execution that is happening. A pending run has
	// not started; a terminal one is finished and its evidence is what a
	// comparison will read.
	if run.Status() != RunRunning {
		return IngestResult{}, fmt.Errorf("%w: run %s is %s, records are accepted only while running",
			ErrEvaluationState, preview(string(request.RunID)), run.Status())
	}
	if request.BehavioralProfile != run.BehavioralProfile() {
		return IngestResult{}, fmt.Errorf(
			"%w: record was produced under profile %s, run %s uses %s",
			ErrInvalidBehaviorRecord, preview(string(request.BehavioralProfile)),
			preview(string(request.RunID)), preview(string(run.BehavioralProfile())))
	}

	digest, err := RecordDigest(request.Record)
	if err != nil {
		return IngestResult{}, err
	}

	state, err := c.ingest.EvaluationIngestState(ctx, request.RunID)
	if err != nil {
		return IngestResult{}, err
	}

	switch {
	case request.Sequence == state.NextSequence():
		// The expected record. Fall through and apply it.

	case request.Sequence+1 == state.NextSequence():
		// A retry of the last accepted record. Replay only when it is
		// provably the same record — a different payload under the same
		// sequence is two records claiming one position.
		//
		// A migrated task 057 run has no recorded digest, so nothing can be
		// proven identical and this conflicts rather than guessing.
		if state.LastDigest() != "" && state.LastDigest() == digest {
			// The counts travel with the cursor, read in one transaction, so
			// this reply describes a state that actually existed rather than
			// a cursor from one moment beside evidence from another.
			return IngestResult{
				Disposition:      IngestReplayed,
				NextSequence:     state.NextSequence(),
				RecordCount:      state.RecordCount(),
				BehaviorComplete: state.BehaviorComplete(),
			}, nil
		}
		return IngestResult{}, fmt.Errorf(
			"%w: sequence %d was already accepted with different content",
			ErrIngestSequence, request.Sequence)

	case request.Sequence < state.NextSequence():
		return IngestResult{}, fmt.Errorf(
			"%w: sequence %d is stale, run %s expects %d",
			ErrIngestSequence, request.Sequence,
			preview(string(request.RunID)), state.NextSequence())

	default:
		return IngestResult{}, fmt.Errorf(
			"%w: sequence %d leaves a gap, run %s expects %d",
			ErrIngestSequence, request.Sequence,
			preview(string(request.RunID)), state.NextSequence())
	}

	return c.applyRecord(ctx, run, request, digest, state)
}

// applyRecord folds the record into both reducers and commits atomically.
func (c *ControlPlane) applyRecord(
	ctx context.Context, run EvaluationRun, request IngestRequest,
	digest string, state EvaluationIngestState,
) (IngestResult, error) {
	aggregate, collector, err := c.currentEvidence(ctx, run)
	if err != nil {
		return IngestResult{}, err
	}

	// Captured before the record is folded in: afterwards the fingerprint is
	// present either way, so "was this new" can only be answered now. Read
	// from the trusted snapshot rather than a second durable set.
	newBehavior := !collector.knows(request.Record.FingerprintID)

	aggregate, err = aggregate.AddRecord(request.Record)
	if err != nil {
		return IngestResult{}, err
	}

	// Saturation is degraded evidence, not a failed ingest: the aggregate
	// legitimately accepted the record, and refusing would discard sound
	// decision evidence because *behavioral* evidence filled up. The snapshot
	// stays incomplete and says so, which is what makes a later comparison
	// refuse rather than quietly under-report.
	//
	// Every other collector error — environment mismatch, fingerprint
	// conflict, invalid identity, overflow — rejects the whole ingest.
	if err := collector.Observe(request.Record); err != nil && !errors.Is(err, ErrBehaviorCapacity) {
		return IngestResult{}, err
	}

	snapshot := collector.Snapshot()

	committed, err := c.ingest.CommitEvaluationIngest(ctx, EvaluationIngestCommit{
		PreviousNextSequence: state.NextSequence(),
		Sequence:             request.Sequence,
		RecordDigest:         digest,
		Aggregate:            aggregate,
		Snapshot:             snapshot,
	})
	if err != nil {
		return IngestResult{}, err
	}

	// The store may report that another request committed this exact record
	// first. Concurrent identical submissions are the retry contract working,
	// so all but one replay rather than conflicting — and the counts come
	// from the transaction that decided it, never from a second read that
	// could observe a third request's state.
	disposition := IngestApplied
	if committed.Disposition == EvaluationIngestAlreadyCommitted {
		disposition = IngestReplayed
	}

	// Only an applied record is announced. A replay — whether detected in
	// preflight or by the commit transaction losing a race — produced no
	// second durable record, so it must produce no second live observation.
	if disposition == IngestApplied {
		c.publishObservation(ctx, run, request, committed, newBehavior)
	}

	return IngestResult{
		Disposition:      disposition,
		NextSequence:     committed.NextSequence,
		RecordCount:      committed.RecordCount,
		BehaviorComplete: committed.BehaviorComplete,
	}, nil
}

// publishObservation notifies subscribers about one applied record.
//
// Counts come from the commit result rather than a fresh read, so the event
// cannot describe a state assembled from two different moments.
func (c *ControlPlane) publishObservation(
	ctx context.Context, run EvaluationRun, request IngestRequest,
	committed EvaluationIngestCommitResult, newBehavior bool,
) {
	if c.realtime == nil {
		return
	}
	scope, ok := c.resolveScope(ctx, run)
	if !ok {
		return
	}

	record := request.Record
	c.realtime.Publish(RealtimeEvent{
		Kind:  RealtimeObservationRecorded,
		Scope: scope,
		Observation: RealtimeObservation{
			Sequence:    request.Sequence,
			RecordCount: committed.RecordCount,
			// False once the collector saturated. Reported honestly: a live
			// view must not show complete behavioral evidence when the
			// snapshot has stopped being the whole truth.
			BehaviorComplete: committed.BehaviorComplete,

			FingerprintID: record.FingerprintID,
			Behavior:      record.Behavior,

			Decision:       record.Decision,
			RiskLevel:      record.RiskLevel,
			ApprovalStatus: record.ApprovalStatus,

			TrustScore:        record.TrustScore,
			AnomalyScore:      record.AnomalyScore,
			AnomalyConfidence: record.AnomalyConfidence,

			NewBehavior: newBehavior,
		},
	})
}

// currentEvidence loads a run's evidence, or starts it empty.
//
// The collector is rebuilt from the stored snapshot through a package-private
// helper. That is what lets a restarted process continue a running evaluation
// rather than beginning its behavioral evidence again — and it stays
// unexported because a public constructor would let any caller forge
// collector state.
func (c *ControlPlane) currentEvidence(
	ctx context.Context, run EvaluationRun,
) (EvaluationAggregate, *BehaviorCollector, error) {
	aggregate, snapshot, err := c.evaluations.EvaluationEvidence(ctx, run.ID())
	switch {
	case errors.Is(err, ErrStoreNotFound):
		aggregate, err = NewEvaluationAggregate(run)
		if err != nil {
			return EvaluationAggregate{}, nil, err
		}
		collector, err := NewBehaviorCollector(run)
		if err != nil {
			return EvaluationAggregate{}, nil, err
		}
		return aggregate, collector, nil

	case err != nil:
		return EvaluationAggregate{}, nil, err
	}

	collector, err := behaviorCollectorFromSnapshot(snapshot)
	if err != nil {
		return EvaluationAggregate{}, nil, err
	}
	return aggregate, collector, nil
}

// ---------------------------------------------------------------------
// Progress
// ---------------------------------------------------------------------

// EvaluationProgressReport is what an evaluation has observed so far.
//
// Facts only. No pass, fail, promotable or safety field: a running evaluation
// has progress, not a verdict, and a field like that would be read as one.
type EvaluationProgressReport struct {
	Run EvaluationRun

	RecordCount              uint64
	BehaviorObservationCount uint64
	DistinctBehaviorCount    int
	BehaviorComplete         bool
	NextIngestSequence       uint64
}

// EvaluationProgress reports a run's current evidence state.
func (c *ControlPlane) EvaluationProgress(
	ctx context.Context, id EvaluationRunID,
) (EvaluationProgressReport, error) {
	run, err := c.evaluations.EvaluationRun(ctx, id)
	if err != nil {
		return EvaluationProgressReport{}, err
	}
	state, err := c.ingest.EvaluationIngestState(ctx, id)
	if err != nil {
		return EvaluationProgressReport{}, err
	}

	// Every count comes from the cursor read above, which loads them in one
	// transaction alongside the sequence. Reading the evidence separately
	// would let a concurrent ingest move one between the two reads and
	// produce a report describing a state that never existed — a cursor at
	// N+1 beside an aggregate already at N+2.
	//
	// The run's status is read separately on purpose: it is independent of
	// evidence, and a lifecycle transition racing this read is a genuinely
	// concurrent fact rather than a torn one.
	return EvaluationProgressReport{
		Run:                      run,
		RecordCount:              state.RecordCount(),
		BehaviorObservationCount: state.BehaviorObservationCount(),
		DistinctBehaviorCount:    state.DistinctBehaviorCount(),
		BehaviorComplete:         state.BehaviorComplete(),
		NextIngestSequence:       state.NextSequence(),
	}, nil
}

// EvaluationIngestState reports a run's ingest cursor, so a client can
// resynchronize after losing track of its own sequence.
func (c *ControlPlane) EvaluationIngestState(
	ctx context.Context, id EvaluationRunID,
) (EvaluationIngestState, error) {
	return c.ingest.EvaluationIngestState(ctx, id)
}

// ---------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------

// EvaluationComparison is the full derived result of comparing two
// evaluations under one set of caller limits.
//
// Not persisted. Every part is a deterministic function of stored evidence,
// and ADR 0030 keeps one source of truth by recomputing rather than storing a
// second copy that can disagree.
type EvaluationComparison struct {
	Reference EvaluationRun
	Candidate EvaluationRun

	Diff      BehaviorDiff
	Scorecard EvaluationScorecard
	Gate      EvaluationGateResult
}

// CompareEvaluations derives diff, scorecard and gate result for two runs.
//
// The four steps below exist here once. A handler repeating them is exactly
// the duplication ADR 0031 forbids, because they are four calls in a fixed
// order with preconditions between them — the shape someone reimplements
// "just for this endpoint".
func (c *ControlPlane) CompareEvaluations(
	ctx context.Context,
	referenceRunID, candidateRunID EvaluationRunID,
	limits EvaluationGateLimits,
) (EvaluationComparison, error) {
	reference, err := c.completedRun(ctx, "reference", referenceRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}
	candidate, err := c.completedRun(ctx, "candidate", candidateRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}

	// Both runs must belong to one project, and this is checked before any
	// evidence is loaded — four reads saved on a request that can never
	// succeed, and no partial work on a pairing this refuses.
	//
	// CompareBehaviorSnapshots already requires the two runs to share an
	// EnvironmentRef, which has always stood in for "the same environment".
	// Task 065 made refs explicitly project-scoped, so two projects may each
	// own a "staging" and that equality alone would no longer establish it.
	// Together the two checks mean what the comparison always intended: the
	// same Environment, which is (ProjectID, EnvironmentRef).
	if err := c.requireSameProject(ctx, reference, candidate); err != nil {
		return EvaluationComparison{}, err
	}

	referenceAggregate, referenceSnapshot, err := c.comparisonEvidence(ctx, reference)
	if err != nil {
		return EvaluationComparison{}, err
	}
	candidateAggregate, candidateSnapshot, err := c.comparisonEvidence(ctx, candidate)
	if err != nil {
		return EvaluationComparison{}, err
	}

	// Task 054 refuses a saturated snapshot, and that refusal is passed
	// through rather than worked around. Incomplete evidence is not a failed
	// policy and not an unsafe candidate — it is a measurement that did not
	// finish, and manufacturing a scorecard from it would claim otherwise.
	diff, err := CompareBehaviorSnapshots(referenceSnapshot, candidateSnapshot)
	if err != nil {
		return EvaluationComparison{}, err
	}

	scorecard, err := NewEvaluationScorecard(referenceAggregate, candidateAggregate, diff)
	if err != nil {
		return EvaluationComparison{}, err
	}

	gate, err := EvaluateEvaluationGate(scorecard, NewEvaluationGatePolicy(limits))
	if err != nil {
		return EvaluationComparison{}, err
	}

	return EvaluationComparison{
		Reference: reference,
		Candidate: candidate,
		Diff:      diff,
		Scorecard: scorecard,
		Gate:      gate,
	}, nil
}

// requireSameProject refuses a comparison spanning two projects.
//
// Same project, not same agent. Comparing two candidates of one agent is the
// intended shape, but comparing two agents inside one project is merely
// unusual rather than ambiguous — the runs are in the same project's same
// environment, and the scorecard means what it says. The ambiguity
// project-scoped refs create sits precisely at the project boundary, and
// requiring the same agent would change behaviour for existing callers with
// no requirement behind it. A promotion workflow that needs agent identity
// adds that narrower precondition alongside itself.
func (c *ControlPlane) requireSameProject(ctx context.Context, reference, candidate EvaluationRun) error {
	referenceProject, err := c.projectOf(ctx, reference)
	if err != nil {
		return err
	}
	candidateProject, err := c.projectOf(ctx, candidate)
	if err != nil {
		return err
	}
	if referenceProject != candidateProject {
		return fmt.Errorf(
			"%w: reference run is in project %s, candidate run is in project %s; "+
				"an environment reference only identifies an environment within one project",
			ErrComparisonScope, preview(string(referenceProject)), preview(string(candidateProject)))
	}
	return nil
}

// comparisonEvidence resolves the evidence a completed run should be compared
// on, materializing the empty case rather than reporting it as absent.
//
// A run that ingested nothing persists no aggregate and no snapshot — rows
// appear on first ingest, and task 058 deliberately does not write evidence
// as a side effect of completing a run. So the store correctly reports no
// evidence, and only this layer knows what that absence means: the run
// exists, it completed, and it observed zero records.
//
// That distinction is security-relevant. Task 056 made minimum-evidence gates
// mandatory precisely because a candidate that ran nothing satisfies every
// maximum and would otherwise look perfect. Turning its comparison into a
// "not found" would hide that candidate instead of failing it — the exact
// outcome ADR 0029 exists to prevent.
//
// This is not a public evidence constructor and must not become one. It
// resolves comparison evidence for a run this service has already loaded and
// verified.
func (c *ControlPlane) comparisonEvidence(
	ctx context.Context, run EvaluationRun,
) (EvaluationAggregate, BehaviorSnapshot, error) {
	aggregate, snapshot, err := c.evaluations.EvaluationEvidence(ctx, run.ID())
	switch {
	case err == nil:
		return aggregate, snapshot, nil
	case !errors.Is(err, ErrStoreNotFound):
		// Corruption, including a partial pair, stays corruption. Only a
		// clean absence is a candidate for the empty case.
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}

	// Absence is not proof of emptiness. A cursor without its evidence is
	// impossible durable state, and the ingest capability already knows how
	// to tell that apart — asking it is what keeps this fallback from
	// silently healing corruption into an empty evaluation.
	state, err := c.ingest.EvaluationIngestState(ctx, run.ID())
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	if !isGenuinelyEmpty(state) {
		return EvaluationAggregate{}, BehaviorSnapshot{}, fmt.Errorf(
			"%w: run %s has no evidence but reports %d records at sequence %d",
			ErrStoreCorrupt, preview(string(run.ID())), state.RecordCount(), state.NextSequence())
	}

	// Built through the ordinary constructors, so these are bound values with
	// the run's identity and nothing hand-assembled.
	empty, err := NewEvaluationAggregate(run)
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	return empty, collector.Snapshot(), nil
}

// isGenuinelyEmpty reports whether a run's durable state is untouched.
//
// Every field, not just the record count: a run that never ingested has no
// cursor row, so its derived state is exactly this shape. Anything else means
// something was written and something else is missing.
func isGenuinelyEmpty(state EvaluationIngestState) bool {
	return state.NextSequence() == 1 &&
		state.LastDigest() == "" &&
		state.RecordCount() == 0 &&
		state.BehaviorObservationCount() == 0 &&
		state.DistinctBehaviorCount() == 0 &&
		state.BehaviorComplete()
}

// completedRun loads a run and requires it to have finished successfully.
//
// A running evaluation has mutable evidence, so a comparison of it would
// describe a moment that has already passed. A failed or cancelled run is not
// a completed evaluation at all.
func (c *ControlPlane) completedRun(
	ctx context.Context, side string, id EvaluationRunID,
) (EvaluationRun, error) {
	run, err := c.evaluations.EvaluationRun(ctx, id)
	if err != nil {
		return EvaluationRun{}, err
	}
	if run.Status() != RunCompleted {
		return EvaluationRun{}, fmt.Errorf(
			"%w: %s run %s is %s, comparison needs a completed evaluation",
			ErrEvaluationState, side, preview(string(id)), run.Status())
	}
	return run, nil
}
