# Release Governance

Who may release Trustvian, what a release tag means once it exists, and which
steps require a human rather than automation.

This document is about **authority**. For the procedure — how to cut a
release, what each artifact contains, and how to verify a downloaded one —
see the [Release Guide](../release-guide.md). The split is deliberate: the
guide answers "how do I do it", this answers "who may, and what can never be
undone".

## Release authority

The procedure is the [release runbook](../release-runbook.md). This section
says who decides.

```text
a person, or an agent on their behalf, runs `make release`
        |
        v
release automation: preflight, gates, build, sign, verify,
approval summary                                            <- publishes nothing
        |
        v
an Organization Admin approves the `release` deployment
on GitHub (web or mobile), after reading the summary         <- the human decision
        |
        v
release automation publishes: the v* tag at the verified
commit, the GitHub Release, the image tags
```

Nothing becomes public without a human's approval, made after verification,
on a page that shows what is being approved. Automation does everything
around that decision and makes none of it: it publishes only once a required
reviewer of the `release` environment has approved.

| Action | Who |
|---|---|
| Start a release (`make release-prep`, `make release`) | A maintainer, an Organization Admin, or an AI agent on their behalf |
| Run the release pipeline up to verification | Automation, dispatched by that command |
| **Approve publishing** | **An Organization Admin**, the `release` environment's required reviewers. Never an agent |
| Create the `v*` tag | The release workflow's `publish` job, after that approval, at the verified commit |
| Publish the GitHub Release and image tags | The same job, after the tag |
| Run a dry run | Anyone who may dispatch workflows. It builds, signs and verifies, and publishes nothing |

History: until `v0.9.0`, a human pushed the tag first and published a draft
afterwards. Under [ADR 0059](../adr/0059-releases-are-dispatched-verified-then-published.md),
a human created the tag at a terminal after verification. Under
[ADR 0060](../adr/0060-agent-operated-releases-with-environment-approval.md),
the human decision is the environment approval, and an agent may operate
everything else.

## Protected tag semantics

Two rulesets cover **every tag** (`refs/tags/*`), not only `v*`, so a release
cannot be created on any other tag either
([ADR 0060 § 3](../adr/0060-agent-operated-releases-with-environment-approval.md#3-only-the-release-app-can-create-a-tag)):

- **"Release tags: creation"** restricts creation, with exactly one bypass
  actor: the `trustvian-release` GitHub App. Its private key is a secret of the
  `release` environment, so only the `publish` job, after an Organization
  Admin approved it, can mint its token. No person can create a tag either,
  not even an Organization Admin.
- **"Release tags: immutable"** restricts update, deletion and force-move,
  with **no bypass actor**: not an Organization Admin, not the App, nobody. A
  published tag is permanent, by server-side rule.

Consequences worth stating plainly:

- **No token but the App's can mint a release on a new tag.** Before this,
  anyone with Contents: write could create a tag and a published release in
  one call to GitHub's release API, with no approval. Now that call needs a
  tag the ruleset refuses to create.
- **Every release is authored by `trustvian-release[bot]`**, and the release
  audit (`.github/workflows/release-audit.yml`) checks that, the tag, and the
  assets' provenance on every release event and weekly. Any finding opens an
  issue labelled `release-audit`.
- **The App's key is human-only.** A human creates the App, generates its key,
  stores it in the `release` environment, and rotates it. No agent ever sees or
  handles it ([Agent Governance](agents.md)).

**A burned version stays burned.** If `publish` creates the tag and a later
step fails, re-running it finishes the release at the same commit. If the
commit itself must change, that version can never be released: the tag cannot
be moved or deleted, and preflight refuses a version whose tag exists. Release
the next version.

The live values are readable from the ruleset itself; see
[Repository Governance § Reading the live configuration](repository.md#reading-the-live-configuration)
rather than trusting this paragraph if the two ever disagree.

## Candidate immutability

A release candidate is optional now. Verification runs before the tag exists,
so a failed attempt burns no version number and leaves nothing to explain.
A prerelease version (`vX.Y.Z-rc.N`) is still the way to put a build in front
of users before calling it stable. Once its tag exists, it is evidence rather
than a work surface:

```text
vX.Y.Z-rc.1 published  →  a problem is found
      |
      v
fix branch  →  PR  →  CI  →  human review  →  main gets a NEW commit
      |
      v
vX.Y.Z-rc.2 at the new commit
```

The published tag stays where it is. `v0.9.0` needed three candidates under
the old tag-first pipeline, and the first two remain in the history as the
record of what that pipeline caught.

Candidate numbers only ever increase. Never:

```text
move an existing RC tag
reuse an RC tag
rewrite a stable release tag
silently promote a different commit
```

## Same-SHA promotion

When a published candidate needs no correction, the stable release is cut from
**that same commit**. `make release` refuses any other: it releases main's
head, so main must not have moved since the candidate:

```text
main@abc123  →  v1.2.0-rc.1  →  verified  →  v1.2.0 at abc123
```

If anything changed — source, workflow, dependency, even documentation — the
result is a new candidate, not a promotion:

```text
main@abc123  →  v1.2.0-rc.1  →  failed
main@def456  →  v1.2.0-rc.2  →  verified  →  v1.2.0 at def456
```

The rule exists because the artifacts a candidate verified were built from one
specific tree. Promoting a different commit would ship something nobody
verified while claiming the candidate's evidence.

## What automation may do

The release workflow runs only when dispatched from `main`
(`on: workflow_dispatch`, with the version and the exact commit as inputs).
Given those inputs, it:

- refuses a version that is not SemVer, not newer than every `v*` tag, or
  already tagged; a commit that is not `main`'s head; a commit whose CI or
  Nightly did not pass; a stable version with no `CHANGELOG.md` section; and
  release notes that do not name the version;
- re-runs the full gate set against that commit — format, vet, tests, race,
  PostgreSQL integration, the processor module, `govulncheck`;
- builds the archives, verifies their checksums, and attests their build
  provenance;
- builds, scans and gates the container image, pushes it **by digest only**,
  with SBOM and provenance attestations, and signs the digest keylessly;
- verifies all of it as a consumer would, on Linux and macOS: checksums,
  contents, `trustvian version`, the archives' attestations, the image
  signature, and an end-to-end scenario run from the extracted archive;
- writes an approval summary: the version and how it was derived, the commit,
  the image digest, every check's result, and the CHANGELOG section;
- then, and only once an Organization Admin has approved the `release`
  deployment, creates the annotated tag at that commit, publishes the GitHub
  Release (not a draft) and creates the image tags from the verified digest.
  Floating tags move only for the newest stable release.

What it may **not** do: move or delete a tag; publish without that approval;
decide that a release should happen.

The approval is the deliberate seam. It is given after every check has passed,
by someone who has read what passed, for the one commit the run verified.

## Agent authority

An AI agent may operate a release, end to end up to the approval:

- run gates, inspect CI, verify published artifacts and signatures;
- run `make release-prep`, write the release notes, and open the pull request;
- run `make release`, a dry run or a real one, and `gh run rerun` on a release
  run;
- follow the [runbook](../release-runbook.md), and report where to approve.

An agent may **not**, whatever credential it happens to hold:

- approve or reject a deployment, or run with a token that can;
- create, move, delete, or reuse a `v*` tag, or create a GitHub Release
  itself;
- merge a pull request;
- repair a published release by mutating published history;
- use the Organization Admin bypass of any ruleset.

A failed release is fixed by moving forward — fix branch, pull request, review,
next attempt — never by rewriting what was published. See
[Agent Governance](agents.md).

## Human checklist

Before approving the `release` deployment, on the run's **Approval summary**:

```text
[ ] every check above the summary passed
[ ] the version, and how it was derived, are what this release should be
[ ] the commit is the one I meant to release
[ ] the CHANGELOG section describes this release
[ ] for a stable version after a candidate: main has not moved since it
```

## Related

- [Compatibility Contract](../compatibility.md) — what a release may and may not change
- [Release Guide](../release-guide.md) — the release procedure and verification
- [Repository Governance](repository.md) — rulesets, review, and merge authority
- [Branching Strategy](branching.md) — where release commits come from
- [Agent Governance](agents.md) — agent authority and prohibitions
