# Compatibility Contract

What Trustvian promises not to break, what it may change, and what a
change costs in version numbers.

This is the canonical answer to one question a maintainer asks on every
pull request: **is this change breaking?** Everything below describes the
contract from `v1.0.0` onward. Before `v1.0`, see
[Branching Strategy § pre-v1.0 discipline](governance/branching.md#pre-v10-discipline).

## How to use this

1. Find the surface you are changing in the [matrix](#compatibility-matrix).
2. Read its classification.
3. Apply the [SemVer rules](#semver-rules) for the kind of change.
4. If the change is breaking or deprecating, say so in the pull request
   and in `CHANGELOG.md`.

## Classifications

| Class | Meaning |
|---|---|
| **STABLE** | Covered by backward-compatibility guarantees. An intentional break requires a major version |
| **STABLE WITH DEPRECATION** | May evolve, but removal or a behavioral break requires a documented deprecation first |
| **OPERATIONALLY STABLE** | Not a source API, but operators build on it. Breaking it requires migration guidance and the appropriate version bump |
| **OBSERVATIONAL** | Useful, deliberately not guaranteed stable byte-for-byte or word-for-word |
| **INTERNAL** | No compatibility promise |
| **EXPERIMENTAL** | Explicitly outside the stability promise, and labelled as such where it appears |

Nothing is labelled EXPERIMENTAL to avoid responsibility. Today the
repository ships no experimental surface — if that changes, it is named
here and in its own documentation.

## Compatibility matrix

The platform `/v1` rows describe the **intended** `v1` contract. That surface
ships with `v1.0` and may still change through reviewed work before the
release; the rows are here so the intent is recorded now rather than
reconstructed later. Everything else in this table is already released.

| Surface | Class | v1 guarantee | Allowed in a minor | Breaking change requires |
|---|---|---|---|---|
| `event.Event` and its field shapes | STABLE | Fields are not removed, renamed, or redefined | New optional fields whose zero value means "unset" | Major |
| `Result` and its field shapes | STABLE | Field presence and meaning | New fields | Major |
| `Engine`, `NewEngine`, `Analyze`, `Observe` signatures | STABLE | Signatures hold | New `Option` functions | Major |
| `Option` functions | STABLE | Existing options keep working; `WithLearningScope`'s default `""` is the pre-`v1.0` behavior | Additional options | Major |
| `alert` package exports | STABLE | `Alert`, `Severity`, `Rule`, `Condition`, `Evaluate`, `Sink`, `WebhookSink` | Additive fields and options | Major |
| `config` exported types and `Compile*` functions | STABLE | Existing documents keep compiling | New optional fields, new document types | Major |
| `StableFeatures` (root package) | STABLE | Field shapes; handed to a `WithContextRisk` callback | New fields | Major |
| `DecisionRecord`, `ContributorRecord` | STABLE | Field shapes and meanings | New fields | Major |
| `DecisionRecord` JSON field names | STABLE | A name, once published, keeps its meaning | New fields | Major |
| Exported sentinel errors | STABLE | An error identity checked with `errors.Is` keeps matching the condition it names | New sentinels | Major |
| Exported enum-like constants | STABLE WITH DEPRECATION | Existing values keep their meaning | New values — **consumers must tolerate unknown values** | Major to remove a value |
| Configuration schema (`policy`, `alerts`, `anomaly`, `storage`) | STABLE | A valid `v1` document keeps loading across `v1.x` | New optional fields; a new schema version alongside `v1` | Major, or a new schema version |
| Configuration defaults | STABLE WITH DEPRECATION | A default is not changed silently | Documented default changes in a minor, called out in CHANGELOG | See [behavioral compatibility](#behavioral-compatibility) |
| CLI commands and flags | OPERATIONALLY STABLE | `analyze`, `baseline`, `version`, `--config`, `--anomaly-config`, `--storage-config` keep working | New commands and flags | Major to remove or repurpose |
| `dev --instrumentation` values | OPERATIONALLY STABLE | `existing`, `none` and `auto` keep their meaning; `auto` never injects on absence of evidence | New modes — `python-zero-code` is reserved and currently refused | Major to remove or repurpose a value |
| CLI exit codes | OPERATIONALLY STABLE | Scoped by command family; `1` means gate failure only for `eval compare` — see [CLI](#cli) | Adding a code, or a new family with its own scoped contract | Major |
| `trustvian-run` GitHub Action inputs and outputs | OPERATIONALLY STABLE | See [GitHub Action](#github-action) | New inputs and outputs | Major to remove or repurpose |
| `trustvian-run` artifact (`result.json`, `trustvian-run.json` v`1`) | OPERATIONALLY STABLE | See [GitHub Action](#github-action) | New metadata fields | Major, or a new metadata version |
| `trustvian-run` job summary | OBSERVATIONAL | Not a machine interface | Any change | None |
| `trustvian-ci-render` flags and exit codes | OPERATIONALLY STABLE | See [GitHub Action](#github-action) | New optional flags | Major to remove or repurpose |
| `trustvian-ci-render` Markdown | OBSERVATIONAL | Not a machine interface | Any change | None |
| `trustvian-ci-comment` flags, exit codes and marker line | OPERATIONALLY STABLE | See [GitHub Action](#github-action) | New optional flags | Major to remove or repurpose; changing the marker orphans existing comments |
| `trustvian-comment` GitHub Action inputs and outputs | OPERATIONALLY STABLE | See [GitHub Action](#github-action) | New inputs and outputs | Major to remove or repurpose |
| Gate comment body | OBSERVATIONAL | Not a machine interface | Any change | None |
| CLI human-readable output | OBSERVATIONAL | Not a machine interface — no wording, spacing, or ordering promise | Any change | None |
| Environment variables read by shipped binaries | OPERATIONALLY STABLE | See [environment variables](#environment-variables) | New variables | Major to remove or rename |
| Collector processor type name and config fields | OPERATIONALLY STABLE | `policy`, `storage`, `health`, `evaluation` keys and their meaning | New optional keys | Major |
| Collector pending ingest state file (`evaluation.pending_state_path`) | INTERNAL | Nothing — it is this Collector's own crash-recovery note, not an interface | Format and contents, in any release; a build refuses a version it does not recognize | None |
| Policy rule semantics | STABLE | First-match-wins ordering; fail-closed to `BLOCK` on invalid policy | New condition fields; new decisions | Major |
| PostgreSQL schema | OPERATIONALLY STABLE | Forward-only, version-gated; see [persisted state](#persisted-state) | Additive columns or tables with a schema-version bump | Major for a destructive change |
| File-store snapshot format | OPERATIONALLY STABLE | Version-tagged; a `v1.x` binary reads what `v1.x` wrote | Additive fields that do not change what a record identifies | Major |
| Persisted `Baseline` JSON | OPERATIONALLY STABLE | Field names are not removed or repurposed within `v1` | Additive fields | Major |
| Metric names and types | OPERATIONALLY STABLE WITH DEPRECATION | Existing metrics keep their name, type, and meaning | New metrics | Deprecation, then a minor to remove |
| Metric label keys | OPERATIONALLY STABLE | Label keys keep their meaning; cardinality stays bounded | New labels — **consumers must tolerate unknown label values** | Major to remove or rename a key |
| Health and readiness endpoints | OPERATIONALLY STABLE | `/livez` and `/readyz` paths, and their HTTP status semantics | New endpoints, new response fields | Major |
| Health response body | OBSERVATIONAL | Status code is the contract; the body is diagnostic | Any change | None |
| Webhook payload envelope | STABLE | `version` field, currently `"1"`; `alert` object field names | Additive fields inside `alert` | New envelope version |
| Platform `/v1` control-plane route shapes | STABLE | Path, method and path-parameter shape of each documented `/v1` route | New routes; new optional request fields | Major, or a new path version |
| Platform `/v1` JSON field names | STABLE | A published request or response field name keeps its meaning | New fields — **consumers must tolerate unknown fields** | Major, or a new path version |
| Platform ingest envelope | STABLE | `version` field, currently `"1"`; `sequence`, `behavioral_profile`, `record` | Additive envelope fields — `fidelity` (075) and `behavior_layer` (083) are both optional | New envelope version |
| Retained observation field set | STABLE | An allowlist expressed as columns. No attribute map, no span-event list, no link list and no payload field exists, so no prompt, completion, argument, result, document, body or arbitrary attribute can be retained or published | New named scalar fields | Major |
| `DecisionRecord` JSON fields | STABLE | A published field name keeps its meaning; absent optional fields mean *unavailable*, never a measured value | New optional fields — `parent_span_id`, `span_lineage`, `duration_nanos` and `span_status` (084). **A record written before a field existed stays valid**; `duration_nanos` distinguishes `""` (unavailable) from `"0"` (measured zero), and a value that is malformed, non-canonical or above `event.MaxDurationNanos` is **refused at ingest** rather than read as unavailable | Major |
| Platform error `code` values and HTTP statuses | STABLE | A code and its status keep the condition they name | New codes | Major |
| Platform error `message` text | OBSERVATIONAL | The code and status are the contract; wording is diagnostic | Any change | None |
| Platform ingest sequence semantics | STABLE | Monotonic from 1; expected applies, identical retry of the last replays, gap and stale fail | — | Major |
| Platform SQLite schema | OPERATIONALLY STABLE | Forward-only, version-gated; currently version 7 | Additive tables, columns or indexes with a schema-version bump and a migration | Major for a destructive change |
| Platform collection paging | STABLE | Every `/v1` collection shares one shape: parent-scoped except `GET /v1/projects`, ordered by `id` byte-ascending, exclusive `after` cursor, `limit` 1–64 defaulting to 64, `next_after` present exactly when another row follows, `404` for a missing parent and `200` with an empty array for an empty one | New optional query parameters | Major, or a new path version |
| `GET /v1/projects` | STABLE | Path, method, and the bounded paging contract above; the one unscoped collection | New optional query parameters | Major, or a new path version |
| `GET /v1/projects/{project_id}/agents` | STABLE | Same | New optional query parameters | Major, or a new path version |
| `GET /v1/agents/{agent_id}/candidates` | STABLE | Same | New optional query parameters | Major, or a new path version |
| `GET /v1/candidates/{candidate_id}/evaluation-runs` | STABLE | Same | New optional query parameters | Major, or a new path version |
| `GET /v1/evidence/behaviors` | STABLE | Task 085's resolution: the finding travels in the query string (`reference_run_id`, `candidate_run_id`, and one of `check` or `behavior`), so the URL is the citable link. Same bounded paging, cursor by fingerprint id in byte order | New optional query parameters | Major, or a new path version |
| `GET /v1/evidence/observations` | STABLE | The same finding reference plus an optional `side`; cursor is the ingest sequence | New optional query parameters | Major, or a new path version |
| Resolution `status` values | STABLE | Exactly `resolved`, `none_found`, `indeterminate` and `aggregate_only`, and always present. **The status describes the finding, not the page**: a page requested past the last match returns zero rows with `resolved`, because the evidence exists and the caller has read it. `none_found` is returned only when nothing matches *at all* **and** the history is complete; an empty result over partial or unavailable history is `indeterminate`. Page exhaustion is signalled by `next_after` being absent | New statuses | Major |
| Resolution `side` for a shared behavior | STABLE | A behavior present in both runs **requires** an explicit `side`; omitting it is `400 invalid_request`. An added behavior defaults to `candidate` and a removed one to `reference`, and a side contradicting either is refused. A `check` reference never carries a side | — | Major |
| Resolution `recorded_count` | STABLE | What the recorded evidence holds — the gate's own actual. **Not a count of the returned rows**, and never reconciled with one: a check counts every record the run ingested while retained history is bounded | — | Major |
| Resolution `exhaustive` | STABLE | True only when the side's retained history is complete. A full set of matches drawn from partial history is a sample and is never labelled exhaustive | — | Major |
| `POST /v1/evaluations/compare-repeated` | STABLE | Task 078's repeated evaluation ([ADR 0053](adr/0053-repeated-evaluation-counts-identities-across-isolated-repetitions.md)). Body: `reference_run_ids` and `candidate_run_ids` (equal length, `1..64`, every id distinct) and `gate_limits` with all five of `added_candidate_presence_minimum` (k), `added_reference_presence_maximum` (j), `max_repeated_added_behaviors`, `max_block_decisions_per_run`, `max_critical_risk_observations_per_run` as canonical decimal strings — none optional, `0 <= j < k <= N`. `400` for a malformed request, repetitions sharing a behavioral profile, repetitions in more than one environment, or fingerprint identity that is not one-to-one across repetitions, `404` for a missing run, `409 incomplete_evidence` for saturated evidence. Response: `runs`, the echoed `gate_limits`, every `repetitions[]` entry (side, index, run, status, profile, record, block and critical-risk counts), every observed `behaviors[]` entry with `reference_runs_present`, `candidate_runs_present` and `classification` (`added`, `removed`, `neither`), `gate.checks[]` — exactly six in a stable order, each with `name`, `actual`, `rule` (`equals`, `at_most`), `bound`, `passed`, and `advisory: "fresh_scope"` on the last two when `runs > 1` — `gate.verdict`, and `producer.control_plane_version`. Behavior descriptors are producer-supplied strings | New response fields, new check names only with a major version | Major |
| `max_repeated_added_behaviors` unit | STABLE | Counts repeatedly added behavioral **identities**, the unit of `max_added_behaviors`: at `runs: 1, k: 1, j: 0` the repeated verdict equals the single-run gate's. Not counted changes | A separate optional limit with its own unit | Major to redefine |
| Scenario file (`config.LoadScenario`) | STABLE | Schema `version: v1`; `name`, `runs`, `reference`/`candidate` `command` and the five `gate` fields required, no defaults, unknown fields refused; optional `project`, `agent`, `environment`, `instrumentation`, per-side `env` and `candidate` passed through to `trustvian dev` | New optional fields | Major |
| `trustvian eval run` result document (`--json`) | STABLE | `version`, `scenario` (`name`, `runs`), `execution_id` (always this invocation's own execution), `producers` (`cli_version`, `control_plane_version`), and `comparison` — the server's repeated result, content unchanged. With `--reference`, also `reference` (`mode`: `execution` or `last`; `execution_id`: the execution whose reference side was reused); absent otherwise | New fields | Major |
| `trustvian eval run --reference <execution-id>\|last` | STABLE | Reuses one recorded execution's reference side, whole, and runs only the N candidate repetitions. `last` is the most recently completed execution of the same scenario name, project, agent and environment. The reference is resolved by the control plane before any workload runs; one that is missing, unfinished, of another N, outside the project or environment, or with incomplete evidence is exit `3`. The current scenario's gate limits apply. The scenario file is validated whole either way, `reference.command` included | New reference modes | Major |
| `trustvian eval run --suite <dir>` | STABLE | Members are the directory's immediate regular `.yaml`/`.yml` files in name byte order; symlinks, an empty suite, duplicate scenario names, more than 64 scenarios or more than 4096 directory entries are usage errors (`2`) before anything runs, as is any invalid member. A preflight failure of the environment (working directory, repository inspection, locating the control plane) is `3`; neither starts a workload or makes a request. Requires `--scenario-timeout` (1s to 24h) per scenario; `--fail-fast` stops scheduling after the first non-PASS; `--reference` accepts only `last`, resolved per member. Members run sequentially through the single-scenario path; nothing is pooled or recomputed ([ADR 0055](adr/0055-a-scenario-suite-is-a-bounded-schedule-and-a-report-not-an-evaluation.md)) | New optional flags | Major |
| `trustvian eval run --suite` result document (`--json`) | STABLE | `version`, `complete`, `suite` (`directory`, `scenario_count`), `options` (`scenario_timeout`, `fail_fast`, `reference`), `members[]` (`file`, `scenario`, `outcome` — exactly `pass`, `fail`, `error`, `skipped` — `exit_code` (absent when skipped), `execution_id`, `result` — the single-scenario result document, unchanged, for `pass` and `fail` — `error` (`code`, `message` — at most 1024 bytes of valid UTF-8, a truncation marker included), `skipped_reason` — `fail_fast` or `cancelled`), `summary`, `exit_code`, `producers.cli_version`. Member error codes include the server's (`not_found`, `conflict`, `incomplete_evidence`, …) and the runner's (`repetition_failed`, `run_not_completed`, `scenario_timeout`, `cancelled`, `completed_without_response`, `execution_state_unknown`, `operational`, `usage`). Capped at 32 MiB encoded; over it, exit `3` with `complete: false`, `error.code: "output_too_large"` and members carrying only `file` and `scenario` | New fields, new error codes | Major |
| Suite deadline | STABLE | The deadline bounds run naming (repository git queries are cancelled with their process group, hooks included, pipes are closed within 500ms, and none start after it) and run completion (an in-flight completion is cancelled and the run failed, never reported complete), as well as the workload. A scenario past `--scenario-timeout` starts no further repetition; its workload's process group gets SIGTERM, then SIGKILL after 5s, and leftover group members are killed; its run is failed; it is an operational error (`scenario_timeout`) even if the workload exited `0`. When the execution's completion request ends without the control plane's own answer — the deadline, a cancellation, the transport, a gateway's 502/503/504, a non-2xx without a `/v1` error envelope, or an unreadable 2xx — the runner fails the execution and the control plane's compare-and-swap decides: failed means `scenario_timeout` or `cancelled`, and that execution can never complete; already completed is reported as `completed_without_response` (exit `3`, a completed and reusable execution, no verdict reported); unreadable is `execution_state_unknown`. A member reported `scenario_timeout` or `cancelled` is never a completed execution. SIGINT/SIGTERM are owned by the suite process alone: members' `dev` sessions do not subscribe and their workloads get no terminal, so one signal reaches the running workload as exactly one SIGTERM to its process group (`cancelled`), and the rest are skipped as `cancelled`, also under `--fail-fast`. `--suite` is refused (exit `2`) where `trustvian dev` is unsupported (Windows); `--scenario` is unaffected | — | Major |
| `POST /v1/scenario-executions` | STABLE | Task 078's persisted executions ([ADR 0054](adr/0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md)). Body: `id`, `scenario_name`, `runs` (`1..64`), `project_id`, `agent_id`, `environment`, and optional `reference` — `{"mode": "last"}` or `{"mode": "execution", "execution_id": …}`. `201` with `execution` (running) and, when a reference was asked for, the resolved `reference_execution`. `404` for a missing reference (explicit, or no completed execution in scope for `last`); `409 conflict` for one that is unfinished, of another N, outside the project or environment, or whose recorded runs no longer match; `409 incomplete_evidence` for saturated reference evidence; `409 already_exists` for a duplicate id; `400` for a malformed body. `last` never falls back to an older execution when the latest is refused | New optional request fields | Major |
| `GET /v1/scenario-executions/{execution_id}` | STABLE | One execution: `id`, `scenario_name`, `project_id`, `agent_id`, `environment`, `runs`, `reference_execution_id` (absent when self-contained), `status` (`running`, `completed`, `failed`), `started_at`, `finished_at`, `completion_sequence` and `verdict` (when completed), and `repetitions[]` (`side`, `index`, `run_id`, `behavioral_profile`; reference side first, empty unless completed). Metadata only | New response fields | Major |
| `POST /v1/scenario-executions/{execution_id}/complete` | STABLE | Body: `candidate_run_ids`, `gate_limits` (compare-repeated's five, all required) and, for a self-contained execution only, `reference_run_ids`; a recorded-reference execution supplies its own and refuses them with `400`. Evaluates through compare-repeated's single implementation and returns `execution` (completed, with `verdict`) and `comparison` in exactly compare-repeated's response shape. A gate FAIL completes the execution. A run outside the execution's project, environment or (candidate side) agent is `400`; completing an execution that is not running is `409 conflict` and writes nothing | New response fields | Major |
| `POST /v1/scenario-executions/{execution_id}/fail` | STABLE | Body `{}`. Marks a running execution failed; a failed one is returned unchanged; a completed one is `409 conflict`. A failed or still-running execution is never a reference | — | Major |
| Scenario execution `completion_sequence` | STABLE | A decimal string, `1, 2, 3 …` per project in the order completions committed. It, and not any timestamp or identifier, is what `last` orders by | — | Major |
| `max_added_behavior_changes` gate limit | STABLE | **Optional.** Bounds counted behavioral **changes** — the comparison's `added_change_count` ([ADR 0052](adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md), issue 131). Omitted or `null` means the check is not evaluated and the verdict is the other five checks alone; `"0"` is the strictest limit. A canonical decimal string like the other three; anything else is `400`. When both behavior limits are supplied, both are enforced | — | Major to make mandatory or to change its unit |
| Gate check `added_behavior_changes` | STABLE | Present on every comparison `gate` and promotion `gate_result`. `state` is exactly `evaluated`, `not_evaluated` or `not_recorded` (a promotion stored before schema 8); `actual`, `maximum`, `passed`, `correlation_state` and `counting_policy_version` are present **only** when evaluated. The evidence routes refuse it as a `check`, directing to `added_changes` | New states only with a major version | Major |
| Promotion `gate_limits.max_added_behavior_changes` | STABLE | The decision-time threshold, or `null` when the decision was made without it — never `"0"` for absent. Historical promotions read `null` | — | Major |
| `max_added_behaviors` unit | STABLE | Counts added behavioral **identities**, unchanged by task 083's counting correction. One act observed at two instrumentation layers still consumes two of this budget, and every stored promotion keeps the meaning it was recorded under | A second limit with its own named unit | Major to redefine |
| Comparison `added_change_count`, `correlation_state`, `counting_policy_version`, `added_changes` | STABLE | Added behavioral **changes** — added identities not wholly beneath added parents: an identity folds only when every retained occurrence of it is recorded beneath an added identity ([ADR 0052](adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)). Never greater than `added_count`, and equal to it whenever `correlation_state` is not `complete`. `counting_policy_version` increments if the rule changes rather than reinterpreting stored results | New additive fields | Major |
| Which gate checks resolve | STABLE | `added_behaviors`, `block_decisions` and `critical_risk_observations` resolve to evidence. `reference_evidence` and `candidate_evidence` are `aggregate_only` by construction — they fail on an absence, which has no supporting records. `added_behavior_changes` is refused (`400`) with directions to the comparison's `added_changes`, whose contributing identities each resolve as a behavior | A check gaining resolution | Major to remove one |
| `GET /v1/evaluation-runs/{run_id}/observations` | STABLE | Task 067's retained history: same bounded paging, except that the cursor is an **ingest sequence** rather than an identifier — canonical decimal, exclusive, starting at 1, and refused rather than coerced when it is not. Task 076 added `session_id`, `trace_id` and `fingerprint_id`: optional, **mutually exclusive**, applied in storage before the page bound, and echoed back as `scope` so a stored response is self-describing. More than one is `400` | New optional query parameters | Major, or a new path version |
| `GET /v1/evaluation-runs/{run_id}/traces` | STABLE | Task 100: the distinct trace identifiers in the run's retained history, each with `observations`, `first_sequence`, `last_sequence` and `error_spans` as decimal strings, and the observation page's `history_state`, `retained_count` and `complete`. Ordered by `first_sequence`, which is also the cursor — exclusive, canonical decimal, refused when it is not; `limit` 1..64; `next_after` exactly when another trace follows. Untraced observations are not listed. A run-scoped list of evaluated actions, not a distributed-trace index | New response fields | Major, or a new path version |
| `GET /v1/evaluation-runs/{run_id}/sessions` | STABLE | Task 103: the traces route's contract over the session column — `session_id`, `observations`, `first_sequence`, `last_sequence`, `error_spans` as decimal strings, the history fields, the same cursor and bounds. Only sessions carried by retained observations are listed | New response fields | Major, or a new path version |
| `GET /v1/projects/{project_id}/evaluation-runs/recent` | STABLE | Task 101 ([ADR 0063](adr/0063-recency-is-a-stored-sort-key-and-a-composite-cursor.md)): runs newest first by creation time, ties by identifier descending, within the project and optionally `agent_id` and `candidate_id`. `order` is `created_at_desc`. Cursor `<20-digit key>.<run id>`, exclusive, refused when malformed; `limit` 1..64; `next_after` exactly when more follow. A missing scope is `404`, a narrowing outside its parent `400` | New response fields | Major, or a new path version |
| Retained observation identity and order | STABLE | An observation is `(run_id, sequence)` and pages in ascending sequence — the order the platform accepted records. **Never ordered by timestamp**, which ties, and never keyed on a span id, which is unique only inside its trace | — | Major |
| Observation page consistency | STABLE | One page is read from one database snapshot. An ingest committing during a read yields the state before it or after it, never a mixture of the two | — | Major |
| Correlation identifier length | STABLE | `trace_id`, `session_id` and `fingerprint_id` are retained and returned **whole, at any length the request body allows**. Retention imposes no limit of its own: the index keys on a fixed-width digest so a storage detail cannot narrow which records are accepted | A length limit would be a change to the record contract, not to storage | Major |
| Observation `history_state` values | STABLE | Exactly `complete`, `partial` and `unavailable`, and always present. `unavailable` means the run has records whose history was never retained — **it is not an empty history**; `partial` means the run passed the retention bound or began before schema 7 | — | Major |
| Collection element shape | STABLE | Each element is the entity's existing detail DTO, field for field; a listing publishes nothing a by-id read does not | New fields, in both places together | Major |
| Platform environment identity | STABLE | An environment is `(project_id, ref)`; a run keeps recording only the ref, and refs are an open set rather than an enum | New optional environment fields | Major |
| `GET /v1/projects/{project_id}/environments` paging | STABLE | Traversal by `ref` ascending, exclusive `after` cursor, `limit` 1–64 defaulting to 64, `next_after` present only when another page follows | New optional query parameters | Major, or a new path version |
| Platform promotion semantics | STABLE | A promotion records a decision, never a deployment or a candidate's location; it is append-only, with no update or delete at any layer | New optional promotion fields | Major |
| Promotion `outcome` values | STABLE | Exactly `accepted` and `rejected`, derived from the gate verdict and never supplied by a caller | — | Major |
| Promotion identity | STABLE | `promotion_id` is caller-owned and the only identity; a duplicate is `409 already_exists`, never a silent overwrite or a body-compared idempotent replay | — | Major |
| Persisted promotion gate evidence | STABLE | The gate result the decision consumed, stored field for field and restored without recomputation; a later gate correction does not rewrite it | New additive evidence fields | Major |
| `GET /v1/projects/{project_id}/promotions` paging | STABLE | Traversal by `promotion_id` ascending, exclusive `after` cursor, `limit` 1–64 defaulting to 64, `next_after` present only when another page follows | New optional query parameters | Major, or a new path version |
| `GET /v1/realtime` route and filter query names | STABLE | Path, method, and the `project_id`, `agent_id`, `run_id` filters | New optional filters | Major, or a new path version |
| Realtime SSE event names | STABLE | A published event name keeps the condition it names | New event names — **consumers must tolerate unknown names** | Major |
| Realtime SSE JSON field names | STABLE | A published field name keeps its meaning | New fields — **consumers must tolerate unknown fields** | Major |
| `stream_ready` semantics | STABLE | Sent first on every connection; `replay_available` false and `resync_required` true | — | Major |
| Realtime no-replay / resync-required contract | STABLE | No history is retained; `Last-Event-ID` triggers no replay; every connection resynchronizes | A separate durable replay capability may be added alongside | Major |
| Slow-consumer disconnect semantics | STABLE | A subscriber that cannot keep up is disconnected rather than silently losing events | — | Major |
| SSE heartbeat timing and wording | OBSERVATIONAL | A comment frame with no domain meaning; interval is not a contract | Any change | None |
| Realtime byte-level SSE formatting | OBSERVATIONAL | Protocol semantics are the contract, not whitespace or frame ordering beyond `stream_ready` first | Any change | None |
| Container entrypoint and ports | OPERATIONALLY STABLE | `trustvian-collector` entrypoint; `4317`, `4318`, `13133`; runs as `nonroot` | New ports | Major |
| Image name and immutable tags | OPERATIONALLY STABLE | `ghcr.io/trustvian/trustvian-collector:vX.Y.Z` is immutable | — | Major |
| Floating image tags (`latest`, `X.Y`) | OBSERVATIONAL | Convenience aliases; they move by design | Move on every stable release | None |
| Release artifact names and checksums | OPERATIONALLY STABLE | `trustvian_<version>_<os>_<arch>.{tar.gz,zip}` plus `checksums.txt` | New platforms, new artifact kinds | Major |
| Archive internal layout | OBSERVATIONAL | No promise beyond the binary being present | Any change | None |
| Everything under `internal/` | INTERNAL | None | Any change | None |

## SemVer rules

### PATCH — `v1.x.Y`

May contain bug fixes, security fixes, performance work, and internal
refactoring. **Must not intentionally break any STABLE or OPERATIONALLY
STABLE surface**, and must not change a documented default.

### MINOR — `v1.Y.0`

May add: backward-compatible API surface, optional configuration fields,
new CLI commands and flags, new metrics, new enum values, new endpoints.
May change a documented default, with the change named in `CHANGELOG.md`.
May remove a surface **only** if it completed the
[deprecation](#deprecation) path.

### MAJOR — `vY.0.0`

Required for any intentional break of a STABLE or OPERATIONALLY STABLE
surface: removing or renaming a field, flag, variable, metric key, or
endpoint; changing an exit code's meaning; a destructive storage change;
or a semantic change that alters decisions for unchanged input outside
the [behavioral](#behavioral-compatibility) rules below.

## Go API

The public surface is the root package, `event`, `alert`, and `config`.
Everything else is `internal/` and carries no promise.

Three properties consumers may rely on, all already true:

- **Every `Engine` option is configurable from outside the module.**
  Four take a value produced by the `config` package —
  `CompilePolicy`, `CompileStorage`, `CompileAnomaly`, `CompileTrust` —
  and `WithContextRisk` takes a callback over the public
  `StableFeatures` type.
- A value obtained from an exported function may be passed to another
  exported function without naming its type. That is what makes
  `config.CompilePolicy` → `trustvian.WithPolicy` work, and it keeps
  working.
- Reading exported fields off a `Result` requires no import beyond the
  root package.

Some exported signatures name types defined under `internal/`. That is
an intentional facade, not an oversight: the `config` package produces
every such value, so a caller never has to name one. It does mean a
change to one of those types' *shapes* is breaking for external callers
even though the type is internal, and it is treated that way.

### The decision record

`Result.DecisionRecord()` returns the serializable public projection of one
analysis. Both its Go field shapes and its JSON field names are STABLE:
adding a field is a minor, removing or redefining one is major.

Three properties are part of the contract rather than implementation detail.
The record carries **no raw event payload** — `Event.Attributes`, tool
arguments, prompts, and completions have no field and cannot appear in its
JSON. It carries **no consumer-side identifiers**; anything associating
records with projects, candidates, or evaluations belongs beside them, not on
them. And **every record a successful `Analyze` produces is marshallable**,
which takes two guards with deliberately different outcomes. A non-finite
trust input is resolved fail-closed inside `trust.Compute` before the
`Result` is returned, so the analysis succeeds carrying conservative finite
numbers. A timestamp `time.Time.MarshalJSON` refuses — a year outside
`[0,9999]`, or a zone offset of 24 hours or more — is rejected by
`Event.Validate()`, so the analysis fails with `event.ErrInvalidTimestamp`
instead. The projection itself sanitizes nothing. All three properties are
asserted by test.

Fixed-shape is not size-bounded. Caller-supplied strings in the record are
not length-limited here; what is excluded is the open-ended part, the
attribute map. Field and request size limits belong to whatever ingests
events over a network.

The record has no schema version field, deliberately. This contract already
governs how its fields may change, and a version number would duplicate that
with machinery nothing reads. A transport that carries records across a
network owns its own envelope version, exactly as `alert.Envelope` does.

### Persistence is not an extension point

Supplying a custom `Store` implementation is **not supported in v1**.
`store.Store`'s methods reference four internal types, so an external
module cannot implement it — deliberately, since making it public would
freeze `Baseline`, the stateful core of the engine, for the life of the
major version. Persistence is *selected* through `config.StorageConfig`
from the backends Trustvian ships: memory, file, and PostgreSQL.

### Resource ownership

A store compiled from configuration is owned by the caller. An `Engine`
never closes a store it was given, because it did not open it. Where a
compiled store holds resources, the caller closes it:

```go
if c, ok := store.(io.Closer); ok {
	defer c.Close()
}
```

There is deliberately no `Engine.Close`: an engine closing a resource it
does not own invites double-closes. If that changes, it changes in a
major version.

Consumers must **not** rely on: exhaustively switching over enum-like
constants without a default branch, or implementing any interface whose
definition lives under `internal/`.

## Configuration

Each configuration document is independently versioned —
`SchemaVersionV1` for policy, `AlertSchemaVersionV1`, `AnomalySchemaVersionV1`,
`StorageSchemaVersionV1` — and each is required, not inferred.

| Change | Allowed in |
|---|---|
| Add an optional field | Minor |
| Add a required field | Major, or a new schema version |
| Remove or rename a field | Major, after deprecation |
| Add an allowed enum value | Minor |
| Remove an allowed enum value | Major, after deprecation |
| Relax validation | Minor |
| Tighten validation so a previously valid document is rejected | Major |
| Change a documented default | Minor, named in CHANGELOG |

**The guarantee:** a configuration document valid under `v1.0` loads
under every later `v1.x` with unchanged meaning.

**The limit, stated because it is easy to assume otherwise:** the loader
calls `KnownFields(true)`, so unknown fields are rejected rather than
ignored. A configuration using a field added in `v1.5` will **not** load
on `v1.2`. Compatibility runs forward, not backward — plan rollbacks
accordingly.

## CLI

Commands, flags, and exit codes are automation-facing and treated as
such.

Exit codes are **scoped by command family**. There is no single global
meaning for every code, and assuming one will be wrong:

| Commands | `0` | `1` | `2` | `3` |
|---|---|---|---|---|
| `analyze`, `baseline`, `version` | success | the run failed | top-level invocation was wrong | — |
| `project`, `agent`, `candidate`, `env`, `promotion`, `eval` except `compare` | success | *unused* | usage | API, network, or server failure |
| `tui` | you quit | *unused* | usage | startup, HTTP, SSE, protocol, or terminal failure |
| `eval compare` | gate **PASS** | gate **FAIL** | usage | API, network, or server failure |
| `eval run` | gate **PASS** | gate **FAIL** | usage — the scenario file, the suite or `--reference`, before anything runs | API, network, or server failure; a reference that is missing, unfinished, of another N, out of scope or incomplete, before any workload; a repetition whose workload exited non-zero, or whose run could not be completed; a scenario past `--scenario-timeout`. A suite exits with the most severe of its members: `3`, then `2`, then `1`, then `0` |
| `dev` | **the child's exit status**, whatever it is — see below | | | |

**Exit code `1` means gate failure only for `trustvian eval compare` and `trustvian eval run`; it
does not change the established meaning of code `1` for legacy
commands.** In particular `trustvian promotion create` exits `0` for a
**rejected** promotion: the gate said FAIL, the platform recorded that
decision, and the API call succeeded. A recorded rejection is a result, not a
failure, and reusing `1` for it would give the code a second meaning that no
script could disambiguate from a gate FAIL. It further requires the server to have returned the explicit
verdict `fail`: `pass` and `fail` are a closed vocabulary, and any other
value — including a missing field, a different case, or a verdict from a
newer server — is an unsupported response and exits `3`. Adding a verdict
to that vocabulary is a minor change; changing what `1` means is major. [Task 060](tasks/v1.0/060-developer-cli.md) resolved the
conflict by scoping rather than renumbering, so no released automation
changed meaning. See
[ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md).

### `dev` propagates its child's status

`trustvian dev` is a **documented exception** to the table above: it exits with
the status of the command it ran, whatever that status is. A wrapper that
rewrote its child's exit code would be unusable in a script, which is the only
place a wrapper is used.

```text
before the child starts    2  the invocation was wrong
                           3  dev could not start the child at all
                     128+N  a signal arrived during composition
after the child starts     the child's status, unmodified
```

So `2` and `3` carry dev's own meaning only up to the moment the child is
running. After that they mean whatever the workload means by them, and dev
cannot tell the difference — the same ambiguity `env`, `nice` and `timeout`
carry, accepted for the same reason: discarding the child's status is worse.

**The status is the child's even when dev's own bookkeeping fails.** If the
workload succeeds and the run cannot be completed, dev prints a warning naming
the run it left non-terminal and still exits `0`. Returning `3` there would make
a successful workload look like a failed one, and a script cannot act on a
distinction dev invented after the fact.

A child killed by a signal exits **`128 + signal`**, the convention every shell
uses. Go reports a signal death as exit code `-1`, which no script can branch
on, and collapsing it to `1` would make "the workload was killed" look identical
to "the workload failed".

A signal that arrives *before* the child starts uses the same encoding, so a
script sees one meaning for "stopped by a signal" whichever phase it
interrupted.

### What `dev` records about the run

The exit status is a script's contract; the evaluation run's terminal state is
the platform's. They are related and they are not the same, and the mapping is
part of the interface:

| What happened | Exit status | Run state |
|---|---|---|
| the workload exited `0` | `0` | **completed** |
| the workload exited non-zero | its own status | **failed**, reason naming the status |
| the workload died from a signal `dev` forwarded | `128 + signal` | **completed** |
| Ctrl-C or a hangup reached the workload through the terminal | `128 + signal` | **completed** |
| the workload died from any other signal nobody forwarded | `128 + signal` | **failed** |
| the workload could not be started | `3` | **failed**, reason saying it could not be started |
| a signal arrived during composition | `128 + signal` | **failed** if a run had been started |

The two middle rows are the same event seen from two sides, and they exist
separately because *who receives Ctrl-C depends on whether stdin is a
terminal*.

With no terminal, `dev` is signalled and forwards, and the forwarding is the
record of it. With a terminal, `dev` places the workload's process group in
the foreground — so a workload that prompts can actually read input — and the
terminal then delivers `SIGINT` to the **workload**, not to `dev`. `dev` sees
nothing at all. So the evidence there is the handover plus the signal:
`SIGINT` is Ctrl-C and `SIGHUP` is the terminal going away, and neither is a
workload failing.

Either way a developer pressing Ctrl-C has not produced a behavioral
regression and has not crashed anything: they stopped watching. The evidence
collected up to that point is real, so the run is completed and the banner
says who ended it. Any other signal nobody forwarded means something else
killed the workload, and that is a failure.

`SIGQUIT` — Ctrl-\ — is deliberately **not** treated as stopping: its
convention is "stop and dump state because something is wrong", which is a
different statement from "I have seen enough".

**One consequence of the terminal handover is worth knowing.** While the
workload runs with the terminal, `dev` is in a background process group, so a
*second* Ctrl-C during teardown is delivered to the workload's group and not
to `dev`. Teardown therefore always runs to completion once the workload has
exited; it cannot be interrupted from the keyboard. It is bounded rather than
unbounded — each helper gets a grace period and is then killed — so this is a
short wait, not a hang. Without a terminal, a second Ctrl-C does reach `dev`
and is forwarded.

`dev` is **not supported on Windows** and refuses with exit `2`. It forwards
`SIGINT` and `SIGTERM` to the child and Windows has no equivalent delivery, so a
partial implementation would leave orphaned processes; see
[task 077](tasks/v1.0/077-unified-otlp-local-dev-runtime.md). The refusal names
WSL2 and
[Local development § Running the parts separately](local-development.md#running-the-parts-separately),
which is the supported path there and everywhere else `dev` is not wanted.

`dev` also refuses, before starting anything, when it cannot establish who owns
the workload's instrumentation. Absence of detectable instrumentation never
selects injection, and that is a stability promise rather than a current
limitation: a future release may add evidence to the list or implement
`python-zero-code`, and neither may turn a refusal into an injected second
instrumentation stack. See
[ADR 0044](adr/0044-instrumentation-ownership-requires-positive-evidence.md).

For the control-plane families, an API failure is always `3` and never
`1`. A 409, a 404, a 500, a timeout, a refused redirect and a malformed
response are all operational. This distinction is the point: a CI script
written as "non-zero means the gate failed" must not report a broken
network as a policy violation.

Within the `analyze`/`baseline` family, note that only top-level
dispatch — no arguments, or an unknown command — produces `2`. A
subcommand's own usage error (a missing file argument, an unknown flag)
has always produced `1`, and still does. That is released behavior, and
task 060 pinned it with tests rather than tidying it.

Human-readable output is not automation-facing. It carries no stability
promise, and parsing it is not a supported integration.

The control-plane commands do have a machine-readable mode: `--json`
writes the API's own successful response body to stdout, and the API's
error envelope to stderr. A successful response must be a syntactically
valid, non-empty JSON body in both output modes; a `2xx` carrying
anything else exits `3` rather than being forwarded as a result. Those fields inherit the `/v1` API contract
rather than defining a second field namespace — the CLI adds no wrapper
and removes no field, so a client must tolerate additive fields exactly
as an HTTP client would.

`trustvian env list` and `trustvian promotion list` are the commands this
describes imprecisely, and the exception is stable rather than incidental.
Neither is one request:
the collection route is bounded per response, so the command follows
every `next_after` and then emits **one** document for the completed
collection — never the first page alone, and never one document per
page. That document is the first page's envelope with `environments`
(respectively `promotions`) replaced by every row of every page in traversal
order and `next_after` removed, because the traversal finished. Rows and unrecognized top-level
fields are forwarded as received, so the additive-field rule above holds
at both levels. A `version` or `project_id` that changes mid-traversal
is an operational error (`3`), not a merge. Results go to stdout and diagnostics to stderr,
including on a gate FAIL, where the comparison evidence is still written
to stdout.

`analyze` and `baseline` still have no structured output mode; automation
that needs structured engine results uses the Go SDK. Adding one would be
additive and allowed in a minor; it is listed under
[public API review outcome](#public-api-review-outcome).

The control-plane commands accept no positional arguments. A trailing
argument is a usage error (`2`) raised before any request is sent, so an
invocation typo cannot be mistaken for an API or gate result. Legacy
`analyze`, `baseline` and `version` keep their existing operand parsing.

### Platform persistence backends

[Task 064](tasks/v1.0/064-postgresql-platform-backend.md) added PostgreSQL
alongside SQLite. Backend selection is a deployment concern and appears in none
of the contracts above.

| Surface | Class | Guarantee | May change | Breaking |
|---|---|---|---|---|
| Backend selection names (`sqlite`, `postgres`) | OPERATIONALLY STABLE | The accepted values and that omitting one means SQLite | New backend names | Major |
| `TRUSTVIAN_PLATFORM_POSTGRES_DSN` | OPERATIONALLY STABLE | `trustvian-local` reads the PostgreSQL DSN from it | An additional configuration source | Major |
| `platform.SchemaVersion` | INTERNAL | One logical version governs both physical schemas; currently **9** | Incremented with a migration on **both** backends | n/a |
| Physical SQLite schema | OPERATIONALLY STABLE | Migrated forward only; a newer schema fails closed | Additive tables and columns via a version bump | Major |
| Physical PostgreSQL schema | OPERATIONALLY STABLE | Same | Same | Major |

`/v1` behaviour is identical on either backend, and no response says which
database answered. PostgreSQL internals — SQLSTATE, constraint, table and column
names, raw SQL, the DSN, host or username — never reach a caller: they map to
the same persistence sentinels the SQLite store already used.

A deployment sharing one PostgreSQL database between processes shares
authoritative state but **not** realtime notifications; each process keeps its
own in-process bus. That is a known limitation of task 064, not a defect, and
task 069 owns cross-node realtime.

**Retained observations index a digest, not the value.** `fingerprint_key`,
`trace_key` and `session_key` hold hex SHA-256 of the column beside each, and the
three correlation indexes key on those. PostgreSQL refuses a B-tree entry over
roughly 2704 bytes at `INSERT`, and none of the three values has a length bound
the ingest path enforces — so indexing the value would turn a record the platform
already accepts into a failed ingest, on PostgreSQL and not on SQLite. The key
columns are derived, never caller-supplied, and are verified against their values
on read. **Any future lookup through one of these indexes must compare the
original value as well as the key**, because a digest narrows rather than
identifies.

**Schema 9 (task 078) adds persisted scenario executions and invents none.**
Two tables on both backends, forward-only: `platform_scenario_executions`
(identity, scope, N, reference provenance, lifecycle, verdict, completion
sequence) and `platform_scenario_repetitions` (side, index, run and profile per
repetition). Metadata only — no scenario command, environment value, gate limit
or content. Existing runs are untouched and are not grouped into executions
after the fact, so `--reference last` on an upgraded database finds nothing
until an execution completes on it. A schema-9 stamp without its two tables is
refused as damage, and a binary that knows only schema 8 refuses a schema-9
database ([ADR 0054](adr/0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md)).

**Schema 8 (issue 131) adds the optional counted-change check to the promotion
history and invents no outcome.** Six columns on `platform_promotions`, on both
backends, forward-only. Every existing promotion reads back with the check
`not_recorded` — the state column's default, which says when the row was
written — and a NULL threshold, count, flag and counting context. Its stored
verdict and outcome are untouched, and no historical decision is re-evaluated.
A binary that knows only schema 7 refuses a schema-8 database rather than
reading it without the new columns.

**Schema 7 (task 067) adds per-observation history and no row.** The step is
forward-only on both backends and creates two tables and three run-scoped
indexes. Runs that existed beforehand keep every byte of their aggregate and
behavior evidence and gain **no** observations: their history reports
`unavailable`, which is a different statement from an empty history and is the
reason no backfill is attempted. A run that was mid-ingest when the upgrade
happened and then continues reports `partial`, because its earliest records were
never retained. Retention is bounded at 4096 observations per run, after which
the run keeps ingesting and reports `partial` — saturation degrades the
historical evidence and never the decision evidence. There is no age-based
expiry: history is deleted with its run.

### Web control plane

The local runtime serves a browser UI from the same listener as the API
([task 063](tasks/v1.0/063-minimal-web-control-plane.md)).

| Surface | Class | Guarantee | May change | Breaking |
|---|---|---|---|---|
| `/` and the WebUI static asset paths | OPERATIONALLY STABLE | The local runtime serves a browser UI at its root origin | Asset filenames, their number and their contents | Major |
| WebUI visual layout, DOM structure, CSS classes, element IDs | OBSERVATIONAL | Nothing | Anything, in any release | Never |

The page's markup is **not** an interface. Automation reads `/v1`, which is the
machine contract every client shares; nothing should scrape the DOM or depend on
a class name. This is the same classification the TUI's rendered dashboard
carries, for the same reason.

Task 063 changes no `/v1` route, field, status code or error envelope, and no
realtime protocol semantics. It adds no CORS header, no authentication, and no
discovery field — the WebUI origin *is* the API origin, so `runtime.json`
remains two fields.

`trustvian tui --run-id <id> [--api-url <url>]` is intended operational
surface: the command name, both flag names, and the exit contract above are
stable. Its **rendered dashboard is not** — layout, spacing, column widths,
row formatting, help wording and connection-status wording are observational
and may change in any release. Automation reads the `/v1` API, which is the
machine interface; nothing should parse the terminal.

The control-plane commands and `tui` accept `--api-url`. It is **no longer
required**: when a local runtime is running in the working directory, they read
its endpoint from `.trustvian/runtime.json` instead. This is additive —
existing scripts that pass `--api-url` behave exactly as before, and an
explicit URL is never overridden by a file.

Omitting `--api-url` with no local runtime is **operational** (`3`), not usage
(`2`): after task 062 the invocation itself is valid and the environment is
what failed. An explicitly malformed `--api-url` remains usage.

*Omitting* means absent from the command line. `--api-url ""` is an explicit
empty endpoint: usage (`2`), and no discovery file is read. The distinction is
load-bearing for CI, where an empty value is an unset variable rather than a
request to use whatever runtime the checkout contains.

### Local runtime discovery file

`.trustvian/runtime.json` is written by the local runtime and consumed by the
shipped CLI and TUI, so its schema is operational surface even though the file
itself is ephemeral:

| Field | Class | Meaning |
|---|---|---|
| `version` | OPERATIONALLY STABLE | Schema version; currently `"1"` |
| `api_url` | OPERATIONALLY STABLE | Base URL of the running local control plane |

Additive fields may be added within version 1 and clients must tolerate them.
The meanings of existing version-1 fields do not change; a new meaning requires
a new version. An **unknown version fails closed** — a client that does not
understand the file refuses it rather than guessing.

The file must be **exactly one** JSON document under **4 KiB**, measured on the
bytes read. A second document, trailing content after the first, or anything
past the bound is refused outright rather than parsed as far as it goes.

The port itself is **not** stable: the runtime binds an ephemeral port and
republishes it on every start. Nothing should record or hard-code it, which is
the reason discovery exists.

`trustvian-local` is a repository-internal executable, not a released artifact.

 No default address or
port is defined yet, and no environment variable is read — task 062 owns
integrated local startup and may add a default then. Adding one is
additive; changing one scripts depend on would not be.

## Environment variables

Only variables read by a shipped binary or by the reference deployment
are operational surface:

| Variable | Class | Role |
|---|---|---|
| `TRUSTVIAN_POSTGRES_DSN`, `TRUSTVIAN_POSTGRES_USER`, `TRUSTVIAN_POSTGRES_PASSWORD`, `TRUSTVIAN_POSTGRES_DB` | OPERATIONALLY STABLE | Reference-deployment storage credentials |
| `TRUSTVIAN_HEALTH_PORT`, `TRUSTVIAN_OTLP_GRPC_PORT`, `TRUSTVIAN_POSTGRES_HOST_PORT` | OPERATIONALLY STABLE | Reference-deployment port mapping |
| `TRUSTVIAN_RUNTIME_POSTGRES_DB` | OPERATIONALLY STABLE | Database the runtime uses after a restore cutover |
| `TRUSTVIAN_IMAGE_REGISTRY` | OPERATIONALLY STABLE | Registry override for image resolution |
| `TRUSTVIAN_DEMO_*` | OBSERVATIONAL | Demo producer only; not a production interface |
| `TRUSTVIAN_TEST_*`, `TRUSTVIAN_WORKFLOW_DIR`, `TRUSTVIAN_ACTION_REF_RESOLVER` | INTERNAL | Test and CI plumbing; may change at any time |

## Collector processor

The processor type name and its configuration keys — `policy`,
`storage`, `health`, `evaluation` — are an operational contract: a Collector
configuration that works on `v1.0` works on every later `v1.x`.

Trustvian guarantees its own keys only. Behavior inherited from the
OpenTelemetry Collector, including how the Collector itself parses and
validates configuration, is not Trustvian's to promise.

## Policy format

Schema and semantics are separate promises, and both hold within `v1`:

- **Schema** — rule and condition field names, decision values, and the
  required `DefaultAction`/`DefaultReason`.
- **Semantics** — first-match-wins evaluation order, and fail-closed to
  `BLOCK` when a policy is empty or invalid. This is a security
  property, not an implementation detail: it does not change in a minor
  or a patch.

New decision values may be added in a minor, so a consumer that switches
on `Decision` needs a default branch.

## Persisted state

| Question | Answer |
|---|---|
| Can state written by `v1.x` be read by a later `v1.y`? | **Yes.** Within a major version, later binaries read earlier state |
| Can a later `v1.y` read a `v1.x` backup? | **Yes**, by the same rule |
| Is downgrade supported? | **No.** Migrations are forward-only, and an older binary meeting a newer schema version fails closed rather than guessing |
| Are migrations forward-only? | **Yes** |

Storage carries its own version independently of the release number:
PostgreSQL `SchemaVersion = 2`, file snapshot `version: 2`. **Any change
to the stored `Baseline` shape bumps the storage schema version whatever
the release number does.**

Both moved from 1 to 2 for learning scopes
([ADR 0024](adr/0024-learning-scope-is-a-baseline-key-dimension.md)).
Version 1 is read and upgraded in both backends, and every pre-scope
baseline lands in the default scope with its learned state unchanged —
nothing is invented and nothing moves between scopes.

The bump was required even though the change looks additive, and the reason
generalizes: `Scope` changes what a persisted record *identifies*. One
version-2 snapshot can hold `(scope A, actor X, prod)` and `(scope B, actor
X, prod)`; a version-1 reader ignores the unknown field, sees two baselines
with the same key, and keeps whichever it loads last. **An "additive field"
that can make two distinct logical identities look like one is not
additive**, and a version that only *might* be misread is treated as one
that will be.

An unrecognized version is still fatal and is not auto-upgraded, because
silently rewriting a layout written by another version is how state gets
corrupted. `ErrSchemaVersionMismatch` and `ErrAmbiguousSchemaState` exist to
make the operator decide. Downgrade is not supported: an older binary
refuses version-2 state rather than collapsing scopes, which is the intended
outcome — recovery is restoring a pre-upgrade backup.

One consequence of bounded fingerprint admission
([ADR 0019](adr/0019-bounded-fingerprint-admission.md)) belongs here: a
baseline written before the bound may hold more identities than the
current cap. It is read whole, never truncated, and this stays true for
the life of `v1`.

## Database schema

- **Additive** — a new nullable column or a new table may land in a
  minor, with the schema version bumped and migration applied
  transactionally.
- **Destructive** — dropping or repurposing a column or table is a major
  change, and needs migration guidance in the release notes.
- **Rollback** — not supported. Restore from a backup taken before the
  upgrade; see [Operations](operations.md).
- **Sequencing** — upgrade the schema before, or as part of, starting
  the new binary. Running a new binary against an old schema fails
  closed at startup rather than degrading.

## Metrics

Metric names, types, and label keys are dashboard contracts. Today:
`trustvian.analyses`, `trustvian.decisions`, `trustvian.observations`,
`trustvian.analysis.duration`, `trustvian.observe.duration`, with the
label keys `trustvian.decision` and `trustvian.outcome`.

- Adding a metric or a label key: minor.
- Adding a label *value*: minor. Consumers must tolerate values they do
  not recognise — unknown decisions are deliberately folded into a
  bounded set rather than passed through.
- Removing or renaming a metric: deprecate first, remove in a later
  minor.
- Removing or renaming a label key: major.

Label cardinality stays bounded by construction. No metric will gain an
actor, session, or fingerprint identifier as a label — that is a
security property, not a performance preference.

## Health and readiness

`/livez` and `/readyz`, and their status semantics, are an orchestration
contract: `200` when live or ready, `503` when not ready or draining.
Response bodies are diagnostic and carry no stability promise.

## Webhook payload

Payloads are versioned. The envelope carries `version`, currently `"1"`,
alongside the `alert` object.

Within envelope version `1`: existing field names and meanings do not
change, and new fields may be added — so a receiver must ignore fields
it does not recognise. A change that would break an existing receiver
ships as a new envelope version, not as a mutation of version `1`.

## Container and release artifacts

The entrypoint is `trustvian-collector`; the image runs as `nonroot` and
exposes `4317`, `4318`, `13133`.

`ghcr.io/trustvian/trustvian-collector:vX.Y.Z` is immutable. `latest`
and `X.Y` are convenience aliases that move on every stable release, and
carry no promise beyond pointing at a signed image — production
deployments should pin the immutable tag.

Release artifacts are named `trustvian_<version>_<os>_<arch>` with a
`.tar.gz` or `.zip` extension, published alongside `checksums.txt`, an
SBOM, provenance attestations, and a Cosign signature. The naming
pattern and the presence of those files are stable; the internal layout
of an archive is not.

## GitHub Action

`.github/actions/trustvian-run` and `.github/actions/trustvian-comment` are the
two jobs of [task 079](tasks/v1.0/079-ci-integration-github-action.md),
documented in
[Running behavioral scenarios in GitHub Actions](ci-github-action.md). Each is
pinned by commit, so a caller only ever gets the version they named; the
classes below say what a newer commit may change.

The run action:

| Surface | Class | Promise |
|---|---|---|
| Inputs `scenario`, `suite`, `reference`, `scenario-timeout`, `fail-fast`, `api-url`, `working-directory`, `artifact-name` | OPERATIONALLY STABLE | Keep their names and meanings; each maps to the CLI flag of the same name, or is the action's own (`working-directory`, `artifact-name`) |
| Outputs `exit-code`, `result`, `head-sha`, `artifact-id`, `runtime-commit` | OPERATIONALLY STABLE | `exit-code` is exactly the CLI's code, empty when it did not run; `result` is one of `present`, `absent`, `invalid`, `oversized` |
| Final exit status | OPERATIONALLY STABLE | Exactly the CLI's code whenever the CLI ran; the [`eval run` row](#cli) defines the codes |
| Artifact files `result.json` and `trustvian-run.json` | OPERATIONALLY STABLE | `result.json` is the CLI's stdout, byte for byte, present only when it is one JSON object within 32 MiB + 64 KiB |
| `trustvian-run.json` fields (`version` `"1"`) | OPERATIONALLY STABLE | Fields keep their names and meanings; new fields may appear and consumers must tolerate them |
| Refusal of `pull_request_target` and `workflow_run` | OPERATIONALLY STABLE | Never relaxed by a minor |
| The pinned runtime commit and Go version (`runtime.env`) | OBSERVATIONAL | Change with the action's commit; the artifact records which were used |
| Job summary wording and layout | OBSERVATIONAL | Head commit, run link, exit code and artifact availability are present; nothing else is promised |

The comment action
([ADR 0058](adr/0058-the-comment-job-is-a-separate-action-that-posts-from-pinned-source.md)):

| Surface | Class | Promise |
|---|---|---|
| Inputs `exit-code`, `artifact-name`, `marker-id`, `github-token` | OPERATIONALLY STABLE | Keep their names and meanings. `exit-code` is required, and empty means the CLI did not run. `marker-id` defaults to `artifact-name`. `github-token` defaults to `github.token`, and only that token is supported, because ownership is `github-actions[bot]` authorship |
| Outputs `renderer-exit`, `posted`, `outcome` | OPERATIONALLY STABLE | `renderer-exit` is `trustvian-ci-render`'s code. `posted` is `true` or `false`. `outcome` is one of `created`, `updated`, `superseded`, `not-permitted`, or empty when nothing was posted |
| What it posts | OPERATIONALLY STABLE | Renderer exits `0`, `1` and `3` are posted, so a no verdict replaces a stale verdict; `2` posts nothing and fails the job. A refused write (a fork) succeeds with a warning and a summary note. Only an API failure or a usage error fails the job |
| Built from the pinned source only, never from `$GITHUB_WORKSPACE` | OPERATIONALLY STABLE | Never relaxed by a minor |
| Refusal of `pull_request_target` and `workflow_run`, and of any event but `pull_request` | OPERATIONALLY STABLE | Never relaxed by a minor |
| Job summary | OBSERVATIONAL | Carries the rendering, plus the poster's note when it could not post |

`cmd/trustvian-ci-render` renders a downloaded artifact as Markdown, offline
([ADR 0057](adr/0057-the-ci-renderer-is-a-standalone-offline-transcriber.md)).
The comment action runs it.

| Surface | Class | Promise |
|---|---|---|
| Flags `--artifact-dir`, `--head-sha`, `--repository`, `--run-id`, `--run-attempt`, `--exit-code`, `--server-url` | OPERATIONALLY STABLE | Keep their names and meanings; an empty `--exit-code` means the CLI did not run |
| Exit codes | OPERATIONALLY STABLE | `0` evidence rendered, `1` no verdict, `2` usage (nothing rendered), `3` no verdict because the artifact was rejected |
| What it accepts | OPERATIONALLY STABLE | `trustvian-run.json` and the `eval run` result document at version `"1"`, each as this page defines them. Unknown fields are tolerated and never rendered; an unknown version, or a value outside a closed vocabulary, renders no verdict |
| Markdown wording, layout and ordering | OBSERVATIONAL | A verdict names the head commit, transcribes every behavior, all six checks, the thresholds and both producers; a no-verdict rendering carries the head commit, the run link and a reason, and no number from the result. Nothing else is promised |

`cmd/trustvian-ci-comment` posts a rendering as the pull request's one gate
comment ([ADR 0058](adr/0058-the-comment-job-is-a-separate-action-that-posts-from-pinned-source.md)).

| Surface | Class | Promise |
|---|---|---|
| Flags `--repository`, `--pull-request`, `--head-sha`, `--marker-id`, `--body-file`; `GITHUB_TOKEN` and `GITHUB_API_URL` | OPERATIONALLY STABLE | Keep their names and meanings; the token is never a flag |
| Exit codes | OPERATIONALLY STABLE | `0` posted, superseded, or not permitted (a warning); `1` the API failed; `2` usage |
| The marker line `<!-- trustvian-behavioral-gate:<id> -->` and ownership rule | OPERATIONALLY STABLE | A comment is the poster's only if `github-actions[bot]` wrote it and its first line is the marker |

The run job's summary carries no verdict. The rendering — in the comment job's
summary and in the comment — is OBSERVATIONAL, like CLI human-readable output:
anyone parsing it is parsing the wrong thing, and the result document is right
there.

## Behavioral compatibility

Type compatibility is not the whole contract. A change that keeps every
signature intact but alters what Trustvian *decides* is still a change
users feel.

| Kind of change | Treated as | Version |
|---|---|---|
| A signal computed incorrectly relative to its documented formula | Bug fix | Patch |
| A documented formula itself changing | Breaking semantic change | Major |
| A new signal shipped disabled by default (zero weight) | Additive | Minor |
| A new signal enabled by default | Breaking semantic change | Major |
| A documented default threshold or weight changing | Compatible tuning, if called out in CHANGELOG | Minor |
| Learning eligibility changing which decisions train the baseline | Breaking semantic change | Major |
| `Engine.Observe` reporting `learned` more accurately for the same input | Bug fix — it reported learning that did not happen | Minor, called out in CHANGELOG |
| Learned-state identity gaining a dimension whose default preserves existing lookups | Additive, with a storage-version bump | Minor |
| Fail-closed behavior becoming less strict | Never permitted without a major, and only with explicit security review | Major |
| `Event.Validate()` rejecting input it previously accepted | Breaking for a producer that sent it | Major, unless the input could not be processed correctly in the first place |

What is **not** promised: that a given event produces a numerically
identical score forever. Baselines are learned state, and scores move as
they learn — that is the product working. What is promised is that the
*rules* producing those scores do not change silently.

When in doubt, ask whether an operator's existing policy would start
making different decisions on unchanged traffic. If yes, treat it as
breaking regardless of what the type signatures say.

## Deprecation

Version-based, not time-based, because an OSS project cannot promise a
calendar:

1. Mark the surface deprecated in its own documentation and in
   `CHANGELOG.md`, naming the replacement.
2. Where the language allows it, make the deprecation visible in the
   surface itself — a Go doc comment beginning `Deprecated:`, a CLI
   warning on stderr, a documented note on a metric.
3. Keep it working for **at least one subsequent minor release**.
4. Remove it only at a boundary the matrix permits.

A surface that never shipped in a stable release needs none of this.

## Security exception

A severe vulnerability may require a change that cannot wait for a
deprecation cycle or a major release. That is permitted, and it is
deliberately narrow. Such a change must carry:

- an explicit security rationale in the release notes,
- a `CHANGELOG.md` entry identifying what changed and why,
- migration guidance wherever one is possible,
- and a `security:` commit, so it is visible in history.

This is an exception for fixing exploitable defects, not a route around
the contract. "It is cleaner this way" is not a security rationale.

## Public API review outcome

The public surface was reviewed against this contract before the `v1`
freeze. Four items were decided:

| Finding | Decision |
|---|---|
| `WithTrustConfig` had no public path — exported but uncallable from outside the module | Fixed: `config.TrustConfig` + `config.CompileTrust` |
| `WithContextRisk`'s callback named an internal type, so it could not be written externally | Fixed: the callback takes the public `StableFeatures` |
| Custom `Store` implementations | Not a v1 extension point; documented above |
| Store lifecycle via `io.Closer` | Caller-owned; documented above, no `Engine.Close` |

Two remain open by choice, neither blocking:

- **No machine-readable CLI output.** `analyze` prints a formatted
  summary; automation uses the SDK. Adding a structured mode later is
  additive and allowed in a minor.
- **An actor at the fingerprint admission bound** stops learning new
  identities with nothing surfacing it. A behavioral question, recorded
  in [ADR 0019](adr/0019-bounded-fingerprint-admission.md).

## Related

- [Branching Strategy](governance/branching.md) — SemVer and release flow
- [Release Governance](governance/releases.md) — who may release
- [Operations](operations.md) — upgrade, backup, and the compatibility matrix for state
- [CHANGELOG.md](../CHANGELOG.md) — where breaking changes and deprecations appear
- [Decision Records](adr/README.md) — why the architecture is shaped this way
