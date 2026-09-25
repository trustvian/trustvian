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
                         ├─▶ CompareEvaluations ─▶ diff · scorecard · gate result
completed candidate run ─┘                                         │
                                                                   ▼
source Environment  (inferred from the two runs)          verdict ⇒ outcome
target Environment  (named by the caller)                          │
         │                                                         │
         └────────▶ CanPromote(source, target) ────────────────────┤
                                                                   ▼
                                                             Promotion
                                                    immutable · append-only
                                          carries the gate result it decided on
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

- a `Promotion` domain value — immutable, fixed-shape, caller-owned identity,
  carrying the decision-time gate result it was decided on;
- one authoritative control-plane operation that owns the ordered
  preconditions and the write;
- `ControlStore` extended with create, read and one bounded project-scoped
  collection, where create is a transaction that revalidates the environment
  state the decision used before it inserts;
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
> under limits *L*. It computed gate result *G*, and its answer was *outcome*.

Every one of those symbols is on the record, *G* included. A promotion that
could only name its inputs would be explaining itself with a recomputation, and
a recomputation answers what today's code thinks rather than what the platform
did.

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

    // limits and gateResult are the decision-time evidence, snapshotted.
    // gateResult is what the workflow actually consumed; limits are its three
    // maximums, kept as a named value because that is how a caller supplied
    // them.
    limits     EvaluationGateLimits
    gateResult EvaluationGateResult

    outcome   PromotionOutcome
    decidedAt time.Time
}

func (p Promotion) ID() PromotionID                  { /* … */ }
func (p Promotion) ProjectID() ProjectID             { /* … */ }
func (p Promotion) CandidateID() CandidateID         { /* … */ }
func (p Promotion) ReferenceRunID() EvaluationRunID  { /* … */ }
func (p Promotion) CandidateRunID() EvaluationRunID  { /* … */ }
func (p Promotion) Source() EnvironmentPosition      { /* … */ }
func (p Promotion) Target() EnvironmentPosition      { /* … */ }
func (p Promotion) GateLimits() EvaluationGateLimits { /* … */ }

// GateResult is the gate result this decision consumed, exactly as it was
// when the decision was made.
//
// Historical evidence, not a live view. Re-deriving a gate result from the
// same runs and limits under a later build may legitimately differ — see
// Historical evidence versus current recomputation — and when it does, this
// value is still what the platform relied on.
func (p Promotion) GateResult() EvaluationGateResult { /* … */ }

func (p Promotion) Outcome() PromotionOutcome        { /* … */ }
func (p Promotion) DecidedAt() time.Time             { /* … */ }
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
//
// Five fields, because almost everything a promotion records is derivable
// from two of them. The gate result is self-describing about which comparison
// it gated — task 056 built it that way — so both run identifiers, both
// candidate identifiers, the environment and the three limits all come out of
// it rather than being supplied alongside it and risking disagreement.
type PromotionDecision struct {
    ID PromotionID

    // Source and Target are the loaded environments, not snapshots. The
    // constructor takes the values so it can ask CanPromote itself and derive
    // the snapshot, rather than trusting a caller-assembled position.
    Source Environment
    Target Environment

    // GateResult is the authoritative result the evaluation path produced.
    // It decides the outcome; no outcome is accepted alongside it.
    GateResult EvaluationGateResult

    DecidedAt time.Time
}

func NewPromotion(d PromotionDecision) (Promotion, error)
```

The constructor:

1. validates `ID` by `validateID`;
2. requires `DecidedAt` to be non-zero (`ErrInvalidTimestamp`);
3. requires `GateResult` to be **bound** — produced by
   `EvaluateEvaluationGate` rather than a zero value. The existing unexported
   `bound` marker exists for exactly this fail-closed check and
   `NewPromotion` is in the same package. An unbound result is
   `ErrInvalidGateEvidence`;
4. **calls `CanPromote(d.Source, d.Target)`** and refuses the construction if
   it is false (`ErrPromotionOrder`);
5. requires the gate result's identity to agree with the promotion's:
   `GateResult.Environment() == d.Source.Ref()`, and
   `GateResult.ReferenceRunID() != GateResult.CandidateRunID()`. A mismatch is
   `ErrInvalidGateEvidence` — it means the result gates a different comparison
   than the one being recorded;
6. **derives** every remaining field:

   | Field | Derived from |
   |---|---|
   | `projectID` | `d.Source.ProjectID()`, guaranteed equal to the target's by `CanPromote` |
   | `referenceRunID`, `candidateRunID` | `GateResult.ReferenceRunID()`, `GateResult.CandidateRunID()` |
   | `candidateID` | `GateResult.CandidateCandidateID()` — the candidate run's candidate is the candidate being promoted |
   | `limits` | the three `Maximum` fields of the gate result's three maximum checks |
   | `source`, `target` | `EnvironmentPosition` snapshots of `d.Source` and `d.Target`, rank guaranteed present by `CanPromote` |
   | `outcome` | `GateResult.Verdict()` — see below |

7. derives the outcome, and accepts no other source for it:

   ```text
   GateVerdictPass → PromotionAccepted
   GateVerdictFail → PromotionRejected
   ```

Three properties this shape buys, each of which is a thing that cannot go
wrong later:

**The outcome cannot disagree with the evidence.** There is no `Outcome`
input. An `accepted` promotion whose stored gate result says FAIL is
unconstructible, not merely discouraged, and the same is true in reverse.

**The identity cannot be forged or mismatched.** The project, both candidates
and both runs all come out of values the service loaded. A caller — and an
adapter — has nothing to supply but an identifier, two environments the
service resolved, a gate result the service computed, and a clock the
composition root owns.

**Ordering has one implementation.** Point 4 calls the one primitive. The
constructor compares no ranks itself, so `CanPromote` remains the only rank
comparison in the repository, as task 065's absence test requires.

### Restoring a stored promotion

```go
func restorePromotion(/* stored scalars */) (Promotion, error)
func restoreEvaluationGateResult(/* stored scalars */) (EvaluationGateResult, error)
```

Both shared by the two backends so neither can accept a row the other would
refuse, exactly as `restoreEnvironment` is.

`restoreEvaluationGateResult` lives in `gate.go` beside the type it rebuilds.
It **evaluates nothing**: it sets `bound`, copies the stored identity, rebuilds
the five checks from their stored operands through the existing `minimumGate`
and `maximumGate` helpers, and takes the **stored verdict verbatim**. That last
point is the whole of the historical-evidence rule applied to the restore path
— recomputing the verdict from the stored checks would mean a later change to
how five checks combine silently rewrote history on read.

`restorePromotion` applies every check a live value faced:

- every identifier by `validateID`;
- `decided_at` parseable and non-zero;
- `outcome` one of the two constants; anything else is `ErrStoreCorrupt`;
- **`outcome` agrees with the restored verdict.** `accepted` with a stored FAIL
  verdict is corruption — this is task 066's own invariant, fixed by this
  task's constructor, so a row that violates it was not written by this code;
- the ordering invariant, by rebuilding two `Environment` values from the
  stored positions through `restoreEnvironment` — active, ranked, in the row's
  project, named after their own refs the way migration names a backfilled
  environment — and asking `CanPromote`. A row whose ranks were inverted by
  damage fails closed, and the repository still contains exactly one rank
  comparison.

What restore deliberately does **not** check: whether the five restored checks
would combine to the stored verdict under today's rule, and whether today's
gate would reach the same verdict from the same evidence. Neither is
corruption. See below.

### Historical evidence versus current recomputation

Two different questions, kept apart because conflating them is how an audit
history quietly becomes a lie.

| Question | Answer |
|---|---|
| *What did the platform rely on when it made this decision?* | `promotion.GateResult()` — the stored snapshot. Always available, never recomputed, never rewritten |
| *What would the current implementation decide from the same evidence?* | `CompareEvaluations(stored run IDs, stored limits)` — a fresh derivation by today's code |

They will normally agree, and the platform makes **no guarantee that they
always will.** A bug fix or a semantic correction in the scorecard or the gate
can legitimately produce a different result from the same immutable evidence
and the same limits:

```text
decision time   runs + limits → PASS → accepted, recorded
later build     runs + limits → FAIL   (a gate defect was corrected)
```

The recorded promotion is still correct about what happened: the platform, as
it then was, accepted that candidate on that result. That is the fact an audit
needs. A divergence is **not corruption of the promotion**, nothing detects it
as one, and nothing overwrites the stored result. If an operator needs to know
that a past decision would go differently today, that is a deliberate
re-evaluation they ask for, and it produces a new answer beside the old record
rather than replacing it.

This is also why the gate result is snapshotted rather than referenced. A
reference is only as good as the guarantee that dereferencing it returns the
same thing, and across software versions that guarantee does not hold for a
derived value.

### What is deliberately not on a `Promotion`

| Field | Why not |
|---|---|
| `AgentID` | Derivable, through immutable edges only: candidate run → `Candidate.AgentID`. A stored copy could only ever agree, and a field that can only agree is a field that can drift |
| `Status`, `State`, `Phase` | A promotion has no lifecycle. It is decided or it does not exist |
| `ApprovedBy`, `Actor`, `RequestedBy` | Nobody is authenticated. A name here would be an unverified string presented as provenance |
| `EvaluationScorecard`, `BehaviorDiff` | Secondary derived views. A promotion did not consume them directly — it consumed the gate result computed from them — and both are recomputable from the immutable runs. ADR 0030 keeps one source of truth for a view nothing decided on |
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

Given all eleven, the workflow holds an authoritative `EvaluationGateResult`,
and the **outcome** is derived from it and from nothing else:

```text
GateResult.Verdict() == PASS  →  accepted
GateResult.Verdict() == FAIL  →  rejected
```

That result is stored on the promotion alongside the outcome it produced, so
the record carries both the answer and the measurement it came from.

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
stored nowhere else, and the **gate result** they produced is a derivation no
later build is obliged to reproduce. Discard the rejected attempt and "we tried
to ship this under these limits and this is what it measured" becomes
unanswerable — which is precisely the question an operator asks when a
candidate did not advance.

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

> **Snapshot what is mutable, what is unrecoverable, and what the decision
> actually consumed. Reference immutable primary evidence. Recompute secondary
> derived views, when someone asks for a current one.**

The third clause is doing real work. "Derived, therefore recomputable" is not
sufficient on its own, because recomputation answers *what would today's code
decide* and a historical record has to answer *what did the platform rely on*.
Those coincide until the first time the gate or the scorecard is corrected, and
a record that cannot survive its own bug fixes is not an audit record.

### Referenced: immutable primary evidence

| Evidence | Why a reference is enough |
|---|---|
| reference run, candidate run | `EvaluationRun` is immutable once completed, and the store has no delete |
| the runs' aggregates and behavior snapshots | Written during ingest, and ingest is refused on a run that is not running — so a completed run's evidence never changes again |
| candidate, agent, project | Caller-owned identity, immutable, never deleted |

A reference is sound here because dereferencing it returns the same bytes
forever, whatever version of the platform is running.

### Snapshotted: mutable, unrecoverable, or consumed by the decision

| Evidence | Why a reference is not enough |
|---|---|
| the three gate limits | Caller-owned and stored nowhere else. Lose them and the verdict is unexplainable |
| **the `EvaluationGateResult`** | **The result the decision consumed.** Re-deriving it later answers a different question, and a corrected gate may answer it differently. Fixed-shape and bounded, so snapshotting it costs a known number of scalar columns |
| source and target `rank` | Configuration an operator changes. ADR 0039 §6 already says promotion-time ordering belongs on the promotion record; this is that |
| source and target `revision` | The configuration the decision was bound to. It is also the token the store revalidates at commit — see [Concurrency](#concurrency-and-retry-semantics) |
| `outcome` | The durable workflow decision. Derived from the stored verdict at construction, stored so a reader needs no derivation at all |
| `decided_at` | When. Not identity |

### Recomputed: secondary derived views

The `BehaviorDiff` and the `EvaluationScorecard` are **not stored**.

Neither is what the decision consumed. The workflow consumed a gate result; the
scorecard and diff are the inputs that produced it, and both are recomputable
from the immutable runs. Storing them would duplicate large derived structures
— the scorecard alone carries dozens of rate and metric comparisons — to answer
a question the stored gate result already answers better, because the gate
result is the part the decision turned on.

A consumer that wants the full comparison as **today's code sees it** posts the
promotion's own stored run identifiers and stored limits to the existing route:

```text
POST /v1/evaluations/compare
  { "reference_run_id": <promotion.reference_run_id>,
    "candidate_run_id": <promotion.candidate_run_id>,
    "gate_limits":      <promotion.gate_limits> }
```

That is a **current re-evaluation**, and the specification says so wherever it
appears. It is not the historical decision evidence, and it is not required to
reproduce the recorded verdict. The recorded verdict lives on the record.

### Why the gate result and not the scorecard

Both are derived; only one is snapshotted. The line is *what the decision
consumed*:

```text
aggregates + snapshots ──▶ BehaviorDiff ──▶ Scorecard ──▶ GateResult ──▶ outcome
└─── referenced ────────┘  └────── recomputed ───────┘   └─ snapshotted ──┘
```

The promotion's outcome is a function of the gate result and nothing else. The
scorecard is one step further back: it is how the gate result was reached, not
what the decision read. Snapshotting the last derived value before the decision
is the smallest snapshot that makes the decision self-explaining, and
`EvaluationGateResult` is fixed-shape by construction — five checks, two
integers each, one closed-vocabulary verdict — so it fits the resource model
exactly. A scorecard snapshot would not be small, and would still not be the
thing the decision consumed.

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

Ordered so that **every structural precondition is checked before any evidence
is read**, and so that an impossible promotion costs a handful of primary-key
lookups rather than the evidence reads and three derivations a comparison
costs.

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
        → diff, scorecard, gate result           evidence reads + derivations

13  NewPromotion{ID, source, target, gate, at}   no reads
        derives outcome from gate.Verdict()
        derives project, candidates, run IDs and limits
        from the source environment and the gate result

14  control.CreatePromotion(promotion)           1 transaction:
        atomically revalidates both environment    2 locked reads
        revisions and the ordering, then inserts   + 1 insert

15  return the stored decision
```

Four steps that look like choices and are:

**Step 11 and step 14 both check ordering, and they are different checks.**
Step 11 answers *is this request structurally possible*, on values just read,
and its failure is `ErrPromotionOrder` — the request itself is wrong. Step 14
answers *is the configuration the decision was built on still the authoritative
one*, atomically with the insert, and its failure is `ErrStoreConflict` — the
request was fine and the world moved. Keeping them separate is what makes the
race observable without pretending the caller sent something invalid.

**Step 12 reuses `CompareEvaluations` whole**, including its own repeat of
`completedRun` and `requireSameProject`. That redundancy — two extra run reads
and the four identity reads behind `requireSameProject` — is accepted
deliberately. The alternative is an internal comparison variant that skips the
checks, which creates a code path where the checks *can* be skipped; a future
edit routing another caller through it would lose them silently. One
implementation of the comparison chain, with its preconditions attached, is
worth six primary-key reads.

**Step 13 does not decide anything.** It receives the authoritative gate result
and derives the outcome from its verdict. No adapter — and not the service
either — chooses `accepted` or `rejected`. The constructor does, from evidence.

**Step 14 is the only write, and the store revalidates rather than
re-deciding.** It does not recompute the gate, re-read evaluation evidence or
construct a promotion; it checks that the two environment rows the decision was
built on are unchanged, and inserts. The division is exact: the service owns
the decision, the store owns the concurrency invariant.

A structural failure writes nothing. A rejected verdict writes a record,
because a verdict was reached.

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
| an unbound or mismatched gate result reaches `NewPromotion` | `ErrInvalidGateEvidence` |
| **an environment changed between the decision and the commit** | **`ErrStoreConflict`** |
| a stored row cannot be restored | `ErrStoreCorrupt` |
| gate verdict FAIL | **no error** — outcome `rejected` |

`ErrStoreConflict` is the existing compare-and-swap vocabulary, used here for
exactly what it already means everywhere else in this repository: the value you
read is no longer the stored value. A caller that receives it may retry the
whole promotion, which re-reads the environments and decides again.

The distinction between it and `ErrPromotionOrder` is deliberate and
observable:

| The caller sees | Meaning | Retry helps? |
|---|---|---|
| `ErrPromotionOrder` | The request was structurally impossible when it arrived — an archived target, a backward rank, an unranked side | No, not until someone changes the configuration |
| `ErrStoreConflict` | The request was fine and the configuration moved under it before the write landed | Yes |

Reporting the race as `ErrPromotionOrder` would tell an operator their request
was wrong when it was not, and would hide a concurrency event behind a
validation message.

No new error *code* string reaches `/v1` — both map onto `conflict`. See
[Status and error mapping](#status-and-error-mapping).

## Persistence

### Capability placement: extend `ControlStore`

Three methods, and no more than task 066 needs:

```go
// CreatePromotion stores one decision, in one transaction that first
// revalidates the environment state the decision was built on.
//
// A promotion is immutable, so this is the only write: there is no update and
// no delete at any layer.
//
// A promotion whose ID already exists is ErrStoreAlreadyExists, whatever the
// rest of the request says — the identifier names a decision that was already
// recorded.
//
// The revalidation is not a courtesy. A promotion carries the revision of each
// environment it was decided against, and those revisions must still be the
// authoritative ones at the serialization point of this insert, or the row
// would record a decision about a configuration that had already been
// replaced. A changed revision is ErrStoreConflict. See Concurrency.
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
    id                              TEXT PRIMARY KEY,
    project_id                      TEXT NOT NULL REFERENCES platform_projects(id),
    candidate_id                    TEXT NOT NULL REFERENCES platform_candidates(id),
    reference_candidate_id          TEXT NOT NULL REFERENCES platform_candidates(id),

    reference_run_id                TEXT NOT NULL,
    candidate_run_id                TEXT NOT NULL,

    source_environment_ref          TEXT NOT NULL,
    source_environment_rank         INTEGER NOT NULL,
    source_environment_revision     TEXT NOT NULL,

    target_environment_ref          TEXT NOT NULL,
    target_environment_rank         INTEGER NOT NULL,
    target_environment_revision     TEXT NOT NULL,

    -- The three caller-owned limits. They are also the three maximum checks'
    -- Maximum operands, stored once and read as both.
    max_added_behaviors             TEXT NOT NULL,
    max_block_decisions             TEXT NOT NULL,
    max_critical_risk_observations  TEXT NOT NULL,

    -- The decision-time gate result: what each check measured, what the two
    -- minimum checks required, and the verdict reached.
    gate_reference_evidence_actual  TEXT NOT NULL,
    gate_reference_evidence_minimum TEXT NOT NULL,
    gate_candidate_evidence_actual  TEXT NOT NULL,
    gate_candidate_evidence_minimum TEXT NOT NULL,
    gate_added_behaviors_actual     TEXT NOT NULL,
    gate_block_decisions_actual     TEXT NOT NULL,
    gate_critical_risk_actual       TEXT NOT NULL,
    gate_verdict                    TEXT NOT NULL,

    outcome                         TEXT NOT NULL,
    decided_at                      TEXT NOT NULL
);

CREATE INDEX platform_promotions_by_project
    ON platform_promotions (project_id, id);
```

Twenty-five columns, every one a bounded scalar. No JSON column, no blob, no
array, no nullable field — the repository stores fixed-shape values as explicit
columns, and a serialized `EvaluationGateResult` in a text column would be a
schema the database could not check and a migration could not see.

#### Restoring the gate result from these columns

`EvaluationGateResult` has thirteen fields. Four need no column because the row
already carries them; one identity column exists only because the row
otherwise would not; the rest are stored or recomputed as follows.

| Gate-result field | Where it comes from |
|---|---|
| `referenceRunID`, `candidateRunID` | the promotion's own two run columns |
| `candidateCandidate` | `candidate_id` — the candidate run's candidate is the candidate being promoted |
| `referenceCandidate` | `reference_candidate_id`, the one identity column the promotion would not otherwise need |
| `environment` | `source_environment_ref` — both runs share it, which is how the source was inferred |
| `referenceEvidence` Actual, Minimum | `gate_reference_evidence_actual`, `…_minimum` |
| `candidateEvidence` Actual, Minimum | `gate_candidate_evidence_actual`, `…_minimum` |
| `addedBehaviors` Actual / Maximum | `gate_added_behaviors_actual` / `max_added_behaviors` |
| `blockDecisions` Actual / Maximum | `gate_block_decisions_actual` / `max_block_decisions` |
| `criticalRiskObservations` Actual / Maximum | `gate_critical_risk_actual` / `max_critical_risk_observations` |
| each check's `Passed` | recomputed by `minimumGate` / `maximumGate` from the two stored operands |
| `verdict` | `gate_verdict`, **stored, never recomputed** |
| `bound` | set by `restoreEvaluationGateResult` |

Two choices in that table are the ones worth defending.

**The two minimums are stored even though today they are always 1.** Today
`EvaluateEvaluationGate` hardcodes a minimum of one record on each side. A
build that changed it would change what a decision required, and a record that
read the constant from the running binary would silently restate history under
the new rule. The column costs a few bytes and removes the whole class.

**`Passed` is recomputed; `verdict` is not.** A check's `Passed` is arithmetic
over two operands the row stores — `actual >= minimum`, `actual <= maximum` —
and recomputing it through the very helpers that produced it introduces no
policy. The verdict *is* policy: it is the rule for combining five checks, and
a build that added a sixth or changed the combination would derive a different
verdict from identical stored checks. So the verdict is read from the column,
verbatim, and restore does **not** cross-check it against the recomputed flags
— a row whose checks would combine differently under today's rule is a record
from an older rule, not a corrupt row.

What restore *does* cross-check is `outcome` against `gate_verdict`, because
that correspondence is task 066's own invariant, fixed by this task's
constructor. A disagreement there means something wrote the row that this code
did not, and it is `ErrStoreCorrupt`.

**PostgreSQL** adds `COLLATE "C"` to `id`, `project_id`, `candidate_id`,
`reference_run_id`, `candidate_run_id` and both environment refs, for the same
reason task 065 did: the collection paginates on `id` in byte order, and a
locale-aware collation would order two backends' pages differently and could
place a row on a page a cursor had already passed.

**`uint64` as `TEXT`** for the three limits, the two revisions and the seven
gate counts, matching
every other `uint64` in this schema — the full unsigned range survives exactly,
where a native integer type would not. **`uint16` rank as `INTEGER`**, matching
`platform_environments`. **Timestamps as `TEXT`** in `RFC3339Nano`, through the
existing `timeText`/`parseTimeText` pair, so a zone offset and nanosecond
precision survive.

**Foreign keys, and deliberately only three.** `project_id`, `candidate_id`
and `reference_candidate_id` point at `ControlStore` entities in the same
capability, and none is ever deleted, so the constraints cost nothing and
document the scope.

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
scorecard, behavioral diff, decision record or raw event. The gate columns are
the exception this task argues for, and they are eight bounded scalars rather
than a blob. `schemaTables` gains exactly one name.

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

### The invariant

> **A promotion may commit only if the source and target environment states
> the decision used are still the authoritative states at the point the insert
> serializes.**

The two `EnvironmentPosition` snapshots on a stored promotion must therefore
correspond to **one coherent authoritative state** under which
`CanPromote(source, target)` was true — not to two independent reads that may
never have been simultaneously current.

This is a persistence invariant, not an in-memory service check, and it is why
`CreatePromotion` is a transaction rather than an insert.

### Why reading each environment once is not enough

Source and target are separate mutable rows, read at different instants, and
the decision is written later still. Nothing about that sequence forces the two
reads to describe one state, and the window is wide — a whole comparison sits
inside it.

The failure is not hypothetical and not cosmetic:

```text
T1  promotion workflow  reads source    → active, rank 30, revision 7
T2  an operator         archives source → revision 8, committed
T3  promotion workflow  reads target    → active, rank 40, revision 3
T4  CanPromote(source@7, target@3)      → true, on a source that no longer reads that way
T5  gate PASS
T6  INSERT accepted promotion out of an archived environment
```

At T6 the platform records that a candidate was accepted to advance *out of an
environment that was archived before the record was written* — and task 065's
whole point is that an archived environment is closed to new work and is
neither a promotion source nor a target. A weaker variant does the same damage
with a re-rank: a target demoted below the source between T3 and T6 yields a
stored "forward" decision that was backward by the time it landed.

Both are durable, both look correct in the history, and neither is detectable
afterwards.

### The correction

`CreatePromotion` runs one transaction, on both backends:

```text
begin a write transaction

for each of source and target, in (project_id, ref) byte order:
    take the row lock and re-read the environment row
    restore it through restoreEnvironment          → a current Environment

require current source revision == promotion.Source().Revision
require current target revision == promotion.Target().Revision
        otherwise → ErrStoreConflict

require CanPromote(current source, current target)
        otherwise → ErrStoreConflict

insert the promotion row
        duplicate identifier → ErrStoreAlreadyExists

commit
```

Three notes on that sequence.

**The revision check is the primary guard; the `CanPromote` call is the
independent one.** Task 065 guarantees every environment mutation advances the
revision by exactly one, so an unchanged revision already implies unchanged
rank and status — which makes the ordering call redundant *if that discipline
holds everywhere, forever*. It is kept anyway, because it costs one function
call and does not depend on the discipline: an out-of-band `UPDATE`, a
migration defect, or a future mutation path that forgets to bump would all slip
past a revision check alone and would not slip past this one.

**The ordering call is a call.** `CanPromote` is invoked on two restored
`Environment` values. The store compares no ranks itself, so this adds no
second implementation of promotion precedence and task 065's absence test keeps
passing unchanged.

**The store revalidates; it does not re-decide.** It does not recompute the
gate, re-read evaluation evidence, reconstruct a scorecard or choose an
outcome. Its entire job is the environment-staleness invariant.

### Any revision change invalidates the attempt

Including a rename, which under task 065 advances the revision without changing
anything `CanPromote` reads.

That is deliberate. The promotion snapshots the revision precisely to bind the
decision to one configuration, and a store that tried to decide *which* changes
matter would have to compare configuration field by field — which means
comparing ranks outside `CanPromote`, and means the answer changes silently
every time a field is added to `Environment`. "The configuration I decided
against is still the current one" is a single, stable, checkable question, and
a rename-induced retry costs one round trip.

### PostgreSQL

Two `SELECT … FOR UPDATE` statements, issued **in `(project_id, ref)` byte
order**, inside the existing `withTx` READ COMMITTED transaction:

```sql
SELECT ref, name, rank, status, revision
  FROM platform_environments
 WHERE project_id = $1 AND ref = $2
   FOR UPDATE
```

Explicitly two statements in a determined order rather than one
`ref = ANY(…) ORDER BY ref FOR UPDATE`: the lock-acquisition order of a
multi-row `FOR UPDATE` follows the query plan, and a specification that depends
on a planner choice is not a specification. Two statements make the order a
property of the code.

**Deterministic ordering is what prevents deadlock.** Two concurrent promotions
with overlapping environment pairs — `staging → production` and
`production → eu-prod`, say — would deadlock if each locked its own source
first. Locking in ref byte order means both reach `production` at the same
point in their sequence, so one waits and neither dies.

**The lock is on two rows, not the project.** Unlike task 065's creation cap,
which is a cross-row invariant over *all* of a project's environments and
therefore locks the project row, this invariant names exactly two rows. A
promotion in one project cannot block a promotion in another, and two
promotions in one project over disjoint environment pairs do not block each
other either.

### SQLite

The same sequence inside one explicit write transaction, taken with write
intent **before** anything is read — the `BEGIN IMMEDIATE` equivalent task 065
established, never a reliance on `SetMaxOpenConns(1)`.

Write intent is taken by touching each environment row in the same
`(project_id, ref)` byte order:

```sql
UPDATE platform_environments SET name = name
 WHERE project_id = ? AND ref = ?
```

A no-op write that escalates the transaction to a writer and whose
`RowsAffected` doubles as the existence check, exactly as `lockProjectForWrite`
does for the creation cap — narrowed here from the project row to the two
environment rows. It changes no column, so it advances no revision.

SQLite serializes writers database-wide regardless, so the property that
matters here is not lock granularity but atomicity: the revision validation and
the insert are one write decision, with no reader-then-writer gap between them.

### This is a cross-row invariant

Stated plainly, because an earlier reading of this design said otherwise and
was wrong. A promotion's validity spans three rows — the source environment,
the target environment, and the promotion being written — and no predicate on
the inserted row can express it. That is the same *class* of problem as task
065's creation cap, with a different shape and therefore a different remedy:

| | Task 065 creation cap | Task 066 promotion commit |
|---|---|---|
| The invariant | "this project holds fewer than 64 environments" | "these two environment rows are unchanged and still ordered" |
| Rows involved | every environment of one project — unbounded, not nameable in advance | exactly two, both named by the promotion |
| Remedy | lock the owning **project** row | lock the **two environment** rows, in ref order |
| Cost | serializes creation within one project | serializes only promotions sharing an environment |

Neither serializes unrelated projects, and neither uses a process-local mutex,
a counter table or a table lock.

### Races, and what each does

| Race | Outcome |
|---|---|
| two requests, same `PromotionID` | The primary key decides. Exactly one commits; the other is `ErrStoreAlreadyExists` → `409 already_exists`. No partial write exists, because the row is written once and never updated |
| two requests, different IDs, same `(candidate, source, target)`, environments unchanged | **Both succeed.** Two decisions were made and two are recorded. They serialize on the two environment locks and neither is refused |
| source archived between the service read and the commit | The revision moved. `ErrStoreConflict` → `409 conflict`, **no row**. The stale accepted promotion of the example above cannot be written |
| target re-ranked below the source before the commit | Same: revision moved, `ErrStoreConflict`, no row |
| either environment renamed before the commit | Same. Any revision change invalidates the attempt, by the rule above |
| promotions in two different projects | Disjoint locks. Neither blocks the other |
| promotions in one project over disjoint environment pairs | Disjoint locks. Neither blocks the other |
| overlapping environment pairs approached from opposite directions | Both lock in ref byte order, so one waits. No deadlock |
| a concurrent `CreateEvaluationRun` or ingest | Cannot affect the decision: both referenced runs are already completed, and ingest is refused on a run that is not running |

**No uniqueness constraint on `(candidate_id, source_ref, target_ref)`.** That
tuple is legitimately repeatable — see
[Why an identifier exists](#why-an-identifier-exists-and-why-candidate-source-target-is-not-one)
— and adding the constraint would make the database enforce a business rule the
domain has not specified and the product has not asked for.

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

Identical shape from `POST` and `GET`: the stored record, including the
decision-time gate result.

```jsonc
{
  "version": "1",
  "id": "promo-2026-03-01-a",
  "project_id": "proj-1",
  "candidate_id": "cand-9",
  "reference_candidate_id": "cand-7",
  "reference_run_id": "run-baseline-7",
  "candidate_run_id": "run-candidate-9",
  "source_environment": { "ref": "staging",    "rank": 30, "revision": "7" },
  "target_environment": { "ref": "production", "rank": 40, "revision": "3" },
  "gate_limits": {
    "max_added_behaviors":            "3",
    "max_block_decisions":            "0",
    "max_critical_risk_observations": "0"
  },
  "gate_result": {
    "reference_evidence":         { "actual": "412", "minimum": "1", "passed": true  },
    "candidate_evidence":         { "actual": "388", "minimum": "1", "passed": true  },
    "added_behaviors":            { "actual": "5",   "maximum": "3", "passed": false },
    "block_decisions":            { "actual": "0",   "maximum": "0", "passed": true  },
    "critical_risk_observations": { "actual": "0",   "maximum": "0", "passed": true  },
    "verdict": "fail"
  },
  "outcome": "rejected",
  "decided_at": "2026-03-01T09:14:22.481Z"
}
```

`gate_result` reuses the existing `gateResultDTO`, `minimumGateDTO` and
`maximumGateDTO` shapes byte for byte — the same field names
`POST /v1/evaluations/compare` already publishes under its `gate` key. One gate
shape on the wire, not two, so a client that can render a comparison's gate can
render a promotion's without new code.

`rank` is a number (a `uint16`, like the environment response). `revision`, the
limits and every gate count are strings (`uint64`). `outcome` and `verdict` are
each one of exactly two values, and they always correspond.

**A reader needs no second request to know why the decision went the way it
did.** That is what snapshotting the result buys: `added_behaviors` above says
this promotion was rejected because the candidate exhibited five new behaviors
against a limit of three, and it will still say that after any future change to
how the gate is computed.

What a second request buys is a different thing — a **current re-evaluation**:

```text
POST /v1/evaluations/compare   with the record's run IDs and gate_limits
  → what today's implementation derives from the same immutable evidence
```

Useful when an operator wants to know whether a past decision would go
differently now. It is explicitly **not** the historical decision evidence, and
the platform does not promise the two agree. See
[Historical evidence versus current recomputation](#historical-evidence-versus-current-recomputation).

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
| an environment changed before the commit (`ErrStoreConflict`) | 409 | `conflict` |
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
version check apply unchanged. A promotion request has six scalar fields; a
response has twenty-five, of which sixteen are the gate result's five fixed
checks and its verdict. All bounded, all scalars, none repeated — the gate
result is fixed-shape by construction, which is what makes snapshotting it
compatible with the resource model. No route accumulates anything in memory
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
| A gate FAIL cannot become `accepted` through an adapter bug | The outcome has no input at all. `NewPromotion` derives it from the gate result's verdict, so an `accepted` promotion carrying a FAIL result is unconstructible rather than merely discouraged, and `restorePromotion` refuses such a row as corrupt |
| A past decision cannot be silently restated under new gate semantics | The decision-time `EvaluationGateResult` is snapshotted and its verdict is read back verbatim. A later recomputation is a separate, explicitly requested answer that never overwrites the record |
| A decision cannot be committed against stale environment configuration | `CreatePromotion` revalidates both environment revisions and re-asks `CanPromote` atomically with the insert, under row locks taken in a deterministic order |
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

- `NewPromotion` succeeds and every accessor returns what was supplied or
  derived.
- `ID` rejected independently: empty, 257 bytes, control character, invalid
  UTF-8, leading and trailing whitespace.
- Zero `DecidedAt` is refused.
- An **unbound** `EvaluationGateResult` — the zero value — is refused with
  `ErrInvalidGateEvidence`. This is the fail-closed check the `bound` marker
  exists for.
- A gate result whose `Environment()` differs from the source environment's ref
  is refused.
- A gate result whose reference and candidate run identifiers are equal is
  refused.
- **The outcome is derived, and cannot be contradicted.** There is no `Outcome`
  input: a PASS result yields `PromotionAccepted` and a FAIL result yields
  `PromotionRejected`, asserted both ways. A test asserts `PromotionDecision`
  has no outcome field, so the ability to supply one cannot be reintroduced
  without failing here.
- Every derived field is derived: `projectID` from the source environment, both
  run identifiers and both candidate identifiers from the gate result, the
  three limits from the gate result's three maximums. No input can override any
  of them.
- Ordering: `NewPromotion` refuses every pair `CanPromote` refuses — unranked
  source, unranked target, archived source, archived target, equal ranks,
  backward, same environment, different projects — and accepts a forward jump
  over an intermediate rank.
- A constructed `Promotion` has no method that returns a modified copy;
  asserted structurally, alongside the absence of any `Update`/`With`/`Set`.
- `EnvironmentPosition` carries exactly `Ref`, `Rank`, `Revision` — a field
  count assertion, so a later `Name` or `Endpoint` fails here first.
- `Promotion.GateResult()` returns a value equal in every field to the one
  passed to the constructor, including all five checks and the verdict.

Restore:

- `restoreEvaluationGateResult` round-trips a result **exactly**: all five
  checks, both minimums, all three maximums, the five `Passed` flags and the
  verdict.
- The restored result is `bound`, so it is usable wherever a live one is.
- **The verdict is taken verbatim, not recomputed.** A row whose stored verdict
  is PASS while its stored checks would combine to FAIL under today's rule
  restores **successfully**, carrying the stored verdict — the explicit test
  for the historical-evidence contract, and the one that fails if somebody
  "helpfully" re-derives the verdict on read.
- `restorePromotion` round-trips a stored row, and fails closed with
  `ErrStoreCorrupt` on: an unknown outcome, an unknown verdict, an unparseable
  timestamp, a rank above 9999, an unparseable `uint64`, **inverted ranks**, and
  an **outcome that disagrees with the stored verdict**.

### Service

Valid path:

- A forward promotion with a PASS gate records `accepted`, stores a gate result
  whose verdict is PASS, and returns it.
- A forward promotion with a FAIL gate records `rejected`, stores a gate result
  whose verdict is FAIL, returns it, and returns **no error**.
- The stored gate result equals the one `CompareEvaluations` produced during
  the decision, field for field — the service snapshots rather than
  re-deriving.
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
- The gate is evaluated **once**: a control plane over an instrumented store
  counts one evidence read per run, proving the service does not compare twice
  to obtain a result it already holds.

History:

- Re-ranking, renaming, archiving and re-activating either environment after a
  promotion leaves the stored promotion byte-identical — snapshotted ranks,
  revisions, gate result and outcome all unchanged.
- The stored revision differs from the environment's current revision after a
  configuration change — the detectability the field exists for.
- **A later recomputation is not required to match.** A control plane whose
  gate evaluation is substituted with one returning the opposite verdict from
  the same evidence — standing in for a future semantic correction — leaves the
  stored promotion completely unchanged: its gate result, verdict and outcome
  still read as recorded, nothing is rewritten, and nothing reports corruption.
  This is the executable form of the historical-evidence contract, and it is
  the test that fails if anyone reintroduces recompute-on-read.
- `CompareEvaluations` is unchanged: a same-project, different-agent comparison
  still succeeds, asserted so this task cannot tighten it by accident.

### Store conformance, SQLite and PostgreSQL

Run through the existing shared conformance suite so both backends answer
identically, plus the differential suite:

- create then read, with nanosecond `decided_at`, `MaxUint64` limits and gate
  counts, rank 0 and rank 9999;
- **every gate-result field round-trips**: both actuals and both minimums of
  the two minimum checks, all three maximum checks' actuals and maximums, all
  five `Passed` flags, and the verdict — a field-by-field comparison rather
  than a spot check, so a column omitted from either backend's `INSERT` or
  `SELECT` fails here;
- an `accepted` row stores verdict `pass` and a `rejected` row stores `fail`;
- duplicate identifier → `ErrStoreAlreadyExists`;
- missing project or candidate → `ErrStoreNotFound`;
- restart persistence: reopen the store and read every field back;
- immutability: the interface exposes no update, asserted structurally, and no
  `UPDATE` statement against `platform_promotions` exists in either backend;
- corrupt rows fail closed, one column at a time, including an outcome that
  disagrees with the stored verdict;
- collection: ordering by `id` byte-ascending with a fixture whose identifiers
  order differently under a locale-aware collation, proving `COLLATE "C"`;
- collection: `limit` 64 accepted, 65 refused, 0 and −1 refused, page never
  larger than `limit`, exclusive cursor, empty project empty, missing project
  `ErrStoreNotFound`;
- collection: a project with 130 promotions enumerates completely with no
  duplicate and no omission, and a promotion created between pages appears if
  and only if its id sorts after the cursor;
- cross-project isolation: one project's promotions never appear in another's.

### Environment staleness at commit

The concurrency invariant, on **both** backends, in the shared conformance
suite so neither can hold it differently. Each case constructs a valid
promotion, changes an environment, and only then calls `CreatePromotion`:

- **Source archived before the commit** → `ErrStoreConflict`, and **no row
  exists**. Without the transactional revalidation this is the stale accepted
  promotion out of an archived environment, so this case is the regression test
  for the whole section.
- **Target re-ranked below the source before the commit** → `ErrStoreConflict`,
  no row, and in particular no stored promotion whose target rank is below its
  source rank.
- **Target archived before the commit** → `ErrStoreConflict`, no row.
- **Either environment renamed before the commit** → `ErrStoreConflict`, no
  row. A rename changes nothing `CanPromote` reads and still invalidates the
  attempt, which is the documented rule; a test pins it so the rule cannot be
  relaxed by accident.
- **Nothing changed** → the promotion commits, and its stored positions equal
  the environments' current rank and revision.
- **Retry after a conflict succeeds.** Re-running the whole workflow against
  the changed configuration produces a promotion carrying the *new* revisions,
  proving the conflict is a retryable race rather than a dead end.

Concurrency, on both backends:

- N concurrent creates of one identifier: exactly one succeeds, the rest are
  `ErrStoreAlreadyExists`, and the stored row equals the winner's.
- Concurrent creates of different identifiers over the **same unchanged**
  environment pair: **all succeed**. They serialize on the two locks and none
  is refused — the test that fails if somebody adds tuple uniqueness.
- Concurrent creates in **different projects** do not block each other, proved
  by the occupancy-hook pattern task 065 used rather than by timing: a hook
  inside one promotion's transaction records whether another is inside its own
  at the same moment.
- Concurrent creates in **one project over disjoint environment pairs** do not
  block each other, by the same hook.
- Concurrent creates over **overlapping environment pairs** *do* exclude each
  other, by the same hook — the positive proof that the lock is real rather
  than merely uncontended.

PostgreSQL specifically:

- **Deterministic lock order prevents deadlock.** Two promotions whose
  environment pairs overlap in opposite directions — `a → b` and `b → c`, with
  refs chosen so a naive source-first order would acquire them in opposite
  sequences — run concurrently many times and never produce a deadlock
  (SQLSTATE 40P01). The ref-ordered acquisition is additionally asserted by
  inspecting the statements the transaction issues, so the property is proved
  structurally rather than only by the absence of a failure.
- The locks are `FOR UPDATE` on two rows of `platform_environments`, never on
  `platform_projects` and never table-wide — asserted by the same statement
  inspection, so a later change to a project-wide lock fails here.

SQLite specifically:

- The transaction takes write intent **before** reading, proved the way task
  065 proves it: with `SetMaxOpenConns` raised above 1, so a read-then-write
  transaction could interleave, the staleness cases above still hold.

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
  outcome **and the decision-time `gate_result`**, with all five checks and the
  verdict.
- `GET /v1/promotions/{id}` returns the same `gate_result` as the `POST` that
  created it, byte for byte — the historical evidence is served from the
  record, not re-derived per request.
- The `gate_result` field names match the `gate` object
  `POST /v1/evaluations/compare` already publishes, asserted by decoding both
  into the same DTO.
- An environment changed between the decision and the commit → `409 conflict`
  with no promotion created, and a subsequent `GET` of the identifier is a
  `404`.
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
- The CLI renders the recorded `gate_result` for `get` and `create`, and
  derives no verdict and no outcome of its own — a scan asserts the family
  contains no comparison of a count against a limit.

### WebUI

- The promotion panel calls `POST /v1/promotions` and renders the server's
  response rather than deriving an outcome.
- The environment picker lists what the API returned; no JavaScript compares
  two ranks, asserted by scanning the bundled assets.
- No JavaScript implements or approximates a gate.
- A `rejected` promotion renders as a recorded outcome, not an error banner,
  and shows the recorded gate result's failing check rather than recomputing
  one.
- A `409` from a concurrent environment change renders as a retryable conflict
  with the server's message, not as a rejected promotion.
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
- `EvaluateEvaluationGate` is still the only function that *computes* a verdict
  from a scorecard; `restoreEvaluationGateResult` rehydrates a stored one and
  evaluates nothing, asserted by a scan for the comparison operators a gate
  would need;
- no adapter constructs an `EvaluationGateResult`, and no adapter derives a
  `PromotionOutcome`;
- the promotion store capability exposes no update and no delete, and no
  `UPDATE` statement targets `platform_promotions` in either backend;
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
6. **The decision-time gate result is snapshotted, not recomputed**, alongside
   the gate limits and the promotion-time environment ordering. The rule that
   decides what is snapshotted, referenced and recomputed, and the reason
   "derived, therefore recomputable" was not sufficient: a recomputation
   answers what today's build decides, and a bug fix in the gate or scorecard
   may legitimately answer it differently from the same immutable evidence. A
   divergence is not corruption, and nothing rewrites the record.
7. **The outcome is derived from the stored verdict**, never supplied. An
   `accepted` promotion carrying a FAIL result is unconstructible, and such a
   row is corruption on read.
8. **`CanPromote` remains the only ordering primitive**, called from the domain
   constructor, the service, and the store's commit-time revalidation —
   re-derived nowhere.
9. **A promotion commits only against the environment state it was decided
   against.** The invariant spans three rows, so `CreatePromotion` is a
   transaction that locks the two environment rows in `(project_id, ref)` byte
   order, revalidates both revisions, re-asks `CanPromote`, and inserts —
   `ErrStoreConflict` if anything moved. Why two-row locking rather than task
   065's project lock, why deterministic ordering, and why any revision change
   invalidates the attempt including a rename.
10. **Stage skipping is permitted**, and the five questions an adjacency rule
    would have to answer with no consumer to answer them for.
11. **No human approval or authorization** in this task, and why a fake actor
    label is worse than none.
12. **No platform concept enters the engine**, unchanged.

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
| Is the gate result persisted? | **Yes** — the decision-time result, in eight bounded columns, verdict included |
| Is a later recomputation guaranteed to match it? | **No**, and it is not required to. A divergence is a corrected implementation, not a corrupt record |
| Is the scorecard persisted? | **No.** A secondary derived view the decision did not consume; recomputable from the immutable runs |
| What is snapshotted? | The gate result, the three limits, source and target `rank` and `revision`, the outcome and the timestamp |
| Where does the outcome come from? | Derived by `NewPromotion` from the gate result's verdict. There is no outcome input at any layer |
| What if environments are re-ranked later? | Nothing. The record is unchanged, and the stored revision makes the change detectable |
| What if an environment changes *before the commit*? | `CreatePromotion` revalidates both revisions and re-asks `CanPromote` inside the transaction that inserts. Anything moved → `ErrStoreConflict` → `409`, no row, retryable |
| Does any revision change invalidate the attempt? | **Yes**, a rename included. The revision is the binding, and deciding which fields "matter" would put a configuration comparison outside `CanPromote` |
| Does promotion take a lock? | Yes — the two environment rows, in `(project_id, ref)` byte order. Never the project, never a table, never a process mutex |
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
3. `platform/gate.go`: `restoreEvaluationGateResult`, beside the type it
   rebuilds. Evaluates nothing; takes the stored verdict verbatim.
4. `platform/promotion_store.go`: the shared row, page and commit-time
   revalidation logic both backends use, including the `CanPromote` re-ask.
5. `ControlStore` + three methods, with compile-time assertions on both
   backends.
6. SQLite: table, index, `SchemaVersion` 4, `migrateV3ToV4`, the three methods,
   and the write-intent transaction that revalidates before inserting.
7. PostgreSQL: the same, with `COLLATE "C"`, `migratePostgresV3ToV4`, and the
   two ordered `SELECT … FOR UPDATE` statements.
8. `ControlPlane.Promote`, `Promotion`, `ProjectPromotions`, in the
   orchestration order above.
9. `httpapi`: three routes, DTOs reusing `gateResultDTO`, `classify` cases.
10. `cmd/trustvian/promotion.go` and its dispatch entry.
11. WebUI: environment list, promotion panel, promotion history.
12. The full test matrix above, on both backends.
13. Documentation and ADR 0040.

Steps 1–5 are one reviewable slice, 6–7 another, 8–9 another, and 10–11 each
their own. Nothing after step 5 is worth writing before the store conformance
suite — including the environment-staleness cases — is green on both
backends.
