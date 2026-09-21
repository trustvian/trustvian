# 0027 — Behavioral diff compares bounded snapshots

**Status:** Accepted

## Context

[ADR 0026](0026-evaluation-aggregation-is-bounded-evidence.md) made
`EvaluationAggregate` fixed-shape and O(1) in the record count, and named the
bill this task would have to pay:

> Task 054 cannot ask this aggregate what behaviors occurred, because it does
> not remember. It needs its own input contract with its own bound — a capped
> set of fingerprints, a sampled window, or a comparison computed at ingest.
> That is more work than reading a retained slice, and it is the work that
> keeps a diff over a long evaluation from costing memory proportional to the
> evaluation.

A behavioral diff genuinely needs per-fingerprint state. A result summary
genuinely does not. The question this ADR settles is where that state lives
and what stops it growing.

The obvious shortcut is to relax ADR 0026 — let the aggregate keep a
fingerprint map, since it is already touching every record. That would make
this task trivial and would be wrong twice over: every evaluation would pay
for behavioral bookkeeping it may never compare against, and the aggregate
would stop being the fixed-shape value that persistence, transport and
scorecards are all being designed against.

## Decision

**Behavioral diff compares explicit bounded snapshots. It does not consume
raw event history, and it does not make `EvaluationAggregate` retain
per-behavior state.**

Three separate concepts:

```text
DecisionRecord ──▶ BehaviorCollector ──▶ BehaviorSnapshot ──▶ BehaviorDiff
                   bounded, mutable      detached, sorted     bounded, factual
```

`EvaluationAggregate` is untouched. Task 053 answers *what happened*; this
answers *which behaviors happened*. They consume the same stream and share no
state, which is what lets a caller run either, both, or neither.

### Per-behavior state is necessary, and therefore bounded

A diff cannot be computed from counters. It needs the set of distinct
fingerprints and, for explainability, each one's stable descriptor.

That set is keyed by a value the *event producer* controls, which is the same
hazard [ADR 0019](0019-bounded-fingerprint-admission.md) found in the engine's
baseline. So it carries the same kind of cap: **512 distinct fingerprints**.

The number is reused deliberately but the constant is not. The platform
declares its own, because it cannot import `internal/baseline` and should not
compile against a core constant even if it could — the two bounds answer
different questions (what one actor's baseline will learn; what one evaluation
will compare) and must be free to move apart.

### Saturation cannot be silent, and cannot be compared

The engine's bound *refuses and keeps going*: an actor past 512 fingerprints
still gets analyzed, because refusing to decide would be worse than learning
nothing new.

A diff cannot take that stance. Its headline output is which behaviors are
new, and an evaluation that stopped collecting at 512 produces a confident,
specific, wrong answer to exactly that question — under-reporting additions in
precisely the runs whose behavioral surface is widest.

So saturation is explicit, sticky, and fatal to comparison. The collector is
marked incomplete permanently, a snapshot reports `Complete() == false`, and
comparison refuses it outright. A snapshot is still obtainable for
diagnostics, because knowing an evaluation saturated is useful; deriving a
number from it is not.

An error a caller must handle is strictly better than a plausible wrong
number nobody questions.

### Identity and descriptor are one-to-one — or nothing

```text
same FingerprintID, different StableFeatures  → refused
same StableFeatures, different FingerprintID  → refused
```

Both directions fail closed, in the collector and again at comparison.

The first prevents two distinct behaviors being reported as one. Overwriting
the descriptor or merging the counts would do exactly that, and the realistic
causes — a tampered record, corrupted evidence, an identity collision — all
warrant refusal rather than a merge that looks like a normal result.

The second prevents the same behavior appearing under two identities, which
happens when evidence crosses identity encodings: a changed hash, mismatched
namespaces, assembled records. Classified naively it becomes `Removed(old)` +
`Added(new)` — a specific, confident claim that behavior changed when only its
encoding did, in the output most likely to be acted on.

The platform checks the relation, never the hash. Recomputing
`hash(StableFeatures)` here would freeze the core's choice of algorithm, its
version prefix, and its serialization; what is verified is that the evidence
handed over is self-consistent.

### Raw records are not retained

No `DecisionRecord`, `Event`, contributor, attribute, timestamp list, or event
identifier survives into a collector or snapshot. What is kept is a
fingerprint, its descriptor, and a count.

This keeps [task 067](../tasks/v1.0/README.md)'s event-history capability the
only place raw history may live, and keeps it an explicit choice rather than a
side effect of having compared something. It is also why duplicates count
twice: detecting a repeat needs every identifier remembered, which is the
unbounded structure this ADR exists to exclude.

### Presence and frequency are evidence, not a score

A diff reports `Added`, `Removed`, `Shared`, counts, normalized rates, and a
signed rate delta. It reports no `DriftScore`, `Severity`, `Passed`, or
`Promotable`, and applies no threshold — not even one deciding that some
frequency shift is "changed" rather than noise.

An added behavior is not a defect. It is frequently the feature the candidate
was built to add. Deciding whether a change is acceptable needs thresholds,
and thresholds are configuration belonging to the gate task; putting one here
would make the component that counts facts also the component that renders
verdicts, and the verdict is what people would read.

[Task 055](../tasks/v1.0/README.md) consumes this as scorecard input.
[Task 056](../tasks/v1.0/README.md) may build the roadmap's
`new_behavior_count <= threshold` gate on `AddedCount`. Neither belongs here.

## Alternatives considered

**Let `EvaluationAggregate` keep a bounded fingerprint map.** Rejected. It
reverses ADR 0026's shape for every caller in order to serve one, makes every
evaluation pay for bookkeeping it may never use, and would mean the value task
057 persists and task 058 serves is no longer the fixed-shape thing they were
designed around.

**Compute the diff at ingest, incrementally, against a reference.** Rejected.
It requires the reference to exist and be chosen before the candidate runs,
which forecloses comparing two completed runs after the fact — the common
case. It also makes the reference a live dependency of ingest.

**Sample: keep the N most frequent behaviors.** Rejected, and it is the most
tempting alternative because it never errors. A sampled diff answers about a
window while looking like it answered about the run, and the behaviors it
drops are the rare ones — which are exactly the ones a security reviewer cares
about. A truncated answer to "what is new" is worse than a refusal.

**Cap by memory rather than by count.** Rejected. A byte budget makes the
admitted set depend on how long the operation names happened to be, so the
same evaluation could saturate or not depending on unrelated naming. Bounding
the count and bounding each string independently is predictable.

**Treat a differing `BehavioralProfileRef` as incomparable.** Rejected.
[ADR 0024](0024-learning-scope-is-a-baseline-key-dimension.md) deliberately
kept learning scope out of behavioral identity, so two runs under different
profiles that observe the same behavior produce the same fingerprint.
Requiring the profiles to match would make isolation look like behavioral
change — inverting the property task 051 built.

## Consequences

Comparison is cheap and bounded: at most 1024 deltas, computed from integer
counts with no accumulated floating-point state. The same counts give
bit-identical rates regardless of arrival order — a stronger determinism
property than ADR 0026's float sums could offer.

The cost is that an evaluation observing more than 512 distinct behaviors
cannot be diffed at all. That is intended, and it is the honest outcome: such
a run is past the point where "which behaviors are new" is a question a
bounded comparison can answer truthfully. Raising the bound is a decision with
a memory argument attached; silently answering anyway is not.

`EnvironmentRef` must match across a comparison, because environment is a
stable fingerprint dimension. Cross-environment comparison would classify
every behavior as simultaneously added and removed, which is arithmetically
correct and completely useless.
