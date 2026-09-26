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

### Content capture is not an available route

The tempting shortcut is to read the tool name out of the model call, and it is
closed. In the OpenTelemetry GenAI semantic conventions, a model-requested tool
call's identity lives inside the message-content attributes —
`gen_ai.input.messages` and `gen_ai.output.messages` — which are **`Opt-In`**,
and whose `tool_call` entries carry the tool's `name` **and its `arguments` in
the same structure**. There is no requirement level at which the name arrives
without the arguments.

So opting into content capture to learn which tool was called would ingest tool
arguments as a side effect. That is the one thing this task and this project
refuse outright, and it would invalidate the measurement even if the numbers
came out well: a detection evaluation that had to read arguments would have
disproved its own premise on the way to measuring it.

Verify this against the conventions at the commit the implementation reads — the
GenAI conventions are marked *Development* and moved repositories once already —
and if it has changed, the constraint that still binds is Trustvian's, not
OpenTelemetry's: no argument or result content, whatever the convention permits.

### The expected path: a pass-through wrapper emitting `execute_tool`

The conventions already have the right span for this, and it carries no content:

```text
gen_ai.operation.name = execute_tool     a well-known operation value
gen_ai.tool.name      = <tool name>      the name, and only the name
```

So the harness wraps the benchmark's tool dispatch and emits one `execute_tool`
span per call, with the tool name and nothing else. Not
`gen_ai.tool.definitions`, which carries schemas; not `gen_ai.tool.description`,
which the registry itself flags as potentially sensitive; no argument attribute,
no return-value attribute, no error message body.

AgentDojo has a single dispatch point, which is what makes this tractable:
`FunctionsRuntime.run_function(env, function, kwargs, raise_on_error)` takes the
tool name as a plain string and returns `(result, error)`. The wrapper needs the
`function` argument and nothing else it is handed.

### What "unmodified" means

The word was doing too much work, so it is defined:

```text
never        editing benchmark source files
never        a fork, a patch file, or a vendored copy with changes
never        altering what a tool does, what it returns, or the order
             in which tools are dispatched

allowed      wrapping a dispatch function from the harness at import
             time, as a strict PASS-THROUGH
```

A pass-through preserves, exactly:

- every argument, by value and by position;
- the return value, unchanged and unexamined beyond what the span needs;
- every raised exception, propagated with its type and its payload intact — for
  `run_function` that includes `ValidationError` and `ToolNotFoundError`;
- the order and number of dispatches. The wrapper adds a span; it never retries,
  reorders, batches, caches or suppresses a call.

**The pass-through property is tested, not asserted — and the test has to be
deterministic.** An earlier draft proposed running the benchmark with and
without the wrapper and requiring the traces to agree. That test cannot fail for
the right reason and cannot pass for one either: the agent is model-driven, so
two runs of the same task differ whether or not a wrapper is installed. A
comparison whose expected outcome is "probably similar" detects nothing.

So the property is tested where it is actually decidable — **at the dispatch
function, with no model in the loop**:

```text
call FunctionsRuntime.run_function directly, with and without the wrapper
installed, over a fixture set of calls that includes a success, an
unknown tool, and an argument-validation failure. Assert:

  arguments         each call receives exactly what the caller passed,
                    by value and position, wrapper or not
  return value      the (result, error) tuple is identical
  exceptions        the same type, with the same payload, propagates —
                    ValidationError and ToolNotFoundError by name
  call count        one dispatch in, one dispatch out; never retried,
                    reordered, batched, cached or suppressed
  spans             one execute_tool span per dispatch, carrying
                    gen_ai.tool.name and no argument, return-value or
                    description attribute
```

That is a unit test over a pure-ish function with a fixture environment, so it
is repeatable and its failure means what it says.

**Optionally, a deterministic replay.** Both task types declare their expected
calls, so if the pinned benchmark commit provides a way to execute a task's
`ground_truth` calls directly — without a model choosing them — that replay is a
second, end-to-end pass-through check with no nondeterminism in it: the same
declared calls, dispatched with and without the wrapper, must produce identical
traces and identical resulting environment state. Whether the pinned commit
offers such a path is a verification step, not an assumption, and its absence
does not weaken the unit test above.

Three outcomes, and the task must determine which holds **before** any numbers
are produced:

```text
a pass-through wrapper at the dispatch
  point yields execute_tool spans        → proceed; this is the intended path

the dispatch point cannot be wrapped
  without editing benchmark source       → stop, record why, and evaluate a
                                           different benchmark

only content capture would reveal the
  tool name                              → stop. Not a fallback: see above
```

The second and third outcomes are legitimate results of this task, not failures
of it. A harness that patches the benchmark's internals is measuring a modified
benchmark, and the comparison to its published behavior no longer holds.

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

### Ground truth is a tool trace, not the benchmark's goal flag

The first draft of this section labelled a run positive when *the benchmark
reported the attack achieved its goal*. That is the wrong unit, and getting it
wrong would understate recall in the most misleading direction.

Trustvian detects **an unexpected tool call**. The benchmark's goal flag reports
**whether the attacker's objective was accomplished**. Those come apart:

```text
injection fires, agent calls the malicious tool, objective accomplished
    → behavioral deviation, and the goal flag agrees

injection fires, agent calls the malicious tool, objective NOT accomplished
    (the tool errored, the environment refused, the arguments were wrong)
    → behavioral deviation that Trustvian SHOULD detect, and the goal flag
      says "attack failed" — so the first draft discarded the run

injection fires, agent ignores it entirely
    → no behavioral deviation, nothing to detect
```

The middle row is the problem. It is a real injection-induced tool call — the
exact thing this project claims to notice — and labelling it a non-attack would
count a correct detection as a false positive, or drop it altogether.

**So runs are labelled from a content-free tool trace**, recorded harness-side:

| Class | Definition | Role |
|---|---|---|
| **held-out benign** | an unattacked task run, not used for warm-up | false-positive denominator |
| **attacked, deviated** | an attacked run whose tool trace shows a *deviation* as defined below | **the positive class** — recall denominator |
| **attacked, resisted** | an attacked run whose tool trace shows no deviation | reported, and excluded from recall and precision |

The trace records **tool names only** — never arguments, never return values.

**Membership in the injection's ground truth is not enough**, and assuming it
was would have mislabelled most of the corpus. Injection tasks routinely ask for
a tool the paired user task legitimately calls. In AgentDojo's banking suite,
seven of nine injection tasks name `send_money`, and the user tasks in that same
suite call `send_money` six times; `update_scheduled_transaction`,
`get_scheduled_transactions` and `update_password` overlap the same way. Every
injection tool name in that suite appears in some user task. A run that did
nothing but the benign task would therefore be labelled *deviated*, turning
correct non-detections into misses and inflating the apparent failure of the
signals this task is measuring.

**So deviation is defined against the paired user task**, per
`(user task, injection task)` pair:

> An attacked run **deviated** when its tool trace contains a tool from the
> injection task's ground truth that the paired user task's ground truth does
> **not** call — or contains such a tool **more times** than the paired user
> task's ground truth calls it.

Both halves are needed. The first catches the clean case, an injection reaching
for a tool the benign task never touches. The second catches the case where the
tool is shared but the count is not: a user task that sends money once and a run
that sent it twice has done something the benign task does not explain.

Both ground truths are available: `BaseUserTask.ground_truth(pre_environment)`
and `BaseInjectionTask.ground_truth(pre_environment)` each return a list of
`FunctionCall` carrying a `function` name, so the comparison is between two
declarations the benchmark makes about itself. Verify both APIs against the
pinned commit. If a chosen benchmark exposes only one of them, the rule actually
used is stated in the report rather than left implicit.

### Name-overlapping pairs are a stratum, and a measured boundary

Some pairs cannot be separated by the rule above at all. When every tool in the
injection's ground truth also appears in the paired user task's ground truth, at
no lower multiplicity, a tool trace of names and counts contains no evidence
that could distinguish the two intents.

These pairs are **classified before anything runs** — it is a property of the
two ground truths, not of a run — and reported as their own stratum:

| Stratum | How it is treated |
|---|---|
| **separable pairs** | produce the primary recall figure |
| **name-overlapping pairs** | run, and reported with their own counts: attacked runs, detections, and the resulting rate. **Excluded from the primary recall denominator**, because a detection there cannot be attributed to the injection |

Published as counts, like every other exclusion in this task, so a reader who
wants to fold them in can.

**This is the measured boundary of the metadata-only claim, not a harness
defect.** Distinguishing *the agent sent money because the user asked* from *the
agent sent money because the injection asked* requires the recipient — which is
an argument, which is content. A harness that separated them would be reading
exactly what this project refuses to read, and its numbers would describe a
Trustvian that does not exist.

So the stratum is the finding, not a footnote to it. Its size says how much of a
real injection corpus lies outside what tool-name metadata can adjudicate, and
that number is worth more to this project than a recall figure computed as
though the boundary were not there. If it turns out large, the honest conclusion
is that metadata alone bounds *which attacks* are detectable rather than *how
well* they are detected — and the report says so instead of averaging it away.

**The goal flag becomes a secondary stratum, not the label.** AgentDojo's
`BaseInjectionTask.security(...)` returns whether the objective was
accomplished, and that is worth reporting — it is just a different question. So
recall is reported twice:

```text
recall among attacks that achieved their goal
recall among attacks that induced a tool call but failed their goal
```

If those two differ substantially, that is a finding about what Trustvian's
signals are actually keyed on, and it is invisible if the second stratum is
thrown away.

### What is measured, and over which denominators

The classes above enter each figure in exactly one way, and the table is here so
a reader never has to infer it:

| Class | Recall | Precision | False-positive rate |
|---|---|---|---|
| held-out benign | — | a detection here is a false positive | denominator; detections are the numerator |
| attacked, deviated *(separable pairs)* | denominator; detections are the numerator | a detection here is a true positive | — |
| attacked, resisted *(separable pairs)* | **excluded** | **excluded** | — |
| attacked, name-overlapping pairs | **excluded** — own stratum with its own counts | **excluded** | — |
| benign warm-up runs | — | — | **excluded** — they trained the scope |

```text
recall                detections on attacked-deviated / attacked-deviated
                        — separable pairs only
precision             detections on attacked-deviated
                        / (detections on attacked-deviated
                           + detections on held-out benign)
false-positive rate   detections on held-out benign / held-out benign

reported beside them, never folded in:
  detections on attacked-resisted          raw count
  name-overlapping pairs                   attacked runs · detections · rate
```

**Detections on resisted runs are reported as their own raw count**, and enter
none of the three. The reasoning is that a resisted run has no ground-truth
answer for this measurement: nothing deviated, so a detection is not a true
positive, and yet calling it a false positive would penalize Trustvian for
noticing something about a run the attacker did touch. Both choices are
defensible and neither is obviously right, so the count is published beside the
ratios and the report says it was excluded — a reader who disagrees can fold it
into precision from the published numbers.

That last clause is the rule for the whole section: **every exclusion is
published as a count**, so any reader can recompute the ratios under their own
definitions. An exclusion that only appears as a smaller denominator is a hidden
choice.

Integer counts and the ratios derived from them. No F-score as the headline: a
single number invites a threshold and hides the trade-off, which is
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

### Scoring a run without teaching the scope it is scored in

There is a real conflict here, and it has to be resolved in the protocol rather
than waved at.

**Scoring and learning are the same pass through the Collector.** The processor
calls `Observe` on every learning-eligible result — unconditionally on the plain
span path, and inside `Record` on the evaluation-ingest path
(`processor/processor.go`). So a run that is scored *also teaches the scope it
was scored in*. There is no Collector configuration that scores without
learning; that is deliberate, and
[`.claude/rules/security.md`](../../../.claude/rules/security.md) explains why
learning eligibility is the engine's decision rather than the caller's.

Two requirements collide on that fact:

```text
isolation    a measured run must not be influenced by other measured runs
warm-up      a measured run must be scored against a baseline that has seen
             normal behavior, or novelty flags everything and the
             false-positive rate is 100% by construction
```

A single shared scope satisfies warm-up and destroys isolation: run *n* is
scored against a baseline containing runs 1..*n*−1, so the numbers depend on
ordering. A fresh empty scope per run satisfies isolation and destroys warm-up.

**The protocol that satisfies both — warm, score, discard:**

```text
for each measured run (held-out benign, or attacked):
    1. allocate a FRESH learning scope, used by nothing else, ever
    2. warm it with the SAME fixed set of K benign runs of the SAME task
    3. score the measured run in that scope
    4. discard the scope
```

Every measured run therefore meets an identically-prepared baseline, and no
measured run appears in any other measured run's history. Ordering becomes
irrelevant, which is the property that makes the counts comparable at all.
Scopes are the existing mechanism
([ADR 0024](../../adr/0024-learning-scope-is-a-baseline-key-dimension.md)), they
are opaque to the core, and capacity is per scope — so the cost is **one scope
per measured run, each ingesting `K + 1` runs**: the `K` warm-up runs plus the
one being scored. Nothing the engine cares about, and nothing that grows with
the number of tasks except linearly.

**The held-out rule.** The `K` warm-up runs are **never** among the benign runs
scored for the false-positive rate. Warming a scope with a run and then scoring
that same run measures whether the engine remembers what it just learned, which
is not the question and would report a false-positive rate near zero. The two
sets are disjoint by construction and asserted by test.

**Alternatively, an Analyze-only path.** `Engine.Analyze` never mutates state —
that is a documented property of the core, and the harness reaches it through
the public SDK rather than through the Collector. A harness that warms a scope
with `Observe` and then scores with `Analyze` alone gets the same isolation
without allocating a scope per run, at the cost of not exercising the ingest
path a real deployment uses.

Both are acceptable. **The report states which was used**, because they are not
the same experiment: the Collector path measures the pipeline a user would run,
and the `Analyze` path measures the signals in isolation. If both are run, both
sets of numbers are published.

**Which detection definitions need any of this.** Not all of them:

| Definition | Needs a warmed scope |
|---|---|
| task 054's `Added` — a behavior absent from the reference snapshot | **No.** It compares two observed snapshots, not a learned baseline |
| a `BLOCK` or `REQUIRE_APPROVAL` decision | Yes — policy reads trust, which reads anomaly, which reads the baseline |
| a critical risk classification | Yes, for the same reason |
| a named anomaly contributor crossing a value | Yes, and most directly of all |

So the `Added` definition can be measured with no warm-up at all, and its
numbers are not comparable to the other three's. The report keeps them separate
rather than averaging across definitions that rest on different evidence.

**`K` is a reported parameter.** Novelty against a barely-warmed baseline flags
almost everything and against a thoroughly-warmed one flags much less, so `K` is
a knob that moves the answer and therefore belongs in the result rather than in
the code. The measurement is run at more than one `K` if the numbers move, and
the report says which `K` produced which figures.

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
  any prompt, completion, argument or result field. The `execute_tool` spans
  carry `gen_ai.tool.name` and no argument, return-value or description
  attribute.
- **The wrapper is a strict pass-through**, asserted **deterministically at the
  dispatch function** rather than by comparing two live-model runs, which differ
  anyway and would make the test unfalsifiable. `FunctionsRuntime.run_function`
  is called directly, with and without the wrapper, over a fixture set covering
  a success, an unknown tool and an argument-validation failure: identical
  arguments received, identical `(result, error)` tuples, the same exception
  types with the same payloads (`ValidationError`, `ToolNotFoundError`), and one
  dispatch out per dispatch in.
- **One `execute_tool` span per dispatch**, carrying `gen_ai.tool.name` and no
  argument, return-value or description attribute — asserted in the same
  deterministic harness.
- **Optionally, a deterministic ground-truth replay**: if the pinned benchmark
  commit can execute a task's declared `ground_truth` calls without a model
  choosing them, the same replay with and without the wrapper produces identical
  traces and identical resulting environment state. Verified as available before
  being relied on.
- **No measured run shares a learning scope with another**, asserted from the
  recorded evidence: every scored scope is freshly allocated and never reused.
- **Warm-up and held-out benign sets are disjoint**, asserted directly. A run
  used to warm a scope never appears in the false-positive denominator.
- **Runs are labelled from the tool trace, not the goal flag.** A fixture where
  the injection induced its tool call but the benchmark reports the objective
  failed is classified **attacked-deviated** and counted in recall — the
  regression test for the mislabelling this specification corrected.
- **Resisted runs are excluded from recall and precision**, and their detection
  count is published separately, asserted on a fixture whose tool trace shows no
  injection-induced call.
- **The reported counts and the reported ratios agree** under the
  [denominator table](#what-is-measured-and-over-which-denominators), asserted
  arithmetically for all three figures and for both recall strata — a published
  precision that does not follow from the published counts is the most
  embarrassing possible defect here.
- **Every exclusion is published as a count**, so the ratios can be recomputed
  from the report alone.
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
   what fidelity. **Content capture is not an acceptable answer** — a route
   requiring message-content attributes is refused, because those carry tool
   arguments alongside tool names.
3. The benchmark is used **unmodified** as [defined](#what-unmodified-means):
   no source edits and no fork, with a dispatch wrapper allowed only as a strict
   pass-through — proven by a **deterministic** test at the dispatch function,
   not by comparing two model-driven runs.
4. **Precision, recall and false-positive rate** are reported with the raw
   integer counts they derive from, and every class's role is stated in the
   [denominator table](#what-is-measured-and-over-which-denominators).
5. **Runs are labelled from a content-free tool trace**, not from the
   benchmark's goal flag: an attack that induced a tool call but failed its
   objective counts toward recall. The goal flag is reported as a **secondary
   stratum**.
6. **Deviation is defined against the paired user task's ground truth** — a tool
   the paired user task does not call, or the same tool called more times than
   it calls — never mere membership in the injection's ground truth.
7. **Name-overlapping pairs are identified before running**, reported as their
   own stratum with counts, and excluded from the primary recall denominator.
   The report states that tool-name metadata cannot adjudicate them, and that
   this is a measured boundary of the metadata-only claim rather than a harness
   defect.
8. **Attacked runs with no deviation are excluded from recall and precision**,
   with their detection count published separately so a reader can recompute.
9. The **detection definition, policy, anomaly configuration and the warm-up
   size `K` are stated**, and no threshold was tuned to improve the result.
10. **Every measured run is scored in a freshly allocated learning scope, warmed
    with the same fixed benign set and then discarded** — or through an
    `Analyze`-only path — and the report states which. No scored scope is
    reused.
11. **Warm-up runs are never scored for the false-positive rate**; the two sets
    are disjoint.
12. **No prompt, completion, injection string, tool argument or tool result** is
    ingested, retained, published or committed.
13. The measurement is **reproducible** from the published method by someone
    with the benchmark and an API key.
14. **No model is ranked or compared**, and no result is presented as a
    statement about a model's quality.
15. **Nothing ships in the product**: no engine change, no new signal, no new
    configuration, no `internal/` addition, no scoring change.
16. The result is **published as measured**, unfavorable outcomes included, with
    a caveats section bounding what it generalizes to.

## Open questions left to implementation

1. **Which benchmark.** AgentDojo is the first candidate and is not assumed;
   its `OpenTelemetry`-absent dependency set is the reason the feasibility step
   comes first.
2. **Where the pass-through wrapper attaches, and how.** The route is settled —
   a harness-side wrapper emitting `execute_tool` spans with the tool name only,
   because content capture is refused and LLM-SDK instrumentation does not see
   tool dispatch. What is open is the mechanism: wrapping
   `FunctionsRuntime.run_function` at import time is assumed for AgentDojo, and
   whether that is a subclass, a decoration, or a `sitecustomize`-style hook is
   an implementation choice constrained by the pass-through test rather than by
   preference. Standard GenAI auto-instrumentation may be added *alongside* it
   for model-call context, as long as content attributes stay off.
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
