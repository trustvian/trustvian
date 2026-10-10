---
name: verify-all
description: Run this repository's CI checks locally, module by module, exactly as .github/workflows/ci.yml resolves them (GOWORK=off per module), and report pass/fail/skipped per check. Use before declaring any Trustvian task done, before committing or opening a PR, or whenever asked to "verify", "run the gate", "run CI locally", "check everything passes" — even if the user only says "run the tests", because a root `go test ./...` silently misses the platform, processor and examples modules. `/verify-all` checks the modules the branch touched; `/verify-all all` checks all four.
---

# verify-all

Reproduce CI's per-module checks locally and report what passed, what failed and
what was **not run**. Report only — never edit code, never run `gofmt -w`, never
commit. A fix is a separate decision for the user.

## Why this exists

This repo is four Go modules — root, `platform/`, `processor/`, `examples/` —
and `go.work` is gitignored. With a workspace active, MVS resolves dependencies
differently from CI; that once hid a reachable vulnerability CI then caught. So
every command below runs with `GOWORK=off`, from that module's directory, the
way `ci.yml` does. A green root `go test ./...` says nothing about the other
three.

## 1. Decide scope

Arguments: `all` → all four modules. A module name (`root`, `platform`,
`processor`, `examples`) → just those. No argument → the modules the branch
touched:

```bash
git fetch -q origin main
{ git diff --name-only origin/main; git ls-files --others --exclude-standard; } | sort -u
```

This compares the working tree (committed + staged + unstaged) to `main`'s tip,
not to the merge-base: branches here are squash-merged, so a merge-base diff of
an already-merged branch lists every file it ever touched. If the branch is
behind `main`, this over-selects, which only costs time.

Map each path: `platform/**` → platform, `processor/**` → processor,
`examples/**` → examples, everything else → root. **A root change selects all
four**: each other module has `replace github.com/trustvian/trustvian => ../`,
so a root edit can break their builds. Paths that cannot affect a build select
nothing: `.claude/**`, `.codex/**`, `.github/copilot-instructions.md`, and any
`*.md` (the doc-drift check below still applies to the two release docs). If
nothing is selected, say so and stop — that is a valid result, not a failure.

State the scope and the reason in one line before running anything.

## 2. Checks per module

Run checks in order; keep going after a failure so the report is complete.
Capture each command's exit status and the tail of its output.

**Format** (every module) — `gofmt -l` exits 0 even when files need formatting;
the *output* is the signal:

```bash
out=$(cd <dir> && gofmt -l .); [ -z "$out" ]   # fail if any path printed
```

| Module (dir) | Checks, in order (all with `GOWORK=off`) |
|---|---|
| root (`.`) | format · `./scripts/check-modules.sh` · `go vet ./...` · `go build ./...` · `go test ./...` · `go test -race ./...` |
| platform (`platform`) | format · vet · build · `go test ./...` · `go test -race -timeout=15m ./...` · `./scripts/check-platform-boundary.sh` (run from repo root) |
| processor (`processor`) | format · vet · build · `go test ./...` · `go test -race ./...` |
| examples (`examples`) | format · vet · build · `go test ./...` · no-internal-imports: `grep -rn "github.com/trustvian/trustvian/internal" --include="*.go" .` must find **nothing** |

Run long race jobs in the background where the harness allows, and run
independent modules concurrently — but never two `-race` runs of the same
module at once.

**Release doc-drift** — if the diff touches `docs/release-runbook.md`,
`docs/releasing-with-claude-code.md`, `scripts/`, or `.github/workflows/`, also
run (repo root):

```bash
go test ./scripts -run 'Release|Publish|Pinned|Digest|SigningIdentity|Approver|Operator|Runbook|Docs|TokenTables|CaseIDs|App|Audit|ReleaseSetup' -count=1
```

**Vulnerability scan** — only with `/verify-all all` or when `go.mod`/`go.sum`
changed (it downloads govulncheck): `GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@latest ./...` per selected module.

## 3. PostgreSQL

Every Postgres-backed test, in every module, **skips and still passes** unless
`TRUSTVIAN_TEST_POSTGRES_DSN` is set. (`TRUSTVIAN_PLATFORM_POSTGRES_DSN` is
`trustvian-local`'s runtime setting, not a test gate — setting only it runs
nothing.) Do not start Docker or a database.

- **DSN not set:** report Postgres coverage as `SKIPPED — TRUSTVIAN_TEST_POSTGRES_DSN unset`, never as passed. Mention `make integration-postgres` as the way to get it.
- **DSN set:** the normal test runs above already exercise Postgres. For the root module add CI's step `go test -race -short ./...`. For platform, prove the tests actually ran, as CI does:

```bash
cd platform && GOWORK=off go test -count=1 -v \
  -run 'TestStoreConformance|TestStoreBackendsAgreeOnLogicalState|TestPostgres|TestRuntimeOnPostgres' \
  . ./localruntime > "$TMPDIR/pg-tests.log" 2>&1
grep -- '--- SKIP: TestPostgres' "$TMPDIR/pg-tests.log"   # any hit = FAIL (DSN wrong/service down)
grep -c -- '--- PASS' "$TMPDIR/pg-tests.log"
```

Write logs to the session scratchpad if one is available, not the repo.

## 4. Not covered locally

Always list these as `not run` so nobody mistakes a local pass for a full CI
pass: the `backup-restore` job (needs the previous release built from its tag),
`release-dry-run`, `container-build`, `workflow-refs` (network), shellcheck on
release scripts (only if `shellcheck` is absent), and the PR-title check
(`make pr-title TITLE='...'` checks one on request).

## 5. Report

One table, then failures. Keep it short; the user reads the table first.

```
Scope: platform, root → all four (root changed)

| Module    | fmt | vet | build | test | race | extra                      |
|-----------|-----|-----|-------|------|------|----------------------------|
| root      | ✓   | ✓   | ✓     | ✓    | ✓    | check-modules ✓            |
| platform  | ✓   | ✓   | ✓     | ✗    | ✓    | boundary ✓                 |
| processor | ✓   | ✓   | ✓     | ✓    | ✓    |                            |
| examples  | ✓   | ✓   | ✓     | ✓    | —    | no-internal-imports ✓      |

PostgreSQL: SKIPPED — TRUSTVIAN_TEST_POSTGRES_DSN unset
Not run locally: backup-restore, release-dry-run, container-build, workflow-refs, govulncheck
```

Use words as well as marks for anything not passing (`✗ FAIL`, `— skipped`,
`not run`). Under the table, for each failure: the exact command, the
module directory, and the relevant output (failing test names and their
assertion lines, or the unformatted file list) — trimmed, not the whole log.
End with one line: either "All selected checks passed" or "N checks failed".
Do not propose or apply fixes unless asked.
