"""A model-free, deterministic stand-in agent for the Trustvian QuickStart.

It calls no model and no tool. It emits the OpenTelemetry spans an instrumented
support agent would emit for one ticket, and exits:

    invoke_agent quickstart-agent          the run, parent of everything below
      chat <model>                         one model call, with fixed token counts
      execute_tool <tool>                  one span per tool call, in plan order

Two modes, chosen by QUICKSTART_MODE. The candidate differs from the reference
in exactly two ways, so a comparison shows both kinds of change:

    reference   crm_lookup, knowledge_search x2, send_reply
    candidate   crm_lookup, knowledge_search x4, export_data, send_reply

  - it adds one tool, export_data: an added behavior;
  - it calls knowledge_search twice as often: a frequency change.

Content-free: no attribute carries a prompt, a completion, a tool argument or
a tool result. Every attribute value is a fixed identifier or a count.

Deterministic: the spans, their attributes and their durations are the same on
every run. Durations are fixed, not measured, so latency buckets do not move
between runs: each tool call lasts 3 ms and the model call 400 ms, laid out back
to back and ending when the agent runs.

Environment:

    QUICKSTART_MODE      reference (default) or candidate
    QUICKSTART_MODEL     the model name the model-call span declares
                         (default stub-model-v1); nothing is called
    OTEL_SERVICE_NAME    the agent Trustvian reports on
    OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, OTEL_EXPORTER_OTLP_ENDPOINT,
    OTEL_RESOURCE_ATTRIBUTES and the other OTLP variables, read by the
    OpenTelemetry SDK exactly as `trustvian dev` sets them

It exits 0 when every span was delivered, 1 when one was not, and 2 when the
environment is invalid.
"""

import os
import re
import sys
import time

from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import (
    SimpleSpanProcessor,
    SpanExporter,
    SpanExportResult,
)

AGENT_NAME = "quickstart-agent"
PROVIDER = "stub"
DEFAULT_MODEL = "stub-model-v1"

# Fixed token counts on the model-call span, so the report's tokens section
# has evidence. They are the same in both modes.
INPUT_TOKENS = 1200
OUTPUT_TOKENS = 300

TOOL_MS = 3
MODEL_MS = 400

# Every tool and the host its call reaches.
TARGETS = {
    "crm_lookup": "crm.localhost",
    "knowledge_search": "kb.localhost",
    "export_data": "export.localhost",
    "send_reply": "mail.localhost",
}

# The tool calls of one run, in order, per mode.
PLANS = {
    "reference": [
        "crm_lookup",
        "knowledge_search",
        "knowledge_search",
        "send_reply",
    ],
    "candidate": [
        "crm_lookup",
        "knowledge_search",
        "knowledge_search",
        "knowledge_search",
        "knowledge_search",
        "export_data",
        "send_reply",
    ],
}

# A model name is an identifier, never text: the format Trustvian's scenario
# declarations accept (1 to 128 characters, no whitespace).
MODEL_NAME = re.compile(r"^\S{1,128}$")

MS = 1_000_000


def emit(tracer, mode, model, end_ns):
    """Emit one run's spans through tracer, the last one ending at end_ns."""
    plan = PLANS[mode]
    total_ms = MODEL_MS + TOOL_MS * len(plan)
    start = end_ns - total_ms * MS

    root = tracer.start_span(
        f"invoke_agent {AGENT_NAME}",
        kind=trace.SpanKind.INTERNAL,
        start_time=start,
        attributes={
            "gen_ai.operation.name": "invoke_agent",
            "gen_ai.agent.name": AGENT_NAME,
        },
    )
    parent = trace.set_span_in_context(root)

    at = start
    chat = tracer.start_span(
        f"chat {model}",
        context=parent,
        kind=trace.SpanKind.CLIENT,
        start_time=at,
        attributes={
            "gen_ai.operation.name": "chat",
            "gen_ai.provider.name": PROVIDER,
            "gen_ai.request.model": model,
            "gen_ai.agent.name": AGENT_NAME,
            "gen_ai.usage.input_tokens": INPUT_TOKENS,
            "gen_ai.usage.output_tokens": OUTPUT_TOKENS,
        },
    )
    at += MODEL_MS * MS
    chat.end(end_time=at)

    for tool in plan:
        span = tracer.start_span(
            f"execute_tool {tool}",
            context=parent,
            kind=trace.SpanKind.CLIENT,
            start_time=at,
            attributes={
                "gen_ai.operation.name": "execute_tool",
                "gen_ai.tool.name": tool,
                "gen_ai.agent.name": AGENT_NAME,
                "http.request.method": "POST",
                "server.address": TARGETS[tool],
                "http.response.status_code": 200,
            },
        )
        at += TOOL_MS * MS
        span.end(end_time=at)

    root.end(end_time=at)


class CountingExporter(SpanExporter):
    """Counts the spans its exporter accepted.

    The SDK logs a failed export and carries on, so a run with no receiver to
    reach would otherwise exit 0 having delivered nothing.
    """

    def __init__(self, exporter):
        self._exporter = exporter
        self.delivered = 0
        self.failed = 0

    def export(self, spans):
        result = self._exporter.export(spans)
        if result == SpanExportResult.SUCCESS:
            self.delivered += len(spans)
        else:
            self.failed += len(spans)
        return result

    def shutdown(self):
        self._exporter.shutdown()

    def force_flush(self, timeout_millis=30000):
        return self._exporter.force_flush(timeout_millis)


def main():
    mode = os.environ.get("QUICKSTART_MODE", "reference")
    if mode not in PLANS:
        print(f"agent: QUICKSTART_MODE must be reference or candidate, got {mode!r}", file=sys.stderr)
        return 2
    model = os.environ.get("QUICKSTART_MODEL", DEFAULT_MODEL)
    if not MODEL_NAME.match(model):
        print("agent: QUICKSTART_MODEL must be 1 to 128 characters with no whitespace", file=sys.stderr)
        return 2

    # Imported here so the tests, which bring their own exporter, need no
    # OTLP receiver and no network.
    from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

    exporter = CountingExporter(OTLPSpanExporter())
    provider = TracerProvider(resource=Resource.create())
    provider.add_span_processor(SimpleSpanProcessor(exporter))

    emit(provider.get_tracer("trustvian-quickstart-agent"), mode, model, time.time_ns())
    provider.shutdown()

    emitted = len(PLANS[mode]) + 2
    if exporter.delivered != emitted:
        print(f"agent: {exporter.delivered} of {emitted} spans delivered", file=sys.stderr)
        return 1
    print(f"agent: {mode} run complete, {emitted} spans delivered")
    return 0


if __name__ == "__main__":
    sys.exit(main())
