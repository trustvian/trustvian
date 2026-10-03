#!/usr/bin/env bash
#
# One-time repository setup for the release pipeline. Idempotent.
#
#   ./scripts/release-setup.sh --check   # read-only: report what is and is not set up
#   ./scripts/release-setup.sh           # apply what is missing (an administrator)
#
# Sets up, through `gh api`:
#   1. the `release` environment, deployable from main only;
#   2. the v* tag ruleset: creation, update and deletion restricted, with
#      Organization Admin as the only bypass actor;
#   3. Immutable releases, when the repository offers them.
#
# What it never does:
#   - add any bypass actor other than Organization Admin, and never a bot, an
#     app or an automation identity (CLAUDE.md, docs/governance/agents.md);
#   - edit an existing ruleset or environment. If either exists but differs
#     from the above, it reports how and exits non-zero. Changing a ruleset is a
#     deliberate human action in the GitHub UI, where it is recorded in the
#     rule history.
#
# The release workflow needs no bypass of its own. It never creates a tag:
# scripts/release.sh, run by a human Organization Admin, creates it, and
# `publish` only waits for it. `gh release create --verify-tag` on an
# existing tag needs contents: write and creates no ref, so the ruleset does
# not apply to it. See docs/release-guide.md § One-time setup.
#
# Applying needs an administrator of the repository. --check needs read
# access only.

set -euo pipefail

readonly REPO="${RELEASE_REPO:-trustvian/trustvian}"
readonly RULESET_NAME="Protect release tags"

mode=apply
case "${1:-}" in
    "") ;;
    --check) mode=check ;;
    *) echo "usage: release-setup.sh [--check]" >&2; exit 2 ;;
esac

problems=0
report() { printf '  %-6s %s\n' "$1" "$2"; }
missing() {
    report MISSING "$1"
    problems=$((problems + 1))
}

echo "Release setup for $REPO ($mode)"

# --- 1. The release environment ---------------------------------------------

# gh api prints a 404's body on stdout, so the fallback replaces the output
# rather than appending to it.
if ! env_policy="$(gh api "repos/$REPO/environments/release" \
    --jq '.deployment_branch_policy | if . == null then "none" else "\(.protected_branches) \(.custom_branch_policies)" end' \
    2>/dev/null)"; then
    env_policy=absent
fi
branches=""
if [ "$env_policy" = "false true" ]; then
    branches="$(gh api "repos/$REPO/environments/release/deployment-branch-policies" \
        --jq '[.branch_policies[] | "\(.type):\(.name)"] | sort | join(",")')"
fi

if [ "$env_policy" = "false true" ] && [ "$branches" = "branch:main" ]; then
    report ok "environment 'release' deploys from main only"
elif [ "$mode" = check ]; then
    missing "environment 'release' restricted to main (now: policy=$env_policy branches=${branches:-none})"
elif [ "$env_policy" != absent ]; then
    # Never edit an existing environment: a PUT replaces its protection rules
    # (reviewers, wait timer) with whatever the request omits.
    missing "environment 'release' exists but is not restricted to main only (policy=$env_policy branches=${branches:-none}); fix it in Settings → Environments"
else
    gh api -X PUT "repos/$REPO/environments/release" --input - >/dev/null <<'JSON'
{"deployment_branch_policy": {"protected_branches": false, "custom_branch_policies": true}}
JSON
    gh api -X POST "repos/$REPO/environments/release/deployment-branch-policies" \
        -f name=main -f type=branch >/dev/null
    report "done" "environment 'release' created, deployable from main only"
fi

# --- 2. The v* tag ruleset ----------------------------------------------------

ruleset_id="$(gh api "repos/$REPO/rulesets" \
    --jq ".[] | select(.target == \"tag\" and .name == \"$RULESET_NAME\") | .id")"
if [ -n "$ruleset_id" ]; then
    shape="$(gh api "repos/$REPO/rulesets/$ruleset_id" --jq '
        "\(.enforcement)|\(.conditions.ref_name.include | sort | join(","))|\([.rules[].type] | sort | join(","))|\([(.bypass_actors // [])[] | "\(.actor_type):\(.bypass_mode)"] | sort | join(","))"')"
    IFS='|' read -r enforcement include rules bypass <<<"$shape"
    ok=1
    [ "$enforcement" = active ] || { missing "ruleset '$RULESET_NAME' is $enforcement, not active"; ok=0; }
    [ "$include" = "refs/tags/v*" ] || { missing "ruleset '$RULESET_NAME' covers '$include', not refs/tags/v*"; ok=0; }
    for rule in creation update deletion; do
        [[ ",$rules," == *",$rule,"* ]] || { missing "ruleset '$RULESET_NAME' does not restrict $rule"; ok=0; }
    done
    [ "$bypass" = "OrganizationAdmin:always" ] ||
        { missing "ruleset '$RULESET_NAME' bypass is '$bypass', want only OrganizationAdmin:always"; ok=0; }
    [ "$ok" = 1 ] && report ok "ruleset '$RULESET_NAME': v* creation, update and deletion restricted; Organization Admin bypass only"
elif [ "$mode" = check ]; then
    missing "ruleset '$RULESET_NAME' on refs/tags/v*"
else
    gh api -X POST "repos/$REPO/rulesets" --input - >/dev/null <<JSON
{
  "name": "$RULESET_NAME",
  "target": "tag",
  "enforcement": "active",
  "conditions": {"ref_name": {"include": ["refs/tags/v*"], "exclude": []}},
  "rules": [{"type": "creation"}, {"type": "update"}, {"type": "deletion"}, {"type": "non_fast_forward"}],
  "bypass_actors": [{"actor_type": "OrganizationAdmin", "actor_id": null, "bypass_mode": "always"}]
}
JSON
    report "done" "ruleset '$RULESET_NAME' created"
fi

# --- 3. Immutable releases --------------------------------------------------------

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
