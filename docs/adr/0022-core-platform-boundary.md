# 0022 — Core and platform are two layers, one direction

**Status:** Accepted

## Context

Trustvian's product direction widened. The target is no longer only a
production-ready behavioral engine, but a **local-first, self-hosted
behavioral security and promotion platform for AI agents**: develop
locally, evaluate in a sandbox, promote with evidence, monitor in
production.

That introduces a domain the engine does not have and should not grow:
projects, agents, candidates, evaluation runs, scorecards, environments,
promotion decisions, a control API, a realtime stream, a dashboard, and
control-plane persistence.

The tempting shape is to let `Engine` absorb it — it already holds the
policy, the store, and the pipeline, so "just add an EvaluationRunID"
looks like the short path. It is the expensive one. An engine that knows
what a Candidate is can no longer be reasoned about, tested, or embedded
in isolation, and every behavioral guarantee this codebase has spent
nine milestones establishing becomes conditional on platform state.

[Task 016](../tasks/016-control.md) already recorded the constraint that
a control plane consumes the core and never forks it. What it did not
settle — because a control plane was then post-`v1.0` and adoption-gated
— is how the boundary is *enforced* once both layers ship in the same
release.

## Decision

**Two layers, one direction of dependency.**

```text
Platform  ──(public API only)──▶  Core
```

Four invariants, stated as invariants because each is independently
violable and each violation is quiet:

1. The platform may depend on the core. **The core must never depend on
   the platform** — not by import, not by interface, not by
   configuration.
2. **The platform must not import `internal/*`.** Enforced by an explicit
   CI check, not by the module boundary alone — see below.
3. **No platform-aware branches in the engine.** No
   `if runningUnderControl`, no mode flag, no `EvaluationRunID` on
   `Event`.
4. **`Engine` stays an engine.** Project, Candidate, EvaluationRun,
   Scorecard, Promotion, Environment, users, and access control do not
   appear in the core as types, fields, or options.

The engine keeps answering questions about individual events and learned
behavioral state. The platform answers a different question — *is this
candidate safe to promote* — by aggregating what the engine already
returns on `Result`.

### The platform lives in a separate Go module in this repository

Three options were weighed.

**A separate repository** gives the hardest boundary and the worst
workflow: the platform would consume tagged releases of the core, so
every cross-cutting change becomes a two-repository dance with a
release in between. For a project where the same maintainers own both
layers and the core is still moving, that cost is paid daily to prevent
a mistake that the next option already prevents.

**One module, separate packages** is the cheapest to start and the
easiest to erode. Nothing but review discipline stops a platform package
importing `internal/`, and the failure is silent — the code compiles,
the tests pass, and the boundary is gone before anyone notices.

**A separate module inside this repository** is chosen. Cross-cutting
changes stay in one repository, one pull request, one CI run. The
repository already runs exactly this arrangement twice — `processor/`
and `examples/` are separate modules resolving the core through a
`replace` directive, and CI verifies both with `GOWORK=off` precisely
because a workspace hides broken module boundaries.

**A module boundary does not, on its own, enforce invariant 2.** Go's
`internal/` rule turns on *import-path ancestry*, not on module
membership: a package may import `github.com/trustvian/trustvian/internal/…`
whenever its own import path sits under `github.com/trustvian/trustvian/`.
So a nested module whose path is `github.com/trustvian/trustvian/platform`
would compile such an import happily, module boundary or not. Verified
rather than assumed: a throwaway module at that path builds a core
`internal/store` import successfully, while the same file under the
module path `trustvian-platform` is rejected with *"use of internal
package … not allowed"*.

That is why `processor/` and `examples/` are named `trustvian-processor`
and `trustvian-examples` rather than repository-prefixed paths — they
get compiler enforcement as a consequence of their module path, not of
being modules.

Invariant 2 is therefore enforced **explicitly**, by an automated check
in CI that no platform source imports
`github.com/trustvian/trustvian/internal/…`. Choosing a non-prefixed
module path for the platform is recommended as a second line of defence,
since it makes the violation a build failure too — but the check is the
enforcement, and the path is the belt to its braces. `GOWORK=off`
verification stays in place for a different property: that the module's
declared dependencies actually resolve, which a workspace hides.

The trade-off accepted: a `replace` directive is not how an external
consumer resolves the module, so the platform module's dependency on
the core is verified rather than assumed — the same discipline
`examples/` already carries, and the same reason `examples/` is a
genuine external-consumer proof rather than a convenience.

### Learning isolation is a core concern, solved generically

Evaluating two candidates against one actor identity would train one
baseline: each candidate teaches the other, and the evaluation measures
a baseline it polluted. Isolation is therefore a prerequisite for
evaluation, and it belongs in the core — but the core must not learn
what a Candidate is.

Decided here: `SessionID` must not become baseline identity,
`EvaluationRunID` must not become fingerprint identity, and candidate
metadata — git SHA, artifact digest, model or tool-set hash — must never
become a fingerprint dimension. A git SHA is not a behavioral dimension;
treating it as one would make every deployment look like a brand-new
actor and destroy the learning the product exists to accumulate.

Not decided here: the mechanism. `baseline.Key` was made composite in
`v0.4` against this kind of need, but whether isolation extends that
key, scopes the store, or takes a third shape is a design question with
its own task. It is the first real engineering decision the platform
requires.

### Evaluation-level scoring is not `Trust.Score`

A scorecard aggregates evidence across an evaluation run. `Trust.Score`
is an event-level number with a documented formula and a compatibility
promise. They are different questions at different granularities, and
the scorecard does not redefine, overload, or replace the trust score.

Promotion additionally never rests on an aggregate alone. Deterministic
hard gates — zero critical policy violations, zero blocked sensitive
actions, zero unapproved sensitive actions, new-behavior count within a
threshold — decide, and a high average cannot override a critical
violation. This is `policy.Evaluate`'s existing fail-closed discipline
raised one level.

## Alternatives considered

- **Extend `Engine` with platform concepts.** Rejected above: it makes
  every engine guarantee conditional on platform state, and it cannot be
  undone once external consumers depend on the shape.
- **Make the platform a plugin layer inside the core.** Rejected. It
  inverts the dependency in practice — the core would need extension
  points shaped by platform needs — while looking like separation.
- **Ship the platform as a separate product later.** Rejected as a
  product decision: the lifecycle this direction serves is only useful
  end to end, and a developer cannot evaluate a candidate locally with
  an engine alone.
- **Store raw event history in the core** so evaluations can replay it.
  Rejected. The core deliberately retains learned state and not an event
  log; that is a security property as much as a storage one. History is
  the platform's to keep.

## Consequences

- The platform can be built, tested, and reviewed without touching
  behavioral code, and the engine keeps a single reason to change.
- Tasks 050 and 051 are the only core changes currently expected: a
  public result and export boundary, and generic learning-scope
  isolation. Both are additive. Any further core change requires
  explicit architectural review, and must be generic — justified by the
  behavioral engine on its own terms rather than by what the platform
  happens to need. A third such task is a reason to re-examine the
  boundary, not proof on its own that it has been crossed.
- What the boundary does prohibit absolutely is platform awareness.
  Project, Candidate, EvaluationRun, Scorecard, Promotion, and
  control-plane or dashboard concerns must never become core types,
  fields, modes, or behavior, however convenient it would be.
- Invariants 2 and 3 are release gates for `v1.0`, asserted by an
  automated check rather than by review, because both fail silently —
  and, for invariant 2, because the module boundary alone would not
  catch it.
- **A client of the platform is not the core.**
  [Task 060](../tasks/v1.0/060-developer-cli.md) added control-plane
  commands to the CLI in the root module. Their wire DTOs mirror the JSON
  nouns of the `/v1` contract, so field names like `ProjectID` appear in
  root-module source — and the name-based tripwire in
  `scripts/check-platform-boundary.sh` flagged them.

  That scan was narrowed to skip exactly those seven adapter files, and
  nothing else. The invariant it proxies for is that *the engine* must not
  grow platform concepts; an HTTP client naming the contract's own fields
  is not that. The engine-facing CLI files (`analyze.go`, `baseline.go`,
  `policy.go`, …) are still scanned, and a platform identity declared in
  one still fails the build.

  The narrowing is safe because the stronger checks still cover those
  files: the root module's build graph must contain no platform package,
  and `cmd/trustvian`'s own architecture test fails on an import of
  `trustvian-platform`, on a platform type named in CLI source, and on
  `trustvian-platform` appearing in the root `go.mod` or `go.sum`. A name
  scan was always the weakest of the four — see
  [ADR 0033](0033-developer-cli-is-a-thin-http-adapter.md).
- No analytical store — ClickHouse or otherwise — ever becomes a
  dependency of the engine. The platform may adopt one when measured
  volume justifies it.
- `v1.0` now means both layers. The engine's production-readiness
  requirements are unchanged and none is dropped; the platform is
  additional scope, not a substitute.
