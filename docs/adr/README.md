# Architecture Decision Records

Architecture Decision Records capture durable architectural decisions made by
the Trustvian project — what was decided, the context at the time, and what
was rejected. They are written once and not rewritten as the system evolves;
the **Status** field says whether a decision is still authoritative.

For how the system is built today see [ARCHITECTURE.md](../ARCHITECTURE.md);
for where it is going see [ROADMAP.md](../ROADMAP.md).

## Status

- **Proposed** — under consideration, not yet an architectural commitment
- **Accepted** — currently authoritative
- **Superseded** — replaced by a later ADR, which is named in the status line
- **Deprecated** — retained for history, discouraged for new work

Accepted does not mean recent, perfect, or unchangeable. It means the decision
still governs. An ADR whose surrounding implementation has grown — a port that
gained new implementations, a boundary that gained new members — stays Accepted
as long as its own decision holds.

## Decisions

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-hexagonal-core-and-pipeline-shape.md) | Hexagonal core, one package per pipeline stage | Accepted |
| [0002](0002-public-api-boundary.md) | Public API boundary: `event` is public, `Policy`/`Config` stay internal | Accepted |
| [0003](0003-opentelemetry-adapter-single-module.md) | OpenTelemetry as an internal adapter, in the same Go module | Accepted |
| [0004](0004-narrow-store-port-in-memory-only.md) | Narrow `Store` port, in-memory implementation only | Accepted |
| [0005](0005-fingerprint-computed-once-per-analyze.md) | Compute the Fingerprint once per `Analyze` call | Accepted |
| [0006](0006-file-backed-persistent-store.md) | File-backed persistent `Store`, synchronous flush-per-`Observe` | Accepted |
| [0007](0007-alert-package-is-public.md) | `alert` is a public package, not `internal/alert` | Accepted |
| [0008](0008-policy-config-boundary.md) | `config` is public; `internal/policy` stays internal | Accepted |
| [0009](0009-alert-config-is-a-separate-document.md) | Alert configuration is a separate document from Policy configuration | Accepted |
| [0010](0010-bounded-process-local-sequence-state.md) | Bounded, process-local sequence state, embedded in `Baseline` | Accepted |
| [0011](0011-transition-rarity-statistic-and-orientation.md) | Transition rarity: the statistic, its orientation, and why Markov waits | Accepted |
| [0012](0012-bounded-trigram-behavioral-context.md) | Bounded higher-order behavioral context: 3-grams, and why arbitrary n waits | Accepted |
| [0013](0013-first-order-markov-surprisal-without-duplicate-evidence.md) | First-order Markov surprisal without duplicate evidence | Accepted |
| [0014](0014-ai-agents-as-first-class-behavioral-actors.md) | AI agents as first-class behavioral actors, not a second engine | Accepted |
| [0015](0015-approval-as-policy-evidence-not-behavioral-anomaly.md) | Approval as policy evidence, not behavioral anomaly | Accepted |
| [0016](0016-delegation-as-behavioral-evidence-not-provenance.md) | Delegation as behavioral evidence, not provenance | Accepted |
| [0017](0017-public-anomaly-configuration-boundary.md) | Public anomaly configuration boundary | Accepted |
| [0018](0018-production-store-boundary-and-postgresql-direction.md) | Production Store boundary and PostgreSQL direction | Accepted |
| [0019](0019-bounded-fingerprint-admission.md) | Bounded fingerprint admission: refuse, never evict | Accepted |
| [0020](0020-v1-compatibility-contract.md) | A repository-wide compatibility contract, not an API-only one | Accepted |
| [0021](0021-public-stable-features-boundary.md) | A public stable-features view for the context-risk callback | Accepted |
| [0022](0022-core-platform-boundary.md) | Core and platform are two layers, one direction | Accepted |
| [0023](0023-interfaces-are-adapters.md) | Interfaces, transports, and stores are adapters over control-plane services | Accepted |
| [0024](0024-learning-scope-is-a-baseline-key-dimension.md) | Learning scope is a baseline-key dimension, not behavioral identity | Accepted |

No ADR is currently Superseded or Deprecated. Several later ADRs build on
earlier ones — 0006 and 0018 on 0004's narrow port, 0007/0008/0017 on 0002's
public-API test, 0013 on 0011's minimum-support floor, 0019 on the bounded-state
mandate 0010/0011/0012/0016 established, 0024 on the mechanism 0022 deliberately
left open — and each cites the earlier decision as
still governing rather than replacing it.

## Writing one

An ADR is warranted when a future maintainer would reasonably ask "why was
this done this way?" and the answer is not obvious from the code. Number it
next in sequence, state the status, and keep the shape the existing records
use: Context, Decision, Alternatives considered, Consequences.

Do not rewrite an existing ADR to match how the system looks now. If a
decision genuinely changes, write a new ADR and mark the old one Superseded by
it.
