package webui

// Task 100: the trace waterfall and the Traces destination.

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

const waterfallDriver = `
import { buildWaterfall, timestampNanos, durationNanos, filterTraces, nextIndex, offsetText } from "./views/waterfall.js";

// Ingested children-first, as real producers do: the parent span ends last.
const page = [
  { sequence: "1", span_id: "b", parent_span_id: "a", span_lineage: "child", timestamp: "2026-10-04T09:00:00.100Z", duration_nanos: "200000000" },
  { sequence: "2", span_id: "c", parent_span_id: "b", span_lineage: "child", timestamp: "2026-10-04T09:00:00.150000001Z" },
  { sequence: "3", span_id: "a", span_lineage: "root", timestamp: "2026-10-04T09:00:00Z", duration_nanos: "1000000000" },
  { sequence: "4", span_id: "d", parent_span_id: "elsewhere", span_lineage: "child", timestamp: "not a time", duration_nanos: "5" },
  { sequence: "5", span_id: "e", parent_span_id: "a", span_lineage: "child", timestamp: "2026-10-04T09:00:00.9Z", duration_nanos: "0" },
];
const layout = buildWaterfall(page);
const out = {
  order: layout.rows.map((r) => r.observation.sequence),
  depth: layout.rows.map((r) => r.depth),
  state: layout.rows.map((r) => r.state),
  offset: layout.rows.map((r) => r.offset),
  left: layout.rows.map((r) => r.left),
  width: layout.rows.map((r) => r.width),
  measured: layout.rows.map((r) => r.measured),
  text: layout.rows.map((r) => r.durationText),
  windowNanos: layout.windowNanos,
  timed: layout.timed,
  untimed: layout.untimed,
  single: buildWaterfall([{ sequence: "1", span_id: "x", timestamp: "2026-10-04T09:00:00Z" }]).rows[0],
  empty: buildWaterfall([]).rows.length,
  ts: {
    nanos: timestampNanos("2026-10-04T09:00:00.123456789Z"),
    offset: timestampNanos("2026-10-04T12:00:00.5+03:00"),
    bad: timestampNanos("2026-10-04 09:00:00"),
  },
  dur: [durationNanos({ duration_nanos: "18446744073709551615" }) > 1e19, durationNanos({ duration_nanos: "01" }), durationNanos({})],
  filter: filterTraces([{ trace_id: "abc" }, { trace_id: "ABD" }, { trace_id: "x" }], " ab ").map((t) => t.trace_id),
  next: [nextIndex(-1, 5, "ArrowDown"), nextIndex(4, 5, "ArrowDown"), nextIndex(0, 5, "ArrowUp"), nextIndex(2, 5, "End"),
    nextIndex(2, 5, "Home"), nextIndex(0, 30, "PageDown"), nextIndex(3, 0, "ArrowDown")],
  offsetText: [offsetText(null), offsetText(500), offsetText(1500), offsetText(2500000), offsetText(3000000000)],
};
process.stdout.write(JSON.stringify(out));
`

func TestWaterfallPlacesRecordedTimingOnRecordedStructure(t *testing.T) {
	var got struct {
		Order       []string
		Depth       []int
		State       []string
		Offset      []*float64
		Left        []*float64
		Width       []float64
		Measured    []bool
		Text        []string
		WindowNanos float64
		Timed       int
		Untimed     int
		Single      struct {
			Left     float64
			Width    float64
			Measured bool
		}
		Empty int
		Ts    map[string]*struct {
			Seconds float64
			Nanos   float64
		}
		Dur        []any
		Filter     []string
		Next       []int
		OffsetText []string
	}
	decodeDriver(t, runDriver(t, waterfallDriver), &got)

	// Structure is the tree's: root first despite arriving third, children
	// under it, and a parent this page lacks is unresolved, not a root.
	if strings.Join(got.Order, ",") != "3,1,2,5,4" {
		t.Errorf("row order = %v, want [3 1 2 5 4] (recorded parentage, not ingest order)", got.Order)
	}
	wantDepth := []int{0, 1, 2, 1, 0}
	wantState := []string{"root", "child", "child", "child", "unresolved"}
	for i := range wantDepth {
		if got.Depth[i] != wantDepth[i] || got.State[i] != wantState[i] {
			t.Errorf("row %d depth/state = %d/%s, want %d/%s", i, got.Depth[i], got.State[i], wantDepth[i], wantState[i])
		}
	}

	// Placement: offsets from the earliest start, in nanoseconds, exact to
	// the nanosecond the producer recorded.
	wantOffset := []float64{0, 100e6, 150e6 + 1, 900e6}
	for i, want := range wantOffset {
		if got.Offset[i] == nil || *got.Offset[i] != want {
			t.Errorf("row %d offset = %v, want %v", i, got.Offset[i], want)
		}
	}
	if got.Offset[4] != nil || got.Left[4] != nil {
		t.Error("a row with an unreadable timestamp was placed on the axis")
	}
	if got.WindowNanos != 1e9 || got.Timed != 4 || got.Untimed != 1 {
		t.Errorf("window %v timed %d untimed %d, want 1e9, 4, 1", got.WindowNanos, got.Timed, got.Untimed)
	}
	approx := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !approx(*got.Left[1], 0.1) || !approx(got.Width[1], 0.2) || !approx(got.Width[0], 1) {
		t.Errorf("geometry: left[1]=%v width[1]=%v width[0]=%v", *got.Left[1], got.Width[1], got.Width[0])
	}

	// An unmeasured duration is a marker with no width — never a measured
	// zero — and a measured zero says so.
	if got.Measured[2] || got.Width[2] != 0 || got.Text[2] != "not available" {
		t.Errorf("unmeasured row: measured=%v width=%v text=%q", got.Measured[2], got.Width[2], got.Text[2])
	}
	if !got.Measured[3] || got.Text[3] != "0 ms (measured)" {
		t.Errorf("measured-zero row: measured=%v text=%q", got.Measured[3], got.Text[3])
	}
	if got.Single.Left != 0 || got.Single.Width != 0 || got.Single.Measured {
		t.Errorf("a single instantaneous row = %+v; it sits at the left edge with no bar", got.Single)
	}
	if got.Empty != 0 {
		t.Error("an empty page produced rows")
	}

	if ts := got.Ts["nanos"]; ts == nil || ts.Nanos != 123456789 {
		t.Errorf("timestampNanos lost the fraction: %+v", ts)
	}
	if ts := got.Ts["offset"]; ts == nil || ts.Nanos != 5e8 || got.Ts["nanos"].Seconds != ts.Seconds {
		t.Errorf("timestampNanos did not honour the offset: %+v vs %+v", ts, got.Ts["nanos"])
	}
	if got.Ts["bad"] != nil {
		t.Error("a non-RFC 3339 timestamp was read")
	}
	if got.Dur[0] != true || got.Dur[1] != nil || got.Dur[2] != nil {
		t.Errorf("durationNanos = %v; non-canonical and absent must be null", got.Dur)
	}

	if strings.Join(got.Filter, ",") != "abc,ABD" {
		t.Errorf("filterTraces = %v", got.Filter)
	}
	wantNext := []int{0, 4, 0, 4, 0, 10, -1}
	for i, want := range wantNext {
		if got.Next[i] != want {
			t.Errorf("nextIndex case %d = %d, want %d (no wrapping at the edges)", i, got.Next[i], want)
		}
	}
	wantText := []string{"not available", "+500 ns", "+1.5 µs", "+2.5 ms", "+3.000 s"}
	for i, want := range wantText {
		if got.OffsetText[i] != want {
			t.Errorf("offsetText case %d = %q, want %q", i, got.OffsetText[i], want)
		}
	}
}

// TestTracesSurfaceIsHonestOwnedAndKeyboardOperable reads the controller for
// the properties a node driver without a document cannot exercise.
func TestTracesSurfaceIsHonestOwnedAndKeyboardOperable(t *testing.T) {
	raw := readAsset(t, "views/traces.js")
	source := stripJSNoise(raw)

	for _, timer := range []string{"setTimeout(", "setInterval("} {
		if strings.Contains(source, timer) {
			t.Errorf("traces.js uses %s", timer)
		}
	}
	for _, forbidden := range []string{"fetch(", "/v1/", "policy_reason", "innerHTML", "localStorage"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("traces.js contains %q", forbidden)
		}
	}

	// Every awaited read checks ownership before it writes.
	awaits := regexp.MustCompile(`await [^;]*;`).FindAllStringIndex(source, -1)
	if len(awaits) < 2 {
		t.Fatalf("found %d awaited reads; this guard would not be meaningful", len(awaits))
	}
	for _, at := range awaits {
		next := source[at[1]:min(at[1]+120, len(source))]
		if !strings.Contains(next, "Surface.owns(ticket)") && !strings.Contains(source[at[0]:at[1]], "clipboard") {
			t.Errorf("an awaited read in traces.js is not followed by an ownership check: %s",
				source[at[0]:at[1]])
		}
	}

	// Keyboard: arrows move, Enter opens, Escape closes and focus returns.
	for _, want := range []string{
		`event.key === "Enter"`, `event.key === "Escape"`, "nextIndex(", "closePanel(true)",
		`"role", "listbox"`, `"role", "option"`, `"aria-selected"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("traces.js lacks %s", want)
		}
	}

	// It never claims to be a complete trace, and says what is not retained.
	if !strings.Contains(raw, "not the complete distributed trace") {
		t.Error("the waterfall does not say it is not the complete distributed trace")
	}
	shell := readAsset(t, "index.html")
	if !strings.Contains(shell, "this is not a complete distributed trace") {
		t.Error("the Traces view does not state its limits")
	}

	// Selecting a row redraws nothing: select() only moves classes and focus.
	sel := wholeFunctionBodyForTest(t, "views/traces.js", "function select(")
	for _, redraw := range []string{"drawTrace(", "drawList(", "clear("} {
		if strings.Contains(sel, redraw) {
			t.Errorf("select() calls %s; selecting a row must not redraw the list or the waterfall", redraw)
		}
	}
}
