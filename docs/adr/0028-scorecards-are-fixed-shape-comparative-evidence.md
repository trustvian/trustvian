# 0028 — Scorecards are fixed-shape comparative evidence

**Status:** Accepted

## Context

[Task 055](../tasks/v1.0/055-evaluation-scorecards.md) composes the bounded
evidence tasks 053 and 054 already produce into a comparison of two
evaluations. The shape of that comparison decides two things that are hard to
undo: what later tasks read, and what callers reach for instead of reading it.

The roadmap names a scorecard as an input to promotion, and states the rule
that constrains it:

> Promotion must never rest on an aggregate score alone... a high average
> cannot override a critical violation.

[ADR 0026](0026-evaluation-aggregation-is-bounded-evidence.md) anticipated
this task and said a scorecard "is an interpretation with its own thresholds".
That framing is refined here: interpretation and *acceptance* are separable,
and only the first belongs at this layer.

## Decision

**An evaluation scorecard is a fixed-shape, multi-dimensional comparison
derived from bounded aggregate and behavioral-diff evidence. It contains no
overall score, no weights, no thresholds, and no verdict.**

### It consumes the reducers' outputs, not records

Input is `EvaluationAggregate` × 2 plus one `BehaviorDiff`. No
`DecisionRecord`, no snapshot, no stream.

Records were already consumed once, by two reducers designed against the same
evidence. A scorecard that re-read them would be a third ingestion path with a
third opportunity to disagree with the other two — and would need its own
bound, its own validation, and its own answer to duplicates.

Both aggregates are required rather than just the candidate's, because the
card's purpose is comparison: *reference block rate → candidate block rate*
cannot be expressed from one side. Equally, the diff cannot be derived from
the aggregates, nor they from it. The two inputs answer different questions,
which is why both exist.

### It is fixed-shape, and does not retain the deltas

Typed identifiers, counters, small fixed structs, copied metric summaries.
No slice, map, pointer, interface, or retained input object.

Task 054's ≤1024 `BehaviorDelta` values are deliberately **not** carried. They
are already available on the `BehaviorDiff` a caller holds; duplicating them
would make the card's size and construction cost scale with behavioral
cardinality for data the caller can already reach. Because only summary
accessors are read, construction cost is independent of how many records were
aggregated and how many behaviors were compared — which is the property that
makes a scorecard cheap to produce, persist, and serve later.

### There is no overall score

No `OverallScore`, `SecurityScore`, `SafetyScore`, `Grade`, or `Rating`, and
no configurable weights.

Three reasons, in increasing order of weight:

1. **No approved weighting model exists.** `0.4 × behavior + 0.3 × trust` is a
   policy statement wearing arithmetic, and nothing in this repository has
   decided those numbers.
2. **A composite would be read instead of the gates.** Given one number and
   seven distributions, callers use the number. It would become the de facto
   promotion criterion regardless of what the gate task decides.
3. **It would violate the roadmap's own rule.** A weighted average lets
   strength in one dimension offset a critical condition in another — exactly
   what "a high average cannot override a critical violation" forbids. Building
   the number and then instructing people not to use it that way is not a
   design.

The card is multi-dimensional because the question is.

### Categorical evidence keeps factual names

`Block` is not renamed `violation`. `critical` risk is not a critical policy
violation. `Denied` is evidence that a producer reported denial, not proof of
noncompliance. `matched rule` says a rule matched, and nothing about its
severity — rule names are not retained at all, so severity could not be
inferred even if it were meaningful to try.

Renaming evidence to conclusions is how a measurement becomes an accusation
without anyone deciding it should.

### Unsupported semantics are absent, not zero

The roadmap names metrics the current evidence cannot support:
`critical_policy_violations`, `blocked_sensitive_actions`,
`unapproved_sensitive_actions`, per-rule compliance, per-resource
sensitivity, delegation stability.

None appears on the card — **not even as a field reporting zero**.

`CriticalPolicyViolations: 0`, computed from evidence with no notion of
severity, is a false security claim and the most dangerous shape one can take:
it reads as a measurement that found nothing rather than a measurement that
never ran. A reviewer seeing the field absent asks why; seeing it zero, they
move on.

Task 056 inherits this as a constraint. If it wants those gates, it must first
define where severity and sensitivity come from.

### Empty denominators are undefined

Every rate, delta, mean comparison and presence ratio reports availability
alongside its value, and reports unavailable when its denominator is zero.

`0.0` for "nothing was observed" collapses it with "observed, never occurred",
and the collapse is always toward looking safe: a candidate that ran nothing
would show a zero block rate, a zero critical-risk rate, and — if overlap
defaulted to `1.0` — perfect behavioral stability. Every one of those is a
fabrication, and together they describe an ideal candidate that did not run.

An empty evaluation is valid evidence that nothing happened. It is not
evidence that nothing bad happened.

### Gates and thresholds remain task 056's

No `max_added_behaviors`, `max_block_rate`, `min_trust_score`. No `Passed`,
`Promotable`, or `Verdict`.

The split this ADR draws, refining ADR 0026's wording:

```text
scorecard:  derived comparative metrics — what changed
gate:       acceptance thresholds and a verdict — whether that is acceptable
```

Thresholds are configuration. A component that computed facts *and* applied
configuration to them would be the place decisions are made while looking like
the place facts are counted.

### `Trust.Score` is not redefined

It stays an event-level engine signal with its own documented formula and
compatibility promise. The card compares aggregate summaries of it, under its
own name, and introduces no competing notion of trust at evaluation level.

## Alternatives considered

**One score with documented weights.** Rejected above. The variant "ship the
score but tell people not to gate on it" is worse, because it relies on
discipline against the path of least resistance.

**Retain the deltas for convenience.** Rejected. The caller already holds the
diff, so it buys nothing and costs the fixed-shape property that makes the
card cheap to persist and serve.

**Report unsupported metrics as zero with a `supported` flag.** Rejected. A
flag beside a number is read less often than the number, and the failure mode
is silent. Absence is self-documenting: nothing to misread.

**Default empty overlap to 1.0** ("no behaviors changed, so identical").
Rejected. It is defensible arithmetic and an indefensible claim — two
evaluations that observed nothing are not behaviorally identical, they are
unmeasured.

**Build the card from the candidate aggregate alone, plus the diff.**
Rejected. Every comparative row would lose its reference side, and the card's
reason to exist is comparison.

## Consequences

The card is cheap and constant: no scan, no growth with either input's size,
and safe to hold or hand onward.

It is also deliberately incomplete. Several metrics the roadmap names are not
here, and task 056 cannot build the corresponding gates until the evidence
they need is defined. That is the intended outcome — the alternative was
inventing the semantics, and inventing security semantics is how a gate ends
up asserting something nobody measured.

Callers wanting per-behavior detail go back to the `BehaviorDiff`. Callers
wanting a single number will not find one, and the absence is the point.
