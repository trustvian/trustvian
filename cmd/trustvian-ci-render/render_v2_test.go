package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trustvian/trustvian/internal/cirender"
)

// reportExample is the version 2 document docs/compatibility.md publishes,
// pinned to the control plane's route by platform/httpapi's
// TestReportVersion2ExampleIsPinned.
var reportExample = filepath.Join("..", "..", "platform", "httpapi", "testdata", "report-v2-example.json")

func readReport(t *testing.T) map[string]any {
	t.Helper()
	return readJSON(t, reportExample)
}

func renderReport(t *testing.T, doc any, lim cirender.Limits) cirender.Rendering {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return cirender.RenderDocument(raw, lim)
}

// The published example renders every section the report carries, inert,
// with no figure the document does not hold.
func TestRenderReportExample(t *testing.T) {
	raw, err := os.ReadFile(reportExample)
	if err != nil {
		t.Fatal(err)
	}
	out := cirender.RenderDocument(raw, cirender.DefaultLimits)
	if out.State != cirender.StateVerdict || out.Rejected != nil {
		t.Fatalf("state %s: %s", out.State, out.Reason)
	}
	assertInert(t, out.Markdown)
	for _, want := range []string{
		"### Trustvian behavioral gate — FAIL\n\n**Scenario** `support` · **runs** 2 · **execution** `e1`",
		"| Model | not recorded | not recorded | not recorded |",
		"| 0/2 | 2/2 | 0–0, mean 0 | 1–1, mean 1 | **+ added** | no | — | unrecorded · semantic 0 · transport 0 · unrecorded 2 |",
		"| 1–1, mean 1 | 0–1, mean 0.5 | unclassified | **lost** |",
		"| `build-host` (`external`) | 2–2, mean 2 | 2–3, mean 2.5 | 1.2× |",
		"| `build-host` (`external`) | reference | not available | not available | not available | not available |",
		"Tokens: not available on either side (`both_unavailable`); no change is shown.",
		"priced by `team-2026-10` (digest `sha256:abab",
		"**Verdict: FAIL**, as the execution recorded it when it completed.",
		"| lost behaviors | 1 | at most | 0 | **FAIL** | |",
		"| calls per run · `api.example` | 0 | at most | 5 | not observed | |",
		"| model calls per run | | | | **deferred** | missing evidence: `run \"e1-candidate-1\" has no model-layer observation",
		"- `compare.added_in_all_runs`: `tool/export → build-host was added in 2/2 candidate runs.",
		"Produced by `trustvian` `v0.13.0 3e86f9c` and control plane `v0.12.0`.",
	} {
		if !strings.Contains(out.Markdown, want) {
			t.Errorf("the rendering lacks %q:\n%s", want, out.Markdown)
		}
	}
	if again := cirender.RenderDocument(raw, cirender.DefaultLimits); again.Markdown != out.Markdown {
		t.Error("two renderings of one document differ")
	}
}

// A report whose execution kept no gate says so, plainly, and shows no check,
// verdict, limit or classification.
func TestRenderReportWithAGateNotRecorded(t *testing.T) {
	doc := notRecordedReport(t)
	out := renderReport(t, doc, cirender.DefaultLimits)
	if out.State != cirender.StateVerdict {
		t.Fatalf("state %s: %s", out.State, out.Reason)
	}
	assertInert(t, out.Markdown)
	for _, want := range []string{
		"### Trustvian behavioral gate — gate not recorded",
		"**Gate: not recorded.** This execution completed before its gate result was kept (schema 14).",
		"Classification is not recorded: it needs the limits the execution did not keep.",
		"| not recorded | no |",
	} {
		if !strings.Contains(out.Markdown, want) {
			t.Errorf("the rendering lacks %q:\n%s", want, out.Markdown)
		}
	}
	for _, banned := range []string{"Verdict", "| Gate check", "Added: present", "recorded limits", "**+ added**"} {
		if strings.Contains(out.Markdown, banned) {
			t.Errorf("a report without a gate renders %q:\n%s", banned, out.Markdown)
		}
	}
}

// notRecordedReport is the example as the route serves an execution completed
// before schema 14.
func notRecordedReport(t *testing.T) map[string]any {
	t.Helper()
	doc := readReport(t)
	comparison := obj(doc, "comparison")
	delete(comparison, "gate_limits")
	comparison["gate"] = map[string]any{"state": "not_recorded"}
	for _, b := range comparison["behaviors"].([]any) {
		b.(map[string]any)["classification"] = "not_recorded"
	}
	return doc
}

// Every member the renderer consumes is validated against the contract: a
// value outside its vocabulary, a shape the control plane never writes, or a
// document that contradicts itself is refused, never rendered.
func TestRenderReportRejectsOutOfContractValues(t *testing.T) {
	behavior := func(d map[string]any) map[string]any { return path(d, "comparison", "behaviors", 0).(map[string]any) }
	frequency := func(d map[string]any, i int) map[string]any {
		return path(d, "comparison", "gate", "frequency_checks", i).(map[string]any)
	}
	target := func(d map[string]any) map[string]any { return path(d, "comparison", "targets", 0).(map[string]any) }
	tests := []struct {
		name  string
		field string
		edit  func(d map[string]any)
	}{
		{"an unknown gate state", "comparison.gate.state", func(d map[string]any) {
			obj(d, "comparison", "gate")["state"] = "missing"
		}},
		{"a recorded gate without limits", "comparison.gate_limits", func(d map[string]any) {
			delete(obj(d, "comparison"), "gate_limits")
		}},
		{"an unrecorded gate with a verdict", "comparison.gate.verdict", func(d map[string]any) {
			delete(obj(d, "comparison"), "gate_limits")
			obj(d, "comparison")["gate"] = map[string]any{"state": "not_recorded", "verdict": "fail"}
			for _, b := range obj(d, "comparison")["behaviors"].([]any) {
				b.(map[string]any)["classification"] = "not_recorded"
			}
		}},
		{"an unrecorded gate with limits", "comparison.gate_limits", func(d map[string]any) {
			obj(d, "comparison")["gate"] = map[string]any{"state": "not_recorded"}
		}},
		{"a recorded gate with a not_recorded classification", "comparison.behaviors[0].classification", func(d map[string]any) {
			behavior(d)["classification"] = "not_recorded"
		}},
		{"three frequency checks", "comparison.gate.frequency_checks", func(d map[string]any) {
			g := obj(d, "comparison", "gate")
			g["frequency_checks"] = g["frequency_checks"].([]any)[:3]
		}},
		{"frequency checks out of order", "comparison.gate.frequency_checks[0].name", func(d map[string]any) {
			fc := obj(d, "comparison", "gate")["frequency_checks"].([]any)
			fc[0], fc[1] = fc[1], fc[0]
		}},
		{"a deferred check with an actual", "comparison.gate.frequency_checks[3].actual", func(d map[string]any) {
			frequency(d, 3)["actual"] = "1"
		}},
		{"a deferred check without missing evidence", "comparison.gate.frequency_checks[3].missing_evidence", func(d map[string]any) {
			delete(frequency(d, 3), "missing_evidence")
		}},
		{"an unknown target outcome", "comparison.gate.frequency_checks[2].targets[0].outcome", func(d map[string]any) {
			path(d, "comparison", "gate", "frequency_checks", 2, "targets", 0).(map[string]any)["outcome"] = "skipped"
		}},
		{"a presence string the counts contradict", "comparison.behaviors[0].display", func(d map[string]any) {
			obj(behavior(d), "display")["candidate_presence"] = "1/2"
		}},
		{"a presence string of another shape", "comparison.behaviors[0].display.candidate_presence", func(d map[string]any) {
			obj(behavior(d), "display")["candidate_presence"] = "2 of 2"
		}},
		{"a mean with a trailing zero", "comparison.behaviors[0].display.candidate_calls_per_run_mean", func(d map[string]any) {
			obj(behavior(d), "display")["candidate_calls_per_run_mean"] = "1.0"
		}},
		{"a ratio with two decimals", "comparison.targets[0].display.call_ratio", func(d map[string]any) {
			obj(target(d), "display")["call_ratio"] = "1.25×"
		}},
		{"an incomparable section with a delta", "comparison.operational.tokens", func(d map[string]any) {
			obj(d, "comparison", "operational", "tokens")["delta"] = map[string]any{}
		}},
		{"an unknown reason", "comparison.operational.tokens.reason", func(d map[string]any) {
			obj(d, "comparison", "operational", "tokens")["reason"] = "unknown"
		}},
		{"a cost without its pricing digest", "comparison.operational.cost.pricing_digest", func(d map[string]any) {
			delete(obj(d, "comparison", "operational", "cost"), "pricing_digest")
		}},
		{"no cost and no reason", "comparison.operational.cost_reason", func(d map[string]any) {
			delete(obj(d, "comparison", "operational"), "cost")
		}},
		{"a cost and a reason", "comparison.operational.cost_reason", func(d map[string]any) {
			obj(d, "comparison", "operational")["cost_reason"] = "pricing_not_configured"
		}},
		{"an unknown cost reason", "comparison.operational.cost_reason", func(d map[string]any) {
			delete(obj(d, "comparison", "operational"), "cost")
			obj(d, "comparison", "operational")["cost_reason"] = "free"
		}},
		{"ten latency buckets", "comparison.targets[0].operational.latency.reference.buckets", func(d map[string]any) {
			side := obj(target(d), "operational", "latency", "reference")
			side["buckets"] = side["buckets"].([]any)[:10]
		}},
		{"an unknown sameness answer", "comparison.sameness.same_model", func(d map[string]any) {
			obj(d, "comparison", "sameness")["same_model"] = "yes"
		}},
		{"an unknown reference mode", "reference.mode", func(d map[string]any) {
			d["reference"] = map[string]any{"mode": "latest", "execution_id": "e0"}
		}},
		{"an unknown fidelity level", "comparison.behaviors[0].candidate_fidelity.level", func(d map[string]any) {
			obj(behavior(d), "candidate_fidelity")["level"] = "inferred"
		}},
		{"comparison version 2", "comparison.version", func(d map[string]any) {
			obj(d, "comparison")["version"] = "2"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := readReport(t)
			tt.edit(doc)
			out := renderReport(t, doc, cirender.DefaultLimits)
			if out.State != cirender.StateNoVerdict || !errors.Is(out.Rejected, cirender.ErrRejected) {
				t.Fatalf("state %s, rejected %v; want a rejection", out.State, out.Rejected)
			}
			if !strings.Contains(out.Reason, tt.field) {
				t.Errorf("reason %q does not name %s", out.Reason, tt.field)
			}
			assertNoBareEvidence(t, out.Markdown)
		})
	}
}

// assertNoBareEvidence is assertNoEvidence for a document rendered with no
// run around it: the heading, the statement and the reason, and nothing from
// the document.
func assertNoBareEvidence(t *testing.T, md string) {
	t.Helper()
	const lead = "### Trustvian behavioral gate — no verdict\n\nNo rendering exists for this document.\n\n**Reason:** `"
	if !strings.HasPrefix(md, lead) || strings.Count(md, "\n") != 5 {
		t.Errorf("a bare no-verdict rendering is not its heading, statement and reason:\n%s", md)
	}
	for _, banned := range []string{"PASS", "FAIL", "Produced by", "](", "<"} {
		if strings.Contains(md, banned) {
			t.Errorf("a bare no-verdict rendering contains %q:\n%s", banned, md)
		}
	}
}

// Absent, null, zero and false stay four different things: an optional
// member is absent or present with its kind, never null, and a zero or false
// is rendered as the value it is.
func TestRenderReportKeepsAbsentNullZeroAndFalseDistinct(t *testing.T) {
	behavior := func(d map[string]any) map[string]any { return path(d, "comparison", "behaviors", 0).(map[string]any) }
	for _, tt := range []struct {
		name string
		edit func(d map[string]any)
		ok   bool
	}{
		{"an absent reference fidelity renders as none", func(d map[string]any) {}, true},
		{"a null reference fidelity", func(d map[string]any) { behavior(d)["reference_fidelity"] = nil }, false},
		{"an absent lost", func(d map[string]any) { delete(behavior(d), "lost") }, false},
		{"a null lost", func(d map[string]any) { behavior(d)["lost"] = nil }, false},
		{"a null cost_reason", func(d map[string]any) {
			delete(obj(d, "comparison", "operational"), "cost")
			obj(d, "comparison", "operational")["cost_reason"] = nil
		}, false},
		{"a null reference", func(d map[string]any) { d["reference"] = nil }, false},
		{"an absent cost_micros is not available", func(d map[string]any) {}, true},
		{"a null cost_micros", func(d map[string]any) {
			obj(d, "comparison", "operational", "cost", "reference")["cost_micros"] = nil
		}, false},
		{"suggestions_truncated null", func(d map[string]any) { obj(d, "comparison")["suggestions_truncated"] = nil }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := readReport(t)
			tt.edit(doc)
			out := renderReport(t, doc, cirender.DefaultLimits)
			if got := out.State == cirender.StateVerdict; got != tt.ok {
				t.Fatalf("rendered %v (%s), want %v", got, out.Reason, tt.ok)
			}
		})
	}
	out := renderReport(t, readReport(t), cirender.DefaultLimits)
	for _, want := range []string{
		"| **+ added** | no | — |",                    // lost false; reference fidelity absent
		"| Cost | not available | not available |  |", // no cost_micros on either side
		"| Priced tokens | 0 | 0 | |",                 // a zero is a zero
	} {
		if !strings.Contains(out.Markdown, want) {
			t.Errorf("the rendering lacks %q:\n%s", want, out.Markdown)
		}
	}
}

// Every string the control plane copied from a producer or a configuration is
// inert: none adds a row, a line, a link or a mention.
func TestRenderReportMakesHostileStringsInert(t *testing.T) {
	doc := readReport(t)
	comparison := obj(doc, "comparison")
	comparison["warnings"] = []any{map[string]any{"code": "model_differs", "text": hostile[0]}}
	suggestion := path(comparison, "suggestions", 0).(map[string]any)
	suggestion["text"] = hostile[1]
	suggestion["rule"] = hostile[2]
	path(suggestion, "evidence", 0).(map[string]any)["value"] = hostile[3]
	obj(comparison, "operational", "cost")["source"] = hostile[4]
	path(comparison, "targets", 0).(map[string]any)["target_name"] = hostile[5]
	path(comparison, "gate", "frequency_checks", 2, "targets", 0).(map[string]any)["target"] = hostile[6]
	obj(comparison, "sameness", "candidate")["model"] = hostile[7]
	obj(doc, "producers")["cli_version"] = hostile[8]
	out := renderReport(t, doc, cirender.DefaultLimits)
	if out.State != cirender.StateVerdict {
		t.Fatalf("state %s: %s", out.State, out.Reason)
	}
	assertInert(t, out.Markdown)
	if strings.Contains(out.Markdown, "\n| repeatedly added behaviors | 0 |") {
		t.Errorf("a forged gate row was rendered:\n%s", out.Markdown)
	}
}

// The comment's size limit: the rendering sheds detail in a stated order and
// never drops a check, a classified or lost behavior, or the sameness block.
func TestRenderReportShedsDetailToFit(t *testing.T) {
	doc := readReport(t)
	full := renderReport(t, doc, cirender.Limits{StringBytes: 256, TotalBytes: 1 << 20})
	if strings.Contains(full.Markdown, "**Shortened:**") {
		t.Fatalf("the full rendering is shortened:\n%s", full.Markdown)
	}
	at := func(total int) cirender.Rendering {
		return renderReport(t, doc, cirender.Limits{StringBytes: 256, TotalBytes: total})
	}

	short := at(len(full.Markdown) - 1)
	if short.State != cirender.StateVerdict ||
		!strings.Contains(short.Markdown, "behaviors that are neither classified nor lost were left out") ||
		strings.Contains(short.Markdown, "`fp-read`") ||
		!strings.Contains(short.Markdown, "`fp-lookup`") || !strings.Contains(short.Markdown, "`fp-export`") ||
		!strings.Contains(short.Markdown, "Latency and status per target") {
		t.Fatalf("the short rendering:\n%s", short.Markdown)
	}

	shortest := at(len(short.Markdown) - 1)
	if shortest.State != cirender.StateVerdict ||
		!strings.Contains(shortest.Markdown, "each target's latency and status evidence was left out") ||
		!strings.Contains(shortest.Markdown, "the suggestions were left out") ||
		strings.Contains(shortest.Markdown, "Latency and status per target") {
		t.Fatalf("the shortest rendering:\n%s", shortest.Markdown)
	}
	for _, kept := range []string{"#### Sameness", "| repeatedly added behaviors |", "| model calls per run |",
		"**+ added**", "**lost**"} {
		if !strings.Contains(shortest.Markdown, kept) {
			t.Errorf("the shortest rendering lost %q", kept)
		}
	}

	none := at(len(shortest.Markdown) - 1)
	if none.State != cirender.StateNoVerdict || !strings.Contains(none.Reason, "does not fit") {
		t.Fatalf("a report that cannot fit: state %s, %s", none.State, none.Reason)
	}
	assertNoBareEvidence(t, none.Markdown)
}

// The comment for a report is the bare rendering with the run's identity and
// the CLI exit code added after its heading, and nothing else changed — so the
// comment and `trustvian eval report --format md` show the same evidence.
func TestCommentOfAReportIsItsDocumentRenderingFramed(t *testing.T) {
	raw, err := os.ReadFile(reportExample)
	if err != nil {
		t.Fatal(err)
	}
	dir := artifact(t, "fail")
	setResult(t, dir, raw)
	comment := render(dir, expected(1), cirender.DefaultLimits)
	if comment.State != cirender.StateVerdict || comment.Rejected != nil {
		t.Fatalf("state %s: %s", comment.State, comment.Reason)
	}
	assertInert(t, comment.Markdown)
	bare := cirender.RenderDocument(raw, cirender.DefaultLimits)
	heading, body, _ := strings.Cut(bare.Markdown, "\n\n")
	frame := "**Commit** `" + fixtureHead + "` · " + runLink + "\n\nCLI exit code: 1\n\n"
	if want := heading + "\n\n" + frame + body; comment.Markdown != want {
		t.Errorf("the comment is not the bare rendering framed:\n%s\nwant\n%s", comment.Markdown, want)
	}

	// A report's verdict and the CLI's exit code must still agree.
	if out := render(dir, expected(0), cirender.DefaultLimits); out.Rejected == nil {
		t.Error("a FAIL report under exit code 0 was rendered")
	}
}

// A suite member embeds its execution's report (D15), and renders it as a
// single scenario's report renders.
func TestRenderSuiteWithReportMembers(t *testing.T) {
	dir := artifact(t, "suite")
	mutateResult(t, dir, func(d map[string]any) {
		for _, m := range d["members"].([]any) {
			member := m.(map[string]any)
			report := readReport(t)
			report["scenario"] = member["scenario"]
			report["execution_id"] = member["execution_id"]
			obj(report, "comparison", "gate")["verdict"] = member["outcome"]
			member["result"] = report
		}
	})
	out := render(dir, expected(1), cirender.DefaultLimits)
	if out.State != cirender.StateReport || out.Rejected != nil {
		t.Fatalf("state %s: %s", out.State, out.Reason)
	}
	assertInert(t, out.Markdown)
	if got := strings.Count(out.Markdown, "#### Sameness"); got != 3 {
		t.Errorf("%d report sections, want 3:\n%s", got, out.Markdown)
	}
	if !strings.Contains(out.Markdown, "#### `b-fail.yaml` — FAIL") {
		t.Errorf("the failing member's heading is missing:\n%s", out.Markdown)
	}
}

// The golden version 2 documents — each the report route's output for a
// fixture execution (platform/httpapi's TestGoldenReportDocuments) — render
// to the golden Markdown beside them.
func TestRenderGoldenReports(t *testing.T) {
	documents, err := filepath.Glob(filepath.Join("testdata", "golden", "v2-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 6 {
		t.Fatalf("%d golden version 2 documents, want 6: %v", len(documents), documents)
	}
	for _, document := range documents {
		name := strings.TrimSuffix(filepath.Base(document), ".json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(document)
			if err != nil {
				t.Fatal(err)
			}
			out := cirender.RenderDocument(raw, cirender.DefaultLimits)
			if out.State != cirender.StateVerdict || out.Rejected != nil {
				t.Fatalf("state %s: %s", out.State, out.Reason)
			}
			assertInert(t, out.Markdown)
			golden := strings.TrimSuffix(document, ".json") + ".md"
			if *update {
				if err := os.WriteFile(golden, []byte(out.Markdown), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if out.Markdown != string(want) {
				t.Errorf("rendering differs from %s:\n%s", golden, out.Markdown)
			}
		})
	}
}

// Every golden Markdown file has its source: a real artifact for version 1,
// a route-produced document for version 2. None is orphaned or hand-written.
func TestEveryGoldenFileHasItsSource(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".md")
		source := filepath.Join("testdata", "artifacts", name)
		if strings.HasPrefix(name, "v2-") {
			source = filepath.Join("testdata", "golden", name+".json")
		}
		if _, err := os.Stat(source); err != nil {
			t.Errorf("%s has no source: %v", file, err)
		}
	}
}
