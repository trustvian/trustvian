#!/usr/bin/env bash
#
# Verify that every GitHub Action referenced by this repository's workflows
# actually exists at the ref it names.
#
#   ./scripts/check-action-refs.sh
#
# This exists because of a real release failure. `v0.9.0-rc.1` passed every
# gate — including actionlint — and then died in job setup with:
#
#   Unable to resolve action `sigstore/cosign-installer@v4`,
#   unable to find version `v4`
#
# The YAML was valid and the action was real; only that *version* did not
# exist, because sigstore/cosign-installer publishes exact tags and no
# floating major. No linter checks that, because it needs a network lookup.
# This script is that lookup, and nothing more — it is deliberately not a
# dependency manager: it does not update, pin, or police versions. "Does
# this ref exist" is the whole question.
#
# Resolution uses `git ls-remote` against the public repository rather than
# the GitHub API. The first version of this script used the API and failed
# in CI while passing locally: an authenticated cross-repository Git-data
# lookup is not something the workflow's scoped token can be relied on to
# perform, and the script reported the resulting error as "ref does not
# exist" — accusing a valid action of being invalid. `git ls-remote` needs
# no credentials for public repositories, behaves identically on a laptop
# and on a runner, costs one request per reference, and separates the three
# outcomes that matter:
#
#   exit 0  + output  the ref exists (and says whether tag or branch)
#   exit 2            the repository answered: no such ref
#   anything else     the lookup itself failed (network, DNS, rate limit,
#                     repository gone) — reported as UNVERIFIED, never as
#                     "does not exist"
#
# Both failure kinds fail the run. Only the wording differs, because a
# maintainer who is told "this ref does not exist" will go looking for the
# wrong problem.
#
# Testing: scripts/test-check-action-refs.sh runs this against a fake
# resolver supplied through TRUSTVIAN_ACTION_REF_RESOLVER, so every outcome
# — including infrastructure failure — is exercised without the network.

set -euo pipefail

cd "$(dirname "$0")/.."

readonly WORKFLOW_DIR="${TRUSTVIAN_WORKFLOW_DIR:-.github/workflows}"

# The repository's own composite actions reference actions too, and a bad
# ref there fails every workflow that uses them. Scanned by default; when a
# test points WORKFLOW_DIR elsewhere, only TRUSTVIAN_ACTION_DIR, if given.
if [ -n "${TRUSTVIAN_WORKFLOW_DIR:-}" ]; then
    readonly ACTION_DIR="${TRUSTVIAN_ACTION_DIR:-}"
else
    readonly ACTION_DIR="${TRUSTVIAN_ACTION_DIR:-.github/actions}"
fi
scan_dirs=("$WORKFLOW_DIR")
if [ -n "$ACTION_DIR" ] && [ -d "$ACTION_DIR" ]; then
    scan_dirs+=("$ACTION_DIR")
fi

# resolve_ref prints the kind of ref that was found (tag, branch, or
# commit) and exits 0; exits 2 when the repository has no such ref; exits 1
# when the lookup could not be performed at all.
#
# TRUSTVIAN_ACTION_REF_RESOLVER replaces this with a command taking
# "<owner/repo> <ref>" and honoring the same contract. It exists for the
# tests and must never be set in CI.
resolve_ref() {
    local repo="$1" ref="$2"

    if [ -n "${TRUSTVIAN_ACTION_REF_RESOLVER:-}" ]; then
        "$TRUSTVIAN_ACTION_REF_RESOLVER" "$repo" "$ref"
        return $?
    fi

    local url="https://github.com/${repo}.git"
    local out status

    # GIT_TERMINAL_PROMPT=0: a repository that needs credentials must fail
    # rather than block CI on a username prompt that will never be answered.
    set +e
    out="$(GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code "$url" \
        "refs/tags/${ref}" "refs/heads/${ref}" 2>/dev/null)"
    status=$?
    set -e

    case "$status" in
        0)
            case "$out" in
                *refs/tags/*) echo "tag" ;;
                *) echo "branch" ;;
            esac
            return 0
            ;;
        2)
            # The repository answered and has no such tag or branch. A full
            # commit SHA is legitimate and invisible to ls-remote, so probe
            # for it before concluding the ref is missing.
            if [[ "$ref" =~ ^[0-9a-f]{40}$ ]]; then
                local tmp
                tmp="$(mktemp -d)"
                set +e
                (
                    git init -q "$tmp" &&
                        GIT_TERMINAL_PROMPT=0 git -C "$tmp" fetch -q --depth=1 "$url" "$ref"
                ) >/dev/null 2>&1
                local fetch_status=$?
                set -e
                rm -rf "$tmp"
                if [ "$fetch_status" -eq 0 ]; then
                    echo "commit"
                    return 0
                fi
            fi
            return 2
            ;;
        *)
            return 1
            ;;
    esac
}

fail_count=0
unverified_count=0

[ -d "$WORKFLOW_DIR" ] || {
    echo "check-action-refs: no $WORKFLOW_DIR directory" >&2
    exit 1
}

# One line per distinct owner/repo@ref, so an action used by five jobs costs
# one lookup rather than five.
# `|| true` on the grep alone: no matches is grep's exit 1, and with
# pipefail that would abort the script silently — the explicit guard below
# is what must report it, loudly and by name.
refs="$( { grep -rhoE --include='*.yml' --include='*.yaml' \
    '^[[:space:]]*-?[[:space:]]*uses:[[:space:]]*[^[:space:]#]+' "${scan_dirs[@]}" || true; } |
    sed -E 's/.*uses:[[:space:]]*//' | tr -d '"'"'" | sort -u)"

[ -n "$refs" ] || {
    echo "check-action-refs: no 'uses:' references found — refusing to pass vacuously" >&2
    exit 1
}

while IFS= read -r use; do
    case "$use" in
        docker://*)
            # A container action. Registry availability is the release
            # pipeline's concern, not this script's.
            printf '  skip: %s (container action)\n' "$use"
            continue
            ;;
        ./*)
            # A local composite action: no ref to resolve, but the path must
            # exist — a missing one fails the same way a bad ref does. The
            # path is relative to the repository root, which is where this
            # script has already cd'd, matching how the runner resolves it.
            if [ -f "$use/action.yml" ] || [ -f "$use/action.yaml" ]; then
                printf '  ok:   %s (local action)\n' "$use"
            else
                printf '  FAIL: %s — no action.yml or action.yaml at that path\n' "$use" >&2
                fail_count=$((fail_count + 1))
            fi
            continue
            ;;
    esac

    if [[ "$use" != *@* ]]; then
        printf '  FAIL: %s (no @ref — a floating default branch is not a release input)\n' "$use" >&2
        fail_count=$((fail_count + 1))
        continue
    fi

    ref="${use##*@}"
    path="${use%@*}"
    # owner/repo, dropping any subdirectory path (owner/repo/sub@ref).
    repo="$(printf '%s' "$path" | cut -d/ -f1,2)"

    if [ -z "$ref" ] || [ "$(printf '%s' "$path" | cut -d/ -f1)" = "" ] ||
        [ "$(printf '%s' "$path" | cut -s -d/ -f2)" = "" ]; then
        printf '  FAIL: %s — not an owner/repo@ref reference\n' "$use" >&2
        fail_count=$((fail_count + 1))
        continue
    fi

    set +e
    kind="$(resolve_ref "$repo" "$ref")"
    status=$?
    set -e

    case "$status" in
        0) printf '  ok:   %s (%s)\n' "$use" "$kind" ;;
        2)
            printf '  FAIL: %s — %s has no tag, branch, or commit %s\n' "$use" "$repo" "$ref" >&2
            fail_count=$((fail_count + 1))
            ;;
        *)
            printf '  UNVERIFIED: %s — the lookup against %s failed (network, rate limit, or repository unavailable). This is NOT evidence that the ref is wrong.\n' "$use" "$repo" >&2
            unverified_count=$((unverified_count + 1))
            ;;
    esac
done <<<"$refs"

if [ "$fail_count" -gt 0 ] || [ "$unverified_count" -gt 0 ]; then
    if [ "$fail_count" -gt 0 ]; then
        printf '\ncheck-action-refs: %d unresolvable action reference(s).\n' "$fail_count" >&2
        printf 'A workflow using one fails at job setup, before any step runs.\n' >&2
    fi
    if [ "$unverified_count" -gt 0 ]; then
        printf '\ncheck-action-refs: %d reference(s) could not be checked.\n' "$unverified_count" >&2
        printf 'Re-run once connectivity is restored; do not assume the references are wrong.\n' >&2
    fi
    exit 1
fi

echo
echo "Action references: OK"
