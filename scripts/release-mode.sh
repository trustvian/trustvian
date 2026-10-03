#!/usr/bin/env bash
#
# Which of the two release modes a command runs in. Sourced by
# scripts/release.sh and scripts/release-prep.sh; tested by
# scripts/test-release-mode.sh.
#
#   manual  a person at an interactive terminal, outside Claude Code. Only
#           here may `make release` approve the `release` deployment, at its
#           prompt, as that person.
#   agent   Claude Code, or anything without an interactive terminal. Never
#           approves: the approval happens on GitHub, web or mobile.
#
# See docs/release-runbook.md § 0 and docs/releasing-with-claude-code.md.

# resolve_mode REQUESTED CLAUDECODE INTERACTIVE
#
# A pure function: REQUESTED is $MODE ("", manual or agent), CLAUDECODE is the
# value of that variable (empty when unset; Claude Code sets it in the shells
# it runs), INTERACTIVE is "yes" when both stdin and stdout are terminals.
#
# Prints "<mode>|<reason>" and returns 0, or prints the refusal on stderr and
# returns 2 (MODE=manual where it cannot be manual) or 64 (an unknown MODE).
resolve_mode() {
    local requested="$1" claudecode="$2" interactive="$3"
    case "$requested" in
        "")
            if [ -n "$claudecode" ]; then
                echo "agent|CLAUDECODE is set"
            elif [ "$interactive" != yes ]; then
                echo "agent|no interactive terminal"
            else
                echo "manual|interactive terminal, not inside Claude Code"
            fi
            ;;
        agent) echo "agent|MODE=agent" ;;
        manual)
            if [ -n "$claudecode" ] || [ "$interactive" != yes ]; then
                echo "MODE=manual needs an interactive terminal outside Claude Code" >&2
                return 2
            fi
            echo "manual|interactive terminal, not inside Claude Code"
            ;;
        *)
            echo "MODE must be manual or agent, not '$requested'" >&2
            return 64
            ;;
    esac
}

# mode_line PREFIX MODE REASON: the first line a release command prints.
mode_line() {
    local prefix="$1" mode="$2" reason="$3"
    if [ "$mode" = manual ]; then
        echo "$prefix: mode manual ($reason)"
    else
        echo "$prefix: mode agent ($reason) — approval happens on GitHub"
    fi
}

# is_interactive: "yes" when stdin and stdout are both terminals.
is_interactive() {
    if [ -t 0 ] && [ -t 1 ]; then echo yes; else echo no; fi
}
