# 078 — Behavioral Scenario Suites

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [054](054-behavioral-diff.md),
[055](055-evaluation-scorecards.md),
[056](056-deterministic-hard-gates.md),
[062](062-integrated-local-developer-workflow.md),
[077](077-unified-otlp-local-dev-runtime.md)

The engine's sequence-aware signals — transition and n-gram deviation and
rarity, from the `v0.6` sequence work — are consumed as existing authoritative
evidence rather than depended on as a milestone. See
[Ordering](#ordering-the-runner-asserts-none-the-engine-may-still-care).
Blocks: [072](README.md) — the OSS `v1.0` release gate

## Objective

Make behavioral testing repeatable: run the same scenario N times against a
reference and N times against a candidate, diff the behavior, and gate the
difference over evidence that survives a nondeterministic workload.

```text
run the same scenario, N times per side
        ↓
collect behavioral evidence per repetition
        ↓
compare against the reference
        ↓
count, per behavior, how many of the N runs showed it
        ↓
apply deterministic limits over those counts
```

Not *"judge whether the answer was good"*. Trustvian has no opinion about
answer quality and this task does not give it one.

## Why

**Every piece of this exists except repeatability.** Behavioral diff (054),
scorecards (055) and deterministic gates (056) are implemented, and after task
077 a workload runs under one command. What is missing is the thing that makes
the loop a *test*: a way to say "run this, the same way, again" and have the
evidence line up.

**The demo shows the shape and the friction.** A reference run and a candidate
run of the same agent produce comparable evidence — but only if somebody
drives both identically, creates both runs, remembers both identifiers and
passes the right limits to `eval compare`. That is a workflow held together by
a developer's memory, which is exactly the thing a scenario file replaces.

**CI is where a behavioral regression should be caught.** A candidate that
quietly gained an `export_customer` behavior is the case Trustvian exists to
notice, and noticing it in review beats noticing it in production. The gate's
exit-code contract already makes that scriptable; nothing assembles the run.

**And one run of an LLM-driven agent is not evidence.** This is the reason the
task cannot stop at "run it again". An agent whose code did not change may call
a tool in one execution and not the next — task 074 already records the
property, for the same demo workload this task would use:

> The demo is model-driven, so that order is the model's choice and differs
> between runs.

Compare one such run with one other and `max_added_behaviors: 0` FAILs on
unchanged code, because task 054 classifies a behavior as `Added` when
`reference == 0 && candidate > 0` and a single reference execution is a single
sample of a distribution. A gate that fails a third of the time on code nobody
touched is worse than no gate: a team meets it twice and learns to re-run CI,
which costs them the one signal the gate exists to carry — the same failure
[ADR 0033 § 9](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) reasoned
through for uninterpretable verdicts.

So repetition is not a convenience knob bolted onto this task. It is the
difference between a behavioral gate and a flaky one, and it is why the gate
limits below are expressed over *counts of runs* rather than over presence in
one.

## Scope

- A **scenario**: a declarative description of how to run a workload
  repeatably, how many times to run it, and the limits its behavior must
  satisfy.
- A **runner** that executes a scenario N times per side, collects evidence
  through the existing pipeline, and associates the result with a reference.
- A **platform aggregation** over the N per-repetition comparisons, producing
  per-behavior integer evidence — in how many reference runs and how many
  candidate runs each behavioral identity appeared — and a gate over it.
- A machine-readable result, and an exit code that follows the contract that
  already exists.

## Non-goals

This list is the task's boundary, and it is longer than its scope on purpose.

**No answer evaluation of any kind.** No expected natural-language answer, no
LLM-as-a-judge, no rubric, no hallucination score, no groundedness or RAG
relevance metric, no similarity threshold, no golden-answer semantics. Those
answer *"is this model any good"*, which the roadmap already names as a
different product.

**No dataset platform.** A scenario may name fixture inputs, because a program
needs input to run. That does not make Trustvian a dataset store: there is no
dataset entity, no versioning, no splits, no labelling, no curation and no
dataset API.

**No prompt registry, versioning or playground.** No model benchmark, no
provider comparison, no cost or token accounting.

**No new evaluation logic in the runner.** The runner computes no diff, no
scorecard, no gate, no policy outcome, no ordering comparison and no presence
count. It calls the control plane, which owns all of them — including whatever
the engine recorded about sequence. The repeated aggregation this task adds is
*control-plane* logic, on the authoritative side of that line; see
[the aggregation](#where-the-counting-lives-a-new-platform-aggregation).

**No statistical inference.** Repetition produces integer counts of runs and
nothing else: no p-value, significance test, confidence interval, variance,
standard deviation, distribution fit, flakiness score or stability score. `k` of
`N` is a threshold a caller states, not an inference the platform draws, and
ADR 0029's reasoning against floats in a verdict applies unchanged.

**No new exit-code scheme.** See [CI](#ci).

## What a scenario is

Illustrative, and deliberately not final — the syntax must be designed against
the repository's existing config conventions rather than invented here:

```yaml
name: support-login

runs: 5

command:
  - python
  - agent.py

inputs:
  - fixtures/login-ticket.json

gate:
  added_candidate_presence_minimum: 4
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
```

Five things and nothing else: what it is called, how many times to run it, how
to run it, what to feed it, and what its behavior must satisfy.

Every field is required and none has a silent default, for the reason
[task 056](056-deterministic-hard-gates.md) gave: zero is a meaningful strict
value for a maximum, so zero cannot also mean "unset", and the failure
direction of an inferred default is toward looking permissive. An omitted field
is a usage error naming it, before anything runs.

The existing config surface is schema-versioned YAML parsed in one place; a
scenario file should follow that convention rather than introducing a second
configuration mechanism.

### `runs` is required and bounded

```text
runs         required · integer · 1 <= runs <= 64 · no default
```

`runs: 0` is a usage error, not a strict limit: zero executions produce no
evidence, and the existing minimum-evidence gates exist precisely so an
evaluation that never happened cannot look perfect.

`runs: 1` is legal and is the degenerate case, not a deprecated one — see
[N = 1](#n--1-is-todays-behavior-exactly).

The upper bound is stated rather than derived. One scenario execution at
`runs: N` creates `2N` evaluation runs and `2N` learning scopes, executes the
workload `2N` times, and holds `N` comparisons in memory before gating; 64 is
where that stops being a CI job. The repository's existing bounded-collection
limit is also 64, so the figure is at least consistent with a bound this
project already chose, and the specification says plainly that the real
constraint is cost per repetition rather than any property of 64.

### Per-behavior evidence is integer counts of runs

For each behavioral identity appearing anywhere in the scenario execution, the
evidence is exactly two integers:

```text
reference_runs_present    how many of the N reference runs showed it   [0, N]
candidate_runs_present    how many of the N candidate runs showed it   [0, N]
```

Integers only, matching
[ADR 0029](../../adr/0029-hard-gates-use-explicit-integer-evidence.md): no
frequency, no proportion, no rate, no confidence interval, no significance
test. `4 / 5` is rendered from the pair for a human; the gate compares the
integers. A float here would reintroduce exactly the replay-ordering
sensitivity ADR 0029 excluded from every gate.

Behavioral identity is `FingerprintID`, unchanged from
[task 054](054-behavioral-diff.md). The repetition index is not part of it.

**The control plane computes this, never the runner.** The runner drives
executions and reports what came back; the counting, like the diff, the
scorecard and the gate, is authoritative platform logic. This is the same
prohibition the task already carries, extended to the one genuinely new
computation it introduces.

### Where the counting lives: a new platform aggregation

The task must state which existing layer owns this, and the answer is **none of
them** — it is a new aggregation composing the existing chain unchanged.

**Not an extension of task 054's diff.** `CompareBehaviorSnapshots(reference,
candidate)` is a binary pure function over exactly two *complete* snapshots, and
[task 055](055-evaluation-scorecards.md) checks a one-to-one correspondence
against it:

```text
reference.RecordCount == diff.ReferenceObservationCount
candidate.RecordCount == diff.CandidateObservationCount
```

Widening the diff to N snapshots per side breaks that correspondence — there is
no single `RecordCount` for N runs — and changes a published signature every
existing caller depends on. Task 054 was deliberately scoped to answer a
presence question about one pair, and it answers it correctly.

**Not an extension of task 055's scorecard.** The scorecard is fixed-shape and
*deliberately does not retain* the diff's ≤1024 per-behavior deltas
([ADR 0028](../../adr/0028-scorecards-are-fixed-shape-comparative-evidence.md)),
which is what makes its construction cost independent of behavior count.
Per-behavior `[0, N]` integers are precisely the per-behavior rows it refused to
hold. Adding them would undo the property the type exists for.

**So: a new aggregation over N unchanged chains.**

```text
repetition i:  reference run ─┐
                              ├─▶ aggregate + collector ─▶ diff ─▶ scorecard
               candidate run ─┘
                                        (existing, unchanged, per repetition)
                                                    │
                       N scorecards + N diffs ──────┘
                              ↓
               RepeatedEvaluationEvidence          ← new: per-behavior [0, N]
                              ↓
               EvaluateRepeatedEvaluationGate      ← new: named gates below
```

Consuming the N diffs rather than re-reducing the `DecisionRecord` stream is the
point. A second reducer over records would be the fourth ingestion path tasks
055 and 056 each declined to add, with a fourth chance to disagree with the
other three.

Reference repetition *i* is paired with candidate repetition *i* solely so the
existing pair-shaped chain can be reused. **The pairing carries no meaning**:
presence counts are invariant under any permutation of either side's
repetitions, and the task must assert that by test rather than by comment.

Bounds, following task 054's figures because the evidence is the same kind:

```text
repetitions                     <= 64
distinct behavioral identities  <= 512 across the whole scenario execution
per identity                    2 uint64 counters, each <= N
```

The 512th distinct identity is admitted and a 513th is refused, marking the
evidence permanently incomplete, and **the gate refuses incomplete evidence** —
the same refusal, for the same reason, that `CompareBehaviorSnapshots` makes
for `ErrIncompleteSnapshot`. A confident `RepeatedAddedBehaviorCount` derived
from evidence that stopped early is the failure mode task 054 named as the
worst thing its type could get wrong, and it is no better one layer up.

### New named gates over that evidence

New gates, not a reinterpretation of the existing ones. ADR 0029 is explicit
that later evidence contracts "will add new evidence and new named gates; they
will not reinterpret the existing counts", and `MaxAddedBehaviors`,
`MaxBlockDecisions` and `MaxCriticalRiskObservations` keep their exact task 056
meanings for `eval compare`.

```text
added_candidate_presence_minimum        k · required · 1 <= k <= N
added_reference_presence_maximum        j · required · 0 <= j <  k
max_repeated_added_behaviors                required · uint64 · 0 is strict
max_block_decisions_per_run                 required · uint64 · 0 is strict
max_critical_risk_observations_per_run      required · uint64 · 0 is strict
```

A behavior is **repeatedly added** when, and only when:

```text
candidate_runs_present >= k    and    reference_runs_present <= j
```

`j < k` is validated at parse time rather than assumed. With `j >= k` a behavior
appearing equally often on both sides would satisfy both halves and be reported
as added, which is not what the word means.

Six checks, evaluated in this stable order, all evaluated on every call with no
short-circuit — the composition rule task 056 established and this gate
inherits:

| # | Check | Rule |
|---|---|---|
| 1 | Reference repetitions completed | `== N` |
| 2 | Candidate repetitions completed | `== N` |
| 3 | Repetitions failing minimum evidence | `== 0` |
| 4 | Repeatedly added behaviors | `<= max_repeated_added_behaviors` |
| 5 | Worst candidate block count across repetitions | `<= max_block_decisions_per_run` |
| 6 | Worst candidate critical-risk count across repetitions | `<= max_critical_risk_observations_per_run` |

Checks 1–3 are evidence-sufficiency gates and are not configurable away, for
the reason [ADR 0029 §
2](../../adr/0029-hard-gates-use-explicit-integer-evidence.md)
gives: a scenario that ran nothing satisfies every maximum.

Checks 5 and 6 take the **maximum across repetitions**, not a sum and not a
mean. The limit therefore keeps exactly the per-comparison meaning task 056
gave it — hence the `_per_run` suffix, which says so in the name. A sum would
silently make a limit of `2` mean something different at `runs: 5` than at
`runs: 1`; a mean would let one clean repetition offset a blocking one, which is
the compensating path ADR 0029 § 9 exists to prevent.

`PASS` iff all six pass. As in task 056, a failed gate is a normal result with a
`nil` error, and `PASS` means exactly *these six checks passed* — not safe, not
promotable.

### N = 1 is today's behavior, exactly

```text
runs: 1
gate:
  added_candidate_presence_minimum: 1
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors:            M
  max_block_decisions_per_run:             B
  max_critical_risk_observations_per_run:  C
```

At `N = 1, k = 1, j = 0`, "present in at least 1 of 1 candidate runs and at most
0 of 1 reference runs" is literally task 054's `reference == 0 && candidate >
0`.
So the six checks above reduce to task 056's five, and the verdict is identical
to `EvaluateEvaluationGate` on the single pair with
`EvaluationGateLimits{MaxAddedBehaviors: M, MaxBlockDecisions: B,
MaxCriticalRiskObservations: C}`.

This is a required test, not a remark. It is what makes the new gates an
addition rather than a redefinition, and it is the test that fails if anyone
later "simplifies" the presence predicate.

### Learning isolation across repetitions

**Each repetition, on each side, runs under its own `BehavioralProfileRef`.**

The alternative is what forces the decision. If the N repetitions share one
learning scope, repetition 2 is analyzed against a baseline that already
contains repetition 1. Novelty, frequency, time-pattern and every sequence
signal then differ between repetitions *because of the order they ran in*, so
`new_behavior` evidence, anomaly scores and risk levels become a function of
repetition index. The presence counts would be measuring learning order rather
than the workload's nondeterminism, which is the one thing the repetitions exist
to measure.

Three existing decisions make per-repetition scopes the cheap answer rather
than a new mechanism:

- [ADR 0024](../../adr/0024-learning-scope-is-a-baseline-key-dimension.md)
  makes a learning scope a `baseline.Key` dimension selected at Engine
  construction, exactly for partitioning learned history. Allocation and reuse
  of profiles is named in `docs/ROADMAP.md` as platform policy that remains
  open; this task settles it for repetitions and claims nothing about the
  general policy.
- [Task 051](051-behavioral-profile-learning-scope-isolation.md) kept scope out
  of behavioral identity, so the same action in two scopes yields the same
  `FingerprintID`. Task 054 already permits the two sides of a comparison to
  carry different profile refs for this reason. Cross-scope comparison is not a
  workaround here; it is the case the diff was designed for.
- ADR 0024 makes the 512-fingerprint admission bound per scope, so N
  repetitions do not divide one baseline's budget between them. Each gets its
  own, and the fingerprint-admission conflict task 051 owns is not tightened by
  this task.

**The repetition index is correlation metadata.** It obeys the same rule as
`SessionID` and `EvaluationRunID`: it never enters `StableFeatures`, the
fingerprint hash, or any field of a `DecisionRecord`. Profile refs are opaque
strings the core never parses (ADR 0024 § *Scope is opaque*), and the runner's
mapping from repetition to profile is internal to the runner — nothing requires
the ref to encode an index, and the specification must not make it do so. The
index appears in exactly one place: the machine-readable result, as correlation,
so a human can find the repetition a count came from.

Scope selection also stays **configuration, never telemetry**, as ADR 0024
requires. The runner chooses each repetition's profile; the workload cannot
influence it, and no scope is derived from a span, a session or an attribute.

One consequence stated rather than hidden: a freshly allocated scope has learned
nothing, so the engine reports maximal novelty in every repetition. That is
*uniform* across repetitions by construction, so it adds no false variance to
the presence counts — but it does mean checks 5 and 6 at a strict limit may fail
for reasons unrelated to the candidate. This is not new to this task; a single
evaluation run against a fresh profile already has the property. Whether a
scenario should warm one shared profile *before* the measured repetitions, and
isolate only those, is the question the measurement below has to answer, and it
is recorded as an open question rather than assumed either way.

## Measurement before implementation

None of the numbers above is known. `k`, `j` and a default `N` are guesses until
somebody measures a real agent, and a guessed threshold that ships becomes the
contract.

So, **before 078 is implemented**:

1. Take one real, instrumented, **nondeterministic** agent. The task 074 demo
   workload qualifies and is already in the repository: its action order is the
   model's choice and differs between runs.
2. Run it against **itself, unchanged**, 10 or more times, using the same
   execution as both reference and candidate.
3. Record the **false-FAIL rate** — the share of comparisons that FAIL although
   nothing changed — under the current task 056 gates at `N = 1`, and under the
   gates above at `N >= 5`.
4. Record whether checks 5 and 6 are usable at all against a freshly allocated
   learning scope, since that decides the warming question above.

That result sets the documented guidance for `N` and `k`, and is recorded in
this task and in its ADR, with the measurement method reproducible by a reader.

The measurement is allowed to refute this amendment. **If the false-FAIL rate at
`N = 1` is already zero against a real agent, the k-of-N machinery is not
justified and this section should be reconsidered rather than implemented** — a
specification whose own measurement cannot contradict it is not measuring
anything. The repository's own evidence points the other way, but "points the
other way" is not a number, which is the whole reason this section exists.

## What it produces

```text
support-login   runs 5

Behavior                  reference   candidate
  crm_lookup                    5/5         5/5
  knowledge_search              5/5         5/5
  send_email                    5/5         5/5
  export_customer               0/5         5/5   + added

Gate
  FAIL   repeatedly added behaviors 1 / max 0
         (present in >= 4 of 5 candidate runs, <= 0 of 5 reference runs)
```

Every behavior carries its two counts, on both sides, whether or not it was
classified as added. A reader has to be able to see *why* something did or did
not cross the threshold, which a bare list of added behaviors does not show.

The counts are what distinguish the finding from the noise this task exists to
absorb:

```text
0/5 → 5/5    a behavior the candidate gained, every time        added
0/5 → 1/5    one appearance in five                             below k = 4
3/5 → 3/5    the workload is nondeterministic on both sides     not added
```

The added behavior is the finding. Whether the agent's answers were good is
not asked and not answered.

### Ordering: the runner asserts none, the engine may still care

Two statements that are easy to collapse into one wrong statement.

**The runner defines no ordering rule.** A scenario file contains no expected
sequence, no step list to match and no order comparator. A model-driven
workload chooses its own order, and a *scripted* sequence assertion would be
testing the model rather than the agent's behavior.

**The engine's sequence evidence remains authoritative.** Trustvian already
learns sequence: `transition_deviation`, `transition_rarity`,
`ngram_deviation` and `ngram_rarity` are real anomaly contributors from tasks
026 and 027. A reordered candidate may therefore legitimately produce a higher
anomaly score, a higher risk level or a `BLOCK` decision — and those feed
`MaxBlockDecisions` and `MaxCriticalRiskObservations` in the existing hard
gate.

So this is the wrong rule, and it is not this task's:

```text
✗  a reordered scenario with the same behavior set still passes
```

and this is the right one:

```text
✓  reordering alone creates no runner-level pass or fail rule.
   The verdict remains entirely the control plane's existing
   diff, scorecard and gate result — including whatever the engine
   recorded about sequence novelty.
```

If a reorder causes the engine to emit critical risk or a block decision and
an existing hard gate fails on it, **the scenario result is FAIL**, and that
is correct: the behavior genuinely changed in a way Trustvian is built to
notice. If the engine produces no gated evidence from the reorder, it passes.
The runner decides neither case, and must not be able to.

### Diff is presence; evaluation is more than diff

A related distinction worth stating because the two are routinely conflated:

```text
BehaviorDiff        added · removed · shared — behavioral presence (task 054)

engine evidence     may additionally include learned sequence novelty,
                    which reaches the scorecard through decision and risk

scenario runner     owns neither, and overrides neither
```

Task 054's diff being set-oriented does **not** mean the evaluation ignores
order. It means the *diff* answers a presence question while the *scorecard*
carries evidence the engine produced, sequence signals included.

**Repetition does not change this either.** The per-behavior counts are counts
of *presence per run*, so they are a repeated presence question, not an ordering
one. A scenario still asserts no sequence, and the engine's learned sequence
evidence still reaches the verdict through checks 5 and 6 of the repeated gate,
exactly as it reached task 056's checks 4 and 5.

## Architecture

```text
scenario file
    ↓
runner            ← this task: invocation, repetition, correlation, reporting
    ↓
trustvian dev     ← task 077: the repeatable local runtime
    ↓
existing pipeline ← unchanged
    ↓
ControlPlane.CompareEvaluations         ← diff · scorecard · gate, per
    ↓                                     repetition, server-owned, unchanged
ControlPlane.CompareRepeatedEvaluations ← new: per-behavior [0, N] counts
                                          and the repeated gate, server-owned
```

The runner is an **adapter**, in exactly the sense ADR 0023 and ADR 0033 use:
it drives the control plane over its existing surface and decides nothing. A
runner that computed its own diff would be the duplication those ADRs exist to
prevent, and it would drift from the server's answer the first time either
changed.

**Repetition does not relax that.** The runner executes the workload N times
per side and submits the run identifiers; it does not count presence, does not
decide which behaviors are repeatedly added, and does not compose a verdict from
N per-repetition verdicts. A runner that summarized N results into one would be
a second gate implementation with a second set of composition rules, and the
interesting failure is that it agrees for a year and then diverges toward a
false PASS.

Reference association is the one genuinely new piece of state, and the task
must settle it: how a scenario names the run it compares against. Candidates
to evaluate include an explicit reference run identifier, the most recent
completed run of the same scenario on the same agent, or a run recorded
against a named environment. Whichever is chosen must be deterministic and
must fail loudly when the reference is missing — a comparison against nothing
is not a pass.

With repetition, the reference is **N runs rather than one**, and the rule has
to name a set deterministically. A "most recent completed run" convenience
default becomes "the most recent completed *scenario execution* of the same
scenario on the same agent, and all N of its reference repetitions" — never a
mix drawn from two executions, because repetitions from different executions
were produced under different conditions and counting them together would
manufacture a distribution that never ran. A reference execution whose
repetition count differs from the candidate's `runs` fails loudly; it is not
truncated to fit.

## CI

```bash
trustvian eval run --scenario scenarios/support-login.yaml
```

The exact command shape is an implementation decision within the existing CLI
conventions. The exit code is **not**:

```text
0   gate PASS
1   gate FAIL          ← the existing eval compare meaning, unchanged
2   usage
3   operational — API, network, or the workload itself failing to run
```

`docs/compatibility.md` states that `1` means gate FAIL for `trustvian eval
compare`. A scenario run is a gate evaluation, so if it lives in the `eval`
family it inherits that meaning rather than redefining it. **A workload that
crashes is `3`, never `1`** — a broken test run is not a behavioral
regression, and conflating them would make every CI failure ambiguous.

## Bounds and failure semantics

| Situation | Behaviour |
|---|---|
| the workload exits non-zero in **any** repetition | operational failure, `3`. No gate verdict is produced, and the remaining repetitions do not run — see below |
| the workload produces no evidence in a repetition | check 3 fails it — task 056 made minimum evidence mandatory precisely so an empty run cannot look perfect, and one empty repetition out of N is still an empty run |
| the reference is missing, or has a different repetition count | operational failure, named. Never an implicit pass, and never truncated to fit |
| behavioral evidence saturates, in a repetition or across the execution | reported as incomplete, distinct from a gate failure — the existing `ErrIncompleteSnapshot` semantics, extended to the aggregation |
| `runs` absent, zero, or above 64 | usage failure, `2`, before anything runs |
| `k` outside `[1, N]`, or `j` outside `[0, k)` | usage failure, `2`, naming the field, before anything runs |
| a scenario file is malformed | usage failure, `2`, before anything runs |
| a suite of scenarios | each runs independently; one failure does not abort the rest unless asked, and the summary is machine-readable |

Scenario count, repetition count, per-scenario duration and output size are all
bounded, with the bounds stated rather than implied.

### One crashing repetition aborts that scenario

Decided, rather than left to implementation: **a repetition that crashes ends
the
scenario with exit `3`, and the remaining repetitions are not executed.** The
suite is unaffected — the row above still holds, and other scenarios run.

Three reasons, in order of weight:

1. **The declared limits mean something specific about N.** "Present in at least
   4 of 5 candidate runs" evaluated over 4 runs is a stricter test than the one
   the author wrote, and evaluated over 3 it is stricter again. Silently
   re-deriving a verdict from fewer repetitions changes the policy without
   telling anyone, in the direction of whichever side happens to lose a run.
2. **Checks 1 and 2 exist to refuse exactly this.** They require N completed
   repetitions per side for the same reason task 056's evidence-sufficiency
   gates are not configurable away: partial evidence that passes every maximum
   is the fail-open shape those gates were added to close.
3. **The remaining repetitions cannot be gated**, so running them spends CI time
   on evidence that has nowhere to go.

And a crash stays `3`, never `1`. A broken workload is not a behavioral
regression, and the existing contract already says so; N repetitions give N
chances to get that confusion wrong, which is why the rule is restated here.

## Security and privacy

Fixture inputs belong to the developer's repository and Trustvian does not
ingest, store or transmit their contents — it passes them to the workload.
Nothing in a scenario result contains prompts, completions, arguments,
results or bodies; the result is the diff, the scorecard and the gate, which
are already metadata-only.

Repetition adds two integers per behavior and a repetition index, all
correlation metadata. It widens no privacy surface: N metadata-only comparisons
are still metadata-only, and the repeated evidence retains strictly less per
behavior than the diffs it is built from.

A scenario file is executable configuration — it names a command — and it is
read from the developer's own repository under their own privileges, exactly
as a Makefile or a test script is. The specification should say so plainly
rather than implying a sandbox this task does not build.

## Compatibility

Additive: a new scenario file format and a new subcommand. Nothing existing
changes, and the diff, scorecard and gate contracts are consumed unchanged.

Additive for the repeated evidence too, and this matters because
`docs/compatibility.md` classifies the platform `/v1` surface as STABLE:

- `BehaviorDiff`, `EvaluationScorecard`, `EvaluationGateLimits` and
  `EvaluateEvaluationGate` are untouched. `MaxAddedBehaviors` keeps its meaning
  and `eval compare` keeps its behavior.
- The repeated aggregation, its gate, its limits and its route are **new**
  surface, published alongside rather than replacing. A new route and new
  response fields are what a minor release is allowed to add.
- Schema impact is whatever persisting a scenario execution requires, under the
  existing forward-only version-gated rule. The task must state its schema step
  explicitly if it takes one, as tasks 065, 066 and 074 each did.

## Tests

- A scenario runs its command with the declared inputs and produces evidence
  through the real pipeline.
- Two runs of one unchanged scenario produce comparable evidence — the
  repeatability property the task exists for.

**Repeated runs.** These are the tests that keep the amendment honest:

- **An unchanged workload compared with itself at N runs PASSES** under the
  documented limits. Driven by a workload whose behavior set genuinely varies
  between executions, not a deterministic stub — a stub would pass whatever the
  thresholds were and prove nothing.
- **A behavior injected into exactly k of N candidate runs FAILS at threshold k
  and PASSES at threshold k + 1**, with nothing else changed. Both halves,
  because the pair is what shows the threshold is load-bearing rather than
  decorative.
- **`N = 1` is equivalent to current behavior**: `runs: 1` with `k = 1`, `j = 0`
  and the three maximums produces the same verdict, check for check, as
  `EvaluateEvaluationGate` on the single pair with the corresponding
  `EvaluationGateLimits`. Asserted against the real gate, not against a restated
  expectation.
- **Presence counts are invariant under permutation** of either side's
  repetitions — the assertion that makes "the pairing carries no meaning" true
  rather than claimed.
- **Repetition bounds are enforced**: `runs` absent, `runs: 0` and `runs: 65`
  are each a usage error naming the field, before the workload runs; `runs: 1`
  and `runs: 64` are accepted.
- **`k` and `j` bounds are enforced**: `k = 0`, `k = N + 1`, `j = k` and
  `j = N + 1` are each a usage error naming the field.
- **A crashing repetition exits `3`, produces no gate verdict, and does not run
  the remaining repetitions** — the exit code asserted, the absence of a verdict
  asserted, and the repetition count actually executed asserted.
- **One empty repetition fails check 3** rather than being averaged away by
  N − 1 healthy ones.
- **Checks 5 and 6 take the maximum across repetitions**: one repetition with
  two block decisions and four with none FAILs at
  `max_block_decisions_per_run: 1`. The test that fails if anyone changes it to
  a sum or a mean.
- **Every check is populated in every result**, including when checks 1–3 fail —
  task 056's no-short-circuit rule, inherited.
- **Evidence saturating at 512 distinct identities refuses the gate**, and does
  not produce a confident count from truncated evidence.
- **Each repetition ran under a distinct `BehavioralProfileRef`**, asserted from
  the recorded evidence; and the repetition index appears in no `StableFeatures`
  field, no fingerprint input and no `DecisionRecord` field, asserted by the
  same structural scan style the task already uses for the runner.
- **The runner computes no presence count and no repeated verdict** — added to
  the existing source scan, alongside "no diff, scorecard, gate or policy
  outcome".
- A candidate with an added behavior produces a diff naming it and a gate FAIL
  under `max_added_behaviors: 0`.
- The same candidate passes under a limit that permits it, proving the verdict
  is the limits' and not the runner's.
- **The runner implements no order comparator**, asserted structurally by a
  scan of the runner's sources — no sequence, step-list or ordering
  comparison exists in it.
- **The runner forwards evidence through the real engine and accepts the
  server's verdict unchanged**: a control plane returning FAIL yields FAIL and
  one returning PASS yields PASS, with no runner-side adjustment in either
  direction.
- **A reorder that produces gated evidence FAILs.** A candidate whose
  reordering drives the engine to a block decision or critical-risk
  observation, under limits that refuse them, produces a scenario FAIL — the
  test that fails if anyone reintroduces a "reordering always passes" rule.
- **A reorder that produces no gated evidence passes**, under the same limits.
  Both halves, because the pair is the contract: the outcome tracks the
  engine, not the runner.
- Every gate limit is required; an omitted one is a usage error naming it.
- A crashing workload exits `3`, never `1`, and produces no gate verdict.
- A missing reference is an explicit failure, never an implicit pass.
- A run producing no evidence fails the minimum-evidence gates.
- The runner computes no diff, scorecard, gate or policy outcome — asserted by
  the same scan style that already keeps the CLI from reimplementing the gate.
- The runner imports no platform package, per ADR 0033.
- Machine-readable output is valid, complete, and emitted exactly once.

## Documentation

Written by the implementation PR: a scenario guide, `docs/platform-cli.md`,
`docs/compatibility.md` (the file format, the new route and limits, and the
subcommand's exit codes), `docs/ROADMAP.md`, this task's status, the task index
and `CHANGELOG.md`.

Also written by the implementation PR: **the measurement result**. The
false-FAIL rates from
[Measurement before implementation](#measurement-before-implementation), the
agent and the method that produced them, and the `N` and `k` guidance they
justify — in this task and in the ADR, not in a pull request description that
stops being findable.

## ADR

Warranted for the boundary decision: why a behavioral scenario compares
behavior and never answer quality, and why that keeps Trustvian out of the
generic-evaluation category. Also for the reference-association rule, which is
the task's one piece of new semantics; and for the ordering distinction — the
runner scripts no sequence, while the engine's learned sequence signals remain
authoritative and may legitimately change a verdict.

Repetition adds four more decisions the ADR must record, because each is a place
a future reader will ask "why not the obvious thing":

- **Why per-behavior evidence is a new platform aggregation** rather than a
  wider `BehaviorDiff` or a richer `EvaluationScorecard` — the correspondence
  check task 055 performs, and the fixed shape ADR 0028 chose, are the reasons
  neither could carry it.
- **Why the repeated limits are new named gates** rather than a reinterpretation
  of `MaxAddedBehaviors`, and why `N = 1` reproducing today's verdict exactly is
  the property that makes that claim checkable — ADR 0029's rule, applied to its
  first real consumer.
- **Why each repetition gets its own learning scope**, which is the first
  concrete profile-allocation policy the platform has adopted, and why it
  settles only repetitions rather than the general allocation question
  `docs/ROADMAP.md` leaves open.
- **Why the thresholds were measured before being written down**, with the
  measurement recorded rather than summarized. An ADR that states `k = 4`
  without
  the run that produced it has frozen a guess.

## Acceptance criteria

1. One scenario file describes how to run a workload, **how many times**, and
   what its behavior must satisfy.
2. Running it twice against an unchanged workload produces comparable
   evidence.
3. A candidate that gains a behavior produces a diff naming it and a
   deterministic gate result under the declared limits.
4. The verdict comes from the control plane; the runner computes nothing —
   including the per-behavior presence counts and the repeated verdict.
5. Exit codes follow the existing contract, and a crashed workload is never
   reported as a gate failure.
6. No answer-quality, judge, rubric, hallucination, relevance or benchmark
   concept exists anywhere in the feature.
7. No dataset, prompt-registry or model-comparison entity is introduced.
8. Results are machine-readable and usable in CI without parsing human output.
9. A scenario asserts no action ordering of its own, **and** does not suppress
   the engine's sequence-aware evidence: a reorder that produces gated
   evidence fails, and one that does not, passes. The runner decides neither.
10. **`runs` is required, bounded to `[1, 64]`, and has no default.** Absent,
    zero or out of range is a usage error naming the field, before the workload
    runs.
11. **An unchanged, nondeterministic workload compared with itself at N runs
    PASSES** under the documented limits — the property the repetition exists
    to deliver.
12. **A behavior present in k of N candidate runs FAILS at threshold k and
    PASSES at threshold k + 1**, with nothing else changed.
13. **`runs: 1` with `k = 1` and `j = 0` reproduces task 056's verdict
    exactly**,
    check for check, proven against the real gate.
14. **Per-behavior evidence is integer counts of runs only** — no rate,
    proportion, confidence interval or significance test appears anywhere in
    the feature.
15. **Each repetition runs under its own `BehavioralProfileRef`**, and the
    repetition index appears in no `StableFeatures` field, no fingerprint input
    and no `DecisionRecord` field.
16. **A crashing repetition exits `3`**, produces no gate verdict, and does not
    execute the remaining repetitions.
17. **The measurement in
    [Measurement before implementation](#measurement-before-implementation) has
    been performed and recorded** in this task and its ADR, and the documented
    `N` and `k` guidance cites it. This criterion is not satisfied by a
    plausible number.

## Open questions left to implementation

1. **Reference association** — explicit identifier, last completed run of the
   same scenario, or environment-scoped. An explicit identifier with a
   documented convenience default is assumed. With repetition the default names
   a whole prior scenario *execution* and all N of its reference repetitions,
   never a mix drawn from two.
2. **Where the subcommand lives** — extending the `eval` family is assumed, so
   it inherits the gate exit-code contract rather than defining one.
3. **Whether a suite is a directory or a file listing scenarios.** A directory
   is assumed.
4. **Whether scenarios run against `trustvian dev` only**, or can attach to an
   already-running runtime. Both, with the latter as the CI path, is assumed.
5. **Whether repetitions run sequentially or concurrently.** Sequential is
   assumed, because concurrent repetitions of a workload that talks to a shared
   external service measure contention rather than the workload. Concurrency is
   an optimization that needs a measurement, and it must never reorder the
   recorded repetition indices.
6. **Whether the measured repetitions should be preceded by a shared warm-up
   profile.** Unresolved on purpose: a fresh scope reports maximal novelty in
   every repetition, which is uniform and therefore harmless to the presence
   counts, but may make `max_block_decisions_per_run: 0` unusable. Step 4 of the
   measurement decides it. Whichever way it goes, the measured repetitions stay
   isolated from one another — that part is settled above, not open.
7. **What `N` and `k` the documentation should recommend.** Deliberately
   unanswered here. The measurement sets them, and no number is written into
   this specification before it exists.
