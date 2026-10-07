# 105 — Pipeline Status Surface

Status: Implemented — see [What shipped](#what-shipped)
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [077](../v1.0/077-unified-otlp-local-dev-runtime.md) (implemented),
[075](../v1.0/075-ai-semantic-telemetry-normalization.md) (implemented),
[083](../v1.0/083-behavioral-layer-classification.md) (implemented)
Decision record: [ADR 0064](../../adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md)
for the suggestion table; [ADR 0065](../../adr/0065-collectors-report-pipeline-status-the-control-plane-holds-it-in-memory.md)
for the collector status report
Blocks: nothing. 107's Overview panel reads the same status route.

## Developer problem

A developer runs `trustvian dev -- python agent.py`, opens the WebUI, and sees
an empty Live view. An empty Live view can mean any of these:

```text
the agent did not start                     the exporter points at the wrong port
the agent ran but emitted no spans          spans arrived with no service.name
spans arrived but at HTTP fidelity only     the engine stopped learning at 512
```

Each has a different fix, and today the only way to tell them apart is to read
Collector logs and the source. A blank screen with no explanation is the first
thing many developers see, and it is where many of them stop.

## Current state, verified against `main` (`d1ad5f6`)

| Fact a developer needs | Exists today? | Evidence |
|---|---|---|
| A status / diagnostics route on `/v1` | **No** | routes registered at `platform/httpapi/handler.go:174-243` include none |
| `trustvian status` | **No** | subcommands at `cmd/trustvian/main.go:44-90` |
| `trustvian dev --check` | **No** | `dev` flags at `cmd/trustvian/dev.go:150-165`; the only `--check` is `evidence --check` |
| Collector health | Liveness only | `processor/internal/health/handler.go:12-13` (`/livez`, `/readyz`, port 13133) |
| Collector self-metrics | Exported as OTel metrics, never reported to the control plane | `processor/internal/metrics/metrics.go:196-250` — `trustvian.analyses`, `.decisions`, `.observations`, `.evaluation.records` and three duration histograms |
| `service.name`, instrumentation scope and SDK on the ingest envelope | **No** | envelope at `processor/internal/evaluation/client.go:255-279` carries version, sequence, behavioral_profile, fidelity, behavior_layer and the record. `service.name` survives only as the actor id fallback (`processor/mapping.go:66-69`) |
| Actor extraction counts (bound vs dropped) | **No** | the chain is `trustvian.actor.id` → resource `service.name` (`processor/mapping.go:66-79`). Nothing counts which link was used or how many spans had neither |
| Model detection | Partial | `gen_ai.request.model` becomes `Operation.Name` for model spans (`internal/semconv/genai.go:117`). `gen_ai.system` is read only as a provider fallback (`genai.go:84`) |
| Fidelity per record | Yes, on the envelope and the realtime observation | `internal/semconv/fidelity.go:26-30`; never persisted (task 081) |
| Engine baseline count, maturity distribution, admission against the 512 bound | **No public accessor** | `Engine` exposes only `NewEngine`, `Analyze`, `Observe` (`engine.go:73,93,170`). The bound is the unexported `maxFingerprints = 512` (`internal/baseline/baseline.go:167`), visible only as `Observe` returning `learned=false` |
| WebUI landing view | Live, hard-coded | `currentView = "view-live"` (`platform/webui/assets/app.js:192`). No logic chooses a landing view |

Two of those rows constrain the design more than the others:

1. **The control plane cannot see the Collector.** Every fact it holds arrived
   inside an ingest envelope. A span that the Collector dropped, or could not
   attribute to an actor, never became an envelope. So "spans without
   `service.name`" cannot be computed from anything the control plane already
   has.
2. **The engine runs inside the Collector, and has no stats accessor.** The
   baseline count and maturity distribution cannot be read without either
   changing the engine or reporting them as unavailable.
   [§ Conflict: engine facts](#conflict-engine-facts-and-engine-unchanged) is
   about this.

## Scope

### 1. `GET /v1/status`

One bounded, read-only document. It has no verdict and no score. Every section
states when it was observed, and states *unavailable* rather than zero when it
was not.

```json
{
  "read_at": "2026-10-05T10:00:00Z",
  "collectors": [{
    "collector_id": "dev-4f1c",
    "last_report_at": "2026-10-05T09:59:58Z",
    "receiver": {"endpoint": "127.0.0.1:4318", "protocol": "http/protobuf"},
    "window_seconds": "30",
    "spans_received": "412",
    "spans_evaluated": "398",
    "spans_without_actor": "14",
    "records_forwarded": "398",
    "records_refused": "0"
  }],
  "producers": [{
    "service_name": "support-agent",
    "first_seen_at": "…", "last_seen_at": "…",
    "spans": "398",
    "instrumentation_scopes": [{"name": "openinference.instrumentation.openai", "version": "0.1.22"}],
    "sdk": {"name": "opentelemetry", "language": "python", "version": "1.27.0"}
  }],
  "models": [{"provider": "ollama", "model": "llama3.2", "calls": "37", "source": "gen_ai.request.model"}],
  "fidelity": {
    "semantic": "201", "transport": "197",
    "targets_with_collapsed_operations": [{"target": "api.example.com", "fidelity": "transport", "distinct_operations": "4"}]
  },
  "actors": {
    "bound_by": {"trustvian.actor.id": "0", "service.name": "398"},
    "dropped_without_actor": "14"
  },
  "engine": {
    "state": "unavailable",
    "reason": "engine statistics are not exposed (task 105 § Conflict: engine facts)"
  },
  "suggestions": []
}
```

Shape rules:

- **Counts are canonical decimal strings**, like every other `uint64` this
  repository puts on a wire.
- **Every collection is bounded**: at most 64 producers, 16 scopes per
  producer, 64 models and 64 collapsed-target rows. When a bound is reached the
  section says `"truncated": true`. Nothing is dropped silently. The bounds match
  ADR 0041's collection limit.
- **Producer, model and fidelity facts are held in memory and are not
  persisted.** They are ephemeral notification state in ADR 0032's sense. A
  restart empties them and the document says so (`"since": <process start>`).
  Nothing here adds a schema step.
- **Nothing is content.** `service.name`, scope name and version, SDK name,
  language and version, `gen_ai.system` and `gen_ai.request.model` are resource
  or identity metadata. Prompts, completions, attributes in general and span
  names are not read for this document. See
  [§ Conflict: service names](#conflict-service-names-and-v0110s-exclusions).

### 2. Where each fact comes from

| Section | Source | Requires |
|---|---|---|
| `collectors` | a new **collector status report**: `POST /v1/collectors/{collector_id}/status`, sent by the processor every 10 s. It carries the window counters the processor already keeps, plus the receiver endpoint | processor change (outbound metadata) |
| `producers` | the same report: the processor keeps a bounded table keyed by resource `service.name` with scope and `telemetry.sdk.*` | processor change |
| `models` | the ingest envelope gains `model` and `provider` (from `gen_ai.request.model` / `llm.model_name` and `gen_ai.provider.name` / `gen_ai.system` / `llm.provider`), set only when fidelity is `semantic` and the layer is `model` | `internal/semconv` + processor + platform ingest |
| `fidelity` | envelope `fidelity` (already carried), grouped by `target_name`. "Collapsed operations" counts distinct `operation_name` values on one `target_name` among `transport`-fidelity records | platform only |
| `actors` | the collector status report: counters for which link of the actor chain bound each span, and for spans with no actor | processor change |
| `engine` | see [§ Conflict](#conflict-engine-facts-and-engine-unchanged) | — |

The collector status report is a push from a server-side component to the
control plane, at a fixed interval. No client polls. It is bounded (one
document of at most 64 KiB per collector per interval) and is authenticated the
same way ingest is: today, by loopback binding. Task 070 owns hardening.

### 3. The suggestion rule table

Implemented per [ADR 0064](../../adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md).
Evaluated on the control plane over the status document alone, in this fixed
order:

| # | `rule` | Condition (integer / categorical, over this document) | Text template |
|---|---|---|---|
| 1 | `status.no_collector` | no collector report received in the last 30 s | "No Collector has reported in 30 s. Is `trustvian dev` running, and is `trustvian-collector` on its path?" |
| 2 | `status.no_producer` | a collector reported, and `producers` is empty or every producer's `last_seen_at` is older than 30 s — *amended: and the Collector has been up for at least 30 s; see [What shipped](#what-shipped)* | "No producer has sent spans in 30 s. Check `OTEL_EXPORTER_OTLP_ENDPOINT` points at `{receiver.endpoint}`." |
| 3 | `status.spans_without_service_name` | `dropped_without_actor` > 0 | "{dropped_without_actor} spans had no `service.name` and no `trustvian.actor.id`, and were not evaluated. Set the `service.name` resource attribute." |
| 4 | `status.collapsed_http_operations` | a **named HTTP target** (category `http`, non-empty target) with `fidelity = transport` and `distinct_operations` ≥ 3 — *amended from "any target"; see [What shipped](#what-shipped)* | "{distinct_operations} operations on {target} are visible only as HTTP. Add OpenInference or OpenTelemetry GenAI instrumentation to see them as model or tool calls." |
| 5 | `status.admission_near_bound` | engine admission available, and `admitted * 10 ≥ bound * 9` (integer form of ≥ 90 %) | "{admitted} of {bound} behaviors are admitted for {actor}. At {bound} the evaluation stops learning new behavior." |

Rule 5 cannot fire while `engine.state` is `unavailable`. That is ADR 0064 § 3:
absent evidence never satisfies a condition. It is listed so that the table is
complete, and so that the gap is visible rather than forgotten.

### 4. `trustvian status` and `trustvian dev --check`

- `trustvian status [--api-url]` performs one `GET /v1/status` and prints the
  response body unchanged. `--json` is the default and only format in the first
  slice. A human rendering is a later slice, rendered from this JSON.
- `trustvian dev --check` composes the runtime exactly as `dev` does, without
  starting the workload. It waits until one collector report arrives or 15 s
  pass, then prints the same JSON as `trustvian status` and exits.
- Exit codes: `0` when the document was read, whatever it says. `3` when the
  control plane or the Collector could not be reached. Suggestions never change
  an exit code (ADR 0064 § 2).

### 5. The Status view

- A new **Status** destination under *Observe*. It renders `/v1/status` and
  computes nothing.
- **It is the landing view when nothing is active.** At startup the WebUI makes
  **one** `GET /v1/status`. If any collector reported within 30 s and at least
  one producer was seen within 30 s, Live is the landing view, as today.
  Otherwise Status is. The control plane decides this and returns it as a
  field: `"landing": "live" | "status"`. The browser does not evaluate the
  30-second rule itself.
- **Live data arrives over the existing realtime bus.** One new event kind,
  `status_changed`, is published when a collector report changes the document.
  It carries no payload beyond the new `read_at`, and the view then re-reads the
  route once. The browser never polls on a timer.
- Every number on the view is a field of `/v1/status`.

## Conflicts this task records

### Conflict: engine facts and "engine unchanged"

The milestone constraint says the engine is unchanged. Three facts this task
was asked to show — baseline count, maturity distribution, and admission against
the 512 bound — exist only inside the engine and have no public accessor.

| Option | Cost |
|---|---|
| **A. Report `engine` as unavailable** in this milestone, and leave rule 5 written but unable to fire | No core change. The [open conflict on fingerprint admission](../../ROADMAP.md#open-conflicts-to-resolve-before-implementation) stays open |
| B. An additive, read-only `Engine.Stats()` (baseline count, per-actor admitted count, a maturity histogram in fixed integer buckets) | A core change. The roadmap requires explicit architectural review for any core change, and that it be generic. This one is generic: an operator of the SDK wants it too |
| C. Count `Observe` returning `learned=false` in the processor | Wrong. `learned=false` also means a decision was not eligible for learning, so the count would conflate admission refusal with gated learning |

**Proposed: A in this milestone, B as a separately reviewed follow-on.** C is
rejected. A human decides.

### Conflict: service names and `v0.11.0`'s exclusions

`v0.11.0`'s exclusions say "Spans the engine did not evaluate, span events,
attributes, service names and all content stay unretained." This task displays
`service.name` and keeps a count of spans the engine did not evaluate.

Neither fact is persisted. Both are held in memory and are lost on restart, so
the word "retained" in that exclusion is not contradicted. They are still
displayed for the first time, though, and the change is stated here rather than
left to be found in review. `service.name` is resource metadata, not content.
It already reaches the platform whenever it is the actor id.

### Conflict: Live is the landing view

`v0.11.0` ("Live remains the landing view and its startup budget is unchanged")
and the WebUI product model ("Live behavioral understanding is" the landing
page) both assume Live is where a developer starts. This task keeps that
whenever anything is active, and replaces it with Status only when Live would
show nothing. Live's startup budget grows by one request (`GET /v1/status`).
[PERFORMANCE.md](../../PERFORMANCE.md) records the measured cost.

## Non-goals

- **No health score, no traffic-light summary, no "pipeline OK".** The document
  has facts and suggestions, and no rollup.
- **No content.** No span names, no attribute values other than the named
  resource and identity keys above, no prompt or completion.
- **No persistence and no history.** Status is now, and a restart forgets it.
- **No remote Collector management.** The control plane reads reports. It does
  not configure, restart or reach into a Collector.
- **No polling** by any client.

## Technical requirements

1. The status route reads in-memory state only and costs O(bounded) per call.
   Its latency and allocations are measured and recorded in `PERFORMANCE.md`.
2. The collector report is idempotent per `(collector_id, report sequence)`. An
   older sequence is ignored.
3. Every bound is enforced at write, with a `truncated` marker at read.
4. A collector that stops reporting ages out after 5 minutes, and is shown as
   stale before that.
5. The processor sends reports from one goroutine with a bounded queue of one.
   If a send fails, the next report replaces the queued one, so a slow control
   plane cannot grow the processor's memory.
6. The platform still imports nothing from core `internal/` packages
   (`scripts/check-platform-boundary.sh`), and the processor still imports
   nothing from the platform (`TestProcessorImportsNothingFromThePlatform`).

## Tests

- Golden `/v1/status` documents for: no collector, a collector with no
  producer, a producer without `service.name`, mixed fidelity, model calls
  present, and truncation at every bound.
- One test per rule-table row: it fires on its evidence, it does not fire when
  the evidence is absent, and removing the table changes nothing else in the
  response (ADR 0064).
- `trustvian status` and `dev --check` print byte-identical JSON for the same
  state.
- The landing decision is a server field. A WebUI test asserts the browser reads
  it and does not compute it.
- A privacy tripwire plants sentinel values in every content attribute
  `internal/semconv/content.go` lists, and asserts none appears in the report,
  the route, or the view.
- The degradation suites pass unchanged: a producer with no service name, scope
  or SDK attributes gets byte-identical behavioral results.
- A processor goroutine-leak test for the reporter.

## Acceptance criteria

1. With nothing running, the WebUI opens on Status and names the next step
   (rule 1, or rule 2 once a Collector has been up for 30 s).
2. With an instrumented agent running, the WebUI opens on Live, as today.
3. A producer missing `service.name` is reported with a count and rule 3's text.
4. A workload whose model calls are visible only as HTTP is reported with rule
   4's text, naming the target.
5. `trustvian status`, `trustvian dev --check` and the Status view show the same
   facts, and every number on the view is in the `/v1/status` response.
6. Unavailable reads *not available*, never `0`. The engine section is
   explicitly unavailable until a reviewed accessor exists.
7. No suggestion changes an exit code, a verdict or a stored record.
8. The route's cost is measured and recorded.

## ADR

ADR 0064 covers suggestions. A new ADR is written with the implementation:
*the collector reports its status to the control plane; the control plane holds
it ephemerally*. It decides the report's push direction, interval, bound and
non-persistence, and why the control plane cannot derive these facts from
ingest alone.

## Risks

- **A status surface is where scope creep starts.** "Show the last 10 span
  names" is the next request, and span names can carry content. The answer is in
  the non-goals, and the privacy tripwire enforces it.
- **A suggestion that is wrong for a setup is worse than none.** Rule 2
  assumes OTLP/HTTP via the environment variable, for example. Each rule text
  names its evidence, so a developer can see why it fired.
- **The 30-second thresholds are stated, not measured.** They are constants in
  the rule table with a version. Changing one is a `rule_version` bump.

## What shipped

The scope above, with these differences. Each is a decision taken while
building, and recorded here rather than left for a reader to find in the code.

| Specified | Shipped | Why |
|---|---|---|
| Producers, models, fidelity and actors as top-level document sections | Nested under each Collector, alongside its spans, learning and receivers | Each Collector reports its own cumulative counts. Merging them would sum counters from different processes and different start times into a figure nobody measured |
| `models` and `provider` added to the ingest envelope | Computed in the Collector from the Event the convention table already produced. A model call is a model-layer behavior, its model is `Operation.Name` and its provider is `Target.Name` | No attribute is read twice, the ingest envelope and its hot path are unchanged, and a status-only Collector (`dev --check`) has no envelope at all |
| Fidelity and the collapsed-operation count computed by the control plane from ingested records | Counted by the Collector and reported | Same reason. Operation names are counted and never reported |
| `window_seconds` and window counters | Cumulative counters since the Collector started, with `started_at` | A window needs either a clock in the report or the control plane to subtract counters from two reports. Cumulative counts with a start time are exact |
| Engine section: baseline count, maturity, admission against 512 | `unavailable` with its reason, plus a per-Collector `learning` section: learned, not learned and observe errors, with not-learned grouped by the decision it followed | Option A of [§ Conflict: engine facts](#conflict-engine-facts-and-engine-unchanged), as the maintainer decided. Rule 5 is in the table and cannot fire. The grouping lets a reader see that a not-learned `allow` was not ineligibility, without the processor restating the engine's eligibility rule |
| `landing` decided at startup | A `landing` field computed by the control plane on every read. The WebUI reads it once at startup and never moves a reader who has already moved | No interface holds a copy of the rule |
| Rule texts and thresholds as specified | Same rules and thresholds. Rule 2 names the first reporting Collector's first receiver endpoint, with a second fixed sentence when none was reported. Rule 4 says "at least N" when the per-target count saturated | The template substitutes only evidence values (ADR 0064) |
| Suggestion `evidence` as a JSON object | An ordered list of `{name, value}` | The WebUI may not enumerate a server object's keys (ADR 0036 § 7). The order is the rule's own |
| Report body bounded at 64 KiB | 256 KiB, the API's request bound. The processor fits an oversized report, scope lists first, and marks what it shortened | A report at every bound with maximal strings measured 681 KB. It fits at 131 KB with all 64 producers once scope lists are dropped |
| Rule 4 over "any target with `fidelity = transport`" | Only a span whose category is `http` and whose target is named is counted per target. The platform also never fires rule 4 for an empty target, as a second guard | Transport fidelity also holds DB spans, whose operation is a span name, and the RPC fallback. The fallback includes OpenInference `CHAIN`, `GUARDRAIL`, `EVALUATOR` and `PROMPT` spans and internal spans with no target. An OpenInference-instrumented CrewAI or LangChain agent filled the empty target with three or more span names within seconds, so rule 4 told an OpenInference user to add OpenInference. Found in review |
| Rule 2 for any reporting Collector with no fresh producer | Only for a reporting Collector up for at least the fresh window (the document's read time less the Collector's start), naming the first such Collector in identifier order with `collector_uptime_seconds` in its evidence | "No producer has sent spans … in 30 s" was printed 1.65 s after `dev --check` started its Collector, and shown in the WebUI by every new `dev` session before its workload sent anything. The sentence claimed 30 s of silence the Collector had not observed, and named a receiver port that closes when the check exits. With nothing else wrong, `dev --check` now prints an empty `suggestions` list. Found in review |
| `status_changed` published on every report | Published only when a report changes what a reader would see, ignoring sequence, uptime and ages | An idle Collector would otherwise wake every open Status view every 10 s |

**Rule 4 rarely fires for plain HTTP clients, measured.** Current
`opentelemetry-instrumentation-requests` and `-httpx` (0.66b1, SDK 1.45.1;
requests 2.34.2, httpx 0.28.1) were measured sending POSTs to one host on three
paths (`/v1/chat/completions`, `/v1/embeddings`, `/v1/rerank`). Both name every
client span after its method alone (`POST`), so three paths are one distinct
operation:

```text
OTEL_SEMCONV_STABILITY_OPT_IN=''      requests  POST  http.method, http.url — no server.address
                                      httpx     POST  http.method, http.url — no server.address
OTEL_SEMCONV_STABILITY_OPT_IN=http    requests  POST  http.request.method, server.address, url.full
                                      httpx     POST  http.request.method, server.address, url.full
```

Without the opt-in there is no `server.address` either, so the target is empty.
`trustvian dev` sets the opt-in. Rule 4 is kept as fixed above. Whether a
different signal, such as distinct `url.path` values (which Trustvian does not
read today) or HTTP spans whose parent is unnamed, should replace it is left
open for the maintainer.

Measured costs are in [PERFORMANCE.md](../../PERFORMANCE.md#v012-task-105-pipeline-status-surface).
The span path costs about 160 ns per span with no new allocation. A typical report
costs the control plane 12 µs, and a typical read 8 µs.

Verified beyond the test suites:

- A real `trustvian dev` session with eight GenAI spans reported its producer,
  scope, 8 semantic spans, 8 bound by `service.name` and 8 learned, against its run.
- `trustvian dev --check` completed in 1.65 s against freshly built helpers.
- Headless Chrome confirmed three behaviors. The WebUI lands on Status with
  nothing reporting and on Live with a fresh producer. A `status_changed` event
  updates an open Status view without a reload. Light, dark and 390 px layouts
  have no page-level horizontal scroll.
