# 053 — Evaluation Result Aggregation

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [050](050-public-serializable-decision-record.md),
[052](052-evaluation-domain.md) ·
**Blocks:** 054 onward · **The first platform logic that consumes core
evidence.**

## Objective

Turn a stream of `trustvian.DecisionRecord` values into one bounded,
deterministic, fixed-shape summary of what an evaluation observed:

```text
Engine ──▶ DecisionRecord ──▶ EvaluationAggregate
```

The aggregate answers factual questions — how many observations, how they were
decided, at what risk, with what approval evidence, over what time range, and
the descriptive statistics of the five numeric signals every record carries.

It answers no interpretive question. Not *did this pass*, not *is this safe*,
not *is this promotable*, not *how different is this from that*, and not *was
this a critical violation*. Those are tasks 054, 055 and 056, and each needs
context this aggregate deliberately does not hold.

## Why

Task 052 built the evaluation vocabulary and pointedly did not import the
core. This is the task with a real reason to: an evaluation is worth nothing
until something summarizes what happened during it.

The input is `DecisionRecord` rather than `Result`, which is the whole point
of [task 050](050-public-serializable-decision-record.md). `Result`'s stage
fields have types from `internal/`, so a platform that consumed it could not
declare a variable to hold one. `DecisionRecord` exists precisely so the
platform never needs another engine change — and this task is the first
occasion to prove that claim rather than assert it.

Aggregating incrementally, one record at a time, establishes the call pattern
future ingest will actually have. A design that required `[]DecisionRecord`
would work today and be wrong the moment records arrive over a network one by
one.

## Scope

- `EvaluationAggregate`, with `NewEvaluationAggregate(run) (EvaluationAggregate, error)`
  and `AddRecord(record) (EvaluationAggregate, error)`.
- Fixed-shape counters for decision, risk, approval, and policy-selection
  shape.
- `MetricSummary` over the five numeric fields.
- Event-time range.
- Validation of every consumed field, fail-closed.
- `platform/go.mod` gains its first root-module dependency.

## Non-Goals

No behavioral diff (054), scorecard (055), hard gates (056), persistence
(057), transport (058), promotion (066), or event history (067).

No score, grade, pass/fail, promotability, critical-violation count,
new-behavior count, sensitivity classification, or approval-compliance
judgement — see [What this must not infer](#what-this-must-not-infer).

No service layer, manager, coordinator, repository, or store. No mutex,
atomic, channel, or goroutine: this is a value.

**No core change.** Not one line.

## Input Boundary

`trustvian.DecisionRecord`, reached through the root module's public API.

The platform imports `github.com/trustvian/trustvian` and
`github.com/trustvian/trustvian/event` — and nothing under `internal/`. That
restriction is why the six decision strings and four risk strings are
re-declared as platform constants rather than imported: `policy.Decision` and
`trust.RiskLevel` live under `internal/`, and `DecisionRecord` deliberately
carries them as `string`. Re-declaring four and six known values is the price
of the boundary, and it is cheap; widening the core's public surface to avoid
it would be the expensive mistake.

`event.ApprovalStatus` **is** public, so the approval constants are imported
rather than restated. The rule is not "re-declare everything" — it is "use the
public type when one exists".

### Three things the record does not carry, deliberately

`DecisionRecord` has no `EvaluationRunID`, no `CandidateID`, and no
behavioral-profile reference, and this task does not add them.

- **Run and candidate association is the caller's.** Whatever feeds records
  into an aggregate knows which run it is feeding. The aggregate captures
  those identifiers from the `EvaluationRun` at construction.
- **`CandidateID` is never inferred from `ActorID`.** A platform Candidate and
  a behavioral actor are different identities that may coincidentally share
  text. Collapsing them would make the platform's identity model depend on a
  naming convention.
- **The behavioral profile cannot be verified from a record.**
  [Task 051](051-behavioral-profile-learning-scope-isolation.md) kept the
  learning scope out of `DecisionRecord` on purpose. The aggregate carries the
  run's `BehavioralProfileRef` because the run knows it — **not** because the
  record proves it. An aggregate cannot attest that the engine which produced
  these records was configured with that scope, and this task does not pretend
  otherwise. Changing the core to make it provable is not on the table.

## Aggregate Model

```text
EvaluationAggregate
  ├── run identity      RunID, CandidateID, Environment, BehavioralProfile
  ├── RecordCount       uint64
  ├── time range        FirstObservedAt, LastObservedAt
  ├── DecisionCounts    Allow, ObserveOnly, Alert, Challenge,
  │                     RequireApproval, Block
  ├── RiskCounts        Low, Medium, High, Critical
  ├── ApprovalCounts    Unspecified, NotRequired, Required, Approved, Denied
  ├── PolicySelection   MatchedRule, MatchedDefault
  └── MetricSummary ×5  IdentityConfidence, AnomalyScore, AnomalyConfidence,
                        TrustScore, ContextRisk
```

Counters are fixed-shape structs, never `map[string]uint64`. The categories
are closed and known from the public contract; a map would accept an unknown
value silently, which is the failure mode this task is supposed to prevent.

`MetricSummary` is `{Count, Sum, Min, Max}` with a `Mean() (float64, bool)`
accessor. The bool is false for an empty summary — an empty average is absent,
not zero, and returning `0` would make "nothing was observed" indistinguishable
from "everything scored zero". `NaN` would be worse: it propagates.

Counter and summary types are returned **by value** and may have exported
fields. They are snapshots; mutating one cannot reach the aggregate.

### Policy selection is shape, not severity

`MatchedRule` and `MatchedDefault` count which path a decision took. That is
all. A rule *name* is an identifier — `block-prod-shell` does not prove
severity, and nothing here parses one. There is no map keyed by rule name,
which would also be unbounded.

`MatchedDefault` and `PolicyRule` are validated as **one piece of evidence**,
because that is what they are:

```text
matched a rule:  MatchedDefault == false  and  PolicyRule != ""
matched default: MatchedDefault == true   and  PolicyRule == ""
```

`DecisionRecord` documents the pairing and `policy.Evaluate` produces exactly
it. Counting the boolean alone would let a hand-built record assert that a
rule matched while naming no rule — fabricated selection evidence that a later
scorecard would read as real. Either malformed combination is refused; there
is deliberately no "unknown" bucket, since inventing one would preserve the
fabrication under a different name.

### What this must not infer

Not computed, each for a concrete reason:

| Not computed | Why |
|---|---|
| `CriticalPolicyViolations` | `PolicyRule` has a name and a reason, no criticality field |
| `BlockedSensitiveActions` | No public rule says what `ContextRisk` makes an action sensitive |
| `NewBehaviorCount` | "New" needs a reference to compare against — task 054 |
| `ApprovalCompliance` | `ApprovalStatus` is producer-supplied evidence, not authorization |
| `Score`, `Grade`, `Passed`, `Promotable` | Interpretation and gating, tasks 055 and 056 |

Each of these is a judgement that needs context the aggregate does not have.
Computing one here would put it in the only place nobody would think to look
for a policy decision.

## Aggregate Construction

`NewEvaluationAggregate` returns an error, because a run can be invalid.

Task 052 made `EvaluationRun`'s fields unexported, which prevents *mutation* —
it does not prevent `platform.EvaluationRun{}`, constructible from any
package. Copying its accessors blindly would produce an aggregate with four
empty identifiers: evidence belonging to no run, in no environment, which the
environment check would then match against records whose environment was also
empty. **One aggregate is evidence for exactly one valid run**, so an invalid
run yields no aggregate.

Validated: the four identifiers against the same policy
[task 052](052-evaluation-domain.md) applies (reusing its helper rather than
restating it), a non-zero `CreatedAt`, and a recognized `RunStatus`.

**Every lifecycle state is accepted.** Aggregation happens *during* execution,
so pending and running are the common cases and a terminal run is equally
valid evidence. Only a status this package never produces is refused — the
same fail-closed stance the transitions take. No existence checks: that needs
a collection, which is task 057.

**The zero value is intentionally unusable.** Unexported fields prevent
mutation, not construction, so `platform.EvaluationAggregate{}` can be written
from any package — and an unbound aggregate has an empty environment, which
the environment guard happily matches against a record whose environment is
also empty. Every other check passes on a well-formed record, and the result
would be a populated evidence summary belonging to no run.

A private marker set only by the constructor, and only after every run check
passes, closes it: `AddRecord` returns `ErrUnboundAggregate` and folds
nothing. **Records can only be folded into an aggregate successfully created
from a valid `EvaluationRun`.**

A boolean rather than re-validating the four identifiers per record: they are
immutable once set, so re-proving them would spend real time on a
per-decision path re-deriving a constant. It also fails in the safer
direction — a future constructor that forgot the marker would produce
aggregates rejecting everything, which is loud and immediate, where a
forgotten entry in a validation list would silently pass.

The check runs **first**, ahead of every record-derived one. When both the
receiver and the record are unusable, the binding fault is the one worth
reporting: an unbound aggregate cannot accept any record, so naming a problem
with the record would send a caller to debug the wrong object.

## Validation / Trust Boundary

`DecisionRecord` is a **detached public struct**. A genuine one comes from
`Engine.Analyze`, but a caller can build or modify one freely, so the
aggregator treats it as untrusted input for every field it consumes.

Every check runs **before any state changes**. A rejected record leaves the
aggregate exactly as it was — no partial counts, no advanced time range.

Validated:

- `EventID` non-empty and `Timestamp` non-zero — a record with neither cannot
  be traced back to anything;
- `Environment` equals the aggregate's — see below;
- `Decision`, `RiskLevel`, `ApprovalStatus` are recognized values;
- the five aggregated numbers are finite and within `[0,1]`.

**Not** validated: `ActorType`, `FingerprintID`, `Contributors`,
`PolicyReason`, the correlation identifiers, and the rest of `Behavior`. This
task is not a second `Event.Validate`. Validating a field it does not consume
would be speculative, and would make the aggregator fail on records that are
perfectly usable for what it does.

**Malformed input is refused, never repaired.** No clamping, no coercion to a
default category. The core guarantees its own output; a record that fails
these checks did not come from a healthy engine, and silently rewriting it
would turn a corruption signal into a plausible-looking number.

### Environment isolation

A record whose `Environment` differs from the aggregate's is refused. Counting
production evidence into a staging evaluation is not a rounding error, it is a
wrong answer, and the run's `EnvironmentRef` is the only thing that can catch
it.

`Behavior.Environment` is checked against `Environment` as well. For genuine
engine output the two are the same value — `features.Extract` derives the
stable environment from the same `Event.Context.Environment` the record
reports — so a disagreement means the record was assembled or tampered with,
and is refused on that basis. Tested explicitly, because it is an internal
consistency claim about the core rather than something the platform can
assume.

## Determinism

**The guarantee: the same ordered record stream produces the same aggregate.**

That is deliberately narrower than order-independence. Counts, `Min`, `Max`,
and the time range are order-independent by construction. `Sum` is not:
floating-point addition is not associative, so reordering a stream can change
the last bits of a sum and therefore of a mean. Claiming bit-identical results
under arbitrary reordering would be false, and a later gate built on that
false claim would be flaky in a way nobody could reproduce.

This is also why hard gates (056) must rest on categorical and count evidence
for critical conditions. An average is a summary, not a verdict, and no
average should be able to outvote a `Block`.

## Resource Bounds

**Aggregate memory is O(1) in the number of records.** The type holds four
identifier strings captured once, a `uint64`, two `time.Time`s, four counter
structs, and five four-field summaries. There is no slice, no map, and no
pointer to anything that grows.

Retaining records would make the aggregate an accidental event archive —
exactly what [task 067](README.md) owns behind its own capability boundary,
and exactly what the core already refuses to become. It is also why no
deduplication set exists (below), and why task 054 will need its own bounded
input contract rather than asking this task to remember every behavior
forever.

Asserted structurally by test — reflection over the type's fields — rather
than by measuring heap growth over ten records, which would prove nothing
about ten million.

### Duplicates count twice

One `AddRecord` call is one observation. The same record supplied twice counts
twice.

The alternative is an `EventID` set, which is unbounded by construction and
would break the invariant above for a property this layer cannot deliver
anyway. Idempotency and replay belong at an ingest or persistence boundary
where retention, identity, and a time window can be designed deliberately.
Documented rather than silently true.

### Counter overflow

`RecordCount` is `uint64`. Overflow is unreachable in practice and is still
checked, because the alternative is wrapping to zero — a bounded aggregate
over an unbounded stream should fail loudly rather than quietly report that
nothing happened.

Every category and metric count is bounded by the total, so one guard on the
total is sufficient; each is nonetheless incremented from the same validated
path. Returns an error, never saturates.

## Error Semantics

Three sentinels, matching the repository's convention of wrapping with
`fmt.Errorf` and matching with `errors.Is`:

| Sentinel | Meaning |
|---|---|
| `ErrInvalidDecisionRecord` | a consumed field is missing, unrecognized, or out of range |
| `ErrEnvironmentMismatch` | the record belongs to a different environment |
| `ErrAggregateOverflow` | `RecordCount` would wrap |

Messages name the offending field and value without dumping the record, and
**bound what they echo**.

`DecisionRecord` is fixed-*shape*, not size-bounded — task 050 says so
explicitly, because caller-supplied identifiers have no length limit. A
rejection path that echoed a field verbatim would let whoever constructed the
record choose how much memory the error allocates and how much output a log
absorbs: a malformed record becoming an amplification primitive at exactly the
moment the system is already unhappy.

Every untrusted string reaching an error goes through one preview helper
bounded at 64 bytes. Quoting is applied to the *truncated prefix*, never to
the whole value — quoting first and truncating after would build the full
escaped copy, potentially several times the original size, before discarding
it, which is the bound the helper exists to provide. Truncation lands on a
rune boundary so the result stays valid UTF-8, and it is visible in the
output, because a silently shortened value looks like the whole thing.

**An error is all that happens.** A rejected record does not fail, cancel, or
complete the `EvaluationRun`, and does not block anything. The aggregator has
no authority over a run's lifecycle — that is orchestration, and it belongs to
a later service task.

## Tests

Construction and empty state. One record, every field. Every decision
category, every risk level, every approval status including the empty
`ApprovalUnspecified` — proving no category aliases another.

Numeric summaries across distinct values, including the `0` and `1` boundaries,
with `Count`, `Sum`, `Min`, `Max` and `Mean` checked; `Mean` reports absent on
an empty summary.

Time range from out-of-order input: input order must not define the range.

Immutability: the source aggregate is unchanged after `AddRecord`. Rejection:
for every malformed class, the returned aggregate equals the original exactly.

Rejection classes: unknown and empty decision, unknown and empty risk, unknown
approval, mismatched environment, mismatched `Behavior.Environment`, empty
`EventID`, zero timestamp, both malformed policy-selection combinations, and —
for each of the five numeric fields — `NaN`, `±Inf`, below `0`, and above `1`.

Construction: a zero-value run is refused, and every lifecycle state is
accepted. A zero-value *aggregate* refuses a record that is valid in every
other respect — deliberately including a matching empty environment, so the
binding guard is what rejects it rather than the environment check doing so by
accident — and reports the binding fault ahead of a record's own faults.

Diagnostics stay bounded: a 1 MiB field in any of seven positions produces an
error under 1 KiB that never reproduces a long run of the input, and a
multi-byte prefix swept across the truncation boundary keeps the message valid
UTF-8.

Duplicates count twice. Structural proof of O(1) shape. Extension of task
052's reflective mutation-surface guard to the new types. An internal-package
test drives the overflow guard by constructing a near-overflow aggregate,
since reaching it honestly is not possible.

A structural assertion that no interpretive accessor — `Passed`, `Score`,
`Grade`, `Promotable`, `CriticalPolicyViolations`, `NewBehaviorCount` — exists
on the API.

**One cross-module integration test** builds a real `event.Event`, analyzes it
with a real `trustvian.NewEngine()`, calls `Result.DecisionRecord()`, and
feeds it to the aggregate — using the public API only. This is the proof that
the boundary composes; hand-built records everywhere else would assume it.

## Benchmarks

`BenchmarkEvaluationAggregateAddRecord` over the one-record path. Unlike task
052's construction, this plausibly runs once per decision, and the invariant
worth measuring is that a record does not allocate merely because the
aggregate has seen many before it. Measured numbers go in the task record and
in `docs/PERFORMANCE.md` if they are meaningful.

## Documentation

New: this file,
[ADR 0026](../../adr/0026-evaluation-aggregation-is-bounded-evidence.md),
`docs/adr/README.md`, `docs/tasks/v1.0/README.md`.

Updated because the platform now depends on the core:
`docs/ARCHITECTURE.md`, `CONTRIBUTING.md`, `docs/release-guide.md`.
Task 052's own statement that *it* needed no core dependency stays true and
stays as written — the current-state prose is what changes.

Also updated: `docs/DOMAIN.md`, `docs/SECURITY.md`, `docs/ROADMAP.md`,
`CHANGELOG.md`.

## Acceptance Criteria

- [ ] `EvaluationAggregate` exists with value-returning `AddRecord`; no
      pointer-mutating public method.
- [ ] The platform imports only `github.com/trustvian/trustvian` and
      `.../event`; no `internal/*`.
- [ ] No core runtime file changed.
- [ ] `EvaluationRun` gained no result, counter, or summary field.
- [ ] The aggregate retains no record, slice, or map — proven structurally.
- [ ] Decision, risk, and approval counts are fixed-shape and exhaustive.
- [ ] Every consumed field is validated before any state change; a rejected
      record leaves the aggregate identical.
- [ ] Cross-environment records are refused.
- [ ] `MatchedDefault` and `PolicyRule` are validated as one coherent piece of
      evidence; neither malformed combination counts.
- [ ] An invalid or zero-value `EvaluationRun` produces no aggregate.
- [ ] A zero-value `EvaluationAggregate` accepts no record.
- [ ] Validation errors bound what they echo from untrusted input.
- [ ] Duplicates count twice, documented.
- [ ] No score, gate, diff, promotion, or new-behavior semantic exists.
- [ ] A real-Engine integration test proves the boundary composes.
- [ ] `gofmt`, `go vet`, `go test`, `-race` pass in every module;
      `GOWORK=off` holds for all three nested modules; `check-modules` and
      `check-platform-boundary` pass.
