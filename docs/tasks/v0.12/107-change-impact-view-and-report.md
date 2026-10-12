# 107 — Change Impact View and Model Change Report

Status: 107-A implemented (the report's identity and JSON) — see [What shipped (107-A)](#what-shipped-107-a); 107-B implemented (one renderer, and the report the comment shows) — see [What shipped (107-B)](#what-shipped-107-b); 107-C implemented (Change Impact and the Overview panel) — see [What shipped (107-C)](#what-shipped-107-c)
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [086](086-scenario-and-input-versioning.md),
[087](087-performance-and-cost-evidence.md), [106](106-frequency-evidence.md),
[081](081-persisted-behavior-fidelity.md), [085](../v1.0/085-evidence-resolution.md)
(implemented), [079](../v1.0/079-ci-integration-github-action.md) (implemented),
[099](../v0.11/099-overview-dashboard.md) (implemented)
Decision records: [ADR 0036](../../adr/0036-webui-is-a-same-origin-adapter-over-v1.md),
[ADR 0057](../../adr/0057-the-ci-renderer-is-a-standalone-offline-transcriber.md),
[ADR 0064](../../adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md),
[ADR 0069](../../adr/0069-a-completed-execution-records-the-gate-it-consumed.md)
(the report's identity, decision D10)
Blocks: nothing

**Presentation only.** This task computes nothing. Every number it shows is
already in a `/v1` response produced by 081, 086, 087 or 106. A number on any
screen that cannot be traced to a `/v1` response is a defect.

## Developer problem

After 086, 087, 106 and 081, the evidence for "what did my model change do" is
complete, and spread across five places: the repeated comparison, the
operational table, the sameness section, the fidelity on each delta and the
suggestions. A developer wants one table. A reviewer on the pull request wants
the same table, rendered identically.

## Current state, verified against `main` (`d1ad5f6`)

| Fact | Evidence |
|---|---|
| Compare calls `POST /v1/evaluations/compare` with `{reference_run_id, candidate_run_id, gate_limits}` and renders gate, behavior diff and scorecard blocks | `platform/webui/assets/v1/api.js:362-367`; `v1/render.js:769-782` |
| Comparisons are **not persisted**, and no comparison id exists | `platform/controlplane.go:1398-1411` |
| Scenario executions are persisted with id, verdict and repetitions, but **not their gate limits or check results** | ADR 0054 § 1, § 4 |
| The CI renderer reads `trustvian eval run --json` documents, version `"1"`, in scenario and suite shapes | `cmd/trustvian-ci-render/document.go:3-14`, `render.go:186-360`, golden files in `testdata/golden/` |
| Evidence resolution is keyed by run pair: `FindingRef{ReferenceRunID, CandidateRunID, Check \| Behavior, Side}` | `platform/evidence.go:90-104` |
| Overview has five panels (the file header says "Six panels" and lists five) | `platform/webui/assets/views/overview.js:3-11`; `index.html:221-227` |

## Scope

### 1. Change Impact replaces Compare

The Compare destination becomes **Change Impact**. One table per comparison,
and every cell is a field of a `/v1` response:

| Section | Source | Columns |
|---|---|---|
| Sameness | 086 `sameness`, `warnings[]` | scenario, inputs, model, prompt: `true` / `false` / `not_recorded` as the strings 086 returns, with both sides' recorded values (digests, `inputs` state, model, prompt name and digest) |
| Behaviors | 078 presence + 106 frequency + 081 fidelity | behavior · k/N reference · k/N candidate · calls/run (min–max, mean) per side · classification · lost · fidelity (with `mixed`) |
| Targets | 106 per-target + 087 operational | target · calls total per side · ratio ‰ · latency buckets per side · status classes · 429s |
| Tokens and cost | 087 `tokens`, cost with provenance | per side; cost only with `pricing_version` and `source` |
| Gate | 056/078 `gate.checks` + 106 `gate.frequency_checks` | every check from both arrays, read together: the six `checks`, then the four `frequency_checks` (106's three, then 081's `max_llm_calls_per_run`) with their `state` (`not_evaluated`, `evaluated`, `deferred`), per-target rows for `max_calls_per_run`, and `missing_evidence` when deferred |
| Suggestions | 105/106 `suggestions[]` | `rule`, `text`, evidence |

**Amends this table (spec maintenance after 106 and 086 landed).** 106
shipped `lost` as a boolean beside `classification`, not as a fourth
classification value, and its three checks in `gate.frequency_checks`, not in
`gate.checks`. Both choices keep 079's closed vocabulary and its exactly-six
checks. Change Impact and the shared renderer therefore:

- read `gate.checks` and `gate.frequency_checks` together, as one gate;
- render the boolean `lost` as its own column;
- never expect `lost` among `classification`'s values, or a seventh entry in
  `gate.checks`.

086's sameness answers are the strings `"true"`, `"false"` and
`"not_recorded"`, not booleans and not `not_stated`.

**k/N is rendered by the control plane.** Each behavior row carries
`runs_present` and N. The response gains a display string `"8/10"` that the
control plane produces, so the browser does no division and no formatting
of a fraction. The same applies to `mean_milli` → `"2.4"` and permille → `"2.4×"`.

**Every row resolves through 085.** A behavior row links to
`GET /v1/evidence/observations` for its fingerprint. A repeated comparison spans
2N runs and 085 resolves per run pair, so each behavior row carries
`present_in: {"reference": [0,1,…], "candidate": […]}` (repetition indices),
together with the repetition's run identifiers. The control plane computes these
from persisted snapshots. The link opens resolution for one repetition, chosen
by the reader from that list. The browser never decides which run "represents"
the behavior.

### 2. The report

```text
GET /v1/scenario-executions/{execution_id}/report             → JSON
GET /v1/scenario-executions/{execution_id}/report?format=md   → see § Conflict: where Markdown is rendered
trustvian eval report <execution_id> [--format json|md]
```

The JSON is the **result document, version 2**. It is version 1 (078 § *Result
document*) plus the 086, 087, 106 and 081 sections and `suggestions[]`, all
additive. `cmd/trustvian-ci-render` gains the new sections, keeps rendering
version 1 documents byte-identically (the existing golden files stay as they
are), and has new golden files for version 2. Its gate section renders
`gate.checks` and `gate.frequency_checks` together, and its behavior table
renders the boolean `lost`, as § 1 amends.

**The Markdown is identical to the 079 comment because it is produced by the
same code.** There is one renderer. Reaching identical output by maintaining
two renderers is not acceptable.

### 3. Overview: a "latest comparison" panel

A sixth Overview panel shows the newest scenario execution in the selected
context. It reads `GET /v1/projects/{id}/scenario-executions` (task 102's
recency key) and then that execution's report JSON: verdict, sameness, the
counts of added, removed and lost behaviors, and the first suggestion. It links
to Change Impact. Like every Overview panel it has its own read time, a Refresh
control, no timer, and separate loading, empty and error states. The stale
"Six panels" comment in `overview.js` becomes accurate.

## Conflicts this task records

### Conflict: a comparison has no identifier

The brief asks for `GET /v1/comparisons/{id}/report`. No comparison is
persisted: `CompareEvaluations` derives the diff, scorecard and gate on demand,
with limits supplied by the caller (`controlplane.go:1398-1411`). Task 091 builds
on that ("a historical gate-failure count has no durable decision context to be
counted from"). A route keyed by a comparison id needs a comparison to persist.

Options:

| Option | Cost |
|---|---|
| **A. Key the report by scenario execution** (proposed). Executions are already persisted. Add a **gate-result snapshot** to the execution (limits, every check with its actual value and outcome), recorded when the execution completes, following 066's rule that a decision keeps the gate evidence it consumed | One schema step. A new ADR amends ADR 0054 § 4 ("the limits are left out deliberately"). That gives 092 the "durable comparison context" 091 says 078 may add |
| B. Persist every comparison as a new entity with its own id | A new aggregate, a new table, a new retention question, and `/v1/comparisons/{id}`. More than this milestone needs |
| C. Re-derive the report from the execution at read time with the limits passed in | No schema step. But "identical to the 079 comment" would then depend on the caller supplying the same limits, and the report could disagree with the recorded verdict. That is the drift 066 was built to prevent |

Under A, a single-run `eval compare` has no report route, because it has no
execution. Its JSON is what `POST /v1/evaluations/compare` already returns,
extended with the same sections.

### Conflict: where Markdown is rendered

The 079 renderer lives in `cmd/trustvian-ci-render`. It is a standalone, offline
transcriber in the root module (ADR 0057). The platform module cannot import a
root `cmd/` package, and could import a root package only if it were public.

| Option | Cost |
|---|---|
| **A. The control plane serves JSON only, and Markdown is rendered client-side by the one renderer** (proposed). The renderer moves to `internal/cirender` in the root module, and `cmd/trustvian-ci-render` and `trustvian eval report --format md` both call it. The WebUI renders the JSON as HTML and does not render Markdown | No new public package. The route has no `format=md`, which amends the brief |
| B. Promote the renderer to a public root package and have the control plane import it | Adds public API under the compatibility contract, and makes the control plane a Markdown producer for a single consumer |

### Conflict: Compare → Change Impact, and `v0.11.0`'s Compare entry points

Overview, Runs and promotions open Compare with sides preselected (task 099,
104). Those entry points are kept. For a pair of runs that belongs to no
execution, Change Impact shows the single-run comparison's sections and marks
frequency, sameness and repeated fidelity *not applicable — not a scenario
execution*.

### Open decision: sameness on the single-run comparison

Task 086 puts `sameness` and `warnings[]` on the repeated comparison only.
`POST /v1/evaluations/compare` does not carry them, and the entry-points
paragraph above marks sameness *not applicable* for a pair of runs outside an
execution. This task decides whether that stays, or whether the single-run
comparison gains the same block, looked up through the executions that
recorded each run, as compare-repeated already does. Either way the answer is
additive and no check reads it.

## Non-goals

- **No computation in any adapter**: no division, no rounding, no
  classification, no choosing a representative run.
- **No new evidence.** If a column needs a number that no `/v1` response has,
  the gap belongs to the task that owns the evidence, not here.
- **No aggregate verdict or "impact score".**
- **No charts that are not a second rendering of printed numbers** (task 099's
  rule).

## Tests

- Golden version 2 result documents for: a model change, an input change, a
  missing-usage producer, a mixed-fidelity behavior, and a deferred gate. Each is
  rendered by the shared renderer and compared with golden Markdown. All
  version 1 golden files stay unchanged.
- `trustvian eval report --format md` output is byte-identical to
  `trustvian-ci-render` output for the same document.
- A WebUI test asserts that every numeric cell in Change Impact is a string
  field of the response, by walking the rendered DOM against the fixture, and
  that no arithmetic operator is applied to a response field.
  `TestUint64CountersAreNeverParsedAsNumbers` and
  `TestHTTPAdapterDoesNotComputeEvaluationLogic` are extended to the new views.
- Every behavior row's link resolves through 085 to observations, end to end,
  on both backends.
- The Overview panel's late response for a previous context is discarded (the
  existing 099 pattern).

## Acceptance criteria

1. Change Impact shows sameness, behaviors with k/N, calls per run and
   fidelity, targets with ratio, latency, errors and 429s, tokens, cost with
   provenance, every gate check, and suggestions, for one scenario execution.
2. Every number on the screen is a field of a `/v1` response.
3. Every behavior row resolves through 085 to its observations.
4. `trustvian eval report` prints the JSON, and its Markdown is byte-identical
   to the 079 comment for the same execution.
5. Overview has a latest-comparison panel with scope, read time and drill-down.
6. Unavailable reads *not available*, and a section that cannot be compared
   shows no delta.
7. Desktop and phone-width layouts work in both themes, with keyboard access
   and no meaning carried by colour alone (`v0.11.0` exit criteria 5 and 6).

## The measured report

At the end of this task, the demo agent runs on two Ollama models at `runs: 10`,
and the resulting report is committed under `docs/results/`. **The demo agent
is not in this repository.** `examples/` has in-process SDK demos and no
OTLP-emitting agent, and the reference workload 082 cites lives in the companion
demo repository. Producing that file therefore needs that repository and a local
Ollama with both models. The report records the model names, the Ollama
version, the hardware, and every unavailable section as it rendered.

## Risks

- **A single table invites a single number.** "Impact: high" is the next
  request. ADR 0064 and the non-goals answer it.
- **Version 2 of the result document is a consumed contract.** It is published
  in `docs/compatibility.md` before 079's action renders it.

## Maintainer decisions

- **D10 — The report is keyed by scenario execution** (Option A of
  [§ Conflict: a comparison has no identifier](#conflict-a-comparison-has-no-identifier)).
  When an execution completes it records a gate-result snapshot in the
  completion transaction: the limits; every entry of `gate.checks` and
  `gate.frequency_checks` with its state, actual value, bound, outcome and
  `missing_evidence`; the verdict. One schema step, v14, on both backends.
  [ADR 0069](../../adr/0069-a-completed-execution-records-the-gate-it-consumed.md)
  amends ADR 0054 § 1 and § 4: the decision keeps the gate evidence it
  consumed, by 066's rule. An execution completed before v14 has no snapshot;
  its report says `gate: not_recorded` and is never re-derived from today's
  code.
- **D11 — The control plane serves JSON only** (Option A of
  [§ Conflict: where Markdown is rendered](#conflict-where-markdown-is-rendered)).
  The route has no `format=md`; the Markdown comes in 107-B, from the one
  renderer.
- **D12 — The single-run comparison gains `sameness` and `warnings[]`**
  ([§ Open decision](#open-decision-sameness-on-the-single-run-comparison)).
  `POST /v1/evaluations/compare` looks each run up through the executions that
  recorded it, exactly as compare-repeated does. A run no execution recorded
  reads `not_recorded`. Additive; no check reads it.

- **D14 — Persist what version 1 already promised.** 107-A found two version 1
  members no execution kept: `producers.cli_version`, and `reference.mode` for
  a reused reference. One forward-only schema step, v15, adds two nullable
  columns to `platform_scenario_executions`, set from the begin request
  `eval run` already sends and validated in format: the version within the
  shared text bounds, the mode `execution` or `last`. An execution begun before
  v15 keeps reading `not_recorded`. The chained upgrade from v10 reaches v15 on
  both backends, and the recovery drill gains a v14 leg and restores at v15.
- **D15 — The PR comment renders the document `eval report` prints.** After a
  completion, `trustvian eval run --json` prints the execution's report, the
  document `GET …/report` serves, unchanged; a suite member embeds its
  execution's report. Against a control plane without the route (`404`) it
  prints version 1, as before, and says so on stderr. The 079 action then
  uploads, and the comment renders, exactly what `eval report --json` returns.
- **D16 — One report holds the per-target operational evidence.** Each report
  target carries its latency buckets, status classes and 429s per side, with
  `runs_with_evidence`, from the computation `GET /v1/evaluations/operational`
  runs, summed over the execution's runs as that route sums a repeated
  comparison. A composition of existing evidence; additive in version 2.
- **D11 stands.** The control plane serves JSON only; the Markdown is rendered
  only by the one renderer.

The task is delivered in three pull requests: **107-A** (this section's
decisions, the snapshot, the report route, its display strings, `eval report`
and D12), **107-B** (the renderer moves to `internal/cirender`, renders version
2, and `eval report` gains Markdown) and **107-C** (Change Impact and the
Overview panel).

## What shipped (107-A)

### Delivered

1. **Schema 14 and the snapshot.** `ScenarioGateRecord`, stored as
   `gate_snapshot` on `platform_scenario_executions`: fixed-shape canonical
   JSON, every field always present, no maps. Written by the same `UPDATE` as
   the verdict; a completion without one is refused. On restore it is decoded
   strictly, re-validated against itself (outcomes against their own actual
   and bound, bounds against the limits they name, frequency states against
   whether their limit was supplied, the verdict against the checks) and
   against its row (verdict column, N), and re-encoded — text that is not
   byte-identical is refused as corrupt. v13 → v14 is forward-only and
   backfills nothing. The chained upgrade from v10 reaches v14 on both
   backends.
2. **The recovery drill**, both directions, both backends: v10 (written by
   the real v0.11.0 `trustvian-local`, now with a completed execution), v12 and
   v13 backups restore into this binary and migrate, the execution reading its
   gate not recorded; a v14 backup restores and re-proves the snapshot, equal
   to what the completion returned. All eight legs pass locally (PostgreSQL 17,
   `pg_dump`/`pg_restore` 18).
3. **`GET /v1/scenario-executions/{execution_id}/report`**, result document
   version 2, published in
   [`compatibility.md` § Result document version 2](../../compatibility.md#result-document-version-2).
   Version 1 member for member — built by the same `newCompareRepeatedResponse`
   completion uses — with the recorded gate and `gate.state`. `409` for a
   running or failed execution, `404` for an unknown one.
4. **Display strings**, produced by the control plane
   (`platform.RenderPresence`, `RenderCallsPerRun`, `RenderCallsPerRunMean`,
   `RenderMilli`, `RenderCallRatio`), beside the integers they format, on every
   behavior and target row; and **`present_in`** per behavior.
5. **`trustvian eval report <execution-id> [--json]`**, printing the document
   unchanged; exit `0`/`2`/`3` as the rest of the eval family.
6. **D12** on `POST /v1/evaluations/compare`.
7. ADR 0069; `compatibility.md` (the version 2 contract with a pinned full
   example, the route, the CLI row, D12, schema 14); `platform-cli.md`;
   CHANGELOG; PERFORMANCE; the v0.12 index and ROADMAP.

### Departures from the specification, and why

- **`gate.frequency_checks` holds four checks, not three.** 081 added
  `max_llm_calls_per_run` after this specification was written. The report
  carries all four; § 1's "the three `frequency_checks`" is stale.
- **No `format=md`, no `--format`** (D11). `eval report` takes `--json` only,
  and both modes print the JSON.
- **Two version 1 members were never persisted** and read `not_recorded` rather
  than being guessed: `producers.cli_version` (the CLI that ran the execution)
  and, for a reused reference, `reference.mode` (`last` or `execution`). Both
  are gaps in what 078 records, not in this task; the version 1 subset test
  covers every other member.
- **`present_in` uses the repetition index the rest of the document uses**,
  1-based, with each repetition's run: `{"index": 1, "run_id": "…"}`, not the
  0-based bare integers of § 1's sketch.
- **An execution completed before v14 reads every `classification` as
  `not_recorded`, and has no `gate_limits`.** Classifying needs k and j, which
  were not kept. Presence, frequency, fidelity, operational sections, targets,
  sameness and suggestions — none of which needs a limit — are still reported.
- **`operational.cost_reason: "pricing_not_configured"`** is new: without it
  an absent cost section would say nothing about why.
- **The mean is rendered exactly**, `2456` → `"2.456"`, rather than to one
  decimal: no digit is rounded away, so the string says what the integer says.
  The ratio keeps `RenderRatio`'s one truncated decimal (`"2.4×"`), the
  rendering 106's suggestion texts already use.
- **The behavior rows are reduced again, by today's code**, from the recorded
  runs' persisted snapshots. Only the gate is a record. ADR 0069's
  consequences say what a disagreement would look like.
- **Per-target latency, status classes and 429s** (§ 1's Targets columns) are
  not in the report: the repeated comparison carries 087's sections per side,
  and the per-target breakdown is `GET /v1/evaluations/operational`'s. The
  report adds no evidence (constraint), so 107-C must read that route, or a
  later task must put it on the comparison.

### Measured

**Storage and latency** ([PERFORMANCE.md](../../PERFORMANCE.md#v012-task-107-a-report-identity-and-json)):
`gate_snapshot` is 1,654 bytes at 64 runs a side with no optional limit and
12,424 bytes at every bound. The report route at the repetition bound (64 runs
a side, 16–18 behaviors each, file-backed SQLite) takes 26.4–28.0 ms and
returns 155,690 bytes; compare-repeated over the same runs takes 26.0–26.3 ms.

**The demo agent on Ollama.** `trustvian-python-agent-demo` at `291fbc9`
(with #13's GenAI `chat` span), `opentelemetry-instrument`, Ollama 0.34.4,
`gemma3:4b` on both sides, Apple M3 Pro, this branch's `trustvian`,
`trustvian-collector` and `trustvian-local` with a pricing table, `runs: 3`,
three tickets a run. The reference side ran `SUPPORT_AGENT_MODE=reference`, the
candidate side `candidate` (which adds `export_customer`). Every optional limit
was set, `max_calls_per_run` naming both `ollama` and `ollama.localhost`.

- A first attempt changed the model instead, `qwen2.5:3b` on the candidate
  side. Its first candidate repetition looped on `knowledge_search` and
  `send_email`, hit the agent's 14-step limit and exited 1, so `eval run`
  stopped with exit 3 and recorded the execution failed. Its report is
  `409 conflict` (`… is failed; only a completed execution has a report`), as
  specified. No verdict was produced and none is claimed.
- Execution `scn-support-candidate-change-20261011T105949-1fc68faf` completed
  FAIL (exit 1). `gate_snapshot` stored 1,864 bytes. `trustvian eval report
  --json` printed 41,254 bytes, exit 0. The live route answered in 3–16 ms.
- The version 1 subset of the report equals the `eval run --json` document
  for the same execution except `version` and `producers.cli_version`, which
  reads `not_recorded`.
- **Sameness:** `same_scenario`, `same_inputs` and `same_model` are `"true"`
  (both sides `gemma3:4b`, one `scenario_digest`), `same_prompt_ref`
  `"not_recorded"`, `warnings: []`.
- **A behavior row**, `tool/export_customer`: `reference_runs_present` `"0"`,
  `candidate_runs_present` `"3"`, `classification` `added`; `display`
  `{"reference_presence": "0/3", "candidate_presence": "3/3",
  "reference_calls_per_run": "0–0", "candidate_calls_per_run": "1–1",
  "reference_calls_per_run_mean": "0", "candidate_calls_per_run_mean": "1"}`;
  `present_in.reference` `[]`, `present_in.candidate` repetitions 1, 2 and 3
  with their runs; candidate fidelity `semantic`, `layer.tool` 3.
- **The model call is counted under two targets**, as the demo's README says:
  `ollama` (the `chat` span) and `ollama.localhost` (its HTTP request), each
  20 calls a run on both sides, `call_ratio` `"1.0×"`. The report shows both
  rows, and `max_calls_per_run` evaluates both (`20` against `30`, `pass`
  twice). They are two behaviors too: `external · gemma3:4b → ollama`,
  semantic, layer model, and `http · POST → ollama.localhost`, transport. The
  report does not add them up anywhere, and it does not merge them:
  `max_llm_calls_per_run` reads only the model layer, `actual` `"20"`, and the
  tokens (`observed` `60` a side, 20 per run) come from the `chat` spans only.
  A reader adding the two target rows would count 40 model calls a run.
- **Tokens:** reference input 39,354, output 8,232; candidate input 42,552,
  output 9,504; `delta` input +3,198, output +1,272; `observed` 60,
  `unobserved` 162 on each side. **Cost** (illustrative prices, with
  `pricing_version` `measurement-107a`, its digest and source): 7,228 and 8,056
  micro-USD, `delta` +828.
- **`max_llm_calls_per_run`:** `{"state": "evaluated", "rule": "at_most",
  "actual": "20", "bound": "30", "passed": true}`.
- The verdict FAIL comes from `repeatedly_added_behaviors` (2:
  `export_customer` and `POST → export.localhost`), `min_candidate_frequency`
  (0 against 2) and `max_lost_behaviors` (2 against 0: `escalate_ticket` and
  `POST → escalation.localhost`, 3/3 and 0/3). Four suggestions fired: two
  `compare.added_in_all_runs`, two `compare.lost_in_half`.

### Proven by test

- The snapshot: built from a real reduction with evaluated, deferred and
  per-target checks, it round-trips through its text; the encoding is pinned
  byte for byte; nineteen kinds of damage (malformed, non-canonical, padded, a
  field missing, a contradicted outcome, a bound that is not its limit, an
  unknown state, another N, a flipped verdict …) are refused as corrupt;
  contradictory parts are refused at construction; a record is a copy; a
  completion without one is refused; a stored record that disagrees with its
  row is refused when the execution loads.
- Schema 14: v13 → v14 and v10 → v14 on both backends, with a completion after
  the migration; fresh and migrated tables agree; every schema statement is
  attributed to its version.
- The report: its version 1 subset equals the document `eval run` builds from
  the completion; a reused reference is named with `mode: not_recorded`;
  running, failed and unknown executions are refused; a pre-v14 execution
  reports `gate: {"state": "not_recorded"}`; the cost reason; SQLite and
  PostgreSQL return byte-identical reports; the version 2 example in
  `compatibility.md` equals the route's output.
- The display strings equal the formatting of the integers beside them; the
  mean rendering is exact; `present_in` lists exactly as many repetitions as
  each presence count, never a repetition that did not complete; every row
  resolves through `GET /v1/evidence/observations` from every run it names.
- D12: a recorded pair states both recorded values and its warning, with a
  gate byte-identical to an unrecorded pair's; an unrecorded pair reads
  `not_recorded` with `warnings: []`.
- `eval report`: the request path, the body printed unchanged in both modes,
  and its usage and API-failure exit codes.

### Current state rows found stale on `main` (`a3dcaf9`)

| Row | On `a3dcaf9` |
|---|---|
| Comparisons are not persisted, `platform/controlplane.go:1398-1411` | Still true, but the lines moved: `EvaluationComparison` is at 1451, `CompareEvaluations` at 1469–1561. 1398–1411 is now `EvaluationProgress` |
| Overview has five panels, `index.html:221-227` | The file is `platform/webui/assets/index.html`, and the five panels are at 248–252 |
| The CI renderer, `render.go:186-360` | The file is 358 lines: `noVerdict` 186, `renderScenario` 223, `renderSuite` 312–358 |
| Scenario executions persist no gate limits or check results (ADR 0054 § 1, § 4) | True on `a3dcaf9`; no longer true after this task (D10) |

The other rows — `api.js:362-367`, `render.js:769-782`,
`document.go:3-14`, `evidence.go:90-104` and `overview.js:3-11` (still "Six
panels" listing five) — hold as written.

## What shipped (107-B)

### Delivered

- **Schema 15 (D14).** `platform_scenario_executions` gains `cli_version` and
  `reference_mode`, nullable, on both backends, forward-only, no backfill.
  `POST /v1/scenario-executions` takes an optional `cli_version` (shared text
  bounds, not blank, not padded); the mode is the one the begin request asked
  for. Restore refuses an empty value, an unknown mode or a mode without a
  reference. The report's `producers.cli_version` and `reference.mode` read
  the recorded values, and `not_recorded` only for an execution begun before
  v15. `eval run` sends its version on begin. The v10 upgrade chains to v15,
  and the recovery drill gained a v14 leg and restores at v15.
- **D16.** Every report target carries `operational.latency` and
  `operational.errors`, per side with `runs_with_evidence`, from one
  accumulation (`targetOperationalRows`) that `GET /v1/evaluations/operational`
  now shares, over the completed repetitions the comparison counts.
- **One renderer.** `cmd/trustvian-ci-render`'s document half moved, unchanged,
  to `internal/cirender`; the command keeps reading and validating the
  artifact. Every version 1 golden file renders byte-identically.
- **Version 2 rendering** in `internal/cirender`: strict parsing and the
  sections listed in § 2, with every figure a string or integer from the
  document, and shedding to the 60,000-byte limit with each shed part named.
- **Six golden version 2 documents**, each the route's output for a fixture
  execution recorded through the real routes, and their Markdown.
- **`trustvian eval report --format md`**, and `trustvian-ci-render --document
  <file>`, byte-identical for every golden document.
- **D15.** `eval run --json` prints the execution's report; suite members
  embed theirs; a control plane without the route keeps version 1, and stderr
  says so.
- **Docs:** ADR 0057's pointer, `docs/compatibility.md`, `docs/platform-cli.md`,
  `docs/ci-github-action.md`, the CHANGELOG, PERFORMANCE, and this section.

### Departures from the specification, and why

- **The comment and `eval report --format md` are not byte-identical; the
  document rendering is.** The comment names the commit, the run link and the
  CLI exit code, which `eval report` cannot know. So the comment is the bare
  rendering with those three lines after its heading, and the bare rendering
  is what `eval report --format md` and the new `trustvian-ci-render
  --document` print, byte for byte. Tests pin both halves.
- **The action's posted comment is still version 1.** The run and comment
  actions build the commit `runtime.env` pins (ADR 0056, ADR 0058), which must
  be a commit of `main` and predates this task. Moving the pin is a separate
  reviewed change once this merges. Until then the end-to-end workflow's new
  `head-runtime` job runs the action's `run.sh` with this commit's runtime,
  requires the artifact to be the version 2 report, and requires its comment
  to be the version 2 rendering, framed.
- **D15's fallback is wider than `404`.** Any failure to read the report
  prints version 1 and names why on stderr, because the verdict is already
  committed and version 1 carries it. A report that contradicts the completion
  (another execution, version, verdict, or a gate not recorded) is printed
  nowhere and exits `3`.
- **The suite document stays version 1.** Its members embed version 1 or 2;
  the renderer reads either.
- **Per-target tokens are not on the report.** D16 names latency, status
  classes and 429s; tokens stay run-level, where cost is computed.
- **A no-fit report under `eval report --format md` exits `3`**, like a
  refused one: nothing was rendered.

### Measured

A real model change on Ollama 0.34.4, Apple M3 Pro, the demo agent at
`7d86e8a`: `gemma3:4b` (reference) against `llama3.2` (candidate), both chosen
by `model_env: OLLAMA_MODEL`, `runs: 3`, execution
`scn-support-model-change-20261012T154532-3ff6efc3`. `eval run --json` exited
`1` (FAIL) and printed a 63,260-byte document; `eval report --json` printed the
same bytes. `eval report --format md` printed 15,537 bytes, identical to
`trustvian-ci-render --document` on `eval run --json`'s output.

- **Sameness:** scenario `same`, inputs none declared on both sides, model
  `gemma3:4b` against `llama3.2` **differs**, prompt reference not recorded;
  one warning, `model_differs`.
- **A behavior row:** `tool · billing_lookup` — `0/3` → `3/3`, calls per run
  `0–0, mean 0` → `2–2, mean 2`, added, not lost, candidate fidelity
  semantic 6; `present_in` names candidate repetitions 1–3 with their runs.
- **Targets:** `ollama` 20 → 18 calls per run (`0.9×`); latency `≤2.5 s 3 ·
  ≤10 s 57` (60 observed) → `≤2.5 s 53 · ≤10 s 1` (54); no HTTP status (the
  GenAI span states none). `ollama.localhost` the same calls and buckets, with
  `2xx` 60 → 54 and no 429.
- **Tokens:** input 39,354 → 24,288, output 8,232 → 5,364. Cost 7,228 → 2,287
  micro-USD (−4,941) under illustrative prices (`measurement-107b`).
- **Gate:** FAIL, as recorded — repeatedly added behaviors 3 (≤ 0), minimum
  candidate presence 0 (≥ 2), lost behaviors 3 (≤ 0); calls per run to
  `ollama` and `ollama.localhost` 18 (≤ 30); model calls per run 18 (≤ 30).
- **Recorded facts:** `producers.cli_version` is the CLI's own version string;
  the execution reused no reference.

The report route at the bound, the renderer's cost and the at-bound document's
Markdown are in [PERFORMANCE](../../PERFORMANCE.md).

### Proven by test

- D14: the version validation table; a mode only beside a reference; a round
  trip and five kinds of stored damage; v14 → v15 and v10 → v15 on both
  backends; fresh and migrated columns agree; the service records the mode
  asked for; the route refuses a malformed version; the report carries it, and
  the version 1 subset test now covers `producers` too — only `version`
  differs.
- D16: every report target's sections equal the operational route's for the
  same runs.
- Rendering: the published example renders every section; out-of-contract
  values are refused (25 cases); absent, null, zero and false stay distinct;
  hostile strings stay inert; shedding keeps the gate, lost and classified
  behaviors and sameness; a gate not recorded shows no check or verdict; the
  comment is the bare rendering framed; suites embed reports.
- The goldens: each is the route's output, byte-identical on PostgreSQL, still
  shows its case, and renders to its Markdown; every golden file has a source.
- `eval report --format md` equals `trustvian-ci-render --document` for all
  fifteen golden documents (six version 2, nine version 1).
- D15: the report is printed unchanged; `404` keeps version 1 and says so; four
  contradictions exit `3`; suite members embed their reports.

### Current state rows found stale on `main` (`836eb81`)

| Row | On `836eb81` |
|---|---|
| Comparisons are not persisted, `platform/controlplane.go:1398-1411` | Still true; `EvaluationComparison` is at 1451 and `CompareEvaluations` at 1476. 1391 is `EvaluationProgressReport` |
| Overview has five panels, `index.html:221-227` | `platform/webui/assets/index.html:248-252` |
| Scenario executions persist no gate limits or check results | No longer true since 107-A (D10) |
| The CI renderer, `document.go:3-14`, `render.go:186-360` | True on `836eb81`; after this task the document and rendering code live in `internal/cirender/document.go` and `render.go` |

`api.js:362-367`, `render.js:769-782`, `evidence.go:90-104` and
`overview.js:3-11` hold as written.

## What shipped (107-C)

### Delivered

- **Compare became Change Impact.** The destination lists the context's
  recorded scenario executions, one page at a time, newest first, and renders
  a completed one's report in one table per section (§ 1). Evidence →
  Scenarios and the Overview link to it, and `#execution=<id>` opens one
  report with a single read.
- **Every figure is the report's own.** Each sits in an element whose
  `data-source` names the member it came from; a closed-vocabulary label that
  contains digits carries `data-vocab`.
- **Every behavior row resolves through 085.** Its `present_in` repetitions are
  controls; one opens that repetition's observations of the behavior in
  Evidence, for that repetition's pair of runs.
- **The single-run comparison** renders D12's sameness and warnings, and says
  frequency and repeated fidelity are not applicable — not a scenario
  execution.
- **Overview's sixth panel, Latest comparison** (§ 3): the newest execution in
  the context, its recorded verdict or *not recorded*, the four sameness
  answers, its added, removed and lost behavior rows, the first suggestion and
  a link to Change Impact; its own read time, Refresh, no timer, a late answer
  discarded. The "Six panels" comment is now accurate.

### Departures from the specification, and why

- **The Overview's counts were tallied in the browser** from the report's
  rows, against exit criterion 7. Corrected when the milestone closed: the
  report now carries `comparison.counts`, computed by the control plane, and
  the panel shows those; the browser tally is gone.
- **The browser check is a Chrome test that skips without a browser**, like
  the PostgreSQL tests without a database: the WebUI has no JavaScript test
  runner beyond the node driver, and a rendered DOM needs a browser.
- **A behavior's evidence link pairs runs by repetition index**: repetition *i*
  resolves against reference *i* and candidate *i*. The reader chooses *i*.

### Proven by test

- In Chrome, for three golden reports (model change, gate not recorded,
  deferred gate): every sourced figure equals its report member, and no other
  digit is rendered; every section is present; a gate not recorded shows no
  check and no verdict.
- Statically: the module contains no arithmetic or numeric formatting, reads
  the display strings rather than deriving them, and the existing guards —
  no uint64 parsed as a number, no markup parsed, every await followed by an
  ownership check, no timer — hold for the new code.
- The panel reads `comparison.counts`, and no browser-side report tally
  exists (`TestOverviewLatestComparisonReadsTheReportsCounts`); the counts
  equal the rows' totals, recorded or not (`TestReportCountsAreTheRowsTotals`).

### Checked by hand

Headless Chrome against the real model-change execution
(`scn-support-model-change-20261012T154532-3ff6efc3`, served by this branch's
`trustvian-local`): desktop in the light and dark schemes rendered every
section; the wide target table scrolls inside its own wrapper. At 390 px,
Chrome's headless window does not go below its minimum width, so the
screenshot is a crop of a wider layout — the existing Status view crops the
same way — and the phone layout was not proven by it.
