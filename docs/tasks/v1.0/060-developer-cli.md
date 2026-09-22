# 060 — Developer CLI

Status: implemented
Depends on: [058](058-local-control-plane-api-and-ingest.md),
[059](059-realtime-infrastructure.md)

## Objective

A scriptable, developer-facing CLI over the local control-plane `/v1` API, so
an evaluation can be driven from a shell script or a CI job without writing Go.

The CLI is an **adapter**. It parses flags, builds one HTTP request, renders or
forwards the response, and chooses a documented exit code. It is not a second
control plane, a second gate evaluator, a second behavioral diff, a SQLite
client, or a local server.

## Existing CLI Compatibility

`trustvian analyze`, `trustvian baseline build` and `trustvian version` are
released operational surface. Task 060 changes none of them: not their flags
(`--config`, `--anomaly-config`, `--storage-config`), not their output, not
their exit codes.

They also keep talking to the engine **in process**. Rewriting them to call the
platform would replace a working local tool with a network dependency, for no
gain — they serve a different purpose than the control-plane commands and are
allowed to look different.

## Architecture

```text
trustvian CLI
     │
     │ HTTP /v1        (the only edge)
     ▼
Control-plane API  (platform/httpapi)
     │
     ▼
ControlPlane service
     ├─ persistence
     ├─ evaluation
     ├─ diff / scorecard / gate
     └─ realtime
```

Two CLI surfaces, deliberately different, in one binary:

```text
analyze / baseline / version   →  root Engine, in process
project / agent / candidate    →  HTTP /v1
eval                           →  HTTP /v1
```

## Control-Plane Client Boundary

The root module is `github.com/trustvian/trustvian`. The platform is a separate
module, `trustvian-platform`, which already depends on the root's public API.

The root module must **not** depend back on `trustvian-platform`. That would
turn a deliberate one-way relationship into a cycle-shaped graph and couple the
shipped CLI to platform implementation types — a client that imports the
server's domain is no longer testing that its wire contract works.

So the CLI uses `net/http`, `encoding/json` and `net/url`, with its own
client-side DTOs for the fields it renders. `go.mod` gains nothing. An
architecture test enforces both halves.

## Positional Arguments

Every platform leaf takes its input through flags and accepts **zero**
positional arguments. Anything left after parsing is a usage error (`2`),
diagnosed before the API URL is built and before any request is sent.

A leftover argument is always a mistake — a lost dash, a stray filename, a
shell-quoting slip. Ignoring it sent the request anyway, which on `eval
compare` meant an invocation the user got wrong still produced a real PASS or
FAIL that CI acted on. A typo must never be mistaken for a gate result.

Parsing and the check are one operation (`parseFlags`), because a separate
check is the kind a new command forgets: it compiles, it works for every
correct invocation, and it fails only on a typo.

A bare trailing `--` is *not* a positional argument — it is the POSIX
end-of-options marker, which `flag` consumes — so `… --id x --` stays valid.
`-- foo` and a bare `-` do reach the argument list and are rejected.

Legacy `analyze`, `baseline` and `version` are unchanged: they take a file
operand, and their parsing is released compatibility surface.

## Command Surface

```text
trustvian project create --id --name
trustvian project get    --id

trustvian agent create   --id --project-id --name
trustvian agent get      --id

trustvian candidate create --id --agent-id
                           [--label --source-ref --artifact-digest
                            --model --toolset-digest --config-digest]
trustvian candidate get    --id

trustvian eval create    --id --candidate-id --environment --behavioral-profile
trustvian eval get       --id
trustvian eval start     --id
trustvian eval complete  --id
trustvian eval fail      --id [--reason]
trustvian eval cancel    --id
trustvian eval progress  --id
trustvian eval ingest-state --id
trustvian eval ingest    --id --sequence --behavioral-profile --record
trustvian eval compare   --reference-run --candidate-run
                         --max-added-behaviors
                         --max-block-decisions
                         --max-critical-risk-observations
```

Every one takes `--api-url` and `--json`.

No `list`, `search`, `update` or `delete`: task 058 owns no such routes, and
faking them client-side would invent semantics (ordering, paging, scoping) that
the API has not decided. No `watch`, `tui`, `serve`, `run-local` or `promote` —
those are tasks 061, 062 and 066.

## API URL

`--api-url` is **required** on every platform command. Task 062 owns integrated
local startup; choosing a default port here would freeze an address before the
thing that binds it exists, and a default is far harder to change than to add.

No environment variable either. That is a second operational surface, and it
should arrive with the server lifecycle it configures.

Validated before any request: scheme must be `http` or `https`, host must be
non-empty, and userinfo, query and fragment are rejected rather than
normalized. Credentials in a URL are refused outright — and the diagnostic
never echoes them back.

## IDs

Caller-owned, always — [ADR 0025](../../adr/0025-platform-domain-values-with-caller-owned-identity.md)
is binding. No UUID dependency, no timestamp-derived IDs, no random suffixes.
Every create command requires an explicit `--id`.

IDs go into URL paths through `url.PathEscape`, never string concatenation, so
a caller-supplied identifier cannot change the route's shape.

## JSON / Human Output

Every `/v1` operation the CLI calls answers with a JSON body, so **a successful
status carrying anything else is an operational failure** (`3`) — in both
output modes. A `2xx` with a malformed body, or an empty one, is a server the
client does not understand, not a success with unusual content.

That check is syntax only (`json.Valid`, plus an explicit empty-body case) and
runs before either mode renders. Without it the two disagreed: human mode
failed because its renderer had to decode the body, while `--json` copied the
bytes through and exited `0`. A machine-readable mode that reports success for
arbitrary bytes is worse than one with no validation, because the exit code
claims the output is usable.

`--json` then writes **the server's own successful response body** to stdout,
byte for byte apart from trailing-newline normalization. Not a re-marshalled
subset: decoding into a smaller struct and re-encoding would silently drop any
field a newer server added, which is exactly the compatibility property the
`/v1` contract promises. Validating syntax does not touch fields, so an
additive server change still passes through untouched.

Human output is observational and renders only fields it knows. Response
decoding never uses `DisallowUnknownFields` — the contract says clients
tolerate additive fields.

Errors never go to stdout. On failure, `--json` writes the server's error
envelope to stderr verbatim; human mode writes `code: message`.

## Exit Codes

Legacy commands are untouched:

```text
analyze / baseline / version success        → 0
analyze / baseline runtime or usage failure → 1
top-level usage (no args, unknown command)  → 2
```

New platform command families:

```text
0  operation succeeded
2  local usage / flag / argument error
3  API, network, timeout, redirect, or server error
```

`1` is unused there, which is what makes it free to mean something specific on
one command:

```text
eval compare
  0  HTTP succeeded and gate.verdict is exactly "pass"
  1  HTTP succeeded and gate.verdict is exactly "fail"
  2  usage
  3  API / network / runtime / server error, and any other verdict
```

This is **command-scoped**. A 409, a 404, a timeout, a redirect and a malformed
response are all `3`, never `1`. A CI job that treats any non-zero as "gate
failed" would otherwise report a broken network as a policy violation, which is
the failure mode this distinction exists to prevent.

**Exit 1 requires the explicit verdict `fail`.** Only `pass` and `fail` are a
recognized vocabulary; `unknown`, `pending`, `PASS` with the wrong case, a
typo, a verdict from a newer server, and a missing field that decodes to `""`
are all responses the CLI cannot interpret — operational failure, not policy
failure. Defaulting anything-but-pass to `fail` would fail a build for a
reason that never happened, and would teach a team that exit `1` is noise.

Only the vocabulary is checked, never the arithmetic: a response reporting
seven added behaviors under a maximum of zero with a verdict of `pass` is
still `0`, because `pass` is an authoritative answer.

The verdict is classified **before** anything is written. An uninterpretable
response is not publishable CI evidence, so it must not reach stdout and then
be retracted by the exit code — a pipeline redirecting stdout to a file would
keep the artifact and lose the retraction. For an unsupported verdict, stdout
is empty and the diagnostic goes to stderr, in both human and `--json` mode.

## Evaluation Ingest

```text
POST /v1/evaluation-runs/{id}/records
{"version":"1","sequence":"7","behavioral_profile":"…","record":{…}}
```

The record file is embedded as **raw JSON** (`json.RawMessage`), validated only
as being well-formed. It is never decoded into `trustvian.DecisionRecord` and
re-encoded: `DecisionRecord` is additive, task 058 deliberately made the server
tolerant of unknown record fields, and an older CLI round-tripping through its
current struct would strip a newer producer's field before the server ever saw
it. A regression test asserts a synthetic unknown field survives to the wire.

`--sequence` is required, canonical unsigned decimal, ≥ 1, parsed as exact
`uint64`. `MaxUint64` is syntactically valid; whether it is *acceptable* is the
server's call against the run cursor. The CLI keeps no sequence cache, state
file or retry database — re-running the command re-sends the same explicit
sequence, and task 058's idempotency contract handles it.

## Evaluation Comparison

All three gate limits are **required**. Omitted is not zero: zero is the
strictest possible limit, and defaulting to it would silently impose maximum
strictness on a caller who forgot a flag. Parsing distinguishes absent from
`"0"`, and the request sends canonical decimal strings.

No default limits, no config-file lookup, no inferred permissive values.

The CLI reads exactly one field to choose its exit code: `gate.verdict`. It
does not count added behaviors, block decisions or critical observations, and
does not compare anything against a threshold — all of that is already
authoritative in the response. Human mode prints the checks the server
returned; that is presentation, not evaluation.

## Resource Bounds

```text
request timeout    30s, fixed named constant (one-shot; not for SSE)
request body       ≤ 256 KiB, checked locally before sending
response body      ≤ 4 MiB, read as limit+1 and failed closed
redirects          refused
retries            none
background work    none after the command returns
```

The request cap matches task 058's server-side limit, so the CLI does not
knowingly build a request the server must reject. The response cap is sized for
a comparison carrying the bounded union of up to 1024 behavioral deltas plus
bounded text.

No automatic retries: a GET may be safe but a lifecycle POST is not, and ingest
already has explicit sequence semantics. A generic retry layer would need
per-operation knowledge it does not have.

## HTTP Semantics

`Content-Type: application/json` on requests with a body, `Accept:
application/json` on all of them. No auth headers — task 070 owns
authentication, and inventing a header now would freeze a speculative contract.

Redirects are refused rather than followed. A control-plane mutation addressed
to one host must not silently become a mutation against another.

## Security

1. API URL is explicit; no default address or port is chosen.
2. Credentials embedded in the URL are rejected, and never echoed.
3. No authentication is claimed or implemented.
4. Redirects are not followed.
5. Request bodies are bounded before sending.
6. Response bodies are bounded while reading.
7. The request timeout is finite.
8. IDs are never generated.
9. Unknown `DecisionRecord` fields survive ingest unmodified.
10. Unknown API response fields are tolerated and preserved.
11. No direct database access of any kind.
12. No token store, keyring or credential file.
13. No listener is bound.
14. No background retry or goroutine outlives a command.
15. API and runtime errors never masquerade as gate failure.

## Tests

- Every leaf command against an `httptest.Server`: method, exact route, request
  body, output, exit code.
- Legacy regression: `analyze`, `baseline`, `version` exit codes pinned.
- `eval compare` exit matrix, including that no other condition yields `1`.
- Gate limits: omitted (each of three), explicit zero, `MaxUint64`, negative,
  overflow.
- `DecisionRecord` unknown field survives ingest.
- Unknown API response fields at top level, nested run, nested gate.
- Response at the limit accepted, one byte over refused.
- Ingest request within and over 256 KiB, counting envelope overhead.
- API URL acceptance and rejection table.
- Redirect refused; second server receives nothing.
- Timeout returns `3` without hanging.
- stdout/stderr separation per outcome class.
- Architecture guard over CLI sources and root `go.mod`.

## Mutation Tests

Legacy runtime error remapped to `3`; gate FAIL mapped to `0`; API 409 mapped
to `1`; an omitted gate limit defaulted to zero; ingest decoded into
`DecisionRecord`; unknown response field rejected; unknown field stripped from
`--json`; redirect allowed; response bound removed; oversized ingest allowed
through; unescaped ID concatenated into a route; an ID generated when omitted;
gate recomputed locally; gate FAIL evidence written to stderr; a direct
platform-module import.

## Documentation

`docs/platform-cli.md` (new), plus `docs/compatibility.md`,
`docs/ARCHITECTURE.md`, `docs/SECURITY.md`, `docs/ROADMAP.md`, `CHANGELOG.md`
and the ADR index.

## Acceptance Criteria

1. Legacy commands byte-identical in behavior and exit code.
2. No `trustvian-platform` import in the root module; `go.mod` unchanged.
3. No SQLite, `database/sql`, or store access in CLI code.
4. No diff, scorecard or gate computation in CLI code.
5. `eval compare` returns `1` only for a successful response whose verdict is
   fail.
6. Unknown record fields preserved; unknown response fields tolerated.
7. All three gate limits required; explicit zero preserved.
8. Request, response and time all bounded; redirects refused.
9. No new dependency.

## Non-Goals

No realtime/watch command (task 061), no listener or integrated startup (task
062), no environment model (065), no promotion (066), no authentication (070),
no list/search routes, no TUI or WebUI, no core runtime change.
