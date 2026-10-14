# The official Trustvian Collector image: an OpenTelemetry Collector with
# the Trustvian processor, which is the long-lived service this repository
# produces. The `trustvian` CLI is a batch tool and ships as a binary
# instead (see docs/release-guide.md).
#
# Published as ghcr.io/trustvian/trustvian-collector by
# .github/workflows/release.yml on a version tag. This file is also
# buildable locally with `make container-build`, which pushes nothing.
#
# Distinct from deployments/docker-compose/Dockerfile.collector, which
# remains the reference deployment's from-source build. Both are kept
# deliberately: a developer must be able to run the repository without a
# published image.
#
# See docs/supply-chain.md and
# docs/tasks/041-container-supply-chain-security.md.

# Pinned to the toolchain go.mod declares.
FROM golang:1.27-alpine AS build

WORKDIR /src

# Module files first, so dependency download caches independently of source
# edits. Both modules are needed: processor/go.mod replaces the core module
# with ../, so the core module's source must be present in this context.
COPY go.mod go.sum ./
COPY processor/go.mod processor/go.sum ./processor/
RUN cd processor && go mod download

COPY . .

# TARGETOS/TARGETARCH are supplied by buildx per platform, which is what
# makes one Dockerfile produce a multi-architecture image. Declared as
# build args rather than read from the build host, so a cross-build cannot
# silently produce a host-architecture binary.
ARG TARGETOS
ARG TARGETARCH

# The release version, passed by release.yml's image job. Written into the
# binary's main.version, which trustvian-collector reports in place of Go's
# "(devel)": Go cannot stamp it from a tag, both because .git is not in this
# context and because the processor module's path is not under the
# repository's root module (ADR 0022). Empty — `make container-build` — stamps
# nothing.
ARG VERSION=

# CGO_ENABLED=0 for a static binary — required by the distroless static
# base below, and what lets one builder cross-compile every platform.
# -trimpath so no build-host paths are embedded.
#
# The only -ldflags is the release version above, when one is given; the
# revision still comes from Go's own build information.
# GOWORK=off explicitly, in addition to .dockerignore excluding go.work:
# the container build must resolve modules the way a consumer does, not the
# way a developer's workspace does. Stated here so re-adding go.work to the
# context cannot silently change how this image is built.
RUN cd processor && \
    CGO_ENABLED=0 GOWORK=off GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath ${VERSION:+-ldflags=-X=main.version=${VERSION}} \
    -o /out/trustvian-collector ./cmd/trustvian-collector

# ---------------------------------------------------------------------
# Runtime: distroless static, non-root.
#
# `static` (not `base`) because the binary is static — there is no libc to
# provide. The image is chosen for two concrete requirements rather than
# for size:
#
#   - It carries CA certificates. The Collector dials PostgreSQL, possibly
#     with sslmode=verify-full, and may export telemetry over TLS. A
#     `scratch` image has no trust store, which would break verification
#     quietly.
#   - It has no shell and no package manager. Operating the Collector is
#     config-in / logs-out with all state in PostgreSQL, so a shell adds
#     attack surface without adding an operational capability.
#
# The accepted cost is that `docker exec` cannot open a shell; see
# docs/supply-chain.md § Debugging the image.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/trustvian-collector /usr/local/bin/trustvian-collector

# uid 65532, provided by the :nonroot tag rather than created here — one
# fewer step to get wrong.
USER nonroot:nonroot

# OTLP/gRPC, OTLP/HTTP, and the operational health endpoints (/livez,
# /readyz) when a `health:` block is configured. Documentation only;
# publishing ports is the deployment's job. All are above 1024, so no
# capability is needed.
#
# No HEALTHCHECK: this image has no shell and no curl by design, and a
# Docker healthcheck needs an executable inside the container. Adding either
# would discard a deliberate security property to satisfy a convenience.
# External HTTP probes against /readyz are the intended mechanism — see
# docs/supply-chain.md § Health probes.
EXPOSE 4317 4318 13133

ENTRYPOINT ["/usr/local/bin/trustvian-collector"]
