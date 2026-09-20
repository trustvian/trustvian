# Trustvian Documentation

The complete index. If you are not sure where to start, the first section is
ordered for a first-time reader.

Everything here describes what Trustvian does today. Design material from
earlier in the project's life is kept separately under
[Historical material](#historical-material) and is not maintained.

## Start here

1. [Project overview](../README.md) — what Trustvian is and the problem it
   solves.
2. [Getting Started](getting-started.md) — install the CLI and SDK, run a
   first analysis.
3. Then pick your integration style: the [Go SDK](sdk-guide.md) to embed the
   engine, or the [CLI](cli-guide.md) to analyze events from files.

## Understand Trustvian

| Document | What's in it |
|---|---|
| [Architecture](ARCHITECTURE.md) | Engine boundaries, the pipeline, package layout, dependency direction |
| [Domain Model](DOMAIN.md) | Event, Fingerprint, Baseline, Anomaly, Trust, Policy, Decision, and how they relate |
| [Anomaly Configuration](anomaly-config-guide.md) | Every signal, what it measures, its defaults and ranges |
| [Sequence Analysis](sequence-analysis.md) | Order-aware detection: transitions, 3-grams, cold start, activation |
| [Use Cases](use-cases.md) | Four worked scenarios with verified input and output |
| [Decision Records](adr/README.md) | Why the durable architectural decisions were made, and which still govern |

## Build with Trustvian

| Document | What's in it |
|---|---|
| [Go SDK Guide](sdk-guide.md) | `Engine`, `Analyze`/`Observe`, `Result`, options, a worked baseline-maturity example |
| [CLI Guide](cli-guide.md) | `trustvian analyze` and `baseline build`, and the event JSON format |
| [Policy Guide](policy-guide.md) | Writing rules and conditions, fail-closed behavior, worked examples |
| [Storage Guide](storage-guide.md) | Memory, file, and PostgreSQL stores — when to use each, configuration, lifecycle |
| [OpenTelemetry Adapter](OPENTELEMETRY.md) | How a span maps to an `Event`, the attribute table, what is not yet implemented |
| [Collector Processor](../processor/README.md) | The standalone OTel Collector processor and its configuration |
| [Examples](../examples/README.md) | Runnable programs, each demonstrating one behavior end to end |

## Operate Trustvian

| Document | What's in it |
|---|---|
| [Operations](operations.md) | Backup, restore, upgrade, and rollback of learned state; the compatibility matrix; the recovery drill |
| [Reference Deployment](../deployments/docker-compose/README.md) | A runnable Collector + Trustvian + PostgreSQL stack with a persistence proof |
| [Storage Guide](storage-guide.md) | Production persistence, credentials, concurrency, schema |
| [Observability](observability.md) | Metrics the processor emits, their cardinality bound, and the runtime's resource limits |
| [Performance](PERFORMANCE.md) | Measured hot-path numbers, allocation and concurrency behavior |

## Security and reliability

Two documents share a name and answer different questions:

| Question | Document |
|---|---|
| What threats does the design account for, and what is tested? | [Runtime security model](SECURITY.md) |
| How do I report a vulnerability? | [Vulnerability reporting](../.github/SECURITY.md) |

| Also relevant | |
|---|---|
| [Supply Chain](supply-chain.md) | The official image: contents, signing, provenance, and how to verify one |
| [Release Guide](release-guide.md) | Verifying downloaded artifacts and recovering from a partial release |
| [Operations](operations.md) | Durability, restore verification, and upgrade safety |

## Contribute

| Document | What's in it |
|---|---|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | The local gates, the four modules, the test tiers |
| [Commit Convention](COMMIT_CONVENTION.md) | Commit subjects and pull request titles — CI validates the title |
| [Compatibility Contract](compatibility.md) | What `v1` promises not to break, and what a breaking change costs in version numbers |
| [Branching Strategy](governance/branching.md) | Branch from `main`, one change per pull request, squash merge |
| [Task Specifications](tasks/README.md) | Active specifications, and where completed ones live |
| [Decision Records](adr/README.md) | Read before proposing an architectural change |

## Project direction and decisions

Four documents, four different questions. The distinction matters:

| Question | Document |
|---|---|
| Is this change breaking? | [Compatibility Contract](compatibility.md) |
| Where is the project going? | [Roadmap](ROADMAP.md) |
| What shipped, and when? | [CHANGELOG.md](../CHANGELOG.md) |
| What is being built right now? | [Active tasks](tasks/README.md) |
| Why is the architecture the way it is? | [Decision records](adr/README.md) |

The [Roadmap](ROADMAP.md) also defines the `v1.0` production-readiness gate.
Its decomposition into implementation tasks lands in
[`tasks/v1.0/`](tasks/v1.0/README.md) as that work is scoped.

## Governance and releases

| Document | What's in it |
|---|---|
| [Repository Governance](governance/repository.md) | The enforced GitHub configuration: rulesets, required checks, review and merge authority, bypass scope |
| [Release Governance](governance/releases.md) | **Who** may release, tag protection, candidate immutability, same-SHA promotion |
| [Release Guide](release-guide.md) | **How** a release is produced and verified |
| [Branching Strategy](governance/branching.md) | Branches, release candidates, hotfixes, maintenance lines |
| [Agent Governance](governance/agents.md) | What AI coding agents may and may not do, and the credential boundary |

Release *authority* and release *procedure* are deliberately separate
documents. If the question is "am I allowed to do this", it is governance; if
it is "what do I run", it is the guide.

## Historical material

Retained for engineering traceability and design history. **It is not current
product documentation**, and it is not maintained.

[`archive/`](archive/README.md) holds the original project specification,
completed task specifications grouped by the release that shipped them, and
historical implementation plans.

---

Code, command output, and benchmark numbers in these documents were run
against this repository rather than written by hand — each file says how to
reproduce them. Engineering conventions this codebase follows are in
[`../CLAUDE.md`](../CLAUDE.md) and [`../.claude/rules/`](../.claude/rules/).
