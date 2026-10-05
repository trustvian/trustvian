# Trustvian v0.11.0 — webui feature

The release that makes the WebUI an investigation tool rather than a viewer.
After `v0.10.0` showed you what your agent does, `v0.11.0` helps you find out
why: an Overview of a project's newest runs, gate verdicts and evidence; a
trace timeline over the actions Trustvian evaluated; and searchable selectors in
place of every identifier you used to paste. Upgrade if you use the local WebUI
or the control plane's `/v1` API. The engine, the CLI's commands and the gate
are unchanged. The platform database moves to schema v10 on first start, and an
older build then refuses it.

Every change is in
[CHANGELOG.md](https://github.com/trustvian/trustvian/blob/v0.11.0/CHANGELOG.md#v0110--webui-feature).

## What's new

- **Light, Dark and System themes.** A switch at the foot of the sidebar; a
  first visit follows your operating system, and an explicit choice is
  remembered without flashing light on load.
- **Searchable selectors instead of pasted identifiers.** Every field that
  asked for an existing project, agent, candidate, run, environment, session or
  scenario execution is a keyboard-operable search showing names, with the
  identifier beside it and a **Copy ID** control. A choice made in one
  destination carries to the others; changing a parent clears what depended on
  it. "Advanced: paste an ID" remains.
- **The Overview.** A new destination: live activity, the newest runs of a
  project, agent or candidate by status, one run's evidence and top behaviors,
  recorded gate verdicts and environments. Every panel says what it covers and
  when it was read; nothing refreshes on a timer and nothing missing reads as
  `0`.
- **Trace investigation.** A **Traces** destination lists a run's traces and
  draws one as a waterfall of the actions Trustvian evaluated, nested by
  recorded parent span, with a details panel beside it. It says plainly that
  it is not a complete distributed trace.
- **Runs newest first.** Runs, the Overview and every run selector list runs
  newest first across a project, an agent or a candidate.
- **Recorded scenario executions.** Evidence → **Scenarios** lists past
  `trustvian eval run` executions, asks the control plane whether one can be
  reused as a `--reference`, and copies the command. Nothing runs from the
  browser.
- **New `/v1` routes:** `GET /v1/projects/{id}/evaluation-runs/recent`,
  `/v1/evaluation-runs/{id}/traces` and `/sessions`,
  `/v1/projects/{id}/scenario-executions` and
  `/v1/scenario-executions/{id}/reference-check`
  ([compatibility](https://github.com/trustvian/trustvian/blob/v0.11.0/docs/compatibility.md)).

## What this release does not include

- **Complete distributed traces.** Trustvian retains the actions it evaluated
  — at most 4096 per run — not every span, attribute, event or service name.
  Keep your trace backend for the full picture.
- **Starting a scenario from the browser.** The WebUI finds and checks recorded
  executions; running one stays in the CLI, because it runs your command.
- **Authentication or remote access** for the local runtime (task 070). It
  still binds loopback only.
- **Promotion sharing across a deployment** and the rest of the `v1.0` gate
  (tasks 068–072), which the roadmap still lists as not specified.
- **Measured detection numbers** for metadata-only detection (task 080, still
  specified and not implemented).

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
- **Schema v10 is one-way.** The first start of `v0.11.0` migrates the platform
  database in place; an older build then refuses it as newer. To keep a way
  back, copy the stopped runtime's state directory first — `.trustvian/`, or
  `~/.trustvian/dev/<hash>/` under `trustvian dev`
  ([where state lives](https://github.com/trustvian/trustvian/blob/v0.11.0/docs/local-development.md)).
  Platform backup tooling is task 071, not yet implemented.
- **With a candidate chosen, run lists show only its runs.** Pick a
  promotion's or provenance's other side under Compare, or paste its ID.
- **A reference check holds for the execution's own repetition count, project
  and environment.** A scenario that differs on any of them is refused by the
  CLI's same validation.

## Verifying

```bash
V=v0.11.0
A=trustvian_${V}_linux_amd64.tar.gz
SHA=$(git ls-remote https://github.com/trustvian/trustvian "refs/tags/$V^{}" | cut -f1)

sha256sum -c checksums.txt --ignore-missing
gh attestation verify "$A" \
  --repo trustvian/trustvian \
  --signer-workflow trustvian/trustvian/.github/workflows/release.yml \
  --source-ref refs/heads/main --source-digest "$SHA"

IMAGE=ghcr.io/trustvian/trustvian-collector
DIGEST=$(crane digest "$IMAGE:$V")
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity https://github.com/trustvian/trustvian/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository trustvian/trustvian \
  --certificate-github-workflow-trigger workflow_dispatch \
  --certificate-github-workflow-sha "$SHA"
```

What each flag pins: [supply-chain.md](https://github.com/trustvian/trustvian/blob/v0.11.0/docs/supply-chain.md#verifying-a-published-image).
