"""Tests for the QuickStart agent: the two modes differ only as documented, and
no attribute carries free text.

Run from this directory, with the requirements installed:

    python -m unittest -v
"""

import collections
import re
import unittest
import unittest.mock

from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

import agent

END_NS = 1_760_000_000_000_000_000

# Every attribute key the agent may set. A new key is a deliberate change to
# what the agent emits, and to this list.
ALLOWED_KEYS = {
    "gen_ai.operation.name",
    "gen_ai.agent.name",
    "gen_ai.provider.name",
    "gen_ai.request.model",
    "gen_ai.tool.name",
    "gen_ai.usage.input_tokens",
    "gen_ai.usage.output_tokens",
    "http.request.method",
    "http.response.status_code",
    "server.address",
}

# The content attributes Trustvian refuses to read (internal/semconv/content.go).
CONTENT_KEYS = {
    "gen_ai.input.messages",
    "gen_ai.output.messages",
    "gen_ai.tool.call.arguments",
    "gen_ai.tool.call.result",
    "gen_ai.tool.definitions",
    "gen_ai.system_instructions",
    "input.value",
    "output.value",
    "llm.input_messages",
    "llm.output_messages",
    "tool.parameters",
    "metadata",
}

# An identifier: no whitespace, no sentence. Free text cannot match it.
IDENTIFIER = re.compile(r"^[a-z0-9][a-z0-9._:/-]{0,63}$", re.IGNORECASE)


def run(mode, model=agent.DEFAULT_MODEL):
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    agent.emit(provider.get_tracer("test"), mode, model, END_NS)
    provider.shutdown()
    return exporter.get_finished_spans()


def tool_calls(spans):
    return collections.Counter(
        s.attributes["gen_ai.tool.name"]
        for s in spans
        if s.attributes.get("gen_ai.operation.name") == "execute_tool"
    )


def non_tool(spans):
    return sorted(
        (s.name, tuple(sorted(s.attributes.items())))
        for s in spans
        if s.attributes.get("gen_ai.operation.name") != "execute_tool"
    )


class ModesTest(unittest.TestCase):
    def test_the_candidate_adds_one_tool_and_doubles_one(self):
        reference = tool_calls(run("reference"))
        candidate = tool_calls(run("candidate"))

        self.assertEqual(set(candidate) - set(reference), {"export_data"})
        self.assertEqual(candidate["export_data"], 1)
        self.assertEqual(set(reference) - set(candidate), set())

        changed = {t for t in reference if candidate[t] != reference[t]}
        self.assertEqual(changed, {"knowledge_search"})
        self.assertEqual(candidate["knowledge_search"], 2 * reference["knowledge_search"])

    def test_everything_else_is_identical(self):
        self.assertEqual(non_tool(run("reference")), non_tool(run("candidate")))

        def tool_shapes(spans):
            return {
                (s.name, tuple(sorted(s.attributes.items())))
                for s in spans
                if s.attributes.get("gen_ai.operation.name") == "execute_tool"
            }

        reference = tool_shapes(run("reference"))
        candidate = tool_shapes(run("candidate"))
        self.assertEqual(len(candidate - reference), 1)
        self.assertEqual(reference - candidate, set())

    def test_one_model_call_with_fixed_token_counts(self):
        for mode in agent.PLANS:
            with self.subTest(mode=mode):
                chats = [s for s in run(mode) if s.attributes.get("gen_ai.operation.name") == "chat"]
                self.assertEqual(len(chats), 1)
                attrs = chats[0].attributes
                self.assertEqual(attrs["gen_ai.request.model"], agent.DEFAULT_MODEL)
                self.assertEqual(attrs["gen_ai.usage.input_tokens"], agent.INPUT_TOKENS)
                self.assertEqual(attrs["gen_ai.usage.output_tokens"], agent.OUTPUT_TOKENS)

    def test_the_model_name_is_the_declared_one(self):
        chats = [s for s in run("reference", "stub-model-v2") if s.name.startswith("chat ")]
        self.assertEqual(chats[0].attributes["gen_ai.request.model"], "stub-model-v2")

    def test_runs_are_deterministic(self):
        for mode in agent.PLANS:
            with self.subTest(mode=mode):
                first, second = run(mode), run(mode)
                shape = lambda spans: [
                    (s.name, s.kind, s.start_time, s.end_time, tuple(sorted(s.attributes.items())))
                    for s in spans
                ]
                self.assertEqual(shape(first), shape(second))

    def test_one_trace_under_one_root(self):
        for mode in agent.PLANS:
            with self.subTest(mode=mode):
                spans = run(mode)
                self.assertEqual(len({s.context.trace_id for s in spans}), 1)
                roots = [s for s in spans if s.parent is None]
                self.assertEqual([r.name for r in roots], ["invoke_agent quickstart-agent"])
                self.assertEqual(len(spans), len(agent.PLANS[mode]) + 2)
                self.assertEqual(max(s.end_time for s in spans), END_NS)


class ContentFreeTest(unittest.TestCase):
    def test_no_attribute_carries_free_text(self):
        for mode in agent.PLANS:
            for span in run(mode):
                for key, value in span.attributes.items():
                    with self.subTest(mode=mode, span=span.name, key=key):
                        self.assertIn(key, ALLOWED_KEYS)
                        self.assertNotIn(key, CONTENT_KEYS)
                        if isinstance(value, str):
                            self.assertRegex(value, IDENTIFIER)
                        else:
                            self.assertIsInstance(value, int)

    def test_no_span_records_events_or_links(self):
        for mode in agent.PLANS:
            for span in run(mode):
                with self.subTest(mode=mode, span=span.name):
                    self.assertEqual(len(span.events), 0)
                    self.assertEqual(len(span.links), 0)


class EnvironmentTest(unittest.TestCase):
    def test_an_unknown_mode_is_refused(self):
        with unittest.mock.patch.dict("os.environ", {"QUICKSTART_MODE": "other"}):
            self.assertEqual(agent.main(), 2)

    def test_a_model_name_with_whitespace_is_refused(self):
        with unittest.mock.patch.dict(
            "os.environ", {"QUICKSTART_MODE": "reference", "QUICKSTART_MODEL": "two words"}
        ):
            self.assertEqual(agent.main(), 2)


if __name__ == "__main__":
    unittest.main()
