# Compatibility Contract

What Trustvian promises not to break, what it may change, and what a
change costs in version numbers.

This is the canonical answer to one question a maintainer asks on every
pull request: **is this change breaking?** Everything below describes the
contract from `v1.0.0` onward. Before `v1.0`, see
[Branching Strategy § pre-v1.0 discipline](governance/branching.md#pre-v10-discipline).

## How to use this

1. Find the surface you are changing in the [matrix](#compatibility-matrix).
2. Read its classification.
3. Apply the [SemVer rules](#semver-rules) for the kind of change.
4. If the change is breaking or deprecating, say so in the pull request
   and in `CHANGELOG.md`.

## Classifications

| Class | Meaning |
|---|---|
| **STABLE** | Covered by backward-compatibility guarantees. An intentional break requires a major version |
| **STABLE WITH DEPRECATION** | May evolve, but removal or a behavioral break requires a documented deprecation first |
| **OPERATIONALLY STABLE** | Not a source API, but operators build on it. Breaking it requires migration guidance and the appropriate version bump |
| **OBSERVATIONAL** | Useful, deliberately not guaranteed stable byte-for-byte or word-for-word |
| **INTERNAL** | No compatibility promise |
| **EXPERIMENTAL** | Explicitly outside the stability promise, and labelled as such where it appears |

Nothing is labelled EXPERIMENTAL to avoid responsibility. Today the
repository ships no experimental surface — if that changes, it is named
here and in its own documentation.

## Compatibility matrix

The platform `/v1` rows describe the **intended** `v1` contract. That surface
ships with `v1.0` and may still change through reviewed work before the
release; the rows are here so the intent is recorded now rather than
reconstructed later. Everything else in this table is already released.

| Surface | Class | v1 guarantee | Allowed in a minor | Breaking change requires |
|---|---|---|---|---|
| `event.Event` and its field shapes | STABLE | Fields are not removed, renamed, or redefined | New optional fields whose zero value means "unset" | Major |
| `Result` and its field shapes | STABLE | Field presence and meaning | New fields | Major |
| `Engine`, `NewEngine`, `Analyze`, `Observe` signatures | STABLE | Signatures hold | New `Option` functions | Major |
| `Option` functions | STABLE | Existing options keep working; `WithLearningScope`'s default `""` is the pre-`v1.0` behavior | Additional options | Major |
| `alert` package exports | STABLE | `Alert`, `Severity`, `Rule`, `Condition`, `Evaluate`, `Sink`, `WebhookSink` | Additive fields and options | Major |
| `config` exported types and `Compile*` functions | STABLE | Existing documents keep compiling | New optional fields, new document types | Major |
| `StableFeatures` (root package) | STABLE | Field shapes; handed to a `WithContextRisk` callback | New fields | Major |
| `DecisionRecord`, `ContributorRecord` | STABLE | Field shapes and meanings | New fields | Major |
| `DecisionRecord` JSON field names | STABLE | A name, once published, keeps its meaning | New fields | Major |
| Exported sentinel errors | STABLE | An error identity checked with `errors.Is` keeps matching the condition it names | New sentinels | Major |
| Exported enum-like constants | STABLE WITH DEPRECATION | Existing values keep their meaning | New values — **consumers must tolerate unknown values** | Major to remove a value |
| Configuration schema (`policy`, `alerts`, `anomaly`, `storage`) | STABLE | A valid `v1` document keeps loading across `v1.x` | New optional fields; a new schema version alongside `v1` | Major, or a new schema version |
| Configuration defaults | STABLE WITH DEPRECATION | A default is not changed silently | Documented default changes in a minor, called out in CHANGELOG | See [behavioral compatibility](#behavioral-compatibility) |
| CLI commands and flags | OPERATIONALLY STABLE | `analyze`, `baseline`, `version`, `--config`, `--anomaly-config`, `--storage-config` keep working | New commands and flags | Major to remove or repurpose |
| CLI exit codes | OPERATIONALLY STABLE | Scoped by command family; `1` means gate failure only for `eval compare` — see [CLI](#cli) | Adding a code, or a new family with its own scoped contract | Major |
| CLI human-readable output | OBSERVATIONAL | Not a machine interface — no wording, spacing, or ordering promise | Any change | None |
| Environment variables read by shipped binaries | OPERATIONALLY STABLE | See [environment variables](#environment-variables) | New variables | Major to remove or rename |
| Collector processor type name and config fields | OPERATIONALLY STABLE | `policy`, `storage`, `health`, `evaluation` keys and their meaning | New optional keys | Major |
| Policy rule semantics | STABLE | First-match-wins ordering; fail-closed to `BLOCK` on invalid policy | New condition fields; new decisions | Major |
| PostgreSQL schema | OPERATIONALLY STABLE | Forward-only, version-gated; see [persisted state](#persisted-state) | Additive columns or tables with a schema-version bump | Major for a destructive change |
| File-store snapshot format | OPERATIONALLY STABLE | Version-tagged; a `v1.x` binary reads what `v1.x` wrote | Additive fields that do not change what a record identifies | Major |
| Persisted `Baseline` JSON | OPERATIONALLY STABLE | Field names are not removed or repurposed within `v1` | Additive fields | Major |
| Metric names and types | OPERATIONALLY STABLE WITH DEPRECATION | Existing metrics keep their name, type, and meaning | New metrics | Deprecation, then a minor to remove |
| Metric label keys | OPERATIONALLY STABLE | Label keys keep their meaning; cardinality stays bounded | New labels — **consumers must tolerate unknown label values** | Major to remove or rename a key |
| Health and readiness endpoints | OPERATIONALLY STABLE | `/livez` and `/readyz` paths, and their HTTP status semantics | New endpoints, new response fields | Major |
| Health response body | OBSERVATIONAL | Status code is the contract; the body is diagnostic | Any change | None |
| Webhook payload envelope | STABLE | `version` field, currently `"1"`; `alert` object field names | Additive fields inside `alert` | New envelope version |
| Platform `/v1` control-plane route shapes | STABLE | Path, method and path-parameter shape of each documented `/v1` route | New routes; new optional request fields | Major, or a new path version |
| Platform `/v1` JSON field names | STABLE | A published request or response field name keeps its meaning | New fields — **consumers must tolerate unknown fields** | Major, or a new path version |
| Platform ingest envelope | STABLE | `version` field, currently `"1"`; `sequence`, `behavioral_profile`, `record` | Additive envelope fields | New envelope version |
| Platform error `code` values and HTTP statuses | STABLE | A code and its status keep the condition they name | New codes | Major |
| Platform error `message` text | OBSERVATIONAL | The code and status are the contract; wording is diagnostic | Any change | None |
| Platform ingest sequence semantics | STABLE | Monotonic from 1; expected applies, identical retry of the last replays, gap and stale fail | — | Major |
| Platform SQLite schema | OPERATIONALLY STABLE | Forward-only, version-gated; currently version 2 | Additive tables or columns with a schema-version bump and a migration | Major for a destructive change |
| `GET /v1/realtime` route and filter query names | STABLE | Path, method, and the `project_id`, `agent_id`, `run_id` filters | New optional filters | Major, or a new path version |
| Realtime SSE event names | STABLE | A published event name keeps the condition it names | New event names — **consumers must tolerate unknown names** | Major |
| Realtime SSE JSON field names | STABLE | A published field name keeps its meaning | New fields — **consumers must tolerate unknown fields** | Major |
| `stream_ready` semantics | STABLE | Sent first on every connection; `replay_available` false and `resync_required` true | — | Major |
| Realtime no-replay / resync-required contract | STABLE | No history is retained; `Last-Event-ID` triggers no replay; every connection resynchronizes | A separate durable replay capability may be added alongside | Major |
| Slow-consumer disconnect semantics | STABLE | A subscriber that cannot keep up is disconnected rather than silently losing events | — | Major |
| SSE heartbeat timing and wording | OBSERVATIONAL | A comment frame with no domain meaning; interval is not a contract | Any change | None |
| Realtime byte-level SSE formatting | OBSERVATIONAL | Protocol semantics are the contract, not whitespace or frame ordering beyond `stream_ready` first | Any change | None |
| Container entrypoint and ports | OPERATIONALLY STABLE | `trustvian-collector` entrypoint; `4317`, `4318`, `13133`; runs as `nonroot` | New ports | Major |
| Image name and immutable tags | OPERATIONALLY STABLE | `ghcr.io/trustvian/trustvian-collector:vX.Y.Z` is immutable | — | Major |
| Floating image tags (`latest`, `X.Y`) | OBSERVATIONAL | Convenience aliases; they move by design | Move on every stable release | None |
| Release artifact names and checksums | OPERATIONALLY STABLE | `trustvian_<version>_<os>_<arch>.{tar.gz,zip}` plus `checksums.txt` | New platforms, new artifact kinds | Major |
| Archive internal layout | OBSERVATIONAL | No promise beyond the binary being present | Any change | None |
| Everything under `internal/` | INTERNAL | None | Any change | None |

## SemVer rules

### PATCH — `v1.x.Y`

May contain bug fixes, security fixes, performance work, and internal
refactoring. **Must not intentionally break any STABLE or OPERATIONALLY
STABLE surface**, and must not change a documented default.

### MINOR — `v1.Y.0`

May add: backward-compatible API surface, optional configuration fields,
new CLI commands and flags, new metrics, new enum values, new endpoints.
May change a documented default, with the change named in `CHANGELOG.md`.
May remove a surface **only** if it completed the
[deprecation](#deprecation) path.

### MAJOR — `vY.0.0`

Required for any intentional break of a STABLE or OPERATIONALLY STABLE
surface: removing or renaming a field, flag, variable, metric key, or
endpoint; changing an exit code's meaning; a destructive storage change;
or a semantic change that alters decisions for unchanged input outside
the [behavioral](#behavioral-compatibility) rules below.

## Go API

The public surface is the root package, `event`, `alert`, and `config`.
Everything else is `internal/` and carries no promise.

Three properties consumers may rely on, all already true:

- **Every `Engine` option is configurable from outside the module.**
  Four take a value produced by the `config` package —
  `CompilePolicy`, `CompileStorage`, `CompileAnomaly`, `CompileTrust` —
  and `WithContextRisk` takes a callback over the public
  `StableFeatures` type.
- A value obtained from an exported function may be passed to another
  exported function without naming its type. That is what makes
  `config.CompilePolicy` → `trustvian.WithPolicy` work, and it keeps
  working.
- Reading exported fields off a `Result` requires no import beyond the
  root package.

Some exported signatures name types defined under `internal/`. That is
an intentional facade, not an oversight: the `config` package produces
every such value, so a caller never has to name one. It does mean a
change to one of those types' *shapes* is breaking for external callers
even though the type is internal, and it is treated that way.

### The decision record

`Result.DecisionRecord()` returns the serializable public projection of one
analysis. Both its Go field shapes and its JSON field names are STABLE:
adding a field is a minor, removing or redefining one is major.

Three properties are part of the contract rather than implementation detail.
The record carries **no raw event payload** — `Event.Attributes`, tool
arguments, prompts, and completions have no field and cannot appear in its
JSON. It carries **no consumer-side identifiers**; anything associating
records with projects, candidates, or evaluations belongs beside them, not on
them. And **every record a successful `Analyze` produces is marshallable**,
which takes two guards with deliberately different outcomes. A non-finite
trust input is resolved fail-closed inside `trust.Compute` before the
`Result` is returned, so the analysis succeeds carrying conservative finite
numbers. A timestamp `time.Time.MarshalJSON` refuses — a year outside
`[0,9999]`, or a zone offset of 24 hours or more — is rejected by
`Event.Validate()`, so the analysis fails with `event.ErrInvalidTimestamp`
instead. The projection itself sanitizes nothing. All three properties are
asserted by test.

Fixed-shape is not size-bounded. Caller-supplied strings in the record are
not length-limited here; what is excluded is the open-ended part, the
attribute map. Field and request size limits belong to whatever ingests
events over a network.

The record has no schema version field, deliberately. This contract already
governs how its fields may change, and a version number would duplicate that
with machinery nothing reads. A transport that carries records across a
network owns its own envelope version, exactly as `alert.Envelope` does.

### Persistence is not an extension point

Supplying a custom `Store` implementation is **not supported in v1**.
`store.Store`'s methods reference four internal types, so an external
module cannot implement it — deliberately, since making it public would
freeze `Baseline`, the stateful core of the engine, for the life of the
major version. Persistence is *selected* through `config.StorageConfig`
from the backends Trustvian ships: memory, file, and PostgreSQL.

### Resource ownership

A store compiled from configuration is owned by the caller. An `Engine`
never closes a store it was given, because it did not open it. Where a
compiled store holds resources, the caller closes it:

```go
if c, ok := store.(io.Closer); ok {
	defer c.Close()
}
```

There is deliberately no `Engine.Close`: an engine closing a resource it
does not own invites double-closes. If that changes, it changes in a
major version.

Consumers must **not** rely on: exhaustively switching over enum-like
constants without a default branch, or implementing any interface whose
definition lives under `internal/`.

## Configuration

Each configuration document is independently versioned —
`SchemaVersionV1` for policy, `AlertSchemaVersionV1`, `AnomalySchemaVersionV1`,
`StorageSchemaVersionV1` — and each is required, not inferred.

| Change | Allowed in |
|---|---|
| Add an optional field | Minor |
| Add a required field | Major, or a new schema version |
| Remove or rename a field | Major, after deprecation |
| Add an allowed enum value | Minor |
| Remove an allowed enum value | Major, after deprecation |
| Relax validation | Minor |
| Tighten validation so a previously valid document is rejected | Major |
| Change a documented default | Minor, named in CHANGELOG |

**The guarantee:** a configuration document valid under `v1.0` loads
under every later `v1.x` with unchanged meaning.

**The limit, stated because it is easy to assume otherwise:** the loader
calls `KnownFields(true)`, so unknown fields are rejected rather than
ignored. A configuration using a field added in `v1.5` will **not** load
on `v1.2`. Compatibility runs forward, not backward — plan rollbacks
accordingly.

## CLI

Commands, flags, and exit codes are automation-facing and treated as
such.

Exit codes are **scoped by command family**. There is no single global
meaning for every code, and assuming one will be wrong:

| Commands | `0` | `1` | `2` | `3` |
|---|---|---|---|---|
| `analyze`, `baseline`, `version` | success | the run failed | top-level invocation was wrong | — |
| `project`, `agent`, `candidate`, `eval` except `compare` | success | *unused* | usage | API, network, or server failure |
| `tui` | you quit | *unused* | usage | startup, HTTP, SSE, protocol, or terminal failure |
| `eval compare` | gate **PASS** | gate **FAIL** | usage | API, network, or server failure |

**Exit code `1` means gate failure only for `trustvian eval compare`; it
does not change the established meaning of code `1` for legacy
commands.** It further requires the server to have returned the explicit
verdict `fail`: `pass` and `fail` are a closed vocabulary, and any other
value — including a missing field, a different case, or a verdict from a
newer server — is an unsupported response and exits `3`. Adding a verdict
to that vocabulary is a minor change; changing what `1` means is major. [Task 060](tasks/v1.0/060-developer-cli.md) resolved the
conflict by scoping rather than renumbering, so no released automation
changed meaning. See
[ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md).

For the control-plane families, an API failure is always `3` and never
`1`. A 409, a 404, a 500, a timeout, a refused redirect and a malformed
response are all operational. This distinction is the point: a CI script
written as "non-zero means the gate failed" must not report a broken
network as a policy violation.

Within the `analyze`/`baseline` family, note that only top-level
dispatch — no arguments, or an unknown command — produces `2`. A
subcommand's own usage error (a missing file argument, an unknown flag)
has always produced `1`, and still does. That is released behavior, and
task 060 pinned it with tests rather than tidying it.

Human-readable output is not automation-facing. It carries no stability
promise, and parsing it is not a supported integration.

The control-plane commands do have a machine-readable mode: `--json`
writes the API's own successful response body to stdout, and the API's
error envelope to stderr. A successful response must be a syntactically
valid, non-empty JSON body in both output modes; a `2xx` carrying
anything else exits `3` rather than being forwarded as a result. Those fields inherit the `/v1` API contract
rather than defining a second field namespace — the CLI adds no wrapper
and removes no field, so a client must tolerate additive fields exactly
as an HTTP client would. Results go to stdout and diagnostics to stderr,
including on a gate FAIL, where the comparison evidence is still written
to stdout.

`analyze` and `baseline` still have no structured output mode; automation
that needs structured engine results uses the Go SDK. Adding one would be
additive and allowed in a minor; it is listed under
[public API review outcome](#public-api-review-outcome).

The control-plane commands accept no positional arguments. A trailing
argument is a usage error (`2`) raised before any request is sent, so an
invocation typo cannot be mistaken for an API or gate result. Legacy
`analyze`, `baseline` and `version` keep their existing operand parsing.

### Platform persistence backends

[Task 064](tasks/v1.0/064-postgresql-platform-backend.md) added PostgreSQL
alongside SQLite. Backend selection is a deployment concern and appears in none
of the contracts above.

| Surface | Class | Guarantee | May change | Breaking |
|---|---|---|---|---|
| Backend selection names (`sqlite`, `postgres`) | OPERATIONALLY STABLE | The accepted values and that omitting one means SQLite | New backend names | Major |
| `TRUSTVIAN_PLATFORM_POSTGRES_DSN` | OPERATIONALLY STABLE | `trustvian-local` reads the PostgreSQL DSN from it | An additional configuration source | Major |
| `platform.SchemaVersion` | INTERNAL | One logical version governs both physical schemas | Incremented with a migration on **both** backends | n/a |
| Physical SQLite schema | OPERATIONALLY STABLE | Migrated forward only; a newer schema fails closed | Additive tables and columns via a version bump | Major |
| Physical PostgreSQL schema | OPERATIONALLY STABLE | Same | Same | Major |

`/v1` behaviour is identical on either backend, and no response says which
database answered. PostgreSQL internals — SQLSTATE, constraint, table and column
names, raw SQL, the DSN, host or username — never reach a caller: they map to
the same persistence sentinels the SQLite store already used.

A deployment sharing one PostgreSQL database between processes shares
authoritative state but **not** realtime notifications; each process keeps its
own in-process bus. That is a known limitation of task 064, not a defect, and
task 069 owns cross-node realtime.

### Web control plane

The local runtime serves a browser UI from the same listener as the API
([task 063](tasks/v1.0/063-minimal-web-control-plane.md)).

| Surface | Class | Guarantee | May change | Breaking |
|---|---|---|---|---|
| `/` and the WebUI static asset paths | OPERATIONALLY STABLE | The local runtime serves a browser UI at its root origin | Asset filenames, their number and their contents | Major |
| WebUI visual layout, DOM structure, CSS classes, element IDs | OBSERVATIONAL | Nothing | Anything, in any release | Never |

The page's markup is **not** an interface. Automation reads `/v1`, which is the
machine contract every client shares; nothing should scrape the DOM or depend on
a class name. This is the same classification the TUI's rendered dashboard
carries, for the same reason.

Task 063 changes no `/v1` route, field, status code or error envelope, and no
realtime protocol semantics. It adds no CORS header, no authentication, and no
discovery field — the WebUI origin *is* the API origin, so `runtime.json`
remains two fields.

`trustvian tui --run-id <id> [--api-url <url>]` is intended operational
surface: the command name, both flag names, and the exit contract above are
stable. Its **rendered dashboard is not** — layout, spacing, column widths,
row formatting, help wording and connection-status wording are observational
and may change in any release. Automation reads the `/v1` API, which is the
machine interface; nothing should parse the terminal.

The control-plane commands and `tui` accept `--api-url`. It is **no longer
required**: when a local runtime is running in the working directory, they read
its endpoint from `.trustvian/runtime.json` instead. This is additive —
existing scripts that pass `--api-url` behave exactly as before, and an
explicit URL is never overridden by a file.

Omitting `--api-url` with no local runtime is **operational** (`3`), not usage
(`2`): after task 062 the invocation itself is valid and the environment is
what failed. An explicitly malformed `--api-url` remains usage.

*Omitting* means absent from the command line. `--api-url ""` is an explicit
empty endpoint: usage (`2`), and no discovery file is read. The distinction is
load-bearing for CI, where an empty value is an unset variable rather than a
request to use whatever runtime the checkout contains.

### Local runtime discovery file

`.trustvian/runtime.json` is written by the local runtime and consumed by the
shipped CLI and TUI, so its schema is operational surface even though the file
itself is ephemeral:

| Field | Class | Meaning |
|---|---|---|
| `version` | OPERATIONALLY STABLE | Schema version; currently `"1"` |
| `api_url` | OPERATIONALLY STABLE | Base URL of the running local control plane |

Additive fields may be added within version 1 and clients must tolerate them.
The meanings of existing version-1 fields do not change; a new meaning requires
a new version. An **unknown version fails closed** — a client that does not
understand the file refuses it rather than guessing.

The file must be **exactly one** JSON document under **4 KiB**, measured on the
bytes read. A second document, trailing content after the first, or anything
past the bound is refused outright rather than parsed as far as it goes.

The port itself is **not** stable: the runtime binds an ephemeral port and
republishes it on every start. Nothing should record or hard-code it, which is
the reason discovery exists.

`trustvian-local` is a repository-internal executable, not a released artifact.

 No default address or
port is defined yet, and no environment variable is read — task 062 owns
integrated local startup and may add a default then. Adding one is
additive; changing one scripts depend on would not be.

## Environment variables

Only variables read by a shipped binary or by the reference deployment
are operational surface:

| Variable | Class | Role |
|---|---|---|
| `TRUSTVIAN_POSTGRES_DSN`, `TRUSTVIAN_POSTGRES_USER`, `TRUSTVIAN_POSTGRES_PASSWORD`, `TRUSTVIAN_POSTGRES_DB` | OPERATIONALLY STABLE | Reference-deployment storage credentials |
| `TRUSTVIAN_HEALTH_PORT`, `TRUSTVIAN_OTLP_GRPC_PORT`, `TRUSTVIAN_POSTGRES_HOST_PORT` | OPERATIONALLY STABLE | Reference-deployment port mapping |
| `TRUSTVIAN_RUNTIME_POSTGRES_DB` | OPERATIONALLY STABLE | Database the runtime uses after a restore cutover |
| `TRUSTVIAN_IMAGE_REGISTRY` | OPERATIONALLY STABLE | Registry override for image resolution |
| `TRUSTVIAN_DEMO_*` | OBSERVATIONAL | Demo producer only; not a production interface |
| `TRUSTVIAN_TEST_*`, `TRUSTVIAN_WORKFLOW_DIR`, `TRUSTVIAN_ACTION_REF_RESOLVER` | INTERNAL | Test and CI plumbing; may change at any time |

## Collector processor

The processor type name and its configuration keys — `policy`,
`storage`, `health`, `evaluation` — are an operational contract: a Collector
configuration that works on `v1.0` works on every later `v1.x`.

Trustvian guarantees its own keys only. Behavior inherited from the
OpenTelemetry Collector, including how the Collector itself parses and
validates configuration, is not Trustvian's to promise.

## Policy format

Schema and semantics are separate promises, and both hold within `v1`:

- **Schema** — rule and condition field names, decision values, and the
  required `DefaultAction`/`DefaultReason`.
- **Semantics** — first-match-wins evaluation order, and fail-closed to
  `BLOCK` when a policy is empty or invalid. This is a security
  property, not an implementation detail: it does not change in a minor
  or a patch.

New decision values may be added in a minor, so a consumer that switches
on `Decision` needs a default branch.

## Persisted state

| Question | Answer |
|---|---|
| Can state written by `v1.x` be read by a later `v1.y`? | **Yes.** Within a major version, later binaries read earlier state |
| Can a later `v1.y` read a `v1.x` backup? | **Yes**, by the same rule |
| Is downgrade supported? | **No.** Migrations are forward-only, and an older binary meeting a newer schema version fails closed rather than guessing |
| Are migrations forward-only? | **Yes** |

Storage carries its own version independently of the release number:
PostgreSQL `SchemaVersion = 2`, file snapshot `version: 2`. **Any change
to the stored `Baseline` shape bumps the storage schema version whatever
the release number does.**

Both moved from 1 to 2 for learning scopes
([ADR 0024](adr/0024-learning-scope-is-a-baseline-key-dimension.md)).
Version 1 is read and upgraded in both backends, and every pre-scope
baseline lands in the default scope with its learned state unchanged —
nothing is invented and nothing moves between scopes.

The bump was required even though the change looks additive, and the reason
generalizes: `Scope` changes what a persisted record *identifies*. One
version-2 snapshot can hold `(scope A, actor X, prod)` and `(scope B, actor
X, prod)`; a version-1 reader ignores the unknown field, sees two baselines
with the same key, and keeps whichever it loads last. **An "additive field"
that can make two distinct logical identities look like one is not
additive**, and a version that only *might* be misread is treated as one
that will be.

An unrecognized version is still fatal and is not auto-upgraded, because
silently rewriting a layout written by another version is how state gets
corrupted. `ErrSchemaVersionMismatch` and `ErrAmbiguousSchemaState` exist to
make the operator decide. Downgrade is not supported: an older binary
refuses version-2 state rather than collapsing scopes, which is the intended
outcome — recovery is restoring a pre-upgrade backup.

One consequence of bounded fingerprint admission
([ADR 0019](adr/0019-bounded-fingerprint-admission.md)) belongs here: a
baseline written before the bound may hold more identities than the
current cap. It is read whole, never truncated, and this stays true for
the life of `v1`.

## Database schema

- **Additive** — a new nullable column or a new table may land in a
  minor, with the schema version bumped and migration applied
  transactionally.
- **Destructive** — dropping or repurposing a column or table is a major
  change, and needs migration guidance in the release notes.
- **Rollback** — not supported. Restore from a backup taken before the
  upgrade; see [Operations](operations.md).
- **Sequencing** — upgrade the schema before, or as part of, starting
  the new binary. Running a new binary against an old schema fails
  closed at startup rather than degrading.

## Metrics

Metric names, types, and label keys are dashboard contracts. Today:
`trustvian.analyses`, `trustvian.decisions`, `trustvian.observations`,
`trustvian.analysis.duration`, `trustvian.observe.duration`, with the
label keys `trustvian.decision` and `trustvian.outcome`.

- Adding a metric or a label key: minor.
- Adding a label *value*: minor. Consumers must tolerate values they do
  not recognise — unknown decisions are deliberately folded into a
  bounded set rather than passed through.
- Removing or renaming a metric: deprecate first, remove in a later
  minor.
- Removing or renaming a label key: major.

Label cardinality stays bounded by construction. No metric will gain an
actor, session, or fingerprint identifier as a label — that is a
security property, not a performance preference.

## Health and readiness

`/livez` and `/readyz`, and their status semantics, are an orchestration
contract: `200` when live or ready, `503` when not ready or draining.
Response bodies are diagnostic and carry no stability promise.

## Webhook payload

Payloads are versioned. The envelope carries `version`, currently `"1"`,
alongside the `alert` object.

Within envelope version `1`: existing field names and meanings do not
change, and new fields may be added — so a receiver must ignore fields
it does not recognise. A change that would break an existing receiver
ships as a new envelope version, not as a mutation of version `1`.

## Container and release artifacts

The entrypoint is `trustvian-collector`; the image runs as `nonroot` and
exposes `4317`, `4318`, `13133`.

`ghcr.io/trustvian/trustvian-collector:vX.Y.Z` is immutable. `latest`
and `X.Y` are convenience aliases that move on every stable release, and
carry no promise beyond pointing at a signed image — production
deployments should pin the immutable tag.

Release artifacts are named `trustvian_<version>_<os>_<arch>` with a
`.tar.gz` or `.zip` extension, published alongside `checksums.txt`, an
SBOM, provenance attestations, and a Cosign signature. The naming
pattern and the presence of those files are stable; the internal layout
of an archive is not.

## Behavioral compatibility

Type compatibility is not the whole contract. A change that keeps every
signature intact but alters what Trustvian *decides* is still a change
users feel.

| Kind of change | Treated as | Version |
|---|---|---|
| A signal computed incorrectly relative to its documented formula | Bug fix | Patch |
| A documented formula itself changing | Breaking semantic change | Major |
| A new signal shipped disabled by default (zero weight) | Additive | Minor |
| A new signal enabled by default | Breaking semantic change | Major |
| A documented default threshold or weight changing | Compatible tuning, if called out in CHANGELOG | Minor |
| Learning eligibility changing which decisions train the baseline | Breaking semantic change | Major |
| `Engine.Observe` reporting `learned` more accurately for the same input | Bug fix — it reported learning that did not happen | Minor, called out in CHANGELOG |
| Learned-state identity gaining a dimension whose default preserves existing lookups | Additive, with a storage-version bump | Minor |
| Fail-closed behavior becoming less strict | Never permitted without a major, and only with explicit security review | Major |
| `Event.Validate()` rejecting input it previously accepted | Breaking for a producer that sent it | Major, unless the input could not be processed correctly in the first place |

What is **not** promised: that a given event produces a numerically
identical score forever. Baselines are learned state, and scores move as
they learn — that is the product working. What is promised is that the
*rules* producing those scores do not change silently.

When in doubt, ask whether an operator's existing policy would start
making different decisions on unchanged traffic. If yes, treat it as
breaking regardless of what the type signatures say.

## Deprecation

Version-based, not time-based, because an OSS project cannot promise a
calendar:

1. Mark the surface deprecated in its own documentation and in
   `CHANGELOG.md`, naming the replacement.
2. Where the language allows it, make the deprecation visible in the
   surface itself — a Go doc comment beginning `Deprecated:`, a CLI
   warning on stderr, a documented note on a metric.
3. Keep it working for **at least one subsequent minor release**.
4. Remove it only at a boundary the matrix permits.

A surface that never shipped in a stable release needs none of this.

## Security exception

A severe vulnerability may require a change that cannot wait for a
deprecation cycle or a major release. That is permitted, and it is
deliberately narrow. Such a change must carry:

- an explicit security rationale in the release notes,
- a `CHANGELOG.md` entry identifying what changed and why,
- migration guidance wherever one is possible,
- and a `security:` commit, so it is visible in history.

This is an exception for fixing exploitable defects, not a route around
the contract. "It is cleaner this way" is not a security rationale.

## Public API review outcome

The public surface was reviewed against this contract before the `v1`
freeze. Four items were decided:

| Finding | Decision |
|---|---|
| `WithTrustConfig` had no public path — exported but uncallable from outside the module | Fixed: `config.TrustConfig` + `config.CompileTrust` |
| `WithContextRisk`'s callback named an internal type, so it could not be written externally | Fixed: the callback takes the public `StableFeatures` |
| Custom `Store` implementations | Not a v1 extension point; documented above |
| Store lifecycle via `io.Closer` | Caller-owned; documented above, no `Engine.Close` |

Two remain open by choice, neither blocking:

- **No machine-readable CLI output.** `analyze` prints a formatted
  summary; automation uses the SDK. Adding a structured mode later is
  additive and allowed in a minor.
- **An actor at the fingerprint admission bound** stops learning new
  identities with nothing surfacing it. A behavioral question, recorded
  in [ADR 0019](adr/0019-bounded-fingerprint-admission.md).

## Related

- [Branching Strategy](governance/branching.md) — SemVer and release flow
- [Release Governance](governance/releases.md) — who may release
- [Operations](operations.md) — upgrade, backup, and the compatibility matrix for state
- [CHANGELOG.md](../CHANGELOG.md) — where breaking changes and deprecations appear
- [Decision Records](adr/README.md) — why the architecture is shaped this way
