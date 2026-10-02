# Running behavioral scenarios in GitHub Actions

> **Run side, and an offline renderer.** These are the first two slices of
> [task 079](tasks/v1.0/079-ci-integration-github-action.md). The action runs a
> scenario or suite, fails the check with the scenario's own exit code, and
> preserves the result document as an artifact.
> [`trustvian-ci-render`](#rendering-the-artifact) turns that artifact into
> Markdown, offline. Nothing **posts** to the pull request yet. The comment job
> is the next slice and belongs in a separate job — see
> [What comes next](#what-comes-next).

The action lives in this repository at `.github/actions/trustvian-run`. It is
a thin workflow adapter over `trustvian eval run`
([Platform CLI](platform-cli.md#behavioral-scenarios-trustvian-eval-run)), in
the sense [ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md) uses:
the control plane decides, the CLI reports, and the action passes on what the
CLI returned.

## The workflow

[`examples/github-actions/behavioral-gate-run.yml`](../examples/github-actions/behavioral-gate-run.yml)
is the copyable version. In full:

```yaml
name: Behavioral gate

on:
  pull_request:
    branches: [main]

jobs:
  run:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: trustvian/trustvian/.github/actions/trustvian-run@REPLACE_WITH_A_REVIEWED_TRUSTVIAN_COMMIT_SHA
        with:
          scenario: scenarios/support-login.yaml
```

Replace the placeholder with the full commit of a Trustvian revision you have
reviewed. As written it cannot resolve, so an unedited copy fails at setup
rather than running an unpinned action.

Every line of the security posture is deliberate, and each is the line most
likely to be deleted as noise:

- **`pull_request`, and never `pull_request_target`.** `pull_request_target`
  runs in the base repository's context: a token that can write to the
  repository, and its secrets. A scenario file names a command, and on a pull
  request that command is the pull request's own code — so under
  `pull_request_target` a contributor's code would run holding that token.
  There is no configuration of it that makes running the candidate safe. The
  action refuses to run under `pull_request_target` and `workflow_run`.
- **Permissions per job, and none at workflow level.** A workflow-level grant
  reaches every job, including the one that runs the workload. The run job
  needs `contents: read` and nothing else.
- **`persist-credentials: false`.** `actions/checkout` defaults to `true` and
  writes the job's token into `.git/config`, where the workload and every
  dependency it loads could read it. The scenario needs the source, not the
  ability to push it.
- **Every action pinned to a full commit.** A tag can be moved; a commit
  cannot.
- **No secrets.** The run job uses none, so a fork pull request — which
  GitHub gives a read-only token and no secrets — runs exactly as a
  same-repository one does.

## Inputs

| Input | Passed as | Notes |
|---|---|---|
| `scenario` | `--scenario` | One scenario file, relative to `working-directory` |
| `suite` | `--suite` | A directory of scenario files. Exactly one of `scenario` and `suite`; the CLI refuses both or neither with exit `2` |
| `reference` | `--reference` | An execution id or `last` with `scenario`; only `last` with `suite`. Needs a control plane that keeps executions between runs — see `api-url` |
| `scenario-timeout` | `--scenario-timeout` | Each suite member's deadline, `1s`–`24h`. Required by the CLI with `suite` |
| `fail-fast` | `--fail-fast` | `true` or `false` (default) |
| `api-url` | `--api-url` | Attach to this control plane. Empty (default): the action starts its own, private one |
| `working-directory` | — | Where the CLI runs: the workload's git checkout, from which `trustvian dev` derives project, agent and candidate. Default `.` |
| `artifact-name` | — | Default `trustvian-run`. Use a distinct name for each invocation in one workflow run |

There is no input for a gate limit, a threshold or `k`. The scenario file is
the only place policy lives; a second copy in workflow YAML would disagree
with it eventually.

The action checks only what it must interpret itself (`fail-fast`,
`artifact-name`, `working-directory`). Every other value goes to the CLI as
one separate argument, attached to its flag (`--scenario=VALUE`), and the
CLI's own usage error is the answer. Nothing is ever placed into a shell
string.

## Outputs

| Output | Meaning |
|---|---|
| `exit-code` | Exactly what `trustvian eval run` exited with. Empty when it did not run |
| `result` | `present`, `absent`, `invalid` or `oversized` — whether stdout held a result document |
| `head-sha` | The commit the run describes — see [Which commit](#which-commit) |
| `artifact-id` | The uploaded artifact's id; empty when nothing was uploaded |
| `runtime-commit` | The Trustvian commit the runtime was built from |

## Exit codes are passed through

| Code | Meaning | The action |
|---|---|---|
| `0` | gate PASS | succeeds |
| `1` | gate FAIL | fails, with the CLI's code |
| `2` | usage | fails, with the CLI's code |
| `3` | operational | fails, with the CLI's code |

The last step of the action exits with exactly the CLI's code, and the
`exit-code` output carries it. `3` is never reported as `1`: an unreachable
control plane or a crashing workload is not a behavioral regression
([ADR 0033 § 10](adr/0033-developer-cli-is-a-thin-http-adapter.md)).

The action sets no `continue-on-error`, and has no "warn only" mode. If you
want a job to survive a FAIL, set `continue-on-error` on the step yourself, in
your own workflow, where reviewers will see it.

The action's **own** failures — an input it cannot interpret, a runtime it
could not build, a control plane that did not start, a result it could not
preserve, an upload that failed — fail the step that hit them, with an
annotation saying which. They never alter `exit-code`: when the CLI ran, that
output and the final step's exit status are still exactly the CLI's.

## What is preserved

The artifact (`artifact-name`, default `trustvian-run`) holds at most two
files:

| File | When | What |
|---|---|---|
| `result.json` | the CLI wrote a result document | stdout of `trustvian eval run --json`, byte for byte |
| `trustvian-run.json` | always, once the CLI has run | the execution metadata below |

```json
{
  "version": "1",
  "head": {"sha": "<40 hex>", "source": "event.pull_request.head.sha"},
  "event": "pull_request",
  "repository": "owner/name",
  "run": {"id": "123", "attempt": "1"},
  "mode": "scenario",
  "control_plane": "started",
  "cli": {"exit_code": 1},
  "result": {"status": "present", "file": "result.json", "bytes": 4096, "sha256": "<64 hex>"},
  "runtime": {"source_commit": "<40 hex>", "go_version": "go1.27.1"}
}
```

- **Uploaded whatever the CLI returned.** A FAIL or an operational error is
  exactly when the result matters.
- **stdout is the document; stderr is not.** stderr — progress, and the
  workload's own output — streams to the job log and is not stored in the
  artifact, which therefore carries nothing the result document does not.
- **Never truncated, never repaired.** A result larger than 32 MiB plus 64 KiB
  (the CLI's own cap on a suite document, plus framing) is `oversized` and is
  not stored. Output that is not exactly one JSON object is `invalid` and is
  not stored. Both fail the action.
- **A verdict without a document is a failure.** The CLI always writes its
  document with a PASS or a FAIL, so exit `0` or `1` with no document fails
  the action. Exit `2` or `3` with no document is expected — a single scenario
  writes none — and is recorded as `absent`.
- **The action reads no field of the result.** It checks that stdout is one
  JSON object and stops there. It does not read the verdict, a count or a
  limit. The next slice's renderer decodes the document strictly; it must not
  trust this action, or this artifact, to have validated anything else.

### Which commit

`head.sha` comes from the event, never from the code:

- On `pull_request` it is `github.event.pull_request.head.sha`, the pull
  request's head commit. `github.sha` on that event is the synthetic merge
  commit, which names nothing the author can check out, so it is never used
  there. A `pull_request` event without a full head commit stops the action
  before anything runs.
- On any other event it is `github.sha`, and `head.source` says so.

`trustvian dev` derives the *candidate* from the checked-out tree, which on a
default `pull_request` checkout is that merge commit. The two are different
identities and are recorded as such.

### The job summary

Minimal on purpose: the head commit, a link to the run, the exit code, and
whether the artifact was uploaded. It carries no verdict, no counts and no
behavior names. The [renderer](#rendering-the-artifact) produces those, and
nothing wires it into a job yet. Every value is checked
against the exact shape it must have (hex, digits, the artifact name's
character set) before it is written, and prose values are escaped and capped
as well.

## The runtime

The action builds `trustvian`, `trustvian-local` and `trustvian-collector` from
**one reviewed commit**, pinned in
[`runtime.env`](../.github/actions/trustvian-run/runtime.env), with a pinned Go
toolchain whose archive is checked against the SHA-256 go.dev publishes. See
[ADR 0056](adr/0056-the-run-action-builds-a-pinned-source-commit.md) for why it
builds rather than downloads.

- **Everything under `$RUNNER_TEMP`.** Nothing is written to your checkout,
  nothing is added to the job's `PATH`, and a Go installation your job already
  has is neither used nor changed.
- **Your job's Go and Git settings do not reach it.** Every Go command runs
  with a cleared environment and `GOTOOLCHAIN=local`, so a `GOTOOLCHAIN`,
  `GOFLAGS`, `GOENV` or `GOPROXY` your job sets for its own code changes
  nothing. Every Git command runs with inherited `GIT_*` variables and Git
  configuration removed, against the runtime's own checkout by name, so a
  `GIT_DIR` or `GIT_WORK_TREE` pointing at your repository cannot redirect it
  there.
- **Genuine build information.** No version is injected. Each binary carries
  Go's own record of the commit it was built from; the action refuses a binary
  whose record is not exactly the pinned commit, unmodified. The result
  document's `cli_version` and `control_plane_version` are what those binaries
  report.
- **Cost.** The first invocation in a job downloads Go and builds three
  binaries — about a minute on a laptop, longer on a hosted runner. Further
  invocations in the same job reuse the toolchain, source and build cache.
  Nothing is cached across jobs yet.
- **Linux and macOS runners only.** `trustvian dev` does not run on Windows.

Changing the runtime is a pull request to `runtime.env`, which the action
version you pin then carries. There is no floating `latest`.

### The control plane

With no `api-url`, the action starts `trustvian-local` on a loopback port with
a state directory under `$RUNNER_TEMP`, waits for it to publish its endpoint,
and stops it — that process, by the PID it recorded — when the CLI is done:
SIGTERM, then SIGKILL after 10s. The Collector each repetition starts belongs to
`trustvian dev`, which stops it. The action stops no other process.

**Cancellation.** Each step execs its script, so a cancellation signal reaches
the script itself. While the CLI runs, SIGINT and SIGTERM are forwarded to the
CLI — which stops its workload, as a suite does on Ctrl-C — and the step waits
for it, stops the control plane it started, and exits `130` or `143`. A
signal that arrives while that control plane is stopping — even after the CLI
finished on its own — lets the stop run to completion, and the step then exits
with the first signal's status. A cancelled run reports no exit code. An
attached control plane is never signalled.

A cancellation shortens the control plane's 10s grace: it is killed no later
than 5s after the first signal. GitHub's runner sends a cancelled step SIGINT,
then SIGTERM 7.5s later, then kills the step's process tree 2.5s after that; a
step killed there could leave its control plane running. Stopping by 5s
leaves the rest of that 10s window for the kill, the reap and the exit. Every
deadline is measured as elapsed time, and a later signal never extends one.

`working-directory` is a literal path: a relative one is resolved against the
job's directory, never through `CDPATH`, and a name such as `-P` is a
directory, not an option.

That control plane is new for every invocation, so `--reference last` finds
nothing in it and exits `3`. To reuse recorded references across runs, give
`api-url` a control plane that persists — and remember the run job then talks
to it from a job that runs untrusted code.

## What this does not protect against

The run job executes the pull request's code. Every test job does; the action
does not pretend to sandbox it. What it guarantees is narrower: the job that
runs the workload holds no write-scoped token and no secret, on any pull
request, fork or not.

It follows that **the run job's outputs are only as trustworthy as the pull
request.** The workload runs as the same user as the action and could, in
principle, tamper with the runtime under `$RUNNER_TEMP` or with the files the
action writes. That cannot escalate — there is nothing to steal — but it means
the artifact is untrusted input. Any later job that reads it must parse it
strictly and execute nothing from it.

## Rendering the artifact

`cmd/trustvian-ci-render` turns a downloaded `trustvian-run` artifact into
Markdown for a pull request comment or a job summary — the same Markdown for
both ([ADR 0057](adr/0057-the-ci-renderer-is-a-standalone-offline-transcriber.md)).

- It works **offline**.
- It needs **no credential**.
- It **executes nothing** from the artifact.
- It is built from the Go standard library alone.

It posts nothing; that is the next slice.

Build it from a trusted, commit-pinned checkout of `trustvian/trustvian` in a
path of its own — the commit you reviewed, never the pull request's:

```yaml
- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  with:
    repository: trustvian/trustvian
    ref: REPLACE_WITH_A_REVIEWED_TRUSTVIAN_COMMIT_SHA # the full 40-character commit
    path: trustvian-renderer
    persist-credentials: false

- name: Build the renderer
  working-directory: trustvian-renderer
  run: go build -o "$RUNNER_TEMP/trustvian-ci-render" ./cmd/trustvian-ci-render

- name: Render
  env:
    HEAD_SHA: ${{ github.event.pull_request.head.sha }}
    RUN_EXIT_CODE: ${{ needs.run.outputs.exit-code }}
  run: |
    "$RUNNER_TEMP/trustvian-ci-render" \
      --artifact-dir "$RUNNER_TEMP/artifact" \
      --head-sha "$HEAD_SHA" \
      --repository "$GITHUB_REPOSITORY" \
      --run-id "$GITHUB_RUN_ID" \
      --run-attempt "$GITHUB_RUN_ATTEMPT" \
      --exit-code "$RUN_EXIT_CODE" \
      > "$RUNNER_TEMP/comment.md"
```

The write-enabled job that will post this never checks out, builds or runs
anything from the pull request: its only inputs from the pull request are the
artifact, read as data, and the event's head commit.

**Every identity comes from the caller, never from the artifact.** Each flag
but `--server-url` is required:

- `--head-sha` is the pull request's head commit, from the event.
- `--repository`, `--run-id` and `--run-attempt` name this run.
- `--exit-code` is the run job's `exit-code` output. An empty value means the
  CLI did not run.
- `--server-url` defaults to `https://github.com` and must be a bare
  `https://` host.

The artifact must agree with every one of these values. The run link is built
from them alone.

| Exit | Meaning | stdout |
|---|---|---|
| `0` | Evidence rendered: a scenario verdict or a suite report | The rendering |
| `1` | No verdict: the CLI did not run, exited `2` or `3`, or preserved no result | The no-verdict state |
| `2` | The flags are missing or malformed | Nothing |
| `3` | No verdict: the artifact failed validation | The no-verdict state |

On `1` and `3`, the reason is also written to stderr, so a broken artifact is
never silently a "no verdict".

### What it checks

The artifact was produced by a job that ran the pull request's code, so it is
read as untrusted data:

- **Fixed names only.** Only `trustvian-run.json` and `result.json` are opened,
  and each must be a regular file — never a symbolic link. `result.file` is
  compared, never followed.
- **Bounded reads.** 64 KiB for the metadata, and 32 MiB + 64 KiB for the
  result — what the action preserves.
- **Consistency, not authenticity.**
  - The metadata's head commit, repository, run id, attempt and exit code must
    equal the caller's.
  - Its size and SHA-256 must match `result.json`. A matching digest proves the
    two files agree, not that either is authentic.
- **Strict JSON.**
  - Invalid UTF-8, duplicate keys at any depth, trailing data and deep nesting
    are refused.
  - Absent, `null`, `0` and `false` are distinct: a required field is never
    filled in.
- **Closed values.** These must be defined values:
  - versions (`"1"`);
  - verdicts and classifications;
  - the six checks, in their stable order;
  - rules and advisories;
  - member outcomes and skip reasons;
  - reference modes;
  - metadata modes and statuses.

  Counts and limits must be canonical decimal strings.
- **Coherent shapes.**
  - A PASS is exit `0` and a FAIL exit `1`.
  - A suite's `exit_code` is the CLI's.
  - Each suite member carries exactly its outcome's fields.
  - An embedded result is that member's own scenario, execution and verdict.

**Unknown fields are tolerated and never rendered.** Both files may gain fields
within version `"1"` ([compatibility](compatibility.md#github-action)), so the
renderer reads only the fields it lists and validates them whatever else is
present. A value outside a closed vocabulary is refused rather than skipped, so
a reviewer sees "no verdict" rather than a rendering that silently dropped what
it did not recognize.

### What it renders

**A verdict**, for one scenario. Every value below is transcribed from the
document:

- the stored PASS or FAIL;
- the scenario and its N;
- the execution, and with `--reference`, the reused execution and mode;
- the `k` and `j` thresholds, as the added and removed rules;
- every behavior, with its descriptor, its fingerprint, its `k/N` on both
  sides and its stored classification — added, removed, or unclassified;
- all six gate checks, passing ones included, with actual, rule, bound,
  outcome and the `fresh scope` advisory;
- the limits;
- both producer versions.

**A report**, for a suite:

- the recorded summary;
- one row per member, with its outcome, exit code, execution, error or skip
  reason;
- each PASS or FAIL member's own verdict.

A suite has no verdict of its own, and none is composed.

**No verdict**, for every other case:

- the run job reported no exit code;
- the CLI exited `2` or `3` with no document;
- the CLI exited outside `0`–`3`;
- the artifact is missing;
- the result was `oversized` or `invalid`;
- a suite document is incomplete;
- validation failed;
- the evidence cannot fit.

This state carries:

- the head commit;
- the run link;
- a fixed reason.

It carries no count, check, zero placeholder or partial data, and nothing from
an earlier rendering.

**The renderer computes nothing.** No count, classification, threshold, check
outcome, percentage or suite verdict is derived. A document whose stored
classification contradicts its own counts is printed as stored.

**A rendering is only as authentic as the artifact.** The workload that
produced the artifact could have rewritten it, digest included. The renderer
refuses incoherent shapes, and ties a single scenario's verdict to the run
job's exit code. It does not recount a suite against its summary or exit code,
because that would mean computing them. The run job's exit code, printed at
the top of every rendering, is the gate.

### Every string is inert

Every value from the artifact is one code span on one line, so GitHub renders
no link, image, HTML, emphasis, `@mention` or `#issue` reference from it:

- **Control characters.** Control, format and line-separator characters —
  newlines and bidirectional overrides included — become `�`.
- **Backticks.** The span's fence is longer than any run of backticks in the
  value.
- **Pipes.** `|` is shown as `｜`, so no value can add a table cell.
- **Length.** Each value is capped at 256 bytes on a UTF-8 boundary, before
  the pipe substitution, and marked *(truncated)*. A value can therefore
  render slightly larger; the body cap below bounds the output.

The body is capped at 60,000 bytes, below GitHub's comment limit:

- When it would be longer, unclassified behaviors are left out, with a visible
  note. Every added and removed behavior and every check stays.
- When even that does not fit, the rendering is no verdict.

## What comes next

The next slice posts the rendering on the pull request. It will be a
**separate job** in the same `pull_request` workflow:

- `needs: run`, with `if: ${{ !cancelled() }}`, so a FAIL and an operational
  error are both reported and a cancellation is not;
- holding the one write scope the comment needs, and nothing else;
- **never checking out, and never executing, pull request code** — it
  downloads the artifact and renders it;
- **running only trusted, commit-pinned renderer and comment code** — the
  renderer built from a reviewed commit of this repository, never a local
  action, script or program from the pull request's checkout. The artifact is
  data only;
- with the run job's exit code remaining the check.

Marker ownership, replacing a stale comment with the no-verdict state,
permission and fork degradation, and the comment API's scope are that slice's
work.

It will not be a step added to the run job — that would put a write-scoped
token next to the workload — and it will not be a `pull_request_target` or
`workflow_run` workflow. Task 079 records the reasoning.

## Related

- [Task 079](tasks/v1.0/079-ci-integration-github-action.md) — the
  specification, and what remains
- [Platform CLI § `trustvian eval run`](platform-cli.md#behavioral-scenarios-trustvian-eval-run)
- [ADR 0056](adr/0056-the-run-action-builds-a-pinned-source-commit.md) —
  packaging and runtime acquisition
- [ADR 0057](adr/0057-the-ci-renderer-is-a-standalone-offline-transcriber.md) —
  the renderer
- [Compatibility](compatibility.md#github-action) — what the action promises
