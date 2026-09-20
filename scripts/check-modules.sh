#!/usr/bin/env bash
#
# Module consistency invariants.
#
# This repository has one published module and three that are deliberately
# not published. That distinction is easy to break by accident and
# expensive to discover during a release, so it is checked here and in CI
# rather than remembered.
#
# See docs/tasks/040-release-artifacts-and-module-consistency.md
# § Module publication model for why each module is classified as it is.

set -euo pipefail

cd "$(dirname "$0")/.."

readonly ROOT_PATH="github.com/trustvian/trustvian"

fail=0
problem() { echo "  FAIL: $*" >&2; fail=1; }
ok()      { echo "  ok: $*"; }

module_path() { awk '/^module /{print $2; exit}' "$1"; }

# check_version_exists reports whether a declared root version is real.
#
# The invariant is "this version exists as something a consumer could
# resolve" — NOT "this checkout happens to have the tag". Those are
# different things, and conflating them is what made this check fail in CI
# while passing locally: actions/checkout fetches no tags by default, so
# `git rev-parse refs/tags/v0.8.0` found nothing in a workspace where
# v0.8.0 very much exists. The message was worse than the failure, because
# it asserted the version did not exist.
#
# Two independent sources of truth, in order of cost:
#
#   1. A local tag. Free, offline, and what a full clone has.
#   2. The module proxy. Authoritative — it is literally what a consumer
#      resolves against — at the cost of a network round trip.
#
# If neither can answer, that is an inconclusive *environment*, not a
# passing or failing invariant, and it is reported as its own error so
# nobody reads a silent skip as a green check.
check_version_exists() {
    local version="$1"

    if git rev-parse -q --verify "refs/tags/$version" >/dev/null 2>&1; then
        ok "requires root $version, which exists as a tag"
        return
    fi

    if [ "${CHECK_MODULES_OFFLINE:-0}" = "1" ]; then
        problem "requires root $version, and it cannot be verified: this checkout has no tags and
        CHECK_MODULES_OFFLINE=1 forbids querying the module proxy.
        Fetch tags (actions/checkout with fetch-tags: true) or allow proxy access."
        return
    fi

    if GOWORK=off GOFLAGS=-mod=mod go list -m "$ROOT_PATH@$version" >/dev/null 2>&1; then
        ok "requires root $version, which resolves from the module proxy"
        return
    fi

    # The proxy was reachable enough to answer and said no, or the network
    # is down. Distinguish them: a resolvable well-known version proves the
    # proxy is reachable, so a failure after that is a real missing version.
    if GOWORK=off GOFLAGS=-mod=mod go list -m "$ROOT_PATH@v0.1.0" >/dev/null 2>&1; then
        problem "requires root $version, which does not exist as a tag or a published module version"
    else
        problem "requires root $version, and it cannot be verified: this checkout has no tags and the
        module proxy is unreachable. Fetch tags (actions/checkout with fetch-tags: true),
        or re-run with network access."
    fi
}

echo "Module consistency"

# ---------------------------------------------------------------------
# 1. The root module is the published one. Its path must be canonical.
# ---------------------------------------------------------------------
root_path="$(module_path go.mod)"
if [ "$root_path" = "$ROOT_PATH" ]; then
    ok "root module path is $ROOT_PATH"
else
    problem "root module path is '$root_path', want '$ROOT_PATH'"
fi

# ---------------------------------------------------------------------
# 2. A published module must declare no replace.
#
# Consumers ignore a dependency's replace directives, so a published
# go.mod containing one builds differently for everyone else than it does
# here — the failure shows up only after the tag is public.
# ---------------------------------------------------------------------
if grep -qE '^\s*replace\s' go.mod || grep -qE '^replace \(' go.mod; then
    problem "root go.mod declares a replace; a published module must not"
else
    ok "root go.mod declares no replace"
fi

# ---------------------------------------------------------------------
# 3-5. Nested modules.
# ---------------------------------------------------------------------
for mod in $(find . -mindepth 2 -name go.mod -not -path "./dist/*" | sort); do
    dir="$(dirname "$mod")"
    path="$(module_path "$mod")"
    echo "  --- $dir ($path)"

    # A replace to the root must point at the repository root and nowhere
    # else. An absolute developer path or a fork would build only on the
    # machine that wrote it.
    replace_target="$(awk -v p="$ROOT_PATH" '
        $1=="replace" && $2==p {print $4; exit}
        $1==p && $2=="=>"      {print $3; exit}
    ' "$mod")"
    has_replace=0
    if [ -n "$replace_target" ]; then
        has_replace=1
        rel="$(cd "$dir" && cd "$replace_target" 2>/dev/null && pwd -P || true)"
        if [ "$rel" = "$(pwd -P)" ]; then
            ok "replaces $ROOT_PATH with the repository root"
        else
            problem "replaces $ROOT_PATH with '$replace_target', which is not the repository root"
        fi
    fi

    # Premature promotion guard: once a nested module's path becomes
    # resolvable, a replace to ../ makes it a broken published module —
    # exactly the state this check exists to prevent reaching silently.
    case "$path" in
        *.*/*)
            if [ "$has_replace" = "1" ]; then
                problem "module path '$path' is resolvable but the module still replaces the root with a local path;
        publishing it in this state would produce a module nobody can build (see the release guide)"
            else
                ok "resolvable module path with no local replace"
            fi
            ;;
        *)
            ok "repository-internal module path (not resolvable, not published)"
            ;;
    esac

    # The declared root version must be real: either a version that exists
    # as a tag, or the zero placeholder Go writes for a fully replaced
    # dependency. A fabricated version misleads anyone reading the floor.
    # Both forms: a single-line `require <path> <version>` and a path
    # inside a `require ( ... )` block. Handling only the block form made
    # this rule silently skip every module using the single-line form.
    required="$(awk -v p="$ROOT_PATH" '
        $1=="require" && $2==p {print $3; exit}
        $1==p && $2 ~ /^v/     {print $2; exit}
    ' "$mod")"
    if [ -z "$required" ]; then
        ok "does not require the root module"
    elif [ "$required" = "v0.0.0-00010101000000-000000000000" ]; then
        ok "requires the root module at the zero placeholder (fully replaced)"
    else
        check_version_exists "$required"
    fi
done

echo
if [ "$fail" -ne 0 ]; then
    echo "Module consistency: FAILED" >&2
    exit 1
fi
echo "Module consistency: OK"
