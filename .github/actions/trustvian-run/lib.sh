# shellcheck shell=bash
#
# Shared functions for the trustvian-run action's steps. Sourced, never run.
#
# Written for bash 3.2 as well as current bash, because a macOS runner's
# /bin/bash is 3.2: no associative arrays, no mapfile, no ${var,,}.
#
# scripts/trustvian_run_action_test.go sources this file and exercises every
# function below directly.

# The largest stdout this action accepts as a result document: the CLI's own
# 32 MiB cap on a suite document (ADR 0055 § 5), plus 64 KiB for its framing.
# Anything larger is refused whole — never truncated, because a truncated
# document is a different document.
readonly TRUSTVIAN_RUN_MAX_RESULT_BYTES=$((32 * 1024 * 1024 + 64 * 1024))

# A summary value is capped at this many bytes before it is rendered.
readonly TRUSTVIAN_RUN_MAX_SUMMARY_VALUE=256

# fail prints a workflow error annotation and exits 1.
#
# Exit 1 here is the action's own failure — setup, input, or preservation —
# and it is never the CLI's code: whenever the CLI ran, its exact code is in
# the exit-code output and the finish step exits with it.
fail() {
    printf '::error title=trustvian-run::%s\n' "$(annotation_text "$*")"
    exit 1
}

warn() {
    printf '::warning title=trustvian-run::%s\n' "$(annotation_text "$*")"
}

# annotation_text makes a message safe to place in a workflow command: a
# newline would end the command early, and '%' starts an escape.
annotation_text() {
    printf '%s' "$1" | LC_ALL=C tr -d '\000-\011\013-\037\177' |
        sed -e 's/%/%25/g' | awk 'BEGIN { ORS = "%0A" } { print }' | sed -e 's/%0A$//'
}

# set_output appends one name=value line to $GITHUB_OUTPUT. Every value this
# action outputs is single-line by construction; a value that is not is
# refused rather than written, because a newline would let it forge a second
# output.
set_output() {
    case "$2" in
        *$'\n'* | *$'\r'*) fail "internal: output $1 is not a single line" ;;
    esac
    printf '%s=%s\n' "$1" "$2" >>"$GITHUB_OUTPUT"
}

# load_runtime_pin reads runtime.env into TRUSTVIAN_SOURCE_REPOSITORY,
# TRUSTVIAN_SOURCE_COMMIT, GO_VERSION and GO_SHA256_<OS>_<ARCH>.
#
# A strict reader rather than `source`: the file is data, and every key and
# value shape is checked, so a malformed pin fails before anything is fetched.
load_runtime_pin() {
    local file="$1" line key value seen=""
    [ -f "$file" ] || fail "runtime pin $file is missing"
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in '' | '#'*) continue ;; esac
        key="${line%%=*}"
        value="${line#*=}"
        [ "$key" != "$line" ] || fail "runtime pin: malformed line"
        case "$key" in
            TRUSTVIAN_SOURCE_REPOSITORY)
                [[ "$value" =~ ^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git$ ]] ||
                    fail "runtime pin: $key must be an https github.com repository URL"
                ;;
            TRUSTVIAN_SOURCE_COMMIT)
                [[ "$value" =~ ^[0-9a-f]{40}$ ]] ||
                    fail "runtime pin: $key must be a full 40-character commit"
                ;;
            GO_VERSION)
                [[ "$value" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
                    fail "runtime pin: $key must be an exact version such as 1.27.1"
                ;;
            GO_SHA256_LINUX_AMD64 | GO_SHA256_LINUX_ARM64 | GO_SHA256_DARWIN_AMD64 | GO_SHA256_DARWIN_ARM64)
                [[ "$value" =~ ^[0-9a-f]{64}$ ]] ||
                    fail "runtime pin: $key must be a SHA-256 digest"
                ;;
            *) fail "runtime pin: unknown key $key" ;;
        esac
        case " $seen " in *" $key "*) fail "runtime pin: $key is set twice" ;; esac
        seen="$seen $key"
        printf -v "$key" '%s' "$value"
    done <"$file"
    for key in TRUSTVIAN_SOURCE_REPOSITORY TRUSTVIAN_SOURCE_COMMIT GO_VERSION \
        GO_SHA256_LINUX_AMD64 GO_SHA256_LINUX_ARM64 GO_SHA256_DARWIN_AMD64 GO_SHA256_DARWIN_ARM64; do
        case " $seen " in *" $key "*) ;; *) fail "runtime pin: $key is missing" ;; esac
    done
}

# go_platform sets GO_OS and GO_ARCH for this runner, or fails. Windows is
# refused: `trustvian dev` refuses to start there, so there is nothing to run.
#
# Like every function here that can fail, it sets variables rather than
# printing: fail inside a command substitution would exit only the subshell.
# shellcheck disable=SC2034 # GO_OS and GO_ARCH are read by the caller.
go_platform() {
    case "$(uname -s)" in
        Linux) GO_OS=linux ;;
        Darwin) GO_OS=darwin ;;
        *) fail "unsupported runner OS $(uname -s): trustvian dev runs on Linux and macOS only" ;;
    esac
    case "$(uname -m)" in
        x86_64 | amd64) GO_ARCH=amd64 ;;
        aarch64 | arm64) GO_ARCH=arm64 ;;
        *) fail "unsupported runner architecture $(uname -m)" ;;
    esac
}

# sha256_of prints a file's SHA-256 digest.
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    else
        shasum -a 256 "$1" | awk '{ print $1 }'
    fi
}

# verify_sha256 fails unless a file's digest is exactly the expected one.
verify_sha256() {
    local actual
    actual="$(sha256_of "$1")"
    [ "$actual" = "$2" ] || fail "checksum mismatch for $(basename "$1"): expected $2, got $actual"
}

# refuse_privileged_event fails on an event that runs in the base repository's
# context. This action executes the workload — the pull request's own code —
# and those events hand that code a write-scoped token and repository secrets.
refuse_privileged_event() {
    case "${GITHUB_EVENT_NAME:-}" in
        pull_request_target | workflow_run)
            fail "refusing to run on ${GITHUB_EVENT_NAME}: that event runs with base-repository privileges, and this action executes the workload. Use pull_request — see docs/ci-github-action.md"
            ;;
    esac
}

# resolve_head sets HEAD_SHA and HEAD_SOURCE: the commit this run describes,
# read from the event context, never computed.
#
# On a pull_request event that is the pull request's head commit. github.sha
# on that event is the ephemeral merge commit, which names nothing the author
# can check out, so it is never used there — a pull_request event without a
# well-formed head commit is an error, not a fallback. On any other event
# github.sha is the commit the run is for.
resolve_head() {
    HEAD_SHA=""
    case "${GITHUB_EVENT_NAME:-}" in
        pull_request)
            HEAD_SOURCE="event.pull_request.head.sha"
            { [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ]; } ||
                fail "pull_request event without an event payload; cannot name the head commit"
            HEAD_SHA="$(jq -r 'if (.pull_request.head.sha | type) == "string" then .pull_request.head.sha else "" end' \
                "$GITHUB_EVENT_PATH" 2>/dev/null)" || HEAD_SHA=""
            ;;
        '')
            fail "GITHUB_EVENT_NAME is not set; this action runs inside a GitHub Actions job"
            ;;
        *)
            HEAD_SOURCE="github.sha"
            HEAD_SHA="${GITHUB_SHA:-}"
            ;;
    esac
    [[ "$HEAD_SHA" =~ ^[0-9a-f]{40}([0-9a-f]{24})?$ ]] ||
        fail "cannot name the head commit: $HEAD_SOURCE is not a full commit"
}

# classify_result prints present, absent, invalid or oversized for a captured
# stdout file. present means exactly one JSON object, within the size bound.
#
# Syntax only. The result document's fields are the next reporting slice's
# strict decoder's concern; this step decides whether there is a document to
# preserve at all, and never repairs one.
classify_result() {
    local file="$1" bytes
    [ -f "$file" ] || {
        echo absent
        return
    }
    bytes="$(wc -c <"$file" | tr -d ' ')"
    if [ "$bytes" -eq 0 ]; then
        echo absent
    elif [ "$bytes" -gt "$TRUSTVIAN_RUN_MAX_RESULT_BYTES" ]; then
        echo oversized
    elif jq -e -s 'length == 1 and (.[0] | type) == "object"' "$file" >/dev/null 2>&1; then
        echo present
    else
        echo invalid
    fi
}

# md_inert renders an untrusted string inert for a markdown job summary, and
# bounded: control characters (newlines included) are dropped, the value is
# cut at max bytes on a character boundary with the cut stated, and every
# ASCII punctuation character is backslash-escaped, so no link, image, HTML
# tag, table cell, code span or @mention can form.
md_inert() {
    local value="$1" max="${2:-$TRUSTVIAN_RUN_MAX_SUMMARY_VALUE}" cut
    value="$(printf '%s' "$value" | LC_ALL=C tr -d '\000-\037\177')"
    if [ "$(printf '%s' "$value" | LC_ALL=C wc -c | tr -d ' ')" -gt "$max" ]; then
        # Byte cut, then drop a trailing partial UTF-8 sequence: iconv -c
        # discards what does not decode.
        cut="$(printf '%s' "$value" | LC_ALL=C head -c "$max" | iconv -c -f UTF-8 -t UTF-8 2>/dev/null)" ||
            cut="$(printf '%s' "$value" | LC_ALL=C head -c "$max" | LC_ALL=C tr -cd '\040-\176')"
        value="$cut … (truncated)"
    fi
    printf '%s' "$value" | LC_ALL=C sed -e 's/[][!"#$%&'\''()*+,./:;<=>?@\\^_`{|}~-]/\\&/g'
}
