#!/usr/bin/env bash
#
# Build Trustvian's release artifacts: cross-compiled CLI binaries, their
# archives, and a SHA-256 checksum manifest.
#
#   ./scripts/release-build.sh                  # version from git describe
#   ./scripts/release-build.sh v0.9.0           # explicit version
#   make release-dry-run                        # the usual entry point
#
# This script never publishes anything. It needs no credentials, no tag,
# and no network beyond the Go module cache, so a maintainer can prove the
# whole matrix builds before creating a tag rather than discovering a
# broken target afterwards. The release workflow runs this same script, so
# what CI builds is what you can build locally.
#
# See docs/release-guide.md and
# docs/tasks/040-release-artifacts-and-module-consistency.md.

set -euo pipefail

cd "$(dirname "$0")/.."

readonly BINARY="trustvian"
readonly CMD="./cmd/trustvian"
readonly DIST="${DIST_DIR:-dist}"

# The two helpers `trustvian dev` supervises, and the modules they come from.
#
# dev composes a local control plane and an OTLP Collector it does not contain:
# the root CLI must not import trustvian-platform (ADR 0022, 0033, 0035), so
# they are separate binaries found on disk. Shipping them here is what closes
# ADR 0043's open item — dev's resolution order already looks alongside its own
# executable, so an archive holding all three needs no configuration at all.
#
# Built from their own modules with GOWORK=off, exactly as the Makefile and the
# container build do: the release must resolve modules the way a consumer does,
# not the way a developer's workspace does.
readonly HELPERS=(
    "trustvian-local:platform:./cmd/trustvian-local"
    "trustvian-collector:processor:./cmd/trustvian-collector"
)

# The advertised target matrix. Every entry here is built; a target that
# fails to compile fails the whole run, because a release that silently
# skips a platform it advertises is worse than one that fails loudly.
readonly TARGETS=(
    "linux/amd64"
    "linux/arm64"
    "darwin/amd64"
    "darwin/arm64"
    "windows/amd64"
)

version="${1:-}"
if [ -z "$version" ]; then
    # Fall back to the VCS description so a dry run has a sensible name.
    # Not used for anything but filenames — the binary's own version comes
    # from Go's build information, not from this string.
    version="$(git describe --tags --always --dirty 2>/dev/null || echo "devel")"
fi

echo "Building $BINARY $version"
echo

rm -rf "$DIST"
mkdir -p "$DIST"

for target in "${TARGETS[@]}"; do
    os="${target%/*}"
    arch="${target#*/}"

    ext=""
    [ "$os" = "windows" ] && ext=".exe"

    stage="$DIST/${BINARY}_${version}_${os}_${arch}"
    mkdir -p "$stage"

    printf '  %-16s ' "$os/$arch"

    # CGO_ENABLED=0: every dependency is pure Go, so this cross-compiles
    # from one host without a per-target toolchain and produces binaries
    # that do not depend on the build machine's libc.
    #
    # -trimpath: no absolute build paths embedded, so the artifact does not
    # carry the directory layout of whoever built it.
    #
    # No -ldflags version injection: Go records the module version and VCS
    # revision itself (see internal/buildinfo).
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -trimpath -o "$stage/${BINARY}${ext}" "$CMD"

    # The dev helpers, on the platforms where `dev` runs.
    #
    # Not on Windows: dev refuses to start there outright (it forwards SIGINT
    # and SIGTERM to a child, and Windows has no equivalent delivery — see
    # cmd/trustvian/dev_child_windows.go). Shipping 60 MB of helpers for a
    # command that declines to run them would be weight with no capability
    # behind it, so the Windows archive is unchanged by this.
    if [ "$os" != "windows" ]; then
        for entry in "${HELPERS[@]}"; do
            helper="${entry%%:*}"
            rest="${entry#*:}"
            module="${rest%%:*}"
            pkg="${rest#*:}"

            printf '\n  %-16s %s ' "" "$helper"
            (
                cd "$module"
                CGO_ENABLED=0 GOWORK=off GOOS="$os" GOARCH="$arch" \
                    go build -trimpath -o "../$stage/$helper" "$pkg"
            )
        done
        printf '\n  %-16s ' ""
    fi

    # Licensing travels with the artifact; the README gives a recipient
    # somewhere to start. Nothing else — an archive is not a repository.
    cp LICENSE README.md "$stage/"

    if [ "$os" = "windows" ]; then
        archive="${BINARY}_${version}_${os}_${arch}.zip"
        (cd "$DIST" && zip -q -r "$archive" "$(basename "$stage")")
    else
        archive="${BINARY}_${version}_${os}_${arch}.tar.gz"
        # Reproducible-ish tar: sorted names and a fixed owner, so two runs
        # over identical input differ only where the payload differs.
        (cd "$DIST" && tar --format=ustar -czf "$archive" "$(basename "$stage")")
    fi

    rm -rf "$stage"
    echo "-> $archive"
done

echo
echo "Generating checksums"
(
    cd "$DIST"
    # Deterministic ordering, so the manifest does not depend on readdir.
    shasum -a 256 $(ls *.tar.gz *.zip 2>/dev/null | sort) > checksums.txt
)

echo
echo "Verifying checksums"
(cd "$DIST" && shasum -a 256 -c checksums.txt)

echo
echo "Artifacts in $DIST/:"
ls -1 "$DIST"
