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
- A documented workflow example with the minimum token permissions it needs,
  and a documented fork-pull-request path that does not escalate.

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

permissions:
  contents: read
  pull-requests: write

jobs:
  behavior:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<pinned sha>

      - uses: trustvian/<action>@<pinned sha>
        with:
          scenario: scenarios/support-login.yaml
          comment: true
```

Four things and nothing more: check out the code, run the scenario, comment,
let the exit code stand. The exact input names and the action's location are
implementation decisions — see
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

  Gate
    repeatedly added behaviors     1 / max 0        FAIL
    worst block decisions per run  0 / max 0        pass
    worst critical risk per run    0 / max 0        pass
    reference repetitions          5 / 5            pass
    candidate repetitions          5 / 5            pass
    repetitions lacking evidence   0 / max 0        pass
```

Every number above is a field in task 078's result document. The action's whole
job is transcription and layout.

Four properties this has to hold:

**Added and removed behaviors both appear, with their `k/N` counts on both
sides.** A removed behavior is a finding too — a candidate that stopped calling
`audit_log` is exactly as interesting as one that started calling
`export_customer`, and task 054 has reported `Removed` since it was written.

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

## Token permissions and fork pull requests

This is the section the task exists to get right.

### The minimum

```yaml
permissions:
  contents: read
  pull-requests: write
```

Nothing else. Not `write-all`, not `issues: write` unless the chosen comment
API requires it, not `actions: write`, not `id-token`. The action documents the
minimum it needs and the documentation states why each one is there.

Note what this repository's own CI does for comparison: `ci.yml` runs with
`contents: read` and nothing more, and says so in a comment — *"a fork pull
request therefore cannot obtain credentials, because the job has none to
obtain."* A behavioral gate that comments cannot be quite that austere, which is
exactly why its token story has to be deliberate.

### `pull_request_target` is refused

```text
✗  on: pull_request_target  +  actions/checkout with the PR head
```

This is the canonical GitHub Actions privilege escalation, and this task must
never document, example, or implement it.

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

| Situation | Behaviour |
|---|---|
| same-repository pull request, `pull-requests: write` granted | scenario runs, comment posted or updated, exit code stands |
| fork pull request, read-only token | scenario runs, **comment skipped with a warning**, result written to the job summary, exit code stands |
| `comment: false` | scenario runs, job summary only, exit code stands |
| token lacks the permission for another reason | same as the fork path: warn, summarize, preserve the exit code |

The degradation is **loud and downward**: the gate still runs, the verdict still
decides the job, and the only thing lost is the rendering. The job summary
(`GITHUB_STEP_SUMMARY`) needs no token at all, which is why it is the fallback
rather than an alternative rendering.

**Two things this explicitly does not do.** It does not detect a fork and switch
event types. It does not stash the result as an artifact for a second,
privileged workflow to pick up and comment with — the `workflow_run` pattern.
That pattern is a legitimate technique, and it is also a privileged workflow
consuming an artifact produced by untrusted code, which needs its own threat
model and its own review. It is named here as deliberately out of scope rather
than overlooked; if it is ever wanted, it is a separate task with a separate
ADR.

### The workload runs untrusted code, and the specification says so

Running a pull request's code in CI is ordinary — every test job does it. The
action does not pretend to sandbox it, exactly as task 078 does not pretend to
sandbox a scenario file. What the action guarantees is narrower and honest:

> The action never combines running the workload with holding a token that can
> write to the repository on a fork pull request.

That is testable. "The workload is safe" is not, and is not claimed.

## Architecture

```text
workflow YAML
    ↓
action            ← this task: invocation, rendering, comment lifecycle
    ↓
trustvian eval    ← task 078's scenario command
    ↓
control plane     ← diff · scorecard · gate · repeated gate, all server-owned
```

Three layers of adapter, and the authority is at the bottom of the stack in all
three. The action's source contains no threshold, no comparison against a limit,
and no verdict composition — asserted by the same structural scan style task 078
applies to its runner and task 060 applies to the CLI.

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
- **No `pull_request_target` anywhere.** A scan of the action, its documentation
  and every example workflow it ships. This is the security test that must fail
  loudly if anyone adds the convenient thing.
- **The documented permissions are the minimum.** A run with
  `pull-requests: write` removed still runs the scenario, still exits correctly,
  and takes the summary path.
- **Fork behavior** exercised against a read-only token: the scenario runs, no
  comment is attempted or the attempt is handled, the summary is written, the
  exit code stands.
- **The `trustvian` version used is recorded in the comment**, asserted on a
  rendered body.

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
`docs/platform-cli.md`) with a complete working workflow, the minimum
permissions and the fork path; `docs/compatibility.md` (the action's inputs, and
the comment body's OBSERVATIONAL class); `docs/ROADMAP.md`; this task's status;
the task index; and `CHANGELOG.md`.

The security posture is documentation, not just implementation. The `docs/`
material must state the `pull_request_target` refusal and its reason where a
reader copying a workflow will see it — a warning in a spec nobody copies from
prevents nothing.

## ADR

Warranted for one decision, and it is the security one: **why the action refuses
`pull_request_target` and degrades on fork pull requests instead.** The
alternatives are real and each was rejected for a stated reason — the privileged
`workflow_run` handoff, a bot token with its own credentials, requiring
maintainer approval before the gate runs — and a future reader who wants
comments on fork pull requests will reach for one of them.

Whether the exit-code passthrough needs its own ADR is doubtful: it inherits
ADR 0033 § 9 and § 10 rather than deciding anything new, and the ADR should say
so rather than restating them.

## Acceptance criteria

1. One action, pinned in a workflow, runs a task 078 scenario and needs no
   other Trustvian-specific step.
2. Exit codes `0`, `1`, `2` and `3` reach the job unchanged. `3` is never
   reported as `1`.
3. The action suppresses no exit code and hard-codes no `continue-on-error`.
4. The comment is rendered from the machine-readable result alone; no number in
   it is computed by the action.
5. Added and removed behaviors both appear with their `k/N` counts on both
   sides, and every gate check appears including passing ones.
6. An uninterpretable result posts no comment and reports why.
7. A comment failure never changes the exit code, and is never silent.
8. One comment per scenario per pull request, edited in place.
9. The documented workflow grants `contents: read` and `pull-requests: write`
   and nothing else.
10. **No `pull_request_target` appears in the action, its documentation, or any
    example it ships**, and running the workload is never combined with a
    repository-write token on a fork pull request.
11. A fork pull request still runs the gate and still fails the job on a FAIL;
    only the comment degrades, loudly.
12. The action contains no threshold, comparison or verdict composition,
    asserted structurally.
13. The `trustvian` version that produced the result is recorded in the comment.

## Open questions left to implementation

1. **Whether the action lives in this repository or its own.** Genuinely open.
   In-repository keeps it versioned with the CLI it wraps and testable in this
   CI; a separate repository is what GitHub's Marketplace and tag-based pinning
   conventions expect, and it keeps a workflow-shaped release cadence out of the
   engine's release pipeline. Neither is assumed, and the decision should be
   recorded wherever it lands.
2. **Composite, Docker, or JavaScript action.** A composite action shipping no
   runtime of its own is assumed, since the work is "run a binary and call one
   API" and a Docker action would pull an image on every job.
3. **How the `trustvian` binary is obtained** — released artifact, container
   image, or a separate setup action. Whichever it is, the version is explicit
   and appears in the comment.
4. **Which comment API** — the issue-comment endpoint on the pull request, or a
   pull request review comment. The issue-comment endpoint is assumed: it needs
   no file or line anchor, and the finding is about the run rather than about a
   line of code.
5. **Whether a suite of scenarios produces one comment or several.** One comment
   summarizing the suite, with per-scenario sections, is assumed — the
   one-comment-per-pull-request property matters more than per-scenario
   isolation.
