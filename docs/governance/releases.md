# Release Governance

Who may release Trustvian, what a release tag means once it exists, and which
steps require a human rather than automation.

This document is about **authority**. For the procedure — how to cut a
release, what each artifact contains, and how to verify a downloaded one —
see the [Release Guide](../release-guide.md). The split is deliberate: the
guide answers "how do I do it", this answers "who may, and what can never be
undone".

## Release authority

```text
Human Organization Admin runs `make release VERSION=vX.Y.Z`
        |
        v
release automation: preflight, gates, build, sign, verify   <- publishes nothing
        |
        v
Human Organization Admin confirms, and the script creates
the protected v* tag at the verified commit                  <- the only way a release begins
        |
        v
release automation publishes: GitHub Release, image tags
```

A release begins with a human decision and cannot become public without a
second one, made after verification. Automation does the work in between and
decides nothing: it never creates the tag, and it publishes nothing until the
tag exists.

| Action | Who |
|---|---|
| Decide that a release happens | Human Organization Admin, by running `make release` |
| Run the release pipeline | Automation, dispatched by that command |
| Create a `v*` tag | Human Organization Admin — nobody else can. `scripts/release.sh` creates it with the admin's own credential, after verification and confirmation |
| Publish the GitHub Release and image tags | Automation, and only once that tag exists at the verified commit |
| Run a dry run | Anyone who may dispatch workflows. It builds, signs and verifies, and publishes nothing |

Until `v0.9.0` the order was reversed: a human pushed the tag first, the
pipeline ran on it, and a human published a draft afterwards. Verification
now happens before the tag exists, so a failed attempt leaves no tag and no
draft behind. [ADR 0059](../adr/0059-releases-are-dispatched-verified-then-published.md)
records why.

## Protected tag semantics

Tags matching `v*` are covered by a repository ruleset that restricts
**creation, update, deletion, and force-move**. A human Organization Admin is
the authorized bypass actor; every other identity — contributors, agents, CI,
`GITHUB_TOKEN` — is refused.

Two consequences worth stating plainly:

- **No workflow can mint a release.** The publish job holds `contents: write`,
  which would otherwise be enough to create a tag. It is not an Organization
  Admin, so the ruleset refuses it, and it does not try: it waits for the tag
  a human creates. No bypass entry exists for `GITHUB_TOKEN`, the GitHub
  Actions integration, or any app, and none may be added
  ([Agent Governance](agents.md)).
- **A published tag is permanent.** Not by convention — by server-side rule.

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
(`on: workflow_dispatch`, with the version and the exact commit as inputs), and
it never creates a tag. Given those inputs, it:

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
- then, and only once a human has created the tag at that commit, publishes
  the GitHub Release (not a draft) and creates the image tags from the
  verified digest. Floating tags move only for a stable release.

What it may **not** do: create, move, or delete a tag; publish before the
human's tag exists; decide that a release should happen.

The human tag is the deliberate seam. It is created after every check has
passed, by someone who has seen them pass, at the one commit they verified.

## Agent authority

An AI agent may prepare and verify a release:

- run gates, inspect CI, verify published artifacts and signatures;
- run a release dry run (`make release VERSION=… DRY_RUN=1`), which publishes
  nothing and creates no tag;
- draft release notes and open a pull request for them;
- report that a candidate is ready for a human decision.

An agent may **not**, whatever credential it happens to hold:

- create, move, delete, or reuse a `v*` tag — including by running
  `make release` without `DRY_RUN`, whose confirmation step creates one;
- publish or edit a GitHub Release;
- promote a candidate to stable;
- repair a failed release by mutating published history;
- use the Organization Admin bypass to do any of the above.

A failed release is fixed by moving forward — fix branch, pull request, review,
next candidate — never by rewriting what was published. See
[Agent Governance](agents.md).

## Human checklist

`make release` checks most of this itself. Before answering its confirmation:

```text
[ ] every job up to verify succeeded (the script waits for this)
[ ] the commit it names is the one I meant to release
[ ] CHANGELOG.md and release-notes.md describe this release
[ ] for a stable version after a candidate: main has not moved since it
```

## Related

- [Compatibility Contract](../compatibility.md) — what a release may and may not change
- [Release Guide](../release-guide.md) — the release procedure and verification
- [Repository Governance](repository.md) — rulesets, review, and merge authority
- [Branching Strategy](branching.md) — where release commits come from
- [Agent Governance](agents.md) — agent authority and prohibitions
