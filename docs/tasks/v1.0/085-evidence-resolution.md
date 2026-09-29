# 085 — Evidence Resolution: From a Finding to the Observations Behind It

Status: specified; implemented
Milestone: `v1.0` — [criterion 17](../../ROADMAP.md#v10-exit-criteria)'s *"and explained"*
Depends on: [067](067-event-history-capability-boundary.md) (implemented) and
[084](084-correlation-operational-evidence.md) (implemented)
Blocks: [076](076-behavioral-evidence-explorer.md)'s navigation,
[079](079-ci-integration-github-action.md)'s rendered link, and
[088](082-agent-inspection-and-evaluation-depth.md#088--review-decisions-and-annotations)
Planned in: [082 § item 085](082-agent-inspection-and-evaluation-depth.md#085--evidence-links-from-a-finding-to-the-observations-behind-it)

## Objective

Make a finding **traceable to the evidence behind it**: from a failed gate check
or a behavioral delta, to the behavioral identities that contributed, to the
retained observations that carried them.

The gate says:

```text
GATE   FAIL
  added_behaviors        actual 3   maximum 0   FAIL
```

and nothing in the platform could answer *which three*, or *which observations
carried them*. `BehaviorDelta` holds no reference to an observation, and
`EvaluationGateResult` is deliberately closed — five named integer checks and a
verdict, with no slice, map or pointer
([ADR 0028](../../adr/0028-gate-result-is-fixed-shape-evidence.md),
[ADR 0029](../../adr/0029-absent-evidence-is-not-a-passing-gate.md)). The
investigation restarted from the run identifier every time.

## What this task does not do

- **No change to the shape of a gate result or a scorecard.** This is the
  central constraint. A `[]Link` field would reopen exactly what ADR 0028 and
  ADR 0029 closed. **Resolution is a query against authoritative state, not a
  payload carried inside a verdict.**
- **No re-derivation.** Every number returned is the number the recorded
  evidence holds. Nothing recomputes a fingerprint, a policy decision, a
  baseline, a count, a rate or a verdict.
- **No storage of its own, and no schema change.** 067 owns retention. See
  [§ Why no new index](#why-no-new-index).
- **No view.** No trace tree, timeline, waterfall or browser change — 076.
- **No annotations** (088), **no counting fold** (083), **no cost or latency
  comparison** (087).
- **No content**, of any kind.

## The finding reference

A finding is identified by **the comparison and what inside it is being asked
about** — and by nothing that is not already durable:

```text
(reference_run_id, candidate_run_id) + one of:
    check    = <named gate check>
    behavior = <fingerprint id>
```

Both run identifiers are caller-owned and immutable, so **the reference is
stable by construction**: the same query resolves to the same finding later, or
says explicitly that the evidence is gone. Nothing is minted, nothing is stored,
and there is no finding table to migrate.

It travels as a query string on a `GET`, which is deliberate: the resolution
**URL is the citable link** 079 renders into a pull-request comment and 088
records a decision against. A `POST` would have made the one thing this task
exists to produce unlinkable.

### Which findings resolve, and to what

| `check` | Side | Resolves to behaviors | Resolves to observations |
|---|---|---|---|
| `added_behaviors` | candidate | the added deltas | per behavior — see below |
| `block_decisions` | candidate | — | candidate observations with `decision = "block"` |
| `critical_risk_observations` | candidate | — | candidate observations with `risk_level = "critical"` |
| `reference_evidence` | — | **aggregate-only** | **aggregate-only** |
| `candidate_evidence` | — | **aggregate-only** | **aggregate-only** |
| `behavior=<id>` | by presence, or explicit | the one delta | that run's observations carrying it |

**`reference_evidence` and `candidate_evidence` are aggregate-only, and say so
rather than inventing a supporting set.** Both are minimum-count checks: they
fail when a run observed *too little*. The evidence for that failure is an
absence, and there is no observation to link to. Returning "every observation in
the run" would be a different question answered confidently — so the resolution
reports `aggregate_only` and stops.

**`added_behaviors` requires a behavior to resolve observations.** The added set
can hold up to the snapshot's 512 entries, and a predicate over 512
fingerprint digests is not a bounded query. The two-step is the one 082 asked
for: resolve the check to its behavioral identities, then resolve one identity
to its observations. The observations route refuses `check=added_behaviors`
without `behavior=` and names the route that supplies one.

### Side is explicit, never guessed

Reference-run and candidate-run evidence stay distinct. Every gate check above
counts something on **one** side, and that side is part of the answer rather
than left for a caller to assume.

For `behavior=<id>` the side defaults from the delta's own presence — `added`
resolves on the candidate, `removed` on the reference — and may be stated
explicitly, which is the only way to ask about a `shared` behavior on a chosen
side. A stated side that contradicts the delta's presence is refused rather than
silently honoured.

## Status: four outcomes that must not be confused

```text
resolved        supporting evidence was found
none_found      none exists — and the history is complete, so that is a fact
indeterminate   none was found, but the history is partial or unavailable
aggregate_only  this check has no per-observation evidence to link to
```

**`indeterminate` is the point of the whole status field.** A page of zero
observations from a run whose history saturated, or from a run that predates
retention, looks exactly like a page of zero observations from a run that
genuinely did nothing. Reporting both as "no supporting observations" would turn
missing evidence into evidence of absence — which is the specific wrong answer
[ADR 0029](../../adr/0029-absent-evidence-is-not-a-passing-gate.md) already
refuses one layer up, and which matters more here because a developer is using
this to decide whether a regression is real.

A resolution also carries **`exhaustive`**, and it is true only when the side's
retained history is `complete`. A full set of matches drawn from partial history
is still a sample, and labelling it exhaustive would let a reader conclude the
matches are all of them.

### A sample is a sample

`block_decisions` and `critical_risk_observations` count **every record the run
ingested**, from the aggregate. Retained history is bounded at 4096 observations
per run and may be partial or absent. So a check reporting `actual 5000` may
resolve to 4096 observations, or to none.

The resolution therefore returns the **recorded** number beside the observations
and never implies the two are the same measurement. Nothing derives a count from
the observations table, and nothing reconciles the two — a mismatch is the
honest description of bounded retention, not a defect to paper over.

## The routes

Two, each with exactly one pagination, both in the repository's established
shape — exclusive `after` cursor, `limit` 1..64 defaulting to 64, `next_after`
present exactly when another row follows:

```text
GET /v1/evidence/behaviors?reference_run_id=&candidate_run_id=&check=|behavior=
GET /v1/evidence/observations?reference_run_id=&candidate_run_id=&check=|behavior=[&side=]
```

`behaviors` pages by fingerprint id in byte order, the order
`BehaviorSnapshot.Entries()` already guarantees. `observations` pages by the
immutable ingest sequence 067 established, so a cursor keeps its meaning.

### Filtering happens in storage

Every filter — fingerprint, decision, risk level — is a SQL predicate applied
**before** `LIMIT`. Filtering an already-paginated page in memory would return
a page of 3 where 64 matches exist, and would make `next_after` describe the
unfiltered stream rather than the answer.

The fingerprint filter uses 067's bounded digest index and compares **both** the
key and the original value, because a digest narrows rather than identifies.

### Why no new index

`decision` and `risk_level` have no index, and none is added.

Both predicates are evaluated inside one run's rows, and `run_id` leads the
primary key, so the scan is a range over at most `MaxRetainedObservations`
(4096) rows with the limit pushed into SQL. That is bounded by construction.
An index on a low-cardinality column — six decisions, four risk levels — would
also be a poor one, and this task's own rule is that a storage decision needs a
measured reason rather than a plausible one. **067's schema is unchanged**, so
this ships with no migration.

## Authority and layering

The resolver reads what was recorded. It never recomputes.

- Behavioral identities come from `CompareBehaviorSnapshots` over the two
  persisted snapshots — the same function and the same inputs
  `CompareEvaluations` uses, so a resolution and a comparison cannot disagree.
- Recorded counts come from the persisted aggregate and that diff.
- Observations come from 067's retained rows, returned as they were written.

**All of it lives in the control plane.** The CLI reaches it over `/v1` and holds
none of it, which the existing architecture test already enforces. 076, 079 and
088 consume it later and inherit the same rule.

Preconditions are the comparison's own, reused rather than restated: both runs
must exist, both must be completed, and both must belong to one project. A
saturated behavioral snapshot refuses a comparison
([054](054-behavioral-diff.md)) and refuses a resolution for the same reason.

## Consistency and privacy

Each observation page and the history metadata describing it are read from **one
snapshot** — a SQLite transaction, and a read-only `REPEATABLE READ` transaction
on PostgreSQL — exactly as 067 established. A concurrent ingest yields the state
before it or after it, never a mixture.

Only 067's retained allowlist is returned. There is no attribute map, no
span-event list and no payload field to return, because no column holds one.
Unavailable and measured-zero durations stay distinct, and every span status
round-trips.

**Nothing is inferred from correlation.** A shared trace id, a parent span
reference, adjacent timestamps and neighbouring sequences establish no causal
link and are never used to select supporting evidence. An observation supports a
finding because a field this task filtered on says so.

## Acceptance criteria

1. A named gate check that FAILed resolves to the behavioral identities that
   contributed to it, and a behavioral identity resolves to its retained
   observations — over `/v1` and through the CLI, with no browser involved.
2. Every number a resolution returns equals the number the recorded evidence
   holds; nothing is recomputed.
3. The same finding reference resolves to the same finding on a later read, or
   reports explicitly that the evidence is gone.
4. Every resolution is bounded and paginated in the established shape, filtered
   in storage before the limit, and states saturation rather than truncating
   silently.
5. `resolved`, `none_found`, `indeterminate` and `aggregate_only` are
   distinguished, and an empty result over partial or unavailable history is
   never reported as `none_found`.
6. `exhaustive` is true only when the side's retained history is complete.
7. Lookups are scoped to the correct run and side; an identifier repeated across
   runs resolves separately in each.
8. A reference naming a behavior the comparison does not contain, a side its
   presence contradicts, or an unknown check is refused with a diagnostic.
9. No prompt, completion, argument, result, document, body or arbitrary
   attribute is reachable through any resolution route.
10. Identical results on both persistence backends.
11. Resolution logic exists only in the control plane.
12. No gate verdict, behavioral count, fingerprint or comparison result changes.

## Validation

At the control-plane, `/v1` and CLI levels, on both backends — the PostgreSQL
half skipped without a database and run against a real one in CI. A privacy
tripwire plants content upstream and asserts it reaches no resolution payload. A
concurrency test commits an ingest inside a page read. A simulated digest
collision asserts the original-value comparison is what excludes it.

## Compatibility

Additive. Two new `GET` routes, one new CLI command family, **no schema change**,
and no existing route, field, contract or verdict altered.
