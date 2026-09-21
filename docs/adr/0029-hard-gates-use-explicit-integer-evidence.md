# 0029 — Hard gates use explicit integer evidence

**Status:** Accepted

## Context

[Task 056](../tasks/v1.0/056-deterministic-hard-gates.md) is the first
platform layer allowed to answer *did this evaluation satisfy the configured
hard gates?* Everything before it describes evidence;
[ADR 0028](0028-scorecards-are-fixed-shape-comparative-evidence.md) stopped
one step short of acceptability on purpose, separating interpretation from
acceptance and keeping only the first.

The roadmap states the rule that constrains this layer:

> Promotion must never rest on an aggregate score alone... a high average
> cannot override a critical violation.

It also lists four example gates, three of which
[task 055](../tasks/v1.0/055-evaluation-scorecards.md) proved are not
derivable from any evidence this chain retains. So this decision has to
settle both what the gate layer is, and what it must refuse to pretend to be.

## Decision

Task 056 hard gates operate only on explicit integer scorecard evidence, fail
closed on missing evidence, use fixed-shape caller-owned limits, and return a
deterministic verdict. Unsupported severity and sensitivity semantics remain
absent.

Concretely: `EvaluateEvaluationGate(scorecard, policy)` returns an
`EvaluationGateResult` carrying five always-evaluated checks and a
`GateVerdict` of `pass` or `fail`.

### 1. Gates consume the scorecard, not aggregates or records

The scorecard has already verified that its three inputs describe the same
comparison — matching runs, candidates, profiles, environment, and both
observation counts. A gate reading aggregates or the record stream directly
would be a fourth ingestion path with a fourth chance to disagree, and would
have to repeat identity matching that already exists one layer down.

### 2. Valid empty evidence fails rather than errors

A candidate that ran zero records has zero block decisions, zero critical
risks, and zero added behaviors. It passes every maximum threshold. That is
fail-open, and it is exactly the shape of a candidate that looks perfect
because it never ran.

So reference and candidate record counts are mandatory gates with a minimum
of one, not configurable away. An empty evaluation is not an input error —
task 055 was right to accept it as valid evidence — it is valid evidence that
fails the sufficiency gate. Those two outcomes must not collapse into one.

### 3. A zero or unbound scorecard is an error

Distinct from the above. A zero-value `EvaluationScorecard{}` has all-zero
counts, which is *indistinguishable by value* from a valid empty evaluation
and opposite in meaning. Task 055 added a private `bound` marker so this
layer could tell them apart; this task uses it rather than inferring validity
from public zero values.

The same reasoning gives `EvaluationGatePolicy` its own marker, for a sharper
reason: `0` is a legitimate maximum. `MaxBlockDecisions = 0` means no block
decision is accepted — the strictest policy expressible. If zero also meant
"unset", the strictest policy and an absent policy would be the same value,
and the failure direction is toward looking permissive. So
`EvaluationGatePolicy{}` is rejected while
`NewEvaluationGatePolicy(EvaluationGateLimits{})` is valid and strict.

`0 == disabled` is rejected for the same reason, along with `Enabled bool`,
nil thresholds, optional rules, and infinity sentinels. Each is a way to turn
a gate off by accident. A caller indifferent to a maximum chooses a high
`uint64` explicitly.

### 4. Integer counts, not averages or rates

No `MinTrustScore`, `MaxAnomalyScore`, `MaxBlockRate`, `MinPresenceOverlap`,
or rate-delta gate.

Task 053's floating-point sums are deterministic for one ordered stream but
not guaranteed bit-identical under arbitrary reordering, so a float gate
could flip on replay of the same evidence. An average also lets strength in
one dimension offset a categorical event in another, which is the roadmap
rule inverted. And no approved threshold semantics exist for any of these
numbers — a threshold invented here would become the contract.

Integer comparisons are order-independent and reproducible, which is what a
hard gate needs to be auditable.

### 5. A fixed gate set, not a rule DSL

No expression language, predicate tree, CEL, JSONPath, field-name strings,
operator strings, `map[string]threshold`, or `[]GateRule`.

A generic predicate language would move policy interpretation into an
unbounded surface, weaken the compile-time schema, complicate persistence
before task 057, and — most importantly — permit gates over fields whose
semantics have not been approved. The five checks are known and named; a
struct says so and a DSL does not.

### 6. Gates keep factual names

`AddedBehaviors`, `BlockDecisions`, `CriticalRiskObservations`.

A block decision is the policy engine doing what it was configured to do. It
is not a "policy violation", "critical violation", or "unsafe action". A
critical risk reading is a risk classification, not a "security incident" or
"policy failure". An added behavior is a count, and the platform makes no
claim that new behavior is bad — the limit is the caller's acceptance policy.

Naming a gate for a judgement would smuggle the judgement into every report
that displays it, and would make the platform appear to have detected
something it did not measure.

### 7. Unsupported roadmap gates stay deferred

`critical_policy_violations`, `blocked_sensitive_actions` and
`unapproved_sensitive_actions` cannot be derived: `PolicyRule` carries no
severity and is not retained, `ContextRisk` has no repository-defined
sensitivity threshold, `ApprovalStatus` is producer-supplied evidence rather
than proof of authorization, and the aggregate keeps no per-event rule,
resource, or delegation correlation.

Substituting an available count for an unavailable one —

```text
critical risk count   != critical policy violation count
block decision count  != blocked sensitive action count
denied approval count != unapproved sensitive action count
```

— would produce a gate whose name promises a guarantee its evidence cannot
support. That is worse than the gate's absence, because it reads as a check
that ran and found nothing.

The roadmap is corrected to separate implemented evidence-backed gates from
deferred semantic ones, keeping the longer-term intent and stating why it
waits. That is an architecture correction, not a quiet scope reduction.

### 8. A failed gate is a normal result, not an error

`MaxAddedBehaviors = 0` with one added behavior returns FAIL and a `nil`
error. An error means the gate evaluation itself could not be trusted —
unbound policy, unbound scorecard, structurally impossible state. Conflating
the two would make callers handle a routine policy outcome in an error path,
where it is easy to log and continue.

### 9. No check compensates for another

Every gate is evaluated on every call; there is no short-circuit. A result
whose evidence gates both failed still reports the real added-behavior, block
and critical-risk counts, because an auditor reading a FAIL needs to see
everything measured rather than everything up to the first problem.

`PASS` requires all five. There is no weighting path to remove because none
is ever built — the absence of a compensating mechanism is the invariant, and
it is what makes "a high average cannot override a critical violation" true
by construction rather than by policy.

The result is fixed-shape for the same auditability reason: `Passed bool`
alone would not say which check failed, while `[]Failure` or
`map[string]GateResult` would be variable-shape and would accept unknown
keys. A struct of named checks is both auditable and closed.

### 10. A verdict is not a promotion

`PASS` means exactly *all five task 056 hard gates passed*. It does not mean
safe, secure, approved, deployable, or promotable, and no field carries those
names. The evaluation function is named for what it does — it is not
`Promote`, `Approve`, `Release` or `Deploy` — and a `PASS` has no side
effect. [Task 066](../tasks/v1.0/README.md) owns promotion workflow, which
needs environment transitions, authorization, and an audit trail that none of
these types carry.

## Alternatives considered

**A weighted composite score with a threshold.** Rejected by the roadmap rule
and by ADR 0028's reasoning: a composite becomes the number callers read
instead of the gates, and it is precisely the mechanism by which a high
average overrides a critical condition.

**Gating on rates rather than counts.** A rate normalizes away the thing the
gate cares about. Ten block decisions in ten records and ten thousand are the
same rate and very different evidence — and the rate is undefined exactly
when evidence is absent, which is when fail-closed matters most.

**Making evidence sufficiency configurable.** Rejected: a caller could
disable the only check standing between "ran nothing" and "passed
everything". If an empty evaluation should be acceptable, that is a decision
for the promotion workflow with its own record, not a threshold quietly set
to zero.

**Deriving severity from `PolicyRule` names.** Rejected. It would make rule
naming load-bearing for security semantics, break silently on rename, and
create a severity contract by accident rather than by decision.

**Optional or individually disabled gates.** Deferred, not rejected forever.
A fixed profile is easier to audit, persist, display and test, and this is
the first version of the contract. Adding optionality later is additive;
removing it after callers depend on it is not.

## Consequences

The gate layer is deliberately small and will look under-powered next to the
roadmap's original list. That gap is now explicit in the roadmap rather than
hidden behind fields reporting zero.

Callers wanting severity, sensitivity, or approval gates must wait for
explicit evidence contracts. When those arrive they will add new evidence and
new named gates; they will not reinterpret the existing counts.

The fixed five-gate profile means adding a sixth gate is a schema change
visible to persistence (task 057) and transport (task 058+). That is the
intended cost: a new gate is a policy decision that should be reviewed, not a
configuration line.

Because `BehaviorSummary` counts are `int` and gate counts are `uint64`, a
structural guard runs before conversion. A negative or arithmetically
impossible summary is an error rather than an enormous `uint64` — the one
place where a silent conversion could turn corrupted evidence into a passing
gate.
