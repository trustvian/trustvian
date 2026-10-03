# Releasing Trustvian — the runbook

The step-by-step procedure for every kind of release, for every repository
owner and for an AI agent operating on their behalf. Each scenario is a list
of commands to run in order; nothing in it depends on knowing how the
pipeline works inside. For the why, see
[ADR 0059](adr/0059-releases-are-dispatched-verified-then-published.md),
[ADR 0060](adr/0060-agent-operated-releases-with-environment-approval.md) and
the [release guide](release-guide.md).

If something here does not match what you see, stop and open an issue rather
than improvising: a release is the one operation in this repository that
cannot be undone. `scripts/runbook_drift_test.go` fails CI when a command,
script, message or link in this file stops matching the code.

---

## 0. Before your first release

### Who may do what

| Role | May | May not |
|---|---|---|
| **Organization Admin** (a release owner) | Everything below, including **approving the `release` deployment** on GitHub, which publishes | Push a `v*` tag by hand; move or delete a tag or a published release |
| **Maintainer** (write access) | Run `make release-prep`, review the prep PR, run `make release` (with or without `DRY_RUN=1`) | Approve the `release` deployment: only its required reviewers, the Organization Admins, can |
| **AI agent** (Claude Code or any other) | Run `make release-prep`, open PRs, run `make release` and `gh run rerun`, with its own token ([§ 0, An agent's token](#an-agents-token)) | Approve a deployment; create or push a `v*` tag; create or edit a GitHub Release; merge a PR; run with a token that has Deployments, Administration, Environments or Workflows permission ([agents.md](governance/agents.md)) |

Publishing always waits for a person: the `publish` job runs in the `release`
environment, and GitHub holds it until an Organization Admin approves it, in
the web UI or the GitHub mobile app.

### Your machine

```bash
gh --version                 # GitHub CLI, any current release
gh auth status               # logged in to github.com
gh api "orgs/trustvian/memberships/$(gh api user --jq .login)" --jq .role
# → admin   (needed only to approve publishing; maintainers can do everything else)
git --version
```

You need nothing else: no Go, no Docker, no signing key. Everything is built
and signed in GitHub Actions.

### The repository (once, by an Organization Admin)

```bash
./scripts/release-setup.sh --check
# Every line should say "ok". If any says MISSING:
./scripts/release-setup.sh
./scripts/release-setup.sh --check
```

This sets:

- the `release-build` environment, deployable from `main` only, where every job
  before publishing runs;
- the `release` environment, deployable from `main` only, with the
  Organization Admins as required reviewers and self-review allowed. A sole
  maintainer must be able to approve their own release;
- the `v*` tag ruleset: nobody may update, delete or force-move a `v*` tag.
  Creation is not restricted, because the workflow creates the tag after
  approval and GitHub accepts no bypass for it
  ([ADR 0060](adr/0060-agent-operated-releases-with-environment-approval.md));
- immutable releases, where GitHub offers them.

### An agent's token

An agent runs with its own fine-grained personal access token, never with
your `gh` login. Create it at **GitHub → Settings → Developer settings →
Fine-grained tokens → Generate new token**:

| Field | Value |
|---|---|
| Resource owner | `trustvian` |
| Repository access | Only select repositories → `trustvian/trustvian` |
| Actions | Read and write |
| Contents | Read and write |
| Pull requests | Read and write |
| Metadata | Read-only (added automatically) |
| Everything else | **No access.** In particular no Deployments (approving a release), no Administration (rulesets), no Environments, and no Workflows (changing `.github/workflows/`) |

Store it outside the repository and start Claude Code with it for that
session only:

```bash
GH_TOKEN="$(cat ~/.config/trustvian/agent-token)" claude
```

`gh`, and git through `gh`'s credential helper, use `GH_TOKEN` instead of your
login. Close the session to drop it.

### The six rules

1. **Releases come from `main`'s head.** There are no release branches.
2. **Never push a `v*` tag by hand.** The workflow creates it, after
   everything is verified and an Organization Admin approved. A hand-pushed tag
   starts nothing and blocks the version.
3. **Never move or delete a tag or a published release.** A bad release is
   fixed by the next patch ([F7](#f7-a-published-release-is-broken)).
4. **One release at a time.** Announce it in a release issue
   ([§ 7](#7-coordinating-between-owners)) before you start.
5. **When in doubt, dry run.** `DRY_RUN=1` builds, signs and verifies
   everything and publishes nothing.
6. **Read what the command prints.** It says which version it resolved and
   how, before it does anything.

---

## 1. Choose the release type

| Since the last release, `main` has… | Release | Command |
|---|---|---|
| only bug fixes, docs, or dependency bumps | **patch** — `v0.10.0` → `v0.10.1` | `BUMP=patch` |
| a new capability, or (before 1.0) any breaking change | **minor** — `v0.10.1` → `v0.11.0` | `BUMP=minor` |
| the decision to declare 1.0 | **major** — `v0.x` → `v1.0.0` | explicit `VERSION=v1.0.0` ([S5](#s5-the-first-stable-release-v100)) |

Add **release candidates** (`-rc.1`, `-rc.2`, …) when someone outside the
project should try a version before it is stable
([S3](#s3-release-candidates-for-outside-testers)).

To see what `main` holds since the last release:

```bash
git fetch origin --tags
last=$(./scripts/release-version.sh latest-stable); echo "$last"
git log --oneline "$last..origin/main"
git show origin/main:CHANGELOG.md | sed -n '/^## Unreleased/,/^## v/p'
./scripts/release-version.sh next minor      # what BUMP=minor would prepare
```

If the `Unreleased` section lists anything under **Added** or
**Changed**, it is a minor release, not a patch.

---

## 2. The lifecycle at a glance

```text
 1  make release-prep BUMP=…        opens a PR: CHANGELOG section + release-notes.md
 2  review and merge that PR         (a human merges; squash)
 3  make release DRY_RUN=1           optional rehearsal: everything except publishing
 4  make release [PRE=rc]            preflight → Nightly if needed → build → sign
                                     → verify on Linux and macOS → approval summary
                                     → "approve at <run URL>"
 5  approve the deployment           an Organization Admin, on GitHub (web or mobile):
                                     tag, GitHub Release, image tags — in that order
 6  after-release checklist          § 5
```

Steps 3 and 4 resolve the version themselves: from the CHANGELOG section the
merged prep PR declared, plus the next `-rc.N` with `PRE=rc`. You never type
it, except for 1.0.0.

---

## 3. Scenarios

Every scenario starts from an up-to-date `main`:

```bash
git checkout main && git pull --ff-only origin main
```

### S1. A minor release (new capability)

Example: `v0.10.1` is out; `main` has the inspection-depth work.

1. **Open the release issue** ([§ 7](#7-coordinating-between-owners)):
   *"Release v0.11.0 — owner: @you"*.
2. **Prepare.**
   ```bash
   make release-prep BUMP=minor TITLE="Inspection depth"
   ```
   Prints `release-prep: v0.10.1 → v0.11.0 (minor bump)`, creates branch
   `release/prep-v0.11.0`, and opens a PR that:
   - turns `## Unreleased` into `## v0.11.0 — Inspection depth`, with a new
     empty `## Unreleased` above it;
   - writes `release-notes.md` from the template, carrying over the previous
     known limits for you to edit.
3. **Review the PR.** A second owner reads the CHANGELOG section and
   `release-notes.md`. Edit the opening paragraph, **What's new**, **What
   this release does not include** and **Known limits** in the PR — these are
   what users read first. Merge it.
4. **Update and rehearse** (recommended for the first release you run, and
   whenever release tooling changed since the last release):
   ```bash
   git pull --ff-only origin main
   make release DRY_RUN=1
   ```
   Prints `release: v0.11.0 — declared by CHANGELOG.md at …`, starts Nightly
   on `main`'s head if it has no run there, waits for it, then dispatches.
   Ends with *"release: dry run of v0.11.0 passed: built, signed and
   verified; nothing published"* and a run link. Takes 30–60 minutes.
5. **Release.**
   ```bash
   make release
   ```
   Same checks, same build and verification, then:
   ```text
   release: every check passed for v0.11.0 at 4f3c…
   release: approve at https://github.com/trustvian/trustvian/actions/runs/… (GitHub web or mobile)
   ```
   **Approve the deployment.** An Organization Admin opens that link, reads
   the run's **Approval summary** (version, commit, how it was derived, the
   CHANGELOG section, every check's result), then chooses **Review
   deployments → release → Approve and deploy**. The workflow then creates
   the tag, publishes the GitHub Release and the image `v0.11.0`, and moves
   `0.11` and `latest`. The command ends with the release URL.
6. **Do the after-release checklist** ([§ 5](#5-after-every-release)) and
   close the release issue.

### S2. A patch release (fixes only)

Example: a bug in `trustvian dev` is fixed on `main` after `v0.11.0`.

1. **Confirm it is a patch** ([§ 1](#1-choose-the-release-type)): the
   `Unreleased` section lists only **Fixed** (and **Security** or docs).
   If a feature has landed too, either release a minor (S1) or wait.
2. **Release issue**, then:
   ```bash
   make release-prep BUMP=patch TITLE="dev fixes"
   ```
   → PR for `v0.11.1`. Review the notes (a patch's notes are usually three
   lines), merge.
3. **Release.**
   ```bash
   git pull --ff-only origin main
   make release            # → v0.11.1; then approve the deployment
   ```
   A patch of the newest line moves `0.11` and `latest`. A patch of an older
   line cannot be made: releases come from `main` only.
4. **After-release checklist.**

### S3. Release candidates for outside testers

Example: design partners should try `v0.11.0` before it is stable.

1. **Prepare the version as for a stable release** — S1 steps 1–3. The
   CHANGELOG now declares `v0.11.0`.
2. **First candidate.**
   ```bash
   make release PRE=rc     # → v0.11.0-rc.1; then approve the deployment
   ```
   It is published as a **prerelease**: never GitHub's "Latest", and the
   image's `0.11` and `latest` do not move. Only `v0.11.0-rc.1` exists. The
   release notes written for `v0.11.0` serve its candidates.
3. **Share it.** Send testers the release URL, or:
   ```bash
   gh release download v0.11.0-rc.1 --repo trustvian/trustvian --pattern '*darwin_arm64*'
   ```
4. **During the candidate phase, `main` is frozen to this release.** Merge
   only fixes for it, and add their CHANGELOG entries to the
   `## v0.11.0` section — not to `Unreleased`, because they ship in
   v0.11.0. Anything else waits in its PR. Say so in the release issue.
5. **Next candidate** after fixes:
   ```bash
   git pull --ff-only origin main
   make release PRE=rc     # → v0.11.0-rc.2 (counted from the existing tags)
   ```
6. **Stable**, when testers are satisfied:
   ```bash
   make release            # → v0.11.0; then approve the deployment
   ```
   The stable release is built from `main`'s head and verified again. It is
   the same code as the last candidate when nothing merged since.
7. **After-release checklist.** Thank the testers in the release notes.

### S4. An urgent security fix

Follow [SECURITY.md](SECURITY.md) first. The release part:

1. **Draft a GitHub Security Advisory** and develop the fix in its temporary
   private fork, so the fix is not public before the release.
2. **CHANGELOG:** put one neutral line under **Security** in
   `Unreleased` ("Fix a denial of service in …"); details go in the
   advisory.
3. **Merge the fix**, then release a patch (S2) immediately. Skip the dry run
   only if release tooling has not changed since the last release.
4. **Publish the advisory** with the fixed version, then request a CVE from
   the advisory page if one applies.
5. **After-release checklist**, plus: verify the advisory names the patched
   version and links the release.

### S5. The first stable release, `v1.0.0`

Only when the [ROADMAP's v1.0 exit criteria](ROADMAP.md#v10-exit-criteria)
are met. Candidates are **mandatory** here: Track A's release principle
verifies the gate against the release candidate.

1. **Prepare explicitly** — `BUMP=major` is refused before 1.0:
   ```bash
   make release-prep VERSION=v1.0.0 TITLE="Local-first behavioral security platform"
   ```
   Asks you to type `v1.0.0` again to confirm, at a terminal. An agent cannot
   confirm it: a person runs this step.
2. **Candidates** as in S3 (`make release PRE=rc` → `v1.0.0-rc.1`, …).
   Verify every v1.0 exit criterion against the candidate and record the
   result in the release issue.
3. **Stable:** `make release` → `v1.0.0`; then approve the deployment.
4. **After-release checklist**, plus: from now on the
   [compatibility contract](compatibility.md) applies.

---

## 4. When something goes wrong

Nothing before the approval publishes anything. Read the message, fix the
cause, run the same command again.

### F1. Preflight refuses

Preflight's messages (`scripts/release-preflight.sh`,
`scripts/release-version.sh`); `…` stands for the version, commit or
workflow it names:

| Message contains | Do |
|---|---|
| `the tag … already exists` | That version is released. Check `gh release list`; you probably need the next one |
| `… is not greater than the existing tag …` | The CHANGELOG declares a version at or below an existing tag. Fix it in a PR |
| `… is not a valid single bump above …` | The CHANGELOG declares a version that skips one (the message lists the valid ones). Fix it in a PR |
| `a major bump from … is refused in 0.x` | Leaving 0.x is explicit: [S5](#s5-the-first-stable-release-v100) |
| `the latest … run for … is not a success` | CI or Nightly failed, or is still running, on `main`'s head. Read its log. Fix `main` first; never release over red CI. A flake: *Re-run failed jobs* on that run, then run again |
| `CHANGELOG.md has no "## …" section` | The prep PR was not merged, or you pulled before it merged |
| `CHANGELOG.md at … declares no "## vX.Y.Z" section below "## Unreleased"` | No prep PR was merged: run `make release-prep` |
| `release-notes.md does not name …` | The notes are from the previous release: run `make release-prep` |
| `commit … is not reachable from …` | Pull `main` and run again |

The command's own checks (`scripts/release.sh`, `scripts/release-prep.sh`):

| Message contains | Do |
|---|---|
| `the working tree is not clean` / `main is not equal to origin/main` | `git status`, commit or stash; `git pull --ff-only origin main` |
| `CI has no run for … yet` | CI starts on the merge to `main`. Wait for it to finish, then run again |
| `Nightly failed for …` | Nightly, which the command started, failed. Read its log, fix, merge, run again |
| `another release run is not finished` | [F5](#f5-another-release-is-running) |
| `CHANGELOG.md's Unreleased section is empty` | Nothing has landed since the last release; there is nothing to prepare |

### F2. The dry run or the verification fails

The command prints the failed job and its log link, and says nothing was
published. No tag exists either: the workflow creates it only after approval.
An untagged image digest may remain in the package's version list; it is not
a release and needs no cleanup.

1. Open the log link and find the first error.
2. Fix it in a normal PR, and merge.
3. `git pull --ff-only origin main` and run the same `make release …` again.
   The new head gets its own CI and Nightly run first.

### F3. Approval is pending or was rejected

- **Pending:** the run waits on GitHub, and `make release` keeps watching. You
  may stop watching (`Ctrl-C`, or `NO_WAIT=1` next time); the run keeps
  waiting. Any Organization Admin can approve it later from the run page.
- **Rejected:** the run ends, `make release` reports *"the release deployment
  was rejected or not approved in time; nothing was published"*. No tag was
  created. Run `make release` again when ready.
- **Approved after a long wait:** fine. Publishing starts when approved.

### F4. Publishing failed halfway

The tag or the GitHub Release exists, but an image tag does not (or the
reverse). Every publish step checks what already happened, so:

```bash
gh run rerun <run-id> --repo trustvian/trustvian --failed
gh run watch <run-id> --repo trustvian/trustvian --exit-status
```

The re-run publish job waits for approval again; approve it as before. Never
delete the tag or the release to "start clean". Immutable releases would
refuse it anyway.

### F5. Another release is running

```bash
gh run list --repo trustvian/trustvian --workflow release.yml --limit 5
```

Find its owner in the open release issue and wait. Do not cancel another
owner's run.

### F6. `main` moved after your dry run

The release uses `main`'s head at the moment you run `make release`. A new
head gets CI and Nightly first, which the command handles. Run another dry
run only if the new commits touch release tooling (`.github/`, `scripts/`,
`Makefile`, `Dockerfile`).

### F7. A published release is broken

1. Do not delete, retag, or re-upload. Releases and tags are immutable.
2. If GitHub allows editing the release text, add at the top: *"Known issue:
   … Fixed in vX.Y.Z+1."*
3. Fix on `main` and ship a patch (S2), or S4 if it is a security issue. The
   patch moves `latest` and the minor tag to the fixed image.
4. Name the broken version in the patch's notes.

---

## 5. After every release

```bash
V=v0.11.0
gh release view "$V" --repo trustvian/trustvian      # assets: 5 archives + checksums.txt
```

1. **Verify it as a user would** ([§ 6](#6-verifying-a-release)), at least
   the cosign and attestation commands.
2. **Package visibility** (the first release of a new image only): the GHCR
   package page must say *Public*.
3. **Follow-ups** in one PR, if they apply: the `trustvian-run` and
   `trustvian-comment` pin in `.github/actions/trustvian-run/runtime.env`;
   the ROADMAP's Released Milestones; any "no release carries …" sentence the
   release made false.
4. **Announce** where the project announces releases.
5. **Close the release issue** with the release URL.

---

## 6. Verifying a release

What anyone can run, against any release since `v0.10.0`. Needs `gh`,
`cosign` v3 or newer, and `crane` (`brew install cosign crane`).

```bash
V=v0.11.0
SHA=$(git ls-remote https://github.com/trustvian/trustvian "refs/tags/$V^{}" | cut -f1)
: "${SHA:?could not resolve $V}"

# Archives: checksums and build provenance
gh release download "$V" --repo trustvian/trustvian --pattern 'trustvian_*_linux_amd64.tar.gz' --pattern checksums.txt
sha256sum -c checksums.txt --ignore-missing
gh attestation verify "trustvian_${V}_linux_amd64.tar.gz" \
  --repo trustvian/trustvian \
  --signer-workflow trustvian/trustvian/.github/workflows/release.yml \
  --source-ref refs/heads/main --source-digest "$SHA"

# Image: signature (Cosign v3 or newer)
IMAGE=ghcr.io/trustvian/trustvian-collector
DIGEST=$(crane digest "$IMAGE:$V")
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity https://github.com/trustvian/trustvian/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository trustvian/trustvian \
  --certificate-github-workflow-trigger workflow_dispatch \
  --certificate-github-workflow-sha "$SHA"
```

Releases up to `v0.9.0` were signed by a tag push and have checksums only;
see [supply-chain.md](supply-chain.md#verifying-a-published-image).

---

## 7. Coordinating between owners

One open issue per release, created before `make release-prep` from the
**Release** issue template
([`.github/ISSUE_TEMPLATE/release.md`](../.github/ISSUE_TEMPLATE/release.md)),
which carries this checklist:

```markdown
Title: Release v0.11.0

Owner: @you · Type: minor · Candidates: yes/no

- [ ] release-prep PR: #___ (reviewed by @___)
- [ ] dry run: <run link>
- [ ] rc.1: <release link> — shared with ___     (S3 only)
- [ ] release: <release link> (approved by @___)
- [ ] verified (§ 6)
- [ ] follow-ups PR: #___
- [ ] announced

During candidates, main accepts only fixes for this release.
```

The owner is the only person (or agent, on the owner's behalf) who runs
`make release` until the issue is closed. Hand over by reassigning the issue.

---

## 8. Reference

| Command | Does |
|---|---|
| `make release-prep BUMP=patch\|minor TITLE="…"` | Computes the next version from the newest stable tag, cuts the CHANGELOG section, writes `release-notes.md`, opens the prep PR. Never tags, never dispatches |
| `make release-prep VERSION=v1.0.0 TITLE="…"` | The same for an explicit version; leaving 0.x asks you to type the version again |
| `make release DRY_RUN=1` | Everything up to and including verification and the approval summary. Publishes nothing |
| `make release` | Releases the version the merged CHANGELOG declares. Publishing waits for an Organization Admin's approval on GitHub |
| `make release PRE=rc` | Releases the next `-rc.N` of the declared version, as a prerelease |
| `make release VERSION=vX.Y.Z` | Explicit override, validated the same way |
| `make release NO_WAIT=1` | Dispatches, prints the run URL, and returns without watching |
| `./scripts/release-version.sh latest-stable` | The newest stable tag |
| `./scripts/release-version.sh next patch` | What `BUMP=patch` would prepare (also `minor`; `major` only with `v1.0.0`) |
| `./scripts/release-version.sh resolve <commit>` | What `make release` would release at a commit, and how |
| `./scripts/release-setup.sh [--check]` | One-time repository setup, or a read-only check of it |
| `gh run rerun <id> --failed` | Finishes a failed or interrupted publish; every publish step is idempotent |
| `/release patch\|minor\|rc\|stable` | In Claude Code: the release operator agent runs this runbook. It never approves and never merges |

| Term | Means |
|---|---|
| Dry run | Build, sign and verify; publish nothing |
| Approval | An Organization Admin approving the `release` deployment of a run on GitHub; the only step that publishes |
| Prerelease / RC | `vX.Y.Z-rc.N`: published for testers, never "Latest", moves no floating image tag |
| Floating tags | The image tags `X.Y` and `latest`; moved only by the newest stable release |
| Digest | The image's immutable `sha256:…` identity; everything is verified by digest |
| Preflight | The checks run before anything is built — locally first, then in the workflow |
