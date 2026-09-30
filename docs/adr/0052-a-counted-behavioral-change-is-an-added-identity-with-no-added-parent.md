# 0052 — A counted behavioral change is an added identity with no added parent

**Status:** Accepted

## Context

[Task 083](../tasks/v1.0/083-behavioral-layer-classification.md) shipped the
classification half of its work and deferred the counting half. The defect it
recorded is small to state and easy to reproduce:

```text
a developer adds one tool
  → the tool span            tool · export_customer
  → its transport child      http · POST → export.localhost
a comparison reports         added 2
```

Two spans observe one act. [ADR 0047](0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
decided they stay two behavioral identities, and gave the reason: the transport
target is the security-relevant part, so folding identity would make
`export_customer` switching to `attacker.example` invisible. Identity is
therefore settled, and counting is where the correction belongs.

083 left four questions open, and [084](../tasks/v1.0/084-correlation-operational-evidence.md)
has since supplied the input the answer needs — a record now carries
`ParentSpanID` and `SpanLineage`. What was still missing was a rule.

Three properties constrain any rule:

1. **`max_added_behaviors` already exists and is already in use**, in stored
   promotions and in callers' pipelines. Redefining what it counts would change
   every existing gate's meaning silently and retroactively.
2. **Undercounting is dangerous in a way overcounting is not.** A count that is
   too low turns a real change into a PASS. A count that is too high is an
   annoyance a caller can see and raise a limit for.
3. **The behavioral layer is not persisted** (083, and 081 owns per-behavior
   persistence). A rule that needed to know "this is a transport span" at
   comparison time could not be evaluated from durable state.

## Decision

### The unit: a counted change is an added identity with no added parent

> A **counted behavioral change** is an added behavioral identity that is not
> the recorded child of another added behavioral identity of the same run.

Behavioral identities are unchanged. Counting happens over the *added* set of a
comparison, using recorded parentage, and the rule is structural — it never asks
what layer an observation came from, which is what makes it evaluable from
durable evidence.

```text
added identities        {tool·export_customer, http·POST→export.localhost}
recorded edge           http·POST→export.localhost   child of   tool·export_customer
roots (no added parent) {tool·export_customer}
counted changes         1        contributing identities: both
```

**Root-anchored, not connected components.** The obvious alternative — count
connected components of the added subgraph — undercounts a real case: two new
tools that both call one shared new transport identity form a single component
and would report one change where a developer made two. Anchoring on roots
reports two, with the shared child contributing to each. Overlapping
contribution sets are honest; a merged count is not.

**A known tool changing destination still counts.** This is the case ADR 0047
protected and it survives unchanged:

```text
reference   tool·export_customer  +  http·POST→export.localhost
candidate   tool·export_customer  +  http·POST→attacker.example

added       {http·POST→attacker.example}
its parent  tool·export_customer — shared, not added
roots       {http·POST→attacker.example}
counted     1
```

The parent is present in both runs, so it is not *added*, so the transport
identity is a root and is counted. The rule folds a change into its parent only
when the parent is itself new.

### Which relationships qualify

Exactly one: the `ParentSpanID` a record carries, resolved **within the same
run and the same `TraceID`**, to another observation of that run.

Never inferred from timing, adjacency, name similarity, matching destinations or
ingestion order — the prohibition 084 states, restated here because counting is
the first consumer with a motive to break it.

`ParentSpanID` is trace-scoped and is not a key on its own (084). The resolution
key is the pair `(TraceID, SpanID)`, so a parent reference that names a span in
another trace, or in another run, resolves to nothing and folds nothing.

### Repeated execution is not change

Counting is over the *added set of a comparison*, which is a set of identities.
A tool called a thousand times contributes one identity and at most one counted
change. Retries, loops and concurrent calls of an identity already present in
the reference are not added at all, so they are not counted. Frequency movement
is `BehaviorDelta`'s `RateDelta` and is deliberately not a change count.

### Every unresolved case counts more, never less

| Case | Resolution | Direction |
|---|---|---|
| Child arrives before parent | Order-independent: edges resolve over the whole retained history, not as records stream | — |
| Parent never observed, or sampled away | Edge resolves to nothing; the child is a root | counts |
| Parent observed but not *added* | Not an added-parent; the child is a root | counts |
| `(TraceID, SpanID)` names two different identities | Ambiguous; the edge is refused and correlation is marked incomplete | counts |
| Duplicate observation of one span, same identity | One node; no effect | — |
| Parent reference into another trace or run | Does not resolve | counts |
| Cycle among added identities | Every participant is treated as a root and correlation is marked incomplete | counts |
| Nested calls | Each level folds into its own added parent; the outermost added identity is the root | folds |
| Concurrent calls | Independent edges; no ordering assumption anywhere | folds |

The column on the right is the safety argument. **Folding only ever lowers the
count**, so every refusal to fold moves the result toward the identity count —
the number this repository has always reported. An unresolved correlation can
make a comparison stricter than it needed to be. It cannot make one pass that
should have failed.

### Correlation completeness is the retained history's, and it is reported

Correlation is computed from retained per-observation history (task 067), which
already stores `trace_id`, `span_id`, `parent_span_id` and `fingerprint_id` in
both backends, and which already reports what it is:

The state is the **candidate run's**, not both runs'. The added set comes from
the two behavior snapshots, which `CompareBehaviorSnapshots` already requires to
be complete; only the edges come from observations, and only added identities
have edges that matter. Consulting the reference's retention too would mark
comparisons partial for a reason that does not exist, and a state reported
pessimistically everywhere is a state nobody reads.

| `ObservationHistoryState` (candidate) | Correlation state | Counted changes |
|---|---|---|
| `complete` | `complete` | folded |
| `partial` — retention saturated at `MaxRetainedObservations`, or the run predates schema 7 | `partial` | **not folded**: equals the added identity count |
| `unavailable` — ingested before schema 7 | `unavailable` | **not folded**: equals the added identity count |

Saturation therefore needs no new bound and no new saturation semantics: the
retention bound *is* the correlation bound, and a history that stopped early is
already a state this platform models and reports. Adding a second bound with its
own saturation rule would be a second thing to reason about that says the same
thing less well.

A comparison whose correlation is not `complete` reports the identity count as
its change count and says why. It does not refuse — refusing would make every
pre-schema-7 run uncomparable, which is a regression for a correction.

### What the API reports

`BehaviorDiff` gains four readers and loses none:

```go
AddedChangeCount() int            // counted changes
AddedChanges() []BehaviorChange   // each root and the identities contributing to it
CorrelationState() CorrelationState // complete | partial | unavailable
CountingPolicyVersion() string    // "1"
```

`AddedCount()` keeps its exact meaning: **added behavioral identities**. Every
existing caller, stored promotion and historical run is unaffected.

`CountingPolicyVersion` is reported so a result carries the rule that produced
it. A future rule change increments it rather than silently reinterpreting
stored results.

### The gate limit is new, separate and optional — specified here, not yet built

`max_added_behaviors` continues to bound added *identities*.

A second limit, `max_added_behavior_changes`, bounds counted *changes*. It is
**optional**: absent means the gate is not evaluated, and a result says so.

This is the one place the platform's "zero is a legitimate maximum, so there is
no unset" rule (`EvaluationGateLimits`) is departed from, and the reason is
compatibility rather than taste. The existing three limits have no absent state
because none was ever needed. A fourth mandatory limit would default to zero for
every existing caller and fail every candidate that added anything — a silent,
retroactive policy change of exactly the kind this ADR exists to avoid. So the
new limit is a pointer, its absence is a documented state, and the other three
are untouched.

**A limit whose unit depends on a flag is a limit nobody can read** — 083's
fourth open question. The answer here is that there is no flag: two limits, two
fixed units, both named for what they count.

**This limit is specified and not yet implemented.** Counting, reporting and the
evidence links ship first; the limit needs new columns on `platform_promotions`
for its threshold and outcome, a schema migration, both backends and a
historical-data rule for promotions recorded before it existed. That is a
separable piece of work with its own persistence risk, and bundling it would
mix a counting correction with a storage migration in one review. Until it
lands, `max_added_behaviors` is the only gate over behavioral change, it counts
identities exactly as it always has, and a counted-change count is reported as
evidence a caller can read. Tracked as [issue 131](https://github.com/trustvian/trustvian/issues/131).

## Alternatives considered

**Redefine `max_added_behaviors` to count changes.** The smallest diff and the
one the defect report suggests. Rejected: it silently changes what every stored
promotion and every caller's pipeline asserted, in the permissive direction, and
gives no way to tell an old result from a new one.

**Fold behavioral identity.** Refused by ADR 0047 and re-refused here; it is the
detection hole the whole design is arranged around.

**Correlate during ingest and persist the edges.** Considered seriously, and it
is what 083 anticipated ("a bounded correlation structure… a bound has to be
chosen, and saturation stated"). It needs a new bound, new saturation semantics,
a new table, a schema migration and backfill reasoning for historical runs —
all to derive something already recoverable from rows both backends store. The
retained history already carries the parentage and already says how complete it
is, so the only honest reason to add the structure would be if retention were
unavailable, and then the correlation would be untrustworthy anyway.

**Use the behavioral layer to decide what folds.** The intuitive rule — "a
transport child folds into its tool parent" — and unavailable: the layer is not
persisted. Structural parentage turns out to be the better rule regardless,
because it does not have to be taught which layers exist.

**Refuse to compare when correlation is incomplete.** Symmetrical with
`CompareBehaviorSnapshots` refusing saturated snapshots, which was the
precedent. Rejected because the failure modes differ: a saturated snapshot makes
the added *set* wrong, while incomplete correlation only makes the *fold*
unavailable, and the unfolded count is the number this platform has always
reported. Refusing would break every historical run to avoid reporting a number
that is correct and merely stricter than it could be.

## Consequences

Counting stays authoritative in the control plane. `BehaviorDiff` is where the
fold happens, the adapters render what it reports, and no adapter recomputes it.

Gate behaviour is unchanged by this decision as implemented: `max_added_behaviors`
still bounds added identities, so a known tool changing destination still reaches
the gate through the identity count, and no existing verdict moves.

No schema change, no migration and no backfill. Correlation is derived from rows
schema 7 already stores, which also makes a completed comparison reproducible
after a restart for free: the same durable rows produce the same edges.

Fingerprints, `StableFeatures`, baselines and every retained observation are
untouched. A test asserts identity did not move.

Historical runs and runs whose retention saturated report `AddedChangeCount ==
AddedCount` with the correlation state saying why, which is the same number they
report today.

The evidence path is preserved and extended: a counted change names its
contributing identities, and each of those resolves through task 085's existing
routes to the observations that carried it. Nothing new is needed to link a
change to its evidence.

083's four open questions are answered: the bound is retention's (§ correlation
completeness), a folded act reports as one change with contributing identities
(§ what the API reports), a missing parent counts (§ every unresolved case), and
the fold is not configurable — a second limit with a fixed unit is used instead
of a flag that would change what the first one means.
