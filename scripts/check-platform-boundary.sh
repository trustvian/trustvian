#!/usr/bin/env bash
#
# The core/platform boundary, checked rather than remembered.
#
# ADR 0022 defines two invariants and makes this check a release gate:
#
#   1. The platform must not import the core's internal/* packages.
#   2. The core must not depend on the platform at all.
#
# Invariant 1 already has a second line of defence: the platform's module
# path is `trustvian-platform`, not `github.com/trustvian/trustvian/platform`,
# and Go's internal/ rule turns on import-path ancestry rather than module
# membership — so such an import is a compile error. That was verified
# empirically when ADR 0022 was written, not assumed. The check stays anyway,
# because the enforcement ADR 0022 specifies is this script: a future change
# to the module path would silently remove the compiler's help, and this is
# what would notice.
#
# Invariant 2 has no compiler help at all. Nothing stops someone adding
# `require trustvian-platform` to the root go.mod, and by the time it hurts,
# the dependency is load-bearing.
#
# Deliberately narrow. This does not attempt to parse every architectural
# rule; it asks the module graph and the compiler, which cannot be fooled by
# formatting, and greps only where no tool can answer.

set -euo pipefail

cd "$(dirname "$0")/.."

readonly ROOT_PATH="github.com/trustvian/trustvian"
readonly PLATFORM_DIR="platform"

fail=0
problem() { echo "  FAIL: $*" >&2; fail=1; }
ok()      { echo "  ok: $*"; }

echo "Core/platform boundary"

if [ ! -f "$PLATFORM_DIR/go.mod" ]; then
    echo "  FAIL: $PLATFORM_DIR/go.mod not found" >&2
    exit 1
fi

platform_path="$(awk '/^module /{print $2; exit}' "$PLATFORM_DIR/go.mod")"

# ---------------------------------------------------------------------
# 1. The module path must not sit under the core's import path.
#
# This is the property that makes the compiler enforce invariant 1. It is
# checked before the import scan because it explains *why* the scan should
# come up empty.
# ---------------------------------------------------------------------
case "$platform_path" in
    "$ROOT_PATH"/*)
        problem "platform module path is '$platform_path', which sits under '$ROOT_PATH';
        Go's internal/ rule is based on import-path ancestry, so this path would
        permit importing the core's internal packages (see ADR 0022)"
        ;;
    *)
        ok "module path '$platform_path' is outside '$ROOT_PATH', so internal/* imports are a compile error"
        ;;
esac

# ---------------------------------------------------------------------
# 2. No core internal/* import in platform source.
# ---------------------------------------------------------------------
if grep -rn --include="*.go" "$ROOT_PATH/internal" "$PLATFORM_DIR"; then
    problem "platform source imports the core's internal packages (ADR 0022 invariant 2)"
else
    ok "no $ROOT_PATH/internal import in platform source"
fi

# ---------------------------------------------------------------------
# 3. The core must not depend on the platform.
#
# Asked of the module graph rather than of the text: `go list -deps` resolves
# what the build actually pulls in, which a grep for an import line would
# miss when the dependency arrives transitively.
# ---------------------------------------------------------------------
if grep -qE "^\s*(require\s+)?$platform_path(\s|\$)" go.mod; then
    problem "the root go.mod requires '$platform_path'; the core must not depend on the platform"
else
    ok "root go.mod does not require '$platform_path'"
fi

if GOWORK=off go list -deps ./... 2>/dev/null | grep -q "^$platform_path"; then
    problem "the core's build graph contains '$platform_path'"
else
    ok "the core's build graph contains no platform package"
fi

# ---------------------------------------------------------------------
# 4. Platform identity must not appear in core runtime code.
#
# ADR 0022's first invariant is that no platform concept becomes a core
# type, field, or option. A name is a weak proxy for a concept, so this is
# deliberately narrow: it looks for Go *declarations* — a type, a struct
# field, a const, a var, or a func — in non-test core source, and ignores
# comments, documentation, and tests, which legitimately discuss these
# concepts precisely because the boundary exists.
#
# The cost of a false positive here is a blocked pull request on a legitimate
# change, so the match is tuned to be specific rather than exhaustive. It is a
# tripwire for the obvious mistake, not a proof of absence; the reviewable
# guarantee remains the module boundary above.
# ---------------------------------------------------------------------
# Two families, matched differently on purpose.
#
# PLATFORM_IDENTIFIERS are distinctive enough that any Go declaration using
# one is a violation: nothing in a behavioral engine legitimately declares a
# CandidateID.
#
# PLATFORM_TYPES are ordinary English words. "Agent" and "Candidate" appear
# constantly in this repository's prose, and `candidate` is a reasonable local
# variable in generic code — so these are matched ONLY as type declarations
# (`type Candidate struct`, `type Agent interface`, …), which is the shape
# that would actually mean the core had grown a platform concept.
readonly PLATFORM_IDENTIFIERS='ProjectID|AgentID|CandidateID|EvaluationRunID|BehavioralProfileRef|EnvironmentRef'
readonly PLATFORM_TYPES='Project|Agent|Candidate|EvaluationRun|Scorecard|Promotion'

# The developer CLI's control-plane adapter (task 060) is exempt from this
# scan, and only from this scan.
#
# ADR 0022's invariant 4 is that *the engine* must not grow platform concepts:
# no Project type, no EvaluationRunID field on Event. These files are not the
# engine. They are an HTTP client whose structs mirror the JSON nouns of the
# /v1 wire contract, so a field named ProjectID there is the contract's own
# name for a value the caller typed on the command line — not the core
# acquiring an evaluation concept.
#
# What actually keeps them honest is stronger than a name scan and is checked
# elsewhere: check 3 above proves the root module's build graph contains no
# platform package, and cmd/trustvian's own architecture test fails on an
# import of trustvian-platform, on a platform type named in CLI source, and on
# trustvian-platform appearing in the root go.mod or go.sum. Both still cover
# these files.
#
# Listed explicitly rather than matched by prefix: adding a new control-plane
# CLI file should require deciding that it belongs here.
readonly CLI_WIRE_ADAPTER_FILES='
./cmd/trustvian/platform_client.go
./cmd/trustvian/platform_command.go
./cmd/trustvian/platform_output.go
./cmd/trustvian/project.go
./cmd/trustvian/agent.go
./cmd/trustvian/candidate.go
./cmd/trustvian/eval.go
'

is_cli_wire_adapter() {
    printf '%s\n' "$CLI_WIRE_ADAPTER_FILES" | grep -qxF "$1"
}

core_go_files() {
    find . -name "*.go" -not -name "*_test.go" \
        -not -path "./$PLATFORM_DIR/*" \
        -not -path "./processor/*" \
        -not -path "./examples/*" \
        -not -path "./dist/*" \
        -not -path "./.git/*" | sort
}

leaks=""
while IFS= read -r f; do
    if is_cli_wire_adapter "$f"; then
        continue
    fi
    # Strip comments before matching so prose explaining the boundary does
    # not read as a violation. gofmt's own printer is not available for
    # this, so: drop //-comments and /* */ blocks, then look for a
    # declaration position — the name as a type/field/const/func, not as a
    # word inside a string.
    stripped="$(sed -e 's://.*::' "$f" | awk '
        /\/\*/ { inblock = 1 }
        !inblock { print }
        /\*\// { inblock = 0 }
    ')"
    hit="$(printf '%s\n' "$stripped" | grep -nE "(^|[[:space:]])($PLATFORM_IDENTIFIERS)([[:space:]]+[A-Za-z*\[]|[[:space:]]*[:=]|\()" || true)"

    # Type declarations only, for the ordinary-word family. Matches
    # `type Candidate struct{...}`, `type Agent interface{...}`, and the
    # grouped form's `\tCandidate struct{...}` inside a `type (` block.
    types="$(printf '%s\n' "$stripped" | grep -nE "^[[:space:]]*(type[[:space:]]+)?($PLATFORM_TYPES)[[:space:]]+(struct|interface)[[:space:]]*[{]" || true)"
    if [ -n "$types" ]; then
        hit="$hit$types"
    fi

    if [ -n "$hit" ]; then
        leaks="$leaks$f:$hit"$'\n'
    fi
done < <(core_go_files)

if [ -n "$leaks" ]; then
    problem "platform identity appears in core runtime code (ADR 0022 invariant 1):"
    printf '%s' "$leaks" >&2
else
    ok "no platform identity declared in core runtime code"
    ok "(the task 060 CLI wire adapter is scanned by cmd/trustvian's own architecture test)"
fi

# ---------------------------------------------------------------------
# 5. The platform stands on its own.
#
# GOWORK=off is the point: a workspace resolves dependencies the module does
# not declare, which is exactly the failure this catches.
# ---------------------------------------------------------------------
if (cd "$PLATFORM_DIR" && GOWORK=off go build ./... >/dev/null 2>&1); then
    ok "platform builds with GOWORK=off"
else
    problem "platform does not build with GOWORK=off"
fi

echo
if [ "$fail" -ne 0 ]; then
    echo "Core/platform boundary: FAILED" >&2
    exit 1
fi
echo "Core/platform boundary: OK"
