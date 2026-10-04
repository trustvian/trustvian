package webui

// The Evidence surface in the browser (task 076).
//
// Two kinds of assertion, and both are here on purpose.
//
// **Behavioral**, run under `node` when one is on PATH. Everything this surface
// can get wrong about a degenerate parent reference — a missing parent, a
// duplicate span id, a cycle, a self-reference, a parent on another page — is a
// property of one pure function, and asserting it by reading the source would be
// asserting that the code looks right. The structural assertions below are what
// hold on a machine without node, so a skip never leaves the behavior unchecked.
//
// **Structural**, always run. These are the ones a future change is most likely
// to break silently: that no status is computed here, that no rendered sentence
// claims a model decided anything, that a stale response is discarded rather
// than drawn, and that the retained field the platform keeps but this page must
// not show stays unshown.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// The node harness
// ---------------------------------------------------------------------

// runDriver executes one ES-module driver against the shipped assets.
//
// The assets are written out rather than re-implemented, which is the whole
// point: the thing under test is the code that ships. Every .js asset is copied
// so a driver can import any of them without this helper knowing which.
func runDriver(t *testing.T, driver string, args ...string) []byte {
	t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the structural evidence assertions still ran")
	}

	// The bundle is laid out in directories, and the driver imports the same
	// specifiers the browser resolves, so the tree is reproduced rather than
	// flattened. A flattened copy would run modules whose relative imports
	// mean something different from what ships.
	dir := t.TempDir()
	for _, name := range assetNames() {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(name), err)
		}
		if err := os.WriteFile(target, []byte(readAsset(t, name)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "driver.mjs"), []byte(driver), 0o600); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBin, append([]string{"driver.mjs"}, args...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the driver under node: %v\n%s", err, stderr.String())
	}
	return stdout
}

// decodeDriver reads a driver's JSON output into target.
func decodeDriver(t *testing.T, out []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(out, target); err != nil {
		t.Fatalf("decode driver output: %v\n%s", err, out)
	}
}

// ---------------------------------------------------------------------
// The trace tree
// ---------------------------------------------------------------------

// traceNode mirrors one row buildTraceTree returns.
type traceNode struct {
	Sequence      string `json:"sequence"`
	State         string `json:"state"`
	Depth         int    `json:"depth"`
	DepthClamped  bool   `json:"depthClamped"`
	DuplicateSpan bool   `json:"duplicateSpan"`
}

const traceDriver = `
import { buildTraceTree, TRACE_MAX_DEPTH } from "./views/trace.js";
const cases = JSON.parse(process.argv[2]);
const out = {};
for (const name of Object.keys(cases)) {
  out[name] = buildTraceTree(cases[name]).map((node) => ({
    sequence: node.observation.sequence,
    state: node.state,
    depth: node.depth,
    depthClamped: node.depthClamped,
    duplicateSpan: node.duplicateSpan,
  }));
}
out["__max_depth__"] = [{ sequence: String(TRACE_MAX_DEPTH), state: "", depth: 0,
  depthClamped: false, duplicateSpan: false }];
process.stdout.write(JSON.stringify(out));
`

// observationFixture is the minimum of a retained observation a tree reads.
type observationFixture struct {
	Sequence     string `json:"sequence"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id"`
	SpanLineage  string `json:"span_lineage"`
}

// TestTraceTreeIsBuiltOnlyFromRecordedParentIdentity drives every degenerate
// parent reference a producer can emit.
//
// Task 067 accepts all of these — it refuses no record for its lineage, and no
// parent-existence check exists at any layer — so every one of them reaches this
// function in production. The load-bearing assertions are that **no row is ever
// lost**, that nothing loops, and that none of the six unplaceable states is
// drawn as a root.
func TestTraceTreeIsBuiltOnlyFromRecordedParentIdentity(t *testing.T) {
	cases := map[string][]observationFixture{
		// A parent and its child, the ordinary case.
		"parent and child": {
			{Sequence: "1", SpanID: "a", ParentSpanID: "", SpanLineage: "root"},
			{Sequence: "2", SpanID: "b", ParentSpanID: "a", SpanLineage: "child"},
		},
		// The parent was never retained, or is on a page not read. Not a root.
		"missing parent": {
			{Sequence: "1", SpanID: "b", ParentSpanID: "gone", SpanLineage: "child"},
		},
		// A span naming itself.
		"self parent": {
			{Sequence: "1", SpanID: "a", ParentSpanID: "a", SpanLineage: "child"},
		},
		// A two-node cycle. Nothing may loop and both rows must appear.
		"cycle": {
			{Sequence: "1", SpanID: "a", ParentSpanID: "b", SpanLineage: "child"},
			{Sequence: "2", SpanID: "b", ParentSpanID: "a", SpanLineage: "child"},
		},
		// Two rows carrying one span id, and a third naming it as its parent.
		"duplicate span id": {
			{Sequence: "1", SpanID: "dup", ParentSpanID: "", SpanLineage: "root"},
			{Sequence: "2", SpanID: "dup", ParentSpanID: "", SpanLineage: "root"},
			{Sequence: "3", SpanID: "c", ParentSpanID: "dup", SpanLineage: "child"},
		},
		// The producer said child and named no parent.
		"child with no parent id": {
			{Sequence: "1", SpanID: "a", ParentSpanID: "", SpanLineage: "child"},
		},
		// Nothing established lineage at all: an Event that came from no span.
		"no lineage": {
			{Sequence: "1", SpanID: "a", ParentSpanID: "", SpanLineage: ""},
		},
		// A child accepted before its parent — ordinary, because a parent span
		// ends after the children it started.
		"child before parent": {
			{Sequence: "1", SpanID: "b", ParentSpanID: "a", SpanLineage: "child"},
			{Sequence: "2", SpanID: "a", ParentSpanID: "", SpanLineage: "root"},
		},
		// No span id at all, with a parent reference that cannot be resolved.
		"no span id": {
			{Sequence: "1", SpanID: "", ParentSpanID: "a", SpanLineage: "child"},
		},
		"empty page": {},
	}

	// A chain deeper than the indent bound, built here so the bound is exercised
	// against the shipped constant rather than a copy of it.
	deep := make([]observationFixture, 0, 40)
	for i := 1; i <= 40; i++ {
		row := observationFixture{
			Sequence:    strings.Repeat("", 0) + itoaTest(i),
			SpanID:      "s" + itoaTest(i),
			SpanLineage: "child",
		}
		if i == 1 {
			row.SpanLineage = "root"
		} else {
			row.ParentSpanID = "s" + itoaTest(i-1)
		}
		deep = append(deep, row)
	}
	cases["deep chain"] = deep

	encoded, err := json.Marshal(cases)
	if err != nil {
		t.Fatalf("encode cases: %v", err)
	}
	var got map[string][]traceNode
	decodeDriver(t, runDriver(t, traceDriver, string(encoded)), &got)

	maxDepth := 0
	if rows, ok := got["__max_depth__"]; ok && len(rows) == 1 {
		maxDepth = atoiTest(rows[0].Sequence)
	}
	if maxDepth <= 0 {
		t.Fatal("TRACE_MAX_DEPTH did not come back from the asset")
	}

	// Every case: one output row per input row, and no row repeated.
	for name, input := range cases {
		rows := got[name]
		if len(rows) != len(input) {
			t.Errorf("%s: tree has %d rows, input had %d — a tree must lose no "+
				"evidence and invent none", name, len(rows), len(input))
			continue
		}
		seen := make(map[string]bool, len(rows))
		for _, row := range rows {
			if seen[row.Sequence] {
				t.Errorf("%s: sequence %s appears twice", name, row.Sequence)
			}
			seen[row.Sequence] = true
		}
	}

	// And the per-case classifications.
	expect := map[string]map[string]string{
		"parent and child":        {"1": "root", "2": "child"},
		"missing parent":          {"1": "unresolved"},
		"self parent":             {"1": "self"},
		"duplicate span id":       {"1": "root", "2": "root", "3": "ambiguous"},
		"child with no parent id": {"1": "unstated"},
		"no lineage":              {"1": "unstated"},
		"child before parent":     {"1": "child", "2": "root"},
		"no span id":              {"1": "unresolved"},
	}
	for name, want := range expect {
		for _, row := range got[name] {
			if want[row.Sequence] != row.State {
				t.Errorf("%s: sequence %s classified %q, want %q",
					name, row.Sequence, row.State, want[row.Sequence])
			}
		}
	}

	// A cycle must be broken and stated, never drawn and never followed.
	var cycled int
	for _, row := range got["cycle"] {
		if row.State == "cycle" {
			cycled++
		}
	}
	if cycled == 0 {
		t.Error("a two-node parent cycle produced no row marked cycle")
	}

	// Nothing unplaceable may read as a root.
	for _, name := range []string{
		"missing parent", "self parent", "child with no parent id", "no lineage",
		"no span id", "cycle",
	} {
		for _, row := range got[name] {
			if row.State == "root" {
				t.Errorf("%s: sequence %s is drawn as a root; a parent this page "+
					"does not have is not a trace root", name, row.Sequence)
			}
		}
	}

	// Depth is bounded, and a clamped row says so.
	clamped := false
	for _, row := range got["deep chain"] {
		if row.Depth > maxDepth {
			t.Errorf("deep chain: sequence %s at depth %d exceeds the bound of %d",
				row.Sequence, row.Depth, maxDepth)
		}
		if row.DepthClamped {
			clamped = true
		}
	}
	if !clamped {
		t.Error("a chain deeper than the bound reported no clamping")
	}

	// A duplicated span id is stated on the rows that carry it.
	for _, row := range got["duplicate span id"] {
		want := row.Sequence == "1" || row.Sequence == "2"
		if row.DuplicateSpan != want {
			t.Errorf("duplicate span id: sequence %s duplicateSpan = %v, want %v",
				row.Sequence, row.DuplicateSpan, want)
		}
	}
}

// itoaTest and atoiTest keep strconv out of this file's imports for two uses.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

func atoiTest(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// ---------------------------------------------------------------------
// Duration, status and the new-behavior label
// ---------------------------------------------------------------------

const factsDriver = `
import { formatDuration, spanStatusLabel, newBehaviorLabel } from "./views/trace.js";
const input = JSON.parse(process.argv[2]);
const out = { durations: {}, statuses: {}, newBehavior: {} };
for (const name of Object.keys(input.durations)) {
  out.durations[name] = formatDuration(input.durations[name]);
}
for (const name of Object.keys(input.statuses)) {
  out.statuses[name] = spanStatusLabel(input.statuses[name]);
}
for (const name of Object.keys(input.newBehavior)) {
  out.newBehavior[name] = newBehaviorLabel(input.newBehavior[name]);
}
process.stdout.write(JSON.stringify(out));
`

// TestRecordedFactsAreRenderedWithoutPromotingAbsenceToAValue is the honesty
// half of task 084 in the browser.
//
// A measured zero and an unavailable duration are different facts, `unset` is not
// success, and neither distinction survives a renderer that treats absence as a
// default. The large-value case is here because the whole reason the API sends
// nanoseconds as text is that JavaScript cannot hold the number — a formatter
// that parsed it would silently round exactly the values worth reading.
func TestRecordedFactsAreRenderedWithoutPromotingAbsenceToAValue(t *testing.T) {
	input := map[string]map[string]any{
		"durations": {
			"absent":            map[string]any{},
			"empty":             map[string]any{"duration_nanos": ""},
			"measured zero":     map[string]any{"duration_nanos": "0"},
			"one and a half ms": map[string]any{"duration_nanos": "1500000"},
			// Above 2^53, where Number() loses the last digits.
			"beyond float64": map[string]any{"duration_nanos": "9007199254740993"},
			"not canonical":  map[string]any{"duration_nanos": "01"},
		},
		"statuses": {
			"unavailable": map[string]any{"span_status": ""},
			"unset":       map[string]any{"span_status": "unset"},
			"ok":          map[string]any{"span_status": "ok"},
			"error":       map[string]any{"span_status": "error"},
			"unknown":     map[string]any{"span_status": "SUCCESS"},
		},
		"newBehavior": {
			"new":          map[string]any{"new_behavior": true},
			"seen":         map[string]any{"new_behavior": false},
			"not recorded": map[string]any{},
		},
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("encode input: %v", err)
	}

	var got struct {
		Durations map[string]struct {
			Measured bool   `json:"measured"`
			Text     string `json:"text"`
		} `json:"durations"`
		Statuses    map[string]string `json:"statuses"`
		NewBehavior map[string]string `json:"newBehavior"`
	}
	decodeDriver(t, runDriver(t, factsDriver, string(encoded)), &got)

	// Unavailable is never zero and never a number.
	for _, name := range []string{"absent", "empty"} {
		entry := got.Durations[name]
		if entry.Measured {
			t.Errorf("duration %q reports measured; nothing measured it", name)
		}
		if strings.Contains(entry.Text, "0") {
			t.Errorf("duration %q renders %q, which contains a digit; an "+
				"unavailable duration is not a measurement", name, entry.Text)
		}
	}

	zero := got.Durations["measured zero"]
	if !zero.Measured {
		t.Error("a measured zero reports unmeasured; a span with an end timestamp " +
			"that equalled its start did take zero time")
	}
	if zero.Text == got.Durations["absent"].Text {
		t.Errorf("a measured zero and an unavailable duration both render %q", zero.Text)
	}

	if want := "1.5 ms"; got.Durations["one and a half ms"].Text != want {
		t.Errorf("1500000ns renders %q, want %q",
			got.Durations["one and a half ms"].Text, want)
	}
	// 9007199254740993 / 1e6 — exact only if the digits were never parsed.
	if want := "9007199254.740993 ms"; got.Durations["beyond float64"].Text != want {
		t.Errorf("a nanosecond count above 2^53 renders %q, want %q — the value "+
			"was parsed as a number somewhere",
			got.Durations["beyond float64"].Text, want)
	}
	if got.Durations["not canonical"].Measured {
		t.Error("a non-canonical duration reports measured; it was coerced rather " +
			"than refused")
	}

	// Only "ok" is success.
	for name, label := range got.Statuses {
		lower := strings.ToLower(label)
		if name == "ok" {
			continue
		}
		for _, claim := range []string{"success", "succeeded", "no error", "healthy", "fine"} {
			if strings.Contains(lower, claim) {
				t.Errorf("span status %q renders %q, which claims %q; only \"ok\" is "+
					"success and only \"error\" is failure", name, label, claim)
			}
		}
	}
	if got.Statuses["unavailable"] == got.Statuses["unset"] {
		t.Errorf("an unavailable status and an unset one both render %q",
			got.Statuses["unset"])
	}
	if !strings.Contains(got.Statuses["unknown"], "not recognize") {
		t.Errorf("an unrecognized status renders %q; a value this page does not "+
			"know must say so rather than be shown as one it does",
			got.Statuses["unknown"])
	}

	// "New to this run" is not novelty against a learned baseline.
	if !strings.Contains(got.NewBehavior["new"], "this run") {
		t.Errorf("new_behavior true renders %q; it means new to *this run*, which "+
			"is a different measurement from novelty against a learned baseline",
			got.NewBehavior["new"])
	}
	for name, label := range got.NewBehavior {
		lower := strings.ToLower(label)
		for _, claim := range []string{"novel", "never seen", "unknown behavior", "anomalous", "suspicious"} {
			if strings.Contains(lower, claim) {
				t.Errorf("new_behavior %q renders %q, which claims %q", name, label, claim)
			}
		}
	}
}

// ---------------------------------------------------------------------
// Every rendered sentence
// ---------------------------------------------------------------------

const sentenceDriver = `
import {
  parentStateText, PARENT_STATES, DURATION_UNAVAILABLE,
  FIDELITY_NOT_RETAINED, SEQUENCE_DEVIATION_NOT_RETAINED,
  NEW_TO_RUN, SEEN_IN_RUN,
} from "./views/trace.js";
import {
  statusSentence, historySentence, RUN_VIEWS, OBSERVATION_COLUMNS,
  RECORDED_COUNT_CAVEAT, EXHAUSTIVE_CAVEAT, PROVENANCE_UNSPECIFIED,
} from "./views/evidence.js";

const sentences = [];
for (const key of Object.keys(PARENT_STATES)) {
  sentences.push(parentStateText(PARENT_STATES[key]));
}
sentences.push(parentStateText("something-else"));
for (const status of ["resolved", "none_found", "indeterminate", "aggregate_only", "?"]) {
  sentences.push(statusSentence(status));
}
for (const state of ["complete", "partial", "unavailable", "?"]) {
  sentences.push(historySentence(state));
}
for (const view of RUN_VIEWS) {
  sentences.push(view.label);
  sentences.push(view.blurb);
}
for (const name of Object.keys(OBSERVATION_COLUMNS)) {
  for (const column of OBSERVATION_COLUMNS[name]) {
    sentences.push(column.label);
  }
}
sentences.push(DURATION_UNAVAILABLE, FIDELITY_NOT_RETAINED,
  SEQUENCE_DEVIATION_NOT_RETAINED, NEW_TO_RUN, SEEN_IN_RUN,
  RECORDED_COUNT_CAVEAT, EXHAUSTIVE_CAVEAT, PROVENANCE_UNSPECIFIED);
process.stdout.write(JSON.stringify(sentences));
`

// TestNoRenderedSentenceAssertsReasoningIntentOrCausality is task 076's
// amendment, asserted against the text that actually reaches the screen.
//
// The absence property, not the presence of a replacement phrase: it fails for
// any future wording that reintroduces the claim rather than only for the one
// wording that did. A trace shows what was observed and in what structure. It
// does not show why a model chose anything, and nothing here may imply it does.
func TestNoRenderedSentenceAssertsReasoningIntentOrCausality(t *testing.T) {
	var sentences []string
	decodeDriver(t, runDriver(t, sentenceDriver), &sentences)
	if len(sentences) < 20 {
		t.Fatalf("only %d sentences came back; this guard would be near-vacuous",
			len(sentences))
	}

	// Words that assert a model's reasoning, intent or plan, plus the two
	// connectives that would turn observed structure into asserted causality
	// between spans.
	forbidden := []string{
		"decided", "decides", "chose", "chooses", "choosing",
		"intended", "intent", "planned", "its plan",
		"reasoned", "reasoning", "thought", "wanted",
		"in order to", "so that it could", "caused the", "led it to",
		"novel", "malicious", "unsafe", "dangerous",
	}
	for _, sentence := range sentences {
		lower := strings.ToLower(sentence)
		for _, bad := range forbidden {
			if strings.Contains(lower, bad) {
				t.Errorf("a rendered sentence contains %q:\n  %s\n"+
					"Sequence deviation may be stated because the engine recorded it; "+
					"sequence intent may not, because nothing did", bad, sentence)
			}
		}
	}
}

// ---------------------------------------------------------------------
// The controller
// ---------------------------------------------------------------------

const controllerDriver = `
import { EvidenceSurface, needsExplicitSide, findingPresentation } from "./views/evidence.js";

// A recording view. It draws nothing; it records what it was asked to draw,
// which is what makes "a stale response was discarded" observable.
function recorder() {
  const calls = [];
  const push = (kind) => (payload) => { calls.push({ kind, payload }); };
  return {
    calls,
    findingLoading: () => { calls.push({ kind: "findingLoading" }); },
    findingPage: push("findingPage"),
    findingError: push("findingError"),
    runNeedsRun: () => { calls.push({ kind: "runNeedsRun" }); },
    runNeedsIdentifier: push("runNeedsIdentifier"),
    runLoading: () => { calls.push({ kind: "runLoading" }); },
    runObservationPage: push("runObservationPage"),
    runError: push("runError"),
    provenanceLoading: () => { calls.push({ kind: "provenanceLoading" }); },
    provenancePair: push("provenancePair"),
    provenanceError: push("provenanceError"),
  };
}

const page = (rows, nextAfter) => ({
  finding: { reference_run_id: "ref", candidate_run_id: "cand" },
  status: "resolved",
  side: "candidate",
  recorded_count: "9",
  history_state: "complete",
  retained_count: "9",
  complete: true,
  history: { state: "complete", retained_count: "9", complete: true },
  observations: rows.map((s) => ({ sequence: String(s), span_id: "s" + s })),
  next_after: nextAfter,
});

const results = {};

// 1. Pagination resets when the question changes, and the cursor asked for is
//    the one the caller passed rather than one carried over.
{
  const asked = [];
  const view = recorder();
  const surface = new EvidenceSurface({
    runObservations: async (runID, scope, after) => {
      asked.push({ runID, scope, after });
      return page([1, 2], "2");
    },
  }, view);

  await surface.openRunView("run-1", "session", "sess-1");
  await surface.nextRunPage("2");
  await surface.openRunView("run-2", "session", "sess-2");
  results.pagination = {
    asked,
    pages: view.calls.filter((c) => c.kind === "runObservationPage")
      .map((c) => c.payload.pageNumber),
  };
}

// 2. A response that lands after the question changed is discarded.
{
  const view = recorder();
  let releaseSlow;
  const slow = new Promise((resolve) => { releaseSlow = resolve; });
  const surface = new EvidenceSurface({
    runObservations: async (runID) => {
      if (runID === "slow") {
        await slow;
        return page([99], "");
      }
      return page([1], "");
    },
  }, view);

  const first = surface.openRunView("slow", "sequence", "");
  const second = surface.openRunView("fast", "sequence", "");
  await second;
  releaseSlow();
  await first;
  results.stale = view.calls.filter((c) => c.kind === "runObservationPage")
    .map((c) => ({
      runID: c.payload.runID,
      rows: c.payload.response.observations.map((o) => o.sequence),
    }));
}

// 3. Client state stays bounded: after three pages the surface holds no rows.
{
  const view = recorder();
  const surface = new EvidenceSurface({
    runObservations: async (runID, scope, after) =>
      page([after === "" ? 1 : Number(after) + 1], String(after === "" ? 1 : Number(after) + 1)),
  }, view);
  await surface.openRunView("run-1", "timeline", "");
  await surface.nextRunPage("1");
  await surface.nextRunPage("2");

  let arrays = 0;
  for (const key of Object.keys(surface)) {
    if (Array.isArray(surface[key])) {
      arrays += 1;
    }
  }
  results.bounded = {
    arrays,
    rendered: view.calls.filter((c) => c.kind === "runObservationPage")
      .map((c) => c.payload.response.observations.length),
  };
}

// 4. The scope carries at most one narrowing, and the right one per view.
{
  const scopes = {};
  const view = recorder();
  const surface = new EvidenceSurface({
    runObservations: async (runID, scope) => { scopes[surface.viewKey] = scope; return page([1], ""); },
  }, view);
  for (const key of ["session", "trace", "sequence", "timeline", "behavior"]) {
    await surface.openRunView("run-1", key, "value-1");
  }
  results.scopes = scopes;
}

// 5. A view needing an identifier that has none asks rather than reading.
{
  const view = recorder();
  let reads = 0;
  const surface = new EvidenceSurface({
    runObservations: async () => { reads += 1; return page([1], ""); },
  }, view);
  await surface.openRunView("run-1", "trace", "");
  results.needsIdentifier = {
    reads,
    kinds: view.calls.map((c) => c.kind),
  };
}

// 6. Which presences leave the side undecided.
results.sides = {
  added: needsExplicitSide("added"),
  removed: needsExplicitSide("removed"),
  shared: needsExplicitSide("shared"),
  unknown: needsExplicitSide("something"),
};

// 7. An empty continuation page keeps the finding's own status.
{
  const view = recorder();
  const surface = new EvidenceSurface({
    resolveFindingObservations: async (finding, after) => ({
      finding: { reference_run_id: "ref", candidate_run_id: "cand" },
      status: "resolved",
      side: "candidate",
      recorded_count: "64",
      exhaustive: true,
      observations: after === "" ? [{ sequence: "1", span_id: "a" }] : [],
      history: { state: "complete", retained_count: "1", complete: true },
      next_after: after === "" ? "1" : "",
    }),
  }, view);
  await surface.openFinding({ referenceRunID: "ref", candidateRunID: "cand", check: "block_decisions" }, "observations");
  await surface.nextFindingPage("1");
  results.emptyContinuation = view.calls.filter((c) => c.kind === "findingPage")
    .map((c) => ({
      status: c.payload.response.status,
      rows: c.payload.response.observations.length,
      nextAfter: c.payload.response.next_after,
    }));
}

// 8. An unrelated surface's read discards nothing and moves no page position.
//    This is the defect a single shared token caused: opening Provenance while a
//    run-history page was in flight threw that page away and left the panel on
//    the loading notice it had already drawn.
{
  const view = recorder();
  let releaseRun;
  const runGate = new Promise((resolve) => { releaseRun = resolve; });
  const surface = new EvidenceSurface({
    runObservations: async () => { await runGate; return page([1, 2], ""); },
    getRun: async (id) => ({ id, candidate_id: "cand-" + id }),
    getCandidate: async (id) => ({ id, metadata: { label: id } }),
  }, view);

  const pendingRun = surface.openRunView("run-1", "sequence", "");
  await surface.loadProvenance("ref", "cand");
  releaseRun();
  await pendingRun;

  results.provenanceDuringRun = {
    kinds: view.calls.map((c) => c.kind),
    runPages: view.calls.filter((c) => c.kind === "runObservationPage")
      .map((c) => c.payload.pageNumber),
  };
}

// 9. The same, the other way round: a finding read in flight survives an
//    unrelated run-history read.
{
  const view = recorder();
  let releaseFinding;
  const findingGate = new Promise((resolve) => { releaseFinding = resolve; });
  const surface = new EvidenceSurface({
    resolveFindingBehaviors: async () => {
      await findingGate;
      return { finding: {}, status: "resolved", side: "candidate", recorded_count: "2",
        behaviors: [{ fingerprint_id: "fp-1" }],
        history: { state: "complete", retained_count: "2", complete: true },
        next_after: "" };
    },
    runObservations: async () => page([1], ""),
  }, view);

  const pendingFinding = surface.openFinding(
    { referenceRunID: "ref", candidateRunID: "cand", check: "added_behaviors" }, "behaviors");
  await surface.openRunView("run-1", "sequence", "");
  releaseFinding();
  await pendingFinding;

  results.runDuringFinding = {
    kinds: view.calls.map((c) => c.kind),
    findingPages: view.calls.filter((c) => c.kind === "findingPage")
      .map((c) => c.payload.pageNumber),
  };
}

// 10. An unrelated surface's read does not reset a page counter that is still on
//     screen beside its continuation control.
{
  const view = recorder();
  const surface = new EvidenceSurface({
    runObservations: async (runID, scope, after) => page([1], after === "2" ? "" : "next"),
    getRun: async (id) => ({ id, candidate_id: "c" }),
    getCandidate: async (id) => ({ id, metadata: {} }),
  }, view);

  await surface.openRunView("run-1", "timeline", "");
  await surface.nextRunPage("1");
  await surface.loadProvenance("ref", "cand");
  await surface.nextRunPage("2");

  results.pagePreserved = view.calls.filter((c) => c.kind === "runObservationPage")
    .map((c) => c.payload.pageNumber);
}

// 11. Changing the question inside one surface still discards its older response.
{
  const view = recorder();
  let releaseFirst;
  const firstGate = new Promise((resolve) => { releaseFirst = resolve; });
  let call = 0;
  const surface = new EvidenceSurface({
    resolveFindingObservations: async (finding) => {
      call += 1;
      if (call === 1) {
        await firstGate;
      }
      return { finding: { behavior: finding.behavior }, status: "resolved",
        side: finding.side, recorded_count: "1", exhaustive: true,
        observations: [{ sequence: "1", span_id: "a" }],
        history: { state: "complete", retained_count: "1", complete: true },
        next_after: "" };
    },
  }, view);

  const first = surface.openFinding(
    { referenceRunID: "r", candidateRunID: "c", behavior: "fp-old", side: "candidate" },
    "observations");
  const second = surface.openFinding(
    { referenceRunID: "r", candidateRunID: "c", behavior: "fp-new", side: "reference" },
    "observations");
  await second;
  releaseFirst();
  await first;

  results.sameSurfaceStale = view.calls.filter((c) => c.kind === "findingPage")
    .map((c) => c.payload.finding.behavior);
}

// 12. Every read path renders something, so no panel can be left on a loading
//     notice with nothing that would start another read.
{
  const view = recorder();
  let reads = 0;
  const surface = new EvidenceSurface({
    runObservations: async () => { reads += 1; return page([1], ""); },
  }, view);
  await surface.openRunView("", "sequence", "");
  results.noRun = { reads, kinds: view.calls.map((c) => c.kind) };
}

// 13. What each resolution status lets the header claim.
{
  const shapes = {};
  for (const sample of [
    { name: "aggregate_only", response: { status: "aggregate_only", exhaustive: false,
      history: { state: "unavailable", retained_count: "0", complete: false } } },
    { name: "resolved_complete", response: { status: "resolved", exhaustive: true,
      history: { state: "complete", retained_count: "6", complete: true } } },
    { name: "indeterminate_partial", response: { status: "indeterminate", exhaustive: false,
      history: { state: "partial", retained_count: "4096", complete: false } } },
    { name: "none_found", response: { status: "none_found", exhaustive: true,
      history: { state: "complete", retained_count: "3", complete: true } } },
    { name: "behaviors_no_exhaustive", response: { status: "resolved",
      history: { state: "complete", retained_count: "2", complete: true } } },
  ]) {
    shapes[sample.name] = findingPresentation(sample.response);
  }
  results.presentation = shapes;
}

process.stdout.write(JSON.stringify(results));
`

// TestEvidenceSurfaceControllerBehavior asserts what the controller is for.
//
// Five properties, each of which is a defect if it fails and none of which is
// visible in the rendering: pagination resets when the question changes, a
// response that arrives after it changed is discarded, client state does not grow
// with history size, the scope carries one narrowing, and a shared behavior is
// never given a side this page picked.
func TestEvidenceSurfaceControllerBehavior(t *testing.T) {
	var got struct {
		Pagination struct {
			Asked []struct {
				RunID string            `json:"runID"`
				Scope map[string]string `json:"scope"`
				After string            `json:"after"`
			} `json:"asked"`
			Pages []int `json:"pages"`
		} `json:"pagination"`
		Stale []struct {
			RunID string   `json:"runID"`
			Rows  []string `json:"rows"`
		} `json:"stale"`
		Bounded struct {
			Arrays   int   `json:"arrays"`
			Rendered []int `json:"rendered"`
		} `json:"bounded"`
		Scopes          map[string]map[string]string `json:"scopes"`
		NeedsIdentifier struct {
			Reads int      `json:"reads"`
			Kinds []string `json:"kinds"`
		} `json:"needsIdentifier"`
		Sides             map[string]bool `json:"sides"`
		EmptyContinuation []struct {
			Status    string `json:"status"`
			Rows      int    `json:"rows"`
			NextAfter string `json:"nextAfter"`
		} `json:"emptyContinuation"`
		ProvenanceDuringRun struct {
			Kinds    []string `json:"kinds"`
			RunPages []int    `json:"runPages"`
		} `json:"provenanceDuringRun"`
		RunDuringFinding struct {
			Kinds        []string `json:"kinds"`
			FindingPages []int    `json:"findingPages"`
		} `json:"runDuringFinding"`
		PagePreserved    []int    `json:"pagePreserved"`
		SameSurfaceStale []string `json:"sameSurfaceStale"`
		NoRun            struct {
			Reads int      `json:"reads"`
			Kinds []string `json:"kinds"`
		} `json:"noRun"`
		Presentation map[string]struct {
			DescribesRetainedHistory bool   `json:"describesRetainedHistory"`
			ShowHistory              bool   `json:"showHistory"`
			ShowExhaustiveCaveat     bool   `json:"showExhaustiveCaveat"`
			ShowRows                 bool   `json:"showRows"`
			CountNote                string `json:"countNote"`
		} `json:"presentation"`
	}
	decodeDriver(t, runDriver(t, controllerDriver), &got)

	// 1. Pagination resets.
	asked := got.Pagination.Asked
	if len(asked) != 3 {
		t.Fatalf("the controller made %d reads, want 3", len(asked))
	}
	if asked[0].After != "" || asked[1].After != "2" || asked[2].After != "" {
		t.Errorf("cursors asked for were %q, %q, %q; want \"\", \"2\", \"\" — a "+
			"cursor is a position in one ordered stream and must not survive a "+
			"change of question", asked[0].After, asked[1].After, asked[2].After)
	}
	want := []int{1, 2, 1}
	for i := range want {
		if i >= len(got.Pagination.Pages) || got.Pagination.Pages[i] != want[i] {
			t.Errorf("page numbers were %v, want %v", got.Pagination.Pages, want)
			break
		}
	}

	// 2. The stale response never reached the view.
	if len(got.Stale) != 1 {
		t.Fatalf("the view was handed %d pages, want 1 — a response that landed "+
			"after the run changed must be discarded, not drawn: %+v",
			len(got.Stale), got.Stale)
	}
	if got.Stale[0].RunID != "fast" {
		t.Errorf("the view drew run %q; the current question was \"fast\"",
			got.Stale[0].RunID)
	}

	// 3. Bounded client state.
	if got.Bounded.Arrays != 0 {
		t.Errorf("the controller holds %d array field(s); a page must replace what "+
			"is on screen rather than accumulate", got.Bounded.Arrays)
	}
	for _, count := range got.Bounded.Rendered {
		if count > 1 {
			t.Errorf("a page carried %d rows where the fixture returned 1; pages "+
				"are being joined together", count)
		}
	}

	// 4. One narrowing per view, and the right one.
	expected := map[string]string{
		"session":  "sessionID",
		"trace":    "traceID",
		"behavior": "fingerprintID",
		"sequence": "",
		"timeline": "",
	}
	for view, key := range expected {
		scope, ok := got.Scopes[view]
		if !ok {
			t.Errorf("the %s view made no read", view)
			continue
		}
		if key == "" {
			if len(scope) != 0 {
				t.Errorf("the %s view narrowed with %v; it reads the whole page", view, scope)
			}
			continue
		}
		if len(scope) != 1 {
			t.Errorf("the %s view sent %d narrowings; the control plane accepts one",
				view, len(scope))
			continue
		}
		if scope[key] == "" {
			t.Errorf("the %s view sent %v, want the %s field set", view, scope, key)
		}
	}

	// 5. A missing identifier asks rather than reading.
	if got.NeedsIdentifier.Reads != 0 {
		t.Errorf("a view with no identifier issued %d read(s); an unnarrowed read "+
			"would return the whole run and read as that session's contents",
			got.NeedsIdentifier.Reads)
	}
	if len(got.NeedsIdentifier.Kinds) == 0 ||
		got.NeedsIdentifier.Kinds[0] != "runNeedsIdentifier" {
		t.Errorf("the view was told %v; it should be asked for the identifier",
			got.NeedsIdentifier.Kinds)
	}

	// 6. Presence decides a side only where it can.
	if got.Sides["added"] || got.Sides["removed"] {
		t.Errorf("presence left the side undecided for %v; an added behavior exists "+
			"only on the candidate and a removed one only on the reference, so the "+
			"control plane derives it", got.Sides)
	}
	if !got.Sides["shared"] || !got.Sides["unknown"] {
		t.Errorf("presence decided a side for %v; a shared behavior has no natural "+
			"side and this page must not pick one", got.Sides)
	}

	// 8. A provenance read leaves a pending run-history read alone.
	if len(got.ProvenanceDuringRun.RunPages) != 1 {
		t.Errorf("a run-history read in flight when Provenance loaded produced %d "+
			"rendered page(s), want 1 — a response discarded here leaves the panel "+
			"on a loading notice that nothing will replace, because selecting a "+
			"subtab starts no read", len(got.ProvenanceDuringRun.RunPages))
	} else if got.ProvenanceDuringRun.RunPages[0] != 1 {
		t.Errorf("the surviving run-history page reported page %d, want 1",
			got.ProvenanceDuringRun.RunPages[0])
	}
	if !containsKind(got.ProvenanceDuringRun.Kinds, "provenancePair") {
		t.Errorf("the provenance read did not render: %v", got.ProvenanceDuringRun.Kinds)
	}

	// 9. And the other way round.
	if len(got.RunDuringFinding.FindingPages) != 1 {
		t.Errorf("a finding read in flight when the run history loaded produced %d "+
			"rendered page(s), want 1", len(got.RunDuringFinding.FindingPages))
	}
	if !containsKind(got.RunDuringFinding.Kinds, "runObservationPage") {
		t.Errorf("the run-history read did not render: %v", got.RunDuringFinding.Kinds)
	}

	// 10. An unrelated read moves no page position.
	wantPages := []int{1, 2, 3}
	if len(got.PagePreserved) != len(wantPages) {
		t.Fatalf("page numbers were %v, want %v", got.PagePreserved, wantPages)
	}
	for i := range wantPages {
		if got.PagePreserved[i] != wantPages[i] {
			t.Fatalf("page numbers were %v, want %v — a provenance read reset a "+
				"counter belonging to rows still on screen beside their "+
				"continuation control", got.PagePreserved, wantPages)
		}
	}

	// 11. Changing the question inside one surface still discards the older one.
	if len(got.SameSurfaceStale) != 1 || got.SameSurfaceStale[0] != "fp-new" {
		t.Errorf("the finding surface rendered %v; only the current question's "+
			"response may reach the view", got.SameSurfaceStale)
	}

	// 12. No read path returns without rendering.
	if got.NoRun.Reads != 0 {
		t.Errorf("a view with no run issued %d read(s)", got.NoRun.Reads)
	}
	if len(got.NoRun.Kinds) == 0 || got.NoRun.Kinds[0] != "runNeedsRun" {
		t.Errorf("a view with no run rendered %v; a path that renders nothing "+
			"leaves whatever was there, which after a loading notice is a panel "+
			"nothing recovers", got.NoRun.Kinds)
	}

	// 13. What each status lets the header claim.
	aggregate, ok := got.Presentation["aggregate_only"]
	if !ok {
		t.Fatal("findingPresentation returned nothing for aggregate_only")
	}
	if aggregate.ShowHistory || aggregate.ShowExhaustiveCaveat || aggregate.ShowRows ||
		aggregate.DescribesRetainedHistory {
		t.Errorf("an aggregate-only result would render %+v; the control plane read "+
			"no history for it, so its history and exhaustiveness fields are the "+
			"zero value and describe nothing", aggregate)
	}
	if !strings.Contains(aggregate.CountNote, "aggregate") {
		t.Errorf("the aggregate-only count note is %q; it must describe the count "+
			"without describing retained history", aggregate.CountNote)
	}
	for _, claim := range []string{"Retained history is bounded", "sample", "not complete"} {
		if strings.Contains(aggregate.CountNote, claim) {
			t.Errorf("the aggregate-only count note claims %q", claim)
		}
	}
	for _, name := range []string{
		"resolved_complete", "indeterminate_partial", "none_found", "behaviors_no_exhaustive",
	} {
		shape, ok := got.Presentation[name]
		if !ok {
			t.Fatalf("findingPresentation returned nothing for %s", name)
		}
		if !shape.ShowHistory || !shape.ShowRows {
			t.Errorf("%s would render %+v; a result that read history must keep "+
				"showing it", name, shape)
		}
	}
	if !got.Presentation["indeterminate_partial"].ShowExhaustiveCaveat {
		t.Error("a partial-history resolution no longer explains that it is a sample")
	}
	if got.Presentation["resolved_complete"].ShowExhaustiveCaveat {
		t.Error("a complete-history resolution claims to be a sample")
	}
	if got.Presentation["behaviors_no_exhaustive"].ShowExhaustiveCaveat {
		t.Error("the behaviors route publishes no exhaustive field, so an absent " +
			"one must state nothing")
	}

	// 7. An empty continuation keeps the finding's status.
	if len(got.EmptyContinuation) != 2 {
		t.Fatalf("the view drew %d finding pages, want 2", len(got.EmptyContinuation))
	}
	last := got.EmptyContinuation[1]
	if last.Rows != 0 {
		t.Fatalf("the continuation page held %d rows; the fixture returns none", last.Rows)
	}
	if last.Status != "resolved" {
		t.Errorf("an empty continuation page reported status %q, want resolved — "+
			"the status describes the finding, not the page", last.Status)
	}
}

// ---------------------------------------------------------------------
// Structural guards
// ---------------------------------------------------------------------

// TestEvidenceSurfaceDecidesNothing is the boundary this whole surface rests on.
//
// The browser may format evidence. It may not decide a status, a side, a count or
// a verdict — task 076's criterion 14 and task 085's criterion 11 say so from
// both ends. The shapes of deciding one are: minting a status literal, deriving
// it from how many rows arrived, and doing arithmetic on a recorded count.
func TestEvidenceSurfaceDecidesNothing(t *testing.T) {
	// stripJSComments rather than stripJSNoise: the status vocabulary *is* string
	// literals, and the scan that blanks literals would make every assertion below
	// pass without looking at anything.
	evidence := stripJSComments(readAsset(t, "evidence.js"))
	app := stripJSComments(readAsset(t, "app.js"))

	// The status is read, not derived.
	if !strings.Contains(evidence, "response.status") && !strings.Contains(app, "response.status") {
		t.Error("nothing reads response.status; the displayed status must be the server's")
	}

	// A status literal may be **compared against** — branching on the server's
	// answer is what a renderer does — and may be the key of the sentence that
	// explains it. It may not be produced: an assignment, a property value or a
	// returned literal is where a browser would start deciding one.
	//
	// So each occurrence is checked in context rather than counted. Counting
	// forbade `status === "aggregate_only"`, which is a read of the server's
	// answer and the opposite of minting it.
	for _, status := range []string{"none_found", "indeterminate", "aggregate_only", "resolved"} {
		occurrences := 0
		for _, source := range map[string]string{"evidence.js": evidence, "app.js": app} {
			literal := `"` + status + `"`
			for index := 0; ; {
				at := strings.Index(source[index:], literal)
				if at < 0 {
					break
				}
				at += index
				index = at + len(literal)
				occurrences++
				before := strings.TrimRight(source[:at], " \t\n")
				switch {
				case strings.HasSuffix(before, "["), // a key of the sentence map
					strings.HasSuffix(before, "==="),
					strings.HasSuffix(before, "!=="):
					continue
				default:
					line := strings.Count(source[:at], "\n") + 1
					t.Errorf("line %d produces the status literal %q rather than "+
						"comparing against one; the status is the control plane's "+
						"answer, and a browser that writes one has decided it",
						line, status)
				}
			}
		}
		if occurrences == 0 {
			t.Errorf("neither asset names %q at all; this guard would be vacuous "+
				"for it", status)
		}
	}

	// Deriving the status from the page length is the defect task 085 documents
	// by name, and it arrives as a length compared against zero next to a status.
	derived := []*regexp.Regexp{
		regexp.MustCompile(`\.length\s*===?\s*0\s*\?\s*["'](none_found|indeterminate|resolved)`),
		regexp.MustCompile(`status\s*=\s*["'](none_found|indeterminate|resolved|aggregate_only)`),
		regexp.MustCompile(`recorded_count\s*[-+*/]`),
		regexp.MustCompile(`recorded_count\s*[<>]`),
		regexp.MustCompile(`exhaustive\s*=\s*(true|false)`),
	}
	for name, source := range map[string]string{"evidence.js": evidence, "app.js": app} {
		for _, pattern := range derived {
			if match := pattern.FindString(source); match != "" {
				t.Errorf("%s contains %q, which derives an answer the control plane "+
					"already gave", name, match)
			}
		}
	}

	// A rendered row count is never presented as a total.
	if regexp.MustCompile(`(?i)\btotal\b`).MatchString(evidence) {
		t.Error("evidence.js uses the word \"total\"; the number of rows drawn is " +
			"how much was drawn, and authoritative counts come from authoritative reads")
	}
}

// TestEvidenceRenderingHoldsNoRetainedFieldItMustNotShow is the privacy half.
//
// `policy_reason` is retained by task 067 and deliberately absent from task 059's
// realtime projection, and the allowlist guard in this package forbids it. It is
// the one free-text, producer-supplied field on the retained row, so it is the one
// this surface is most likely to acquire by accident.
func TestEvidenceRenderingHoldsNoRetainedFieldItMustNotShow(t *testing.T) {
	lists := renderAllowlists(t)
	fields, ok := lists["EVIDENCE_OBSERVATION_FIELDS"]
	if !ok {
		t.Fatal("EVIDENCE_OBSERVATION_FIELDS is not declared in render.js")
	}
	if len(fields) == 0 {
		t.Fatal("EVIDENCE_OBSERVATION_FIELDS is empty; the guard would be vacuous")
	}
	for _, field := range fields {
		if field == "policy_reason" {
			t.Error("EVIDENCE_OBSERVATION_FIELDS allowlists policy_reason")
		}
	}

	// And it appears in no shipped asset at all, so no view can reach it by
	// another route. Literals are kept here on purpose: the two ways to render the
	// field are a property access and a string key, and only one of them survives
	// a scan that blanks literals.
	for name, source := range scriptAssets(t) {
		if strings.Contains(stripJSComments(source), "policy_reason") {
			t.Errorf("%s names policy_reason outside a comment", name)
		}
	}

	// **The allowlist is consulted, not merely declared.** A list nothing reads
	// is a guard that passes without looking at anything, so this asserts the one
	// accessor exists and that every direct field read in the evidence source
	// names a field the list holds. Those are the two ways a value can reach a
	// cell, and both are covered.
	if !strings.Contains(stripJSComments(readAsset(t, "render.js")), "EVIDENCE_OBSERVATION_FIELDS.includes") {
		t.Error("render.js declares EVIDENCE_OBSERVATION_FIELDS but no accessor " +
			"checks it; an allowlist nothing consults protects nothing")
	}

	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	// `observation.x` and `row.x` are how trace.js and evidence.js reach a
	// retained field directly. `behavior` is the nested descriptor, rendered
	// through BEHAVIOR_FIELDS, which is its own allowlist.
	direct := regexp.MustCompile(`\b(?:observation|row)\.([a-z][a-z0-9_]*)\b`)
	for _, name := range []string{"trace.js", "evidence.js"} {
		source := stripJSComments(readAsset(t, name))
		matches := direct.FindAllStringSubmatch(source, -1)
		if len(matches) == 0 {
			t.Errorf("%s reads no observation field directly; this scan would be "+
				"vacuous", name)
		}
		for _, match := range matches {
			if match[1] == "behavior" {
				continue
			}
			if !allowed[match[1]] {
				t.Errorf("%s reads observation.%s, which EVIDENCE_OBSERVATION_FIELDS "+
					"does not allowlist", name, match[1])
			}
		}
	}

	// The fields it does allowlist must all be ones task 067 retains. A name
	// that is not is either a typo — rendering "—" forever — or an invention.
	retained := map[string]bool{
		"sequence": true, "event_id": true, "timestamp": true,
		"actor_id": true, "actor_type": true, "identity_confidence": true,
		"fingerprint_id": true, "new_behavior": true,
		"anomaly_score": true, "anomaly_confidence": true, "trust_score": true,
		"context_risk": true, "risk_level": true,
		"decision": true, "policy_rule": true, "matched_default": true,
		"trace_id": true, "span_id": true, "session_id": true,
		"delegated_from": true, "approval_status": true,
		"parent_span_id": true, "span_lineage": true,
		"duration_nanos": true, "span_status": true,
	}
	for _, field := range fields {
		if !retained[field] {
			t.Errorf("EVIDENCE_OBSERVATION_FIELDS allowlists %q, which task 067 "+
				"does not retain", field)
		}
	}
}

// TestHistoricalViewsStateThatFidelityWasNotRetained is the narrowing this task
// had to make, asserted so it cannot quietly become an inference.
//
// Task 076's criterion 2 asks a view to state its fidelity. Fidelity and
// behavioral layer ride beside a record at ingest and on realtime, and task 067
// retained neither — so a historical view genuinely does not know. Saying so is
// the honest answer; deriving it from the shape of a descriptor would be the
// fabrication task 075 refuses for operation names.
func TestHistoricalViewsStateThatFidelityWasNotRetained(t *testing.T) {
	trace := readAsset(t, "trace.js")
	if !strings.Contains(trace, "FIDELITY_NOT_RETAINED") {
		t.Fatal("trace.js declares no FIDELITY_NOT_RETAINED notice")
	}

	app := stripJSComments(readAsset(t, "app.js"))
	if !strings.Contains(app, "FIDELITY_NOTE") {
		t.Error("no run-history view states that fidelity was not retained")
	}

	// And nothing derives one. The live view reads `fidelity` from a realtime
	// frame, which is legitimate; the evidence surface must not, because no
	// retained payload carries it.
	evidence := stripJSComments(readAsset(t, "evidence.js"))
	for _, derived := range []string{"fidelity", "behavior_layer"} {
		if strings.Contains(evidence, derived) {
			t.Errorf("evidence.js reads %q; retained history carries neither, so a "+
				"value read there would have been invented", derived)
		}
	}

	// The same for sequence deviation, which task 067 also does not retain.
	if !strings.Contains(trace, "SEQUENCE_DEVIATION_NOT_RETAINED") {
		t.Fatal("trace.js declares no sequence-deviation notice")
	}
}

// TestParentageIsNeverInferredFromTimeOrAdjacency is task 084's rule in the
// browser.
//
// Parentage is read from the recorded parent reference and from nothing else. The
// shapes of inferring it are: reading a timestamp while building structure,
// comparing two rows' times, and using an array index as a parent.
func TestParentageIsNeverInferredFromTimeOrAdjacency(t *testing.T) {
	trace := stripJSComments(readAsset(t, "trace.js"))

	if !strings.Contains(trace, "parent_span_id") {
		t.Fatal("trace.js never reads parent_span_id; the guard would be vacuous")
	}

	forbidden := []*regexp.Regexp{
		// The tree builder must not touch a clock.
		regexp.MustCompile(`timestamp`),
		regexp.MustCompile(`Date\.`),
		regexp.MustCompile(`getTime\(`),
		// Nor sort by anything, which is how "adjacent" becomes "related".
		regexp.MustCompile(`\.sort\(`),
	}
	for _, pattern := range forbidden {
		if match := pattern.FindString(trace); match != "" {
			t.Errorf("trace.js contains %q; structure comes from the recorded parent "+
				"reference, never from timing, adjacency or ordering", match)
		}
	}
}

// TestEvidenceGenerationTokenGuardsEveryAsyncContinuation is the stale-callback
// rule, applied to the surface that changes question most often.
//
// The same shape realtime.js is held to, and for the same reason: a browser
// cannot cancel an in-flight promise, so a late response is discarded by checking
// the token — and the check has to be the statement immediately after the read,
// because "in between" is where a stale response gets used.
func TestEvidenceGenerationTokenGuardsEveryAsyncContinuation(t *testing.T) {
	source := stripJSNoise(readAsset(t, "evidence.js"))

	readThenGuard := regexp.MustCompile(
		`await this\.deps\.\w+\([^)]*\);\s*
\s*if \(this\.\w+\.stale\(token\)\)`)
	reads := regexp.MustCompile(`await this\.deps\.\w+\(`)

	total := len(reads.FindAllString(source, -1))
	guarded := len(readThenGuard.FindAllString(source, -1))
	if total == 0 {
		t.Fatal("no awaited external reads found in evidence.js; this guard would " +
			"pass vacuously")
	}
	if guarded != total {
		t.Errorf("evidence.js has %d awaited external reads but only %d are followed "+
			"immediately by a generation check. A response that lands after the "+
			"question changed would mutate the current view", total, guarded)
	}

	// Every catch must check too, since a rejection from an abandoned read would
	// otherwise replace the current view with an old error.
	catches := strings.Count(source, "} catch (")
	if guards := strings.Count(source, ".stale(token)"); guards < total+catches {
		t.Errorf("evidence.js has %d reads and %d catch blocks but only %d "+
			"generation checks", total, catches, guards)
	}

	// Restarting a question clears the page position with the token, so a new
	// question cannot page into the middle of the old answer.
	if !regexp.MustCompile(
		`restart\(\) \{\s*
\s*this\.generation \+= 1;\s*
\s*this\.cursor = "";\s*
\s*this\.page = 0;`).
		MatchString(source) {
		t.Error("restarting a request surface does not reset its cursor and page")
	}
	// Continuing one keeps it, because a continuation is the same question.
	if !regexp.MustCompile(
		`paginate\(after\) \{\s*
\s*this\.generation \+= 1;\s*
\s*this\.cursor = after;`).
		MatchString(source) {
		t.Error("continuing a request surface does not keep its page position")
	}
}

// TestEvidenceSurfacesCancelOnlyThemselves is the defect a single shared token
// caused.
//
// One counter for three unrelated questions meant reading any surface abandoned
// every other. Opening Provenance while a run-history page was in flight
// discarded that page's response and left the panel on the "Reading…" it had
// already drawn — and selecting a subtab starts no read, so nothing recovered
// it. The same counter reset an unrelated surface's page number while its rows
// and continuation control stayed on screen.
//
// Structural half: there is no counter on the surface itself, and there are
// three separate request objects. The behavioral half is in the controller
// driver below.
func TestEvidenceSurfacesCancelOnlyThemselves(t *testing.T) {
	source := stripJSNoise(readAsset(t, "evidence.js"))

	if regexp.MustCompile(`this\.generation = 0;`).FindAllString(source, -1) == nil {
		t.Fatal("no generation counter found at all; this guard would be vacuous")
	}
	// Exactly one declaration, and it is the per-surface one.
	if count := strings.Count(source, "this.generation = 0;"); count != 1 {
		t.Errorf("%d generation counters are declared; one belongs to RequestSurface "+
			"and each surface holds its own instance", count)
	}
	if !strings.Contains(source, "class RequestSurface") {
		t.Fatal("evidence.js declares no RequestSurface")
	}

	surfaces := regexp.MustCompile(`this\.\w+Request = new RequestSurface\(\);`).
		FindAllString(source, -1)
	if len(surfaces) != 3 {
		t.Errorf("EvidenceSurface holds %d request surfaces, want three — finding, "+
			"run history and provenance are independent questions", len(surfaces))
	}

	// And no method resets more than its own.
	if strings.Contains(source, "invalidate()") {
		t.Error("evidence.js still has a page-wide invalidate(); cancelling is a " +
			"property of one surface, not of the page")
	}
}

// TestEvidenceCallsOnlyTheRoutesThisTaskDesigned keeps the client honest about
// its own surface.
func TestEvidenceCallsOnlyTheRoutesThisTaskDesigned(t *testing.T) {
	raw := readAsset(t, "api.js")

	for _, route := range []string{
		"/v1/evaluation-runs/${segment(runID)}/observations${query}",
		"/v1/evaluation-runs/${segment(runID)}/behaviors${query}",
		"/v1/evidence/behaviors${pageQuery(findingParams(finding), after, null)}",
		"/v1/evidence/observations${pageQuery(findingParams(finding), after, null)}",
	} {
		if !strings.Contains(raw, route) {
			t.Errorf("api.js no longer calls %s; this check would pass vacuously", route)
		}
	}

	stripped := stripJSComments(raw)
	// The narrowings this task added, and no others. A parameter the server does
	// not publish would be a capability invented in a browser.
	allowed := map[string]bool{
		"session_id": true, "trace_id": true, "fingerprint_id": true,
		"reference_run_id": true, "candidate_run_id": true,
		"check": true, "behavior": true, "side": true,
		"after": true, "limit": true, "run_id": true,
		// Task 101's recency collection narrows a project by these two;
		// task 102's execution collection filters by the other two.
		"agent_id": true, "candidate_id": true, "environment": true, "scenario": true,
	}
	for _, match := range regexp.MustCompile(`params\.set\("([a-z_]+)"`).
		FindAllStringSubmatch(stripped, -1) {
		if !allowed[match[1]] {
			t.Errorf("api.js sets query parameter %q, which is not one this task "+
				"designed", match[1])
		}
	}

	// A side is sent only when a caller stated one. A default here is the exact
	// silent answer task 085 refused.
	if !strings.Contains(stripped, "if (finding.side)") {
		t.Error("api.js does not guard the side parameter; a shared behavior must " +
			"carry the side a caller chose and no other")
	}
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`side\s*(\|\||\?\?)\s*["']candidate["']`),
		regexp.MustCompile(`params\.set\("side",\s*["']candidate["']`),
	} {
		if match := pattern.FindString(stripped); match != "" {
			t.Errorf("api.js contains %q, which defaults a side", match)
		}
	}
}

// TestGateAndDeltaRowsReachTheirEvidenceInOneStep is criterion 13.
//
// From a gate FAIL a developer reaches the contributing behavioral identities in
// one step, and from one of those the retained observations — without typing an
// identifier. The identifiers come from the comparison's own echo of them, which
// is what makes "no typing" true rather than merely convenient.
func TestGateAndDeltaRowsReachTheirEvidenceInOneStep(t *testing.T) {
	render := stripJSComments(readAsset(t, "render.js"))
	app := stripJSComments(readAsset(t, "app.js"))

	for _, hook := range []string{"onResolveCheck", "deltaControls"} {
		if !strings.Contains(render, hook) {
			t.Errorf("render.js has no %s hook, so a gate row cannot carry an "+
				"evidence control", hook)
		}
		if !strings.Contains(app, hook) {
			t.Errorf("app.js supplies no %s, so the control is never wired", hook)
		}
	}

	if !strings.Contains(app, "openFinding") {
		t.Error("app.js never opens a finding; the navigation does not exist")
	}
	// The run identifiers come from the comparison response, not from the form.
	if !strings.Contains(app, "response.reference_run_id") ||
		!strings.Contains(app, "response.candidate_run_id") {
		t.Error("the finding reference is not built from the comparison's own run " +
			"identifiers")
	}
	// A delta whose presence leaves the side undecided offers both sides rather
	// than one, and the question is asked through the one helper that answers it
	// — so the rule cannot be restated differently in two places.
	branches := regexp.MustCompile(
		`needsExplicitSide\([^)]*\)\)? \{[\s\S]{0,400}?open\("reference"[\s\S]{0,200}?open\("candidate"`).
		FindAllString(app, -1)
	if len(branches) != 2 {
		t.Errorf("%d of the two delta controls offer both sides when presence "+
			"cannot decide; defaulting to one would answer a question nobody "+
			"asked, silently", len(branches))
	}
	if strings.Contains(app, `change === "shared"`) {
		t.Error("app.js restates the shared-behavior rule inline; it belongs in " +
			"needsExplicitSide, where it is tested")
	}
}

// TestEvidenceSurfaceExistsInTheShell checks the markup the navigation lands on.
func TestEvidenceSurfaceExistsInTheShell(t *testing.T) {
	shell := readAsset(t, "index.html")

	for _, needed := range []string{
		`id="nav-evidence"`,
		`id="view-evidence"`,
		`data-section="evidence-finding"`,
		`data-section="evidence-run"`,
		`data-section="evidence-provenance"`,
		`id="form-evidence-run"`,
		`id="form-evidence-provenance"`,
		`id="evidence-finding-result"`,
		`id="evidence-run-result"`,
		`id="evidence-provenance-result"`,
	} {
		if !strings.Contains(shell, needed) {
			t.Errorf("the shell is missing %s", needed)
		}
	}

	// Every identifier field carries inline help, the rule task 074 established.
	for _, field := range []string{"evidence-run-id", "evidence-identifier"} {
		if !strings.Contains(shell, `for="`+field+`"`) {
			t.Errorf("%s has no label", field)
		}
	}

	// The panel states what it cannot show, so a reader is not left to assume the
	// absence is an oversight.
	for _, phrase := range []string{"prompt", "tool argument", "arbitrary attribute"} {
		if !strings.Contains(shell, phrase) {
			t.Errorf("the Evidence view does not name %q among what it cannot show", phrase)
		}
	}
}

// TestSubtabSwitchersAreScopedToTheirPanel is the bug adding a second set of
// sub-sections would otherwise have introduced.
//
// A document-wide `.subtab` query wired every button to both switchers, so
// showing an Evidence section hid every Manage one and vice versa. The scope is
// the fix; this is what keeps it.
func TestSubtabSwitchersAreScopedToTheirPanel(t *testing.T) {
	// The selector is a string literal, so comments are stripped and literals kept.
	app := stripJSComments(readAsset(t, "app.js"))

	if strings.Contains(app, `document.querySelectorAll(".subtab")`) {
		t.Error("app.js queries every .subtab in the document; with several views " +
			"holding sub-sections that wires each button to every switcher")
	}
	if !regexp.MustCompile(`view\.querySelectorAll\("\.subtab"\)`).MatchString(app) {
		t.Error("the subtab query is not scoped to a view")
	}
	// Four views hold sub-sections now, which is what makes the scope
	// load-bearing rather than a precaution.
	for _, view := range []string{
		`setupSubtabs("view-manage"`, `setupSubtabs("view-evidence"`,
		`setupSubtabs("view-promotion"`, `setupSubtabs("view-run"`,
	} {
		if !strings.Contains(app, view) {
			t.Errorf("app.js does not set up subtabs with %s)", view)
		}
	}
}

// TestProvenanceShowsAbsenceAsAbsence is criterion 12.
//
// Every field the producer did not supply renders as explicitly not stated —
// never blank, never a default, and never omitted so that a reader assumes the
// two sides matched. And task 086's fields are absent, because this task must not
// implement them.
func TestProvenanceShowsAbsenceAsAbsence(t *testing.T) {
	evidence := stripJSNoise(readAsset(t, "evidence.js"))

	if !strings.Contains(evidence, "PROVENANCE_UNSPECIFIED") {
		t.Fatal("evidence.js declares no unspecified-provenance value")
	}
	if !strings.Contains(evidence, "CANDIDATE_METADATA_FIELDS") {
		t.Error("the provenance panel does not iterate the metadata allowlist")
	}

	// Task 086's additions are not this task's to implement.
	for _, deferred := range []string{"prompt_ref", "prompt_reference", "scenario_digest", "input_digest"} {
		if strings.Contains(evidence, deferred) {
			t.Errorf("evidence.js names %q, which is task 086's field and not this "+
				"task's to render", deferred)
		}
	}

	lists := renderAllowlists(t)
	metadata, ok := lists["CANDIDATE_METADATA_FIELDS"]
	if !ok || len(metadata) != 6 {
		t.Fatalf("CANDIDATE_METADATA_FIELDS holds %d fields, want the six "+
			"CandidateMetadata already carries", len(metadata))
	}
}

// containsKind reports whether a recorded view call of that kind happened.
func containsKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}
