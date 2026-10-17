# v0.13.0 Tasks

Tasks for `v0.13.0`. Numbering continues the global sequence; 107 was the last
task number in use before them.

## Maintainer decisions

- **109 is accepted** and implemented as its specification was merged;
  decisions 1–6 are binding.
- **110 is accepted.** It is next, in its own pull request after 109's,
  because it touches the same Live and Overview files.
- **108 stays Proposed.** It changes `OperationCategory` and `TargetName`, and
  so fingerprints and baselines, and needs its own rollout decision first.
- **080 stays Specified.** It gates nothing.

| Task | Status |
|---|---|
| [108 — Legacy HTTP semantic conventions](108-legacy-http-semantic-conventions.md) | Proposed; needs a rollout decision |
| [109 — Live Observatory: pause, resume and following one actor](109-live-pause-and-actor-following.md) | Accepted; implemented on `main` ([What shipped](109-live-pause-and-actor-following.md#what-shipped)) |
| [110 — The browser computes nothing: every figure it still derives, decided](110-browser-computes-nothing.md) | Accepted; next, after 109 |
| [080 — Metadata-only detection evaluation](../v1.0/080-metadata-only-detection-evaluation.md) | Specified; moved from `v0.12.0`, where it gated nothing (maintainer decision D13). Its specification stays under `v1.0/` |

088 and 092, deferred from `v0.12.0`, are listed in the
[roadmap](../../ROADMAP.md#v0120--change-impact) and not yet specified.
