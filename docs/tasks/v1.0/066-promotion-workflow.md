# 066 — Promotion Workflow

Status: specified. Not implemented
Depends on: [052](052-evaluation-domain.md),
[055](055-evaluation-scorecards.md),
[056](056-deterministic-hard-gates.md),
[057](057-local-platform-persistence.md),
[058](058-local-control-plane-api-and-ingest.md),
[060](060-developer-cli.md),
[063](063-minimal-web-control-plane.md),
[064](064-postgresql-platform-backend.md),
[065](065-environment-model.md)
Blocks: [072](README.md) — the `v1.0` release gate

## Objective

Turn completed evaluation evidence into a durable platform decision: **this
candidate was accepted to advance from this environment toward that one, on
this evidence, under these limits, at this moment.**

```text
completed reference run ─┐
                         ├─▶ CompareEvaluations ─▶ diff · scorecard · gate
completed candidate run ─┘                                    │
                                                              ▼
source Environment  (inferred from the two runs)      gate verdict
target Environment  (named by the caller)                     │
         │                                                    │
         └────────▶ CanPromote(source, target) ───────────────┤
                                                              ▼
                                                        Promotion
                                                   immutable · append-only
```

Task 066 is the first layer allowed to answer:

> May this candidate advance from this environment toward that environment,
> under these explicit gate limits, on these evaluation runs?

It is not allowed to answer, and does not model:

> Has anything been deployed? Did a release system move an artifact? Where
> does this environment live? Who is authorized to approve this?

**Trustvian records the decision. Trustvian does not deploy.**

## Why

Three things exist and do not yet meet.

**The gate produces a verdict that nothing keeps.** Task 056 gives
`EvaluateEvaluationGate` a deterministic PASS/FAIL over caller-owned limits,
and `POST /v1/evaluations/compare` returns it. The moment that response is
closed, the verdict is gone: the limits that produced it were caller-owned and
stored nowhere, so "which policy did we ship this under" has no answer three
months later. The evidence survives; the decision does not.

**The ordering primitive has no caller.** Task 065 added
`CanPromote(from, to)` and one wrapper, `ControlPlane.PromotionOrder`, whose
only caller today is its own test. It answers "is B forward of A" and
authorizes nothing, by design — 065 stopped exactly there. Something has to
ask it in anger.

**The roadmap's release criterion 11 is unmet.**

> A promotion decision is recorded together with the evidence it rested on.

Nothing in the platform records a promotion decision at all, and criterion 12
requires CLI, TUI and WebUI to reach the same result through control-plane
services with no interface carrying its own copy of promotion logic. Task 063
deliberately shipped the WebUI with promotion reserved for this milestone,
"**including a disabled or mock 'Promote' control**".

## Scope

A complete vertical slice, on both persistence backends:

- a `Promotion` domain value — immutable, fixed-shape, caller-owned identity;
- one authoritative control-plane operation that owns the ordered
  preconditions and the write;
- `ControlStore` extended with create, read and one bounded project-scoped
  collection;
- `SchemaVersion` 3 → 4 on SQLite and PostgreSQL, with one new table and a
  migration that fabricates no history;
- three `/v1` routes;
- a `trustvian promotion` CLI family;
- the WebUI slice task 063 reserved: choose a target, request a promotion,
  render the server's answer, read the history.

## Non-Goals

**Deployment, in every form.** No deployment target, endpoint, URL,
credential, secret, token, job, callback, webhook, status or outbound network
call. Nothing in this task connects to anything.

**Candidate residence.** No `Candidate.CurrentEnvironment`, no
`Environment.DeployedCandidate`, no "what is running in production" query.
See [Residence is not modeled](#residence-is-not-modeled).

**Rollback.** `CanPromote` is forward-only and this task does not invert it.
See [Rollback is not reverse promotion](#rollback-is-not-reverse-promotion).

**Human approval and authorization.** No `approved_by`, approver role, RBAC,
permission check, user account, session, or multi-step approval. Task 070 owns
platform security hardening; organizational access control is beyond `v1.0`.
An `event.Context` `ApprovalStatus` is producer-supplied behavioral evidence
and task 056 already refused to read it as authorization — this task does not
reverse that.

**A policy registry.** No `PromotionPolicy`, `GatePolicy` table,
`PolicyAssignment` or `EnvironmentPolicy`. Gate limits stay caller-owned,
fixed-shape `EvaluationGateLimits`, snapshotted onto the decision.

**Any core change.** No `event.Event`, `StableFeatures`, `Fingerprint`,
`baseline.Key`, `Engine`, `Analyze`, `Observe`, trust, anomaly, policy or
learning-scope change. No platform type — `PromotionID`, `CandidateID`,
`EnvironmentRef`, `ProjectID` or any other — enters the core module. The
engine produces evidence; the platform owns promotion.

**A TUI promotion action.** Task 061 and
[ADR 0034](../../adr/0034-tui-is-a-bounded-realtime-http-client.md) scope the
TUI to the live development loop and say it does not own administration.

**Chronological history across an unbounded collection.** See
[Why the cursor is the identifier](#why-the-cursor-is-the-identifier-and-not-the-timestamp).

## What a Promotion Is

A `Promotion` is a **durable, immutable record of a decision the platform
made**. Present tense, precisely:

> At `decided_at`, the platform was asked whether candidate *C* should advance
> from environment *S* to environment *T* on the evidence of runs *R* and *K*
> under limits *L*. Its answer was *outcome*.

Four things that answer deliberately is not:

| It is not | Because |
|---|---|
| a deployment | An `Environment` holds no address. Nothing was moved, and the platform has no observer that could tell you whether anything was |
| a statement of residence | The record says a decision was made, not where the candidate now runs |
| an authorization | Nobody was identified, so nobody approved anything. It records that the *evidence* met the *limits* |
| a safety claim | Task 056 is explicit that PASS means "the five configured gates passed" and never "safe", and promotion inherits that unchanged |

A promotion is therefore closer to a **signed-off test result** than to a
release. That framing is what keeps every field on it justifiable and keeps
the platform honest about what it can observe.

### Residence is not modeled

An accepted `sandbox → production` promotion does **not** make the candidate
"in production". Trustvian has no deployment observer and cannot know whether
anything ran. Adding `Candidate.CurrentEnvironment` would create a field whose
value is a guess the platform would then serve as fact — and the first time a
deployment failed after an accepted promotion, the platform would be
confidently wrong about the single thing an operator most needs to be right.

If a future milestone integrates with a deployment system, current residence
becomes an *evidence contract with that system* — something observed and
attributed, with its own freshness and trust semantics — not a field this task
can write. That is a separate capability and this task does not prepare for it
speculatively.

### Rollback is not reverse promotion

`CanPromote(production, staging)` is false and stays false. A rollback is a
statement about what is *running*, which is residence, which this task does
not model. Calling it "promotion backwards" would make the ordering relation
mean two things and would require the platform to claim it knows something it
does not.

If a recorded decision later turns out to be wrong, the correction is a new
record in a future workflow, never a mutation of the old one. History is
append-only here for exactly this reason.

## Domain Model

### `Promotion`

```go
// Promotion is one recorded platform decision. Immutable: it has no
// transition method, and the store has no update.
type Promotion struct {
    id          PromotionID
    projectID   ProjectID
    candidateID CandidateID

    referenceRunID EvaluationRunID
    candidateRunID EvaluationRunID

    source EnvironmentPosition
    target EnvironmentPosition

    limits EvaluationGateLimits

    outcome   PromotionOutcome
    decidedAt time.Time
}

func (p Promotion) ID() PromotionID                 { /* … */ }
func (p Promotion) ProjectID() ProjectID            { /* … */ }
func (p Promotion) CandidateID() CandidateID        { /* … */ }
func (p Promotion) ReferenceRunID() EvaluationRunID { /* … */ }
func (p Promotion) CandidateRunID() EvaluationRunID { /* … */ }
func (p Promotion) Source() EnvironmentPosition     { /* … */ }
func (p Promotion) Target() EnvironmentPosition     { /* … */ }
func (p Promotion) GateLimits() EvaluationGateLimits{ /* … */ }
func (p Promotion) Outcome() PromotionOutcome       { /* … */ }
func (p Promotion) DecidedAt() time.Time            { /* … */ }
```

Unexported fields with accessors, matching every other platform domain value
(ADR 0025). No pointer receiver, no setter, no `With…`, no lifecycle. A
`Promotion` that exists is finished.

### `PromotionID`

```go
type PromotionID string
```

Added to the existing identifier block in `domain.go` alongside `ProjectID`,
`AgentID`, `CandidateID` and `EvaluationRunID`, validated by the same
`validateID` rules: non-empty, ≤ 256 bytes, valid UTF-8, no control
characters, no leading or trailing whitespace. **Caller-owned** — nothing
generates it, and no UUID dependency appears (ADR 0025 is binding).

#### Why an identifier exists, and why `(candidate, source, target)` is not one

That tuple is not identity because it is not unique, and it must not be:

- a candidate rejected under strict limits, fixed, re-evaluated and promoted
  again produces **two decisions about the same tuple**, and the first must
  not be overwritten by the second — that is the whole audit value;
- two teams deciding the same advance under different limits produce two
  distinct facts;
- a promotion that a deployment later reverses operationally leaves the record
  standing, and a subsequent promotion of the same tuple is a new decision.

Timestamps are not identity either: two decisions can share a clock tick, a
clock can move backwards, and making time the key would make identity depend
on a value nobody supplied. An explicit caller-owned identifier is what makes
a retried HTTP request name the same decision rather than create a second one
— see [Duplicates and retries](#duplicates-and-retries).

### `PromotionOutcome`

```go
type PromotionOutcome string

const (
    // PromotionAccepted: every structural precondition held and the gate
    // verdict was PASS.
    PromotionAccepted PromotionOutcome = "accepted"

    // PromotionRejected: every structural precondition held and the gate
    // verdict was FAIL.
    //
    // Not an incident, not a fault, and not a security event. The evidence
    // was complete and valid; it did not meet the caller's limits.
    PromotionRejected PromotionOutcome = "rejected"
)
```

A closed two-value vocabulary. An unrecognized persisted value is corruption
(`ErrStoreCorrupt`), never coerced — the same rule `EnvironmentStatus` follows,
for the same reason: a row saying `"approved"` means something wrote it that
this code did not, and guessing which of two states it meant is how a rejected
decision silently reads as accepted.

### `EnvironmentPosition`

```go
// EnvironmentPosition is one environment as it stood when the decision was
// made. A snapshot, not a reference: rank, and the revision that produced it,
// are configuration a later operator may change.
type EnvironmentPosition struct {
    Ref      EnvironmentRef
    Rank     uint16
    Revision uint64
}
```

Exported fields, matching `MinimumCountGate` and `MaximumCountGate` — a plain
value snapshot nested inside an unexported-field domain type, produced only by
this package.

Three fields and no more. There is no `Name` (a label humans read, which
changes without changing anything the decision rested on), no `Status` (a
`Promotion` exists only if both environments were active, so the record's
existence *is* the status assertion), and no `ProjectID` (both environments
belong to the promotion's project, which the record already carries once).

### Construction

```go
// PromotionDecision is the complete input to NewPromotion.
type PromotionDecision struct {
    ID PromotionID

    CandidateID    CandidateID
    ReferenceRunID EvaluationRunID
    CandidateRunID EvaluationRunID

    // Source and Target are the loaded environments, not snapshots. The
    // constructor takes the values so it can ask CanPromote itself and derive
    // the snapshot, rather than trusting a caller-assembled position.
    Source Environment
    Target Environment

    Limits    EvaluationGateLimits
    Outcome   PromotionOutcome
    DecidedAt time.Time
}

func NewPromotion(d PromotionDecision) (Promotion, error)
```

The constructor:

1. validates every identifier by `validateID`;
2. requires `DecidedAt` to be non-zero (`ErrInvalidTimestamp`);
3. requires `Outcome` to be one of the two constants (`ErrInvalidID`-class
   validation on a closed vocabulary — see below);
4. requires `ReferenceRunID != CandidateRunID`;
5. **calls `CanPromote(d.Source, d.Target)`** and refuses the construction if
   it is false (`ErrPromotionOrder`);
6. derives `projectID` from `d.Source.ProjectID()` — guaranteed equal to the
   target's, because `CanPromote` already required it;
7. snapshots `Source`/`Target` into `EnvironmentPosition` values, taking rank
   from `Rank()` (ranked is guaranteed, again by `CanPromote`).

Point 5 is the important one. The domain enforces its own ordering invariant,
and it does so by calling **the one ordering primitive**, not by comparing two
integers. `CanPromote` remains the only rank comparison in the repository, as
task 065's absence test requires.

Point 6 removes a forgeable input: the project is derived, never supplied.

`NewPromotion` takes no gate *result*. It takes the outcome the workflow
reached. See [Gate evidence](#gate-evidence-what-is-stored-and-what-is-recomputed).

### Restoring a stored promotion

```go
func restorePromotion(/* stored scalars */) (Promotion, error)
```

Shared by both backends so neither can accept a row the other would refuse,
exactly as `restoreEnvironment` is. It applies every check a live value faced,
including the ordering one: it rebuilds two `Environment` values from the
stored positions through `restoreEnvironment` — active, ranked, in the row's
project, named after their own refs the way migration names a backfilled
environment — and asks `CanPromote`. A row whose ranks were inverted by damage
therefore fails closed with `ErrStoreCorrupt`, and the repository still
contains exactly one rank comparison.

### What is deliberately not on a `Promotion`

| Field | Why not |
|---|---|
| `AgentID` | Derivable, through immutable edges only: candidate run → `Candidate.AgentID`. A stored copy could only ever agree, and a field that can only agree is a field that can drift |
| `Status`, `State`, `Phase` | A promotion has no lifecycle. It is decided or it does not exist |
| `ApprovedBy`, `Actor`, `RequestedBy` | Nobody is authenticated. A name here would be an unverified string presented as provenance |
| `EvaluationScorecard`, `BehaviorDiff` | Derived from immutable evidence plus the stored limits, and recomputable bit-for-bit. ADR 0030 keeps one source of truth |
| `EvaluationGateResult` | Same — see [Gate evidence](#gate-evidence-what-is-stored-and-what-is-recomputed) |
| `Reason`, `Notes`, `Message` | Free text is not evidence, and a closed outcome vocabulary is what makes a history machine-readable |
| `Metadata map[string]…` | Unbounded row shape. Every platform value in this repository is fixed-shape |
| environment `Name` | A human label that changes without changing the decision |
| deployment anything | See [Non-Goals](#non-goals) |

## Decision Semantics

### The complete rule

A promotion **decision is reached** if and only if every structural
precondition holds:

```text
1.  the promotion identifier is well formed and unused
2.  both runs exist
3.  both runs are RunCompleted
4.  the two runs are not the same run
5.  both runs resolve to the same Project
6.  both runs resolve to the same Agent
7.  both runs record the same EnvironmentRef              → this is the source
8.  the source Environment exists in that project
9.  the target Environment exists in that project
10. CanPromote(source, target) is true
        ├─ same project
        ├─ both active
        ├─ both ranked
        └─ target.rank > source.rank
11. the comparison is computable from the stored evidence
```

Given all eleven, the **outcome** is:

```text
gate verdict PASS  →  accepted
gate verdict FAIL  →  rejected
```

Fail closed, throughout. No condition compensates for another; there is no
score, no weighting, no override, no "mostly passed". A missing condition is
not a weak promotion — it is not a promotion at all.

### Gate PASS is necessary, never sufficient

Task 056 says a PASS means exactly "all five configured hard gates passed",
and explicitly not safe, approved, promotable or deployed. Promotion adds
conditions 1–10 on top of it and still calls the result a *decision* rather
than a blessing. A PASS between two runs of different agents, or toward an
archived environment, or backwards down the ranks, produces no promotion at
all.

### Which conditions produce a record, and which produce an error

This is the model decision, stated once:

> **A `Promotion` row exists if and only if the workflow reached a verdict.**

- conditions 1–11 hold → a record, with outcome `accepted` or `rejected`
- any of 1–11 fails → an **error**, and **no record**

Both branches of the verdict are persisted. A structural failure is not.

**Why both verdicts are recorded.** The roadmap's release criterion is "a
promotion decision is recorded together with the evidence it rested on", and a
history containing only the advances that happened is not a history of
decisions. More concretely, a rejection is the one outcome that is *not*
otherwise recoverable: the runs are immutable and the comparison is
recomputable, but the **limits** that produced the FAIL are caller-owned and
stored nowhere else. Discard the rejected attempt and "we tried to ship this
under these limits and it did not pass" becomes unanswerable — which is
precisely the question an operator asks when a candidate did not advance.

**Why structural failures are not recorded.** A request naming two agents'
runs, or an archived target, or a run that never completed, is not a decision
the platform made — it is a request the platform could not evaluate. Recording
it would either force a third outcome value (`invalid`, `refused`) whose
meaning overlaps rejection, or quietly widen `rejected` to mean two different
things, and a consumer reading a history could no longer tell "the evidence
failed the limits" from "somebody sent a malformed request". Keeping the
vocabulary at two values keeps `rejected` meaning exactly one thing.

**The alternative considered and rejected.** Persisting only accepted
promotions is smaller and would satisfy a reading of the roadmap that treats
"promotion" as "advance". It was rejected on the limits-are-lost argument
above: under it, the single most common audit question — *why did this not
ship* — is answerable only if somebody wrote the limits down somewhere the
platform does not own.

**`rejected` is not an incident.** Nothing alerts on it, nothing escalates,
no field is named `violation` or `failure`, and the HTTP status is `201`
because the platform did successfully record what it was asked to decide. A
rejected promotion is a normal, expected, frequently correct outcome of
running a gate.

### The same-agent invariant

```text
Candidate(reference run).AgentID == Candidate(candidate run).AgentID
```

Required. The candidate **being promoted** is the candidate run's
`CandidateID`.

`ControlPlane.CompareEvaluations` requires the same *project* and deliberately
not the same agent — task 065's `requireSameProject` doc says so in as many
words, and closes with "a promotion workflow that needs agent identity adds
that narrower precondition alongside itself." This task is that workflow, and
this is that precondition.

Why promotion needs it when comparison does not: a comparison between two
agents in one project is unusual but coherent — both runs are in the same
project's same environment and the scorecard means what it says. A *promotion*
asserts that one candidate's evidence justifies that candidate advancing, and
evidence drawn from a different agent justifies nothing about this one. One
agent's clean run must not be able to carry another agent's candidate forward.

**`CompareEvaluations` is not tightened.** Its contract is released, its
callers include `POST /v1/evaluations/compare` and `trustvian eval compare`,
and narrowing it would change behaviour for existing callers with no
requirement behind it. The narrower rule lives in the promotion workflow and
is checked there.

### The source environment is inferred, never supplied

The caller supplies the target. It does **not** supply the source.

It cannot usefully: both runs already carry an `EnvironmentRef`,
`CompareBehaviorSnapshots` already refuses two snapshots whose environments
differ, and the project is already derived from the runs. So
`(project, shared ref)` is unambiguously bound into the evidence, and asking
the caller to restate it would add an input whose only possible effect is to
disagree with the evidence.

The workflow therefore reads the ref from both runs, requires them equal
(`ErrPromotionScope` if not), and resolves the source `Environment` from
`(derived project, that ref)`. A caller that sends a `source_environment`
field gets a `400`, because the request decoder rejects unknown fields.

Requiring equality here duplicates a check `CompareBehaviorSnapshots` performs
on the evidence. That is intentional and not redundant: the promotion workflow
needs the shared ref as an *input* — it cannot resolve the source environment
without it — and reading it before any evidence is loaded is what lets an
impossible pairing be refused cheaply. The evidence-level check remains where
it is and is not weakened.

### The target environment

Supplied by the caller as an `EnvironmentRef`, resolved inside the project
derived from the runs. A ref that exists only in a *different* project is
`ErrStoreNotFound` — it does not exist in this project, and identity is the
pair.

### Ordering: `CanPromote` and nothing else

Condition 10 is one call. No layer re-derives it:

- the domain constructor calls `CanPromote`;
- the control plane calls `CanPromote`;
- the HTTP adapter calls neither — it calls the control plane;
- the CLI compares no ranks;
- the WebUI compares no ranks in JavaScript.

Task 065's absence test — no function other than `CanPromote` compares two
ranks — is extended to cover this task's new files, and the WebUI and CLI
architecture tests gain the same assertion.

### Stage skipping is permitted

```text
dev 10 → sandbox 20 → staging 30 → production 40
```

`dev → production` is a valid promotion.

The rule is exactly: **any active, ranked target for which
`CanPromote(source, target)` is true.** No adjacency, no "next stage", no
predecessor pointer, no transition graph.

Why. There is no approved adjacency relation in this repository, and inventing
one would require defining, with no consumer to define it against:

| Case | An adjacency rule would have to answer |
|---|---|
| two environments share a rank | which of the peers is "next", or whether neither is |
| an intermediate environment is archived | whether the gap closes, or whether promotion is blocked until someone re-activates it |
| an intermediate environment is unranked | whether it is skipped silently or counts as a stage |
| ranks are 10, 20, 40 | whether the missing 30 is a gap or just a numbering choice |
| an operator re-ranks mid-flight | whether a promotion valid a moment ago now is not |

[ADR 0039](../../adr/0039-environments-are-project-owned-ranked-references.md)
already settled the shape of the answer: a forward jump *is* ordered, and if a
workflow decides promotions may not skip a stage, "it implements that from
these same ranks as its own policy, without a second ordering model." Nothing
in the roadmap asks for that policy. This task therefore permits the jump and
adds no policy layer; a later milestone with a stated requirement can add one
over the same ranks without redesigning anything here.

Names never enter it. `prod-eu` and `production` are unrelated strings, and
deriving order from them is the mistake ADR 0039 §3 rejected.

## Gate Evidence: What Is Stored and What Is Recomputed

The governing rule, and the one that decides every field:

> **Snapshot what is mutable or otherwise unrecoverable. Reference what is
> immutable. Recompute what is derived.**

### Recoverable, therefore referenced

| Evidence | Why a reference is enough |
|---|---|
| reference run, candidate run | `EvaluationRun` is immutable once completed, and the store has no delete |
| the run's aggregate and behavior snapshot | Written during ingest, and ingest is refused on a run that is not running — so a completed run's evidence never changes again |
| candidate, agent, project | Caller-owned identity, immutable, never deleted |

### Unrecoverable or mutable, therefore snapshotted

| Evidence | Why a reference is not enough |
|---|---|
| the three gate limits | Caller-owned and stored nowhere. Lose them and the verdict is unexplainable and unreproducible |
| source and target `rank` | Configuration an operator changes. ADR 0039 §6 already says promotion-time ordering belongs on the promotion record; this is that |
| source and target `revision` | Makes "the configuration has moved since this decision" detectable today, by comparing against the environment's current revision |
| `outcome` | The decision itself — the one fact the platform is being asked to remember |
| `decided_at` | When. Not identity; see above |

### Derived, therefore recomputed

The `BehaviorDiff`, the `EvaluationScorecard` and the `EvaluationGateResult`
are **not stored**.

All three are deterministic functions of immutable stored evidence and the
stored limits. Task 056 built the gate on integer counts precisely so the
result is bit-identical under record reordering, and `EvaluationComparison`
already documents itself as "not persisted … ADR 0030 keeps one source of
truth by recomputing rather than storing a second copy that can disagree."
Storing a gate result on the promotion would be exactly that second copy.

**How a promotion is explained, then.** The record carries both run IDs and
all three limits, which is the complete input to the existing route:

```text
POST /v1/evaluations/compare
  { "reference_run_id": <promotion.reference_run_id>,
    "candidate_run_id": <promotion.candidate_run_id>,
    "gate_limits":      <promotion.gate_limits> }
```

That returns the diff, the scorecard and the full five-check gate result, and
it is guaranteed to reproduce the verdict the promotion recorded, because
every input is either immutable or stored on the record. One implementation of
the explanation, reachable from the decision, with nothing duplicated.

**The alternative considered and rejected.** Snapshotting a fixed-shape
`EvaluationGateResult` onto the row (five checks plus verdict, ~16 more
columns) would make `GET /v1/promotions/{id}` self-contained. It was rejected
because every one of those columns is recomputable from data the row already
names, and a duplicated derivation is a derivation that can disagree with its
source after a bug fix in the gate — at which point the platform holds two
answers and no rule for which is right. The cost of the decision is one extra
documented request when a consumer wants the full breakdown.

## Control-Plane Service

One authoritative operation. Adapters call it and translate; they load no
runs, compare no evaluations, evaluate no gates, load no environments, compare
no ranks and write no rows (ADR 0031, ADR 0023).

```go
// PromotionRequest is the fixed-shape input to Promote.
//
// Everything the server can derive is absent by construction: no project,
// agent or candidate identity, no source environment, no ranks, no verdict,
// no timestamp.
type PromotionRequest struct {
    ID PromotionID

    ReferenceRunID EvaluationRunID
    CandidateRunID EvaluationRunID

    TargetEnvironment EnvironmentRef

    GateLimits EvaluationGateLimits
}

func (c *ControlPlane) Promote(
    ctx context.Context, request PromotionRequest, at time.Time,
) (Promotion, error)

func (c *ControlPlane) Promotion(
    ctx context.Context, id PromotionID,
) (Promotion, error)

func (c *ControlPlane) ProjectPromotions(
    ctx context.Context, projectID ProjectID, after PromotionID, limit int,
) ([]Promotion, error)
```

`at` is supplied by the adapter, matching `StartEvaluationRun` and every other
lifecycle operation — the clock is the composition root's, and
`httpapi.WithClock` already exists so tests are not time-dependent.

### Orchestration order

Ordered so that **every structural precondition is checked before any
evidence is read**, and so that an impossible promotion costs a handful of
primary-key lookups rather than the evidence reads and three derivations a
comparison costs.

```text
 1  validate request shape                       no reads
      ID, both run IDs, target ref by validateID
      reference run ID != candidate run ID

 2  load candidate run, require RunCompleted     1 read
 3  load reference run, require RunCompleted     1 read

 4  resolve candidate run → candidate → agent    2 reads
 5  resolve reference run → candidate → agent    2 reads

 6  require same project                         no reads   ErrComparisonScope
 7  require same agent                           no reads   ErrPromotionScope
 8  require same EnvironmentRef → source ref     no reads   ErrPromotionScope

 9  load source environment (project, ref)       1 read
10  load target environment (project, target)    1 read
11  require CanPromote(source, target)           no reads   ErrPromotionOrder

12  CompareEvaluations(reference, candidate, limits)
        → diff, scorecard, gate                  evidence reads + derivations

13  outcome = accepted if gate.Verdict() == GateVerdictPass else rejected
14  NewPromotion(…)                              no reads
15  control.CreatePromotion(promotion)           1 write
16  return the promotion
```

Notes on three steps that look like choices and are:

**Step 12 reuses `CompareEvaluations` whole**, including its own repeat of
`completedRun` and `requireSameProject`. That redundancy — two extra run reads
and the four identity reads behind `requireSameProject` — is accepted
deliberately. The alternative is an
internal comparison variant that skips the checks, which creates a code path
where the checks *can* be skipped; a future edit routing another caller through
it would lose them silently. One implementation of the comparison chain, with
its preconditions attached, is worth six primary-key reads.

**Step 15 is the only write**, and it happens after every check and after the
verdict. A structural failure writes nothing. A rejected verdict writes a
record, because a verdict was reached.

**Step 1 validates the promotion ID before anything is loaded**, so a
malformed identifier is a `400` that touched no storage.

### Errors

Two new sentinels. Both are conditions a caller must be able to distinguish
and neither is accurately described by an existing one.

```go
// ErrPromotionScope reports two runs that are not a promotion-eligible pair:
// they belong to different agents, or they record different environments.
//
// Not ErrComparisonScope, which is specifically "different projects" and is
// still returned for that. Not a state conflict: the pairing itself can never
// succeed, so the caller must change the request rather than retry it.
var ErrPromotionScope = errors.New(
    "platform: runs do not describe one agent evaluated in one environment")

// ErrPromotionOrder reports a source/target pair CanPromote refuses.
//
// Covers every case the relation defines: an archived environment on either
// side, an unranked one on either side, equal ranks, a backward move, the same
// environment twice, and two projects. The configuration may legitimately
// change, so this is a conflict rather than a permanently invalid request.
var ErrPromotionOrder = errors.New(
    "platform: target environment is not forward of the source")
```

Everything else reuses an existing sentinel with its existing meaning:

| Condition | Error |
|---|---|
| malformed promotion ID, run ID or target ref | `ErrInvalidID` |
| promotion ID already used | `ErrStoreAlreadyExists` |
| run, environment, candidate, agent or project missing | `ErrStoreNotFound` |
| a run is pending, running, failed or cancelled | `ErrEvaluationState` |
| the two runs belong to different projects | `ErrComparisonScope` |
| behavioral evidence saturated | `ErrIncompleteSnapshot` |
| a stored row cannot be restored | `ErrStoreCorrupt` |
| gate verdict FAIL | **no error** — outcome `rejected` |

No new error *code* string reaches `/v1`. See
[Status and error mapping](#status-and-error-mapping).

## Persistence

### Capability placement: extend `ControlStore`

Three methods, and no more than task 066 needs:

```go
// CreatePromotion stores one decision. A promotion is immutable, so this is
// the only write: there is no update and no delete at any layer.
//
// A promotion whose ID already exists is ErrStoreAlreadyExists, whatever the
// rest of the request says — the identifier names a decision that was already
// recorded.
CreatePromotion(ctx context.Context, promotion Promotion) error

// Promotion loads one by identifier. Identity is global, like a project,
// agent, candidate or run identifier and unlike an environment ref.
Promotion(ctx context.Context, id PromotionID) (Promotion, error)

// ProjectPromotions returns at most limit of one project's promotions, in
// identifier byte order, whose ID sorts after `after`. An empty `after`
// starts at the beginning.
//
// limit is 1 to MaxPromotionPage inclusive; anything outside that is
// ErrInvalidID. A project with no promotions returns an empty slice; one that
// does not exist returns ErrStoreNotFound.
ProjectPromotions(
    ctx context.Context, projectID ProjectID, after PromotionID, limit int,
) ([]Promotion, error)
```

**`ControlStore`, not `EvaluationStore`, and not a new interface.** A promotion
is project-scoped control-plane state that names environments — and environment
identity is `(ProjectID, EnvironmentRef)`, which `ControlStore` already owns. A
speculative `PromotionStore` would be an interface with one implementation pair
and one consumer, which is the abstraction CLAUDE.md says not to build ahead of
need. No `Database`, no repository, no generic query capability.

**No update method.** Immutability enforced by the absence of a mechanism is
stronger than immutability enforced by a check, and there is nothing to change.

**No delete method.** A hard delete of a historical decision is not a feature
this product has; task 065 made the same call for environments, for the same
reason.

Compile-time assertions for both backends, as task 064 established.

### Schema: version 4, both dialects

`SchemaVersion` moves **3 → 4**. One new table, `platform_promotions`, and one
index. Nothing existing changes shape.

```sql
CREATE TABLE platform_promotions (
    id                             TEXT PRIMARY KEY,
    project_id                     TEXT NOT NULL REFERENCES platform_projects(id),
    candidate_id                   TEXT NOT NULL REFERENCES platform_candidates(id),

    reference_run_id               TEXT NOT NULL,
    candidate_run_id               TEXT NOT NULL,

    source_environment_ref         TEXT NOT NULL,
    source_environment_rank        INTEGER NOT NULL,
    source_environment_revision    TEXT NOT NULL,

    target_environment_ref         TEXT NOT NULL,
    target_environment_rank        INTEGER NOT NULL,
    target_environment_revision    TEXT NOT NULL,

    max_added_behaviors            TEXT NOT NULL,
    max_block_decisions            TEXT NOT NULL,
    max_critical_risk_observations TEXT NOT NULL,

    outcome                        TEXT NOT NULL,
    decided_at                     TEXT NOT NULL
);

CREATE INDEX platform_promotions_by_project
    ON platform_promotions (project_id, id);
```

Sixteen columns, every one a bounded scalar. No JSON column, no blob, no
array, no nullable field.

**PostgreSQL** adds `COLLATE "C"` to `id`, `project_id`, `candidate_id`,
`reference_run_id`, `candidate_run_id` and both environment refs, for the same
reason task 065 did: the collection paginates on `id` in byte order, and a
locale-aware collation would order two backends' pages differently and could
place a row on a page a cursor had already passed.

**`uint64` as `TEXT`** for the three limits and the two revisions, matching
every other `uint64` in this schema — the full unsigned range survives exactly,
where a native integer type would not. **`uint16` rank as `INTEGER`**, matching
`platform_environments`. **Timestamps as `TEXT`** in `RFC3339Nano`, through the
existing `timeText`/`parseTimeText` pair, so a zone offset and nanosecond
precision survive.

**Foreign keys, and deliberately only two.** `project_id` and `candidate_id`
point at `ControlStore` entities in the same capability, and neither is ever
deleted, so the constraint costs nothing and documents the scope.

`reference_run_id` and `candidate_run_id` carry **no foreign key**. Two
reasons, both precedent:

- they name `EvaluationStore` entities, and the interface split exists because
  "a later backend may reasonably implement one and not the other" — a
  promotions table in `ControlStore` that could not be created without the run
  table would quietly couple the two capabilities;
- task 065 refused exactly this constraint in the other direction (no foreign
  key from runs to environments) because a constraint there would make a
  historical record unloadable if the referenced row ever became unreachable.
  A historical decision that stops loading is worse than a dangling reference
  it can report.

**One index, justified.** Task 065's schema note says no index beyond the
primary keys, and explains why: no query searched by a non-key column. This
task adds one that does — `WHERE project_id = ? AND id > ? ORDER BY id LIMIT ?`
— and `(project_id, id)` is exactly that range scan in its own order. The
promotions primary key is `id` alone because promotion identity is global, so
unlike `platform_environments` the scan is not free from the primary key. No
index on `candidate_id`, `decided_at` or `outcome`: no route queries them.

**Not added, and asserted absent:** no promotion table column, and no table at
all, for a deployment target, endpoint, credential, secret, token, approver,
scorecard, gate result, diff, decision record or raw event. `schemaTables`
gains exactly one name.

### Migration v3 → v4, on both backends

Create the table, create the index, stamp 4. **No backfill.**

A schema-3 database recorded no promotion decisions, because no promotion
decision could be made. Synthesizing one from historical evaluation runs would
fabricate an audit record — inventing a decision, a set of limits nobody chose
and a moment nobody decided anything — which is the one thing an audit history
must never contain. An existing database therefore migrates to an empty
promotion history, which is the accurate answer.

The migration chain becomes v1 → v2 → v3 → v4, one step at a time, exactly as
task 065 extended v1 → v2 → v3. A v4 database opened by a v3 binary is refused,
as every version step here is.

### Semantics, identical on both backends

Verified by the shared conformance suite and the SQLite/PostgreSQL
differential suite:

| | |
|---|---|
| create, then read | returns a value equal in every field, including nanosecond `decided_at` and `MaxUint64` limits |
| duplicate identifier | `ErrStoreAlreadyExists`, whatever the rest of the promotion says |
| missing project or candidate | `ErrStoreNotFound` |
| unknown `outcome` in a row | `ErrStoreCorrupt` |
| inverted ranks in a row | `ErrStoreCorrupt` |
| unparseable `decided_at`, rank or limit | `ErrStoreCorrupt` |
| collection ordering | `id` byte-ascending |
| collection scope | one project; another project's promotions never appear |
| restart | every promotion still loads, byte-identical |

## Concurrency and Retry Semantics

Promotion creation takes **no lock**, and the contrast with task 065 is worth
stating because it looks inconsistent and is not.

The environment cap is a *cross-row* invariant — "this project has fewer than
64 environments" is a statement about rows other than the one being written —
so no predicate on the inserted row can express it, and creation had to
serialize on the owning project row. Promotion has no cross-row invariant. Every
rule it enforces is either a property of the request, a property of rows it
only reads, or a property of the single row it writes. A primary key is
sufficient, and a lock would serialize writes for nothing.

| Race | Outcome |
|---|---|
| two requests, same `PromotionID` | The primary key decides. Exactly one commits; the other is `ErrStoreAlreadyExists` → `409 already_exists`. No partial write exists, because the row is written once and never updated |
| two requests, different IDs, same `(candidate, source, target)` | **Both succeed.** Two decisions were made and two are recorded |
| a concurrent re-rank or archive of either environment | The workflow read each environment once and snapshotted rank and revision. The decision describes a configuration that existed; a later reader detects the move by comparing the stored revision against the environment's current one. No lock, no retry, no invalidation |
| a concurrent `CreateEvaluationRun` or ingest | Cannot affect the decision: both referenced runs are already completed, and ingest is refused on a run that is not running |

**No uniqueness constraint on `(candidate_id, source_ref, target_ref)`.** That
tuple is legitimately repeatable — see
[Why an identifier exists](#why-an-identifier-exists-and-why-candidate-source-target-is-not-one)
— and adding the constraint would make the database enforce a business rule
the domain has not specified and the product has not asked for.

### Duplicates and retries

A caller-owned `PromotionID` makes a retry unambiguous, and the recovery path
is the one this repository already uses.

- A retried `POST` whose first attempt committed gets `409 already_exists`.
- The client then issues `GET /v1/promotions/{id}` and reads what was
  recorded.

There is **no byte-identical idempotent replay**, and no idempotency
subsystem. Task 073's Collector ingest established the pattern in this
repository — a lost response is reconciled by reading authoritative state
back, not by assuming what it said — and a promotion is a single immutable row,
so reading it back is complete and unambiguous. Comparing a stored row against
a resubmitted body to decide whether to replay or conflict would add a second
notion of request equality with nothing to gain.

## HTTP API

Three routes, following every existing `/v1` convention: DTOs own the JSON,
domain values carry no tags, `POST` creates, identifiers are path parameters,
and the response envelope carries `"version": "1"`.

```text
POST   /v1/promotions                          201
GET    /v1/promotions/{promotion_id}           200
GET    /v1/projects/{project_id}/promotions    200
```

`POST` is flat with no scope in the path, matching `POST /v1/agents` and
`POST /v1/environments` — the scope is derived, not routed. `GET` by
identifier is flat because promotion identity is global, matching
`/v1/agents/{agent_id}` and unlike the project-scoped environment routes.

### Request

```jsonc
// POST /v1/promotions
{
  "id": "promo-2026-03-01-a",
  "reference_run_id": "run-baseline-7",
  "candidate_run_id": "run-candidate-9",
  "target_environment": "production",
  "gate_limits": {
    "max_added_behaviors":            "3",
    "max_block_decisions":            "0",
    "max_critical_risk_observations": "0"
  }
}
```

`uint64` limits are strings, matching `gateLimitsDTO` and the repository's
rule that a `uint64` crosses `/v1` as a decimal string. All three are
**required** and pointer-typed in the DTO so that omitted is distinguishable
from `"0"`: zero is the strictest expressible limit, and defaulting to it
would fail a caller's promotion under a policy they never chose. A missing
limit is `400`.

**What the request may not contain.** Request bodies are strictly decoded —
`DisallowUnknownFields`, as every `/v1` route already is — so a field the
server derives is not merely ignored, it is a `400`:

```text
project_id   agent_id   candidate_id        derived from the runs
source_environment  source_rank  target_rank  derived from the runs and the registry
gate_verdict   outcome   decided_at           computed by the server
```

That is a security property, not a style choice. A caller cannot express
`"outcome": "accepted"` or `"source_rank": 0`, so no adapter bug can forward
one.

### Response

Identical shape from `POST` and `GET`: the stored record, nothing derived.

```jsonc
{
  "version": "1",
  "id": "promo-2026-03-01-a",
  "project_id": "proj-1",
  "candidate_id": "cand-9",
  "reference_run_id": "run-baseline-7",
  "candidate_run_id": "run-candidate-9",
  "source_environment": { "ref": "staging",    "rank": 30, "revision": "7" },
  "target_environment": { "ref": "production", "rank": 40, "revision": "3" },
  "gate_limits": {
    "max_added_behaviors":            "3",
    "max_block_decisions":            "0",
    "max_critical_risk_observations": "0"
  },
  "outcome": "rejected",
  "decided_at": "2026-03-01T09:14:22.481Z"
}
```

`rank` is a number (a `uint16`, like the environment response). `revision` and
the limits are strings (`uint64`). `outcome` is one of exactly two values.

A consumer wanting the five-check breakdown posts the record's own
`reference_run_id`, `candidate_run_id` and `gate_limits` to
`/v1/evaluations/compare`, which is guaranteed to reproduce the verdict — see
[Gate evidence](#gate-evidence-what-is-stored-and-what-is-recomputed).

### Collection

```text
GET /v1/projects/{project_id}/promotions?limit=&after=
```

```jsonc
{
  "version": "1",
  "project_id": "proj-1",
  "promotions": [ … ],          // id-ascending, at most `limit` entries
  "next_after": "promo-2026-03-01-a"   // omitted on the last page
}
```

Semantics are **identical to the environment collection**, deliberately — one
pagination shape in this API, not two:

| | |
|---|---|
| scope | one project, named in the path |
| sort key | `id`, byte-ascending |
| cursor | `after`, exclusive, a `PromotionID` validated by the same rules as any identifier |
| limit | `1..64`, defaulting to 64; a larger value is `400`, never a silent clamp |
| `next_after` | present exactly when the page filled its limit and another row follows |
| continuation detection | a short page is the end; a full page is resolved by a second bounded call with `limit=1` after the page's last id — never by fetching `limit+1` |
| concurrent insert | a promotion created during a traversal appears if and only if its id sorts after the caller's position. Nothing already returned moves, because `id` is immutable |

`MaxPromotionPage = 64`, exported for the same reason `MaxEnvironmentPage` is:
the HTTP layer must reject the number the store enforces, and two copies of a
limit is how two layers come to disagree.

#### Why the cursor is the identifier and not the timestamp

`decided_at` would be the useful audit order, and it is the wrong cursor.

Timestamps are stored as `RFC3339Nano` text, which is **not lexically
ordered**: Go trims trailing fractional zeros, so `…00.1Z` sorts before
`…00.05Z`, and a non-UTC offset breaks it again. A cursor over that column
would silently skip and repeat rows. Fixing it means either a second timestamp
encoding in one schema — two formats for one concept, which is exactly how two
backends drift — or a server-assigned monotonic sequence, which is new
identity-adjacent state.

`id` is immutable, unique, already validated, and byte-ordered on both
backends by `COLLATE "C"`. It gives an **exact** traversal, which is the only
property a keyset cursor must have.

What this costs: the collection is ordered by identity, not by time. Every row
carries `decided_at`, so a consumer can present a page chronologically. A
consumer that needs newest-first across an unbounded history is asking for a
time-ordered history capability, which is
[task 067](README.md)'s subject — the event-history capability boundary —
and this task does not pre-empt its cursor design. Promotion history is not
blocked in the meantime: it is enumerable, exactly, in full.

### Status and error mapping

| Condition | Status | Code |
|---|---|---|
| promotion recorded — **`accepted` or `rejected`** | 201 | — |
| read, list | 200 | — |
| malformed id, run id, target ref, or missing/invalid gate limit | 400 | `invalid_request` |
| unknown request field | 400 | `invalid_request` |
| runs in different projects (`ErrComparisonScope`) | 400 | `invalid_request` |
| runs in different agents or environments (`ErrPromotionScope`) | 400 | `invalid_request` |
| run, environment, candidate or project not found | 404 | `not_found` |
| promotion identifier already used | 409 | `already_exists` |
| a run is not completed (`ErrEvaluationState`) | 409 | `conflict` |
| target not forward of source (`ErrPromotionOrder`) | 409 | `conflict` |
| behavioral evidence saturated (`ErrIncompleteSnapshot`) | 409 | `incomplete_evidence` |
| body over 256 KiB | 413 | `payload_too_large` |
| storage damage | 500 | `internal` |

**No new error code string.** `ErrPromotionScope` maps onto
`invalid_request` because the pairing can never succeed whatever the state
becomes; `ErrPromotionOrder` maps onto `conflict` because the configuration
may legitimately change — the same distinction `ErrComparisonScope` and
`ErrEnvironmentUnavailable` already draw.

**A gate FAIL is `201`, never `409` and never `500`.** The platform was asked
to decide and it decided. Reporting evidence-backed rejection as a server
failure would tell an operator their platform is broken when it is working
exactly as configured, and reporting it as a conflict would suggest retrying
something that will deterministically produce the same answer.

### Bounds

The existing 256 KiB `MaxBytesReader`, `Content-Type` check and envelope
version check apply unchanged. A promotion request has six scalar fields and a
response has sixteen, all bounded; no route accumulates anything in memory
beyond one bounded page.

## CLI

A noun family with verb subcommands, matching `project`, `agent`, `candidate`,
`env` and `eval`:

```text
trustvian promotion create --reference-run <id> --candidate-run <id>
                           --target-environment <ref> --id <id>
                           --max-added-behaviors <n>
                           --max-block-decisions <n>
                           --max-critical-risk-observations <n>
                           [--api-url <url>] [--json]
trustvian promotion get    --id <id> [--api-url <url>] [--json]
trustvian promotion list   --project-id <id> [--api-url <url>] [--json]
```

Not `trustvian promote`. This repository has no verb-family precedent — `eval
compare` shows that verbs live as subcommands of a noun — and a promotion is a
thing that can be listed and fetched, which a verb family has no room for.

All three limits are **required**, with the same refusal `eval compare` uses:
`--max-block-decisions is required; zero is a strict limit, not a default`.

The CLI remains a thin HTTP adapter (ADR 0033). It imports nothing from
`trustvian-platform`, compares no ranks, evaluates no gate, and derives no
project, agent or candidate. `promotion list` follows `next_after` to
completion with no page ceiling, bounded by cursor progress, exactly as
`env list` does — and `promotion list --json` synthesizes one completed
collection document under the same stated exception, preserving unknown fields
at both the row and envelope level.

### Exit codes, and why a rejection is `0`

| Situation | Exit |
|---|---|
| promotion recorded, outcome `accepted` | `0` |
| promotion recorded, outcome `rejected` | **`0`** |
| missing flag, bad flag, trailing argument | `2` |
| `4xx`, `5xx`, transport failure, unreadable body | `3` |

**Code `1` is not extended to this family.** `docs/compatibility.md` states
that `1` means gate FAIL **for `trustvian eval compare` only**, and that
sentence is an automation-facing contract. A rejected promotion is a
successfully recorded decision: the command did exactly what it was asked and
the platform now holds a durable record. Making it non-zero would conflate
"the decision was rejected" with "the request failed", which is the precise
confusion the family-scoped exit codes exist to prevent.

A pipeline that wants to stop on a failing gate already has the command for
it, and the ordering is the natural one anyway:

```bash
trustvian eval compare --reference-run "$REF" --candidate-run "$CAND" \
  --max-added-behaviors 3 --max-block-decisions 0 \
  --max-critical-risk-observations 0            # exit 1 on gate FAIL
trustvian promotion create --id "$ID" --reference-run "$REF" \
  --candidate-run "$CAND" --target-environment production \
  --max-added-behaviors 3 --max-block-decisions 0 \
  --max-critical-risk-observations 0            # exit 0, records the decision
```

A pipeline that wants to record the rejection *and* stop reads `outcome` from
`--json`. Extending code `1` later is a compatibility decision with its own
justification, not something this task should take by default.

Human output states the outcome plainly and factually, in task 063's gate
wording discipline — `Outcome: rejected`, never *unsafe*, *blocked*,
*violation*, *not production-ready*.

## WebUI

Task 063 reserved promotion for this milestone explicitly, including
forbidding a mock control, and the roadmap gives the WebUI ownership of
"environments, promotion and investigation". This task delivers the minimal
slice that makes that true, and no more.

**Added:**

1. **A project's environments, read-only.** A bounded list from
   `GET /v1/projects/{project_id}/environments`, following `next_after`,
   rendering `ref`, `name`, `rank` and `status` as text. It exists because a
   promotion needs a target chosen from the registry rather than typed.
2. **A promotion panel** on the existing run view: pick a target from that
   list, enter the three limits, `POST /v1/promotions`, render the server's
   response — `outcome`, both environment positions, the limits and
   `decided_at`.
3. **A project's promotion history**, paginated from
   `GET /v1/projects/{project_id}/promotions`, with a link from each row to
   the runs it names.

**Not added:** environment creation, renaming, re-ranking, archiving or
activating in the browser — task 065 placed environment configuration in the
CLI and this task does not move it. No promotion editing or deletion; there is
nothing to edit.

**Unchanged from ADR 0036 and task 063:** a static, same-origin, framework-free
`/v1` client under the existing strict CSP; server values rendered as text
only; an allowlisted field set; nothing stored in the browser. And
specifically:

- **no rank comparison in JavaScript** — the browser never decides which
  targets are promotable; it offers the environments the API returned and the
  server refuses the rest with `ErrPromotionOrder`;
- **no gate evaluation in JavaScript** — the verdict and the outcome come from
  the server, as task 063 already requires for gate verdicts;
- **no promotion decision logic in JavaScript** of any kind. The browser
  collects inputs, posts them, and renders an answer.

A rejected promotion renders as a recorded outcome, not an error state.

## TUI

Unchanged. No promote action, no promotion view, no new key binding.

Task 061 scoped the TUI to the live development loop and ADR 0034 states it
does not own administration. An environment is configured once and read often
and a promotion is decided once and read later; neither is what a live
dashboard is for. A test asserts the absence of a promotion command in the TUI.

## Realtime

**No realtime event is added.** `RealtimeEventKind` gains no value, and the
bus, the SSE route and the filters are untouched.

The question is whether an in-scope consumer requires one, and none does. The
TUI does not own promotion. The WebUI client that issues the `POST` receives
the authoritative result synchronously in the response — a live event would
tell it something it already knows. The CLI is request/response. No second
viewer is specified in this milestone.

Adding an event kind is an addition to a STABLE surface, and doing it without
a consumer freezes a payload shape nobody has validated against a real screen.
If a later milestone puts two operators in front of one project's history at
once, that milestone adds the event — publishing only after the durable
commit, over the existing bounded infrastructure, exactly as lifecycle events
already do.

## Security

| Property | How it is held |
|---|---|
| A caller cannot forge project, agent or candidate identity | None of the three is a request field. All are derived from the two run IDs through immutable edges, and an unknown field is a `400` |
| A caller cannot submit its own gate verdict | `outcome` and `gate_verdict` are not request fields. The verdict comes from `EvaluateEvaluationGate` over evidence the server loaded |
| A caller cannot submit authoritative ranks | `source_rank` and `target_rank` are not request fields. Ranks are read from the registry at decision time |
| A caller cannot name its own source environment | Inferred from the two runs; a `source_environment` field is a `400` |
| A gate FAIL cannot become `accepted` through an adapter bug | The outcome is computed in the control plane and written there. No adapter constructs a `Promotion`, and `NewPromotion` is the only constructor |
| Ordering is decided server-side | `CanPromote`, called in the control plane and again in the domain constructor. No adapter compares ranks; a test asserts it |
| Cross-agent evidence cannot promote another agent's candidate | The same-agent invariant, checked before any evidence is read |
| A historical record cannot be silently rewritten | No update method at any layer, no `UPDATE` statement, no `PUT`/`PATCH` route, no CLI verb. A corrupted row fails closed rather than being adopted |
| No credential, secret or deployment target enters the model | No such field exists on `Environment` (task 065) or `Promotion`, asserted by the same identifier-scan style task 065 uses |
| No outbound network call | Promotion operations reach storage only, asserted by a test that fails if an HTTP transport is dialled |
| No raw event or content retention | Nothing on the row is derived from event payloads. Behavioral evidence stays in the bounded aggregate and snapshot tables task 053/054 own |
| No platform type enters the core | Unchanged and re-asserted: the root module's dependency graph contains no platform package |

Unauthenticated access is unchanged and remains task 070's subject. Worth
stating plainly: with no authentication, anyone who can reach the API can
record a promotion decision, and that is true of every `/v1` write route
today. This task adds no new *class* of exposure, and it deliberately does not
add a fake actor label to paper over the gap — an unverified `requested_by`
string would look like provenance and be worth nothing.

## Bounds

Fixed-shape everywhere. No `map[string]any`, no arbitrary metadata, no
free-form evidence blob, no embedded `DecisionRecord` array, no raw events, no
prompts, no tool arguments. Identifiers use the existing 256-byte
`validateID`. The collection is bounded at 64 rows per page on both the store
and the route. The control plane accumulates nothing across requests.

## Tests

Every test below is written by the **implementation** PR. They run with
`GOWORK=off` in the platform module as CI does, and with `-race` for anything
touching concurrent creation. PostgreSQL tests stay gated on
`TRUSTVIAN_TEST_POSTGRES_DSN` exactly as task 064 left them, and the platform
CI job's "prove the PostgreSQL tests ran rather than skipped" step covers the
new ones automatically.

### Domain

- `NewPromotion` succeeds and every accessor returns what was supplied.
- Each identifier rejected independently: empty, 257 bytes, control character,
  invalid UTF-8, leading and trailing whitespace.
- `ReferenceRunID == CandidateRunID` is refused.
- Zero `DecidedAt` is refused.
- An outcome outside the two constants is refused; the vocabulary is closed.
- `projectID` is **derived**, not supplied: constructing with two environments
  of one project yields that project, and no input can override it.
- Ordering: `NewPromotion` refuses every pair `CanPromote` refuses — unranked
  source, unranked target, archived source, archived target, equal ranks,
  backward, same environment, different projects — and accepts a forward jump
  over an intermediate rank.
- A constructed `Promotion` has no method that returns a modified copy;
  asserted structurally, alongside the absence of any `Update`/`With`/`Set`.
- `EnvironmentPosition` carries exactly `Ref`, `Rank`, `Revision` — a field
  count assertion, so a later `Name` or `Endpoint` fails here first.
- `restorePromotion` round-trips a stored row; and fails closed with
  `ErrStoreCorrupt` on an unknown outcome, an unparseable timestamp, a rank
  above 9999, an unparseable `uint64`, and **inverted ranks**.

### Service

Valid path:

- A forward promotion with a PASS gate records `accepted` and returns it.
- A forward promotion with a FAIL gate records `rejected`, returns it, and
  returns **no error**.
- A forward jump over an intermediate rank is accepted (the skipping policy).
- Two promotions of the same `(candidate, source, target)` under different
  identifiers both succeed and both persist.

Refused, each producing **no stored row**:

- reference and candidate runs in different projects → `ErrComparisonScope`;
- same project, different agents → `ErrPromotionScope`;
- same project and agent, different environment refs → `ErrPromotionScope`;
- either run pending, running, failed or cancelled → `ErrEvaluationState`;
- reference run id equal to candidate run id → `ErrInvalidID`;
- target environment absent from the project → `ErrStoreNotFound`;
- target ref that exists only in another project → `ErrStoreNotFound`;
- source unranked, target unranked, source archived, target archived, equal
  ranks, backward target, target equal to source → `ErrPromotionOrder`;
- duplicate promotion identifier → `ErrStoreAlreadyExists`.

Ordering and cost:

- **No evidence is read when a structural precondition fails.** Proved with an
  `EvaluationStore` whose `EvaluationEvidence` fails the test if called — the
  tripwire pattern task 065 used for `CompareEvaluations` — exercised for the
  cross-agent, archived-target and backward-target cases.
- **No row is written when any precondition fails**, proved by a store that
  fails the test if `CreatePromotion` is called.

History:

- Re-ranking, renaming, archiving and re-activating either environment after a
  promotion leaves the stored promotion byte-identical, including its
  snapshotted ranks and revisions.
- The stored revision differs from the environment's current revision after a
  configuration change — the detectability the field exists for.
- A promotion's verdict is reproducible: feeding the stored run IDs and stored
  limits back through `CompareEvaluations` yields the same `GateVerdict` as
  the recorded outcome.
- `CompareEvaluations` is unchanged: a same-project, different-agent
  comparison still succeeds, asserted so this task cannot tighten it by
  accident.

### Store conformance, SQLite and PostgreSQL

Run through the existing shared conformance suite so both backends answer
identically, plus the differential suite:

- create then read, with nanosecond `decided_at`, `MaxUint64` limits, rank 0
  and rank 9999;
- duplicate identifier → `ErrStoreAlreadyExists`;
- missing project, missing candidate → `ErrStoreNotFound`;
- restart persistence: reopen the store and read every field back;
- immutability: the interface exposes no update, asserted structurally, and no
  `UPDATE` statement against the promotions table exists in either backend;
- corrupt rows fail closed, one column at a time;
- collection: ordering by `id` byte-ascending with a fixture whose identifiers
  order differently under a locale-aware collation, proving `COLLATE "C"`;
- collection: `limit` 64 accepted, 65 refused, 0 and −1 refused, page never
  larger than `limit`, exclusive cursor, empty project empty, missing project
  `ErrStoreNotFound`;
- collection: a project with 130 promotions enumerates completely with no
  duplicate and no omission, and a promotion created between pages appears if
  and only if its id sorts after the cursor;
- cross-project isolation: one project's promotions never appear in another's.

Concurrency:

- N concurrent creates of one identifier: exactly one succeeds, the rest are
  `ErrStoreAlreadyExists`, and the stored row equals the winner's.
- Concurrent creates of different identifiers in one project all succeed.
- Concurrent creates in **different** projects do not block each other, on
  PostgreSQL, proved by the occupancy-hook pattern task 065 used rather than
  by timing.

### Migration

On both backends:

- a v3 database migrates to v4, gains exactly the promotions table and its
  index, and stamps 4;
- **no promotion row exists afterwards**, whatever the database's evaluation
  history contains — asserted against a fixture with many completed runs
  across several environments;
- a v1 database reaches v4 through v2 and v3, and everything it held survives;
- a v4 database is verified rather than re-migrated on reopen;
- a v4 database is refused by a binary that understands only v3;
- promotions created after migration behave identically to those in a database
  created at v4.

### HTTP

- Each route: success shape, status code, envelope version.
- `201` for `accepted` **and** for `rejected`; the response body carries the
  outcome.
- Every server-derived field is refused in a request body — `project_id`,
  `agent_id`, `candidate_id`, `source_environment`, `source_rank`,
  `target_rank`, `gate_verdict`, `outcome`, `decided_at` — each a `400`.
- Each of the three gate limits missing → `400`; `"0"` accepted as a strict
  limit and echoed back as `"0"`.
- A non-numeric or out-of-range limit string → `400`.
- Every service error maps to its documented status and code, including
  `ErrPromotionScope` → `400 invalid_request` and `ErrPromotionOrder` →
  `409 conflict`.
- A malformed `promotion_id` path parameter is rejected before any store call,
  proved with a control plane over a store that fails if called.
- Duplicate create → `409 already_exists`, and the subsequent `GET` returns
  the first decision.
- Collection: `limit` default, `limit=64` returns at most 64 with no false
  continuation, `limit=65`/`0`/`abc` → `400`, malformed `after` → `400`, a
  cursor naming no row is a position rather than an error, 130 promotions
  enumerate over three pages.
- Body over the limit → `413`; unknown route → `404`.
- A response carrying an unrecognized additive field is still accepted by a
  client that ignores it — the additive-compatibility rule.

### CLI

- Each subcommand issues exactly one request to the expected method and path,
  against a stub server, with the expected body.
- The CLI imports no platform package; the existing architecture test covers
  the new file.
- Exit codes: `0` for `accepted`, **`0` for `rejected`**, `2` for usage, `3`
  for operational. A test asserts `1` is never returned by this family.
- Each limit flag is required, and its absence is a usage error naming the
  flag.
- `--json` forwards the server's response unchanged for `create` and `get`;
  `promotion list --json` synthesizes one document, preserves unknown row and
  envelope fields, carries no `next_after`, and refuses a non-progressing
  cursor.
- `promotion list` follows more pages than any previous implementation ceiling
  and aggregates every row exactly once.
- No rank comparison anywhere in the family, asserted by the existing
  `TestCLIDecidesNoPromotionPrecedence` scan extended to the new file.

### WebUI

- The promotion panel calls `POST /v1/promotions` and renders the server's
  response rather than deriving an outcome.
- The environment picker lists what the API returned; no JavaScript compares
  two ranks, asserted by scanning the bundled assets.
- No JavaScript implements or approximates a gate.
- A `rejected` promotion renders as a recorded outcome, not an error banner.
- `4xx` and `5xx` render the server's message without inventing a verdict.
- No credential, endpoint, deployment target or secret is rendered or stored;
  CSP and the allowlisted field set are unchanged.

### Boundary and absence tests

Each fails loudly if a later change crosses the line:

- the **core** module exports no `Promotion` identifier and its build graph
  contains no platform package;
- `Candidate` has no `CurrentEnvironment`, and `Environment` no
  `DeployedCandidate`, `Endpoint`, `URL`, `Credential`, `Secret` or `Token` —
  a field-name scan over both files;
- `Promotion` declares no deployment, credential, approver or free-text field;
- no environment or promotion operation opens a network connection;
- no human-approval, RBAC or permission identifier exists in the platform
  module;
- no promotion policy registry, DSL or assignment type exists;
- the TUI has no promotion command;
- `CanPromote` is still the only rank comparison in the platform module, and
  no adapter has one;
- `EvaluateEvaluationGate` is still the only gate implementation;
- the promotion store capability exposes no update and no delete;
- `schemaTables` gained exactly one name.

## Documentation

Written by the **implementation** PR:

| File | Change |
|---|---|
| `docs/DOMAIN.md` | A `Promotion` section: what it is, what it is not, the decision rule, the same-agent invariant, what is snapshotted and what is recomputed |
| `docs/ARCHITECTURE.md` | The promotion workflow in the control-plane layer; the new store capability; the three routes |
| `docs/compatibility.md` | `SchemaVersion` 4; three new `/v1` route rows; the promotion collection paging row; the CLI family in the exit-code table; the statement that `1` is still `eval compare` only |
| `docs/SECURITY.md` | A section on what a promotion decision is and is not — not a deployment, not an authorization, not a safety claim — and the forgery-resistance properties above |
| `docs/ROADMAP.md` | 066 implemented; 067 next; the `Promotion` row in the domain-concepts table updated from "a platform workflow moving a candidate between environments" to what was actually built |
| `docs/tasks/v1.0/066-promotion-workflow.md` | Status → specified and implemented |
| `docs/tasks/v1.0/README.md` | The 066 row updated; 067–072 unchanged |
| `CHANGELOG.md` | An Unreleased entry |
| `docs/adr/README.md` | ADR 0040 indexed |

### ADR required by the implementation PR

Repository convention, confirmed by task 065: the specification PR writes the
specification, and the **implementation** PR adds the ADR. This PR therefore
adds no ADR.

**ADR 0040 — "Promotions are immutable evidence-backed platform decisions"**,
after verifying `docs/adr/README.md` that 0040 is still free. It must record,
with the alternatives that were rejected:

1. A promotion records a **platform decision**, not a deployment, and
   Trustvian has no observer that could claim otherwise.
2. A promotion is **append-only and immutable**: no update, no delete, at any
   layer.
3. **Candidate residence is not modeled**, and why a mutable
   `CurrentEnvironment` would be a guess served as fact.
4. **Both verdicts are recorded**, structural failures are not, and the
   vocabulary is two values — with the "accepted only" alternative and why the
   caller-owned limits argument defeated it.
5. **Same-agent evidence is required** for promotion while
   `CompareEvaluations` stays same-project.
6. **Gate limits and promotion-time environment ordering are snapshotted**;
   the diff, scorecard and gate result are **recomputed**, with the rule that
   decides which is which.
7. **`CanPromote` remains the only ordering primitive**, called from the
   domain constructor and the service, re-derived nowhere.
8. **Stage skipping is permitted**, and the five questions an adjacency rule
   would have to answer with no consumer to answer them for.
9. **No human approval or authorization** in this task, and why a fake actor
   label is worse than none.
10. **No platform concept enters the engine**, unchanged.

## Acceptance Criteria

Every question this task owns, answered:

| Question | Answer |
|---|---|
| What is a Promotion? | An immutable record of a platform decision: this candidate was accepted or rejected to advance from source to target, on these runs, under these limits, at this time |
| Is it deployment? | **No.** Nothing is moved and nothing is connected to |
| Does `Candidate` gain `CurrentEnvironment`? | **No.** The platform cannot observe residence |
| What identifies a Promotion? | A caller-owned `PromotionID`. Not the tuple, not the timestamp |
| Are rejected attempts persisted? | Yes, when a verdict was reached. Structural failures are errors with no row |
| Which Candidate is promoted? | The candidate run's `CandidateID` |
| Must the runs share an Agent? | **Yes**, in the promotion workflow. `CompareEvaluations` is unchanged |
| Where does the source Environment come from? | Inferred: the shared `EnvironmentRef` of the two runs, in the derived project |
| How is the target supplied? | A caller-supplied `EnvironmentRef`, resolved in that project |
| Must both be active and ranked? | Yes — `CanPromote` requires it |
| Is skipping ranked stages allowed? | **Yes.** Any target for which `CanPromote` is true |
| What role does `CanPromote` play? | The whole ordering decision, called in the service and again in the domain constructor. Nothing else compares ranks |
| Is gate PASS necessary? | Yes, for `accepted` |
| Is it sufficient? | No — ten structural conditions precede it |
| Are gate limits persisted? | Yes, all three |
| Is the gate result persisted? | **No.** Recomputed from the runs and the stored limits |
| Is the scorecard persisted? | **No.** Same reason |
| What mutable facts are snapshotted? | Source and target `rank` and `revision`, the three limits, the outcome and the timestamp |
| What if environments are re-ranked later? | Nothing. The record is unchanged, and the stored revision makes the change detectable |
| Can a Promotion be edited or deleted? | **No**, at any layer. There is no mechanism |
| How are retries handled? | Duplicate id → `409 already_exists`; the client reads it back with `GET` |
| Which store capability owns it? | `ControlStore`, three methods, no update, no delete |
| `SchemaVersion` after implementation? | **4** |
| v3 → v4 behaviour? | Create the table and index, stamp 4, fabricate nothing |
| HTTP routes? | `POST /v1/promotions`, `GET /v1/promotions/{promotion_id}`, `GET /v1/projects/{project_id}/promotions` |
| How is history discovered? | The project-scoped collection, paginated |
| Collection semantics? | `id` byte-ascending, exclusive `after`, `limit` 1–64 default 64, `next_after` only when another page follows |
| CLI commands? | `trustvian promotion create`, `get`, `list` |
| Exit code for a rejection? | **`0`** — a recorded decision. Code `1` is not extended |
| WebUI surface? | Environment list (read-only), promotion panel, promotion history |
| Does the TUI gain promotion? | **No** |
| Realtime events? | **None added** |
| ADR? | 0040, by the implementation PR |
| Explicitly deferred | Chronological history ordering → 067; authentication and RBAC → 070; deployment integration and residence → beyond `v1.0`; an adjacency/skip policy → any later milestone with a stated requirement |

The specification is complete when a reviewer can answer every row above
without reading the implementation, and the implementation PR makes no product
decision this document left open.

## Implementation Checklist

1. `PromotionID` in the `domain.go` identifier block.
2. `platform/promotion.go`: `Promotion`, `PromotionOutcome`,
   `EnvironmentPosition`, `PromotionDecision`, `NewPromotion`,
   `restorePromotion`, `MaxPromotionPage`, the two sentinels.
3. `platform/promotion_store.go`: the shared row/page logic both backends use.
4. `ControlStore` + three methods, with compile-time assertions on both
   backends.
5. SQLite: table, index, `SchemaVersion` 4, `migrateV3ToV4`, the three methods.
6. PostgreSQL: the same, with `COLLATE "C"` and `migratePostgresV3ToV4`.
7. `ControlPlane.Promote`, `Promotion`, `ProjectPromotions`, in the
   orchestration order above.
8. `httpapi`: three routes, DTOs, `classify` cases.
9. `cmd/trustvian/promotion.go` and its dispatch entry.
10. WebUI: environment list, promotion panel, promotion history.
11. The full test matrix above, on both backends.
12. Documentation and ADR 0040.

Steps 1–4 are one reviewable slice, 5–6 another, 7–8 another, and 9–10 each
their own. Nothing after step 4 is worth writing before the store conformance
suite is green on both backends.
