# 079 — CI Integration: A GitHub Action

Status: specified; not implemented
Milestone: `v0.10.0` — the
[developer preview](../../ROADMAP.md#v0100--developer-preview)
Depends on: [078](078-behavioral-scenario-suites.md)
Blocks: nothing. It is **not** a `v1.0` exit criterion — see
[Relationship to the release gate](#relationship-to-the-release-gate)

## Objective

Make the behavioral gate visible where the change is reviewed.

```text
pull request
    ↓
run the scenario                    ← task 078's command, unchanged
    ↓
exit code decides the job           ← 0 / 1 / 2 / 3, passed through
    ↓
render the result on the PR         ← this task: a comment, from JSON only
```

Nothing about the verdict is decided here. The action runs a command, reports
what that command returned, and exits with the code it was given.

## Why

**A gate nobody reads is a gate nobody acts on.** Task 078 produces a
machine-readable result and an exit code a CI job can branch on, which is
enough to *fail* a build and not enough to *explain* one. A reviewer who sees a
red check has to open the job, find the step, scroll the log, and reconstruct
which behavior appeared in how many runs. Most will re-run the job instead.

**The finding is short and belongs in the conversation.** "This candidate gained
`export_customer` in 5 of 5 runs, and your limit is 0" is two lines. It is also
exactly the kind of thing that changes a review, and exactly the kind of thing
that is lost in a log.

**`v0.10.0`'s exit criterion says "usable in CI", and means it.** The
[developer preview](../../ROADMAP.md#v0100--developer-preview) requires the
scenario step to be usable from CI, with the result legible on the pull request
rather than in a log a reviewer has to open. This task is that last clause.

**And the wrapper is where CI integrations get security wrong.** A behavioral
gate has to run the workload — the pull request's own code — and then write to
the pull request. Those two things in one privileged job is the standard
GitHub Actions privilege-escalation shape, and it is the reason this task has a
token section before it has a rendering section.

## Scope

- A **GitHub Action** that invokes task 078's scenario command in a workflow.
- **Exit-code passthrough**: `0`, `1`, `2`, `3` reach the job unchanged.
- A **pull request comment**, rendered from the machine-readable result only.
- A **two-job workflow shape** that separates running the workload from holding
  a token that can write to the pull request — see
  [the job split](#the-job-split-running-the-workload-never-holds-the-write-token).
- A documented workflow example with the minimum permissions **per job**, and a
  documented fork-pull-request path that does not escalate.

## Non-goals

**No evaluation logic of any kind.** The action computes no diff, no scorecard,
no gate, no presence count, no repeated verdict, and no summary of its own. It
is an adapter in exactly the sense
[ADR 0023](../../adr/0023-interfaces-are-adapters.md) and
[ADR 0033](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) use, one
layer further out than the CLI: the CLI is a thin HTTP adapter over the control
plane, and this is a thin workflow adapter over the CLI.

**No new exit-code scheme.** Task 078's contract is inherited whole. The action
does not invent a code, collapse two codes into one, or map a code to a
different meaning for GitHub's benefit.

**No re-derivation for display.** If a number is not in the result document, the
comment does not contain it. The action does not recompute a total, a
percentage, a severity or a trend to make the comment read better.

**No second configuration surface.** A scenario file is the configuration. The
action's inputs name *which scenario* and *where the runtime is*, not what the
limits are. Gate limits in workflow YAML would be a second place policy lives,
and the two would disagree.

**No content.** Nothing the action reads or writes contains a prompt,
completion, tool argument, tool result or body — task 078's result is already
metadata-only, and the comment is a projection of it.

**No GitHub-specific behavior in the CLI.** Whatever the action needs, it gets
from the existing machine-readable output. A `--github` flag, a
`::error::` annotation writer, or a `GITHUB_*` environment variable read from
inside `trustvian` would put one CI provider into the product.

**No status check, review, or merge action.** The action comments and exits. It
does not create a check run of its own, submit a review, approve, request
changes, label, or merge. The job's own conclusion is the signal, and a gate
that could approve its own pull request is a governance problem rather than a
feature.

## What it does

```yaml
# .github/workflows/behavior.yml — illustrative
name: Behavioral gate

on:
  pull_request:
    branches: [main]

# No workflow-level grant. Each job states its own, so the job that runs
# the workload cannot inherit one it does not need.

jobs:
  run:
    runs-on: ubuntu-latest
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@<pinned sha>
        with:
          persist-credentials: false

      # Runs the scenario, uploads the result document, exits with the
      # scenario's own code — so this job is the gate.
      - uses: trustvian/<action>@<pinned sha>
        with:
          scenario: scenarios/support-login.yaml

  comment:
    runs-on: ubuntu-latest
    needs: run
    if: always()
    permissions:
      pull-requests: write
    steps:
      # Downloads the artifact and renders it. No checkout, so no
      # pull request code exists in this job to execute.
      - uses: trustvian/<action>@<pinned sha>
        with:
          mode: comment
```

Two jobs, and the split is the security property rather than a structural
preference — see
[the job split](#the-job-split-running-the-workload-never-holds-the-write-token).
The `run` job is the gate: its exit code is the check a reviewer sees. The
`comment` job renders and can fail without changing that.

The exact input names, whether this ships as one action with two modes, and the
action's location are implementation decisions — see
[Open questions](#open-questions-left-to-implementation).

## Exit codes are passed through, not interpreted

```text
0   gate PASS          → the step succeeds
1   gate FAIL          → the step fails
2   usage              → the step fails
3   operational        → the step fails
```

Three rules, each with a failure it prevents:

**The action never translates a code.** In particular `3` never becomes `1`.
[ADR 0033 § 10](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) is the
reasoning and it applies with more force here, because a workflow is exactly
where "non-zero means the gate failed" gets written down. An action that mapped
an unreachable control plane onto a gate failure would report a policy violation
that did not happen — and a team that meets that twice learns to ignore the one
code that means their agent regressed.

**The action never suppresses a code.** No `continue-on-error` baked in, no
`|| true`, no "warn only" mode that reports success. A caller who wants the job
to survive a FAIL sets `continue-on-error` themselves, in their own workflow,
where it is visible in review. An action that decided that for them would turn
a gate off in a place nobody looks.

**A failure to comment never changes the code.** If the API call that posts the
comment fails — revoked permission, rate limit, fork restriction — the step
still exits with the code the scenario produced. The comment is a rendering of
the verdict; losing the rendering must not lose the verdict, and it must not
invent one either.

The last rule has a corollary worth stating: a comment failure is reported as a
warning in the job log and in the job summary, never silently swallowed. "The
gate passed and I could not tell you why" is information.

## The comment is rendered, never computed

```text
Trustvian behavioral gate — FAIL

  support-login   runs 5

  Behavior                  reference   candidate
    crm_lookup                    5/5         5/5
    knowledge_search              5/5         5/5
    send_email                    5/5         5/5
    export_customer               0/5         5/5   + added

  Added when present in >= 4 of 5 candidate runs and <= 0 of 5 reference runs

  Gate
    repeatedly added behaviors     1 / max 0        FAIL
    worst block decisions per run  0 / max 0        pass
    worst critical risk per run    0 / max 0        pass
    reference repetitions          5 / 5            pass
    candidate repetitions          5 / 5            pass
    repetitions lacking evidence   0 / max 0        pass

  trustvian <version> · control plane <version>
```

Every number above is a field of task 078's
[result document](078-behavioral-scenario-suites.md#result-document). The
action's whole job is transcription and layout.

Five properties this has to hold:

**The `k` and `j` thresholds appear.** A reader shown `0/5 → 5/5   + added`
cannot check the label without the rule that produced it, and task 078's own
output carries the thresholds for exactly that reason. They are fields of the
document, so showing them is transcription and not an inference.

**Added and removed behaviors both appear, with their `k/N` counts on both
sides — carrying the label task 078 assigned.** A removed behavior is a finding
too: a candidate that stopped calling `audit_log` is as interesting as one that
started calling `export_customer`. Task 078 defines
[repeatedly removed](078-behavioral-scenario-suites.md#repeatedly-removed-is-a-classification-not-a-gate)
as the mirrored rule and classifies it in the control plane, so the action
renders a classification rather than deriving one — and it renders every
behavior's counts whether or not either label applies. An action that decided
for itself what "removed" means at `N > 1` would be the second implementation
this whole specification exists to avoid.

**Every check appears, passing ones included.** Task 056 evaluates every gate on
every call with no short-circuit, precisely so an auditor sees everything
measured rather than everything up to the first problem. A comment that showed
only failures would undo that property at the last step, and would leave a
reviewer unable to tell a check that passed from a check that did not run.

**The verdict and its reasons come from the document.** The action does not
decide that one added behavior is why the gate failed; the result says which
checks failed and the comment shows them.

**An uninterpretable result produces no comment.** If the document is missing,
is not valid JSON, or carries a verdict outside the closed vocabulary, the
action says so and posts nothing — the same classification-before-output rule
[ADR 0033 § 9](../../adr/0033-developer-cli-is-a-thin-http-adapter.md)
established, for the same reason: published CI evidence must not be retracted
after the fact, and a pull request comment is the least retractable output this
project has.

### One comment, updated

Re-running a workflow, or pushing to the same pull request, must not accumulate
comments. The action maintains **one** comment per scenario per pull request,
identified by a stable marker it writes into the body, and edits it in place.

A reviewer reading a pull request with nine stale gate comments cannot tell
which one is current, which is a worse outcome than no comment. The marker is
also what makes the behavior testable rather than incidental.

### Consumed fields

The action reads task 078's
[result document](078-behavioral-scenario-suites.md#result-document) and nothing
else. That section is the contract; this one records what consuming it means.

```text
scenario name · N · k · j          the header and the threshold line
per behavior: identity, both
  counts, and 078's classification  the behavior table
the six checks, with actual,
  limit or rule, and outcome        the gate block, all six
verdict                             the headline
producer versions                   the footer
```

Two consequences:

**A missing required field is uninterpretable.** Not a blank cell, not a dash,
not a best-effort comment with a gap in it — no comment at all, and a reported
reason. The action has no way to distinguish "the field is absent because the
producer is older" from "the field is absent because something truncated the
document", and a comment that silently omits a failing check is worse than
silence.

**The version in the footer comes from the document, not from a separate
call.** Asking the binary its version, or reading an action input, would report
what is installed rather than what produced this result — and those differ
exactly when it matters, such as a cached binary or a retried job. The document
knows what wrote it.

### Every rendered string is untrusted

This is the subtlety in *rendered, never computed*: rendering faithfully is not
the same as rendering raw.

Behavioral identities are descriptors built from telemetry — host names, service
names, tool names, operation names — and that telemetry is emitted by **the pull
request's own workload**. Scenario names come from a file in the pull request.
So an author controls, near enough exactly, the strings this action writes into
a comment on their own pull request:

```text
a tool named  [click here](http://evil.example)   becomes a link
a tool named  <img src=x onerror=...>             becomes HTML
a tool named  @org/security-team                  notifies a team
a tool named  Fixes #1234                         cross-references an issue
a tool named  | 0/5 | 5/5 | pass |                forges a table row
```

The last one is the interesting one. The others are nuisances; a forged table
row can make a FAIL look like a PASS to a reviewer skimming the comment, which
turns a rendering bug into a bypass of the thing the gate exists to report.

So every string sourced from the document is rendered **inert**:

| Requirement | Why |
|---|---|
| escaped, or wrapped in a code span with backticks in the value handled | markdown and HTML both stop being active |
| `@mentions` neutralized | a comment must not notify people the author chose |
| control characters and newlines stripped | a newline is how a forged table row or a fake section gets in |
| a per-string length cap, truncation marked | one 10 KB tool name must not push the gate block out of view |
| a total-size cap below GitHub's comment limit, with truncation stated visibly in the comment | a comment silently cut off at the API's limit can lose the verdict; a reader must be told the body was shortened |

**Escaping is layout, not computation.** It changes how a value is displayed,
never what the value is, and it derives no number — so it does not weaken the
*renders only* rule, it is part of rendering correctly. The same treatment
applies to the job summary, which is markdown rendered in the same way and is
the *fallback* path, so it is the one most likely to be forgotten.

## Token permissions and fork pull requests

This is the section the task exists to get right.

### The job split: running the workload never holds the write token

The obvious shape — one job that runs the scenario and then comments — is
wrong, and it is wrong on a **same-repository** pull request, where the token is
not read-only.

```text
one job, contents: read + pull-requests: write
    ↓
actions/checkout        persist-credentials defaults to true, so the token
                        is written into .git/config
    ↓
the scenario runs       the pull request's own code, and its whole
                        dependency tree
    ↓
that code can read a token that can write to the pull request
```

Nothing exotic is required to exploit it. A compromised transitive dependency in
the workload reads the credential out of the local git config, or out of the
environment, and it now holds write scope in the base repository. The pull
request that introduced it looks like a dependency bump.

**So the two capabilities are placed in two jobs of the same `pull_request`
workflow**, and neither job holds both:

| | `run` job | `comment` job |
|---|---|---|
| Grants | `contents: read` | `pull-requests: write` |
| Checks out PR code | yes, `persist-credentials: false` | **never** |
| Executes PR code | yes — this is the scenario | **never** |
| Produces | the result document, as an artifact | the comment |
| Exit code | the scenario's, unchanged — **this job is the gate** | cannot change the gate |

Four properties make that split real rather than cosmetic:

**Permissions are scoped per job, never at workflow level.** GitHub's
documentation is explicit that a job-level `permissions` key overrides a
workflow-level one, and that once any scope is named every unnamed scope becomes
`none`. A workflow-level grant would hand `pull-requests: write` to the job
running the workload, which is the whole thing being avoided — so the example
declares no workflow-level `permissions` block at all.

**`persist-credentials: false` on the checkout.** `actions/checkout` defaults
`persist-credentials` to `true` and writes the token into the local git config
for later git commands to use. The scenario needs the source, not the ability to
push it, and the default is the reason this has to be stated rather than
assumed.

**The comment job never checks out.** Not with `persist-credentials: false`,
not sparsely, not the base ref. It downloads an artifact and renders it; there
is no pull request code in that job for a compromised dependency to live in,
because there is no pull request code in it at all.

**The gate is the run job.** The comment job is `needs: run` with `if:
always()`, so a FAIL still gets commented — but the check a reviewer sees is
the run job's, and a comment job that fails, is skipped, or is never granted its
permission cannot turn a FAIL into a pass. This is the same rule as
[a comment failure never changing the exit code](#exit-codes-are-passed-through-not-interpreted),
enforced structurally by putting the verdict in a different job from the
rendering.

### Why this is not the rejected `workflow_run` handoff

It resembles it — an artifact crosses from the job that ran untrusted code to
the job that comments — so the difference has to be stated rather than left to
the reader.

```text
workflow_run handoff        a SECOND workflow, triggered after the first,
                            running in the BASE repository's context with a
                            write-scoped token and access to secrets, even
                            for a fork pull request

this job split              ONE workflow, the same pull_request event, the
                            same token GitHub already decided to issue for
                            that event
```

Three differences, each load-bearing:

- **Same event, one workflow.** There is no second trigger and no privileged
  re-entry. Both jobs run under the `pull_request` event the contributor's
  push already caused.
- **No base-repository privilege is acquired.** `workflow_run` escalates: it
  gets a write token *because* it runs in the base context. Nothing here
  escalates — the `comment` job asks for a scope of the token this event
  already carries.
- **On a fork pull request the comment job's token is still read-only.**
  GitHub issues a read-only `GITHUB_TOKEN` for a fork `pull_request` event, and
  declaring `pull-requests: write` in a job does not manufacture write access
  it was never granted. The fork path below is therefore the same whether the
  work is in one job or two.

That last point is the one that matters most: the split solves the
*same-repository* exposure, and changes nothing about forks. Both problems are
real and they are not the same problem.

### The minimum, per job

```yaml
# run job
permissions:
  contents: read

# comment job
permissions:
  pull-requests: write
```

Nothing else in either. Not `write-all`, not `actions: write`, not `id-token`,
and not `contents: read` in the comment job — it checks nothing out.

Two things the implementation must confirm rather than inherit from this
paragraph:

- **Whether a same-run artifact download needs any `GITHUB_TOKEN` scope.**
  `actions/download-artifact` documents a token as required for a *different*
  workflow run or repository, and `actions: read` is the scope named there.
  Within one run it uses the run's own artifact service, so the expectation is
  **no `GITHUB_TOKEN` scope at all** — which is why `actions: read` is absent
  from the comment job above. Confirm it against the pinned action version, and
  if a scope turns out to be required, document it and say why.
- **Whether the chosen comment API needs `issues: write` instead of
  `pull-requests: write`.** The issue-comment endpoint on a pull request is
  reached through the issues API, and the two scopes are not interchangeable in
  every case. Whichever one is required is the one documented, and only that
  one.

Note what this repository's own CI does for comparison: `ci.yml` runs with
`contents: read` and nothing more, and says so in a comment — *"a fork pull
request therefore cannot obtain credentials, because the job has none to
obtain."* The `run` job matches that austerity exactly. The `comment` job is
the only place a write scope exists, and it is the only place that runs nothing.

### `pull_request_target` is refused

```text
✗  on: pull_request_target  +  actions/checkout with the PR head
```

This is the canonical GitHub Actions privilege escalation, and no workflow this
task ships or documents may be triggered by it.

**Stated precisely, because the check has to be mechanical.** What is refused is
the *trigger*, not the string:

```text
refused     any `on:` trigger — in the action, in a shipped example
            workflow, or in any YAML code block in its documentation —
            that names `pull_request_target`

required    prose explaining the refusal and its reason, wherever a
            reader copying a workflow will meet it
```

An earlier draft of this specification asked for both "the string appears
nowhere" and "the documentation explains the refusal", which cannot both hold: a
document that explains why `pull_request_target` is refused necessarily contains
the word. The refusal is about what a workflow is *triggered by*, so that is
what the scan checks, and the explanation is not collateral damage.

`pull_request_target` runs in the **base** repository's context: the workflow
file comes from the base branch, the `GITHUB_TOKEN` carries write scope, and
repository secrets are available. Checking out the pull request's head there and
then *running it* — which is the whole point of a behavioral scenario — executes
a contributor's code with a token that can write to the repository. The attack
is one line in the workload, and the workflow that enabled it looks reasonable.

The scenario runner makes this sharper than usual. Task 078 states plainly that
a scenario file is **executable configuration**: it names a command, and that
command is the pull request's own code. So the thing this action runs is
untrusted by construction on a fork pull request. There is no configuration of
`pull_request_target` that makes running untrusted code with a write token safe,
and a "we only check out the base" variant cannot run the candidate at all,
which is the entire job.

### What fork pull requests get instead

On a `pull_request` event from a fork, GitHub issues a **read-only**
`GITHUB_TOKEN` and withholds secrets. That is the correct posture, and the
action works within it rather than around it:

| Situation | `run` job | `comment` job |
|---|---|---|
| same-repository pull request | scenario runs, artifact uploaded, exit code stands | comment posted or updated |
| fork pull request, read-only token | unchanged — scenario runs, artifact uploaded, exit code stands | **comment skipped with a warning**; result written to the job summary |
| the comment job is not run at all | unchanged | nothing; the gate is unaffected |
| token lacks the permission for another reason | unchanged | same as the fork path: warn, summarize |

The degradation is **loud and downward**: the gate still runs, the verdict still
decides the check, and the only thing lost is the rendering. The job summary
(`GITHUB_STEP_SUMMARY`) needs no token at all, which is why it is the fallback
rather than an alternative rendering.

Note that the `run` job column is identical on every row. That is the point of
the split: fork or not, the job that executes the workload behaves the same way
and holds the same read-only scope, so a fork pull request is not a different
security posture — it is the same posture with the rendering unavailable.

**Two things this explicitly does not do.** It does not detect a fork and switch
event types. And it does not reach for the `workflow_run` pattern to comment on
fork pull requests — a second, privileged workflow that picks up the artifact
and posts with a base-context token. That pattern is a legitimate technique, and
it is also a privileged workflow consuming an artifact produced by untrusted
code, which needs its own threat model and its own review. It is named here as
deliberately out of scope rather than overlooked; if it is ever wanted, it is a
separate task with a separate ADR. It is **not** what
[the job split](#why-this-is-not-the-rejected-workflow_run-handoff) does, and
that section says why in detail, because the two look alike from a distance.

### The workload runs untrusted code, and the specification says so

Running a pull request's code in CI is ordinary — every test job does it. The
action does not pretend to sandbox it, exactly as task 078 does not pretend to
sandbox a scenario file. What the action guarantees is narrower and honest:

> The action never combines running the workload with holding a token that can
> write to the repository — on any pull request, fork or not.

The earlier form of that sentence ended at *"on a fork pull request"*, which was
too weak: forks get a read-only token from GitHub anyway, so the fork case was
never the exposure. The same-repository case was, and
[the job split](#the-job-split-running-the-workload-never-holds-the-write-token)
is what makes the stronger sentence true.

It is testable, and the test is structural rather than behavioral: the job that
executes the workload holds no write-scoped permission, and the job that holds
one executes nothing. "The workload is safe" is not testable, and is not
claimed.

## Architecture

```text
                      pull_request event
                              │
         ┌────────────────────┴────────────────────┐
         │ run job                                 │ comment job
         │   contents: read                        │   pull-requests: write
         │   checkout, persist-credentials: false  │   no checkout
         │        ↓                                │        ↓
         │   action (run mode)                     │   action (comment mode)
         │        ↓                                │        ↑
         │   trustvian eval  ← task 078's command  │        │
         │        ↓                                │        │
         │   control plane   ← diff · scorecard ·  │        │
         │                     gate · repeated     │        │
         │                     gate, server-owned  │        │
         │        ↓                                │        │
         │   result document ──── artifact ────────┼────────┘
         │        ↓                                │
         │   exit code = the gate                  │   renders · comments
         └─────────────────────────────────────────┴─────────────────────────
```

Three layers of adapter, and the authority is at the bottom of the stack in all
three. The action's source contains no threshold, no comparison against a limit,
and no verdict composition — asserted by the same structural scan style task 078
applies to its runner and task 060 applies to the CLI.

**The artifact is a trust boundary, not just a transport.** The comment job
receives a document produced by a job that executed the pull request's code, so
it parses it strictly rather than trustingly: a size bound before parsing, a
schema-shaped decode rather than free-form traversal, the closed verdict
vocabulary, and no execution of anything the document contains. An
uninterpretable artifact produces no comment, exactly as a missing one does. The
document is data in both directions — this is the same posture task 078 takes
toward `DecisionRecord` at its own ingest boundary.

The action needs a `trustvian` binary. How it gets one — a released artifact, a
container image, a build from the checkout, or a separate setup action — is an
implementation decision, with one constraint: **the version is explicit and
recorded in the comment.** A gate result whose producer is unknown is not
evidence, and a floating `latest` would silently change a team's gate.

## Compatibility

Additive. A new repository-level artifact, no change to the CLI, the platform,
or any `/v1` route.

Two surfaces this creates, which the implementation must classify in
[the compatibility contract](../../compatibility.md):

- **The action's input names** are automation-facing the moment someone pins the
  action in a workflow. They belong in the matrix, at the same class as CLI
  flags — OPERATIONALLY STABLE, new inputs allowed, removal or repurposing a
  breaking change.
- **The comment body** is not. It is rendering, and it belongs where CLI
  human-readable output already sits: OBSERVATIONAL, with no wording, spacing or
  ordering promise. Anyone parsing the comment is parsing the wrong thing; the
  machine-readable result is right there.

## Tests

Testing a GitHub Action means testing it without GitHub, plus one path that
cannot be faked.

- **Exit codes pass through**, one case per code: a scenario command exiting
  `0`, `1`, `2` and `3` produces a step exit of the same value. `3` stays `3` —
  the regression test for the whole exit-code section.
- **A comment failure does not change the exit code**: with the comment API
  refused, a `0` still exits `0` and a `1` still exits `1`.
- **A comment failure is reported**, not swallowed — asserted on the warning
  output.
- **The comment is rendered from the document alone.** Given a fixed result
  document, the body is deterministic; given a document with a field removed,
  the action reports an uninterpretable result rather than filling in a
  plausible value.
- **An invalid or empty document posts nothing** and produces no partial
  comment.
- **Added *and* removed behaviors appear with counts on both sides.**
- **Every gate check appears**, including passing ones, in the stable order the
  result lists them in.
- **One comment per scenario per pull request**: two consecutive runs edit one
  comment rather than creating two, asserted through the marker.
- **The action computes nothing** — a structural scan of its sources for a
  threshold comparison, a verdict composition, or an arithmetic operation over
  gate counts.
- **No workflow trigger names `pull_request_target`.** A scan of every `on:`
  block in the action, in every example workflow it ships, and in every YAML
  code block in its documentation. Prose mentioning it is expected and must not
  fail the scan — the check is on triggers, not on the string.
- **The job that runs the workload holds no write-scoped permission**, and **the
  job that holds one executes no pull request code** — asserted against the
  shipped example workflows by parsing their `permissions` and their steps. This
  pair is the structural form of the guarantee, and it is the test that fails if
  anyone collapses the two jobs back into one.
- **No shipped example grants workflow-level `permissions`**, since a
  workflow-level grant would reach the run job.
- **Every shipped checkout sets `persist-credentials: false`.**
- **The comment job's example declares no `contents` scope** — it checks nothing
  out, so it needs none.
- **The comment job treats the artifact as untrusted**: an oversized artifact is
  refused before parsing; a structurally invalid one produces no comment; a
  verdict outside the closed vocabulary produces no comment.
- **Rendered strings are inert.** A result document whose behavior names contain
  a markdown link, `@org/team`, raw HTML, backticks, a newline, and a
  10,000-character string renders inert in **both** the comment and the job
  summary: no link, no mention, no HTML, no broken code span, no extra table row
  or line, and the oversized name truncated with the truncation marked.
- **A forged table row cannot alter the gate block.** The specific regression
  test for the case that matters: a behavior name containing pipe characters and
  a newline does not produce a row that reads as a passing check.
- **Total size is capped below the comment limit**, and a body that had to be
  shortened says so visibly.
- **A missing required document field produces no comment** and names the field,
  rather than rendering a gap.
- **The version in the comment comes from the document**, asserted by rendering
  a document whose recorded version differs from the binary's.
- **Fork behavior** exercised against a read-only token: the run job is
  unaffected, the comment is skipped with a warning, the summary is written, the
  exit code stands.
- **A failed, skipped, or never-run comment job does not change the check** —
  asserted on the run job's conclusion.

The one path that cannot be faked is a real pull request comment against a real
API. The task should carry a minimal end-to-end job — this repository's own pull
requests are the natural place — rather than asserting the GitHub half from
mocks alone, in the same spirit that `internal/otel`'s tests build real spans
rather than hand-rolling a `ReadOnlySpan`.

## Relationship to the release gate

079 is in the [developer preview](../../ROADMAP.md#v0100--developer-preview) and
is **not** a `v1.0` exit criterion. Criterion 15 requires a behavioral scenario
to run repeatably and produce a gate result **usable in CI**, which task 078
satisfies on its own: the exit-code contract is what makes it scriptable.

This task makes that result *legible* rather than merely usable, which is a
preview goal — the preview's own exit criterion names it — and not a release
gate. Nothing in `v1.0` is weakened by its absence, and shipping it in the
preview does not make it gate-verified; Track A's release principle applies.

## Documentation

Written by the implementation PR: a CI guide (or a section of
`docs/platform-cli.md`) with the complete **two-job** workflow, the per-job
permissions and why each is where it is, and the fork path;
`docs/compatibility.md` (the action's inputs, and the comment body's
OBSERVATIONAL class); `docs/ROADMAP.md`; this task's status; the task index; and
`CHANGELOG.md`.

The security posture is documentation, not just implementation, and it is the
part most likely to be lost in transit — people copy workflows.

- The published workflow **is** the two-job shape. A one-job convenience variant
  must not appear anywhere, because that is the version that gets copied.
- The `pull_request_target` refusal and its reason are stated in prose where a
  reader copying a workflow will meet them. That prose is required, and the
  trigger scan above is written so that requiring it does not break the scan.
- `persist-credentials: false` is explained rather than just present, since a
  reader who does not know the default is `true` will delete it as noise.

## ADR

Warranted for one decision with two halves, and both are security:

**Why the action splits running the workload from holding the write token**, and
why that split is two jobs of one `pull_request` workflow rather than the
`workflow_run` handoff it resembles. A future maintainer will look at two jobs
passing an artifact and try to simplify them into one; the ADR is what tells
them that the second job's whole purpose is to hold a permission the first must
not have, and that `actions/checkout` persisting credentials by default is why
the exposure is real rather than theoretical.

**Why it refuses `pull_request_target` and degrades on fork pull requests
instead.** The alternatives are real and each was rejected for a stated reason —
the privileged `workflow_run` handoff, a bot token with its own credentials,
requiring maintainer approval before the gate runs — and a future reader who
wants comments on fork pull requests will reach for one of them.

The two halves belong in one record because they answer the same question at two
distances: a fork pull request and a same-repository pull request are different
exposures, and the mitigation for one is not the mitigation for the other.

Whether the exit-code passthrough needs its own ADR is doubtful: it inherits
ADR 0033 § 9 and § 10 rather than deciding anything new, and the ADR should say
so rather than restating them.

## Acceptance criteria

1. One action, pinned in a workflow, runs a task 078 scenario and needs no
   other Trustvian-specific step.
2. Exit codes `0`, `1`, `2` and `3` reach the job unchanged. `3` is never
   reported as `1`.
3. The action suppresses no exit code and hard-codes no `continue-on-error`.
4. The comment is rendered from task 078's
   [result document](078-behavioral-scenario-suites.md#result-document) alone;
   no number in it is computed by the action, and a missing required field
   produces no comment.
5. Added and removed behaviors both appear with their `k/N` counts on both
   sides, carrying the classification task 078 assigned rather than one the
   action derived; the `k` and `j` thresholds appear; and every gate check
   appears including passing ones.
6. An uninterpretable result posts no comment and reports why.
7. A comment failure never changes the exit code, and is never silent.
8. One comment per scenario per pull request, edited in place.
9. **The job that runs the workload holds no write-scoped permission, and the
   job that holds one executes no pull request code.** Permissions are granted
   per job; no shipped example grants them at workflow level; every shipped
   checkout sets `persist-credentials: false`.
10. **No workflow trigger — in the action, in any shipped example, or in any
    YAML block in its documentation — names `pull_request_target`**, while the
    documentation still explains the refusal in prose.
11. A fork pull request still runs the gate and still fails the check on a FAIL;
    only the comment degrades, loudly. A failed, skipped or absent comment job
    never changes the check.
12. The action contains no threshold, comparison or verdict composition,
    asserted structurally.
13. The `trustvian` version that produced the result is recorded in the comment,
    read from the document rather than from the environment.
14. **Every document-sourced string is rendered inert** in both the comment and
    the job summary — escaped or code-spanned, mentions neutralized, control
    characters and newlines stripped, per-string and total size capped, and any
    truncation stated visibly.

## Open questions left to implementation

1. **Whether the action lives in this repository or its own.** Genuinely open.
   In-repository keeps it versioned with the CLI it wraps and testable in this
   CI; a separate repository is what GitHub's Marketplace and tag-based pinning
   conventions expect, and it keeps a workflow-shaped release cadence out of the
   engine's release pipeline. Neither is assumed, and the decision should be
   recorded wherever it lands.
2. **Whether the two jobs are one action with two modes, two separate actions,
   or a reusable workflow.** Open. One action with a `mode` input keeps a single
   pinned reference and one version to reason about; two actions make the
   privilege difference obvious at the call site; a reusable workflow ships the
   job split itself, so a caller cannot accidentally collapse it — which is the
   strongest argument for it and also the least flexible. The *split* is
   settled; its packaging is not.
3. **Composite, Docker, or JavaScript action.** A composite action shipping no
   runtime of its own is assumed for the run side, since the work is "run a
   binary" and a Docker action would pull an image on every job. The comment
   side calls one API and renders markdown, which may want a different answer.
4. **How the `trustvian` binary is obtained** — released artifact, container
   image, or a separate setup action. Whichever it is, the version is explicit
   and appears in the comment, read from the result document.
5. **Which comment API**, and therefore which scope — the issue-comment endpoint
   on the pull request (reached through the issues API) or a pull request review
   comment. The issue-comment endpoint is assumed: it needs no file or line
   anchor, and the finding is about the run rather than about a line of code.
   Whichever it is, only the scope it actually requires is documented and
   granted.
6. **Whether a suite of scenarios produces one comment or several.** One comment
   summarizing the suite, with per-scenario sections, is assumed — the
   one-comment-per-pull-request property matters more than per-scenario
   isolation.
