# 0054 — Scenario executions are persisted, and references are resolved by the control plane

**Status:** Accepted

## Context

[ADR 0053](0053-repeated-evaluation-counts-identities-across-isolated-repetitions.md)
built task 078's repeated evaluation in self-contained mode: every
`trustvian eval run` executed both sides. It deferred persistence (§ 6) together
with the one feature that needs it:

```text
trustvian eval run --scenario FILE --reference <execution-id>|last
```

That command runs only the N candidate repetitions and compares them against the
reference side of an earlier execution. Task 078 fixes what the reference must be:

- a whole set of N runs from **one** execution, never a mix of two;
- never truncated or padded to fit;
- a loud failure when it is missing — a comparison against nothing is not a pass.

This ADR records how executions are stored, what `last` means, and who decides.

## Decisions

### 1. An execution is persisted, metadata only, under schema 9

Schema 9 adds two tables on both backends.

- **`platform_scenario_executions`** holds, for each execution:
  - identity: the execution id;
  - scope: scenario name, project, agent and environment;
  - N;
  - provenance: the reference execution it reused, if any;
  - lifecycle: status, start and finish times;
  - the verdict and a completion sequence.
- **`platform_scenario_repetitions`** holds the ordered associations: for each
  repetition, its side, its index, its run and its behavioral profile.

Every execution is persisted, self-contained ones included. A self-contained
execution is what a later `--reference` reuses.

**Nothing else is stored.** That excludes:

- the scenario command and its environment values;
- the gate limits;
- prompts, completions and tool arguments or results.

The limits are left out deliberately (§ 4).

The step is forward-only and version-gated, `8 → 9`, on SQLite and PostgreSQL
alike.

- **It invents no execution.** Runs recorded before schema 9 were never part of
  a persisted execution. Grouping them into one by the shape of their
  identifiers would assert a scenario, an N and a reference side that nobody
  recorded.
- **Damage and newer schemas are refused.** A v9 stamp without v9's tables is
  damage and is refused. A newer stamp is refused, as for every earlier step.

There is no foreign key from a repetition to its run, for the reason the
promotion history gives ([ADR 0040](0040-promotions-are-immutable-evidence-backed-platform-decisions.md)):
runs belong to another store capability. A run that has gone is reported by name
when the reference is resolved, and the execution still loads.

### 2. Completed means evaluated, not passed

An execution moves through three states:

```text
running ──complete──▶ completed   (the comparison was evaluated: PASS or FAIL)
   └─────fail──────▶ failed       (no verdict: a repetition, a run's completion,
                                   or the comparison failed or was refused)
```

**A gate FAIL is a completed execution, and a usable reference.** Its reference
side is as good as a passing execution's. Rejecting it would make "reference"
quietly mean "a candidate that passed".

**Running and failed executions are never references.** That includes an
execution whose runner was killed, which stays `running` forever.

The transitions are atomic and their retry rules are defined:

- **Begin** inserts the execution. An existing id is `already_exists`.
- **Complete** is one transaction. It locks the project row and assigns the
  next completion sequence in the project. It inserts the associations and
  compare-and-swaps the status from `running`. A second completion is a
  conflict and writes nothing.
- **Fail** compare-and-swaps from `running`. Failing a failed execution
  returns it unchanged, because the runner's cleanup must be safe to retry.
  Failing a completed execution is a conflict.

### 3. `last` is a completion sequence, chosen first and validated second

`last` selects the completed execution with the same scenario name, project,
agent and environment, and the highest **completion sequence**.

The sequence is an integer assigned per project, inside the transaction that
completes the execution, under a lock on the project row. A uniqueness
constraint on (project, sequence) backs it up. The execution id breaks ties
only in a damaged database.

The sequence does not depend on a clock, an execution id or the stored
RFC3339Nano text. That text keeps the caller's zone offset, so it does not sort
chronologically, which is the trap `store.go` already warns about.

**The latest execution is the reference, or there is none.** If its N differs,
or its evidence is unusable, the error names it. An older execution is never
substituted: a silent fallback would compare against a reference nobody asked
for.

### 4. The control plane resolves and validates every reference

Reference resolution, compatibility and association checks are the control
plane's (`BeginScenarioExecution`). The runner sends a mode and an id.

**A reference is compatible when:**

- it is **completed**;
- it has the **same N**;
- it is in the **same project and environment** as the new execution, which the
  comparison itself requires.

**Scenario name and agent:** `last` matches on both. An explicit id may name
another scenario or agent. Naming it is the caller's decision, and a renamed
scenario keeps its history this way.

**Stored associations are checked against the runs.** Every reference run must:

- exist;
- be completed;
- carry the profile the execution recorded;
- be in the reference's project and environment;
- have complete evidence.

All of this happens **before any candidate workload runs**, and again at
completion.

**A completed run with zero records passes resolution.** It fails the existing
minimum-evidence check, as it would in a self-contained execution. It is never
reported as a missing reference.

**The current scenario's limits always apply.** Limits are not persisted, so
nothing can be inherited, and nothing is required to match the reference's.
The question a reference answers is "what did the reference side do". The
current scenario owns the question "what is acceptable".

### 5. One gate implementation

Completion calls `CompareRepeatedEvaluations` once, unchanged. In recorded mode
the reference runs come from the stored execution, never from the client; a
client that sends reference runs for a recorded execution is refused.

Every rule of the self-contained comparison therefore holds:

- isolation across all 2N profiles;
- one environment;
- one-to-one identity;
- complete evidence;
- the six checks.

Candidate runs are additionally held to the execution's scope: project,
environment and agent.

The response carries the comparison in exactly compare-repeated's shape. The
runner embeds it unchanged.

### 6. The runner stays a thin adapter

`trustvian eval run` derives the execution's scope (project, agent, environment)
before anything runs. It uses the identity derivation every candidate
repetition performs, over the same configuration. That derivation names where
runs happen; it decides no evaluation question, and the control plane checks
every candidate run against it at completion.

The runner then:

1. begins the execution;
2. runs N or 2N repetitions;
3. completes the execution, or marks it failed on any abort.

Exit codes are unchanged:

| Situation | Exit |
|---|---|
| gate PASS | `0` |
| gate FAIL | `1` |
| usage error | `2` |
| a reference that is missing, unfinished, of another N or incomplete | `3`, before any workload |
| a workload or run-finalization failure | `3`, with the execution recorded failed |

`execution_id` remains the current invocation's identity. A `reference` block
(mode and resolved execution id) is added to the result document only when the
reference side was reused.

## Alternatives considered

**Order `last` by finish time.** Rejected. Wall clocks move backwards and
differ across processes, and the stored text does not sort. A sequence assigned
under the lock that serializes completions is chronological by construction.

**Fall back to the newest usable execution.** Rejected. It turns "your latest
reference is broken" into a comparison against a different one, silently.

**Persist the gate limits and require them to match.** Rejected. The reference
answers "what did the reference side do". The current scenario owns "what is
acceptable", and requiring a match would make tightening a limit impossible
without re-running the reference.

**Resolve `last` in the CLI by listing executions.** Rejected. It moves a
decision the control plane must own into an adapter (ADR 0033), and it needs a
list API this slice has no other reason to publish.

**Reconstruct executions for runs recorded before schema 9.** Rejected. Their
grouping, N and reference side were never recorded.

## Consequences

- **New `/v1` surface:**
  - `POST /v1/scenario-executions` (begin);
  - `GET /v1/scenario-executions/{id}`;
  - `POST /v1/scenario-executions/{id}/complete`;
  - `POST /v1/scenario-executions/{id}/fail`.
- **Existing comparison routes are unchanged.** `POST
  /v1/evaluations/compare-repeated` remains for callers that manage their own
  runs.
- **New CLI surface:** `--reference` on `trustvian eval run`, and the result
  document's optional `reference` block.
- **Schema 9** on both backends.
- **Not built:**
  - suites of scenarios;
  - the GitHub Action;
  - a repeated limit over counted changes;
  - scenario and input versioning;
  - a listing route for executions.

  Task 078 records each as remaining work.
