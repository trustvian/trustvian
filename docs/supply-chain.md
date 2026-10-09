# Supply Chain

How Trustvian's official container image is built, verified, and
distributed. Maintainer- and verifier-facing; for running Trustvian see the
[README](../README.md), and for the binary release process see the
[release guide](release-guide.md).

> **Scope note.** This document is about *software supply-chain* security:
> which commit produced an artifact, what is inside it, and how you confirm
> both. That is unrelated to Trustvian's *runtime* behavioral security — and
> unrelated to the separately planned "Runtime Identity & Provenance"
> feature, which concerns the provenance of observed actors, not of builds.

## The official image

```text
ghcr.io/trustvian/trustvian-collector
```

An OpenTelemetry Collector with the Trustvian processor — the long-lived
service this repository produces. The `trustvian` CLI is a batch tool and
ships as a signed-checksum binary instead; see the
[release guide](release-guide.md).

> **No image has been published yet.** The pipeline below is implemented and
> verified locally, and runs on the next version tag. Until then, `docker
> pull` of any tag will fail — the examples in this document describe what
> will work after the first release, not what works today.

### Tags

| Tag | Mutable | Use |
|---|---|---|
| `vX.Y.Z` | **No** | The deployment contract. Pin this, or a digest. |
| `X.Y` | Yes | Tracks patches within a minor version. |
| `latest` | Yes | Convenience. Never a deployment contract. |
| `@sha256:…` | **No** | Strongest reference. What signatures are made over. |

A prerelease (`v0.9.0-rc.1`) publishes **only** its immutable tag. Moving
`latest` or `X.Y` to a release candidate would hand it to everyone tracking
a floating tag.

### Architectures

`linux/amd64` and `linux/arm64`. Both are built and verified; nothing else
is advertised, because an architecture nobody has built is a claim rather
than a feature.

## What is in the image

Built from `Dockerfile` at the repository root, on
`gcr.io/distroless/static-debian12:nonroot`.

| Property | Value |
|---|---|
| Size | ~43 MB |
| User | `nonroot` (uid 65532) |
| Shell | **none** |
| Package manager | **none** |
| Contents | the Collector binary, plus the base image's CA certificates |
| Capabilities | none added |
| Ports | 4317 (OTLP/gRPC), 4318 (OTLP/HTTP), 13133 (health) — all unprivileged |

The base is chosen for two concrete requirements, not for being small:

- **CA certificates are needed.** The Collector dials PostgreSQL, possibly
  with `sslmode=verify-full`, and may export telemetry over TLS. A
  `scratch` image has no trust store, so verification would fail — or worse,
  appear to work while verifying nothing.
- **A shell is not needed.** Operating the Collector is config-in,
  logs-out, with all state in PostgreSQL. A shell would add attack surface
  without adding an operational capability.

The image is verified to contain no Go toolchain, no repository source, and
no `.git`; the build context excludes those via `.dockerignore`.

### Health probes

The runtime serves `/livez` and `/readyz` on port 13133 when a `health:`
block is configured. There is deliberately **no Docker `HEALTHCHECK`** in
the image.

A `HEALTHCHECK` needs an executable inside the container, and this image has
no shell, no `curl`, and no `wget`. Honoring one would mean either adding a
shell — discarding a security property chosen on purpose — or adding a
Trustvian subcommand that exists only to satisfy Docker. Neither is worth
it for a capability every supervisor can provide from outside.

Probe it externally instead:

```bash
curl -fsS http://localhost:13133/readyz    # 200 ready, 503 not ready
curl -fsS http://localhost:13133/livez     # 200 alive, 503 stopping
```

The endpoints are unauthenticated and carry a status string and nothing
else — no DSN, no hostname, no database error. Bind them to an internal
address in deployments where that matters; network placement is the access
control.

### Debugging the image

There is no shell, so `docker exec … sh` will not work. That is deliberate.
Debug through:

- **Logs** — the Collector logs its startup, its configured storage
  backend, and every processing error to stdout.
- **`trustvian version`** on the CLI binary of the same release, to confirm
  which commit an artifact came from.
- **Inspecting the filesystem without a shell**, which needs no rebuild:

  ```bash
  # Everything the image contains, from outside it.
  docker export "$(docker create ghcr.io/trustvian/trustvian-collector:v0.9.0)" | tar -t
  ```

- **A local debug build**, if something genuinely requires a shell: change
  the runtime stage's base to `alpine:3.22` in a local working copy. That is
  deliberately a source edit rather than a build flag — a shell should not be
  one argument away from the released image.

## Pipeline

One release run builds both artifact kinds, from one commit, after one set of
gates. `make release` dispatches it from `main`; nothing becomes public until
`verify` has passed and an Organization Admin has approved the `release`
deployment, after which publish creates the tag
([ADR 0059](adr/0059-releases-are-dispatched-verified-then-published.md),
[ADR 0060](adr/0060-agent-operated-releases-with-environment-approval.md)).

```text
workflow_dispatch on main (version, commit == main head)
  preflight → gates
  ├─ build   archives (CLI + dev helpers) + checksums → build provenance attested
  └─ image   build → scan → gate → push BY DIGEST (+ SBOM, provenance) → sign digest
  verify     ubuntu + macOS: checksums, contents, version, gh attestation verify,
             scenario from the archive; cosign verify the digest
  summary    the approval page: version, derivation, commit, checks, CHANGELOG
  ── an Organization Admin approves the `release` deployment ──
  publish    annotated v* tag at the commit → GitHub Release
             → image vX.Y.Z from the verified digest
             → X.Y and latest (stable only)
```

Scanning happens **before** pushing, so a known-bad image is never pushed.
Signing follows the push because a signature is made over a digest, which
exists only once the image is in the registry. The push carries **no tag**,
so no version and no `latest` resolve to it until `publish` tags exactly the
digest `verify` checked. The digest itself is not secret: it, its signature
tag and its transparency-log entry are visible, for dry runs and failed runs
too. Floating
tags move last, by digest and without a rebuild.

### What the scan covers

The gate scans a `linux/amd64` build, then the multi-arch push reuses that
build's cache — so the scanned content is the amd64 layer that gets
published. The arm64 layer is the same source and the same base image
version, but is not itself scanned before push. This is a deliberate
trade-off: scanning a multi-platform result requires either pushing it
first or adding registry-manipulation tooling to the release path. Stated
here rather than left implicit.

### Partial-failure behavior

Everything before `publish` is invisible to users. A failure there leaves at
most an untagged, signed digest and attestations for archives nobody
received, and the same version is simply released again.

`publish` is the only job that can leave a partial state, and every step in it
checks what already happened, so re-running it finishes the release:
- a draft from an interrupted release creation is completed;
- an image version tag already at the verified digest is left alone, and one
  at another digest is refused, because a version tag never moves.

Recovery is in the
[release guide](release-guide.md#when-something-fails).

## Vulnerability policy

Two scanners covering disjoint ground. No third is added for coverage
either already provides.

| Scope | Tool | Gate |
|---|---|---|
| Go code and dependencies | `govulncheck` | Fails on any **reachable** vulnerability, at any severity |
| Container OS packages and the Go binary | Trivy | Fails on `CRITICAL`/`HIGH` **with a fix available** |

Why these thresholds:

- **`govulncheck` gates on reachability, not severity.** Once code actually
  calls a vulnerable symbol, severity is secondary. Its symbol-level
  analysis is why it is preferred over a graph-only scanner: it
  distinguishes "a vulnerable module is in the dependency graph" from "our
  code can reach the vulnerability", and on this repository that
  distinction is not hypothetical.
- **Trivy ignores unfixed findings.** An advisory with no fixed version
  cannot be resolved by rebuilding, so blocking on it converts an upstream
  problem into an inability to ship our own security fixes. Unfixed
  findings are still reported; they are just not a gate.
- **`MEDIUM`/`LOW` are reported, not gated**, to keep the gate actionable.

### Scanning resolves modules the way a consumer does

Every `govulncheck` invocation — in CI and in `make vulncheck` — runs with
`GOWORK=off`, for all four modules.

This is not a detail. `go.work` is git-ignored, so it exists on developer
machines and not in CI. With a workspace active, Go's minimal version
selection raises each module's dependency versions to satisfy *every*
workspace member, so the root module inherits versions the processor
requires. That masked a real finding: the root module resolved
`golang.org/x/text` at `v0.29.0` on its own — reachable, and vulnerable —
while the workspace silently upgraded it to `v0.41.0` and reported clean.

The module's own build list is what a consumer gets, so it is the only one
worth scanning.

### Current exceptions

| Finding | Scope | Reason | Review condition |
|---|---|---|---|
| `GO-2026-5932` | `golang.org/x/crypto`, indirect dependency of the `processor` module | No fixed version exists upstream, and the vulnerability is not reachable from Trustvian code (`govulncheck` reports 0 reachable) | Whenever a fixed `x/crypto` is released, or if the symbol becomes reachable |

Resolved rather than excepted:

- `GO-2026-6355` and `GO-2026-6354` in `golang.org/x/crypto` — cleared by
  upgrading to `v0.56.0`.
- `GO-2026-5970` in `golang.org/x/text` (infinite loop on invalid input),
  reachable in the **root** module through
  `postgres.NewStore` → `pgxpool.NewWithConfig` → `norm.Form.*` — cleared by
  raising the floor to `v0.41.0`. `pgx/v5 v5.11.0` is the latest release and
  requires the vulnerable `v0.29.0`, so upgrading the owning direct
  dependency could not fix this; an explicit indirect requirement was the
  only available resolution.
- The Go security release of 2026-10-08, cleared by raising what each module
  actually reached, with no exception:
  - `GO-2026-6603`, `GO-2026-6610`, `GO-2026-6611`, `GO-2026-6612` and
    `GO-2026-6617` in `golang.org/x/net/http2`, reachable in the
    **processor** module, which imports `x/net/http2` through the Collector's
    OTLP receiver (`otlpreceiver` → `confighttp`). They were cleared by an
    explicit indirect requirement on `golang.org/x/net v0.60.0`. The
    Collector modules (`configgrpc`, `confighttp`, `otelcol` and others at
    `v0.160.0` / `v1.66.0`) require at most `v0.58.0`, so as with `x/text`,
    upgrading them could not fix it. Minimal version selection raised
    `x/crypto` to `v0.57.0`, `x/sync` to `v0.23.0`, `x/sys` to `v0.48.0` and
    `x/text` to `v0.42.0` with it, because `x/net v0.60.0` requires them.
  - `GO-2026-6603`, `-6605`, `-6607`, `-6608`, `-6610`, `-6611`, `-6612`,
    `-6613` and `-6617` in the standard library, reachable in the
    **examples** module. It pinned `go 1.27.0`, so CI scanned it with that
    toolchain. Its directive is now `go 1.27`, as the other three modules
    declare, and every `actions/setup-go` step sets `check-latest: true`, so
    scans and builds use the newest 1.27 patch: `go1.27.2`, which fixes all
    of them, and `GO-2026-6599`, `-6600`, `-6604` and `-6609` with them. The
    root, platform and processor modules were already scanned with
    `go1.27.2`, and showed nothing from the standard library.
  - The `trustvian-run` and `trustvian-comment` actions build their pinned
    runtime with the Go version in `.github/actions/trustvian-run/runtime.env`.
    That pin moved from `1.27.1` to `1.27.2`, with go.dev's published
    archive digests. The source commit it builds still carries
    `x/net v0.58.0`. Moving it to a commit with this fix is the separate
    reviewed change `runtime.env` describes.

Exceptions are recorded per-finding with a review condition; there are no
time-unbounded blanket ignores, and a finding is excepted only when no
compatible fix exists.

## Signing and attestations

**Signing:** keyless Cosign (Sigstore), using the release workflow's GitHub
OIDC identity. No private key exists in the repository or in a secret, so
there is none to leak or rotate. Signatures are made over the image
**digest**, not a tag, because a tag can later be moved.

The release pipeline installs Cosign through `sigstore/cosign-installer`,
pinned to an exact version (that project publishes no floating major tag —
assuming one existed is what broke `v0.9.0-rc.1`). It currently installs
**Cosign v3**, which stores signatures in the new Sigstore bundle format by
default. **Verify with Cosign v3 or newer**; a v2 client does not read that
format by default and will report no matching signatures.

**Attestations:** BuildKit's built-in SBOM (SPDX 2.3) and SLSA v1
provenance, attached to the image in the registry. Platform-standard
mechanisms rather than a bespoke format, and no extra tool in the release
path. The provenance records which commit, workflow, and builder produced
the image.

### What covers which artifact

Stated plainly, because the two artifact kinds are covered differently and a
reader who assumes otherwise is assuming too much.

| Artifact | Contains | Covered by |
|---|---|---|
| release archives | `trustvian`, and on macOS/Linux `trustvian-local` and `trustvian-collector` | SHA-256 `checksums.txt`, published with the release; from `v0.10.0`, SLSA build provenance for every archive and for `checksums.txt` (`actions/attest-build-provenance`) |
| container image | `trustvian-collector` only | SBOM (SPDX 2.3), SLSA v1 provenance, keyless Cosign signature over the digest |

**The archives carry build provenance from `v0.10.0`, and no SBOM.** The
build job attests every archive and `checksums.txt` with
`actions/attest-build-provenance`, signed keylessly by the same release run.
`verify` checks those attestations with `gh attestation verify` before
anything is published, and anyone can check them afterwards (below). The
archives of `v0.9.0` and earlier have checksums only.

The image contains only `trustvian-collector` because it *is* the Collector
deployment image — its entrypoint is that binary. It does not ship the CLI, and
adding the helpers to the archives did not change what it contains.

## Verifying a published image

These commands apply once a release exists. Use **Cosign v3 or newer** (see
above).

```bash
IMAGE=ghcr.io/trustvian/trustvian-collector
TAG=v0.10.0   # substitute a real released tag

# Resolve the immutable digest, and use it for everything below.
DIGEST=$(docker buildx imagetools inspect "$IMAGE:$TAG" --format '{{.Manifest.Digest}}')
# Without Docker: DIGEST=$(crane digest "$IMAGE:$TAG")

# The commit the tag points at. No clone needed.
SHA=$(git ls-remote https://github.com/trustvian/trustvian "refs/tags/$TAG^{}" | cut -f1)

# Signature: keyless, so verification asserts *which workflow run* signed it.
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity "https://github.com/trustvian/trustvian/.github/workflows/release.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository trustvian/trustvian \
  --certificate-github-workflow-trigger workflow_dispatch \
  --certificate-github-workflow-sha "$SHA"

# SBOM and provenance attestations.
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .SBOM }}'
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .Provenance }}'

# Which architectures the tag covers.
docker buildx imagetools inspect "$IMAGE:$TAG"
```

**The identity is the part that matters.** A keyless signature means nothing
without an assertion about *who* signed. Each flag pins one property of the
certificate Fulcio issued to the signing run:

| Flag | Pins | A signature it rejects |
|---|---|---|
| `--certificate-identity` | the workflow file, `release.yml`, and the ref it ran on, `refs/heads/main` | one made by another workflow file in this repository, or by `release.yml` run from any other branch |
| `--certificate-oidc-issuer` | GitHub Actions as the token issuer | one from any other OIDC provider |
| `--certificate-github-workflow-repository` | `trustvian/trustvian` | one from a fork or a renamed copy |
| `--certificate-github-workflow-trigger` | `workflow_dispatch`, the only event `release.yml` runs on | one from a push, schedule or any other event |
| `--certificate-github-workflow-sha` | the commit the version tag points at | one from a run at any other commit, including an earlier or later release |

**The SHA is what binds the signature to the version.** A release runs from
`main`, so the identity names the branch, not the version. The release's
preflight requires the released commit to be exactly the run's commit, so the
certificate's workflow SHA is the commit the tag points at. A dry run at the
same commit produces a signature that passes too, over an image built from the
same source and checks; it is never tagged, because only `publish` creates
tags, and only from the digest its own run verified.

**Releases up to `v0.9.0` were signed by a tag push.** For those, use the
identity of that pipeline:

```bash
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity "https://github.com/trustvian/trustvian/.github/workflows/release.yml@refs/tags/$TAG" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository trustvian/trustvian \
  --certificate-github-workflow-trigger push \
  --certificate-github-workflow-sha "$SHA"
```

**Why not a regular expression.** Earlier versions of this guide matched
`--certificate-identity-regexp '^https://github.com/trustvian/trustvian/'`.
That accepts a certificate from *any* workflow file in the repository, on
*any* ref, from *any* trigger. Someone who can push a branch that adds a
workflow with `id-token: write` could therefore sign an image that passes,
without running the release pipeline or its gates. The exact identity accepts
only `release.yml`, dispatched on `main`, at this release's commit.

**Attestations are BuildKit's, not Cosign's.** `release.yml` builds with
`--sbom=true --provenance=mode=max`. That attaches the SBOM and the provenance
to the image index as BuildKit attestation manifests, which
`docker buildx imagetools inspect` reads. Cosign signs the digest but attaches
no attestation of its own, so `cosign download attestation` finds none for
this image.

## Who authored a release

From `v0.10.0`, every release and its tag are written by the
`trustvian-release` GitHub App, after an Organization Admin approved the run.
No other actor may create a tag
([ADR 0060 § 3](adr/0060-agent-operated-releases-with-environment-approval.md#3-only-the-release-app-can-create-a-tag)).

```bash
V=v0.10.0
gh api repos/trustvian/trustvian/releases/tags/$V --jq .author.login     # trustvian-release[bot]
ref=$(gh api repos/trustvian/trustvian/git/ref/tags/$V --jq '"\(.object.type) \(.object.sha)"')
echo "$ref"                                                               # tag <sha>: annotated
gh api repos/trustvian/trustvian/git/tags/${ref#tag } --jq '"\(.tagger.name) → \(.object.type) \(.object.sha)"'
```

`.github/workflows/release-audit.yml` checks this for every release, on every
release event and weekly, together with the tag's commit being on `main` and
every asset's provenance. A finding opens an issue labelled `release-audit`.
Releases up to `v0.9.0` predate the App and are exempt by name.

## Verifying a release archive

From `v0.10.0`. `gh attestation verify` checks the archive's SLSA build
provenance against the same run: `release.yml`, dispatched on `main`, at the
tagged commit.

```bash
V=v0.10.0
A=trustvian_${V}_linux_amd64.tar.gz
SHA=$(git ls-remote https://github.com/trustvian/trustvian "refs/tags/$V^{}" | cut -f1)

sha256sum -c checksums.txt --ignore-missing
gh attestation verify "$A" \
  --repo trustvian/trustvian \
  --signer-workflow trustvian/trustvian/.github/workflows/release.yml \
  --source-ref refs/heads/main \
  --source-digest "$SHA" \
  --deny-self-hosted-runners
```

| Flag | Pins |
|---|---|
| `--repo` | attestations stored by `trustvian/trustvian` |
| `--signer-workflow` | signed by `release.yml`, not any other workflow in the repository |
| `--source-ref` | built from `refs/heads/main` |
| `--source-digest` | built at the commit the version tag points at |
| `--deny-self-hosted-runners` | built on a GitHub-hosted runner |

## Local verification

Everything except signing can be exercised without publishing. Signing is
genuinely CI-only — keyless Cosign needs a GitHub OIDC token that exists
only inside a workflow run.

```bash
make container-build   # multi-stage build, nothing pushed
make container-scan    # Trivy, same gate as CI
make sbom              # SPDX SBOM extracted to dist/sbom.spdx.json
make vulncheck         # govulncheck across both modules
```

`make sbom` writes the SBOM to `dist/`, which is git-ignored: SBOMs are
generated per build and belong to the artifact, not to the source tree.

**`make sbom` needs a `docker-container` builder.** Attestations are not
supported by buildx's default `docker` driver, so on a stock Docker
Desktop the target fails with *"Attestation is not supported for the
docker driver."* Create one once:

```bash
docker buildx create --name tv-builder --driver docker-container --bootstrap
docker buildx use tv-builder
```

CI is unaffected — `docker/setup-buildx-action` provisions a container
driver already. `make container-build` and `make container-scan` work on
either driver.

## The reference deployment is unchanged

`deployments/docker-compose/` still builds from source, and must keep
doing so — running the repository should never require a published image,
and demonstrating the source-to-running-system path is that deployment's
purpose.

After releases exist, an operator may substitute the official image:

```yaml
  otel-collector:
    image: ghcr.io/trustvian/trustvian-collector:v0.9.0   # once released
    # build:                     # replaces the from-source build
    #   context: ../..
```

The default stays `build:`.

## Related reading

- [Release guide](release-guide.md) — tags, binaries, module publication
- [Security model](SECURITY.md) — Trustvian's runtime security properties
- [Reference deployment](../deployments/docker-compose/README.md)
- [Task 041](archive/tasks/v0.9/041-container-supply-chain-security.md) — why this is
  shaped the way it is
