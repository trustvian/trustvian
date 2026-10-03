#!/usr/bin/env bash
#
# Tests for scripts/release-mode.sh: resolve_mode over every combination of
# requested mode × CLAUDECODE × interactive terminal, the exact refusal, and
# the mode lines release.sh prints.
#
#   ./scripts/test-release-mode.sh

set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source-path=SCRIPTDIR source=release-mode.sh
source ./scripts/release-mode.sh

pass_count=0
fail_count=0
ok() { printf 'ok    %s\n' "$1"; pass_count=$((pass_count + 1)); }
bad() { printf 'FAIL  %s\n' "$1" >&2; fail_count=$((fail_count + 1)); }

# expect REQUESTED CLAUDECODE INTERACTIVE WANT_STATUS WANT_OUT WANT_ERR
expect() {
    local requested="$1" claudecode="$2" interactive="$3" want_status="$4" want_out="$5" want_err="$6"
    local out err status
    set +e
    out="$(resolve_mode "$requested" "$claudecode" "$interactive" 2>/tmp/release-mode-err.$$)"
    status=$?
    set -e
    err="$(cat /tmp/release-mode-err.$$)"
    rm -f /tmp/release-mode-err.$$
    local what="MODE='$requested' CLAUDECODE='$claudecode' interactive=$interactive"
    if [ "$status" = "$want_status" ] && [ "$out" = "$want_out" ] && [ "$err" = "$want_err" ]; then
        ok "$what → ${want_out:-refused ($want_status)}"
    else
        bad "$what → status $status, out '$out', err '$err'; want $want_status, '$want_out', '$want_err'"
    fi
}

readonly MANUAL="manual|interactive terminal, not inside Claude Code"
readonly REFUSAL="MODE=manual needs an interactive terminal outside Claude Code"

#      requested CLAUDECODE interactive status out                                  err
expect ""        ""         yes         0      "$MANUAL"                            ""
expect ""        ""         no          0      "agent|no interactive terminal"      ""
expect ""        1          yes         0      "agent|CLAUDECODE is set"            ""
expect ""        1          no          0      "agent|CLAUDECODE is set"            ""
expect agent     ""         yes         0      "agent|MODE=agent"                   ""
expect agent     ""         no          0      "agent|MODE=agent"                   ""
expect agent     1          yes         0      "agent|MODE=agent"                   ""
expect agent     1          no          0      "agent|MODE=agent"                   ""
expect manual    ""         yes         0      "$MANUAL"                            ""
expect manual    ""         no          2      ""                                   "$REFUSAL"
expect manual    1          yes         2      ""                                   "$REFUSAL"
expect manual    1          no          2      ""                                   "$REFUSAL"
expect Manual    ""         yes         64     ""                                   "MODE must be manual or agent, not 'Manual'"
expect auto      1          no          64     ""                                   "MODE must be manual or agent, not 'auto'"

# The four first lines, exactly as the docs quote them.
line() {
    local want="$1"
    shift
    local got
    got="$(mode_line "$@")"
    if [ "$got" = "$want" ]; then ok "$want"; else bad "mode_line $* = '$got', want '$want'"; fi
}
line "release: mode manual (interactive terminal, not inside Claude Code)" release manual "interactive terminal, not inside Claude Code"
line "release: mode agent (CLAUDECODE is set) — approval happens on GitHub" release agent "CLAUDECODE is set"
line "release: mode agent (no interactive terminal) — approval happens on GitHub" release agent "no interactive terminal"
line "release: mode agent (MODE=agent) — approval happens on GitHub" release agent "MODE=agent"

echo
echo "$pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
