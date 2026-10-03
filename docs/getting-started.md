# Getting Started

Two paths:
- [**Developer preview**](#developer-preview) runs an agent you already have
  under Trustvian, from the release archive alone, with no Go toolchain and no
  clone.
- [**The engine**](#requirements) covers the CLI and the Go SDK over events you
  construct.

## Developer preview

From `v0.10.0`. Run your agent under Trustvian, watch its behavior in a browser,
gate a change to it over repeated runs, then put that gate on your pull
requests.

**You need:**
- Linux or macOS, on amd64 or arm64. `trustvian dev` and `eval run --suite`
  refuse to run on Windows; use WSL2 there.
- `git`. The agent's directory must be a git repository with at least one
  commit. Trustvian names the project after the repository and the candidate
  after the commit.
- An agent that already sends OpenTelemetry traces over OTLP. Trustvian sets
  the standard `OTEL_EXPORTER_OTLP_*` variables and does not add an SDK — see
  [instrumentation ownership](platform-cli.md#instrumentation-ownership). It
  must also declare `OTEL_SERVICE_NAME`: that is the agent Trustvian reports on.

### 1. Install from the archive

```bash
V=v0.10.0
A=trustvian_${V}_linux_amd64          # or linux_arm64, darwin_amd64, darwin_arm64
curl -sLO https://github.com/trustvian/trustvian/releases/download/$V/$A.tar.gz
curl -sLO https://github.com/trustvian/trustvian/releases/download/$V/checksums.txt
sha256sum -c checksums.txt --ignore-missing   # macOS: shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf $A.tar.gz
export PATH="$PWD/$A:$PATH"
trustvian version
```

The archive holds three binaries, and they must stay side by side: `trustvian`
finds the other two next to itself.

```text
trustvian            the CLI
trustvian-local      the local control plane
trustvian-collector  the OTLP receiver and the Trustvian processor
```

### 2. Start the control plane

In your agent's repository, in a terminal of its own:

```bash
cd my-agent
echo '.trustvian/' >> .gitignore
git add .gitignore && git commit -m 'Ignore Trustvian local state'
trustvian-local
```

```text
Trustvian local runtime
API:   http://127.0.0.1:39529
Web:   http://127.0.0.1:39529/
...
Local clients in this directory can now omit --api-url.
```

It keeps its state in `.trustvian/` and binds loopback only. Ignoring that
directory matters: an untracked file there makes the worktree dirty, so the
candidate would read `git:<sha>+dirty`. Leave it running and open the `Web:` URL
in a browser.

### 3. Run your agent under Trustvian

In a second terminal, in the same directory, with the `API:` URL from step 2:

```bash
OTEL_SERVICE_NAME=support-agent \
  trustvian dev --api-url http://127.0.0.1:39529 -- ./agent.sh
```

`dev` prints the project, agent, candidate and run it provisioned. It runs your
command unchanged with an OTLP receiver around it, and exits with your command's
status. The browser's **Live** view shows the run as soon as telemetry arrives,
with no identifier to type. Select it to see the agent, tool and service flow
with the decision, risk, trust and anomaly the server computed.

`dev` does not use step 2's discovery file. Without `--api-url` it starts a
control plane of its own that stops when your command exits, which is too soon
to watch. Every flag is in
[Platform CLI § `trustvian dev`](platform-cli.md#trustvian-dev).

Use one identity throughout: the defaults, with no `--project` or `--candidate`.
An agent belongs to the project that first registered it. A scenario in step 4
runs under the repository's name, so after a `dev --project other` run for the
same agent it stops with `run is outside the scenario execution's scope`
(exit `3`).

### 4. Gate a change with a scenario

A scenario runs each side `runs` times and compares which behaviors appear. Save
this as `scenarios/export-tool.yaml`. It is the scenario this path is tested
with, where the candidate's one change is that it reaches a third tool:

```yaml
version: v1
name: export-tool
runs: 3
instrumentation: existing
reference:
  command: [./agent.sh]
  env: {SPAN_COUNT: "2", OTEL_SERVICE_NAME: support-agent}
candidate:
  command: [./agent.sh]
  env: {SPAN_COUNT: "3", OTEL_SERVICE_NAME: support-agent}
gate:
  added_candidate_presence_minimum: 1        # k
  added_reference_presence_maximum: 0        # j
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
```

```bash
trustvian eval run --scenario scenarios/export-tool.yaml
echo $?    # 0 PASS, 1 gate FAIL, 2 usage, 3 operational
```

`eval run` finds step 2's control plane on its own. It prints each repetition's
`dev` output to stderr, then the behaviors with their presence counts and the
gate's six checks:

```text
Behavior                                  reference   candidate
  tool/crm_lookup → tools.localhost              3/3         3/3
  tool/export_customer → tools.localhost         0/3         3/3   + added
  tool/knowledge_search → tools.localho…         3/3         3/3
...
  FAIL repeatedly added behaviors: 1 (<= 0)
...
Gate: FAIL
```

`export_customer` appears in every candidate run and in no reference run, so
the gate FAILs with exit `1`: the candidate added a behavior. `--json` writes
the result document instead.

Every field is required and nothing has a default; the format is in
[Platform CLI § Behavioral scenarios](platform-cli.md#behavioral-scenarios-trustvian-eval-run).

**More than one scenario:**

```bash
trustvian eval run --suite scenarios --scenario-timeout 10m
```

This runs every `.yaml` and `.yml` file in the directory, one at a time, each
within the timeout. The suite exits with its most severe result. See
[`--suite`](platform-cli.md#a-suite-of-scenarios---suite).

### 5. Calibrate `N`, `k` and `j` before trusting a FAIL

The stand-in above is deterministic, so an unchanged candidate always passes.
**A model-driven agent is not.** Its behavior set moves between runs, and no
default `k` ships. Before you rely on a verdict, run a scenario whose candidate
is identical to its reference several times. Count how often it FAILs against
itself, and choose `runs`, `k` and `j` where that count is zero. The procedure
and its cost are in
[Calibrating `N`, `k` and `j`](platform-cli.md#calibrating-n-k-and-j).

### 6. Put the gate on your pull requests

Copy
[`examples/github-actions/behavioral-gate.yml`](../examples/github-actions/behavioral-gate.yml)
to `.github/workflows/`, pin both `uses:` lines to a reviewed Trustvian commit,
and point `scenario:` (or `suite:` and `scenario-timeout:`) at your files. It
has two jobs:
- **`run`** runs the scenario on your pull request's code with read-only
  permissions. Its exit code is the check.
- **`comment`** holds the workflow's only write scope. It runs no pull request
  code and posts the result as one comment, edited in place on each push.

The actions build Trustvian from the pinned commit rather than downloading this
archive. On a pull request from a fork, GitHub gives the comment job a
read-only token: you get a warning and the job summary, not a comment. See
[Running behavioral scenarios in GitHub Actions](ci-github-action.md).

### The agent this path is tested with

`./agent.sh` stands in for your agent. It wraps the repository's model-free
test producer,
[`processor/cmd/agent-producer`](../processor/cmd/agent-producer/main.go). The
producer emits `crm_lookup`, `knowledge_search` and `export_customer` as GenAI
tool spans, `SPAN_COUNT` of them, in that order. To reproduce the walkthrough
exactly, build it with Go from a clone
(`cd processor && go build ./cmd/agent-producer`) and copy it into your
directory beside this wrapper:

```sh
#!/bin/sh
# agent-producer reads its own variables rather than OTEL_EXPORTER_OTLP_*.
OTLP_ENDPOINT="$TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT" SERVICE_NAME="$OTEL_SERVICE_NAME" \
ENVIRONMENT=local MODE=semantic exec ./agent-producer
```

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
