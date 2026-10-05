# v0.12.0 Tasks — Change Impact

Tasks for the [`v0.12.0` milestone](../../ROADMAP.md#v0120--change-impact).
Numbering continues the global sequence. 104 was the last task number in use.
081, 086 and 087 are earlier reservations: 081 is from the roadmap's milestone
sequence, and 086 and 087 are from [082](../v1.0/082-agent-inspection-and-evaluation-depth.md).
Their specifications live here because this is the milestone that builds them.

| Task | Status |
|---|---|
| [105 — Pipeline status surface](105-pipeline-status-surface.md) | Specified |
| [087 — Performance and cost evidence](087-performance-and-cost-evidence.md) | Specified |
| [106 — Frequency evidence](106-frequency-evidence.md) | Specified |
| [086 — Scenario and input versioning](086-scenario-and-input-versioning.md) | Specified |
| [081 — Persisted per-behavior fidelity](081-persisted-behavior-fidelity.md) | Specified |
| [107 — Change Impact view and Model Change Report](107-change-impact-view-and-report.md) | Specified |

[080 — Metadata-only detection evaluation](../v1.0/080-metadata-only-detection-evaluation.md)
runs alongside as a measurement. It gates nothing, and its specification stays
under `v1.0/`.

**Proposed build order: 105, then 087 → 106, then 086, then 081 alone, then
107.** 105 depends on none of the others. 106's 429 rule reads 087's status
classes, and its `max_llm_calls_per_run` reads the layer counts 081 persists.
107 renders all of them and computes nothing.

**Schema steps.** 087, 086, 081 and 107 (the gate-result snapshot on an
execution) each add one forward-only step on both backends, numbered in the
order they land. 105 and 106 add none: 105's state is ephemeral, and 106 reads
evidence that is already persisted.

Every task here keeps the milestone's constraints:

- content-free;
- nothing new in `StableFeatures`, a fingerprint or a baseline key;
- the engine unchanged;
- integer evidence for anything a gate reads;
- *unavailable is not zero*;
- the browser computes nothing;
- suggestions are rule-table outputs under
  [ADR 0064](../../adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md),
  never verdicts.

Where a task could not keep one of them as the brief wrote it, the task says so
under a *Conflict* heading.
