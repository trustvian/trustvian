# 0048 — Retained observation history is sequence-identified, bounded, and honest about its own absence

**Status:** Accepted

## Context

Until task 067 the platform retained two things per evaluation run: a
fixed-shape aggregate and a behavior snapshot capped at 512 distinct
behaviors. Both are reductions, and neither can answer *which* observation
scored 0.91, when it happened relative to its neighbours, or what trace it
belonged to. Realtime carried that detail and dropped it when the connection
closed ([ADR 0032](0032-realtime-is-bounded-ephemeral-not-authoritative.md)).

Five earlier records named task 067 as the one place raw history may live
([0026](0026-evaluation-aggregation-is-bounded-evidence.md),
[0027](0027-behavioral-diff-compares-bounded-snapshots.md),
[0030](0030-local-persistence-stores-authoritative-bounded-state.md),
[0031](0031-control-plane-owns-ingest-and-http-is-an-adapter.md),
[0032](0032-realtime-is-bounded-ephemeral-not-authoritative.md)), and each
deferred three questions rather than answering them: what identifies a retained
observation, what bounds it, and what a run says when it has no history.

They are one decision because answering any two badly makes the third
unanswerable. An identity that depends on producer-supplied values cannot be
paged deterministically; a bound with no way to report saturation presents a
prefix as a whole; and a history that cannot say *absent* has to say *empty*,
which is a claim about the past that the database cannot support.

## Decision

### An observation is identified by `(RunID, Sequence)`, and ordered by it

`Sequence` is the ingest sequence the retry contract already assigns. It is
dense, monotonic per run, and allocated by the transaction that admits the
record — so it cannot collide, and it does not depend on any producer
behaving well.

**The three producer-supplied candidates are all refused as identity.** A
`SpanID` is unique only inside its trace. A `ParentSpanID` is a trace-scoped
reference that [ADR 0047](0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)'s
task explicitly forbids indexing. An `EventID` is caller-supplied and
unconstrained. Each would make identity a property of the telemetry rather than
of this platform's own admission decision.

**Ordering is by sequence, not by timestamp**, and the timestamp is retained
without being a sort key. Equal timestamps are ordinary at millisecond
resolution, and a tie under a timestamp sort makes pagination
non-deterministic — a page boundary that moves is the defect keyset pagination
exists to prevent. Producer clocks are also not authority. And a parent span
ends *after* the children it started, so sorting by time would reorder a trace
against its own arrival while [ADR 0047](0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)'s
task forbids inferring parentage from adjacency anyway.

The sequence is stored as fixed-width zero-padded text, so byte order and
numeric order are the same thing on both backends. A signed 64-bit column would
have been the obvious alternative and is the trap this schema refuses
everywhere else.

### Retention is bounded per run, and saturation is degraded evidence

`MaxRetainedObservations` is 4096 per run. Reaching it stops retention and
leaves the aggregate, the snapshot and the cursor advancing untouched.

This is the rule the behavior collector already applies at 512 distinct
behaviors, and the reason is the same: refusing the record would discard sound
*decision* evidence because *historical* evidence filled up, which inverts their
importance. A run whose history saturates is still a run whose verdict is
trustworthy.

### A run's history has three states, because two would force a lie

| State | Means |
|---|---|
| **Unavailable** | this run has records and none of their history was ever retained |
| **Complete** | every accepted record is retained |
| **Partial** | some are retained and some are not |

A schema-6 database's runs have aggregates and behavior snapshots and no
observations. Reporting "complete, zero rows" for one would be a fabricated
historical fact — the same refusal
[ADR 0040](0040-promotions-are-immutable-evidence-backed-platform-decisions.md)
made when its migration declined to synthesize promotions from old evaluations.

The distinction is drawn with no backfill and no new column on runs: a history
row is created by the first ingest under schema 7, so its **absence beside a
non-zero record count** is the evidence. A run that ingested nothing has no
history to be missing and is trivially complete.

**Saturation and a pre-retention prefix are one state, deliberately.** Both mean
*do not read this as the whole run*, no consumer can act on the difference, and
a state nobody can act on is one somebody eventually reads as "complete enough".

### The row is written inside the ingest transaction

Not beside it and not after it. [ADR 0031](0031-control-plane-owns-ingest-and-http-is-an-adapter.md)
established that evidence and cursor become durable together because the split
failure modes are silent: evidence without the cursor double-counts on retry,
and a cursor without evidence loses a record the protocol believes arrived. A
third artefact written outside that transaction reintroduces the same class of
bug — an observation with no aggregate to belong to, or an aggregate whose
history is short by one and reports itself complete.

### The retained field set is an allowlist expressed as columns

There is no attribute map, no span-event list, no link list and no payload
column. A field that cannot be declared cannot be written, which is a stronger
guarantee than a filter applied on the way past, and it keeps the record
boundary task 050 drew and
[ADR 0030](0030-local-persistence-stores-authoritative-bounded-state.md)
restated.

`Contributors` is excluded even though it is already on the record: it is its
one variable-length field, and retaining it would make a row's size a function
of producer behaviour.

## Alternatives considered

**A surrogate key — a UUID or an autoincrement.** Rejected: it is a second
identity for something that already has one, it needs its own ordering
guarantee, and an autoincrement is a signed 64-bit column on both backends.

**Ordering by `(timestamp, sequence)`.** Rejected. It reads as the more natural
order and is worse in the case that matters: it re-sorts a trace against its own
arrival, it makes the page key compound for no gain, and the timestamp
contributes nothing the sequence does not already order.

**Age-based retention in this task.** Deferred. The platform has no scheduler,
and adding one here would be a second capability wearing this task's number.
What this record does fix is that an absent history is *reported as absent*,
which is the only part a later sweep needs from it: a sweep sets the same flag
and needs no schema change.

**A configurable retention bound.** Deferred for the reason 512 and
`MaxListPage` are constants: a per-deployment retention setting is a decision
task 068 and task 071 should make with measured volume in hand, not one this
task should guess at.

**Letting saturation fail the ingest.** Rejected on the reasoning above, and it
would also make a run's verdict depend on how chatty it was.

## Consequences

Schema **6 → 7** on both backends, forward-only, adding two tables and three
run-scoped indexes and **no row**. Every index leads with `run_id` because every
access pattern task 082 stated is run-scoped; `parent_span_id` is not indexed,
because it is meaningful only beside a trace id and an index would suggest
otherwise.

One bounded read capability and one `/v1` route. **Nothing reads history back
into the engine** — a retained observation influences no decision, no baseline
and no fingerprint. History is evidence, not input, and that is what keeps this
change invisible to every behavioral outcome.

Task 085 can now link a finding to the observations behind it, and 076 can draw
a session, a trace and a timeline over them. Neither is delivered here.

Two stamping defects were found and fixed while landing this, in both backends'
`v5 → v6` step: each stamped `SchemaVersion` rather than the literal version it
produced, which was correct only while 6 was newest. Every step now stamps and
recovers against its own version, which is the trap
`migrateV1ToV2` had already documented and the reason the guard tests name
versions by constant rather than by "whatever is current".
