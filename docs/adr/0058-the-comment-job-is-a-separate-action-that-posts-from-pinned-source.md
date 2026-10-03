# 0058 — The comment job is a separate action that posts from pinned source

**Status:** Accepted. The poster shipped first (#140), and the action that runs
it followed, pinned to the poster's merge commit (§ 6).

## Context

[Task 079](../tasks/v1.0/079-ci-integration-github-action.md) needs the
behavioral gate's result on the pull request. Two slices have shipped:
- the run action, which runs the pull request's code and preserves the result
  ([ADR 0056](0056-the-run-action-builds-a-pinned-source-commit.md));
- the renderer, which turns that artifact into inert Markdown or an explicit
  no-verdict state, offline
  ([ADR 0057](0057-the-ci-renderer-is-a-standalone-offline-transcriber.md)).

What remains is the job that holds `pull-requests: write` and posts. It is the
one place in the whole integration that holds a write-scoped token, so its
shape is the security property. The task settles that it is a separate job of
the same `pull_request` workflow that never runs pull request code. It leaves
open how that job is packaged, how the comment is found again, and what it
does when the token cannot write.

## Decision

### 1. A separate composite action, not a mode of the run action

The comment job uses `.github/actions/trustvian-comment`, an action of its own.
With a `mode` input on the run action, one reference would sometimes run the
workload and sometimes hold the write token, and keeping those apart would be
a property of an input's value. Two actions make it a property of which action
a job names. The structural tests then assert that the job naming the comment
action:
- holds `pull-requests: write`, and no `contents` scope;
- checks nothing out;
- uses no `./` action;
- reads nothing from the workspace.

The comment action refuses `pull_request_target` and `workflow_run`, exactly as
the run action does, through the same `refuse_privileged_event`.

### 2. Everything it runs is built from the pinned source

The action builds `cmd/trustvian-ci-render` and `cmd/trustvian-ci-comment` from
the commit pinned in `runtime.env`. It uses the run action's machinery:
- the pinned Go toolchain, checked against go.dev's digest;
- `fetch_source`, which fetches the pinned commit into its own directory under
  `$RUNNER_TEMP`;
- `source_is_pinned`, which refuses a checkout that differs from the pin;
- the `vcs.revision` check on every binary.

**It never builds or reads anything from `$GITHUB_WORKSPACE`, and a source scan
asserts it.** The comment job has no checkout. If a caller adds one anyway,
nothing in it is built or run.

### 3. The run's identity comes from the event, not the artifact

- **Head commit and pull request number** come from the event, through
  `resolve_head`.
- **Repository, run id and attempt** come from the runner's environment.
- **The run job's exit code** is the action's one required input; an empty
  value means the CLI did not run.
- **The artifact** comes from `actions/download-artifact` in the same run, which
  needs no token scope. The run-side end-to-end workflow established that.

The renderer then requires the artifact to agree with all of them (ADR 0057).

### 4. The poster: one comment per marker, found by ownership

`cmd/trustvian-ci-comment` is standard library only.
- It may use `net/http`, and imports no `os/exec`, `plugin`, `reflect` or
  `unsafe`; a test asserts this the way ADR 0057's does.
- The token is read from `GITHUB_TOKEN` and never from the command line.
- The API base comes from `GITHUB_API_URL`, and must be a bare `https://` host.
  That covers github.com and GHE.com. GitHub Enterprise Server's
  `https://HOST/api/v3` is refused: it is not supported in this slice.
- **No redirect is followed.** A 3xx is a failure, so the token is never sent
  anywhere but the validated host. Go keeps `Authorization` on a redirect to
  the same hostname, including one that downgrades to `http`.

**The body** is the marker line `<!-- trustvian-behavioral-gate:<id> -->`, a
newline, then the rendering. `<id>` is the run artifact's name, which the
action passes by default, so there is one comment per scenario or suite per
pull request.

**Ownership.**
- The poster lists the pull request's issue comments, 100 per page and at most
  30 pages.
- A comment is its own only if `user.login` is `github-actions[bot]` **and** the
  body's first line is exactly the marker.
- A human comment carrying the marker, a marker quoted in a code span, and a
  marker on a later line are someone else's and are never edited. A user
  login cannot contain brackets, so no person can hold that login.
- The poster updates the newest comment it owns, creates one if none exists,
  and warns when it finds more than one.

**The superseded guard.** Just before writing — after the listing, to keep the
window short — the poster reads the pull request. If its head is no longer the
event's head commit, a newer run owns the comment. This run then writes nothing
and emits a notice, rather than replacing a newer verdict with an older one. A
head that is not a commit SHA is an API failure, never a reason to write
nothing.

**Searching is bounded at 3,000 comments.** Comments are listed oldest first,
so on a pull request with more, a gate comment beyond the bound is not found.
A new one is created, and a warning says the search stopped.

**Failures.**

| GitHub answers | The poster | Exit |
|---|---|---|
| 403 to any request, or 404 to a write — a fork, or a read-only token | a `::warning::` and a job-summary note; nothing written | `0` |
| 404 to a read | a `::error::`: the repository or number is wrong, since even a fork's read-only token can read the pull request | `1` |
| 404 to an edit | the comment was deleted since it was listed, so the rendering is posted as a new one | `0` |
| 429, 5xx, a rate-limiting 403, or a transport error | one retry, after `Retry-After` capped at 10s, otherwise 2s; then a `::error::` and a job-summary note | `1` |
| any other non-2xx status, a redirect included | a `::error::` and a job-summary note | `1` |

A 403 that carries `Retry-After` or `X-RateLimit-Remaining: 0` is GitHub's rate
limit, not a refusal, so it is retried rather than degraded.

**Creating is not retried blindly.** A GET or a PATCH is idempotent and is
simply sent again. A POST that failed may still have landed — GitHub can answer
502 to a write it committed — so before its one retry the poster lists the
comments afresh, and edits the comment that landed instead of creating a
second one.

Duplicates can still arise from outside this command, for example from two
workflows racing on one marker. The poster then updates the newest and warns:
an older duplicate keeps its old text, and the warning is how a maintainer
learns to remove it.

No response body is ever echoed, so nothing GitHub — or anything answering in
its place — returns reaches the job log.

### 5. Every rendering is posted, and the summary always carries it

The renderer's exits `0` (evidence), `1` (no verdict) and `3` (artifact
rejected) are all posted. A no-verdict body therefore replaces a stale verdict,
which is the failure mode task 079 names the worst. Exit `2` is the action's
own malformed context: it posts nothing and fails the job. The same rendering
is written to `$GITHUB_STEP_SUMMARY` in every case, so the fork path still
shows it.

A comment-job failure never changes the gate. The verdict is the run job's
exit code, in another job.

### 6. Two pull requests, because a pin can only name merged source

§ 2 builds the poster from the commit `runtime.env` pins, and that commit has to
contain the poster. So the poster lands first, with its fake-API test suite and
no workflow change. The action and its pin follow, naming the poster's merge
commit. Any other order would build the poster from somewhere other than the
pin, such as the action's own checkout, which for this repository's self-test
is the pull request's workspace.

### 7. This repository's self-test uses the local action, and examples never do

`.github/workflows/trustvian-run-action.yml` will run the comment action as
`./.github/actions/trustvian-comment` on this repository's own pull requests.
It then reads the comment back and asserts exactly one marker comment for the
head commit. On a fork pull request it asserts the warning path instead.

Running a local action in a write-scoped job is acceptable **here, and only
here**:
- **On a fork pull request**, GitHub issues a read-only token, so the job
  cannot write whatever it runs.
- **On a same-repository pull request**, the author already has write access to
  this repository and can change any workflow directly.

Neither case grants anyone a capability they lacked. In a consumer's
repository, the pull request's author is exactly who must not reach the write
token. So a shipped example names the action by a reviewed commit SHA, and
never by `./`.

## Alternatives considered

- **A `mode: comment` input on the run action.** Rejected (§ 1). The privilege
  difference would rest on an input's value.
- **Posting with `curl` and `jq` in the action's shell.** Rejected. That is
  harder to test against a fake API, and cannot be held to the same import
  guarantees.
- **Finding the comment by marker alone.** Rejected. Anyone can write the
  marker into a comment, and the poster must never edit a human's.
- **Skipping the superseded check.** Rejected. Two runs racing on one pull
  request could leave the older commit's verdict standing on the newer head.
  Workflow concurrency narrows the race but cannot close it, because a
  cancelled run's comment job may still be writing.
- **Failing the comment job on a fork.** Rejected. Task 079 asks for the fork
  path to degrade loudly but successfully: a red comment job on every fork
  pull request would teach reviewers to ignore it.

## Consequences

- The poster is the only Trustvian code that writes to GitHub. It writes only
  issue comments, only on the pull request it is given, and edits only its own.
- `trustvian-ci-comment`'s flags, exit codes and marker format are
  OPERATIONALLY STABLE. Changing the marker would orphan every existing gate
  comment. The comment body is OBSERVATIONAL.
- The action reports what the poster did as an `outcome` output, read from the
  lines the pinned poster writes. A test holds those lines to the poster's
  source, so renaming a message cannot silently empty the output.
- The run action's toolchain, source and verification steps moved into its
  `lib.sh`, which the comment action sources. One implementation now serves
  both, and so does every isolation test of it.
