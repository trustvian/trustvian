# 078 — Behavioral Scenario Suites

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [054](054-behavioral-diff.md),
[055](055-evaluation-scorecards.md),
[056](056-deterministic-hard-gates.md),
[062](062-integrated-local-developer-workflow.md),
[075](075-ai-semantic-telemetry-normalization.md) — added after the
[measurement](#measurement-before-implementation); see
[Sequencing](#sequencing-078-follows-075),
[077](077-unified-otlp-local-dev-runtime.md),
[083](082-agent-inspection-and-evaluation-depth.md#083--behavioral-layer-identity-and-display-classification)
— added by [082](082-agent-inspection-and-evaluation-depth.md); see
[the amendment](#amendment--task-082)

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

That quotation is about **order**, and the
[measurement](#measurement-before-implementation) later showed why that matters:
order is precisely what behavioral identity ignores, so this workload's variation
never reached the diff. The claim that one run is not evidence stands; the
evidence offered for it here did not support it, and is corrected rather than
quietly kept.

Where that variation reaches a *different set of services*, comparing one such
run with one other makes `max_added_behaviors: 0` FAIL on unchanged code, because
task 054 classifies a behavior as `Added` when `reference == 0 && candidate > 0`
and a single reference execution is a single sample of a distribution. A gate that
fails a third of the time on code nobody touched is worse than no gate: a team
meets it twice and learns to re-run CI, which costs them the one signal the gate
exists to carry — the same failure
[ADR 0033 § 9](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) reasoned
through for uninterpretable verdicts.

**How often that actually happens is now partly measured, and the answer was
"not at all" for the first workload tried.** The conditional in the paragraph
above is doing real work: a model can vary its turn count, its arguments and its
action order without ever varying which services it reaches, and behavioral
identity is deliberately insensitive to all four. See
[Measurement before implementation](#measurement-before-implementation) for the
numbers and for what a workload must look like before this argument can be tested
at all.

So repetition is not a convenience knob bolted onto this task — but what it buys
is broader than the original claim. It is the difference between a behavioral gate
and a flaky one *where flakiness exists*, and where it does not, `N` runs are what
let a scenario **demonstrate** that: "every behavior, 10 of 10, isolated
profiles" is a statement no single comparison can make. The gate limits below are
expressed over counts of runs to serve both, which is why they survive a zero
false-FAIL rate.

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
- **A run-scoped behavior route**, which is this task's
  [first implementation slice](#first-slice-a-run-scoped-behavior-route) and the
  only part of it that does not wait for 075.

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

**What repetition is for, after the measurement.** This task originally justified
`runs: N` as the thing that *absorbs* presence variance. The
[measurement](#measurement-before-implementation) found no variance to absorb in
the workload it tested — and in doing so demonstrated the other half of what
repetition is for, which is the half that survives a zero rate:

> every behavior, 10/10, in all four configurations

A single run cannot say that. `N` runs with per-repetition isolation is what makes
the **absence** of variance demonstrable, and "this workload's behavior set did
not move across ten isolated executions" is a stronger statement than any single
comparison can make. That is a claim about evidence quality rather than a
threshold, it holds whether or not `k` is ever justified, and it is why `runs: N`
stays regardless of how the k-of-N question resolves.

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

### First slice: a run-scoped behavior route

The aggregation above is the whole task, and it waits for
[075](#sequencing-078-follows-075). One piece of it does not, and it should ship
first because two other tasks already need it.

```text
GET /v1/evaluation-runs/{run_id}/behaviors
```

The behavior set of **one** run: for each behavioral identity the run observed,
its `FingerprintID`, its descriptor, and how many observations carried it.

**And the fidelity the descriptor was read at — but that part waits on task 081,
not on 075.** 075 has landed, so a descriptor may now name a tool rather than a
transport; what it did not do is persist fidelity *per behavior*, which needs a
forward-only schema step in both backends. Until 081 lands, this route can return
the descriptor but not the fidelity qualifying it — and a consumer would then be
back to guessing a tool name from its shape, which is the guess the indicator
exists to remove. So the route's fidelity field ships with 081 or after it, and
this specification should not imply otherwise.

Bounded and paged exactly as [task 065](065-environment-model.md)'s collections
are, reused rather than redesigned:

```text
order      id byte-ascending
cursor     `after`, exclusive
limit      1–64, default 64
404        the run does not exist
200        a run with no evidence — an empty page, not an error
```

**Why it is worth its own slice.** There is no per-run behavior snapshot on `/v1`
today. `GET /v1/evaluation-runs/{run_id}/progress` returns counts, and
`POST /v1/evaluations/compare` is the only route that returns behaviors at all.
So the companion demo reads a run's behavior set by **comparing the run against
itself** — which `CompareEvaluations` permits only because the same-run refusal
exists for promotions and not for comparisons. Three things make that worth
replacing rather than blessing:

- it works because nothing forbids it, not because anything promises it. A
  same-run guard added for any reason would break it silently, and a consumer
  less careful than that demo would report confidently wrong counts;
- it costs `N` extra comparisons per side, each computing a full diff, scorecard
  and gate that nobody reads;
- **[079](079-ci-integration-github-action.md) and
  [080](080-metadata-only-detection-evaluation.md) both need per-run behavior
  before the full aggregation ships.** 079 renders per-run presence into a pull
  request comment; 080 needs a run's behavior set to score detections against a
  labelled benchmark. Neither should wait for k-of-N to be justified.

It publishes what a self-compare already returns, minus the scorecard and gate —
so it is new surface over existing evidence rather than a new computation, and
[Compatibility](#compatibility) covers it as an additive `/v1` route.

The demo's `make stability` switches to this route when it exists, and retires the
self-compare it documents as a workaround.

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

#### No default `k` ships, and the guidance is set semantics

`k` and `j` stay **required and explicit**, as every other limit here is. The
[measurement](#measurement-before-implementation) produced no basis for a default
and this task's own rule — a guessed threshold that ships becomes the
contract — applies to its author as much as to anyone else.

The documented guidance is therefore:

```text
k: 1   j: 0      unless presence variance has been measured for that workload
```

At `k = 1, j = 0` the predicate is plain set semantics — "in at least one
candidate run and no reference run" — which is [exactly task 054's
rule](#n--1-is-todays-behavior-exactly) and behaves identically at every `N`.
A scenario that has not measured its workload's variance therefore gets the
semantics it already understands, with `N` buying evidence quality rather than a
threshold, and the presence counts reported beside the verdict so the developer
can *see* whether variance exists before choosing to tolerate any.

Raising `k` above 1 is a deliberate statement — "this workload is known to vary,
and I have measured by how much". The specification does not make it for them, and
the result document carries `k` and `j` precisely so a reader can tell which
choice was made.

Six checks, evaluated in this stable order, all evaluated on every call with no
short-circuit — the composition rule task 056 established and this gate
inherits:

| # | Check | Rule |
|---|---|---|
| 1 | Reference repetitions completed | `== N` |
| 2 | Candidate repetitions completed | `== N` |
| 3 | Repetitions failing minimum evidence | `== 0` |
| 4 | Repeatedly added behaviors | `<= max_repeated_added_behaviors` |
| 5 | Worst candidate block count across repetitions | `<= max_block_decisions_per_run` — [advisory at `N > 1`](#checks-5-and-6-are-advisory-against-a-fresh-scope) |
| 6 | Worst candidate critical-risk count across repetitions | `<= max_critical_risk_observations_per_run` — [advisory at `N > 1`](#checks-5-and-6-are-advisory-against-a-fresh-scope) |

Checks 1–3 are evidence-sufficiency gates and are not configurable away, for
the reason [ADR 0029 §
2](../../adr/0029-hard-gates-use-explicit-integer-evidence.md)
gives: a scenario that ran nothing satisfies every maximum.

Checks 5 and 6 are marked **advisory** at `N > 1`, because against
per-repetition learning scopes they
[report `0` by construction](#checks-5-and-6-are-advisory-against-a-fresh-scope).
They are still evaluated and still fail the verdict when they fail; the marker
says the question could not be answered, so a pass is not read as an answer.

They take the **maximum across repetitions**, not a sum and not a
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

#### `trustvian dev` needs a profile flag this task can use

[Task 077](077-unified-otlp-local-dev-runtime.md) shipped `dev` deriving the
learning profile **from the candidate**: the candidate id *is* the profile, which
is correct for 077's own purpose — two runs of one commit sharing a baseline is
what makes "this behavior is new" mean anything on the second run.

It is the wrong coupling for this task. A runner needing a profile per repetition
would have to allocate a **candidate** per repetition to get one, which changes
the identity of the thing under test to obtain an isolation property that has
nothing to do with identity. The companion demo does exactly that today, and says
so as a workaround rather than a design.

So this task requires an additive flag on `dev`:

```text
--behavioral-profile <ref>    the learning scope for this run
                              default: the candidate, exactly as today
```

Three properties, none of them new mechanism:

- **Additive.** Omitted, `dev` behaves precisely as it does now. No existing
  invocation changes meaning, and 077's own documented default is untouched.
- **Opaque to the core.** A profile ref is a string the engine never parses
  ([ADR 0024](../../adr/0024-learning-scope-is-a-baseline-key-dimension.md)
  § *Scope is opaque*), so `dev` passes it through to the Collector
  configuration and nothing interprets it.
- **The mapping stays in the runner.** Nothing requires the ref to encode a
  repetition index, and this specification must not make it do so — as
  [stated above](#learning-isolation-across-repetitions). The flag accepts a ref;
  which ref each repetition gets is the runner's business.

`dev` already keeps one baseline file per profile and refuses two concurrent
writers of one, so a profile per repetition needs no change to that mechanism
either — `N` sequential repetitions produce `N` files and no contention.

### Checks 5 and 6 are advisory against a fresh scope

A freshly allocated scope has learned nothing, so the engine reports maximal
novelty in every repetition. That much was predicted here before the
[measurement](#measurement-before-implementation); what the measurement added is
the *direction* of the consequence, and it is the opposite of what this section
first worried about.

**Measured.** Anomaly *confidence* — the second number the engine reports beside
the score, precisely so
[cold start is two numbers rather than one](../../ARCHITECTURE.md#cold-start-two-numbers-not-one)
— sits at its floor in every isolated repetition and never moves:

```text
isolated   0.1786 in every one of ten repetitions (one excursion to 0.1957)
shared     0.1786 → 0.5714 → 0.7214 → 0.7857 → 0.8500 → 0.9143 → 0.9795
                 → 1.0000 → 1.0000 → 1.0000        saturated by repetition 8
```

At confidence near zero a maximally novel event contributes almost nothing to the
trust score, because `trust.Compute` combines the two as
`effectiveAnomaly = Anomaly.Score * Anomaly.Confidence`. So trust is barely
penalized, no `BLOCK` is decided, and no critical-risk observation is recorded:
**checks 5 and 6 read `0` in all ninety comparisons of every isolated
configuration.** They did not fail spuriously, which is what this section feared.
They could not fire at all.

**The conflict, stated.** Per-repetition isolation is required so presence counts
measure the workload rather than learning order. Checks 5 and 6 need a warmed
baseline to mean anything. One set of repetitions cannot provide both, and the
shared column above is what warming costs: the checks become live only after it,
and the warming is a function of repetition index — which is the contamination
isolation exists to prevent.

**Decision: keep isolation, and mark those two checks advisory.**

```text
at N > 1, with per-repetition profiles:
    check 5   worst candidate block count             advisory: fresh scope
    check 6   worst candidate critical-risk count     advisory: fresh scope
```

Both are still evaluated, still reported with their actual values, and still
contribute to the verdict when they fail — nothing is removed and no limit is
ignored. What changes is that the runner and the
[result document](#result-document) **must label them** at `N > 1`, so a scenario
setting them to `0` and seeing them pass is not read as evidence that the
candidate blocked nothing. It is evidence that the question was not asked.

The two rejected alternatives, and why:

| Alternative | Rejected because |
|---|---|
| Warm one shared profile before the measured repetitions, isolate only those | Two execution phases per side, warming runs producing evidence nobody counts, and "how much warming is enough" becomes exactly the guessed constant this task refuses everywhere else |
| Drop checks 5 and 6 from scenario gates entirely | Loses a real signal for a deployment that *does* warm a profile, and silently diverges from what `eval compare` reports for the same evidence |

This resolves what was open question 6, which asked the measurement to decide it.

## Measurement before implementation

`k`, `j` and a default `N` are guesses until somebody measures a real agent, and
a guessed threshold that ships becomes the contract. So this section required a
measurement before implementation, and said plainly that the measurement was
allowed to refute it:

> **If the false-FAIL rate at `N = 1` is already zero against a real agent, the
> k-of-N machinery is not justified and this section should be reconsidered rather
> than implemented** — a specification whose own measurement cannot contradict it
> is not measuring anything.

**It has been performed, and the rate is zero.** This section is therefore
reconsidered below rather than left standing on an expectation.

### What was measured

Performed 2026-09-27 against Trustvian `5362f51` — the commit that added
`trustvian dev`, which is what made repeated invocation cheap enough to sweep.
Recorded in the companion demo repository, at commit `e5dcaf8`:

- [`docs/results/2026-09-27-stability.md`](https://github.com/trustvian/trustvian-python-agent-demo/blob/main/docs/results/2026-09-27-stability.md)
  — conditions, per-run tables, raw counts
- [`docs/upstream/078-measurement.md`](https://github.com/trustvian/trustvian-python-agent-demo/blob/main/docs/upstream/078-measurement.md)
  — the contribution addressed to this section

Forty model-driven runs of the **unchanged** reference side of that repository's
Ollama `gemma3:4b` agent: `N = 10`, in two learning configurations (one shared
profile, and one profile per repetition), at two temperatures (0.7, and 1.3 as a
stress case above every shipping default). Gate limits were task 056's three at
zero.

```text
                        adjacent pairs   all ordered pairs   presence
shared,   T=0.7            0 / 9 FAIL         0 / 90 FAIL    every behavior 10/10
isolated, T=0.7            0 / 9 FAIL         0 / 90 FAIL    every behavior 10/10
shared,   T=1.3            0 / 9 FAIL         0 / 90 FAIL    every behavior 10/10
isolated, T=1.3            0 / 9 FAIL         0 / 90 FAIL    every behavior 10/10
```

**Zero false FAILs in every configuration**, and every behavior present in all
ten runs of every configuration.

### The agent was nondeterministic; the behavior set was not

This is the finding, and it is more useful than the rate.

Turn counts varied, and varied more at the higher temperature: 21 records in
most runs, with excursions to 22 and 23 at `T = 0.7`, and **21, 23, 25, 23, 21,
21, 25** across the isolated repetitions at `T = 1.3` — four of ten runs taking
two to four extra turns. The agent was measurably less deterministic and the gate
did not notice, because **the behavior set was four distinct identities in every
one of the forty runs.**

Two properties of this task's own design explain that, and both are working
exactly as specified:

- **At HTTP fidelity the workload's behavioral surface is saturated.** A behavior
  is a method and a destination; that agent's reference toolset has three tools on
  three hosts, the model call is the fourth, and its prompt requires all three
  actions for each of three tickets per run. For a behavior to be absent from a
  run the model would have to skip one tool for every ticket in that run.
- **Behavioral identity is not sequence-dependent**, by deliberate design in
  [task 054](054-behavioral-diff.md) and reaffirmed in
  [Ordering](#ordering-the-runner-asserts-none-the-engine-may-still-care).

Those two together are the problem with the workload this section chose. It was
selected because "its action order is the model's choice and differs between
runs" — and order *does* differ. But **order variance is precisely what the diff
ignores**, so the property the workload was chosen for cannot produce the
presence variance the k-of-N machinery exists to absorb.

### So this measurement does not qualify as the one this section needs

Both halves are stated, because only stating the first would be misleading:

- the k-of-N machinery is **not justified by this measurement**;
- this measurement **does not qualify** as the measurement this section asked
  for, because the workload it named is structurally incapable of exhibiting the
  phenomenon. A zero rate from a workload that cannot vary is not evidence that
  workloads do not vary.

The earlier claim that "the repository's own evidence points the other way" is now
contradicted by a number, and is withdrawn.

### The section stays, with two re-run conditions

This section is **not** deleted, and the machinery below is **not** implemented on
the strength of an expectation. It waits for a measurement that can fail, which
needs at least one of:

1. **A toolset wider than one run visits.** An agent with more tools than its
   prompt requires per task, so *which* services a run reaches is a real choice
   rather than a fixed consequence. Then presence counts vary for the reason this
   task cares about.
2. **[Task 075](075-ai-semantic-telemetry-normalization.md)'s tool-name
   fidelity.** At that fidelity a behavior is `export_customer` rather than
   `POST → export.localhost`, so the behavioral surface grows to the size of the
   toolset and stops being saturated by a three-step workflow. **This is the
   decisive one**, and it is why [sequencing](#sequencing-078-follows-075) placed
   this task after 075.

   **It is now available.** 075 is implemented, so this condition is satisfied as
   soon as somebody re-runs the sweep against a workload emitting an
   agent-oriented convention. That is the cheapest remaining path to the number
   this section needs, and it does not require condition 1.

Raising the temperature is *not* one of the conditions, and that is a measured
result rather than an assumption: `T = 1.3` nearly doubled the turn-count spread
and moved the FAIL rate by nothing at all.

When either condition holds, the sweep is re-run and this section is revisited
with the new numbers. Until then the guidance below is written so that a scenario
which has measured nothing gets set semantics rather than a threshold somebody
guessed.

## Sequencing: 078 follows 075

**This task's implementation follows
[075](075-ai-semantic-telemetry-normalization.md), and 079 follows this one.**

**075 has since landed, so the edge is satisfied.** What this task still waits on
is its own measurement re-run at the fidelity 075 now delivers — see
[the two re-run conditions](#the-section-stays-with-two-re-run-conditions) — rather
than on another task. The reasoning below is kept because it is why the edge was
added, and because a reader who finds a zero false-FAIL rate in the results will
otherwise ask why the k-of-N design survived it.

That was a change when it was made. The dependency list at the top of this file names 054, 055, 056,
062 and 077, and `docs/ROADMAP.md` previously placed 075 beside this thread rather
than before it, on the grounds that repetition transports whatever telemetry
exists and 075 only decides how richly it is read. That reasoning is still true
for *running* a scenario. It is not true for **deciding k**.

The [measurement](#measurement-before-implementation) is why. The k-of-N question
can only be answered by a workload whose behavioral surface is larger than one run
visits, and at HTTP fidelity a behavior is a method and a destination — so the
surface is the size of the *host set*, which a workflow-shaped agent saturates.
At 075's tool-name fidelity a behavior is `export_customer`, and the surface
becomes the size of the *toolset*. The phenomenon this task's central mechanism
exists to absorb may only be observable there.

Implementing before that is possible and would be a mistake: the thresholds would
ship unmeasured, which is the failure this task's own measurement rule was written
to prevent.

Two things are *not* blocked by 075, and should proceed:

- the [run-scoped behavior route](#first-slice-a-run-scoped-behavior-route),
  which 079 and 080 both need and which is independent of fidelity;
- the additive
  [`--behavioral-profile` flag on `dev`](#trustvian-dev-needs-a-profile-flag-this-task-can-use),
  which is a coupling fix worth making on its own terms.

079 renders this task's result document and computes nothing, so it follows this
task as it always did — one step further out now.

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
classified as added or removed. A reader has to be able to see *why* something
did or did not cross the threshold, which a bare list of added behaviors does
not show. The machine-readable form of all of this is the
[result document](#result-document).

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

### Repeatedly removed is a classification, not a gate

The gate above names only *repeatedly added* behaviors, and that leaves a hole a
consumer would otherwise fill for itself: a behavior the candidate stopped
producing is a finding too, and task 054 has classified `Removed` since it was
written. So this task defines the mirror explicitly, and stops there.

A behavior is **repeatedly removed** when, and only when:

```text
reference_runs_present >= k    and    candidate_runs_present <= j
```

The same `k` and `j` the added rule uses, reflected. No second pair of
thresholds: one `k` states how many runs make a behavior's presence real for
this scenario, and that judgement does not change direction with the comparison.

**There is no removed gate, and that is deliberate.** No maximum, no limit, no
contribution to the verdict. Task 056 gates added behaviors and not removed
ones, and inventing a limit here would be a new acceptance rule this task has no
evidence contract for — exactly what
[ADR 0029 § 7](../../adr/0029-hard-gates-use-explicit-integer-evidence.md)
requires be added as new named gates with their own review rather than slipped
in beside an existing one. A candidate that legitimately dropped a behavior
would then fail a gate nobody chose.

So the classification exists to be *reported*, and the six checks stay six. What
this buys is that no consumer has to derive it: a behavior is labelled added,
removed, or neither, by the control plane, and
[task 079](079-ci-integration-github-action.md) renders the label it was given.

## Result document

The machine-readable result is a consumed contract, not an output format, so its
required content is stated here rather than discovered by whoever parses it
first. [Task 079](079-ci-integration-github-action.md) renders it into a pull
request comment and
[task 080](080-metadata-only-detection-evaluation.md) may read it as
measurement input — two consumers, which is the point at which "whatever the
implementation emits" stops being good enough.

**Exact JSON field names are the implementation's**, and are published in
`docs/compatibility.md` when they exist. What is fixed is that each of the
following is present and machine-readable:

```text
scenario        name · runs (N) · k · j
                the thresholds, because a reader cannot check a k/N count
                against a rule the document does not carry

per behavior    behavioral identity · its descriptor
                reference_runs_present  [0, N]
                candidate_runs_present  [0, N]
                classification — repeatedly added, repeatedly removed, or
                neither, as classified above and never by a consumer

gate            the six checks, in the stable order this task defines,
                each with its actual value, its limit or rule, and its
                own pass/fail outcome — all six, including the ones
                that passed; and checks 5 and 6 additionally carry
                `advisory: fresh scope` at N > 1, because against
                per-repetition profiles they report 0 by construction

verdict         pass | fail — the closed vocabulary, nothing else

producers       the CLI version and the control-plane version that
                produced the result

identity        the scenario execution, and the reference and candidate
                evaluation run identifiers
```

Four properties of that list are load-bearing rather than incidental.

**Every behavior appears, not only the classified ones.** The counts are what
distinguish a finding from the nondeterminism this task exists to absorb, and a
document listing only added behaviors cannot show a reader why something did
*not* cross the threshold.

**All six checks appear with their outcomes.** Task 056 evaluates every gate on
every call with no short-circuit so an auditor sees everything measured; a
document that dropped the passing checks would undo that at the serialization
boundary, and would leave a consumer unable to tell a check that passed from one
that did not run.

**Checks 5 and 6 carry their advisory marker at `N > 1`.** This is the same
concern one step further: against freshly allocated scopes those two
[report `0` by construction](#checks-5-and-6-are-advisory-against-a-fresh-scope),
so a document that showed them passing without saying so would leave a consumer
unable to tell a check that passed from one that could not fail. Measured, not
inferred — they read `0` in all ninety comparisons of every isolated
configuration.

**The producer versions are in the document.** A gate result whose producer is
unknown is not evidence, and a consumer must not have to make a second call to
find out what produced the first.

**Nothing in it is content.** Behavioral identities, descriptors, counts,
thresholds, verdicts and identifiers — no prompt, completion, tool argument,
tool result or body, exactly as the rest of this chain already guarantees.
Note for consumers that behavioral descriptors are *producer-supplied strings*:
they come from the workload's own telemetry, so a consumer rendering them
anywhere treats them as untrusted input — see
[task 079](079-ci-integration-github-action.md).

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
- `GET /v1/evaluation-runs/{run_id}/behaviors` is likewise **new** surface over
  evidence that already exists, and it is additive in the strongest sense: it
  returns what a self-compare of the same run already returns. Nothing about
  `POST /v1/evaluations/compare` changes, including the fact that it permits a
  same-run comparison — the route makes that workaround unnecessary rather than
  forbidden, and any decision to refuse it belongs to whoever adds that guard.
- `--behavioral-profile` on `trustvian dev` is additive and defaults to today's
  behavior, so no existing invocation changes meaning. `docs/compatibility.md`
  classifies `dev`'s flags, and a new flag with a default equal to the current
  derivation is a minor addition.
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
- **Checks 5 and 6 are marked advisory at `N > 1`** and not at `N = 1`, in both
  the runner's output and the result document — asserted on the marker, because
  [an unmarked passing check reads as evidence it is
  not](#checks-5-and-6-are-advisory-against-a-fresh-scope). The marker does not
  change the verdict: a repetition that genuinely blocks still FAILs the check
  with the marker present, asserted by the pair.
- **The documented default guidance produces set semantics.** A scenario at
  `k = 1, j = 0` classifies a behavior as repeatedly added exactly when task 054
  would classify it as added at every `N`, not only at `N = 1` — the property that
  makes [the guidance](#no-default-k-ships-and-the-guidance-is-set-semantics)
  safe for a workload whose variance nobody has measured.
- **No `k` or `j` default exists.** A scenario omitting either is a usage error
  naming the field, asserted for both — the test that fails if anyone writes the
  guidance value in as a default.
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

**The [run-scoped behavior route](#first-slice-a-run-scoped-behavior-route).**
Shippable and testable before the rest of this task:

- **A run's behavior set matches what a self-compare of the same run returns** —
  identity for identity and count for count. The test that makes the route a
  replacement for the workaround rather than a second answer, and the one that
  fails if they ever disagree.
- **Paging follows task 065's rules**: `id` byte-ascending, `after` exclusive,
  `limit` outside `[1, 64]` a usage error, `next_after` present exactly when
  another row follows.
- **A missing run is `404`; a run with no evidence is `200` with an empty page** —
  both halves, because collapsing the second into the first would make "nothing
  happened" indistinguishable from "no such run", which is the distinction
  [ADR 0029 § 2](../../adr/0029-hard-gates-use-explicit-integer-evidence.md)
  insists on one layer down.
- **The route publishes no content**: identities, descriptors and counts only, no
  prompt, completion, argument, result or body.
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
- **The result document carries every field
  [it is required to](#result-document)**, including `N`, `k`, `j`, all six
  checks with their outcomes, and both producer versions — asserted field by
  field, because two other tasks consume it.
- **Every observed behavior appears in the document**, not only the classified
  ones, with both counts.
- **A repeatedly removed behavior is classified by the control plane** and
  labelled as such in the document: present in `>= k` reference runs and
  `<= j` candidate runs. And it contributes to **no** check — asserted by
  showing that adding one does not change the verdict.

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

The first sweep is already recorded above rather than waiting for that PR, because
it changed this specification and a specification revised by evidence should carry
the evidence. The implementation PR adds whatever the re-run under
[one of the two conditions](#the-section-stays-with-two-re-run-conditions)
produces.

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
- **Why the measurement's zero rate did not delete this task**, and what
  distinguishes "the machinery is unjustified" from "the measurement could not
  test it". The reasoning is
  [above](#so-this-measurement-does-not-qualify-as-the-one-this-section-needs) and
  belongs in the ADR because a future reader finding a zero false-FAIL rate in the
  results will otherwise reasonably ask why the k-of-N design survived it.
- **Why checks 5 and 6 are advisory rather than warmed or dropped**, with the
  measured confidence floor that forced the choice.

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
8. Results are machine-readable and usable in CI without parsing human output,
   and the [result document](#result-document) carries every field listed there
   — `N`, `k` and `j`, every behavior with both counts and its classification,
   all six checks with their outcomes, the verdict, and both producer versions.
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

    **Partially satisfied, and recorded as such.** The sweep has been performed
    and is cited above; it returned a zero false-FAIL rate and therefore did not
    produce a `k`. The criterion is met for "performed and recorded" and is
    explicitly *not* met for "guidance that a measurement justifies" — the
    guidance is `k = 1, j = 0`, which is set semantics and asserts no threshold.
    It closes when a sweep against one of the
    [two re-run conditions](#the-section-stays-with-two-re-run-conditions) exists.
18. **Repeatedly removed behaviors are classified by the control plane** under
    the mirrored rule, reported in the result document, and gate nothing. No
    consumer derives the classification for itself.
19. **`k` and `j` have no defaults**, and the documented guidance of
    `k = 1, j = 0` produces task 054's set semantics at every `N` — not only at
    `N = 1`.
20. **Checks 5 and 6 are marked `advisory: fresh scope` at `N > 1`**, in the
    runner's output and in the result document, and the marker changes no verdict.
    A scenario setting them to `0` and seeing them pass can be told apart from one
    where the question could be answered.
21. **`GET /v1/evaluation-runs/{run_id}/behaviors` returns a run's behavior set**,
    paged as task 065's collections are, `404` for a missing run and `200` with an
    empty page for a run with no evidence — and returns the same identities and
    counts a self-compare of that run does.
22. **`trustvian dev` accepts an additive `--behavioral-profile <ref>`** that
    defaults to the candidate, so a runner can isolate repetitions without
    changing candidate identity.

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
6. ~~**Whether the measured repetitions should be preceded by a shared warm-up
   profile.**~~ **Resolved by the measurement: no.** Isolation is kept and checks
   5 and 6 are marked advisory at `N > 1` instead — see
   [Checks 5 and 6](#checks-5-and-6-are-advisory-against-a-fresh-scope) for the
   numbers and the two rejected alternatives.
7. **What `N` and `k` the documentation should recommend.** Still open for `k`,
   and now open for a different reason: the measurement was performed and
   produced no threshold, because the workload could not vary. The documented
   guidance is `k = 1, j = 0` — set semantics, asserting nothing — until a sweep
   against one of the
   [two re-run conditions](#the-section-stays-with-two-re-run-conditions) exists.
   `N` is a cost-versus-evidence choice rather than a threshold, and the
   measurement supports stating `10` as a usable figure: forty runs at `N = 10`
   completed in roughly two hours of model time on a laptop.

## Amendment — task 082

[Task 082](082-agent-inspection-and-evaluation-depth.md) planned inspection and
evaluation depth around this specification. **None of this task's non-goals
change.** Three relationships are recorded, and one new dependency.

### 083 is a new dependency, and it is ahead of this task

This task's central mechanism is a k-of-N threshold over *how many runs showed a
behavior*. A measurement against the reference workload found that one behavioral
change produces **two** added behaviors, because an instrumented tool call and the
HTTP request beneath it are two behavioral identities and no rule says which one
counts:

```text
BEHAVIORAL DIFF: added 2
    + POST  → export.localhost
    + export_customer →
```

At `max_added_behaviors: 0` that is invisible. The moment a team sets a nonzero
budget they are allowing half of a change and cannot know it. And because this
task counts behaviors, re-running
[its own threshold measurement](#the-section-stays-with-two-re-run-conditions)
before the counting rule exists would measure the double count and then write the
resulting `k` down as guidance.

So **083 joins 075 as a prerequisite for the re-run**, not for the runner. The two
exempt slices — the run-scoped behavior route and the additive
`--behavioral-profile` flag — are unaffected and can still land first.

### 086 owns scenario and input versioning, and this task's non-goals stand

The **Non-goals** section above refuses a dataset platform: "no dataset entity, no
versioning, no splits, no labelling, no curation and no dataset API", and refuses
a prompt registry. [086](082-agent-inspection-and-evaluation-depth.md#086--scenario-and-input-versioning)
**does not reverse any of that.** It records a *digest* of the scenario definition
and of its declared inputs on the evidence a scenario execution produces, plus a
prompt *reference* on `CandidateMetadata`, so that a comparison can state whether
both sides ran the same thing.

The distinction is the whole of it: versioning stays git's, and Trustvian records
which version was used rather than becoming a place to keep versions. No dataset
entity, no dataset API, no golden outputs, no prompt text.

### 087 owns performance and cost, and this task's non-goal is unchanged

"No cost or token accounting" above remains a non-goal **of this task**: this
runner accounts for neither, and adding either to it would put evidence
computation in a runner that
[computes nothing](078-design-notes.md#it-computes-nothing-and-that-is-enforced-three-ways).
[087](082-agent-inspection-and-evaluation-depth.md#087--performance-and-cost-evidence)
is a separate item on the control-plane side, and it depends on 084 rather than on
anything here.

### The minimum-evidence rule generalizes, and 082 says so

This task establishes that
[minimum-evidence gates cannot be configured away](078-design-notes.md#minimum-evidence-gates-cannot-be-configured-away),
because fewer observations push every maximum-count gate toward PASS.
[082](082-agent-inspection-and-evaluation-depth.md#storage-scale-and-completeness)
adopts that rule for every new comparison and summary it plans, and adds two
consequences this task should also hold to when it is implemented: a deployment
that samples its trace pipeline records the ratio or declares it unknown, and an
incomplete run stays a distinct state from a clean run with a small behavioral
surface.
