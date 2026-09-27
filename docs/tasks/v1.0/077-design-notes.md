# 077 — Design notes

**Temporary.** These notes exist to be approved, then folded into the three ADRs
task 077 calls for and deleted before the pull request is merge-ready. They are
not a specification and they do not supersede
[077](077-unified-otlp-local-dev-runtime.md).

## What the reading changed

Three findings reshaped the design before any of the questions below could be
answered honestly.

**1. A working prototype already exists, outside this repository.**
`trustvian/trustvian-python-agent-demo` carries `scripts/tv-dev.sh` — 498 lines
that compose the runtime, the hierarchy, the Collector and the OTLP environment
around an unmodified workload. Its own header says it is "a stand-in for task
077's `trustvian dev`" and that it will be deleted when 077 ships; it probes for
`trustvian dev` on every run and tells the developer to retire it.

That is prior art with the failure modes already burned in, and this design
adopts its ordering wholesale rather than rediscovering it. It also fixes the
command's name: the demo probes for `trustvian dev`, so that is what this must
be called.

**2. Environment agreement is a hard ingest constraint, not a nicety.**
`platform/behavior.go:213` refuses a record whose `Environment` differs from the
run's `EnvironmentRef`, with `ErrBehaviorEnvironmentMismatch`. The engine fills
`Event.Context.Environment` from the resource attribute
`deployment.environment.name` (`internal/otel/otel.go:99`).

So a run created with environment `local` and a child that emits no
`deployment.environment.name` produces **zero usable evidence** — every record
is refused. `dev` must make the child emit it. This is the single most important
consequence for slices 2 and 3, and it is why the child's environment is not
purely "inherited plus the OTLP endpoint".

**3. `OTEL_SEMCONV_STABILITY_OPT_IN=http` is mandatory.** Without it, Python
zero-code instrumentation emits legacy `http.url` / `http.method`, while the
processor reads `server.address` and `http.request.method` — so every span
arrives with an empty target and distinct behaviors collapse into fewer than
there are. The prototype documents this as non-optional and the demo's contract
test asserts its presence. It is a correctness requirement for the *evidence*,
not a preference.

---

## 1. Where `trustvian dev` lives, and how it finds its parts

### The constraint

The root CLI must not import `trustvian-platform`
([ADR 0022](../../adr/0022-core-platform-boundary.md),
[0033](../../adr/0033-developer-cli-is-a-thin-http-adapter.md),
[0035](../../adr/0035-local-runtime-composes-platform-without-reversing-modules.md)),
and `scripts/check-platform-boundary.sh` is a release gate that checks the
module graph. `scripts/release-build.sh` builds exactly one binary —
`./cmd/trustvian` — for five targets. `trustvian-local` and
`trustvian-collector` are repository-internal and ship in no artifact.

### Options

| | Where `dev` lives | What an installed user has |
|---|---|---|
| **A** | root CLI, supervising two located child processes | only `trustvian`; helpers must be found or built |
| **B** | a new `platform/cmd/trustvian-dev` binary | nothing — it is not shipped either, and it is not `trustvian dev` |
| **C** | root CLI, with helper code moved into the root module | requires the platform in the root module — **refused** |
| **D** | A, plus a managed download into `~/.trustvian/` | an outbound network call from a security tool |

### Recommendation: A

B fails the product requirement: the spec's one command is `trustvian dev`, and
the demo probes for exactly that. A second unshipped binary moves the problem
without solving it.

C is the boundary reversal ADR 0035 §1 exists to refuse — and refuses
explicitly, naming this kind of pressure: *"Task 062 is not authorization to
reverse ADR 0022 or ADR 0033. It is the task that tests whether those boundaries
survive the first real pressure on them."* 077 is the second such test.

D is refused on two grounds: "loopback only, no outbound call" is a constraint
of this task, and a security tool that downloads and executes binaries has
acquired a supply-chain surface that would need its own review, its own
signature verification, and its own ADR.

So `dev` is a **supervisor in the root CLI** that locates two executables and
runs them as children. It imports nothing from the platform. The coupling it
does acquire is textual, not structural, and there is precedent: ADR 0035 §9
already has the root module and the platform implementing the *same discovery
file format* on both sides "by contract rather than by import". `dev` extends
that pattern to the Collector's configuration file, whose keys
`docs/compatibility.md` already classifies OPERATIONALLY STABLE.

### Resolution order, per helper

Explicit first, then the two that need no configuration, then nothing:

```text
1. --local-bin / --collector-bin          explicit always wins
2. $TRUSTVIAN_LOCAL_BIN / $TRUSTVIAN_COLLECTOR_BIN
3. the directory of the running trustvian executable  (os.Executable)
4. $PATH
```

Step 3 is the one that matters: it is what would make a release archive
containing all three binaries work with no configuration at all, and it costs
nothing to support now. Nothing searches the working directory or guesses a
repository layout — `make dev` sets the environment variables instead, so
repository knowledge stays in the Makefile where it already is.

### What an installed user must have on disk

**Today: `trustvian dev` cannot work from a bare `go install`.** The release
archive contains one binary. This is stated plainly rather than worked around,
and the error says so:

```text
trustvian dev: cannot find 'trustvian-local'.

dev supervises two helper processes that are not part of the released
trustvian binary:

  trustvian-local      the local control plane
  trustvian-collector  the OTLP receiver and the Trustvian processor

Searched:
  --local-bin             not given
  $TRUSTVIAN_LOCAL_BIN    not set
  /usr/local/bin          alongside this executable — not found
  $PATH                   not found

From a repository checkout:

  make dev ARGS='-- python agent.py'

Nothing is downloaded. trustvian dev makes no network call.
```

**Decision I need from you.** Shipping both helpers in the release archive would
close this properly, and it is not free: it triples artifact size, puts an
OpenTelemetry Collector build into every release, and needs a supply-chain
review plus changes to `release-build.sh` and the release workflow.
`docs/compatibility.md` classifies "Archive internal layout" as OBSERVATIONAL,
so adding files breaks no promise. I recommend it as a **separate task** rather
than smuggling a release-artifact change into a feature PR. Until then `dev` is
a checkout-and-`make` capability, which is exactly what the demo repository and
task 078 need.

---

## 2. OTLP ownership: compose the Collector

### Recommendation: compose, as the spec assumes

Owning a receiver means owning gRPC, HTTP, compression, partial success and
backpressure — a protocol stack the Collector already carries correctly, in a
module that already exists and is already tested. The spec's own framing table
says fan-out stays first-class if the Collector is composed, and task 073's
processor is explicitly not deprecated.

### One Collector process per evaluation run

Forced, not chosen: `processor.EvaluationConfig` compiles once and is fixed for
the process's lifetime, so a different `run_id` means a different process.

### Config generation without a YAML library

`.claude/rules/go.md` confines `go.yaml.in/yaml/v3` to the `config` package, and
the root CLI may not import it. A `text/template` in the standard library is
sufficient and is what this uses — the document is fixed-shape with five
substituted scalars.

Every substituted value is one this process chose (two ports, an API URL it
bound, a run id and profile it derived), so there is no untrusted interpolation.
The template is still written with explicit quoting for the string fields, and a
test asserts a value containing a quote or newline is refused before a file is
written rather than escaped into it — identity fields are refused, not
sanitized, which is the same rule task 054 applies.

### Port selection and readiness

Two ports are needed: the OTLP receiver and the processor's health endpoint.
Chosen by binding `127.0.0.1:0`, reading the port, and closing — advisory, so a
race is possible and must be handled rather than assumed away.

Readiness is the prototype's hard-won two-step, and copying it is deliberate:

```text
1. poll the processor's /livez on the health port
2. then TCP-connect the OTLP port
```

Step 2 exists because step 1 proves only that the *health* port bound. The OTLP
port can still lose its race, and without step 2 that failure surfaces later as
silently missing telemetry. While polling, the child process is watched: if the
Collector exits, its own log is reported instead of waiting out a timeout.

A port collision is retried with fresh ports, bounded, and reported as a
collision rather than as a startup failure.

---

## 3. State directory versus "byte-identical afterwards"

### The conflict is real

ADR 0035 §12 says runtime state is project-local — `.trustvian/` in the working
directory — and gives good reasons: visible to `ls`, removable with `rm -rf`,
isolated per checkout. `make local` does exactly that.

077 acceptance criterion 2 says the application repository is byte-identical
afterwards, and the spec's test hashes a fixture project before and after. When
`dev` runs in the application's repository, those cannot both hold.

### Recommendation: state outside the repository, and extend discovery

The byte-identical test decides, and it decides against writing into the
repository. `dev` is a wrapper around *someone else's* repository — possibly one
the developer does not own, possibly read-only, possibly a worktree they do not
want dirtied — and "no modification to your source" is the product promise the
whole task rests on. A directory appearing in `git status` is a modification,
whether or not a tracked file changed.

So:

```text
~/.trustvian/dev/<hash of the absolute workload directory>/
    platform.db
    runtime.json
    collector-<run>.yaml
    collector-pending-<run>.json
    path            the absolute directory this state belongs to
```

ADR 0035 §12's actual *requirements* survive: isolation per checkout is the path
hash, removability is one `rm -rf` of a printed path, and visibility is restored
by `dev` printing the path on startup — which is strictly better than requiring
the developer to know the convention. What is traded away is `ls` finding it in
the project, and `dev` compensates by saying where it is every time.

`make local` and `trustvian-local` are **unchanged**: `.trustvian/` in the
working directory stays the default there. This is a decision for `dev` only,
recorded in the provisioning ADR.

### The consequence that has to be handled

`cmd/trustvian/platform_client.go:406` resolves discovery from
`./.trustvian/runtime.json`. With `dev`'s state elsewhere, `trustvian eval
compare` in that directory would stop finding the endpoint — a real regression
against task 062's "clients in this directory can omit `--api-url`".

Fix: the resolver gains a **second, documented location**, tried only when no
`--api-url` was given and the working directory has no discovery file:

```text
--api-url given      → that endpoint, always            (unchanged)
./.trustvian/        → that endpoint                    (unchanged)
~/.trustvian/dev/<hash of cwd>/  → that endpoint        (new)
neither              → the existing operational error, naming both
```

Additive, in the root module, and it does not touch ADR 0035 §10a: presence on
the command line still decides, and `--api-url ""` is still a usage error that
reads no file.

---

## 4. Identity

### Derivation

| Concept | Source | Fallback |
|---|---|---|
| Project | `git rev-parse --show-toplevel` basename | working directory basename; `--project` |
| Agent | **inherited `OTEL_SERVICE_NAME`**, else `--agent` | none — stop and name the flag |
| Candidate | `git:<short sha>`, `+dirty` when the worktree is dirty | `--candidate`; no git → stop and name the flag |
| Environment | `local` | `--environment` |
| Run | generated per invocation | — |

`git` is invoked as a subprocess, not as a Go dependency. Absent git, or a
directory that is not a repository, is reported and names the flag that fixes
it — never a fabricated commit.

### The dirty marker

`git status --porcelain` non-empty → the candidate id carries a `+dirty`
suffix and the printed banner says so. Evaluating uncommitted work is normal;
attributing it to a clean commit is not.

Two candidates are therefore distinguishable but a dirty worktree is *not*
uniquely identified — two different dirty states share one candidate id. That is
deliberate: a content hash of the worktree would change on every keystroke and
make every save look like a new candidate, which is the ephemeral-identity
failure the spec forbids.

### Keeping the Agent consistent with the actor the processor derives

This is the question that decides whether any of it works. The processor derives
`Actor.ID` from the resource attribute `service.name`. If the provisioned Agent
and that value differ, the hierarchy describes an agent no telemetry ever
mentions.

```text
inherited OTEL_SERVICE_NAME is set
    → adopt it as the Agent id. dev does NOT override it.
      The application's own declared identity wins, because "read what
      their instrumentation already emits" is the product principle.

not set, --agent given
    → provision that Agent, and export OTEL_SERVICE_NAME=<it> to the child,
      so the two cannot disagree.

neither
    → stop. The message names --agent.
```

So **yes, `dev` exports `OTEL_SERVICE_NAME`** — but only when the child did not
already declare one. Overriding a declared identity would silently relabel the
application's telemetry.

### The three variables that are not negotiable

Beyond the OTLP endpoint, the child needs:

```text
OTEL_RESOURCE_ATTRIBUTES  deployment.environment.name=<env ref>
                          APPENDED to any inherited value, never replacing it.
                          Without it every record is refused — finding 2 above.

OTEL_SEMCONV_STABILITY_OPT_IN=http
                          Without it targets arrive empty — finding 3 above.

OTEL_TRACES_EXPORTER=otlp, OTEL_METRICS_EXPORTER=none,
OTEL_LOGS_EXPORTER=none, OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
                          traces only, to the run's Collector
```

If the child already set `deployment.environment.name` to something else, `dev`
**stops** rather than appending a conflicting value or silently letting ingest
fail later: the run's environment and the telemetry's must agree, and the
developer is the one who can resolve which is right.

### Run identity and the no-ephemeral-values rule

The run id is generated per invocation — the spec's identity table says so — and
is `dev-<candidate>-<UTC RFC3339 compact>`. That is a timestamp, which is
ephemeral, and it is *correlation metadata*: `EvaluationRunID` must not become
fingerprint identity (ADR 0022), and it does not.

The scan test asserts no pid, port, timestamp or random token reaches
**candidate or agent** identity. It deliberately does not forbid them in the run
id, because a run per invocation cannot be deterministic and pretending
otherwise would collide two runs into one.

### Run lifecycle, in the prototype's order

The ordering is load-bearing and every step has a reason found the hard way:

```text
1. runtime up, discovery published
2. project → environment → agent → candidate, each get-then-create
      idempotent: a second run would get 409 already_exists and die
      environment before any run names it: a new environment is active on
      creation, which is the state a run requires
3. eval create, eval start
4. Collector starts  — AFTER the run is running: ingest is refused for a
                       pending run, and the sink reads its cursor at startup
5. child launches
6. child exits
      non-zero → stop Collector, eval fail --reason, report. NOT complete:
                 a workload that failed produced no verdict, and nothing
                 downstream may read it as evidence
      zero     → wait for evidence to land (the SDK flushes at exit, then
                 the Collector posts; the control plane's count is the only
                 authoritative signal), then stop the Collector, THEN
                 eval complete — ingest is refused once a run is terminal,
                 so a late span would fail the batch
7. runtime down, if dev started it
```

---

## 5. Instrumentation ownership

Python zero-code is deferred (spec open question 2), so `auto` resolves to
`existing` or stops. `python-zero-code` is **reserved and refused** with a
message saying it is not yet available, so the name cannot be reused for
something else later.

### The positive-evidence list for `existing`

Evaluated against a **snapshot of the inherited environment taken before `dev`
sets anything**, plus the command line. This is the subtlety that makes the
whole mode meaningful: `dev` sets `OTEL_*` itself, so evidence read afterwards
would be `dev` detecting its own configuration and concluding the child is
instrumented.

```text
Environment (inherited, pre-snapshot):
  OTEL_TRACES_EXPORTER                    set and non-empty
  OTEL_EXPORTER_OTLP_ENDPOINT             set and non-empty
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT      set and non-empty
  OTEL_SDK_DISABLED                       set — an explicit statement
                                          about the SDK either way
  OTEL_SERVICE_NAME                       set and non-empty
  OTEL_RESOURCE_ATTRIBUTES                set and non-empty

Command line:
  argv[0] basename is opentelemetry-instrument
  any argument begins -javaagent: and names an opentelemetry agent jar
  argv[0] basename is otel-cli

Explicit:
  --instrumentation existing
```

Not exhaustive, and the spec says so. That is precisely why `auto` fails closed
rather than falling through to injection.

### The stop message

```text
trustvian dev: cannot establish instrumentation ownership.

--instrumentation auto found no positive evidence that

    python agent.py

already initializes OpenTelemetry, and absence of evidence is not evidence
of absence: a program that initializes the SDK a moment after it starts
cannot be detected beforehand. Attaching a second instrumentation stack to
one that exists would report every action twice, which reads as an actor
behaving strangely.

So nothing was launched. Choose explicitly:

  --instrumentation existing   your agent already sends OpenTelemetry;
                               dev configures OTLP and injects nothing
  --instrumentation none       dev manages no instrumentation at all;
                               telemetry reaches the Collector another way

  --instrumentation python-zero-code
                               not available in this build (task 077 open
                               question 2); it will attach a managed Python
                               runtime after compatibility checks
```

### What `existing` means exactly

OTLP routing and the resource attributes from §4, and nothing else. No wrapper
in argv, no interpreter flag, no `PYTHONPATH` or `PYTHONSTARTUP`, no
`LD_PRELOAD`, no `JAVA_TOOL_OPTIONS`, no file written anywhere.

The test asserts this by diffing the child's environment against the parent's
and requiring the delta to be exactly the documented variable set — which is a
stronger statement than "we did not mean to inject anything".

---

## 6. Windows

### Recommendation: refuse, with a message

`windows/amd64` is in the release matrix, so `dev` compiles there and must
behave predictably. It cannot behave *correctly*: on Windows
`os.Process.Signal(os.Interrupt)` is unsupported, `SIGTERM` cannot be delivered
to another process, and there is no `Setpgid` — orderly child-tree shutdown
needs Job Objects and `GenerateConsoleCtrlEvent`, which is a different lifecycle
model.

Every line of the spec's child-process discipline table — signal forwarding, the
runtime outliving the child, nothing orphaned — would be partially true. The
spec says never a silent partial, so:

```text
trustvian dev: not supported on Windows.

dev supervises a child process and forwards SIGINT and SIGTERM to it, and
Windows has no equivalent delivery — a wrapper that could not stop what it
started would leave orphaned processes holding your ports.

On Windows, use WSL2, or run the parts by hand:
  docs/local-development.md § Running the parts separately
```

Exit `2` (usage). One test asserts the refusal, built for `GOOS=windows`. Proper
support is its own task with its own process-model design.

---

## 7. Slice plan, adjusted

One branch, one PR, one commit per slice.

| Slice | What | Files |
|---|---|---|
| **1** | the wrapper: argv after `--`, cwd/env/stdio, signals, exit code, process-group cleanup, Windows refusal | `cmd/trustvian/dev.go`, `dev_child.go`, `dev_child_unix.go`, `dev_child_windows.go`, `dev_test.go`, `main.go`, `docs/compatibility.md` |
| **2** | helper resolution, state dir, runtime + Collector composition, dynamic ports, two-step readiness, discovery, child launch with the OTLP set, shutdown order | `dev_helpers.go`, `dev_runtime.go`, `dev_collector.go` (+ template), `platform_client.go` (resolver fallback), tests |
| **3** | identity derivation and provisioning over `/v1`, run lifecycle including `eval fail` on a failed workload | `dev_identity.go`, `dev_provision.go`, `dev_git.go`, tests |
| **4** | modes `existing` / `none` / `auto`, `python-zero-code` reserved and refused, evidence from the pre-snapshot | `dev_instrumentation.go`, tests |
| **5** | end-to-end fixture test, byte-identical assertion, three ADRs, docs, CHANGELOG, delete these notes | `dev_e2e_test.go`, `docs/adr/0042–0044`, `docs/local-development.md`, `docs/platform-cli.md`, `docs/OPENTELEMETRY.md`, `docs/ROADMAP.md`, task index, `CHANGELOG.md` |

Slice 2 is the largest and is where the two-step readiness and the resolver
fallback land. If it grows past reviewable size I will split the Collector out
of it and report that rather than landing one unreviewable commit.

### Slice 5's end-to-end test, stated so it cannot be softened

Three requirements, each of which exists because something regressed silently
without it:

- It runs the **real** `trustvian-local` and `trustvian-collector`, not the
  fakes the unit tests use. The fakes prove dev's supervision; only the real
  binaries prove the composition.
- It proves a span exported **just before the workload exits** lands in the
  run. That is the case the whole shutdown ordering exists for — SDK flush,
  then Collector post, then the run going terminal — and the one a naive
  teardown loses without saying so.
- It asserts the **second** sequential run of one candidate reports **non-zero
  anomaly confidence**. That is the only assertion that fails if the engine
  store regresses to the in-memory default, which would quietly leave the two
  engine-evidence gates unable to fire.

### Deferred with python-zero-code, and why

The spec's tests and acceptance criteria are satisfied in these slices except:

- `python-zero-code` attaches only after its compatibility check passes —
  deferred with the mode.
- `auto` selecting a zero-code path on positive evidence — unreachable until the
  mode exists, so `auto` resolves to `existing` or stops, which the spec's open
  question 2 anticipates.
- "instrumentation failure in the selected mode degrades loudly" — partially
  testable now: `existing` and `none` cannot fail to attach because they attach
  nothing. The forced-incompatible-interpreter test arrives with the mode.

### Not required by 077, worth noting for the demo

`scripts/tv-dev.sh` has two capabilities `dev` will not have after slice 5:
`--expect-records` / `--expect-records-from` (waiting for a specific record
count) and `runtime up|down|url` (one control plane across many runs). The
second is what task 078's CI path needs and what `--api-url` on `dev` should
cover; the first is a test-harness concern that belongs to 078's runner, not to
`dev`. Neither blocks the demo's adoption, and I will say so in the PR rather
than growing `dev` to match a script it replaces.

---

## Nothing here requires a prohibited change

- **No engine change.** The engine is untouched.
- **No platform-aware core change.** Nothing in `internal/` or the root package
  learns what a run is.
- **No new third-party dependency.** `os/exec`, `os/signal`, `syscall`,
  `text/template`, `net`, `encoding/json`, `os`, `path/filepath` — all standard
  library. `git` is a subprocess, not an import.
- **No module boundary reversal.** The root CLI gains no platform import; the
  boundary check stays green.

One thing I am **not** doing without your word: adding `trustvian-local` and
`trustvian-collector` to the release archive. See §1.
