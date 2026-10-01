# 0053 — Repeated evaluation counts identities across isolated repetitions

**Status:** Accepted

## Context

[Task 078](../tasks/v1.0/078-behavioral-scenario-suites.md) makes a behavioral
comparison repeatable: run a scenario N times per side and gate over evidence that
survives a nondeterministic workload. Its
[measurement re-run](../tasks/v1.0/078-behavioral-scenario-suites.md#the-re-run-at-tool-name-fidelity-2026-10-01)
showed why that matters. An unchanged agent's set of behavioral identities moved
between isolated runs, and a single-run gate at zero failed 48/90 and 59/90
unchanged pairs.

The task specified the shape of the answer: per-behavior integer presence counts,
k-of-N classification, and six named checks. It left one question open (its open
question 8): **what unit `max_repeated_added_behaviors` counts.** It also left
several implementation choices to the slice that builds it.

## Decisions

### 1. The unit: repeatedly added behavioral identities

`max_repeated_added_behaviors` bounds the number of **behavioral identities**
(fingerprints) the control plane classifies as repeatedly added:

```text
candidate_runs_present >= k   and   reference_runs_present <= j
```

This is the unit of `max_added_behaviors`, and that is the reason for it:

- **It is what makes N = 1 an addition rather than a redefinition.** At N = 1,
  k = 1, j = 0 the predicate is task 054's `reference == 0 && candidate > 0`, and
  the count is exactly the single-run gate's `AddedCount`. A test asserts the
  repeated gate reaches the same verdict as `EvaluateEvaluationGate` on the same
  pair, check for check. Any other unit would silently change what a limit means
  at N = 1.
- **Existing single-run semantics are untouched.** `max_added_behaviors`,
  `max_added_behavior_changes` and `eval compare` keep their exact meanings.

**The consequence, stated rather than discovered.** A new tool and the transport
child it calls are two identities. Their presence counts moved together in every
run the measurement recorded, so both cross `k` together. At
`max_repeated_added_behaviors: 0` that is invisible, as it is for
`max_added_behaviors`. At any nonzero budget one act consumes two units. The
documentation says so wherever the limit is described.

**Counted changes are not aggregated.** ADR 0052's `added_change_count` is
computed per comparison pair, from that pair's added set and that candidate run's
recorded parentage. A change's root is a property of a pair, not a stable key
across repetitions. Counting "the same change" in k of N runs would need a change
identity that is stable across runs, and none is defined. Summing or maximising
pairwise change counts would count something no single run defines. A repeated
limit over changes is therefore **not** approximated here. If it is built, it is
a separate, optional limit — the analogue of #131 for `max_added_behavior_changes` —
with its own change key and its own review.

### 2. Presence is read per repetition, so the pairing carries no meaning

[The design notes](../tasks/v1.0/078-design-notes.md#the-reduction-and-permutation-invariance)
proposed reducing N pairwise `BehaviorDiff` values. This slice instead reads each
repetition's persisted behavior snapshot and aggregate — the evidence
`CompareEvaluations` itself reads — one run at a time, with no pair formed.

The reason is that a diff ties a run's counts to its pair partner. If one
repetition did not complete, its partner's presence would be lost with it. Read
per run, presence counts are invariant under any permutation of either side **by
construction**. The permutation test still asserts it. The same validation still
applies:

- an incomplete snapshot is refused;
- identity must be one-to-one across every repetition, in both directions. A
  fingerprint carrying two descriptors is refused, and so is a descriptor
  arriving under two fingerprints (`ErrFingerprintConflict`). Counted per
  fingerprint, the second would split one behavior's presence below `k`, and a
  behavior every candidate run added would pass a zero budget;
- more than 512 distinct identities across the execution is refused.

Every repetition must also run in **one environment**: the same project and the
same `EnvironmentRef`, which is what `CompareEvaluations` requires of its pair.
Environment is a fingerprint dimension, so a repetition elsewhere presents each
behavior under fingerprints no other repetition uses, and its presence would
split the same way. The control plane refuses this with
`ErrBehaviorEnvironmentMismatch` — the single-run comparison's error for it —
before any evidence is loaded, so a run that ingested nothing is held to it as
well.

The N = 1 equivalence is proven against the real single-run chain, so the change
of route costs no assurance.

### 3. Isolation is the control plane's to verify

Each repetition must run under its own behavioral profile. Otherwise repetition
i meets a baseline that learned from 1..i−1, and presence measures learning order
instead of the workload. The runner allocates a fresh profile per repetition
through `trustvian dev --behavioral-profile`. The control plane **refuses**
repetitions that share a profile (`ErrRepeatedIsolation`) rather than trusting
the runner to have done so.

### 4. Six checks, all evaluated; the engine checks take the worst repetition

| # | Check | Rule |
|---|---|---|
| 1 | reference repetitions completed | `== N` |
| 2 | candidate repetitions completed | `== N` |
| 3 | repetitions failing minimum evidence (completed with zero records) | `== 0` |
| 4 | repeatedly added behaviors | `<= max_repeated_added_behaviors` |
| 5 | worst candidate block decisions | `<= max_block_decisions_per_run` |
| 6 | worst candidate critical-risk observations | `<= max_critical_risk_observations_per_run` |

All six are populated on every call, with no short-circuit (task 056's rule).

- **A non-completed repetition fails check 1 or 2 as a result, not an error,** so
  everything measured is still shown.
- **Checks 5 and 6 take the maximum across candidate repetitions**, never a sum
  (which would change a limit's meaning with N) and never a mean (which would let
  a clean repetition offset a blocking one). Both directions are pinned by test.
- **Each repetition's block and critical-risk counts are retained in the
  result**, so the repetition that blocked can be found.

**Advisory at N > 1.** Checks 5 and 6 carry `advisory: fresh_scope`. Against a
fresh learning scope the engine has learned nothing, so a pass does not answer the
learned-policy question. The tool-fidelity measurement also showed that one of
these checks *can* fire against a fresh scope, so neither a pass nor a fail is a
learned verdict. The marker changes no verdict.

### 5. One crashed repetition ends the scenario with exit 3

`trustvian eval run` executes repetitions sequentially, reference then
candidate. The first repetition whose workload exits non-zero ends the scenario:

- the remaining repetitions do not run;
- no comparison is requested;
- the exit is `3`.

A verdict over fewer runs than the scenario declares would be a different test
from the one its author wrote. A broken workload is never `1`.

**A repetition whose run could not be completed ends the scenario the same
way**, even when its workload exited `0`. `trustvian dev` keeps the workload's
status when its own completion request fails, because that is its published
contract. But a run the control plane does not hold as completed would fail
check 1 or 2, and that turns a bookkeeping failure into a behavioral `FAIL`. So
`dev` reports the completion separately to the runner, and the runner treats a
missing completion as it treats a crash.

Each repetition runs as `trustvian dev` runs, with two differences that only
the runner asks for:

- the workload's standard output goes to the runner's stderr, so the runner's
  stdout carries one result document;
- a bare command name is resolved against the side's own `PATH`, from its
  `env`. The parent's `PATH` is not used, so two sides that select different
  virtualenvs run different executables.

Neither difference changes a process-wide descriptor or variable. The exit-code
contract is `eval compare`'s, unchanged: `0` PASS, `1` FAIL, `2` usage, `3`
operational.

### 6. No persistence in this slice

The self-contained mode runs both sides in one invocation and needs no stored
state. The specification's persisted scenario executions exist to serve
`--reference <execution>|last`, which compares against a recorded prior
execution. That mode is **not** in this slice, so this slice takes **no schema
step** and the schema version stays where it is. The execution identifier is
correlation only. It appears in run identifiers, profile names and the result
document, and in no fingerprint input or record field.

## Alternatives considered

**Count repeated changes instead of identities.** Rejected for this limit:
pairwise `added_change_count` has no stable cross-repetition unit, and using it
would break N = 1 equivalence with `max_added_behaviors`. Deferred as a separate
optional limit with its own contract.

**Reduce N pairwise diffs.** Equivalent counts in the happy path, but it couples
each run's presence to its partner's completion. Rejected for per-run reading,
which makes the permutation invariance structural.

**Trust the runner for isolation.** Rejected: the control plane holds each run's
recorded profile and can verify isolation, so it does.

**Persist executions now.** Rejected for this slice: nothing in it reads them,
and an unread table is a migration with no consumer.

## Consequences

- `POST /v1/evaluations/compare-repeated` is new `/v1` surface. The existing
  comparison route, its limits and its result are unchanged.
- `trustvian eval run --scenario <file>` is new CLI surface in the `eval` family,
  inheriting its exit-code contract.
- The scenario file is a new configuration document in `config`, with every
  threshold required and no defaults.
- **Not yet built:** `--reference` against a prior execution, its persistence and
  migration, suites of scenarios, and a repeated limit over counted changes. Task
  078 records each as remaining work.
