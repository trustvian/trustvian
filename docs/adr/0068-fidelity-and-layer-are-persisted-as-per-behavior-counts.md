# 0068 — Fidelity and layer are persisted as per-behavior counts

**Status:** Accepted, 2026-10-10 (task
[081](../tasks/v0.12/081-persisted-behavior-fidelity.md), maintainer decision
D6). Amends [ADR 0047](0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
§ 2 in one respect, stated below, and changes nothing else in it.

## Context

ADR 0047 § 2 made the behavior layer a non-identity classification that rides
where fidelity rides: the outbound span attribute, the ingest envelope and the
realtime observation. It is "never persisted, never fingerprinted, and never a
baseline key". Fidelity had the same limit, recorded in ADR 0045's
consequences. A comparison is built from persisted evidence, so neither reached
a comparison delta, a scorecard or the CI comment. The repeated gate could not
count model calls per run either, so task 106's `max_llm_calls_per_run` had to
wait (decision D3).

Two observations of one behavior can disagree about either fact, because
neither is a `StableFeatures` dimension. One span carried
`gen_ai.operation.name` and its retry did not, yet both mapped to the same
identity.

## Decision

1. **"Never persisted" becomes "persisted as per-behavior counts".** Each
   behavior entry stores, at schema 13 on both backends, how many of its
   observations stated each fidelity:
   - `semantic`, `transport` or `unrecorded`;

   and each layer:
   - `model`, `tool`, `retrieval`, `transport`, `unclassified` or
     `unrecorded`.

   Each group sums to the observations. The layer rule ties them:
   - `model + tool + retrieval + unclassified == semantic`;
   - `transport == transport`.

   Both rules are checked on write and on restore.
2. **Unchanged:** the layer is never fingerprinted and never a baseline key.
   There is no `model` operation category. Fidelity is likewise outside
   `StableFeatures`. A test pins both types' fields.
3. **The reported fidelity is the lowest level seen.** It is `transport` if
   any observation was transport, else `semantic` if any was semantic, else
   `unrecorded`. `mixed` is true when two counters are non-zero. Runs are
   summed first, then classified. The rule is implemented once, on the
   control plane.
4. **An envelope's pair is counted as stated only when the layer rule allows
   it:** `(transport, transport)`, or `semantic` with model, tool, retrieval
   or no layer.
   - **Contradictory pairs** are refused with `400`: transport with a
     semantic layer, or semantic with the transport layer.
   - **Partial pairs**, one field without the other, are counted unrecorded
     in both groups. The envelope contract makes the fields independently
     optional, and the Collector always sends a countable pair.

## Alternatives considered

- **Keep § 2 as written, persist fidelity only.** `max_llm_calls_per_run`
  would then stay deferred forever. And a second migration on the platform's
  widest table, later, would double the risk task 081 exists to contain.
- **Report the majority, or the latest, level.** A majority has no threshold
  anyone chose and needs an invented tie-break. The latest depends on ingest
  order, so two backends could disagree.
- **Refuse every partial pair.** That would keep the contract tests honest only
  by breaking them: a client sending only `fidelity`, which ADR 0045's own
  tests do, would start receiving `400`.

## Consequences

- Comparison deltas and repeated-comparison rows carry per-side fidelity, with
  the counts beside the level, so "999 semantic, 1 transport → transport,
  mixed" is visible rather than surprising.
- A behavior recorded before schema 13 reads `unrecorded`, never `transport`.
  Nothing is backfilled: task 067 never retained fidelity.
- The four contradictory pairs, accepted before schema 13, are now refused.
  No Trustvian producer sends one.
