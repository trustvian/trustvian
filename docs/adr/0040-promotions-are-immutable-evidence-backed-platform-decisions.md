# 0040 — Promotions are immutable evidence-backed platform decisions

**Status:** Accepted

## Context

[Task 056](../tasks/v1.0/056-deterministic-hard-gates.md) made the platform
able to answer *did this candidate clear the limits* with a deterministic
PASS or FAIL. [Task 065](../tasks/v1.0/065-environment-model.md) and
[ADR 0039](0039-environments-are-project-owned-ranked-references.md) made it
able to answer *is `production` forward of `staging` in this project*. Neither
answer was written down anywhere. Ask the same question an hour later and the
platform recomputes it from whatever code is deployed then, against whatever
the environment registry says then.

That is the gap [task 066](../tasks/v1.0/066-promotion-workflow.md) closes,
and the reason it is a milestone of its own rather than a route on top of the
gate: it is the first layer permitted to conclude that a candidate may advance
between environments, and the last one that could plausibly be mistaken for
deploying something.

The mistake is the expensive one. A record named "promotion", holding a
candidate and a target environment, sitting next to a green check mark, reads
to almost everyone as *this candidate is now running in production*. Trustvian
has no deployer, no orchestrator, no cluster credential and — decisively — no
observer that could tell whether the thing it recorded ever happened. Anything
the record implies beyond *somebody decided this, on this evidence, at this
time* would be a claim the system cannot substantiate.

## Decision

### 1. A promotion records a platform decision, not a deployment

One sentence governs the whole milestone:

> Trustvian records a promotion decision. Trustvian does not deploy anything.

A `Promotion` is a durable, append-only record that a candidate's evidence was
evaluated against a gate, and that the gate's verdict authorized — or refused
— advancement from one environment to another. It is not a deployment, a
release, a rollout, an approval, an authorization, or a rollback. It triggers
no webhook and no CI/CD pipeline, and nothing in the platform acts on it.

The rule is enforced rather than asserted. `Deployment`, `Release`,
`DeployedCandidate` and `CurrentEnvironment` are absent by test, and the WebUI
and CLI carry absence tests forbidding "deployed to", "now running in", "is
live in", "released to" and "rolled out" as descriptions of a promotion. The
CLI prints the disclaimer directly: *Trustvian recorded this decision. Nothing
was deployed.*

### 2. Append-only and immutable, at every layer

`ControlStore` gained `CreatePromotion`, `Promotion` and `ProjectPromotions`
and nothing else. There is no `UpdatePromotion`, no `DeletePromotion`, no
status field and no lifecycle. A recorded decision is a historical fact about
a moment; editing one does not correct history, it destroys it.

The consequence is deliberate: a decision made in error is superseded by a
later decision, and both remain visible. Correcting the record in place would
leave an audit trail that cannot be distinguished from one that was never
wrong.

### 3. Candidate residence is not modeled

There is no `CurrentEnvironment`, on a candidate or anywhere else.

A mutable "this candidate currently lives in production" field is the natural
next thought, and it is exactly the claim section 1 refuses. Trustvian would
have to set it at the moment of the *decision*, because that is the only
moment it observes — and from then on it would be a guess served as fact, kept
accurate only by a deployment system Trustvian is not integrated with and
cannot see. It would be wrong the first time a deploy failed, was rolled back
outside Trustvian, or never ran, and nothing in the platform would notice.

What exists instead is history: the promotions recorded for a candidate. A
consumer that wants a current-state view can compute one from that history and
own the assumption it is making, which is the honest place for the assumption
to live.

### 4. Both verdicts are recorded; structural failures are not

`PromotionOutcome` has exactly two values, `accepted` and `rejected`. A gate
that returns FAIL produces a recorded `rejected` promotion — a real decision,
durably stored.

The alternative was recording accepted promotions only, treating a FAIL as
"nothing happened". It was rejected because the gate limits are
**caller-owned**: the requester supplies them. If only acceptances were
recorded, a caller could retry with progressively looser limits until one
passed, and the history would show a single clean acceptance with no trace of
the attempts that preceded it. Recording both verdicts makes limit-shopping
visible, which is precisely what an audit trail is for.

A *structural* failure is not a decision and is not recorded: unknown runs,
runs from two different agents, a target that is not forward of the source, a
malformed request. Nothing was decided, so nothing is written. The line is
whether the gate ran: a gate verdict is history, a rejected request is an
error.

### 5. Same-agent evidence is required, while `CompareEvaluations` stays same-project

A promotion requires both evaluation runs to belong to the same agent.
`CompareEvaluations` — task 065's rule — continues to require only the same
project.

The two are different questions. Comparing behavior across two agents in a
project is a legitimate analysis. Concluding from it that *this candidate may
advance* is not: the evidence would describe two different programs, and the
resulting record would assert something about a candidate that the comparison
never examined. Narrowing the comparison itself would have removed a
capability nothing asked to remove, so the constraint lives where it is
load-bearing.

### 6. The decision-time gate result is snapshotted, field for field

This is the security-critical decision of the milestone.

A stored `Promotion` contains the exact `EvaluationGateResult` the decision
consumed — every count, every limit, every per-check `Passed` flag, and the
verdict — persisted in explicit columns and restored without recomputation.
`restoreEvaluationGateResult` calls neither `EvaluateEvaluationGate` nor the
`minimumGate`/`maximumGate` helpers, and derives no flag from its own stored
operands.

The tempting argument was "the result is derived from immutable evidence,
therefore it is recomputable, therefore storing it is redundant". It does not
hold. A recomputation answers *what does today's build decide*, and the stored
record has to answer *what did the platform rely on at the time*. A bug fix in
the gate or the scorecard may legitimately answer the same immutable evidence
differently — that is what a bug fix is — and history has to survive its own
corrections. An audit trail that silently re-derives itself is not an audit
trail.

Deriving only the `Passed` flags from their own stored operands was considered
separately and rejected for a sharper reason: a corrected helper would then
return new flags beside an old stored verdict, producing a value that is
neither the historical result nor a current one. There is no reading of such a
record that is true.

The rule that decides what is stored how:

| Kind | Treatment | Why |
|---|---|---|
| The gate result and its limits | **Snapshotted** | What the decision consumed. Must survive a correction to the code that produced it |
| Environment rank and revision at decision time | **Snapshotted** | The ordering the decision relied on, which a later re-rank must not silently rewrite |
| Runs, candidates, project | **Referenced** | Immutable identities; a reference cannot drift |
| Anything else | **Recomputed on read** | Not evidence |

What is *not* rewritten is as important. A restored row that disagrees with
today's arithmetic — `actual = 1`, `minimum = 1`, `passed = false` — restores
exactly as written. Only a row that cannot be read at all is corruption; a row
that merely disagrees with the current build is history.
`TestRestorePreservesAHistoricallyInconsistentCheck` and
`TestRestoreDoesNotRecomputeTheVerdictFromTheFlags` hold this executably.

### 7. The outcome is derived from the stored verdict, never supplied

`outcomeFor` is the only place in the codebase where `accepted ↔ PASS` and
`rejected ↔ FAIL` are written. No caller, adapter, HTTP body or control-plane
parameter can supply an outcome: `PromotionDecision` has no `Outcome` field,
and strict decoding rejects one in a request.

The one cross-check applied on read is that invariant. An `accepted` promotion
carrying a FAIL gate result is unconstructible through the domain, so a row
holding one was written by something this code is not, and it is reported as
corruption rather than repaired. Repairing it would pick one of two mutually
contradictory claims about a security decision and present the guess as the
record.

### 8. `CanPromote` remains the only ordering primitive

Task 065 built one function that answers whether one environment is forward of
another. Task 066 calls it from three places — the domain constructor, the
control-plane service, and the store's commit-time revalidation — and
re-derives the comparison nowhere. No rank arithmetic exists in the HTTP
layer, the CLI, or the WebUI's JavaScript.

`CanPromote` authorizes nothing, which ADR 0039 already recorded. It answers
an ordering question; the gate decides the outcome.

The source environment is likewise **inferred**, from the environment the two
runs share, never supplied. A caller-supplied source could disagree with the
evidence, and the record would then describe a promotion from somewhere the
evaluation never ran. Strict decoding rejects a `source_environment` field.

### 9. A promotion commits only against the environment state it was decided against

The invariant spans three rows: the two environment rows the decision was made
against, and the promotion row being written. A decision built against
`staging` at rank 30 must not commit after somebody re-ranked `staging` to 45,
because the ordering it relied on no longer holds.

`CreatePromotion` is therefore a transaction that authoritatively re-reads
both environments, compares both stored revisions against the ones the
decision recorded, re-asks `CanPromote` on the freshly read values, and only
then inserts. Anything moved → `ErrStoreConflict`, and no row is written.

**Any** revision change invalidates the attempt, a rename included. The
alternative — comparing only the fields promotion "cares about" — would
require the store to know which environment changes are semantically relevant
to a promotion, which is domain logic in the persistence layer and a judgment
that would need revisiting every time `Environment` gains a field. The
revision is a single monotone fact about *has this row changed*, and treating
it as such keeps the store free of gate and ordering semantics.

### 10. Atomic correctness is the shared contract; writer concurrency is per-backend

Both backends guarantee the same thing: the re-read, the revision comparison,
the `CanPromote` re-ask and the insert are one write decision, with no window
in which another writer can change an environment in between.

How they guarantee it differs, and the ADR records the difference rather than
claiming identical locking:

**PostgreSQL** takes `SELECT … FOR UPDATE` row locks on the two environment
rows, issued as two statements in `(project_id, ref)` byte order. The lock is
two rows — never the project row, never the table — so promotions in different
projects and over disjoint environment pairs make genuinely independent
progress, while promotions sharing an environment serialize on that row and no
other.

The byte ordering deserves a precise note, because the obvious justification
for it is wrong. Locking source-then-target would *also* be deadlock-free
today, and provably rather than by luck: `CanPromote` requires the target to
rank strictly above the source, so every transaction takes its two rows in
increasing rank order and two transactions can never take the same pair in
opposite orders. No cycle is reachable. But that proof belongs to
`CanPromote`, not to the store, and it lapses the moment the promotion rule
admits anything that is not strictly rank-forward. Byte order makes deadlock
freedom a property of the two rows, it survives a change to the promotion
rule, and it costs one comparison.

**SQLite** enters an explicit write transaction and serializes writers
database-wide. That is accepted for the local backend, and it is stated
plainly rather than dressed up: SQLite has no row-level writer concurrency
here, and **no task 066 test asserts non-blocking behaviour on it**. The
SQLite tests assert correctness under a multi-connection pool — that write
intent is taken before reading, that a second writer which blocks still
observes authoritative state once it proceeds, and that it either commits or
reports `ErrStoreConflict`. Claiming parallelism the backend does not offer
would be a promise the next person to raise the pool size would discover was
false.

No process-local mutex exists on either path. A mutex would be correct in one
process and silently wrong in two, which is the failure mode that looks fine
in every test.

### 11. Stage skipping is permitted

`sandbox → production`, skipping `staging`, is a valid promotion if the ranks
are forward and the gate passed.

An adjacency rule — *you may only promote to the next rank* — was considered
and rejected, because it cannot be specified without answering five questions
that have no consumer to answer them for: what "next" means when ranks are
sparse (10, 20, 40); what happens when two environments share a rank; whether
an archived environment still occupies a position in the sequence; whether a
hotfix path may bypass it and who authorizes that; and what a project with one
environment means. Every answer is a policy, and policy without a consumer is
guesswork encoded as a constraint. `CanPromote` says forward; forward is the
rule.

### 12. No human approval, authorization or actor

There is no `approved_by`, no actor identity, no RBAC, no user or session
authentication, and no approval state. Task 066 records what the platform
decided on evidence, and the platform does not know who asked.

A nullable or client-supplied actor label was considered and is worse than
none. An audit field nobody authenticates is a field that can say anything,
and its presence invites readers to treat it as attested when it is not.
Authentication is its own milestone; until it exists, the honest record is one
that makes no claim about who.

### 13. No platform concept enters the engine

Unchanged, and worth restating because promotion is the milestone most likely
to be the exception. `event.Event`, `StableFeatures`, `Fingerprint`,
`Baseline`, `Anomaly`, `Trust`, `Policy`, `Engine`, `Analyze`, `Observe` and
learning-scope semantics are untouched. There is no `PromotionEngine`. The
engine does not know promotion exists, the store owns no gate logic, and the
HTTP, CLI and WebUI adapters own no promotion logic of their own.

## Alternatives considered

**Model the deployment.** Rejected on the central rule: Trustvian has no
observer that could confirm a deployment happened, so any such record would be
a claim it cannot substantiate. See sections 1 and 3.

**Recompute the gate result on read.** Rejected in section 6. It answers a
different question from the one an audit record exists to answer.

**Record acceptances only.** Rejected in section 4: with caller-owned limits,
a history of successes only is a history that hides limit-shopping.

**A generic `PromotionStore`, `Repository` or `Database` abstraction.**
Rejected as the premature abstraction CLAUDE.md names directly. `ControlStore`
gained three methods that say what promotion actually needs, matching the
narrow-port convention ADR 0004 established and every store ADR since has
kept.

**Idempotent `POST` by request-body comparison.** Rejected. A duplicate
`PromotionID` returns `409 already_exists` and the client GETs the existing
record. Comparing bodies to decide whether two requests "mean the same thing"
would make identity depend on field-by-field equality of a payload that
includes caller-chosen limits — so two genuinely different decisions could
collide, and one would be silently discarded. `PromotionID` is the only
identity, which is ADR 0025's caller-owned-identity rule applied unchanged.

**Synthesize promotions for pre-existing evaluations during migration.**
Rejected outright, and the v3 → v4 migration asserts an empty history. Every
completed evaluation in a v3 database was gated by something, but nobody
decided to advance any of them. Turning gate results into promotion rows would
fabricate an audit trail of decisions that were never made — the single most
damaging thing this milestone could do.

## Consequences

`SchemaVersion` is **4** on both backends. The v3 → v4 migration creates
`platform_promotions` and its by-project index and stamps 4; it writes no
rows. A v4 database opened by a v3 binary is refused, as every version step
here is. Task 074 owns 4 → 5, which is why task 066 lands first.

The platform gains three routes — `POST /v1/promotions`,
`GET /v1/promotions/{promotion_id}`,
`GET /v1/projects/{project_id}/promotions` — the CLI gains
`trustvian promotion create|get|list`, and the WebUI gains the promotion slice
[task 063](../tasks/v1.0/063-minimal-web-control-plane.md) reserved. The CLI
family is `promotion`, not `promote`: the noun names a record, the verb would
name an action Trustvian does not perform. Exit code `1` keeps its existing
single meaning — `trustvian eval compare` gate FAIL — and a rejected promotion
does not reuse it, because a recorded rejection is a successful API call.

`ProjectPromotions` is bounded at `MaxPromotionPage` (64) and pages on the
immutable `PromotionID` in byte order, reusing task 065's pagination shape
rather than inventing a second one.

The TUI gained nothing. It observes; it does not mutate.

A consumer that wants "where does this candidate live now" must compute it
from promotion history and own that assumption. That is the intended
consequence of section 3, not an omission to fill in later.
