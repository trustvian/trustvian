# 0043 — `trustvian dev` provisions the local hierarchy from the repository

**Status:** Accepted

## Context

Watching an agent behave has, until now, required six commands: create a
project, an agent, a candidate and an evaluation run, start the run, then open
the TUI or the browser.
[ADR 0041](0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
removed the last step's typing. The four creates remained, and each one needs an
identifier the developer must invent and then keep consistent between runs — the
identifier *is* the learning scope
([ADR 0024](0024-learning-scope-is-a-baseline-key-dimension.md)), so an
inconsistent one silently produces a fresh baseline every time.

[Task 077](../tasks/v1.0/077-unified-otlp-local-dev-runtime.md) removes them. The
question this record answers is where the identifiers come from, given that
[ADR 0025](0025-platform-domain-values-with-caller-owned-identity.md) makes
identity caller-owned: the platform does not generate ids, so somebody must.

There is a second constraint, and it is the sharp one. The task states that the
application under test is never written to — "not its files, not its dependency
manifests, not its git state". But `make local` publishes its endpoint at
`./.trustvian/runtime.json`, in the working directory, which for dev is the
workload's repository.

## Decision

### 1. Identity is derived from the repository, and nothing is invented

| Value | Derivation | When it cannot be derived |
|---|---|---|
| Project | the git repository's directory name | `--project` required |
| Agent | the workload's own `OTEL_SERVICE_NAME`, or `service.name` in `OTEL_RESOURCE_ATTRIBUTES` | **refused**, with a message |
| Candidate | `git:<short sha>`, plus `+dirty` when the worktree has uncommitted changes | `--candidate` required |
| Environment | `local` | — |
| Run | `dev-<candidate>-<RFC 3339 UTC to milliseconds>` | — |

Every one is overridable by a flag. The derivations are conveniences; the flag
is the contract.

The Agent is the one that refuses rather than guessing, and that asymmetry is
deliberate. The processor derives the actor from the resource `service.name` on
the arriving spans. If dev invented an Agent id from, say, the executable's
name, the hierarchy would name one actor and the evidence another — and
`platform/behavior.go` refuses a record whose `Environment` differs from the
run's, so a mismatch surfaces as a run with no usable evidence rather than as an
error naming the cause. A workload that declares no service name is a workload
dev cannot place, so it says so.

For the same reason dev reads `service.name` out of `OTEL_RESOURCE_ATTRIBUTES` as
well as `OTEL_SERVICE_NAME`: both configure the same thing in the specification,
and honouring only one would refuse workloads that are correctly configured.

Three variables are then set, not negotiable, and named on the banner rather
than summarized: the OTLP endpoint, `service.name` (so the declaration dev read
is the declaration that arrives), and `deployment.environment.name` — without
the last one the engine cannot fill the record's environment and the run gets
zero usable evidence.

The run id carries milliseconds because a second-resolution stamp collides when
two runs start inside one second, and a collision is a run id already in use.

### 2. The hierarchy is ensured, not created

`dev_provision.go` does get-then-create for each of project, agent and
candidate: an existing one is used as it is. That is what makes the second run
of one candidate meet the first run's baseline instead of failing on a duplicate
id. Only the evaluation run is always new.

The run is started before the Collector, because nothing in the Collector
creates a run — run lifecycle is the control plane's
([ADR 0031](0031-control-plane-owns-ingest-and-http-is-an-adapter.md)) — and it
is completed or failed on the way out, with the child's own status preserved.

Instrumentation ownership is resolved *before* any of this
([ADR 0044](0044-instrumentation-ownership-requires-positive-evidence.md)): a
refusal must not leave a provisioned run behind.

### 3. State lives outside the repository, and discovery gains a second location

dev's state directory is `~/.trustvian/dev/<first 12 hex of sha256 of the
absolute workload path>/`, holding the generated Collector configuration, the
runtime and Collector logs, the pending-state note, the per-profile baseline and
its lock, and a `workload-path` file recording which directory the hash came
from.

Nothing is written into the workload's repository. `dev` prints the state path on
every start, because state a developer cannot find is state they cannot inspect
or delete.

That relocation has a consequence which has to be handled rather than accepted:
`resolveLocalDiscovery` read `./.trustvian/runtime.json`, so `trustvian eval
compare` run in the workload's directory would stop finding the endpoint — a real
regression against [task 062](../tasks/v1.0/062-integrated-local-developer-workflow.md)'s
"clients in this directory can omit `--api-url`". So the resolver gains a second,
documented location:

```text
--api-url given                    → that endpoint, always        (unchanged)
./.trustvian/runtime.json          → that endpoint                (unchanged)
~/.trustvian/dev/<hash of cwd>/    → that endpoint                (new)
neither                            → the existing operational error, naming both
```

Additive, in the root module, and it does not touch
[ADR 0035](0035-local-runtime-composes-platform-without-reversing-modules.md)
§10a: presence on the command line still decides, and `--api-url ""` is still a
usage error that reads no file.

### 4. Byte-identical is asserted, not claimed

`TestDevJourneyAgainstRealBinaries` hashes every file in a fixture repository —
including everything under `.git` — before and after two real runs, and compares
the trees. `runGit` passes `--no-optional-locks` so that even reading the git
state cannot write to it.

## Alternatives considered

**Generating identifiers dev owns (a uuid per run, a uuid per candidate).**
Rejected: a candidate id that changes per invocation is a fresh learning scope
per invocation, which is the failure ADR 0024 exists to prevent. The commit is
the natural identity of "this version of the agent".

**Deriving the Agent id from the command or the executable name.** Rejected: the
processor derives the actor from `service.name`, so a derived Agent id that
disagrees produces a run whose evidence cannot be attributed, and the symptom
(an empty run) does not name the cause.

**Writing the discovery file into the workload's directory anyway, and deleting
it afterwards.** Rejected: "afterwards" does not happen when the process is
killed, and a stray file in someone's repository — possibly committed — is
exactly what the constraint forbids. Crash-safety cannot be the difference
between honouring it and not.

**Keying the state directory by the repository name instead of the path hash.**
Rejected: two checkouts of one repository are two workloads with two baselines,
and a name would merge them.

**Requiring `--api-url` always, and never starting a runtime.** Rejected: that is
the six-command workflow with extra steps. Attaching to an existing control plane
stays supported, is what task 078's CI path uses, and is announced on the banner
so a developer knows which control plane they are looking at.

## Consequences

- A developer can watch an agent behave with one command, and two runs of one
  commit share a learning scope without anything being typed twice.
- A workload that declares no OpenTelemetry service name is refused until
  `--agent` is given. This is the intended trade: it is the one derivation whose
  wrong answer is silent.
- dev's state accumulates under `~/.trustvian/dev/`, one directory per workload
  path, and nothing prunes it. The `workload-path` file is what makes a
  directory identifiable when a developer goes looking; pruning is left for when
  it is a real complaint rather than an imagined one.
- `resolveLocalDiscovery` now has two locations, so an unexpected endpoint has
  two places to come from. The operational error names both.
- **Closed: the release archive ships both helpers**, so `trustvian dev` works
  from a downloaded release with no checkout. This was left open here and taken
  as its own change rather than folded into a feature change, which is what let
  the cost be measured instead of estimated.

  The resolution order needed no edit. Step 3 — *beside this binary* — was
  written for exactly this and carried the note "no release ships that way
  today; it costs nothing now and is the whole mechanism if the archive ever
  grows." The archive grew and the mechanism worked unchanged.

  Three things this ADR predicted, checked against what shipping it actually
  cost:

  | Predicted | Measured |
  |---|---|
  | "triples artifact size" | ~4.5x compressed, 8.8 MB → ~40 MB. `trustvian` 18.9 MB, `trustvian-local` 21.3 MB, `trustvian-collector` 39.5 MB uncompressed — a Collector build is larger than the CLI it accompanies |
  | "puts an OpenTelemetry Collector build into every release" | Into every **macOS and Linux** release. Not Windows, where `dev` refuses to start at all, so the helpers would be weight with no capability behind it |
  | "needs a supply-chain review" | Performed, and the finding is that the posture is unchanged: the archives carry a SHA-256 manifest and no attestation, before and after. Attesting them is a real gap, now recorded in `docs/supply-chain.md` rather than assumed |

  `docs/compatibility.md` classifies "Archive internal layout" as OBSERVATIONAL,
  so adding files breaks no promise. `go install` still yields only
  `trustvian`, because the helpers live in repository-internal modules the root
  module must not import — the not-found message names that case explicitly
  instead of implying a misconfiguration.
