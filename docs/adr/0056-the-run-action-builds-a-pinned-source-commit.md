# 0056 — The run action is an in-repository composite action that builds a pinned source commit

**Status:** Accepted

## Context

[Task 079](../tasks/v1.0/079-ci-integration-github-action.md) puts task 078's
behavioral gate into GitHub Actions. Its first slice is the run side: invoke
`trustvian eval run --json`, pass the exit code through, and preserve the
result document for a later reporting job. The specification left four
questions to implementation, and this slice has to answer three of them:

1. Whether the action lives in this repository or its own (open question 1).
2. Composite, Docker or JavaScript (open question 3).
3. How the `trustvian` binary is obtained (open question 4), with one
   constraint: the version is explicit, and a floating `latest` is refused.

The packaging of the comment side (open question 2) is the next slice's
decision, and the security split itself — run and comment in different jobs —
is already settled by the task, not by this ADR.

The runtime question had a fact behind it that had to be checked rather than
assumed. The newest release, `v0.9.0`, ships `trustvian` alone in every
archive, and that binary answers `unknown command "eval"`: it predates
`eval run`, `dev`, and the two helper binaries `dev` supervises
(`trustvian-local`, `trustvian-collector`). No release can run a scenario.

## Decision

### 1. In this repository, at `.github/actions/trustvian-run`

The action is versioned with the CLI it wraps, reviewed in the same pull
requests, and tested by this repository's CI against the real binaries. A
caller pins it the way it pins any action in a subdirectory:

```text
uses: trustvian/trustvian/.github/actions/trustvian-run@<full commit>
```

A Marketplace listing and tag-based pinning would want a repository of its
own. Neither is needed to use or pin the action, and moving it later changes
only the `uses:` path.

### 2. A composite action of bash scripts

The work is "obtain a binary, run it, keep its stdout": no runtime of its own
is needed. A Docker action would pull an image on every job and could not run
on macOS runners; a JavaScript action would add a Node dependency tree to
review for what four short scripts do.

- `setup.sh` provides the runtime, `run.sh` runs the CLI and preserves its
  result, `actions/upload-artifact` uploads it, and `finish.sh` writes the
  summary and exits with the CLI's code.
- **Inputs reach the scripts only through the environment.** No `${{ }}`
  expression appears inside a script, so no input is spliced into shell text.
  The CLI receives each value as one argument attached to its flag
  (`--scenario=VALUE`).
- **The final step exits with exactly the CLI's code.** The action's own
  failures (input, setup, preservation, upload) fail the step that hit them
  and never alter the `exit-code` output. This inherits
  [ADR 0033](0033-developer-cli-is-a-thin-http-adapter.md) § 9 and § 10; it
  decides nothing new about exit codes.
- **The action refuses `pull_request_target` and `workflow_run`.** It executes
  the workload, and those events would hand it a write-scoped token and
  secrets. Refusing them in the action, not only in the documentation, means
  a copied workflow on the wrong trigger fails closed.

### 3. The runtime is built from one reviewed commit

`runtime.env` pins two things:

- **the Trustvian source:** a repository URL and a full 40-character commit;
- **the Go toolchain:** an exact version and the SHA-256 that go.dev publishes
  for each supported archive (Linux and macOS, amd64 and arm64).

`setup.sh` downloads that toolchain and verifies its digest. It fetches exactly
that commit with a depth-one fetch by commit id, under a git configuration that
ignores the runner's user and system settings. It then builds the three
binaries the way the release does: `CGO_ENABLED=0`, `-trimpath`, and no
`-ldflags`. The build runs in a cleared environment, so a `GOFLAGS`, `GOPROXY`,
`GONOSUMDB` or `GOTOOLCHAIN` the job set for its own code cannot change it.
Module checksums are verified against the public checksum database.

It then **verifies rather than trusts:**

- Every binary's Go build information must record `vcs.revision` equal to the
  pinned commit and `vcs.modified=false`.
- The built `trustvian` must offer `eval run --suite --scenario-timeout`.

Nothing is fabricated. The producer versions in the result document are what
the binaries report from Go's own build information — for the commit pinned
here, `v0.0.0-20261002054939-576da8fe1f67` with its revision. That pseudo-version
is what Go derives for a commit with no tag in its depth-one history, and it is
genuine.

**Everything lives under `$RUNNER_TEMP`.** The toolchain, the source, the build
and module caches, the binaries, and the control plane's state are all there.
Nothing is written to the workload's checkout. Nothing is added to the job's
`PATH`. A Go installation the job already set up is neither used nor changed.

The commit pinned in this slice is the one that merged task 078's suites
(pull request #137): the first commit with every command the action runs.

### 4. The action provides its own control plane, or attaches to one

With no `api-url`, `run.sh` does what `make local` does:

- it starts `trustvian-local` on a loopback port with a private state
  directory;
- it reads the endpoint from the discovery file, accepting only
  `http://127.0.0.1:<port>`;
- it passes that endpoint to the CLI with `--api-url`.

When the CLI is done, `run.sh` stops that one process, by the PID it recorded:
SIGTERM, then SIGKILL after 10s. It uses SIGTERM rather than SIGINT because a
non-interactive shell starts background processes with SIGINT ignored. A trap
stops it on every exit path. The Collector each repetition starts belongs to
`trustvian dev`, which stops it, so the action stops nothing else.

With `api-url`, the action starts nothing and passes the URL through; the CLI
owns what a bad one means.

### 5. The artifact is the CLI's stdout, untouched, and minimal metadata

- **`result.json`** is the CLI's stdout, byte for byte. It is stored only when
  stdout is exactly one JSON object within 32 MiB plus 64 KiB — the CLI's own
  suite cap plus framing.
- **`trustvian-run.json`** is always written once the CLI has run. It holds:
  - the head commit, from the event, and where it came from;
  - the event, repository and run;
  - the mode;
  - whether the control plane was started or attached;
  - the CLI's exact exit code;
  - the result's status, size and SHA-256;
  - the runtime commit and Go version.

Output that is missing after a PASS or FAIL, invalid, or oversized is
reported and fails the action. It is never truncated, repaired or stored as a
result. stderr streams to the job log and is not stored, so the artifact
carries nothing the result document does not.

The action checks syntax and stops. It reads no field of the document, so the
next slice's renderer must decode it strictly — the artifact was produced by a
job that ran untrusted code.

## Alternatives considered

**Download a release archive.** This is the obvious choice, and it has a
digest to verify. It was rejected because no release contains the commands
(`v0.9.0` was checked: `unknown command "eval"`). Publishing one is a release
decision that this task does not make. Once a release ships all three binaries
with `eval run --suite`, switching `setup.sh` to download and verify that
archive changes only the runtime half of this ADR.

**Build from the action's own tree** (`$GITHUB_ACTION_PATH/../../..`). It
would need no fetch. But a downloaded action has no `.git`, so Go would record
no revision, and the producer version would be unknown — the outcome task 079
says is not evidence.

**`actions/setup-go` inside the action.** It changes the job's `PATH` and Go
version for every later step, including a workload written in Go that set up
its own toolchain. A composite action cannot undo that.

**Let `GOTOOLCHAIN` download Go through a preinstalled `go`.** It depends on
whatever `go` the runner happens to have, and self-hosted runners may have
none.

**Cache the built runtime across jobs.** It is deferred. A cache restore
replaces "verify what was built" with "trust what was stored", and the cost it
saves is measured in minutes, not correctness.

**A container image.** It is Linux only, and pulled on every job.

## Consequences

- **New, automation-facing surface:** the action's inputs and outputs, and the
  `trustvian-run.json` metadata document (version `"1"`), classified in
  [the compatibility contract](../compatibility.md#github-action).
- **The first invocation in a job compiles three binaries.** That took about a
  minute on a laptop; later invocations in the job reuse the toolchain, source
  and caches.
- **Changing the runtime is a reviewed change to `runtime.env`,** carried by
  whatever action commit a caller pins. There is no `latest`.
- **`--reference last` against the action's own control plane finds nothing**,
  because that control plane is new for every invocation. Reusing recorded
  references needs `api-url` and a persistent control plane.
- **Not built in this slice:**
  - the pull request comment and its job;
  - rendering a verdict;
  - caching across jobs;
  - a release-archive runtime;
  - Windows runners.
