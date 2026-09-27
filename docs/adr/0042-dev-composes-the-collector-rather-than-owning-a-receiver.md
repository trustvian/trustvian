# 0042 — `trustvian dev` composes the Collector rather than owning a receiver

**Status:** Accepted

## Context

[Task 077](../tasks/v1.0/077-unified-otlp-local-dev-runtime.md) asks for one
command: `trustvian dev -- python agent.py`. The developer runs their agent
unchanged and gets a local control plane, an OTLP endpoint their SDK can reach,
a provisioned evaluation run, and a URL to watch it.

The OTLP endpoint is the part with a real choice behind it. The CLI could open
one itself — the root module already depends on OpenTelemetry through
`internal/otel`, and an `otlptracehttp` receiver is not much code to write.

Two things argue against that reading of "not much code".

First, an OTLP receiver is not a handler; it is a protocol stack. gRPC *and*
http/protobuf, gzip, partial success responses, request size limits,
backpressure when the consumer is slower than the producer, and the semantics of
each of those when a client gets them wrong. `trustvian-collector`
([task 073](../tasks/v1.0/073-otel-collector-evaluation-ingest.md)) already carries that
stack, from upstream, plus the Trustvian processor that scores what arrives and
posts evidence to a run ([ADR 0038](0038-collector-evaluation-ingest-is-an-http-adapter.md)).

Second, `.claude/rules/architecture.md` states the boundary directly: "A future
OTel Collector processor is explicitly a separate deliverable — it needs the
heavy `otelcol-builder` toolchain, which must never leak into this module's
dependency graph." A receiver in `cmd/trustvian` is that leak, arriving through
the front door.

## Decision

### 1. dev supervises `trustvian-collector`; it does not implement a receiver

`cmd/trustvian/dev_collector.go` generates a configuration, starts the real
binary, waits for it to report ready, and stops it on the way out. The CLI stays
a process supervisor, which is what `cmd/trustvian/cli_architecture_test.go`
enforces: `net.Listen` is permitted in exactly that one file, for reserving a
port, and every serving construct — `http.Serve`, `http.ListenAndServe`,
`(*http.Server)`, `.Accept(` — remains forbidden everywhere in the package.

Narrowing that guard rather than loosening it is the point. The test's failure
message is the record of what the exemption is for.

### 2. The configuration is generated as text, not through a YAML library

`.claude/rules/go.md` confines `go.yaml.in/yaml/v3` to the `config` package.
A `text/template` is sufficient for a fixed-shape document with a handful of
substituted scalars, and it adds no dependency to the root module.

This makes the generated document a contract expressed in text — the same shape
[ADR 0035](0035-local-runtime-composes-platform-without-reversing-modules.md) §9
chose for the discovery file: both sides implement one format and neither
imports the other. `docs/compatibility.md` already classifies the processor's
configuration keys OPERATIONALLY STABLE, so the surface being written has a
promise attached.

Every substituted scalar is validated before it reaches the template
(`validateCollectorScalar`). A value that would need quoting is **refused**, not
rewritten: a rewritten path is a different file, and a rewritten run id is a
different run. The template quotes its string scalars, so the check is scoped to
what actually breaks a quoted YAML scalar rather than to a general blocklist —
an early version banned every colon and refused the legitimate profile
`git:43af19c`.

### 3. Both OTLP protocols are enabled

dev steers any SDK that reads the environment to http/protobuf. A workload that
builds its exporter in code chooses its own transport and never consults
`OTEL_EXPORTER_OTLP_PROTOCOL` — this repository's own
`processor/cmd/demo-producer` uses `otlptracegrpc` exactly that way.

An HTTP-only receiver would have observed nothing from it, silently, which is
the failure mode this command exists to remove. So both are enabled and both
endpoints are advertised: `TRUSTVIAN_DEV_OTLP_ENDPOINT` and
`TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT`. One extra loopback port is a small price for
meaning "if it emits OpenTelemetry, Trustvian can observe it".

### 4. One Collector process per run, with a file-backed engine store

The Collector is per-invocation, so its engine is too. The first version of the
generated configuration had no `storage:` block, and the consequence was not
cosmetic: with the in-memory default the learned baseline was discarded at every
run's end. That made the design's own claim that "two runs of one candidate share
a learned baseline" false, and it made both engine-evidence gates inert — with an
empty baseline every behavior is novel, anomaly *confidence* is zero, trust is
never penalized, no `BLOCK` is ever decided, and `max_block_decisions` and
`max_critical_risk_observations` can only ever read zero. A gate that cannot
fail is not a gate.

So dev configures a file store, one file per behavioral profile, because the
profile *is* the learning scope ([ADR 0024](0024-learning-scope-is-a-baseline-key-dimension.md)).

The file store has no cross-process locking — `docs/storage-guide.md` says to use
it for a single-process deployment. Two writers on one file is worse than two
separate files: the store reads, modifies and rewrites a snapshot, so concurrent
runs would silently discard each other's learning and leave a file belonging to
neither. Nothing in the store detects that, so dev refuses it, with a pid lock
beside the baseline and a message naming the other run and how to get a separate
learning scope.

### 5. The debug exporter terminates the pipeline, at `basic` verbosity

A pipeline needs an exporter and dev has nowhere else to send spans. Verbosity
is `basic` so span attributes are not printed: this command must not become a way
to read prompt content out of a log. Tasks 075 and 076 own that boundary and
neither has moved.

## Alternatives considered

**An OTLP receiver in `cmd/trustvian`.** Rejected: it reimplements a protocol
stack the repository already ships, and it is the dependency leak
`.claude/rules/architecture.md` names explicitly. The architecture test would
have had to be weakened rather than narrowed.

**Embedding the Collector as a library in the root module.** Rejected for the
same dependency reason, more strongly: `otelcol` pulls in the builder's whole
graph, and `make check-modules` exists to keep it out.

**Emitting the configuration with a YAML library.** Rejected: it means either a
fourth third-party dependency in the root module or widening `config`'s
confinement, for a fixed-shape document of about thirty lines.

**A single long-lived Collector shared by successive runs.** Rejected for now:
the processor's `evaluation:` block names one run id, so a shared process would
need a reconfiguration path that does not exist. The per-run process plus a
per-profile file store gets the baseline continuity that mattered, without it.

**An in-memory engine store, accepting baseline loss.** Rejected once measured:
it is the difference between two gates that can fire and two that cannot.
`TestDevJourneyAgainstRealBinaries` asserts anomaly confidence grows between two
sequential runs of one candidate, and that assertion fails if the store
regresses.

## Consequences

- The CLI remains a supervisor. The architecture test's `net.Listen` exemption is
  scoped to one file and one purpose, and will fail if a second file needs it.
- The generated configuration is a text contract against OPERATIONALLY STABLE
  processor keys. A processor that renames one breaks dev, and
  `docs/compatibility.md` is where that promise is recorded.
- dev needs two loopback ports for OTLP instead of one. Ports are chosen by
  binding `:0` and closing, which is advisory — a collision is retried
  (`collectorPortAttempts`) rather than reported, because it is not a condition a
  developer can act on.
- The per-profile baseline file is shared state between runs, with a
  single-writer rule enforced by dev rather than by the store. A second
  concurrent run of the same candidate is refused, not queued.
- Because the Collector is a separate process with its own log, a diagnostic
  reads `collector.log` in dev's state directory. The generated document
  deliberately carries no `service.telemetry:` block:
  `trustvian-collector` is assembled with a minimal telemetry factory whose
  configuration type is `struct{}`, so any key there rejects the whole document.
