// The Overview destination (task 099): where to look first.
//
// Six panels, each a summary of records `/v1` already serves and each saying
// what it covers and when it was read:
//
//   Live now        the realtime scope cards of this connection, for the project
//   Runs            one page of the chosen candidate's runs, by status
//   Newest run      the authoritative progress and behaviors of the newest run
//                   on that page
//   Gate verdicts   the first page of the project's recorded promotion decisions
//   Environments    the project's whole environment collection
//
// What it refuses to show is as deliberate as what it shows: no project-wide
// "recent runs" (no such collection — task 101), no decision or risk
// distribution across a run (only a bounded page of retained history exists),
// no trend, and no health score. A missing figure reads "not available".
//
// Reads happen when the destination is opened, when the context changes while
// it is open, and when Refresh is pressed. There is no timer.

import { element, clear } from "../core/dom.js";
import { shortTime } from "../core/format.js";
import { createSurface } from "../core/ownership.js";
import { emptyState, inlineEmpty, skeleton } from "../ui/feedback.js";
import * as model from "./overview-model.js";

const NOT_AVAILABLE = "not available";
const TOP_BEHAVIORS = 8;
const RECENT_RUNS = 6;

function panelHead(host, title, scope, readAt) {
  const head = element("div", "panel-head");
  head.append(element("h2", "panel-title", title));
  const meta = element("p", "panel-meta");
  meta.append(element("span", "panel-scope", scope));
  if (readAt !== undefined) {
    meta.append(element("span", "panel-read", model.readClock(readAt)));
  }
  head.append(meta);
  host.append(head);
}

function link(label, onOpen, aria) {
  const button = element("button", "link-button", label);
  button.type = "button";
  if (aria) {
    button.setAttribute("aria-label", aria);
  }
  button.addEventListener("click", onOpen);
  return button;
}

function failure(host, error) {
  const box = element("div", "result result-error");
  box.append(element("p", "err-msg", error && error.message ? error.message : String(error)));
  host.append(box);
}

function figure(key, value, className) {
  const cell = element("div", className ? `figure ${className}` : "figure");
  cell.append(element("span", "figure-key", key));
  cell.append(element("span", "figure-value", value === "" || value === undefined || value === null
    ? NOT_AVAILABLE
    : String(value)));
  return cell;
}

// barChart draws labelled proportions. Each row prints its value; the bar is
// a second rendering of that number and carries nothing the text does not.
function barChart(rows, className) {
  const list = element("ul", className ? `bars ${className}` : "bars");
  for (const row of rows) {
    const item = element("li", `bar-row ${row.className || ""}`);
    const label = element("span", "bar-label");
    label.append(element("span", "bar-word", row.label));
    if (row.detail) {
      label.append(element("span", "bar-detail", row.detail));
    }
    item.append(label);
    const track = element("span", "bar-track");
    track.setAttribute("aria-hidden", "true");
    const fill = element("span", "bar-fill");
    fill.style.width = `${Math.max(0, Math.min(1, row.ratio)) * 100}%`;
    track.append(fill);
    item.append(track);
    item.append(element("span", "bar-value", row.value));
    if (row.action) {
      item.append(row.action);
    }
    list.append(item);
  }
  return list;
}

export function createOverview(deps) {
  const { selection, hosts, nav } = deps;
  const now = typeof deps.now === "function" ? deps.now : () => Date.now();

  const evidenceSurface = createSurface("overview.evidence");
  const verdictSurface = createSurface("overview.verdicts");
  let visible = false;
  let evidence = {
    runID: "", progress: null, behaviors: null, readAt: 0,
    loading: false, progressError: null, behaviorsError: null,
  };
  let verdicts = { projectID: "", page: null, readAt: 0, loading: false, error: null };

  const projectID = () => (selection.selection.project === null ? "" : selection.selection.project.id);
  // The run page describes the current scope only when its key matches it;
  // a page read for a previous scope is never summarised under this one.
  const runsReady = () => projectID() !== "" && selection.pages.runs.parentID === selection.runsKey;
  const scopeWords = () => {
    const scope = selection.runScope;
    return scope.level === "" ? "" : `${scope.level} ${scope.label}`;
  };

  // newestRun is the first row of the recency page — newest by creation time
  // across the whole scope, which the server ordered (task 101).
  function newestRun() {
    const page = selection.pages.runs;
    if (!page.loaded || !runsReady() || page.rows.length === 0) {
      return null;
    }
    return page.rows[0];
  }

  // The run the reader pinned for the summary on this page. Local, not the
  // context's run: pinning a run from a project-wide list must not narrow
  // the whole Overview to that run's candidate.
  let pinnedRunID = "";

  // summarised is the run the evidence panel describes: the one pinned here,
  // or the context's run when it is in this scope's list, and otherwise the
  // newest in the scope. The panel title says which.
  function summarised() {
    const rows = runsReady() ? selection.pages.runs.rows : [];
    const pinned = rows.find((row) => row.id === pinnedRunID);
    if (pinned !== undefined) {
      return { run: pinned, chosen: true };
    }
    const chosen = selection.selection.run;
    const inScope = chosen === null ? undefined : rows.find((row) => row.id === chosen.id);
    if (inScope !== undefined) {
      return { run: inScope, chosen: true };
    }
    const newest = newestRun();
    return newest === null ? null : { run: newest, chosen: false };
  }
  const summarisedRun = () => {
    const target = summarised();
    return target === null ? null : target.run;
  };

  // ---------------------------------------------------------------- reads

  async function loadEvidence(run, force) {
    const runID = run === null ? "" : run.id;
    if (!force && runID === evidence.runID && (evidence.loading || evidence.readAt > 0)) {
      return;
    }
    const ticket = evidenceSurface.begin(runID);
    evidence = {
      runID, progress: null, behaviors: null, readAt: 0,
      loading: runID !== "", progressError: null, behaviorsError: null,
    };
    drawEvidence();
    if (runID === "") {
      return;
    }
    // Two independent reads; one failing does not hide the other's answer.
    const [progress, behaviors] = await Promise.allSettled([
      deps.getProgress(runID),
      deps.runBehaviors(runID, ""),
    ]);
    if (!evidenceSurface.owns(ticket)) {
      return;
    }
    evidence = {
      runID,
      progress: progress.status === "fulfilled" ? progress.value : null,
      behaviors: behaviors.status === "fulfilled" ? behaviors.value : null,
      progressError: progress.status === "rejected" ? progress.reason : null,
      behaviorsError: behaviors.status === "rejected" ? behaviors.reason : null,
      readAt: now(),
      loading: false,
    };
    drawEvidence();
  }

  async function loadVerdicts(force) {
    const project = projectID();
    if (!force && project === verdicts.projectID && (verdicts.loading || verdicts.readAt > 0)) {
      return;
    }
    const ticket = verdictSurface.begin(project);
    verdicts = { projectID: project, page: null, readAt: 0, loading: project !== "", error: null };
    drawVerdicts();
    if (project === "") {
      return;
    }
    try {
      const page = await deps.listPromotions(project, "");
      if (!verdictSurface.owns(ticket)) {
        return;
      }
      verdicts = { projectID: project, page, readAt: now(), loading: false, error: null };
    } catch (error) {
      if (!verdictSurface.owns(ticket)) {
        return;
      }
      verdicts = { projectID: project, page: null, readAt: now(), loading: false, error };
    }
    drawVerdicts();
  }

  function readWhatIsMissing() {
    void selection.ensure("runs");
    void selection.ensure("environments");
    void loadVerdicts(false);
    void loadEvidence(summarisedRun(), false);
  }

  // ----------------------------------------------------------------- draw

  function drawScope() {
    const host = hosts.scope;
    clear(host);
    const chosen = selection.selection;
    if (chosen.project === null) {
      host.append(document.createTextNode(
        "Choose a project to see its summaries. Nothing is read until you do."));
      return;
    }
    const parts = [chosen.project.label];
    if (chosen.agent !== null) {
      parts.push(chosen.agent.label);
    }
    if (chosen.candidate !== null) {
      parts.push(chosen.candidate.label);
    }
    host.append(document.createTextNode(`Scoped to ${parts.join(" › ")}. `));
    host.append(document.createTextNode(
      "Runs are read newest first across the deepest level chosen; narrow with an agent or a candidate."));
  }

  function drawLive() {
    const host = hosts.live;
    clear(host);
    const project = projectID();
    panelHead(host, "Live now", "Active scopes on this connection since it last synchronized");
    const cards = deps.liveCards().filter((card) => project === "" || card.scope.project_id === project);
    if (cards.length === 0) {
      host.append(inlineEmpty(project === ""
        ? "No activity on this connection yet."
        : "No activity for this project on this connection yet. An instrumented agent appears here as it works."));
      return;
    }
    const list = element("ul", "live-list");
    for (const card of cards.slice(0, 8)) {
      const item = element("li", "live-item");
      const name = deps.labelForScope(card.scope) || card.scope.agent_id || "(unnamed agent)";
      item.append(element("span", "live-name", name));
      item.append(element("span", "live-run mono", card.scope.run_id || "no run"));
      item.append(element("span", "live-seen", `${card.seenLive} seen live`));
      if (card.lastDecision) {
        item.append(element("span", "live-decision", `last: ${card.lastDecision}`));
      }
      item.append(link("Watch", () => nav.openLiveScope(card.key), `Watch ${name} in Live`));
      list.append(item);
    }
    host.append(list);
    if (cards.length > 8) {
      host.append(element("p", "panel-note", `${cards.length - 8} more in Live.`));
    }
  }

  function drawRuns() {
    const host = hosts.runs;
    clear(host);
    const page = selection.pages.runs;
    const ready = runsReady();
    panelHead(host, "Runs",
      ready && page.loaded
        ? (page.whole
          ? `Every run in ${scopeWords()}, newest first`
          : `The newest ${page.rows.length} in ${scopeWords()}; more exist`)
        : "Newest first in the chosen scope",
      ready ? page.readAt : undefined);
    if (projectID() === "") {
      host.append(inlineEmpty("Choose a project."));
      return;
    }
    if (page.loading && !page.loaded) {
      const box = element("div");
      skeleton(box, 4, 3);
      host.append(box);
      return;
    }
    if (page.error) {
      failure(host, page.error);
      return;
    }
    if (!page.loaded) {
      host.append(inlineEmpty("Not read yet."));
      return;
    }
    if (page.rows.length === 0) {
      host.append(emptyState("No runs yet", `A run appears here once one is created in ${scopeWords()}.`, "runs"));
      return;
    }

    const breakdown = model.statusBreakdown(page.rows);
    host.append(element("p", "panel-note",
      `${breakdown.total} run(s)${page.whole ? "" : " on this page"}, by status:`));
    host.append(barChart(breakdown.entries.map((entry) => ({
      label: entry.status,
      value: String(entry.count),
      ratio: entry.count / breakdown.total,
      className: `status-${entry.status.replace(/[^a-z]/g, "")}`,
    })), "bars-status"));

    const sides = deps.compareSides();
    host.append(element("h3", "panel-subtitle", "Newest first"));
    const list = element("ul", "run-list");
    // The server's order: newest by creation time across the whole scope,
    // ties by identifier. Not re-sorted here.
    for (const run of page.rows.slice(0, RECENT_RUNS)) {
      const item = element("li", "run-item");
      // The chip chooses the run for this page's summary; Open goes to its
      // workspace. Two different intents, so two controls.
      const summarise = element("button", "ident-chip", run.id);
      summarise.type = "button";
      const target = summarised();
      const isChosen = target !== null && target.chosen && target.run.id === run.id;
      summarise.setAttribute("aria-pressed", isChosen ? "true" : "false");
      summarise.setAttribute("aria-label", `Summarise run ${run.id}`);
      summarise.addEventListener("click", () => {
        pinnedRunID = run.id;
        void loadEvidence(run, false);
        drawRuns();
        drawEvidence();
      });
      item.append(summarise);
      item.append(element("span", `run-status status-${String(run.status).replace(/[^a-z]/g, "")}`, run.status || NOT_AVAILABLE));
      item.append(element("span", "run-meta", `${run.environment || ""} · ${shortTime(run.created_at)}`));
      const controls = element("span", "run-controls");
      controls.append(link("Open", () => nav.openRun(run.id), `Open run ${run.id}`));
      for (const side of ["reference", "candidate"]) {
        const held = sides[side];
        const chosen = held !== null && held.id === run.id;
        const control = element("button", "chip-button", side === "reference" ? "Reference" : "Candidate");
        control.type = "button";
        control.setAttribute("aria-pressed", chosen ? "true" : "false");
        control.setAttribute("aria-label", `Use ${run.id} as the ${side} in Compare`);
        control.addEventListener("click", () => {
          nav.useInCompare(side, run);
          drawRuns();
        });
        controls.append(control);
      }
      item.append(controls);
      list.append(item);
    }
    host.append(list);
    if (page.rows.length > RECENT_RUNS || page.nextAfter !== "") {
      const more = element("p", "panel-note");
      more.append(document.createTextNode(page.rows.length > RECENT_RUNS
        ? `Showing the newest ${RECENT_RUNS} of ${page.rows.length} loaded. `
        : ""));
      if (page.nextAfter !== "" && !page.capped) {
        const next = element("button", "btn-quiet", page.loading ? "Loading…" : "Load next page");
        next.type = "button";
        next.disabled = page.loading;
        next.addEventListener("click", () => { void selection.loadMore("runs"); });
        more.append(next);
      }
      more.append(link("All in Runs", () => nav.openRuns()));
      host.append(more);
    }
    const chosenCount = (sides.reference !== null ? 1 : 0) + (sides.candidate !== null ? 1 : 0);
    const foot = element("p", "panel-foot");
    foot.append(document.createTextNode(`${chosenCount} of 2 sides chosen for Compare. `));
    foot.append(link("Open Compare", () => nav.openCompare()));
    host.append(foot);
  }

  function drawEvidence() {
    const host = hosts.evidence;
    clear(host);
    const target = summarised();
    const run = target === null ? null : target.run;
    let scope = "Authoritative counts for one run";
    if (target !== null) {
      scope = target.chosen
        ? `${run.id} · the run you chose`
        : `${run.id} · the newest by creation time in ${scopeWords()}`;
    }
    panelHead(host, target !== null && target.chosen ? "Chosen run" : "Newest run", scope,
      evidence.runID !== "" ? evidence.readAt : undefined);
    if (run === null) {
      // Still reading the runs is a different answer from having none.
      const runsPage = selection.pages.runs;
      if (projectID() !== "" && runsPage.loading && !runsPage.loaded) {
        const box = element("div");
        skeleton(box, 3, 3);
        host.append(box);
        return;
      }
      host.append(inlineEmpty(projectID() === "" ? "Choose a project." : "No run to summarise yet."));
      return;
    }
    if (evidence.loading || evidence.runID !== run.id) {
      const box = element("div");
      skeleton(box, 3, 3);
      host.append(box);
      return;
    }

    if (evidence.progressError) {
      failure(host, evidence.progressError);
    } else {
      const progress = evidence.progress || {};
      const figures = element("div", "figures");
      figures.append(figure("Status", progress.status || run.status));
      figures.append(figure("Records", progress.record_count));
      figures.append(figure("Behavior observations", progress.behavior_observation_count));
      figures.append(figure("Distinct behaviors",
        typeof progress.distinct_behavior_count === "number" ? String(progress.distinct_behavior_count) : ""));
      figures.append(figure("Behavior evidence",
        progress.behavior_complete === true ? "complete"
          : (progress.behavior_complete === false ? "incomplete — saturated at 512" : ""),
        progress.behavior_complete === false ? "is-warn" : ""));
      host.append(figures);
    }

    if (evidence.behaviorsError) {
      failure(host, evidence.behaviorsError);
    } else if (evidence.behaviors !== null) {
      const rows = Array.isArray(evidence.behaviors.behaviors) ? evidence.behaviors.behaviors : [];
      const more = typeof evidence.behaviors.next_after === "string" && evidence.behaviors.next_after !== "";
      host.append(element("h3", "panel-subtitle",
        `Behaviors by observations${more ? ", within the first page" : ""}`));
      if (rows.length === 0) {
        host.append(inlineEmpty("This run retained no behaviors."));
      } else {
        const top = model.topBehaviors(rows, TOP_BEHAVIORS);
        const max = top[0].observations;
        host.append(barChart(top.map((row) => {
          const behavior = row.behavior || {};
          const label = [behavior.operation_name, behavior.target_name]
            .filter((part) => typeof part === "string" && part !== "").join(" → ") || row.fingerprint_id;
          return {
            label,
            detail: behavior.operation_category || "",
            value: typeof row.observations === "string" ? row.observations : NOT_AVAILABLE,
            ratio: model.decimalRatio(row.observations, max),
          };
        }), "bars-behavior"));
      }
    }

    const actions = element("p", "panel-foot");
    actions.append(link("Open run", () => nav.openRun(run.id)));
    actions.append(link("Investigate traces", () => nav.openTraces(run.id)));
    actions.append(link("Watch live", () => nav.watchRun(run.id)));
    host.append(actions);
  }

  function drawVerdicts() {
    const host = hosts.verdicts;
    clear(host);
    const page = verdicts.page;
    const more = page !== null && typeof page.next_after === "string" && page.next_after !== "";
    panelHead(host, "Gate verdicts",
      more ? "The first page of recorded promotion decisions, in identifier order"
        : "Recorded promotion decisions, in identifier order",
      verdicts.projectID !== "" ? verdicts.readAt : undefined);
    if (projectID() === "") {
      host.append(inlineEmpty("Choose a project."));
      return;
    }
    if (verdicts.loading) {
      const box = element("div");
      skeleton(box, 3, 4);
      host.append(box);
      return;
    }
    if (verdicts.error) {
      failure(host, verdicts.error);
      return;
    }
    const rows = page !== null && Array.isArray(page.promotions) ? page.promotions : [];
    if (rows.length === 0) {
      host.append(emptyState("No decisions recorded",
        "A promotion records a gate verdict against a target environment. Compare evaluates one without recording it.",
        "promotion"));
      return;
    }
    const tally = model.verdictTally(rows);
    host.append(element("p", "panel-note",
      tally.map((entry) => `${entry.count} ${entry.outcome}`).join(" · ") + (more ? " on this page" : "")));
    const list = element("ul", "verdict-list");
    for (const row of rows.slice(0, 8)) {
      const item = element("li", `verdict-item outcome-${String(row.outcome).replace(/[^a-z]/g, "")}`);
      item.append(element("span", "verdict-outcome", row.outcome || NOT_AVAILABLE));
      const gate = row.gate_result && typeof row.gate_result.verdict === "string" ? row.gate_result.verdict : NOT_AVAILABLE;
      item.append(element("span", "verdict-gate", `gate ${gate}`));
      const target = row.target_environment && row.target_environment.ref ? row.target_environment.ref : NOT_AVAILABLE;
      item.append(element("span", "verdict-target", `→ ${target}`));
      item.append(element("span", "verdict-runs mono", `${row.reference_run_id} → ${row.candidate_run_id}`));
      item.append(element("span", "verdict-time", shortTime(row.decided_at)));
      item.append(link("Open", () => nav.openPromotion(row.id), `Open promotion ${row.id}`));
      list.append(item);
    }
    host.append(list);
    host.append(element("p", "panel-note",
      "A verdict is the gate's outcome under the limits recorded with it — not a judgement that a candidate is safe."));
  }

  function drawEnvironments() {
    const host = hosts.environments;
    clear(host);
    const page = selection.pages.environments;
    const ready = projectID() !== "" && page.parentID === projectID();
    panelHead(host, "Environments", "Every environment of the project", ready ? page.readAt : undefined);
    if (projectID() === "") {
      host.append(inlineEmpty("Choose a project."));
      return;
    }
    if (page.loading && !page.loaded) {
      host.append(inlineEmpty("Loading…"));
      return;
    }
    if (page.error) {
      failure(host, page.error);
      return;
    }
    if (!page.loaded) {
      host.append(inlineEmpty("Not read yet."));
      return;
    }
    if (page.rows.length === 0) {
      host.append(inlineEmpty("This project has no environments."));
      return;
    }
    const list = element("ul", "env-list");
    for (const row of page.rows) {
      const item = element("li", "env-item");
      item.append(element("span", "env-name", row.name || row.ref));
      item.append(element("code", "env-ref", row.ref));
      item.append(element("span", "env-meta",
        [row.rank === undefined || row.rank === null ? "unranked" : `rank ${row.rank}`, row.status]
          .filter((part) => part).join(" · ")));
      list.append(item);
    }
    host.append(list);
  }

  function drawAll() {
    drawScope();
    drawLive();
    drawRuns();
    drawEvidence();
    drawVerdicts();
    drawEnvironments();
  }

  selection.subscribe((what) => {
    if (!visible) {
      return;
    }
    if (what === "project" || what === "agent" || what === "candidate") {
      // A pin belongs to the scope it was made in.
      pinnedRunID = "";
      drawScope();
      // The scope's runs are read on demand (task 101), and this page is
      // what demands them. ensure() reads nothing it already holds for this
      // scope, so the notification it raises does not read again.
      void selection.ensure("runs");
      void selection.ensure("environments");
    }
    if (what === "project") {
      void loadVerdicts(false);
    }
    // The newest run may have changed with the runs page or the candidate.
    void loadEvidence(summarisedRun(), false);
    drawAll();
  });

  return Object.freeze({
    enter() {
      visible = true;
      drawAll();
      readWhatIsMissing();
    },
    leave() {
      visible = false;
    },
    // refresh reads every panel again. The only way a panel is re-read
    // without the context changing, because nothing here runs on a timer.
    refresh() {
      void selection.refresh("runs");
      void selection.refresh("environments");
      void loadVerdicts(true);
      void loadEvidence(summarisedRun(), true);
    },
    drawLive() {
      if (visible) {
        drawLive();
      }
    },
    redrawRuns() {
      if (visible) {
        drawRuns();
      }
    },
  });
}
