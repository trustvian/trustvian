#!/usr/bin/env bash
#
# Tests for scripts/release-version.sh and the version checks it shares with
# scripts/release-preflight.sh: bumps, release-candidate counting, the first
# release, a declared version that is not one bump above the newest stable
# tag, the 0.x → 1.0.0 guard, and release notes for prereleases. Offline:
# tag lists are passed in, never fetched.
#
#   ./scripts/test-release-version.sh

set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source-path=SCRIPTDIR source=release-version.sh
source ./scripts/release-version.sh

pass_count=0
fail_count=0
ok() { printf 'ok    %s\n' "$1"; pass_count=$((pass_count + 1)); }
bad() { printf 'FAIL  %s\n' "$1" >&2; fail_count=$((fail_count + 1)); }

# is WANT DESCRIPTION CMD...: CMD prints exactly WANT and succeeds.
is() {
    local want="$1" what="$2" got
    shift 2
    if got="$("$@" 2>/dev/null)" && [ "$got" = "$want" ]; then ok "$what = $want"; else bad "$what = '$got', want '$want'"; fi
}
refuses() {
    local what="$1"
    shift
    if ("$@") >/dev/null 2>&1; then bad "$what"; else ok "$what"; fi
}
# says DESCRIPTION NEEDLE CMD...: CMD fails, and its message contains NEEDLE.
says() {
    local what="$1" needle="$2" out
    shift 2
    if out="$("$@" 2>&1)"; then bad "$what: succeeded"; return; fi
    if [[ "$out" == *"$needle"* ]]; then ok "$what"; else bad "$what: message '$out' lacks '$needle'"; fi
}

tags=(v0.8.0 v0.9.0-rc.1 v0.9.0-rc.2 v0.9.0-rc.3 v0.9.0)

# --- latest stable --------------------------------------------------------------
is v0.9.0 "latest_stable of v0.8.0..v0.9.0" latest_stable "${tags[@]}"
is v0.10.1 "latest_stable ignores candidates" latest_stable v0.10.0 v0.10.1 v0.11.0-rc.1
is "" "latest_stable with no stable tag" latest_stable v0.1.0-rc.1

# --- bumps ----------------------------------------------------------------------
is v0.9.1 "patch from v0.9.0" next_version v0.9.0 patch
is v0.10.0 "minor from v0.9.0" next_version v0.9.0 minor
is v0.10.2 "patch from v0.10.1" next_version v0.10.1 patch
is v0.11.0 "minor from v0.10.1" next_version v0.10.1 minor
is v0.1.0 "minor with no stable tag yet" next_version "" minor
is v0.0.1 "patch with no stable tag yet" next_version "" patch
says "major in 0.x without a target" "refused in 0.x" next_version v0.10.1 major
says "major in 0.x to v2.0.0" "refused in 0.x" next_version v0.10.1 major v2.0.0
is v1.0.0 "major in 0.x with target v1.0.0" next_version v0.10.1 major v1.0.0
is v2.0.0 "major from v1.4.2" next_version v1.4.2 major
says "an unknown bump" "BUMP must be" next_version v0.9.0 huge

# --- release candidates ------------------------------------------------------------
is v0.10.0-rc.1 "first rc" next_rc v0.10.0 "${tags[@]}"
is v0.9.0-rc.4 "rc after rc.3" next_rc v0.9.0 "${tags[@]}"
is v0.10.0-rc.11 "rc.10 sorts after rc.9" next_rc v0.10.0 v0.10.0-rc.9 v0.10.0-rc.10 v0.10.0-rc.2
is v0.10.0-rc.1 "an rc of another version does not count" next_rc v0.10.0 v0.10.1-rc.5 v0.1.0-rc.7

# --- a declared version must be exactly one bump above -------------------------------
for v in v0.9.1 v0.10.0 v1.0.0 v0.10.0-rc.1 v1.0.0-rc.2; do
    if (check_bump "$v" v0.9.0) >/dev/null 2>&1; then ok "check_bump $v above v0.9.0"; else bad "check_bump $v above v0.9.0"; fi
done
says "v0.12.0 above v0.10.1 names both" "v0.12.0 is not a valid single bump above v0.10.1" check_bump v0.12.0 v0.10.1
says "v0.10.3 above v0.10.1 lists the valid ones" "want v0.10.2 (patch), v0.11.0 (minor) or v1.0.0 (major)" check_bump v0.10.3 v0.10.1
says "v0.10.0 above v0.10.0" "not a valid single bump" check_bump v0.10.0 v0.10.0
if (check_bump v3.0.0 "") >/dev/null 2>&1; then ok "any version is a first release"; else bad "first release refused"; fi

# --- the version a CHANGELOG declares -------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
printf '# Changelog\n\n## Unreleased\n\n- x\n\n## v0.11.0 — Inspection depth\n\n## v0.10.1 — Fixes\n' >"$tmp/a.md"
is v0.11.0 "declared below Unreleased" declared_from "$tmp/a.md"
printf '# Changelog\n\n## Unreleased\n\n## v0.10.0\n' >"$tmp/b.md"
is v0.10.0 "declared heading without a title" declared_from "$tmp/b.md"
printf '# Changelog\n\n## v0.10.0 — above Unreleased\n\n## Unreleased\n' >"$tmp/c.md"
is "" "nothing declared below Unreleased" declared_from "$tmp/c.md"

# --- release notes for prereleases ------------------------------------------------------
printf '# Trustvian v0.11.0 — Inspection depth\n' >"$tmp/notes.md"
if (check_notes v0.11.0-rc.2 "$tmp/notes.md") >/dev/null 2>&1; then ok "rc notes may name the base version"; else bad "rc notes naming the base refused"; fi
if (check_notes v0.11.0 "$tmp/notes.md") >/dev/null 2>&1; then ok "stable notes name the version"; else bad "stable notes refused"; fi
refuses "rc of another version" check_notes v0.12.0-rc.1 "$tmp/notes.md"
printf '# Trustvian v0.11.01\n' >"$tmp/near.md"
refuses "v0.11.01 is not v0.11.0" check_notes v0.11.0-rc.1 "$tmp/near.md"
printf '# Trustvian v0.10.0\n' >"$tmp/old.md"
refuses "a stable release never accepts another version's notes" check_notes v0.11.0 "$tmp/old.md"

echo
echo "$pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
