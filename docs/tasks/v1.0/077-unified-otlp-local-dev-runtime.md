# 077 — Unified OTLP Local Dev Runtime

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [062](062-integrated-local-developer-workflow.md),
[073](073-otel-collector-evaluation-ingest.md),
[074](074-zero-input-live-behavior-webui.md)
Blocks: [072](README.md) — the OSS `v1.0` release gate;
[078](078-behavioral-scenario-suites.md)

[075](075-ai-semantic-telemetry-normalization.md) is **optional semantic
enrichment running in parallel**, not a prerequisite. This runtime transports
whatever telemetry the workload emits; 075 decides how richly that telemetry
is read. `trustvian dev` is useful at today's HTTP, DB and RPC fidelity and
becomes better when 075 lands. Neither blocks the other, and they may be
implemented concurrently.

## Objective

Collapse the local setup into one command.

```bash
trustvian dev -- python agent.py
```

```text
Trustvian Dev

Project      my-repo
Agent        support-agent
Candidate    git:43af19c
Environment  local

● OTLP receiving
● Agent running

Live behavior
  model → ollama
  GET   → crm
  GET   → knowledge
  POST  → export      NEW

Web
  http://127.0.0.1:<port>/
```

The application is **not modified** and gains **no Trustvian dependency**. The
runtime composes around it.

## Why

**The current local path has five steps that are each reasonable and
collectively too many.** `make local` starts the control plane and publishes a
discovery file — task 062 did that well. But to see an agent's behavior a
developer still has to run a Collector, write a processor configuration
naming an evaluation run, create a project, an agent, a candidate and a run
through the API or CLI, start the workload with the right OTLP environment,
and then find the WebUI. Every one is documented; together they are the reason
someone gives up before the first pulse appears.

**Task 074 removed the browser half of this friction and left the terminal
half.** After 074, opening the WebUI needs no identifier — but something still
has to create the hierarchy and point telemetry at it. 074 says so explicitly
and names automatic local provisioning as separate follow-up work. **This is
that work.**

**The product claim depends on it.** *"Run your agent locally. Trustvian shows
how it behaves before you ship it."* is a one-command promise, and the release
gate's Track B opens with running locally.

## Scope

One command that:

- starts the local control plane, as `make local` does today;
- receives OTLP, by composing the existing Collector or by other means the
  task must evaluate;
- establishes **instrumentation ownership explicitly**, never by inferring
  absence;
- binds loopback ports chosen dynamically, and publishes discovery state;
- establishes the Project, Agent, Candidate, Environment and EvaluationRun
  context deterministically;
- launches the child command and gets out of its way;
- prints the WebUI URL and the active evaluation;
- shuts down cleanly and propagates the child's exit code.

## Non-goals

**No embedding.** The application does not import, link, wrap or configure
Trustvian. If it must, the design is wrong.

**No source modification, ever.** The runtime does not write to the
application's repository — not `requirements.txt`, not `pyproject.toml`, not a
config file, not a lockfile, not a `conftest.py`.

**No replacement of the Collector processor.** Task 073's processor remains
the right answer for fan-out deployments and is not deprecated by this.

**No remote or production runtime.** Loopback, one developer, one machine.
Multi-node is task 069's.

**No language-specific product surface.** Python zero-code instrumentation may
be *offered* (below), but Trustvian does not become a Python tool.

**No provisioning outside the local runtime.** This task's automatic
provisioning is a property of `trustvian dev` on a developer's machine. It
does not make arbitrary OTLP anywhere create durable entities — that remains
refused, as task 074 states.

## Child process discipline

The command wraps an arbitrary program, so it must behave like a well-mannered
wrapper. This is unglamorous and it is where wrappers usually fail:

```text
working directory    inherited, unchanged
arguments            passed through verbatim after --
environment          inherited, plus only the OTLP variables required
stdin/stdout/stderr  passed through; the child's output is not captured,
                     interleaved or reformatted
signals              SIGINT and SIGTERM forwarded to the child; the runtime
                     shuts down after the child, not before it
exit code            the child's, propagated exactly — including signal deaths
cleanup              only processes the runtime started; never the child's
                     own descendants beyond normal group semantics, and never
                     anything it did not start
```

A developer must be able to `^C` and get their shell back, with the child's
status intact and nothing orphaned.

## Local identity

Provisioning is only acceptable if identity is **deterministic** — the same
repository and the same commit must produce the same identity across runs, or
the baseline learns nothing and every run looks novel.

Sources to evaluate:

| Concept | Source | Fallback |
|---|---|---|
| Project | the git repository, else the working directory name | explicit flag |
| Agent | `OTEL_SERVICE_NAME` / `service.name` | explicit flag — **not** a guess |
| Candidate | the git commit, with a dirty-worktree marker | explicit flag |
| Environment | `local` | explicit flag |
| EvaluationRun | this invocation | generated per run |

Two rules that matter more than the table:

**No ephemeral identity may become behavioral identity.** Not a process ID,
container ID, pod name, port, timestamp or random token. A candidate is a
version, and a version that changes every run teaches the baseline nothing —
this is the same rule task 051 applied when it kept run and candidate metadata
out of the fingerprint.

**A missing `service.name` is not silently invented.** If the agent's identity
cannot be established, the runtime says so and asks for a flag. A fabricated
agent name pollutes a durable hierarchy and is indistinguishable afterwards
from a real one. A documented fallback — the binary name, say — is acceptable
only if it is stated in the output, so the developer sees what was assumed.

A dirty worktree must be visible in the candidate identity. Evaluating
uncommitted work is normal and useful; silently attributing it to a clean
commit is not.

## Instrumentation ownership

The runtime wraps an arbitrary child process, and that bounds what it can know.

### What cannot be promised

A child may initialize OpenTelemetry **after** it starts — from application
code, a framework's startup path, a Java agent, Python site customization, an
environment-driven loader, or a mechanism that does not exist yet. There is no
general, reliable way to inspect an arbitrary program beforehand and prove
whether it will do so.

So the runtime must never reason:

```text
I detected no instrumentation, therefore there is none, therefore I will
inject my own.
```

That inference is wrong precisely when it is most expensive: a child that
instruments itself a moment later then has two stacks, every action is
observed twice, and **duplicate spans are a behavioral lie** — the same action
looks like an actor doing something twice, which is exactly the kind of
novelty Trustvian is built to notice.

### Positive evidence only

Instrumentation ownership is therefore an explicit mode with conservative
defaults. Conceptually — the flag spelling is an implementation decision:

| Mode | Meaning |
|---|---|
| `existing` | Trustvian only routes and configures OTLP. It injects nothing |
| `python-zero-code` | Trustvian deliberately attaches its managed Python zero-code runtime, after compatibility checks |
| `none` | Trustvian launches the child and manages no instrumentation. For telemetry that reaches the Collector by another route |
| `auto` | Resolve conservatively, below |

`auto` resolves like this, and only like this:

```text
if positive evidence proves a supported existing instrumentation path
        → existing
else if a safe, explicitly supported zero-code path can be proven for this
        interpreter and environment
        → that path
else
        → stop, with an actionable message naming the modes
```

**Absence of evidence never selects injection.** When ownership cannot be
established safely, the runtime asks rather than guesses — one clear message
beats a silently doubled dataset that looks like an agent behaving strangely.

### What counts as positive evidence

Illustrative, and explicitly **not** exhaustive:

```text
an explicit Trustvian mode flag
a recognized OTEL_* configuration already present in the environment
a recognized runtime wrapper in the command line
a recognized Java agent argument
a recognized `opentelemetry-instrument` invocation
a future explicit runtime contract
```

No list covers every instrumentation setup, and the specification does not
pretend one does. That is exactly why `auto` fails closed rather than falling
through to injection.

### The guarantee, stated precisely

> The runtime never knowingly attaches a second instrumentation stack when an
> existing one is explicitly selected or positively identified.

and

> When Trustvian cannot establish instrumentation ownership safely, it asks
> for an explicit mode rather than guessing.

Both are testable. "The runtime detects all existing instrumentation" is not,
and is not claimed.

## Python zero-code, if offered

Optional, and only under conditions the specification makes binding:

- the application repository is **never** modified;
- managed tooling lives outside it — `~/.trustvian/runtime/` or equivalent —
  and never on the application's dependency path in a way that survives the
  run;
- interpreter compatibility and importability are **proven before use**, not
  assumed: a virtualenv, a different Python version, a conda environment and a
  system interpreter are all normal and all break naive injection. This proof
  is what lets `auto` select the path at all;
- failure is **safe and loud**: if instrumentation cannot be attached
  reliably, the runtime says so plainly and either continues without it — with
  the developer told the behavior will be transport fidelity at best — or
  stops. It never half-attaches and never silently produces partial telemetry.

**The promise must be stated honestly.** Not *"zero dependencies anywhere"*,
which is false the moment an OTel package is loaded into the interpreter. The
promise is:

> No Trustvian dependency in your application, and no modification to your
> source.

## Direct OTLP, or keep composing the Collector

The task must evaluate, and decide, whether the unified runtime should expose
an OTLP endpoint itself or continue to compose the minimal Collector.

The long-term principle is *if it emits OpenTelemetry, Trustvian can observe
it.* The near-term caution is that OTLP is a protocol stack — gRPC and HTTP,
compression, partial success, backpressure — and owning it is a real, ongoing
cost that the Collector already carries correctly.

Framing for the decision, not a decision:

| Compose the Collector | Own the receiver |
|---|---|
| No new protocol surface, no new dependency | One process, one port, simpler mental model |
| A second binary to ship, configure and supervise | A protocol stack to implement and keep current |
| Fan-out stays first-class | Fan-out needs a deliberate story |

Whatever is chosen, **the Collector processor is not removed**. It is the
right answer for deployments that fan out to other backends, and task 073's
work stands.

## Failure semantics

| Situation | Behaviour |
|---|---|
| a port is unavailable | pick another; loopback, dynamic, never a fixed port a developer must free |
| the control plane fails to start | fail before launching the child, with the reason; never start a workload that has nowhere to report |
| identity cannot be established | stop and name the flag that fixes it |
| instrumentation ownership cannot be established | stop, naming the available modes. Never inject on absence of evidence |
| instrumentation cannot attach in the selected mode | stated plainly; continue degraded or stop, never silently partial |
| the child exits immediately | report its status; do not hang waiting for telemetry |
| the child is killed by a signal | propagate faithfully |
| the runtime is interrupted | forward to the child, wait, shut down, leave nothing orphaned |

## Security

Loopback binding only, as task 062 established. No account, no token, no
outbound call. The runtime writes only to its own state directory and to the
platform database — **never** to the application's repository. Discovery state
carries no credential.

The content boundary of tasks 075 and 076 applies unchanged: this runtime
transports telemetry, it does not widen what Trustvian retains.

## Compatibility

Additive: a new CLI command family. `make local`, `trustvian-local`, the
Collector processor and every existing route keep working unchanged, and a
developer who prefers the explicit path keeps it.

Exit codes follow the existing control-plane family — but note that this
command's exit code is the **child's**, which is a deliberate and documented
exception this task must state clearly in `docs/compatibility.md` when it
ships. A wrapper that swallowed its child's status would be unusable in a
script.

## Tests

- Launch, run, exit: a trivial child's exit code is propagated exactly,
  including non-zero and signal deaths.
- Arguments after `--` reach the child verbatim, including flags that collide
  with Trustvian's own.
- Working directory, environment and stdio pass through; the child's output is
  not reformatted.
- `SIGINT` and `SIGTERM` reach the child; the runtime outlives it and then
  stops; nothing is orphaned, asserted by process inspection.
- Identity is deterministic: the same repository and commit produce the same
  project, agent and candidate across runs.
- A dirty worktree is visibly distinguished from a clean commit.
- A missing `service.name` stops with an actionable message, or applies a
  documented fallback **and says so** — never a silent invention.
- No ephemeral value reaches candidate or agent identity, asserted by a scan.
- **Mode semantics**, one case each: `existing` injects nothing and only
  configures OTLP; `python-zero-code` attaches only after its compatibility
  check passes; `none` manages nothing.
- **`auto` fails closed.** With no positive evidence of either path, the
  runtime **stops with an actionable message naming the modes** — it does not
  inject. This is the regression test for the whole section, and it replaces
  any test that would have required detecting arbitrary in-process OTel
  initialization, which is not generally possible and is not asserted
  anywhere.
- `auto` selects `existing` when positive evidence is present — one case per
  recognized form of evidence.
- With `existing` selected or positively identified, **no second stack is
  attached**, asserted by counting instrumentation initializations rather than
  by inspecting the child.
- Instrumentation failure in the selected mode degrades loudly; a test forces
  an incompatible interpreter and asserts the runtime does not half-attach.
- **The application repository is never written to**, asserted by hashing a
  fixture project before and after a full run.
- Ports are dynamic; two concurrent runtimes do not collide.
- The WebUI URL printed is reachable and shows the run without an identifier
  being typed — the task 074 journey, end to end.

## Documentation

Written by the implementation PR: `docs/local-development.md` (the one-command
path as primary), `docs/platform-cli.md`, `docs/compatibility.md` (the new
command and its child-exit-code exception), `docs/OPENTELEMETRY.md`,
`docs/ROADMAP.md`, this task's status, the task index and `CHANGELOG.md`.

## ADR

Warranted for three decisions: the OTLP ownership question — compose the
Collector or own the receiver; automatic local provisioning, why it is
acceptable inside `trustvian dev` and refused everywhere else, and what makes
local identity deterministic; and **instrumentation ownership as
positive-evidence-only**, recording that absence of detectable instrumentation
never implies absence of instrumentation, and why the cost of guessing wrong
is a doubled dataset that reads as anomalous behavior.

## Acceptance criteria

1. One command starts everything and runs an existing agent with no source
   modification and no Trustvian dependency.
2. The application repository is byte-identical afterwards.
3. Project, Agent, Candidate and Run are established automatically and
   deterministically; the same commit yields the same identity.
4. Identity that cannot be established is reported, never invented.
5. The child's stdio, signals and exit code behave as a plain wrapper's.
6. Nothing the runtime did not start is stopped; nothing it started is left.
7. The printed WebUI URL shows the running agent with no identifier typed.
8. When an existing instrumentation path is explicitly selected or positively
   identified, no second stack is attached. When ownership cannot be
   established safely, the runtime asks for an explicit mode rather than
   guessing — it never injects on absence of evidence.
9. Instrumentation failure in the selected mode is loud and safe.
10. `make local`, the Collector processor and every existing surface keep
    working.

## Open questions left to implementation

1. **OTLP ownership** — compose the Collector or own the receiver. Composing
   is assumed until the cost of supervising a second binary is measured
   against the cost of owning a protocol stack.
2. **Whether Python zero-code ships in this task** or follows once the
   compatibility matrix is understood. Following is assumed — which makes
   `auto`'s second branch unreachable until it lands, and `auto` therefore
   resolves to `existing` or stops.
3. **The command name** — `trustvian dev` is assumed; it must not collide with
   the existing families.
4. **Whether a run is created per invocation or per session** when a child is
   restarted under one runtime. Per invocation is assumed.
