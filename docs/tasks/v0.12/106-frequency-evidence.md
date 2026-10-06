# 106 — Frequency Evidence

Status: Specified; not implemented
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [078](../v1.0/078-behavioral-scenario-suites.md) (implemented),
[087](087-performance-and-cost-evidence.md) for the 429 rule,
[081](081-persisted-behavior-fidelity.md) for `max_llm_calls_per_run`
Decision records: [ADR 0029](../../adr/0029-hard-gates-use-explicit-integer-evidence.md),
[ADR 0053](../../adr/0053-repeated-evaluation-counts-identities-across-isolated-repetitions.md),
[ADR 0064](../../adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md)
Blocks: [107](107-change-impact-view-and-report.md)'s behavior columns

## Developer problem

The comparison today answers *was a behavior present*. After a model change, a
developer also asks *how often*:

```text
the candidate still calls crm_lookup in 10/10 runs — but 9 times per run, not 3
the knowledge base now gets 2.4× the traffic, and returns 429s
send_email was in every reference run and is missing from 6 of 10 candidate runs
```

Presence alone shows none of these. The first is a cost and load change, the
second is an outage waiting to happen, and the third often means the agent
stopped finishing its task.

## Current state, verified against `main` (`d1ad5f6`)

| Evidence | State | Evidence in source |
|---|---|---|
| Per-behavior "seen in k of N" | **Implemented** (078) | `RepeatedBehaviorPresence{ReferenceRunsPresent, CandidateRunsPresent, Classification}` (`platform/repeated.go:169-175`); on the wire as `reference_runs_present` / `candidate_runs_present` (`platform/httpapi/repeated.go:63-69`) |
| Repeatedly added / removed classification | **Implemented** | `platform/repeated.go:236-245`. Removed is reported, not gated (078 § *Repeatedly removed is a classification, not a gate*) |
| Observations per behavior per run | **Persisted** | `BehaviorEntry.Observations` per run (`platform/behavior.go:90-101`) |
| Calls per run, per-target ratio | **Absent** | nothing aggregates `Observations` across a scenario's runs |
| Rates on the single-run delta | Float, in Go only | `BehaviorDelta.ReferenceRate/CandidateRate/RateDelta float64` (`platform/behavior.go:407-419`); **not on the wire** (`httpapi/dto.go:378-384`) |
| Lost transitions | **Not recorded** | no transition is recorded per execution. The only sequences in the platform are ingest order and completion order (`platform/observation.go:74`, `scenario_execution.go:121-126`) |
| Layer (model / tool / …) per behavior | **Not persisted, by ADR 0047 § 2** | carried on the envelope and realtime only |
| HTTP status / 429 | **Absent** until 087 | — |
| Scenario gate keys | five | `ScenarioGate` (`config/scenario.go:94-99`) |

## Scope

### 1. Frequency evidence on the repeated comparison

The control plane computes all of it, from evidence it already persists
(per-run `BehaviorEntry.Observations`) and from 087's per-behavior summaries.
Every figure is an integer.

Per behavior, per side:

| Field | Meaning |
|---|---|
| `runs_present` | **unchanged**: 078's `reference_runs_present` / `candidate_runs_present`. This task adds no second name for it. `seen_in_k_of_n` is the *rendering* "k/N" of this field and N, and is not a new field |
| `calls_total` | Σ observations over the side's N runs |
| `calls_per_run_max`, `calls_per_run_min` | max and min over the N runs, counting a run where it was absent as 0 |
| `calls_per_run_mean_milli` | `⌊calls_total × 1000 / N⌋`, an integer in thousandths |

Per target (behaviors that share `target_name` and `target_category`), the same
four fields, plus:

| Field | Meaning |
|---|---|
| `call_ratio_permille` | `⌊candidate.calls_total × 1000 / reference.calls_total⌋`. **Unavailable** when the reference total is 0, never "infinite" and never 0 |

A behavior's classification gains one value, **`lost`**: present in N/N
reference runs and in fewer than N candidate runs. It is a classification, like
`repeatedly_removed`, and is reported for every behavior it applies to.

**Lost transitions are deferred, not approximated.** The brief asks for them
"where 078's execution already records them", and it records none. Recording
transitions per execution would need the engine's sequence state, which `dev`
runs at weight 0 (078 amendment 2026-10-03), or a new per-run transition
aggregate. Either is its own task. This task reports `lost_transitions:
"not_recorded"` and does not reconstruct transitions from 067's retained
observations, which hold the first 4096 per run and are not a complete sequence.

### 2. Four new named gate limits, all optional

These are new named gates under ADR 0029 § 7. They do not reinterpret
`max_repeated_added_behaviors` or any other existing limit. Each follows #131's
`OptionalGateLimit` precedent: **omitted means `not_evaluated`**, and present
means evaluated. Zero is a strict value, not "unset".

| Limit | Check value | Passes when | Evidence it needs |
|---|---|---|---|
| `min_candidate_frequency` | the minimum `candidate.runs_present` over behaviors with `reference.runs_present == N` | value ≥ limit | presence (always available) |
| `max_lost_behaviors` | the count of behaviors classified `lost` | value ≤ limit | presence |
| `max_calls_per_run` | per named target: max over candidate runs of that target's calls in one run | value ≤ the target's limit, for every named target | presence + per-run observations |
| `max_llm_calls_per_run` | max over candidate runs of model-layer calls in one run | value ≤ limit | **layer per behavior, persisted** — only after 081 persists it |

`_per_run` takes the **maximum across repetitions**, never a sum or a mean,
for the reason 078 gave for `max_block_decisions_per_run`: a sum would make a
limit mean something different at `runs: 5` than at `runs: 1`, and a mean would
let one quiet run offset a heavy one.

**A configured limit whose evidence is absent is `deferred`.** For example,
`max_llm_calls_per_run` on a run ingested without layer evidence. The check
reports `deferred` with the missing evidence named, and is never computed from
a substitute. See
[§ Conflict: deferred and fail-closed](#conflict-deferred-and-fail-closed) for
what that does to the verdict.

Scenario file fields, under `gate:`, parsed by the root `config` package with
the existing conventions (schema version, every present field validated, no
silent defaults):

```yaml
gate:
  # … the five existing limits …
  min_candidate_frequency: 8
  max_lost_behaviors: 0
  max_calls_per_run:
    - target: crm.internal
      max: 6
    - target: kb.internal
      max: 10
  max_llm_calls_per_run: 12
```

`max_calls_per_run` is a list of at most 16 entries with unique targets. A
target named in it that appears in **neither** side's evidence is reported
`not_observed`. Without that, a typo would pass silently.

### 3. The comparison rule table

Per [ADR 0064](../../adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md),
evaluated on the control plane over this comparison's evidence only, in this
fixed order:

| # | `rule` | Condition | Text template |
|---|---|---|---|
| 1 | `compare.added_in_all_runs` | a behavior with `candidate.runs_present == N` and `reference.runs_present == 0` | "{behavior} was added in {N}/{N} candidate runs. If this was expected, add it to the scenario; otherwise investigate before promoting." |
| 2 | `compare.target_load_with_429` | a target with `call_ratio_permille ≥ 2000` and candidate `http_429 > 0` | "{target} received {ratio} the reference's calls and returned {http_429} 429 responses. Check its rate limit." |
| 3 | `compare.lost_in_half` | a behavior with `reference.runs_present == N` and `candidate.runs_present * 2 ≤ N` | "{behavior} was in every reference run and is missing from {N − k} of {N} candidate runs. Task completion may have regressed." |

`{ratio}` is rendered by the control plane from the integer permille (2400 ‰ →
"2.4×"). Rule 2 cannot fire until 087's status classes exist, and never fires on
a run whose status codes are unavailable (ADR 0064 § 3).

## Conflicts this task records

### Conflict: `seen_in_k_of_n` already exists

078 already publishes per-behavior presence as two integers. This task does not
add a second field for it, and asks 107 to render "k/N" from the existing pair
on the control plane.

### Conflict: ratios and means against ADR 0029 and 078

ADR 0029 § 4 forbids rate gates. 078 says per-behavior repeated evidence has "no
frequency, no proportion, no rate". This task introduces two derived numbers,
`calls_per_run_mean_milli` and `call_ratio_permille`. Both are:

- **integers**, computed from integer sums by one floor division, so they are
  order-independent and reproducible;
- **reporting only**: **no gate reads either.** The four gates above read counts
  and per-run maxima. Rule 2 reads the permille ratio, and per ADR 0064 a rule is
  not a gate.

078's sentence is about the *gate's* evidence, and its gate is unchanged. This
file amends 078's per-behavior evidence by adding reported fields, and states
that it does so.

### Conflict: `max_calls_per_run[target]` and ADR 0029 § 5

ADR 0029 § 5 rejects `map[string]threshold` and gates over fields whose
semantics have not been approved. A per-target limit is keyed by a string, and
the targets are producer-supplied.

Proposed resolution: the semantics are fixed and approved here ("calls to this
target in one run"), and only the *key* is configurable. The list is bounded
(16) and validated (unique, non-empty, at most 255 bytes). It produces **one
named check** (`max_calls_per_run`), whose evidence lists each target's actual
value, limit and outcome in configuration order. The check fails if any target
fails. This is the closest shape to ADR 0029 that the request allows. A human
decides whether that is enough, or whether a per-target limit should wait.

### Conflict: deferred and fail-closed

ADR 0029 § 2 fails closed on missing evidence. The brief asks for limits to be
"deferred rather than approximated". Those agree on *not approximating*. They
can disagree on the verdict.

**Proposed: a `deferred` check fails the verdict.** A developer who configured
`max_llm_calls_per_run` and ran with a producer that emits no layer evidence
asked a question that could not be answered. Reporting PASS would answer it
anyway. The output names the missing evidence, and the developer can remove the
limit to opt out. The alternative, deferred-does-not-fail, is safe only if every
renderer makes the deferred check impossible to miss. That is a weaker
guarantee, and it is the one ADR 0029 was written to avoid.

### Conflict: `max_lost_behaviors` gates what 078 chose not to gate

078 deliberately did not gate removal: "a candidate that legitimately dropped a
behavior would then fail a gate nobody chose." `max_lost_behaviors` is optional
and omitted by default, so nobody is subject to a gate they did not choose.
This specification is the "own review" that ADR 0029 § 7 asks for.

## Non-goals

- **No statistical inference.** 078's non-goal stands: no variance, no
  confidence interval, no significance test, no flakiness score.
- **No default for any new limit.**
- **No lost-transition evidence** until transitions are recorded.
- **No rate gate.**
- **No content.**

## Technical requirements

1. All new evidence is computed by the control plane in
   `CompareRepeatedEvaluations` (`platform/repeated.go:395`) from persisted
   evidence. The runner and the CLI compute nothing.
2. Every new field is invariant under any permutation of either side's
   repetitions, proven by test, as 078 requires for presence.
3. At N = 1 with none of the new limits configured, the result is
   byte-identical to today's apart from the added reporting fields. With the
   limits omitted, the verdict is unchanged in every case.
4. Overflow on any sum is an error, never a wrap.

## Tests

- Golden repeated comparisons over a fixed record set: calls per run,
  min/max/mean-milli, per-target totals and permille, the `lost`
  classification, on both backends.
- `call_ratio_permille` is unavailable at a reference total of 0, and both
  floor boundaries are tested.
- One test per gate: omitted → `not_evaluated`, satisfied, violated, and
  evidence absent → `deferred` → FAIL.
- `max_calls_per_run`: a typo'd target is `not_observed`, duplicates are
  refused, and the 17th entry is refused.
- One test per rule-table row (ADR 0064), including rule 2 not firing without
  status evidence.
- The degradation suite: a producer with no usage, status or layer evidence
  gets byte-identical behavioral results and an unchanged verdict when the new
  limits are omitted.
- The result-document schema test (`docs/compatibility.md`) covers the new
  fields as additive.

## Acceptance criteria

1. Every behavior in a repeated comparison carries runs present, calls total,
   calls per run (min, max, mean in thousandths) per side, and its
   classification, including `lost`.
2. Every target carries the same figures and a permille call ratio, or
   *unavailable*.
3. The four limits are optional, named, integer, and each is reported as
   evaluated, not evaluated, deferred or not observed. None is computed from a
   substitute.
4. The scenario file accepts the four fields with the existing parse-time
   validation.
5. The three rules fire exactly on their stated evidence and change nothing
   else.
6. Lost transitions are reported as not recorded.

## Risks

- **Calls per run measures the model as much as the change.** A different model
  calling a tool twice as often is a real behavioral change, and it is also
  what a nondeterministic model does on a bad day. Reporting min and max next
  to the mean is the honest minimum. No rule fires on calls per run alone.
- **Per-target limits invite a long configuration.** That is why the list is
  bounded at 16.
