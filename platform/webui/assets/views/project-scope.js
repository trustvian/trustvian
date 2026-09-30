// What Compare and Promotions hold, and the rule that it belongs to a project.
//
// Both destinations are scoped to the project named in the sidebar, and both
// used to decide whether to fetch by asking "have I loaded before?". A
// boolean cannot answer that question: after switching from project A to
// project B it is still true, so B's Compare showed A's agents and B's
// Promotions showed A's history, under B's name. The cache was valid for a
// project and was being keyed on nothing.
//
// The fix is to remember *which* project each destination's contents describe
// and compare identity rather than a flag. Reused for the paging cursor and
// the two chosen comparison sides, which had the same problem: a reference
// run assigned under A stayed assigned under B, where it does not belong.
//
// No DOM, no requests. The view reads these and draws; this decides what is
// still true.

import { createSurface } from "../core/ownership.js";

export function createProjectScope() {
  // One surface per independent read, not one per destination.
  //
  // Compare's three lists are three reads: asking for a candidate's runs
  // must not abandon an agent page still arriving, or the skeleton over the
  // agent list would never clear. Promotions is separate again — listing
  // history and browsing a comparison are different destinations that
  // happen to share a project.
  const compareSurfaces = {
    agents: createSurface("compare.agents"),
    candidates: createSurface("compare.candidates"),
    runs: createSurface("compare.runs"),
  };
  const compareLevels = Object.freeze(["agents", "candidates", "runs"]);
  const promotionSurface = createSurface("promotions");

  let project = "";
  // The project each destination's contents actually describe, which is not
  // the same as the project currently selected — that difference is the
  // whole point.
  let comparePopulatedFor = "";
  let promotionPopulatedFor = "";

  let agent = "";
  let candidate = "";
  let sides = { reference: null, candidate: null };
  let promotionCursor = "";
  let promotionPageNumber = 0;
  // Per-level, because Compare's three lists are three independent reads:
  // fetching candidates must not take away the skeleton over the runs table.
  let compareLoading = { agents: false, candidates: false, runs: false };
  let promotionLoading = false;

  return {
    get project() { return project; },
    get agent() { return agent; },
    get candidate() { return candidate; },
    get sides() { return sides; },
    get promotionCursor() { return promotionCursor; },
    get promotionPageNumber() { return promotionPageNumber; },
    get compareLoading() { return compareLoading; },
    get promotionLoading() { return promotionLoading; },

    // setProject records the scope and, when it actually changed, drops
    // everything the previous one populated.
    //
    // Returns whether it changed, so the caller can avoid resetting a
    // destination a reader is looking at merely because the sidebar was
    // re-rendered. Selecting the same project again is not a change.
    setProject(nextProject) {
      if (nextProject === project) {
        return false;
      }
      project = nextProject;
      agent = "";
      candidate = "";
      sides = { reference: null, candidate: null };
      promotionCursor = "";
      promotionPageNumber = 0;
      comparePopulatedFor = "";
      promotionPopulatedFor = "";
      compareLoading = { agents: false, candidates: false, runs: false };
      promotionLoading = false;
      // Anything in flight belonged to the project just left.
      for (const level of compareLevels) {
        compareSurfaces[level].retarget(nextProject);
      }
      promotionSurface.retarget(nextProject);
      return true;
    },

    // needsCompare and needsPromotions answer "is what I am holding about
    // the project I am scoped to?" — identity, not a loaded flag.
    needsCompare() { return project !== "" && comparePopulatedFor !== project; },
    needsPromotions() { return project !== "" && promotionPopulatedFor !== project; },

    // ---- compare --------------------------------------------------------
    // As in run-state, the loading flag can only be lowered with the ticket
    // that raised it, so an older read finishing late cannot take away a
    // newer one's skeleton.
    beginCompare(level) {
      const surface = compareSurfaces[level];
      const ticket = surface.begin(project);
      compareLoading = { ...compareLoading, [level]: true };
      return { ...ticket, level };
    },
    ownsCompare(ticket) {
      const surface = compareSurfaces[ticket.level];
      return surface !== undefined && surface.owns(ticket);
    },
    commitCompare(ticket) {
      if (!this.ownsCompare(ticket)) {
        return false;
      }
      compareLoading = { ...compareLoading, [ticket.level]: false };
      // Only the agent list establishes that Compare is populated for this
      // project: it is the read entering the destination performs, and the
      // two below it are the reader browsing within a project already
      // established.
      if (ticket.level === "agents") {
        comparePopulatedFor = project;
      }
      return true;
    },
    failCompare(ticket) {
      if (!this.ownsCompare(ticket)) {
        return false;
      }
      compareLoading = { ...compareLoading, [ticket.level]: false };
      return true;
    },
    setAgent(id) { agent = id; candidate = ""; },
    setCandidate(id) { candidate = id; },

    // assignSide moves a run onto one side of the comparison.
    //
    // Assigning a run that already holds the other side moves it rather than
    // letting one run be both: a run compared against itself is not a
    // comparison.
    assignSide(side, run) {
      const other = side === "reference" ? "candidate" : "reference";
      const next = { reference: sides.reference, candidate: sides.candidate };
      if (next[other] !== null && next[other].id === run.id) {
        next[other] = null;
      }
      next[side] = run;
      sides = next;
    },
    comparisonReady() {
      return sides.reference !== null
        && sides.candidate !== null
        && sides.reference.id !== sides.candidate.id;
    },

    // ---- promotions -----------------------------------------------------
    beginPromotions() {
      const ticket = promotionSurface.begin(project);
      promotionLoading = true;
      return ticket;
    },
    ownsPromotions(ticket) { return promotionSurface.owns(ticket); },
    commitPromotions(ticket, cursor, pageNumber) {
      if (!promotionSurface.owns(ticket)) {
        return false;
      }
      promotionCursor = cursor;
      promotionPageNumber = pageNumber;
      promotionPopulatedFor = project;
      promotionLoading = false;
      return true;
    },
    resetPromotions() {
      promotionCursor = "";
      promotionPageNumber = 0;
      promotionPopulatedFor = "";
      promotionLoading = false;
      promotionSurface.retarget(project);
    },
  };
}
