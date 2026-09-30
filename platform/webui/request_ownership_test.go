package webui

// Regression tests for three state defects found in review, and for the
// ownership rule that prevents the class they belong to.
//
// These drive the shipped modules under node with real, deliberately
// out-of-order promises, rather than asserting that the source contains a
// particular string. That distinction is the point: every one of these
// defects was invisible to a source scan because the code looked correct —
// what was wrong was which of two responses arrived last, and what a boolean
// meant after the reader had moved.
//
// The state is testable this way because it was extracted for that reason.
// `views/run-state.js` and `views/project-scope.js` touch no DOM and issue no
// requests: they hold what a surface knows and decide whether a response is
// still wanted, so a test can hold both ends of the race.

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// Finding 1 — run-scoped state survived a change of run
// ---------------------------------------------------------------------

// TestOpeningAnotherRunClearsEveryRunScopedField is the defect, exactly.
//
// Opening run B after viewing run A's Behaviors tab left `behaviors.loaded`
// true, so the tab rendered A's rows and fetched nothing. The same applied to
// the paging cursor, which would have sent A's continuation in a request for
// B, and to the narrowing, the selection and the strip's counts.
func TestOpeningAnotherRunClearsEveryRunScopedField(t *testing.T) {
	const driver = `
import { createRunState } from "./views/run-state.js";

const s = createRunState();
s.openRun("run-a");

// Populate everything a run's workspace can hold.
s.commitDetail(s.beginRun(), { id: "run-a", status: "completed" });
s.commitProgress(s.beginRun(), { record_count: "6" });
s.commitBehaviors(s.beginBehaviors(), {
  behaviors: [{ fingerprint_id: "fp-a" }],
  next_after: "cursor-a",
});
s.commitObservations(s.beginObservations(), {
  observations: [{ sequence: "1" }],
  next_after: "obs-cursor-a",
  history_state: "complete",
  retained_count: "6",
});
s.select("1");
s.narrow("traceID", "trace-a");

const before = {
  behaviorsLoaded: s.behaviors.loaded,
  behaviorRows: s.behaviors.rows.length,
  behaviorCursor: s.behaviors.nextAfter,
};

s.openRun("run-b");

console.log(JSON.stringify({
  before,
  after: {
    runID: s.runID,
    detail: s.detail,
    progress: s.progress,
    behaviorsLoaded: s.behaviors.loaded,
    behaviorRows: s.behaviors.rows.length,
    behaviorCursor: s.behaviors.nextAfter,
    observationsLoaded: s.observations.loaded,
    observationRows: s.observations.rows.length,
    observationCursor: s.observations.nextAfter,
    observationRetained: s.observations.retained,
    selected: s.selected,
    scope: s.scope,
  },
}));
`
	var got struct {
		Before struct {
			BehaviorsLoaded bool   `json:"behaviorsLoaded"`
			BehaviorRows    int    `json:"behaviorRows"`
			BehaviorCursor  string `json:"behaviorCursor"`
		} `json:"before"`
		After struct {
			RunID               string `json:"runID"`
			Detail              any    `json:"detail"`
			Progress            any    `json:"progress"`
			BehaviorsLoaded     bool   `json:"behaviorsLoaded"`
			BehaviorRows        int    `json:"behaviorRows"`
			BehaviorCursor      string `json:"behaviorCursor"`
			ObservationsLoaded  bool   `json:"observationsLoaded"`
			ObservationRows     int    `json:"observationRows"`
			ObservationCursor   string `json:"observationCursor"`
			ObservationRetained string `json:"observationRetained"`
			Selected            string `json:"selected"`
			Scope               struct {
				SessionID     string `json:"sessionID"`
				TraceID       string `json:"traceID"`
				FingerprintID string `json:"fingerprintID"`
			} `json:"scope"`
		} `json:"after"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	// The setup has to have worked, or everything below passes vacuously.
	if !got.Before.BehaviorsLoaded || got.Before.BehaviorRows != 1 || got.Before.BehaviorCursor != "cursor-a" {
		t.Fatalf("run A's behaviors were not populated: %+v", got.Before)
	}

	if got.After.RunID != "run-b" {
		t.Errorf("run id = %q, want run-b", got.After.RunID)
	}
	if got.After.BehaviorsLoaded {
		t.Error("behaviors are still marked loaded after opening another run; the " +
			"tab would render the previous run's rows and fetch nothing")
	}
	if got.After.BehaviorRows != 0 {
		t.Errorf("%d behavior rows survived the run change", got.After.BehaviorRows)
	}
	if got.After.BehaviorCursor != "" {
		t.Errorf("behavior cursor = %q; the next page of the previous run's "+
			"history would be requested for this one", got.After.BehaviorCursor)
	}
	if got.After.ObservationsLoaded || got.After.ObservationRows != 0 || got.After.ObservationCursor != "" {
		t.Errorf("observation state survived the run change: %+v", got.After)
	}
	if got.After.ObservationRetained != "" {
		t.Errorf("retained count = %q; the strip would report the previous run's "+
			"total", got.After.ObservationRetained)
	}
	if got.After.Selected != "" {
		t.Errorf("selected row = %q; a row of the previous run's table stays marked",
			got.After.Selected)
	}
	if got.After.Scope.TraceID != "" || got.After.Scope.SessionID != "" || got.After.Scope.FingerprintID != "" {
		t.Errorf("the narrowing survived the run change: %+v", got.After.Scope)
	}
	if got.After.Detail != nil || got.After.Progress != nil {
		t.Error("the run record or its progress survived the run change; the strip " +
			"would show the previous run's status and counts")
	}
}

// ---------------------------------------------------------------------
// Finding 2 — a stale response could overwrite the current view
// ---------------------------------------------------------------------

// TestALateResponseForTheRunLeftBehindIsDiscarded resolves out of order.
//
// A's read is started, the reader opens B, B's read starts and finishes, and
// only then does A's land. A is the response that arrives last, so without
// ownership it is the one that wins.
func TestALateResponseForTheRunLeftBehindIsDiscarded(t *testing.T) {
	const driver = `
import { createRunState } from "./views/run-state.js";

// A promise whose settling this test controls, so "arrives last" is a fact
// rather than a timing hope.
function deferred() {
  let settle;
  const promise = new Promise((resolve) => { settle = resolve; });
  return { promise, settle };
}

const s = createRunState();
const slowA = deferred();
const fastB = deferred();

s.openRun("run-a");
const ticketA = s.beginBehaviors();
const readA = slowA.promise.then((r) => s.commitBehaviors(ticketA, r));

s.openRun("run-b");
const ticketB = s.beginBehaviors();
const readB = fastB.promise.then((r) => s.commitBehaviors(ticketB, r));

// B first...
fastB.settle({ behaviors: [{ fingerprint_id: "fp-b" }], next_after: "" });
const committedB = await readB;
const afterB = s.behaviors.rows.map((row) => row.fingerprint_id);

// ...then A, late.
slowA.settle({ behaviors: [{ fingerprint_id: "fp-a" }], next_after: "cursor-a" });
const committedA = await readA;

console.log(JSON.stringify({
  committedB,
  committedA,
  afterB,
  final: s.behaviors.rows.map((row) => row.fingerprint_id),
  finalCursor: s.behaviors.nextAfter,
  runID: s.runID,
}));
`
	var got struct {
		CommittedB  bool     `json:"committedB"`
		CommittedA  bool     `json:"committedA"`
		AfterB      []string `json:"afterB"`
		Final       []string `json:"final"`
		FinalCursor string   `json:"finalCursor"`
		RunID       string   `json:"runID"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if !got.CommittedB {
		t.Fatal("run B's own response was refused; the guard is too strict to be useful")
	}
	if got.CommittedA {
		t.Error("a response for the run the reader left was committed")
	}
	if len(got.Final) != 1 || got.Final[0] != "fp-b" {
		t.Errorf("behaviors on screen = %v, want run B's; A's response arrived last "+
			"and overwrote the current run", got.Final)
	}
	if got.FinalCursor != "" {
		t.Errorf("cursor = %q; the next page would continue run A's collection "+
			"under run B", got.FinalCursor)
	}
}

// TestAResponseForASupersededFilterIsDiscarded is the same rule for a
// narrowing, where the run has not changed.
//
// Following a trace while the unnarrowed page is still arriving must not end
// with the unnarrowed rows under a "Trace: …" chip.
func TestAResponseForASupersededFilterIsDiscarded(t *testing.T) {
	const driver = `
import { createRunState } from "./views/run-state.js";

function deferred() {
  let settle;
  const promise = new Promise((resolve) => { settle = resolve; });
  return { promise, settle };
}

const s = createRunState();
s.openRun("run-a");

const wholeRun = deferred();
const ticketWhole = s.beginObservations();
const readWhole = wholeRun.promise.then((r) => s.commitObservations(ticketWhole, r));

// The reader follows a trace while the unnarrowed page is outstanding.
s.narrow("traceID", "trace-1");
const narrowed = deferred();
const ticketNarrow = s.beginObservations();
const readNarrow = narrowed.promise.then((r) => s.commitObservations(ticketNarrow, r));

narrowed.settle({ observations: [{ sequence: "2" }], next_after: "" });
const committedNarrow = await readNarrow;

wholeRun.settle({ observations: [{ sequence: "1" }, { sequence: "2" }], next_after: "w" });
const committedWhole = await readWhole;

console.log(JSON.stringify({
  committedNarrow,
  committedWhole,
  rows: s.observations.rows.map((row) => row.sequence),
  cursor: s.observations.nextAfter,
  scope: s.scope,
}));
`
	var got struct {
		CommittedNarrow bool     `json:"committedNarrow"`
		CommittedWhole  bool     `json:"committedWhole"`
		Rows            []string `json:"rows"`
		Cursor          string   `json:"cursor"`
		Scope           struct {
			TraceID string `json:"traceID"`
		} `json:"scope"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if !got.CommittedNarrow {
		t.Fatal("the narrowed page was refused; the guard is too strict")
	}
	if got.CommittedWhole {
		t.Error("the unnarrowed response was committed after the reader narrowed; " +
			"the table would show the whole run under a filter chip")
	}
	if len(got.Rows) != 1 || got.Rows[0] != "2" {
		t.Errorf("rows = %v, want only the narrowed page's", got.Rows)
	}
	if got.Cursor != "w" && got.Cursor != "" {
		t.Errorf("cursor = %q; paging would continue the unnarrowed collection", got.Cursor)
	}
	if got.Scope.TraceID != "trace-1" {
		t.Errorf("narrowing = %q, want trace-1", got.Scope.TraceID)
	}
}

// TestASupersededResponseLeavesTheLoadingIndicatorAlone is the subtler half.
//
// Two reads overlap on one surface. The older one finishing must not clear
// the flag the newer one raised, or the skeleton disappears while the newer
// read is still outstanding and the table reads as empty.
func TestASupersededResponseLeavesTheLoadingIndicatorAlone(t *testing.T) {
	const driver = `
import { createRunState } from "./views/run-state.js";

const s = createRunState();
s.openRun("run-a");

const older = s.beginObservations();
const newer = s.beginObservations();
const raised = s.observations.loading;

// The older read completes — successfully in one case, by failing in the
// other. Neither may touch the flag.
const olderCommitted = s.commitObservations(older, { observations: [{ sequence: "9" }] });
const loadingAfterOlderSuccess = s.observations.loading;
const olderFailed = s.failObservations(older);
const loadingAfterOlderFailure = s.observations.loading;
const rowsAfterOlder = s.observations.rows.length;

const newerCommitted = s.commitObservations(newer, { observations: [{ sequence: "1" }] });
const loadingAfterNewer = s.observations.loading;

console.log(JSON.stringify({
  raised,
  olderCommitted,
  olderFailed,
  loadingAfterOlderSuccess,
  loadingAfterOlderFailure,
  rowsAfterOlder,
  newerCommitted,
  loadingAfterNewer,
}));
`
	var got struct {
		Raised                   bool `json:"raised"`
		OlderCommitted           bool `json:"olderCommitted"`
		OlderFailed              bool `json:"olderFailed"`
		LoadingAfterOlderSuccess bool `json:"loadingAfterOlderSuccess"`
		LoadingAfterOlderFailure bool `json:"loadingAfterOlderFailure"`
		RowsAfterOlder           int  `json:"rowsAfterOlder"`
		NewerCommitted           bool `json:"newerCommitted"`
		LoadingAfterNewer        bool `json:"loadingAfterNewer"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if !got.Raised {
		t.Fatal("beginning a read did not raise the loading flag")
	}
	if got.OlderCommitted {
		t.Error("a superseded read committed its rows")
	}
	if got.RowsAfterOlder != 0 {
		t.Errorf("%d rows from a superseded read reached the table", got.RowsAfterOlder)
	}
	if !got.LoadingAfterOlderSuccess {
		t.Error("a superseded read cleared the loading flag on success; the newer " +
			"read's skeleton would vanish while it was still outstanding")
	}
	if got.OlderFailed {
		t.Error("a superseded read was allowed to report a failure")
	}
	if !got.LoadingAfterOlderFailure {
		t.Error("a superseded read cleared the loading flag by failing; a newer " +
			"read's skeleton would vanish, and its error would be replaced")
	}
	if !got.NewerCommitted || got.LoadingAfterNewer {
		t.Error("the current read could not commit or could not clear its own flag")
	}
}

// TestRunSurfacesDoNotInvalidateEachOther keeps independent reads independent.
//
// Opening a run's behaviors must not abandon an observation page that is
// still wanted, and neither must abandon the run record itself. Three reads
// of one run are three surfaces.
func TestRunSurfacesDoNotInvalidateEachOther(t *testing.T) {
	const driver = `
import { createRunState } from "./views/run-state.js";

const s = createRunState();
s.openRun("run-a");

const observations = s.beginObservations();
const behaviors = s.beginBehaviors();
const run = s.beginRun();

// Each surface still owns its own outstanding ticket after the others
// started, and each can commit independently.
console.log(JSON.stringify({
  ownsObservations: s.ownsObservations(observations),
  ownsBehaviors: s.ownsBehaviors(behaviors),
  ownsRun: s.ownsRun(run),
  committedObservations: s.commitObservations(observations, { observations: [{ sequence: "1" }] }),
  behaviorsStillLoading: s.behaviors.loading,
  committedBehaviors: s.commitBehaviors(behaviors, { behaviors: [{ fingerprint_id: "fp" }] }),
  committedRun: s.commitDetail(run, { id: "run-a" }),
}));
`
	var got map[string]bool
	decodeDriver(t, runDriver(t, driver), &got)

	for _, key := range []string{
		"ownsObservations", "ownsBehaviors", "ownsRun",
		"committedObservations", "behaviorsStillLoading",
		"committedBehaviors", "committedRun",
	} {
		if !got[key] {
			t.Errorf("%s is false; starting a read on one surface interfered with "+
				"another. Three reads of one run are three independent surfaces", key)
		}
	}
}

// TestAStalePageNeverReachesTheHierarchy covers the level browser.
//
// The guard has to live inside the browser rather than in its caller: the
// assignment to `levels` happens inside the awaited method, so a check after
// the call would run after the clobber it was meant to prevent.
func TestAStalePageNeverReachesTheHierarchy(t *testing.T) {
	const driver = `
import { HierarchyBrowser } from "./v1/discovery.js";

function deferred() {
  let settle;
  const promise = new Promise((resolve) => { settle = resolve; });
  return { promise, settle };
}

const slowA = deferred();
const fastB = deferred();
let asked = [];

const browser = new HierarchyBrowser({
  listProjects: () => Promise.resolve({ projects: [] }),
  listProjectAgents: (projectID) => {
    asked.push(projectID);
    return projectID === "project-a" ? slowA.promise : fastB.promise;
  },
  listAgentCandidates: () => Promise.resolve({ candidates: [] }),
  listCandidateRuns: () => Promise.resolve({ evaluation_runs: [] }),
});

const readA = browser.loadAgents("project-a", "");
// The reader switches project; everything outstanding belongs to the old one.
browser.invalidate();
const readB = browser.loadAgents("project-b", "");

fastB.settle({ agents: [{ id: "agent-b" }], next_after: "" });
const resultB = await readB;
const afterB = browser.levels.agents.rows.map((row) => row.id);

slowA.settle({ agents: [{ id: "agent-a" }], next_after: "cursor-a" });
const resultA = await readA;

console.log(JSON.stringify({
  asked,
  supersededReturnedNull: resultA === null,
  currentReturnedAPage: resultB !== null,
  afterB,
  final: browser.levels.agents.rows.map((row) => row.id),
  finalCursor: browser.levels.agents.nextAfter,
  parent: browser.parents.agents,
}));
`
	var got struct {
		Asked                  []string `json:"asked"`
		SupersededReturnedNull bool     `json:"supersededReturnedNull"`
		CurrentReturnedAPage   bool     `json:"currentReturnedAPage"`
		AfterB                 []string `json:"afterB"`
		Final                  []string `json:"final"`
		FinalCursor            string   `json:"finalCursor"`
		Parent                 string   `json:"parent"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if len(got.Asked) != 2 {
		t.Fatalf("the browser made %d reads, want 2", len(got.Asked))
	}
	if !got.CurrentReturnedAPage {
		t.Fatal("the current read returned nothing; the guard is too strict")
	}
	if !got.SupersededReturnedNull {
		t.Error("a superseded read returned a page rather than null")
	}
	if len(got.Final) != 1 || got.Final[0] != "agent-b" {
		t.Errorf("levels.agents = %v, want the current project's; the late page "+
			"reached the browser", got.Final)
	}
	if got.FinalCursor != "" {
		t.Errorf("cursor = %q; the previous project's continuation would be "+
			"followed under this one", got.FinalCursor)
	}
	if got.Parent != "project-b" {
		t.Errorf("parent = %q, want project-b", got.Parent)
	}
}

// ---------------------------------------------------------------------
// Finding 3 — project-scoped state survived a change of project
// ---------------------------------------------------------------------

// TestChangingProjectClearsCompareAndPromotions is the defect.
//
// Both destinations decided whether to fetch by asking "have I loaded
// before?". That is still true after a project change, so project B showed
// project A's agents, A's assigned comparison sides and A's promotion
// history, under B's name in the sidebar.
func TestChangingProjectClearsCompareAndPromotions(t *testing.T) {
	const driver = `
import { createProjectScope } from "./views/project-scope.js";

const s = createProjectScope();
s.setProject("project-a");

s.commitCompare(s.beginCompare("agents"));
s.setAgent("agent-a");
s.setCandidate("candidate-a");
s.assignSide("reference", { id: "run-a-1" });
s.assignSide("candidate", { id: "run-a-2" });
s.commitPromotions(s.beginPromotions(), "promo-cursor-a", 3);

const before = {
  needsCompare: s.needsCompare(),
  needsPromotions: s.needsPromotions(),
  ready: s.comparisonReady(),
  cursor: s.promotionCursor,
};

const changed = s.setProject("project-b");

console.log(JSON.stringify({
  before,
  changed,
  sameProjectIsNotAChange: s.setProject("project-b"),
  after: {
    project: s.project,
    agent: s.agent,
    candidate: s.candidate,
    reference: s.sides.reference,
    candidateSide: s.sides.candidate,
    ready: s.comparisonReady(),
    promotionCursor: s.promotionCursor,
    promotionPageNumber: s.promotionPageNumber,
    needsCompare: s.needsCompare(),
    needsPromotions: s.needsPromotions(),
  },
}));
`
	var got struct {
		Before struct {
			NeedsCompare    bool   `json:"needsCompare"`
			NeedsPromotions bool   `json:"needsPromotions"`
			Ready           bool   `json:"ready"`
			Cursor          string `json:"cursor"`
		} `json:"before"`
		Changed                 bool `json:"changed"`
		SameProjectIsNotAChange bool `json:"sameProjectIsNotAChange"`
		After                   struct {
			Project             string `json:"project"`
			Agent               string `json:"agent"`
			Candidate           string `json:"candidate"`
			Reference           any    `json:"reference"`
			CandidateSide       any    `json:"candidateSide"`
			Ready               bool   `json:"ready"`
			PromotionCursor     string `json:"promotionCursor"`
			PromotionPageNumber int    `json:"promotionPageNumber"`
			NeedsCompare        bool   `json:"needsCompare"`
			NeedsPromotions     bool   `json:"needsPromotions"`
		} `json:"after"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if got.Before.NeedsCompare || got.Before.NeedsPromotions || !got.Before.Ready ||
		got.Before.Cursor != "promo-cursor-a" {
		t.Fatalf("project A was not populated: %+v", got.Before)
	}
	if !got.Changed {
		t.Error("changing the project did not report a change")
	}
	if got.SameProjectIsNotAChange {
		t.Error("selecting the same project again reported a change; re-rendering " +
			"the sidebar would discard a reader's comparison")
	}
	if got.After.Agent != "" || got.After.Candidate != "" {
		t.Errorf("the chosen agent or candidate survived the project change: %+v", got.After)
	}
	if got.After.Reference != nil || got.After.CandidateSide != nil || got.After.Ready {
		t.Error("an assigned comparison side survived the project change; the run " +
			"it names does not belong to the project now on screen")
	}
	if got.After.PromotionCursor != "" || got.After.PromotionPageNumber != 0 {
		t.Errorf("promotion paging survived the project change: cursor %q, page %d",
			got.After.PromotionCursor, got.After.PromotionPageNumber)
	}
	if !got.After.NeedsCompare {
		t.Error("Compare reports it is populated for the new project; it holds the " +
			"previous project's agents and would fetch nothing")
	}
	if !got.After.NeedsPromotions {
		t.Error("Promotions reports it is populated for the new project")
	}
}

// TestALateResponseForThePreviousProjectIsDiscarded closes the same race one
// level up: not "is the cache stale" but "may this response repopulate it".
func TestALateResponseForThePreviousProjectIsDiscarded(t *testing.T) {
	const driver = `
import { createProjectScope } from "./views/project-scope.js";

const s = createProjectScope();
s.setProject("project-a");

// Reads for A are outstanding when the reader switches.
const compareA = s.beginCompare("agents");
const promotionsA = s.beginPromotions();

s.setProject("project-b");

const compareB = s.beginCompare("agents");
const promotionsB = s.beginPromotions();

console.log(JSON.stringify({
  oldCompareAccepted: s.commitCompare(compareA),
  oldCompareFailAccepted: s.failCompare(compareA),
  oldPromotionsAccepted: s.commitPromotions(promotionsA, "cursor-a", 7),
  stillNeedsCompare: s.needsCompare(),
  stillNeedsPromotions: s.needsPromotions(),
  cursorAfterOldResponse: s.promotionCursor,
  pageAfterOldResponse: s.promotionPageNumber,
  newCompareAccepted: s.commitCompare(compareB),
  newPromotionsAccepted: s.commitPromotions(promotionsB, "cursor-b", 1),
  cursorAfterNewResponse: s.promotionCursor,
  needsCompareAfterNew: s.needsCompare(),
}));
`
	var got struct {
		OldCompareAccepted     bool   `json:"oldCompareAccepted"`
		OldCompareFailAccepted bool   `json:"oldCompareFailAccepted"`
		OldPromotionsAccepted  bool   `json:"oldPromotionsAccepted"`
		StillNeedsCompare      bool   `json:"stillNeedsCompare"`
		StillNeedsPromotions   bool   `json:"stillNeedsPromotions"`
		CursorAfterOldResponse string `json:"cursorAfterOldResponse"`
		PageAfterOldResponse   int    `json:"pageAfterOldResponse"`
		NewCompareAccepted     bool   `json:"newCompareAccepted"`
		NewPromotionsAccepted  bool   `json:"newPromotionsAccepted"`
		CursorAfterNewResponse string `json:"cursorAfterNewResponse"`
		NeedsCompareAfterNew   bool   `json:"needsCompareAfterNew"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if got.OldCompareAccepted || got.OldCompareFailAccepted {
		t.Error("a Compare response for the previous project was accepted")
	}
	if got.OldPromotionsAccepted {
		t.Error("a promotion page for the previous project was accepted")
	}
	if !got.StillNeedsCompare || !got.StillNeedsPromotions {
		t.Error("a late response for the previous project marked the new project's " +
			"destinations as populated, so neither would fetch")
	}
	if got.CursorAfterOldResponse != "" || got.PageAfterOldResponse != 0 {
		t.Errorf("the previous project's paging was restored: cursor %q, page %d",
			got.CursorAfterOldResponse, got.PageAfterOldResponse)
	}
	if !got.NewCompareAccepted || !got.NewPromotionsAccepted {
		t.Fatal("the new project's own responses were refused; the guard is too strict")
	}
	if got.CursorAfterNewResponse != "cursor-b" || got.NeedsCompareAfterNew {
		t.Error("the new project's response did not take effect")
	}
}

// TestCompareLevelsAndPromotionsAreIndependent keeps one surface from
// abandoning another's work.
//
// Compare's three lists are three reads. Asking for a candidate's runs must
// not strand the skeleton over the agent list, and listing promotions must
// not abandon either.
func TestCompareLevelsAndPromotionsAreIndependent(t *testing.T) {
	const driver = `
import { createProjectScope } from "./views/project-scope.js";

const s = createProjectScope();
s.setProject("project-a");

const agents = s.beginCompare("agents");
const runs = s.beginCompare("runs");
const promotions = s.beginPromotions();
const bothRaised = s.compareLoading.agents && s.compareLoading.runs && s.promotionLoading;

// Each completes on its own terms, in an order none of them chose.
const runsCommitted = s.commitCompare(runs);
const agentsStillLoading = s.compareLoading.agents;
const agentsCommitted = s.commitCompare(agents);
const promotionsCommitted = s.commitPromotions(promotions, "", 1);

console.log(JSON.stringify({
  bothRaised,
  runsCommitted,
  agentsStillLoading,
  agentsCommitted,
  promotionsCommitted,
  agentsLoadingAtEnd: s.compareLoading.agents,
  runsLoadingAtEnd: s.compareLoading.runs,
  promotionLoadingAtEnd: s.promotionLoading,
}));
`
	var got map[string]bool
	decodeDriver(t, runDriver(t, driver), &got)

	for _, key := range []string{
		"bothRaised", "runsCommitted", "agentsStillLoading",
		"agentsCommitted", "promotionsCommitted",
	} {
		if !got[key] {
			t.Errorf("%s is false; a read on one surface interfered with another", key)
		}
	}
	for _, key := range []string{"agentsLoadingAtEnd", "runsLoadingAtEnd", "promotionLoadingAtEnd"} {
		if got[key] {
			t.Errorf("%s is still true; a completed read did not clear its own flag", key)
		}
	}
}

// ---------------------------------------------------------------------
// The mechanism itself
// ---------------------------------------------------------------------

// TestOwnershipDoesNotDependOnCancellation states the design choice.
//
// An abort is a request to stop that the network may decline, and it can lose
// the race with a response already queued. A surface whose correctness rested
// on the abort winning would be right almost always, which is the worst place
// for a correctness property to be. Aborting is worth doing on top; it cannot
// be the mechanism.
func TestOwnershipDoesNotDependOnCancellation(t *testing.T) {
	for _, name := range []string{"ownership.js", "run-state.js", "project-scope.js"} {
		source := stripJSNoise(readAsset(t, name))
		for _, forbidden := range []string{"AbortController", "AbortSignal", ".abort("} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s uses %s; a stale response must be refused on arrival, "+
					"not depended upon to never arrive", name, forbidden)
			}
		}
	}

	const driver = `
import { createSurface } from "./core/ownership.js";

const a = createSurface("a");
const b = createSurface("b");

const first = a.begin("run-1");
const second = a.begin("run-1");
const onB = b.begin("run-1");

a.retarget("run-2");
const afterRetarget = a.begin();
a.retarget("run-1");
const backAgain = a.begin("run-1");

console.log(JSON.stringify({
  supersededBySecond: a.owns(first),
  secondOwnedBeforeRetarget: false,
  otherSurfaceUnaffected: b.owns(onB),
  retargetAbandonedSecond: a.owns(second),
  currentIsOwned: a.owns(backAgain),
  oldTicketAfterReturning: a.owns(afterRetarget),
  nullIsNotOwned: a.owns(null),
  undefinedIsNotOwned: a.owns(undefined),
  contextKey: a.contextKey(),
}));
`
	var got struct {
		SupersededBySecond      bool   `json:"supersededBySecond"`
		OtherSurfaceUnaffected  bool   `json:"otherSurfaceUnaffected"`
		RetargetAbandonedSecond bool   `json:"retargetAbandonedSecond"`
		CurrentIsOwned          bool   `json:"currentIsOwned"`
		OldTicketAfterReturning bool   `json:"oldTicketAfterReturning"`
		NullIsNotOwned          bool   `json:"nullIsNotOwned"`
		UndefinedIsNotOwned     bool   `json:"undefinedIsNotOwned"`
		ContextKey              string `json:"contextKey"`
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if got.SupersededBySecond {
		t.Error("an older ticket on the same surface is still owned")
	}
	if !got.OtherSurfaceUnaffected {
		t.Error("work on one surface was invalidated by another; independent reads " +
			"must stay independent")
	}
	if got.RetargetAbandonedSecond {
		t.Error("retargeting did not abandon an outstanding ticket")
	}
	if !got.CurrentIsOwned {
		t.Error("the current ticket is not owned; nothing could ever commit")
	}
	if got.OldTicketAfterReturning {
		t.Error("a ticket issued before the subject changed is owned again after " +
			"returning to that subject; it is still a different request")
	}
	if got.NullIsNotOwned || got.UndefinedIsNotOwned {
		t.Error("a missing ticket was treated as owned")
	}
	if got.ContextKey != "run-1" {
		t.Errorf("context = %q, want run-1", got.ContextKey)
	}
}
