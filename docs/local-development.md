# Local development

One command runs your agent under Trustvian. Everything it needs is composed
around it, and your repository is never written to.

## Quick start

In your agent's git repository, with the release archive's binaries on your
`PATH` ([Getting Started](getting-started.md#developer-preview)):

```bash
trustvian dev -- python agent.py
```

That is the whole loop. `trustvian dev` starts a local control plane and an OTLP
receiver, provisions the project / agent / candidate / run hierarchy from your
git repository, points your workload's OpenTelemetry exporter at the receiver,
runs your command unchanged, and prints a URL to watch it. With the repository's
test producer standing in for `python agent.py`:

```text
Trustvian dev

  Project     my-agent
  Agent       support-agent   (declared by the workload)
  Candidate   git:188ac38
  Environment local
  Run         dev-git-188ac38-20261013T212105.524Z

  State    ~/.trustvian/dev/382774ec0ca4
  Baseline ~/.trustvian/dev/382774ec0ca4/baseline-git_3a188ac38.json
  API      http://127.0.0.1:63349
  OTLP     http://127.0.0.1:63351  (gRPC 127.0.0.1:63352)
  Web      http://127.0.0.1:63349/
  Owner    existing   (auto: $OTEL_SERVICE_NAME is set)
  Set      OTEL_BSP_SCHEDULE_DELAY OTEL_EXPORTER_OTLP_ENDPOINT OTEL_EXPORTER_OTLP_HEADERS OTEL_EXPORTER_OTLP_PROTOCOL OTEL_EXPORTER_OTLP_TRACES_ENDPOINT OTEL_EXPORTER_OTLP_TRACES_HEADERS OTEL_EXPORTER_OTLP_TRACES_PROTOCOL OTEL_LOGS_EXPORTER OTEL_METRICS_EXPORTER OTEL_RESOURCE_ATTRIBUTES OTEL_SEMCONV_STABILITY_OPT_IN OTEL_TRACES_EXPORTER TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT

Running agent.sh
…
Run dev-git-188ac38-20261013T212105.524Z complete.
```

A worktree with uncommitted changes reads `git:<sha>+dirty`. The control plane
this starts stops when your command exits; to keep watching afterwards, start
`trustvian-local` yourself and pass its `API:` URL with `--api-url`, as
[Getting Started](getting-started.md#3-run-your-agent-under-trustvian) does.

From a Trustvian checkout, `make dev ARGS='-- <command>'` builds all three
binaries into `bin/` and runs `dev` in the checkout's own directory, so it suits
a workload that lives there.

Your command's exit status is `dev`'s own, so the wrapper is transparent to
scripts. `Ctrl-C` goes to your workload; the run is completed either way.

The full command surface — every flag, the identity derivations, the exit codes —
is in [Platform CLI § `trustvian dev`](platform-cli.md#trustvian-dev).

### Your workload must already emit OpenTelemetry

`dev` configures an exporter; it does not attach an SDK. A workload with no
instrumentation stops with a message rather than being injected into, because a
workload that initializes the SDK a moment after it starts cannot be detected
beforehand, and attaching a second stack would report every action twice. See
[ADR 0044](adr/0044-instrumentation-ownership-requires-positive-evidence.md).

### What `dev` needs on disk

`dev` supervises two helper executables it does not contain:

```text
trustvian-local      the local control plane
trustvian-collector  the OTLP receiver and the Trustvian processor
```

They are separate binaries rather than linked-in packages because the root CLI
must not import `trustvian-platform` (ADR 0022, 0033, 0035).

**Two ways to have them**, and `dev` needs no configuration for either:

- **A release archive.** The macOS and Linux archives ship all three binaries
  side by side, and dev looks alongside its own executable, so `trustvian dev`
  works from a download with no checkout. Keep the three together.
- **A checkout.** `make dev` builds both and points `dev` at them.

`go install` gives you only `trustvian`: it builds the root module's command,
and the helpers live in repository-internal modules. `dev` says so and names
the ways forward rather than downloading anything — it makes no network call at
all. Each can also be given explicitly (`--local-bin`, `--collector-bin`, or
`$TRUSTVIAN_LOCAL_BIN` / `$TRUSTVIAN_COLLECTOR_BIN`).

Not on Windows, where `dev` refuses to start at all; see
[Running the parts separately](#running-the-parts-separately).

### Where `dev` keeps its state

Outside your repository, under `~/.trustvian/dev/<hash of this directory>/`,
printed on every start. It holds the generated Collector configuration, both
helper logs, and the learned baseline. `rm -rf` that directory to start over.

One consequence worth knowing: two runs of one commit share a learned baseline,
which is the point — the second run's anomaly *confidence* is non-zero, so the
engine-evidence gates can actually fire. It also means two concurrent `dev` runs
of the same candidate are refused, because the baseline file has one writer.

## Running the parts separately

`trustvian dev` is a convenience over two processes you can always run yourself.
Do that when you want a control plane that outlives several runs, when you are on
Windows (where `dev` refuses — see
[compatibility](compatibility.md)), or when you are debugging the composition
itself.

```bash
make dev-binaries          # builds bin/trustvian-local and bin/trustvian-collector
```

**Terminal A — the control plane.** Publishes its endpoint into `.trustvian/`,
so clients in that directory need no `--api-url`:

```bash
./bin/trustvian-local
```

**Create the hierarchy and start a run.** `dev` does this for you; by hand it is
the six commands below. A run may only name an environment its project owns, so
the environment comes first. The behavioral profile is the learning scope:

```bash
./bin/trustvian project create --id proj-1 --name Checkout
./bin/trustvian env create --project-id proj-1 --ref local --name Local
./bin/trustvian agent create --id agent-1 --project-id proj-1 --name "Checkout agent"
./bin/trustvian candidate create --id cand-1 --agent-id agent-1
./bin/trustvian eval create --id run-1 --candidate-id cand-1 \
    --environment local --behavioral-profile checkout
./bin/trustvian eval start --id run-1
```

**Terminal B — the receiver.** `trustvian-collector` needs a configuration
whose `evaluation:` block names the running run. `processor/config.yaml` has
none: it only enriches spans. Add the block, with the `API:` URL from terminal
A; [`processor/README.md`](../processor/README.md#configuration) documents the
keys:

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:4317
      http:
        endpoint: 127.0.0.1:4318

processors:
  trustvian:
    evaluation:
      api_url: http://127.0.0.1:63309      # terminal A's API: URL
      run_id: run-1
      behavioral_profile: checkout
      required: true
      pending_state_path: .trustvian/run-1.pending.json

exporters:
  debug:
    verbosity: basic

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [trustvian]
      exporters: [debug]
```

```bash
./bin/trustvian-collector --config=collector.yaml
```

The flag takes `=`. `--config collector.yaml`, with a space, is not read, and the
Collector falls back to `./config.yaml`.

**Terminal C — your workload**, pointed at the receiver:

```bash
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces \
OTEL_SERVICE_NAME=checkout-agent \
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local \
  python agent.py
```

All three variables matter. Without `deployment.environment.name` the engine
cannot fill the record's environment, and `platform/behavior.go` refuses a record
whose environment differs from the run's — producing a run with no usable
evidence rather than an error naming the cause.

**Complete the run** when the workload is done:

```bash
./bin/trustvian eval complete --id run-1
```

## Starting the platform without a workload

```bash
make local
```

Then, in another terminal **in the same directory**:

```bash
./bin/trustvian project create --id proj-1 --name Checkout
./bin/trustvian env create --project-id proj-1 --ref local --name Local
./bin/trustvian agent create --id agent-1 --project-id proj-1 --name "Checkout agent"
./bin/trustvian candidate create --id cand-1 --agent-id agent-1
./bin/trustvian eval create --id run-1 --candidate-id cand-1 \
    --environment local --behavioral-profile checkout
./bin/trustvian eval start --id run-1
./bin/trustvian tui --run-id run-1
```

No `--api-url` anywhere. `make local` publishes the endpoint it bound, and
every client in that directory reads it.

Stop the runtime with `Ctrl-C`.

## What `make local` starts

```text
Application / Agent
      │  Engine.Analyze
      ▼
DecisionRecord
      │  HTTP /v1
      ▼
┌──────────────────────────────────────┐
│ local runtime                        │
│   SQLite ← ControlPlane              │
│              ├─ Realtime Bus ── SSE ─┼──▶ TUI · WebUI
│              └─ HTTP API ────────────┼──▶ CLI · WebUI
└──────────────────────────────────────┘
```

The engine stays outside it. Your application analyzes events, serializes a
`DecisionRecord`, and posts it — the server never runs a second engine.

## Watching an agent behave

The shortest useful loop, with nothing typed into the browser:

```bash
trustvian dev -- python agent.py    # prints the URL
```

Or, with the parts running separately, `trustvian-local` in terminal A, the
Collector in terminal B and your instrumented agent in terminal C.

Then open the `Web:` URL. The page lands on the **Live Observatory**, already
connected, and each active agent appears as a card the moment telemetry for it
arrives. The newest is selected and its behavior flow animates — one pulse per
observation, along the edge that observation describes. Click a behavior and
the inspector shows what Trustvian decided about it: decision, risk, trust,
anomaly and confidence, exactly as the server reported them.

```text
                 POST
support-agent ───────────▶ ollama.localhost
             ├─ GET  ────▶ crm.localhost
             ├─ GET  ────▶ knowledge.localhost
             ├─ POST ────▶ mail.localhost
             └─ POST ────▶ export.localhost   NEW
```

You do not need a Project, Agent, Candidate or EvaluationRun identifier to see
this, and there is no form to fill in first. **They still have to exist** — the
Collector's `evaluation:` block names a run that must already be running, and
nothing here creates a durable entity because telemetry arrived. What changed is
that `trustvian dev` creates them from your repository, and a person no longer
retypes those identifiers into a browser to see the result.

A behavior the run had not shown before is marked `NEW` on the edge, on its
target, in the timeline, and in the inspector. It is never labelled dangerous
or unsafe — `NEW` means new, and any stronger reading would be a judgement the
platform did not make.

If nothing is running, **Projects** lists what exists and **Runs** takes you
from there to a run's workspace — one bounded page at a time, when you ask.
Clicking a run opens its observations and behaviors, and selecting an
observation opens its detail beside the table, with its session, trace and
behavior as controls you can follow. The control-plane forms live under
**Manage** and are not needed to watch or investigate anything. See
[the web interface guide](webui.md).

The reference end-to-end workflow is the companion repository
`trustvian/trustvian-python-agent-demo`: a Python agent driven by Ollama,
instrumented with runtime OpenTelemetry, feeding the Collector and the
evaluation ingest path. This repository deliberately keeps no second Python
demo — two demos of the same thing drift.

## State

```text
.trustvian/platform.db     evaluation state, durable across restarts
.trustvian/runtime.json    the endpoint the running server bound
```

Project-local, so two checkouts are two independent environments and
`rm -rf .trustvian` ends one completely. `.trustvian/` is gitignored.

`trustvian dev` writes neither of these into your repository. Its state lives
under `~/.trustvian/dev/<hash of the workload directory>/`, holding the same
database plus the generated Collector configuration, both helper logs, and the
learned baseline and its lock. A `workload-path` file records which directory the
hash came from, so a directory you find later is identifiable.

Clients in a directory `dev` has run in still need no `--api-url`: discovery
looks in `./.trustvian/` first and then in that directory's `dev` state.

The database survives restarts; the realtime stream does not, by design —
reconnecting resynchronizes from durable state rather than replaying history.

## The endpoint is not fixed

The runtime binds `127.0.0.1:0` and lets the OS choose a free port, so nothing
collides with whatever else you are running. That port changes every restart,
which is why discovery exists: clients read the current endpoint instead of
remembering one.

If you want to see it:

```bash
cat .trustvian/runtime.json
# {"version":"1","api_url":"http://127.0.0.1:54321"}
```

The browser UI is the same endpoint — `make local` prints it as `Web:` — so the
one field locates both. See [Web control plane](webui.md).

## A shared database, when you need one

`make local` uses SQLite and needs nothing else. That is the default and it is
not changing.

A deployment that wants several processes against one set of state can select
PostgreSQL instead:

```bash
TRUSTVIAN_PLATFORM_POSTGRES_DSN='postgres://user:password@host:5432/trustvian' \
  ./bin/trustvian-local --backend postgres
```

The DSN is read from the environment rather than a flag because a command line
is visible to every process through `ps`. It is never printed, never logged and
never written into `runtime.json`.

Selecting an unknown backend, or `postgres` without a DSN, fails before the
listener binds — nothing falls back to SQLite from a backend you asked for. The
CLI, the TUI and the WebUI behave identically either way; they cannot tell which
database answered.

Two caveats worth knowing. Two processes sharing one PostgreSQL database share
authoritative state but **not** realtime notifications: each keeps its own
in-process bus, so a dashboard connected to one does not see events published by
the other. And this is still unauthenticated and loopback-only — a shared
database does not make the listener safe to expose.

## Reaching a different control plane

An explicit endpoint always wins over discovery:

```bash
./bin/trustvian eval compare --api-url https://control.example \
    --reference-run baseline --candidate-run candidate \
    --max-added-behaviors 0 --max-block-decisions 0 \
    --max-critical-risk-observations 0
```

That matters for CI: a checked-out working directory can never redirect a
command that named its own endpoint.

`--api-url ""` counts as naming one — badly. It exits **2** without reading any
discovery file, because an empty value almost always means an unset variable:

```bash
# $CONTROL_PLANE_URL is unset → exit 2, not a gate result from the local runtime
./bin/trustvian eval compare --api-url "$CONTROL_PLANE_URL" …
```

## When there is no runtime

```text
$ ./bin/trustvian eval get --id run-1
trustvian: no local runtime was found.
  .trustvian/runtime.json: no local Trustvian runtime found; start one with `make local` or pass --api-url
  ~/.trustvian/dev/bafb746f7eb5/runtime.json: no local Trustvian runtime found; start one with `make local` or pass --api-url
Start one with 'make local' or 'trustvian dev', or pass --api-url
$ echo $?
3
```

Exit **3**, not 2 — the command was valid and the environment was not. Exit `1`
still means a failed gate, and only for `eval compare`, `eval compare-repeated`
and `eval run`.

## Security

The local runtime is **unauthenticated**. It binds loopback only, refuses any
other address, and there is no flag to change that — loopback is the entire
security boundary.

Discovery is not authentication: `runtime.json` carries no token and no secret,
and a discovered URL is accepted only if it is `http` on a numeric loopback
address. It must be exactly one JSON document under 4 KiB. A repository that
ships its own `.trustvian/runtime.json` cannot redirect your commands anywhere
else — and cannot make `make local` connect anywhere else either: the startup
liveness probe validates the address before it dials it.

**Do not expose this runtime, or put a reverse proxy in front of it.** Task 070
owns authentication and remote access.

## Options

```text
trustvian-local [--state-dir <dir>] [--listen <loopback-address>]
                [--backend sqlite|postgres] [--pricing <file>]
```

`--state-dir` defaults to `.trustvian`; `--listen` defaults to `127.0.0.1:0`
and accepts only numeric loopback (`127.0.0.1:8080`, `[::1]:0`).

`--pricing` takes a versioned pricing table, and every comparison then carries
a `cost` section with the table's `pricing_version`, `source` and
`pricing_digest`. Trustvian ships no prices. Without the flag there is no cost
section and nothing else in any response changes:

```yaml
version: 1
pricing_version: "team-2026-10"
source: "copied from provider pricing page, 2026-10-01"
currency: USD
models:
  - model: llama3.2            # a model call's operation name
    input_micros_per_million_tokens: 100000
    output_micros_per_million_tokens: 400000
```

Prices are integer micro-units of the currency per million tokens. A table that
does not validate stops the runtime before it starts (exit 2).

`trustvian-local` is built from a repository-internal module, so `go install`
does not produce it. It does ship in the macOS and Linux release archives
beside `trustvian`, because `trustvian dev` supervises it.

## Related

- [Platform CLI](platform-cli.md) — commands, flags, exit codes
- [Web control plane](webui.md) — the browser interface on the same listener
- [Terminal dashboard](tui.md) — watching a run live
- [ADR 0035](adr/0035-local-runtime-composes-platform-without-reversing-modules.md)
  — why the runtime lives in the platform module
- [ADR 0042](adr/0042-dev-composes-the-collector-rather-than-owning-a-receiver.md)
  — why `dev` supervises `trustvian-collector` instead of opening its own receiver
- [ADR 0043](adr/0043-dev-provisions-the-local-hierarchy-from-the-repository.md)
  — where `dev`'s identifiers come from, and why its state is outside your repository
- [ADR 0044](adr/0044-instrumentation-ownership-requires-positive-evidence.md)
  — why `dev` refuses rather than injecting instrumentation
