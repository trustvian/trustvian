#!/usr/bin/env bash
#
# One-time repository setup for the release pipeline. Idempotent.
#
#   ./scripts/release-setup.sh --check   # read-only: report what differs
#   ./scripts/release-setup.sh           # an administrator: make it so
#
# The desired state (docs/adr/0060-agent-operated-releases-with-environment-approval.md):
#
#   1. Environment `release-build`: deployable from main only, no reviewers.
#      Every job of release.yml before publish runs in it.
#   2. Environment `release`: deployable from main only, required reviewers =
#      the organization's admins (at most six, GitHub's limit), no wait timer,
#      and "prevent self-review" OFF. A sole maintainer dispatches and approves
#      their own release; with self-review prevented they never could. The
#      approval is the human decision; who dispatched is not the point.
#   3. Tag ruleset on refs/tags/v*: update, deletion and force-move restricted,
#      with NO bypass actor; creation NOT restricted.
#
#      Why creation is not restricted: publish creates the release tag as
#      github-actions[bot] after a person approves, and GitHub does not accept
#      the built-in GitHub Actions app as a bypass actor in an organization's
#      repository (HTTP 422: "Actor GitHub Actions integration must be part of
#      the ruleset source or owner organization"). Restricting creation would
#      therefore refuse the release itself. Without it, someone with write
#      access can create a v* tag the workflow will never release (no tag
#      trigger; preflight and publish refuse it), and, through GitHub's
#      release API, publish an unapproved, unsigned release. ADR 0060 § 3
#      records that as an OPEN DECISION: read it before applying this. A
#      published tag can never be moved or deleted, by anyone.
#   4. Immutable releases, when the repository offers them.
#
# Apply mode replaces an existing environment's protection rules and the tag
# ruleset with the above. It never adds a bypass actor. Run it as a human
# repository administrator, after the change that introduced it was reviewed.
# An AI agent never runs it except with --check (docs/governance/agents.md).

set -euo pipefail

readonly REPO="${RELEASE_REPO:-trustvian/trustvian}"
readonly ORG="${REPO%%/*}"
readonly RULESET_NAME="Protect release tags"

mode=apply
case "${1:-}" in
    "") ;;
    --check) mode=check ;;
    *) echo "usage: release-setup.sh [--check]" >&2; exit 2 ;;
esac

problems=0
report() { printf '  %-7s %s\n' "$1" "$2"; }
missing() {
    report MISSING "$1"
    problems=$((problems + 1))
}

echo "Release setup for $REPO ($mode)"

# --- The organization's admins, as reviewer ids ------------------------------------

admins="$(gh api "orgs/$ORG/members?role=admin&per_page=100" --jq 'map(.id) | sort | .[:6] | map(tostring) | join(",")' 2>/dev/null || true)"
[ -n "$admins" ] || { echo "  could not list $ORG's admins (needs read:org)"; exit 1; }

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

# --- 1 and 2. Environments ------------------------------------------------------------

ensure_env release-build ""
ensure_env release "$admins"

# --- 3. The v* tag ruleset ----------------------------------------------------------------

ruleset_json() {
    cat <<JSON
{
  "name": "$RULESET_NAME",
  "target": "tag",
  "enforcement": "active",
  "conditions": {"ref_name": {"include": ["refs/tags/v*"], "exclude": []}},
  "rules": [{"type": "update"}, {"type": "deletion"}, {"type": "non_fast_forward"}],
  "bypass_actors": []
}
JSON
}

ruleset_id="$(gh api "repos/$REPO/rulesets" \
    --jq ".[] | select(.target == \"tag\" and .name == \"$RULESET_NAME\") | .id")"
shape=""
if [ -n "$ruleset_id" ]; then
    shape="$(gh api "repos/$REPO/rulesets/$ruleset_id" --jq '
        "\(.enforcement)|\(.conditions.ref_name.include | sort | join(","))|\([.rules[].type] | sort | join(","))|\([(.bypass_actors // [])[] | "\(.actor_type):\(.bypass_mode)"] | sort | join(","))"')"
fi
readonly WANT_SHAPE="active|refs/tags/v*|deletion,non_fast_forward,update|"
if [ "$shape" = "$WANT_SHAPE" ]; then
    report ok "ruleset '$RULESET_NAME': v* update, deletion and force-move restricted; creation open to the release workflow; no bypass"
elif [ "$mode" = check ]; then
    missing "ruleset '$RULESET_NAME' is '${shape:-absent}', want '$WANT_SHAPE' (enforcement|refs|rules|bypass)"
elif [ -n "$ruleset_id" ]; then
    ruleset_json | gh api -X PUT "repos/$REPO/rulesets/$ruleset_id" --input - >/dev/null
    report "done" "ruleset '$RULESET_NAME' updated: creation unrestricted, update and deletion restricted, no bypass"
else
    ruleset_json | gh api -X POST "repos/$REPO/rulesets" --input - >/dev/null
    report "done" "ruleset '$RULESET_NAME' created"
fi

# --- 4. Immutable releases ---------------------------------------------------------------

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
