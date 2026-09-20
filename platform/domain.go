// Package platform is Trustvian's control-plane domain: the vocabulary an
// evaluation is described in, and the rules about what may change once one
// has begun.
//
//	Project
//	  └─ Agent
//	      └─ Candidate
//	          └─ EvaluationRun ──▶ EnvironmentRef
//	                          └──▶ BehavioralProfileRef
//
// It is deliberately a separate Go module, and deliberately not named
// github.com/trustvian/trustvian/platform. Go's internal/ rule turns on
// import-path ancestry rather than module membership, so a repository-
// prefixed path would be allowed to import the engine's internal packages;
// trustvian-platform is rejected by the compiler instead. See
// docs/adr/0022-core-platform-boundary.md.
//
// The dependency direction is one-way: the platform may depend on the core's
// public API, and the core never depends on the platform. Today this package
// does not import the core at all. That is not an oversight — the domain has
// no use for a DecisionRecord yet, and adding the dependency to demonstrate
// the relationship would be exactly the speculative coupling the boundary
// exists to prevent. Task 053 introduces it when aggregation needs it.
//
// What this package is not: persistence, transport, aggregation, behavioral
// diff, scorecard, gate, or promotion. Those are separate tasks, and each
// would be easier to add here than to remove later.
package platform

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// maxIdentifierLength bounds every identifier, reference, name, and metadata
// field in this package.
//
// One constant rather than one per kind. 256 is not a new number: it is what
// config.maxNameLength already applies to public policy rule names in the
// core, and these values play the same role — caller-supplied text that
// crosses an API and lands in storage. Inventing a second limit for the same
// kind of thing would make neither easier to reason about.
const maxIdentifierLength = 256

// Sentinel errors, wrapped with fmt.Errorf and matched with errors.Is,
// following the convention the core packages already use. Five categories, not
// one per field: a caller branches on "the identifier was unusable", never on
// which of nine identifiers it was. The wrapped message names the field.
var (
	ErrInvalidID         = errors.New("platform: invalid identifier")
	ErrInvalidName       = errors.New("platform: invalid name")
	ErrInvalidMetadata   = errors.New("platform: invalid candidate metadata")
	ErrInvalidTransition = errors.New("platform: invalid lifecycle transition")
	ErrInvalidTimestamp  = errors.New("platform: invalid timestamp")
)

// Identifiers and references. Each is its own type so the compiler rejects
// the one mistake that would otherwise be silent and unfindable:
//
//	run.CandidateID = project.ID   // does not compile
//
// They are opaque and caller-owned. This package never generates one, never
// reads a clock to make one, requires no UUID format, and parses no meaning
// out of one — whichever adapter admits an entity into a system owns its
// identity. See docs/adr/0025-platform-domain-values-with-caller-owned-identity.md.
type (
	ProjectID       string
	AgentID         string
	CandidateID     string
	EvaluationRunID string
)

// EnvironmentRef names the environment a run targeted — nothing more.
//
// Deliberately a reference rather than a model. The real environment, with
// its credentials, endpoints, policies and promotion ordering, is a later
// task's design; a run only needs to record what it pointed at, and a struct
// invented here would pre-empt requirements that do not exist yet.
//
// Not an enum. "local", "sandbox", "staging" and "production" are plausible
// values, not a closed set.
type EnvironmentRef string

// BehavioralProfileRef names the learned behavioral history a run is
// evaluated against. A later service maps it onto the core's learning scope:
//
//	platform BehavioralProfileRef
//	        ↓  a service or adapter decides the allocation
//	core     trustvian.WithLearningScope(string)
//
// It is emphatically **not** equal by convention to a CandidateID, an
// EvaluationRunID, or a commit SHA. Those answer different questions, and
// whether two runs of one candidate share a profile is a decision a later
// task must be free to make. Collapsing the identities now would make that
// choice permanently and invisibly.
type BehavioralProfileRef string

// Project is a local control-plane workspace owning agents, and later the
// policy and environment configuration they are evaluated with.
//
// It is not a tenant, an organization, an access-control boundary, or a
// billing boundary, and carries no field anticipating one. Multi-tenancy is
// an access-control concern for a different product surface; a speculative
// OwnerID here would be a shape nothing reads and everything has to keep.
// Its state is unexported and reachable only through the accessors below.
// That is not ceremony: with exported fields, every invariant a constructor
// establishes could be undone by one assignment from outside the package, and
// the guarantee this type offers would be a convention rather than a
// property. See docs/adr/0025-platform-domain-values-with-caller-owned-identity.md.
type Project struct {
	id   ProjectID
	name string
}

// NewProject validates and returns a Project.
func NewProject(id ProjectID, name string) (Project, error) {
	if err := validateID("project id", string(id)); err != nil {
		return Project{}, err
	}
	if err := validateName("project name", name); err != nil {
		return Project{}, err
	}
	return Project{id: id, name: name}, nil
}

// ID returns the project's identifier.
func (p Project) ID() ProjectID { return p.id }

// Name returns the project's human-facing name.
func (p Project) Name() string { return p.name }

// Agent is the stable logical identity of an agentic application across every
// version of it. It belongs to exactly one Project.
//
// Its identity is independent of commit, artifact digest, model, deployment,
// and evaluation run — all of which describe a Candidate. An Agent that
// changed identity per commit would make every deployment a new actor, which
// is the same mistake ADR 0022 rules out for fingerprints, one layer up.
type Agent struct {
	id        AgentID
	projectID ProjectID
	name      string
}

// NewAgent validates and returns an Agent owned by projectID.
//
// The owning project is recorded, not verified: this constructor cannot see
// other entities, and checking existence needs both loaded at once. Carrying
// the reference is what makes that check possible later.
func NewAgent(id AgentID, projectID ProjectID, name string) (Agent, error) {
	if err := validateID("agent id", string(id)); err != nil {
		return Agent{}, err
	}
	if err := validateID("agent project id", string(projectID)); err != nil {
		return Agent{}, err
	}
	if err := validateName("agent name", name); err != nil {
		return Agent{}, err
	}
	return Agent{id: id, projectID: projectID, name: name}, nil
}

// ID returns the agent's identifier — stable across every Candidate.
func (a Agent) ID() AgentID { return a.id }

// ProjectID returns the project this agent belongs to.
func (a Agent) ProjectID() ProjectID { return a.projectID }

// Name returns the agent's human-facing name.
func (a Agent) Name() string { return a.name }

// CandidateMetadata describes which version or configuration a Candidate is.
// Every field is optional, bounded, and **descriptive only** — nothing in
// Trustvian reads any of it to make a decision, and none of it ever reaches a
// fingerprint, StableFeatures, or a baseline key.
//
// A fixed set of fields rather than a map, deliberately. Fixed fields bound
// the total size by construction instead of by a counted limit, remove the
// aliasing question entirely (there is nothing to deep-copy), keep the shape
// reviewable, and make it obvious that this is context rather than somewhere
// to put arbitrary payload. A field can be added in a later task; an open map
// cannot be taken back.
type CandidateMetadata struct {
	// Label is a human version label, e.g. "v3" or "with-retry-tool".
	Label string

	// SourceRef is a commit, tag, or branch.
	SourceRef string

	// ArtifactDigest identifies the built artifact, e.g. an image digest.
	ArtifactDigest string

	// Model identifies the model this candidate runs on.
	Model string

	// ToolsetDigest and ConfigDigest identify the tool set and configuration
	// the candidate was assembled with.
	ToolsetDigest string
	ConfigDigest  string
}

func (m CandidateMetadata) validate() error {
	for _, f := range []struct{ name, value string }{
		{"label", m.Label},
		{"source_ref", m.SourceRef},
		{"artifact_digest", m.ArtifactDigest},
		{"model", m.Model},
		{"toolset_digest", m.ToolsetDigest},
		{"config_digest", m.ConfigDigest},
	} {
		if f.value == "" {
			continue // every field is optional
		}
		if err := validateText(ErrInvalidMetadata, "candidate "+f.name, f.value); err != nil {
			return err
		}
	}
	return nil
}

// Candidate is one version or configuration of an Agent being evaluated. It
// belongs to exactly one Agent.
//
// Candidate identity is **platform identity, never behavioral identity**. Two
// candidates built from the same commit are still two candidates; a candidate
// is not a fingerprint dimension, a learning scope, or an actor.
//
// There is deliberately no SetSourceRef, no ChangeAgent, and no other way for
// one Candidate to become another. Evaluating a different artifact means
// creating another Candidate — which is the only thing that keeps a finished
// EvaluationRun describing what it actually ran.
type Candidate struct {
	id       CandidateID
	agentID  AgentID
	metadata CandidateMetadata
}

// NewCandidate validates and returns a Candidate owned by agentID.
func NewCandidate(id CandidateID, agentID AgentID, meta CandidateMetadata) (Candidate, error) {
	if err := validateID("candidate id", string(id)); err != nil {
		return Candidate{}, err
	}
	if err := validateID("candidate agent id", string(agentID)); err != nil {
		return Candidate{}, err
	}
	if err := meta.validate(); err != nil {
		return Candidate{}, err
	}
	return Candidate{id: id, agentID: agentID, metadata: meta}, nil
}

// ID returns the candidate's identifier.
func (c Candidate) ID() CandidateID { return c.id }

// AgentID returns the agent this candidate is a version of.
func (c Candidate) AgentID() AgentID { return c.agentID }

// Metadata returns the candidate's descriptive metadata.
//
// Returned by value, which is safe precisely because CandidateMetadata holds
// only strings: a caller receives a copy and there is nothing to alias. This
// is the second reason the type is a fixed set of fields rather than a map.
func (c Candidate) Metadata() CandidateMetadata { return c.metadata }

// RunStatus is the state of an execution, and only of an execution.
//
// Completed means the run finished. It does **not** mean the candidate
// passed, is safe, or may be promoted — those are a gate's answer and a
// promotion workflow's answer, each reading a run's evidence separately. This
// type has no Passed, Score, or Grade for that reason: such a field would let
// a run answer a question it holds no evidence for, and would become the
// field a future caller reads instead of the gate.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// IsTerminal reports whether s is final. A terminal run is historical
// evidence: it never transitions again, and re-running means creating a new
// run.
func (s RunStatus) IsTerminal() bool {
	switch s {
	case RunCompleted, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

func (s RunStatus) valid() bool {
	switch s {
	case RunPending, RunRunning, RunCompleted, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// EvaluationRun is one bounded execution assessing exactly one Candidate
// against one environment reference and one behavioral profile reference.
//
//	Pending ──▶ Running ──▶ Completed
//	   │           ├──────▶ Failed
//	   │           └──────▶ Cancelled
//	   └──────────────────▶ Cancelled
//
//	Completed, Failed, Cancelled ──X▶ anything
//
// It holds no results. There is no decision record slice, no event list, no
// scorecard, no diff, and no gate outcome here: this is the container
// identity and lifecycle that aggregation will later attach to, and a
// "helpful" counter on it would be a second implementation of that.
//
// Transitions return a new EvaluationRun rather than mutating the receiver,
// matching how the core already treats a domain value — a run handed to a
// caller stays valid and cannot change underneath them.
// Its state is unexported, which is what makes the lifecycle above a
// guarantee rather than a description. With exported fields a caller could
// write `run.Status = RunCompleted` on a pending run, or clear its candidate,
// and every rule this type enforces would be advisory. The transition methods
// are the only way to move a run, and there is deliberately no setter.
type EvaluationRun struct {
	id          EvaluationRunID
	candidateID CandidateID
	environment EnvironmentRef
	profile     BehavioralProfileRef

	status RunStatus

	// createdAt is when the run was recorded. startedAt and finishedAt are
	// zero until the corresponding transition happens.
	createdAt  time.Time
	startedAt  time.Time
	finishedAt time.Time

	// failureReason is operator context explaining why an execution ended,
	// non-empty only for a failed run. Bounded and control-free: this is not
	// an error-log model, and it carries no error object, stack trace, or
	// agent payload.
	failureReason string
}

// NewEvaluationRun returns a run in RunPending. Every reference is required:
// a run that did not know its candidate, environment, or behavioral profile
// could not be interpreted afterwards, which is the only thing a run is for.
//
// createdAt is supplied by the caller rather than read from the clock, so
// every transition in this package is deterministic and testable without a
// clock abstraction.
func NewEvaluationRun(
	id EvaluationRunID,
	candidateID CandidateID,
	environment EnvironmentRef,
	profile BehavioralProfileRef,
	createdAt time.Time,
) (EvaluationRun, error) {
	if err := validateID("evaluation run id", string(id)); err != nil {
		return EvaluationRun{}, err
	}
	if err := validateID("evaluation run candidate id", string(candidateID)); err != nil {
		return EvaluationRun{}, err
	}
	if err := validateID("evaluation run environment", string(environment)); err != nil {
		return EvaluationRun{}, err
	}
	if err := validateID("evaluation run behavioral profile", string(profile)); err != nil {
		return EvaluationRun{}, err
	}
	if createdAt.IsZero() {
		return EvaluationRun{}, fmt.Errorf("%w: evaluation run created_at is not set", ErrInvalidTimestamp)
	}

	return EvaluationRun{
		id:          id,
		candidateID: candidateID,
		environment: environment,
		profile:     profile,
		status:      RunPending,
		createdAt:   createdAt,
	}, nil
}

// ID returns the run's identifier.
func (r EvaluationRun) ID() EvaluationRunID { return r.id }

// CandidateID returns the candidate this run assessed.
func (r EvaluationRun) CandidateID() CandidateID { return r.candidateID }

// Environment returns the environment reference this run targeted.
func (r EvaluationRun) Environment() EnvironmentRef { return r.environment }

// BehavioralProfile returns the profile reference this run was evaluated
// against. Named for what it is rather than after the field, because
// `run.Profile()` would leave a reader guessing which kind of profile.
func (r EvaluationRun) BehavioralProfile() BehavioralProfileRef { return r.profile }

// Status returns the state of the execution — not a verdict. See RunStatus.
func (r EvaluationRun) Status() RunStatus { return r.status }

// CreatedAt returns when the run was recorded.
func (r EvaluationRun) CreatedAt() time.Time { return r.createdAt }

// StartedAt returns when the execution began, or the zero time if it never
// did — a run cancelled while pending has no start.
func (r EvaluationRun) StartedAt() time.Time { return r.startedAt }

// FinishedAt returns when the run reached a terminal state, or the zero time
// if it has not.
func (r EvaluationRun) FinishedAt() time.Time { return r.finishedAt }

// FailureReason returns operator context for a failed run, and "" for every
// other state.
func (r EvaluationRun) FailureReason() string { return r.failureReason }

// Start moves a pending run to RunRunning.
func (r EvaluationRun) Start(at time.Time) (EvaluationRun, error) {
	if err := r.transitionAllowed(RunRunning); err != nil {
		return r, err
	}
	if err := notBefore("started_at", at, "created_at", r.createdAt); err != nil {
		return r, err
	}
	// r is a value receiver: these assignments mutate this function's own
	// copy, and the caller's run is untouched.
	r.status = RunRunning
	r.startedAt = at
	return r, nil
}

// Complete moves a running run to RunCompleted.
//
// This records that the execution finished. It records nothing about whether
// the candidate passed — see RunStatus.
func (r EvaluationRun) Complete(at time.Time) (EvaluationRun, error) {
	return r.finish(RunCompleted, at, "")
}

// Fail moves a running run to RunFailed, recording why the execution ended.
// reason is optional and bounded.
func (r EvaluationRun) Fail(at time.Time, reason string) (EvaluationRun, error) {
	return r.finish(RunFailed, at, reason)
}

// Cancel moves a pending or running run to RunCancelled.
func (r EvaluationRun) Cancel(at time.Time) (EvaluationRun, error) {
	return r.finish(RunCancelled, at, "")
}

func (r EvaluationRun) finish(to RunStatus, at time.Time, reason string) (EvaluationRun, error) {
	if err := r.transitionAllowed(to); err != nil {
		return r, err
	}
	if reason != "" {
		if err := validateText(ErrInvalidMetadata, "evaluation run failure reason", reason); err != nil {
			return r, err
		}
	}

	// A cancelled run may never have started, in which case the only
	// ordering that exists to check is against creation.
	after, afterName := r.startedAt, "started_at"
	if after.IsZero() {
		after, afterName = r.createdAt, "created_at"
	}
	if err := notBefore("finished_at", at, afterName, after); err != nil {
		return r, err
	}

	r.status = to
	r.finishedAt = at
	r.failureReason = reason
	return r, nil
}

// transitionAllowed encodes the whole state machine in one place, so a new
// state cannot be added without deciding what may reach it.
func (r EvaluationRun) transitionAllowed(to RunStatus) error {
	if !r.status.valid() {
		return fmt.Errorf("%w: run is in an unrecognized state %q", ErrInvalidTransition, r.status)
	}
	if r.status.IsTerminal() {
		return fmt.Errorf("%w: %s -> %s: a terminal run is historical evidence; create a new run instead",
			ErrInvalidTransition, r.status, to)
	}

	allowed := false
	switch r.status {
	case RunPending:
		allowed = to == RunRunning || to == RunCancelled
	case RunRunning:
		allowed = to == RunCompleted || to == RunFailed || to == RunCancelled
	}
	if !allowed {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, r.status, to)
	}
	return nil
}

// notBefore rejects an out-of-order timestamp rather than rewriting it. A
// silently corrected instant is worse than a refused one: it would make a
// run's own record of when it ran untrue, which is the one thing the run is
// evidence of.
func notBefore(name string, at time.Time, thanName string, than time.Time) error {
	if at.IsZero() {
		return fmt.Errorf("%w: evaluation run %s is not set", ErrInvalidTimestamp, name)
	}
	if at.Before(than) {
		return fmt.Errorf("%w: evaluation run %s %s precedes %s %s",
			ErrInvalidTimestamp, name, at.Format(time.RFC3339Nano), thanName, than.Format(time.RFC3339Nano))
	}
	return nil
}

// validateID checks an opaque identifier or reference.
//
// The leading/trailing whitespace rule earns its place: "cand-1 " and
// "cand-1" are different map keys and different database rows, and accepting
// both creates two identities a human reads as one.
func validateID(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidID, field)
	}
	if err := validateText(ErrInvalidID, field, value); err != nil {
		return err
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s has leading or trailing whitespace", ErrInvalidID, field)
	}
	return nil
}

// validateName checks a human-facing name. Spaces and Unicode are allowed —
// a project name is for people — but a name of only whitespace is not a name.
func validateName(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidName, field)
	}
	return validateText(ErrInvalidName, field, value)
}

// validateText applies the bounds every caller-supplied string in this
// package shares: a length limit, valid UTF-8, and no control characters.
//
// A bounded scan of at most maxIdentifierLength bytes, with no regular
// expression, no reflection, and no allocation.
func validateText(kind error, field, value string) error {
	if len(value) > maxIdentifierLength {
		return fmt.Errorf("%w: %s is %d bytes (max %d)", kind, field, len(value), maxIdentifierLength)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", kind, field)
	}
	for _, r := range value {
		// Unicode's C0 and C1 control ranges, plus DEL. Rejected because
		// these survive no boundary intact: a newline splits a log line, a
		// NUL truncates a C string, and an escape sequence rewrites a
		// terminal an operator is reading.
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return fmt.Errorf("%w: %s contains the control character %U", kind, field, r)
		}
	}
	return nil
}
