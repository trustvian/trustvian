# 081 — Persisted Per-Behavior Fidelity

Status: Implemented — see [What shipped](#what-shipped)
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [075](../v1.0/075-ai-semantic-telemetry-normalization.md) (implemented),
[083](../v1.0/083-behavioral-layer-classification.md) (implemented)
Decision records: [ADR 0045](../../adr/0045-conventions-are-read-frameworks-are-not.md)
(consequences), [ADR 0047](../../adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
§ 2, which this task proposes to amend (see
[§ Layer is persisted too](#layer-is-persisted-too-amends-adr-0047--2))
Blocks: `max_llm_calls_per_run` (specified here since D3); 107's fidelity column
Planned in: [ROADMAP § Milestone sequence](../../ROADMAP.md#milestone-sequence),
*"081 is a deferral, not a discovery"*

## Developer problem

A comparison says `+ POST → api.example.com` was added. The developer cannot
tell whether that is a new HTTP call, or a model or tool call the producer
named, which Trustvian can only see as HTTP. The live view knows, because
fidelity rides on the envelope and the realtime observation. The comparison,
the scorecard, the CI comment and 107's Change Impact table do not know,
because fidelity is never stored.

## Why this is a deferral

Task 075 put fidelity everywhere it needs no storage: the outbound span
attribute, the ingest envelope, the realtime observation and the WebUI.
Carrying it on a comparison delta needs it persisted per behavior. That is a
forward-only schema step in both backends, plus the backup, restore and upgrade
path. It was kept separate on purpose. A migration bug damages a user's
database, and the rule for a behavior whose observations disagree about fidelity
is a decision to be made rather than one that happens by accident.
`TestFidelityIsNotPersistedYet` records the gap (`platform/httpapi/fidelity_test.go:190-217`).
It POSTs a comparison and fails if the body contains `"fidelity"`.

**This task stays in its own pull request**, with nothing else in it, for the
same reason.

## Current state, verified against `main` (`d1ad5f6`)

| Fact | Evidence |
|---|---|
| Fidelity is `transport` or `semantic`, a closed set | `internal/semconv/fidelity.go:26-30` |
| Layer is `model`, `tool`, `retrieval`, `transport` or unclassified, and is claimed exactly when fidelity is `semantic` | ADR 0047 § 2; `TestLayerIsClaimedExactlyWhenFidelityIsSemantic` |
| Both ride on the envelope | `processor/internal/evaluation/client.go:255-279` |
| A behavior entry stores `FingerprintID`, descriptors and `Observations` | `platform/behavior.go:90-101`; `platform_behavior_entries` (`platform/sqlite.go:1332-1352`) |
| Neither fidelity nor layer is stored | the test above; ADR 0047 § 2: "never persisted" |
| Schema version | **10** on both backends (`platform/sqlite.go:38`, `platform/postgres_schema.go:5`) |

## The disagreement rule

A fingerprint is computed from `StableFeatures`, and fidelity is not one of its
dimensions (`fidelity.go:17-20`). So two observations of **one** behavior can
disagree about fidelity. For example, one span carried `gen_ai.operation.name`
and the next, from a retry path, did not. Both mapped to the same identity only
because the transport span's descriptors matched.

**Rule: record the count at each level, and report the lowest level seen.**

```text
fidelity_semantic     uint64   observations with fidelity = semantic
fidelity_transport    uint64   observations with fidelity = transport
fidelity_unrecorded   uint64   observations with no fidelity on the envelope
                               (the in-process SDK path, or records ingested
                               before this step)

reported fidelity     transport  if fidelity_transport > 0
                      semantic   if fidelity_transport = 0 and fidelity_semantic > 0
                      unrecorded otherwise
                      — plus "mixed": true whenever two counters are non-zero
```

Invariant: the three counters sum to the entry's `Observations`. It is enforced
on write and on restore, as 084's operational invariants are.

**Why the lowest, and not the majority or the latest:**

- **Fidelity is a claim about evidence quality, and the claim must hold for every
  observation it covers.** "This behavior was named by telemetry" is true of a
  behavior only if every observation was named. One transport observation means
  some of what this identity counted was inferred. Reporting `semantic` would
  overstate the evidence. That is the direction 075 forbids:
  `TestFidelityNeverExceedsTheEvidence`.
- **A majority is order-independent but has no threshold anyone chose**, and a
  50/50 split needs a tie-break invented here.
- **The latest observation depends on ingest order**, and two backends ingesting
  the same records must agree (criterion 6).
- **The counts keep everything else.** A developer who wants to know "mostly
  semantic, one transport" reads the counters, which are always shown next to
  the reported level.

Across runs, such as N repetitions or a reference against a candidate, the same
rule applies to the summed counters. The control plane sums and classifies. No
adapter does.

## Layer is persisted too (amends ADR 0047 § 2)

ADR 0047 § 2 says layer "is never persisted, never fingerprinted, and never a
baseline key". Task 106's `max_llm_calls_per_run` needs model-layer calls
counted per run, which needs layer per behavior persisted. Layer is claimed
exactly when fidelity is `semantic`. That makes it the same kind of fact,
arriving on the same envelope, with the same disagreement problem. Persisting it
in a second migration on the same table would double the migration risk this
task exists to contain.

**Proposed:** this step also persists layer counts (`layer_model`, `layer_tool`,
`layer_retrieval`, `layer_transport`, `layer_unclassified`, `layer_unrecorded`),
with the same sum invariant. A new ADR amends ADR 0047 § 2 from "never
persisted" to "persisted as per-behavior counts, never fingerprinted, never a
baseline key". The parts of § 2 that matter for detection (no identity change,
no `model` category) are unchanged.

If a human prefers to keep ADR 0047 § 2 as written, the layer columns are
dropped from this task, and `max_llm_calls_per_run` stays `deferred`
indefinitely (106 § 2).

## `max_llm_calls_per_run` moved here from 106 (maintainer decision D3)

The limit needs persisted layer evidence, and this task persists it. Shipped
with 106, it would have made every scenario that configured it fail as
`deferred` until this task landed. So it is specified here, and the scenario
parser rejects the field as unknown until then. 106's file records the move.

| Limit | Check value | Passes when | Evidence it needs |
|---|---|---|---|
| `max_llm_calls_per_run` | max over candidate runs of model-layer calls in one run | value ≤ limit | the per-behavior `layer_model` counts this task persists |

It follows 106's other optional limits exactly:

- **Optional.** Omitted means `not_evaluated`, and the verdict is unchanged.
- **Integer.** It reads the per-run maximum, never a sum or a mean.
- **One more `gate.frequency_checks` entry**, after 106's three.
- **Deferred when evidence is absent.** A run whose layer counts are all
  `layer_unrecorded` or `layer_unclassified` leaves the check `deferred`, which
  fails the verdict and names the missing evidence (D1). It is never computed
  from a substitute such as category `external`.

Tests (moved from 106):

- omitted → `not_evaluated`, satisfied, violated, and layer evidence absent →
  `deferred` → FAIL;
- a run migrated from before this schema step reads `deferred`, never `0`;
- the scenario parser accepts `gate.max_llm_calls_per_run` with 106's
  parse-time validation, and refuses it as unknown before this task.

Acceptance (moved from 106 criteria 3 and 4): `max_llm_calls_per_run` is
optional, named and integer, is reported as evaluated, not evaluated or
deferred, and is accepted by the scenario file with the existing parse-time
validation.

## Scope

1. **One forward-only schema step**, on both SQLite and PostgreSQL, adding the
   counters above to `platform_behavior_entries`. Existing rows migrate as
   `fidelity_unrecorded = observations` and `layer_unrecorded = observations`,
   with every other counter set to `0`. That is the honest reading, following
   084's precedent, and it keeps the invariants true for migrated rows.
2. **Ingest** updates the counters from the envelope's `fidelity` and
   `behavior_layer` in the transaction that already writes the entry. An
   envelope without them increments `unrecorded`. A malformed value is refused
   at ingest, as it is today. It is not counted as unrecorded, because that would
   let a submitter erase its own evidence by corrupting it (084's rule).
3. **The comparison delta carries fidelity.** `BehaviorDelta` and its wire DTO
   gain per-side `fidelity` (the reported level, `mixed`, and the three counts).
   The repeated comparison's behavior rows carry the same, summed over the
   side's runs.
4. **`TestFidelityIsNotPersistedYet` is inverted.** It is replaced by a test
   that a delta reports fidelity with its counts, and the old test's assertion
   is deleted together with its doc comment, as the comment itself asks.
5. **Backup, restore and upgrade.** The automated recovery drill is extended. A
   v-previous backup restores into a binary at this step and migrates, and a
   backup taken at this step restores and verifies the invariants on both
   backends.

## Non-goals

- **No identity change.** Fidelity and layer stay out of `StableFeatures`, the
  fingerprint and the baseline key. A test proves it.
- **No per-observation fidelity in 067's history.** That is a separate change,
  and 076's narrowing stays as recorded.
- **No backfill from retained observations.** 067 does not retain fidelity, so
  there is nothing to backfill from. Old rows say *unrecorded*.
- **No new fidelity level** and no new layer value.

## Acceptance criteria

1. Schema step applied forward-only on both backends, refusing a newer stamp
   and a damaged one, as every earlier step does.
2. The disagreement rule is implemented once, on the control plane, and tested
   for all-semantic, all-transport, mixed, unrecorded and migrated entries.
3. A comparison delta reports fidelity per side with its counts, and
   `TestFidelityIsNotPersistedYet` is replaced by that positive assertion.
4. A reported fidelity never exceeds the evidence: one transport observation
   makes the behavior `transport`.
5. The counters sum to observations, on write and on restore.
6. SQLite and PostgreSQL return identical results.
7. The recovery drill covers the step in both directions it supports (upgrade,
   and restore at the new version).
8. A producer that emits no convention gets byte-identical behavioral results.
   Its behaviors read `transport`, as today's live view does.

## Schema numbering

The brief names this step **schema v11**. The roadmap rule (082 § *Migrations
stay forward-only and drilled*) is that each step takes the next number in
landing order, stated in its own specification "rather than assuming a number".
Under the proposed phase order, 087 (per-behavior operational columns) and 086
(execution digests) both land first and both need a step. 081 would then be
**v13**, not v11. Two ways to reconcile it:

- **Keep the phase order and number in landing order** (proposed). The number in
  this file is filled in when the step lands.
- **Move 081 to the front of Phase 2** so it takes v11. It still stays alone in
  its own pull request.

A human decides. The specification does not depend on the number.

**Landed (maintainer decision D4):** task 087 took **v11**, the next free
version, with its per-behavior `operational_counts` column. Task 086 then took
**v12**, ten nullable provenance columns on `platform_scenario_executions`. 081
takes the next free version when it lands: **v13**.

## Risks

- **This is the milestone's migration risk.** It adds columns to the widest
  table the platform has, on both backends, and it is the second step to touch
  that table if 087 lands first. The drill and the restore-time invariant checks
  are what stand between a bug and a damaged database.
- **"Lowest seen" will sometimes surprise.** A behavior that was semantic 999
  times and transport once reads `transport, mixed`. That is correct, and the
  counts show why.

## What shipped

Branch `feat/081-persisted-behavior-fidelity`, alone in its pull request.
Schema **v13**. The engine, `DecisionRecord`, `StableFeatures`, fingerprints
and baseline keys are unchanged.

### Maintainer decisions applied

- **D6 — layer is persisted with fidelity, in the same step.**
  [ADR 0068](../../adr/0068-fidelity-and-layer-are-persisted-as-per-behavior-counts.md)
  amends ADR 0047 § 2 from "never persisted" to "persisted as per-behavior
  counts". The layer is still never fingerprinted, never a baseline key, and
  there is no `model` category. ADR 0047 carries a pointer, not a rewrite.
- **D7 — schema v13**, the next free version (087 took 11, 086 took 12).
- **D8 — `max_llm_calls_per_run` is deferred unless every candidate run has
  at least one observation with fidelity `semantic`.** With semantic evidence,
  the check value is that run's `layer_model` observations, its per-run
  maximum over candidate runs. *Superseded by D9.*
- **D9 — `max_llm_calls_per_run` is deferred unless every completed candidate
  run has at least one model-layer observation** (follow-up, after the third
  measured run below). A run with no model-layer observation cannot be told
  apart from one whose model calls were invisible: with tools named and model
  calls as plain HTTP, D8 evaluated the check to `0` and passed while the agent
  made 20 model calls a run. When the check evaluates, its value is therefore
  at least `1`; a reported `0` can no longer occur. The deferred output names
  the run as D8's did: the lowest identifier, and the count of runs lacking the
  evidence.

### Departures from the specification

| Specified | Shipped | Why |
|---|---|---|
| "A malformed value is refused" | The pair is checked as well as each value. **Contradictory** pairs are now refused with `400`: `transport` with `model`, `tool` or `retrieval`, and `semantic` with `transport`. **Partial** pairs (one field without the other) are accepted and counted `unrecorded` in both groups | The invariants `layer_transport == fidelity_transport` and `model+tool+retrieval+unclassified == semantic` cannot hold if every pair the contract accepts is counted as stated. Refusing partial pairs would break the published envelope contract, under which both fields are independently optional, and four existing contract tests send them. **Decision for a human: see below** |
| Delta carries "the reported level, mixed, and the three counts" | The same, and the six layer counts beside them, in one per-side `reference_fidelity` / `candidate_fidelity` object | `max_llm_calls_per_run` reads the layer counts, and a reader checking a deferred or evaluated check needs to see them. The brief's measurement asks for them on a comparison |
| Per-side fidelity on every delta | Omitted on a side that did not observe the behavior | A side with no observation has no fidelity. Rendering `unrecorded` there would claim an observation that never happened |
| Deferred "names the missing evidence" | Names the lowest run identifier lacking model-layer evidence (D9; semantic evidence under D8), and how many lack it | Order independence: the result must not depend on how the runs were listed |
| — | `scenario_digest` changed once for every scenario | 086's canonical gate encodes every optional limit, `null` when omitted. Adding `max_llm_calls_per_run` the same way, as instructed, moves every digest. Pinned by `TestTask081MovedEveryScenarioDigestOnce` |
| "The automated recovery drill is extended" | Platform drill tests, `TestPlatformDrillSQLite` and `TestPlatformDrillPostgres`, run in the *Backup, restore & upgrade* CI job | `scripts/backup-postgres.sh` and its drill cover only the runtime's baseline tables, and nothing drilled the platform database before. The v10 leg uses the real v0.11.0 `trustvian-local`, which CI builds from its tag. The v12 leg is a downgraded database, because no release ships v12 |
| One column or two (the brief's question) | One: `fidelity_counts`, nine counters | The two groups are one fact, the pair an envelope stated, and three invariants span them, so one decode validates them together. 087 measured that each extra column costs every ingest in the SQLite driver, and nothing reads these in SQL |

The disagreement rule is implemented once, as `BehaviorFidelity.Reported` in
`platform/behavior_fidelity.go`. It landed with the counters in the first
commit, not in a layer of its own.

### The invariant, checked against every envelope the code produces

`TestEveryEnvelopeStatesACountablePair` drives nine span shapes through the
Collector:
- a named tool, a model and a retrieval;
- plain HTTP, and a span with no attributes;
- a GenAI operation outside the enum, and a tool with no name;
- an OpenInference CHAIN, and an LLM span.

Every envelope is `(transport, transport)` or semantic with model, tool,
retrieval or no layer. The same holds for every released Collector: 075 and
083 shipped together in v0.10.0, and v0.9.0 had no platform ingest at all.
`trustvian eval ingest` sends neither field.

So **the invariant holds for every envelope Trustvian produces**. It would not
hold for every envelope the control plane accepts, which is why partial and
contradictory pairs are handled as above.

### Measured

**Ingest.** `BenchmarkIngestDecisionRecord` at 64 behaviors, six runs, against
`main` at `8d89421`:

| | Time | Bytes | Allocations |
|---|---|---|---|
| before | 1.93 ms | 790 KB | 10,510 |
| after | 2.00 ms | 786 KB | 9,684 |

The column first added about 860 allocations per ingest; see
[PERFORMANCE.md](../../PERFORMANCE.md) for where. Hoisting
`loadBehaviorEntries`' scan targets out of its row loop left fewer than before.

**Storage.** One 512-behavior run grew by 12,288 bytes after `VACUUM`. That is
at most 188 bytes per behavior.

**The demo agent on Ollama.** `trustvian-python-agent-demo`,
`opentelemetry-instrument`, `gemma3:4b`, `runs: 2`, the same agent on both
sides, `max_llm_calls_per_run: 50`. Quoted from the `--json` documents:

1. **Fully instrumented**, execution
   `scn-fidelity-081-full-20261009T234919-153cf6db`. The demo names its tools.
   A measurement-only harness outside the demo repository also names each
   Ollama request as a GenAI `chat` span, with names only. PASS.
   - `external/gemma3:4b` on each side: `semantic`, not mixed, 40
     observations, `layer.model` 40.
   - Each tool (`crm_lookup`, `knowledge_search`, `send_email`, …):
     `semantic`, `layer.tool` equal to its observations.
   - Each HTTP behavior, including `POST → ollama.localhost` (40):
     `transport`.
   - `max_llm_calls_per_run`: `{"state": "evaluated", "rule": "at_most",
     "actual": "20", "bound": "50", "passed": true}`. That is 20 model calls in
     the busier candidate run.
2. **No GenAI or OpenInference instrumentation**, execution
   `scn-fidelity-081-plain-20261009T235416-97558e8b`. FAIL, exit 1.
   - Every behavior `transport`, not mixed. `POST → ollama.localhost`
     transport 40.
   - `max_llm_calls_per_run`: `{"state": "deferred", "missing_evidence":
     "run \"scn-fidelity-081-plain-20261009T235416-97558e8b-candidate-1\" has no
     semantically named observation; model calls cannot be counted (2 of 2
     candidate runs have none)"}`. The verdict FAIL is that deferral (D1).
3. **The demo as shipped**, execution
   `scn-fidelity-081-shipped-20261009T235849-76ae1b7a`. Tools are named, model
   calls are plain HTTP. PASS.
   - `max_llm_calls_per_run`: `{"state": "evaluated", "rule": "at_most", "actual":
     "0", "bound": "50", "passed": true}`. Yet `POST → ollama.localhost` was observed 40
     times a side.
   - **D8 does not close this case.** The tool spans are semantic evidence, so
     the check evaluates, and model calls the producer never named read 0.
   - **D9 closes it.** The check now requires a model-layer observation in
     every candidate run, so this execution would read `deferred`, naming
     the candidate run with the lowest identifier, and fail. Execution 2's
     message now reads "has no model-layer observation". The runs above were
     not repeated after D9; the case is proven by test (below).

### Proven by test

- The fold, the disagreement rule and the invariants:
  - all-semantic, all-transport, mixed, unrecorded and migrated entries;
  - one transport observation in a thousand reads `transport, mixed`;
  - runs summed, then classified;
  - every invariant refuses a violation;
  - the stored form round-trips, refuses damage, and decodes without
    allocating.
- The migration:
  - v12 → v13 on both backends;
  - v10 → v13 in one upgrade on both, asserted in the v10 tests;
  - fresh and migrated tables have the same columns;
  - the SQL backfill equals the Go value;
  - six damaged rows are refused as corrupt.
- Ingest:
  - every pair the envelope can spell, counted or refused as above;
  - the counts survive a restart;
  - a refused pair writes nothing;
  - the in-process path stays unrecorded.
- Surfaces:
  - `TestComparisonsReportPersistedFidelity` replaces
    `TestFidelityIsNotPersistedYet`;
  - the byte-identity tests against `946a4fc` and `a1ee3fd` hold once the
    added members are removed;
  - SQLite and PostgreSQL agree on 106's golden, which now ingests real pairs;
  - the 079 renderer renders the fail, pass and suite documents carrying the
    new fields byte for byte.
- `max_llm_calls_per_run`:
  - omitted, satisfied, violated, the real maximum when every run has
    model-layer evidence, tools named with model calls as transport deferred
    (D9, `TestMaxLLMCallsPerRunStates` and `TestMaxLLMCallsPerRunOverHTTP`),
    transport-only deferred, migrated deferred, and one run of N lacking
    evidence deferring the whole check;
  - an evaluated check never reports 0 (D9);
  - deferral is order-independent and fails the verdict;
  - the scenario field validates and moves the digest.
- The drill: v10 (from v0.11.0) and v12 backups migrate, and a v13 backup
  restores with its counts, on SQLite and PostgreSQL.
- Identity: the same record under every countable pair keeps its fingerprint
  and entry, and the identity pin names 081.

### Current state rows found stale on `main` (`8d89421`)

| Row | Then (`d1ad5f6`) | Now |
|---|---|---|
| Fidelity's closed set | `fidelity.go:26-30` | `internal/semconv/fidelity.go:23-32` |
| The envelope | `client.go:255-279` | `processor/internal/evaluation/client.go:258-279` (`fidelity` at 273, `behavior_layer` at 279) |
| A behavior entry | `platform/behavior.go:90-101` | `90-107`; 087 added `Operational` |
| `platform_behavior_entries` | `platform/sqlite.go:1332-1352` | `platform/sqlite.go:1461-1472`, plus v11's `operational_counts` as an ALTER |
| Schema version | 10, `sqlite.go:38` and `postgres_schema.go:5` | 12 on `main`, 13 here. The constant is only in `sqlite.go:38`; `postgres_schema.go:5` is a comment |
| `TestFidelityIsNotPersistedYet` | `fidelity_test.go:190-217` | Held; replaced here |
| `TestLayerIsClaimedExactlyWhenFidelityIsSemantic` | — | `internal/semconv/layer_test.go:44`. Note it asserts semantic ⇔ `Layer.Valid()` on `Normalize`'s output, where a no-convention span has *no* layer. The Collector's `layerFor` then writes `transport`, so the pair the envelope carries is `(transport, transport)` |
