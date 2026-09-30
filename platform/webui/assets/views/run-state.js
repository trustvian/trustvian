// The run workspace's state, and the rule that it all belongs to one run.
//
// Extracted from the view because the bug it exists to prevent is not a
// rendering bug. Opening run B while run A's tabs were populated left B
// showing A's behaviors: the tab had already been marked loaded, so nothing
// fetched, and the rows on screen described a run the reader had left. The
// same shape applied to the observation page, the selected row, the strip
// counts and the paging cursor.
//
// Holding it here rather than as a dozen module-level variables makes the
// invariant statable and testable: **every field is scoped to `runID`, and
// changing `runID` clears all of them at once.** A reset that is one method
// cannot be half-applied, which is exactly how the original was wrong.
//
// It touches no DOM and makes no request, so it can be driven directly —
// with real overlapping promises — rather than only inspected as source.

import { createSurface } from "../core/ownership.js";

function emptyObservations() {
  return { rows: [], nextAfter: "", loaded: false, loading: false, state: "", retained: "" };
}

function emptyBehaviors() {
  return { rows: [], nextAfter: "", loaded: false, loading: false };
}

function emptyScope() {
  return { sessionID: "", traceID: "", fingerprintID: "" };
}

// scopeKey renders a narrowing as a comparable string.
//
// The observation surface is about a run *and* a filter: a response for the
// unnarrowed view is stale once the reader has followed a trace, even though
// the run has not changed.
function scopeKey(runID, scope) {
  return [runID, scope.sessionID, scope.traceID, scope.fingerprintID].join("\u0000");
}

export function createRunState() {
  // Three surfaces, because these are three independent reads. Opening a
  // run must not invalidate an observation page that is still wanted, and
  // fetching behaviors must not stop the observation skeleton.
  const runSurface = createSurface("run");
  const observationSurface = createSurface("observations");
  const behaviorSurface = createSurface("behaviors");

  let runID = "";
  let detail = null;
  let progress = null;
  let scope = emptyScope();
  let observations = emptyObservations();
  let behaviors = emptyBehaviors();
  let selected = "";

  const state = {
    get runID() { return runID; },
    get detail() { return detail; },
    get progress() { return progress; },
    get scope() { return scope; },
    get observations() { return observations; },
    get behaviors() { return behaviors; },
    get selected() { return selected; },

    // openRun points the whole workspace at a different run.
    //
    // Everything run-scoped is cleared in one place: the two pages, their
    // cursors, the narrowing, the selected row, and the record and progress
    // the strip reads. Any outstanding request for the previous run is
    // abandoned by retargeting all three surfaces, so a response still in
    // flight cannot repopulate the tab a reader has just opened.
    openRun(nextRunID) {
      runID = nextRunID;
      detail = null;
      progress = null;
      scope = emptyScope();
      observations = emptyObservations();
      behaviors = emptyBehaviors();
      selected = "";
      runSurface.retarget(nextRunID);
      observationSurface.retarget(scopeKey(nextRunID, scope));
      behaviorSurface.retarget(nextRunID);
    },

    // narrow replaces the observation filter with exactly one dimension.
    //
    // Replaces rather than adds: the server accepts at most one, and a page
    // offering a second that silently dropped the first would claim a
    // capability the protocol does not have.
    narrow(field, value) {
      scope = emptyScope();
      scope[field] = value;
      observations = emptyObservations();
      selected = "";
      observationSurface.retarget(scopeKey(runID, scope));
    },

    clearNarrowing() {
      scope = emptyScope();
      observations = emptyObservations();
      selected = "";
      observationSurface.retarget(scopeKey(runID, scope));
    },

    select(sequence) { selected = String(sequence); },
    clearSelection() { selected = ""; },

    // ---- run record -------------------------------------------------
    beginRun() { return runSurface.begin(runID); },
    ownsRun(ticket) { return runSurface.owns(ticket); },
    commitDetail(ticket, record) {
      if (!runSurface.owns(ticket)) {
        return false;
      }
      detail = record;
      return true;
    },
    commitProgress(ticket, record) {
      if (!runSurface.owns(ticket)) {
        return false;
      }
      progress = record;
      return true;
    },

    // ---- observations -------------------------------------------------
    //
    // The loading flag is owned here rather than by the view, because the
    // rule it obeys is an ownership rule: **only the current request may
    // lower it.** Written as a `finally` in the caller it was wrong in a way
    // that is easy to miss — an older request finishing late cleared the
    // flag a newer one had just raised, so the skeleton vanished while the
    // newer read was still outstanding. Here there is no way to clear it
    // without passing a ticket.
    beginObservations() {
      const ticket = observationSurface.begin(scopeKey(runID, scope));
      observations = { ...observations, loading: true };
      return ticket;
    },
    ownsObservations(ticket) { return observationSurface.owns(ticket); },
    commitObservations(ticket, response) {
      if (!observationSurface.owns(ticket)) {
        return false;
      }
      observations = {
        rows: Array.isArray(response.observations) ? response.observations : [],
        nextAfter: typeof response.next_after === "string" ? response.next_after : "",
        loaded: true,
        loading: false,
        state: response.history_state || "",
        retained: response.retained_count || "",
      };
      selected = "";
      return true;
    },
    // failObservations lowers the flag for a request that errored, and only
    // if that request is still the current one.
    failObservations(ticket) {
      if (!observationSurface.owns(ticket)) {
        return false;
      }
      observations = { ...observations, loading: false };
      return true;
    },

    // ---- behaviors ------------------------------------------------------
    beginBehaviors() {
      const ticket = behaviorSurface.begin(runID);
      behaviors = { ...behaviors, loading: true };
      return ticket;
    },
    ownsBehaviors(ticket) { return behaviorSurface.owns(ticket); },
    commitBehaviors(ticket, response) {
      if (!behaviorSurface.owns(ticket)) {
        return false;
      }
      behaviors = {
        rows: Array.isArray(response.behaviors) ? response.behaviors : [],
        nextAfter: typeof response.next_after === "string" ? response.next_after : "",
        loaded: true,
        loading: false,
      };
      return true;
    },
    failBehaviors(ticket) {
      if (!behaviorSurface.owns(ticket)) {
        return false;
      }
      behaviors = { ...behaviors, loading: false };
      return true;
    },
  };
  return state;
}
