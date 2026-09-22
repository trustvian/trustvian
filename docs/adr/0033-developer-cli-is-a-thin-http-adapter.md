# 0033 — The developer CLI is a thin HTTP adapter

**Status:** Accepted

## Context

[Task 060](../tasks/v1.0/060-developer-cli.md) adds control-plane commands to
the shipped `trustvian` binary, so an evaluation can be driven from a shell
script or a CI job.

Two things make this more delicate than it looks.

First, the CLI lives in the root module, `github.com/trustvian/trustvian`,
while the platform is a separate module, `trustvian-platform`, which already
depends on the root's public API. The obvious implementation — import the
platform and call `ControlPlane` directly — would reverse that edge.

Second, the CLI already has released exit-code semantics, and CI wants
different ones. The roadmap has carried this as an open conflict since the
milestone was planned.

## Decision

The developer CLI consumes the platform exclusively through the versioned `/v1`
HTTP API. It does not import platform implementation packages, persistence
adapters, or reproduce control-plane decisions. Existing engine CLI commands
retain their released behavior and exit-code semantics. New evaluation commands
have a command-scoped CI exit-code contract, with gate failure distinguishable
from invocation or operational failure.

### 1. HTTP, not a Go import

`trustvian-platform` depends on the root module's public API
([ADR 0022](0022-core-platform-boundary.md)). If the root module imported the
platform back, the two would be mutually dependent in everything but Go's
formal sense — and the shipped CLI, which is part of the root module's release
artifact, would carry the platform's implementation types, its SQLite driver,
and its schema into every binary.

The HTTP API is the boundary that already exists for exactly this. A client
that imports the server's domain is also no longer evidence that the wire
contract works; one that speaks JSON is.

### 2. SQLite is never touched directly

The platform database is an implementation detail of
[ADR 0030](0030-local-persistence-stores-authoritative-bounded-state.md), with
invariants — schema version, sequence monotonicity, bounded snapshots — that
live in `ControlPlane`, not in the file. A second writer reaching past those is
how a store acquires states its own code believes impossible.

There is no `--db` flag, and there will not be one.

### 3. The CLI recomputes nothing

Diff, scorecard and gate are already authoritative outputs of tasks 054, 055
and 056. A CLI that recomputed any of them would be a second implementation of
a security-relevant decision, and the interesting failure is not that it
disagrees loudly — it is that it agrees for a year and then diverges on an edge
case nobody tests, in the direction of a false PASS.

The CLI reads `gate.verdict` and nothing else to pick its exit code. Human
output prints the checks the server returned, which is rendering, not
evaluation.

It validates that verdict's *vocabulary*, never its arithmetic — see point 9.

### 4. IDs belong to the caller

[ADR 0025](0025-platform-domain-values-with-caller-owned-identity.md) put
identity in the caller's hands because an evaluation is usually named by
something that already exists — a CI run, a commit, a release candidate. A CLI
that generated IDs would produce values nothing else in the pipeline can
reconstruct.

### 5. Nothing is generated

No UUID dependency, no timestamp-derived identifiers, no random suffixes. A
`create` without `--id` is a usage error, not an invitation to invent one. This
also keeps the CLI reproducible: the same script run twice addresses the same
entities.

Lifecycle timestamps are likewise the server's; the CLI sends no clock value.

### 6. `analyze`, `baseline` and `version` are unchanged

They are released operational surface with a documented contract, and they
serve a different purpose: running the engine locally against a file of events,
with no platform and no network. Rewriting them to go through HTTP would
replace a working offline tool with a service dependency and break every
existing invocation.

They keep talking to the engine in process. The binary now has two surfaces
that look different because they *are* different.

### 7. New commands rather than repurposed ones

`trustvian analyze` could plausibly have grown an `--api-url`. It would also
have acquired two incompatible meanings, two output shapes and two exit-code
contracts under one name — and no way for a script to say which it wanted.

Additive command families cost nothing to ignore.

### 8. The exit-code conflict is resolved by scoping, not by renumbering

Legacy: `0` success, `1` the command failed, `2` the invocation was wrong. CI
wants `1` to mean *gate failed*. Both cannot be globally true.

Renumbering the legacy codes would break released automation. So the new
command families simply do not use `1`:

```text
project / agent / candidate / eval (all but compare)
  0 success   2 usage   3 operational
```

That leaves `1` unclaimed on exactly the command that needs it.

### 9. `eval compare` gives `1` a unique, command-scoped meaning

```text
0  HTTP succeeded and gate.verdict is exactly "pass"
1  HTTP succeeded and gate.verdict is exactly "fail"
2  usage
3  operational, including any other verdict
```

Scoped to one command, this adds a meaning without changing one. A reader of
`docs/compatibility.md` is told plainly that `1` means gate failure *for
`eval compare` only*.

**`pass` and `fail` are a closed vocabulary, and everything else is
operational.** An earlier draft mapped pass to `0` and *anything else* to `1`,
which quietly reported `unknown`, `pending`, `PASS` with the wrong case, a
typo, a verdict from a newer server, and a missing field decoding to `""` as
policy failures. None of those is a gate failure; they are responses this
client cannot interpret. Failing a build for one states something that did not
happen, and a team that meets it a few times learns to treat exit `1` as noise
— which costs them the one signal the code exists to carry.

Only the word is checked. A response reporting seven added behaviors under a
maximum of zero with a verdict of `pass` still exits `0`: `pass` is an
authoritative answer, and second-guessing it here is the second gate
implementation point 3 rules out.

The classification happens **before** anything is written. An uninterpretable
response is not publishable CI evidence, so it must not reach stdout and then
be retracted by an exit code — a pipeline redirecting stdout to a file keeps
the artifact and loses the retraction.

### 10. An API error must never look like a gate failure

A 409, a 404, a 500, a timeout, a refused redirect, a malformed body and an
oversized body are all `3`.

This is the load-bearing half. CI scripts are written as "non-zero means the
gate failed"; under that habit, a conflated exit code turns a broken network
into a reported policy violation, or — worse, in the other direction — teaches
a team to ignore the code that actually means their agent regressed.

### 11. Structured output is the API's JSON, not a second schema

`--json` writes the server's successful response body through unchanged, after
checking that it is syntactically JSON at all.

Every `/v1` operation this CLI calls answers with a JSON body, so a `2xx`
carrying anything else is a server the client does not understand. Without that
check the two output modes disagreed: human mode failed because its renderer
had to decode the body, while `--json` copied arbitrary bytes to stdout and
exited `0`. A machine-readable mode that reports success for a malformed body
is worse than one with no validation at all, because the exit code asserts the
output is usable.

The check is syntax only — `json.Valid`, plus an explicit empty-body case — so
it touches no fields and an additive server change still passes through.

Decoding into a CLI struct and re-marshalling would create a second field
namespace with its own versioning problem, and would silently drop any field a
newer server added — exactly the compatibility property `/v1` promises. The
CLI's own DTOs exist only for human rendering.

### 12. Additive API fields must be tolerated

Response decoding never uses `DisallowUnknownFields`. The compatibility
contract says a client must tolerate fields it does not know; a CLI that
rejected them would make every additive server change a breaking one, which
inverts the contract.

### 13. `eval ingest` transports the record without decoding it

The record file is embedded as raw JSON. It is never unmarshalled into
`trustvian.DecisionRecord` and marshalled again.

`DecisionRecord` is an additive public contract
([task 050](../tasks/v1.0/050-public-serializable-decision-record.md)), and
[task 058](../tasks/v1.0/058-local-control-plane-api-and-ingest.md)
deliberately decodes it leniently server-side so a newer producer is not
rejected by an older server. A CLI that round-tripped through its own current
struct would strip the newer field *before transmission* — defeating that
tolerance from the client side, silently, with no error anywhere.

### 14. No realtime or watch command here

[Task 059](../tasks/v1.0/059-realtime-infrastructure.md) built the SSE
infrastructure, and task 061 is the realtime terminal UX. Task 060 is the scripting and CI surface: one-shot
operations, machine-readable output, documented exit codes.

A `watch` here would also need a different timeout model — the 30s one-shot
request bound is meaningless for a long-lived stream — and would start
answering interaction questions that belong with the milestone that owns them.

### 15. No listener is started

No `http.ListenAndServe`, no `serve` command. A CLI that binds a port is a
server, with a lifecycle, a shutdown story and an exposure surface. Tests use
`httptest.Server` as the endpoint.

### 16. Task 062 owns integrated local startup

Running the engine, the control plane and the CLI together from one command is
a real requirement, and a separate one. It needs process lifecycle, storage
location, port selection and shutdown ordering — none of which Task 060 has to
decide in order to be useful.

### 17. `--api-url` is explicit rather than defaulted

Defaulting to a local port would freeze an address before the thing that binds
it exists. Task 062 can add a default once it owns startup; adding one later is
easy, and changing one that scripts already rely on is not.

No environment variable either, for the same reason — that is a second
operational surface, and it should arrive with the server lifecycle it
configures.

### 18. No authentication or TLS policy is invented

Task 070 owns authentication and security hardening. A `--token` flag now would
freeze a credential shape before there is anything to authenticate against, and
a half-designed auth surface is harder to remove than to add.

The CLI sends no auth header and claims no transport guarantee. It does refuse
credentials embedded in `--api-url` — not as authentication, but because a URL
is the wrong place for a secret and accepting one would put it in shell
history, process listings and logs.

### 19. Platform commands take no positional arguments

Every leaf receives its input through flags, so anything left after parsing is
a mistake: a lost dash, a stray filename, a shell-quoting slip. Ignoring it
sent the request anyway.

On `eval compare` that meant a mistyped invocation still produced a real PASS
or FAIL for CI to act on — an exit code carrying a policy meaning, derived from
a command the user did not write. The rejection is a usage error before the API
URL is built and before any request is sent.

Parsing and the check are one function, because a separate check is the kind a
new command forgets: it compiles, it works for every correct invocation, and it
fails only on a typo.

Legacy `analyze`, `baseline` and `version` keep their own parsing untouched —
they take a file operand, and their behavior is released surface.

## Alternatives considered

**Import `trustvian-platform` and call `ControlPlane` directly.** Simplest to
write and the fastest to run — no serialization, no network. Rejected because
it reverses the module edge, pulls the SQLite driver into the shipped root
binary, and removes the only end-to-end evidence that the `/v1` contract is
usable by a real client.

**A shared client package used by both the CLI and future consumers.** Premature:
there is one consumer. It would also be a third versioned surface between the
API and its callers.

**Global exit-code renumbering, so all commands mean the same thing.** Cleaner
to document, and a breaking change to released automation for a cosmetic gain.
Compatibility beats symmetry.

**A generic retry layer in the client.** Rejected because retry safety is
per-operation: a GET is safe, a lifecycle POST is not, and ingest already has
explicit sequence and replay semantics that a generic layer would not know
about.

**A third-party CLI framework (Cobra, urfave/cli).** The existing CLI uses
`flag`; adding a framework would be a new dependency in the root module for
ergonomics alone, against the confinement rule in `.claude/rules/go.md`.

## Consequences

The CLI pays serialization and a network hop for every operation, and cannot be
used without a running control plane. Both are accepted: it is a client.

New platform commands cannot report *why* an operation failed with the
precision an in-process caller could — they get a code and a message. That is
what the error envelope is for.

`1` is now overloaded across the binary: command failure for legacy commands,
gate failure for `eval compare`, unused elsewhere. This is documented
explicitly rather than smoothed over, because a reader who assumes uniformity
will be wrong in a way that matters.

When task 062 adds integrated startup, `--api-url` can gain a default without
breaking any script that passes it. When task 070 adds authentication, the
client gains a header; nothing about the boundary changes.
