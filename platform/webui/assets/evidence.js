// The Evidence surface: from a finding to the behaviors to the observations,
// and from an observation into its session, trace and behavioral context.
//
// Task 076. What this file is, and what it deliberately is not:
//
//   - It **navigates** task 085's resolution and task 067's retained history. It
//     resolves nothing itself: no status, no count, no side, no verdict and no
//     sequence judgement is computed here. Every one of those is read from a
//     `/v1` response and rendered.
//   - It holds **one page at a time**. A continuation replaces what is on screen
//     rather than appending to it, so the memory a history costs is a page
//     however far you read — the same shape task 066's promotion history uses,
//     for the same reason.
//   - It **stores nothing**. No browser storage of any kind, and nothing about
//     which finding somebody was reading survives a reload. The control-plane
//     database is the only source of truth.
//
// The controller is separated from the DOM on purpose. Everything above the
// "Rendering" divider is testable without a browser, which is where the
// properties that actually go wrong live: that changing the finding resets the
// cursor, and that a response arriving after the question changed is discarded
// rather than drawn.

import {
  buildTraceTree,
  formatDuration,
  newBehaviorLabel,
  parentStateText,
  spanStatusClass,
  spanStatusLabel,
  FIDELITY_NOT_RETAINED,
  SEQUENCE_DEVIATION_NOT_RETAINED,
} from "./trace.js";
import * as render from "./render.js";

// ---------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------

// RUN_VIEWS is the five presentations of one run's retained history.
//
// Each names the narrowing it reads through, which is at most one — the server
// refuses more, and a view that wanted two would be a question task 076 does not
// ask. `sequence` and `timeline` read the unnarrowed page and differ only in what
// they show of it: one is about behavioral identity in order, the other about
// timing and outcome in order. They are separate views rather than one table
// with more columns because they answer different questions and a reader looking
// for one should not have to ignore the other.
export const RUN_VIEWS = Object.freeze([
  Object.freeze({
    key: "session",
    label: "Session actions",
    param: "sessionID",
    identifierLabel: "Session ID",
    blurb: "One bounded interaction's actions, in the order the platform accepted them.",
  }),
  Object.freeze({
    key: "trace",
    label: "Trace context",
    param: "traceID",
    identifierLabel: "Trace ID",
    blurb:
      "One invocation's actions and the parent/child structure recorded for them. " +
      "Structure is drawn from recorded parent span identity and from nothing else.",
  }),
  Object.freeze({
    key: "sequence",
    label: "Behavior sequence",
    param: "",
    identifierLabel: "",
    blurb: "The behavioral identities this run exhibited, in ingest order.",
  }),
  Object.freeze({
    key: "timeline",
    label: "Decision timeline",
    param: "",
    identifierLabel: "",
    blurb:
      "The decisions in order, with the duration and span status recorded for " +
      "each. Durations are per observation and are never summed into a latency.",
  }),
  Object.freeze({
    key: "behavior",
    label: "Behavior detail",
    param: "fingerprintID",
    identifierLabel: "Fingerprint ID",
    blurb: "One behavioral identity's retained observations inside this run.",
  }),
]);

// viewFor returns one view spec, or null for a key this page does not publish.
export function viewFor(key) {
  for (const view of RUN_VIEWS) {
    if (view.key === key) {
      return view;
    }
  }
  return null;
}

// ---------------------------------------------------------------------
// Server-stated facts, rendered
// ---------------------------------------------------------------------

// RESOLUTION_STATUS_TEXT explains each status task 085 publishes.
//
// The status is the server's; these are the sentences that keep the four apart
// on screen. Three of them look identical in a payload, and reading any of them
// as "no evidence" is the specific wrong answer a developer would act on.
const RESOLUTION_STATUS_TEXT = new Map([
  ["resolved", "This finding has supporting evidence in the retained history."],
  [
    "none_found",
    "No retained observation matches this finding, and this side's retained " +
      "history is complete — so that is a fact about the run.",
  ],
  [
    "indeterminate",
    "No retained observation matches, and this side's retained history is " +
      "partial or unavailable — so the absence establishes nothing either way.",
  ],
  [
    "aggregate_only",
    "This check counts an absence: it fails when a run observed too little. " +
      "There is no observation that caused it, so none is linked and none is " +
      "invented.",
  ],
]);

// statusSentence renders one status, or says the value is unrecognized.
export function statusSentence(status) {
  const key = typeof status === "string" ? status : "";
  if (RESOLUTION_STATUS_TEXT.has(key)) {
    return RESOLUTION_STATUS_TEXT.get(key);
  }
  return "The control plane reported a status this page does not recognize.";
}

// HISTORY_STATE_TEXT explains task 067's three retained-history states.
const HISTORY_STATE_TEXT = new Map([
  ["complete", "Every accepted record of this run is retained."],
  [
    "partial",
    "Some of this run's records are retained and some are not — it saturated " +
      "retention, or began before retention existed. Do not read this history " +
      "as the whole run.",
  ],
  [
    "unavailable",
    "This run has records and none of their history was retained. This is an " +
      "absence with a known cause, not an empty history.",
  ],
]);

// historySentence renders one retained-history state.
export function historySentence(state) {
  const key = typeof state === "string" ? state : "";
  if (HISTORY_STATE_TEXT.has(key)) {
    return HISTORY_STATE_TEXT.get(key);
  }
  return "The retained-history state was reported as a value this page does not recognize.";
}

// RECORDED_COUNT_CAVEAT is why a recorded count may exceed the rows beneath it.
//
// A block or critical-risk check counts every record the run ingested, from the
// aggregate; retained history is bounded and may be partial or absent. The two
// are different measurements, task 085 refuses to reconcile them, and a reader
// who thought they were the same number would read bounded retention as a
// discrepancy.
export const RECORDED_COUNT_CAVEAT =
  "The recorded count is what the evidence holds — it counts records the run " +
  "ingested. Retained history is bounded, so it may hold fewer. The two are " +
  "different measurements and are not reconciled.";

// EXHAUSTIVE_CAVEAT is what `exhaustive: false` means.
export const EXHAUSTIVE_CAVEAT =
  "Paging to the end would not yield every matching observation that ever " +
  "existed, because this side's retained history is not complete. What is here " +
  "is a sample.";

// AGGREGATE_ONLY_COUNT_NOTE describes the one number an aggregate-only
// resolution holds.
//
// Deliberately says nothing about retained history. RECORDED_COUNT_CAVEAT
// contrasts the recorded count with the rows beneath it, and an aggregate-only
// result has no rows and read no history to contrast it with.
export const AGGREGATE_ONLY_COUNT_NOTE =
  "The recorded count is the number this check counted, read from the run's " +
  "aggregate. It is the gate's own actual.";

// findingPresentation decides which blocks a resolution's header may render.
//
// **`aggregate_only` is an applicability statement, not a history statement.**
// The two minimum-count checks fail when a run observed *too little*, so the
// control plane reports that they have no per-observation evidence to link to
// and returns without reading any history at all. The history and exhaustiveness
// fields on that response are therefore the zero value — `unavailable`,
// `retained_count: "0"`, `complete: false`, `exhaustive: false` — and they
// describe nothing that was measured.
//
// Rendered unconditionally, those defaults said "this run has records and none
// of their history was retained" and "what is here is a sample" about runs whose
// retained history is complete. Both are claims the response never made. So the
// header asks this first, and shows only what the result established.
//
// Nothing here is a second query: not reading history for a check that has none
// to attribute is the control plane's decision, and asking for it separately
// just to fill a panel would add a read to produce a number that still would not
// describe this finding.
export function findingPresentation(response) {
  const status = response === null || response === undefined ? "" : response.status;
  if (status === "aggregate_only") {
    return Object.freeze({
      describesRetainedHistory: false,
      showHistory: false,
      showExhaustiveCaveat: false,
      showRows: false,
      countNote: AGGREGATE_ONLY_COUNT_NOTE,
    });
  }
  return Object.freeze({
    describesRetainedHistory: true,
    showHistory: true,
    // Only the observation resolution publishes `exhaustive`; the behaviors
    // route has no such field, so an absent one states nothing and shows
    // nothing.
    showExhaustiveCaveat: response.exhaustive === false,
    showRows: true,
    countNote: RECORDED_COUNT_CAVEAT,
  });
}

// PROVENANCE_UNSPECIFIED is how an unsupplied provenance field renders.
//
// Never blank, never a default, and never omitted. An unknown model is
// materially different from a model both sides shared, and a panel that could
// not tell them apart would let a reader assume the two sides matched.
export const PROVENANCE_UNSPECIFIED = "not stated";

// provenanceValue renders one supplied-or-not provenance field.
export function provenanceValue(value) {
  if (typeof value !== "string" || value === "") {
    return PROVENANCE_UNSPECIFIED;
  }
  return value;
}

// ---------------------------------------------------------------------
// Row values
//
// Pure projections of one server object onto the cells a view draws. Separated
// from the DOM so a test can assert that what reaches the screen is what the
// route returned, without a browser and without trusting the renderer.
// ---------------------------------------------------------------------

// behaviorDescriptorText summarizes a recorded descriptor, as recorded.
//
// Operation and target are printed exactly as the observation carried them. No
// semantic name is invented and no transport operation is relabelled as a tool
// or model call: task 075 refuses that for operation names and the retained row
// carries no fidelity field that could justify it here.
export function behaviorDescriptorText(behavior) {
  if (behavior === undefined || behavior === null || typeof behavior !== "object") {
    return "—";
  }
  const parts = [];
  for (const key of render.BEHAVIOR_FIELDS) {
    const value = behavior[key];
    if (typeof value === "string" && value !== "") {
      parts.push(value);
    }
  }
  return parts.length === 0 ? "—" : parts.join(" · ");
}

// observationRow is one retained observation's cells, in column order.
//
// Every value is read from a field task 067 retained, through the allowlist in
// render.js. Nothing is derived, combined or thresholded.
export function observationRow(observation) {
  const duration = formatDuration(observation);
  const field = (key) => render.retainedValue(observation, key);
  return Object.freeze({
    sequence: field("sequence"),
    recordedAt: field("timestamp"),
    behavior: behaviorDescriptorText(observation.behavior),
    fingerprint: field("fingerprint_id"),
    newBehavior: newBehaviorLabel(observation),
    decision: field("decision"),
    risk: field("risk_level"),
    trust: field("trust_score"),
    anomaly: field("anomaly_score"),
    confidence: field("anomaly_confidence"),
    duration: duration.text,
    durationMeasured: duration.measured,
    spanStatus: spanStatusLabel(observation),
    session: field("session_id"),
    trace: field("trace_id"),
    span: field("span_id"),
  });
}

// behaviorRow is one resolved behavioral identity's cells.
export function behaviorRow(delta) {
  return Object.freeze({
    fingerprint: render.displayValue(delta.fingerprint_id),
    behavior: behaviorDescriptorText(delta.behavior),
    presence: render.displayValue(delta.presence),
    referenceCount: render.displayValue(delta.reference_count),
    candidateCount: render.displayValue(delta.candidate_count),
  });
}

// needsExplicitSide reports whether a behavioral delta's presence leaves the
// side undecided.
//
// **Only a shared behavior does.** A behavior the candidate added exists nowhere
// else and one it removed exists only in the reference, so for those two the
// control plane derives the side and this page sends none — which keeps that
// rule in one place, where it is already tested. A shared behavior is contained
// by both runs, both hold their own observations of it, and those two sets are
// exactly what a developer is comparing; picking one silently would answer a
// question nobody asked with an answer indistinguishable from the one they
// wanted. So it is offered one control per side, and the server refuses a
// resolution that names none.
//
// An unrecognized presence is treated as undecided for the same reason: asking
// is recoverable, and guessing is not.
export function needsExplicitSide(presence) {
  return presence !== "added" && presence !== "removed";
}

// ---------------------------------------------------------------------
// The controller
// ---------------------------------------------------------------------

// RequestSurface is one independently-cancellable read and its page position.
//
// **One per surface, never one for the page.** A single shared counter made
// every surface cancel every other: opening Provenance while a run-history page
// was in flight discarded that page's response, and the panel kept the "Reading…"
// it had already drawn — because selecting a subtab does not start a read. The
// same counter also reset an unrelated surface's page number while its rendered
// rows and continuation control stayed on screen, so the footer claimed page 1 of
// a page 2 that was still visible.
//
// The fix is that these are separate questions. Finding resolution, run history
// and provenance each own a token and, where they page, a cursor and a page
// number; a read on one invalidates only reads on the same one.
class RequestSurface {
  constructor() {
    // The generation token. The browser cannot cancel an in-flight promise, so a
    // late response is discarded by comparing the token it was issued under
    // against the current one.
    this.generation = 0;
    this.cursor = "";
    this.page = 0;
  }

  // restart begins a new question: the previous read on this surface is
  // abandoned and pagination returns to the start.
  //
  // Resetting the cursor is not a courtesy. A cursor is a position in one
  // ordered stream, and carrying it into a different stream would page into the
  // middle of an unrelated answer.
  restart() {
    this.generation += 1;
    this.cursor = "";
    this.page = 0;
    return this.generation;
  }

  // paginate continues the same question: the previous read on this surface is
  // still abandoned — two clicks of a continuation control must not race — but
  // the page position is kept.
  paginate(after) {
    this.generation += 1;
    this.cursor = after;
    return this.generation;
  }

  // stale reports whether a response issued under `token` has been superseded.
  stale(token) {
    return token !== this.generation;
  }

  // advance records that a page rendered, and returns its number.
  advance() {
    this.page += 1;
    return this.page;
  }
}

// EvidenceSurface drives every read this surface makes.
//
// Three independent surfaces, because they answer three unrelated questions:
// which finding is open, which run view is open, and which two candidates'
// provenance is being read. Reading one leaves the other two exactly as they
// were — their in-flight responses still land, and their page positions do not
// move.
export class EvidenceSurface {
  constructor(deps, view) {
    this.deps = deps;
    this.view = view;

    this.finding = null;
    this.findingRoute = "";
    this.findingRequest = new RequestSurface();

    this.runID = "";
    this.viewKey = "";
    this.identifier = "";
    this.runRequest = new RequestSurface();

    this.provenanceRequest = new RequestSurface();
  }

  // ------------------------------------------------------------------
  // Findings
  // ------------------------------------------------------------------

  // openFinding resolves a finding from its first page.
  //
  // `route` is which resolution the finding is asked of — behaviors or
  // observations — and it comes from render.evidenceRouteFor for a gate check, or
  // from the caller for a behavior. It selects a URL and decides nothing about
  // the answer.
  async openFinding(finding, route) {
    this.finding = finding;
    this.findingRoute = route;
    await this.readFinding("", this.findingRequest.restart());
  }

  // nextFindingPage follows the cursor the last page published.
  //
  // One page forward per action, replacing what is on screen. There is no
  // previous-page control because reverse traversal is not something `/v1`
  // offers, and keeping every visited page in order to walk backwards is the
  // unbounded accumulation this shape exists to avoid.
  async nextFindingPage(after) {
    await this.readFinding(after, this.findingRequest.paginate(after));
  }

  // readFinding reads one bounded page of the open finding under one token.
  //
  // The token is taken by the caller rather than here, because only the caller
  // knows whether this is a new question or the next page of the current one —
  // and those differ in exactly one respect, which is whether the page position
  // survives.
  async readFinding(after, token) {
    if (this.finding === null) {
      return;
    }
    const finding = this.finding;
    const route = this.findingRoute;
    this.view.findingLoading(finding, route);

    try {
      let response;
      if (route === "observations") {
        response = await this.deps.resolveFindingObservations(finding, after);
        if (this.findingRequest.stale(token)) {
          return;
        }
      } else {
        response = await this.deps.resolveFindingBehaviors(finding, after);
        if (this.findingRequest.stale(token)) {
          return;
        }
      }
      this.view.findingPage({
        finding,
        route,
        response,
        pageNumber: this.findingRequest.advance(),
      });
    } catch (error) {
      if (this.findingRequest.stale(token)) {
        return;
      }
      this.view.findingError(error);
    }
  }

  // ------------------------------------------------------------------
  // Run views
  // ------------------------------------------------------------------

  // openRunView reads one run view from its first page.
  async openRunView(runID, viewKey, identifier) {
    this.runID = runID;
    this.viewKey = viewKey;
    this.identifier = identifier;
    await this.readRun("", this.runRequest.restart());
  }

  async nextRunPage(after) {
    await this.readRun(after, this.runRequest.paginate(after));
  }

  // scopeForView renders the current view's narrowing.
  //
  // At most one field, because the server refuses more than one and this page has
  // no view that wants two.
  scopeForView() {
    const spec = viewFor(this.viewKey);
    const scope = {};
    if (spec === null || spec.param === "") {
      return scope;
    }
    scope[spec.param] = this.identifier;
    return scope;
  }

  // readRun reads one bounded page of the open run view under one token.
  //
  // **Every path through here renders something.** A read that returns without
  // drawing would leave whatever was on screen — which, after a loading notice,
  // is a panel that says "Reading…" forever and has no control that would start
  // another read.
  async readRun(after, token) {
    const spec = viewFor(this.viewKey);
    if (this.runID === "" || spec === null) {
      this.view.runNeedsRun();
      return;
    }
    if (spec.param !== "" && this.identifier === "") {
      this.view.runNeedsIdentifier(spec);
      return;
    }

    const runID = this.runID;
    const scope = this.scopeForView();
    this.view.runLoading(spec, runID, this.identifier);

    try {
      const response = await this.deps.runObservations(runID, scope, after);
      if (this.runRequest.stale(token)) {
        return;
      }
      this.view.runObservationPage({
        spec,
        runID,
        identifier: this.identifier,
        response,
        pageNumber: this.runRequest.advance(),
      });
    } catch (error) {
      if (this.runRequest.stale(token)) {
        return;
      }
      this.view.runError(error);
    }
  }

  // ------------------------------------------------------------------
  // Provenance
  // ------------------------------------------------------------------

  // loadProvenance reads the supplied version provenance of two runs' candidates.
  //
  // Two runs and four reads, because provenance lives on the candidate a run
  // evaluated and a run names it. Nothing is cached between calls: the control
  // plane is authoritative and a stale label is worse than a second request.
  //
  // Four awaits and four checks. The token is this surface's own, so starting
  // this read abandons an earlier provenance read and nothing else — a run-history
  // page in flight still lands, and its page number does not move.
  async loadProvenance(referenceRunID, candidateRunID) {
    const token = this.provenanceRequest.restart();
    this.view.provenanceLoading();

    try {
      const referenceRun = await this.deps.getRun(referenceRunID);
      if (this.provenanceRequest.stale(token)) {
        return;
      }
      const candidateRun = await this.deps.getRun(candidateRunID);
      if (this.provenanceRequest.stale(token)) {
        return;
      }
      const referenceCandidate = await this.deps.getCandidate(referenceRun.candidate_id);
      if (this.provenanceRequest.stale(token)) {
        return;
      }
      const candidateCandidate = await this.deps.getCandidate(candidateRun.candidate_id);
      if (this.provenanceRequest.stale(token)) {
        return;
      }
      this.view.provenancePair({
        reference: { run: referenceRun, candidate: referenceCandidate },
        candidate: { run: candidateRun, candidate: candidateCandidate },
      });
    } catch (error) {
      if (this.provenanceRequest.stale(token)) {
        return;
      }
      this.view.provenanceError(error);
    }
  }
}

// ---------------------------------------------------------------------
// Rendering
//
// Below this divider everything touches the DOM. Values reach it through
// render.element, which sets textContent — no server string becomes markup here
// any more than anywhere else in this application.
// ---------------------------------------------------------------------

// sideBadge renders which run's evidence is being read.
//
// Reference and candidate stay visibly distinct, and a resolution with no side —
// an aggregate-only one — says that rather than showing a blank chip that would
// read as one of the two.
export function sideBadge(side) {
  if (side === "reference") {
    const node = render.element("span", "side side-reference", "reference run");
    return node;
  }
  if (side === "candidate") {
    return render.element("span", "side side-candidate", "candidate run");
  }
  return render.element("span", "side side-none", "no side — this check counts no observations");
}

// statusBanner renders the server's status word and its sentence.
//
// The word is `response.status`, rendered. **It describes the finding, not the
// page**, so it is drawn identically on a continuation that came back empty:
// paging past the last match is still `resolved`, and re-deriving the status from
// how many rows arrived is the defect task 085 documents by name.
export function statusBanner(status) {
  const box = render.element("div", "status-box");
  const word = typeof status === "string" && status !== "" ? status : "—";
  const line = render.element("p", "status-line");
  line.append(render.element("span", `status-chip status-${word}`, word));
  box.append(line);
  box.append(render.element("p", "status-why", statusSentence(status)));
  return box;
}

// historyBox renders what a side's retained history is.
export function historyBox(state, retainedCount, complete) {
  const box = render.element("div", "history-box");
  box.append(render.element(
    "p", "history-line",
    `Retained history: ${render.displayValue(state)} · ` +
    `${render.displayValue(retainedCount)} observation(s) retained · ` +
    `complete: ${render.displayValue(complete)}`,
  ));
  box.append(render.element("p", "history-why", historySentence(state)));
  return box;
}

// pageFooter says where the reader is, without claiming a total.
//
// It never reports a row count as an authoritative total: the number of rows on
// screen is how much was drawn, and the authoritative counts live in
// `recorded_count` and in the run's own progress. "End of results" is about the
// page, and it is worded so it cannot be read as "there is no evidence".
export function pageFooter(pageNumber, nextAfter, onNext) {
  const box = render.element("div", "page-nav");
  box.append(render.element("p", "page-state", `Page ${pageNumber} of this read`));
  if (typeof nextAfter === "string" && nextAfter !== "") {
    const button = render.element("button", null, "Load next page");
    button.type = "button";
    button.addEventListener("click", () => onNext(nextAfter));
    box.append(button);
  } else {
    box.append(render.element(
      "p", "page-end",
      "End of results — you have read every row this read returns. That is a " +
      "statement about the page, not about whether evidence exists.",
    ));
  }
  return box;
}

// renderObservationTable renders a bounded page of retained observations.
//
// `columns` names which cells to draw, so the five run views and the resolution
// view share one table rather than five near-identical ones. Every column reads a
// field of observationRow, which reads an allowlisted field of the response.
export function renderObservationTable(host, observations, columns, onNavigate) {
  const rows = [];
  for (const observation of observations) {
    const values = observationRow(observation);
    const cells = [];
    for (const column of columns) {
      // A long recorded descriptor is the one cell that should wrap; every other
      // value is a short word, and letting the table break those mid-word turned
      // "critical" into two lines. The wrapping cell says so rather than the
      // stylesheet guessing from a column index that differs per view.
      cells.push(column.wrap === true
        ? render.element("span", "cell-wrap", values[column.key])
        : values[column.key]);
    }
    if (typeof onNavigate === "function") {
      cells.push(onNavigate(observation));
    }
    rows.push(cells);
  }
  const headers = columns.map((column) => column.label);
  if (typeof onNavigate === "function") {
    headers.push("Open");
  }
  host.append(render.table(headers, rows, `${rows.length} row(s) on this page`));
}

// OBSERVATION_COLUMNS are the column sets the five run views draw.
//
// Declared once, so a column is a named thing rather than an index into an
// array literal, and so a view's shape is readable beside the question it answers.
export const OBSERVATION_COLUMNS = Object.freeze({
  session: Object.freeze([
    Object.freeze({ key: "sequence", label: "Seq" }),
    Object.freeze({ key: "behavior", label: "Action, as recorded", wrap: true }),
    Object.freeze({ key: "newBehavior", label: "New to run" }),
    Object.freeze({ key: "decision", label: "Decision" }),
    Object.freeze({ key: "risk", label: "Risk" }),
    Object.freeze({ key: "trust", label: "Trust" }),
    Object.freeze({ key: "anomaly", label: "Anomaly" }),
    Object.freeze({ key: "confidence", label: "Confidence" }),
  ]),
  sequence: Object.freeze([
    Object.freeze({ key: "sequence", label: "Seq" }),
    Object.freeze({ key: "fingerprint", label: "Fingerprint" }),
    Object.freeze({ key: "behavior", label: "Behavior, as recorded", wrap: true }),
    Object.freeze({ key: "newBehavior", label: "New to run" }),
    Object.freeze({ key: "session", label: "Session" }),
    Object.freeze({ key: "trace", label: "Trace" }),
  ]),
  timeline: Object.freeze([
    Object.freeze({ key: "sequence", label: "Seq" }),
    Object.freeze({ key: "recordedAt", label: "Recorded at" }),
    Object.freeze({ key: "behavior", label: "Action, as recorded", wrap: true }),
    Object.freeze({ key: "decision", label: "Decision" }),
    Object.freeze({ key: "risk", label: "Risk" }),
    Object.freeze({ key: "duration", label: "Duration" }),
    Object.freeze({ key: "spanStatus", label: "Span status" }),
  ]),
  behavior: Object.freeze([
    Object.freeze({ key: "sequence", label: "Seq" }),
    Object.freeze({ key: "recordedAt", label: "Recorded at" }),
    Object.freeze({ key: "newBehavior", label: "New to run" }),
    Object.freeze({ key: "decision", label: "Decision" }),
    Object.freeze({ key: "risk", label: "Risk" }),
    Object.freeze({ key: "trust", label: "Trust" }),
    Object.freeze({ key: "anomaly", label: "Anomaly" }),
    Object.freeze({ key: "session", label: "Session" }),
    Object.freeze({ key: "trace", label: "Trace" }),
  ]),
  resolution: Object.freeze([
    Object.freeze({ key: "sequence", label: "Seq" }),
    Object.freeze({ key: "behavior", label: "Action, as recorded", wrap: true }),
    Object.freeze({ key: "newBehavior", label: "New to run" }),
    Object.freeze({ key: "decision", label: "Decision" }),
    Object.freeze({ key: "risk", label: "Risk" }),
    Object.freeze({ key: "session", label: "Session" }),
    Object.freeze({ key: "trace", label: "Trace" }),
  ]),
});

// renderTraceTree renders one page of a trace as its recorded structure.
//
// A list of rows with an indentation depth rather than nested markup: the depth
// is producer-controlled and bounded by trace.js, and a flat list with an
// accessible depth is readable by a screen reader in a way nested tables are not.
//
// Every row states how its parent reference relates to this page. A row whose
// parent is not here is *not* drawn as a root — it is drawn at the top level
// saying that its parent is not on this page, which is the distinction a tree
// inferred from adjacency would have destroyed.
export function renderTraceTree(host, observations) {
  const nodes = buildTraceTree(observations);
  if (nodes.length === 0) {
    host.append(render.emptyState("No retained observations for this trace."));
    return;
  }

  const list = render.element("ol", "trace-tree");
  list.setAttribute("aria-label", "Recorded trace structure");
  for (const node of nodes) {
    const values = observationRow(node.observation);
    // Depth as a class rather than an inline style: the policy forbids inline
    // style and the stylesheet declares one rule per depth up to trace.js's
    // bound, so indentation cannot depend on a value a producer chose.
    const item = render.element("li", `trace-node depth-${node.depth}`);
    item.setAttribute("aria-level", String(node.depth + 1));

    const head = render.element("p", "trace-head");
    head.append(render.element("span", "trace-seq", values.sequence));
    head.append(render.element("span", "trace-action", values.behavior));
    head.append(render.element("span", `trace-status ${spanStatusClass(node.observation)}`,
      values.spanStatus));
    item.append(head);

    const meta = render.element("p", "trace-meta");
    meta.append(render.element("span", null, `span ${values.span}`));
    meta.append(render.element("span", null, parentStateText(node.state)));
    meta.append(render.element("span", null, `duration ${values.duration}`));
    meta.append(render.element("span", null, values.newBehavior));
    meta.append(render.element("span", null, `decision ${values.decision}`));
    item.append(meta);

    if (node.duplicateSpan) {
      item.append(render.element(
        "p", "trace-warn",
        "More than one retained observation carries this span id, so a " +
        "reference to it does not identify one of them.",
      ));
    }
    if (node.depthClamped) {
      item.append(render.element(
        "p", "trace-warn",
        "Indentation is clamped at this page's depth bound; the recorded chain " +
        "is deeper than it is drawn.",
      ));
    }
    list.append(item);
  }
  host.append(list);

  host.append(render.element(
    "p", "note",
    "Structure is the recorded parent span reference and nothing else. Rows are " +
    "in the order the platform accepted them, which is not wall-clock order — a " +
    "parent span ends after the children it started. Durations are per " +
    "observation and are not summed.",
  ));
}

// renderProvenancePanel renders both sides' supplied version provenance.
//
// Both sides, side by side, with every unsupplied field printed as "not stated".
// A field this task must not implement — task 086's prompt reference and scenario
// digests — is absent because nothing reads it, not because it is hidden.
export function renderProvenancePanel(host, pair) {
  render.clear(host);
  const grid = render.element("div", "provenance");
  for (const entry of [
    { side: "reference", data: pair.reference },
    { side: "candidate", data: pair.candidate },
  ]) {
    const column = render.element("section", `provenance-side side-${entry.side}`);
    column.append(render.element("h4", null, entry.side === "reference"
      ? "Reference run"
      : "Candidate run"));
    column.append(render.element("p", "note-inline",
      `run ${render.displayValue(entry.data.run.id)} · candidate ${render.displayValue(entry.data.run.candidate_id)}`));

    const metadata = entry.data.candidate.metadata;
    const pairs = [];
    for (const key of render.CANDIDATE_METADATA_FIELDS) {
      pairs.push([
        key,
        provenanceValue(metadata === undefined || metadata === null ? "" : metadata[key]),
      ]);
    }
    column.append(render.definitionList(pairs.map(([key, value]) => [
      render.displayValue(key),
      value,
    ])));
    grid.append(column);
  }
  host.append(grid);
  host.append(render.element(
    "p", "note",
    "Provenance is what the producer supplied when the candidate was created. " +
    `Every field it did not supply reads "${PROVENANCE_UNSPECIFIED}" — an ` +
    "unknown model is not the same fact as a model both sides shared.",
  ));
}

// FIDELITY_NOTE and SEQUENCE_NOTE are re-exported so a view can state both
// limitations without importing trace.js for two strings.
export { FIDELITY_NOT_RETAINED as FIDELITY_NOTE };
export { SEQUENCE_DEVIATION_NOT_RETAINED as SEQUENCE_NOTE };
