# Getting Started

**First time?** The [QuickStart](quickstart.md) runs an agent we provide, with
only the release archive and Python, in about ten minutes. This page is for
running **your own** agent.

Two paths:
- [**Developer preview**](#developer-preview) runs an agent you already have
  under Trustvian, from the release archive alone, with no Go toolchain and no
  clone.
- [**The engine**](#requirements) covers the CLI and the Go SDK over events you
  construct.

## Developer preview

From `v0.10.0`; the commands below are from `v0.12.1`. Run your agent under
Trustvian, watch its behavior in a browser, gate a change to it over repeated
runs, then put that gate on your pull requests. The
[learning path](learning-path.md) explains each step level by level.

**You need:**
- Linux or macOS, on amd64 or arm64. `trustvian dev` and `eval run` (with
  `--scenario` or `--suite`) refuse to run on Windows; use WSL2 there.
- `git`. The agent's directory must be a git repository with at least one
  commit. Trustvian names the project after the repository and the candidate
  after the commit.
- An agent that already sends OpenTelemetry traces over OTLP. Trustvian sets
  the standard `OTEL_EXPORTER_OTLP_*` variables and does not add an SDK — see
  [instrumentation ownership](platform-cli.md#instrumentation-ownership). It
  must also declare `OTEL_SERVICE_NAME`: that is the agent Trustvian reports on.

The outputs below come from the [QuickStart agent](../examples/quickstart/)
standing in for yours, in a repository named `my-agent`, as the agent
`support-agent`.

### 1. Install from the archive

Install the three binaries exactly as
[QuickStart § 1](quickstart.md#1-install-from-the-archive) does, checking the
archive against `checksums.txt`, and put their directory on your `PATH`. They
must stay side by side: `trustvian` finds `trustvian-local` and
`trustvian-collector` next to itself.

### 2. Start the control plane

In your agent's repository, in a terminal of its own:

```bash
cd my-agent
echo '.trustvian/' >> .gitignore
git add .gitignore && git commit -m 'Ignore Trustvian local state'
trustvian-local
```

```text
[main 93d3ae8] Ignore Trustvian local state
 1 file changed, 1 insertion(+)
Trustvian local runtime
API:   http://127.0.0.1:58861
Web:   http://127.0.0.1:58861/
Store: sqlite
State: .trustvian/platform.db
Local clients in this directory can now omit --api-url.
Press Ctrl-C to stop.
```

It keeps its state in `.trustvian/` and binds loopback only. Ignoring that
directory matters: an untracked file there makes the worktree dirty, so the
candidate would read `git:<sha>+dirty`. Ignore your virtual environment and
build output the same way. Leave it running and open the `Web:` URL in a
browser: with nothing arriving it opens on **Status**, which says why.

### 3. Run your agent under Trustvian

In a second terminal, in the same directory, with the `API:` URL from step 2:

```bash
OTEL_SERVICE_NAME=support-agent \
  trustvian dev --api-url http://127.0.0.1:58861 -- .venv/bin/python agent.py
```

```text
Trustvian dev

  Project     my-agent
  Agent       support-agent   (declared by the workload)
  Candidate   git:93d3ae8
  Environment local
  Run         dev-git-93d3ae8-20261015T235209.355Z

  State    ~/.trustvian/dev/faecabcba6f0
…
  Attached to a control plane this command did not start.
  Owner    existing   (auto: $OTEL_SERVICE_NAME is set)
…
Running python (+1 arguments)

agent: reference run complete, 6 spans delivered

Run dev-git-93d3ae8-20261015T235209.355Z complete.
```

Replace `.venv/bin/python agent.py` with the command that starts your agent.
`dev` prints the project, agent, candidate and run it provisioned. It runs your
command unchanged with an OTLP receiver around it, and exits with your
command's status. The browser's **Live** view shows the run as soon as
telemetry arrives, with no identifier to type. Select it to see the agent, tool
and service flow with the decision, risk, trust and anomaly the server
computed. Several actors inside one run draw as one agent there; following one
actor and pausing the view are planned
([task 109](ROADMAP.md#v0130--proposed)). After the run, **Status** shows the
spans it received, its producers and the model calls the telemetry named. If
nothing arrives, Status's suggestions say what to check, and
`trustvian dev --check` proves the pipeline with no workload at all.

`dev` does not use step 2's discovery file. Without `--api-url` it starts a
control plane of its own that stops when your command exits, which is too soon
to watch. Every flag is in
[Platform CLI § `trustvian dev`](platform-cli.md#trustvian-dev).

Use one identity throughout: the defaults, with no `--project` or `--candidate`.
An agent belongs to the project that first registered it. A scenario in step 4
runs under the repository's name, so after a `dev --project other` run for the
same agent it stops with `run is outside the scenario execution's scope`
(exit `3`). A candidate likewise belongs to the agent that first registered it,
so two agents in one repository need distinct candidates (`--candidate`, or
`candidate:` on a scenario side). `dev` still runs against a candidate another
agent registered, but prints one `warning:` line to stderr naming the owning
agent, this agent, and that a later scenario for this agent exits `3`.

### 4. Gate a change with a scenario

A scenario runs each side `runs` times and compares which behaviors appear.
Start from the QuickStart's
[`scenario.yaml`](../examples/quickstart/scenario.yaml) and change, for your
agent:

- each side's `command`, to the command that starts your agent;
- each side's `env`: your `OTEL_SERVICE_NAME`, and whatever selects the
  reference and the candidate behavior. The two sides may also differ by
  `candidate:`, a candidate identifier of their own;
- `model_env` (or `model:`), to declare the model each side ran with, so
  sameness can say whether it changed;
- `name` and `runs`. The five gate limits are required, and none has a
  default.

Commit the scenario, then run it:

```bash
trustvian eval run --scenario scenarios/my-change.yaml
echo $?    # 0 PASS, 1 gate FAIL, 2 usage, 3 operational
```

`eval run` finds step 2's control plane on its own. It prints each repetition's
`dev` output to stderr. On stdout it prints what each side recorded, the
behaviors with their presence counts, the gate's six checks, and whether both
sides ran the same scenario, inputs, model and prompt reference.
[QuickStart § 6](quickstart.md#6-gate-a-change) shows that output for the
QuickStart agent, and [§ 7](quickstart.md#7-read-the-report) its report.
`--json` writes the result document instead. The format is in
[Platform CLI § Behavioral scenarios](platform-cli.md#behavioral-scenarios-trustvian-eval-run).

**More than one scenario:**

```bash
trustvian eval run --suite scenarios --scenario-timeout 10m
```

This runs every `.yaml` and `.yml` file in the directory, one at a time, each
within the timeout. The suite exits with its most severe result. See
[`--suite`](platform-cli.md#a-suite-of-scenarios---suite).

### 5. Calibrate `N`, `k` and `j` before trusting a FAIL

The QuickStart agent is deterministic, so an unchanged candidate always passes.
**A model-driven agent is not.** Its behavior set moves between runs, and no
default `k` ships. Before you rely on a verdict, run a scenario whose candidate
is identical to its reference several times. Count how often it FAILs against
itself, and choose `runs`, `k` and `j` where that count is zero. The procedure
and its cost are in
[Calibrating `N`, `k` and `j`](platform-cli.md#calibrating-n-k-and-j).

### 6. Put the gate on your pull requests

Copy
[`examples/github-actions/behavioral-gate.yml`](../examples/github-actions/behavioral-gate.yml)
to `.github/workflows/`, pin both Trustvian `uses:` lines to a reviewed commit,
and point `scenario:` (or `suite:` and `scenario-timeout:`) at your files. It
has two jobs:
- **`run`** runs the scenario on your pull request's code with read-only
  permissions. Its exit code is the check.
- **`comment`** holds the workflow's only write scope. It runs no pull request
  code and posts the result as one comment, edited in place on each push: the
  version 2 report, the same Markdown as `trustvian eval report --format md`.

The actions build Trustvian from the commit their `runtime.env` pins rather
than downloading this archive. Pin them to a release's commit, and re-pin when
you upgrade: a pin older than `v0.12.1` posts the version 1 rendering. On a
pull request from a fork, GitHub gives the comment job a read-only token: you
get a warning and the job summary, not a comment. See
[Running behavioral scenarios in GitHub Actions](ci-github-action.md).

## Requirements

- **Go 1.27 or newer** for the CLI and SDK. A Go 1.21+ toolchain with the
  default `GOTOOLCHAIN=auto` downloads 1.27 automatically.
- **Docker with Compose v2**, only for the
  [reference deployment](../deployments/docker-compose/README.md).

## Install

CLI:

```bash
go install github.com/trustvian/trustvian/cmd/trustvian@latest
```

Or clone and use the Makefile:

```bash
git clone https://github.com/trustvian/trustvian.git
cd trustvian
make build      # -> bin/trustvian
make demo       # analyze the bundled example fixture
make baseline-demo
```

Go SDK, in your own module:

```bash
go get github.com/trustvian/trustvian
```

## Your first analysis (CLI)

Create `event.json`:

```json
[
  {
    "id": "evt-1",
    "timestamp": "2026-01-01T12:00:00Z",
    "actor": { "id": "svc-payment", "type": "service", "identity_confidence": 0.95 },
    "operation": { "category": "http", "name": "POST /payment" },
    "target": { "name": "payment-db" },
    "context": { "environment": "production" },
    "attributes": { "duration_ms": 42 }
  }
]
```

```bash
trustvian analyze event.json
```

```
Trustvian Behavioral Analysis

Service: svc-payment
Anomaly: 1.00
Trust:   0.95
Risk:    LOW

Detected:
  ! fingerprint never observed for this actor

Decision: ALLOW
Reason:   risk within tolerance
```

`Anomaly: 1.00` looks alarming on its own — this is the *first time*
Trustvian has ever seen this actor, so it's maximally novel by
definition. But `Trust: 0.95` and `Decision: ALLOW` show the full
picture: novelty on its own, from an identity Trustvian has high
confidence in, isn't treated as dangerous. This split is deliberate —
see [Architecture](ARCHITECTURE.md#cold-start-two-numbers-not-one) and
[Use Cases](use-cases.md) for why.

Full command reference: [CLI Guide](cli-guide.md).

## Your first analysis (Go SDK)

```go
package main

import (
	"context"
	"fmt"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

func main() {
	engine := trustvian.NewEngine()

	result, err := engine.Analyze(context.Background(), event.Event{
		ID:        "evt-1",
		Timestamp: time.Now(),
		Actor: event.Actor{
			ID:                 "svc-payment",
			Type:               event.ActorTypeService,
			IdentityConfidence: 0.95,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryHTTP,
			Name:     "POST /payment",
		},
		Target:  event.Target{Name: "payment-db"},
		Context: event.Context{Environment: "production"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(result.Trust.Score, result.Trust.Risk, result.Decision)
	// 0.95 low observe_only — NewEngine's default policy observes; see below

	engine.Observe(context.Background(), result) // safe to call unconditionally
}
```

Full reference and a worked multi-event example: [Go SDK Guide](sdk-guide.md).

## Configuring a custom policy from a file

The default `Engine` above always resolves to `observe_only` — real
`ALLOW`/`BLOCK`/`REQUIRE_APPROVAL` differentiation needs a configured
`Policy`. As of `v0.5`, you can load one from a YAML file without
writing Go:

```go
cfg, err := config.LoadFile("trustvian.yaml")
if err != nil {
	panic(err)
}
p, err := config.CompilePolicy(cfg)
if err != nil {
	panic(err)
}
engine := trustvian.NewEngine(trustvian.WithPolicy(p))
```

See [Policy Guide § Loading a Policy from a YAML
file](policy-guide.md#loading-a-policy-from-a-yaml-file) for the file
format and what strict decoding rejects. The CLI can load the same
file directly, without any Go code:

```bash
trustvian analyze --config trustvian.yaml event.json
```

See [CLI Guide § --config](cli-guide.md#--config-path).

## Enabling behavioral signals (`v0.6`/`v0.7`)

Sequence/Markov/delegation detection (`transition_deviation`,
`ngram_deviation`, `markov_surprisal`, `delegation_deviation`, ...)
all ship **opt-in, disabled by default** — every existing user's
behavior stays unchanged until deliberately configured. As of
[task 033](archive/tasks/v0.7/033-v07-stabilization-release-gate.md), enabling them
follows the identical pattern as Policy above, through a second,
independent document:

```go
cfg, err := config.LoadAnomalyFile("anomaly.yaml")
if err != nil {
	panic(err)
}
ac, err := config.CompileAnomaly(cfg)
if err != nil {
	panic(err)
}
engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(ac))
```

```bash
trustvian analyze --anomaly-config anomaly.yaml event.json
```

See [Anomaly Configuration Guide](anomaly-config-guide.md) for the
full field reference (every weight/threshold, its default, and its
valid range).

## Where to next

- Writing custom rules (`BLOCK`/`ALERT`/`ALLOW` decisions): [Policy Guide](policy-guide.md)
- Keeping what Trustvian learns across restarts, and PostgreSQL: [Storage Guide](storage-guide.md)
- Running it as a service — Collector, PostgreSQL, health endpoints: [Reference Deployment](../deployments/docker-compose/README.md)
- Operating it — metrics and resource bounds: [Observability](observability.md); backup, restore, upgrade: [Operations](operations.md)
- Verifying a release you download: [Release Guide § Verifying a published release](release-guide.md#verifying-a-published-release)
- Enabling anomaly signals: [Anomaly Configuration Guide](anomaly-config-guide.md)
- Feeding in OpenTelemetry spans: [OpenTelemetry Adapter](OPENTELEMETRY.md)
- Four worked real-world scenarios: [Use Cases](use-cases.md)
- How the pieces fit together: [Architecture](ARCHITECTURE.md)
