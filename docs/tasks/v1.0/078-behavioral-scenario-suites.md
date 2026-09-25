# 078 — Behavioral Scenario Suites

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [054](054-behavioral-diff.md),
[055](055-evaluation-scorecards.md),
[056](056-deterministic-hard-gates.md),
[062](062-integrated-local-developer-workflow.md),
[077](077-unified-otlp-local-dev-runtime.md)
Blocks: [072](README.md) — the OSS `v1.0` release gate

## Objective

Make behavioral testing repeatable: run the same scenario against a reference
and a candidate, diff the behavior, gate the difference.

```text
run the same scenario
        ↓
collect behavioral evidence
        ↓
compare against the reference
        ↓
apply deterministic limits
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

## Scope

- A **scenario**: a declarative description of how to run a workload
  repeatably, and the limits its behavior must satisfy.
- A **runner** that executes a scenario, collects evidence through the
  existing pipeline, and associates the result with a reference.
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

**No new evaluation logic.** The runner computes no diff, no scorecard, no
gate and no policy outcome. It calls the control plane, which owns all four.

**No new exit-code scheme.** See [CI](#ci).

## What a scenario is

Illustrative, and deliberately not final — the syntax must be designed against
the repository's existing config conventions rather than invented here:

```yaml
name: support-login

command:
  - python
  - agent.py

inputs:
  - fixtures/login-ticket.json

gate:
  max_added_behaviors: 0
  max_block_decisions: 0
  max_critical_risk_observations: 0
```

Four things and nothing else: what it is called, how to run it, what to feed
it, and what its behavior must satisfy. The gate block is exactly
`EvaluationGateLimits` — the same three `uint64` maximums task 056 defined,
where zero is a strict limit and not a default, so every one must be stated.

The existing config surface is schema-versioned YAML parsed in one place; a
scenario file should follow that convention rather than introducing a second
configuration mechanism.

## What it produces

```text
Behavioral Diff
  + export_customer

Gate
  FAIL   added behaviors 1 / max 0
```

Reference behavior:

```text
crm_lookup · knowledge_search · send_email
```

Candidate behavior:

```text
crm_lookup · knowledge_search · export_customer · send_email
```

The added behavior is the finding. Whether the agent's answers were good is
not asked and not answered.

**Ordering is not asserted.** A model-driven workload chooses its own order,
and a scenario that failed because the agent did the same things in a
different sequence would be testing the model. What a scenario compares is the
behavioral surface — which behaviors occurred — and what the engine itself
recorded about sequence drift, which is a learned signal rather than a
scripted expectation.

## Architecture

```text
scenario file
    ↓
runner            ← this task: invocation, correlation, reporting
    ↓
trustvian dev     ← task 077: the repeatable local runtime
    ↓
existing pipeline ← unchanged
    ↓
ControlPlane.CompareEvaluations   ← diff · scorecard · gate, server-owned
```

The runner is an **adapter**, in exactly the sense ADR 0023 and ADR 0033 use:
it drives the control plane over its existing surface and decides nothing. A
runner that computed its own diff would be the duplication those ADRs exist to
prevent, and it would drift from the server's answer the first time either
changed.

Reference association is the one genuinely new piece of state, and the task
must settle it: how a scenario names the run it compares against. Candidates
to evaluate include an explicit reference run identifier, the most recent
completed run of the same scenario on the same agent, or a run recorded
against a named environment. Whichever is chosen must be deterministic and
must fail loudly when the reference is missing — a comparison against nothing
is not a pass.

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
| the workload exits non-zero | operational failure, `3`. No gate verdict is produced from a run that did not complete |
| the workload produces no evidence | the existing minimum-evidence gates fail it — task 056 made that mandatory precisely so an empty run cannot look perfect |
| the reference is missing | operational failure, named. Never an implicit pass |
| behavioral evidence saturates | reported as incomplete, distinct from a gate failure — the existing `ErrIncompleteSnapshot` semantics |
| a scenario file is malformed | usage failure, `2`, before anything runs |
| a suite of scenarios | each runs independently; one failure does not abort the rest unless asked, and the summary is machine-readable |

Scenario count, per-scenario duration and output size are all bounded, with
the bounds stated rather than implied.

## Security and privacy

Fixture inputs belong to the developer's repository and Trustvian does not
ingest, store or transmit their contents — it passes them to the workload.
Nothing in a scenario result contains prompts, completions, arguments,
results or bodies; the result is the diff, the scorecard and the gate, which
are already metadata-only.

A scenario file is executable configuration — it names a command — and it is
read from the developer's own repository under their own privileges, exactly
as a Makefile or a test script is. The specification should say so plainly
rather than implying a sandbox this task does not build.

## Compatibility

Additive: a new scenario file format and a new subcommand. Nothing existing
changes, and the diff, scorecard and gate contracts are consumed unchanged.

## Tests

- A scenario runs its command with the declared inputs and produces evidence
  through the real pipeline.
- Two runs of one unchanged scenario produce comparable evidence — the
  repeatability property the task exists for.
- A candidate with an added behavior produces a diff naming it and a gate FAIL
  under `max_added_behaviors: 0`.
- The same candidate passes under a limit that permits it, proving the verdict
  is the limits' and not the runner's.
- **No ordering assertion exists**: a scenario whose actions occur in a
  different order but with the same behavioral surface still passes; a test
  asserts the runner compares sets and recorded sequence signals, not a script.
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
`docs/compatibility.md` (the file format and the subcommand's exit codes),
`docs/ROADMAP.md`, this task's status, the task index and `CHANGELOG.md`.

## ADR

Warranted for the boundary decision: why a behavioral scenario compares
behavior and never answer quality, and why that keeps Trustvian out of the
generic-evaluation category. Also for the reference-association rule, which is
the task's one piece of new semantics.

## Acceptance criteria

1. One scenario file describes how to run a workload and what its behavior
   must satisfy.
2. Running it twice against an unchanged workload produces comparable
   evidence.
3. A candidate that gains a behavior produces a diff naming it and a
   deterministic gate result under the declared limits.
4. The verdict comes from the control plane; the runner computes nothing.
5. Exit codes follow the existing contract, and a crashed workload is never
   reported as a gate failure.
6. No answer-quality, judge, rubric, hallucination, relevance or benchmark
   concept exists anywhere in the feature.
7. No dataset, prompt-registry or model-comparison entity is introduced.
8. Results are machine-readable and usable in CI without parsing human output.
9. Behavioral ordering is never asserted by a scenario.

## Open questions left to implementation

1. **Reference association** — explicit identifier, last completed run of the
   same scenario, or environment-scoped. An explicit identifier with a
   documented convenience default is assumed.
2. **Where the subcommand lives** — extending the `eval` family is assumed, so
   it inherits the gate exit-code contract rather than defining one.
3. **Whether a suite is a directory or a file listing scenarios.** A directory
   is assumed.
4. **Whether scenarios run against `trustvian dev` only**, or can attach to an
   already-running runtime. Both, with the latter as the CI path, is assumed.
