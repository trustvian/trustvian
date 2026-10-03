# 0060 — Agent-operated releases, approved through a GitHub environment

**Status:** Accepted, with § 3's open decision closed by option 2: an
organization-owned GitHub App, `trustvian-release`, is the only actor that can
create a tag. Amends [ADR 0059](0059-releases-are-dispatched-verified-then-published.md):
§ 5 (who creates the tag) and the confirmation step are replaced, and version
selection is added. The pipeline's order, its verification, the digest-only
push and the signing identity are unchanged. Takes effect when a human
repository administrator has created the App and run `scripts/release-setup.sh`
after this is merged (`docs/release-runbook.md` § 0). Until then, preflight
stops at the missing `RELEASE_APP_ID`.

## Context

ADR 0059 made a release one command, `make release`, run by a human
Organization Admin. That person typed the version, answered `y` at a
terminal, and the script created the tag with their credential. That kept a
human decision in front of every release, but it tied the decision to running
the command:

- **An agent could do everything except the last step,** and the last step
  needed the admin to be at the agent's terminal.
- **The version was typed,** although the merged CHANGELOG already declared
  it.
- **A missing Nightly** made the command stop and print a command to run.
- **Nothing told the approver what they were approving** beyond the version
  and the commit.

The owners want an agent (Claude Code) to run the whole release, with a human
approving publication, from wherever they are.

## Decision

### 1. The human gate is the `release` environment

The workflow's jobs split across two environments:
- preflight, gates, build, image, verify and the new **summary** job run in
  `release-build`: deployable from `main` only, no reviewers;
- **only `publish` runs in `release`**, whose required reviewers are the
  Organization Admins.

GitHub holds `publish` until one of them approves the deployment, in the web
UI or the mobile app.

**"Prevent self-review" is off.** With a sole maintainer, it would make every
release impossible: they dispatch, and only they can approve. The decision
that matters is that a human with release authority looked at this run and
approved it. Whether they also dispatched it doesn't change that, and an
agent's dispatch is not a human's anyway.

Before `publish`, the `summary` job writes the page the approver reads:
- the version, and how it was derived (a new `derivation` input, which
  `scripts/release.sh` fills);
- the commit;
- the image digest;
- every check's result;
- the CHANGELOG section that declares the version, verbatim.

### 2. `publish` creates the tag, after approval

`publish` creates the annotated tag at `$COMMIT` through the API, as
`trustvian-release[bot]` (§ 3). It then creates the release, with the same
token, and the image tags, with `GITHUB_TOKEN`, idempotently, as before:
- an existing tag is accepted only if it is that same annotated tag at that
  commit, which happens on a re-run;
- anything else stops publish before anything is published.

The 60-minute wait for a human-created tag is gone.

### 3. Only the release App can create a tag

The design asked first for tag creation restricted, with GitHub Actions as its
only bypass. That is not possible here. In an organization's repository,
GitHub refuses the built-in GitHub Actions app as a ruleset bypass actor:
HTTP 422, *"Actor GitHub Actions integration must be part of the ruleset
source or owner organization"*. The REST API offers no other actor type for it.

The first version of this ADR therefore dropped the creation rule, keeping
update, deletion and force-move restricted, and argued that a squatted tag
blocks a version but releases nothing. Review found where that argument
fails, and it was recorded as an open decision.

**Why the gap mattered.** The argument held for the *workflow's* path to
publication, not for GitHub's release API. `POST /repos/{owner}/{repo}/releases`
with a new `tag_name` creates the tag and a published release in one call,
with nothing but Contents: write:
- **Who could do it:** every maintainer, and the agent's token (§ 6), which
  needs Contents: write to push branches.
- **Deny rules don't stop it.** They match the command an agent types. A
  three-line `go run` program, a `curl` or a `make` target makes the same call,
  and `go run`, `make` and `curl` are everyday commands an agent must be able
  to use.
- **It isn't limited to `main`.** `target_commitish` may name any branch or
  commit, so the release could carry code nobody reviewed.
- **It can't be undone.** Immutable releases, which protect real releases from
  tampering, would make such a release permanent under the project's name.

So "nothing is public without a human's approval" would have rested on
compliance, not on anything GitHub enforces.

**Decision: option 2.** A dedicated GitHub App, `trustvian-release`, owned by
the organization and installed only on this repository, with Contents: read
and write and Metadata: read, is the only actor that can create a tag.
- **Two rulesets**, both on **every tag** (`refs/tags/*`), so no release can
  be created on a non-`v` tag either:
  - **"Release tags: creation"** restricts creation, with one bypass actor:
    the App (`Integration`, its id, mode `always`). GitHub accepts an
    organization's own app as a bypass actor, unlike the Actions app.
  - **"Release tags: immutable"** restricts update, deletion and force-move,
    with no bypass actor at all.
- **Only the approved publish job holds the App.**
  - The App's private key is a secret of the `release` environment, so it
    exists only in `publish`, after a required reviewer approved it.
  - Publish mints a token with `actions/create-github-app-token`, scoped to
    this repository and to `contents: write`, and revoked when the job ends.
  - It writes the tag and every `gh release` call with that token, so every
    release is authored by `trustvian-release[bot]`.
  - `GITHUB_TOKEN` pushes the image tags to GHCR and reads the approvals.
    No job holds `contents: write`.
- **The id is a repository variable**, `RELEASE_APP_ID`. Preflight fails
  early, naming the setup step, when it is missing.
- **A human sets it up** (`docs/release-runbook.md` § 0): creating the App,
  generating and storing its key, and setting the variable. No agent ever
  handles the key. `scripts/release-setup.sh` checks the variable and the
  secret by name, and applies both rulesets. If GitHub refuses the App as a
  bypass actor, it stops with GitHub's exact response; it never falls back to
  a weaker ruleset.
- **A release audit** (`.github/workflows/release-audit.yml`) checks each
  release as it is created, edited or published, and every release weekly. It
  requires that the release:
  - is authored by `trustvian-release[bot]`;
  - has an annotated tag at a commit on `main`;
  - has every asset attested by `release.yml`.

  On any finding it fails and opens or updates an issue labelled
  `release-audit`. Releases up to `v0.9.0` are exempt by name.

**What this closes, and what it costs.**
- Neither a maintainer, nor an agent, nor any workflow other than the approved
  publish job can create a tag, and therefore no one can create a release on a
  new tag.
- An Organization Admin can't create one by hand any more either. That is the
  point: every tag comes from an approved, verified run.
- The cost is one credential, the App's private key: issued by a human, held
  only in the `release` environment, and rotated by generating a new key.
- A release on an *existing* tag without one stays possible with Contents
  write. Today every tag has its release; the audit reports any that doesn't
  come from the App.

### 4. The scripts are agent-safe

`scripts/release.sh`, in agent mode (§ 8):
- needs no terminal, asks nothing for a release, creates no tag, pushes
  nothing, and never approves a deployment;
- dispatches the run and watches it. When the run is `waiting`, it prints
  `approve on GitHub, web or mobile: <run URL>` and keeps watching until the
  release is published, rejected or failed;
- with `--no-wait` (`NO_WAIT=1`), returns after dispatch.

The watch reads the run's status, which needs no Deployments permission.

When Nightly has no run for the commit, the command starts one on `main` and
waits for it. In an interactive shell it asks first; under `DRY_RUN` or
without a terminal it just does it. A failed or unfinished run is reported,
never started over.

### 5. The version is derived

- `scripts/release-version.sh` derives versions with preflight's SemVer
  helpers:
  - the newest stable tag;
  - the next patch, minor or major. Major is refused in 0.x unless the target
    is exactly `v1.0.0`;
  - the version `CHANGELOG.md` declares at a commit: the first `## vX.Y.Z`
    below `## Unreleased`;
  - the next release candidate of a version, counted numerically, so `rc.10`
    follows `rc.9`.
- `make release-prep BUMP=patch|minor TITLE="…"` cuts the CHANGELOG section,
  writes `release-notes.md` from a template that carries the previous known
  limits, and opens the PR. `VERSION=v1.0.0` needs the version typed again at
  a terminal.
- `make release` releases the declared version, `PRE=rc` its next candidate,
  and prints the version and its derivation before anything else.
- Preflight now also requires the version's base to be exactly one bump above
  the newest stable tag. Release notes may name the base version for its
  prereleases.

### 6. Least privilege for agents

An agent runs with its own fine-grained personal access token for this
repository only:
- Actions: read and write;
- Contents: read and write;
- Pull requests: read and write;
- Metadata: read;
- **no Deployments, no Administration, no Environments, and no Workflows.**

Without Workflows, the token cannot push a change under `.github/workflows/`.
That is what stops an agent from writing a copy of `release.yml` without the
`release` environment and dispatching it from its own branch. Preflight would
refuse a non-`main` ref, but only in the unmodified workflow.

Approving a pending deployment needs read access to deployments, so this token
cannot approve. It cannot change rulesets or environments either.

`.claude/settings.json` adds deny rules as defense in depth. They cover:
- approving deployments;
- `git tag` and tag pushes;
- editing environments and rulesets through `gh api`;
- running `release-setup.sh` without `--check`;
- `gh pr merge`.

The token is the real boundary.

**What the token cannot do.** Since § 3, the agent's token cannot create a
tag, and so cannot create a release on a new tag either: the "Release tags:
creation" ruleset admits only the release App. The token table is otherwise
unchanged. Agents never see or handle the App's key, which only a human
stores. The deny rules remain defense in depth.

### 7. A release operator for Claude Code

`.claude/agents/release-operator.md` follows `docs/release-runbook.md` step by
step, as `docs/releasing-with-claude-code.md` shows case by case (E1–E8,
X1–X18). Its instructions forbid it to merge a pull request or approve a
deployment, and require it to stop and ask when a step would need either.
`/release minor|patch|rc|stable [--dry-run]`, and nothing else, invokes it;
plain-language requests go to the agent directly. Both always pass
`MODE=agent`.

### 8. Two modes: manual and agent

The same `make release` serves a person at a terminal and an agent, and says
which on its first line (`scripts/release-mode.sh`, a pure
`resolve_mode(requested, CLAUDECODE, interactive)`):

| | Manual | Agent |
|---|---|---|
| When | an interactive terminal (stdin and stdout), `CLAUDECODE` unset | `CLAUDECODE` set (Claude Code sets it in every shell it runs; verified), no interactive terminal, or `MODE=agent` |
| At the approval | asks `[y]es / [n]o, reject / [l]ater`; `y`/`n` review the deployment as that person, with a comment saying so | never reviews anything; prints where to approve on GitHub and keeps watching |
| Forced by | `MODE=manual`, refused (before anything is dispatched) inside Claude Code or without a terminal | `MODE=agent`, always allowed |

**Why the terminal path may approve.** A person who runs `make release` in
their own terminal already holds the authority to approve; making them leave
the terminal for GitHub's web UI added a step, not a safeguard. The approval
still goes through GitHub's `pending_deployments` API, as that person, and
GitHub records it as their review.

**Why an agent can never reach it.** The review call lives in one function,
`manual_review_deployment`. It re-checks manual mode, an interactive terminal
and an unset `CLAUDECODE` immediately before calling. Only the manual prompt
calls it, and only the manual branch calls the prompt; a structural test
fails if that ever changes. Behind it sit the same boundaries as before: the
agent's token has no Deployments permission, and `.claude/settings.json`
denies any command that sets manual mode.

**Who ran it is recorded, informationally.** `release.yml` takes a required
`operator` input (`manual` or `agent`), which `scripts/release.sh` sets. It
shows in the run name and in every job's summary next to
`github.triggering_actor`; publish's summary lists who approved, from the
run's approvals API. `operator` is what the script reported. GitHub's records
of who triggered the run and who approved it are the authoritative ones.

## Alternatives considered

- **Keep the terminal confirmation and the human-created tag (ADR 0059 § 5).**
  It is the strongest tag protection, but it keeps the human at the agent's
  terminal, which is the problem this solves.
- **GitHub Actions as the creation bypass.** It is what was asked first;
  GitHub refuses it for an organization's repository (§ 3).
- **Leaving tag creation unrestricted (this ADR's first version).** It needs
  no credential, but anyone with Contents: write could publish an unapproved,
  permanent release through the API (§ 3).
- **A GitHub App as the creation bypass: chosen** (§ 3), at the cost of one
  credential held only by the approved publish job.
- **Approval by an agent with an admin's token.** It defeats the point of the
  gate, which is why the agent's token has no Deployments permission.

## Consequences

- Releasing: an agent or a person runs `make release`, and an Organization
  Admin approves the deployment on GitHub, from anywhere.
- The single `v*` ruleset is replaced by two on every tag. Creation is
  admitted only for the release App, and update, deletion and force-move for
  no one: Organization Admin loses its tag bypass. A human administrator
  creates the App and applies the rulesets with `scripts/release-setup.sh`,
  and `--check` shows the difference first.
- Every release from now on is authored by `trustvian-release[bot]`, and the
  release audit checks that it is.
- Governance changes: `docs/governance/releases.md`,
  `docs/governance/agents.md` and CLAUDE.md now describe the release App,
  used only by the approved publish job, as the tag's creator, the environment approval as the human decision, and what an
  agent may run.
- `docs/release-runbook.md` is the procedure, kept honest by
  `scripts/runbook_drift_test.go`.
