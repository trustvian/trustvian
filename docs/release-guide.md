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

**The processor and examples modules are repository-internal**, and not
merely "not published yet". Their module paths — `trustvian-processor`,
`trustvian-examples` — are not resolvable: `go get trustvian-processor`
fails with *"malformed module path: missing dot in first path element"*.
Neither has ever been tagged. Both carry `replace
github.com/trustvian/trustvian => ../`, which is exactly right for a
module built from this repository rather than fetched from a proxy.

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
published module and two repository-internal ones, no invariant is
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

## Preparing a release

### 1. Dry run first

```bash
make release-dry-run
```

Builds every advertised target, archives them, and generates and verifies
checksums — with no tag, no credentials, and no upload. Run this before
creating a tag; a target that fails to compile should be found now, not
after a tag is public. CI also runs this on every push.

### 2. Confirm the gates pass

```bash
make check                 # gofmt, vet, build, race
make check-modules         # module publication invariants
make integration-postgres  # PostgreSQL integration and stress tiers
make vulncheck             # reachable vulnerabilities, all modules
```

Then confirm, **on the exact commit you will tag**:

- **CI is green** — every job in `ci.yml`, including *Backup, restore &
  upgrade*, which is the only automated proof of the upgrade path from the
  previous release, and *Workflow action references*, which proves every
  action the release workflow uses actually exists. The release workflow
  runs only on a tag, so an unresolvable action reference is otherwise
  found only after the tag is public — that is how `v0.9.0-rc.1` failed.
- **Nightly is green** — the PostgreSQL stress tier with database-restart
  durability, the reference deployment smoke test, and the recovery drill.
  The scheduled run covers `main`; if the release commit is newer than the
  last run, start one from the Actions tab (*Nightly → Run workflow*).
- **`make recovery-drill`** passes locally if Docker is available — it is
  the end-to-end proof that a backup of this release restores and serves.

The release workflow re-runs format, vet, tests, race, PostgreSQL
integration, the processor module, module consistency, and the
vulnerability scan against the tagged commit. It does **not** re-run the
backup/restore/upgrade tier, the examples module, or the nightly tiers,
which is why this step asks for green runs of those on the same commit.

### 3. Prepare the notes

Release notes come from [`CHANGELOG.md`](../CHANGELOG.md), not from a
generated commit dump. Rename the `## Unreleased` heading to the version
being released — the changelog's own convention is that this rename
happens only once a real tag exists, so it is part of releasing, not part
of preparing.

To control the release body exactly, put it in `release-notes.md` at the
repository root before tagging; the workflow uses it when present.

### 4. Choose the version

Semantic versioning, matching the compatibility promise in `CHANGELOG.md`.
The workflow rejects any tag that is not `vMAJOR.MINOR.PATCH` with an
optional prerelease or build suffix, so `v0.9.0` and `v0.9.0-rc.1` are
valid and `0.9` or `v0.9` are not.

### 5. Tag and push

Release tags are cut from `main`, which every change reaches by pull
request — see [Branching Strategy](governance/branching.md). Tag the commit on
`main` that the candidate verified:

```bash
git checkout main && git pull origin main
git tag -a v0.9.0 -m "Trustvian v0.9.0"
git push origin v0.9.0
```

Pushing the tag triggers `.github/workflows/release.yml`.

## What the automation does

| Step | Behavior |
|---|---|
| Trigger | Push of a tag matching `v*`. Never a branch push. |
| Tag validation | Rejects non-SemVer tags before building anything. |
| Source | Checks out `github.ref` — the tagged commit — and **verifies** `HEAD` equals the commit the tag points at. |
| Gates | Re-runs module consistency, format, vet, tests, race, PostgreSQL integration (`-short`), the processor module with `GOWORK=off`, and `govulncheck` for the root and processor modules, against the tagged source. |
| Prerelease | A tag with a prerelease suffix (`v0.9.0-rc.1`) is marked as a prerelease, so it never becomes GitHub's "Latest release", and the floating image tags are left alone. |
| Artifacts | `scripts/release-build.sh` — the same script `make release-dry-run` runs. |
| Checksums | SHA-256 manifest, generated and verified. |
| Publish | Creates a **draft** GitHub Release with the archives and `checksums.txt` attached. |
| Container | After the release job succeeds: build → Trivy scan (gate) → push `vX.Y.Z` → keyless Cosign signature → move `X.Y` and `latest` to the signed digest. See [supply-chain.md](supply-chain.md). |

The release is a draft on purpose: a human reviews the notes against the
changelog and presses publish. That is the last cheap moment to catch a
wrong version or an incomplete changelog.

If any target fails to build, the script exits non-zero and the publish
step never runs — no draft is created for a partial binary matrix.

**Permissions.** `ci.yml` and `nightly.yml` are `contents: read`. The
release job takes `contents: write`, which is what creating a release
requires, and nothing more. Only the container job takes `packages: write`
(to push to GHCR, with the workflow-scoped token) and `id-token: write` (for
keyless signing). No workflow uses `pull_request_target`.

### The release is not atomic

Two jobs publish to two places, so a failure can leave one half done. None
of these states is presented to users as a finished release, because the
GitHub Release stays a draft until a maintainer publishes it — but the
registry is public as soon as an image is pushed.

| Failure | What exists afterwards | Recovery |
|---|---|---|
| A gate or binary build fails | Nothing | Fix on a new commit; tag it as the next patch version |
| Container scan fails | Draft release; **no image** | Fix (usually a base-image or dependency bump) and release the next patch version. Delete the draft. |
| Push fails | Draft release; no complete image | *Re-run failed jobs* on the workflow run — the build is repeatable from the same tag |
| **Signing fails after push** | Draft release; `vX.Y.Z` pushed **unsigned**; `X.Y` and `latest` **not moved** | *Re-run failed jobs*: it rebuilds, re-pushes `vX.Y.Z`, and signs. Do not publish the draft until `cosign verify` succeeds for the digest the summary reports |
| Floating-tag promotion fails | Draft release; signed `vX.Y.Z`; floating tags still on the previous release | Re-run failed jobs, or retag by digest: `docker buildx imagetools create -t ghcr.io/trustvian/trustvian-collector:latest ghcr.io/trustvian/trustvian-collector@<digest>` |

Floating tags move only after signing succeeds, so anyone pulling `latest`
or `X.Y` always gets a signed image — the one ordering guarantee that
matters for users who do not verify.

Never move or re-push an existing release tag in git to "fix" a release.
Released versions are immutable; ship the next patch instead.

### Before publishing the draft

1. Every job in the release run is green.
2. The image verifies — `cosign verify` as in
   [supply-chain.md § Verifying a published image](supply-chain.md#verifying-a-published-image).
3. **The package is public.** GitHub creates a new container package as
   private on its first push. Check
   `https://github.com/orgs/trustvian/packages/container/package/trustvian-collector`
   and, the first time only, set its visibility to public — otherwise
   `docker pull` fails for everyone else.
4. The notes match `CHANGELOG.md`.

## Artifacts

Only the `trustvian` CLI is packaged. The processor's binaries belong to a
non-published module, and libraries are distributed as Go modules rather
than as binaries.

| OS | Arch | Archive |
|---|---|---|
| linux | amd64 | `trustvian_<version>_linux_amd64.tar.gz` |
| linux | arm64 | `trustvian_<version>_linux_arm64.tar.gz` |
| darwin | amd64 | `trustvian_<version>_darwin_amd64.tar.gz` |
| darwin | arm64 | `trustvian_<version>_darwin_arm64.tar.gz` |
| windows | amd64 | `trustvian_<version>_windows_amd64.zip` |

Each archive holds the binary, `LICENSE`, and `README.md` — nothing else.

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
# Checksums
curl -sLO https://github.com/trustvian/trustvian/releases/download/v0.9.0/checksums.txt
curl -sLO https://github.com/trustvian/trustvian/releases/download/v0.9.0/trustvian_v0.9.0_linux_amd64.tar.gz
sha256sum -c checksums.txt --ignore-missing

# The binary identifies the commit it was built from
tar -xzf trustvian_v0.9.0_linux_amd64.tar.gz
./trustvian_v0.9.0_linux_amd64/trustvian version

# The Go module resolves at the tag
cd "$(mktemp -d)" && go mod init probe
go get github.com/trustvian/trustvian@v0.9.0
```

The CLI archives are integrity-protected by `checksums.txt`, which is
attached to the same release; they are **not** individually signed. The
container image is signed and carries SBOM and provenance attestations —
see [supply-chain.md § Verifying a published
image](supply-chain.md#verifying-a-published-image).

The `v0.9.0` commands above are examples for the first release that ships
these artifacts; substitute a real released version.

## After the release

- Rename `## Unreleased` in `CHANGELOG.md` to the released version.
- Update milestone status in [`docs/ROADMAP.md`](ROADMAP.md).
- Leave [`README.md`](../README.md) alone — it is deliberately evergreen and
  carries no version status.
