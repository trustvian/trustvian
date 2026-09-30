# Platform CLI

The `trustvian` binary has two surfaces.

`analyze`, `baseline build` and `version` run the behavioral engine **in
process** against a file of events. They need no network and no server, and
nothing on this page changes them.

`project`, `agent`, `candidate` and `eval` drive a **local control plane** over
its `/v1` HTTP API. They are what this page is about: one request per command,
machine-readable output, and exit codes a CI job can branch on.

`dev` is neither: it is a process supervisor that composes the other two for one
run of your workload. It is documented [below](#trustvian-dev), and
[Local development](local-development.md) is the guide.

The CLI is a client. It computes no diff, no scorecard and no gate — those are
the control plane's, and the CLI reports what it returned. See
[ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md).

## Finding the control plane

`--api-url` is optional. When `make local` is running from the same working
directory, platform commands discover `.trustvian/runtime.json` automatically;
pass an endpoint to reach anything else.

```text
--api-url given    → that endpoint, always
--api-url omitted  → ./.trustvian/runtime.json
```

*Given* means present on the command line, not non-empty. `--api-url ""` is a
usage error (exit `2`) and reads no discovery file — in a script that is an
unset variable, and falling back to a local runtime there would silently run
the command against the wrong control plane.

```bash
# local: nothing to pass
trustvian project get --id proj-1

# anywhere else: name the endpoint
trustvian project get --api-url "https://control.example" --id proj-1
```

Explicit input always wins, which is what keeps CI and sandbox invocations
unaffected by whatever happens to be checked out. There is no environment
variable, no parent-directory search and no `$HOME` lookup — exactly
`./.trustvian/runtime.json`.

A **discovered** URL is held to a stricter rule than one you type: `http` on a
numeric loopback address, nothing else. A repository that ships its own runtime
file cannot redirect your commands to someone else's server.

With neither available the command exits **3**, not 2: the invocation was valid
and the runtime was missing, which is operational rather than a usage error.
See [local development](local-development.md).

The URL must be absolute `http` or `https`, with a host and no path, query,
fragment, or embedded credentials. A URL carrying a password is rejected — and
the diagnostic does not repeat it, because a rejection that echoes the secret
into shell history and CI logs has not protected anything.

## No authentication yet

The CLI sends no credentials and claims no transport guarantee. Task 070 owns
authentication; there is no `--token`, no `login`, and no credential store. Run
the control plane on loopback and treat it as the local trust boundary that it
is.

## Caller-owned identifiers

The CLI never generates an ID. Every `create` requires `--id`, and reuses
whatever your pipeline already calls the thing — a CI run number, a commit, a
release candidate. A generated identifier would be one nothing else in your
pipeline could reconstruct.

## Commands

```text
trustvian project create --id <id> --name <name>
trustvian project get    --id <id>

trustvian agent create   --id <id> --project-id <id> --name <name>
trustvian agent get      --id <id>

trustvian candidate create --id <id> --agent-id <id>
                           [--label] [--source-ref] [--artifact-digest]
                           [--model] [--toolset-digest] [--config-digest]
trustvian candidate get    --id <id>

trustvian eval create       --id <id> --candidate-id <id>
                            --environment <ref> --behavioral-profile <ref>
trustvian eval get          --id <id>
trustvian eval start        --id <id>
trustvian eval complete     --id <id>
trustvian eval fail         --id <id> [--reason <text>]
trustvian eval cancel       --id <id>
trustvian eval progress     --id <id>
trustvian eval ingest-state --id <id>
trustvian eval ingest       --id <id> --sequence <n>
                            --behavioral-profile <ref> --record <file>
trustvian eval compare      --reference-run <id> --candidate-run <id>
                            --max-added-behaviors <n> --max-block-decisions <n>
                            --max-critical-risk-observations <n>

trustvian env create   --project-id <id> --ref <ref> --name <name> [--rank <n>]
trustvian env get      --project-id <id> --ref <ref>
trustvian env list     --project-id <id>
trustvian env set      --project-id <id> --ref <ref> --revision <n>
                       [--name <name>] [--rank <n> | --clear-rank]
trustvian env archive  --project-id <id> --ref <ref> --revision <n>
trustvian env activate --project-id <id> --ref <ref> --revision <n>

trustvian promotion create --id <id> --reference-run <id> --candidate-run <id>
                           --target-environment <ref>
                           --max-added-behaviors <n> --max-block-decisions <n>
                           --max-critical-risk-observations <n>
trustvian promotion get    --id <id>
trustvian promotion list   --project-id <id>
```

Every command additionally accepts `[--api-url <url>]` and `[--json]`.

All of them accept `--json`.

Candidate metadata is descriptive only. The CLI runs no `git` command and
computes no digests: a value it derived would claim a provenance it cannot
actually vouch for.

**`--max-added-behaviors` counts behavioral identities**, not acts. A producer
emitting agent-oriented telemetry observes one act at two layers — the tool call
and the request the tool made — and each is its own behavioral identity:

```text
tool  · export_customer                 one identity
http  · POST → export.localhost      another identity
```

So one act can consume two of this budget, and `trustvian compare` now prints
both units so the difference is visible:

```text
Behaviors: +2 / -0 / 3 shared
Counted changes: 1 (policy 1)
```

A **counted change** is an added identity that is not the recorded child of
another added identity — where an identity is such a child only when every
retained occurrence of it is — so the tool and its transport child are two
identities and one change. Where the correlation is not complete — a run that
predates retention, one whose retained history saturated, or one whose recorded
parentage is ambiguous or cyclic — the line reads
`correlation partial: counted changes equal added behaviors`, because without
recorded parentage nothing may fold.

`--max-added-behaviors` still bounds **identities** and is unchanged, so no
existing pipeline's verdict moves. Identity itself is still not folded: that is
what keeps a tool changing destination detectable. See
[ADR 0052](adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)
and [ADR 0047](adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md).

At `--max-added-behaviors 0` none of this is observable. It matters the moment a
budget is nonzero.

`trustvian evidence` pages like every other collection here: `--limit` 1..64 and
an exclusive `--after` cursor the previous page printed. It aggregates nothing
client-side, because the status and exhaustiveness fields describe *one* page
read at one instant and stitching several together would blur them.

Two collections exist — `env list` and `promotion list` — because two entities
have a consumer that needs one, and both traverse a project by an immutable key
in byte order with the server bounding every page. There is no `search`, and no
`update` or `delete` for a promotion, because the API has no such routes: a
client-side list would have to invent ordering, paging and scoping nothing has
decided, and a recorded decision is history rather than a row to edit.

### Following a finding to its evidence

`trustvian evidence` answers the question a failed gate leaves open. The gate
prints a number; this turns the number into the evidence behind it, in two steps
— which behavioral identities contributed, and which retained observations
carried them.

```console
$ trustvian eval compare --reference-run run-ref --candidate-run run-cand     --max-added-behaviors 0 --max-block-decisions 0     --max-critical-risk-observations 0
GATE   FAIL
  added_behaviors        actual 2   maximum 0   FAIL

$ trustvian evidence behaviors --reference-run run-ref --candidate-run run-cand     --check added_behaviors
STATUS  resolved
SIDE    candidate
RECORDED 2

BEHAVIORS (2)
  fp-delete         added     tool · delete_customer
  fp-export         added     tool · export_customer → export.localhost

HISTORY complete (4 retained)

$ trustvian evidence observations --reference-run run-ref     --candidate-run run-cand --behavior fp-export
STATUS  resolved
SIDE    candidate
RECORDED 2
EXHAUSTIVE true

OBSERVATIONS (2)
  seq 2      2026-03-04T05:06:07Z   block    critical tool · export_customer → export.localhost
  seq 4      2026-03-04T05:06:09Z   block    low      tool · export_customer → export.localhost

HISTORY complete (4 retained)
```

`--check` takes the gate's own check names. Three of the five resolve to
evidence: `added_behaviors` to behaviors and then to each behavior's
observations, and `block_decisions` and `critical_risk_observations` straight to
the observations that carried them. `reference_evidence` and
`candidate_evidence` report `aggregate_only` — they fail when a run observed
*too little*, and an absence has no supporting records to link to.

**`RECORDED` and the rows below it are different measurements.** The recorded
count is what the gate counted, across every record the run ingested. The rows
are what retained history still holds, which is bounded and may be partial. A
larger recorded count beside a shorter list is bounded retention describing
itself, not a discrepancy — and `EXHAUSTIVE` says whether paging to the end
would yield all of them.

**An empty answer is not always the same answer.** `none_found` means the
history is complete and nothing matched, so nothing happened. `indeterminate`
means the history is partial or predates retention, so the absence establishes
nothing. The command prints which, because the two look identical otherwise.

**The status describes the finding, not the page.** Paging past the last match
prints `STATUS resolved` with no rows and a note saying the evidence is on the
earlier pages — never "no supporting evidence", which would be the opposite of
the truth at exactly the moment you finished reading it. Whether more rows
follow is the `--after` cursor's job, printed as `more: --after <cursor>` and
absent when there are none.

**A behavior present in both runs needs `--side`.** Both runs hold their own
observations of it, and those two sets are what you are comparing — so the
control plane refuses to pick one for you:

```console
$ trustvian evidence observations --reference-run run-ref     --candidate-run run-cand --behavior fp-shared
trustvian: behavior "fp-shared" is present in both runs; name the side to
resolve — reference or candidate

$ trustvian evidence observations --reference-run run-ref     --candidate-run run-cand --behavior fp-shared --side reference
```

An added or removed behavior needs no `--side`: it exists in one run only, and
naming the other is refused rather than silently honoured. A `--check` never
takes one, because a gate check already counts one side.

### Recording a promotion

`trustvian promotion create` records a **decision**. It does not deploy
anything, and Trustvian has no observer that could tell whether a deployment
followed — so the command prints the boundary rather than implying otherwise:

```text
Trustvian recorded this decision. Nothing was deployed.
```

Three things are deliberately absent from the request. There is no
`--source-environment`: the source is inferred from the environment the two
runs share, and a caller-supplied one could disagree with the evidence. There
is no `--outcome`: the outcome is derived from the gate verdict alone, and the
server rejects a request that tries to set one. And there is no actor or
approval flag: the platform records what it decided on evidence, not who asked.

A **rejected** promotion is a successful call and exits `0`. The gate said
FAIL, the platform recorded that decision durably, and nothing went wrong —
exit `1` keeps its single existing meaning, a gate FAIL from `eval compare`.
Both verdicts are recorded on purpose: the limits are yours, so a history that
showed only acceptances would hide an attempt retried with looser ones.

`promotion_id` is the only identity. Re-sending a request with an identifier
that already exists is `409 already_exists` — never a silent overwrite, and
never an idempotent replay decided by comparing request bodies. On a retry
after an ambiguous failure, `promotion get --id <id>` tells you whether the
first attempt landed.

## A worked example

```bash
URL="http://127.0.0.1:8080"

trustvian project   create --api-url "$URL" --id proj-1  --name "Checkout"
trustvian agent     create --api-url "$URL" --id agent-1 --project-id proj-1 --name "Checkout agent"
trustvian candidate create --api-url "$URL" --id cand-2  --agent-id agent-1 \
    --label "v2" --source-ref "$GIT_SHA"

trustvian eval create --api-url "$URL" --id run-42 \
    --candidate-id cand-2 --environment local --behavioral-profile checkout-agent
trustvian eval start  --api-url "$URL" --id run-42

# ... your harness produces decision records ...

trustvian eval ingest --api-url "$URL" --id run-42 \
    --sequence 1 --behavioral-profile checkout-agent --record record-1.json

trustvian eval complete --api-url "$URL" --id run-42
trustvian eval progress --api-url "$URL" --id run-42
```

## Ingest and the sequence

`--sequence` is a canonical decimal integer of at least 1, and it is yours to
track. The CLI keeps no cursor, state file or retry database: re-running the
command re-sends the same explicit sequence, and the server's idempotency
contract decides whether that is a new record or a replay.

`trustvian eval ingest-state` reports the sequence the server expects next.

The record file is sent **exactly as written**. The CLI validates that it is
well-formed JSON and nothing else — it never decodes it into its own struct,
so a record from a newer producer keeps every field on its way to the server.

## `trustvian dev`

```text
trustvian dev [options] -- <command> [args...]
```

Runs your command under Trustvian. The command is not modified and gains no
Trustvian dependency: a local control plane, an OTLP receiver and an evaluation
run are composed around it. `--` is required, and everything after it is the
command.

```text
--api-url <url>          attach to a control plane already running, instead of
                         starting one
--local-bin <path>       path to trustvian-local      ($TRUSTVIAN_LOCAL_BIN)
--collector-bin <path>   path to trustvian-collector  ($TRUSTVIAN_COLLECTOR_BIN)
--instrumentation <mode> existing | none | auto   (default: auto)
-h, --help               print the usage message
```

Identity, each derived when not given:

| Flag | Default |
|---|---|
| `--project <id>` | the git repository's name |
| `--agent <id>` | the workload's own `OTEL_SERVICE_NAME`, or `service.name` in `OTEL_RESOURCE_ATTRIBUTES` — **required** when it declares neither |
| `--candidate <id>` | `git:<short sha>`, plus `+dirty` when the worktree has uncommitted changes |
| `--environment <ref>` | `local` |
| `--run-id <id>` | `dev-<candidate>-<UTC timestamp to milliseconds>` |

The candidate is also the behavioral profile, which is the learning scope — so
two runs of one commit meet one baseline. `--agent` is the one derivation that
refuses rather than guessing: the processor derives the actor from the arriving
`service.name`, and an invented Agent id would produce a run whose evidence
cannot be attributed to it.

### Instrumentation ownership

`dev` configures an exporter; it does not attach an SDK.

| Mode | Behavior |
|---|---|
| `existing` | your workload already sends OpenTelemetry; `dev` sets the OTLP variables and injects nothing |
| `none` | `dev` manages no instrumentation at all, not even routing |
| `auto` (default) | resolve from positive evidence, or **stop** |

`auto` never falls through to injection. Absence of detectable instrumentation
is not evidence of absence — a workload that initializes the SDK a moment after
it starts cannot be detected beforehand, and a second stack attached to one that
exists reports every action twice, which reads as an actor behaving strangely.
So `auto` either finds positive evidence (an `OTEL_*` variable that an
auto-configuring SDK reads, `opentelemetry-instrument` in `argv[0]`, an
OpenTelemetry `-javaagent:`, or `NODE_OPTIONS` requiring an `@opentelemetry`
package) and behaves as `existing`, or it stops with a message naming the
choices. `OTEL_SDK_DISABLED=true` is refused for every mode that would route.
See [ADR 0044](adr/0044-instrumentation-ownership-requires-positive-evidence.md).

`--instrumentation python-zero-code` is named and reserved, and refused by this
build.

### What it sets, and what it does not touch

Every variable `dev` adds to the workload's environment is printed on the banner,
by name. With ownership `existing` the three that matter are:

```text
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT   the composed receiver
OTEL_SERVICE_NAME                    the Agent identity dev resolved
OTEL_RESOURCE_ATTRIBUTES             deployment.environment.name=<environment>
```

`TRUSTVIAN_DEV_OTLP_ENDPOINT` and `TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT` are also
exported, for a workload that builds its exporter in code and chooses its own
transport, alongside the protocol, exporter-selection and semantic-convention
variables the banner lists.

**`--instrumentation none` still declares identity.** It means `dev` routes no
telemetry — it does not mean `dev` sets nothing. Two variables remain, for
reasons that are not cosmetic:

```text
OTEL_RESOURCE_ATTRIBUTES   deployment.environment.name=<environment>, appended
OTEL_SERVICE_NAME          the Agent, and only when the workload declares none
```

`deployment.environment.name` is how the engine fills a record's environment, and
the platform **refuses** a record whose environment differs from its run's — so
without it a run collects zero usable evidence while everything else looks
healthy. `service.name` is the actor the run is about: the processor derives it
from the arriving spans, so a run provisioned for one agent and telemetry
declaring another cannot be attributed. `OTEL_SERVICE_NAME` is left alone when the
workload already declares its own, because relabelling somebody else's telemetry
is not `dev`'s to do.

Everything else — the endpoint, the protocol, the exporter selections, the batch
delay, the semantic-convention opt-in — is routing, and `none` sets none of it.

Your repository is never written to — not its files, not its dependency
manifests, not its git state. `dev`'s own state lives under
`~/.trustvian/dev/<hash of the workload directory>/` and the path is printed on
every start.

### Signals and exit status

`dev` is transparent: its exit status is your command's own, including
`128+signal` when the command was killed by one. `SIGINT` and `SIGTERM` are
forwarded to the command's process group, and the evaluation run is **completed**
rather than failed when the developer stopped it — a developer pressing `Ctrl-C`
has not observed a failing agent.

If your command's stdin is a terminal, `dev` hands the terminal to it, so an
interactive workload can read input. `Ctrl-C` is then delivered by the kernel to
the whole foreground group, which includes `dev`. A second `Ctrl-C` during
teardown is not delivered to `dev`, because the terminal is already back — see
[compatibility](compatibility.md).

Windows is refused rather than partially supported: there is no `SIGTERM`
delivery, no `os.Interrupt` for another process, and no `Setpgid`, so a wrapper
could not stop what it started. Use WSL2, or run the parts separately.

### Exit codes, before the command starts

| Code | Meaning |
|---|---|
| `2` | the invocation was wrong (unknown flag, no `--`, unknown mode) |
| `3` | this wrapper could not start (no helper binary, ownership unresolved, another run holds the baseline) |

Once the command starts, the exit status is entirely the command's. That is why
`dev` is the one exception in the table below.

## Exit codes

Exit codes are **scoped by command family**. This matters, so it is worth
reading once rather than assuming.

| Commands | 0 | 1 | 2 | 3 |
|---|---|---|---|---|
| `analyze`, `baseline`, `version` | success | command failed | top-level usage | — |
| `project`, `agent`, `candidate`, most of `eval` | success | *unused* | usage | API or network failure |
| `eval compare` | gate **PASS** | gate **FAIL** | usage | API or network failure |
| `dev` | the command’s own | the command’s own | usage | could not start |

**Exit 1 means gate failure only for `eval compare`.** It does not change what
`1` has always meant for `analyze` and `baseline`.

`dev` is the exception to the whole table: once the command it wraps has started,
`dev`'s exit status is that command's own, whatever it is. `2` and `3` can only
be reported *before* the command starts.

The distinction that matters for CI: an HTTP 409, a 404, a 500, a timeout, a
refused redirect and a malformed response are all **3**, never 1. A script
written as "non-zero means the gate failed" would otherwise report a broken
network as a policy violation.

## CI: comparing a candidate against a reference

```bash
#!/usr/bin/env bash
# Note: no `set -e` around the compare itself — a gate FAIL is an expected
# outcome to branch on, not a script error. With `set -e` enabled elsewhere,
# run the command in a condition or capture $? immediately, as below.
set -uo pipefail

URL="http://127.0.0.1:8080"

trustvian eval compare \
  --api-url "$URL" \
  --reference-run baseline \
  --candidate-run candidate \
  --max-added-behaviors 0 \
  --max-block-decisions 0 \
  --max-critical-risk-observations 0 \
  --json > comparison.json
status=$?

case $status in
  0) echo "gate passed" ;;
  1) echo "gate failed"; exit 1 ;;
  *) echo "evaluation error (exit $status)"; exit "$status" ;;
esac
```

`comparison.json` is written on **both** `0` and `1`. A gate FAIL is a result,
not an error: CI needs the evidence to publish alongside the failure, so it
goes to stdout in both cases and stderr stays empty.

All three gate limits are required, and an omitted limit is **not** zero. Zero
is the strictest limit there is; defaulting to it would fail your build under a
policy you never chose, and it would look exactly like a real regression.

## `--json`

`--json` writes the API's own successful response body to stdout, unchanged
apart from a trailing newline. The CLI adds no wrapper and removes no field, so
a field a newer server added still reaches you.

On failure, the server's error envelope goes to **stderr** and the command
exits 3:

```json
{"version":"1","error":{"code":"conflict","message":"run is not running"}}
```

Results always go to stdout, diagnostics always to stderr. A pipeline capturing
stdout gets evidence or nothing.

## Bounds

```text
request timeout    30s
request body       ≤ 256 KiB (matches the server's limit)
response body      ≤ 4 MiB
redirects          refused
retries            none
```

Redirects are refused rather than followed: a mutation addressed to one host
must not silently become a mutation against another.

There are no automatic retries. A `GET` is safe to repeat but a lifecycle
`POST` is not, and ingest already has explicit sequence semantics — so retrying
is a decision for your script, which knows what it was doing.

## Related

- [Compatibility contract](compatibility.md) — what is stable here
- [ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md) — why the CLI is
  an HTTP adapter
- [Task 060](tasks/v1.0/060-developer-cli.md) — the specification
- [ADR 0040](adr/0040-promotions-are-immutable-evidence-backed-platform-decisions.md)
  — why a promotion records a decision and never a deployment
- [Local development](local-development.md) — the `trustvian dev` guide
- [Task 077](tasks/v1.0/077-unified-otlp-local-dev-runtime.md) — the `dev`
  specification
