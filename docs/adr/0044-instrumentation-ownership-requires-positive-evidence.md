# 0044 — Instrumentation ownership requires positive evidence

**Status:** Accepted

## Context

`trustvian dev` composes an OTLP endpoint and points the workload at it
([ADR 0042](0042-dev-composes-the-collector-rather-than-owning-a-receiver.md)).
Doing that usefully needs an answer to one narrow question: is the workload
already going to initialize OpenTelemetry, or does dev need to attach something?

There is no general way to know. A program can initialize the SDK from
application code, a framework's startup path, a Java agent, Python site
customization, an environment-driven loader, or a mechanism that does not exist
yet — all of them *after* the process starts, which is after the only moment dev
could inspect anything.

The tempting shape is auto-detection with a fallback: look for signs of
instrumentation, and inject a zero-code agent when none are found. The
attractiveness is obvious — it is the shape that makes `trustvian dev -- python
agent.py` work on an uninstrumented script.

The cost is specific. If the workload instruments itself a moment later, it now
has two SDKs. Every action is observed twice. And duplicate spans are not a
degraded signal in this system — they are a behavioral *lie*. One actor appears
to do everything twice, at twice the rate, with a sequence no baseline has seen,
which is precisely the novelty Trustvian exists to notice. The engine would
report an anomaly that the wrapper created. That is worse than reporting nothing,
because it is wrong in the direction of a false alarm on the security-relevant
path, and the cause is invisible from inside the data.

## Decision

### 1. Absence of evidence never selects injection

`--instrumentation` has three available modes:

| Mode | Meaning |
|---|---|
| `existing` | the workload already sends OpenTelemetry; dev configures OTLP and injects nothing |
| `none` | dev manages no instrumentation at all, not even routing — but still declares identity, see §7 |
| `auto` (default) | resolve from positive evidence, or **stop** |

`auto` has exactly two outcomes: it finds positive evidence and behaves as
`existing`, or it refuses to start anything. It never falls through to injection.

An unknown mode is a usage error naming the modes, never a silent fallback to
`auto` — a typo that quietly selected a mode is the one thing this decision
exists to prevent.

### 2. The evidence list is positive-only, and deliberately not exhaustive

| Evidence | Why it proves an SDK exists |
|---|---|
| `OTEL_TRACES_EXPORTER`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` set | each is read by an auto-configuring SDK; a workload carrying one has an SDK to configure |
| `argv[0]` is `opentelemetry-instrument` | the workload is being started *through* instrumentation |
| `-javaagent:` naming a jar whose basename contains `opentelemetry` | a Java agent that is an OpenTelemetry agent — the name is checked as well as the flag, because `-javaagent:` is also used by profilers and coverage tools |
| `NODE_OPTIONS` containing both `--require` and `@opentelemetry` | Node's zero-code path is `--require` of a registration module; both halves are checked because `--require` alone is used for all sorts of things |

No list covers every instrumentation setup, which is exactly why `auto` fails
closed rather than treating a miss as proof of absence.

`otel-cli` is **not** on the list. It is a standalone binary that emits a span
and exits; its presence on a command line says something was instrumented, not
that the *workload* will configure an SDK in-process. It proves nothing dev can
act on, so it is absent rather than included for the appearance of coverage.

### 3. Evidence is read from the pre-mutation environment

`resolveOwnership` takes the environment snapshot captured before dev sets
anything. This is the subtlety that makes the mode meaningful at all: dev sets
`OTEL_*` variables itself, so evidence read afterwards would be dev detecting its
own configuration and concluding the workload is instrumented — a wrong answer
that looks right, and one that would make `auto` accept everything.

### 4. `OTEL_SDK_DISABLED=true` is the opposite of evidence

It is checked first, for every mode that would route, and it refuses.

It is a variable an auto-configuring SDK reads, so a naive list would count it as
proof an SDK exists. But a workload whose SDK is switched off emits nothing, so
routing telemetry to it produces an evaluation run with no records — which the
minimum-evidence gates then fail for a reason that looks nothing like the cause.
Per the specification the value is case-insensitive `true`/`false` only; `1` and
`yes` are not disables, because guessing there would turn an unrelated value into
a refusal.

`--instrumentation none` is exempt, since it routes nothing and makes no claim
about how telemetry arrives.

### 5. Ownership is resolved before anything starts

Before the control plane, before the Collector, before provisioning. A refusal
must not leave a started runtime, a half-configured Collector or a provisioned
run behind — the message says "so nothing was launched", and that has to be
true.

### 6. `python-zero-code` is named and refused, not omitted

The mode is parsed and rejected with a message explaining what it will do and
why it waits. Named rather than omitted so the name cannot be reused for
something else, and so a caller who asks for it gets a straight answer instead of
"unknown mode".

It waits on a compatibility check: a virtualenv, a different Python version, a
conda environment and a system interpreter are all normal and all break naive
injection. Half-attaching is the failure this ADR is about, one layer down.

### 7. `none` means no routing, not no variables

`--instrumentation none` sets no endpoint, no protocol, no exporter selection, no
batch delay and no semantic-convention opt-in. It does still set two:

```text
OTEL_RESOURCE_ATTRIBUTES   deployment.environment.name=<environment>, appended
OTEL_SERVICE_NAME          the Agent, and only when the workload declares none
```

Those are identity, not instrumentation, and identity is what makes the evidence
belong to this run rather than to nothing.
[ADR 0043](0043-dev-provisions-the-local-hierarchy-from-the-repository.md) §1 has
the mechanism: the platform *refuses* a record whose environment differs from its
run's, and the processor derives the actor from the arriving `service.name`. A run
whose telemetry declares neither collects zero usable evidence while every process
involved reports success — which is the same class of silent wrongness this whole
record is about, arriving from the other direction.

So `none` is a statement about who configures the exporter, not a request to be
left entirely alone. The banner prints both names like any other variable, and
says what they are for; an earlier version printed "Set nothing", which was
false in exactly the expensive direction.

## Alternatives considered

**Auto-detect, and inject when nothing is found.** Rejected: this is the
duplicate-span failure. It is silent, it manufactures exactly the signal the
engine is built to trust, and the workload that triggers it is the ordinary case
of "initializes the SDK in `main`".

**Inject, and de-duplicate spans downstream.** Rejected: de-duplication needs a
rule for what counts as the same span across two independent SDKs with
independent ids and independent clocks. Any such rule is a heuristic in the
evidence path, and a wrong merge is unattributable.

**Default to `existing` instead of `auto`.** Rejected: it is a claim dev makes on
the developer's behalf. A workload that is not instrumented then produces a run
with no records, and the minimum-evidence gate failure does not name the cause.
`auto` refusing with an actionable message is the same information, at the right
time.

**Probe the process after it starts — read `/proc`, look for the SDK in memory.**
Rejected: it is platform-specific, racy against the very initialization it is
looking for, and it cannot answer the question before the moment the answer is
needed.

**Accept `OTEL_SDK_DISABLED` as evidence of an SDK.** Rejected on measurement:
the run that results has no records at all.

## Consequences

- `trustvian dev -- python agent.py` on an *uninstrumented* script stops with an
  explanation rather than working by injection. That is a real ergonomic cost,
  taken knowingly, and it is what `python-zero-code` is for.
- The stop message is load-bearing. It says what was looked for, why absence is
  not an answer, and what to choose — because a refusal a developer cannot act on
  is a bug in a different form.
- The evidence list will grow. Adding to it is safe by construction: every entry
  can only turn a refusal into `existing`, never the reverse.
- Terminal handover has a signal consequence worth recording here: when dev hands
  the terminal to the workload, `Ctrl-C` is delivered by the kernel to the
  foreground process group, so dev sees it too and the run is completed with the
  child's own status. A *second* `Ctrl-C` during teardown is not delivered to dev,
  because by then dev has taken the terminal back and the workload's group is
  gone. `docs/compatibility.md` records that row;
  `TestCtrlCOnATerminalCompletesTheRun` asserts the first.
- `TestAutoFailsClosedWithNoEvidence` and
  `TestARefusedOwnershipStartsNothingAndProvisionsNothing` assert the
  fail-closed direction specifically, so a change that adds an injection
  fallback fails a test that names why.
