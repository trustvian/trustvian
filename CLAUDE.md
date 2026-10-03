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
`tasks/`) and `.claude/rules/`. `docs/tasks/NNN-*.md` are the current,
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

Never approve or reject a deployment, above all the `release` environment:
that approval is the human decision to publish a release. The one exception is
the boundary proof a user asks for (`docs/releasing-with-claude-code.md`,
Setup step 4): a *rejection* attempt that must fail with 403. Never create, edit
or delete a GitHub Release or a `v*` tag yourself; the release workflow does,
after that approval. Claude may run a
release (`make release-prep`, `make release`, `gh run rerun` on a release run)
by following `docs/release-runbook.md`, and stops at printing where a human
approves. Run it with the agent's own token, which has no Deployments,
Administration or Environments permission (`docs/governance/agents.md`).

Never use `--force` or `--force-with-lease` against protected branches or
tags.

Never add an AI agent, bot, automation identity, or the current credential as
a ruleset bypass actor, and never use an existing bypass entry — including the
Organization Admin bypass — even when running under a credential that holds
it. The one bypass actor that exists, the `trustvian-release` GitHub App on
the "Release tags: creation" ruleset, was configured by a human (ADR 0060);
never change it, act as it, or see or handle its private key.

These operations are prohibited even if the authenticated GitHub credential
has administrator privileges. Technical capability is not authorization.

Normal changes must use:

short-lived branch -> Pull Request -> CI -> human approval -> main

If a task appears to require violating one of these invariants, STOP and
request explicit human intervention instead.

This section is a reminder, not a security boundary — see
`docs/governance/agents.md` for the credential isolation that is.

## Commit Messages

Before every commit:

1. inspect `git diff --cached`;
2. determine type and optional scope;
3. generate the commit message according to `docs/COMMIT_CONVENTION.md`;
4. ensure the message describes the actual staged change.

Show the message before committing, and commit exactly that message. Never
reuse a task title as the subject — the task says what was asked, the diff
says what changed.

Pull Request titles must follow the same summary format; `make pr-title
TITLE='...'` checks one, and CI checks it on every pull request.

## Merge Authority

Claude may prepare a pull request. Claude may never merge one into `main`.

Every pull request into `main` needs at least one human approval, from an
authorized maintainer or an Organization Admin. The final merge action is
reserved for a human Trustvian Organization Admin, who may be the same person
who reviewed it.

During normal GitHub operations Claude should authenticate with a dedicated
least-privilege identity, separate from any human Organization Admin's
credential.

Claude may never supply the required human approval. A review from an agent
identity does not satisfy it, even though GitHub cannot tell the two apart.

Even when Claude is authenticated using an administrator credential,
technical capability does not constitute authorization.

When a pull request becomes merge-ready, STOP and report:

```text
WAITING FOR HUMAN ORGANIZATION ADMIN MERGE
```

Do not merge it, do not enable auto-merge on it, and do not configure a
bypass around the requirement.

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