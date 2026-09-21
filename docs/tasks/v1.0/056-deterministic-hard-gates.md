# 056 — Deterministic Hard Gates

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [055](055-evaluation-scorecards.md) · **Blocks:** 057 onward

## Objective

Turn a valid `EvaluationScorecard` plus explicit caller-owned limits into a
deterministic pass/fail verdict:

```text
EvaluationScorecard ──┐
                      ├──▶ EvaluateEvaluationGate ──▶ EvaluationGateResult
EvaluationGatePolicy ─┘                                   PASS | FAIL
```

This is the first platform layer permitted to answer *did the evaluation
satisfy the configured hard gates?*

It still answers none of: should production deployment happen now, who may
promote, which environment receives it. [Task 066](README.md) owns promotion
workflow. A `PASS` has no side effect.

## Why

[Task 055](055-evaluation-scorecards.md) deliberately stopped one step short
of acceptability, and recorded why: interpretation and acceptance are
separable, and the scorecard owns only the first. The limits live with the
caller because they are a policy choice, not a property of the evidence —
three added behaviors may be routine for one agent and disqualifying for
another, and no fact in the scorecard can settle which.

Gating consumes the scorecard rather than the aggregates or the raw record
stream because the scorecard has already verified that its three inputs
describe the same comparison. A gate reading aggregates directly would be a
fourth ingestion path with a fourth chance to disagree, and would have to
repeat identity matching that task 055 already performs.

### Hard gates, not averages

The gates are explicit, deterministic, fixed-shape, integer-based,
fail-closed, and auditable. They are not a weighted score, an average, a
fuzzy recommendation, a severity guess, a sensitivity guess, or a generic
expression language.

**A high average must never override one failed hard gate.** That rule comes
from the roadmap, and it is the reason no float participates in a verdict —
see [ADR 0029](../../adr/0029-hard-gates-use-explicit-integer-evidence.md).

## Inputs

```go
func EvaluateEvaluationGate(
    scorecard EvaluationScorecard,
    policy EvaluationGatePolicy,
) (EvaluationGateResult, error)
```

No aggregate, no diff, no record stream, no collector, no service, no
builder, no clock. Evaluation is a pure fixed-shape transformation.

The verb is deliberate. Not `Promote`, `Approve`, `Release`, or `Deploy` —
this evaluates a gate and returns a verdict.

## Gate Policy

```go
type EvaluationGateLimits struct {
    MaxAddedBehaviors           uint64
    MaxBlockDecisions           uint64
    MaxCriticalRiskObservations uint64
}

func NewEvaluationGatePolicy(limits EvaluationGateLimits) EvaluationGatePolicy
```

**`0` is a valid, meaningful maximum.** `MaxBlockDecisions = 0` means no
candidate block decision is accepted. So a zero limit cannot also mean
"unset", and `EvaluationGatePolicy{}` must be distinguishable from a
constructor-produced strict all-zero policy. A private `bound` marker draws
that line — the same mechanism task 053 used for the aggregate and task 055
for the card.

```text
EvaluationGatePolicy{}                        → ErrInvalidGatePolicy
NewEvaluationGatePolicy(EvaluationGateLimits{}) → valid; three maximums of 0
```

`0 == disabled` is rejected as ambiguous and unsafe: the strictest possible
policy and an absent policy would become the same value, and the failure
direction is toward looking permissive.

`MaxUint64` is equally valid and needs no special-casing. A caller who does
not care about a maximum chooses a sufficiently high limit explicitly. There
is no infinity sentinel and no `Enabled bool`, `nil` threshold, or optional
rule — a fixed profile is easier to audit, persist, display and test, and
every one of those knobs is a way to turn a gate off by accident.

No range validation is needed on a constructor-produced policy: every
`uint64` is a legitimate maximum.

## Supported Gates

Exactly five checks, evaluated in this stable order:

| # | Check | Source | Rule |
|---|---|---|---|
| 1 | Reference evidence | `ReferenceRecordCount()` | `>= 1` |
| 2 | Candidate evidence | `CandidateRecordCount()` | `>= 1` |
| 3 | Added behaviors | `Behavior().AddedCount` | `<= MaxAddedBehaviors` |
| 4 | Block decisions | `Decisions().Block.Candidate().Count()` | `<= MaxBlockDecisions` |
| 5 | Critical-risk observations | `Risks().Critical.Candidate().Count()` | `<= MaxCriticalRiskObservations` |

Checks 1–2 are evidence-sufficiency gates and are always enabled. Checks 3–5
are caller-configured acceptance rules over directly observable integers.

No rates, no deltas, no reference-side comparison for the maximum gates. The
policy states absolute candidate constraints.

### Names stay factual

**Block decisions** are policy-engine decisions. A block may be the policy
doing exactly what it was designed to do. This is not `policy violations`,
`critical violations`, or `unsafe actions`; the caller decides whether block
decisions are acceptable for this evaluation.

**Critical-risk observations** are a risk classification. Not a `critical
violation`, `security incident`, or `policy failure`.

**Added behaviors** is a count. The platform makes no claim that added
behavior is bad; the limit is the caller's acceptance policy.

## Evidence Sufficiency

Task 055 correctly allows a valid empty evaluation, and that is useful
evidence. But a candidate that ran zero records has zero block decisions,
zero critical risks and zero added behaviors — and would pass every maximum
threshold. That is fail-open, and it is the exact shape of a candidate that
looks perfect because it never ran.

So both evidence gates are mandatory and not configurable away in this task.

The distinction that must not collapse:

```text
zero / unbound scorecard                        → error
valid scorecard with zero reference or candidate → valid result, FAIL verdict
```

A valid empty scorecard is not an input error. It is valid evidence that
fails the sufficiency gate.

## Unsupported Gates

Task 055 established that current evidence cannot derive
`critical_policy_violations`, `blocked_sensitive_actions`,
`unapproved_sensitive_actions`, per-rule compliance, per-resource
sensitivity, or delegation compliance. `PolicyRule` carries no severity and
is not retained; `ContextRisk` has no repository-defined sensitivity
threshold; `ApprovalStatus` is producer-supplied evidence, not proof of
authorization; the aggregate keeps no per-event rule, resource, or delegation
correlation.

Task 056 **must not manufacture those gates from unrelated fields**:

```text
critical risk count     != critical policy violation count
block decision count    != blocked sensitive action count
denied approval count   != unapproved sensitive action count
```

Each of those is a different question, and substituting one for another would
produce a gate whose name promises a guarantee the evidence cannot support.
They remain absent — not reported as zero — and
[`docs/ROADMAP.md`](../../ROADMAP.md) is corrected to separate implemented
evidence-backed gates from deferred semantic gates. The longer-term intent is
kept, with the reason it is deferred.

Also absent in this task: approval gates (`MaxDeniedApprovals`,
`RequireAllApproved`), sensitive-resource gates
(`SensitiveThreshold`, `MaxSensitiveActions`), and critical-policy-violation
gates. Each needs explicit evidence or an approved threshold contract that
does not exist yet.

### No floating-point gate

No `MinTrustScore`, `MaxAnomalyScore`, `MaxBlockRate`, `MinPresenceOverlap`,
or rate-delta gate.

1. Task 053's floating-point sums are not guaranteed bit-identical under
   arbitrary record reordering, so a float gate could flip on replay.
2. An average lets evidence in one dimension offset a categorical event in
   another — precisely what the roadmap rule forbids.
3. No approved threshold semantics exist for any of them.

### No rule DSL

No `Expression`, `Predicate`, `RuleEngine`, CEL, JSONPath, field-name
strings, operator strings, `map[string]threshold`, or `[]GateRule`. The gate
set is fixed and known. A generic predicate language would move policy
interpretation into an unbounded surface, weaken the compile-time schema,
complicate persistence before task 057, and permit gates on fields whose
semantics have not been approved.

## Verdict Semantics

```go
type GateVerdict string

const (
    GateVerdictPass GateVerdict = "pass"
    GateVerdictFail GateVerdict = "fail"
)
```

`PASS` means exactly *all five task 056 hard gates passed*. It does not mean
safe, secure, approved, or promotable, and no field carries those names.

**Every gate is evaluated every time.** There is no short-circuit: even when
the first check fails, all five checks are populated with their actual
values. A result where both evidence gates failed still reports the real
added-behavior, block and critical-risk counts, because an auditor reading a
FAIL needs to see everything that was measured, not everything up to the
first problem.

```text
PASS iff all five checks pass
FAIL otherwise
```

No check can compensate for another. There is no weighting path to remove,
because none is ever built.

## Result Shape

Fixed-shape throughout — no slice, map, pointer, interface, retained
scorecard or policy, string reason, or arbitrary metadata. The field names
describe the known checks.

```go
type MinimumCountGate struct { Actual, Minimum uint64; Passed bool }
type MaximumCountGate struct { Actual, Maximum uint64; Passed bool }
```

The result carries the five checks, the verdict, and the identity needed to
explain *which comparison was gated*: reference run and candidate IDs,
candidate run and candidate IDs, and the environment. The two sides may
legitimately name different candidates and different behavioral profiles —
task 051 kept learning scope out of behavioral identity so that works.

Returning only `Passed bool` would be unauditable; returning `[]Failure` or
`map[string]GateResult` would be variable-shape. A fixed struct is both.

`EvaluationGateResult` carries a private marker so later layers can
distinguish a real evaluation from `EvaluationGateResult{}`. Copying a valid
result preserves validity. Policy and result are read-shaped values: no
public mutation method, no pointer-only setter, no mutex, no goroutine, no
service, no repository, no callback.

## Determinism

For identical scorecard and policy the result is bit-for-bit equivalent.
Every comparison is integer. There are no clocks, no random numbers, no
floating-point comparisons, no map iteration, and no external state.

Because the result is a fixed struct there is no dynamic ordering. Where docs
or a UI enumerate checks, the stable order is the one in the table above —
never sorted by failure or severity.

## Error Semantics

| Sentinel | Meaning |
|---|---|
| `ErrInvalidGatePolicy` | zero, unbound, or not constructor-produced policy |
| `ErrInvalidGateEvidence` | zero, unbound, or structurally impossible scorecard |

Two sentinels, not one per check. **A failed gate is not an error.** A
perfectly valid evaluation may fail policy:

```text
MaxAddedBehaviors = 0, candidate AddedCount = 1
→ Verdict == FAIL, error == nil
```

An error means the gate evaluation itself could not be trusted.

### Internal consistency guard

A constructor-produced scorecard is trusted, but `BehaviorSummary` counts are
`int` and the gates are `uint64`. A negative count must never become an
enormous gate value through conversion, so the guard runs *before* any
conversion:

```text
AddedCount, RemovedCount, SharedCount, both distinct counts >= 0
AddedCount   + SharedCount == CandidateDistinctCount
RemovedCount + SharedCount == ReferenceDistinctCount
```

Violations are `ErrInvalidGateEvidence`, never `uint64(-1)`. This is not a
re-validation of every task 055 field — only the arithmetic that a lossy
conversion sits on.

## Security

Fail-closed at every boundary: unbound policy rejected, unbound scorecard
rejected, empty evidence failing the sufficiency gates rather than passing
the maximums, integer-only comparisons, every check evaluated, and no
compensating path between checks. `docs/SECURITY.md` records these.

## Resource Bounds

Retained state is a fixed number of identifiers, counters and small structs —
O(1) in record count, behavior count, session, actor and policy-rule
cardinality alike. No event identifier, record, delta, fingerprint or rule
name reaches a result.

## Tests

**Policy.** Zero policy rejected with `ErrInvalidGatePolicy`;
constructor-produced all-zero policy valid with three maximums of zero, proving
zero means strict rather than disabled; `MaxUint64` limits valid and
non-overflowing.

**Scorecard validity.** Zero scorecard rejected with
`ErrInvalidGateEvidence`; constructor-produced card accepted; internally
corrupted behavior summary (negative count, or `Added + Shared !=
CandidateDistinct`) rejected before conversion, via an internal test.

**Evidence sufficiency.** Both non-empty; empty reference; empty candidate;
both empty. Each returns a complete result with `nil` error.

**Each maximum gate.** Boundaries `Actual < Maximum`, `Actual == Maximum`,
`Actual > Maximum`, including `Maximum = 0`. Block reads the *candidate*
block count — not a rate, the reference count, or critical risk;
critical-risk reads candidate critical risk and nothing else.

**Verdict composition.** All five pass → PASS; any one fails → FAIL;
multiple fail → FAIL; all five checks populated in every case.

**Averages cannot override.** A card with very favourable trust, identity and
overlap values but one failed count gate returns FAIL; changing only the
offending integer or its threshold turns it PASS. The absence of a
compensating path is the invariant.

**Structural.** Recursive inspection of policy and result rejecting slice,
map, pointer, interface, or a retained `EvaluationScorecard`, `BehaviorDiff`,
`EvaluationAggregate` or `DecisionRecord`; public-surface assertions that no
member is named for an unsupported concept (`CriticalPolicyViolations`,
`BlockedSensitiveActions`, `UnapprovedSensitiveActions`, `SensitiveActions`,
`ApprovalCompliance`, `PolicyCompliance`, `DelegationCompliance`,
`MinTrustScore`, `MaxAnomalyScore`, `MinPresenceOverlap`, `OverallScore`,
`WeightedScore`, `Recommendation`, `Promotable`, `Promote`, `Deploy`);
absence of pointer-only public mutation methods, in the task 052/053
reflection style.

**Real-Engine integration.** One full-chain test — `Engine` → `DecisionRecord`
→ aggregate + collector → diff → scorecard → gate — built with public APIs
only and no hard-coded fingerprint hashes. The candidate introduces one new
behavior; with `MaxAddedBehaviors = 0` the result is `Actual = 1`, `Passed =
false`, FAIL; re-evaluating *the same scorecard* with `MaxAddedBehaviors = 1`
yields PASS. That difference, without re-running the engine, is the proof
that evidence and acceptance policy are separate.

## Mutation Tests

Each of these must cause a targeted failure, and all are restored before
commit: policy bound check; scorecard bound check; empty-reference gate;
empty-candidate gate; added-behavior comparison (`<=` boundary);
candidate-block source; candidate-critical-risk source; verdict composition
ignoring one failure; the negative/impossible behavior-summary guard.

## Benchmarks

`EvaluateEvaluationGatePass` and `EvaluateEvaluationGateFail`, with scorecard
and policy built outside the loop. Evaluation is fixed-shape and O(1), and
should reach `0 B/op` / `0 allocs/op` naturally — the API is not contorted to
achieve it. Results recorded in `docs/PERFORMANCE.md`.

## Documentation

New: this file, ADR 0029, the ADR index, `docs/tasks/v1.0/README.md`.
Updated: `ARCHITECTURE.md`, `DOMAIN.md`, `SECURITY.md`, `ROADMAP.md`
(hard-gate correction), `PERFORMANCE.md`, `CHANGELOG.md`, and a narrow
clarification where task 055 docs say task 056 must define severity and
sensitivity — it does not invent those contracts; they remain unsupported.

## Acceptance Criteria

- [ ] Consumes only `EvaluationScorecard` and an explicit gate policy.
- [ ] Zero/unbound scorecard and zero/unbound policy both rejected.
- [ ] Constructor-produced all-zero policy valid and strict.
- [ ] Valid empty evidence FAILs rather than erroring.
- [ ] Both evidence-sufficiency gates mandatory.
- [ ] Exactly five gates, all evaluated every time, no short-circuit.
- [ ] PASS only when all five pass; no compensating path.
- [ ] Integer-only comparisons; no float, rate, or delta gate.
- [ ] No overall score, weight, or rule DSL.
- [ ] Unsupported severity/sensitivity gates absent, not zero.
- [ ] Result fixed-shape, retaining no input object.
- [ ] `EvaluationScorecard` semantics unchanged; no core runtime change.
- [ ] No persistence, transport, or promotion workflow.
- [ ] `gofmt`, `go vet`, `go test`, `-race` pass; `GOWORK=off` holds;
      `check-modules` and `check-platform-boundary` pass.
