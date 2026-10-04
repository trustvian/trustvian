# v0.11.0 Tasks — WebUI Experience

Tasks for the [`v0.11.0` milestone](../../ROADMAP.md#v0110--webui-experience).
Numbering continues the global sequence; 096 was the last task number in use.

| Task | Status |
|---|---|
| [097 — Theme preference: Light, Dark and System](097-theme-preference.md) | Implemented |
| [098 — Selection-based context workflows](098-selection-based-context-workflows.md) | Implemented |
| [099 — Overview dashboard](099-overview-dashboard.md) | Implemented |
| [100 — Trace investigation over retained evidence](100-trace-investigation.md) | Implemented |
| [101 — Recency-ordered run discovery](101-recency-ordered-run-discovery.md) | Implemented |
| [102 — Scenario execution discovery and reference selection](102-scenario-execution-discovery.md) | Implemented |
| [103 — Session selection over retained observations](103-session-selection.md) | Implemented |
| [104 — The Runs destination reads the shared context](104-runs-destination-from-shared-context.md) | Implemented |

097–100 are implemented and each records what shipped. 101–104 complete the
milestone's discovery and selection workflows: 101 first (its schema v10 key
is what 102 reads), then 103, 102 and 104.

The first build was **097 → 098 → 099 → 100**; each later task reads the
context and selectors 098 introduced, drawn with 097's tokens.

Tasks 100–103 each add one bounded collection route; 101 also adds schema v10's
sort keys. None of them changes what the platform retains, what the realtime
protocol carries, or how a gate decides.
