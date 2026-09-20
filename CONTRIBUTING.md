# Contributing to Trustvian

Issues and pull requests are welcome. This document covers what CI
enforces and how to run the same checks locally.

**Found a security vulnerability?** Do not open an issue or pull request —
report it privately as described in [.github/SECURITY.md](.github/SECURITY.md).

For what the code should look like, see [CLAUDE.md](CLAUDE.md) and
[.claude/rules/](.claude/rules/) — they document the conventions this
codebase actually follows (package shape, error handling, test style,
dependency confinement), not generic Go advice.

## Development workflow

Trustvian uses short-lived branches and pull requests into `main`. There is
no second integration branch, and no pull request targets anything else:

```text
branch from main:  feat/<description>   (or fix/, docs/, ci/, security/, …)
   ↓  implement, run the gates below
open a pull request against main  →  CI  →  review  →  squash merge
```

Keep a branch to one reviewable change, and delete it once it merges. The
full model — naming, release candidates, hotfixes, maintenance lines — is
[docs/governance/branching.md](docs/governance/branching.md).

`main` enforces this: direct pushes are rejected, the checks below are
required, every review thread must be resolved, and merges are squash-only.
Every pull request needs at least one human approval — from a maintainer or an
Organization Admin — and the final merge is performed by an Organization
Admin. The rules and the reasoning behind each are in
[docs/governance/repository.md](docs/governance/repository.md).

## Before opening a pull request

```bash
make check
```

That runs `gofmt -l`, `go vet`, a build, and the race tests for the root
module — the same gates CI runs. `make help` lists every target.

New behavior needs a test. Security-relevant behavior needs a test that
would fail if the behavior regressed; several of this repository's
guarantees were verified by deliberately breaking the implementation and
confirming the test caught it.

## The four modules

Trustvian is four Go modules, and they are separate on purpose:

| Module | What it is |
|---|---|
| `.` (root) | The engine, CLI, and public API |
| `processor/` | The OpenTelemetry Collector processor |
| `examples/` | Runnable examples — and proof the public API works from outside |
| `platform/` | The control-plane domain (evaluation), which the engine never depends on |

All three nested modules resolve the core through a `replace` directive.
**Always verify them with the workspace disabled**:

```bash
cd processor && GOWORK=off go build ./... && GOWORK=off go test -race ./...
cd examples  && GOWORK=off go build ./... && GOWORK=off go test ./...
cd platform  && GOWORK=off go build ./... && GOWORK=off go test -race ./...
```

A Go workspace makes these modules resolve the local source automatically,
which hides a broken module boundary until someone tries to consume the
published module. CI runs both with `GOWORK=off` for exactly this reason.

`examples/` must never import `internal/*`. It exists to demonstrate that
the public API is sufficient; an internal import would compile here and
break for every real consumer. CI checks this.

## PostgreSQL tests

Storage integration and stress tests are opt-in. They skip when
`TRUSTVIAN_TEST_POSTGRES_DSN` is unset, so `go test ./...` never requires a
database:

```bash
make integration-postgres
```

That starts the reference deployment's PostgreSQL service and runs the full
suite against it. See [docs/storage-guide.md](docs/storage-guide.md) for
the manual setup and for the database-restart durability test, which needs
its own opt-in because restarting a server disrupts everything else
connected to it.

## Test tiers

Not every test runs on every commit:

| Tier | Runs | What it covers |
|---|---|---|
| **Pull request / push** | `main` | Format, vet, build, tests, race, and `govulncheck` — all four modules. PostgreSQL integration with `-short`. Backup, restore, and upgrade from the previous release against PostgreSQL 17. Release-matrix dry run, container build (amd64), module consistency, workflow action references, Compose config validation. |
| **Nightly** | Scheduled, or on demand | The full PostgreSQL stress tier (high-contention writes, concurrent first-writes, bounded row counts), database-restart durability, the reference deployment's end-to-end smoke test, and its recovery drill. |

The split is by cost, not by importance. Correctness gates belong on pull
requests, where a failure means the change is wrong. The stress tier takes
minutes and measures contention behavior, so it runs on a schedule where
it is useful as a trend rather than a tax on every push.

Run the stress tier locally by omitting `-short`:

```bash
TRUSTVIAN_TEST_POSTGRES_DSN='...' go test -race ./...          # everything
TRUSTVIAN_TEST_POSTGRES_DSN='...' go test -race -short ./...   # integration only
```

## Workflow action references

`scripts/check-action-refs.sh` proves every `uses:` reference in
`.github/workflows` resolves to a real tag, branch, or commit — the check
that would have caught `sigstore/cosign-installer@v4` before it reached a
release tag. It uses `git ls-remote`, so it needs no token:

```bash
./scripts/check-action-refs.sh          # every workflow reference
./scripts/test-check-action-refs.sh     # the checker's own tests (no network)
```

`scripts/image-name.sh` prints the canonical container repository and is the
only place it is spelled — the release workflow, CI, and the Makefile all
resolve it through there, so the image that gets scanned is the image that
gets signed:

```bash
./scripts/image-name.sh        # -> ghcr.io/trustvian/trustvian-collector
./scripts/test-image-name.sh   # its tests, including the uppercase-owner case
```

A reference that cannot be looked up at all is reported as `UNVERIFIED`
rather than as missing. Both fail the run; only one of them means the
reference is wrong.

## Reference deployment

```bash
cd deployments/docker-compose
docker compose config    # what CI validates on every push
./smoke-test.sh          # the full end-to-end proof, nightly in CI
./recovery-drill.sh      # backup → restore → cutover → readiness, nightly in CI
```

## Backup, restore, and upgrade tests

These drive the real `pg_dump`/`pg_restore` scripts, so they need
PostgreSQL client tools matching the server's major version, and opt in
separately from the other PostgreSQL tests:

```bash
TRUSTVIAN_TEST_BACKUP_RESTORE=1 TRUSTVIAN_TEST_POSTGRES_DSN='...' \
  go test -race -run 'Backup|Restore|Upgrade' ./scripts/
```

The upgrade test additionally needs a CLI built from the previous release
tag; [docs/operations.md § Recovery drill](docs/operations.md#recovery-drill)
shows how.

## CI

| Workflow | Trigger | Purpose |
|---|---|---|
| `.github/workflows/ci.yml` | push / PR on `main` | Quality gates |
| `.github/workflows/nightly.yml` | schedule, manual | Expensive tiers |
| `.github/workflows/release.yml` | version tag only | Release artifacts, container image, signing |

`ci.yml` and `nightly.yml` are read-only, need no secrets, and publish
nothing, so pull requests from forks run the full gate set with nothing to
leak. Only `release.yml` publishes, and only on a tag — see
[docs/release-guide.md](docs/release-guide.md).

If CI fails on formatting, run `make fmt` — CI reports violations but never
rewrites your code.

## Modules and releases

This repository publishes exactly one Go module — the root,
`github.com/trustvian/trustvian`. The `processor`, `examples`, and `platform`
modules are repository-internal: their module paths are not resolvable and
they are built from a clone.

All three resolve the core through a local `replace`. `platform` requires it
at the zero placeholder Go writes for a fully replaced dependency, because the
`DecisionRecord` API it consumes has not been published in any tag — naming a
released version would be a claim that is false.

`platform`'s module path also sits outside `github.com/trustvian/trustvian` on
purpose, which is what makes a core `internal/*` import a compile error rather
than a convention — `make check-platform-boundary` enforces that and the
reverse direction.

`make check-modules` enforces the publication invariants;
[docs/release-guide.md](docs/release-guide.md) explains the model and what
promoting a module would require.

To build the release artifact matrix locally — no tag, credentials, or
upload:

```bash
make release-dry-run
```

`make check-modules` verifies a declared root version from a local git tag
when the checkout has one, and from the module proxy otherwise, so it works
in a shallow clone. Set `CHECK_MODULES_OFFLINE=1` to forbid the proxy
fallback.

## Commits and releases

Before changing anything an operator or a consumer can see — the Go API,
a configuration field, a CLI flag, a metric, the storage format —
check [docs/compatibility.md](docs/compatibility.md) for what that
surface promises. Say in the pull request which of the three it is:
backward-compatible, a deprecation, or a breaking change. Breaking
changes and deprecations belong in `CHANGELOG.md`.

Trustvian uses Conventional Commits for commit subjects and pull request
titles:

```text
<type>(<scope>): <imperative summary>
```

```text
feat(store): add PostgreSQL baseline persistence
fix(postgres): restore readiness after a reconnect
docs: clarify the collector configuration
```

Because pull requests are squash-merged, the pull request title becomes the
commit subject in `main` — CI validates it with
`./scripts/check-pr-title.sh`. See
[docs/COMMIT_CONVENTION.md](docs/COMMIT_CONVENTION.md) for the types, scopes,
and breaking-change format.

Release tags are cut from `main` and are immutable, including failed release
candidates. Release history lives in
[CHANGELOG.md](CHANGELOG.md), and milestone status in
[docs/ROADMAP.md](docs/ROADMAP.md) — please don't add project-status prose
to the README, which is deliberately evergreen.
