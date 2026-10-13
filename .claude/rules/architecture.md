# Architecture

For the full human-facing writeup (system diagram, dependency-direction
verification, storage/OTel boundaries) see
[`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md); for why specific
non-obvious decisions were made, see [`docs/adr/`](../../docs/adr/).
This file is the condensed version for driving day-to-day changes.

## The pipeline

```
Event → Features → Fingerprint → Baseline(read) → Anomaly → Trust → Policy → Decision
                                       ↑
                              Baseline(write) via explicit Observe()
```

Every stage is a small package with a pure function at its center
(`features.Extract`, `fingerprint.Compute`, `anomaly.Score`,
`trust.Compute`, `policy.Evaluate`). `internal/baseline` is the one
stateful domain type, and even it is immutable value-with-copy-on-write,
not a mutex-guarded struct — concurrency-safe storage of "the current
Baseline for this key" is `internal/store`'s job, not `baseline`'s.

Dependency direction only ever points earlier in this list. `policy`
may import `trust`; `trust` must never import `policy`. If you find
yourself wanting a later stage to import an earlier one back, that's a
sign the data belongs on the value passed downward, not fetched via a
new import.

## The internal/ boundary — and why `event` isn't inside it

Go's `internal/` visibility rule blocks anything outside this module
from importing an internal package. That's the encapsulation
mechanism: no `pkg/` wrapper is needed on top of it.

But it cuts both ways. `Event` is the one type every caller — in-module
or not — must *construct* just to call `Engine.Analyze` at all. Leaving
it under `internal/event` (as it was through Slice 7) would have made
the public SDK's own entry point uncallable by any external consumer.
That's why Slice 8 moved it to a public `event` package, sibling to
`internal/`.

Everything else that appears on `Result` (`Features`, `Fingerprint`,
`Anomaly`, `Trust`, `policy.Decision`, `policy.Explanation`) stays
under `internal/` on purpose, and this is *not* an oversight to "fix"
symmetrically with `Event`. Go's restriction is on importing a
package, not on reading exported fields of a value you already have —
`result.Trust.Score` and `result.Decision == "block"` both work fine
from outside the module without importing anything beyond the root
`trustvian` package. The types that would need to move are only the
ones an external caller must *construct*: `Policy`, `anomaly.Config`,
`trust.Config`, `store.Store` implementations. None of those have an
external consumer yet — the only things constructing them today are
this module's own tests and `cmd/trustvian`, both in-module and
unaffected. Promote one to a public package only when a real external
consumer needs it; doing it speculatively is exactly the kind of
abstraction CLAUDE.md says not to build ahead of need.

## Ports, not repositories

`internal/store.Store` is intentionally narrow — `Get` and `Observe`,
nothing else. It is not a generic repository interface. Baseline's real
access pattern is "read the current snapshot, apply one incremental
update," and the interface says exactly that. Resist widening it for
symmetry with some hypothetical CRUD need.

## Policy is data

`policy.Policy` is a plain value (`Rules []Rule` + a mandatory
`DefaultAction`/`DefaultReason`), evaluated by a generic,
first-match-wins `Evaluate`. It is not a set of Go closures or a
strategy interface. This is what lets the YAML loader (`config.LoadFile`
→ `config.CompilePolicy`) exist as a pure adapter that produces the same
`Policy` value, without
touching the evaluator — and it's why a misconfigured `Policy` (empty
or invalid `DefaultAction`, empty `DefaultReason`) fails closed to
`BLOCK` inside `Evaluate` itself rather than relying on every caller to
validate their own config.

## OTel is an adapter, never a dependency of the core

`internal/otel` is the *only* package in this module allowed to import
`go.opentelemetry.io/otel*`. The core engine (`event` through
`internal/policy`, and the root `Engine`) has zero OTel awareness. The
OTel Collector processor is a separate module, `processor/`, because the
Collector's heavy dependency tree must never leak into this module's
dependency graph.

## Composition root

`Engine` (root package) is the only place all the stages get wired
together. It holds a `Store`, a `Policy`, and the two `Config` values,
all supplied via functional options over sane defaults. There is no
global/default engine — every caller constructs one explicitly. This
is deliberate (see Architecture Risk #8 in the project's design notes):
a package-level singleton would be more convenient to call but would
break testability and the explicit dependency direction the rest of
this document describes.

## Abstractions that should not exist here

- A plugin/strategy interface for anomaly algorithms — there is one
  deterministic algorithm; add the interface when a second one exists,
  not before.
- An event bus or pull-based `EventSource` — the pipeline is
  synchronous and push-based (`Analyze(ctx, event)`), matching the
  spec's own vertical-slice-first instruction.
- Per-event goroutines — `Analyze`/`Observe` are fully synchronous,
  which is also what keeps goroutine-leak risk at zero for this MVP.
