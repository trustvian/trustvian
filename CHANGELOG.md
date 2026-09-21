# Changelog

All notable changes to Trustvian are documented in this file. Prior to
`v0.1`, the project was pre-release iteration on a single evolving
`develop` branch with no tagged snapshots — this file starts at the
point where a tag first exists for something external users can
actually depend on.

## Unreleased

### Security

- **Per-actor fingerprint state is now bounded.** `Baseline.Fingerprints`
  had no upper limit, and `Fingerprint.ID` is derived from `Event` fields
  the caller supplies — so one actor emitting a distinct operation name
  per call could grow its baseline, and its PostgreSQL row, without
  limit. `internal/baseline` now caps a baseline at 512 fingerprint
  identities and refuses admission of new ones beyond it.

  The bound refuses rather than evicts. Nothing already learned is
  removed to make room, because an absent fingerprint scores as
  maximally novel with zero confidence and therefore contributes nothing
  to trust — evicting learned entries under pressure would let a flood of
  manufactured fingerprints suppress detection for an actor rather than
  merely cost memory. See
  [ADR 0019](docs/adr/0019-bounded-fingerprint-admission.md).

  A baseline already holding more than 512 identities, learned before
  this bound existed, keeps every one of them and keeps updating them; it
  admits nothing new and is never truncated. Upgrading from `v0.9`
  requires no migration and loses no learned state.

  Behavioral change, confined to actors past the cap: a new fingerprint
  observed by an actor already holding 512 is analyzed and decided
  normally but is not learned, so it continues to score as unknown.
  Known fingerprints keep learning regardless of how full the baseline
  is.

  No public API, configuration, CLI, Collector, or storage schema change.

### Added

- **Deterministic hard gates: a scorecard plus explicit limits becomes a
  verdict.** `platform.EvaluateEvaluationGate` pairs an `EvaluationScorecard`
  with a caller-owned `EvaluationGatePolicy` and returns a fixed-shape
  `EvaluationGateResult` carrying five checks and a PASS/FAIL `GateVerdict`.

  The five checks, all evaluated on every call: reference evidence present,
  candidate evidence present, added behaviors within limit, candidate block
  decisions within limit, candidate critical-risk observations within limit.
  PASS requires all five, and there is no short-circuit — a FAIL reports
  everything measured.

  Every comparison is an integer. No mean, rate, delta, or presence ratio
  takes part in a verdict: floating-point sums are not guaranteed
  bit-identical under record reordering, and an average is precisely how
  strength in one dimension offsets a condition in another.

  Fail-closed throughout. An unbound policy or unbound scorecard is an error.
  A *valid* evaluation that observed nothing is not — it fails the two
  non-configurable sufficiency gates, which exist because a candidate that
  ran zero records satisfies every maximum. `0` is a strict limit rather than
  "unset", so a private marker separates the strictest policy from an absent
  one.

  Names stay factual: a block decision is the policy engine doing what it was
  configured to do, not a violation; a critical-risk observation is a risk
  classification, not an incident. Gates the evidence cannot support —
  critical policy violations, sensitive-resource access, approval compliance,
  per-rule or delegation compliance — remain absent rather than reported as
  zero, and `docs/ROADMAP.md` now separates implemented evidence-backed gates
  from those deferred until an explicit evidence contract exists.

  A verdict performs nothing. PASS means only that the configured gates
  passed; promotion is a later task. No change to `EvaluationScorecard`, no
  core runtime change, no persistence, and no transport. See [ADR
  0029](docs/adr/0029-hard-gates-use-explicit-integer-evidence.md).

- **Evaluation scorecards: one fixed-shape comparison of two evaluations.**
  `platform.NewEvaluationScorecard` composes the evidence tasks 053 and 054
  already produce:

  ```text
  reference EvaluationAggregate ───────┐
  BehaviorDiff(reference → candidate) ─┼──▶ EvaluationScorecard
  candidate EvaluationAggregate ───────┘
  ```

  It reports how the decision, risk, approval and policy-selection
  distributions moved, how the five numeric signals moved, and how much
  behavioral presence overlapped — counts, rates and signed deltas.

  **No third reducer.** Records were consumed once, by two reducers designed
  against the same stream; re-reading them would be a third ingestion path
  with a third chance to disagree. Both aggregates are required, because
  comparison is the point, and the diff cannot be derived from them nor they
  from it.

  **Evidence must describe one comparison.** Each aggregate is matched
  against its own side of the diff, all three must agree on the environment,
  and both observation counts must match — catching the case where one
  reducer saw records the other did not. There is no partial card.

  **Fixed-shape and O(1).** Construction costs 214 ns and zero allocations
  for a 1,024-delta comparison over 2,048 records — identical to an empty
  one, because only summary accessors are read. The diff's deltas are not
  copied; a caller wanting per-behavior rows reads the `BehaviorDiff` it
  already holds.

  **Still evidence, not judgement.** No overall score, weight, threshold, or
  verdict: a composite would let one dimension offset another, which the
  roadmap forbids, and would become the number read instead of the gate.
  Empty denominators are undefined rather than zero — two evaluations that
  observed nothing are unmeasured, not identical.

  **Unsupported semantics are absent, not zero.** Critical policy violations,
  blocked or unapproved sensitive actions, per-rule compliance and delegation
  stability have no field, because no current evidence can express them.
  Reporting `0` would be a false security claim. Task 056 must define where
  severity and sensitivity come from before those gates can exist. See [ADR
  0028](docs/adr/0028-scorecards-are-fixed-shape-comparative-evidence.md).

- **Behavioral diff: which behavioral shapes changed between two
  evaluations.** `BehaviorCollector` reduces a `DecisionRecord` stream into a
  bounded set of behaviors; `BehaviorSnapshot` is the detached, deterministic
  result; `CompareBehaviorSnapshots` reports each behavior as `Added`,
  `Removed` or `Shared` with counts and normalized frequencies.

  ```text
  DecisionRecord ──▶ BehaviorCollector ──▶ BehaviorSnapshot ──▶ BehaviorDiff
  ```

  The comparison key is `FingerprintID` and nothing else — not actor, session,
  candidate, run, profile, commit, or digest. A diff keyed by any of those
  would report change every time a candidate was rebuilt.

  **`EvaluationAggregate` is unchanged.** [ADR
  0026](docs/adr/0026-evaluation-aggregation-is-bounded-evidence.md) made it
  O(1) and left this task to bring its own bounded contract; this is that
  contract, as a separate reducer. An evaluation that will never be compared
  pays nothing for behavioral bookkeeping.

  **Bounded, and loud when it cannot answer.** At most 512 distinct behaviors
  per collector and 1,024 deltas per diff; every retained string capped at 256
  bytes and rejected rather than truncated, because two behaviors must not
  merge because a name was shortened. A 513th distinct behavior marks the
  collector **permanently incomplete**, and an incomplete snapshot **cannot be
  compared** — silently comparing the first 512 would produce a confident,
  specific, wrong "new behavior" count.

  Fingerprint identity and behavior descriptor must agree **one-to-one, both
  ways**. One fingerprint with two shapes would merge two behaviors; one shape
  under two fingerprints would be reported as removed-and-added, claiming
  behavior changed when only its encoding did. Both fail closed, in the
  collector and again during comparison. The platform never recomputes the
  core's hash to check this, so the fingerprint algorithm stays free to
  change.

  Rates are derived from integer counts at comparison time, so equal counts
  give identical rates whatever order records arrived in. Environments must
  match; candidate, run and profile references may differ, since [task
  051](docs/tasks/v1.0/051-behavioral-profile-learning-scope-isolation.md)
  kept learning scope out of behavioral identity.

  **Evidence, not judgement.** No drift score, severity, threshold, pass, or
  promotability. `AddedCount` is the factual basis a later gate may build on.
  See [ADR
  0027](docs/adr/0027-behavioral-diff-compares-bounded-snapshots.md).

- **Evaluation result aggregation: the platform now consumes engine
  evidence.** `platform.EvaluationAggregate` folds
  `trustvian.DecisionRecord` values into a bounded, fixed-shape summary of
  what one evaluation observed:

  ```text
  Engine ──▶ DecisionRecord ──▶ EvaluationAggregate
  ```

  It counts observations, decisions by category, risk levels, approval
  evidence, and policy-selection shape; summarizes the five numeric signals as
  `{Count, Sum, Min, Max}` with an explicitly-absent mean when empty; and
  bounds the evidence in event time. `AddRecord` returns a new aggregate, so
  the receiver is never mutated and a rejected record leaves it identical.

  **This is the first real platform → core dependency, and it needed no core
  change** — which is the claim [task
  050](docs/tasks/v1.0/050-public-serializable-decision-record.md) built
  `DecisionRecord` to make good on. The platform imports the public API only;
  `policy.Decision` and `trust.RiskLevel` stay internal, so their small closed
  sets of string values are re-declared rather than the core's surface being
  widened.

  **Bounded by construction.** O(1) memory in the number of records: no slice,
  no map, no retained record, and no deduplication set. Retaining records
  would make it an accidental event archive, which is a separate capability
  with its own boundary. One consequence is stated rather than left implicit:
  duplicates count twice, because idempotency needs a retention window only an
  ingest boundary can define.

  **Evidence, not judgement.** No score, grade, pass, promotability,
  critical-violation count, or new-behavior count — each needs context the
  aggregate does not hold, and would become the field people read instead of
  the gate. See [ADR
  0026](docs/adr/0026-evaluation-aggregation-is-bounded-evidence.md).

  Malformed input fails closed: every consumed field is validated before any
  state changes, and nothing is clamped or coerced. Cross-environment records
  are refused. `MatchedDefault` and `PolicyRule` are validated as one pairing,
  so a hand-built record cannot claim a rule matched while naming none.
  `NewEvaluationAggregate` returns an error rather than binding evidence to an
  invalid or zero-value run, and a zero-value aggregate accepts no record at
  all — both zero values are writable from any package, since unexported
  fields prevent mutation rather than construction. Rejection paths echo only
  a bounded preview of untrusted strings, so a malformed record cannot turn an
  error into an amplification primitive. `AddRecord` costs 86 ns and zero
  allocations.

- **`platform/`: the control-plane domain, as a fourth Go module.** The first
  platform-layer runtime code — `Project`, `Agent`, `Candidate`,
  `EvaluationRun`, and opaque `EnvironmentRef` / `BehavioralProfileRef`
  references — establishing what an evaluation is, what it belongs to, and
  what may change once one has begun.

  ```text
  Project
    └─ Agent
        └─ Candidate
            └─ EvaluationRun ──▶ EnvironmentRef
                            └──▶ BehavioralProfileRef
  ```

  A separate module at `trustvian-platform`, deliberately **not** under
  `github.com/trustvian/trustvian`: Go's `internal/` rule turns on
  import-path ancestry rather than module membership, so a repository-prefixed
  path would be allowed to import the engine's internal packages, and this one
  is a compile error instead. The domain itself needed nothing from the core,
  and adding a dependency to demonstrate the relationship would have been the
  speculative coupling the boundary exists to prevent — so the module began
  with none. Aggregation introduced one, on its own merits, in the entry
  above.

  Identifiers are typed, opaque, and caller-owned: the domain generates none,
  reads no clock, and requires no UUID format. Candidate metadata is a fixed
  set of optional descriptive fields rather than a map — bounded by
  construction, with nothing to alias — and never becomes behavioral identity.
  A run's `Status` records that an execution finished, never that a candidate
  passed; gates and promotion are separate later concerns. See [ADR
  0025](docs/adr/0025-platform-domain-values-with-caller-owned-identity.md).

  **No engine change.** `scripts/check-platform-boundary.sh` enforces both
  directions of [ADR 0022](docs/adr/0022-core-platform-boundary.md)'s
  boundary in CI: no core `internal/*` import in the platform, no platform
  package in the core's build graph, and no platform identifier declared in
  core runtime code.

  Nothing here is usable yet: no persistence, transport, aggregation,
  behavioral diff, scorecard, gate, or promotion. Those are later tasks, and
  each would have been easier to add now than to remove later.

- **Learning scopes: independent behavioral history under one actor
  identity.** `trustvian.WithLearningScope("...")` partitions an Engine's
  learned state. Two engines over one store with different scopes never
  share a `Baseline`, even for the same actor in the same environment —
  each accumulates its own history and each starts cold. Two engines with
  the same scope share one profile; the scope is the identity, not the
  Engine instance.

  `baseline.Key` gains a `Scope` field, so learned-state identity is
  `{Scope, ActorID, Environment}`. Everything downstream follows from that
  one change: `InMemory` shards by the whole key, `FileStore` persists the
  `Key` the `Baseline` already carries, PostgreSQL gets one composite
  primary key, and `Freezer` becomes scope-aware without learning that
  scopes exist.

  **Scope is not behavioral identity.** The same event analyzed under two
  scopes produces the same `StableFeatures` and the same `Fingerprint.ID`;
  only the learned evidence it is compared against differs. Scope is absent
  from the fingerprint hash, from `StableFeatures`, and from
  `DecisionRecord`.

  **Scope cannot come from an event.** It is Engine configuration, fixed at
  construction, and is never derived from `SessionID`, `TraceID`, or
  `Attributes` — so an event producer cannot choose which learned profile it
  trains. This is a trust boundary, not tidiness; see
  [SECURITY.md](docs/SECURITY.md#learning-scope-selection).

  The default scope is `""`. `NewEngine()` without the option behaves
  exactly as before and finds exactly the state it found before.

  The 512-fingerprint bound ([ADR
  0019](docs/adr/0019-bounded-fingerprint-admission.md)) is unchanged and
  now applies per scoped baseline: filling one scope leaves every other with
  its own independent capacity. The constant has never bounded the *number*
  of baselines and does not now.

  See [ADR
  0024](docs/adr/0024-learning-scope-is-a-baseline-key-dimension.md).

- **`DecisionRecord`: a serializable public projection of one analysis.**
  `Result` is readable from outside the module, but four of its fields have
  types from `internal/`, so a consumer could inspect a result without being
  able to declare, store, or serialize one. `result.DecisionRecord()` returns
  a public type with explicit JSON field names carrying the evidence behind a
  decision: behavioral shape, fingerprint, anomaly score and contributors,
  trust and risk, the policy rule and reason, and correlation identifiers.

  It carries no raw event payload — `Event.Attributes`, tool arguments,
  prompts, and completions have no field and cannot reach its JSON — and no
  consumer-side identifiers. Both are asserted by test. The projection is
  pure: no I/O, no clock, no scoring, and its slices are copied rather than
  aliased, so a record shares no memory with the `Result` it came from.

  `StableFeatures` gained JSON tags so the record serializes consistently.
  The type is unreleased, so no published representation changed.

- **Every `Engine` option is now usable from outside the module.** Two
  were exported but uncallable by third-party code, because their
  parameter types live under `internal/` and no public path produced
  one:

  - `WithTrustConfig` gains `config.TrustConfig` and
    `config.CompileTrust`, following the pattern `CompilePolicy`,
    `CompileStorage`, and `CompileAnomaly` already established. Risk
    thresholds are pointers, so an unset one keeps its default rather
    than silently becoming zero — a zero threshold would classify every
    result as Critical.
  - `WithContextRisk` now takes `func(trustvian.StableFeatures) float64`.
    `StableFeatures` is a new public type carrying the six stable
    dimensions of an event and nothing per-occurrence. See
    [ADR 0021](docs/adr/0021-public-stable-features-boundary.md).

  `examples/configured-engine` configures all five options from a
  separate Go module, which is what proves the path works.

  **Source-breaking for in-module callers of `WithContextRisk`**, whose
  signature changed. Done deliberately before the `v1` freeze, when it
  costs nothing, rather than after it, when it would cost a major
  version. No in-repository caller used the option.

### Fixed

- **A pathological `WithContextRisk` callback could make a successful
  analysis unserializable.** `trust.Compute` clamps its inputs with
  `min`/`max`, which propagate `NaN`, so a callback returning `NaN` put one
  into `Trust.ContextRisk` and `Trust.Score` — and `encoding/json` refuses
  non-finite floats. `Analyze` returned success and the resulting record
  could not be marshalled.

  A non-finite input is now treated as invalid rather than as a position on
  the scale, and resolves to whichever end trusts least: context risk and the
  anomaly inputs to `1`, identity confidence to `0`. Both directions fail
  closed, and `-Inf` no longer reads as "no risk". Analysis still succeeds,
  because a detector that stops deciding when a caller's callback misbehaves
  is worse than one that assumes the worst. Ordinary out-of-range finite
  values keep their documented clamp behavior, and the trust formula is
  unchanged.

- **An unencodable `Event.Timestamp` could make a successful analysis
  unserializable.** `Event.Validate()` rejected a zero timestamp but accepted
  every other `time.Time` — and not every `time.Time` is one
  `time.Time.MarshalJSON` will encode. A year outside `[0,9999]` or a zone
  offset of 24 hours or more is constructible in Go and refused by RFC 3339,
  so such an event analyzed successfully and produced a `DecisionRecord`
  `json.Marshal` would not accept.

  `Validate()` now rejects both cases with a new `event.ErrInvalidTimestamp`,
  wrapped by `Analyze` through the existing `trustvian: invalid event:` path.
  The rule is exactly the standard library's, asserted by a test that derives
  its expectation from `MarshalJSON` rather than restating it, so the check
  can neither drift permissive nor start refusing timestamps Go accepts.

  A missing timestamp still returns `ErrMissingTimestamp`; the zero time is
  encodable, so only the earlier check distinguishes absent from unencodable.
  Rejection is at the input, where the caller still holds the event:
  `DecisionRecord()` does not sanitize, because a security record with a
  silently rewritten timestamp is worse than a refused event.

  Behavioral change for callers submitting such timestamps — previously
  accepted, now an error. No in-repository caller produces one, and no
  encodable timestamp's treatment changed.

- **`Engine.Observe` reported learning that did not happen.**
  `Baseline.Observe` refuses an unknown fingerprint once a baseline holds
  512 identities, but the store call still succeeded, so `Observe` returned
  `learned == true` regardless. Task 049 recorded this as an open conflict:
  a long run against a wide-surface actor could quietly stop learning while
  reporting that it had.

  The admission outcome now propagates from `Baseline.Observe` through
  `Store.Observe` to `Engine.Observe`. `learned` is false for an ineligible
  decision, false when an unknown fingerprint is refused at capacity, false
  when the store is frozen, and true when a known fingerprint keeps learning
  at capacity.

  **Admission policy is unchanged** — ADR 0019's refuse-never-evict stands,
  capacity refusal is still not an error, and analysis is unaffected. Only
  the reporting changed.

### Changed

- **Persisted state carries learning scopes, and both backends versioned.**
  The FileStore snapshot moves to `version: 2` and PostgreSQL to
  `SchemaVersion = 2`. Each reads its predecessor and upgrades in place:
  every pre-scope baseline lands in the default scope with its learned state
  unchanged, because a `Key` with no `Scope` field deserializes to exactly
  that. Nothing is invented, and no learned state moves between scopes.

  PostgreSQL's upgrade adds `scope text NOT NULL`, makes the primary key
  `(scope, actor_id, environment)`, and restamps each row's derived
  `schema_version` — all inside the existing migration transaction and
  advisory lock, so it is atomic and safe against a racing process. Scope is
  part of the primary key deliberately: storing it only in the jsonb would
  let a second scope's insert collide with the first's row.

  **Downgrade is not supported, deliberately.** Adding a JSON field is
  normally additive, but `Scope` changes what a record *identifies* — one
  version-2 snapshot can hold two baselines a version-1 reader sees as the
  same key, and it would keep whichever it loaded last. A file an old binary
  refuses is a recoverable operational problem; two learned profiles
  silently merged is corrupted state nothing detects. Recovery from a
  downgrade is restoring a pre-upgrade backup.

- **Source-breaking for in-module callers of the `store.Store` port.**
  `Observe` returns `(baseline.Baseline, bool, error)`. `Store` is not a
  public extension point — see below — so no external consumer is affected.

- Documented explicitly that **custom `Store` implementations are not a
  v1 extension point**. `store.Store` references internal types and
  stays internal; persistence is selected through
  `config.StorageConfig` from the shipped backends. Also documented that
  a compiled store is owned by the caller — an `Engine` never closes one
  it was handed, and there is no `Engine.Close`.

- `Engine`'s documentation claimed full configuration required code
  inside the module. That stopped being true when `CompilePolicy`,
  `CompileAnomaly`, and `CompileStorage` shipped; it is now accurate.
  A `config.StorageConfig` field also still described PostgreSQL as
  recognized but unimplemented, two years after it shipped.

- **A compatibility contract covering every observable surface**, not
  just the Go API: [docs/compatibility.md](docs/compatibility.md). The
  `v0.1.0` promise in this file covers `event.Event`, `Result`, and
  `Engine`; configuration schemas, CLI flags and exit codes, environment
  variables, Collector configuration, storage formats, metric names and
  labels, health endpoints, the webhook envelope, container interface,
  and release artifacts were all outside it. Each is now classified,
  with the version bump a breaking change costs.

  It also states how behavioral change is treated, which type signatures
  cannot express: enabling a signal by default or altering which
  decisions train the baseline is breaking, whereas correcting a signal
  against its documented formula is a fix. Deprecation is
  version-based — one subsequent minor, minimum — and the security
  exception is deliberately narrow. See
  [ADR 0020](docs/adr/0020-v1-compatibility-contract.md).

  Documentation only: no API, configuration, storage, or behavior
  change.

### Fixed

- Documentation stated a per-actor memory bound the implementation did
  not enforce: `docs/observability.md`'s growth table extrapolated from a
  120-fingerprint measurement as though fingerprint count were capped.
  `observability.md`, `SECURITY.md`, `storage-guide.md`, and
  `PERFORMANCE.md` now distinguish the structural cap from the one size
  figure actually measured, and separate per-actor growth (bounded) from
  actor-count growth (deliberately not).

## v0.9.0 — Operational Readiness

Makes Trustvian *operable*, where `v0.8` made it deployable. It adds no
behavioral capability: every quality gate now runs on a machine rather than
by discipline, releases produce verifiable artifacts, the runtime reports
its own health and shuts down cleanly, it emits bounded operational
metrics, and the learned behavioral state it protects can be backed up,
restored, and carried across an upgrade — each of those proven by a test
rather than a procedure.

Seven vertical slices (039–045), all additive. Existing deployments need no
configuration change: the health endpoints are opt-in, the metrics require
no wiring, and `InMemory` remains the default store.

One change is visible to Go consumers: the module path is now lowercase
(`github.com/trustvian/trustvian`) following the organization rename. See
[Changed](#changed) below for what that means for an existing import.

### Added

- **CI & quality gate automation**
  ([task 039](docs/archive/tasks/v0.9/039-ci-quality-gate-automation.md), first `v0.9`
  slice) — the checks this project has run by hand since `v0.1` now run
  automatically, across every module and boundary a release depends on.

  The previous workflow was the stock GitHub Go template: root module
  only, `go build` plus `go test`, and triggered solely on pull requests
  targeting `main`. Since work lands on `develop` and reaches `main` by
  release-time pull request, CI effectively first reported at the moment of
  release — and never covered `gofmt`, `go vet`, `-race`, the processor
  module, the examples module, PostgreSQL persistence, or the reference
  deployment at all.

  `.github/workflows/ci.yml` now runs on push and pull request for both
  `main` and `develop`: format, vet, build, test, and race across the root
  module; the processor and examples modules built and tested with
  `GOWORK=off`, so a Go workspace cannot mask a broken module boundary; a
  check that `examples/` imports no `internal/*`; PostgreSQL integration
  against a service container using the existing
  `TRUSTVIAN_TEST_POSTGRES_DSN` mechanism with no test modified; and
  validation of the reference deployment's Compose configuration.

  `.github/workflows/nightly.yml` carries the expensive tiers — the full
  PostgreSQL stress suite and the deployment's end-to-end smoke test — on a
  schedule and on demand. The split is by cost, not importance: correctness
  gates belong on pull requests, while contention benchmarks are more
  useful as a trend than as a tax on every push.

  Both workflows are read-only (`permissions: contents: read`), require no
  secrets, and publish nothing, so pull requests from forks run the full
  gate set with nothing to leak. Release and container publishing are
  deliberately later slices.

- **Release artifacts and module consistency**
  ([task 040](docs/archive/tasks/v0.9/040-release-artifacts-and-module-consistency.md),
  second `v0.9` slice) — releases through `v0.8.0` were produced entirely
  by hand, and no release has ever carried a binary. Trustvian now has a
  tag-triggered release pipeline and a documented module publication model.

  **`trustvian version`** reports the module version, VCS revision, commit
  time, and whether the build was made from a modified tree — read from
  Go's own build information, so there is no `-ldflags` contract to honor
  and no package-level version variable.

  **Release binaries.** `scripts/release-build.sh` cross-compiles the CLI
  for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, and
  `windows/amd64`, archives each with `LICENSE` and `README.md`, and emits
  a verified SHA-256 `checksums.txt`. Built with `CGO_ENABLED=0` and
  `-trimpath`. `make release-dry-run` runs the whole matrix locally with no
  tag, credentials, or upload, and CI runs it on every push — so a broken
  target surfaces on the pull request rather than after a tag is public.

  **`.github/workflows/release.yml`** triggers on a pushed `v*` tag,
  rejects non-SemVer tags, verifies the checkout is the tagged commit,
  re-runs the full quality gates against that source, builds the matrix,
  and opens a **draft** GitHub Release with the artifacts attached for a
  maintainer to review and publish. It takes `contents: write` and nothing
  else; `ci.yml` and `nightly.yml` remain read-only.

  **Module publication model, written down and enforced.** The root module
  is the published one. The `processor` and `examples` modules declare
  non-resolvable paths (`trustvian-processor`, `trustvian-examples`) and
  have never been tagged — they are repository-internal by construction,
  and their `replace ... => ../` directives are correct for that, not a
  release blocker awaiting removal. `scripts/check-modules.sh` enforces the
  invariants this depends on — the published module declares no `replace`,
  every declared root version exists as a tag, and a module cannot be
  promoted to a resolvable path while still carrying a local replace — and
  runs in CI. See [`docs/release-guide.md`](docs/release-guide.md).

  `processor/go.mod`'s root requirement moved from `v0.5.0` to `v0.8.0`,
  the release that first contained the `config.StorageConfig` API it uses.
  This changes no resolution — the `replace` still supplies the code — but
  the declared floor is now truthful, and it is verified rather than
  assumed: with the replace removed, the processor builds against the
  published `v0.8.0` from the module proxy.

  The consistency check establishes that a declared version is real from
  either a local git tag or the module proxy, rather than requiring a local
  tag. Requiring one conflated *the version existing* with *the checkout
  having fetched tags*, which made the check fail on shallow CI checkouts
  while passing on developer machines. A checkout that can consult neither
  source now reports that explicitly instead of passing or blaming the
  version.

- **Runtime liveness and readiness endpoints, and bounded graceful
  shutdown** for the Collector runtime. Opt-in via a `health:` block on the
  processor's configuration; omitting it leaves existing Collector configs
  behaving exactly as before.

  ```yaml
  processors:
    trustvian:
      health:
        endpoint: 0.0.0.0:13133      # default
        readiness_timeout: 2s        # default
  ```

  `GET /livez` reports whether the runtime is functioning. It deliberately
  does **not** consult the database: restarting a process because its
  dependency is unreachable produces a restart storm that cannot fix
  anything. `GET /readyz` reports whether this instance can safely process
  work, and does consult the configured store — so PostgreSQL configured and
  unusable returns 503 rather than silently falling back to non-durable
  storage. A store with no external dependency (in-memory, file) is ready
  once started.

  Both return `200 {"status":"ok"}` or `503` with a status string and
  nothing else: no DSN, hostname, database error, or behavioral state. The
  reason a probe failed goes to the runtime's logs. Readiness probes are
  bounded by `readiness_timeout` and cost one driver-level ping — no
  migration, no query, no state load.

  Shutdown transitions readiness to not-ready first, then stops the health
  server, then closes the Store exactly once; repeated shutdown is safe.
  Signal handling and in-flight draining remain the Collector framework's,
  which already stops receivers before processors — this runtime adds no
  second shutdown owner.

  The official image documents port 13133 but adds **no Docker
  `HEALTHCHECK`**: it has no shell by design, and adding one would discard a
  deliberate security property. External HTTP probes are the intended
  mechanism.

- **Operational metrics for the Collector runtime**
  ([task 043](docs/archive/tasks/v0.9/043-self-observability-resource-safety.md)) — five
  OpenTelemetry instruments emitted through the `MeterProvider` the
  Collector already injects. Nothing to configure, no vendor client, and no
  second telemetry backend:

  | Metric | Type | Unit | Attributes |
  |---|---|---|---|
  | `trustvian.analyses` | Counter | `{analysis}` | `trustvian.outcome`: `analyzed`, `invalid_event`, `error` |
  | `trustvian.decisions` | Counter | `{decision}` | `trustvian.decision`: the six `policy.Decision` values, plus `other` |
  | `trustvian.analysis.duration` | Histogram | `s` | — |
  | `trustvian.observations` | Counter | `{observation}` | `trustvian.outcome`: `learned`, `not_eligible`, `error` |
  | `trustvian.observe.duration` | Histogram | `s` | — |

  These are the facts only Trustvian knows. Span counts are deliberately
  **not** among them: the Collector already counts spans accepted, refused,
  and dropped per component, and a parallel Trustvian counter would be a
  second, subtly different number for the same thing.

  **Cardinality is fixed at 15 time series**, regardless of how many actors,
  environments, or tenants a deployment sees — and it is enforced
  structurally rather than by review. Every attribute vocabulary is closed,
  and its measurement options are pre-built at construction, so a value with
  no entry has no option to record with; an unrecognized decision records as
  `other` instead of opening a series. Actor IDs, trace IDs, raw error text,
  DSNs, and behavioral payload cannot become labels, which a test asserts
  against recorded telemetry rather than against documentation.

  Instrumentation lives in the processor, never in the engine, so the core
  module's zero-OpenTelemetry dependency graph is unchanged and no SDK
  consumer gains a dependency. Recording is an in-process update — a failing
  metrics backend cannot slow, block, or alter a security decision — and the
  provider's lifecycle stays with the Collector, which created it.

  The span path stays allocation-free: `BenchmarkConsumeTraces` reports the
  same 33 allocs/op as before these metrics existed. With a real metrics SDK
  attached it costs ~350 ns more per span, which is the SDK's own
  aggregation across five measurements and is paid only when a metrics
  pipeline is configured. See
  [`docs/observability.md`](docs/observability.md).

  Both duration histograms carry explicit bucket boundaries (1 ms to 10 s).
  The SDK's defaults assume milliseconds; these instruments record seconds,
  so without the advice every measurement lands in the first bucket.

  Note that the bundled `cmd/trustvian-collector` — the minimal demo binary
  the reference deployment runs — supplies a **no-op `MeterProvider`**, so
  it records these metrics and exports none of them. That binary's
  hand-built telemetry factory exists to keep the demo's dependency graph
  small. Collecting them for real means building a Collector distribution
  with `ocb` and `otelconftelemetry`, which is how a production
  distribution is built anyway.

  Existing deployments need no configuration change.

- **Backup, restore, and upgrade procedures for learned behavioral state**
  ([task 044](docs/archive/tasks/v0.9/044-operations-backup-restore-upgrade.md)) —
  documented, tested, and built on PostgreSQL's own `pg_dump`/`pg_restore`
  rather than a Trustvian backup format.

  **[`docs/operations.md`](docs/operations.md)** is the one runbook: what
  Trustvian persists (and that policy and configuration never live in the
  database), taking a backup, restoring it safely, verifying learned state,
  upgrading, rolling back, a compatibility matrix, and a recovery drill. The
  `memory` store is stated plainly as having no recovery story.

  **`scripts/backup-postgres.sh`** takes an **online** backup — no downtime:
  `pg_dump` reads one snapshot and every Trustvian write is a single-row
  transaction, which a concurrent-writer test confirms. It produces a new
  directory with a custom-format archive (schema and data), a `MANIFEST`
  carrying no connection detail, and `SHA256SUMS`; artifacts are `0600`
  inside a `0700` directory; an existing path is never overwritten; and
  connection settings come only from the libpq environment, so no DSN ever
  appears on a command line.

  **`scripts/restore-postgres.sh`** restores only into an existing, empty
  database with no other sessions, requires matching checksums with no
  bypass, restores in a single transaction, and verifies the result's
  structure. Schema compatibility stays with Trustvian's own startup check —
  the script does not re-implement it. On failure the target is quarantined
  (`ALLOW_CONNECTIONS false`), because an empty database left behind by a
  failed restore would otherwise be adopted by Trustvian as a brand-new
  deployment and silently learn from zero. Neither script creates, drops,
  or renames a database, or moves traffic.

  **Recovery is proven by behavior, not row counts.** State learned through
  the real `Analyze`/`Observe` loop, backed up and restored, makes a new
  engine produce identical baselines and identical anomaly, trust, and
  decision results — which also differ from a cold start, so the comparison
  cannot pass vacuously. Upgrading is tested from the **real `v0.8.0`
  release**, built from its tag: the current release reads its database in
  place with identical results, the old release still reads it afterwards,
  a rollback restore reproduces pre-upgrade analysis, and both releases
  refuse a newer schema version.

  CI runs these on every pull request in a dedicated job with PostgreSQL 17
  client tools. `deployments/docker-compose/recovery-drill.sh` (also
  `make recovery-drill`, and nightly in CI) runs the whole chain on the real
  runtime — online backup, guarded restore, cutover, `/livez` and `/readyz`,
  learning continuing from the restored state, and readiness through a
  database outage.

  The reference deployment now enables the `health:` block (port 13133 on
  loopback) and gains `TRUSTVIAN_RUNTIME_POSTGRES_DB`, so cutting over to a
  restored database is one explicit variable. The drill found that setting
  it on a single command is not enough — Compose recreates the collector on
  the original database the next time a dependent service runs — so the
  documentation says to record it in `.env`.

- **[`docs/supply-chain.md`](docs/supply-chain.md)** — image identity,
  tagging, architectures, the scan policy, signing, and how to verify a
  published image. `docs/SECURITY.md` gains a supply-chain section kept
  explicitly distinct from Trustvian's runtime behavioral security.

- **[`docs/release-guide.md`](docs/release-guide.md)** — maintainer-facing:
  the module model, how to prepare and verify a release, what the
  automation does, and what promoting the processor to a published module
  would require.

- **[`CONTRIBUTING.md`](CONTRIBUTING.md)** — how to run the gates locally,
  why the processor and examples modules must be verified with `GOWORK=off`,
  how to run the PostgreSQL tests, and what the test tiers mean.

- **Official container image, with supply-chain verification** — an
  OpenTelemetry Collector running the Trustvian processor, published to
  `ghcr.io/trustvian/trustvian-collector` on a version tag. **No image has
  been published yet**; the pipeline runs on the next release.

  Built from a root `Dockerfile` onto
  `gcr.io/distroless/static-debian12:nonroot` for `linux/amd64` and
  `linux/arm64`. ~43 MB, runs as uid 65532, no shell, no package manager,
  no added capabilities, and containing nothing but the Collector binary and
  the base image's CA certificates — which are there because PostgreSQL TLS
  needs a trust store. A `.dockerignore` keeps `.git`, `dist/`, and local
  env files out of the build context.

  Releases carry an immutable `vX.Y.Z` image tag as the deployment
  contract; `X.Y` and `latest` are convenience tags, and a prerelease moves
  neither.

  The existing Docker Compose reference deployment still builds from
  source. Running the repository does not require a published image.

- **Security policy and issue templates**
  ([task 045](docs/archive/tasks/v0.9/045-v0.9-stabilization-release-gate.md), the
  `v0.9` release gate). [`.github/SECURITY.md`](.github/SECURITY.md) gives
  vulnerability reporters a private path — GitHub's private vulnerability
  reporting — with what to include and what to expect; before it, the
  repository had no stated reporting path, and GitHub surfaced the threat
  model as the security policy. Minimal bug-report and feature-request
  templates, with a link routing security reports away from public issues.

### Changed

- **The Go module path is now `github.com/trustvian/trustvian`** (lowercase),
  following the rename of the GitHub organization from `Trustvian` to
  `trustvian`. Every import path, repository URL, badge, and clone/install
  command moves with it.

  **This changes the import path for consumers.** Go module paths are
  case-sensitive, so the old and new paths are different modules to the
  toolchain:

  ```go
  import trustvian "github.com/trustvian/trustvian"   // from the next release
  ```

  Releases up to and including `v0.8.0` were published as
  `github.com/Trustvian/trustvian` and remain resolvable only under that
  path — their `go.mod` declares it. The lowercase path is resolvable from
  the first release tagged after the rename; until then,
  `go get github.com/trustvian/trustvian@latest` cannot resolve, and
  existing consumers should stay on the old path. Do not import both paths
  in one build: the toolchain would treat them as two modules and duplicate
  every type.

  The product name is unchanged. Only the organization identifier, and the
  technical paths derived from it, are lowercase.

- GitHub Actions workflows now use `actions/checkout@v7` and
  `actions/setup-go@v7`, replacing the `@v4`/`@v5` majors that run on the
  deprecated Node.js 20 runtime. Both new majors run on Node.js 24; the
  only breaking change across the intervening majors was that runtime move.
  Workflow permissions are unchanged.

- The nightly workflow now runs the PostgreSQL database-restart durability
  test, which previously ran in no workflow.

### Fixed

- **The release workflow could not run to completion.** It referenced
  `sigstore/cosign-installer@v4`, a version that has never existed — that
  project publishes exact tags and no floating major — so the container job
  of the first release candidate failed at job setup, before building
  anything. The installer is pinned to `v4.1.2`, and
  `scripts/check-action-refs.sh` now verifies in CI that every action
  reference in every workflow resolves, since valid YAML naming a
  nonexistent action version passes both linting and review. It resolves
  references with `git ls-remote` and needs no token, and it distinguishes
  "this reference does not exist" from "the lookup could not be performed"
  — an earlier API-based version reported a valid action as nonexistent
  when the lookup itself failed. The checker has its own tests.

- **Container image references are normalized to lowercase.** The release
  workflow built its image name from the GitHub organization's display name
  (`Trustvian`), and an OCI repository name must be lowercase, so every
  container step failed on the first one that ran. `scripts/image-name.sh`
  is now the one source for `ghcr.io/trustvian/trustvian-collector`, shared
  by the release workflow, CI, and the Makefile, so the scanned, pushed,
  signed, and attested repository cannot diverge. CI now builds the same
  reference a release does, which is what makes the expression testable
  before a tag exists.

- **Release candidates are marked as prereleases.** `gh release create` was
  called without `--prerelease` for any tag, so publishing a
  `v0.9.0-rc.N` draft would have made it GitHub's "Latest release". A tag
  with a prerelease suffix now creates a prerelease, using the same test
  that already keeps the `X.Y` and `latest` image tags from moving.

- Documentation corrected against the code during the `v0.9` release gate:
  the getting-started SDK example claimed an `allow` decision where the
  default engine returns `observe_only`; the release guide still described
  image signing as future work; the contributor guide's test-tier table
  predated the vulnerability, backup/restore/upgrade, and recovery-drill
  gates; 31 broken internal documentation links; stale "planned for `v0.9`"
  comments in the reference deployment. Operator documentation now states
  PostgreSQL support explicitly — 17 tested, 13+ expected — which
  previously lived only in a task file.

### Security

- **Supply-chain controls for released artifacts.** An SPDX SBOM and SLSA
  provenance attestation are attached to each published image using
  BuildKit's built-in mechanisms, and the image digest is signed with
  keyless Cosign via GitHub OIDC — so no signing key exists in the
  repository or in a secret. Signatures are made over digests rather than
  tags, since a tag can be moved.

- **Vulnerability gates.** `govulncheck` now runs in CI for both modules and
  fails on any *reachable* vulnerability; Trivy scans the container image
  and fails on `CRITICAL`/`HIGH` findings **with a fix available**. Unfixed
  advisories are reported but do not block, because blocking on an
  unfixable upstream issue would prevent shipping our own security fixes.
  Policy and every live exception: [`docs/supply-chain.md`](docs/supply-chain.md).

- **Affected dependencies updated to clear reachable vulnerability
  findings** surfaced by the new supply-chain gate. `golang.org/x/text` moves
  to `v0.41.0` in the root module, resolving `GO-2026-5970`, which was
  reachable from `postgres.NewStore` through pgx's connection setup. The
  latest `pgx/v5` requires the affected version, so raising the floor
  directly was the only available fix. `golang.org/x/crypto` moves to
  `v0.56.0` in the processor module, clearing two further advisories.
  `golang.org/x/sync` follows to `v0.22.0` as a transitive requirement.

  One finding remains as a documented exception: `GO-2026-5932` in
  `golang.org/x/crypto` has no upstream fix and is not reachable from
  Trustvian code. It is recorded with its scope and review condition in
  [`docs/supply-chain.md`](docs/supply-chain.md); no gate was weakened and
  no blanket ignore was added.

- **Vulnerability scanning resolves dependencies the way a consumer does.**
  Every scan runs with the Go workspace disabled, for all three modules. A
  workspace raises each module's dependency versions to satisfy the others,
  which can mask a vulnerability that a consumer of one module alone is
  genuinely exposed to — as happened here. The examples module is now
  scanned as well.

- **Publishing permissions are scoped to the publishing job.**
  `packages: write` and `id-token: write` exist only in the container job of
  the release workflow; `ci.yml` and `nightly.yml` remain `contents: read`.
  Registry authentication uses the workflow-scoped token, so there is no
  long-lived credential to rotate. No workflow uses `pull_request_target`.

- **Backup and restore safety.** Backups hold every actor's behavioral
  profile and are created non-world-readable; no credential reaches a
  backup, its manifest, script output, or a process listing; corrupt
  backups are refused; a restore never overwrites a database in use; and a
  failed restore is quarantined instead of being left for Trustvian to
  initialize as empty. `docs/SECURITY.md` records each property with the
  test that proves it, and the limits: a checksum detects corruption but
  does not authenticate, and any future change to the persisted `Baseline`
  shape must bump the schema version, or a binary-only downgrade would
  silently drop the new fields.

- **Floating image tags move only after signing.** The release workflow
  previously pushed `vX.Y.Z`, `X.Y`, and `latest` together and then signed:
  a signing failure would have left `latest` pointing at an unsigned image.
  It now pushes only the immutable tag, signs its digest, and then retags
  `X.Y` and `latest` to that digest without a rebuild. The release job also
  re-runs `govulncheck` against the tagged source, since the vulnerability
  database changes independently of the commit. Partial-release states and
  their recovery are documented in
  [`docs/release-guide.md`](docs/release-guide.md#the-release-is-not-atomic).

### Removed

- `.github/workflows/go.yml`, the stock template superseded by `ci.yml`.
  Keeping it would have run a strictly weaker duplicate of the same gates.

## v0.8.0 — Production Runtime & Storage

Makes Trustvian deployable as a real production system rather than a
library and a CLI against a local file: behavioral state can now be
selected through public configuration, persisted to PostgreSQL with
transaction-safe concurrent learning, and run from a documented reference
deployment. Five vertical slices, all additive — `InMemory` remains the
default and every `v0.5`–`v0.7` behavior is preserved.

### Added

- **Production Store Contract & Public Selection Boundary**
  ([task 034](docs/archive/tasks/v0.8/034-production-store-contract-and-public-boundary.md),
  first `v0.8` slice) — `config.StorageConfig` +
  `config.CompileStorage` (plus `LoadStorage`/`LoadStorageFile`) make
  persistence selectable through public API for the first time. Before
  this, `store.Store` lived in `internal/store` with methods referencing
  internal types, so an external consumer could neither name nor
  implement it and no exported function returned one — `WithStore` was
  in-module-only in practice, silently pinning every external deployment
  to the in-memory default and losing all learned baselines on restart.
  Supported backends at the time of this slice were `memory` and `file`;
  `postgres` was recognized but not yet implemented (see the next entry,
  which implemented it). Any compile error yields a nil Store — never a
  silent downgrade to non-durable storage, which would lose exactly the
  state an operator asked to keep.
  `trustvian analyze` / `baseline build` gain `--storage-config <path>`,
  which makes `baseline build` genuinely useful: a corpus learned by one
  invocation is now scored against by a separate one. Also adds
  `TestStoreContract` — the nine `Store` guarantees, executable against
  every implementation from one place, including a
  same-key-concurrency (no-lost-updates) test that a naive
  read-then-compute-then-write database backend would fail — and a new
  [`examples/persistent-baseline`](examples/persistent-baseline/)
  demonstrating restart survival through public API alone. No new
  dependency, no schema, no PostgreSQL code, no change to
  `store.Store`/`InMemory`/`FileStore` behavior, and `NewEngine`'s
  in-memory default is unchanged. See [ADR
  0018](docs/adr/0018-production-store-boundary-and-postgresql-direction.md).

- **PostgreSQL Store implementation**
  ([task 035](docs/archive/tasks/v0.8/035-postgresql-store-implementation.md), second
  `v0.8` slice) — `type: postgres` is now functional. The new
  `internal/store/postgres` package implements the existing
  `internal/store.Store` port against PostgreSQL and passes all nine of
  `TestStoreContract`'s guarantees **unmodified** — no contract test was
  weakened to accommodate it. This is the backend that makes a
  horizontally-scaled deployment coherent: several Trustvian instances
  share one baseline instead of each holding its own opinion of what
  "normal" means for the same actor.

  Configuration is a new `postgres:` block on the existing
  `StorageConfig` document (`dsn`, optional `max_connections` and
  `connect_timeout_seconds`), so selecting a production database is a
  YAML edit rather than a new mechanism — usable from the Go SDK, a YAML
  file, and `--storage-config` on both CLI subcommands.

  `Observe` is atomic and lost-update-free, **including the first
  observation for a key that does not exist yet**: it runs `INSERT ...
  ON CONFLICT DO NOTHING` before `SELECT ... FOR UPDATE`, because
  `FOR UPDATE` locks nothing when no row matches. Both protections are
  verified by mutation testing rather than asserted. State lives in one
  row per `baseline.Key`, authoritative in a single `jsonb` column using
  the identical encoding `FileStore` already writes, with derived
  inspection-only scalar columns (`fingerprint_count`,
  `observation_count`, `last_observed`, ...) so an operator can query
  learned state with plain SQL. Schema creation is automatic,
  idempotent, transactional, and safe under concurrent startup; a
  mismatched recorded version aborts with `ErrSchemaVersionMismatch`
  rather than being silently upgraded.

  Fail-closed throughout: an unreachable, unauthenticated, or
  schema-incompatible database returns an error and a nil Store from
  `CompileStorage`, with **no fallback** to the file or memory backend at
  any layer. The DSN is never logged and never wrapped into an error —
  `pgxpool.ParseConfig`'s error specifically is not wrapped, because pgx
  echoes an unparseable DSN verbatim while redacting parseable ones, and
  there is a regression test for exactly that. Every SQL value is a bound
  parameter.

  Also adds `Close()` on the PostgreSQL store via `io.Closer` — an
  optional, type-asserted capability following the existing
  `store.Freezer` precedent, so `store.Store` itself gained no method —
  and [`docs/storage-guide.md`](docs/storage-guide.md).

  One new direct dependency: `github.com/jackc/pgx/v5`, confined to the
  new package (only `config/compile.go` imports it), so the driver stays
  out of the build of everything that imports `internal/store`.
  `InMemory` remains the default, `FileStore` is unchanged and not
  deprecated, and `go test ./...` passes with no PostgreSQL installed —
  every test needing a server skips on an unset
  `TRUSTVIAN_TEST_POSTGRES_DSN`.

- **Store durability, concurrency & migration hardening**
  ([task 036](docs/archive/tasks/v0.8/036-store-durability-concurrency-and-migration-hardening.md),
  third `v0.8` slice) — proves the PostgreSQL backend holds under
  production failure and concurrency conditions, and fixes two
  schema-metadata safety gaps found while doing so. No new backend, no new
  configuration, no new dependency.

  **Fixed — ambiguous schema metadata is now rejected instead of guessed
  at**, via a new `postgres.ErrAmbiguousSchemaState` (distinct from
  `ErrSchemaVersionMismatch`, so an operator can tell "wrong version" from
  "unreadable metadata"):

  - A database holding baseline rows but **no recorded schema version** was
    silently treated as fresh and stamped with the current version. An
    operator who restored a partial backup, or ran `DELETE FROM
    trustvian_schema_version`, could therefore have an older binary adopt
    state written by a newer one. "No recorded version" now means "new
    database" only when there is also no data.
  - **Multiple version rows** were resolved by an unordered `LIMIT 1`; a
    database marked version 99 was observed being accepted because a
    leftover version 1 row came back instead. Ambiguity now fails startup.

  Both are fail-closed by design: recovery is an operator decision, since
  guessing is precisely what a schema version check exists to prevent.

  **Verified guarantees** (all newly tested; see
  [`docs/storage-guide.md`](docs/storage-guide.md) for the operator-facing
  summary): no lost updates under heavy same-key and first-write
  contention; distinct keys do not serialize globally; a failed
  transaction leaves the previous baseline byte-identical; committed state
  survives a real **PostgreSQL restart** as well as a client restart;
  connection loss yields an explicit error with stored state equal to the
  acknowledged writes; cancellation and deadline expiry are reportable
  through `errors.Is`; a transaction waiting on a row lock is cancellable;
  an exhausted connection pool fails on the caller's deadline rather than
  hanging; no connection leaks on any path; `Close` is idempotent;
  migration is atomic and safe under simultaneous startup; a corrupt
  stored baseline fails loudly and is **never silently reset**.

  **Storage choice does not affect behavior.** InMemory, FileStore, and
  PostgreSQL now have an explicit test asserting they produce identical
  learned state, identical `Anomaly`/`Trust` values, and identical
  `Decision`s — along with the learning-eligibility (anti-poisoning) gate
  and the `v0.7` agent-security semantics holding on all three.

  **Documented, now that it is verified**: `Observe` runs at READ
  COMMITTED and relies on explicit row locking rather than isolation
  level; PostgreSQL deadlock is structurally impossible because exactly
  one row is locked per transaction; Trustvian performs **no** transaction
  retries and needs none; pool-sizing guidance; and that automatic
  migration means the runtime role needs table-creation rights on first
  run (never superuser), with instructions for splitting migration from
  runtime identity.

  Integration tests gain a stress tier, selected with the standard
  `-short` flag rather than a new mechanism. `go test ./...` still requires
  no PostgreSQL.

- **Reference Docker Compose deployment**
  ([task 037](docs/archive/tasks/v0.8/037-reference-docker-compose-deployment.md),
  fourth `v0.8` slice) — a runnable local stack demonstrating Trustvian
  analyzing real OTLP telemetry against PostgreSQL-backed behavioral state
  that survives a restart:

  ```bash
  cd deployments/docker-compose
  docker compose up -d --build
  docker compose run --rm demo-producer
  ```

  The topology is an OpenTelemetry Collector running the Trustvian
  processor, writing baselines to PostgreSQL. **No standalone Trustvian
  server was introduced** — the deployment containerizes the existing
  Collector-processor runtime. Includes a deterministic `smoke-test.sh`
  that proves the whole path (startup, analysis, persistence across
  `down`/`up`, fail-closed on an unavailable database, and `down -v`
  teardown) and exits non-zero on failure. Images are built locally from
  source; nothing is published.

  **The Collector processor can now select a Store.** `processors.trustvian`
  gains a `storage:` block using the same `config.StorageConfig` schema the
  Go SDK and CLI already accept:

  ```yaml
  processors:
    trustvian:
      storage:
        version: v1
        type: postgres
        postgres:
          dsn: ${env:TRUSTVIAN_POSTGRES_DSN}
  ```

  Before this, the processor called `NewEngine` with at most `WithPolicy`
  and never `WithStore`, so **every Collector deployment silently ran on
  the non-durable in-memory store and lost all learned baselines on
  restart** — the same reachability gap task 034 fixed for the CLI, in the
  one runtime that is actually long-lived. The block is decoded into
  `config.StorageConfig` and compiled by `config.CompileStorage`; the
  processor contains no database code, no DSN parsing, and no parallel
  storage configuration model. An unreachable database now fails Collector
  startup rather than falling back. `Shutdown` also releases the connection
  pool, which it previously did not.

  Omitting `storage:` preserves the previous in-memory behavior exactly.

  The deployment's PostgreSQL service doubles as the documented environment
  for the storage integration and stress suites (`make integration-postgres`),
  reusing the existing `TRUSTVIAN_TEST_POSTGRES_DSN` variable with no test
  changed.

### Changed

- `processor/go.mod` now uses a `replace` directive against the repository
  root instead of requiring `github.com/trustvian/trustvian v0.5.0`, which
  predates the storage configuration API the processor now needs. The
  release-time decision (bump to a released `v0.8.0` and drop the replace,
  or keep it) belongs to task 038.
- `cmd/trustvian-collector` registers the Collector's upstream
  `envprovider` alongside `fileprovider`, so `${env:VAR}` references in a
  Collector config resolve. This is what lets a deployment keep its
  database DSN out of its config file; no Trustvian-side interpolation
  mechanism was added.
- `newEngine` in `cmd/trustvian` now returns a cleanup function alongside
  the `Engine`, so the CLI releases the PostgreSQL connection pool on
  exit. Internal to the CLI; no public API change.

### Fixed

- A PostgreSQL integration test terminated *every* connection to the test
  database to simulate connection loss. Because `go test ./...` runs
  package binaries in parallel, it could kill connections belonging to
  other packages' tests mid-transaction, failing them with SQLSTATE 57P01.
  It now targets only its own store's connections, identified by
  `application_name`. Test-only; no product behavior involved.

### Removed

- `config.ErrStorageTypeNotImplemented`, added earlier in this same
  unreleased milestone for the state "a backend Trustvian recognizes but
  cannot construct." PostgreSQL was the only value ever in that state and is
  now implemented, leaving an exported sentinel no code path could return —
  something callers could write permanently dead `errors.Is` checks against.
  Removed during release stabilization rather than frozen by the release:
  it never appeared in a published version, so removing it now affects
  nobody, whereas removing it after `v0.8.0` would be a breaking change.
  Re-adding an error is backward-compatible if a future backend needs that
  distinction again.

### Security

Each item below is enforced by a test, not by convention:

- **Explicit storage selection fails closed.** A configured but unreachable,
  unauthenticated, or schema-incompatible PostgreSQL database is an error
  with a nil Store — there is no code path from "the database is
  unavailable" to a working in-memory or file store, at any layer (SDK, CLI,
  Collector processor, or external consumer). Silent persistence downgrade
  would mean every subsequent decision was made against state the operator
  believed was durable and shared.
- **Schema downgrade protection.** A database recorded at a schema version
  newer than the running binary aborts startup rather than being mutated.
  Ambiguous metadata — baseline data present with no recorded version, or
  multiple version rows — also fails closed instead of being guessed at.
- **Credential secrecy.** The DSN is never logged, never wrapped into an
  error, and never written to persisted state. `pgxpool.ParseConfig`'s error
  is deliberately not wrapped, because pgx echoes an *unparseable* DSN
  verbatim while redacting parseable ones.
- **SQL parameterization.** Every value reaches the database as a bound
  parameter. The only Go-assembled parts of any statement are two
  compile-time table-name constants.
- **Lost-update protection.** Concurrent `Observe` for the same key cannot
  lose an update, including the first observation for a key that does not
  exist yet.
- **Bounded state, no event warehouse.** Persistence stores current learned
  `Baseline` state only — one row per key, with every existing cardinality
  bound intact. No raw events, prompts, tool arguments, HTTP bodies, or SQL
  payloads are stored.
- **Anti-poisoning holds across backends.** The learning-eligibility gate
  lives above the Store, so durable shared persistence cannot be used to
  normalize blocked behavior.
- **Corruption is never silently reset.** An unreadable stored baseline
  produces an explicit error and the row is left intact, rather than being
  overwritten with a blank one.

## v0.7.0 — AI Agent Behavioral Security

Makes AI agents first-class behavioral actors in the existing engine —
no second pipeline, no agent-specific detector — then validates the
combined result and closes the public-configuration gap that validation
surfaced: five vertical slices, all additive, with `v0.5`/`v0.6`
behavior preserved by default.

### Added

- **AI Agent Event/Context Foundation**
  ([task 014](docs/archive/tasks/v0.7/014-ai-agent.md)) — `event.Context` gains
  three optional fields: `SessionID` (session/conversation grouping),
  `DelegatedFrom` (single-hop agent-to-agent delegation), and
  `ApprovalStatus` (a new exported enum type — `ApprovalUnspecified`
  (zero value) / `ApprovalNotRequired` / `ApprovalRequired` /
  `ApprovalApproved` / `ApprovalDenied` — recording a per-event
  human-approval fact, not a workflow state). None of the three affect
  `Fingerprint`/`baseline.Key` identity — proven, not just asserted,
  by a test that runs 1,000 distinct `SessionID` values through the
  real engine and confirms exactly one `Fingerprint` accumulates all
  1,000 observations. The task also proves `v0.6`'s existing bounded
  3-gram signal (`ngram_deviation`) already detects AI-agent
  tool-sequence novelty with zero new detector code. No new package,
  no new dependency, no agent-specific `Fingerprint`/`Baseline`/
  `Anomaly` type. Existing `v0.1`–`v0.6` callers are unaffected:
  `BenchmarkEngineAnalyze`'s allocation profile is unchanged. See [ADR
  0014](docs/adr/0014-ai-agents-as-first-class-behavioral-actors.md).
- **Approval-Aware Policy Semantics**
  ([task 030](docs/archive/tasks/v0.7/030-approval-aware-policy-semantics.md)) —
  `ApprovalStatus` gains its first real consumer, entirely inside
  `internal/policy`: `policy.Input`/`Condition` and
  `config.PolicyCondition` each gain an `ApprovalStatus` field (config
  key `approval_status`), letting a `Policy` rule require approval for
  an operation via the existing `Unless` mechanism
  (`Unless: &Condition{ApprovalStatus: Approved}`) — no new Rule-level
  primitive, no new anomaly signal. Fail-safe by construction, proven
  by test: an event self-declaring `ApprovalNotRequired` cannot
  override a `Policy` rule that requires approval, and missing
  evidence (`ApprovalUnspecified`) fails closed to `BLOCK` rather than
  silently passing. `Anomaly.Score`/`Trust.Score`/`Fingerprint.ID` are
  provably unaffected by `ApprovalStatus` — only `Decision` differs.
  The mechanism is domain-generic, not coupled to `ActorTypeAIAgent`.
  `ApprovalStatus` remains untrusted, self-reported evidence: this
  task adds no cryptographic verification of its provenance. No new
  package, no new pipeline stage, no new dependency; CLI and the OTel
  Collector processor require zero code changes. See [ADR
  0015](docs/adr/0015-approval-as-policy-evidence-not-behavioral-anomaly.md).
- **Delegation Behavioral Semantics**
  ([task 031](docs/archive/tasks/v0.7/031-delegation-behavioral-semantics.md)) —
  a new, opt-in `internal/anomaly` signal, `delegation_deviation`:
  has this actor ever received delegation from this immediate
  delegator before? `internal/features.VolatileFeatures` gains
  `DelegatedFrom` (read from `Context.DelegatedFrom`, never into
  `StableFeatures`); `internal/baseline.Baseline` gains a bounded
  (64-entry) `DelegatorCounts` map, scoped to the actor receiving
  delegation, not to any one operation; `internal/anomaly.Config`
  gains `DelegationWeight` (defaults to `0`, opt-in). Behavioral
  familiarity, not authorization or authenticated provenance: a
  familiar delegator is never thereby authorized, and an unfamiliar
  one is never thereby malicious — `DelegatedFrom` remains exactly as
  unauthenticated and self-reported as task 014 left it. Proven, not
  just documented: existing learning-eligibility (BLOCKed attempts
  never "normalize" a forged delegator through repetition), actor
  isolation, score-before-learn, cardinality bounding (200 distinct
  delegators stay capped at 64), and independence from both
  `Fingerprint` identity and task 030's approval-policy evidence. No
  new package, no new pipeline stage, no new `Store.Observe`
  parameter, no new dependency; `BenchmarkEngineAnalyzeDelegationAbsent`
  confirms zero added allocation when unused. See [ADR
  0016](docs/adr/0016-delegation-as-behavioral-evidence-not-provenance.md).
- **Agent Security Scenario Validation**
  ([task 032](docs/archive/tasks/v0.7/032-agent-security-scenario-validation.md)) —
  validated combined agent-security scenarios (unexpected privileged
  tool use, a sensitive read-then-external-post sequence, an approval
  violation, an unexpected delegator, external-destination drift, and
  one combined case exercising delegation, sequence, and approval
  evidence together) using existing primitives only — this is a
  validation and documentation task, not a new detection feature. No
  new anomaly signal, `Policy` primitive, or pipeline stage. Adds
  `scenario_test.go` (eleven new tests, including a combined poisoning
  regression and a no-correlated-evidence-explosion audit under every
  `v0.6`/`v0.7` signal weight at once) and a new, fully-public example,
  [`examples/ai-agent-security`](examples/ai-agent-security/),
  demonstrating task 030's approval mechanism end-to-end through
  `config.PolicyConfig`/`CompilePolicy` alone — the first example in
  that directory with a genuinely differentiated `ALLOW`/`BLOCK`
  decision. Also surfaces, and documents rather than patches, a real
  gap: `anomaly.Config` has no public-`config`-package equivalent of
  `policy.Policy`'s path, so delegation/sequence signals remain
  demonstrable only from inside this module today.
- **`v0.7` Stabilization & Release Gate**
  ([task 033](docs/archive/tasks/v0.7/033-v07-stabilization-release-gate.md)) —
  closes the gap task 032 found: `config.AnomalyConfig` (new public
  document) compiled by `config.CompileAnomaly` into the exact
  `anomaly.Config` value `trustvian.WithAnomalyConfig` already
  accepted, mirroring `config.CompilePolicy`'s exact shape as a third
  independent document alongside Policy and Alert configuration. Every
  existing `anomaly.Config` field is now publicly configurable
  (`DelegationWeight`, `NGramWeight`, `TransitionWeight`,
  `MarkovWeight`, `SensitiveTargetFloor`, and the rest) — none newly
  invented; `internal/baseline`'s state-size bounds stay internal,
  deliberately. `CompileAnomaly(AnomalyConfig{})` reproduces
  `anomaly.DefaultConfig()` byte-for-byte, proven by
  `TestCompileAnomalyZeroValueMatchesDefaultConfig` — existing callers
  who configure nothing see unchanged behavior. `trustvian analyze`/
  `baseline build` gain a matching `--anomaly-config <path>` flag.
  [`examples/ai-agent-security`](examples/ai-agent-security/) now
  demonstrates the full combined scenario — delegation, sequence, and
  approval evidence together — through public config alone, verified
  by a new `main_test.go` run from within the genuinely separate
  `examples` Go module (no `internal/*` import). Full `v0.7` regression
  (tasks 014/030/031/032's own tests, plus every `v0.6` sequence test)
  re-confirmed green. No new package, no new anomaly detector, no
  processor change, no new dependency. **`v0.7` is release-ready** —
  tagging `v0.7.0` remains a separate, explicit action. See [ADR
  0017](docs/adr/0017-public-anomaly-configuration-boundary.md).

## v0.6.0 — Behavioral Detection Depth

Adds sequence-aware behavioral detection on top of `v0.5`'s
configuration boundary: pairwise transition novelty and rarity,
bounded 3-gram (higher-order) detection, and a first-order Markov
severity curve over the same transition evidence — four vertical
slices plus a stabilization pass, all opt-in and byte-for-byte
compatible with `v0.5.0` by default.

### Added

- **Sequence Analysis Foundation**
  ([task 025](docs/archive/tasks/v0.6/025-sequence-analysis-foundation.md)) — a new,
  opt-in anomaly signal, `transition_deviation`: has the immediately
  preceding action ever led to this one before, for this actor?
  `internal/anomaly.Config` gains `TransitionWeight` (defaults to `0`,
  the same "ships opt-in" precedent `FrequencyWeight`/
  `TimePatternWeight` already set); `internal/baseline.Baseline` gains
  `LastFingerprintID`/`LastFingerprintTime`, and `FingerprintStats`
  gains a bounded (64-entry) `PredecessorCounts` map — no new
  `SequenceStore`, `SequenceKey`, or public API of any kind (see [ADR
  0010](docs/adr/0010-bounded-process-local-sequence-state.md)).
  Existing `v0.5` callers see byte-for-byte unchanged `Score`
  output and allocation profile (`BenchmarkEngineAnalyze`: 456 B/op, 17
  allocs/op, unchanged). See
  [docs/sequence-analysis.md](docs/sequence-analysis.md) for the full
  design.
- **Transition Rarity**
  ([task 026](docs/archive/tasks/v0.6/026-transition-rarity.md)) — a new, opt-in
  anomaly signal, `transition_rarity`, evolving task 025's binary
  seen/unseen `transition_deviation` into a graded common/uncommon/rare
  measure for transitions that *have* been seen:
  `rarity(A->B) = 1 - (PredecessorCounts_B[A] / OutgoingTransitionTotal_A)`
  — an empirical relative frequency, deliberately never called a
  "probability" (no Markov model, no smoothing — see [ADR
  0011](docs/adr/0011-transition-rarity-statistic-and-orientation.md)).
  `internal/baseline.FingerprintStats` gains one new scalar,
  `OutgoingTransitionTotal` (no new map, no new cardinality dimension);
  `internal/anomaly.Config` gains `MinTransitionObservations` (defaults
  to `20`, a minimum-support cold-start gate) and
  `TransitionRarityWeight` (defaults to `0`, the same "ships opt-in"
  precedent every other signal weight in this package already set).
  `transition_deviation` and `transition_rarity` remain mutually
  exclusive by construction — an unseen transition is never also
  reported as "rare." Existing `v0.5`/task-025 callers see byte-for-byte
  unchanged `Score` output; `Engine.Analyze`'s allocation profile stays
  unchanged (`456 B/op, 17 allocs/op`), though its latency grows by a
  small, reported (not hidden) amount — see
  [docs/PERFORMANCE.md § v0.6 task 026](docs/PERFORMANCE.md#v06-task-026-transition-rarity).
- **Bounded n-gram Detection**
  ([task 027](docs/archive/tasks/v0.6/027-bounded-ngram-detection.md)) — two new,
  opt-in anomaly signals, `ngram_deviation`/`ngram_rarity`, extending
  order-awareness one step further back than tasks 025/026: given a
  3-gram `A -> B -> C`, has this exact (grandparent, predecessor) pair
  ever led to this destination before, and if so, how commonly? A
  fixed 3-gram only — no configurable `n`, no Markov model.
  `internal/baseline.Baseline` gains one new scalar,
  `PreviousFingerprintID`; `FingerprintStats` gains two new,
  *independently* bounded (64-entry each) maps, `TrigramCounts`
  (destination-keyed by a `(grandparent, predecessor)` pair via the
  new `baseline.TrigramKey` type) and `TrigramContinuationTotal`
  (predecessor-keyed by grandparent) — see [ADR
  0012](docs/adr/0012-bounded-trigram-behavioral-context.md) for why a
  scalar cannot answer a 3-gram's denominator, and for a genuine
  correctness subtlety this task's own review caught:
  `TrigramContinuationTotal`'s cardinality does not inherit
  `PredecessorCounts`'s existing bound "for free" and needed its own.
  `internal/anomaly.Config` gains `NGramWeight`, `MinNGramObservations`
  (defaults to `20`, a *separate* field from
  `MinTransitionObservations` — the two gate statistically different
  denominators), and `NGramRarityWeight` (both signal weights default
  to `0`, the same "ships opt-in" precedent every other signal weight
  in this package already set). Proven, end-to-end, to add genuine
  information beyond tasks 025/026: both pairwise hops of a sequence
  can be independently familiar while the complete 3-gram has never
  occurred, and `ngram_deviation` still detects it (see
  `TestScoreNGramDeviationDetectsNovelTrigramDespiteFamiliarPairwiseTransitions`).
  Existing `v0.5`/task-025/026 callers see byte-for-byte unchanged
  `Score` output; `Engine.Analyze`'s allocation profile stays unchanged
  (`456 B/op, 17 allocs/op`), though its latency grows by a small,
  reported (not hidden) amount — see
  [docs/PERFORMANCE.md § v0.6 task 027](docs/PERFORMANCE.md#v06-task-027-bounded-n-gram-detection).
- **Markov Transition Scoring**
  ([task 028](docs/archive/tasks/v0.6/028-markov-transition-scoring.md)) — a new,
  opt-in anomaly signal, `markov_surprisal`, computing first-order
  Markov surprisal (`-log2(P(B|A))`, bounded into `[0,1)`) over the
  identical evidence task 026's `transition_rarity` already reads.
  Before writing any code, this task answered a mandatory question —
  what capability would Markov scoring add beyond `transition_rarity`?
  — and found the honest answer: **none, as evidence.** Both signals
  are proven (`TestMarkovSurprisalIsMonotonicReparameterizationOfRarity`)
  to be strictly monotonic functions of the identical
  `count/total` frequency; what differs is curve shape (surprisal
  preserves more resolution across the rare tail than the linear
  `1-frequency` mapping does). `markov_surprisal` is therefore built as
  an alternative, **mutually exclusive** scoring curve, not a second,
  independently-weighted signal: `anomaly.Score` forces
  `transition_rarity`'s own contribution to `Score` to zero whenever
  `Config.MarkovWeight` (defaults to `0`) is enabled, enforced in code
  and proven by
  `TestScoreMarkovAndTransitionRarityAreMutuallyExclusiveInScoring` — a
  caller cannot double-count this evidence by any combination of the
  two weights. No new `Baseline`/`FingerprintStats` state: reuses task
  025/026's `PredecessorCounts`/`OutgoingTransitionTotal` and
  `MinTransitionObservations` exactly. Never evaluates a `count == 0`
  transition (that remains `transition_deviation`'s domain), so
  `-log2(0)` is never computed — `Inf`/`NaN` are impossible by
  construction. See [ADR
  0013](docs/adr/0013-first-order-markov-surprisal-without-duplicate-evidence.md)
  for the full duplication analysis and design. Existing
  `v0.5`/task-025/026/027 callers see byte-for-byte unchanged `Score`
  output; `Engine.Analyze`'s allocation profile stays unchanged
  (`456 B/op, 17 allocs/op`), though its latency grows by a small,
  reported (not hidden) amount — see
  [docs/PERFORMANCE.md § v0.6 task 028](docs/PERFORMANCE.md#v06-task-028-markov-transition-scoring).

## v0.5.0 — Policy & Configuration

Adds a public, versioned configuration boundary for both `Policy` and
Alert Evaluation, and wires the former into the CLI and the OTel
Collector processor.

### Added

- **Public Policy configuration model + compiler**
  ([task 019](docs/archive/tasks/v0.5/019-policy-config-model.md)) — a new public
  package, `config` (a sibling to `event`/`alert`, not `internal/` —
  see [ADR 0008](docs/adr/0008-policy-config-boundary.md)):
  `PolicyConfig`/`PolicyRule`/`PolicyCondition` (primitive-typed
  structs mirroring `policy.Policy`/`Rule`/`Condition` exactly),
  `(PolicyConfig).Validate() error`, and
  `CompilePolicy(PolicyConfig) (policy.Policy, error)`. A caller
  outside this module can receive a `policy.Policy` from
  `CompilePolicy` and pass it straight into `trustvian.WithPolicy` via
  ordinary Go type inference, without ever importing `internal/policy`
  — a Go language property verified empirically, not assumed.
- **Config file loader & schema v1 parsing**
  ([task 020](docs/archive/tasks/v0.5/020-policy-config-loader.md)) —
  `Load([]byte) (PolicyConfig, error)` and
  `LoadFile(path string) (PolicyConfig, error)`, decoding a YAML
  document directly into task 019's existing public types via one new
  dependency, `go.yaml.in/yaml/v3`. Unknown fields and duplicate
  mapping keys are both rejected unconditionally — a config-time typo
  fails loudly rather than silently falling through to a different
  security behavior. `LoadFile` bounds its read to 1 MiB.
- **CLI configuration integration**
  ([task 021](docs/archive/tasks/v0.5/021-cli-config-integration.md)) —
  `trustvian analyze`/`trustvian baseline build` accept an optional
  `--config <path>`, consuming `config.LoadFile`/`config.CompilePolicy`
  directly. Omitting it preserves the CLI's pre-existing built-in
  default policy exactly; an invalid or missing explicit `--config`
  fails the whole command closed (non-zero exit, no analysis output),
  never a silent fallback.
- **OTel Collector policy configuration integration**
  ([task 022](docs/archive/tasks/v0.5/022-collector-config-integration.md)) — the
  standalone [`processor/`](processor/README.md) module accepts an
  optional `policy:` block in its own Collector configuration, decoded
  via `go-viper/mapstructure/v2` (pointed at `PolicyConfig`'s own
  `yaml` tags — Collector's confmap decoder only reads `mapstructure`
  tags) and compiled via the same `config.CompilePolicy`. An invalid
  explicit `policy:` block fails Collector startup outright.
  `processor/go.mod` depends on this exact `v0.5.0` release, verified
  with a clean `GOWORK=off go build ./... && GOWORK=off go test ./...
  -race` — no local workspace involved.
- **Declarative Alert configuration**
  ([task 023](docs/archive/tasks/v0.5/023-declarative-alert-configuration.md)) — a
  new, independent public model in the same `config` package:
  `AlertConfig`/`AlertRuleConfig`/`AlertConditionConfig`,
  `(AlertConfig).Validate() error`,
  `CompileAlerts(AlertConfig) ([]alert.Rule, error)`, and
  `LoadAlerts`/`LoadAlertsFile`. Deliberately a separate document and
  compilation path from `PolicyConfig`/`CompilePolicy` — see [ADR
  0009](docs/adr/0009-alert-config-is-a-separate-document.md) — not a
  combined schema. Not yet wired into the CLI or the Collector
  processor (neither has an alert-delivery flow for it to plug into
  yet); a Go SDK caller can use it directly today.

## v0.4.0 — Alert & Notification Foundation

Adds the Foundation stage of the Alert & Notification phase: a
minimal, explainable, externally deliverable `Alert`, without changing
`Decision` semantics or the existing detection pipeline. Delivery
reliability (retry, deduplication, cooldown) and provider-specific
sinks (Slack, Teams, PagerDuty) remain explicitly out of scope for this
release.

### Added

- **Alert & Notification Foundation**
  ([task 018](docs/archive/tasks/v0.4/018-alert-notification-foundation.md)) — a new
  public package, `alert` (a sibling to `event`, not `internal/` — see
  [ADR 0007](docs/adr/0007-alert-package-is-public.md)), turns a
  `Result`/`Decision` into a minimal, explainable, externally
  deliverable `Alert`: `Alert` (a view assembled from `Result`, no
  parallel model), `Severity`
  (`INFO`/`LOW`/`MEDIUM`/`HIGH`/`CRITICAL`, explicitly distinct from
  risk/anomaly score/trust score/decision), a `policy.Condition`-shaped
  Alert Evaluation matcher (`Condition`/`Rule`/`Evaluate` — flat
  AND-of-optional-fields, no combinators, structurally independent from
  `internal/policy`), the `Sink` interface, and `WebhookSink` — a
  generic HTTPS webhook signed with HMAC-SHA256 over a timestamp-bound
  payload, delivering a versioned contract
  (`Envelope{Version, Alert}`, `PayloadVersion = "1"`). No new pipeline
  stage: `Decision` semantics and the core detection engine are
  unchanged, verified via `go list -deps` showing zero HTTP/provider-SDK
  dependency reached `event` through `internal/policy` or `Engine`.
  Verified end-to-end via
  [`examples/alert-webhook`](examples/alert-webhook/README.md) — a
  genuinely external module building an `Alert` from a live
  `Engine.Analyze` call and delivering it to a signed webhook, no OTel
  involved. Day-of-week seasonality aside, this closes the last item
  `trustvian-project-spec.md` § 18 named as unimplemented for the
  Foundation stage; the Reliability and Additional-sinks-and-governance
  stages (retry, deduplication, cooldown, Slack/Teams/PagerDuty) remain
  explicitly deferred — see
  [§ v0.4.0](#v040--alert--notification-foundation).

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.4.0` (Alert & Notification Foundation) as shipped, and
  the roadmap was extended with the planned milestone sequence toward
  `v1.0.0` (`v0.5` Policy & Configuration through `v0.9` Operational
  Readiness).

## v0.3.0 — Baseline & anomaly depth

Adds the one piece of time-based pattern awareness the roadmap
committed to for this milestone — hour-of-day seasonality — as a new,
deterministic, opt-in anomaly signal. No new pipeline stages; day-of-week
seasonality was explicitly scoped and deferred, not built.

### Added

- **Hour-of-day time-pattern anomaly signal**
  ([task 017](docs/archive/tasks/v0.3/017-baseline-time-patterns.md)) —
  `baseline.FingerprintStats` gained `HourActivity [24]float64` (a
  per-UTC-hour EWMA of traffic share) and a dedicated
  `TimePatternObservations` maturity counter, gating the signal
  independently of `Count` so a pre-existing persisted `FileStore`
  record — where `HourActivity` unmarshals to its zero array — is
  correctly treated as immature rather than falsely mature-with-empty-
  data. `anomaly.Score` gained a sixth signal, `time_pattern_deviation`,
  shipped **opt-in** like `frequency_deviation` before it
  (`Config.TimePatternWeight` defaults to `0`). An empirically
  discovered result: reusing the existing `emaAlpha = 0.2` for
  `HourActivity` was tried first and rejected — it made uniform,
  patternless hour-of-day traffic look sharply time-anomalous purely as
  a measurement-phase artifact — so a new, slower, separately-justified
  `hourActivityAlpha = 0.02` constant exists instead. Day-of-week
  seasonality was scoped and explicitly **not** built; it moved to
  [ROADMAP.md § Future research](docs/ROADMAP.md#future-research) as a
  separate vertical slice. See
  [DOMAIN.md § Baseline / § Anomaly](docs/DOMAIN.md).

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.3.0` (baseline & anomaly depth) as shipped.

## v0.2.0 — OpenTelemetry maturation

Completes the OpenTelemetry integration story in the outbound
direction, and takes the first step toward a production Collector
deployment — without pulling OTel, or the Collector toolchain, into the
core.

### Added

- **Outbound `trustvian.*` result attributes**
  ([task 008](docs/archive/tasks/v0.2/008-otel.md)) —
  `internal/otel.AttributesFromResult` derives five outbound attributes
  (`trustvian.anomaly.score`, `trustvian.trust.score`,
  `trustvian.risk.level`, `trustvian.decision`,
  `trustvian.fingerprint.id`) from a `Result`, for a caller to attach to
  a span or export alongside one. `trustvian.behavior.id` — named in the
  original spec alongside the five above but never defined beyond its
  name — is deliberately not implemented: every plausible meaning
  collapses into `Fingerprint.ID`, and CLAUDE.md's OpenTelemetry section
  is explicit about not inventing telemetry attributes without
  documenting them. Verified via `go list -deps` that this introduces no
  import cycle and that `internal/otel` remains the sole package in this
  module depending on OpenTelemetry. See
  [OPENTELEMETRY.md § Trustvian output
  attributes](docs/OPENTELEMETRY.md#trustvian-output-attributes).
- **Standalone OTel Collector processor**
  ([task 009](docs/archive/tasks/v0.2/009-otel-collector.md)) —
  [`processor/`](processor/README.md), a separate Go module
  (`trustvian-processor`) implementing a real OpenTelemetry Collector
  traces processor: maps `ptrace.Span` into Trustvian events, runs them
  through the public `Engine` API, and writes the outbound
  `trustvian.*` attributes onto enriched spans before forwarding them.
  Verified end-to-end against a real OTel-SDK span sent over real
  OTLP/gRPC to a real running Collector binary built with this
  processor. Necessarily a parallel implementation of task 008's
  mapping/attribute code, not a reuse of it — `internal/otel` is
  unreachable from a genuinely separate module, and the Collector's
  `ptrace.Span` data model shares no relationship with the SDK's
  `sdktrace.ReadOnlySpan`. [ADR 0002](docs/adr/0002-public-api-boundary.md)'s
  public API boundary was considered and deliberately not revisited, so
  the processor runs Trustvian's zero-configuration default `Policy`
  today — every span it scores resolves to `trustvian.decision =
  "observe_only"` — documented as a known limitation. See
  [`processor/README.md` §
  Configuration](processor/README.md#configuration).

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.2` as shipped; `v0.3` (baseline & anomaly depth) became
  the new "next up."

## v0.1.0 — Behavioral core hardening & first public release

The core pipeline — `Event → Features → Fingerprint → Baseline →
Anomaly → Trust → Policy → Decision` — was already implemented, tested,
and benchmarked before this milestone. `v0.1` is the pass that took it
from "works and is tested" to "hardened, explainable, benchmarked,
demonstrated, and released as a stable OSS artifact." No new pipeline
stages were added; this release is depth, not breadth.

### Added

- **Target category** ([task 001](docs/archive/tasks/v0.1/001-feature-model.md)) —
  `event.Event.Target.Category` (`internal`, `external`, `database`) is
  a new, optional stable dimension. It flows through
  `internal/features.Extract` into `StableFeatures` and, from there,
  into the behavioral `Fingerprint`, so an actor that has only ever
  called internal services suddenly calling an external one is now a
  detectable category shift, not just a specific-hostname novelty.
  Zero value (`unspecified`) is accepted by `Event.Validate()` and
  never required.
- **Fingerprint versioning** ([task 002](docs/archive/tasks/v0.1/002-fingerprint.md))
  — `internal/fingerprint.Compute` now writes an explicit version
  marker into its hash before the stable fields. A future change to
  which dimensions feed the hash (or to the hash algorithm) bumps this
  one constant, producing a disjoint ID space instead of silently
  reinterpreting old `Fingerprint.ID`s under new semantics — the thing
  that stops being harmless once a persistent `Store` is in play (see
  `store.FileStore`, shipped in task 003). The fingerprint's design —
  what feeds the hash, in what order, and why FNV-1a — is now written
  up in [DOMAIN.md § Fingerprint](docs/DOMAIN.md#fingerprint).
- **`frequency_deviation` anomaly signal**
  ([task 004](docs/archive/tasks/v0.1/004-anomaly.md)) — `internal/baseline` now
  tracks an EWMA of inter-observation intervals
  (`FingerprintStats.IntervalMean`/`IntervalVariance`/
  `IntervalObservations`), and `internal/anomaly.Score` uses it to
  detect abnormal request *frequency*: an actor that normally calls an
  operation every 10 seconds and suddenly calls it every 100ms now
  triggers a dedicated signal, not just categorical novelty. Zero-cost
  (no allocation) on the common "frequency is normal" path, gated by
  `Config.FrequencyZThreshold`/`FrequencyWeight`, matching the existing
  latency/error signal shape exactly.

  The signal ships **opt-in**: `anomaly.DefaultConfig()` sets
  `FrequencyWeight` to `0`, so `frequency_deviation` is computed and
  reported in `Anomaly.Contributors` but contributes nothing to
  `Anomaly.Score` until an operator raises the weight. This mirrors
  `SensitiveTargetFloor`, which likewise ships empty and inert. The
  reason is calibration against real jitter: because the signal divides
  by the standard deviation of a fingerprint's *own* inter-event
  intervals, traffic with only a few milliseconds of natural jitter
  around a ten-second cadence produces `|z| > 3` — a clamped `1.0`
  signal — for entirely ordinary, on-cadence events. Measure your
  fleet's `IntervalMean`/`IntervalVariance` before enabling; see
  [DOMAIN.md § Anomaly](docs/DOMAIN.md#anomaly) and
  [examples/frequency-abuse](examples/frequency-abuse/).
- **Out-of-order observation guard in `internal/baseline`** — an
  observation whose timestamp does not strictly follow the
  fingerprint's `LastObserved` (clock skew, out-of-order delivery, a
  backdated event from an untrusted producer) is still counted, but its
  interval is no longer folded into the interval EWMA, and
  `LastObserved` now advances monotonically rather than regressing.
  `anomaly.Score` correspondingly refuses to fire `frequency_deviation`
  on a negative interval. Without this, a single backdated event could
  drive `IntervalMean` arbitrarily negative and make every subsequent
  on-time event look anomalous — a baseline-poisoning path that
  `Observe`'s decision gating structurally could not catch, since such
  an event is normally decided `observe_only`. See
  [SECURITY.md § baseline poisoning](docs/SECURITY.md#baseline-poisoning).
- **`Trust.Explain()`** ([task 005](docs/archive/tasks/v0.1/005-trust-risk.md)) — a
  new method rendering a `Trust` value as a short, human-readable
  sentence (identity confidence, anomaly at its effective confidence,
  context risk, and the resulting risk level). The multiplicative trust
  formula itself is unchanged; a new scenario-matrix test now sweeps
  identity confidence, anomaly score/confidence, and context risk
  across representative ranges and asserts the formula never produces a
  value outside `[0,1]` and is monotonic in every input.
- **Attribute matching in policy `Condition`**
  ([task 006](docs/archive/tasks/v0.1/006-policy.md)) — `policy.Condition` gained an
  `Attributes map[string]string` field, ANDed with every other
  `Condition` field, closing the original spec's own
  `tool.category: secrets` policy example. This is flat key/value
  equality only — no AND/OR/NOT combinators, no comparison operators,
  and no dynamic policy loading were added; `Condition{}`'s zero-value
  "matches everything" behavior is unchanged.
- **`Result.Explain()`** ([task 007](docs/archive/tasks/v0.1/007-decision.md)) — a
  new method rendering a complete, human-readable decision summary:
  final decision, trust/risk/anomaly scores, every contributing
  anomaly signal with its detail, and the matched policy rule (or
  default reason). Every field the project's own Decision checklist
  names was already present on `Result`; this makes assembling them
  into a readable explanation a reusable SDK method instead of
  CLI-only formatting logic.
- **`examples/`** ([task 010](docs/archive/tasks/v0.1/010-examples.md)) — a new,
  runnable `examples/` directory with six self-contained programs
  (`basic`, `credential-misuse`, `unexpected-dependency`,
  `external-destination`, `frequency-abuse`, `ai-agent`), each a
  genuine external consumer of this module (via `go mod replace`),
  each demonstrating the full `Event → ... → Decision` path with real,
  captured `go run` output. `make examples` runs and verifies all six.
- **Two closed performance-measurement gaps**
  ([task 011](docs/archive/tasks/v0.1/011-performance.md)) —
  `BenchmarkEventFromSpan` (`internal/otel`) and
  `BenchmarkInMemoryMemoryGrowth` (`internal/store`, at 100/1,000/10,000
  distinct keys) close the two gaps [PERFORMANCE.md](docs/PERFORMANCE.md)
  had explicitly named as unmeasured. Every pipeline stage named in the
  project roadmap is now benchmarked, with numbers reproduced fresh as
  part of this release (see [PERFORMANCE.md § Measured
  results](docs/PERFORMANCE.md#measured-results)).
- **Dedicated security test suite**
  ([task 012](docs/archive/tasks/v0.1/012-security-tests.md)) — new tests for
  malformed/extreme input (`NaN`/`±Inf` `IdentityConfidence`, very long
  strings, negative `duration_ms`), resource-exhaustion safety (a
  100,000-key `Attributes` map, a single actor producing 5,000 distinct
  fingerprints — neither panics), and an explicit end-to-end proof of
  cross-actor `Baseline` isolation
  (`TestAnalyzeCrossActorIsolation`). [SECURITY.md](docs/SECURITY.md)
  now documents every threat named in the project roadmap, each with a
  test reference or an explicit deferred-mitigation label.

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.1` as shipped; `v0.2` (OpenTelemetry maturation) is the
  new "next up."

### Verified, not changed

`v0.1` is a hardening release: the pipeline shape, the trust formula,
and the policy evaluator's fail-closed guarantee are all unchanged from
before this milestone. What changed is depth — a new stable dimension,
a new anomaly signal, a versioned fingerprint hash, richer policy
matching, and, above all, verification: broader test coverage, a
documented performance baseline, and a documented security posture.

## Public API compatibility promise

Starting at `v0.1.0`, the following are committed stable — a breaking
change to any of them will be called out explicitly in a future
changelog entry and reflected in a major/minor version bump, not made
silently:

- **`event.Event`'s public field shapes** — `Actor`, `Operation`,
  `Target`, `Context`, `Attributes`, and the enum types backing them
  (`ActorType`, `OperationCategory`, `OperationDirection`,
  `TargetCategory`). New optional fields may be added in a
  backward-compatible way (zero value = "unset", never required by
  `Validate()`); existing fields will not be removed, renamed, or have
  their meaning changed.
- **`Result`'s public field shapes** — `Event`, `Features`,
  `Fingerprint`, `BaselineKey`, `Anomaly`, `Trust`, `Decision`,
  `Explanation`, and `Result.Explain()`'s general contract (a
  human-readable summary covering decision, scores, contributors, and
  policy reason — exact wording is not part of the promise, field
  presence is).
- **`Engine`'s public method signatures** — `NewEngine(opts ...Option)`,
  `Analyze(ctx context.Context, ev event.Event) (Result, error)`, and
  `Observe(ctx context.Context, result Result) (learned bool, err error)`.
- **The `Option` functions** — `WithStore`, `WithPolicy`,
  `WithAnomalyConfig`, `WithTrustConfig`, `WithContextRisk`, and the
  `Option func(*Engine)` type itself.

**Everything under `internal/` carries no compatibility promise.**
This includes, but is not limited to, `internal/features.Features`,
`internal/fingerprint.Fingerprint`, `internal/anomaly.Anomaly` and
`anomaly.Config`, `internal/trust.Trust` and `trust.Config`,
`internal/policy.Policy`/`Condition`/`Rule`, and
`internal/store.Store` and its implementations. External callers can
already *read* these types' exported fields through a `Result` value
without importing them (see [ADR
0002](docs/adr/0002-public-api-boundary.md)), but cannot construct or
name them directly from outside this module — Go's `internal/`
visibility rule enforces that boundary, and this project reserves the
right to change anything inside it, including field shapes, without
that counting as a breaking change to the public API described above.

**Where those two paragraphs meet.** `Result`'s fields are *typed* by
internal packages, so the two promises above need a precise boundary
between them. The promise on `Result` is a promise about `Result`
itself: that a field named `Trust` exists on it, that it is the trust
stage's output, and that it will not be removed, renamed, or
repurposed. It does not extend one level down into the type that field
holds. `trust.Trust` could gain, lose, or rename its own fields — so
`result.Trust.Score` is *not* covered by this promise even though
`result.Trust` is. The same applies to `result.Anomaly` (an
`anomaly.Anomaly`), `result.Features`, `result.Fingerprint`,
`result.BaselineKey`, `result.Decision`, and `result.Explanation`.

In practice these types are stable and no reshaping is planned; the
distinction is about what a future release is *permitted* to do without
calling it a break, not a warning that it will. If you depend on a
specific inner field — `result.Trust.Score` and
`result.Anomaly.Contributors` are the common ones — pin the minor
version and read the changelog before upgrading. Promoting any of these
types to a public package with a real promise attached is tracked in
[ROADMAP.md § future research](docs/ROADMAP.md#future-research), gated
on an external consumer actually needing it.
