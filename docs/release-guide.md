# Release Guide

Maintainer-facing. For using Trustvian, start at the
[README](../README.md); for contributing, see
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Module publication model

This repository contains four Go modules and publishes exactly one. That
distinction is load-bearing and easy to get wrong, so it is written down
here and enforced by `scripts/check-modules.sh`.

| Module | Path | Published | Distribution |
|---|---|---|---|
| root | `github.com/trustvian/trustvian` | **Yes** | Go module + release binaries |
| processor | `trustvian-processor` | No | Built from a clone |
| examples | `trustvian-examples` | No | Read and run in place |
| platform | `trustvian-platform` | No | Built from a clone |

**The root module is the product.** It carries the engine, the public API
(`event`, `config`, `alert`, and the root package), and the `trustvian`
CLI. It is tagged `vX.Y.Z` and must never contain a `replace` directive:
consumers ignore a dependency's replaces, so one in a published `go.mod`
means the module builds differently for everyone else than it does here —
a failure that only appears after the tag is public.

**The processor, examples, and platform modules are repository-internal**,
and not merely "not published yet". Their module paths —
`trustvian-processor`, `trustvian-examples`, `trustvian-platform` — are not
resolvable: `go get trustvian-processor` fails with *"malformed module path:
missing dot in first path element"*. None has ever been tagged.

They differ in how they reach the core, and the difference is worth stating
precisely rather than averaging over:

All three carry `replace github.com/trustvian/trustvian => ../`, which is
exactly right for a module built from this repository rather than fetched from
a proxy.

`platform` additionally requires the root at the **zero placeholder**
(`v0.0.0-00010101000000-000000000000`) that Go writes for a fully replaced
dependency. That is the honest version: the `DecisionRecord` API it consumes
landed after `v0.9.0` and has not been published in any tag, so naming a
released version would assert something untrue. `scripts/check-modules.sh`
recognizes this form explicitly.

`platform`'s module path is additionally load-bearing rather than
conventional: being outside `github.com/trustvian/trustvian` is what makes a
core `internal/*` import a compile error — see
[ADR 0022](adr/0022-core-platform-boundary.md).

Each exists as a separate module for a reason that has nothing to do with
publishing:

- **processor** keeps the heavy OpenTelemetry Collector dependency tree out
  of the root module's graph. `processor/README.md` documents running it
  with `go run ./cmd/trustvian-collector` from a clone.
- **examples** is a module that *cannot* import `internal/*`, which is what
  makes it a real proof that the public API is sufficient for an outside
  consumer.

### Development state versus release state

These are different, and the difference is why the `replace` exists:

| | Root dependency resolves from | Why |
|---|---|---|
| **Development** | the repository (`replace => ../`) | Lets the processor build against root changes that are not released yet — exactly what `v0.8` needed when the processor required `config.StorageConfig` before `v0.8.0` existed |
| **Release** | the module proxy | What any consumer would get |

The processor's declared floor is `v0.8.0`, and that is verified rather
than assumed: with the `replace` removed and `go mod tidy` run, the
processor builds against the published `v0.8.0` from the proxy. The
release contains every root API it uses, so the `replace` is about
developing against unreleased changes, not about any gap in the release.

`scripts/check-modules.sh` enforces the invariants that hold in *both*
states, and there is deliberately no separate release mode: with one
published module and three repository-internal ones, no invariant is
stricter at release time. `release.yml` runs the same check. A mode split
becomes worth adding when a nested module is actually promoted.

### Promoting the processor

The processor is not consumable by anyone who has not cloned this
repository. That is a deliberate current state, not an oversight — no
external consumer exists and nothing documents a path for one.

The trigger to revisit is concrete: someone wanting to build a Collector
containing this processor with `ocb` / `otelcol-builder`, which requires a
resolvable module path. Promoting it then means:

1. Rename the module path to `github.com/trustvian/trustvian/processor`.
2. Remove `replace github.com/trustvian/trustvian => ../`.
3. `require github.com/trustvian/trustvian vX.Y.Z` at a **released**
   version — so the root module must be tagged first. This ordering is not
   optional: a nested module cannot require an unreleased parent.
4. Tag the nested module as `processor/vX.Y.Z`. Go derives a nested
   module's version from a tag prefixed with its directory; a root `vX.Y.Z`
   tag does **not** version it.
5. Keep local development working — most simply through `go.work`, which
   already lists all four modules, rather than a replace in the published
   `go.mod`.

`scripts/check-modules.sh` fails if the path becomes resolvable while the
local replace is still present, so a half-finished promotion cannot merge
quietly.

Step 3 is already satisfied in substance: the processor is verified to
build against the published `v0.8.0`. What remains is the path rename and
the nested tagging, not an API gap.

### Running the module check

```bash
make check-modules
```

It verifies a declared root version from a local git tag when the checkout
has one, and from the module proxy otherwise. `CHECK_MODULES_OFFLINE=1`
forbids the proxy fallback, which is how its own tests stay deterministic
without network access.

CI fetches tags (`fetch-tags: true`) so the offline path is the normal one.
A shallow checkout without tags still works — it just consults the proxy —
and a checkout that can do neither fails with a message naming the cause
rather than blaming the version.

## The module path changed with the organization rename

The GitHub organization was renamed from `Trustvian` to `trustvian`, and the
module path followed it to `github.com/trustvian/trustvian`. Two consequences
are load-bearing at release time:

- **`v0.8.0` and earlier are only resolvable at the old path.** Their
  `go.mod` declares `github.com/Trustvian/trustvian`, and the proxy checks
  that declaration against the requested path. GitHub redirects the *web and
  VCS* URLs after a rename, but that does not make an old tag resolve under
  the new module path.
- **The first tag created after the rename is the first version of the new
  module path.** Until it exists, `go get github.com/trustvian/trustvian`
  has nothing to resolve. Publishing it is what makes every install command
  in the README and the guides work.

`processor/go.mod` still records `require github.com/trustvian/trustvian
v0.8.0` as its API floor. That version exists as a tag in this repository —
which is how `check-modules.sh` verifies it — but not as a published version
of the *new* module path. The `replace` directive means nothing ever fetches
it. Raise the floor to the first release published under the new path when
one exists.

## Releasing

**The procedure is the [release runbook](release-runbook.md):** every scenario
(minor, patch, candidates, security fix, 1.0), every failure, and the
after-release checklist, step by step. [Releasing with Claude
Code](releasing-with-claude-code.md) shows the same procedure run by an agent,
case by case. This section keeps the why.

### Two modes: manual and agent

`make release` runs in one of two modes, chosen by `scripts/release-mode.sh`
and printed on its first line:

- **Manual**: a person at an interactive terminal, outside Claude Code.
  When publish waits for the `release` environment, the command asks
  `[y]es / [n]o, reject / [l]ater` and, on `y` or `n`, reviews the deployment
  as that person through GitHub's `pending_deployments` API. That call lives in
  one function, which re-checks the mode, the terminal and `CLAUDECODE` right
  before making it.
- **Agent**: inside Claude Code (`CLAUDECODE` is set), without an interactive
  terminal, or forced with `MODE=agent`. It never approves: it prints where to
  approve on GitHub and keeps watching.

Manual mode cannot be forced where it does not apply: the command refuses it
inside Claude Code or without a terminal, before anything is dispatched. Each
run records the mode as its `operator` input, in the run name and every job's
summary next to the triggering actor. That record is informational: GitHub's
own record of who triggered the run and who approved the deployment, which
publish's summary lists, is the authoritative one.

```bash
make release-prep BUMP=minor TITLE="…"   # opens the prep PR; a human merges it
make release                             # build, sign, verify; publish after approval
make release DRY_RUN=1                   # build, sign, verify; publish nothing
```

The commands need an authenticated `gh` and git, and nothing else. They build
nothing, hold no key, create no tag and approve nothing, so an AI agent may
run them ([Agent Governance](governance/agents.md)). Nothing irreversible
happens until every check has passed **and an Organization Admin has approved
the `release` deployment on GitHub**. [ADR 0059](adr/0059-releases-are-dispatched-verified-then-published.md)
and [ADR 0060](adr/0060-agent-operated-releases-with-environment-approval.md)
record the design.

**This is the only release path.** `release.yml` runs only on
`workflow_dispatch` from `main`. Pushing a `v*` tag by hand starts nothing, and
a tag pushed that way would make the next `make release` for that version
refuse, because the tag would already exist.

### The version is derived, not typed

`make release-prep` cuts the CHANGELOG section for the next version: one
`BUMP` (patch or minor) above the newest stable tag, computed by
`scripts/release-version.sh`. A major bump is refused in 0.x: leaving 0.x is
`VERSION=v1.0.0`, typed again to confirm. The merged section *declares* the
version.

`make release` releases the declared version, or with `PRE=rc` its next
candidate (`-rc.N`, counted from the existing tags). It prints the version
and how it was derived before anything else. Preflight requires the version's
base to be exactly one bump above the newest stable tag, so a mistyped or
skipped version is refused, and the message names both.

### Before you run it

1. **The release-prep pull request is merged.** It carries:
   - `CHANGELOG.md` with a `## vX.Y.Z` section, cut from `## Unreleased` with
     an empty `## Unreleased` above it;
   - `release-notes.md` at the repository root, the release body, naming the
     version (its candidates use the same notes).
2. **CI and Nightly are green on main's head.** Preflight reads the latest run
   of each for that exact commit:
   - **No Nightly run yet**, the usual case after a merge: `make release`
     starts one on `main` and waits for it with `gh run watch --exit-status`,
     then runs preflight again. In an interactive shell it asks first; under
     `DRY_RUN=1` or without a terminal (an agent) it just does it.
   - **No CI run yet:** CI starts on the merge to `main` and has no manual
     trigger, so `make release` stops and asks you to wait for it.
   - **A failed or unfinished run of either** is reported with its link, never
     started over: a second run would hide the first one's result. Investigate
     it; if it was a flake, *Re-run failed jobs* on that run, then run
     `make release` again.
3. **You are on `main`, clean, and equal to `origin/main`.** The release is
   always `origin/main`'s head.

### What happens

```text
make release [PRE=rc]
  local:  main, clean, == origin/main; resolve and print the version;
          scripts/release-preflight.sh; start Nightly if it has no run
  ↓ dispatch release.yml (version, commit = origin/main head, derivation)
  preflight  SemVer; one bump above the newest stable tag; newer than every
             v* tag; tag absent; commit == main head; CI and Nightly succeeded
             on it; CHANGELOG section; release notes
  gates      modules, gofmt, vet, test, race, PostgreSQL, processor, govulncheck
  build      archives (version stamped by a tag local to the runner), checksums,
             build provenance attested for every archive and checksums.txt
  image      scan (gate), push BY DIGEST (no tag), cosign sign the digest
  verify     ubuntu + macOS: checksums, three binaries, `trustvian version`,
             gh attestation verify, a model-free scenario from the archive;
             cosign verify the digest (ubuntu)
  summary    the page the approver reads: version, derivation, commit, image,
             every check's result, the CHANGELOG section
  ↓ manual mode asks [y]es / [n]o, reject / [l]ater; agent mode prints
    "waiting for approval — approve on GitHub, web or mobile: <run URL>"
  approval   an Organization Admin approves the `release` deployment
  publish    annotated tag vX.Y.Z at the commit → GitHub Release (not a draft)
             → image vX.Y.Z from the verified digest → X.Y and latest (newest
             stable only)
  ↓
  the script prints the release URL, or the failed job and its log
```

Every job up to `summary` runs in the `release-build` environment, which has
no reviewers. Only `publish` runs in `release`, whose required reviewers are
the Organization Admins, so GitHub holds it until one approves. Self-review is
allowed on purpose: a sole maintainer must be able to approve the release they
started.

**Version.** SemVer: `vMAJOR.MINOR.PATCH`, optionally `-prerelease`. Build
metadata (`+…`) is refused, because `+` is not valid in an image tag. A
prerelease is marked as one, never becomes *Latest*, and never moves `X.Y` or
`latest`. A patch to an older line is not supported by this path.

**No release candidate is needed to test the pipeline.** A failure before
publish creates no tag, no release and no image tag, so nothing needs a new
number, and a dry run is the rehearsal. Candidates (`PRE=rc`) are for putting
a build in front of users.

**The tag.** `publish` creates the annotated tag at the verified commit, after
the approval, with a token for the `trustvian-release` GitHub App, and writes
the GitHub Release with it too, so every release is authored by
`trustvian-release[bot]`. The App is the only actor allowed to create any tag,
and only the approved publish job holds its key. Nobody can move or delete a
tag ([Release Governance](governance/releases.md#protected-tag-semantics)).
`make release` also refuses while another release run is queued or running.

### Dry runs

`DRY_RUN=1` runs everything up to and including the approval summary against
real infrastructure: real archives, real attestations, and a real signed
image, pushed by digest. It then skips publish. Nothing becomes public:
- there is no tag and no release;
- the image digest has no version tag;
- the attestations name a commit and no version.

Run one after any change to the release pipeline, and whenever you want to
know that a release would pass before you commit to it.

`make release-dry-run` is the local counterpart and needs no infrastructure:
it builds the archive matrix with `scripts/release-build.sh` and checks the
checksums. CI runs it on every change.

Preflight still applies to a dry run, including green CI and Nightly and the
release notes naming the version.

### When something fails

The runbook's [§ 4](release-runbook.md#4-when-something-goes-wrong) has the
procedure for each case. In short:

| Fails in | What exists | What to do |
|---|---|---|
| Local checks or preflight | Nothing, or a Nightly run it started | Fix what it names, then run it again |
| gates, build, image, verify or summary | At most an untagged, signed image digest, and attestations for archives nobody received | Fix on a new commit, by pull request, then run `make release` again for the **same version**. No tag was created, so no number is burned |
| Approval rejected, or never given | Same as above | Run it again when ready |
| publish | The tag, and possibly a release or image tags | `gh run rerun <id> --failed`, and approve again. Each publish step checks what already happened and finishes the rest |

`publish`'s steps are idempotent:
- An existing tag is accepted only if it is publish's own annotated tag at the
  commit; anything else stops the step before anything is published.
- A draft for the tag (an interrupted `gh release create`, or any other) is
  made exactly this release before it is published: stray assets removed, every
  asset uploaded, and the title and notes reset. A published release is checked
  to have every asset.
- An image tag lookup that fails for any reason other than "not found" stops
  the step rather than being read as "absent".
- An image version tag already at the digest is left alone. At another digest,
  the step refuses: a version tag never moves.
- The floating tags move only when this is the newest stable release, so
  re-running an older release's publish never pulls `latest` back.

Never delete or move a release tag, and never delete a published release, to
"fix" one. Released versions are immutable; ship the next version instead.

### One-time setup

```bash
./scripts/release-setup.sh --check   # read-only: what differs
./scripts/release-setup.sh           # a human administrator: make it so
```

The script is idempotent and sets the whole desired state, replacing what
differs:

- [ ] **`release-build` environment**, deployable from `main` only, no
  reviewers. Every job before publish runs in it.
- [ ] **`release` environment**, deployable from `main` only, with the
  organization's admins as required reviewers (GitHub allows six), no wait
  timer, and self-review allowed.
- [ ] **The `trustvian-release` GitHub App**, created by a human: its id in the
  repository variable `RELEASE_APP_ID`, its private key in the `release`
  environment's secret `RELEASE_APP_PRIVATE_KEY`
  ([runbook § 0](release-runbook.md#the-release-app-human-only)). The script
  checks both, the secret by name only.
- [ ] **"Release tags: creation"** on every tag: creation restricted, the App
  the only bypass actor. **"Release tags: immutable"** on every tag: update,
  deletion and force-move restricted, no bypass actor
  ([why](governance/releases.md#protected-tag-semantics)). They replace the
  single "Protect release tags" ruleset, which the script removes once both
  are in place.
- [ ] **Immutable releases**, enabled when the repository offers it (Settings
  → General → Releases). Once a release is published, its assets and tag cannot
  change. That is why publish uploads everything before publishing, and why a
  failed publish is finished rather than redone.
- [ ] **The GHCR package is public.** GitHub creates a new container package as
  private on its first push. Check it once at
  `https://github.com/orgs/trustvian/packages/container/package/trustvian-collector`.
- [ ] **Agents have their own token** with no Deployments, Administration,
  Environments or Workflows permission
  ([runbook § 0](release-runbook.md#an-agents-token)).

## Artifacts

The `trustvian` CLI, and on macOS and Linux the two helpers `trustvian dev`
supervises. Libraries are distributed as Go modules rather than as binaries.

| OS | Arch | Archive |
|---|---|---|
| linux | amd64 | `trustvian_<version>_linux_amd64.tar.gz` |
| linux | arm64 | `trustvian_<version>_linux_arm64.tar.gz` |
| darwin | amd64 | `trustvian_<version>_darwin_amd64.tar.gz` |
| darwin | arm64 | `trustvian_<version>_darwin_arm64.tar.gz` |
| windows | amd64 | `trustvian_<version>_windows_amd64.zip` |

Each archive holds `LICENSE`, `README.md`, and:

| Binary | In which archives | What it is |
|---|---|---|
| `trustvian` | all five | the CLI |
| `trustvian-local` | macOS and Linux | the local control plane |
| `trustvian-collector` | macOS and Linux | the OTLP receiver and the Trustvian processor |

Nothing else — an archive is not a repository.

**Why the two helpers ship.** `trustvian dev` supervises a control plane and
an OTLP Collector it does not contain: the root CLI must not import
`trustvian-platform` (ADR 0022, 0033, 0035), so they are separate binaries
found on disk. dev's resolution order looks alongside its own executable, so
an archive holding all three makes `dev` work from a download with no
checkout and no configuration. This closes the open item
[ADR 0043](adr/0043-dev-provisions-the-local-hierarchy-from-the-repository.md)
recorded.

**Not on Windows**, where `dev` refuses to start at all — it forwards SIGINT
and SIGTERM to a child and Windows has no equivalent delivery. Shipping 60 MB
of helpers for a command that declines to run them would be weight with no
capability behind it, so the Windows archive is unchanged.

**The cost, measured rather than estimated.** A macOS/Linux archive goes from
roughly 8.8 MB compressed to roughly 40 MB: `trustvian` is 18.9 MB uncompressed,
`trustvian-local` 21.3 MB, and `trustvian-collector` 39.5 MB. ADR 0043
predicted "triples"; the measured figure is about 4.5x compressed, because an
OpenTelemetry Collector build is larger than the CLI it accompanies.

A consumer who wants only the CLI can delete the other two, or use
`go install`, which builds the root module's command alone.

Built with `CGO_ENABLED=0` (every dependency is pure Go, so the matrix
cross-compiles from one runner and the binaries carry no libc dependency)
and `-trimpath` (no build-machine paths embedded). There is no `-ldflags`
version injection: `trustvian version` reads Go's own build information,
which records the module version, VCS revision, commit time, and whether
the tree was dirty.

**Byte-for-byte reproducibility is not claimed.** The build avoids the
obvious sources of nondeterminism, but this has not been tested, and an
untested reproducibility claim is worse than none.

## Verifying a published release

```bash
V=v0.10.0
A=trustvian_${V}_linux_amd64
curl -sLO https://github.com/trustvian/trustvian/releases/download/$V/$A.tar.gz
curl -sLO https://github.com/trustvian/trustvian/releases/download/$V/checksums.txt

# Integrity
sha256sum -c checksums.txt --ignore-missing

# Provenance: built by release.yml, dispatched on main, at the tagged commit
SHA=$(git ls-remote https://github.com/trustvian/trustvian "refs/tags/$V^{}" | cut -f1)
gh attestation verify $A.tar.gz \
  --repo trustvian/trustvian \
  --signer-workflow trustvian/trustvian/.github/workflows/release.yml \
  --source-ref refs/heads/main \
  --source-digest "$SHA"

# The binary reports the version
tar -xzf $A.tar.gz && ./$A/trustvian version

# The Go module resolves at the tag
cd "$(mktemp -d)" && go mod init probe && go get github.com/trustvian/trustvian@$V
```

The container image: [supply-chain.md § Verifying a published
image](supply-chain.md#verifying-a-published-image).

Archives from `v0.10.0` on carry SLSA build provenance. `v0.9.0` and earlier
have checksums only.

## After the release

- Open a pull request that opens the next `## Unreleased` work, updates
  milestone status in [`docs/ROADMAP.md`](ROADMAP.md), and bumps pins that name
  a release (`.github/actions/trustvian-run/runtime.env`).
- Leave `release-notes.md` in place. Preflight refuses the next release until
  it names the next version, which is when to replace it.
- Leave [`README.md`](../README.md) alone. It is deliberately evergreen and
  carries no version status.
