#!/usr/bin/env bash
#
# One-time repository setup for the release pipeline. Idempotent.
#
#   ./scripts/release-setup.sh --check   # read-only: report what differs
#   ./scripts/release-setup.sh           # a human administrator: make it so
#
# The desired state (docs/adr/0060-agent-operated-releases-with-environment-approval.md):
#
#   1. Environment `release-build`: deployable from main only, no reviewers.
#      Every job of release.yml before publish runs in it.
#   2. Environment `release`: deployable from main only, required reviewers =
#      the organization's admins (at most six, GitHub's limit), no wait timer,
#      and "prevent self-review" OFF. A sole maintainer dispatches and approves
#      their own release; with self-review prevented they never could. It
#      holds the secret RELEASE_APP_PRIVATE_KEY (checked by name only; this
#      script never reads, writes or prints a secret).
#   3. The trustvian-release GitHub App: its id in the repository variable
#      RELEASE_APP_ID (created by a human; this script only checks it).
#   4. Ruleset "Release tags: creation": every tag (refs/tags/*), rule
#      creation, and ONE bypass actor, the App (Integration, RELEASE_APP_ID,
#      always). Only publish, after approval, holds that App's token, so only
#      an approved release run can create any tag, and so any release.
#   5. Ruleset "Release tags: immutable": every tag, rules update, deletion
#      and force-move, with NO bypass actor at all.
#   6. Immutable releases, when the repository offers them.
#
# Rulesets 4 and 5 replace the single "Protect release tags" ruleset, which
# apply mode deletes once both exist with the right shape. If GitHub refuses
# the App as a bypass actor, apply mode stops and prints GitHub's response
# exactly; it never falls back to a weaker ruleset.
#
# Run apply mode as a human repository administrator, after the change that
# introduced it was reviewed. An AI agent runs only --check
# (docs/governance/agents.md).

set -euo pipefail

readonly REPO="${RELEASE_REPO:-trustvian/trustvian}"
readonly ORG="${REPO%%/*}"
readonly CREATION_RULESET="Release tags: creation"
readonly IMMUTABLE_RULESET="Release tags: immutable"
readonly OBSOLETE_RULESET="Protect release tags"
readonly APP_SECRET_NAME="RELEASE_APP_PRIVATE_KEY"

# --- The ruleset payloads (tested by scripts/release_setup_test.go) -------------

# creation_ruleset_json APP_ID: only the App may create a tag, any tag.
creation_ruleset_json() {
    local app_id="$1"
    [[ "$app_id" =~ ^[0-9]+$ ]] || { echo "creation_ruleset_json: app id must be a number" >&2; return 1; }
    cat <<JSON
{
  "name": "$CREATION_RULESET",
  "target": "tag",
  "enforcement": "active",
  "conditions": {"ref_name": {"include": ["refs/tags/*"], "exclude": []}},
  "rules": [{"type": "creation"}],
  "bypass_actors": [{"actor_type": "Integration", "actor_id": $app_id, "bypass_mode": "always"}]
}
JSON
}

# immutable_ruleset_json: nobody may move, delete or force-update a tag.
immutable_ruleset_json() {
    cat <<JSON
{
  "name": "$IMMUTABLE_RULESET",
  "target": "tag",
  "enforcement": "active",
  "conditions": {"ref_name": {"include": ["refs/tags/*"], "exclude": []}},
  "rules": [{"type": "update"}, {"type": "deletion"}, {"type": "non_fast_forward"}],
  "bypass_actors": []
}
JSON
}

# The shape --check compares: enforcement|include|rules|bypass actors.
creation_shape() { echo "active|refs/tags/*|creation|Integration:$1:always"; }
immutable_shape() { echo "active|refs/tags/*|deletion,non_fast_forward,update|"; }

# --- Reporting ---------------------------------------------------------------------

problems=0
report() { printf '  %-7s %s\n' "$1" "$2"; }
missing() {
    report MISSING "$1"
    problems=$((problems + 1))
}

# --- Environments ------------------------------------------------------------------

# env_state NAME: "absent", or
# "<custom_branch_policies>|<branches>|<reviewer ids>|<prevent_self_review>|<wait_timer>".
env_state() {
    local name="$1" base branches custom reviewers self wait
    # gh api prints a 404's body on stdout, so the fallback replaces the output.
    if ! base="$(gh api "repos/$REPO/environments/$name" --jq '
        [ (.deployment_branch_policy.custom_branch_policies // false),
          ([.protection_rules[]? | select(.type == "required_reviewers") | .reviewers[]? | .reviewer.id] | sort | map(tostring) | join(",")),
          ([.protection_rules[]? | select(.type == "required_reviewers") | .prevent_self_review] | first // false),
          ([.protection_rules[]? | select(.type == "wait_timer") | .wait_timer] | first // 0)
        ] | map(tostring) | join("|")' 2>/dev/null)"; then
        echo absent
        return
    fi
    IFS='|' read -r custom reviewers self wait <<<"$base"
    branches=""
    if [ "$custom" = true ]; then
        branches="$(gh api "repos/$REPO/environments/$name/deployment-branch-policies" \
            --jq '[.branch_policies[] | "\(.type):\(.name)"] | sort | join(",")')"
    fi
    echo "$custom|$branches|$reviewers|$self|$wait"
}

# ensure_env NAME REVIEWERS: make environment NAME deployable from main only,
# with REVIEWERS (comma-separated user ids, or empty) and self-review allowed.
ensure_env() {
    local name="$1" want_reviewers="$2" have want
    have="$(env_state "$name")"
    want="true|branch:main|$want_reviewers|false|0"
    if [ "$have" = "$want" ]; then
        report ok "environment '$name': main only, reviewers [${want_reviewers:-none}], self-review allowed"
        return
    fi
    if [ "$mode" = check ]; then
        missing "environment '$name' is '$have', want '$want' (custom-branches|branches|reviewer ids|prevent_self_review|wait_timer)"
        return
    fi
    local reviewers_json="" rid
    for rid in ${want_reviewers//,/ }; do
        reviewers_json="${reviewers_json:+$reviewers_json,}{\"type\":\"User\",\"id\":$rid}"
    done
    reviewers_json="[$reviewers_json]"
    gh api -X PUT "repos/$REPO/environments/$name" --input - >/dev/null <<JSON
{"wait_timer": 0, "prevent_self_review": false, "reviewers": $reviewers_json,
 "deployment_branch_policy": {"protected_branches": false, "custom_branch_policies": true}}
JSON
    # Exactly one branch policy: main.
    local id
    for id in $(gh api "repos/$REPO/environments/$name/deployment-branch-policies" \
        --jq '.branch_policies[] | select(.type != "branch" or .name != "main") | .id'); do
        gh api -X DELETE "repos/$REPO/environments/$name/deployment-branch-policies/$id" >/dev/null
    done
    if ! gh api "repos/$REPO/environments/$name/deployment-branch-policies" \
        --jq '.branch_policies[] | select(.type == "branch" and .name == "main") | .id' | grep -q .; then
        gh api -X POST "repos/$REPO/environments/$name/deployment-branch-policies" -f name=main -f type=branch >/dev/null
    fi
    report "done" "environment '$name' set: main only, reviewers [${want_reviewers:-none}], self-review allowed"
}

# check_app_secret: the `release` environment lists a secret named
# RELEASE_APP_PRIVATE_KEY. Names only: secret values are never readable, and
# this script never sets one. A human stores the key (docs/release-runbook.md § 0).
check_app_secret() {
    local names
    if ! names="$(gh api "repos/$REPO/environments/release/secrets" --jq '[.secrets[].name] | join(",")' 2>/dev/null)"; then
        missing "the 'release' environment's secrets could not be listed (does it exist?)"
        return
    fi
    if [[ ",$names," == *",$APP_SECRET_NAME,"* ]]; then
        report ok "environment 'release' has a secret named $APP_SECRET_NAME"
    else
        missing "environment 'release' has no secret named $APP_SECRET_NAME (a human stores the App's private key there)"
    fi
}

# --- Rulesets ------------------------------------------------------------------------

ruleset_id() {
    gh api "repos/$REPO/rulesets" --jq ".[] | select(.target == \"tag\" and .name == \"$1\") | .id"
}

ruleset_shape() {
    gh api "repos/$REPO/rulesets/$1" --jq '
        "\(.enforcement)|\(.conditions.ref_name.include | sort | join(","))|\([.rules[].type] | sort | join(","))|\([(.bypass_actors // [])[] | "\(.actor_type):\(if .actor_type == "OrganizationAdmin" then "" else .actor_id end):\(.bypass_mode)" | sub("::"; ":")] | sort | join(","))"'
}

# ensure_ruleset NAME WANT_SHAPE PAYLOAD: create or replace NAME. Stops, with
# GitHub's exact response, if GitHub refuses it.
ensure_ruleset() {
    local name="$1" want="$2" payload="$3" id shape="" response
    id="$(ruleset_id "$name")"
    [ -z "$id" ] || shape="$(ruleset_shape "$id")"
    if [ "$shape" = "$want" ]; then
        report ok "ruleset '$name': $want"
        return
    fi
    if [ "$mode" = check ]; then
        missing "ruleset '$name' is '${shape:-absent}', want '$want' (enforcement|refs|rules|bypass)"
        return
    fi
    if [ -n "$id" ]; then
        response="$(gh api -X PUT "repos/$REPO/rulesets/$id" --input - <<<"$payload" 2>&1)" || {
            echo "  FAILED  GitHub refused ruleset '$name'. Its response, exactly:" >&2
            printf '%s\n' "$response" >&2
            echo "  Nothing else was changed after this. Do not weaken the ruleset to make it pass: see ADR 0060 § 3." >&2
            exit 1
        }
    else
        response="$(gh api -X POST "repos/$REPO/rulesets" --input - <<<"$payload" 2>&1)" || {
            echo "  FAILED  GitHub refused ruleset '$name'. Its response, exactly:" >&2
            printf '%s\n' "$response" >&2
            echo "  Nothing else was changed after this. Do not weaken the ruleset to make it pass: see ADR 0060 § 3." >&2
            exit 1
        }
    fi
    report "done" "ruleset '$name' set: $want"
}

# --- Main ------------------------------------------------------------------------------

main() {
    mode=apply
    case "${1:-}" in
        "") ;;
        --check) mode=check ;;
        *) echo "usage: release-setup.sh [--check]" >&2; exit 2 ;;
    esac

    echo "Release setup for $REPO ($mode)"

    local admins app_id=""
    admins="$(gh api "orgs/$ORG/members?role=admin&per_page=100" --jq 'map(.id) | sort | .[:6] | map(tostring) | join(",")' 2>/dev/null || true)"
    [ -n "$admins" ] || { echo "  could not list $ORG's admins (needs read:org)"; exit 1; }

    # 1 and 2. Environments, and the App's key in `release` (names only).
    ensure_env release-build ""
    ensure_env release "$admins"
    check_app_secret

    # 3. The App's id.
    app_id="$(gh variable get RELEASE_APP_ID --repo "$REPO" 2>/dev/null || true)"
    if [[ "$app_id" =~ ^[0-9]+$ ]]; then
        report ok "repository variable RELEASE_APP_ID is set ($app_id)"
    else
        missing "repository variable RELEASE_APP_ID (the trustvian-release App's id; a human creates the App)"
    fi

    # 5 before 4: tags are made immutable first, so there is never a moment
    # in which anything could move or delete one.
    ensure_ruleset "$IMMUTABLE_RULESET" "$(immutable_shape)" "$(immutable_ruleset_json)"
    if [[ "$app_id" =~ ^[0-9]+$ ]]; then
        ensure_ruleset "$CREATION_RULESET" "$(creation_shape "$app_id")" "$(creation_ruleset_json "$app_id")"
    elif [ "$mode" = check ]; then
        missing "ruleset '$CREATION_RULESET' cannot be checked without RELEASE_APP_ID"
    else
        echo "  STOPPED: RELEASE_APP_ID is not set, so '$CREATION_RULESET' cannot name the App. Create the App first (docs/release-runbook.md § 0)." >&2
        exit 1
    fi

    # The obsolete single ruleset, removed only once both replacements are in
    # place with the right shape.
    local old
    old="$(ruleset_id "$OBSOLETE_RULESET")"
    if [ -n "$old" ]; then
        if [ "$mode" = check ]; then
            missing "obsolete ruleset '$OBSOLETE_RULESET' is still present (apply mode removes it after the two replacements are in place)"
        elif [ "$(ruleset_shape "$(ruleset_id "$IMMUTABLE_RULESET")")" = "$(immutable_shape)" ] &&
            [ "$(ruleset_shape "$(ruleset_id "$CREATION_RULESET")")" = "$(creation_shape "$app_id")" ]; then
            gh api -X DELETE "repos/$REPO/rulesets/$old" >/dev/null
            report "done" "obsolete ruleset '$OBSOLETE_RULESET' removed; its replacements are in place"
        else
            missing "obsolete ruleset '$OBSOLETE_RULESET' kept: its replacements are not both in place"
        fi
    else
        report ok "no obsolete '$OBSOLETE_RULESET' ruleset"
    fi

    # 6. Immutable releases.
    local immutable
    if ! immutable="$(gh api "repos/$REPO/immutable-releases" --jq .enabled 2>/dev/null)"; then
        immutable=unavailable
    fi
    case "$immutable" in
        true) report ok "immutable releases enabled" ;;
        unavailable) report n/a "immutable releases are not offered for this repository" ;;
        *)
            if [ "$mode" = check ]; then
                missing "immutable releases (Settings → General → Releases)"
            else
                gh api -X PUT "repos/$REPO/immutable-releases" >/dev/null
                report "done" "immutable releases enabled"
            fi
            ;;
    esac

    if [ "$problems" -ne 0 ]; then
        echo "$problems item(s) need attention."
        exit 1
    fi
    echo "Release setup is complete."
}

mode=check

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
