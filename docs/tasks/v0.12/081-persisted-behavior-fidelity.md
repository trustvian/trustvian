# 081 — Persisted Per-Behavior Fidelity

Status: Specified; not implemented
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [075](../v1.0/075-ai-semantic-telemetry-normalization.md) (implemented),
[083](../v1.0/083-behavioral-layer-classification.md) (implemented)
Decision records: [ADR 0045](../../adr/0045-conventions-are-read-frameworks-are-not.md)
(consequences), [ADR 0047](../../adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
§ 2, which this task proposes to amend (see
[§ Layer is persisted too](#layer-is-persisted-too-amends-adr-0047--2))
Blocks: 106's `max_llm_calls_per_run`; 107's fidelity column
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
version, with its per-behavior `operational_counts` column. 081 takes the next
free version when it lands, which is v12 if 086 has not landed first, and v13
if it has.

## Risks

- **This is the milestone's migration risk.** It adds columns to the widest
  table the platform has, on both backends, and it is the second step to touch
  that table if 087 lands first. The drill and the restore-time invariant checks
  are what stand between a bug and a damaged database.
- **"Lowest seen" will sometimes surprise.** A behavior that was semantic 999
  times and transport once reads `transport, mixed`. That is correct, and the
  counts show why.
