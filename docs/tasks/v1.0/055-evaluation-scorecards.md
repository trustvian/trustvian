# 055 — Evaluation Scorecards

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [053](053-evaluation-result-aggregation.md),
[054](054-behavioral-diff.md) · **Blocks:** 056 onward

## Objective

Compose the bounded evidence tasks 053 and 054 already produce into one
fixed-shape comparison of two evaluations:

```text
reference EvaluationAggregate ───────┐
BehaviorDiff(reference → candidate) ─┼──▶ EvaluationScorecard
candidate EvaluationAggregate ───────┘
```

It answers how the decision, risk, approval and policy-selection
distributions moved, how the five numeric signals moved, and how much
behavioral presence overlapped.

It answers no question of acceptability. Not *did this pass*, not *should this
be promoted*, not *is an added behavior acceptable*, not *was a blocked action
a critical violation*. Each needs policy or threshold semantics that no
current type carries, and [task 056](README.md) owns them.

## Why

Both inputs already exist and neither is sufficient alone. The aggregates hold
distributions and numeric summaries but nothing about *which* behaviors
occurred; the diff holds behavioral presence but no decisions, risks or trust
values. A comparison needs both.

Composing them — rather than building a third reducer over `DecisionRecord` —
is the whole point. Records were consumed once, by two reducers designed
against the same stream. A scorecard that re-read them would be a third
ingestion path with a third chance to disagree with the other two.

### The word means a card, not a number

A scorecard here is *a structured card of comparable evidence*, not *one
number deciding quality*. There is deliberately no `OverallScore`,
`SecurityScore`, or `Grade`, and no weighting model — see
[ADR 0028](../../adr/0028-scorecards-are-fixed-shape-comparative-evidence.md).

The roadmap's own rule is the reason: *a high average must not override a
critical violation*. A composite number is precisely the field a caller would
read instead of task 056's gates, and once it exists someone will compare it
to a threshold.

## Inputs

```go
func NewEvaluationScorecard(
    reference EvaluationAggregate,
    candidate EvaluationAggregate,
    diff BehaviorDiff,
) (EvaluationScorecard, error)
```

Direction is `reference → candidate`, matching `CompareBehaviorSnapshots`.
Not symmetric.

No `DecisionRecord`, no `BehaviorSnapshot`, no raw stream. No collector, no
service, no builder. Construction is a pure bounded transformation.

## Evidence Compatibility

A scorecard must never combine evidence from unrelated evaluations, so
construction verifies correspondence before producing anything.

**Identity**, each side against its own side of the diff:

```text
reference.RunID            == diff.ReferenceRunID
reference.CandidateID      == diff.ReferenceCandidateID
reference.BehavioralProfile == diff.ReferenceBehavioralProfile

candidate.RunID            == diff.CandidateRunID
candidate.CandidateID      == diff.CandidateCandidateID
candidate.BehavioralProfile == diff.CandidateBehavioralProfile

reference.Environment == candidate.Environment == diff.Environment
```

Note what is *not* required: the two profiles need not equal each other, and
neither need the two candidate IDs. Comparing two candidates under two
learning scopes is the normal case —
[task 051](051-behavioral-profile-learning-scope-isolation.md) kept scope out
of behavioral identity precisely so that works.

**Observation counts**, and this one earns its place:

```text
reference.RecordCount == diff.ReferenceObservationCount
candidate.RecordCount == diff.CandidateObservationCount
```

Tasks 053 and 054 are designed to consume the same stream. If the aggregate
saw 100 records and the collector saw 91, the two halves describe different
evaluations, and presenting them as one card would put a decision distribution
from 100 events beside a behavioral summary from 91. There is no correct way
to reconcile that, so it is refused rather than best-efforted.

**Validity.** A zero-value `EvaluationAggregate{}` or `BehaviorDiff{}` is not
evidence. Both are constructible from any package — unexported fields prevent
mutation, not construction — and a zero aggregate's counts are all zero, which
is *indistinguishable from a valid empty evaluation* by value and completely
different in meaning.

The aggregate already carries task 053's `bound` marker. `BehaviorDiff` has no
marker and does not need one: a constructor-produced diff always has non-empty
run, candidate, profile and environment identifiers, because they originate in
an `EvaluationRun` that `validateRunBinding` already checked. A zero diff has
none of them. That is a reliable discriminator, and it means task 054's shape
is not modified for this task's convenience.

**Structural consistency**, cheap and fixed-shape:

```text
Decisions.Total()       == RecordCount
Risks.Total()           == RecordCount
Approvals.Total()       == RecordCount
PolicySelection.Total() == RecordCount
every MetricSummary.Count == RecordCount
```

Not a re-validation of the records — they no longer exist here. These are
arithmetic identities task 053 guarantees, checked because a scorecard is a
trust boundary *between evidence components* even when each component is
internally sound.

## Scorecard Shape

Fixed-shape throughout: typed identifiers, integer counts, small fixed
structs, and copied `MetricSummary` values. No slice, no map, no pointer, no
interface, no retained `BehaviorDiff` or `EvaluationAggregate`.

**Task 054's ≤1024 deltas are not retained.** A UI wanting per-behavior rows
reads the `BehaviorDiff` it already has; the scorecard is the compact summary.
This is what makes construction cost independent of both record count and
behavior count.

Distributions are explicit structs, never `map[string]RateComparison`: the
category sets are closed and known, and a map would accept an unknown key,
add cardinality, and weaken the schema.

## Rate Semantics

```go
type EvidenceRate  struct{ count, total uint64 }   // Value() (float64, bool)
type RateComparison struct{ reference, candidate EvidenceRate } // Delta() (float64, bool)
```

**An empty denominator makes a rate undefined, not zero.** `Value()` reports
`false` when `total == 0`, and `Delta()` is defined only when both sides are.
"No observations" and "observed, never occurred" are different facts that a
bare `0.0` would merge — and the direction of that error is toward looking
safe.

The same applies to every derived ratio in the card.

## Behavioral Comparison

From the diff's summary accessors only:

```text
ReferenceDistinctCount, CandidateDistinctCount
AddedCount, RemovedCount, SharedCount
```

plus three ratios named for what they mathematically are:

```text
AddedCandidateRate   = AddedCount   / CandidateDistinctCount
RemovedReferenceRate = RemovedCount / ReferenceDistinctCount
PresenceOverlap      = SharedCount  / (Added + Removed + Shared)
```

`PresenceOverlap` is Jaccard presence similarity. It is **not** a
`BehavioralStabilityScore`: naming it for a judgement would smuggle in the
claim that more overlap is better, which is a question about the change, not
about the numbers.

Each is undefined when its denominator is zero. A comparison of two empty
evaluations has no evidence from which to compute overlap — neither `1.0`
("identical") nor `0.0` ("completely different") is true.

## Numeric Signal Comparison

`MetricComparison{reference, candidate MetricSummary}` with
`MeanDelta() (float64, bool)`, for `IdentityConfidence`, `AnomalyScore`,
`AnomalyConfidence`, `TrustScore`, `ContextRisk`.

`TrustScore` is **not** redefined. `Trust.Score` remains an event-level engine
signal with its own documented formula and compatibility promise; this
compares the aggregate summaries of it and nothing more.

No variance, percentile, or histogram — nothing consumes them, and the first
two need either sample retention or an error budget somebody must own.

## Unsupported Semantics

**Not derivable from current evidence, and therefore absent:**

| Concept | Why not |
|---|---|
| `CriticalPolicyViolations` | `PolicyRule` carries a name, not severity; the aggregate retains no rule names at all |
| `BlockedSensitiveActions` | No repository-defined threshold makes a `ContextRisk` value "sensitive" |
| `UnapprovedSensitiveActions` | Same, plus approval status is producer-supplied evidence |
| per-rule compliance | The aggregate retains no per-event rule correlation |
| per-resource sensitivity | No target correlation is retained |
| delegation stability | `DelegatedFrom` is not retained by the aggregate |

**Absence, not zero.** None of these appears as a field reporting `0`. A
`CriticalPolicyViolations: 0` derived from evidence that cannot express
violations is a false security claim, and the most dangerous kind — it reads
as a measurement that found nothing rather than a measurement that never ran.

Names stay factual throughout. `Block` is not renamed `violation`; `critical`
risk is not a critical policy violation; `Denied` is evidence that a producer
reported denial, not proof of noncompliance.

### Gate-ready versus permitted

Directly available to task 056: candidate `Block` count and rate, candidate
`Critical` risk count and rate, `RequireApproval` counts, every approval
status count, `AddedCount`, `RemovedCount`, `PresenceOverlap`, and the metric
summaries.

**Availability is not permission.** Task 056 must decide which of these can
legitimately gate a promotion and whether an explicit policy-severity contract
is needed first. This task makes evidence visible; it grants nothing.

*Resolved by [task 056](056-deterministic-hard-gates.md):* it gates the added
behavior, candidate block, and candidate critical-risk counts under their
factual names, and invents no severity or sensitivity contract. The semantic
gates above remain unsupported until explicit evidence exists.

## Zero / Empty Evidence

A valid empty evaluation produces a valid scorecard: counts zero, every rate
and mean and ratio undefined. It is *not* an error.

A zero-value input is an error. The distinction is exactly the one task 053
drew, one level up.

## Determinism

Categorical counts and the rates derived from them are integer arithmetic and
independent of arrival order.

Mean comparisons inherit task 053's floating-point semantics: deterministic
for the same ordered stream, not guaranteed bit-identical under arbitrary
reordering, because floating-point addition is not associative.

That asymmetry is another reason a critical hard gate must rest on counts
rather than on a weighted average.

## Error Semantics

| Sentinel | Meaning |
|---|---|
| `ErrInvalidScorecardEvidence` | zero, unbound, or structurally impossible aggregate or diff |
| `ErrScorecardEvidenceMismatch` | individually valid inputs describing different comparisons |

Two, not one per identity field. Caller-controlled identifiers reach messages
only through task 053's bounded preview helper.

## Security / Resource Bounds

Retained state is a fixed number of identifiers, counters, small structs and
metric summaries — O(1) in event count, behavior count, session, actor and
policy-rule cardinality alike. No event identifier, record, delta, fingerprint
or rule name survives into a card.

`EvaluationScorecard{}` carries a private marker so task 056 can fail closed
on a card its constructor did not produce.

## Tests

Happy path with identity captured; zero aggregate on either side rejected;
zero diff rejected; reference and candidate run mismatch; candidate ID
mismatch; per-side profile mismatch (while cross-side difference is accepted);
environment mismatch; and the observation-count mismatch, built by feeding one
evidence path a record the other did not see.

Exact distributions with small hand-chosen numbers for decision, risk,
approval and policy selection, covering counts, rates and signed deltas.

Empty-denominator semantics for every derived value. Behavioral summaries for
identical, added-only, removed-only, mixed, both-empty, empty-reference and
empty-candidate cases. Numeric comparisons including an undefined side.

Structural tests pinning absence: no `OverallScore`, `Grade`, `Passed`,
`Promotable`, `CriticalPolicyViolations` or similar; and a recursive check
that the card holds no slice, map, pointer, interface, or retained evidence
object.

One real-Engine integration test driving the full public chain.

## Benchmarks

`NewEvaluationScorecardTypical` and `NewEvaluationScorecardEmpty`. The
property worth measuring is that a full 1024-delta diff costs no more than a
tiny one, because only summary accessors are read.

## Documentation

New: this file, ADR 0028, the ADR index, `docs/tasks/v1.0/README.md`.
Updated: `ARCHITECTURE.md`, `DOMAIN.md`, `SECURITY.md`, `ROADMAP.md`,
`PERFORMANCE.md`, `CHANGELOG.md`, and a narrow consistency fix to ADR 0027's
identity section.

## Acceptance Criteria

- [ ] Consumes only `EvaluationAggregate` and `BehaviorDiff`; no third reducer.
- [ ] `EvaluationAggregate` and `BehaviorCollector` semantics unchanged.
- [ ] No core runtime change.
- [ ] Fixed-shape, O(1), retaining no deltas or raw history.
- [ ] Every identity and both observation counts verified; no partial card.
- [ ] Zero-value evidence refused; valid empty evidence accepted.
- [ ] Empty denominators undefined, never zero.
- [ ] No overall score, weight, threshold, or verdict.
- [ ] Unsupported semantics absent rather than reported as zero.
- [ ] `gofmt`, `go vet`, `go test`, `-race` pass; `GOWORK=off` holds;
      `check-modules` and `check-platform-boundary` pass.
