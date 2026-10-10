---
name: ship
description: Commit, push and open (or update) a Trustvian pull request the way this repository requires — commit messages generated from the staged diff per docs/COMMIT_CONVENTION.md, the verify-all gate before anything leaves the machine, a squash-ready PR title checked with `make pr-title`, a PR body in the repo's established style, and a hard stop at WAITING FOR HUMAN ORGANIZATION ADMIN MERGE. Use whenever the user says "commit", "commit this", "ship it", "push", "open a PR", "raise a PR", "update the PR description", "send this for review", "wrap this up", or finishes a task and asks what's next — even if they only say "commit", because a Trustvian commit message has rules a generic one breaks. Invoking this skill is the user's explicit request to commit and push.
---

# ship

Take finished work from the working tree to a pull request that a human
Organization Admin can review and merge. The skill commits, pushes and opens
the PR. It never merges, never approves, never enables auto-merge — that line
is the repository's governance (CLAUDE.md, `docs/governance/agents.md`), not a
preference.

Invoking `/ship` (or asking to commit/push/open a PR) is the explicit request
CLAUDE.md requires before committing or pushing. It is not standing
permission: the next round of changes needs its own request.

## Why the rules are strict here

Trustvian squash-merges. The **PR title** becomes a permanent commit on
`main`, read later by release notes and `git log` archaeology. The branch
commits are squashed away but are still read during review, so they should
say what each step changed. The PR **body** is the reviewer's context — the
human who merges reads it to decide.

The most common failure is writing the message from the task rather than the
diff. The task says what was asked; the diff says what changed. A message that
claims a test, a fix or a measurement the diff does not contain puts a false
statement into permanent history — worse than a vague message.

## 1. Look before staging

```bash
git status --short
git branch --show-current
git diff --stat; git diff --cached --stat
git log --oneline origin/main..HEAD
```

- **On `main`?** Never commit there. Create a branch first:
  `git switch -c <type>/<short-description>` — lowercase, hyphenated, type
  from the list in §3, no ticket-number-only names, no personal prefixes
  (`docs/governance/branching.md`). Example: `fix/postgres-readiness-after-reconnect`.
- **Decide what belongs in this change.** Stage by path (`git add <paths>`),
  never `git add -A` / `git add .` blindly. Files that look unrelated to the
  work — local `.claude/` edits, `CLAUDE.md` tweaks, scratch files — are often
  the user's own in-progress work. If it is not obvious they belong, ask
  rather than sweep them in.
- **Never stage** `go.work` / `go.work.sum` (gitignored on purpose — CI runs
  `GOWORK=off`), `.env*`, credentials, `bin/`, `.trustvian/` state, or
  `*-workspace/` eval output.
- **One coherent change per commit.** If the diff mixes a feature, a CI fix
  and a refactor, propose separate commits (stage them separately). Equally,
  don't fragment one implementation into many tiny commits.

## 2. Gate before anything leaves the machine

Before the first **push** of a round (not before every local commit — those
are squashed anyway), run the `verify-all` skill for the modules the branch
touched. If any check fails, stop: report the failure, do not push, do not
open the PR. Fixing it is a separate decision for the user.

Keep verify-all's result — it is the *Testing* evidence in the PR body.
Anything verify-all reports as **skipped** (Postgres tests without
`TRUSTVIAN_TEST_POSTGRES_DSN`, the drill without its env var) is reported as
skipped, never as passed.

If the user explicitly says to skip the gate, do so and say in the PR body
that it was not run locally.

## 3. Write the commit message from `git diff --cached`

Read the staged diff itself (not just `--stat`). Then:

**Type** — the *primary* change, the most meaningful type, not the easiest:

| Type | For |
|---|---|
| `feat` | new user-visible functionality |
| `fix` | a bug or incorrect behavior corrected |
| `security` | primary purpose is a security outcome (incl. a dependency bump that remediates a vuln) |
| `perf` | faster/leaner, no behavior change |
| `refactor` | restructuring, no behavior change |
| `test` | test-only |
| `docs` | documentation-only |
| `ci` | workflows, repository automation |
| `build` | build system, dependencies, packaging |
| `chore` | genuinely nothing else — not an escape hatch |

Two traps specific to a security engine: anything that changes what the
engine **decides** (`internal/policy`, `anomaly`, `trust`, learning
eligibility) is `feat` or `fix`, never `refactor` — `refactor` is a claim that
behavior is unchanged. And a breaking change to public API, config or storage
format gets `!` after the type/scope **and** a `BREAKING CHANGE:` footer, even
pre-v1.0.

**Scope** — the architectural area, not the file, and only if it adds
information: `engine event features fingerprint baseline anomaly trust policy
store postgres otel config alert cli platform processor examples deploy
scripts ci release governance`. A new area may get a new scope; never
`fix(store_go)` or a path.

**Subject** — `<type>(<scope>): <summary>`: imperative ("add", not
"added"), lowercase after the colon, no trailing period, ~72 chars. Test: *if
applied, this commit will …*. Prefer verbs that commit to something: add,
remove, prevent, support, validate, restore, persist, reject, document,
enforce.

**Body** — when behavior, architecture, security semantics, persistence,
reliability or compatibility changed, or the *why* isn't obvious. The diff
shows what; the body keeps why. This repo's style (see `git log`): a short
plain paragraph of context, then hyphen bullets naming each concrete change
and the reasoning behind non-obvious choices. Name decisions (`D8`), ADRs and
tasks when the diff implements them. Wrap prose ~72 cols; don't wrap
identifiers, commands or URLs.

**Footers** — `Refs:/Closes:/Fixes: #N` only for issue numbers that really
exist (check with `gh issue view N` if unsure). Never invent one. End with the
co-author trailer given in the session's attribution reminder, if any.

**Self-check** — reread every sentence against the staged diff. Each claim
(a test added, a count, a benchmark number, "unchanged") must be visible in
it. Delete anything that isn't.

Show the full message to the user, then commit **exactly** that message via a
heredoc, never by interpolating diff-derived text into `-m "…"`:

```bash
git commit -F - <<'EOF'
fix(postgres): restore readiness after a reconnect

…body…

Co-Authored-By: …
EOF
```

If a pre-commit hook fails, fix the cause and make a **new** commit attempt —
don't `--no-verify`, and don't `--amend` a commit that was already pushed.

## 4. Push

```bash
git push -u origin "$(git branch --show-current)"
```

Never to `main`. No `--force` / `--force-with-lease` unless the user asked for
it on their own feature branch — and never on `main` or a tag. A rejected push
(remote moved) means fetch and rebase/merge, then tell the user what you did.

## 5. PR title

The title is the eventual squash commit on `main`, so it summarizes the
**whole branch** (`git diff origin/main...HEAD`, `git log origin/main..HEAD`),
not the last commit — and still not the task title. Same format as a commit
subject. Check it:

```bash
make pr-title TITLE='feat(platform): persist per-behavior fidelity at schema 13'
```

CI runs the same check on every PR; a failing title is a failing PR. A
length note over 72 chars is advisory — tighten it if you can.

## 6. PR body

Write it to a file in the scratchpad and pass `--body-file` (never inline
untrusted text into a shell argument). Follow the shape reviewers here are
used to (PR #164 is the reference — `gh pr view 164` to see it):

1. **Opening paragraph** — task/ADR/milestone it implements, what it delivers
   in one or two sentences, and what it explicitly does *not* change (e.g.
   "Engine, fingerprints and baseline keys are unchanged.").
2. **`## Commits`** (or "one per layer" when that's how it was built) — a
   numbered list, one entry per branch commit: bold name, its type/scope in
   backticks, then sub-bullets with the concrete changes. Skip this section
   for a single small commit; a few bullets suffice.
3. **`## Measured`** — only when there are real numbers from this work
   (benchmarks, ingest/storage, runtime results). Tables welcome. Never
   estimate or round a number into existence.
4. **`## Testing`** — what verify-all ran per module, pass/fail/**skipped**
   honestly, plus any manual runs.
5. **`## Decide`** — open questions only a human can settle: a rule chosen
   among alternatives, a known gap, a compatibility discontinuity, a CI-time
   risk. Each with the choice made and the alternatives. Omit if there are
   none — don't manufacture them.
6. The stop line, then the attribution line from the session's attribution
   reminder:

```text
WAITING FOR HUMAN ORGANIZATION ADMIN MERGE

🤖 Generated with [Claude Code](https://claude.com/claude-code)
```

Tone: plain, precise, specific. Identifiers in backticks. No marketing
adjectives, no "this PR aims to".

## 7. Open or update

```bash
gh pr view --json number,url,title 2>/dev/null   # existing PR for this branch?
gh pr create --base main --head "$(git branch --show-current)" \
  --title "$TITLE" --body-file "$BODY_FILE"      # new
gh pr edit <n> --title "$TITLE" --body-file "$BODY_FILE"   # existing: keep it current
```

When updating an existing PR after new commits, revise the body so it still
describes the whole branch — don't just append.

Then `gh pr checks <n>` once and report the state. Don't sit polling CI
unless the user asks you to watch it.

## 8. Stop

Report: branch, commit subject(s), PR URL, the gate's result, CI state, and
anything the *Decide* section asks of the reviewer. End with:

```text
WAITING FOR HUMAN ORGANIZATION ADMIN MERGE
```

Do not merge, do not `gh pr merge --auto`, do not approve or request an
approval from an agent identity, do not touch rulesets or bypass lists. If
something seems to require one of those, stop and ask a human — technical
capability is not authorization.
