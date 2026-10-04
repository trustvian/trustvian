package webui

// Task 099: the Overview.
//
// The reductions are tested as arithmetic under node; the controller is held
// to the same ownership and no-timer rules as every other surface by reading
// its source, since it builds DOM a node driver has no document for.

import (
	"regexp"
	"strings"
	"testing"
)

const overviewDriver = `
import * as m from "./views/overview-model.js";
const runs = [
  { id: "a", status: "completed", created_at: "2026-10-04T09:00:00.5Z" },
  { id: "b", status: "running", created_at: "2026-10-04T09:00:00.25Z" },
  { id: "c", status: "completed", created_at: "2026-10-04T09:00:01Z" },
  { id: "d", status: "paused", created_at: "" },
  { id: "e", status: "", created_at: "2026-10-04T09:00:01Z" },
];
const out = {
  breakdown: m.statusBreakdown(runs),
  emptyBreakdown: m.statusBreakdown([]),
  newest: m.newestFirst(runs).map((r) => r.id),
  compare: [
    m.compareDecimal("9", "10"), m.compareDecimal("10", "9"), m.compareDecimal("18446744073709551615", "18446744073709551614"),
    m.compareDecimal("5", "5"), m.compareDecimal("x", "5"), m.compareDecimal("5", "x"),
  ],
  ratio: {
    half: m.decimalRatio("5", "10"),
    full: m.decimalRatio("10", "10"),
    zeroMax: m.decimalRatio("3", "0"),
    bad: m.decimalRatio("3.5", "10"),
    huge: m.decimalRatio("9223372036854775807", "18446744073709551615"),
    tiny: m.decimalRatio("1", "18446744073709551615"),
  },
  top: m.topBehaviors([
    { fingerprint_id: "fp-b", observations: "9" },
    { fingerprint_id: "fp-a", observations: "10" },
    { fingerprint_id: "fp-c", observations: "9" },
    { fingerprint_id: "fp-d", observations: "18446744073709551615" },
  ], 3).map((r) => r.fingerprint_id),
  tally: m.verdictTally([{ outcome: "accepted" }, { outcome: "rejected" }, { outcome: "accepted" }, {}]),
  clock: { never: m.readClock(0), at: m.readClock(new Date(2026, 9, 4, 9, 5, 7).getTime()) },
};
process.stdout.write(JSON.stringify(out));
`

func TestOverviewReductionsStateTheirScopeAndInventNothing(t *testing.T) {
	type entry struct {
		Status  string
		Outcome string
		Count   int
	}
	var got struct {
		Breakdown struct {
			Entries []entry
			Total   int
		}
		EmptyBreakdown struct {
			Entries []entry
			Total   int
		}
		Newest  []string
		Compare []int
		Ratio   map[string]float64
		Top     []string
		Tally   []entry
		Clock   map[string]string
	}
	decodeDriver(t, runDriver(t, overviewDriver), &got)

	// Lifecycle order first, unknown statuses kept under their own word, an
	// absent status named as absent — and no zero rows for statuses nothing
	// reported.
	wantBreakdown := []entry{
		{Status: "running", Count: 1}, {Status: "completed", Count: 2},
		{Status: "paused", Count: 1}, {Status: "not reported", Count: 1},
	}
	if got.Breakdown.Total != 5 || len(got.Breakdown.Entries) != len(wantBreakdown) {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
	for i, want := range wantBreakdown {
		if got.Breakdown.Entries[i].Status != want.Status || got.Breakdown.Entries[i].Count != want.Count {
			t.Errorf("breakdown[%d] = %+v, want %+v", i, got.Breakdown.Entries[i], want)
		}
	}
	if got.EmptyBreakdown.Total != 0 || len(got.EmptyBreakdown.Entries) != 0 {
		t.Errorf("an empty page produced figures: %+v", got.EmptyBreakdown)
	}

	// Parsed time, not text order (".5Z" sorts after "1Z" as text); ties by
	// identifier; a run with no creation time last.
	if strings.Join(got.Newest, ",") != "c,e,a,b,d" {
		t.Errorf("newestFirst = %v, want [c e a b d]", got.Newest)
	}

	wantCompare := []int{-1, 1, 1, 0, -1, 1}
	for i, want := range wantCompare {
		if sign(got.Compare[i]) != want {
			t.Errorf("compareDecimal case %d = %d, want sign %d", i, got.Compare[i], want)
		}
	}

	if got.Ratio["half"] != 0.5 || got.Ratio["full"] != 1 || got.Ratio["zeroMax"] != 0 || got.Ratio["bad"] != 0 {
		t.Errorf("decimalRatio basics = %v", got.Ratio)
	}
	if r := got.Ratio["huge"]; r < 0.499 || r > 0.501 {
		t.Errorf("decimalRatio of 2^63-1 over 2^64-1 = %v, want about 0.5", r)
	}
	if got.Ratio["tiny"] != 0 {
		t.Errorf("decimalRatio of 1 over 2^64-1 = %v, want 0 at drawing precision", got.Ratio["tiny"])
	}

	if strings.Join(got.Top, ",") != "fp-d,fp-a,fp-b" {
		t.Errorf("topBehaviors = %v, want [fp-d fp-a fp-b]", got.Top)
	}

	tally := map[string]int{}
	for _, e := range got.Tally {
		tally[e.Outcome] = e.Count
	}
	if tally["accepted"] != 2 || tally["rejected"] != 1 || tally["not reported"] != 1 || len(tally) != 3 {
		t.Errorf("verdictTally = %v", got.Tally)
	}

	if got.Clock["never"] != "not read yet" || got.Clock["at"] != "read at 09:05:07" {
		t.Errorf("readClock = %v", got.Clock)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// TestOverviewOwnsEveryReadAndRunsNoTimer holds the controller to the rules
// every surface follows.
func TestOverviewOwnsEveryReadAndRunsNoTimer(t *testing.T) {
	source := stripJSNoise(readAsset(t, "views/overview.js"))

	for _, timer := range []string{"setTimeout(", "setInterval(", "requestAnimationFrame("} {
		if strings.Contains(source, timer) {
			t.Errorf("overview.js uses %s; freshness is a read time and a Refresh control", timer)
		}
	}
	for _, forbidden := range []string{"fetch(", "/v1/", "localStorage"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("overview.js contains %q; it reads through injected calls only", forbidden)
		}
	}

	// Every awaited read is followed by an ownership check before anything
	// is written, so a late answer for a previous run or project is dropped.
	awaits := regexp.MustCompile(`await [^;]*;`).FindAllStringIndex(source, -1)
	if len(awaits) < 2 {
		t.Fatalf("found %d awaited reads; this guard would not be meaningful", len(awaits))
	}
	for _, at := range awaits {
		next := source[at[1]:min(at[1]+160, len(source))]
		if !strings.Contains(next, "Surface.owns(ticket)") {
			t.Errorf("an awaited read in overview.js is not followed by an ownership check:\n%s",
				source[at[0]:at[1]])
		}
	}

	// No aggregate the server did not compute.
	raw := strings.ToLower(readAsset(t, "views/overview.js"))
	for _, invented := range []string{"health score\"", "risk score\"", "\"healthy\"", "\"unhealthy\"", "\"safe\"", "\"unsafe\""} {
		if strings.Contains(raw, invented) {
			t.Errorf("overview.js renders %s; the console shows the server's figures, not a verdict of its own", invented)
		}
	}
	if !strings.Contains(raw, `const not_available = "not available"`) {
		t.Error("overview.js has no explicit not-available wording; an absent figure must not render as 0")
	}
}

// TestOverviewIsADestinationButNotTheLandingView pins where it sits.
func TestOverviewIsADestinationButNotTheLandingView(t *testing.T) {
	shell := readAsset(t, "index.html")
	if !strings.Contains(shell, `id="nav-overview" data-view="view-overview"`) {
		t.Error("no Overview destination in the sidebar")
	}
	if !regexp.MustCompile(`<section class="view" id="view-overview" data-nav="nav-overview" hidden>`).MatchString(shell) {
		t.Error("the Overview view is missing or visible on load; Live stays the landing view")
	}
	for _, panel := range []string{"overview-live", "overview-runs", "overview-evidence", "overview-verdicts", "overview-environments"} {
		if !strings.Contains(shell, `id="`+panel+`"`) {
			t.Errorf("no %s panel", panel)
		}
	}
}
