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
                            [--max-added-behavior-changes <n>]
trustvian eval run          --scenario <file> [--reference <execution-id>|last]
                            [--collector-bin <path>]
trustvian eval run          --suite <dir> --scenario-timeout <duration>
                            [--fail-fast] [--reference last] [--collector-bin <path>]
trustvian eval operational  --reference-run <id> --candidate-run <id>
                            [--reference-run <id> --candidate-run <id> ...]
                            [--after <cursor>] [--limit <n>]
trustvian eval compare-repeated
                            --reference-run <id> [--reference-run <id> ...]
                            --candidate-run <id> [--candidate-run <id> ...]
                            --added-candidate-presence-minimum <k>
                            --added-reference-presence-maximum <j>
                            --max-repeated-added-behaviors <n>
                            --max-block-decisions-per-run <n>
                            --max-critical-risk-observations-per-run <n>
                            [--min-candidate-frequency <n>] [--max-lost-behaviors <n>]
                            [--max-calls-per-run <target>=<max> ...]
                            [--max-llm-calls-per-run <n>]

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
                           [--max-added-behavior-changes <n>]
trustvian promotion get    --id <id>
trustvian promotion list   --project-id <id>

trustvian status
```

### Pipeline status: `trustvian status`

`trustvian status` reads `GET /v1/status` once and prints the document as the
server sent it — with or without `--json`, because the document is the only
format there is
([task 105](tasks/v0.12/105-pipeline-status-surface.md)). It is the answer to
*why is nothing arriving?*: which Collectors are reporting to this control
plane, what each has received, which producers it has seen with their
instrumentation scopes and SDK, which model calls the telemetry named, how many
spans arrived only as HTTP, how each span's actor was bound, and what the
engine's `Observe` reported.

```bash
trustvian status | jq '.suggestions[].text'
```

The `suggestions` array holds the outputs of a fixed, versioned rule table
over the same document
([ADR 0064](adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md)):

| Rule | Fires when |
|---|---|
| `status.no_collector` | no Collector has reported in 30 s. When ingest records arrived within the 30 s anyway, its second sentence says the Collector feeding them has no `status:` block, with `last_ingest_age_seconds` in its evidence |
| `status.no_producer` | no reporting Collector has seen a span in 30 s, and at least one has been up for 30 s; names the first such Collector, its uptime and its receiver endpoint when one is reported. A Collector that started seconds ago has not been silent for 30 s, so it fires nothing |
| `status.spans_without_service_name` | spans reached a Collector with neither `service.name` nor `trustvian.actor.id`, and were never evaluated |
| `status.collapsed_http_operations` | 3 or more distinct operations reached one named HTTP target at transport fidelity. DB spans, unmapped convention spans and HTTP spans with no `server.address` or `service.peer.name` are never counted. Common HTTP client instrumentation names every span after its method alone, so this rule rarely fires for it — see [task 105](tasks/v0.12/105-pipeline-status-surface.md#what-shipped) |
| `status.admission_near_bound` | cannot fire: the engine reports no fingerprint admission count |

A suggestion changes nothing else: no exit code, no verdict, no stored record.
Branch on its `rule`, never on its `text`. `trustvian status` exits `0` whenever
the document was read, whatever it says, and `3` when the control plane could
not be reached.

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

**`--max-added-behavior-changes` bounds counted changes**, and is optional — on
`eval compare` and `promotion create` alike
([issue 131](https://github.com/trustvian/trustvian/issues/131)). Omitted, the
request carries no such field and the server reports the check as not evaluated:
the verdict is the other five checks alone, exactly as before the flag existed.
`0` is not the same thing — it is the strictest limit, and the check is evaluated
against it. Given alongside `--max-added-behaviors`, **both** are enforced;
neither replaces the other.

```text
Gate checks:
  PASS Added behaviors: 2 (maximum 5)
  ...
  PASS Added behavior changes: 1 (maximum 1)            evaluated, passed
  ---- Added behavior changes: not evaluated (no limit supplied)
  ---- Added behavior changes: not recorded (decided before this check existed)
  FAIL Added behavior changes: 4 (maximum 3; correlation partial, so this is the identity count)
```

A not-evaluated or not-recorded check prints neither PASS nor FAIL, because it
had neither. Where correlation is not complete the counted-change count *is* the
identity count (ADR 0052), so a limit chosen on the assumption of folding fails
rather than passes — the conservative direction. Every word is the server's: the
CLI compares no count against any limit.

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

### Latency, errors, tokens and cost per target: `eval operational`

`trustvian eval compare` carries three run-level sections on its scorecard —
`latency`, `errors` and `tokens` — and, when the control plane was started with
`--pricing`, a `cost` section. Each is comparable only when both runs carried
its evidence. Otherwise it says which run did not, and carries no `delta`.
`eval operational` breaks the same evidence down by target:

```text
trustvian eval operational --reference-run run-ref --candidate-run run-cand
```

It prints `GET /v1/evaluations/operational`'s body unchanged, in both output
modes. Repeat each flag once per run to read a repeated comparison's sides
(the same N each). Every section then says on how many runs that target carried
its evidence. Rows are ordered by target category, then name. A page holds at
most 64 rows, and `next_after` is the `--after` for the next one. Exit status is
`0`, `2` for usage and `3` for an API failure, like the rest of `eval`.

Latency is descriptive. Through a model-driven agent the model dominates it,
and nothing here attributes a change to the change under test.

### Any runs, repeated: `eval compare-repeated`

`trustvian eval run` reaches the repeated comparison through a scenario
execution. `eval compare-repeated` asks `POST /v1/evaluations/compare-repeated`
about any runs, for example two executions' candidate sides:

```text
trustvian eval compare-repeated \
  --reference-run scn-a-candidate-1 --reference-run scn-a-candidate-2 \
  --candidate-run scn-b-candidate-1 --candidate-run scn-b-candidate-2 \
  --added-candidate-presence-minimum 2 --added-reference-presence-maximum 0 \
  --max-repeated-added-behaviors 0 --max-block-decisions-per-run 0 \
  --max-critical-risk-observations-per-run 0
```

Repeat each run flag once per repetition, in order. The five limits are
required and none has a default, as in a scenario file. Task 106's three
limits are optional, with `--max-calls-per-run <target>=<max>` repeated once
per target. The command prints the response body unchanged in both output
modes, `sameness` and `warnings` included. Like `eval compare`, it reads only
`gate.verdict`, for its exit status: `0` PASS, `1` FAIL, `2` usage, `3` an API
failure or a verdict it does not recognize.

Sameness here comes from the executions that recorded the runs: each side
states a value only when every one of its runs was recorded with the same one
(see [Provenance](#provenance-what-each-side-ran)).

### Fidelity on a comparison

Since task 081 every behavior row of `eval compare` (`behavior_diff.deltas[]`)
and of the repeated comparison carries `reference_fidelity` and
`candidate_fidelity`, printed unchanged by `--json`:

```json
"candidate_fidelity": {"level": "transport", "mixed": true,
  "semantic": "2", "transport": "1", "unrecorded": "0",
  "layer": {"model": "0", "tool": "2", "retrieval": "0", "transport": "1",
            "unclassified": "0", "unrecorded": "0"}}
```

- **`level`** is the lowest fidelity any observation had: `transport` if one
  was transport, else `semantic` if one was semantic, else `unrecorded`.
- **`mixed`** says the observations disagreed. One transport observation in a
  thousand makes the behavior `transport, mixed`, because "named by telemetry"
  must hold for every observation it covers.
- **The counts** say how it split. A repeated comparison sums each side's runs
  first, then derives the level.
- **A missing side:** a side that never observed the behavior has neither
  field.
- **Before schema 13:** behaviors recorded then read `unrecorded`.

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
`added_behavior_changes` is refused with directions rather than resolved: it
counts changes, and its evidence is already in the comparison's
`added_changes`, each of whose contributing identities resolves with
`--behavior`.

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

### Checking the pipeline: `--check`

```text
trustvian dev --check [--api-url <url>] [--local-bin <path>] [--collector-bin <path>]
```

Starts the control plane and a Collector exactly as `dev` does — without a
workload, an evaluation run or a baseline — waits for the Collector's first
status report (at most 15 seconds), prints the status document `trustvian
status` prints, and stops everything it started. No `--` and no command.

Use it before the first real run, or when a run produced nothing: it shows
whether the helpers start and whether the Collector reaches the control plane.
With nothing else wrong, the answer is a reporting `dev-check` Collector, no
producers, and an **empty `suggestions` list**. The Collector has only just
started, so it has not been silent long enough for `status.no_producer` to
claim anything. Its receiver ports are chosen per run and close when the check
exits, so they are not an endpoint to export to. A real `trustvian dev` session
sets the producer's endpoint itself.

The Collector it starts is named `dev-check` and reports status only, so a check
against a shared control plane with `--api-url` never replaces a running `dev`
session's own `dev` entry, and nothing is written to the database. Exit status:
`0` when the `dev-check` Collector reported, whatever the document says; `2`
for a usage error; `3` when the control plane or the Collector could not be
started, or when the Collector did not report within the 15 seconds. In that
last case the document is still printed to stdout, showing what the control
plane saw, so a script can tell "the pipeline is not checked" from "checked".

Every `trustvian dev` Collector reports its status the same way, as `dev`,
naming both of its OTLP receivers — so `trustvian status`, or the WebUI's Status
view, can be read while a workload runs.

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
| `status`, `dev --check` | the document was read; for `--check`, and its Collector reported | *unused* | usage | API or network failure; for `--check`, a helper that could not start or a Collector that did not report within the wait (the document is still printed) |
| `eval run` | gate **PASS** | gate **FAIL** | usage — the scenario, the suite or `--reference`, before anything runs | API or network failure, a reference the control plane refused (before any workload), a repetition whose workload or run failed, or a scenario past its `--scenario-timeout`. A suite exits with its most severe scenario: 3, then 2, then 1, then 0 |
| `dev` | the command’s own | the command’s own | usage | could not start |

**Exit 1 means gate failure only for `eval compare` and `eval run`.** It does not change what
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
`--max-added-behavior-changes` is the one optional limit: omitting it means
that check is not evaluated, which is also not zero.

## Behavioral scenarios: `trustvian eval run`

One run of a model-driven agent is not evidence: task 078's measurement found an
unchanged agent's behavior set moving between isolated runs, so a single
comparison at zero failed half the unchanged pairs. A scenario runs each side
`runs: N` times and gates over integer presence counts instead.

```yaml
version: v1
name: support-login
runs: 5
instrumentation: existing          # optional; dev's own modes
reference:
  command: [python, agent.py]
  env: {AGENT_MODE: reference}
candidate:
  command: [python, agent.py]
  env: {AGENT_MODE: candidate}
gate:
  added_candidate_presence_minimum: 1        # k
  added_reference_presence_maximum: 0        # j
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
```

```bash
trustvian eval run --scenario scenarios/support-login.yaml --json > result.json
```

**Every field and `runs` is required, and nothing has a default.**
- `1 <= runs <= 64` and `0 <= j < k <= runs`.
- Unknown fields are refused, and every error names its field.
- `runs` and every gate value must be a YAML integer. `5.9`, `-0.5`, `1e1` and
  a value past 64 bits are refused rather than converted. This includes values
  that arrive through a `<<` merge or an alias.
- Any of these is exit `2` before a repetition starts.
- Three **optional** frequency limits go under `gate:` too (task 106). Each is
  omitted by default, and omitted means it is not evaluated — never defaulted:

  ```yaml
    min_candidate_frequency: 4        # every behavior in all N reference runs is in >= 4 candidate runs
    max_lost_behaviors: 0             # behaviors in every reference run and missing from a candidate run
    max_calls_per_run:                # per target name, the most calls any one candidate run makes
      - {target: crm.internal, max: 6}
    max_llm_calls_per_run: 40         # the most model-layer calls any one candidate run makes (task 081)
  ```

  `min_candidate_frequency` is at most `runs`. `max_calls_per_run` lists 1 to 16
  unique targets of at most 255 bytes. A target neither side ever called is
  reported `not_observed` and fails the check, so a typo cannot pass. A
  configured limit whose evidence is absent is `deferred` and fails the
  verdict, naming what was missing. Exit codes are unchanged, and a FAIL from
  one of these is a gate FAIL, exit `1`.
- `max_llm_calls_per_run` (task 081) counts the behaviors whose envelopes named
  a model call: GenAI `chat`, `text_completion`, `embeddings` and the like, or an
  OpenInference `LLM` or `EMBEDDING` span. It is **deferred unless every
  candidate run has at least one model-layer observation**. A run with none
  cannot be told apart from one whose model calls were invisible: a producer
  with no GenAI or OpenInference instrumentation, or one that names its tools
  but sends its model calls as plain HTTP, sees those calls as transport. So
  when the check evaluates, its value is at least 1; it never reports 0. The
  deferred check names the run that lacked the evidence (the lowest run
  identifier) and, when more than one did, how many. A model call that reaches
  the model server as a plain HTTP request is counted under that host by
  `max_calls_per_run`, not here.
- `k = 1, j = 0` is the documented guidance for a workload whose variance you
  have not measured. It is set semantics — "in at least one candidate run and no
  reference run" — at every N. It is guidance, not a default, and the
  measurement does not justify any other value as one. For a nondeterministic
  workload, [calibrate it first](#calibrating-n-k-and-j).

**What runs.**
- Every invocation is a recorded **scenario execution**, named by the
  `execution_id` in the result. It begins before anything runs and completes
  with the verdict, or is recorded failed when the scenario stops.
- `2N` repetitions, one at a time: the reference side, then the candidate side.
  With `--reference`, only the `N` candidate repetitions — see below.
- Each repetition is a full `trustvian dev` run against the one control plane
  `--api-url` resolves to. Each has a fresh run id and its own
  `--behavioral-profile`, so no repetition meets a baseline another one taught.
- The first repetition whose workload exits non-zero ends the scenario with exit
  **3**: the rest do not run and no verdict is produced. A broken workload is
  never a gate failure.
- So does a repetition whose run the control plane could not complete, even
  when its workload exited `0`.
- Repetition output goes to stderr: `dev`'s own lines and the workload's
  standard output. `--json` stdout is the result document alone.
- A bare command name, such as `python`, is looked up on the side's own
  `PATH` when its `env` sets one. A `PATH` pointing into a virtualenv therefore
  runs that virtualenv's `python`. As with the shell, the first match you may
  execute wins. The parent's `PATH` is never a fallback.

**What decides.** The control plane, completing the execution through the
same comparison `POST /v1/evaluations/compare-repeated` serves. The runner
counts nothing.
- It refuses repetitions that span environments or projects, or that share a
  behavioral profile. It also refuses evidence in which one behavior appears
  under two fingerprints, or one fingerprint names two behaviors.
- For each behavior the server reports in how many reference runs and how many
  candidate runs it appeared.
- It classifies a behavior *added* when `candidate_runs_present >= k` and
  `reference_runs_present <= j`, *removed* by the mirror rule (reported, never
  gated), or *neither*.
- It evaluates six checks: both sides completed `N` repetitions, no completed
  repetition was empty, repeatedly added behaviors against
  `max_repeated_added_behaviors`, and the worst candidate repetition's block
  decisions and critical-risk observations against the two `_per_run` limits.
  Any of the three frequency limits the scenario sets is evaluated too, and
  printed under the six.
- It also reports how often (task 106), all as integers:
  - per behavior and side: `calls_total`, `calls_per_run_min`/`max` and
    `calls_per_run_mean_milli` (thousandths), and whether the behavior was
    `lost`;
  - per target: the same, plus `call_ratio_permille`, which is absent when the
    reference never called the target;
  - up to 64 named `suggestions` from a fixed rule table, which change no check
    and no verdict.

```text
support-login   runs 5

Behavior                                  reference   candidate
  tool/crm_lookup                               5/5         5/5
  tool/export_customer                          0/5         5/5   + added

Gate (k = 1, j = 0)
  PASS reference repetitions completed: 5 (== 5)
  ...
  FAIL repeatedly added behaviors: 1 (<= 0)
  PASS worst candidate block decisions: 0 (<= 0)   advisory: fresh scope
```

- **`max_repeated_added_behaviors` counts behavioral identities**, the unit of
  `--max-added-behaviors`. A tool and the HTTP request it makes are two, so at
  any nonzero budget one act consumes two
  ([ADR 0053](adr/0053-repeated-evaluation-counts-identities-across-isolated-repetitions.md)).
- **Checks 5 and 6 are marked `advisory: fresh scope` when `runs > 1`.** Each
  repetition's learning scope is new, so neither a pass nor a fail there is a
  learned-policy verdict. The marker changes no verdict. Sequence evidence
  from a reorder cannot fail them; see [Ordering](#ordering-a-scenario-asserts-none).
- At `runs: 1, k: 1, j: 0` the verdict is exactly `eval compare`'s, check for
  check.

**`--json`** writes the result document: the scenario name and `runs`, the
execution id, both producer versions (`cli_version`, `control_plane_version`),
`provenance` as the control plane recorded it (task 086), and the server's
repeated result under `comparison`. With `--reference` it also carries
`reference`: the mode asked for and the execution that was reused.

### Provenance: what each side ran

"Did I change the model, the prompt, the scenario or the inputs?" is answered
on the comparison itself (task 086,
[ADR 0067](adr/0067-scenario-provenance-is-recorded-per-execution-side.md)).
Every execution records, per side, a scenario digest, an input digest and the
model and prompt reference the side declared. Every comparison then says
whether the two sides were the same.

Optional fields in the scenario file:

```yaml
inputs: [fixtures/tickets.json, fixtures/accounts.csv]   # relative to this file
reference:
  model: llama3.2                   # stated, or
candidate:
  model_env: OLLAMA_MODEL           # read from the side's environment
  prompt_ref: {name: support/system@v14, digest: "sha256:<64 hex>"}
  # or prompt_ref_env: SUPPORT_PROMPT_REF, holding <name>@sha256:<64 hex>
```

- **What moves `scenario_digest`:** any value of the definition — name,
  `runs`, scope, commands, env *keys*, input paths and **every gate limit**, so
  a changed threshold alone reads `same_scenario: "false"` with a
  `scenario_differs` warning. What does not: env *values*, input contents
  (those are `input_digest`), the model and the prompt reference. Task 081
  added `max_llm_calls_per_run` to the digested gate, `null` when omitted, so
  each scenario's digest changed once. An execution recorded by an earlier CLI
  then reads `same_scenario: "false"` against one recorded by this one.
- **`scenario_digest`** is `sha256:` and the hex SHA-256 of the validated
  scenario re-encoded as canonical JSON. The encoding has a fixed field order,
  `env` reduced to its sorted keys, `inputs` as sorted clean paths,
  `max_calls_per_run` sorted by target, and an omitted optional limit as
  `null`. Comments, formatting and key order change nothing; any value does.
  Environment *values* are never digested: they often hold secrets, and a
  digest of a short secret can be brute-forced. `model`, `model_env`,
  `prompt_ref` and `prompt_ref_env` are excluded too. They are compared on
  their own, so a run that changed only its model has the same scenario
  digest.
- **`input_digest`** is `sha256:` over the canonical JSON list of
  `{"path", "sha256"}`. The list is sorted by path. Each path is written with
  `/`, lexically clean and relative to the scenario file, and `sha256` is the
  hex digest of the file's exact bytes, so a changed line ending is a changed
  input. Each input must be a regular file of at most 64 MiB inside the
  repository: the nearest directory above the scenario holding `.git`, after
  symbolic links. A scenario may list at most 64 inputs. With no `inputs:`,
  nothing is declared, and the execution records `not_declared`, not the
  digest of an empty list. The runner does not pass inputs to the workload;
  `command` still decides what it reads.
- **`model` and `prompt_ref`** are declarations, never observations and never
  content. Each side states one in the file, or names a variable of the
  environment its workload runs with (the process environment plus the side's
  `env`). An unset or empty variable declares nothing. A model and a prompt
  name are 1 to 128 characters with no whitespace, so neither can hold prompt
  text; a prompt digest is `sha256:` and 64 lowercase hex. Refusing is not
  proof that a value is not text: the format rule guards against pasting a
  prompt by accident, and is not a privacy guarantee. Put only a name and a
  digest there. A value that does
  not fit is exit `2` before anything runs. With `--reference`, the reused
  reference side keeps the declarations of the execution that ran it.

The CLI computes both digests and sends them with the declarations when the
execution begins. The control plane checks their format and stores them; it
cannot recompute them, so a digest is exactly as trustworthy as the client
that sent it. `eval run` prints the recorded digests on stderr as the execution
begins. The human result prints what was recorded and the `sameness` block:

```text
Recorded
  reference: scenario sha256:f9a2…0e8d   inputs not_declared   model llama3.2   prompt_ref not_recorded
  candidate: scenario sha256:f9a2…0e8d   inputs not_declared   model gemma3:4b   prompt_ref not_recorded

Sameness
  same_scenario    true
  same_inputs      true
  same_model       false
  same_prompt_ref  not_recorded
  …
  warning model_differs: The reference and candidate sides declared different models.
```

Each answer is `true`, `false` or `not_recorded`. A value missing on either
side is `not_recorded`, never `false`. That covers an execution from before
schema 12, a client that sent no provenance, and a side that declared no
model. Each `false` adds a warning (`scenario_differs`, `inputs_differ`,
`model_differs`, `prompt_ref_differs`). Warnings state a fact and give no
advice: a changed model is often the point of the comparison. **No check reads
sameness or warnings, and neither changes the verdict.**

**Upgrade the control plane before the CLI.** A control plane older than task 086 refuses the `provenance` field that `trustvian eval run` sends when it begins a scenario execution, and the run fails with `400` before any workload starts. `trustvian dev` itself sends no provenance and is unaffected.

### Calibrating `N`, `k` and `j`

**An unchanged deterministic workload passes at `k = 1, j = 0`.** Its behavior
set is the same in every run, so nothing is ever present on the candidate side
and absent from the reference side. CI asserts this at the control plane
(`TestAnUnchangedDeterministicWorkloadPassesAtTheDocumentedLimits`, `N = 5`) and
through the real binaries (`TestEvalRunAgainstRealBinaries`).

**An unchanged nondeterministic workload can fail at `k = 1, j = 0`, and no
default `k` ships.** Task 078 measured a model-driven agent
([2026-10-01, tool-name fidelity](https://github.com/trustvian/trustvian-python-agent-demo/blob/a19d7f4/docs/results/2026-10-01-stability-tool-fidelity.md),
summarized in [the task](tasks/v1.0/078-behavioral-scenario-suites.md#the-re-run-at-tool-name-fidelity-2026-10-01)):
- At `N = 5, j = 0`, the `k = 1` gate failed 6 of 252 unchanged
  self-comparison splits at T = 0.7, and 1 of 252 at T = 1.3.
- At T = 1.3, no `k` removed every crossing.

That is one agent, one model and one toolset. It justifies no default, so you
calibrate from your own workload's self-comparison false-FAIL rate, with the
commands that already exist:

1. **Write a self-comparison scenario** at the `N` you intend to run: `candidate`
   identical to `reference` (same command, same `env`), and the gate you are
   considering. Start from `k = 1, j = 0` with every maximum at `0`.
2. **Run it `M` times**, saving each result:
   `trustvian eval run --scenario self.yaml --json > self-$i.json`. Both sides
   are the unchanged workload, so every FAIL is a false FAIL.
3. **Count the FAILs.** Exit `1` and `"verdict": "fail"` both mark one.
4. **Choose limits where that count is 0 of `M`.** If it is not 0, raise `k`,
   raise `N`, or budget the variance with `max_repeated_added_behaviors`, and
   measure again. Then confirm that a candidate with a known added behavior
   still FAILs at the limits you chose.
5. **Record `N`, `k`, `j`, `M` and the count beside the scenario.** Measure
   again when the model, its temperature, the prompt or the toolset changes.

**Cost: `M × 2N` workload runs.** At the measurement's 2.7 minutes per
repetition, `M = 10` at `N = 5` is 100 repetitions, about four and a half hours.

**What it shows.** 0 FAILs in `M` runs describes those `M` runs. It does not make
the rate zero, and a larger `M` is stronger evidence. Some workloads have no
limits that reach 0 at a given `N`, as the T = 1.3 measurement did not.

The companion repository's
[`make stability`](https://github.com/trustvian/trustvian-python-agent-demo/blob/a19d7f4/Makefile)
(`tools/stability.py`) is a reference implementation of this measurement. It
runs an unchanged agent under isolated profiles, counts how often it fails
against itself, and is what produced the numbers above.

### Ordering: a scenario asserts none

A scenario file has no step list and no expected sequence. An `order:`,
`sequence:` or `steps:` key is refused as an unknown field. So the runner has no
ordering rule to pass or fail. A reorder reaches the verdict only as evidence
the engine itself recorded: a block decision or a critical-risk observation,
read by checks 5 and 6.

**Under `trustvian eval run`, a reorder cannot produce gated evidence end to
end, and checks 5 and 6 are advisory there.**
- Every repetition runs under `trustvian dev`, whose generated Collector
  configuration sets no anomaly block. So every sequence weight is at its
  default of 0: `transition_weight`, `transition_rarity_weight`, the n-gram
  weights and the Markov weight. The signals are reported, and they add nothing
  to the anomaly score.
- Each repetition also starts from a fresh learning scope, with no learned
  order to deviate from. Anomaly confidence stays at its floor (0.1786
  measured), so no anomaly yields a block.

This covers what order alone tells the engine. Signals that read a behavior's
own history are unaffected: latency, errors and novelty.

Neither is suppression. Nothing in the scenario path suppresses the engine's
sequence evidence. Where the sequence signals are opted in against a learned
profile, a reorder the engine blocks fails the same checks, and the same
reorder at weight 0 passes. Two tests prove it:
- `TestAnalyzeReorderedSequenceIsGatedOnlyWithTransitionWeight` covers the
  engine.
- `TestAReorderFailsTheRepeatedGateOnlyThroughEngineEvidence` goes through the
  real control plane.

### Reusing a recorded reference: `--reference`

```bash
# Record a reference once (both sides run).
trustvian eval run --scenario scenarios/support-login.yaml

# Later: run only the candidate side against it.
trustvian eval run --scenario scenarios/support-login.yaml --reference last
trustvian eval run --scenario scenarios/support-login.yaml \
  --reference scn-support-login-20261001T120000-1a2b3c4d
```

- **Only the `N` candidate repetitions run.** The reference side is the recorded
  execution's `N` reference runs, all of them, from that one execution. It is
  never mixed with another execution's runs, never truncated and never padded.
  The recorded execution's *candidate* runs are never used.
- **`last`** is the most recently completed execution of the same scenario
  `name`, for the same project, agent and environment this invocation derives.
  "Most recent" is the order completions were recorded, not a clock. If that
  execution cannot be used, the command says so by name and stops. It never
  falls back to an older one.
- **An explicit id** may name an execution of another scenario name or agent,
  because you chose it. It must still be in the same project and environment.
- **A usable reference is completed**, which includes a gate FAIL: completed
  means "evaluated", not "passed". Its `runs` must equal this scenario's, and
  its recorded runs must still exist, be completed, carry their recorded
  profiles and have complete evidence.
- **Anything else is exit `3` before any workload runs:** a reference that is
  missing, still running, failed, of another `N` or incomplete. A recorded run
  with zero records is not an error here. It fails the minimum-evidence check,
  as it would in a self-contained run.
- **This scenario's gate limits apply**, whatever limits the recorded execution
  was evaluated under. Recorded executions keep no limits to inherit.
- **The scenario file is validated whole.** `reference.command` is still
  required, although `--reference` does not run it.

The control plane resolves and validates the reference; the runner only names
it ([ADR 0054](adr/0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md)).

### A suite of scenarios: `--suite`

```bash
trustvian eval run --suite scenarios/ --scenario-timeout 10m --json > suite.json
trustvian eval run --suite scenarios/ --scenario-timeout 10m --reference last --fail-fast
```

- **Members.** Every `.yaml` and `.yml` file directly inside the directory, in
  byte order of their names.
  - No subdirectories.
  - A symbolic link is refused.
  - At most 64 scenarios, and at most 4096 directory entries examined.
  - `--suite` and `--scenario` cannot be combined.
- **Everything is checked first.** Every file is validated, scenario names must
  be distinct, and every scope is derived, before any workload or request.
  - A problem with the invocation or its files is exit `2`.
  - An environment that cannot be read (the working directory, repository
    inspection, locating the control plane) is exit `3`.
- **Each member runs exactly as `--scenario` would.** It keeps its own `runs`,
  its own limits, its own recorded execution and the control plane's verdict.
  The suite pools nothing and recomputes nothing.
- **A failure does not stop the others** unless you pass `--fail-fast`, which
  stops after the first scenario that is not a PASS. Members that did not run
  are reported `skipped` with a reason (`fail_fast` or `cancelled`), never as
  passes.
- **`--scenario-timeout` is required: 1s to 24h, per scenario.** It covers
  begin, every repetition (including `dev`'s startup and teardown) and
  completion. When it passes:
  1. no further repetition of that scenario starts;
  2. its workload's process group gets SIGTERM, then SIGKILL after 5s, and any
     leftover group members are killed;
  3. the execution is failed;
  4. the scenario is an operational error, `scenario_timeout`, even if the
     workload trapped the signal and exited `0`.

  The suite continues.
- **If the deadline passes while the execution is being completed** — or a
  gateway answers 502, 503 or 504, or anything other than the control plane's
  own `/v1` answer comes back — the control plane decides which happened
  first. Either the execution is failed
  (`scenario_timeout` or `cancelled`), or it had already completed. The
  second case is reported as `completed_without_response`, exit `3`: the
  execution is complete and reusable as a reference, but no verdict was
  received for it.
- **`--suite` is not supported on Windows** (exit `2`), because deadlines are
  enforced by stopping process groups. Use WSL2. `--scenario` is unaffected.
- **`--reference last`** is resolved per scenario: the most recent completed
  execution of *that* scenario's name, project, agent and environment, with
  its N. A missing or unusable one is that scenario's error, before it runs
  anything. An explicit execution id is refused with `--suite`; use it with
  `--scenario`.
- **Ctrl-C (or SIGTERM)** stops the running scenario: its workload's process
  group gets one SIGTERM from the suite. The execution is failed, and the rest
  are marked `skipped: cancelled`, also with `--fail-fast`. The suite exits
  `3`.
  - The suite is the only receiver of these signals.
  - Its workloads run without the terminal (stdin is `/dev/null`), so
    Ctrl-C never reaches a workload twice.
- **Exit code:** the most severe scenario's, `3` over `2` over `1` over `0`.

**`--json`** writes one suite document:

- `version`, `complete`;
- `suite` (`directory`, `scenario_count`);
- `options` (`scenario_timeout`, `fail_fast`, `reference`);
- `members[]`, each with:
  - `file`, `scenario` (`name`, `runs`), `outcome` (`pass`, `fail`, `error`,
    `skipped`), `exit_code`, `execution_id`;
  - `result` (the single-scenario document, unchanged) for a PASS or FAIL;
  - `error` (`code`, `message` of at most 1024 bytes of valid UTF-8);
  - `skipped_reason`;
- `summary`, `exit_code`, `producers.cli_version`.

The document is capped at **32 MiB**. Over that, the suite exits `3` and writes
a document with `complete: false` and an `output_too_large` error, listing each
member's file and scenario but no outcomes or results.
([ADR 0055](adr/0055-a-scenario-suite-is-a-bounded-schedule-and-a-report-not-an-evaluation.md))

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
- [GitHub Actions](ci-github-action.md) — running `eval run` on every pull
  request
- [Task 077](tasks/v1.0/077-unified-otlp-local-dev-runtime.md) — the `dev`
  specification
