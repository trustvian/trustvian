# 0047 — Behavioral identity is per observation; counting is a control-plane policy

**Status:** Accepted. § 2's "never persisted" is amended by
[ADR 0068](0068-fidelity-and-layer-are-persisted-as-per-behavior-counts.md):
the layer is persisted as per-behavior counts. Everything else here stands.

## Context

A producer emitting agent-oriented telemetry observes one act at two
instrumentation layers. The reference workload's export-customer act produces a
GenAI tool span and, beneath it, the HTTP request the tool made. Each span maps
to its own `Event`, so each gets its own `StableFeatures` and its own
`Fingerprint`, and a comparison reports:

```text
BEHAVIORAL DIFF: added 2
    + tool · export_customer
    + http · POST → export.localhost
```

Both rows describe the same change. At `max_added_behaviors: 0` that is
invisible; the first team to allow one added behavior allows half of one act and
cannot tell.

[Task 082](../tasks/v1.0/082-agent-inspection-and-evaluation-depth.md) recorded
two open decisions about this and deliberately did not pre-empt them. This record
makes both, and separates a third question the earlier framing had folded into
them.

**The word "behavior" was doing three jobs**, which is how the problem became
hard to state:

| Term | What it is |
|---|---|
| **Observation** | one observed action — one span, one `Event`, one `DecisionRecord` |
| **Behavioral identity** | the `StableFeatures` tuple and its `Fingerprint` — *what kind of action* |
| **Counted change** | what a comparison reports as added, and what a gate limit measures |

Two observations of one act is correct. Two behavioral identities is correct.
Two *counted changes* is the defect.

## Decision

### 1. Behavioral identity stays per observation

Two spans observing one act remain two behavioral identities. Identity is not
changed, no fingerprint composition changes, and no baseline is migrated.

The reason is detection, not migration cost. Folding the layers into one
fingerprint necessarily drops the transport target from identity:

```text
before   tool · export_customer     +     http · POST → export.localhost
after    tool · export_customer           (the destination is no longer identity)
```

The destination is the security-relevant part. An agent whose `export_customer`
tool starts posting to `attacker.example` changes no tool-layer behavior at all,
so a folded identity would make that change invisible. That trades a counting
annoyance for a detection hole, which is the wrong direction for a behavioral
security engine. `TestToolSwitchingDestinationChangesTheTransportIdentity` pins
the property this preserves.

### 2. There is no `model` operation category

`event.OperationCategory` gains no sixth value. It is a `StableFeatures`
dimension, so adding `model` would re-fingerprint every model call a producer had
already been emitting and discard those actors' baselines — to improve a label.

Instead, `semconv.Layer` is a **non-identity classification** saying which
instrumentation layer supplied the identity: `model`, `tool`, `retrieval`,
`transport`, or unclassified. It rides exactly where `trustvian.fidelity` rides —
the outbound span attribute, the ingest envelope, the realtime observation — and
is never persisted, never fingerprinted, and never a baseline key.

It also carries strictly more information than a category could. `external` today
means both "a model was consulted" and "a document store was queried"; a category
cannot separate those without becoming two categories, and a classification
separates them at no cost to identity.

**One gate, not two.** A layer is claimed exactly when fidelity is `semantic`.
Two indicators derived from one table must not be able to disagree about one
span, and `TestLayerIsClaimedExactlyWhenFidelityIsSemantic` enforces it.

### 3. Counting is a control-plane policy, and it is deferred

Because identity is right and counting is wrong, the correction belongs where
counting lives: `platform`'s `BehaviorDiff` and `EvaluationGatePolicy`. Not in
the engine, not in an adapter, and not in a renderer.

It is **not implemented**, and the reason is verified rather than assumed:

- `ParentSpanID` is read nowhere in this repository.
- `event.Context` and `DecisionRecord` carry no parent identity, so the counting
  layer cannot know two records describe one act.
  `TestParentIsUnreachableAtCountingTime` asserts this and fails when it changes.
- The processor is stateless per span, and a child span completes and exports
  **before** its parent.

So the prerequisite is
[084](../tasks/v1.0/082-agent-inspection-and-evaluation-depth.md#084--correlation-and-operational-evidence-on-the-record-boundary),
and 084 alone is not sufficient: a correlation-aware counting policy also needs a
bounded correlation structure, an out-of-order rule, and a decision about what a
folded act *reports*. None of those is guessed at here.

### 4. Until then, the semantics are stated rather than hidden

`max_added_behaviors` counts **behavioral identities**, and one act observed at
two layers contributes two. That was already true and was written down nowhere.
It is now in `docs/OPENTELEMETRY.md`, in the CLI's flag help, and in
`docs/cli-guide.md`.

Missing correlation produces this same documented fallback — two counted changes
— and never a guess. **Nothing is inferred from timestamps, adjacency, span names
or similar names**, and no transport observation is dropped to make a count
smaller. A deleted observation is evidence destroyed, which is a worse failure
than a count that needs explaining.

## Alternatives considered

**Fold the fingerprint.** Rejected on detection grounds in decision 1. It is
also the only option here that would have required a baseline migration.

**Add a `model` category.** Rejected in decision 2: it changes identity to
improve a label, and cannot express the model-versus-retrieval distinction
without a further category.

**Suppress the transport span when a tool span exists.** Rejected outright. It
needs the same correlation the fold needs, so it is not actually cheaper — and it
destroys the observation that carries the destination, which is the evidence a
security decision depends on.

**Infer parentage from timing or name similarity.** Rejected. A heuristic that is
usually right about which HTTP call a tool made is a heuristic that silently
misattributes evidence when it is wrong, in a system whose whole claim is that
decisions are explainable from the evidence beneath them.

**Count in the engine.** Rejected on boundary grounds. The engine analyses one
event at a time and holds no evaluation concept; a count spanning observations is
platform logic, and ADR 0022 keeps that direction fixed.

## Consequences

- A model call, a tool call, a retrieval and a transport operation are
  distinguishable in a rendered view without a category change and without a
  baseline reset.
- **The layer establishes no direction.** `transport` is the value for an inbound
  `SERVER` or `CONSUMER` span as much as for an outbound `CLIENT` one, and for a
  span whose kind states no direction at all; what they share is that no
  convention named the operation. Direction is `Operation.Direction`, is not
  published on the realtime observation, and must not be inferred from the layer.
- **The double count remains.** This record does not fix it; it decides where the
  fix belongs and what it must not do. `TestCountingFoldIsNotImplementedYet`
  fails the moment it closes, so the deferral cannot quietly become permanent.
- [078](../tasks/v1.0/078-behavioral-scenario-suites.md)'s k-of-N thresholds
  count behaviors, so its measurement re-run still waits on the counting policy
  rather than on this record.
- A fourth layer value (`retrieval`) exists that no earlier document anticipated.
  It is not scope creep: `external` was ambiguous, and three values would have
  required asserting a model call that did not happen.
- The layer is **not persisted**, so a comparison delta carries none — the same
  limitation fidelity has, deferred to the same task (081) for the same reason.
- Unclassified is a published state. A consumer renders four layers plus "not
  classified" plus "a value this build does not recognize", which is three more
  states than a boolean would have offered and each is honest.
