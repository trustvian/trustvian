# Platform CLI

The `trustvian` binary has two surfaces.

`analyze`, `baseline build` and `version` run the behavioral engine **in
process** against a file of events. They need no network and no server, and
nothing on this page changes them.

`project`, `agent`, `candidate` and `eval` drive a **local control plane** over
its `/v1` HTTP API. They are what this page is about: one request per command,
machine-readable output, and exit codes a CI job can branch on.

The CLI is a client. It computes no diff, no scorecard and no gate — those are
the control plane's, and the CLI reports what it returned. See
[ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md).

## Finding the control plane

Explicit `--api-url` is required when you are **not** using the local runtime.
When `make local` is running from the same working directory, platform commands
discover `.trustvian/runtime.json` automatically:

```text
--api-url given    → that endpoint, always
--api-url omitted  → ./.trustvian/runtime.json
```

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
trustvian project create --api-url <url> --id <id> --name <name>
trustvian project get    --api-url <url> --id <id>

trustvian agent create   --api-url <url> --id <id> --project-id <id> --name <name>
trustvian agent get      --api-url <url> --id <id>

trustvian candidate create --api-url <url> --id <id> --agent-id <id>
                           [--label] [--source-ref] [--artifact-digest]
                           [--model] [--toolset-digest] [--config-digest]
trustvian candidate get    --api-url <url> --id <id>

trustvian eval create       --api-url <url> --id <id> --candidate-id <id>
                            --environment <ref> --behavioral-profile <ref>
trustvian eval get          --api-url <url> --id <id>
trustvian eval start        --api-url <url> --id <id>
trustvian eval complete     --api-url <url> --id <id>
trustvian eval fail         --api-url <url> --id <id> [--reason <text>]
trustvian eval cancel       --api-url <url> --id <id>
trustvian eval progress     --api-url <url> --id <id>
trustvian eval ingest-state --api-url <url> --id <id>
trustvian eval ingest       --api-url <url> --id <id> --sequence <n>
                            --behavioral-profile <ref> --record <file>
trustvian eval compare      --api-url <url> --reference-run <id> --candidate-run <id>
                            --max-added-behaviors <n> --max-block-decisions <n>
                            --max-critical-risk-observations <n>
```

All of them accept `--json`.

Candidate metadata is descriptive only. The CLI runs no `git` command and
computes no digests: a value it derived would claim a provenance it cannot
actually vouch for.

There is no `list`, `search`, `update` or `delete`, because the API has no such
routes. A client-side list would have to invent ordering, paging and scoping
that nothing has decided yet.

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

## Exit codes

Exit codes are **scoped by command family**. This matters, so it is worth
reading once rather than assuming.

| Commands | 0 | 1 | 2 | 3 |
|---|---|---|---|---|
| `analyze`, `baseline`, `version` | success | command failed | top-level usage | — |
| `project`, `agent`, `candidate`, most of `eval` | success | *unused* | usage | API or network failure |
| `eval compare` | gate **PASS** | gate **FAIL** | usage | API or network failure |

**Exit 1 means gate failure only for `eval compare`.** It does not change what
`1` has always meant for `analyze` and `baseline`.

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
