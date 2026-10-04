# v0.11.0 Tasks — WebUI Experience

Tasks for the [`v0.11.0` milestone](../../ROADMAP.md#v0110--webui-experience).
Numbering continues the global sequence; 096 was the last task number in use.

| Task | Status |
|---|---|
| [097 — Theme preference: Light, Dark and System](097-theme-preference.md) | Implemented |
| [098 — Selection-based context workflows](098-selection-based-context-workflows.md) | Implemented |
| [099 — Overview dashboard](099-overview-dashboard.md) | Planned |
| [100 — Trace investigation over retained evidence](100-trace-investigation.md) | Planned |
| [101 — Recency-ordered run discovery](101-recency-ordered-run-discovery.md) | Planned — a capability gap the milestone records and does not close |

The build order is the one the milestone states: **097 → 098 → 099 → 100.**
Each later task reads the context and the selectors 098 introduces, and every
surface they add is drawn with 097's tokens. 101 is not part of the build: it
names the one question the dashboard cannot honestly answer with today's
collections, and why answering it needs a decision before it needs code.

Every task here is presentation over `/v1` except 100, which adds one bounded,
run-scoped collection route. None of them changes what the platform retains,
what the realtime protocol carries, or how a gate decides.
