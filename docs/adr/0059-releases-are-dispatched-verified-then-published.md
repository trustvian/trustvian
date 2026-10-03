# 0059 — Releases are dispatched, verified, then published

**Status:** Accepted. Replaces the tag-push pipeline that shipped `v0.9.0`
(task 040, task 041). `make release` is the only release path.

## Context

Through `v0.9.0`, a release began when a human Organization Admin pushed a
`v*` tag. `release.yml` ran on that push: it re-ran the gates, built the
archives, created a **draft** GitHub Release, pushed the image under its
version tag, signed it, and moved `X.Y` and `latest`. A human then checked the
draft and published it.

That order put the irreversible step first. A tag is permanent by ruleset, so
every pipeline failure after it cost a version number. `v0.9.0` took three
candidates:
- `rc.1` died at job setup on an action ref that did not exist;
- `rc.2` failed at the first container step on an uppercase registry name;
- `rc.3` passed.

The two failed tags stay in the history. Release candidates became the way to
test a pipeline that could only be tested by releasing. The image had the same
shape: the version tag was pushed before the signature existed, and before
anyone had verified the image as a consumer would.

The verification itself was a manual checklist after the fact: download, check
checksums, run the binary, `cosign verify`. It was easy to skip and impossible
to enforce. The archives had checksums but no provenance.

## Decision

### 1. One command, dispatched from main

`make release VERSION=vX.Y.Z [DRY_RUN=1]` runs `scripts/release.sh`. It checks
locally that main is clean and equal to `origin/main` and runs the preflight,
then dispatches `release.yml` (`workflow_dispatch`) with the version and
`origin/main`'s head as inputs. It needs `gh` and git, and nothing else: it
builds nothing and holds no key.

The workflow refuses to run from anything but `refs/heads/main`. It runs in a
`release` environment that deploys from `main` only, in a single concurrency
group that is never cancelled, so two releases cannot interleave. A
cancellation in the middle of publishing would leave a half-published release
for a person to finish.

The tag-push trigger is gone. Pushing a `v*` tag by hand starts nothing.

### 2. Verify before anything is public

The jobs run in this order:
1. **preflight**
2. **gates**
3. **build** and **image**, in parallel
4. **verify**, on ubuntu and macOS
5. **publish**

Only publish is irreversible.

- **preflight** (`scripts/release-preflight.sh`, the same script the local
  command runs) refuses:
  - a version that is not SemVer, or not newer than every `v*` tag;
  - a version whose tag already exists;
  - a commit that is not `main`'s head;
  - a commit whose CI or Nightly did not succeed;
  - a stable version without a `CHANGELOG.md` section;
  - a `release-notes.md` that does not name the version. A stale file would
    publish the previous release's notes.
- **build** creates the annotated tag *in its own clone only*, so Go stamps
  the module version from VCS exactly as for a tagged build. It then builds the
  archives and attests every archive and `checksums.txt` with
  `actions/attest-build-provenance`.
- **image** scans, pushes **by digest only** (§ 3), and signs that digest.
- **verify** does what a consumer would, on both runner OSes:
  - checksums;
  - the three binaries in that platform's archive;
  - `trustvian version` printing exactly the version;
  - `gh attestation verify` on every archive, pinned to `release.yml`, main and
    the commit;
  - the run action's model-free PASS scenario through `trustvian eval run`,
    from the extracted archive with a clean `HOME` and `PATH`;
  - on Linux, `cosign verify` with the exact identity (§ 4).
- **publish** runs only when every job above succeeded and the run is not a
  dry run. It creates the GitHub Release, which is not a draft, and the image
  tags from the verified digest. Every step checks whether it already
  happened, so *Re-run failed jobs* finishes a partial publish:
  - a leftover draft for the tag is made exactly this release;
  - an image lookup that fails other than "not found" stops the step;
  - `latest` and the floating tags move only for the newest stable version,
    so re-running an older release cannot pull them back.

A dry run is the whole pipeline minus publish. It builds, signs and verifies
real artifacts, so the pipeline can be tested without releasing anything.

### 3. The image is pushed by digest, and tagged by publish

`image` pushes the multi-arch index with `push-by-digest=true` and no tag,
then signs it. No version tag and no release resolve to it, so pushing it
publishes no release. It is not secret: the digest, its `sha256-….sig`
signature tag and the transparency-log entry are visible, as for any signed
image, including those of dry runs and failed runs.

A version tag never moves. If `vX.Y.Z` already points at another digest,
publish refuses rather than overwriting it. Verified before relying on it:
against a local registry, a `--sbom=true --provenance=mode=max` multi-arch
`push-by-digest` build leaves the repository with no tags. Its digest is the
index, with the SBOM readable through `imagetools inspect`, and
`imagetools create` tags that same digest.

### 4. The signing identity is the dispatch on main

Under `workflow_dispatch`, the keyless certificate identity is
`…/.github/workflows/release.yml@refs/heads/main`, with trigger
`workflow_dispatch` and workflow SHA `GITHUB_SHA`. The version is no longer in
the identity. A consumer therefore pins:
- the workflow file and branch;
- the repository and trigger;
- the workflow SHA, set to the commit the version tag points at.

The SHA is what binds a signature to a version. Preflight requires the
released commit to equal `GITHUB_SHA`, which means two things:
- the certificate's workflow SHA *is* the released commit;
- the workflow definition that ran is the one being released.

`verify` checks exactly this identity before anything is published, and the
guides now document it. Images up to `v0.9.0` carry the old identity
(`@refs/tags/vX.Y.Z`, trigger `push`), and the guides keep that form for them.

### 5. A human creates the tag, after verification

The `v*` ruleset lets only a human Organization Admin create a tag. That is
the rule this project's governance rests on (`docs/governance/releases.md`),
and an automation identity may never be added as a bypass actor
(CLAUDE.md, `docs/governance/agents.md`). So the workflow does not create the
tag:
- `publish` waits for it, up to an hour. It refuses a tag that is lightweight
  or that is not at the commit.
- `scripts/release.sh` waits for every job up to `verify` to succeed, then
  asks for confirmation. It then creates the annotated tag through the API with
  the admin's own credential, and watches publish finish.

The confirmation reads the terminal and has no override. A shell without one
(CI, an AI agent's tool call) can dispatch only a dry run, which creates no
tag. `make release` also refuses while another release run is queued or
running, because GitHub's concurrency group would otherwise cancel a queued
run in favor of the new one.

This keeps the property that mattered in the old design: a release cannot
become public without a human decision. It moves that decision to after the
evidence exists.

### 6. No release candidate is needed

A candidate existed to find out whether the pipeline worked on a real tag.
That is now what a dry run and the verify job are for, and a failure costs
nothing: no tag, no draft, no image tag. A prerelease version is still
accepted, marked as a prerelease, and never moves `latest`. It is for putting
a build in front of users, not for testing the pipeline.

## Alternatives considered

- **Keep the tag push and add verification before the draft.** The tag would
  still come first, so a failure would still burn a version.
- **Let the workflow create the tag**, through the GitHub Actions integration
  or an app as a ruleset bypass actor. It is fully autonomous, but it gives an
  automation identity the one power the governance reserves to a human. This
  project's rules forbid adding one, and the trade is not worth it for saving
  one confirmation.
- **Two runs:** build and verify, then a separate publish dispatch that
  downloads the first run's artifacts. That needs no waiting job, but the
  handoff between runs becomes a trust boundary of its own: which run, from
  which ref, at which commit. One run with a waiting step keeps "publish needs
  verify" a property of the workflow graph.
- **A draft that a human publishes.** That was the old seam. With verification
  automated, a human reading a draft adds less than a human confirming a tag at
  a verified commit. Immutable releases also favor publishing once, complete.

## Consequences

- Releasing is `make release VERSION=vX.Y.Z`, plus one confirmation. A dry run
  is the same command with `DRY_RUN=1`.
- Every release now needs a green Nightly on its exact commit. When the
  commit has no Nightly run yet, `make release` starts one on `main` and waits
  for it; it asks first, except for a dry run. A failed or unfinished CI or
  Nightly run is reported and never started over, so a flaky failure needs a
  person to look at it and re-run it.
- Only a version newer than every existing tag can be released. A patch to an
  older line (`release/X.Y`, `docs/governance/branching.md`) is not supported
  by this path. It would need its own preflight rule when it first happens.
- Archives now carry SLSA build provenance, verifiable with
  `gh attestation verify`. That closes the gap `docs/supply-chain.md` recorded.
- Verification of new images uses the dispatch identity (§ 4). Instructions
  for `v0.9.0` keep the tag-push identity.
- One-time setup: the `release` environment and the existing tag ruleset, plus
  Immutable releases. `scripts/release-setup.sh` creates what is missing, and
  `--check` reports without changing anything. It never adds a bypass actor
  and never edits an existing ruleset.
- `release-notes.md` stays on `main` after a release. Preflight refuses the
  next release until the file names the new version.
