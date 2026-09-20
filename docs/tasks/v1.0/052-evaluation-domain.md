# 052 — Evaluation Domain: Project, Agent, Candidate, EvaluationRun

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [049](049-platform-architecture-alignment.md),
[051](051-behavioral-profile-learning-scope-isolation.md) ·
**Blocks:** 053 onward · **The first platform-layer runtime code.**

## Objective

Establish the vocabulary and invariants an evaluation needs, as a pure domain
in a new `platform/` module:

```text
Project
  └─ Agent
      └─ Candidate
          └─ EvaluationRun ──▶ EnvironmentRef
                          └──▶ BehavioralProfileRef
```

This task answers: *what is an evaluation, what does it belong to, which
identities are stable, and what may change once a run begins?*

It does not answer: *what did the evaluation observe, how is that aggregated,
did it pass, or where is any of it stored?* Those are tasks 053–057.

## Why

[ADR 0022](../../adr/0022-core-platform-boundary.md) fixed the two-layer model
and the direction of dependency; [task 049](049-platform-architecture-alignment.md)
sequenced the work; tasks 050 and 051 made the two core changes the platform
needs and were explicitly the last expected. Everything after this point is
platform code, and platform code has had nowhere to live.

Starting with the domain rather than with persistence or an API is
deliberate. [ADR 0023](../../adr/0023-interfaces-are-adapters.md) sequences
capability interfaces *after* the call patterns exist, and a store or a
transport designed before the entities it moves would be designed against a
guess. The entities are also the part that later tasks cannot avoid agreeing
on: 053 aggregates *into* a run, 056 gates *on* one, 057 persists them, 058
serves them.

## Scope

- A new `platform/` Go module, module path `trustvian-platform`.
- `Project`, `Agent`, `Candidate`, `CandidateMetadata`, `EvaluationRun`.
- Typed identifiers and two opaque references: `EnvironmentRef`,
  `BehavioralProfileRef`.
- Construction-time validation and an explicit run lifecycle.
- A boundary check script and a CI job.

## Non-Goals

No persistence of any kind — no SQLite, PostgreSQL platform tables,
migrations, JSON files, embedded key-value stores, `database/sql`, repository
interfaces, `ControlStore`, or `EvaluationStore`. Task 057 owns it.

No HTTP handlers, routes, DTOs, SSE, WebSockets, local server, or ingest.
Task 058 owns it. **No JSON or YAML tags**: a domain type is not a wire
contract, and the transport contract is designed when the transport exists.

No aggregation — no event counts, decision tallies, anomaly or trust
averages, violation counts, new-behavior counts, or approval compliance. Task
053 owns it, and a "helpful" counter here would be a second implementation of
it.

No behavioral diff (054), no scorecard (055), no gates (056), no promotion
(066), no environment model (065), no raw event history (067).

**No core change.** Tasks 050 and 051 were the last two expected. A perceived
need for a third is an architectural warning, not a requirement.

## Domain Model

### Project

A local control-plane workspace that owns agents, and later the policy and
environment configuration those agents are evaluated with.

It is **not** a tenant, organization, RBAC boundary, or billing boundary.
Multi-tenancy is not implemented and no field anticipates it: no owner,
organization, subscription, region, or access control.

```text
Project.ID()    ProjectID
Project.Name()  string
```

### Agent

The stable logical identity of an agentic application across every version of
it. An Agent belongs to exactly one Project.

Its identity is deliberately independent of git SHA, artifact digest, model
version, deployment, and evaluation run — those describe a *Candidate*. An
Agent that changed identity per commit would make every deployment a new
actor, which is the same mistake [ADR 0022](../../adr/0022-core-platform-boundary.md)
rules out for fingerprints, one layer up.

```text
Agent.ID()         AgentID
Agent.ProjectID()  ProjectID
Agent.Name()       string
```

### Candidate

One version or configuration of an Agent being evaluated. Belongs to exactly
one Agent.

Candidate identity is **platform identity, never behavioral identity**. None
of its metadata reaches a fingerprint, `StableFeatures`, or a baseline key.

`Candidate.ID()`, `Candidate.AgentID()`, and `Candidate.Metadata()` read it.

Metadata is a fixed set of optional descriptive fields rather than a map.
`CandidateMetadata` stays a plain exported struct of strings — it is the one
type here with no invariant to protect, and returning it by value hands a
caller a copy with nothing to alias:

```text
CandidateMetadata.Label           human version label
CandidateMetadata.SourceRef       commit, tag, or branch
CandidateMetadata.ArtifactDigest  image or artifact digest
CandidateMetadata.Model           model identifier
CandidateMetadata.ToolsetDigest   digest of the tool set
CandidateMetadata.ConfigDigest    digest of the configuration
```

A `map[string]string` was considered and rejected. Fixed fields make the
shape reviewable, bound the total size by construction rather than by a
counted limit, remove the aliasing question entirely (there is nothing to
deep-copy), and make it obvious that this is descriptive context rather than
somewhere to put arbitrary payload. Adding a field later is additive; taking
back an open map is not.

Every field is optional and every field is bounded. Metadata is **descriptive
only** — nothing reads it to make a decision.

### EvaluationRun

One bounded execution assessing exactly one Candidate against one environment
reference and one behavioral profile reference.

```text
EvaluationRun.ID()                 EvaluationRunID
EvaluationRun.CandidateID()        CandidateID
EvaluationRun.Environment()        EnvironmentRef
EvaluationRun.BehavioralProfile()  BehavioralProfileRef
EvaluationRun.Status()             RunStatus
EvaluationRun.CreatedAt()          time.Time
EvaluationRun.StartedAt()          time.Time  zero until started
EvaluationRun.FinishedAt()         time.Time  zero until terminal
EvaluationRun.FailureReason()      string     non-empty only when failed
```

It holds **no results**: no `[]DecisionRecord`, no events, no scorecard, no
diff, no gate result. It is the container identity and lifecycle that task
053 will aggregate into.

### EnvironmentRef

An opaque reference saying *this run targeted environment X*. Nothing more.

Task 065 owns the real environment model — credentials, URLs, policies,
promotion ordering, deployment targets. Introducing a `struct Environment`
here would pre-empt a design whose requirements do not exist yet, and it is
not needed for a run to record what it targeted.

Not an enum. `local`, `sandbox`, `staging`, `production` are plausible
values, not a closed set, and nothing in the architecture requires exactly
those.

### BehavioralProfileRef

Which learned behavioral history a run is evaluated against. The mapping is:

```text
platform BehavioralProfileRef
        ↓  a later service or adapter decides
core     trustvian.WithLearningScope(string)
```

Task 052 does not perform that mapping and does not import the core to do it.

Critically, a profile reference is **not** equal by convention to a
`CandidateID`, an `EvaluationRunID`, or a git SHA. Those answer different
questions, and a later service will deliberately choose how profiles are
allocated and reused — two runs of one candidate may share a profile, or
not. Collapsing them now would make that choice for it, permanently and
invisibly.

## Identity Rules

Every identifier is its own string-backed type: `ProjectID`, `AgentID`,
`CandidateID`, `EvaluationRunID`, `EnvironmentRef`, `BehavioralProfileRef`.
The point is narrow — the compiler rejects `run.CandidateID = project.ID` —
not to build UUID machinery.

**Identifiers are caller-owned and opaque.** The domain never generates one,
never reads a clock, requires no UUID format, parses no meaning out of one,
and embeds no database identity. See
[ADR 0025](../../adr/0025-platform-domain-values-with-caller-owned-identity.md)
for why generation belongs to whatever adapter creates the entity.

**Entity state is unexported.** Construction goes through the four `New*`
functions, reading goes through value-returning accessors, and there is no
setter. The claim is precise rather than grand: Go has no immutability, and a
caller can always copy a value — but *domain identity and lifecycle state
cannot be mutated through the exported API*. With exported fields,
`run.Status = RunCompleted` on a pending run would compile from any package,
and every rule below would be a convention rather than a property.

**Ownership is carried, not verified.** An `Agent` records its `ProjectID`
and a `Candidate` records its `AgentID`, so a later service can check that
those targets exist — but a constructor validates only the entity in front of
it. Cross-entity existence checks need both entities loaded, which is task
057's concern. There is no package-level registry; the domain must not
quietly become persistence.

## Lifecycle Rules

```text
Pending ──▶ Running ──▶ Completed
   │           ├──────▶ Failed
   │           └──────▶ Cancelled
   └──────────────────▶ Cancelled

Completed, Failed, Cancelled ──X▶ anything
```

A terminal run is historical evidence. Re-running means creating a new run,
which is what keeps a run's record of what happened true afterwards.

`Start`, `Complete`, `Fail`, and `Cancel` are the **only** public way a run's
state moves. They **return a new value** rather than mutating the receiver,
matching how `internal/baseline.Baseline` already treats a domain value in
this repository, and no entity has a pointer-receiver method — a method set
that cannot mutate in place is what makes that guarantee structural. A
rejected transition returns the original value unchanged along with the error,
so a caller that ignores the error does not silently hold a half-applied
state.

### `Completed` is not a verdict

`RunStatus == Completed` means *the execution finished*. It does not mean the
candidate passed, is safe, or may be promoted. Task 056 owns gates and task
066 owns promotion; a `Passed` boolean here would let a run answer a question
it has no evidence for. This is asserted by test, not only documented.

## Validation

One identifier policy, not six. `maxIdentifierLength = 256` reuses the value
`config`'s `maxNameLength` already applies to public rule names, rather than
inventing a second number for the same kind of thing.

**Identifiers and references** must be non-empty, valid UTF-8, at most 256
bytes, free of control characters, and free of leading or trailing
whitespace. The whitespace rule matters more than it looks: `"cand-1 "` and
`"cand-1"` are different map keys and different database rows, and accepting
both creates two identities a human reads as one.

**Names** must be non-empty after trimming, valid UTF-8, at most 256 bytes,
and free of control characters. Spaces and Unicode are allowed — a project
name is for people.

**Metadata fields** are optional; when present each is bounded and control-free.

Sentinel errors, wrapped with `fmt.Errorf` and checked with `errors.Is`,
matching the repository's existing convention: `ErrInvalidID`,
`ErrInvalidName`, `ErrInvalidMetadata`, `ErrInvalidTransition`,
`ErrInvalidTimestamp`. Five, not one per field.

### Chronology

`StartedAt >= CreatedAt`, `FinishedAt >= StartedAt`. Violations are rejected,
never silently rewritten. Timestamps are caller-supplied — no domain
transition reads the clock, so every transition is deterministic and testable
without a clock abstraction this repository does not have.

Instants are compared as `time.Time` and stored as given. There is no
project-wide UTC-normalization convention to follow, and rewriting a caller's
location would discard information for no invariant.

## Security / Resource Bounds

Platform state will eventually receive caller-controlled strings across an
API. The bounds above are the whole of the answer for this task, and the
shape of the model is most of it: there is no map, no `any`, no raw payload
field, no prompt or completion field, and no tool-argument blob. A Candidate
carries identity context, not event history — task 067 owns history behind
its own capability boundary.

`FailureReason` is bounded and control-free: operator context explaining why
an execution ended, not an error-log model. No error objects, no stack
traces, no agent payload.

Two properties this task must preserve rather than create: an event producer
cannot select platform ownership identifiers (nothing in `event.Event`
carries one), and candidate metadata cannot become fingerprint identity
(nothing in the core can see it). Both are enforced by the boundary, checked
in CI.

## Tests

Construction and validation for each entity: empty and over-long identifiers,
empty names, control characters, surrounding whitespace, boundary lengths at
exactly 256 and 257 bytes, and valid multi-byte Unicode names.

Ownership: an Agent carries exactly one `ProjectID`, a Candidate exactly one
`AgentID`, and a missing owner is rejected.

Identity distinction: one Agent with two Candidates sharing a git SHA yields
two Candidates; metadata is descriptive and does not merge them. Agent
identity is independent of candidate metadata.

References: valid opaque values accepted, empty rejected, no enum enforced;
and the typed identifiers are mutually unassignable — asserted by a
compile-time test rather than prose.

Lifecycle: every allowed transition, every rejected one, terminal
immutability, and that a rejected transition returns the run unchanged.

Encapsulation: a reflective audit asserts that no entity has an exported
field, a `Set*` method, or a pointer receiver. These invariants are enforced
by *absence*, and absence is what a behavioral test cannot notice being
removed. Mutating a `CandidateMetadata` a caller received does not reach the
candidate.

One internal-package test covers the case the exported API cannot construct:
an `EvaluationRun` whose status is not one this package produces — reachable
the moment something deserializes a run — must fail closed rather than be
treated as pending.

Chronology: normal ordering, start before creation rejected, finish before
start rejected.

`Completed` is not a verdict — asserted explicitly.

Boundary: the module builds and tests with `GOWORK=off`, imports no core
`internal/*`, and the core does not depend on it.

No test involves the core. The domain does not import it.

## Benchmarks

None, and this is a deliberate statement rather than an omission. This is
control-plane domain code: entities are constructed when a human or an API
call creates one, not per event, so there is no hot path comparable to
`Engine.Analyze`. Validation is a bounded scan of a ≤256-byte string with no
regular expressions, no reflection, and no deep copying — if it needed a
benchmark, the right response would be to simplify it instead.

## Documentation

New: this file, [ADR 0025](../../adr/0025-platform-domain-values-with-caller-owned-identity.md),
`docs/adr/README.md`, `docs/tasks/v1.0/README.md`.

Updated for the module count changing from three to four: `CONTRIBUTING.md`,
`docs/release-guide.md`, `docs/README.md`, `docs/supply-chain.md`,
`docs/ROADMAP.md`, `scripts/check-modules.sh`, `Makefile`.

Updated for the new layer: `docs/ARCHITECTURE.md`, `docs/DOMAIN.md`,
`docs/SECURITY.md`, `CHANGELOG.md`.

Historical `CHANGELOG.md` entries and archived task documents keep their
"three modules" wording: they describe what was true when written.

## Acceptance Criteria

- [ ] `platform/` is a separate module at `trustvian-platform`, building and
      testing under `GOWORK=off`.
- [ ] The platform imports no `github.com/trustvian/trustvian/internal/…`,
      enforced by script and CI.
- [ ] The core module does not depend on the platform module.
- [ ] No platform identifier or concept appears in core runtime code.
- [ ] `Project`, `Agent`, `Candidate`, `EvaluationRun` exist only in the
      platform.
- [ ] Identifiers are typed, caller-owned, and never generated by the domain.
- [ ] Candidate metadata is a bounded fixed set, descriptive only.
- [ ] `EnvironmentRef` is a reference; no environment model exists.
- [ ] `BehavioralProfileRef` is distinct from `CandidateID` and
      `EvaluationRunID` at the type level.
- [ ] Entity state is unexported; no setter and no pointer-receiver method
      exists, and no invariant can be bypassed through the exported API.
- [ ] The run lifecycle rejects every disallowed transition, and terminal
      states are final.
- [ ] `Completed` means execution finished, proven not to mean "passed".
- [ ] No persistence, transport, aggregation, diff, scorecard, gate, or
      promotion code exists.
- [ ] No core runtime file changed; no tag or release was created.
- [ ] `gofmt`, `go vet`, `go test`, `go test -race` pass in every module;
      `make check-modules` and the boundary check pass.
