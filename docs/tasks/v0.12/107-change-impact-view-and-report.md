# 107 — Change Impact View and Model Change Report

Status: Specified; not implemented
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [086](086-scenario-and-input-versioning.md),
[087](087-performance-and-cost-evidence.md), [106](106-frequency-evidence.md),
[081](081-persisted-behavior-fidelity.md), [085](../v1.0/085-evidence-resolution.md)
(implemented), [079](../v1.0/079-ci-integration-github-action.md) (implemented),
[099](../v0.11/099-overview-dashboard.md) (implemented)
Decision records: [ADR 0036](../../adr/0036-webui-is-a-same-origin-adapter-over-v1.md),
[ADR 0057](../../adr/0057-the-ci-renderer-is-a-standalone-offline-transcriber.md),
[ADR 0064](../../adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md);
a new ADR for the report's identity (see
[§ Conflict: a comparison has no identifier](#conflict-a-comparison-has-no-identifier))
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
| Sameness | 086 `sameness`, `warnings[]` | scenario, inputs, model, prompt: same / different / not stated, with digests |
| Behaviors | 078 presence + 106 frequency + 081 fidelity | behavior · k/N reference · k/N candidate · calls/run (min–max, mean) per side · classification · fidelity (with `mixed`) |
| Targets | 106 per-target + 087 operational | target · calls total per side · ratio ‰ · latency buckets per side · status classes · 429s |
| Tokens and cost | 087 `tokens`, cost with provenance | per side; cost only with `pricing_version` and `source` |
| Gate | 056/078/106 checks | every check, including `not_evaluated` and `deferred` |
| Suggestions | 105/106 `suggestions[]` | `rule`, `text`, evidence |

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
are), and has new golden files for version 2.

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
