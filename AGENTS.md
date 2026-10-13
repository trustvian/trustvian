# Trustvian Development Guide

## Project

Trustvian is an open-source behavioral security and trust engine
for applications, services, and AI agents.

## Documentation

The rules below are the condensed, durable principles. For the current
implementation's details — package structure, dependency direction,
domain model, security model, measured performance, and why
significant decisions were made — see `docs/` (`ARCHITECTURE.md`,
`DOMAIN.md`, `SECURITY.md`, `PERFORMANCE.md`, `ROADMAP.md`, `adr/`,
`tasks/`) and `.claude/rules/`. `docs/tasks/<version>/NNN-*.md` (and the unversioned `docs/tasks/NNN-*.md`) are the current,
independently-scoped implementation tasks — check there before
starting new work to see if it's already planned and scoped. Keep
`docs/` in sync with the code: when an
architectural change lands, update the relevant `docs/` file(s) and
add an ADR under `docs/adr/` if a future developer would reasonably
ask "why did we do this?"

## Language

Go.

Use the latest stable Go version supported by the project.

## Architecture

Prefer:

- Clean Architecture
- Hexagonal Architecture
- Small interfaces
- Dependency inversion
- Explicit domain models
- Testable components

Avoid:

- Global state
- Unnecessary abstractions
- Reflection unless justified
- Framework-heavy design
- Premature microservices

## Core Pipeline

Event
 -> Features
 -> Fingerprint
 -> Baseline
 -> Anomaly
 -> Trust
 -> Policy
 -> Decision

## Security

Security decisions must be:

- Explainable
- Deterministic where possible
- Auditable
- Testable

Never silently weaken a security policy.

## Performance

Trustvian is a runtime security engine.

Pay attention to:

- Allocations
- CPU overhead
- Lock contention
- Goroutine leaks
- Memory growth
- Hot paths

Use benchmarks for performance-sensitive code.

## OpenTelemetry

Follow OpenTelemetry semantic conventions.

Do not invent telemetry attributes without documenting them.

## Testing

Every new feature must include tests.

Prefer:

- Unit tests
- Table-driven tests
- Integration tests where necessary
- Benchmarks for hot paths

Run:

go test ./...

before considering a task complete.

## Git

Do not create commits unless explicitly requested.

Do not push to GitHub unless explicitly requested.

## Repository Safety

Never delete, rename, force-push, or rewrite `main`.

Never disable, weaken, delete, or bypass GitHub protection for `main`.

Never change the repository default branch away from `main`.

Never delete, move, or overwrite release tags.

Never use `--force` or `--force-with-lease` against protected branches or
tags.

Never add an AI agent, bot, automation identity, or the current credential as
a ruleset bypass actor, and never use an existing bypass entry — including the
Organization Admin bypass — even when running under a credential that holds
it.

These operations are prohibited even if the authenticated GitHub credential
has administrator privileges. Technical capability is not authorization.

## Commit Messages

All commits and Pull Request titles must follow
[Trustvian Commit Convention](docs/COMMIT_CONVENTION.md).

Before creating a commit, inspect the staged diff and generate the message
from the actual change.

Use:

`<type>(<scope>): <imperative summary>`

Do not use vague messages such as `update`, `changes`, `fix stuff`, `WIP`, or
`final`.

## Merge Authority

AI agents may create and update Pull Requests but must never merge a Pull
Request into `main`.

Every Pull Request to `main` requires at least one valid human review.

The reviewer may be an authorized maintainer or Organization Admin.

The final merge action is reserved exclusively for a human Trustvian
Organization Admin.

An Organization Admin may also act as the reviewer.

AI agents must use least-privilege credentials, distinct from any human
Organization Admin's credential.

An AI agent cannot satisfy the required human review. A review submitted by an
agent identity does not count, whatever GitHub's permission model allows.

AI agents must never use, request, derive, or configure a bypass around this
requirement.

When a pull request becomes merge-ready, stop and report that it is waiting
for a human Organization Admin. Do not merge it.

See `docs/governance/agents.md` for the credential isolation that makes this a
boundary rather than a request.

## Implementation Strategy

Do not implement large features in one step.

First:

1. Inspect the existing code.
2. Explain the proposed design.
3. Identify affected files.
4. Implement the smallest vertical slice.
5. Run tests.
6. Review the implementation.
7. Benchmark when relevant.
8. Update documentation.

Never rewrite working code unnecessarily.