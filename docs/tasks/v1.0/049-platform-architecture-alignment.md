# 049 — Platform Architecture Alignment

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** nothing · **Blocks:** [050](#the-milestone-sequence-this-task-produced)
through 072 · **First slice of `v1.0` Track B, and the only one that
writes no runtime code.**

## Objective

Align the repository's architecture and roadmap with the widened product
direction — a local-first behavioral security and promotion platform —
**before** any platform code exists, so that every task after this one
inherits one boundary, one vocabulary, and one dependency direction
rather than negotiating them mid-implementation.

This task changes documentation only. It implements nothing.

## Why

The roadmap described `v1.0` as production hardening of the engine, and
placed a control plane beyond it, gated on external adoption. The
product direction changed: the control plane is now `v1.0` scope, and
it is local-first rather than enterprise-scale.

Left unaligned, that mismatch is expensive in a specific way. The first
platform task would have to decide the core/platform boundary while
writing code against it, which is how "just add an `EvaluationRunID` to
`Event`" becomes a merged pull request. The engine has nine milestones
of behavioral guarantees that hold precisely because nothing outside the
pipeline can reach into it; a boundary decided under implementation
pressure is the realistic way that ends.

Deciding it first costs one documentation pass. Deciding it late costs a
major version.

## Scope

- Record the product lifecycle and the two-layer model in
  [ADR 0022](../../adr/0022-core-platform-boundary.md), including the
  module-boundary choice and its trade-offs.
- Restructure [ROADMAP.md](../../ROADMAP.md)'s `v1.0` into two tracks —
  engine production readiness, unchanged, and the platform — and publish
  the milestone sequence.
- Replace [ARCHITECTURE.md](../../ARCHITECTURE.md)'s Control/Cloud section
  with the platform boundary: the four invariants, what the core already
  provides, and the one core change the platform requires.
- Update [task 016](../016-control.md): its constraint stands, its
  scheduling assumption does not.
- Name the platform domain vocabulary — Project, Agent, Candidate,
  Evaluation Run, Behavioral Profile, Scorecard, Promotion — so later
  tasks share it.
- State the learning-isolation problem and the constraints on solving
  it, without solving it.
- Record in [ADR 0023](../../adr/0023-interfaces-are-adapters.md) that CLI,
  TUI, and WebUI are adapters over one set of control-plane services,
  that the realtime bus is defined by its abstraction rather than its
  transport, and that persistence is a set of capabilities rather than
  one database interface.

## Non-Goals

Nothing is implemented. Specifically not: a CLI subcommand, a TUI, a
WebUI, frontend dependencies, SQLite, DuckDB, ClickHouse, a message
broker, HTTP endpoints, SSE or WebSockets,
`Project`/`Candidate`/`EvaluationRun` types, a platform module, or a
control API.

Nothing in the engine changes: no scoring formula, no fingerprint, no
baseline key, no storage schema, no public API rename. No multi-tenancy,
authentication, or access control. No existing production-readiness
requirement is dropped and no security or resource bound is weakened.

## Technical Requirements

The direction recorded must satisfy the constraints the engine already
operates under, not relax them:

- The platform depends on the core; the core never depends on the
  platform.
- The platform does not import `internal/*`.
- No platform-aware branch, type, or configuration exists in the engine.
- `SessionID` does not become baseline identity; `EvaluationRunID` does
  not become fingerprint identity; candidate metadata never becomes a
  fingerprint dimension.
- Scorecards aggregate evidence at evaluation level and do not redefine
  or overload event-level `Trust.Score`.
- Promotion gates are deterministic; an aggregate score cannot override
  a critical violation.
- No analytical store becomes a dependency of the engine.

## Tests

None. This task adds no runtime code, and a test asserting prose would
be theatre.

Two of the invariants above become real tests when code exists, and they
are release gates rather than suggestions:

- the platform module imports no `internal/*` package;
- the engine contains no platform-aware branch.

Both fail silently if merely reviewed, which is why they are written
down here as testable before anything can violate them.

## Benchmarks

None.

## Documentation

`ROADMAP.md`, `ARCHITECTURE.md`, `tasks/016-control.md`,
`adr/0022-core-platform-boundary.md`,
`adr/0023-interfaces-are-adapters.md`, `adr/README.md`,
`tasks/README.md`, `tasks/v1.0/README.md`, and this file.

Each distinguishes shipped behavior from approved direction from future
work. No future capability is described as if it exists.

## The milestone sequence this task produced

Reconciled after the first pass to put the evaluation domain before
persistence — capability boundaries are designed against known call patterns,
not guessed — and to make the TUI a first-class interface delivered before the
full WebUI.

**Architecture and core boundary** (the only engine-touching work):

| Task | Milestone |
|---|---|
| 050 | Public serializable decision record |
| 051 | Behavioral profile: learning-scope isolation |

**Evaluation foundation** (pure domain and logic, no persistence):

| Task | Milestone |
|---|---|
| 052 | Evaluation domain: Project, Agent, Candidate, EvaluationRun, environments |
| 053 | Evaluation result aggregation |
| 054 | Behavioral diff |
| 055 | Scorecards |
| 056 | Deterministic hard gates |

**Local developer platform:**

| Task | Milestone |
|---|---|
| 057 | Local platform persistence |
| 058 | Local control-plane API and ingest |
| 059 | Realtime infrastructure |
| 060 | Developer CLI |
| 061 | Terminal dashboard (TUI) |
| 062 | Integrated local developer workflow |
| 063 | Minimal web control plane |

**Shared sandbox and promotion:** 064 PostgreSQL platform backend ·
065 environment model · 066 promotion workflow.

**Production history and scale:** 067 event-history capability boundary ·
068 ClickHouse reference adapter if justified · 069 multi-node and load
validation · 070 platform security hardening · 071 platform backup, restore
and upgrade · 072 OSS platform `v1.0` release gate.

Tasks 050 and 051 are the only core changes currently expected. Any
additional core change requires explicit architectural review and must remain
generic rather than platform-aware.

## Conflicts this task surfaced

Neither blocks planning; both are cheaper to decide than to discover.

**CLI exit codes.** The CLI ships `0`/`1`/`2` meaning success, run failure,
usage error, and [the compatibility contract](../../compatibility.md#cli) treats
them as automation-facing. The evaluation direction wants `1` to mean *gate
failed*. Task 060 owns the choice: separate codes for `eval`, or a
contract-level change that costs a major version.

**Fingerprint admission during long evaluations.** A baseline admits at most
512 identities ([ADR 0019](../../adr/0019-bounded-fingerprint-admission.md)). A
long evaluation of a wide-surface agent could reach it and silently stop
learning while still reporting. Task 051 owns it, since learning-scope
isolation changes what counts toward the bound.

## Acceptance Criteria

- [ ] ADR 0022 records the two-layer model, the four invariants, the
      module-boundary choice with its trade-offs, and the learning
      isolation constraints.
- [ ] ADR 0023 records the interface, transport, and storage adapter
      rules, including that no interface owns authoritative logic.
- [ ] `ROADMAP.md` `v1.0` covers both tracks, retains every engine
      production-readiness requirement, and publishes tasks 049–072.
- [ ] `ARCHITECTURE.md` documents the platform boundary and the core
      properties the platform builds on.
- [ ] Task 016's constraint is preserved and its scheduling assumption
      marked historical.
- [ ] Platform concepts are named and scoped; none is implemented.
- [ ] The CLI/TUI/WebUI split is defined, with the TUI sequenced before
      the full WebUI.
- [ ] The learning-isolation problem is stated with its constraints and
      explicitly left unsolved.
- [ ] No runtime code changes. `go test ./...` and `go test -race ./...`
      pass unchanged.
- [ ] No documentation describes future capability as shipped.
