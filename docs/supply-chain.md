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

One release identity, shared with the binary release. The container job
`needs` the release job, so it builds from source whose tag was already
validated and whose gates already passed — it does not re-establish trust.

```text
tag push (v*)
  ├─ release job    validate SemVer → verify tagged commit → gates
  │                 → binaries + checksums → draft GitHub Release
  └─ container job  build → scan → gate → push vX.Y.Z (+ SBOM, provenance)
                    → sign digest → move X.Y and latest to that digest
```

Scanning happens **before** publishing, so a known-bad image is never
pushed. Signing follows publishing because a signature is made over a
digest, which exists only once the image is in the registry. The floating
tags move last, by digest and without a rebuild, so they only ever point at
a signed image.

### What the scan covers

The gate scans a `linux/amd64` build, then the multi-arch push reuses that
build's cache — so the scanned content is the amd64 layer that gets
published. The arm64 layer is the same source and the same base image
version, but is not itself scanned before push. This is a deliberate
trade-off: scanning a multi-platform result requires either pushing it
first or adding registry-manipulation tooling to the release path. Stated
here rather than left implicit.

### Partial-failure behavior

The GitHub Release is created as a **draft**. If the container job fails
after the binary job succeeded, the release stays a draft, so an incomplete
release is never presented as finished — publication is a human action
taken after seeing every job's result.

The registry is different: an image is public the moment it is pushed. The
one window that matters is between pushing `vX.Y.Z` and signing it. If
signing fails there, the immutable tag exists unsigned, but `X.Y` and
`latest` have **not** moved, so no one following a floating tag receives an
unsigned image. Recovery for this and every other partial state is in the
[release guide](release-guide.md#the-release-is-not-atomic).

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

## Verifying a published image

These commands apply once a release exists.

```bash
IMAGE=ghcr.io/trustvian/trustvian-collector
TAG=v0.9.0   # substitute a real released tag

# Resolve the immutable digest, and use it for everything below.
DIGEST=$(docker buildx imagetools inspect "$IMAGE:$TAG" --format '{{.Manifest.Digest}}')

# Signature: keyless, so verification asserts *which workflow* signed it.
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity-regexp '^https://github.com/trustvian/trustvian/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# SBOM and provenance attestations.
cosign download attestation "$IMAGE@$DIGEST" | jq -r '.payload' | base64 -d | jq .predicateType
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .SBOM }}'
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .Provenance }}'

# Which architectures the tag covers.
docker buildx imagetools inspect "$IMAGE:$TAG"
```

The `--certificate-identity-regexp` is the part that matters: a keyless
signature is only meaningful together with an assertion about *who* signed,
and pinning it to this repository's workflows is what makes the check
useful.

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
