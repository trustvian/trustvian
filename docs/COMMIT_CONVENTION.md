# Commit Convention

## Purpose

Trustvian uses a lightweight form of Conventional Commits for commit subjects
and pull request titles.

The point is not ceremony. Because every pull request lands as a **squash
merge**, the pull request title becomes a commit in `main` forever, and
`main`'s history is the raw material for release notes, `git log` archaeology,
and — eventually — automated changelog generation. A subject line that says
what kind of change it is and where it lands makes all three cheaper.

Two things this convention does *not* do: derive version numbers
automatically, or write `CHANGELOG.md` for you. Versions are chosen by a
maintainer and the changelog is written deliberately. The prefix is
information, not a trigger.

This convention applies **prospectively**. History before its adoption uses
short imperative subjects without a prefix and is not rewritten — published
history stays as it is.

## Format

```text
<type>(<scope>): <summary>

<body>

<footer>
```

`scope`, `body`, and `footer` are optional. The minimum valid commit is a type
and a summary:

```text
docs: clarify PostgreSQL configuration
```

A fuller example:

```text
feat(store): persist baselines across collector restarts

Baseline state lived only in memory, so a restart discarded everything the
engine had learned and every fingerprint looked novel again.

Readiness now reflects persistence availability rather than silently falling
back to in-memory storage, because a silent fallback changes the security
model without telling anyone.

Refs: #42
```

## Types

| Type | Use it for |
|---|---|
| `feat` | New user-visible functionality or capability |
| `fix` | A bug or incorrect behavior corrected |
| `security` | Security hardening or vulnerability remediation |
| `perf` | Performance improvement with no intended behavior change |
| `refactor` | Internal restructuring with no intended external change |
| `test` | Test-only changes |
| `docs` | Documentation-only changes |
| `ci` | CI, GitHub Actions, and repository automation |
| `build` | Build system, dependencies, packaging |
| `chore` | Repository maintenance that genuinely fits nothing else |

Pick the most meaningful type, not the easiest one. `chore` is not a generic
escape hatch — a dependency bump is `build`, a workflow edit is `ci`, a
scoring change is `feat` or `fix`.

Two distinctions worth stating, because this is a security engine:

- A change that alters what the engine **decides** is `feat` or `fix`, never
  `refactor`. `refactor` asserts that behavior is unchanged, and in
  `internal/policy`, `internal/anomaly`, or `internal/trust` that is a strong
  claim — make it only when it is true.
- Use `security` when the primary reason for the change is a security outcome.
  A dependency upgrade taken to remediate a vulnerability is `security`, not
  `build`.

## Scopes

A scope names the architectural area, not the file. Use one when it adds
information; omit it when the change is repository-wide.

Scopes that match this repository's actual structure:

```text
engine        the root package and Analyze/Observe wiring
event         the public event package
features      internal/features
fingerprint   internal/fingerprint
baseline      internal/baseline
anomaly       internal/anomaly
trust         internal/trust
policy        internal/policy
store         internal/store
postgres      internal/store/postgres
otel          internal/otel
config        config
alert         alert
cli           cmd/trustvian
processor     the OpenTelemetry Collector processor module
platform      the platform module: control plane, stores, /v1 API, trustvian-local
webui         the browser interface, platform/webui
examples      the examples module
deploy        deployments/
scripts       repository scripts
ci            workflows and CI automation
release       the release workflow and release tooling
governance    repository policy and branching documentation
```

This list is a guide, not a closed enumeration — a new area gets a new scope.
What to avoid is inventing one per file:

```text
good:   fix(postgres): retry the connection after recovery
good:   ci(release): verify the container provenance
good:   docs: document the branching strategy

avoid:  fix(postgres_store_go): ...
avoid:  fix(internal/store/postgres/store.go): ...
```

## Summary

The first line matters more than the rest. It should:

- be imperative — "add", not "added" or "adds";
- start lowercase after the colon;
- have no trailing period;
- stay within about 72 characters where practical;
- say what changes, specifically.

The test: *if applied, this commit will…*

```text
if applied, this commit will  →  add PostgreSQL baseline persistence
therefore                     →  feat(store): add PostgreSQL baseline persistence
```

Prefer verbs that commit to something: `add`, `remove`, `prevent`, `support`,
`validate`, `restore`, `persist`, `reject`, `document`, `simplify`, `reduce`,
`enforce`.

## Body

Use a body when the subject alone does not explain the change. The diff
already shows *what* the code does; the body is where *why* survives.

Write it when behavior, architecture, security semantics, persistence,
reliability, or compatibility changed — or when the reason is not obvious from
the code.

```text
good:

fix(store): treat PostgreSQL availability as part of readiness

A configured-but-unreachable database silently fell back to in-memory
storage, so the collector reported healthy while quietly changing its
persistence model mid-incident.
```

```text
avoid:

fix(store): fix storage

Changed store.go and config.go. Added an if statement. Updated tests.
```

Separate the subject from the body with exactly one blank line, and wrap prose
at roughly 72 characters. Do not wrap URLs, hashes, commands, or identifiers —
readability wins over the column.

## Footers

```text
Refs: #42      related to an issue, but does not resolve it
Closes: #42    merging this resolves the issue
Fixes: #42     merging this resolves the reported bug
```

Never invent an issue number. If you do not know one, omit the footer.

## Breaking Changes

Mark a breaking change with a `!` after the type/scope **and** a
`BREAKING CHANGE:` footer. Both, for visibility — one is easy to miss in a
subject line, the other is easy to miss in a long body.

```text
feat(config)!: replace legacy storage configuration

Replace the legacy storage configuration with the persistence provider
model.

BREAKING CHANGE: `storage.type` has been replaced by
`persistence.provider`. Existing deployments must update their
configuration before upgrading.
```

A breaking change must never hide behind `refactor` or `chore`.

Trustvian is pre-`v1.0`, and `0.x` is not permission to break things quietly.
Public API, configuration, and storage-format changes are called out whatever
the version number says — that discipline is what makes a stable `v1.0.0`
reachable. See
[Branching Strategy](governance/branching.md#pre-v10-discipline).

## Pull Request Titles

A pull request title **is** a commit subject, because squash merging turns it
into one. It follows the same rule:

```text
<type>(<scope>): <summary>
```

```text
feat(alert): add webhook notification routing
fix(postgres): restore readiness after a reconnect
docs(governance): define agent merge restrictions
```

The pull request *description* is a different document. It is review context —
motivation, what changed, how it was tested, compatibility and security impact
— and should not be squeezed into commit-message shape. The final commit body
may be distilled from it, but stays concise.

## Squash Merging

```text
branch commits          →   squashed   →   main
(may be messy)                              (one clean subject per change)
```

Because the squash commit is what survives, intermediate branch commits carry
less weight: local `wip` commits are fine while you work. What must not survive
into `main` is a subject like `WIP`, `temp`, `fix test`, `oops`, or `final2`.

One squash commit should represent one coherent change. Do not bundle a
feature, a CI fix, and a refactor into one pull request; equally, do not split
one coherent implementation into a dozen fragments.

Published history — anything already in `main` or reachable from a release tag
— is never rewritten.

## AI Agent Requirements

Agents follow this convention, and generate the message from the diff rather
than from the task they were given.

Before every commit:

1. Inspect `git status`, `git diff`, and `git diff --cached`.
2. Classify the *primary* change into one type.
3. Choose a scope only if it clarifies.
4. Write the subject as `<type>(<scope>): <imperative summary>`.
5. Add a body when behavior, architecture, security, persistence,
   reliability, or compatibility changed.
6. Add a footer only for issue numbers that actually exist.
7. Verify the message against `git diff --cached` — every claim in it must be
   visible in the staged change.

Then show the message before committing, and commit exactly that message.

Two failure modes to avoid specifically:

- **Reusing the task title as the commit subject.** The task describes what
  was asked; the commit describes what changed. They are often different, and
  the diff is the authority.
- **Claiming behavior the diff does not contain.** A commit that says it adds
  a test, or fixes a race, when the staged change does neither, is worse than
  a vague one — it puts a false claim into permanent history.

Use a heredoc or repeated `-m` flags for multi-line messages, and never
interpolate untrusted content into the commit command.

## Examples

```text
feat: add notification routing
feat(store): add PostgreSQL baseline persistence
fix(postgres): restore readiness after a reconnect
security(alert): reject unsafe callback targets
perf(anomaly): reduce baseline lookup allocations
refactor(store): isolate persistence initialization
test(postgres): cover recovery after a database outage
docs: document the commit convention
ci(release): verify the container provenance
build(otel): update OpenTelemetry dependencies
chore: remove obsolete development scripts
feat(config)!: replace legacy storage configuration
```

## Anti-Patterns

```text
Update README          no type, and says nothing
fixed bug              past tense, no type, no subject
changes                describes nothing
WIP                    not a finished change
final2                 not a description
fix stuff              vague
feat: Added new feature.   past tense, capitalized, trailing period
FEAT: add feature      type is lowercase
feature: add feature   not a valid type — it is `feat`
chore: updates         `chore` used as an escape hatch, and vacuous
```

## Related

- [CONTRIBUTING.md](../CONTRIBUTING.md) — the local gates and module layout
- [Branching Strategy](governance/branching.md) — branches, releases, and tags
- [Repository Governance](governance/repository.md) — review and merge authority
