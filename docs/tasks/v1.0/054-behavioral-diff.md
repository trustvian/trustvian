# 054 — Behavioral Diff

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [052](052-evaluation-domain.md),
[053](053-evaluation-result-aggregation.md) ·
**Blocks:** 055 onward

## Objective

Answer one question, factually:

> What behavioral shapes changed between a reference evaluation and a
> candidate evaluation?

```text
reference run ──▶ BehaviorCollector ──▶ BehaviorSnapshot ─┐
                                                          ├─▶ BehaviorDiff
candidate run ──▶ BehaviorCollector ──▶ BehaviorSnapshot ─┘
```

The diff reports which behaviors appeared only in the candidate, which
disappeared, which occurred in both, how often each occurred in each run, and
how its relative frequency moved.

It reports nothing about whether that is good. Not *did this pass*, not *is
this safe*, not *is this drift severe*, not *should it be promoted*. Tasks 055
and 056 own interpretation and gates, and a `DriftScore` here would become the
number people read instead of the gate.

## Why

[ADR 0026](../../adr/0026-evaluation-aggregation-is-bounded-evidence.md) made
`EvaluationAggregate` O(1) in the record count and named the consequence
explicitly:

> Task 054 cannot ask this aggregate what behaviors occurred, because it does
> not remember. It needs its own input contract with its own bound.

This is that contract. Task 053 stays exactly as it is — answering *what
happened* in fixed shape — and this task adds a second, separate reducer that
answers *which behaviors happened*, with per-behavior state that the aggregate
deliberately refuses to hold.

A diff needs per-fingerprint state; a result summary does not. Merging them
would force every evaluation to pay for behavioral bookkeeping it may never
compare against, and would reopen the growth ADR 0026 closed.

## Scope

- `BehaviorCollector` — a bounded, explicitly mutable reducer over
  `DecisionRecord`, bound to one `EvaluationRun`.
- `BehaviorSnapshot` — a detached, deterministic, read-shaped value.
- `CompareBehaviorSnapshots(reference, candidate) (BehaviorDiff, error)`.
- `BehaviorDiff` — bounded, factual comparison evidence.

## Non-Goals

No scorecard (055), gates (056), persistence (057), transport (058),
promotion (066), or event history (067).

No `DriftScore`, `StabilityScore`, `Severity`, `Passed`, `Promotable`, or
`AcceptableChange`. No threshold of any kind — not even a "changed" threshold
deciding that a 2% frequency shift matters.

No statistical significance, confidence interval, or p-value.

No change to `EvaluationAggregate`. No change to the core.

## Input Boundary

`trustvian.DecisionRecord` and `trustvian.StableFeatures`, through the root
module's public API. Public `event` types and constants for the three
enumerated behavioral dimensions.

Nothing under `internal/`.

`event.ActorType`, `event.OperationCategory` and `event.TargetCategory` are
public types with public constants, so the collector validates against those
constants directly. Their `valid()` methods are unexported, which is why the
switch is restated here rather than called — a smaller cost than widening the
core's public surface, and the same trade
[task 053](053-evaluation-result-aggregation.md) made for the decision and
risk strings.

## Bounded Behavioral Evidence

### Behavior identity is `FingerprintID`

The comparison key is the core's stable behavioral-shape identity, and
nothing else. Not `EventID`, `ActorID`, `SessionID`, `TraceID`, `CandidateID`,
`EvaluationRunID`, `BehavioralProfileRef`, a commit, or an artifact digest.

Those identify *who acted*, *which evaluation this was*, or *what was
deployed*. A diff keyed by any of them would report a change every time a
candidate was rebuilt — which is the exact failure
[ADR 0022](../../adr/0022-core-platform-boundary.md) rules out for
fingerprints, arriving one layer up.

### Retained descriptor

Each entry keeps the fingerprint, its `StableFeatures`, and an observation
count. The descriptor is what lets a future CLI say

```text
added: tool / shell.execute → build-host
```

instead of printing an opaque hash.

Nothing else is retained: no `Event`, no `DecisionRecord`, no contributors, no
event identifiers, no timestamps, no attributes, no payload.

### Every retained string is bounded

A capped entry count is not a bound if each entry can hold an arbitrarily
large string, and `DecisionRecord` is fixed-*shape*, not size-bounded.

`FingerprintID`, `OperationName`, and `TargetName` are each limited to 256
bytes — the platform's existing identifier limit — and must be valid UTF-8
with no control characters. `OperationName` must be non-empty:
`Event.Validate` rejects a missing operation name, so genuine engine output
always carries one. `TargetName` may be empty. The three enumerated dimensions
are validated against the public constant sets. `Environment` is already
bounded by `EnvironmentRef`.

**Identity fields are never truncated — they are rejected.** Two different
behaviors must not become one because a display string was shortened, and a
diff that merged them would under-report added behavior. Truncation is for
*diagnostics*; identity gets refusal.

### Capacity: 512 distinct fingerprints

512 is the platform's own bound, chosen because it is the only behavioral
cardinality figure this project has already justified —
[ADR 0019](../../adr/0019-bounded-fingerprint-admission.md) reasoned it out
for the engine's per-actor baseline, and an evaluation observing more distinct
shapes than an actor's entire learned repertoire is past the point where a
behavioral diff is the right tool.

It is **not** imported from `internal/baseline.maxFingerprints`. The platform
cannot import internal packages, and should not compile against a core
constant even if it could: the two bounds answer different questions and must
be free to diverge.

## Snapshot Model

A snapshot captures the run's identity (`RunID`, `CandidateID`,
`EnvironmentRef`, `BehavioralProfileRef`), the observation and distinct-behavior
counts, whether it is **complete**, and its bounded entries.

Entries are sorted by `FingerprintID` ascending before storage, so two
snapshots built from the same records in different arrival orders are
byte-identical. Go's map iteration order is deliberately randomized; depending
on it would make a diff's output vary between runs of the same comparison.

A snapshot is **detached**: later `Observe` calls on the collector cannot
change a snapshot already taken, and the entries accessor returns a defensive
copy so a caller cannot reach back into it.

### Saturation is explicit and sticky

The first 512 distinct fingerprints are admitted. Repeat observations of an
admitted fingerprint only increment a counter, so they stay bounded forever.

An unseen 513th fingerprint is **not** admitted, returns `ErrBehaviorCapacity`,
and marks the collector incomplete **permanently**. Subsequent `Observe` calls
fail rather than continuing to accumulate counts that would look complete.

A snapshot is still obtainable for diagnostics, and reports `Complete() ==
false`.

**`CompareBehaviorSnapshots` refuses an incomplete snapshot.** This is the
single most important refusal in the task. Silently comparing the first 512
behaviors would produce a confident, specific, wrong `AddedCount` — a
statement about "new behavior" derived from evidence that stopped early.
An error is recoverable; a plausible wrong number is not.

## Comparison Semantics

The union of fingerprints across two complete snapshots is the domain. Each is
classified as exactly one of:

```text
Added    reference == 0  and  candidate > 0
Removed  reference > 0   and  candidate == 0
Shared   reference > 0   and  candidate > 0
```

Factual names. Not `unsafe`, `violation`, `drift`, `regression`, or
`critical` — an added behavior may be the feature the candidate was built to
add.

The direction matters and the function is not symmetric: `reference →
candidate`.

### Frequency

Rates are computed **at comparison time, from integer counts**:

```text
ReferenceRate = ReferenceCount / ReferenceObservationCount
CandidateRate = CandidateCount / CandidateObservationCount
RateDelta     = CandidateRate - ReferenceRate
```

An absent behavior has rate 0. Normalizing per snapshot is what makes a
900-record reference comparable to a 90-record candidate.

Deriving rates from integers at the end — rather than accumulating floats
while observing — gives a stronger guarantee than
[task 053](053-evaluation-result-aggregation.md)'s sums could offer: **the
same integer counts produce the same rates and deltas regardless of arrival
order.** There is no accumulated floating-point error to depend on ordering.

`RateDelta` is signed and uninterpreted. No threshold decides that some
magnitude is meaningful.

### Compatibility

Both snapshots must be constructor-produced and complete.

`reference.Environment` must equal `candidate.Environment`. Environment is a
stable fingerprint dimension, so comparing staging against production would
turn the environment difference *itself* into behavioral change — every
behavior would be simultaneously Added and Removed.

`CandidateID`, `RunID` and `BehavioralProfileRef` may all differ, and usually
will: comparing two candidates is the point.
[Task 051](051-behavioral-profile-learning-scope-isolation.md) kept the
learning scope out of behavioral identity, so two runs under different
profiles observing the same behavior produce the same fingerprint. Requiring
profiles to match would make isolation look like change.

There is no check that two candidates belong to the same agent. Proving that
needs both entities loaded, and this task has no collection to load them from
— a later service will enforce it. No package-global registry, and no change
to `EvaluationRun`.

### Fingerprint/descriptor conflict

One `FingerprintID` identifies exactly one behavior shape. A second record
claiming the same fingerprint with different `StableFeatures` fails closed
with `ErrFingerprintConflict`, in the collector and again during comparison.

The alternative — overwriting, or merging the counts — would silently report
two different behaviors as one. The cause is a tampered or corrupted record,
or an identity collision, and all three deserve refusal rather than a
plausible-looking merge.

## Capacity / Saturation

| Situation | Result |
|---|---|
| distinct fingerprints ≤ 512 | admitted |
| repeat of an admitted fingerprint | counter increments; no growth |
| unseen fingerprint at 512 | refused, `ErrBehaviorCapacity`, collector permanently incomplete |
| `Observe` after saturation | refused |
| `Snapshot()` after saturation | returned, `Complete() == false` |
| comparing an incomplete snapshot | refused, `ErrIncompleteSnapshot` |

## Validation / Trust Boundary

`DecisionRecord` is a detached public struct; every field this task consumes
is validated as untrusted input, **before any collector state changes**.

Validated: non-empty `EventID`, non-zero `Timestamp`, the three-way
environment agreement, `FingerprintID`, the three enumerated dimensions,
`OperationName`, `TargetName`, and fingerprint/descriptor consistency.

**Not** validated: `Decision`, `RiskLevel`, `ApprovalStatus`, `TrustScore`,
`AnomalyScore`, `AnomalyConfidence`, `ContextRisk`, `IdentityConfidence`,
`PolicyRule`, `Contributors`. This task consumes none of them, and task 053
already validates them for the aggregate. Rejecting a record over a field
nothing here reads would fail evaluations that are perfectly usable for a
diff.

### Environment agreement

`record.Environment`, `record.Behavior.Environment`, and the collector's
environment must all be equal. The first two are the same value in genuine
engine output; a disagreement means the record was assembled rather than
produced.

### Duplicates count twice

One successful `Observe` is one observation. The same record twice counts
twice, in both the total and the per-fingerprint count.

No `EventID` set — that is unbounded by construction, and replay semantics
need a retention window only an ingest boundary can define. The same position
task 053 took, for the same reason.

## Determinism

Sorting by `FingerprintID` makes snapshot and diff output independent of
arrival order and of Go's randomized map iteration.

Rates are derived from integers at comparison time, so equal counts give
bit-identical rates. Unlike task 053's float sums, this holds under arbitrary
reordering.

## Error Semantics

| Sentinel | Where the fault is | Meaning |
|---|---|---|
| `ErrInvalidBehaviorRecord` | the record | a consumed field is missing, unrecognized, or over-long |
| `ErrBehaviorEnvironmentMismatch` | the record, or the pairing | evidence from a different environment |
| `ErrFingerprintConflict` | the evidence | one fingerprint, two different behavior shapes |
| `ErrBehaviorCapacity` | neither | a 513th distinct behavior arrived |
| `ErrIncompleteSnapshot` | the snapshot | saturated evidence cannot be compared |
| `ErrUnboundCollector` | the receiver | not created by `NewBehaviorCollector` |
| `ErrBehaviorOverflow` | neither | an observation counter would wrap |

Wrapped with `fmt.Errorf`, matched with `errors.Is`. Caller-controlled strings
reach messages only through task 053's bounded preview helper, reused rather
than reimplemented.

## Security / Resource Bounds

Retained state, stated completely:

```text
collector/snapshot ≤ 512 entries × (256-byte fingerprint
                                  + 256-byte operation name
                                  + 256-byte target name
                                  + bounded enums
                                  + uint64)
                   + fixed counters and run identity
diff               ≤ 1024 deltas
```

Nothing grows with event count, `EventID` cardinality, actor cardinality,
session cardinality, policy-rule cardinality, or contributor count.
Observation counts grow numerically, not structurally.

Counters are guarded against `uint64` wrap — total and per-entry — and return
an error rather than saturating.

## Tests

**Collector:** construction from a valid run; zero-value run refused;
zero-value collector refuses evidence. One fingerprint, repeats, several
distinct. Duplicates counting twice. Cross-environment and
`Behavior.Environment` mismatch refused. Retained-string bounds at exactly 256
and 257 bytes for all three strings, with no truncation. Corrupt enumerated
dimensions refused. Fingerprint/descriptor conflict refused with state
unchanged.

**Capacity:** exactly 512 admitted; repeats at capacity still counted; the
513th refused with the collector permanently incomplete; `Observe` after
saturation refused; no 513th entry stored.

**Snapshot:** identity captured; entries sorted; arrival order irrelevant;
detached from later collector mutation; accessor returns a defensive copy;
complete and incomplete states reported correctly.

**Comparison:** identical snapshots give zero Added/Removed and zero
`RateDelta`; candidate-only Added; reference-only Removed; a shared
frequency shift with exact counts, rates and signed deltas; all three empty
combinations; environment mismatch refused; differing profile refs compared
successfully; differing candidate IDs giving Shared behavior; cross-snapshot
fingerprint conflict refused; either side incomplete refused; arrival-order
independence; two disjoint full snapshots giving exactly 1024 bounded deltas.

**Real engines:** two streams through real `Engine`s produce real
`DecisionRecord`s; `shell.read` is Shared, `db.query` Removed,
`shell.execute` Added. Behaviors are located through their stable
descriptors, never through hard-coded hashes.

## Benchmarks

`Observe` for an existing fingerprint and for a new one; `Compare` on a
representative pair and on two full 512-entry snapshots. The property that
matters is that per-observation cost does not grow with observation count and
that comparison is bounded by ≤1024 entries — not zero allocations, since a
bounded map and a defensive copy legitimately allocate.

## Documentation

New: this file,
[ADR 0027](../../adr/0027-behavioral-diff-compares-bounded-snapshots.md),
`docs/adr/README.md`, `docs/tasks/v1.0/README.md`.

Updated: `docs/ARCHITECTURE.md`, `docs/DOMAIN.md`, `docs/SECURITY.md`,
`docs/ROADMAP.md`, `docs/PERFORMANCE.md`, `CHANGELOG.md`.

## Acceptance Criteria

- [ ] `EvaluationAggregate` is unchanged.
- [ ] Only public core API is consumed; no `internal/*` import; no core change.
- [ ] Behavior identity is `FingerprintID` alone.
- [ ] At most 512 entries per collector/snapshot; at most 1024 deltas.
- [ ] Every retained string is bounded and never truncated.
- [ ] Saturation is explicit, sticky, and refuses comparison.
- [ ] One fingerprint with two shapes fails closed, in both places.
- [ ] Environments must match; profile, candidate and run refs need not.
- [ ] Duplicates count twice with no dedup set.
- [ ] Snapshots and diffs are deterministically ordered.
- [ ] No score, severity, threshold, pass, or promotability appears.
- [ ] No persistence, transport, or raw history.
- [ ] `gofmt`, `go vet`, `go test`, `-race` pass in every module; `GOWORK=off`
      holds; `check-modules` and `check-platform-boundary` pass.
