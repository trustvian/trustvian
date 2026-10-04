# Trustvian Sentinel

Sentinel is the repository's English-language Copilot code-review rubric.
GitHub posts reviews under Copilot's identity; this configuration does not
create or rename a GitHub account. Its report requests roadmap alignment,
code quality and change-risk scores, evidence, findings and a recommendation.
The scores are advisory and never replace tests or required human review.

## Configuration

The rubric is `.github/copilot-instructions.md`. Copilot instruction compliance
and the exact summary layout are best effort; this is not a deterministic
scoring service or a guarantee that every review contains a score table.
No additional AI provider secret or privileged Actions workflow is introduced.
Copilot availability, entitlement and usage charges follow GitHub's current
Copilot policies. Do not claim the integration is active before checking it.

## Human administrator activation

After the instructions are merged, an authorized human administrator configures
Settings → Rules → Rulesets → New branch ruleset:

- Name: Trustvian Sentinel — automatic Copilot review.
- Enforcement: Active.
- Target: all branches, to cover PRs targeting any branch.
- Enable Automatically request Copilot code review.
- Enable Review new pushes and Review draft pull requests.
- Add no bypass actors. Leave Protect main and release-tag rules unchanged.

Under Settings → Copilot → Code review, enable custom instructions if disabled.
Keep Allow Copilot to approve pull requests and Allow Copilot approvals to count
toward merge requirements disabled. Choose the supported review effort that
matches the repository's budget; Balanced is appropriate for security-sensitive
changes if available.

This is a governance configuration performed by a human, following
[agent governance](governance/agents.md). An agent must not configure or use
ruleset bypasses. Merely adding this file does not enable automatic reviews.

## Acceptance check

1. Open a small test PR after activation and verify Copilot is requested.
2. Confirm an English review references the current commit and attempts the
   Sentinel rubric; inspect its evidence rather than trusting numeric scores.
3. Push a new commit and verify a new review is requested for that commit.
4. Check a draft and, where available, a fork PR. Record entitlement or platform
   limitations rather than claiming universal coverage from one successful PR.
5. Confirm required human approval and merge restrictions are unchanged.

Existing PRs may need Copilot requested manually from the Reviewers menu.
If Copilot omits the summary, request a re-review and inspect its instruction
references. Do not fabricate a Sentinel report or mark an unreviewed PR reviewed.
For a strictly guaranteed separate comment and machine-validated scoring,
additional orchestration would be needed; this native integration does not
promise that behavior.

## Official references

- [Configure automatic Copilot reviews](https://docs.github.com/en/copilot/how-tos/copilot-on-github/set-up-copilot/configure-code-review)
- [Use Copilot code review](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-code-review)
- [Repository custom instructions](https://docs.github.com/en/copilot/how-tos/configure-custom-instructions/add-repository-instructions)
