# 087 — Performance and Cost Evidence

Status: Implemented — see [What shipped](#what-shipped)
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [084](../v1.0/084-correlation-operational-evidence.md) (implemented),
[075](../v1.0/075-ai-semantic-telemetry-normalization.md) (implemented)
Blocks: [106](106-frequency-evidence.md)'s 429 rule and
[107](107-change-impact-view-and-report.md)'s latency, errors and tokens columns
Planned in: [082 § 087](../v1.0/082-agent-inspection-and-evaluation-depth.md#087--performance-and-cost-evidence).
That section is the baseline. Everything it states is copied below and then
extended. Each place this file departs from it is marked **Amends 082**.

## Developer problem

*From 082, unchanged:* The candidate passed every behavioral gate, and made the
agent twice as slow and three times as expensive. Trustvian says nothing about
it, so the team finds out from a bill or from a user.

*Extended:* After changing a model, a prompt or a tool, a developer wants to
know **where** the time and the errors went. They want to know which target got
slower, which started returning 429s, and how many tokens the model calls used.
A run-wide sum does not answer that.

## Current state, verified against `main` (`d1ad5f6`)

| Capability | State | Evidence |
|---|---|---|
| Duration and status on the record | **Implemented** (084) | `DecisionRecord.duration_nanos`, `span_status` |
| Run-level duration and status aggregates | **Implemented, per run only** | `DurationSummary{Count, Unobserved, Sum, Min, Max}`, `SpanStatusCounts{Unavailable, Unset, OK, Error}` (`platform/operational.go:31-115`); persisted as nine columns (`platform/evidence_columns.go:55-62`) |
| Per-target or per-behavior duration / status | **Absent** | `BehaviorEntry` is `{FingerprintID, Behavior, Observations}` (`platform/behavior.go:90-101`). Per-observation duration and status exist only on 067's retained `Observation`, which keeps at most 4096 per run and is not an aggregate |
| Operational evidence on any HTTP response | **Absent, by 084's decision** | no duration or status field in `platform/httpapi/dto.go`. 084 § *Where the summaries are exposed* left the HTTP shape to this task |
| HTTP response status code | **Absent** | not read by `internal/otel`, the processor, `internal/features` or the platform |
| Token usage | **Unconsidered** | `gen_ai.usage.*` and `llm.token_count.*` are neither read nor refused (`docs/OPENTELEMETRY.md:474`) |
| Pricing configuration | **Absent** | no pricing code. The platform has no YAML. Schema-versioned YAML is parsed only in the root `config` package |
| Scorecard sections for any of this | **Absent** | `EvaluationScorecard` (`platform/scorecard.go:258-281`) has decisions, risks, approvals, policy, metrics and behavior |

**Re-verified against `fa6a2d9` before implementation.** None of the cited code
files changed between `d1ad5f6` and `fa6a2d9`, and every file:line above still
holds, with two exceptions:

- The token row's `docs/OPENTELEMETRY.md:474` had moved to `:502`.
- The "operational evidence on any HTTP response" row was already inaccurate
  at `d1ad5f6`. 067's observation DTO (`platform/httpapi/dto.go:1010-1016`)
  publishes per-observation `duration_nanos` and `span_status`. What was absent
  was *aggregate* operational evidence on a response, which is what this task
  adds.

## Scope

*From 082:* latency and error comparison from 084's recorded evidence; token
counts from the GenAI and OpenInference usage attributes; cost only from an
explicitly versioned, operator-supplied pricing table; **unavailable is not
zero** everywhere.

*Extended into concrete evidence:*

### 1. Per-behavior operational evidence, persisted

Each `BehaviorEntry` gains one fixed-shape, `O(1)` operational summary. It is
updated in the same ingest transaction that already writes the entry:

```text
duration buckets   11 uint64 counters, integer milliseconds, upper-inclusive:
                   ≤1 · ≤5 · ≤10 · ≤50 · ≤100 · ≤250 · ≤500 · ≤1000 · ≤2500 · ≤10000 · >10000
duration_unobserved            uint64
duration_sum_nanos, min, max   uint64 (084's DurationSummary, per behavior)
span status                    unavailable · unset · ok · error  (084's vocabulary)
http status class              1xx · 2xx · 3xx · 4xx · 5xx · unavailable
http_429                       uint64  — counted separately because 106's rule needs it
tokens_input, tokens_output    uint64 sums
tokens_observed, tokens_unobserved   uint64 — records with / without usage attributes
```

Invariants, enforced on write and on restore exactly as 084 enforces them:

- each bucket set partitions the behavior's observation count, summed
  overflow-safely;
- `tokens_observed + tokens_unobserved == observations`;
- `http_429 ≤ 4xx`.

"Per target" is an aggregation over behaviors that share `target_name` and
`target_category`. The control plane computes it on read, from the per-behavior
summaries. It is never stored separately and never computed by an adapter.

**Bounded by construction.** A snapshot holds at most 512 entries
(`maxBehaviorEntries`, `platform/behavior.go:42`), so this adds a fixed number of
counters per entry and no new unbounded collection.

### 2. Status class and tokens reach the platform on the envelope, not the record

The two new inputs ride on the ingest envelope next to `fidelity` and
`behavior_layer`. They do not go on `DecisionRecord`.

| Envelope field | Read from | Rule |
|---|---|---|
| `http_status_code` | `http.response.status_code`, legacy `http.status_code` | an integer 100–599. Anything else is absent. Never inferred from span status |
| `tokens_input` | `gen_ai.usage.input_tokens`, legacy `gen_ai.usage.prompt_tokens`, `llm.token_count.prompt` | a non-negative integer. A malformed value is absent, never `0` |
| `tokens_output` | `gen_ai.usage.output_tokens`, legacy `gen_ai.usage.completion_tokens`, `llm.token_count.completion` | as above |

`llm.token_count.total` is read only when both parts are absent, and is then
recorded as `tokens_unsplit`. A total is never split into input and output.

**Why the envelope:** fidelity and layer already established this route (ADR
0047 § 2). These are adapter facts about a span, not engine evidence. Putting
them on the envelope keeps the engine and `DecisionRecord` unchanged, which is a
milestone constraint. The consequence is stated plainly: **the in-process SDK
path (`internal/otel`) produces no envelope, so a run ingested that way reports
status classes and tokens as unavailable.** That is the honest outcome, and it is
not a defect to work around.

### 3. `internal/semconv` and `docs/OPENTELEMETRY.md`

- A **usage table** in `internal/semconv` lists the token keys above in one
  place. These keys are string literals, for the reason ADR 0045 records about
  the vanished `genaiconv` package.
- `docs/OPENTELEMETRY.md` § *What is neither read nor refused* loses the token
  row. The mapping table gains a "Usage" section and a "Status code" row.
- `TestContentAttributesAreNeverRead` stays as it is. Usage keys are not content
  keys, and a new test asserts the two sets are disjoint.
- **Token counts are read from the producer's usage attributes, and from
  nothing else.** Counting tokens in prompt or completion text is refused
  (082 criterion 8).

### 4. New fixed-shape scorecard sections

`EvaluationScorecard` gains three sections, fixed shape in the sense of ADR
0028: closed structs with no maps and no slices.

```text
latency    reference / candidate: the run-level 11 buckets, unobserved,
           sum, min, max (nanoseconds); no mean, no percentile
errors     reference / candidate: span status counts, http status class
           counts, http_429
tokens     reference / candidate: input, output, unsplit sums; observed and
           unobserved record counts
```

A section whose evidence is unavailable on either side carries
`"comparable": false`, with a `reason` from a closed set
(`reference_unavailable`, `candidate_unavailable`, `both_unavailable`), and has
**no delta fields at all**. 082's criterion 2 says a comparison involving such a
run reports no delta. This is how that is enforced: the field does not exist,
so it cannot be rendered.

**The run-level sections are fixed shape. The per-target table is not.** That
is why the per-target view is a separate read rather than part of the
scorecard:

```text
GET /v1/evaluations/operational?reference_run_id=…&candidate_run_id=…&after=…&limit=…
```

It returns one row per target, with both sides' buckets, status classes and
tokens, ordered by target identity and paginated in the repository's single
keyset shape. A repeated comparison (078) gets the same table summed across
its N runs per side. Each row carries `runs_with_evidence` per side so that a
target seen in 3 of 10 runs is not read as if it were seen in all of them.

### 5. Cost, optional, with provenance

- **Configuration.** A schema-versioned YAML document parsed by the root
  `config` package. That is the only place `go.yaml.in/yaml/v3` may be
  imported. `trustvian-local` takes it with `--pricing <file>`:

  ```yaml
  version: 1
  pricing_version: "team-2026-10"
  source: "copied from provider pricing page, 2026-10-01"
  currency: USD
  models:
    - model: llama3.2            # matched against the model-layer behavior's operation name
      input_micros_per_million_tokens: 0
      output_micros_per_million_tokens: 0
  ```

  Prices are **integer micro-units per million tokens**, so cost is integer
  arithmetic. A floating-point price would make the same records cost
  different amounts after a reordering, which ADR 0029's reasoning excludes.
- **Computation.** On the control plane, at read time:
  `cost_micros = Σ tokens × price / 1 000 000`, accumulated in 128 bits and
  rounded down at each model, with overflow reported as an error and never
  wrapped. A model with no price contributes `unpriced_tokens`. It is never
  given a guessed price.
- **Provenance travels with every figure.** Each cost carries
  `pricing_version`, `pricing_digest` (SHA-256 of the canonical document) and
  `source`. A renderer that has a cost but not its provenance renders neither
  (082 criterion 4).
- **No pricing configured** means no cost section and no error. Nothing else in
  the response changes (082 criterion 6).
- **No bundled prices**, and **no cost gate** (082 non-goals, unchanged).

### 6. Persistence

One forward-only schema step on both backends. It adds the per-behavior
operational columns to `platform_behavior_entries` and nothing else. Existing
rows are migrated as *unavailable*, as 084's step 5 → 6 did: every
`*_unobserved` / `unavailable` counter is set to the entry's observation count,
and every other counter is set to `0`. The step's number is the next free
version when this lands (see [§ Schema numbering](#schema-numbering)).
Backup, restore and upgrade are extended to the new columns.

## Non-goals

*From 082, unchanged:* no model benchmarking and no provider comparison; no
bundled pricing data; no statistical significance testing; no cost gate in the
first slice.

**Amends 082:** 082 says "No latency percentiles or distribution statistics".
This task keeps *no percentiles*, and adds a **fixed-boundary bucket count**.
The argument:

- A bucket count is an integer per fixed interval. It is order-independent,
  reproducible and needs no float, which are the properties ADR 0029 wants and a
  percentile estimate does not have.
- Min/max/sum alone cannot show the thing a developer asks about: "most calls
  are fine and a few now take ten seconds". Eleven integers can.
- No percentile is derived from the buckets in this slice. Adding p50/p95
  later would need its own argument here, and would be reported as a bucket
  boundary ("p95 ≤ 2500 ms") rather than as an interpolated number.

Also out of scope: **no latency attribution**. 082 notes that latency through a
model-driven agent is dominated by the model. Every rendering of latency says
it is descriptive, and makes no claim about the change under test.

## Technical requirements

1. Nothing added here enters `StableFeatures`, a fingerprint or a baseline key.
   A test proves it (082 criterion 7).
2. A producer that emits no usage attributes, no status code and no duration
   gets byte-identical behavioral results, decisions and existing aggregates.
   The degradation suite is extended to prove it.
3. Ingest cost per record grows by a bounded constant. This is benchmarked
   (`BenchmarkIngestWithOperational…` against the existing ingest benchmark),
   and any new allocation on the no-evidence path is fixed before merge.
4. SQLite and PostgreSQL return identical results for every new read.

## Tests

- Golden tests over a fixed record set and a fixed pricing version: buckets,
  status classes, tokens and cost, on both backends (082 validation).
- A run with no token attributes renders *not available*, not `0`. A comparison
  with it has `comparable: false` and no delta field.
- Removing the pricing configuration removes the cost section, and nothing
  else changes byte for byte.
- Cost reproducibility: identical records and pricing version give an identical
  figure across ingest orders.
- Bucket boundaries: exactly-on-boundary durations, a 0 ns duration (observed),
  and an unobserved duration.
- Migration from the previous schema marks existing entries unavailable and
  keeps every invariant.
- A privacy tripwire: sentinel content in every refused attribute, asserted
  absent from the new routes and sections.

## Acceptance criteria

1. *(082 #1)* A comparison reports reference and candidate latency summaries
   and error counts when the telemetry supplied them, at run level and per
   target.
2. *(082 #2)* A run whose producer supplied no duration, status or token counts
   reports each as unavailable, and no comparison involving it reports a delta.
3. *(082 #3)* Token counts are read only from the documented usage attributes,
   and `docs/OPENTELEMETRY.md` lists them.
4. *(082 #4)* A cost figure is never rendered without its pricing version and
   provenance.
5. *(082 #5)* Cost is reproducible, proven by test.
6. *(082 #6)* Absent pricing produces no cost and no error.
7. *(082 #7)* No token count, duration, status code or cost enters
   `StableFeatures`, a fingerprint or a baseline key.
8. *(082 #8)* No content is read to obtain a token count.
9. *(new)* HTTP status classes and a 429 count are reported per target, and are
   *unavailable* for spans without a status code. Span status is never used as a
   stand-in.
10. *(new)* The engine and `DecisionRecord` are unchanged.

## Schema numbering

The roadmap assigns each schema step the next number in landing order (082
§ *Migrations stay forward-only and drilled*). If this task lands first, as the
proposed phase order has it, it takes **v11**. Task 081 was asked to be "schema
v11", and it then becomes a later number. See
[081 § Schema numbering](081-persisted-behavior-fidelity.md#schema-numbering).

## Risks

*From 082:* a cost number is a figure people quote, and a wrong one damages
trust in every other number. Hence provenance as a criterion. Latency through a
local model is dominated by the model.

*New:* per-behavior operational columns roughly double the width of
`platform_behavior_entries`. That is bounded (512 rows per run) but it is real,
and the storage footprint per run is measured and recorded in `PERFORMANCE.md`.

## What shipped

All of the scope above, in seven commits: the attribute readers, the
processor envelope, persistence, the scorecard sections, the per-target read,
cost, and the proofs. The differences below are each a decision taken while
building.

| Specified | Shipped | Why |
|---|---|---|
| Schema 11 "adds the per-behavior operational columns" | One column, `operational_counts`: the 31 counters as canonical decimal text, comma-separated in a documented order, decoded without allocating and validated exactly as 31 columns would be | Measured. Every ingest rewrites the run's entries and reads them back three times. With a column per counter, `IngestDecisionRecord` at 64 behaviors went from 1.69 ms, 498 KB and 9.5k allocations to 3.70 ms, 2.28 MB and 43.7k — almost all of it the SQLite driver decoding columns nothing queries. With one column it is 1.94 ms, 790 KB and 10.5k. See [PERFORMANCE.md](../../PERFORMANCE.md#v012-task-087-performance-and-cost-evidence) |
| "Unavailable on either side" | Per section. Latency is available when a duration was measured. Errors are available when any observation stated a span status other than *unavailable*, or an HTTP status code. Tokens are available when any observation reported a count | An unset span status is a stated outcome. An absent one is not, and treating it as success would let an unstatused run look clean (084) |
| Deltas | Every `delta` field is candidate minus reference of the same counter, as signed canonical decimal text, present only when the section is comparable | One rule, no derived figure. A signed uint64 difference does not fit an int64, so it is sign and magnitude |
| Cost "matched against the model-layer behavior's operation name" | Matched against the operation name of any behavior that reported tokens; every other behavior's tokens are `unpriced_tokens` | The layer is not persisted until [081](081-persisted-behavior-fidelity.md). A model call's operation name is its model (075), so the match is exact for model calls. A retrieval named like a priced model would be priced too, which no measured producer does |
| Rounding "at each model" | Per model per side: tokens are summed per model first, then priced once in 128 bits and rounded down. An unsplit total is never priced | Order-independent, proven by test. No input or output rate applies to a total |
| Cost overflow "an error" | `409 conflict` with `ErrCostOverflow` | The comparison exists; this pricing table cannot state its cost in uint64 micro-units |
| Cost placement | `scorecard.cost` on a comparison and `operational.cost` on a repeated comparison, both absent without `--pricing` | Absent rather than empty, so no pricing changes nothing else, byte for byte (tested on both routes) |
| Per-target read for repeated comparisons | `reference_run_id` and `candidate_run_id` repeat once per run, equal counts, no run twice on one side. The same run on both sides is allowed, as `compare` allows a self-comparison | One route for N = 1 and N > 1 |
| A CLI command printing the same JSON | `trustvian eval operational --reference-run … --candidate-run …`, repeatable, printing the body unchanged in both output modes | Named for its route, as `eval compare` is for `/v1/evaluations/compare`. It is the evaluations family's per-target read, and a body printed unchanged needs no second format |
| Token counts "a non-negative integer" | An integer from 0 to 2^32 per observation, as `event.MaxTokenCount`; larger is absent at the Collector and `400` at the control plane | Found in review. Without a bound, two records near 2^63 tokens make every later sum over that behavior overflow, and an overflowing sum refuses the comparison — gate included — that the evidence only sits beside. With it, overflow needs billions of records on one side. The same reasoning as 084's `MaxDurationNanos` |
| A cost figure per side | Each cost side also carries `token_observations` and `observations_without_tokens` | Found in review. A cost over 1 of 1,000 model calls is a figure, not a total, and the cost section must say so without a reader cross-referencing `tokens` |
| Restore re-proves "the invariants" | Also refuses a duration sum the bucket counts cannot produce (each count times its bucket's edges), beside 084's extrema bounds | Found in review: buckets ≤1, ≤5 and ≤10 ms with extrema 0 and 10 ms allow 16 ms at most, and the extrema check alone accepted 19 ms |
| `trustvian-local --pricing` | Validated before anything starts (a bad table is exit 2). Startup prints `Pricing: <version> (<digest>)` only when given | A runtime that started without the prices it was handed would publish comparisons silently missing their cost |

### Measured with a real model

A `trustvian dev` session from this branch ran the CrewAI quickstart twice,
against Ollama `llama3.2`:

- OpenInference CrewAI, OpenAI and requests instrumentors;
- `OPENINFERENCE_HIDE_INPUTS`/`OUTPUTS` set;
- `openinference-instrumentation-openai` 0.1.63, `-crewai` 1.1.18,
  `opentelemetry-instrumentation-requests` 0.66b0.

Read back through `trustvian eval operational` and `eval compare`:

| Target | Observations (ref / cand) | Duration | Status | Tokens |
|---|---|---|---|---|
| `openai` (the model calls, OpenInference `LLM` spans) | 4 / 4 | measured: 3 in ≤2500 ms, 1 in ≤10000 ms | span `ok`; **no HTTP status code** | **arrived**: 1,380 in / 239 out vs 1,356 / 217, 4 of 4 observed |
| `127.0.0.1` (the tool's HTTP calls, requests) | 4 / 4 | measured, all ≤5 ms | `2xx` 4 of 4, from `http.response.status_code` | none |
| unnamed (CrewAI agent and task spans) | 7 / 7 | measured | span `ok`; no status code | none |

With a fixture table pricing `llama3.2` at 100,000 / 400,000 micro-units per
million tokens, the reference cost 233 micro-units and the candidate 222. Both
check by hand: (1380·100000 + 239·400000)/10⁶ = 233.6, and
(1356·100000 + 217·400000)/10⁶ = 222.4, each rounded down.

What the real instrumentation emits:

- **Tokens:** OpenInference `openai` emits `llm.token_count.prompt`,
  `.completion` and `.total`. The parts outrank the total, so no total was read.
- **Status codes:** `requests` emits `http.response.status_code` under the opt-in
  `trustvian dev` sets, and `http.status_code` without it. Both are read.
- **Model calls carry no status code** in this setup. The openai SDK calls the
  model through `httpx`, which the harness does not instrument. 106's 429 rule
  therefore sees only instrumented HTTP spans, and never fires without them.

### Findings for the maintainer

- **Parent spans that aggregate their children's tokens double count.**
  OpenInference CrewAI puts `llm.token_count.total` on the crew-completed span
  (0 in this run), and the GenAI conventions allow usage on `invoke_agent`
  spans. Either counts its children's tokens again. Kept apart as
  `tokens_unsplit`, a total cannot inflate input, output or cost. It does
  inflate `unsplit` and `unpriced_tokens`. Restricting token sums to the model
  layer needs 081's persisted layer.
- **A Collector newer than its control plane is refused.** The ingest envelope
  is strictly decoded, so a pre-087 control plane answers `400` to a record
  carrying the new fields, as a pre-083 one does to `behavior_layer`.
  `trustvian dev` builds both from one release, so it never meets this. A
  separately deployed Collector must be upgraded after its control plane.
- **The ingest and compare benchmarks had failed at setup since task 065** (an
  unregistered environment), so no ingest number had been measured since. The
  fixture registers one now.
- **Two review findings are left as designed.** A replayed record keeps its first
  envelope's operational facts, as it keeps its fidelity: the digest is the
  record's, and a retry carrying different facts replays silently. And each
  page of `GET /v1/evaluations/operational` reloads every named run's snapshot,
  up to 128 of 512 entries each, with no cache.
- **The platform race suite with PostgreSQL took 874 s locally**, against a CI
  job timeout of 15 minutes that `main` used 14m47s of.

