# QuickStart agent

The agent [the QuickStart](../../docs/quickstart.md) runs. It is a small Python
program that stands in for a support agent: it calls no model and no tool, and
emits the OpenTelemetry spans an instrumented agent would emit for one ticket.
Nothing here needs Go.

| File | What it is |
|---|---|
| [`agent.py`](agent.py) | The agent: standard library, `opentelemetry-sdk` and the OTLP/HTTP exporter |
| [`requirements.txt`](requirements.txt) | Every Python package it installs, pinned with hashes; generated from [`requirements.in`](requirements.in) by the command in its header |
| [`scenario.yaml`](scenario.yaml) | The QuickStart's scenario: reference mode against candidate mode, `runs: 3` |
| [`.gitignore`](.gitignore) | `.trustvian/`, `.venv/` and `__pycache__/`, so the candidate never reads `+dirty` |
| [`test_agent.py`](test_agent.py) | Its tests |

## What it emits

One trace per run:

```text
invoke_agent quickstart-agent          gen_ai.operation.name=invoke_agent
  chat stub-model-v1                   gen_ai.operation.name=chat, provider stub,
                                       1200 input and 300 output tokens
  execute_tool crm_lookup              → crm.localhost
  execute_tool knowledge_search        → kb.localhost
  …                                    one span per tool call
```

Every tool span carries `gen_ai.tool.name`, `gen_ai.agent.name`,
`http.request.method`, `server.address` and `http.response.status_code` `200`.
The model-call span carries `gen_ai.request.model`, `gen_ai.provider.name` and
the two `gen_ai.usage.*` counts. That is every attribute it sets: **no prompt,
completion, tool argument or tool result**, and every value is a fixed
identifier or a count.

Durations are fixed, not measured: 400 ms for the model call and 3 ms for each
tool call, laid out back to back and ending when the agent runs. So every run
falls in the same latency buckets.

## Two modes

`QUICKSTART_MODE` chooses the tool calls of a run:

| Mode | Tool calls |
|---|---|
| `reference` (default) | `crm_lookup`, `knowledge_search` ×2, `send_reply` |
| `candidate` | `crm_lookup`, `knowledge_search` ×4, `export_data`, `send_reply` |

The candidate differs in exactly two ways: it adds one tool, `export_data`
(an added behavior), and it calls `knowledge_search` twice as often (a
frequency change). Everything else, the model call and its tokens included, is
identical.

## Environment

| Variable | Meaning |
|---|---|
| `QUICKSTART_MODE` | `reference` or `candidate`; anything else exits `2` |
| `QUICKSTART_MODEL` | the model name the model-call span declares, default `stub-model-v1`. Nothing is called. 1 to 128 characters with no whitespace, as a scenario's model declaration |
| `OTEL_SERVICE_NAME` | the agent Trustvian reports on |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_RESOURCE_ATTRIBUTES`, … | read by the OpenTelemetry SDK, as `trustvian dev` sets them ([instrumentation ownership](../../docs/platform-cli.md#instrumentation-ownership)) |

It exits `0` when every span was delivered and `1` when one was not, so a run
with no receiver to reach fails rather than passing empty.

## Tests

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python -m unittest -v
```

They prove the two modes differ only as stated above, that a run is
deterministic, and that no attribute carries free text. CI runs them, and
`make quickstart-check` runs the QuickStart's own commands against this
directory.
