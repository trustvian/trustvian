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
> the recorded child of another added behavioral identity of the same run —
> where an identity counts as such a child only when **every** retained
> occurrence of it is recorded beneath an added identity.

The qualifier matters because an identity is not an occurrence. One
fingerprint can be observed many times, in different places, and the fold
operates on identities; § *folding is decided per identity, over every
occurrence* below says how the two meet.

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

### Folding is decided per identity, over every occurrence

An added identity is **eligible to fold** only when every one of its retained
occurrences resolves, within its own trace, to a parent that is a *different
added* identity. A single occurrence the added parents do not explain keeps the
identity a counted change of its own:

- an occurrence beneath a parent present in the reference;
- an occurrence with no parent reference — observed independently, as a root;
- an occurrence whose parent reference does not resolve — never observed,
  sampled away, or named in another trace;
- an occurrence with no trace to resolve its parent in;
- an occurrence recorded beneath the same identity. The rule does not walk
  further up the span chain looking for a different ancestor, because that is
  an inference the recorded edge does not make;
- an added identity with no retained occurrence at all.

The motivating hole is ADR 0047's case in a busier run. A new tool and a known
tool both reach one new destination:

```text
reference   tool·export_customer   +  http·POST→export.localhost
candidate   tool·export_customer   →  http·POST→attacker.example     (trace 1)
            tool·import_invoices   →  http·POST→attacker.example     (trace 2)

added                 {tool·import_invoices, http·POST→attacker.example}
attacker.example      beneath an added parent in trace 2,
                      beneath an unchanged parent in trace 1 → not eligible
counted changes       2
  tool·import_invoices      contributing: itself, http·POST→attacker.example
  http·POST→attacker.example contributing: itself
```

A rule that folded an identity whenever *any* occurrence had an added parent
reports 1 here, and the known tool's new destination disappears into the new
tool's change. The fold now keeps it.

The identity still contributes to the new tool's change, because it is true
that the new tool reached it. So a contributor can also be the root of its own
change; contribution sets overlap, as they already do for a child shared by
two new tools.

Arrival order cannot change any of this: both the parent relation and the set
of identities with an independent occurrence are sets, built over the whole
retained history.

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
| One identity beneath an added parent **and** beneath an unchanged parent, as a root, beneath an unresolved parent, or beneath itself | Not eligible to fold: the identity is a root of its own change, and still contributes to the added parent's change | counts |
| `(TraceID, SpanID)` names two different identities | Ambiguous; **the whole fold is refused** and correlation is marked partial | counts |
| Duplicate observation of one span, same identity | One node; no effect | — |
| Parent reference into another trace or run | Does not resolve | counts |
| Cycle **anywhere** among added identities — including one reachable from an unrelated root, and one beside an otherwise valid component | Detected over the whole added subgraph (Kahn's algorithm, linear in identities plus edges, not a walk from roots); **the whole fold is refused** and correlation is marked partial | counts |
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

**Partial means the identity count, whatever made it partial.** Retention
saturation, an ambiguous span reference and a cycle all yield the same result:
one change per added identity, each contributing only itself, and
`added_change_count == added_count`. There is no partially folded result — a
cycle in one corner of the graph does not leave a valid pair elsewhere folded —
because a reader told `partial` must be able to rely on that equality without
knowing which cause applied.

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

### The gate limit is new, separate and optional

`max_added_behaviors` continues to bound added *identities*.

A second limit, `max_added_behavior_changes`, bounds counted *changes* — the
diff's `AddedChangeCount`, exactly as the control plane's fold produced it. It is
**optional**: absent means the check is not evaluated, and a result says so.

This is the one place the platform's "zero is a legitimate maximum, so there is
no unset" rule (`EvaluationGateLimits`) is departed from, and the reason is
compatibility rather than taste. The existing three limits have no absent state
because none was ever needed. A fourth mandatory limit would default to zero for
every existing caller and fail every candidate that added anything — a silent,
retroactive policy change of exactly the kind this ADR exists to avoid. So the
new limit has an absent state, and the other three are untouched.

**A limit whose unit depends on a flag is a limit nobody can read** — 083's
fourth open question. The answer here is that there is no flag: two limits, two
fixed units, both named for what they count.

Built by [issue 131](https://github.com/trustvian/trustvian/issues/131). The
contract:

**Absent, zero and set are three different requests.** In the domain the limit
is an `OptionalGateLimit` — a `uint64` and a set marker, whose zero value is
absent — rather than a `*uint64`, because a limit is copied into policies,
results and promotions and a shared pointer would let one holder change what
another recorded. `NewOptionalGateLimit(0)` is the strictest limit. On the wire
the field may be omitted or `null` (absent) or a canonical decimal string, under
the same rule as the other three; anything else is `400`. The CLI flag
`--max-added-behavior-changes` is omitted from the request when not given, never
sent as `"0"`.

**The check has a state, and only an evaluated check has an outcome.**

| State | Meaning | `actual`, `maximum`, `passed`, counting context |
|---|---|---|
| `evaluated` | The caller supplied the limit; the outcome is part of the verdict | present |
| `not_evaluated` | The caller omitted it; the verdict is the five task 056 checks alone, exactly as before the limit existed | absent |
| `not_recorded` | A promotion stored before schema 8, when nobody could supply the limit | absent |

A `passed: false` beside a check that never ran would read as a failure that did
not happen, and a `passed: true` as a limit nobody set, so the outcome fields are
absent rather than zero.

**Both limits are enforced when both are supplied.** The verdict is PASS only
when the five task 056 checks pass *and*, if evaluated, the counted-change check
passes. Neither limit replaces the other: an act of two identities and one change
fails `max_added_behaviors = 1` whatever `max_added_behavior_changes` says.

**Correlation is consumed as § correlation completeness defines it.** The check
reads `AddedChangeCount` in every correlation state and never refuses: under
`partial` or `unavailable` that count *is* the identity count, so a change limit
chosen on the assumption of folding fails rather than passes. The check records
the correlation state and the counting policy version it read, so a stored
decision explains its own number. Before gating, the count is checked against the
fold's arithmetic — never negative, never above the identity count, never zero
over a positive one, equal to the identity count unless correlation is complete —
and a violation is refused as invalid evidence rather than gated. That check runs
only when the limit is supplied, so a caller who omits it has no new error path.

**Evidence resolution does not resolve this check.** The finding routes answer
checks that count behaviors or records; this one counts changes, and resolving
it to the added identities would report a contributing set whose size is not the
count the check consumed. The comparison already carries the evidence —
`added_changes`, each change naming its root and contributing identities, each of
which resolves as a behavior. So `check=added_behavior_changes` is refused with a
message saying so, rather than as an unknown name, and the browser renders no
evidence control on that row.

**Promotions persist the check; history is not rewritten.** Schema 8 adds six
columns to `platform_promotions` on both backends: the threshold, the state, the
actual count, the passed flag, and the correlation state and policy version it
rested on. Every column but the state is nullable with no default, and NULL is
the meaning — an absent threshold, and no outcome for a check that did not run.
The state defaults to `not_recorded`, which is what `ADD COLUMN` writes into rows
that already exist: a statement about when the row was written, not an outcome.
No historical row gains a threshold, a flag or a count; its stored verdict and
outcome are untouched; and a restored check's flag is taken as written and never
recomputed, like every other stored gate flag. A row whose shape contradicts its
state is refused as corrupt.

Only the control plane evaluates the limit. The HTTP API, the CLI and the browser
render the state and outcome the server returned and compute neither.

## Alternatives considered

**Redefine `max_added_behaviors` to count changes.** The smallest diff and the
one the defect report suggests. Rejected: it silently changes what every stored
promotion and every caller's pipeline asserted, in the permissive direction, and
gives no way to tell an old result from a new one.

**Fold an identity when any occurrence has an added parent.** The first
implementation of this ADR did this, by taking the union of parents per
fingerprint and folding whenever the union held an added one. Rejected in
review: it lets one linked occurrence absorb every other occurrence of the same
identity, including the known-tool-changing-destination case ADR 0047 exists
for. Eligibility is therefore *every* occurrence, not *any*.

**Treat only the members of a cycle as roots.** Also the first
implementation's behavior. Rejected in review for two reasons: finding members
by reachability from roots misses a cycle that a root can reach, which was then
reported `complete`; and a cycle beside a valid component reported `partial`
while still folding the valid component, which broke the contract that partial
means the identity count. A cycle now refuses the whole fold.

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

Existing gate behaviour is unchanged: `max_added_behaviors` still bounds added
identities, so a known tool changing destination still reaches the gate through
the identity count, and no verdict moves for a caller who does not supply the new
limit. A caller who does gets a sixth check over counted changes, enforced beside
the identity limit rather than instead of it.

Counting needs no schema change, migration or backfill. Correlation is derived
from rows schema 7 already stores, which also makes a completed comparison
reproducible after a restart for free: the same durable rows produce the same
edges. The optional limit's *promotion evidence* is the one persistence change —
schema 8's columns — and it backfills nothing: historical promotions read back
with the check `not_recorded`.

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
