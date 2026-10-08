# 106 — Frequency Evidence

Status: Implemented — see [What shipped](#what-shipped)
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [078](../v1.0/078-behavioral-scenario-suites.md) (implemented),
[087](087-performance-and-cost-evidence.md) for the 429 rule,
[081](081-persisted-behavior-fidelity.md) now owns `max_llm_calls_per_run` (D3)
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

**Re-verified against `946a4fc` before implementation.** Rows that no longer
held as written:

- **Moved line references.** 087 moved several cited lines:
  - classification: `platform/repeated.go:236-245` → `:247-256`;
  - `CompareRepeatedEvaluations`: `:395` → `:411`;
  - `BehaviorEntry`: `platform/behavior.go:90-101` → `:90-107`, gaining 087's
    `Operational`;
  - `BehaviorDelta`: `:407-419` → `:435-447`;
  - the delta DTO: `httpapi/dto.go:378-384` → `:389-395`.
- **"HTTP status / 429: Absent until 087"** no longer holds. 087 persists
  per-behavior HTTP status classes and `http_429`.
- **"Calls per run, per-target ratio: Absent"** still holds for calls. A
  per-target aggregation now exists: 087's `GET /v1/evaluations/operational`
  sums operational summaries per target.

The rest held. `observation.go:74`, `scenario_execution.go:121-126`,
`httpapi/repeated.go:63-69` and `ScenarioGate` at `config/scenario.go:94-99`
were unchanged.

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
| ~~`max_llm_calls_per_run`~~ | **Moved to [081](081-persisted-behavior-fidelity.md#max_llm_calls_per_run-moved-here-from-106-maintainer-decision-d3)** (maintainer decision D3). It needs layer evidence only 081 persists, and shipping it first would make every scenario that configures it fail as deferred. The scenario parser keeps refusing it as unknown until 081 | | |

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

**Decided (D2), recorded in [ADR 0066](../../adr/0066-a-per-target-call-limit-is-one-named-check-over-a-bounded-list.md):**
the semantics are fixed and approved here ("calls to this
target in one run"), and only the *key* is configurable. The list is bounded
(16) and validated (unique, non-empty, at most 255 bytes). It produces **one
named check** (`max_calls_per_run`), whose evidence lists each target's actual
value, limit and outcome in configuration order. The check fails if any target
fails. This is the closest shape to ADR 0029 that the request allows, and the maintainer
accepted it.

### Conflict: deferred and fail-closed

ADR 0029 § 2 fails closed on missing evidence. The brief asks for limits to be
"deferred rather than approximated". Those agree on *not approximating*. They
can disagree on the verdict.

**Decided (D1): a `deferred` check fails the verdict.** A developer who configured
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

## What shipped

All four layers as scoped, with D1–D3 applied. No schema step: everything is
read from what 078 and 087 already persist. The differences below are each a
decision taken while building.

| Specified | Shipped | Why |
|---|---|---|
| `lost` as a new value of `classification` | A boolean `lost` beside `classification` on every behavior. A behavior can be both `removed` and lost | `classification` is a closed vocabulary. The 079 renderer refuses any value outside `added`/`removed`/`neither`, so every released renderer would have shown *no verdict* for any document containing `lost`. The compatibility contract requires existing clients to keep accepting the documents |
| The new checks among the gate's checks | `gate.frequency_checks`: always three entries in fixed order, each with `state` `not_evaluated`, `evaluated` or `deferred` | `gate.checks` is "exactly six in a stable order" (`docs/compatibility.md`), and the renderer refuses a seventh. The verdict accounts for both arrays |
| "max and min over the N runs" | Over the side's **completed** repetitions. With none completed, `runs` is `"0"` and the per-run figures are absent | A run that did not complete is not evidence, and 078's completion checks already fail it. Reporting it as zero calls would be a measurement nobody made |
| `min_candidate_frequency` over behaviors with `reference.runs_present == N` | The same, and `deferred` when no behavior was in every reference run | A minimum over nothing is not a figure. Under D1 it fails rather than passing vacuously |
| `max_calls_per_run[target]` | Keyed by target **name**, summed across target categories | A scenario author knows a host, not the category a convention filed it under |
| A typo'd target "reported `not_observed`" | `not_observed` also **fails** the check | Otherwise the typo still passes the verdict, and only a careful reader would notice (ADR 0066 § 4) |
| The limits echoed in `gate_limits` | Echoed only when supplied (`omitempty`) | With the limits omitted the echo is byte-identical to before 106 |
| `{ratio}` rendered from permille | One decimal, truncated: 2400 → "2.4×", 2999 → "2.9×" | Computed from the integer, so every renderer gets the same text |
| Rule 2 "never fires without status evidence" | Fires only when `call_ratio_permille ≥ 2000` **and** the candidate's summed `http_429 > 0` | Without status codes `http_429` is 0, so no separate availability check is needed (tested) |
| Suggestions | `suggestions[]` and `suggestions_truncated` on compare-repeated, at most 64 | Rules 1 and 3 fire per behavior, so 512 behaviors could produce over 1,000 sentences without a bound |

### Measured with a real model

`trustvian eval run` at `runs: 5` ran the demo agent
(`trustvian-python-agent-demo`, `harness/run_agent.py`, 3 tickets) against
itself:

- the same configuration on both sides, under `trustvian dev` built from this
  branch;
- Ollama `gemma3:4b` at the agent's default temperature;
- `opentelemetry-instrument` with requests instrumentation;
- all three frequency limits configured.

Every figure below is read from the `compare-repeated` response in the result
document of execution
`scn-frequency-106-measurement-20261007T203736-0eb5d3c5`.

| Behavior | Present (ref / cand) | Calls per run, both sides (min / max / mean‰) |
|---|---|---|
| `http/POST → ollama.localhost` (the model) | 5/5 / 5/5 | 20 / 20 / 20000 |
| `tool/send_email`, `http/POST → mail.localhost` | 5/5 / 5/5 | 6 / 6 / 6000 |
| `tool/crm_lookup`, `tool/knowledge_search`, `http/GET → crm.localhost`, `→ knowledge.localhost` | 5/5 / 5/5 | 4 / 4 / 4000 |
| six others, once per run | 5/5 / 5/5 | 1 / 1 / 1000 |

- **Per target:** every `call_ratio_permille` was **1000**. The model received
  100 calls a side, and the unnamed target 85.
- **Checks:**
  - `min_candidate_frequency` evaluated, actual 5 ≥ 1;
  - `max_lost_behaviors` evaluated, actual 0 ≤ 0;
  - `max_calls_per_run` passed, at `crm.localhost` 4 ≤ 50 and `ollama.localhost`
    20 ≤ 50.
- **Verdict:** PASS, with no suggestions and `lost_transitions: "not_recorded"`.

What this does and does not show:

- **The pipeline:** the figures arrive from real telemetry, through the gate,
  as specified.
- **No variance:** this agent at its default temperature made exactly the same
  calls in all ten runs. Every minimum equals its maximum, so the run exercises
  none of the min/max spread, `lost`, or any rule. Measuring those against a
  real model needs a nonzero temperature (the demo's `make stability` setting
  is 0.7) and is left to Phase 4's measurement.
- **Tool spans carry no target.** The convention table names tools by tool name
  only, so every tool behavior shares the unnamed target, here 85 calls a
  side. `max_calls_per_run` therefore limits hosts, not tools. A per-tool limit
  would be a different check.

Proven by test:

- the N = 1 response is byte-identical to `main`'s at `946a4fc` once the added
  members are removed;
- every new counter is invariant under all 36 orderings of three runs a side;
- SQLite and PostgreSQL agree on a golden;
- overflow is an error;
- each check is tested in each state, and each rule firing, not firing and
  without evidence;
- removing the rule table changes nothing else;
- the 079 renderer renders a document carrying every new field byte for byte;
- `StableFeatures` and the baseline key keep exactly their fields.

