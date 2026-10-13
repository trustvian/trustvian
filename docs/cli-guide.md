# CLI Guide

```bash
go install github.com/trustvian/trustvian/cmd/trustvian@latest
# or: make build   (from a clone; produces bin/trustvian)
```

```
trustvian analyze [--config <path>] [--anomaly-config <path>] [--storage-config <path>] <events.json>
trustvian baseline build [--config <path>] [--anomaly-config <path>] [--storage-config <path>] <events.json>
trustvian version
trustvian help
```

These run the engine in process against a file of events. The control-plane
commands (`project`, `agent`, `candidate`, `env`, `promotion`, `eval`,
`evidence`, `status`), `dev` and `tui` are in [Platform CLI](platform-cli.md)
and [Terminal dashboard](tui.md).

By default, the CLI uses a built-in starter policy (block on
high/critical risk, alert on medium, allow otherwise) — see
[`cmd/trustvian/policy.go`](../cmd/trustvian/policy.go) — and
`anomaly.DefaultConfig()`'s own scoring thresholds/weights, with every
`v0.6`/`v0.7` opt-in signal (transition/n-gram/Markov/delegation
deviation) left disabled, exactly as it always has been. Pass
`--config` to use a real policy from a file instead, and/or
`--anomaly-config` to enable and tune anomaly signals — see below.

## `--config <path>`

Both `analyze` and `baseline build` accept an optional `--config
<path>`, loading a schema-v1 YAML policy config the same way any other
caller outside this module would — `config.LoadFile` +
`config.CompilePolicy`, then `trustvian.WithPolicy` — and using the
result instead of the built-in default policy for that invocation.

```bash
trustvian analyze --config trustvian.yaml event.json
```

See [Configuration](../README.md#configuration) in the
README for the YAML format and [Policy Guide § Loading a Policy from a
YAML file](policy-guide.md#loading-a-policy-from-a-yaml-file) for the
full field reference — the CLI doesn't add or change any config
semantics of its own.

Without `--config`, behavior is exactly what it was before this flag
existed — the built-in default policy, unchanged.

If the given file is missing, fails to parse, or fails validation
(e.g. an unrecognized field, an invalid decision value, a duplicate
YAML key), the command fails closed: non-zero exit, an error on
stderr naming the problem, and **no analysis report is printed** — the
CLI never falls back to the built-in default policy when an explicit
`--config` was requested and couldn't be honored.

There is no environment-variable config, no auto-discovery of a
default config file path, and no live reload — `--config` is the only
way to select a file, and it's read once per invocation.

## `--anomaly-config <path>`

Both `analyze` and `baseline build` also accept an optional
`--anomaly-config <path>`, loading a schema-v1 YAML anomaly config the
same way any other caller outside this module would —
`config.LoadAnomalyFile` + `config.CompileAnomaly`, then
`trustvian.WithAnomalyConfig` — and using the result instead of
`anomaly.DefaultConfig()` for that invocation. This is how `v0.6`/`v0.7`
signals (opt-in, disabled by default) get enabled from the CLI.

```bash
trustvian analyze --anomaly-config anomaly.yaml event.json
```

See [Anomaly Configuration Guide](anomaly-config-guide.md) for the full
field reference. Same fail-closed discipline as `--config`: a missing,
unparseable, or invalid `--anomaly-config` file fails the whole
command — non-zero exit, an error naming the problem, no analysis
report — never a silent fallback to `anomaly.DefaultConfig()`. Without
it, behavior is exactly what it was before this flag existed.
`--config` and `--anomaly-config` are independent — pass either, both,
or neither.

## `--storage-config <path>`

Both subcommands accept `--storage-config <path>`, selecting where
learned baselines live (`config.LoadStorageFile` +
`config.CompileStorage`, then `trustvian.WithStore`). **This is what
makes a baseline survive past a single command.** Without it the CLI
uses an in-memory store and learns nothing durable — unchanged from
before the flag existed, and the reason `baseline build` was close to a
dry run until `v0.8` ([task
034](archive/tasks/v0.8/034-production-store-contract-and-public-boundary.md)).

```yaml
# storage.yaml
version: v1
type: file
file:
  path: /var/lib/trustvian/baseline.json
```

```bash
# Learn from a corpus, persisting what it learns:
trustvian baseline build --storage-config storage.yaml corpus.json

# A separate invocation now scores against that persisted baseline:
trustvian analyze --storage-config storage.yaml event.json
```

`type` is required — there is no implicit default, because silently
choosing the non-durable backend is exactly the failure this flag
exists to prevent. Supported values are `memory`, `file`, and
`postgres`; see [`storage-guide.md`](storage-guide.md) for how to choose
between them and for the full `postgres` configuration reference.

Fail-closed discipline is strictest here: a missing, unparseable, or
invalid `--storage-config`, an unopenable or corrupt state file, a
`postgres` block with no DSN, or an unreachable database all fail the
whole command — non-zero exit, no analysis report, and **never** a silent
fallback to in-memory storage. Substituting a non-durable store for an
explicitly requested durable one would lose precisely the state you asked
to keep.

All three config flags are independent — pass any combination.

## `trustvian analyze`

Scores every event in the file and prints one report per event.
`analyze` is strictly read-only across the whole file — even multiple
events in one file never build on each other's baseline (each is
scored as if it were the actor's first-ever observation). If you want
to see a baseline mature over many events, use the Go SDK — see
[Go SDK Guide § watching trust mature](sdk-guide.md#watching-trust-mature).

```bash
trustvian analyze event.json
```

```
Trustvian Behavioral Analysis

Service: svc-payment
Anomaly: 1.00
Trust:   0.95
Risk:    LOW

Detected:
  ! fingerprint never observed for this actor

Decision: ALLOW
Reason:   risk within tolerance
```

- `Service` — `Actor.ID`
- `Anomaly` — `Result.Anomaly.Score`, `[0,1]`
- `Trust` — `Result.Trust.Score`, `[0,1]`
- `Risk` — `Result.Trust.Risk`, one of `LOW`/`MEDIUM`/`HIGH`/`CRITICAL`
- `Detected:` — one line per `Result.Anomaly.Contributors[i].Detail`, only shown when at least one signal fired
- `Decision` / `Reason` — `Result.Decision` and `Result.Explanation.Reason`

More worked examples (agent tool calls, service-to-service, a sudden
sensitive-destination access): [Use Cases](use-cases.md).

## `trustvian baseline build`

Replays a corpus of events through the *same gated* `Analyze`+`Observe`
path as live traffic — it does not blindly trust every event in the
file, on purpose (see
[Go SDK Guide § Observe and learning](sdk-guide.md#observe-and-learning)).
Prints a summary instead of a per-event report.

```bash
trustvian baseline build corpus.json
```

```
Trustvian Baseline Build

Events processed: 4
Learned:          3
Skipped:          1 (flagged by policy; not learned from)
```

**Without `--storage-config`, this result does not persist.** The CLI
then uses an in-memory store, so a `Baseline` doesn't survive the
process exiting — running `baseline build` and then a separate
`analyze` invocation gets you a *fresh*, empty baseline in the second
process. To pre-seed a baseline a later invocation reads, pass the same
`--storage-config` to both, as
[above](#--storage-config-path). A long-running service builds its
baseline inside the process that serves `Analyze` calls — see the
[Go SDK Guide](sdk-guide.md).

## Event JSON format

`<events.json>` is a JSON array of events (even for one event, wrap it
in `[...]`):

```json
[
  {
    "id": "evt-1",
    "timestamp": "2026-01-01T12:00:00Z",
    "actor": {
      "id": "svc-payment",
      "type": "service",
      "identity_confidence": 0.95
    },
    "operation": {
      "category": "http",
      "name": "POST /payment",
      "direction": "inbound"
    },
    "target": {
      "name": "payment-db"
    },
    "context": {
      "environment": "production"
    },
    "attributes": {
      "duration_ms": 42,
      "error": false
    }
  }
]
```

| Field | Required | Notes |
|---|---|---|
| `id` | yes | any unique string |
| `timestamp` | yes | RFC 3339 |
| `actor.id` | yes | |
| `actor.type` | yes | `service`, `user`, `service_account`, `ai_agent`, `device`, `unknown` |
| `actor.identity_confidence` | yes | `[0, 1]` |
| `operation.category` | yes | `http`, `db`, `rpc`, `tool`, `external` |
| `operation.name` | yes | e.g. `"POST /payment"`, `"search_customer"` |
| `operation.direction` | no | `inbound`, `outbound`, or omit |
| `target.name` | no | destination service/DB/host |
| `context.environment` | no | |
| `context.trace_id` / `context.span_id` | no | for OTel correlation |
| `attributes.duration_ms` | no | feeds the latency anomaly signal |
| `attributes.error` | no | feeds the error anomaly signal |
| `attributes.*` (other keys) | no | passed through, available to a custom `ContextRisk` function |

Full Go-side type reference: [Go SDK Guide § the Event type](sdk-guide.md#the-event-type).

## Makefile shortcuts

```bash
make demo             # trustvian analyze against a bundled fixture
make baseline-demo    # trustvian baseline build against a bundled corpus
make run ARGS="analyze path/to/events.json"
```
