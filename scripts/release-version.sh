#!/usr/bin/env bash
#
# Which version to release, derived rather than typed.
#
#   ./scripts/release-version.sh latest-stable          # newest stable v* tag
#   ./scripts/release-version.sh next patch|minor|major [vX.Y.Z]
#   ./scripts/release-version.sh declared <commit>      # from CHANGELOG.md at <commit>
#   ./scripts/release-version.sh next-rc <vX.Y.Z>       # next candidate of a version
#   ./scripts/release-version.sh resolve <commit>       # what `make release` releases
#
# `resolve` reads VERSION and PRE from the environment, as `make release`
# passes them, and prints two lines: the version, and how it was derived.
#
# Tags come from the remote (`git ls-remote`), never from the local clone, so
# a stale clone cannot derive a version someone already released. The SemVer
# helpers are release-preflight.sh's, so a version this script derives is
# compared exactly as preflight will compare it. See
# docs/adr/0060-agent-operated-releases-with-environment-approval.md.

set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source-path=SCRIPTDIR source=release-preflight.sh
source ./scripts/release-preflight.sh

# remote_tags: every v* tag on the remote, one per line.
remote_tags() {
    local out
    out="$(git ls-remote --tags --refs "$REMOTE" 'v*')" || fail "could not list the remote's tags"
    local line
    while IFS= read -r line; do
        if [ -n "$line" ]; then printf '%s\n' "${line##*refs/tags/}"; fi
    done <<<"$out"
}

# next_version LATEST BUMP [TARGET]: the version one BUMP above LATEST. With
# no stable tag yet (LATEST empty), counting starts from v0.0.0.
#
# A major bump in 0.x is refused unless TARGET is exactly v1.0.0: leaving 0.x
# is a decision (the v1.0 exit criteria and the compatibility contract), not
# something a bump keyword should do by accident.
next_version() {
    local latest="${1:-v0.0.0}" bump="$2" target="${3:-}"
    local p m M
    read -r p m M <<<"$(bumps "$latest")"
    case "$bump" in
        patch) echo "$p" ;;
        minor) echo "$m" ;;
        major)
            if [[ "$latest" == v0.* ]] && [ "$target" != v1.0.0 ]; then
                fail "a major bump from $latest is refused in 0.x; leaving 0.x is explicit: VERSION=v1.0.0"
            fi
            echo "$M"
            ;;
        *) fail "BUMP must be patch, minor or major, not '$bump'" ;;
    esac
}

# declared_from FILE: the first "## vX.Y.Z" heading below "## Unreleased".
declared_from() {
    awk '
        /^## Unreleased[[:space:]]*$/ { seen = 1; next }
        seen && /^## v[0-9]/ { sub(/^## /, ""); print $1; exit }
    ' "$1"
}

# declared_version COMMIT: the version the CHANGELOG at COMMIT declares.
declared_version() {
    local commit="$1" tmp version
    tmp="$(mktemp)"
    git show "$commit:CHANGELOG.md" >"$tmp" 2>/dev/null || { rm -f "$tmp"; fail "CHANGELOG.md is missing at $commit"; }
    version="$(declared_from "$tmp")"
    rm -f "$tmp"
    [ -n "$version" ] || fail "CHANGELOG.md at $commit declares no \"## vX.Y.Z\" section below \"## Unreleased\""
    is_semver "$version" || fail "CHANGELOG.md at $commit declares '$version', which is not vMAJOR.MINOR.PATCH"
    is_prerelease "$version" && fail "CHANGELOG.md declares the prerelease $version; declare the stable version and use PRE=rc"
    echo "$version"
}

# next_rc VERSION TAG...: VERSION-rc.N, one above the highest existing rc of
# VERSION. Counted numerically, so rc.10 follows rc.9.
next_rc() {
    local version="$1" tag n max=0
    shift
    for tag in "$@"; do
        case "$tag" in
            "$version"-rc.*)
                n="${tag#"$version"-rc.}"
                [[ "$n" =~ ^(0|[1-9][0-9]*)$ ]] || continue
                ((10#$n > max)) && max=$((10#$n))
                ;;
        esac
    done
    echo "$version-rc.$((max + 1))"
}

# resolve COMMIT: the version `make release` releases, from $VERSION and $PRE.
resolve() {
    local commit="$1" tags version how
    tags="$(remote_tags)" || exit 1
    if [ -n "${VERSION:-}" ]; then
        is_semver "$VERSION" || fail "VERSION='$VERSION' is not vMAJOR.MINOR.PATCH[-prerelease]"
        version="$VERSION"
        how="given explicitly (VERSION=$VERSION)"
        if [ -n "${PRE:-}" ]; then fail "give VERSION or PRE, not both"; fi
    else
        version="$(declared_version "$commit")"
        how="declared by CHANGELOG.md at ${commit:0:12}"
        case "${PRE:-}" in
            "") ;;
            rc)
                # shellcheck disable=SC2086 # one tag per word
                version="$(next_rc "$version" $tags)"
                local base
                base="$(base_version "$version")"
                how="next candidate of $base, $how"
                ;;
            *) fail "PRE must be rc, not '$PRE'" ;;
        esac
    fi
    local latest
    # shellcheck disable=SC2086
    latest="$(latest_stable $tags)"
    echo "$version"
    echo "$how; newest stable tag: ${latest:-none}"
}

cli() {
    local cmd="${1:-}" tags
    shift || true
    case "$cmd" in
        latest-stable | next | next-rc)
            # Captured first: a failed lookup must fail the command, not read
            # as "no tags" and derive v0.0.1.
            tags="$(remote_tags)" || exit 1
            ;;
    esac
    case "$cmd" in
        latest-stable)
            # shellcheck disable=SC2086 # one tag per word
            latest_stable $tags
            ;;
        next)
            # shellcheck disable=SC2086
            next_version "$(latest_stable $tags)" "${1:-}" "${2:-}"
            ;;
        declared) declared_version "${1:?declared <commit>}" ;;
        next-rc)
            is_semver "${1:-}" || fail "next-rc <vX.Y.Z>"
            # shellcheck disable=SC2086
            next_rc "$1" $tags
            ;;
        resolve) resolve "${1:?resolve <commit>}" ;;
        *)
            echo "usage: release-version.sh latest-stable | next <bump> [target] | declared <commit> | next-rc <version> | resolve <commit>" >&2
            exit 2
            ;;
    esac
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    cli "$@"
fi
