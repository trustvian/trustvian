# Trustvian

**Behavioral security and evaluation for AI agents** — *Trust the Behavior.*

> OpenTelemetry observes behavior. Trustvian evaluates whether that
> behavior should be trusted.

Trustvian is an open-source, local-first behavioral security and evaluation
platform for AI agents and agentic applications, built on a behavioral trust
engine that is equally usable on its own.

The **engine** turns runtime behavior — API calls, service-to-service traffic,
database access, AI-agent tool calls — into an explainable trust score and a
security decision. It learns each actor's normal behavior, measures how far a
new action deviates from it, and evaluates that evidence against policy you
control.

The **platform** asks the next question: *did this version of this agent behave
differently from the last one, and does that difference pass the gates your
team set?* It runs on a laptop, with no account and loopback binding by
default, and consumes the engine as an ordinary dependency — neither layer
absorbs the other, and the direction of that dependency never reverses
([ADR 0022](docs/adr/0022-core-platform-boundary.md)).

Decisions are deterministic and explainable: every score carries the
signals that produced it, and every decision carries the rule that made
it. There is no model to retrain and no opaque verdict.

Behavioral observability does not require content observability. Trustvian
answers *what did this actor do, and is that normal* from metadata — actor,
operation, target, tool and model identity, sequence, timing, status and
correlation — and does not need, retain, fingerprint or display prompt text,
completions, reasoning, tool arguments, tool results, request bodies or SQL.

[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![CI](https://github.com/trustvian/trustvian/actions/workflows/ci.yml/badge.svg)](https://github.com/trustvian/trustvian/actions/workflows/ci.yml)
[![Latest tag](https://img.shields.io/github/v/tag/trustvian/trustvian?label=latest)](https://github.com/trustvian/trustvian/tags)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

## Why Trustvian?

Authentication and authorization answer questions about *identity*.
Telemetry answers questions about *activity*. Neither answers the question
that matters once an actor is already inside your system:

| Layer | Question |
|---|---|
| Identity | Who is this actor? |
| Telemetry | What did this actor do? |
| **Trustvian** | **Does this behavior match what should be trusted?** |

A valid credential does not stop behaving strangely. A service keeps its
permissions when it starts reaching an unfamiliar dependency; an AI agent
keeps its API key when it begins calling tools in an order it has never
used before. Static permissions are evaluated once, then drift silently.
Trustvian instead looks at behavior that is *unusual for this particular
actor*:

- a service suddenly calling a dependency it has never contacted
- an AI agent using an unfamiliar sequence of tools
- an actor reaching an unexpected destination
- a delegated action arriving from an unfamiliar delegator
- an operation occurring far outside its learned latency or timing pattern

Unfamiliar behavior is **evidence, not a verdict**. Trustvian quantifies and
explains the deviation; your policy decides what to do about it. A new
deployment and an intrusion can look alike to a baseline — which is exactly
why the decision stays configurable and the reasoning stays visible.

## How It Works

```text
Event → Features → Fingerprint → Baseline → Anomaly → Trust → Policy → Decision
```

| Stage | What it does |
|---|---|
| **Event** | A normalized record of one action: actor, operation, target, context |
| **Features** | Splits it into *stable* dimensions (the behavior's identity) and *volatile* ones (latency, errors, timing) |
| **Fingerprint** | A deterministic hash of the stable dimensions — "this kind of action by this kind of actor" |
| **Baseline** | Per-actor learned statistics for each fingerprint: counts, timing, error rates, sequence history |
| **Anomaly** | Scores deviation, keeping every contributing signal — plus a separate confidence for how much the baseline is worth trusting yet |
| **Trust** | Combines identity confidence, anomaly evidence, and context risk into one score and a risk level |
| **Policy** | First-match-wins rules over that result |
| **Decision** | `allow`, `observe_only`, `alert`, `challenge`, `require_approval`, or `block` |

Learning is explicit and gated. `Analyze` never mutates state; `Observe`
folds a result into the baseline only when the action actually proceeded, so
repeating a blocked action cannot teach the engine to accept it.

## Key Capabilities

**Behavioral baselines** — deterministic fingerprints, per-actor frequency
and recency, latency and error-rate statistics, time-of-day patterns. All
state is bounded: no actor can grow its baseline without limit.

**Sequence analysis** — order-aware detection beyond single events:
transition deviation, transition rarity, bounded 3-gram novelty, and
first-order Markov surprisal. Each signal is opt-in.

**Trust and policy** — explainable anomaly contributors, a multiplicative
trust score, risk classification, and ordered policy rules that fail closed
on misconfiguration.

**Alerts** — declarative rules over results, delivered to a generic HTTPS
webhook with HMAC-SHA256 request signing.

Persistence, OpenTelemetry integration, and AI-agent security each have
their own section below.

## Quick Start

### Install

```bash
go install github.com/trustvian/trustvian/cmd/trustvian@latest   # CLI
go get github.com/trustvian/trustvian                            # library
```

### CLI

The repository ships event fixtures, so the fastest first run is from a
clone:

```bash
git clone https://github.com/trustvian/trustvian && cd trustvian
go run ./cmd/trustvian analyze cmd/trustvian/testdata/normal.json
```

```text
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

A maximum anomaly score on a first-ever event is expected: the fingerprint
is genuinely novel, but confidence in that reading is zero, so it
contributes nothing to trust yet.

To learn from a corpus and then score against it, use `baseline build` with
persistent storage:

```bash
trustvian baseline build --storage-config storage.yaml corpus.json
trustvian analyze        --storage-config storage.yaml events.json
```

See the [CLI guide](docs/cli-guide.md).

### Go SDK

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

func main() {
	engine := trustvian.NewEngine()
	ctx := context.Background()

	ev := event.Event{
		ID:        "evt-001",
		Timestamp: time.Now(),
		Actor: event.Actor{
			ID:                 "svc-checkout",
			Type:               event.ActorTypeService,
			IdentityConfidence: 1.0,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryHTTP,
			Name:     "GET /api/orders",
		},
		Target: event.Target{Name: "orders-api"},
	}

	result, err := engine.Analyze(ctx, ev)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Explain())

	// Fold this observation into the baseline, if the action proceeded.
	if _, err := engine.Observe(ctx, result); err != nil {
		log.Fatal(err)
	}
}
```

See the [SDK guide](docs/sdk-guide.md).

## Production Persistence

`NewEngine()` uses an in-memory store, so nothing persists unless you ask
for it. Three backends are available, all selected through the same public
configuration document.

| Backend | Intended use | Survives restart | Shared across processes |
|---|---|---|---|
| `memory` | Tests and ephemeral workloads. The default. | No | No |
| `file` | Single-process durable use — a CLI run, or one long-lived service | Yes | No — there is no cross-process file locking |
| `postgres` | Production and horizontally-scaled deployments | Yes | Yes |

PostgreSQL is the backend to choose when more than one instance must agree
about an actor. Two instances with two separate files hold two different
baselines, so the same actor can be familiar to one and novel to the other,
and the decision an event receives depends on which replica handled it.

```yaml
# storage.yaml
version: v1
type: postgres
postgres:
  dsn: postgres://trustvian:PASSWORD@db.internal:5432/trustvian?sslmode=require
  max_connections: 25          # optional
  connect_timeout_seconds: 15  # optional
```

> Trustvian's own YAML loader does **not** expand environment variables, so
> a DSN written here is a literal secret in a file. To keep credentials out
> of configuration, build `config.StorageConfig` in Go and read the DSN from
> the environment yourself, or — inside the Collector — use its native
> `${env:VAR}` syntax. See the [storage guide](docs/storage-guide.md).

In Go:

```go
s, err := config.CompileStorage(cfg)
if err != nil {
	return err // never a silent fallback
}
if c, ok := s.(io.Closer); ok {
	defer c.Close()
}
engine := trustvian.NewEngine(trustvian.WithStore(s))
```

**Storage selection fails closed.** If PostgreSQL is explicitly configured
and the database is unreachable, unauthenticated, or running an incompatible
schema version, initialization returns an error and no store — never a
fallback to in-memory. A silent downgrade would mean every later decision
was made against state you believed was durable and shared.

Concurrent `Observe` for one actor is atomic and loses no updates, including
the first observation for an actor the database has never seen. Durability,
concurrency, schema, and failure semantics:
[storage guide](docs/storage-guide.md).

## OpenTelemetry

Two integration paths. The Collector is not required to use Trustvian.

**In-process** — an instrumented application with Trustvian as a library.
The inbound adapter maps a finished span to an `Event` using standard
semantic conventions, and the outbound side writes `trustvian.*` attributes
back onto the span.

**Through the Collector** — no application changes:

```text
OTLP → OpenTelemetry Collector → Trustvian processor → Engine
```

```yaml
processors:
  trustvian:
    storage:
      version: v1
      type: postgres
      postgres:
        dsn: ${env:TRUSTVIAN_POSTGRES_DSN}
```

Spans are enriched in place with `trustvian.anomaly.score`,
`trustvian.trust.score`, `trustvian.risk.level`, `trustvian.decision`, and
`trustvian.fingerprint.id`, then forwarded unchanged in shape.

See the [OpenTelemetry adapter](docs/OPENTELEMETRY.md) and the
[Collector processor](processor/README.md).

## Configuration

Three independent configuration documents, each with its own schema,
loader, and compiler. They stay separate because they answer different
questions.

| Document | Question it answers | Guide |
|---|---|---|
| **Policy** | What decision should be made? | [policy-guide.md](docs/policy-guide.md) |
| **Anomaly** | Which behavioral signals contribute, and how strongly? | [anomaly-config-guide.md](docs/anomaly-config-guide.md) |
| **Alerts** | Which results should raise an alert, and where is it delivered? | [`examples/alert-webhook`](examples/alert-webhook/) |

Each compiles through public API — `config.CompilePolicy`,
`config.CompileAnomaly`, `config.CompileAlerts`, `config.CompileStorage` —
so an external consumer configures the engine without importing anything
internal. The CLI accepts the same documents through `--config`,
`--anomaly-config`, and `--storage-config`.

A misconfigured policy fails closed to `block` rather than falling through
to a permissive default.

## AI Agent Security

AI agents run through the same pipeline as services and users — no second
engine and no agent-specific detector. What agents add is context:

| Field | Meaning |
|---|---|
| `SessionID` | Groups actions belonging to one conversation or task |
| `DelegatedFrom` | The actor that delegated this action |
| `ApprovalStatus` | Recorded approval evidence for this action |

That makes agent-specific behavior measurable: unfamiliar tool sequences,
delegation from a delegator this agent has not accepted work from before,
and policy that distinguishes an approved sensitive action from an
unapproved one.

Two boundaries stated plainly:

- Trustvian **evaluates** recorded delegation and approval evidence. It does
  not authenticate that evidence, and it is not an approval workflow — a
  `DelegatedFrom` value is a claim carried by your telemetry, not a verified
  provenance chain.
- Familiar is not the same as authorized, and unfamiliar is not the same as
  malicious. Both are inputs to a decision you configure.

See [use cases](docs/use-cases.md) and
[sequence analysis](docs/sequence-analysis.md).

## Local-First Platform

Everything above is the engine. The platform is the layer that turns it into a
workflow rather than a library:

> Develop locally. Evaluate in sandbox. Promote with evidence. Monitor in
> production.

```text
telemetry → live behavior → behavioral evidence
          → reference versus candidate → gate → promotion
```

One command starts all of it — SQLite, the control plane, the realtime bus,
and a loopback HTTP listener serving both the `/v1` API and the browser:

```bash
make local
```

```text
Trustvian local runtime
API:   http://127.0.0.1:54321
Web:   http://127.0.0.1:54321/
State: .trustvian/platform.db
```

Clients started in the same directory discover that endpoint themselves, so
there is no `--api-url` to pass and no account to create.

| Capability | What it does |
|---|---|
| **Evaluation run** | A bounded execution assessing one candidate, grouping its records, results and behavioral evidence |
| **Behavioral diff** | Which behavioral shapes a candidate gained, lost, or shares with a reference run — keyed by fingerprint, never by commit or artifact digest |
| **Scorecard** | A fixed-shape comparison of the two runs' decision, risk, approval and numeric evidence. Deliberately no composite score |
| **Hard gates** | Five deterministic integer checks returning PASS or FAIL against limits you own. A favorable average cannot override a failed check, because no weighting path exists |
| **Environments and promotion** | A project owns environments; a promotion is an append-only record of a *decision* and the gate evidence it rested on — never a deployment, and never a claim about where a candidate now runs |
| **Three interfaces** | CLI for CI, TUI for the inner loop, browser for management and investigation — each an adapter over the same control-plane services, none carrying its own copy of a gate |

Metadata only throughout: a diff, a scorecard, a gate result and a promotion
record contain no prompt, completion, tool argument, tool result or body.

**This layer lives on `main` and is not in a release yet.** The current stable
line is `v0.9.x`, which ships the engine, `analyze`/`baseline`/`version`, and
the Collector processor — so `go install ...@latest` does *not* include the
platform commands. Build from a clone to use them.

Four milestones are **specified and not implemented**, and the platform is not
usable end to end without them. [docs/ROADMAP.md](docs/ROADMAP.md) is
authoritative for their status:

| Task | What it adds |
|---|---|
| [075](docs/tasks/v1.0/075-ai-semantic-telemetry-normalization.md) | Agent-oriented OpenTelemetry read at the fidelity it carries, so a tool call is a tool call rather than an HTTP POST |
| [076](docs/tasks/v1.0/076-behavioral-evidence-explorer.md) | *Why* a behavior was familiar, new or anomalous — sessions, traces and behavioral sequence, from metadata alone |
| [077](docs/tasks/v1.0/077-unified-otlp-local-dev-runtime.md) | One command that wraps an existing agent and composes the runtime around it, with no source modification |
| [078](docs/tasks/v1.0/078-behavioral-scenario-suites.md) | The same behavioral scenario run again, diffed and gated — repeatably, in CI |

- [Local development](docs/local-development.md) — what `make local` starts,
  and how discovery works
- [Platform CLI](docs/platform-cli.md) — `project`, `agent`, `candidate`,
  `eval`, and the CI exit-code contract
- [Terminal dashboard](docs/tui.md) · [Web interface](docs/webui.md)

## Reference Deployment

A runnable Compose stack demonstrating the full production path:

```bash
cd deployments/docker-compose
docker compose up -d --build
docker compose run --rm demo-producer
```

```text
demo producer → OTLP → Collector + Trustvian processor → PostgreSQL
```

The demo sends deterministic telemetry, the engine learns from it, and the
baseline survives a full `docker compose down` and `up`. The runtime serves
`/livez` and `/readyz` on `127.0.0.1:13133`. `./smoke-test.sh` verifies the
whole path, and `./recovery-drill.sh` proves a backup restores and serves;
both exit non-zero on failure. Backup, restore, and upgrade procedures are
in [docs/operations.md](docs/operations.md).

This is a **reference deployment** for local evaluation, not hardened
production orchestration: credentials are placeholders and TLS is off. Its
[deployment guide](deployments/docker-compose/README.md) documents what to
change first.

## Architecture

```mermaid
flowchart TD
    A["Applications · Services · AI Agents"] -->|"telemetry"| B["OpenTelemetry Collector<br/>(optional)"]
    A -->|"Go SDK, direct"| C
    B --> C["Trustvian Engine"]
    C --> D["Features → Fingerprint"]
    D --> E["Baseline"]
    E --> F["Anomaly"]
    F --> G["Trust"]
    G --> H["Policy → Decision"]
    E <--> I["Store port"]
    I --> J["InMemory"]
    I --> K["FileStore"]
    I --> L["PostgreSQL"]
```

The engine is a hexagonal core: each pipeline stage is a small package built
around a pure function, and storage sits behind a two-method port. The core
carries no database, OpenTelemetry, or transport dependency — adapters
depend on the core, never the reverse.

The platform is a separate Go module that depends on the core's public API and
imports nothing under `internal/`, and the core does not depend on the platform
at all. `make check-platform-boundary` asks the module graph rather than
trusting review, and CI runs it on every pull request.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and the
[decision records](docs/adr/).

## Security Model

Design properties, each enforced by tests rather than convention:

- **Explainable decisions** — every anomaly keeps its contributing signals; every decision carries a reason.
- **Policy-controlled learning** — only actions that proceeded are eligible to be learned, so repeating a blocked action cannot poison a baseline.
- **Bounded state** — every per-actor map has a cardinality cap.
- **Fail-closed storage** — explicitly configured persistence never silently degrades to a non-durable store.
- **Schema compatibility protection** — a newer schema version, or ambiguous metadata, aborts startup rather than being mutated.
- **Parameterized SQL** — every value is a bound parameter; credentials are never logged or wrapped into errors.
- **No raw telemetry warehouse** — current learned state is persisted, not event history. No prompts, tool arguments, request bodies, or SQL payloads.

Identity confidence is an **input** Trustvian trusts, not something it
computes; Trustvian is not an authenticator. See
[docs/SECURITY.md](docs/SECURITY.md).

Found a vulnerability? Report it privately, not in a public issue — see
[.github/SECURITY.md](.github/SECURITY.md).

## Documentation

[docs/README.md](docs/README.md) is the full index. The most-used entries:

| Topic | Document |
|---|---|
| Getting started | [getting-started.md](docs/getting-started.md) |
| Architecture · Domain model | [ARCHITECTURE.md](docs/ARCHITECTURE.md) · [DOMAIN.md](docs/DOMAIN.md) |
| Go SDK · CLI | [sdk-guide.md](docs/sdk-guide.md) · [cli-guide.md](docs/cli-guide.md) |
| Storage and persistence | [storage-guide.md](docs/storage-guide.md) |
| Local platform: the one-command runtime | [local-development.md](docs/local-development.md) |
| Platform CLI · Terminal dashboard · Web interface | [platform-cli.md](docs/platform-cli.md) · [tui.md](docs/tui.md) · [webui.md](docs/webui.md) |
| Operations: backup, restore, upgrade | [operations.md](docs/operations.md) |
| Policy · Behavioral signals | [policy-guide.md](docs/policy-guide.md) · [anomaly-config-guide.md](docs/anomaly-config-guide.md) |
| Sequence analysis | [sequence-analysis.md](docs/sequence-analysis.md) |
| OpenTelemetry · Collector processor | [OPENTELEMETRY.md](docs/OPENTELEMETRY.md) · [processor/README.md](processor/README.md) |
| Reference deployment | [deployments/docker-compose/README.md](deployments/docker-compose/README.md) |
| Observability and resource bounds | [observability.md](docs/observability.md) |
| Security · Performance | [SECURITY.md](docs/SECURITY.md) · [PERFORMANCE.md](docs/PERFORMANCE.md) |
| Verifying releases and the container image | [release-guide.md](docs/release-guide.md) · [supply-chain.md](docs/supply-chain.md) |
| Use cases · Decision records | [use-cases.md](docs/use-cases.md) · [adr/](docs/adr/) |

## Examples

Every example under [`examples/`](examples/) is a runnable Go program with
its own README and real captured output. The directory is a separate Go
module, which also proves the public API is usable from outside this
repository.

**Getting started** — [`basic`](examples/basic/)

**Security scenarios** — [`credential-misuse`](examples/credential-misuse/),
[`external-destination`](examples/external-destination/),
[`unexpected-dependency`](examples/unexpected-dependency/),
[`frequency-abuse`](examples/frequency-abuse/)

**AI agents** — [`ai-agent`](examples/ai-agent/),
[`ai-agent-security`](examples/ai-agent-security/)

**Persistence** — [`persistent-baseline`](examples/persistent-baseline/)

**Alerts** — [`alert-webhook`](examples/alert-webhook/)

## Development

```bash
make check     # gofmt, vet, build, race tests
make test      # all tests
make bench     # benchmarks with allocation stats
make examples  # run every example
```

PostgreSQL integration and stress tests are opt-in and skip without a
database, so `go test ./...` never requires one:

```bash
make integration-postgres
```

`make help` lists every target. Engineering conventions are documented in
[CLAUDE.md](CLAUDE.md) and [.claude/rules/](.claude/rules/).

## Project Status & Releases

Trustvian is under active development. The engine is released and stable at
`v0.9.x`. The local-first platform described above is on `main`, is not in any
release yet, and is gated on `v1.0` — which also needs 075–078 above.

- **Release history** — [CHANGELOG.md](CHANGELOG.md)
- **Milestone status and planned work** — [docs/ROADMAP.md](docs/ROADMAP.md)
- **Published releases** — [GitHub Releases](https://github.com/trustvian/trustvian/releases)
- **Early design history** — [docs/archive/project-spec.md](docs/archive/project-spec.md) (historical, not maintained)

## Contributing

Issues and pull requests are welcome. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the quality gates, the three-module
layout, and how to run the PostgreSQL tests, and
[docs/governance/branching.md](docs/governance/branching.md) for how branches,
pull requests, and releases fit together. The conventions this codebase
follows are documented in [CLAUDE.md](CLAUDE.md) and
[.claude/rules/](.claude/rules/).

## License

[Apache License 2.0](LICENSE)
