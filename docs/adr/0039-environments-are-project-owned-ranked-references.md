# 0039 — Environments are project-owned ranked references

**Status:** Accepted

## Context

[Task 052](../tasks/v1.0/052-evaluation-domain.md) gave an `EvaluationRun` an
`EnvironmentRef` — an opaque string saying *this run targeted environment X* —
and deferred the model to a later milestone. Three years of that deferral fit
in one sentence each:

A typo was an environment. `POST /v1/evaluation-runs` with
`"environment": "stagin"` created a perfectly valid run, and every comparison
bounded by that reference then described a population of one. Nothing was
positioned to notice, because nothing knew which environments a project had.

Promotion had nothing to order. [Task 066](../tasks/v1.0/README.md) moves a
candidate between environments on evidence, and `sandbox → staging →
production` is a sequence somebody has to have written down. Deriving it from
names would make `prod-eu` and `production` unrelated strings.

And the roadmap already said a project owns environments, while the project
owned only agents. An environment that became global state would make
`staging` mean one thing across every project sharing a PostgreSQL backend —
the multi-tenancy this milestone does not have, arriving by accident.

[Task 065](../tasks/v1.0/065-environment-model.md) is the specification this
records.

## Decision

### 1. Identity is `(ProjectID, EnvironmentRef)`, and no `EnvironmentID` exists

The registry is keyed by the reference runs already carry.

Four places had already made that reference the environment's identity:
`EvaluationRun` persists it; `EvaluationAggregate.AddRecord` compares a
`DecisionRecord`'s environment against it byte for byte and refuses a
mismatch; every snapshot, diff, scorecard and gate result is bounded by it;
and `/v1` publishes it as a STABLE field name.

A separate identifier would have needed a translation on the per-record
ingest path — a lookup per record, or a second stored copy of the ref to
compare against — and a translation is a thing that can disagree with itself.
The `*ID` naming of other entities is a convention, not a reason.

Uniqueness is **per project**. Global uniqueness would let the first project
to create `staging` take the name from every other one. The consequence is a
route shape: reading one environment needs its project, so the path carries
both, unlike the flat `/v1/agents/{agent_id}` whose IDs really are globally
unique.

### 2. The set of references stays open

`local`, `sandbox`, `staging`, `production` are plausible values, not a closed
set. Task 052 said so when there was no registry to tempt anyone; now that
there is one, it is worth restating as a rule. The registry says which
environments *a project* has, not which environments *exist*.

### 3. Ordering is an optional per-project integer rank, and nothing else

```go
func CanPromote(from, to Environment) bool
```

True iff both belong to one project, both are active, both are ranked, and
`to`'s rank is strictly greater.

Rejected: a directed graph of allowed transitions, which is a workflow DAG
whose questions — cycles, reachability, whether a two-edge path is one
promotion or two — task 066 would have to answer anyway and none of which is
answerable now. Rejected: predecessor/successor pointers, which break on two
environments at one stage, on archiving one in the middle, and on inserting
one. Rejected: an ordered enum, by decision 2. Rejected: ordering derived from
names.

A rank is an integer an operator chooses, stored on the row that owns it,
mutable without touching any other row. Unranked is a real state, not rank 0:
an environment with no position hosts runs normally and participates in no
promotion, which is what lets migration preserve history without inventing an
order. Two environments may share a rank; that means peers, and a peer is not
a next stage.

### 4. Ordering is not authorization

`CanPromote` answers "is B forward of A". Whether a candidate *may* move —
on what evidence, with whose approval, and with what recorded — is task 066's
entirely. A gate PASS is not a promotion either
([task 056](../tasks/v1.0/056-deterministic-hard-gates.md) made that point at
the other end).

A forward jump over an intermediate rank is *ordered*. If a workflow decides
promotions may not skip a stage, it implements that from these same ranks as
its own policy, without a second ordering model.

### 5. Archive, never delete

There is no delete at any layer — not in the store, the service, `/v1`, or
the CLI. A hard delete of an environment historical runs reference would
leave those refs resolving to nothing and every scorecard bounded by one
describing an environment the platform can no longer name.

An archived environment accepts no new runs, is neither a promotion source
nor a target, remains readable, configurable and re-activatable, and keeps
every historical reference resolvable. Archiving destroys nothing, so
un-archiving restores nothing that was lost.

Retention — whether a platform may ever forget an environment — is a real
question with no current consumer, and it belongs with whatever first needs
to delete anything at all.

### 6. A run stores the immutable reference, never a snapshot

`EvaluationRun` is unchanged: it records an `EnvironmentRef` and nothing else.

`ref` and `projectID` are immutable, so a completed run's environment cannot
become a different one. `name` is read by humans and by nothing that decides
anything. `status` describes the present. And `rank` must be read *live* by
whatever promotes, so copying it onto a run would freeze the one field that
has to move.

A snapshot would therefore duplicate fields nothing reads and freeze the one
that must not be frozen. What genuinely needs historical fidelity is a
promotion — "this candidate moved on this evidence, when the order was this" —
and that belongs on the promotion record task 066 owns.

### 7. The platform's environment and the core's remain separate concepts

The core has an environment too: `baseline.Key.Environment`, a behavioral
dimension set from `event.Context.Environment`. It is not this entity, and
this task does not make the engine aware of one.

They meet at a **string comparison that already existed**, entirely on the
platform side:

```text
core     DecisionRecord.Environment   (from baseline.Key.Environment)
                    │  compared, never translated
platform EvaluationRun.Environment()  (EnvironmentRef)
                    │  resolved once, at run creation
platform Environment{project, ref}
```

`AddRecord` and the behavior collector keep comparing strings and gain no
store. Resolution happens exactly once, in `CreateEvaluationRun`. No core
package imports the platform, and the engine still receives an opaque string
its caller chose.

### 8. The creation cap bounds growth; migration preserves history

A project may create at most 64 environments. Schema 2 had no registry and no
cap, so a database migrating to schema 3 may legitimately hold more, and the
v2 → v3 backfill preserves every distinct `(project, environment)` pair its
runs reference — nothing dropped, merged, renamed, archived or ranked.

So the cap governs **creation, not existence**. A project above it keeps every
row readable, configurable, archivable and enumerable, and refuses only the
creation of refs it does not already have. Dropping the excess would orphan
the evidence naming it; failing the migration closed would brick a valid
database with no remediation this design has an operation for.

### 9. Creation serializes on the owning project row

The cap is a cross-row invariant, so no predicate on the inserted row can
hold it: two transactions can each count 63, each insert a different ref, and
each commit against an intact primary key.

`CreateEnvironment` therefore runs one transaction that locks the owning
project row — `SELECT … FOR UPDATE` on PostgreSQL, a write-intent touch of
the same row on SQLite, whose `BEGIN IMMEDIATE` equivalent is taken before
anything is counted rather than relying on `SetMaxOpenConns(1)`. Rejected: a
counter table, which adds a row that can disagree with the rows it counts;
and a table-level lock, which would serialize unrelated projects.

Inside that transaction the checks are **ordered**: missing project, then
existing identity, then the cap. A ref the project already has is
`ErrStoreAlreadyExists` whatever the count — that request adds nothing — while
a previously-absent ref at or over the cap is `ErrEnvironmentLimit` however
many callers ask for it at once. Sharing a name with another caller creates no
room.

### 10. The collection is bounded twice, and paginates on the immutable key

`GET /v1/projects/{project_id}/environments` is the platform API's first
collection route — the concrete filtering requirement
[task 063](../tasks/v1.0/063-minimal-web-control-plane.md) deferred its design
to.

Two bounds, for two different risks: creation is capped at 64 per project, and
a response carries at most 64 rows per page. The second does not follow from
the first, because migration can produce a larger project than anyone may
create.

Traversal is by `ref`, not by rank. Rank is mutable and re-ranking is one of
the two operations this entity exists for, so a cursor over rank could move a
row from a page the caller has not read into one it already read. `ref` is
immutable and unique inside the project the path already names, so enumeration
is exact under concurrent renaming, re-ranking and archiving — and the cursor
never assumes a ref is unique anywhere else. Every row carries its rank, and
`CanPromote` stays the only answer to precedence; sorting rows for display is
presentation.

## Consequences

An `EvaluationRun` can no longer name an environment that does not exist,
which is a behavioural change for any caller that relied on the old
permissiveness. Existing databases keep working because the migration
backfills what their runs referenced; a *new* project needs its environments
created before its first run, which `trustvian env create` and
`POST /v1/environments` exist for.

`CompareEvaluations` now requires both runs to belong to one project. Making
refs explicitly project-scoped turned the existing ref-equality check into a
hazard — two projects may each own a `staging` — and the ownership check is
what makes the precondition mean what it always intended. Same-project
comparisons across two agents remain allowed; a narrower agent rule, if
promotion needs one, arrives with promotion.

`SchemaVersion` is 3 on both backends. A v3 database opened by a v2 binary is
refused, as every version step here is.

Task 066 can now ask one question and get a deterministic answer, and has to
invent no environment model to do it.
