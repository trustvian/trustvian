# Trustvian v0.10.0 — Developer Preview

The first release you can use for the thing Trustvian is for: run an agent you
already have under Trustvian, watch what it does, and stop a pull request that
changes its behavior.

This is a **preview**. The platform described here ships for the first time,
and its surfaces are not yet covered by a `v1` compatibility promise.
Everything below is in
[CHANGELOG.md](https://github.com/trustvian/trustvian/blob/v0.10.0/CHANGELOG.md#v0100--developer-preview),
grouped by capability.

## The developer journey

The whole path, from this release's archive alone, is in
[Getting Started § Developer preview](https://github.com/trustvian/trustvian/blob/v0.10.0/docs/getting-started.md#developer-preview).

1. **Install from the archive.** The macOS and Linux archives now hold three
   binaries side by side: `trustvian` (the CLI), `trustvian-local` (the local
   control plane) and `trustvian-collector` (the OTLP receiver with the
   Trustvian processor). Keep them in one directory: `trustvian` finds the
   other two next to itself. Verify against `checksums.txt`.
2. **`trustvian dev -- <command>`** runs your agent unchanged. It starts a
   control plane and an OTLP receiver, provisions the project, agent, candidate
   and run from your git repository, and points your agent's existing
   OpenTelemetry exporter at the receiver. It adds no SDK. It exits with your
   command's status. Start `trustvian-local` first and pass `--api-url` to keep
   the control plane, and the WebUI, running between commands.
3. **The WebUI**, at the printed `Web:` URL, opens on **Live**. It shows every
   run producing telemetry, with nothing to type. Select one to see the agent,
   tool and service flow at the fidelity the telemetry carries, with the
   decision, risk, trust and anomaly computed for each step. From there it
   compares runs, follows a failed gate check to the observations behind it,
   and manages projects, environments and promotions.
4. **A scenario file** names a reference command, a candidate command, how many
   times to run each (`runs: N`) and the gate's limits. Every field is
   required; nothing has a default.
5. **`trustvian eval run --scenario FILE`** runs each side N times, each run
   under its own learning scope. The control plane counts in how many runs each
   behavior appeared and gates the difference. Exit codes: `0` PASS, `1` gate
   FAIL, `2` usage, `3` operational. `--reference last` reuses a recorded
   reference side. **`--suite DIR --scenario-timeout D`** runs a directory of
   scenarios with a deadline for each.
6. **Calibrate `N`, `k` and `j`** before trusting a FAIL from a model-driven
   agent: run an unchanged candidate against itself and choose limits where it
   never fails ([guide](https://github.com/trustvian/trustvian/blob/v0.10.0/docs/platform-cli.md#calibrating-n-k-and-j)).
7. **The two-job GitHub workflow**
   ([example](https://github.com/trustvian/trustvian/blob/v0.10.0/examples/github-actions/behavioral-gate.yml),
   [guide](https://github.com/trustvian/trustvian/blob/v0.10.0/docs/ci-github-action.md)).
   - `trustvian-run` runs the scenario on the pull request's code, read-only.
     Its exit code is the check.
   - `trustvian-comment`, in a separate job holding the only write scope and
     running no pull request code, posts the result as one comment, edited in
     place on every push.
   - Both build Trustvian from a commit you pin, rather than downloading a
     release.

Also new: AI semantic telemetry. GenAI tool spans become behaviors named by
tool, and Trustvian records which instrumentation layer each behavior came from.
Each decision gets durable, bounded event history. Gate checks and behavioral
deltas resolve to the evidence behind them. The platform also adds an
environment model, recorded promotions and a PostgreSQL backend.

## What this preview does not include

The milestone is not gated on, and does not deliver:
- multi-node and load validation (069), platform security hardening (070), or
  platform backup and restore (071) — the local control plane is
  **unauthenticated and binds loopback only**;
- sandbox sharing;
- multi-tenancy, access control, an MCP server surface, ML-based detection, or
  prompt- and content-level analysis.

Event history (067), the evidence explorer (076) and promotion did land in this
release. They are not part of what the preview promises.

`trustvian-platform` and `trustvian-processor` remain repository-internal Go
modules. Only `github.com/trustvian/trustvian` is published.

## Known limits

- **No default `k`; calibrate first.** An unchanged *deterministic* workload
  passes at `k = 1, j = 0`. An unchanged *nondeterministic* one can fail
  there. Task 078 measured a model-driven agent failing 6 of 252 unchanged
  self-comparisons at T = 0.7 and 1 of 252 at T = 1.3. At T = 1.3, no `k`
  removed every crossing.
- **A scenario asserts no ordering.** Under `eval run`, `dev`'s Collector
  configuration leaves `transition_weight` at 0, and every repetition starts
  from a fresh learning scope. A reorder alone therefore cannot fail a scenario,
  and the block-decision and critical-risk checks are marked advisory.
- **No Windows for `dev` or `--suite`.** Both refuse to run on Windows; use WSL2.
  The Windows archive carries only `trustvian`.
- **A fork pull request gets no comment.** GitHub gives the comment job a
  read-only token. The job records a warning and a job-summary note and stays
  green; the check is still the run job's.
- **GitHub Enterprise Server is not supported by the poster.**
  `GITHUB_API_URL` must be a bare `https://` host; GHES's `/api/v3` path is
  refused. github.com and GHE.com work.
- **The comment action supports the job's own `GITHUB_TOKEN` only.** It finds
  its earlier comment by `github-actions[bot]` authorship, so another identity
  would post duplicates.
- **A git repository with a commit is required for `eval run`.** It derives
  the project and candidate from git and has no flag to name them. Outside a
  repository it refuses, and its message suggests `trustvian dev --candidate`,
  which `eval run` does not accept.
- **One agent, one project.** An agent belongs to the project that first
  registered it. A scenario for the same agent under a different project stops
  with exit `3` ("run is outside the scenario execution's scope").
- **macOS binaries are not notarized.** Download with `curl` as documented. A
  browser download is quarantined by Gatekeeper; clear it with
  `xattr -d com.apple.quarantine` on the three binaries.

## Verifying

```bash
sha256sum -c checksums.txt --ignore-missing
./trustvian_v0.10.0_linux_amd64/trustvian version    # trustvian v0.10.0
```

The container image `ghcr.io/trustvian/trustvian-collector:v0.10.0` is signed
with keyless Cosign and carries SBOM and provenance attestations; see
[supply-chain.md](https://github.com/trustvian/trustvian/blob/v0.10.0/docs/supply-chain.md#verifying-a-published-image).
