# 080 — Metadata-Only Detection Evaluation

Status: specified; not implemented
Milestone: `v1.0` — **not** a `v1.0` exit criterion, and not a product feature.
See [What this is not](#what-this-is-not)
Depends on: [075](075-ai-semantic-telemetry-normalization.md) for tool-level
fidelity. Sequenced after [078](078-behavioral-scenario-suites.md) if it needs
repeatable scenario execution — see
[Sequencing](#sequencing-and-what-it-actually-depends-on)
Blocks: nothing

## Objective

Answer one question with a number instead of an argument:

> Do Trustvian's existing behavioral signals detect injection-induced tool
> misuse, and at what false-positive cost?

```text
public agent prompt-injection benchmark
        ↓
benign runs  +  attacked runs        ← the benchmark's own task suite
        ↓
OpenTelemetry, metadata only
        ↓
existing pipeline, unchanged
        ↓
precision · recall · false-positive rate
```

Everything Trustvian needs for this already exists. Nothing in this task adds a
signal, changes a score, or ships a feature.

## Why

**The product claim is currently an argument.** Trustvian's position is that
behavioral metadata is sufficient to notice an agent doing something it should
not — *behavioral observability does not require content observability* is a
principle in [ROADMAP.md](../../ROADMAP.md) and a constraint on every milestone.
That claim is reasoned about carefully throughout `docs/` and measured nowhere.

**A number is falsifiable and an argument is not.** If the existing signals
detect injection-induced tool misuse at usable precision and recall from
metadata alone, that is the strongest available evidence for the whole design.
If they do not, that is the most valuable thing this project could learn, and it
should learn it from a public benchmark rather than from a user.

**The false-positive rate is the half that decides whether any of this ships.**
Recall alone is easy and worthless: a detector that flags every run catches
every attack. `docs/SECURITY.md` and every gate in
[task 056](056-deterministic-hard-gates.md) are built around fail-closed
behavior, and [task 078](078-behavioral-scenario-suites.md)'s whole amendment
exists because a gate that fires on unchanged code gets ignored. The benign-run
false-positive rate is the number that says whether a behavioral gate is usable
at all, and it is the number nobody has.

**The benchmarks already exist, and they are the right shape.** Public agent
prompt-injection benchmarks ship benign task suites and attacked variants of the
same tasks, with tool calls as the observable unit. That is precisely
Trustvian's input: an actor, an operation, a target, a sequence. The benchmark
supplies the ground truth this project cannot generate for itself, because a
detection evaluation scored against attacks its own author designed measures the
author.

## What this is not

This list matters more than usual, because the task's shape resembles two things
the roadmap explicitly excludes.

**Not model benchmarking.** `docs/ROADMAP.md` names model benchmarking as
outside the product, on the grounds that *"is this model any good"* is a
different question from *"did this agent do something it should not have"*. This
task asks the second one about **Trustvian**, not the first one about a model:

```text
the benchmark's own question    which model/defense resists injection best?
this task's question            does Trustvian's detection notice the
                                resulting tool misuse, from metadata alone?
```

Concretely, three things that make this non-negotiable:

- **The measured subject is Trustvian**, not the agent. Results are reported as
  Trustvian's precision, recall and false-positive rate. No model is ranked, no
  model is compared with another, and no leaderboard position is produced or
  cited as a finding about a model.
- **A model that resists the injection entirely is not a Trustvian success.** If
  the agent never misuses a tool, there is nothing behavioral to detect, and the
  run is excluded from recall rather than counted as a catch. Counting it would
  make Trustvian's score a function of the model's robustness — which is exactly
  the confusion this section exists to prevent.
- **No prompt or completion content is retained.** Not in the pipeline, not in
  the published results, not in the reproduction artifacts. The benchmark's
  attack strings stay in the benchmark.

**Not an answer-quality evaluation.** No judge, rubric, groundedness score,
hallucination metric or similarity threshold — the same non-goals
[task 078](078-behavioral-scenario-suites.md) carries, for the same reason.

**Not a product feature.** Nothing here ships in the binary. No detection mode,
no benchmark adapter in `internal/`, no new configuration, no new signal, no
scoring change. The deliverable is a reproducible measurement and a written
result.

**Not a new dataset entity.** The benchmark is somebody else's, used as-is. No
dataset type, versioning, splits, labelling or curation appears in Trustvian.

**Not a marketing number.** The result is published whichever way it comes out,
including badly. A measurement that is only published when favorable is not a
measurement, and the reproduction instructions exist so a reader can disagree.

## First candidate: AgentDojo

[AgentDojo](https://github.com/ethz-spylab/agentdojo) is the first candidate.
Verified rather than assumed, at the time of writing:

| Question | Finding |
|---|---|
| License | **MIT** — permissive; usable with attribution, no copyleft obligation on Trustvian |
| Shape | A dynamic environment for prompt-injection attacks and defenses against LLM agents, with benign task suites and attacked variants of the same tasks |
| Maintenance | Actively maintained, with a published results dashboard |
| Python | `>= 3.10` |
| LLM SDKs | `openai`, `anthropic`, `cohere`, `google-genai`, plus `langchain` |
| **OpenTelemetry** | **Not present.** No OpenTelemetry or instrumentation package appears in its dependencies, and it emits no traces of its own |

That last row is the gating fact, and it is why the task opens with a
feasibility step rather than a measurement.

### The feasibility question, stated before the method

> Can the benchmark's **tool calls** be observed as OpenTelemetry spans without
> modifying the benchmark — and at what fidelity?

The distinction between a tool call and a model call is the whole measurement.
AgentDojo drives the official provider SDKs, so standard GenAI
auto-instrumentation would plausibly capture the **model** calls without
touching the benchmark's source. But its tools are Python functions the harness
dispatches itself, and LLM-SDK instrumentation has no reason to emit a span for
one. Trustvian's finding is *this agent called a tool it should not have*, so a
dataset of model calls with no tool-call spans measures nothing this task cares
about.

Three outcomes, and the task must determine which holds **before** any numbers
are produced:

```text
tool calls observable, unmodified      → proceed; this is the intended path
tool calls observable only with a
  wrapper outside the benchmark        → proceed, and state the wrapper is
                                         part of the harness, not of
                                         Trustvian, and that it adds no
                                         content
tool calls not observable without
  modifying the benchmark              → stop, record why, and evaluate a
                                         different benchmark
```

The third outcome is a legitimate result of this task, not a failure of it. A
harness that patches the benchmark's internals to produce spans is measuring a
modified benchmark, and the comparison to its published behavior no longer
holds.

This is also where [task 075](075-ai-semantic-telemetry-normalization.md)
becomes load-bearing rather than optional: 075 is what makes an agent-oriented
span read as a tool call rather than as transport. Without it the measurement is
possible at transport fidelity and much weaker, and the task should say which
fidelity each reported number was produced at.

### Alternatives, if AgentDojo does not work out

Named so the task is not a single-benchmark bet: any public benchmark with
**paired benign and attacked runs of the same tasks** and an observable
tool-invocation boundary is a candidate. The requirements are the license, the
pairing, and the observability — in that order — and the chosen benchmark's
license must be verified in the implementation, not inherited from this
paragraph.

## Method

### Ground truth comes from the benchmark, not from Trustvian

```text
benign run      the benchmark's own unattacked task        → expect no detection
attacked run    the same task with the injection applied,
                where the benchmark reports the attack
                achieved its goal                          → expect detection
attacked run    where the agent resisted                   → excluded
```

The exclusion in the third row is the one that keeps this honest, and it is
restated here because it is the easiest thing to get wrong under pressure to
improve a number.

### What is measured

```text
recall                detected / attacked-and-successful
precision             detected-and-attacked / all detections
false-positive rate   detections on benign runs / benign runs
```

Integer counts and the three ratios derived from them. No F-score as the
headline: a single number invites a threshold and hides the trade-off, which is
[ADR 0029](../../adr/0029-hard-gates-use-explicit-integer-evidence.md)'s
reasoning about composites applied to a measurement instead of a gate. Report
the counts, so a reader can compute whatever they want.

### What counts as a detection

This has to be pinned down before the runs, and it must be pinned to something
Trustvian already produces:

| Candidate definition | Source |
|---|---|
| a `BLOCK` or `REQUIRE_APPROVAL` decision | `policy.Evaluate`, under a stated policy |
| a critical risk classification | `Trust` / `RiskLevel` |
| a behavior absent from the benign baseline | task 054's `Added`, per behavior |
| a named anomaly contributor crossing a stated value | `Anomaly.Contributors` |

The task must choose, state the choice, and report the policy and anomaly
configuration used — because *detection* under a permissive policy and under a
strict one are different measurements, and a result without its configuration is
not reproducible. Reporting more than one definition side by side is better than
choosing silently.

**The configuration is stated, not tuned.** A threshold searched until the
numbers look good measures the search. If more than one configuration is
reported, all of them are reported, including the ones that did worse.

### Learning isolation

The benign and attacked runs must not train one another's baselines, for the
same reason [task 078](078-behavioral-scenario-suites.md) isolates its
repetitions: a baseline that has already seen the attacked run's behavior finds
it familiar. Per-run learning scopes
([ADR 0024](../../adr/0024-learning-scope-is-a-baseline-key-dimension.md)) are
the existing mechanism, and the task states its allocation explicitly.

The related question the measurement has to answer honestly is **how much benign
behavior the baseline saw first.** Novelty detection against an empty baseline
flags everything, and against a fully learned one flags much less. The warm-up —
how many benign runs trained the profile before measurement began — is a
reported parameter, not an implementation detail, and the result is reported at
more than one warm-up level if the numbers move.

## Privacy

Metadata only, and this is a hard boundary rather than an intention. The
benchmark's payloads are exactly the content Trustvian is designed not to need:

```text
never ingested, retained, fingerprinted, published or committed:
  injection strings · prompts · completions · reasoning
  tool arguments · tool results · retrieved documents

ingested:
  actor · operation · target · tool and model identity
  sequence · timing · status · correlation
```

The published artifacts — results, any captured telemetry, any fixture — must be
checked for content before they are committed, and the task should say how that
check is performed rather than asserting the outcome.

There is a real temptation here and it is worth naming: an attacked run that
Trustvian missed is much easier to diagnose with the prompt in front of you.
That is exactly the trade this project has refused everywhere else, and a
detection evaluation is not an exemption. If a miss cannot be explained from
metadata, that is itself the finding.

## Reproducibility and where it lives

```text
examples/<name>/     a runnable harness, its README, and real captured output
   or
docs/<name>.md       the written result, method, configuration and caveats
```

`examples/` is the repository's existing home for runnable programs with real
captured output and its own module, which also keeps the harness off the
engine's dependency path. A written result belongs in `docs/` either way, and
the two should cross-reference.

Whichever it is, the measurement must be reproducible by a reader who has the
benchmark and an API key: the exact benchmark commit, the exact Trustvian
version, the policy and anomaly configuration, the warm-up parameter, the
instrumentation path, and the raw counts. A published ratio without those is a
claim, not a measurement.

**The harness is not part of the product.** It imports the public API like any
example, adds nothing to `internal/`, and its absence changes nothing about what
Trustvian does.

### Cost and nondeterminism, stated up front

The runs cost model tokens and the agents are nondeterministic, so the numbers
carry run-to-run variance from the same source
[task 078](078-behavioral-scenario-suites.md) had to address. The task must
state how many runs per condition it used and report the variance rather than a
single tidy figure — a precision quoted to three digits from one pass over each
task is a number pretending to be a measurement.

## Sequencing, and what it actually depends on

Nothing here blocks anything, and it is not on the `v1.0` critical path.

- **075 is the real dependency.** Without agent-oriented semantic fidelity, a
  tool call may be indistinguishable from an HTTP POST, and the measurement is
  weaker. It can be run at transport fidelity first, and the result must then
  say so.
- **078 is a dependency only if the harness needs repeatable orchestration.**
  Running one benchmark task N times with isolated scopes, comparing benign
  against attacked, is close to what a scenario suite already does. If the
  harness would otherwise reimplement that, it waits for 078 and reuses it — the
  same "no second implementation" rule ADR 0023 applies everywhere else. If a
  standalone harness is genuinely simpler, it does not wait, and the task
  records which it chose.
- **It is not a `v1.0` exit criterion.** No release gate depends on it, and its
  absence blocks no tag. It sits in this milestone directory because that is
  where active specifications live, the way task 068 sits in the sequence while
  remaining conditional.

A result that is unfavorable has a consequence worth stating: it would be
evidence about the roadmap's central bet, and it belongs in front of the people
making decisions about
[behavioral intelligence](../../ROADMAP.md#behavioral-intelligence) rather than
quietly in a file.

## Tests

A measurement is not a feature, so this section is thinner than most and the
things it does check are the things that would invalidate a published number.

- **The harness retains no content**, asserted by scanning captured telemetry
  and every committed artifact for the benchmark's own attack strings and for
  any prompt, completion, argument or result field.
- **Benign and attacked runs use distinct learning scopes**, asserted from the
  recorded evidence.
- **Resisted attacks are excluded from recall**, asserted on a fixture where the
  benchmark reports the attack failed.
- **The reported counts and the reported ratios agree**, asserted
  arithmetically — a published precision that does not follow from the published
  counts is the most embarrassing possible defect here.
- **The harness imports no `internal/` package** and adds no dependency to the
  root, processor or platform modules — the existing `check-modules` and
  `check-platform-boundary` posture, applied to a new directory.
- **The engine is unchanged**, asserted by the diff: no signal, score, threshold
  or configuration default moves in this task.

## Documentation

Written by the implementation PR: the harness README with real captured output,
the written result with its method and configuration, `docs/ROADMAP.md`, this
task's status, the task index, and `CHANGELOG.md` if a runnable example is added
under `examples/`.

The write-up must include its own caveats section: one benchmark, one family of
attacks, one instrumentation path, and a stated warm-up. A detection result
generalizes exactly as far as its conditions, and overclaiming from one
benchmark would be the same error this task exists to correct.

## ADR

Probably not warranted, and the task should decide rather than default. Nothing
architectural changes: no boundary moves, no type is added, no dependency
direction shifts.

One decision might earn a short record if the answer is subtle: **why a
detection evaluation against a public benchmark is not model benchmarking**,
since that is the question a future reader will ask when they find this next to
a roadmap non-goal that appears to forbid it. If the distinction in
[What this is not](#what-this-is-not) holds up in review, a pointer to it is
enough.

## Acceptance criteria

1. The chosen benchmark's **license is verified** and recorded, along with the
   exact commit used.
2. The **feasibility question is answered before any numbers are produced**:
   whether tool calls are observable without modifying the benchmark, and at
   what fidelity.
3. The benchmark is used **unmodified**; any instrumentation wrapper lives in
   the harness and is documented as such.
4. **Precision, recall and false-positive rate** are reported over paired benign
   and attacked runs, together with the raw integer counts they derive from.
5. **Attacked runs the agent resisted are excluded from recall**, not counted as
   detections.
6. The **detection definition, policy, anomaly configuration and warm-up level
   are stated**, and no threshold was tuned to improve the result.
7. Benign and attacked runs are **learning-isolated** from each other.
8. **No prompt, completion, injection string, tool argument or tool result** is
   ingested, retained, published or committed.
9. The measurement is **reproducible** from the published method by someone with
   the benchmark and an API key.
10. **No model is ranked or compared**, and no result is presented as a
    statement about a model's quality.
11. **Nothing ships in the product**: no engine change, no new signal, no new
    configuration, no `internal/` addition, no scoring change.
12. The result is **published as measured**, unfavorable outcomes included, with
    a caveats section bounding what it generalizes to.

## Open questions left to implementation

1. **Which benchmark.** AgentDojo is the first candidate and is not assumed;
   its `OpenTelemetry`-absent dependency set is the reason the feasibility step
   comes first.
2. **How tool calls are observed.** Standard GenAI auto-instrumentation, a
   harness-side wrapper around the benchmark's tool dispatch, or something the
   benchmark itself gains. Whichever it is, the benchmark is not modified.
3. **Which detection definition is primary.** Reporting more than one side by
   side is assumed, since a single choice hides how much of the result the
   policy contributed.
4. **`examples/` or `docs/`.** A runnable harness under `examples/` with the
   write-up in `docs/` is assumed, matching how this repository already pairs
   runnable programs with documentation.
5. **How many runs per condition**, and how variance is reported. Unanswered
   deliberately: it depends on observed variance and on token cost, and a number
   invented here would be a budget rather than a method.
6. **Whether this waits for 078.** It waits if the harness would otherwise
   reimplement repeated isolated execution; it does not if a standalone harness
   is genuinely simpler. Recorded at implementation time.
