#!/usr/bin/env bash
#
# Tests for scripts/release-preflight.sh's offline checks: SemVer shape and
# precedence, "newer than every tag", and the CHANGELOG and release-notes
# checks. The network checks (tags on the remote, CI and Nightly runs) are
# thin wrappers over git ls-remote and gh api and are not faked here.
#
#   ./scripts/test-release-preflight.sh

set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source-path=SCRIPTDIR source=release-preflight.sh
source ./scripts/release-preflight.sh

pass_count=0
fail_count=0

ok() {
    printf 'ok    %s\n' "$1"
    pass_count=$((pass_count + 1))
}

bad() {
    printf 'FAIL  %s\n' "$1" >&2
    fail_count=$((fail_count + 1))
}

# succeeds DESCRIPTION CMD...: CMD exits 0 (in a subshell, so `fail`'s exit
# cannot end this script).
succeeds() {
    local what="$1"
    shift
    if ("$@") >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi
}

refuses() {
    local what="$1"
    shift
    if ("$@") >/dev/null 2>&1; then bad "$what"; else ok "$what"; fi
}

cmp_is() {
    local a="$1" b="$2" want="$3" got
    got="$(semver_cmp "$a" "$b")"
    if [ "$got" = "$want" ]; then ok "cmp $a $b = $want"; else bad "cmp $a $b = $got, want $want"; fi
}

# --- shape ---------------------------------------------------------------
for v in v0.10.0 v1.0.0 v10.20.30 v1.0.0-rc.1 v1.0.0-alpha v1.0.0-0.3.7 v1.0.0-x-y.1; do
    succeeds "is_semver $v" is_semver "$v"
done
for v in 0.10.0 v0.10 v01.0.0 v1.00.0 v1.0.0+build.1 v1.0.0-rc.1+b v1.0.0- v1.0.0-rc..1 "v1.0.0 " ""; do
    refuses "is_semver '$v'" is_semver "$v"
done
succeeds "v1.0.0-rc.1 is a prerelease" is_prerelease v1.0.0-rc.1
refuses "v1.0.0 is not a prerelease" is_prerelease v1.0.0

# --- precedence: the SemVer 2.0.0 § 11 example chain, and the core -------
chain=(v1.0.0-alpha v1.0.0-alpha.1 v1.0.0-alpha.beta v1.0.0-beta v1.0.0-beta.2
    v1.0.0-beta.11 v1.0.0-rc.1 v1.0.0 v1.0.1 v1.1.0 v2.0.0)
for ((i = 0; i + 1 < ${#chain[@]}; i++)); do
    cmp_is "${chain[i]}" "${chain[i + 1]}" -1
    cmp_is "${chain[i + 1]}" "${chain[i]}" 1
done
cmp_is v0.10.0 v0.9.0 1
cmp_is v0.10.0 v0.10.0 0
cmp_is v0.10.0 v0.10.0-rc.3 1
cmp_is v0.9.0-rc.10 v0.9.0-rc.9 1

# --- newer than every tag --------------------------------------------------
succeeds "v0.10.0 after v0.9.0 and its candidates" check_newer v0.10.0 v0.8.0 v0.9.0 v0.9.0-rc.1 v0.9.0-rc.3
refuses "v0.9.1 after v0.10.0" check_newer v0.9.1 v0.9.0 v0.10.0
refuses "v0.10.0-rc.1 after v0.10.0" check_newer v0.10.0-rc.1 v0.10.0
refuses "an equal version" check_newer v0.10.0 v0.10.0

# --- CHANGELOG and release notes -------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
printf '# Changelog\n\n## Unreleased\n\n## v0.10.0 — Developer Preview\n\n## v0.9.0 — Operational Readiness\n' >"$tmp/CHANGELOG.md"
succeeds "CHANGELOG has the stable section" check_changelog v0.10.0 "$tmp/CHANGELOG.md"
refuses "CHANGELOG lacks v0.11.0" check_changelog v0.11.0 "$tmp/CHANGELOG.md"
refuses "v0.1.0 does not match the v0.10.0 heading" check_changelog v0.1.0 "$tmp/CHANGELOG.md"
succeeds "a prerelease needs no section" check_changelog v0.11.0-rc.1 "$tmp/CHANGELOG.md"
printf '# Trustvian v0.10.0 — Developer Preview\n' >"$tmp/notes.md"
succeeds "notes name v0.10.0" check_notes v0.10.0 "$tmp/notes.md"
refuses "notes do not name v0.11.0" check_notes v0.11.0 "$tmp/notes.md"
: >"$tmp/empty.md"
refuses "empty notes" check_notes v0.10.0 "$tmp/empty.md"
refuses "missing notes" check_notes v0.10.0 "$tmp/absent.md"

echo
echo "$pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
