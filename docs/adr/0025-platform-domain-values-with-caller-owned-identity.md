# 0025 — Platform domain entities are values with caller-owned identity

**Status:** Accepted

## Context

[Task 052](../tasks/v1.0/052-evaluation-domain.md) introduces the first
platform-layer runtime code: `Project`, `Agent`, `Candidate`,
`EvaluationRun`. Four later tasks consume these entities —
053 aggregates into a run, 056 gates on one, 057 persists them, 058 serves
them over an API — and each will need an identifier for an entity it creates.

[ADR 0022](0022-core-platform-boundary.md) fixed the layering and the module
boundary. [ADR 0023](0023-interfaces-are-adapters.md) fixed that transports
and stores are adapters holding no authoritative logic. Neither says where an
identifier comes from, and that gap is load-bearing: task 057 could
reasonably have a store generate one on insert, task 058 could reasonably
have a client supply it, and task 060 could reasonably have the CLI mint one.
Those three answers are mutually incompatible, and the incompatibility would
surface as data whose identity depends on which door it came through.

The second question this settles is smaller but easier to get wrong: what a
run's status means once it reaches `Completed`.

## Decision

### Identifiers are caller-owned and opaque; the domain never makes one

A domain constructor validates an identifier and stores it. It does not
generate one, read a clock, require a UUID, parse structure out of one, or
carry a database key.

Generation belongs to whichever adapter creates the entity, which is exactly
the division ADR 0023 already draws for transports and stores — an identifier
is a side effect of *admitting* an entity into a system, not of *being* one.
That leaves each adapter free to make the right local choice (a CLI may want
a readable slug, an API may want a client-supplied idempotency key, a store
may want a sequence) without any of them disagreeing about what the domain
requires.

The cost is accepted deliberately: nothing stops a caller from reusing an
identifier, because the domain cannot see other entities. Uniqueness is a
property of a *collection*, and the collection does not exist until task 057.
Pretending otherwise would mean a registry inside the domain, which is
persistence wearing a domain's clothes.

### Entities are values; transitions return new values

No entity is an interface, has getters and setters, or mutates in place. A
lifecycle transition returns a new `EvaluationRun`, leaving the receiver
untouched — the same treatment `internal/baseline.Baseline` already gets in
the core, for the same reason: a value handed to a caller stays valid and
cannot be changed underneath them.

A rejected transition returns the original value alongside the error, so a
caller that ignores the error holds the unchanged run rather than a
half-applied one.

### Identity fields do not mutate

A `Candidate` is one version or configuration. There is no
`SetSourceRef`, no `ChangeAgent`, and no path by which one Candidate becomes
another. Evaluating a different artifact means creating another Candidate —
which is the only way a finished `EvaluationRun` keeps describing what it
actually ran.

### `Completed` is execution state, not a verdict

`RunStatus` records what happened to the execution: it ran and finished, it
failed, or it was cancelled. It does not record whether the candidate passed.

No `Passed`, `Score`, `Grade`, or `Promotable` field exists on a run.
Deterministic hard gates are [task 056](../tasks/v1.0/README.md) and promotion
is task 066; both read a run's *evidence* and decide separately. A boolean
here would let a run answer a question it holds no evidence for, and — worse
— would be the obvious field for a future caller to read instead of the gate.

## Alternatives considered

**Generate identifiers in the domain constructor.** Rejected. It makes
construction non-deterministic, forces a clock or randomness into a package
that otherwise needs neither, and makes every test assert around a value it
cannot predict. It also decides for tasks 057 and 058 what their identifiers
look like, which is precisely the decision they should make.

**Require UUIDs.** Rejected. It adds a dependency for a constraint nothing
needs, forbids identifiers a human can read, and would make a CLI's natural
`my-agent/v3` illegal for no safety gain. Opaque means opaque.

**A `map[string]string` for candidate metadata.** Rejected. Fixed optional
fields bound the total size by construction rather than by a counted limit,
remove the aliasing question (there is nothing to deep-copy), keep the shape
reviewable, and make it obvious that this is descriptive context rather than
a place to put arbitrary payload. A field can be added later; an open map
cannot be taken back.

**Mutable entities with a `Validate()` method.** Rejected for the lifecycle
specifically. Validation after the fact cannot express "this transition was
not allowed *from that state*" — by the time `Validate` runs, the previous
state is gone. Transition methods make the illegal states unreachable instead
of detectable.

## Consequences

Uniqueness, existence, and referential integrity are unenforced at this
layer, by design. `Agent.ProjectID` and `Candidate.AgentID` are carried so
that a service holding both can check them; the domain only guarantees the
reference is well-formed.

Later tasks inherit a constraint rather than a mechanism: whatever creates an
entity owns its identifier. Task 057 must not add generation to a store
interface as a side effect of persistence, and task 058 must decide
explicitly whether a client supplies one.

Task 056's gate result has nowhere to live on `EvaluationRun`, which is
intended. It will need its own type, and that type will be about a verdict
rather than about an execution.
