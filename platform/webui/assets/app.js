// Application wiring: forms, tabs, lifecycle actions and the live view.
//
// This file owns interaction. It holds no platform rules — whether a lifecycle
// transition is legal, whether a gate passes, whether evidence is sufficient —
// because all three belong to the control plane and a second copy in a browser
// would be a rule that can disagree with the one that matters.
//
// It also stores nothing about the platform. No identifier, record or page is
// kept in browser storage: a reload legitimately forgets which IDs were open,
// and the database stays the only source of truth. The one value the browser
// keeps is the reader's colour-scheme choice, through core/theme.js and
// nowhere else (ADR 0061). The watched run in the URL fragment
// is navigation state, never read back as fact.

import * as api from "./v1/api.js";
import * as render from "./v1/render.js";
import { RealtimeSession, STATE } from "./v1/realtime.js";
import { LiveModel } from "./live/model.js";
import { GraphCanvas, PULSE_MS } from "./live/graph.js";
import { renderRail, renderCanvasNotices } from "./live/rail.js";
import { TimelineFeed, renderTimeline } from "./live/timeline.js";
import { renderInspector } from "./live/inspector.js";
import { LabelCache, HierarchyBrowser } from "./v1/discovery.js";
import * as dash from "./ui/dashboard.js";
import { icon } from "./ui/icons.js";
import { notify } from "./ui/feedback.js";
import { createRunState } from "./views/run-state.js";
import { createProjectScope } from "./views/project-scope.js";
import { createSurface } from "./core/ownership.js";
import * as evidence from "./views/evidence.js";
import * as theme from "./core/theme.js";
import { createSelectionContext } from "./views/context.js";
import { bindSelector } from "./views/selectors.js";
import { createOverview } from "./views/overview.js";

const byID = (id) => document.getElementById(id);

// ---------------------------------------------------------------------
// Chrome
// ---------------------------------------------------------------------

const globalError = byID("global-error");

function showProblem(message) {
  globalError.textContent = message;
  globalError.hidden = false;
}

function clearProblem() {
  globalError.textContent = "";
  globalError.hidden = true;
}

// report renders a failure into a panel, and never as a gate result.
//
// An unreachable control plane and a rejected comparison are different kinds of
// answer. Collapsing them would let a network error read as a failed candidate,
// which is the browser's version of the exit-code separation task 060 encoded.
function report(target, error) {
  render.clear(target);
  target.append(render.errorState(error));
  showProblem(error.operational
    ? "The control plane could not answer. This is not a gate result."
    : error.message);
}

// ---------------------------------------------------------------------
// Theme (task 097)
// ---------------------------------------------------------------------
//
// The pre-paint script has already applied a stored choice. This wires the
// switcher to it and keeps the note beside it honest about two things a
// reader cannot otherwise see: which scheme System currently resolves to,
// and whether a choice will survive a reload.

const themeStorage = theme.browserStorage(window);
const systemDarkQuery = window.matchMedia("(prefers-color-scheme: dark)");
let themePreference = theme.readThemePreference(themeStorage);
let themePersisted = themeStorage !== null;

function drawThemeSwitch() {
  for (const preference of theme.THEME_PREFERENCES) {
    byID(`theme-${preference}`).checked = preference === themePreference;
  }
  const note = byID("theme-note");
  if (!themePersisted) {
    note.textContent = "This browser is not keeping the choice; it lasts until reload.";
  } else if (themePreference === "system") {
    note.textContent = `Following the system: ${
      theme.resolveTheme("system", systemDarkQuery.matches) === "dark" ? "dark" : "light"}.`;
  } else {
    note.textContent = "";
  }
}

for (const preference of theme.THEME_PREFERENCES) {
  byID(`theme-${preference}`).addEventListener("change", (event) => {
    if (!event.target.checked) {
      return;
    }
    themePreference = preference;
    theme.applyThemePreference(document.documentElement, preference);
    themePersisted = theme.writeThemePreference(themeStorage, preference);
    drawThemeSwitch();
  });
}
systemDarkQuery.addEventListener("change", drawThemeSwitch);
theme.applyThemePreference(document.documentElement, themePreference);
drawThemeSwitch();

// ---------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------
//
// A persistent sidebar of destinations, each backed by a capability /v1
// actually serves. There is no destination for work that does not exist yet:
// an empty one teaches a reader the product is thinner than it is.
//
// A view names the destination that stays lit while it is open, which is how
// the run workspace can be a full page without being a seventh place to go —
// a reader who opened a run came from Runs, and that is where Back returns
// them.

// PAGE_TITLES is the title each destination shows. Declared here rather than
// read from the sidebar's own label so a heading can say more than a nav item
// has room for.
const PAGE_TITLES = Object.freeze([
  Object.freeze({ view: "view-live", title: "Live" }),
  Object.freeze({ view: "view-overview", title: "Overview" }),
  Object.freeze({ view: "view-projects", title: "Projects" }),
  Object.freeze({ view: "view-runs", title: "Evaluation runs" }),
  Object.freeze({ view: "view-run", title: "Run" }),
  Object.freeze({ view: "view-compare", title: "Compare runs" }),
  Object.freeze({ view: "view-evidence", title: "Evidence" }),
  Object.freeze({ view: "view-promotion", title: "Promotions" }),
  Object.freeze({ view: "view-manage", title: "Manage" }),
]);

// navCount publishes a figure the console already holds.
//
// Only a real one: it appears when a page has been read and disappears when
// the scope changes, because a stale badge is worse than none. It counts the
// page on screen and says so nowhere else — the strip is where an
// authoritative total belongs.
function navCount(navID, value) {
  const item = byID(navID);
  if (item === null) {
    return;
  }
  const existing = item.querySelector(".nav-count");
  if (value === "" || value === undefined || value === null) {
    if (existing !== null) {
      existing.remove();
    }
    return;
  }
  if (existing !== null) {
    existing.textContent = String(value);
    return;
  }
  item.append(render.element("span", "nav-count", String(value)));
}

function titleFor(viewID) {
  for (const entry of PAGE_TITLES) {
    if (entry.view === viewID) {
      return entry.title;
    }
  }
  return "";
}

// Decorative glyphs, placed from the markup's own data-icon so the shell
// stays the single place that says which destination carries which. Built
// rather than written as markup, because nothing in this bundle parses any.
for (const host of document.querySelectorAll("[data-icon]")) {
  host.prepend(icon(host.dataset.icon));
}
byID("brand-mark").append(icon("evidence"));

const navItems = Array.from(document.querySelectorAll(".nav-item"));
// The Overview controller, created once the selection context exists. Null
// until then; the two places that reach it before that check.
let overview = null;
const views = Array.from(document.querySelectorAll(".view"));
let currentView = "view-live";

// openView reveals one destination and marks the sidebar entry it belongs to.
function openView(viewID) {
  if (currentView === "view-overview" && viewID !== "view-overview" && overview !== null) {
    overview.leave();
  }
  currentView = viewID;
  const target = byID(viewID);
  const owner = target === null ? viewID : target.dataset.nav;
  for (const view of views) {
    view.hidden = view.id !== viewID;
  }
  for (const item of navItems) {
    if (item.id === owner) {
      item.setAttribute("aria-current", "page");
    } else {
      item.removeAttribute("aria-current");
    }
  }
  byID("page-title").textContent = titleFor(viewID);
  drawCrumbs();
}

for (const item of navItems) {
  item.addEventListener("click", () => {
    openView(item.dataset.view);
    onEnterView(item.dataset.view);
  });
  item.addEventListener("keydown", (event) => {
    // Arrow-key movement down the sidebar, so the switcher works without a
    // pointer.
    const index = navItems.indexOf(item);
    if (event.key === "ArrowDown") {
      event.preventDefault();
      navItems[(index + 1) % navItems.length].focus();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      navItems[(index - 1 + navItems.length) % navItems.length].focus();
    }
  });
}

// setupSubtabs switches between the sections inside one view.
//
// Scoped to a view, because several have sub-sections now: a document-wide
// `.subtab` query would wire every button to every switcher and hide one
// view's sections whenever another's were shown.
function setupSubtabs(viewID, defaultSection) {
  const view = byID(viewID);
  const subtabs = Array.from(view.querySelectorAll(".subtab"));
  const show = (id) => {
    for (const tab of subtabs) {
      const active = tab.dataset.section === id;
      tab.setAttribute("aria-selected", active ? "true" : "false");
      const section = byID(tab.dataset.section);
      if (section !== null) {
        section.hidden = !active;
      }
    }
  };
  for (const tab of subtabs) {
    tab.addEventListener("click", () => show(tab.dataset.section));
    tab.addEventListener("keydown", (event) => {
      const index = subtabs.indexOf(tab);
      if (event.key === "ArrowRight") {
        const next = subtabs[(index + 1) % subtabs.length];
        show(next.dataset.section);
        next.focus();
      } else if (event.key === "ArrowLeft") {
        const previous = subtabs[(index - 1 + subtabs.length) % subtabs.length];
        show(previous.dataset.section);
        previous.focus();
      }
    });
  }
  show(defaultSection);
  return show;
}

const showManageSection = setupSubtabs("view-manage", "manage-project");
const showEvidenceSection = setupSubtabs("view-evidence", "evidence-finding");
const showPromotionSection = setupSubtabs("view-promotion", "promotion-history-section");
const showRunSection = setupSubtabs("view-run", "run-overview");

// openManage reveals one Manage subsection, for the create-then-show flows
// that used to jump to a top-level tab.
function openManage(sectionID) {
  openView("view-manage");
  showManageSection(sectionID);
}

// openEvidence reveals one Evidence subsection, for the navigation that starts
// in Compare and lands here.
function openEvidence(sectionID) {
  openView("view-evidence");
  showEvidenceSection(sectionID);
}

// busy disables a control for the duration of one request.
//
// The only reason any control is ever disabled: a request already in flight, or
// a required field empty. Never because this page decided a transition was
// illegal.
async function busy(button, work) {
  const previous = button === null ? false : button.disabled;
  if (button !== null) {
    button.disabled = true;
  }
  try {
    return await work();
  } finally {
    if (button !== null) {
      button.disabled = previous;
    }
  }
}

const value = (id) => byID(id).value.trim();

// ---------------------------------------------------------------------
// Projects, agents, candidates
// ---------------------------------------------------------------------

const projectResult = byID("project-result");
const agentResult = byID("agent-result");
const candidateResult = byID("candidate-result");

async function loadProject(id, submitter) {
  await busy(submitter, async () => {
    try {
      render.renderProject(projectResult, await api.getProject(id));
      clearProblem();
      openManage("manage-project");
    } catch (error) {
      report(projectResult, error);
    }
  });
}

async function loadAgent(id, submitter) {
  await busy(submitter, async () => {
    try {
      render.renderAgent(agentResult, await api.getAgent(id));
      clearProblem();
      openManage("manage-agent");
    } catch (error) {
      report(agentResult, error);
    }
  });
}

async function loadCandidate(id, submitter) {
  await busy(submitter, async () => {
    try {
      render.renderCandidate(candidateResult, await api.getCandidate(id));
      clearProblem();
      openManage("manage-candidate");
    } catch (error) {
      report(candidateResult, error);
    }
  });
}

byID("form-create-project").addEventListener("submit", async (event) => {
  event.preventDefault();
  const submitter = event.submitter;
  await busy(submitter, async () => {
    try {
      // The created entity is opened for this page session. A convenience, not
      // a catalog: nothing is persisted and a reload forgets it.
      render.renderProject(projectResult, await api.createProject(value("project-id"), value("project-name")));
      clearProblem();
    } catch (error) {
      report(projectResult, error);
    }
  });
});

byID("form-create-agent").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!requireField("agent-project-id", "Choose the project this agent belongs to.")) {
    return;
  }
  await busy(event.submitter, async () => {
    try {
      render.renderAgent(agentResult, await api.createAgent(
        value("agent-id"), value("agent-project-id"), value("agent-name"),
      ));
      clearProblem();
    } catch (error) {
      report(agentResult, error);
    }
  });
});

byID("form-create-candidate").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!requireField("candidate-agent-id", "Choose the agent this candidate is a version of.")) {
    return;
  }
  await busy(event.submitter, async () => {
    // The fixed metadata fields task 052 defines. Not a map: an open-ended bag
    // would be a schema this task has no mandate to invent.
    const metadata = {
      label: value("candidate-label"),
      source_ref: value("candidate-source-ref"),
      artifact_digest: value("candidate-artifact-digest"),
      model: value("candidate-model"),
      toolset_digest: value("candidate-toolset-digest"),
      config_digest: value("candidate-config-digest"),
    };
    try {
      render.renderCandidate(candidateResult, await api.createCandidate(
        value("candidate-id"), value("candidate-agent-id"), metadata,
      ));
      clearProblem();
    } catch (error) {
      report(candidateResult, error);
    }
  });
});

// ---------------------------------------------------------------------
// Evaluation runs
// ---------------------------------------------------------------------

const runResult = byID("run-result");
const progressResult = byID("progress-result");
const lifecycleButtons = ["run-start", "run-complete", "run-cancel", "run-fail", "run-refresh"];

let openRunID = "";

function setOpenRun(id) {
  openRunID = id;
  const enabled = id !== "";
  for (const buttonID of lifecycleButtons) {
    // Enabled because a run is open, not because a transition is judged legal.
    byID(buttonID).disabled = !enabled;
  }
}

async function refreshRun(submitter) {
  if (openRunID === "") {
    return;
  }
  await busy(submitter, async () => {
    try {
      render.renderRun(runResult, await api.getRun(openRunID));
      render.renderProgress(progressResult, await api.getProgress(openRunID));
      clearProblem();
    } catch (error) {
      report(runResult, error);
    }
  });
}

async function loadRun(id, submitter) {
  await busy(submitter, async () => {
    try {
      render.renderRun(runResult, await api.getRun(id));
      setOpenRun(id);
      clearProblem();
      openManage("manage-evaluation");
      try {
        render.renderProgress(progressResult, await api.getProgress(id));
      } catch (progressError) {
        report(progressResult, progressError);
      }
    } catch (error) {
      report(runResult, error);
    }
  });
}

byID("form-create-run").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!requireField("run-candidate-id", "Choose the candidate this run executes.")
    || !requireField("run-environment", "Choose the environment this run records.")) {
    return;
  }
  await busy(event.submitter, async () => {
    try {
      const run = await api.createRun(
        value("run-id"), value("run-candidate-id"),
        value("run-environment"), value("run-profile"),
      );
      render.renderRun(runResult, run);
      setOpenRun(value("run-id"));
      clearProblem();
    } catch (error) {
      report(runResult, error);
    }
  });
});

// One handler shape for all four transitions. Each sends the request and shows
// whatever the server says — including a refusal, which is information rather
// than a bug.
function lifecycle(buttonID, call) {
  byID(buttonID).addEventListener("click", async () => {
    if (openRunID === "") {
      return;
    }
    const button = byID(buttonID);
    await busy(button, async () => {
      try {
        const run = await call(openRunID);
        render.renderRun(runResult, run);
        clearProblem();
        // The panel below already shows the new record, but a status moving
        // from "running" to "completed" is one word changing in a definition
        // list. This says the server accepted the transition, in the status
        // the server returned — never a word this page chose.
        notify(`Run ${openRunID} is ${run.status}.`, "done");
        try {
          render.renderProgress(progressResult, await api.getProgress(openRunID));
        } catch (ignored) {
          // The transition succeeded; a failed progress read is reported by
          // the refresh action rather than masking the result above.
        }
      } catch (error) {
        report(runResult, error);
        // A refused transition is the server's answer and the reason it is
        // worth surfacing: the button stays enabled, so without this the
        // only sign of a refusal is a panel the reader may not be looking at.
        notify(error.operational
          ? "The control plane could not answer. This is not a gate result."
          : error.message, "fail");
      }
    });
  });
}

lifecycle("run-start", api.startRun);
lifecycle("run-complete", api.completeRun);
lifecycle("run-cancel", api.cancelRun);
lifecycle("run-fail", (id) => api.failRun(id, byID("run-fail-reason").value));
byID("run-refresh").addEventListener("click", () => refreshRun(byID("run-refresh")));

// ---------------------------------------------------------------------
// Open by ID
// ---------------------------------------------------------------------

function openForm(formID, inputID, loader) {
  byID(formID).addEventListener("submit", (event) => {
    event.preventDefault();
    const id = value(inputID);
    if (id === "") {
      return;
    }
    loader(id, event.submitter);
  });
}

openForm("form-open-project", "open-project-id", loadProject);
openForm("form-open-agent", "open-agent-id", loadAgent);
openForm("form-open-candidate", "open-candidate-id", loadCandidate);
openForm("form-open-run", "open-run-id", loadRun);

// ---------------------------------------------------------------------
// The Live Observatory — the default surface
// ---------------------------------------------------------------------
//
// Three regions and a timeline, driven by one unfiltered subscription. The
// page asks for nothing: it subscribes on load, discovers active scopes from
// RealtimeScope alone, and draws the most recently active run until a
// developer pins a different one.
//
// Two subscriptions exist, deliberately separate rather than one mode switch.
// The global one answers "what is happening now". The single-run watch under
// Investigate narrows to one run and reads its authoritative snapshot, which
// is still the right tool when a run is finishing.
//
// Neither polls. Every request is caused by a person, by the single startup
// snapshot, or by a reconnect taking that same snapshot again.

const connChip = byID("conn-chip");
const connText = byID("conn-text");
const canvasScope = byID("canvas-scope");
const railHost = byID("rail");
const canvasNotices = byID("canvas-notices");
const inspectorHost = byID("inspector");
const timelineHost = byID("timeline");

const watchState = byID("watch-state");
const liveSnapshot = byID("live-snapshot");
const watchReconnect = byID("watch-reconnect");
const watchStop = byID("watch-stop");

// Connection state, in the header, in words.
//
// "Live" is never shown while the connection is not established: a graph that
// keeps drawing during a reconnect is claiming a continuity it does not have.
const CONN_TEXT = Object.freeze({
  [STATE.IDLE]: "Idle",
  [STATE.CONNECTING]: "Connecting",
  [STATE.RESYNCING]: "Syncing",
  [STATE.LIVE]: "Live",
  [STATE.RECONNECTING]: "Reconnecting",
  [STATE.FAILED]: "Disconnected",
});

const CONN_CLASS = Object.freeze({
  [STATE.IDLE]: "conn-idle",
  [STATE.CONNECTING]: "conn-wait",
  [STATE.RESYNCING]: "conn-wait",
  [STATE.LIVE]: "conn-live",
  [STATE.RECONNECTING]: "conn-wait",
  [STATE.FAILED]: "conn-down",
});

const STATE_TEXT = Object.freeze({
  [STATE.IDLE]: "Not watching.",
  [STATE.CONNECTING]: "Connecting — waiting for the stream to synchronize.",
  [STATE.RESYNCING]: "Synchronized. Reading authoritative state…",
  [STATE.LIVE]: "Live.",
  [STATE.RECONNECTING]: "Disconnected. Reconnecting…",
  [STATE.FAILED]: "Failed. The stream never synchronized; use Reconnect now to try again.",
});

// prefers-reduced-motion, read once and re-read on change.
//
// When it is set no pulse element is created at all — the stylesheet also
// disables movement, and doing both means a reduced-motion browser never even
// builds the animated element. Every fact the pulse carried stays in the
// edge's state text, the NEW badge and the timeline row.
const reducedMotionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
let reducedMotion = reducedMotionQuery.matches;

const liveModel = new LiveModel();
const timeline = new TimelineFeed();
const labels = new LabelCache({ getAgent: (id) => api.getAgent(id) });
const hierarchy = new HierarchyBrowser({
  listProjects: (after) => api.listProjects(after),
  listProjectAgents: (projectID, after) => api.listProjectAgents(projectID, after),
  listAgentCandidates: (agentID, after) => api.listAgentCandidates(agentID, after),
  listCandidateRuns: (candidateID, after) => api.listCandidateRuns(candidateID, after),
});

const canvas = new GraphCanvas(byID("canvas"), {
  reducedMotion,
  onSelectEdge: (edgeID) => selectEdge(edgeID),
});

reducedMotionQuery.addEventListener("change", (event) => {
  reducedMotion = event.matches;
  canvas.reducedMotion = reducedMotion;
});

// The inspected behavior, by fingerprint within the selected run. Ephemeral UI
// state: a reload legitimately forgets it.
let inspectedEdgeID = "";

// Authoritative counters for the selected run, read from /v1 once per
// selection. Never accumulated from the stream — a count derived from SSE
// frames would drift the moment one was dropped, and the stream's own bound
// makes dropping possible by design.
let authoritative = { runID: "", recordCount: "", distinctCount: "", complete: null };

const labelForScope = (scope) => labels.labelFor("agent", scope.agent_id);

labels.onChange = () => {
  drawRail();
  drawCanvas();
  drawHeader();
};

function drawRail() {
  if (overview !== null) {
    overview.drawLive();
  }
  renderRail(railHost, liveModel.cards, {
    selectedKey: liveModel.selectedKey,
    following: liveModel.following,
    labelFor: labelForScope,
    onSelect: (key) => {
      // An explicit choice pins it. Activity elsewhere raises its own card and
      // never takes the graph being read.
      liveModel.select(key);
      inspectedEdgeID = "";
      void refreshAuthoritative();
      drawAll();
    },
    onFollow: () => {
      liveModel.follow();
      inspectedEdgeID = "";
      void refreshAuthoritative();
      drawAll();
    },
  });
}

function drawCanvas() {
  const card = liveModel.selectedCard();
  const agentLabel = card === undefined
    ? ""
    : (labelForScope(card.scope) || card.scope.agent_id || "");
  canvas.render(liveModel.graph, agentLabel);
  canvas.highlight(inspectedEdgeID, liveModel.graph);
  renderCanvasNotices(canvasNotices, liveModel.graph);
}

function drawInspector() {
  const card = liveModel.selectedCard();
  const edge = liveModel.graph === null || inspectedEdgeID === ""
    ? null
    : liveModel.graph.edges.get(inspectedEdgeID) || null;
  renderInspector(inspectorHost, edge, {
    hasActivity: liveModel.cards.size > 0,
    agentID: card === undefined ? "" : card.scope.agent_id,
    runID: card === undefined ? "" : card.scope.run_id,
    candidateID: card === undefined ? "" : card.scope.candidate_id,
    projectID: card === undefined ? "" : card.scope.project_id,
  });
}

function drawTimeline() {
  renderTimeline(timelineHost, timeline, {
    selectedFingerprint: inspectedEdgeID,
    onSelect: (entry) => {
      // Selecting a row selects the behavior — and the run it belongs to,
      // because inspecting evidence from a run the canvas is not drawing
      // would show a fingerprint with no topology around it.
      if (entry.scopeKey !== liveModel.selectedKey) {
        liveModel.select(entry.scopeKey);
        void refreshAuthoritative();
      }
      inspectedEdgeID = entry.fingerprintID;
      drawAll();
      // Focus follows the selection, so a keyboard user lands on what they
      // just opened rather than staying in the table.
      inspectorHost.focus({ preventScroll: true });
    },
  });
}

// drawHeader fills the Live view's summary strip.
//
// The strip carries two kinds of figure and keeps them apart: what this
// connection has seen, and what the control plane holds. A frame count and a
// record count are different facts, and one strip item that blurred them
// would be reporting the stream as the database — so the live figure is
// labelled "Seen on this connection" and never sits under a count's label.
function drawHeader() {
  const card = liveModel.selectedCard();
  if (card === undefined) {
    dash.summaryStrip(byID("live-strip"), [
      { key: "Agent", value: "None selected" },
      { key: "Counts", value: "Select a run" },
    ]);
    canvasScope.textContent = "No run selected.";
    return;
  }

  const name = labelForScope(card.scope);
  const items = [
    { key: "Agent", value: name || card.scope.agent_id || "(unnamed agent)" },
  ];
  if (card.scope.environment) {
    items.push({ key: "Environment", value: card.scope.environment });
  }
  if (card.scope.run_id) {
    items.push({ key: "Run", value: card.scope.run_id });
  }
  items.push({ key: "Seen on this connection", value: String(card.seenLive) });

  if (authoritative.runID === card.scope.run_id && authoritative.recordCount !== "") {
    items.push({ key: "Observations", value: authoritative.recordCount });
    if (authoritative.distinctCount !== "") {
      items.push({ key: "Behaviors", value: authoritative.distinctCount });
    }
    if (authoritative.complete === false) {
      items.push({ key: "Evidence", value: "incomplete" });
    } else if (authoritative.complete === true) {
      items.push({ key: "Evidence", value: "complete" });
    }
  } else {
    items.push({ key: "Observations", value: "loading…" });
  }
  dash.summaryStrip(byID("live-strip"), items);

  canvasScope.textContent = card.scope.run_id
    ? `Drawing run ${card.scope.run_id}.`
    : "Drawing the selected run.";
}

function drawAll() {
  drawRail();
  drawCanvas();
  drawInspector();
  drawTimeline();
  drawHeader();
}

function selectEdge(edgeID) {
  inspectedEdgeID = edgeID;
  drawCanvas();
  drawInspector();
  drawTimeline();
}

// refreshAuthoritative reads the selected run's progress, once per selection.
//
// One bounded by-id read caused by a person choosing a scope — not a walk, and
// emphatically not per observation. A failure leaves the previous counts and
// says nothing: the header is not where an unreachable control plane should be
// reported, and the connection chip already carries that.
async function refreshAuthoritative() {
  const card = liveModel.selectedCard();
  if (card === undefined || !card.scope.run_id) {
    authoritative = { runID: "", recordCount: "", distinctCount: "", complete: null };
    return;
  }
  const runID = card.scope.run_id;
  authoritative = { runID, recordCount: "", distinctCount: "", complete: null };
  try {
    const progress = await api.getProgress(runID);
    // A late response for a scope the developer has since left must not
    // overwrite the current one.
    if (liveModel.selectedCard()?.scope.run_id !== runID) {
      return;
    }
    authoritative = {
      runID,
      recordCount: typeof progress.record_count === "string" ? progress.record_count : "",
      distinctCount: typeof progress.distinct_behavior_count === "number"
        ? String(progress.distinct_behavior_count)
        : "",
      complete: typeof progress.behavior_complete === "boolean"
        ? progress.behavior_complete
        : null,
    };
    drawHeader();
  } catch (ignored) {
    // Counts stay as they were; the connection state is reported elsewhere.
  }
}

// The global session. Its authoritative snapshot is one page of projects and
// nothing else — see the startup budget below.
const liveSession = new RealtimeSession(
  {
    snapshot: () => loadRootProjects(),
    realtimePath: () => api.realtimePath(""),
  },
  {
    onState: (state) => {
      connText.textContent = CONN_TEXT[state] || state;
      connChip.className = `conn-chip ${CONN_CLASS[state] || "conn-idle"}`;
      if (state === STATE.RESYNCING || state === STATE.RECONNECTING) {
        canvasScope.textContent = state === STATE.RECONNECTING
          ? "Disconnected. The graph below is paused and no longer live."
          : "Resynchronizing…";
      }
      // Back to the selection's own wording once live again; without this the
      // canvas kept saying "Resynchronizing…" after the stream had settled.
      if (state === STATE.LIVE) {
        drawHeader();
      }
    },
    onRows: () => {
      // The timeline owns the feed, built from scoped observations. The
      // session's own row window carries no scope and would be a second,
      // poorer copy of the same data.
    },
    onSnapshot: (projects) => {
      renderProjectsLevel(projects);
      // The project selector offers the page the snapshot already read, so
      // opening it costs no request.
      selection.offerProjects(hierarchy.levels.projects);
    },
    onLifecycle: () => {
      // Lifecycle frames move a card's activity, which the observation path
      // already does. Nothing durable is derived from an event.
    },
    onProblem: (reason) => showProblem(`Activity stream: ${reason}`),
    onObservation: (scope, observation) => {
      const at = Date.now();
      const { card, edge } = liveModel.observe(scope, observation, at);

      // One lookup per newly seen agent, never per observation. The card
      // renders its identifier immediately and gains a name when one arrives.
      void labels.resolveAgent(scope.agent_id);

      timeline.add({
        at,
        scopeKey: card.key,
        agentLabel: labelForScope(scope) || scope.agent_id || "",
        fingerprintID: observation.fingerprint_id || "",
        operationCategory: observation.behavior ? observation.behavior.operation_category || "" : "",
        operationName: observation.behavior ? observation.behavior.operation_name || "" : "",
        targetName: observation.behavior ? observation.behavior.target_name || "" : "",
        decision: observation.decision || "",
        riskLevel: observation.risk_level || "",
        newBehavior: observation.new_behavior === true,
      });

      // A newly selected scope needs its authoritative counts.
      if (authoritative.runID !== (card.scope.run_id || "")) {
        void refreshAuthoritative();
      }

      if (edge === null) {
        // The frame belonged to a run the canvas is not drawing. Its card and
        // the timeline record it; the graph does not move.
        drawRail();
        drawTimeline();
        drawHeader();
        return;
      }

      drawRail();
      drawCanvas();
      drawTimeline();
      drawHeader();

      // Exactly one pulse per received observation, after the topology that
      // carries it exists. Nothing animates without a frame behind it.
      canvas.pulse(edge);

      // A new behavior is the thing a developer must not miss, so the
      // inspector opens it — unless they are already reading something else,
      // in which case stealing the panel would be the same discourtesy as
      // stealing the graph.
      if (edge.newBehavior && inspectedEdgeID === "") {
        inspectedEdgeID = edge.id;
        drawCanvas();
        drawInspector();
      }
    },
    onConnect: () => {
      // A card, an edge and a timeline row are all statements about the
      // current stream. A view that survived a reconnect would assert a
      // continuity stream_ready explicitly denies.
      liveModel.reset();
      timeline.breakSegment();
      inspectedEdgeID = "";
      authoritative = { runID: "", recordCount: "", distinctCount: "", complete: null };
      canvas.clear("");
      drawAll();
    },
  },
);

// ---------------------------------------------------------------------
// Bounded discovery, as a console
// ---------------------------------------------------------------------
//
// The startup budget, in one place so it cannot drift: one page of
// GET /v1/projects, no child traversal, no continuation followed. The same on
// every reconnect, so a flapping connection cannot amplify into a crawl.
//
// 64 projects × 64 agents × 64 candidates × 64 runs is sixteen million rows
// across a quarter of a million requests, during which the resync buffer holds
// 64 frames. Under live traffic that buffer overflows, the client abandons and
// resynchronizes, and the crawl starts again — a resync loop that gets worse
// the larger the database is. A bounded route is not a bounded workflow.
//
// Which is why the runs table is reached through a visible list of agents and
// a visible list of candidates rather than a single "all runs in this project"
// collection: /v1 publishes no such collection, and synthesising one here
// would be exactly the crawl above. Two bounded lists are the honest shape of
// the data, and being visible is what keeps them from being a menu.

async function loadRootProjects(after) {
  return hierarchy.loadProjects(after);
}

// Loading is a state a surface can be in, not the absence of one. Each
// collection carries its own flag so a table that is fetching draws the shape
// of the rows that are coming instead of an empty line that looks like "there
// is nothing here".
// The hierarchy's loading flags, one per level.
//
// The run workspace's and Compare's live in their state modules instead,
// because clearing one is an ownership decision — only the request that
// raised a flag may lower it — and a rule enforced by a module cannot be
// forgotten at a call site. These four stay here because openLevel is the
// only thing that touches them.
const loading = {
  projects: false,
  agents: false,
  candidates: false,
  runs: false,
};

// The run workspace's state, and the project-scoped state Compare and
// Promotions share. Both own the rule that their contents belong to one
// subject, and both refuse a response that arrived for a subject the reader
// has left. See views/run-state.js and views/project-scope.js.
const runState = createRunState();
const projectScope = createProjectScope();

let openedProject = "";
let openedProjectName = "";
let openedAgent = "";
let openedAgentName = "";
let openedCandidate = "";

// ------------------------------- location --------------------------------

// drawCrumbs renders the path that reached the current view.
//
// Built from the selection, not from a history of clicks: the trail says what
// is selected, so it is the same whether a reader arrived by browsing or by
// opening something directly.
function drawCrumbs() {
  const host = byID("crumbs");
  const trail = [];
  if (currentView === "view-projects" || openedProject === "") {
    if (openedProject !== "") {
      trail.push({ label: openedProjectName || openedProject });
    }
  } else {
    trail.push({
      label: openedProjectName || openedProject,
      onOpen: () => { openView("view-projects"); onEnterView("view-projects"); },
    });
  }
  if (currentView === "view-runs" || currentView === "view-run") {
    if (openedAgent !== "") {
      trail.push({
        label: openedAgentName || openedAgent,
        onOpen: () => { openView("view-runs"); },
      });
    }
    if (openedCandidate !== "") {
      trail.push({
        label: openedCandidate,
        onOpen: () => { openView("view-runs"); },
      });
    }
  }
  if (currentView === "view-run" && runState.runID !== "") {
    trail.push({ label: runState.runID });
  }
  dash.renderCrumbs(host, trail);
}

// setProjectScope records which project the console is scoped to.
//
// The sidebar states it, and the forms that need a project are filled from
// it. Filling a field is a convenience and the field stays editable — the
// value sent is still whatever it holds when it is submitted.
function setProjectScope(id, name) {
  openedProject = id;
  openedProjectName = name || "";
  byID("scope-project").textContent = name || id || "None selected";
  byID("promotion-project").value = id;
  byID("promotion-list-project").value = id;
  byID("agent-project-id").value = id;

  // An actual change, not a re-render. Compare and Promotions hold records
  // that belong to a project, and both used to survive one being swapped
  // underneath them — so project B's Compare showed A's agents and B's
  // Promotions showed A's history, under B's name in the sidebar.
  //
  // setProject abandons anything in flight for the project being left and
  // drops the agent, the candidate, both chosen comparison sides and the
  // promotion cursor. resetCompare clears what the browser is holding, and
  // both destinations redraw empty rather than keeping rows a reader would
  // reasonably read as B's.
  if (projectScope.setProject(id)) {
    compareHierarchy.reset();
    resetPromotionHistory();
    renderCompareView();
    render.clear(compareResult);
    compareResult.append(render.emptyState("No comparison run."));
    render.clear(promotionHistory);
    promotionHistory.append(render.emptyState("No promotions listed."));
    // A decision opened from the previous project's history is not this
    // project's, however it is spelled.
    byID("promotion-detail").hidden = true;
    render.clear(byID("promotion-detail"));
    // The finding's evidence context came from a comparison of the previous
    // project's runs, so it is no longer about anything on screen. Reset to
    // the empty shape rather than null: the evidence controls read its two
    // fields, and a null here would move this bug rather than fix it.
    evidenceComparison = { referenceRunID: "", candidateRunID: "" };
    // The hierarchy the Runs destination browses is project-scoped too, so
    // an agent or candidate page still in flight for the previous project
    // must not land in it. The projects page itself is not project-scoped
    // and is left alone.
    hierarchy.invalidate();
  }
  drawCrumbs();
}

// --------------------------------- projects -------------------------------

// Client-side filtering over the rows already on screen, and nothing more.
//
// The box narrows what is visible; it never asks the server for a page it was
// not going to fetch, and it never implies the collection is only what it can
// see. The row count beside it says which of the two is happening.
let projectFilter = "";

function matchesFilter(text, filter) {
  return filter === "" || String(text).toLowerCase().includes(filter);
}

function renderProjectsView() {
  const level = hierarchy.levels.projects;
  const rows = level.rows.filter(
    (row) => matchesFilter(row.id, projectFilter) || matchesFilter(row.name || "", projectFilter),
  );

  dash.summaryStrip(byID("projects-strip"), [
    { key: "Projects on this page", value: level.loaded ? String(level.rows.length) : "" },
    // Only when a filter is narrowing something, so the strip never states a
    // figure that is the one beside it.
    { key: "Matching the filter", value: projectFilter === "" ? "" : String(rows.length) },
  ]);

  dash.dataTable(byID("projects-table"), {
    loading: loading.projects,
    skeletonRows: 4,
    emptyTitle: level.loaded && level.rows.length === 0
      ? "No projects yet"
      : "Nothing matches that filter",
    emptyHint: level.loaded && level.rows.length === 0
      ? "A project is created by the CLI, the API, or under Manage."
      : "Clear the filter to see the rest of this page.",
    emptyIcon: "projects",
    columns: [
      {
        key: "name",
        label: "Project",
        cell: (row) => dash.identChip(row.name || row.id, () => openProject(row), "Open project"),
      },
      { key: "id", label: "Identifier", className: "ident", cell: (row) => row.id },
    ],
    rows,
    keyOf: (row) => row.id,
    selected: openedProject,
    onOpen: (row) => openProject(row),
  });

  byID("projects-page-state").textContent = level.loaded
    ? (level.nextAfter === ""
      ? `${level.rows.length} project(s); this is the whole collection.`
      : `${level.rows.length} project(s) on this page; more exist.`)
    : "";
  byID("projects-pager").hidden = level.nextAfter === "";
  navCount("nav-projects", level.loaded ? String(level.rows.length) : "");
}

// openProject scopes the console and moves to that project's runs.
//
// One request: the project's first page of agents. Nothing below it is
// fetched until a reader asks for it.
function openProject(row) {
  setProjectScope(row.id, row.name);
  openedAgent = "";
  openedAgentName = "";
  openedCandidate = "";
  selection.assume("project", row);
  openView("view-runs");
  void openLevel("agents", () => hierarchy.loadAgents(row.id, ""));
}

byID("projects-search").addEventListener("input", (event) => {
  projectFilter = event.target.value.trim().toLowerCase();
  renderProjectsView();
});

byID("projects-more").addEventListener("click", () => {
  void openLevel("projects", () => hierarchy.loadProjects(hierarchy.levels.projects.nextAfter));
});

byID("scope-change").addEventListener("click", () => {
  openView("view-projects");
  onEnterView("view-projects");
});

// ----------------------------------- runs ---------------------------------

const RUN_STATUS_FILTERS = Object.freeze([
  Object.freeze({ key: "", label: "All" }),
  Object.freeze({ key: "created", label: "Created" }),
  Object.freeze({ key: "running", label: "Running" }),
  Object.freeze({ key: "completed", label: "Completed" }),
  Object.freeze({ key: "failed", label: "Failed" }),
  Object.freeze({ key: "cancelled", label: "Cancelled" }),
]);

let runStatusFilter = "";
let runFilter = "";

function renderRunStatusChips() {
  const host = byID("runs-status-filter");
  render.clear(host);
  for (const option of RUN_STATUS_FILTERS) {
    const chip = render.element("button", "chip-button", option.label);
    chip.type = "button";
    chip.setAttribute("aria-pressed", runStatusFilter === option.key ? "true" : "false");
    chip.addEventListener("click", () => {
      runStatusFilter = option.key;
      renderRunsView();
    });
    host.append(chip);
  }
}

function renderAgentsList() {
  dash.pickList(byID("agents-list"), byID("agents-more"), {
    loading: loading.agents,
    level: hierarchy.levels.agents,
    selected: openedAgent,
    pendingMessage: "Choose a project.",
    emptyMessage: "This project has no agents.",
    labelOf: (row) => row.name || row.id,
    detailOf: (row) => (row.name && row.name !== row.id ? row.id : ""),
    onOpen: (row) => {
      openedAgent = row.id;
      openedAgentName = row.name || "";
      openedCandidate = "";
      runsScopeOpen = true;
      labels.remember("agent", row.id, row.name);
      byID("candidate-agent-id").value = row.id;
      selection.assume("agent", row);
      void openLevel("candidates", () => hierarchy.loadCandidates(row.id, ""));
    },
  });
  byID("agents-count").textContent = hierarchy.levels.agents.loaded
    ? `${hierarchy.levels.agents.rows.length} on this page`
    : "";
}

function renderCandidatesList() {
  dash.pickList(byID("candidates-list"), byID("candidates-more"), {
    loading: loading.candidates,
    level: hierarchy.levels.candidates,
    selected: openedCandidate,
    pendingMessage: "Choose an agent.",
    emptyMessage: "This agent has no candidates.",
    labelOf: (row) => (row.metadata && row.metadata.label ? row.metadata.label : row.id),
    detailOf: (row) => (row.metadata && row.metadata.label && row.metadata.label !== row.id
      ? row.id
      : ""),
    onOpen: (row) => {
      openedCandidate = row.id;
      byID("run-candidate-id").value = row.id;
      runsScopeOpen = false;
      selection.assume("candidate", row);
      void openLevel("runs", () => hierarchy.loadRuns(row.id, ""));
    },
  });
  byID("candidates-count").textContent = hierarchy.levels.candidates.loaded
    ? `${hierarchy.levels.candidates.rows.length} on this page`
    : "";
}

// runColumns are the columns a run table shows, in one place.
//
// Shared by the Runs destination and Compare's selection table, so the two
// cannot drift into describing the same record differently.
function runColumns(onOpen) {
  return [
    {
      key: "id",
      label: "Run",
      cell: (row) => dash.identChip(row.id, () => onOpen(row), "Open run"),
    },
    { key: "candidate_id", label: "Candidate", className: "ident", cell: (row) => row.candidate_id },
    { key: "status", label: "Status", cell: (row) => row.status },
    { key: "environment", label: "Environment", cell: (row) => row.environment },
    { key: "behavioral_profile", label: "Profile", cell: (row) => row.behavioral_profile },
    { key: "created_at", label: "Created", className: "ident", cell: (row) => dash.shortTime(row.created_at) },
    { key: "started_at", label: "Started", className: "ident", cell: (row) => dash.shortTime(row.started_at) },
    { key: "finished_at", label: "Finished", className: "ident", cell: (row) => dash.shortTime(row.finished_at) },
  ];
}

function renderRunsView() {
  renderAgentsList();
  renderCandidatesList();
  renderRunStatusChips();

  const level = hierarchy.levels.runs;
  const rows = level.rows.filter((row) => {
    if (runStatusFilter !== "" && row.status !== runStatusFilter) {
      return false;
    }
    return matchesFilter(row.id, runFilter) || matchesFilter(row.candidate_id || "", runFilter);
  });

  const narrowed = runStatusFilter !== "" || runFilter !== "";
  dash.summaryStrip(byID("runs-strip"), [
    { key: "Agent", value: openedAgentName || openedAgent },
    { key: "Candidate", value: openedCandidate },
    { key: "Runs on this page", value: level.loaded ? String(level.rows.length) : "" },
    { key: "Shown", value: level.loaded && narrowed ? String(rows.length) : "" },
  ]);

  dash.dataTable(byID("runs-table"), {
    loading: loading.runs,
    skeletonRows: 4,
    emptyTitle: !level.loaded
      ? "Choose an agent, then a candidate"
      : (level.rows.length === 0 ? "This candidate has no runs" : "Nothing matches those filters"),
    emptyHint: !level.loaded
      ? "The two lists above are this project's agents and their versions."
      : (level.rows.length === 0
        ? "A run appears here once one is created for this candidate."
        : "Clear the status chip or the filter to see the rest of this page."),
    emptyIcon: "runs",
    columns: runColumns((row) => { void openRunDetail(row.id); }),
    rows,
    keyOf: (row) => row.id,
    selected: runState.runID,
    onOpen: (row) => { void openRunDetail(row.id); },
  });

  byID("runs-page-state").textContent = level.loaded
    ? (level.nextAfter === ""
      ? `${level.rows.length} run(s); this is the whole collection.`
      : `${level.rows.length} run(s) on this page; more exist.`)
    : "";
  byID("runs-pager").hidden = level.nextAfter === "";
  navCount("nav-runs", level.loaded ? String(level.rows.length) : "");
  syncRunsScope();
}

// The scope bar is a chooser, and once it has chosen it is two lists taking
// a third of the screen to state what the strip below already says. It
// collapses on selection and comes back on request; nothing it held is lost,
// because the agent and the candidate are both on the strip.
let runsScopeOpen = true;

function syncRunsScope() {
  const chosen = openedCandidate !== "";
  const collapse = chosen && !runsScopeOpen;
  byID("runs-scope").hidden = collapse;
  const toggle = byID("runs-scope-toggle");
  toggle.hidden = !chosen;
  toggle.setAttribute("aria-expanded", collapse ? "false" : "true");
  toggle.textContent = collapse ? "Change agent or candidate" : "Hide the chooser";
}

byID("runs-scope-toggle").addEventListener("click", () => {
  runsScopeOpen = !runsScopeOpen;
  syncRunsScope();
});

byID("runs-search").addEventListener("input", (event) => {
  runFilter = event.target.value.trim().toLowerCase();
  renderRunsView();
});
byID("agents-more").addEventListener("click", () => {
  void openLevel("agents",
    () => hierarchy.loadAgents(hierarchy.parents.agents, hierarchy.levels.agents.nextAfter));
});
byID("candidates-more").addEventListener("click", () => {
  void openLevel("candidates",
    () => hierarchy.loadCandidates(hierarchy.parents.candidates, hierarchy.levels.candidates.nextAfter));
});
byID("runs-more").addEventListener("click", () => {
  void openLevel("runs",
    () => hierarchy.loadRuns(hierarchy.parents.runs, hierarchy.levels.runs.nextAfter));
});

// renderProjectsLevel redraws every level the browser holds.
//
// Kept under its old name because the realtime snapshot calls it: a reconnect
// re-reads one page of projects and this is what puts it on screen.
function renderProjectsLevel() {
  renderProjectsView();
  renderRunsView();
}

// openLevel performs exactly one bounded request per user action.
// hierarchySurface owns the Runs destination's reads.
//
// The browser itself refuses to commit a page whose generation has moved, so
// a stale response never reaches `levels`. This owns what happens after the
// read: which render wins, whose error is shown, and whose skeleton the
// loading flag belongs to.
const hierarchySurface = createSurface("hierarchy");

async function openLevel(level, load) {
  const ticket = hierarchySurface.begin(`${openedProject}\u0000${level}`);
  // Raised before the request and lowered whatever happens to it, so a
  // failure leaves a message rather than a skeleton that never resolves.
  loading[level] = true;
  renderProjectsLevel();
  try {
    await load();
    if (!hierarchySurface.owns(ticket)) {
      return;
    }
    clearProblem();
  } catch (error) {
    if (!hierarchySurface.owns(ticket)) {
      return;
    }
    loading[level] = false;
    report(byID(LEVEL_HOSTS[level] || "runs-table"), error);
    return;
  } finally {
    // Only the current request may lower the flag. An older one finishing
    // late would otherwise take away the skeleton a newer read just raised.
    if (hierarchySurface.owns(ticket)) {
      loading[level] = false;
    }
  }
  renderProjectsLevel();
}

const LEVEL_HOSTS = Object.freeze({
  projects: "projects-table",
  agents: "agents-list",
  candidates: "candidates-list",
  runs: "runs-table",
});

// ------------------------------ run workspace -----------------------------
//
// One run, opened by clicking its row. Three sections over the same record:
// what the control plane holds about the run, the observations it retained,
// and the distinct behaviors those observations fell into.
//
// Every figure on the strip is a value /v1 returned. None is counted from the
// rows on screen — retained history is bounded and the authoritative counts
// are not, so a table that counted itself would report how much it drew.

function renderRunStrip() {
  const items = [];
  const runDetail = runState.detail;
  const runProgress = runState.progress;
  if (runDetail !== null) {
    // A failed or cancelled run is the one fact on this strip somebody needs
    // to see before they read anything else.
    const stopped = runDetail.status === "failed" || runDetail.status === "cancelled";
    items.push({
      key: "Status",
      value: runDetail.status,
      className: stopped ? "is-stop" : "",
    });
    items.push({ key: "Candidate", value: runDetail.candidate_id });
    items.push({ key: "Environment", value: runDetail.environment });
  }
  if (runProgress !== null) {
    items.push({ key: "Records", value: runProgress.record_count });
    items.push({ key: "Behavior observations", value: runProgress.behavior_observation_count });
    items.push({ key: "Distinct behaviors", value: runProgress.distinct_behavior_count });
  }
  dash.summaryStrip(byID("run-strip"), items);
}

// openRunDetail reads one run's authoritative state. One bounded read, plus
// its progress.
//
// `openRun` clears everything scoped to the previous run in one step — both
// tab pages, their cursors, the narrowing, the selected row, the record and
// the progress — and abandons anything still in flight for it. Clearing them
// individually here is how the original was wrong: the behaviors tab kept its
// rows and its loaded flag, so opening a second run showed the first run's
// behaviors and fetched nothing.
async function openRunDetail(runID) {
  runState.openRun(runID);
  closeDetailPanel();
  openView("view-run");
  showRunSection("run-overview");
  byID("page-title").textContent = runID;
  renderRunStrip();
  renderRunActions();

  const ticket = runState.beginRun();
  try {
    const run = await api.getRun(runID);
    if (!runState.commitDetail(ticket, run)) {
      return;
    }
    render.renderRun(byID("investigate-run"), run);
    setOpenRun(runID);
    // The run is now "the" run everywhere a run is chosen — when it belongs
    // to the candidate the context is on. A run opened from somewhere else
    // (a promotion row, a paste) carries its own chain and is adopted.
    if (selection.selection.candidate !== null
      && selection.selection.candidate.id === run.candidate_id) {
      selection.assume("run", run);
    } else {
      void selection.adopt("run", runID);
    }
    byID("watch-run-id").value = runID;
    byID("evidence-run-id").value = runID;
    clearProblem();
    try {
      const progress = await api.getProgress(runID);
      if (!runState.commitProgress(ticket, progress)) {
        return;
      }
      render.renderProgress(byID("investigate-progress"), progress);
    } catch (progressError) {
      if (!runState.ownsRun(ticket)) {
        return;
      }
      report(byID("investigate-progress"), progressError);
    }
  } catch (error) {
    // A failure for a run the reader has left is not this run's failure, and
    // reporting it would replace a newer message with an older one.
    if (!runState.ownsRun(ticket)) {
      return;
    }
    report(byID("investigate-run"), error);
  }
  if (!runState.ownsRun(ticket)) {
    return;
  }
  renderRunStrip();
  renderRunActions();
  drawCrumbs();
}

// renderRunActions offers the things a reader can do with the open run.
//
// Every one of them is navigation or a read. The lifecycle transitions stay
// under Manage: the server decides whether one is legal, and putting them
// beside a table of evidence would suggest this page knew.
function renderRunActions() {
  const host = byID("run-view-actions");
  render.clear(host);
  if (runState.runID === "") {
    return;
  }
  const watch = render.element("button", "link-button", "Watch this run live");
  watch.type = "button";
  watch.addEventListener("click", () => {
    byID("watch-run-id").value = runState.runID;
    openView("view-live");
    byID("watch-start").focus();
  });
  host.append(watch);

  const history = render.element("button", "link-button", "Open in evidence");
  history.type = "button";
  history.addEventListener("click", () => {
    byID("evidence-run-id").value = runState.runID;
    openEvidence("evidence-run");
  });
  host.append(history);
}

// ------------------------------ observations ------------------------------
//
// One bounded page of a run's retained history, optionally narrowed to one
// correlated view. The narrowing is at most one of session, trace or behavior
// — the server refuses more than one — and it is applied in storage before the
// page bound, which is why it is a request parameter and not a filter over a
// page that already arrived.

const OBSERVATION_SCOPES = Object.freeze([
  Object.freeze({ field: "sessionID", label: "Session" }),
  Object.freeze({ field: "traceID", label: "Trace" }),
  Object.freeze({ field: "fingerprintID", label: "Behavior" }),
]);

function renderObservationScopeChips() {
  const observationScope = runState.scope;
  const host = byID("observations-scope");
  render.clear(host);
  let narrowed = false;
  for (const entry of OBSERVATION_SCOPES) {
    const value = observationScope[entry.field];
    if (value === "") {
      continue;
    }
    narrowed = true;
    const chip = render.element("button", "chip-clear");
    chip.type = "button";
    chip.append(render.element("span", null, `${entry.label}: `));
    chip.append(render.element("span", "mono", value));
    chip.append(render.element("span", null, " ✕"));
    chip.setAttribute("aria-label", `Remove the ${entry.label.toLowerCase()} narrowing`);
    chip.addEventListener("click", () => {
      runState.clearNarrowing();
      void loadObservations("");
    });
    host.append(chip);
  }
  if (!narrowed) {
    host.append(render.element("span", "toolbar-note", "Whole run"));
  }
}

// narrowObservations replaces the narrowing with exactly one dimension.
//
// Replaces rather than adds: the server accepts at most one, and offering a
// second control that silently dropped the first would be a page pretending
// to a capability the protocol does not have.
function narrowObservations(field, value) {
  // Retargets the observation surface, so a page still in flight for the
  // previous narrowing cannot land under this one.
  runState.narrow(field, value);
  showRunSection("run-observations");
  void loadObservations("");
}

async function loadObservations(after) {
  if (runState.runID === "") {
    return;
  }
  const runID = runState.runID;
  const scope = runState.scope;
  // Raising the flag and taking the ticket are one step, and only this
  // ticket can lower it again.
  const ticket = runState.beginObservations();
  renderObservationsView();
  try {
    const response = await api.runObservations(runID, scope, after);
    // Superseded — a different run, a different narrowing, or a newer page
    // for this one. Nothing is committed and the flag is left alone, because
    // it belongs to whichever request is current now.
    if (!runState.commitObservations(ticket, response)) {
      return;
    }
    closeDetailPanel();
    clearProblem();
  } catch (error) {
    if (!runState.failObservations(ticket)) {
      return;
    }
    report(byID("observations-table"), error);
    return;
  }
  renderObservationsView();
}

function renderObservationsView() {
  renderObservationScopeChips();

  const observationPage = runState.observations;

  dash.dataTable(byID("observations-table"), {
    loading: runState.observations.loading,
    skeletonRows: 6,
    emptyTitle: observationPage.loaded ? "No retained observation in this view" : "Nothing loaded yet",
    emptyHint: observationPage.loaded
      ? "Retained history is bounded; a narrowing may have no observations under it."
      : undefined,
    emptyIcon: "clock",
    columns: [
      {
        key: "sequence",
        label: "Seq",
        cell: (row) => dash.identChip(
          render.retainedValue(row, "sequence"),
          () => selectObservation(row),
          "Open observation",
        ),
      },
      {
        key: "timestamp",
        label: "Time",
        className: "ident",
        cell: (row) => dash.shortTime(render.retainedValue(row, "timestamp")),
      },
      {
        key: "operation",
        label: "Operation",
        className: "wrap",
        cell: (row) => behaviorText(row.behavior, "operation_name"),
      },
      {
        key: "target",
        label: "Target",
        className: "wrap",
        cell: (row) => behaviorText(row.behavior, "target_name"),
      },
      {
        key: "decision",
        label: "Decision",
        className: "keep",
        // The word is the server's and carries the meaning alone. The mark
        // and the colour are reinforcement, so the column still reads on a
        // monochrome display and to a colour-blind reader.
        cell: (row) => dash.verdict(
          render.retainedValue(row, "decision"),
          dash.decisionEdge(row.decision),
          dash.decisionClass(row.decision),
        ),
      },
      {
        key: "risk_level",
        label: "Risk",
        className: "keep",
        cell: (row) => dash.verdict(
          render.retainedValue(row, "risk_level"),
          dash.riskIsSevere(row.risk_level) ? "flag" : "",
          dash.riskClass(row.risk_level),
        ),
      },
      { key: "trust_score", label: "Trust", align: "right", cell: (row) => render.retainedValue(row, "trust_score") },
      // Anomaly, context risk and the confidences are in the detail panel.
      // Nine columns did not fit beside an open panel, and a column that
      // scrolls out of sight is not a column a reader can scan.
      { key: "duration_nanos", label: "Duration", align: "right", cell: (row) => dash.durationText(render.retainedValue(row, "duration_nanos")) },
    ],
    rows: observationPage.rows,
    keyOf: (row) => String(row.sequence),
    selected: runState.selected,
    edgeOf: (row) => dash.decisionEdge(row.decision),
    onOpen: (row) => selectObservation(row),
  });

  const parts = [];
  if (observationPage.retained !== "") {
    parts.push(`${observationPage.retained} retained`);
  }
  if (observationPage.state !== "") {
    parts.push(observationPage.state === "complete"
      ? "the whole history is retained"
      : `history is ${observationPage.state}`);
  }
  if (observationPage.nextAfter !== "") {
    parts.push("more exist on this page's continuation");
  }
  byID("observations-page-state").textContent = parts.join(" · ");
  byID("observations-pager").hidden = observationPage.nextAfter === "";
}

// behaviorText reads one allowlisted field of a retained behavior.
//
// Through the allowlist, in the same direction render.js uses: the list is
// iterated, never the object, so a field /v1 adds later cannot reach a cell
// because somebody wrote `behavior.new_thing`.
function behaviorText(behavior, key) {
  if (behavior === undefined || behavior === null || !render.BEHAVIOR_FIELDS.includes(key)) {
    return render.displayValue(undefined);
  }
  return render.displayValue(behavior[key]);
}

byID("observations-more").addEventListener("click", () => {
  void loadObservations(runState.observations.nextAfter);
});

// --------------------------- the detail panel -----------------------------

// selectObservation opens one observation beside the table.
//
// The table is not redrawn: the reader's place in it is theirs, and a page
// they had paged forward to must not jump back. Selection moves as a class,
// and closing the panel puts focus back on the row it came from.
function selectObservation(observation) {
  runState.select(observation.sequence);
  dash.markSelected(byID("observations-table"), runState.selected);

  const panel = byID("detail");
  const body = byID("detail-body");
  render.clear(body);
  byID("detail-title").textContent = `Observation ${render.retainedValue(observation, "sequence")}`;

  body.append(dash.detailGroup("Decision", [
    ["Decision", render.retainedValue(observation, "decision")],
    ["Risk level", render.retainedValue(observation, "risk_level")],
    ["Policy rule", render.retainedValue(observation, "policy_rule")],
    ["Matched default", render.retainedValue(observation, "matched_default")],
    ["Approval status", render.retainedValue(observation, "approval_status")],
  ]));

  body.append(dash.detailGroup("Scores", [
    ["Trust", render.retainedValue(observation, "trust_score")],
    ["Anomaly", render.retainedValue(observation, "anomaly_score")],
    ["Anomaly confidence", render.retainedValue(observation, "anomaly_confidence")],
    ["Context risk", render.retainedValue(observation, "context_risk")],
    ["Identity confidence", render.retainedValue(observation, "identity_confidence")],
  ]));

  body.append(dash.detailGroup("Timing", [
    ["Timestamp", render.retainedValue(observation, "timestamp")],
    ["Duration", dash.durationText(render.retainedValue(observation, "duration_nanos"))],
    ["Span status", render.retainedValue(observation, "span_status")],
    ["Span lineage", render.retainedValue(observation, "span_lineage")],
  ]));

  body.append(dash.detailGroup("Behavior", [
    ["Operation", behaviorText(observation.behavior, "operation_name")],
    ["Category", behaviorText(observation.behavior, "operation_category")],
    ["Target", behaviorText(observation.behavior, "target_name")],
    ["Target category", behaviorText(observation.behavior, "target_category")],
    ["Actor", render.retainedValue(observation, "actor_id")],
    ["New behavior", render.retainedValue(observation, "new_behavior")],
  ]));

  // Correlation references. Each one that narrows the table is a control;
  // each one that does not is flat, so a chip never promises a journey the
  // page cannot make.
  body.append(dash.detailRefs("Correlation", [
    {
      key: "Session",
      value: render.retainedValue(observation, "session_id"),
      onOpen: (value) => narrowObservations("sessionID", value),
    },
    {
      key: "Trace",
      value: render.retainedValue(observation, "trace_id"),
      onOpen: (value) => narrowObservations("traceID", value),
    },
    {
      key: "Behavior",
      value: render.retainedValue(observation, "fingerprint_id"),
      onOpen: (value) => narrowObservations("fingerprintID", value),
    },
    { key: "Span", value: render.retainedValue(observation, "span_id") },
    { key: "Parent span", value: render.retainedValue(observation, "parent_span_id") },
    { key: "Delegated from", value: render.retainedValue(observation, "delegated_from") },
    { key: "Event", value: render.retainedValue(observation, "event_id") },
  ]));

  panel.hidden = false;
}

function closeDetailPanel() {
  const panel = byID("detail");
  if (panel.hidden) {
    return;
  }
  panel.hidden = true;
  render.clear(byID("detail-body"));
}

byID("detail-close").addEventListener("click", () => {
  const key = runState.selected;
  closeDetailPanel();
  // The selection survives the panel closing: the row stays marked and keeps
  // the focus, so the reader is where they were rather than at the top.
  dash.focusRow(byID("observations-table"), key);
});

// -------------------------------- behaviors -------------------------------

async function loadBehaviors(after) {
  if (runState.runID === "") {
    return;
  }
  const runID = runState.runID;
  const ticket = runState.beginBehaviors();
  renderBehaviorsView();
  try {
    const response = await api.runBehaviors(runID, after);
    if (!runState.commitBehaviors(ticket, response)) {
      return;
    }
    clearProblem();
  } catch (error) {
    if (!runState.failBehaviors(ticket)) {
      return;
    }
    report(byID("behaviors-table"), error);
    return;
  }
  renderBehaviorsView();
}

function renderBehaviorsView() {
  const behaviorPage = runState.behaviors;
  dash.dataTable(byID("behaviors-table"), {
    loading: runState.behaviors.loading,
    skeletonRows: 4,
    emptyTitle: behaviorPage.loaded ? "This run retained no behaviors" : "Nothing loaded yet",
    emptyIcon: "compare",
    columns: [
      {
        key: "fingerprint_id",
        label: "Behavior",
        cell: (row) => dash.identChip(
          row.fingerprint_id,
          (value) => narrowObservations("fingerprintID", value),
          "Show observations of behavior",
        ),
      },
      { key: "operation_category", label: "Category", cell: (row) => behaviorText(row.behavior, "operation_category") },
      { key: "operation_name", label: "Operation", className: "wrap", cell: (row) => behaviorText(row.behavior, "operation_name") },
      { key: "target_name", label: "Target", className: "wrap", cell: (row) => behaviorText(row.behavior, "target_name") },
      { key: "target_category", label: "Target category", cell: (row) => behaviorText(row.behavior, "target_category") },
      // The authoritative count, from the collection. Never the number of
      // rows this table happens to be showing.
      { key: "observations", label: "Observations", align: "right", cell: (row) => render.displayValue(row.observations) },
    ],
    rows: behaviorPage.rows,
    keyOf: (row) => row.fingerprint_id,
    onOpen: (row) => narrowObservations("fingerprintID", row.fingerprint_id),
  });
  byID("behaviors-page-state").textContent = behaviorPage.loaded
    ? (behaviorPage.nextAfter === ""
      ? `${behaviorPage.rows.length} behavior(s); this is the whole collection.`
      : `${behaviorPage.rows.length} behavior(s) on this page; more exist.`)
    : "";
  byID("behaviors-pager").hidden = behaviorPage.nextAfter === "";
}

byID("behaviors-more").addEventListener("click", () => {
  void loadBehaviors(runState.behaviors.nextAfter);
});

// A run's sections read on demand, one page each, the first time they are
// opened. Opening a run reads the run and its progress and nothing else.
for (const tab of byID("run-tabs").querySelectorAll(".subtab")) {
  tab.addEventListener("click", () => {
    // The detail panel describes a row in the observation table. Leaving it
    // open over another section would show a record beside a table that does
    // not contain it, and its Close would return focus to a row nobody can
    // see. The selection itself survives: coming back re-marks the row.
    if (tab.dataset.section !== "run-observations") {
      closeDetailPanel();
    }
    if (tab.dataset.section === "run-observations") {
      if (runState.observations.loaded) {
        renderObservationsView();
      } else {
        void loadObservations("");
      }
    }
    // `loaded` is meaningful again because opening a run clears it. It used
    // not to be: it stayed true across a run change, so this asked nothing
    // and the tab showed the previous run's behaviors.
    if (tab.dataset.section === "run-behaviors" && !runState.behaviors.loaded) {
      void loadBehaviors("");
    }
  });
}

// ------------------------------ entering a view ---------------------------

// onEnterView reads what a destination needs, once, when it is opened.
//
// One page per destination and never more: nothing here follows a cursor, and
// a destination already holding a page is left alone so returning to it does
// not re-fetch what is on screen.
function onEnterView(viewID) {
  if (viewID === "view-overview" && overview !== null) {
    overview.enter();
  }
  if (viewID === "view-projects" && !hierarchy.levels.projects.loaded) {
    void openLevel("projects", () => hierarchy.loadProjects(""));
  }
  // Both of these ask whether what they are holding is about the project
  // they are scoped to — identity, not a loaded flag. A boolean stayed true
  // across a project change, so the destination kept the previous project's
  // records and fetched nothing.
  if (viewID === "view-compare" && projectScope.needsCompare()) {
    void openCompareLevel("agents", () => compareHierarchy.loadAgents(openedProject, ""));
  }
  // A destination scoped to a project shows that project's records. Making a
  // reader press a button to see history that the scope already determines is
  // the form-first habit this console is replacing.
  if (viewID === "view-promotion" && projectScope.needsPromotions()) {
    void loadPromotionPage(openedProject, "", 1, null);
  }
  // The target list is the scoped project's environments, read once per
  // project when somebody comes to record a decision.
  if (viewID === "view-promotion") {
    void selection.ensure("environments");
  }
  // Runs shows the context's agent and candidate, wherever they were chosen.
  if (viewID === "view-runs" && runsNeedSync) {
    void syncRunsHierarchy();
  }
}

// ---------------------------------------------------------------------
// The single-run watch, unchanged in behaviour
// ---------------------------------------------------------------------

const session = new RealtimeSession(
  {
    snapshot: async (id) => ({
      run: await api.getRun(id),
      progress: await api.getProgress(id),
    }),
    realtimePath: (id) => api.realtimePath(id),
  },
  {
    onState: (state) => {
      watchState.textContent = STATE_TEXT[state] || state;
      const watching = state !== STATE.IDLE;
      watchStop.disabled = !watching;
      watchReconnect.disabled = !watching;
    },
    onRows: () => {
      // The Live timeline owns the feed. A second writer would interleave two
      // connections' rows into one apparent sequence, which is the appearance
      // of history this page must not create.
    },
    onSnapshot: ({ run, progress }) => {
      render.renderSnapshot(liveSnapshot, run, progress);
      // The watch and the Investigate panel describe the same run, so an
      // authoritative read refreshes both rather than letting one go stale.
      if (openRunID === session.runID) {
        render.renderRun(byID("investigate-run"), run);
        render.renderProgress(byID("investigate-progress"), progress);
      }
    },
    onLifecycle: () => {
      // Status arrives with the authoritative read a terminal event triggers.
      // Nothing is derived from the event itself.
    },
    onProblem: (reason) => showProblem(`Realtime: ${reason}`),
  },
);

byID("form-watch").addEventListener("submit", (event) => {
  event.preventDefault();
  const runID = value("watch-run-id");
  if (runID === "") {
    showProblem("Choose a run to watch, or paste its identifier.");
    return;
  }
  render.clear(liveSnapshot);
  liveSnapshot.append(render.emptyState("No snapshot."));
  // Navigation state only.
  window.location.hash = `run=${encodeURIComponent(runID)}`;
  session.watch(runID);
});

watchReconnect.addEventListener("click", () => session.reconnectNow());
watchStop.addEventListener("click", () => {
  session.stop();
  render.clear(liveSnapshot);
  liveSnapshot.append(render.emptyState("No snapshot."));
});

// ---------------------------------------------------------------------
// Compare
// ---------------------------------------------------------------------

const compareResult = byID("compare-result");

// Compare chooses two runs by assigning rows, not by naming identifiers.
//
// The identifier is still the value the server receives — the contract is
// unchanged — but nobody has to find one, copy one or recognise one in a
// menu. A reader browses to a candidate, sees its runs, and says which of
// them is the reference and which is the candidate. Both choices are then
// shown in full, so what is about to be compared is on screen rather than
// implied by two opaque strings.
//
// Compare keeps its own browser. Sharing the Runs destination's would mean
// choosing a comparison moved the reader's place in the run table, and
// changing that table would silently change what a pending comparison meant.
const compareHierarchy = new HierarchyBrowser({
  listProjects: (after) => api.listProjects(after),
  listProjectAgents: (projectID, after) => api.listProjectAgents(projectID, after),
  listAgentCandidates: (agentID, after) => api.listAgentCandidates(agentID, after),
  listCandidateRuns: (candidateID, after) => api.listCandidateRuns(candidateID, after),
});

// The agent, the candidate and the two chosen sides live in projectScope,
// because all four belong to a project and all four used to survive a change
// of one. A reference run assigned under project A stayed assigned under B,
// where that run does not exist.

async function openCompareLevel(level, load) {
  const ticket = projectScope.beginCompare(level);
  renderCompareView();
  try {
    // The browser refuses to commit a page whose generation has moved, so a
    // stale response changes no level. This guards what happens *after* the
    // read: the render, the error and the loading flag.
    await load();
    if (!projectScope.commitCompare(ticket)) {
      return;
    }
    clearProblem();
  } catch (error) {
    if (!projectScope.failCompare(ticket)) {
      return;
    }
    report(byID(COMPARE_LEVEL_HOSTS[level] || "compare-runs-table"), error);
    return;
  }
  renderCompareView();
}

const COMPARE_LEVEL_HOSTS = Object.freeze({
  agents: "compare-agents-list",
  candidates: "compare-candidates-list",
  runs: "compare-runs-table",
});

// assignSide records one run as a side of the comparison.
//
// Assigning a run that already holds the other side moves it, rather than
// letting the same run be both: a run compared against itself is not a
// comparison, and refusing it here means the reader sees why immediately
// instead of reading a server error.
function assignSide(side, run) {
  projectScope.assignSide(side, run);
  renderCompareView();
}

function renderCompareSides() {
  const compareSides = projectScope.sides;
  for (const side of ["reference", "candidate"]) {
    const host = byID(`compare-${side}-summary`);
    const chosen = compareSides[side];
    render.clear(host);
    byID(`compare-side-${side}`).classList.toggle("cmp-side-chosen", chosen !== null);
    if (chosen === null) {
      host.append(render.emptyState(
        `No ${side} chosen. Use a row's ${side === "reference" ? "Reference" : "Candidate"} control above.`,
      ));
      continue;
    }
    render.renderRun(host, chosen);
  }

  const ready = projectScope.comparisonReady();
  byID("compare-submit").disabled = !ready;
  byID("compare-ready").textContent = ready
    ? "Ready to compare."
    : "Choose a reference and a candidate to enable this.";

  // Provenance reads the same two runs, so it needs nothing typed either.
  // Provenance and the promotion form read the same two runs, so neither
  // needs anything typed. Cleared alongside the sides when the project
  // changes, since a run identifier from the previous project is not a
  // convenience there — it is a wrong answer left in a field.
  byID("evidence-provenance-reference").value = compareSides.reference === null
    ? "" : compareSides.reference.id;
  byID("promotion-reference").value = compareSides.reference === null
    ? "" : compareSides.reference.id;
  byID("evidence-provenance-candidate").value = compareSides.candidate === null
    ? "" : compareSides.candidate.id;
  byID("promotion-candidate").value = compareSides.candidate === null
    ? "" : compareSides.candidate.id;
  // The same two runs, shown by the selectors that send them. Declared later
  // in the file, so guarded until the bindings exist.
  if (sideSelectors !== null) {
    sideSelectors.provenanceReference.setLocal(compareSides.reference);
    sideSelectors.provenanceCandidate.setLocal(compareSides.candidate);
    sideSelectors.promotionReference.setLocal(compareSides.reference);
    sideSelectors.promotionCandidate.setLocal(compareSides.candidate);
  }
}

// sideControl renders one row's assign button for one side.
function sideControl(side, run) {
  const held = projectScope.sides[side];
  const chosen = held !== null && held.id === run.id;
  const control = render.element(
    "button",
    "chip-button",
    side === "reference" ? "Reference" : "Candidate",
  );
  control.type = "button";
  control.setAttribute("aria-pressed", chosen ? "true" : "false");
  control.setAttribute("aria-label", `Use ${run.id} as the ${side}`);
  control.addEventListener("click", (event) => {
    event.stopPropagation();
    assignSide(side, run);
  });
  return control;
}

function renderCompareView() {
  dash.pickList(byID("compare-agents-list"), byID("compare-agents-more"), {
    loading: projectScope.compareLoading.agents,
    level: compareHierarchy.levels.agents,
    selected: projectScope.agent,
    pendingMessage: "Choose a project in the sidebar.",
    emptyMessage: "This project has no agents.",
    labelOf: (row) => row.name || row.id,
    detailOf: (row) => (row.name && row.name !== row.id ? row.id : ""),
    onOpen: (row) => {
      projectScope.setAgent(row.id);
      void openCompareLevel("candidates", () => compareHierarchy.loadCandidates(row.id, ""));
    },
  });
  byID("compare-agents-count").textContent = compareHierarchy.levels.agents.loaded
    ? `${compareHierarchy.levels.agents.rows.length} on this page`
    : "";

  dash.pickList(byID("compare-candidates-list"), byID("compare-candidates-more"), {
    loading: projectScope.compareLoading.candidates,
    level: compareHierarchy.levels.candidates,
    selected: projectScope.candidate,
    pendingMessage: "Choose an agent.",
    emptyMessage: "This agent has no candidates.",
    labelOf: (row) => (row.metadata && row.metadata.label ? row.metadata.label : row.id),
    detailOf: (row) => (row.metadata && row.metadata.label && row.metadata.label !== row.id
      ? row.id
      : ""),
    onOpen: (row) => {
      projectScope.setCandidate(row.id);
      void openCompareLevel("runs", () => compareHierarchy.loadRuns(row.id, ""));
    },
  });
  byID("compare-candidates-count").textContent = compareHierarchy.levels.candidates.loaded
    ? `${compareHierarchy.levels.candidates.rows.length} on this page`
    : "";

  const level = compareHierarchy.levels.runs;
  dash.dataTable(byID("compare-runs-table"), {
    loading: projectScope.compareLoading.runs,
    skeletonRows: 3,
    emptyTitle: level.loaded ? "This candidate has no runs" : "Choose an agent, then a candidate",
    emptyHint: level.loaded ? undefined : "Assigning a side needs a run to assign.",
    emptyIcon: "runs",
    columns: [
      { key: "id", label: "Run", className: "ident", cell: (row) => row.id },
      { key: "candidate_id", label: "Candidate", className: "ident", cell: (row) => row.candidate_id },
      { key: "status", label: "Status", cell: (row) => row.status },
      { key: "environment", label: "Environment", cell: (row) => row.environment },
      { key: "created_at", label: "Created", className: "ident", cell: (row) => dash.shortTime(row.created_at) },
      { key: "reference", label: "Set as reference", cell: (row) => sideControl("reference", row) },
      { key: "candidate", label: "Set as candidate", cell: (row) => sideControl("candidate", row) },
    ],
    rows: level.rows,
    keyOf: (row) => row.id,
  });
  byID("compare-runs-page-state").textContent = level.loaded
    ? (level.nextAfter === ""
      ? `${level.rows.length} run(s); this is the whole collection.`
      : `${level.rows.length} run(s) on this page; more exist.`)
    : "";
  byID("compare-runs-pager").hidden = level.nextAfter === "";

  renderCompareSides();
}

byID("compare-agents-more").addEventListener("click", () => {
  void openCompareLevel("agents", () => compareHierarchy.loadAgents(
    compareHierarchy.parents.agents, compareHierarchy.levels.agents.nextAfter,
  ));
});
byID("compare-candidates-more").addEventListener("click", () => {
  void openCompareLevel("candidates", () => compareHierarchy.loadCandidates(
    compareHierarchy.parents.candidates, compareHierarchy.levels.candidates.nextAfter,
  ));
});
byID("compare-runs-more").addEventListener("click", () => {
  void openCompareLevel("runs", () => compareHierarchy.loadRuns(
    compareHierarchy.parents.runs, compareHierarchy.levels.runs.nextAfter,
  ));
});

const LIMIT_INPUTS = Object.freeze([
  Object.freeze({ id: "compare-max-added", key: "maxAddedBehaviors", label: "Max added behaviors" }),
  Object.freeze({ id: "compare-max-block", key: "maxBlockDecisions", label: "Max block decisions" }),
  Object.freeze({ id: "compare-max-critical", key: "maxCriticalRiskObservations", label: "Max critical risk observations" }),
  // Optional (ADR 0052, issue 131): empty means the check is not evaluated,
  // which is not the same request as 0 — so an empty field is omitted, never
  // sent as "0".
  Object.freeze({ id: "compare-max-changes", key: "maxAddedBehaviorChanges", label: "Max added behavior changes", optional: true }),
]);

byID("form-compare").addEventListener("submit", async (event) => {
  event.preventDefault();
  await busy(event.submitter, async () => {
    // The two sides come from the rows the reader assigned, never from a
    // field. The button is disabled until both are chosen, and this is the
    // same condition stated once more so a submit that reached here anyway
    // says what is missing instead of sending an empty identifier.
    if (!projectScope.comparisonReady()) {
      showProblem("Choose a reference run and a candidate run in the table above.");
      return;
    }

    const limits = {};
    for (const input of LIMIT_INPUTS) {
      const text = value(input.id);
      if (input.optional === true && text === "") {
        continue;
      }
      // Validated as canonical decimal text, never parsed. 0 is valid, and the
      // uint64 maximum must reach the server intact — Number() would round it.
      if (!api.isCanonicalUint64(text)) {
        showProblem(`${input.label} must be a whole number from 0 to 18446744073709551615.`);
        byID(input.id).focus();
        return;
      }
      limits[input.key] = text;
    }

    try {
      const response = await api.compare(
        projectScope.sides.reference.id, projectScope.sides.candidate.id, limits,
      );
      // The verdict inside is the server's. This call renders it; it does not
      // recompute it from the limits above.
      //
      // The evidence controls are passed in here rather than built in the
      // renderer, because a finding reference needs the two run identifiers this
      // comparison actually used — and they are the server's echo of them, not
      // the form's contents.
      evidenceComparison = {
        referenceRunID: response.reference_run_id,
        candidateRunID: response.candidate_run_id,
      };
      byID("evidence-provenance-reference").value = response.reference_run_id;
      byID("evidence-provenance-candidate").value = response.candidate_run_id;
      render.renderComparison(compareResult, response, comparisonEvidenceControls());
      clearProblem();
    } catch (error) {
      // A refused comparison is rendered as the refusal it is. Insufficient
      // evidence is not a FAIL, and presenting it as one would invent a
      // verdict the gate never reached.
      report(compareResult, error);
    }
  });
});

// ---------------------------------------------------------------------
// Evidence (task 076)
// ---------------------------------------------------------------------

const evidenceFindingResult = byID("evidence-finding-result");
const evidenceRunResult = byID("evidence-run-result");
const evidenceProvenanceResult = byID("evidence-provenance-result");
const evidenceViewSelect = byID("evidence-view");
const evidenceViewBlurb = byID("evidence-view-blurb");
const evidenceIdentifierField = byID("evidence-identifier-field");
const evidenceIdentifierLabel = byID("evidence-identifier-label");

// The comparison the Evidence surface is currently about.
//
// Set by a successful comparison and by nothing else. A finding reference is two
// run identifiers plus what inside the comparison is being asked about, and both
// identifiers have to come from a comparison that actually succeeded — inventing
// a pair here would produce a citable link to a finding nobody resolved.
let evidenceComparison = { referenceRunID: "", candidateRunID: "" };

for (const view of evidence.RUN_VIEWS) {
  const option = render.element("option", null, view.label);
  option.value = view.key;
  evidenceViewSelect.append(option);
}

// syncEvidenceViewFields shows the identifier field only for a view that needs
// one, and names what it wants.
//
// Three of the five views read a narrowing; two read the run's whole history a
// page at a time. Leaving an unused identifier box on screen for the latter two
// would suggest the view ignores something it was given.
function syncEvidenceViewFields() {
  const spec = evidence.viewFor(evidenceViewSelect.value);
  if (spec === null) {
    return;
  }
  evidenceViewBlurb.textContent = spec.blurb;
  const needed = spec.param !== "";
  evidenceIdentifierField.hidden = !needed;
  if (needed) {
    evidenceIdentifierLabel.textContent = spec.identifierLabel;
  }
}

evidenceViewSelect.addEventListener("change", () => {
  syncEvidenceViewFields();
});

// evidenceRunFor picks which run a resolution's rows belong to.
//
// The side is the server's answer, and it is what decides the run: reading a
// candidate-side observation's session out of the reference run would be the
// cross-side mistake task 085 exists to prevent, arriving one layer later.
function evidenceRunFor(response) {
  const finding = response.finding === undefined || response.finding === null
    ? {}
    : response.finding;
  if (response.side === "reference") {
    return typeof finding.reference_run_id === "string" ? finding.reference_run_id : "";
  }
  if (response.side === "candidate") {
    return typeof finding.candidate_run_id === "string" ? finding.candidate_run_id : "";
  }
  return "";
}

// openRunView fills the run-history form and loads it, with nothing typed.
async function openRunView(runID, viewKey, identifier) {
  byID("evidence-run-id").value = runID;
  evidenceViewSelect.value = viewKey;
  byID("evidence-identifier").value = identifier;
  syncEvidenceViewFields();
  openEvidence("evidence-run");
  await evidenceSurface.openRunView(runID, viewKey, identifier);
}

// observationNavigation offers the three correlated views an observation can be
// read in, and offers each only when the observation recorded the identifier it
// needs.
//
// A control for a session the observation does not carry would resolve to an
// empty page and read as "this session is empty" rather than "this action
// belonged to no session".
function observationNavigation(runID, observation) {
  const box = render.element("span", "row-actions");
  if (runID === "") {
    return box;
  }
  const targets = [
    { label: "Session", view: "session", value: observation.session_id },
    { label: "Trace", view: "trace", value: observation.trace_id },
    { label: "Behavior", view: "behavior", value: observation.fingerprint_id },
  ];
  for (const target of targets) {
    if (typeof target.value !== "string" || target.value === "") {
      continue;
    }
    const button = render.element("button", "link-button", target.label);
    button.type = "button";
    button.addEventListener("click", () => {
      void openRunView(runID, target.view, target.value);
    });
    box.append(button);
  }
  if (box.childElementCount === 0) {
    box.append(render.element("span", "muted", "no correlation recorded"));
  }
  return box;
}

// behaviorObservationControls offers the observation resolution for one resolved
// behavioral identity.
//
// **A shared behavior gets one control per side and no default.** Both runs hold
// their own observations of it and those two sets are what a developer is
// comparing; choosing one silently would answer a question nobody asked with an
// answer indistinguishable from the one they wanted. For an added or removed
// behavior presence decides the side, so no side is sent and the server derives
// it — which keeps one rule in one place.
function behaviorObservationControls(finding, delta) {
  const box = render.element("span", "row-actions");
  const presence = delta.presence;
  const open = (side, label) => {
    const button = render.element("button", "link-button", label);
    button.type = "button";
    button.addEventListener("click", () => {
      void evidenceSurface.openFinding({
        referenceRunID: finding.reference_run_id,
        candidateRunID: finding.candidate_run_id,
        behavior: delta.fingerprint_id,
        side,
      }, "observations");
    });
    return button;
  };

  if (evidence.needsExplicitSide(presence)) {
    box.append(open("reference", "Reference observations"));
    box.append(open("candidate", "Candidate observations"));
    return box;
  }
  box.append(open("", "Observations"));
  return box;
}

// findingHeader renders everything a resolution says about the finding itself.
//
// Drawn identically on every page of the same resolution, because all of it
// describes the finding rather than the page — including the status, which stays
// `resolved` on a continuation that came back empty.
function findingHeader(host, response) {
  const finding = response.finding === undefined || response.finding === null
    ? {}
    : response.finding;

  const context = render.element("p", "note-inline");
  context.append(render.element("span", null,
    `Reference ${render.displayValue(finding.reference_run_id)} → candidate ${render.displayValue(finding.candidate_run_id)}`));
  host.append(context);

  const asking = render.element("p", "note-inline");
  if (typeof finding.check === "string" && finding.check !== "") {
    asking.append(render.element("span", null, `Gate check: ${finding.check}`));
  }
  if (typeof finding.behavior === "string" && finding.behavior !== "") {
    asking.append(render.element("span", null, `Behavior: ${finding.behavior}`));
  }
  asking.append(evidence.sideBadge(response.side));
  host.append(asking);

  host.append(evidence.statusBanner(response.status));

  // What this result actually established. An aggregate-only check read no
  // history, so its history and exhaustiveness fields are the zero value and
  // describe nothing — rendering them claimed a run's retained history was
  // missing and sampled when it was complete.
  const shape = evidence.findingPresentation(response);

  if (shape.showHistory) {
    const history = response.history === undefined || response.history === null
      ? {}
      : response.history;
    host.append(evidence.historyBox(history.state, history.retained_count, history.complete));
  }

  host.append(render.element("p", "note",
    `Recorded count: ${render.displayValue(response.recorded_count)}. ${shape.countNote}`));
  if (shape.showExhaustiveCaveat) {
    host.append(render.element("p", "note", evidence.EXHAUSTIVE_CAVEAT));
  }
  // Both notes are about how retained observations are rendered. An
  // aggregate-only result renders none, so neither applies to it.
  if (shape.describesRetainedHistory) {
    host.append(render.element("p", "note", evidence.FIDELITY_NOTE));
    host.append(render.element("p", "note", evidence.SEQUENCE_NOTE));
  }
  return shape;
}

// The Evidence surface's renderer.
//
// Every method here draws a value the control plane returned. None of them
// decides a status, a side, a count or a verdict, and none of them accumulates:
// a page replaces what was on screen.
const evidenceView = {
  findingLoading(finding, route) {
    render.clear(evidenceFindingResult);
    evidenceFindingResult.append(render.element("p", "empty",
      route === "observations"
        ? "Resolving the observations behind this finding…"
        : "Resolving the behaviors behind this finding…"));
  },

  findingPage({ response, route, pageNumber }) {
    render.clear(evidenceFindingResult);
    const shape = findingHeader(evidenceFindingResult, response);

    const finding = response.finding === undefined || response.finding === null
      ? {}
      : response.finding;

    // An aggregate-only check has nothing to page through and nothing to link
    // to. The status banner above is the whole answer, and an empty table with a
    // page footer under it would read as "we looked and found none".
    if (!shape.showRows) {
      clearProblem();
      return;
    }

    if (route === "observations") {
      const rows = Array.isArray(response.observations) ? response.observations : [];
      const runID = evidenceRunFor(response);
      if (rows.length === 0) {
        evidenceFindingResult.append(render.emptyState(
          "No rows on this page. What that means is the status above, not this line."));
      } else {
        evidence.renderObservationTable(
          evidenceFindingResult, rows, evidence.OBSERVATION_COLUMNS.resolution,
          (observation) => observationNavigation(runID, observation),
        );
      }
    } else {
      const rows = Array.isArray(response.behaviors) ? response.behaviors : [];
      if (rows.length === 0) {
        evidenceFindingResult.append(render.emptyState(
          "No behavioral identities on this page. What that means is the status above."));
      } else {
        const body = rows.map((delta) => {
          const values = evidence.behaviorRow(delta);
          return [
            values.fingerprint,
            values.behavior,
            values.presence,
            values.referenceCount,
            values.candidateCount,
            behaviorObservationControls(finding, delta),
          ];
        });
        evidenceFindingResult.append(render.table(
          ["Fingerprint", "Behavior, as recorded", "Presence", "Reference", "Candidate", "Evidence"],
          body, `${body.length} row(s) on this page`,
        ));
      }
    }

    evidenceFindingResult.append(evidence.pageFooter(
      pageNumber, response.next_after,
      (after) => { void evidenceSurface.nextFindingPage(after); },
    ));
    clearProblem();
  },

  findingError(error) {
    report(evidenceFindingResult, error);
  },

  runNeedsRun() {
    render.clear(evidenceRunResult);
    evidenceRunResult.append(render.emptyState(
      "This view needs an evaluation run. Open an observation from a finding, or type the run identifier."));
  },

  runNeedsIdentifier(spec) {
    render.clear(evidenceRunResult);
    evidenceRunResult.append(render.emptyState(
      `This view needs a ${spec.identifierLabel}. Open one from a row, or type the value the observation recorded.`));
  },

  runLoading(spec) {
    render.clear(evidenceRunResult);
    evidenceRunResult.append(render.element("p", "empty", `Reading ${spec.label}…`));
  },

  runObservationPage({ spec, runID, response, pageNumber }) {
    render.clear(evidenceRunResult);
    evidenceRunResult.append(render.element("p", "note-inline",
      `${spec.label} · run ${render.displayValue(runID)}`));
    evidenceRunResult.append(render.element("p", "note", spec.blurb));
    evidenceRunResult.append(evidence.historyBox(
      response.history_state, response.retained_count, response.complete));
    evidenceRunResult.append(render.element("p", "note", evidence.FIDELITY_NOTE));
    if (spec.key === "sequence") {
      evidenceRunResult.append(render.element("p", "note", evidence.SEQUENCE_NOTE));
    }

    const rows = Array.isArray(response.observations) ? response.observations : [];
    if (rows.length === 0) {
      evidenceRunResult.append(render.emptyState(
        "No rows on this page. The retained-history state above is what says whether that means anything."));
    } else if (spec.key === "trace") {
      evidence.renderTraceTree(evidenceRunResult, rows);
    } else {
      evidence.renderObservationTable(
        evidenceRunResult, rows, evidence.OBSERVATION_COLUMNS[spec.key],
        (observation) => observationNavigation(runID, observation),
      );
    }

    evidenceRunResult.append(evidence.pageFooter(
      pageNumber, response.next_after,
      (after) => { void evidenceSurface.nextRunPage(after); },
    ));
    clearProblem();
  },

  runError(error) {
    report(evidenceRunResult, error);
  },

  provenanceLoading() {
    render.clear(evidenceProvenanceResult);
    evidenceProvenanceResult.append(render.element("p", "empty", "Reading supplied provenance…"));
  },

  provenancePair(pair) {
    evidence.renderProvenancePanel(evidenceProvenanceResult, pair);
    clearProblem();
  },

  provenanceError(error) {
    report(evidenceProvenanceResult, error);
  },
};

// The surface, wired to the /v1 client and to the renderer above.
//
// Injected rather than reached for, which is what lets the controller's real
// properties — that changing the question resets the cursor, and that a response
// arriving after it changed is discarded — be tested without a browser.
const evidenceSurface = new evidence.EvidenceSurface({
  resolveFindingBehaviors: (finding, after) => api.resolveFindingBehaviors(finding, after),
  resolveFindingObservations: (finding, after) => api.resolveFindingObservations(finding, after),
  runObservations: (runID, scope, after) => api.runObservations(runID, scope, after),
  getRun: (runID) => api.getRun(runID),
  getCandidate: (candidateID) => api.getCandidate(candidateID),
}, evidenceView);

syncEvidenceViewFields();

byID("form-evidence-run").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!requireField("evidence-run-id", "Choose a run, or paste its identifier.")) {
    return;
  }
  await busy(event.submitter, async () => {
    await evidenceSurface.openRunView(
      value("evidence-run-id"), evidenceViewSelect.value, value("evidence-identifier"),
    );
  });
});

byID("form-evidence-provenance").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!requireField("evidence-provenance-reference", "Choose a reference run.")
    || !requireField("evidence-provenance-candidate", "Choose a candidate run.")) {
    return;
  }
  await busy(event.submitter, async () => {
    await evidenceSurface.loadProvenance(
      value("evidence-provenance-reference"), value("evidence-provenance-candidate"),
    );
  });
});

// comparisonEvidenceControls is what makes a gate FAIL one click from its
// evidence.
//
// The route a check resolves through is navigation metadata declared in
// render.js; the answer is entirely the control plane's, including whether a
// check has per-observation evidence at all.
function comparisonEvidenceControls() {
  return {
    onResolveCheck(checkKey) {
      const button = render.element("button", "link-button", "Evidence");
      button.type = "button";
      button.addEventListener("click", () => {
        openEvidence("evidence-finding");
        void evidenceSurface.openFinding({
          referenceRunID: evidenceComparison.referenceRunID,
          candidateRunID: evidenceComparison.candidateRunID,
          check: checkKey,
        }, render.evidenceRouteFor(checkKey));
      });
      return button;
    },
    deltaControls(delta) {
      const box = render.element("span", "row-actions");
      const open = (side, label) => {
        const button = render.element("button", "link-button", label);
        button.type = "button";
        button.addEventListener("click", () => {
          openEvidence("evidence-finding");
          void evidenceSurface.openFinding({
            referenceRunID: evidenceComparison.referenceRunID,
            candidateRunID: evidenceComparison.candidateRunID,
            behavior: delta.fingerprint_id,
            side,
          }, "observations");
        });
        return button;
      };
      if (evidence.needsExplicitSide(delta.change)) {
        box.append(open("reference", "Reference observations"));
        box.append(open("candidate", "Candidate observations"));
        return box;
      }
      box.append(open("", "Observations"));
      return box;
    },
  };
}

// ---------------------------------------------------------------------
// Promotion
// ---------------------------------------------------------------------

const promotionResult = byID("promotion-result");
const promotionHistory = byID("promotion-history");
const promotionTarget = byID("promotion-target");


const PROMOTION_LIMIT_INPUTS = Object.freeze([
  Object.freeze({ id: "promotion-max-added", key: "maxAddedBehaviors", label: "Max added behaviors" }),
  Object.freeze({ id: "promotion-max-block", key: "maxBlockDecisions", label: "Max block decisions" }),
  Object.freeze({ id: "promotion-max-critical", key: "maxCriticalRiskObservations", label: "Max critical risk observations" }),
  Object.freeze({ id: "promotion-max-changes", key: "maxAddedBehaviorChanges", label: "Max added behavior changes", optional: true }),
]);

// Loading the target list is a convenience, not a filter.
//
// Every environment the project has is offered. Deciding which of them a
// candidate may advance toward means comparing ranks, that comparison has one
// implementation and it is in the platform, and a browser that pre-filtered
// the list would be a second one.
byID("promotion-load-environments").addEventListener("click", async (event) => {
  await busy(event.currentTarget, async () => {
    const projectID = value("promotion-project");
    if (projectID === "") {
      showProblem("Enter a project ID to load its environments.");
      byID("promotion-project").focus();
      return;
    }
    try {
      // Every page, not the first. Task 065's cap governs creation rather than
      // existence, so a migrated project may hold more environments than it
      // could now create and a valid target may sit past page one. The
      // traversal, its cursor-progress checks and its defensive page ceiling
      // all live in api.js beside the other client bounds.
      const collection = await api.listAllEnvironments(projectID);
      render.renderEnvironmentOptions(promotionTarget, collection);
      clearProblem();
    } catch (error) {
      // A traversal the server would not terminate is reported, never
      // silently truncated: a short list would read as "that environment does
      // not exist" rather than as the failure it is.
      report(promotionResult, error);
    }
  });
});

byID("form-promotion-create").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!requireField("promotion-reference", "Choose a reference run.")
    || !requireField("promotion-candidate", "Choose a candidate run.")) {
    return;
  }
  await busy(event.submitter, async () => {
    const limits = {};
    for (const input of PROMOTION_LIMIT_INPUTS) {
      const text = value(input.id);
      if (input.optional === true && text === "") {
        continue;
      }
      // Canonical decimal text, never parsed. 0 is valid and the uint64
      // maximum must reach the server intact — Number() would round it.
      if (!api.isCanonicalUint64(text)) {
        showProblem(`${input.label} must be a whole number from 0 to 18446744073709551615.`);
        byID(input.id).focus();
        return;
      }
      limits[input.key] = text;
    }

    const target = value("promotion-target");
    if (target === "") {
      showProblem("Choose a target environment.");
      promotionTarget.focus();
      return;
    }

    try {
      const response = await api.createPromotion(
        value("promotion-id"), value("promotion-reference"),
        value("promotion-candidate"), target, limits,
      );
      // A rejected decision is a successfully recorded decision. It is
      // rendered as the outcome it is, not as an error.
      render.renderPromotion(promotionResult, response);
      clearProblem();
    } catch (error) {
      // A refused request is rendered as the refusal it is. A structural
      // refusal is not a rejected promotion, and presenting it as one would
      // invent a decision the platform never made.
      report(promotionResult, error);
    }
  });
});

// openPromotion reads one decision into the panel under the history. Its own
// surface, so opening a second row while the first is in flight shows the
// second, never whichever answered last.
const promotionDetail = byID("promotion-detail");
const promotionDetailSurface = createSurface("promotion.detail");

async function openPromotion(promotionID, submitter) {
  const ticket = promotionDetailSurface.begin(promotionID);
  promotionDetail.hidden = false;
  render.clear(promotionDetail);
  promotionDetail.append(render.element("p", "empty", `Reading promotion ${promotionID}…`));
  await busy(submitter, async () => {
    try {
      const response = await api.getPromotion(promotionID);
      if (!promotionDetailSurface.owns(ticket)) {
        return;
      }
      render.renderPromotion(promotionDetail, response);
      clearProblem();
    } catch (error) {
      if (!promotionDetailSurface.owns(ticket)) {
        return;
      }
      report(promotionDetail, error);
    }
  });
}

byID("form-promotion-get").addEventListener("submit", async (event) => {
  event.preventDefault();
  await openPromotion(value("promotion-open-id"), event.submitter);
});

// Promotion history is navigated a page at a time.
//
// Three values, all in memory and none persisted: which project is being read,
// the cursor the last page published, and which page number is on screen. A
// reload forgets them, which is the same answer every other part of this page
// gives and is why nothing is written to storage.
//
// Deliberately not the CLI's shape. `trustvian promotion list` traverses to
// completion because a shell pipeline wants one document; a browser doing that
// would hold a project's entire audit trail in an array that only grows.
const promotionNav = byID("promotion-history-nav");
const promotionPageState = byID("promotion-page-state");
const promotionNextButton = byID("promotion-next-page");

// The project, the cursor and the page number live in projectScope, so a
// change of project clears all three and abandons whatever is in flight.

function resetPromotionHistory() {
  projectScope.resetPromotions();
  render.clear(promotionPageState);
  syncPromotionNav();
}

// loadPromotionPage reads exactly one page and replaces what is on screen.
//
// Replacement rather than append: the memory cost of history stays one page
// however far anyone reads, and there is no array quietly growing behind the
// view.
async function loadPromotionPage(projectID, after, pageNumber, submitter) {
  const ticket = projectScope.beginPromotions();
  await busy(submitter, async () => {
    try {
      const response = await api.listPromotions(projectID, after);
      // History for a project the reader has left is not this project's
      // history. Checked before anything is drawn, because the table below
      // is the one belonging to whichever project is scoped now.
      if (!projectScope.ownsPromotions(ticket)) {
        return;
      }

      // The page that arrived is rendered either way. It is valid history the
      // server returned, and withholding it because the *continuation* is
      // malformed would hide evidence over a navigation fault.
      render.renderPromotionList(promotionHistory, response, {
        // The row opens the run through the page's existing path, so
        // GET /v1/evaluation-runs/{id} stays authoritative and the promotion
        // record never becomes a second source of truth for it.
        //
        // null, not an omitted argument: busy() distinguishes "no button to
        // disable" by identity, and an undefined submitter would throw.
        onOpenRun: (runID) => { void loadRun(runID, null); },
        onOpenPromotion: (promotionID) => { void openPromotion(promotionID, null); },
      });

      // A continuation that cannot advance is a server fault, and following it
      // would turn "Load next page" into an infinite loop. The cursor is
      // dropped rather than followed — which also leaves Start over reachable,
      // because the cursor is broken and the project is not.
      const fault = api.promotionCursorFault(after, response);
      if (fault !== "") {
        projectScope.commitPromotions(ticket, "", pageNumber);
        render.renderPromotionPageState(promotionPageState, pageNumber, false);
        showProblem(fault);
        return;
      }

      const cursor = typeof response.next_after === "string" ? response.next_after : "";
      projectScope.commitPromotions(ticket, cursor, pageNumber);
      render.renderPromotionPageState(promotionPageState, pageNumber, cursor !== "");
      clearProblem();
    } catch (error) {
      if (!projectScope.ownsPromotions(ticket)) {
        return;
      }
      // The cursor is not trustworthy after a failed read, but the project
      // still is: leaving Start over available is the difference between a
      // transient blip and having to retype the project.
      projectScope.commitPromotions(ticket, "", pageNumber);
      report(promotionHistory, error);
    }
  });
  if (!projectScope.ownsPromotions(ticket)) {
    return;
  }

  // Applied after busy() has restored the clicked button, not inside it.
  //
  // busy() disables its submitter and restores the value it found on the way
  // out. When the submitter *is* the next-page button, anything this function
  // set inside that window is overwritten by the restore — so reaching the
  // last page would leave "Load next page" enabled with nothing to load. The
  // nav state is therefore the last thing that happens.
  syncPromotionNav();
}

// syncPromotionNav makes the controls describe the state that actually exists.
function syncPromotionNav() {
  const loaded = projectScope.promotionPageNumber > 0;
  promotionNav.hidden = !loaded;
  promotionNextButton.disabled = projectScope.promotionCursor === "";
}

byID("form-promotion-list").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!requireField("promotion-list-project", "Choose a project.")) {
    return;
  }
  resetPromotionHistory();
  await loadPromotionPage(value("promotion-list-project"), "", 1, event.submitter);
});

promotionNextButton.addEventListener("click", async (event) => {
  if (projectScope.project === "" || projectScope.promotionCursor === "") {
    return;
  }
  await loadPromotionPage(
    projectScope.project,
    projectScope.promotionCursor,
    projectScope.promotionPageNumber + 1,
    event.currentTarget);
});

// Start over re-reads the first page rather than remembering earlier ones.
// Reverse pagination would need an API semantic that does not exist, and
// keeping every visited page to walk backwards is the accumulation this shape
// exists to avoid.
byID("promotion-restart").addEventListener("click", async (event) => {
  if (projectScope.project === "") {
    return;
  }
  await loadPromotionPage(projectScope.project, "", 1, event.currentTarget);
});


// ---------------------------------------------------------------------
// Selection context and selectors (task 098)
// ---------------------------------------------------------------------
//
// Every field that used to ask for a project, agent, candidate, run or
// environment identifier now offers a searchable selector over the context's
// pages, and keeps its identifier field as the paste path. The field is still
// what a form sends, so no request changed shape.

// The shared selection context (task 098): which project, agent, candidate
// and run the console is about, and one page of options for each. Every
// selector reads it; choosing in one destination is the preselection in all
// of them. See views/context.js for the rule that a selection belongs to its
// parent.
const selection = createSelectionContext({
  listProjects: (after) => api.listProjects(after),
  listProjectAgents: (projectID, after) => api.listProjectAgents(projectID, after),
  listAgentCandidates: (agentID, after) => api.listAgentCandidates(agentID, after),
  listCandidateRuns: (candidateID, after) => api.listCandidateRuns(candidateID, after),
  listAllEnvironments: (projectID) => api.listAllEnvironments(projectID),
  getRun: (id) => api.getRun(id),
  getCandidate: (id) => api.getCandidate(id),
  getAgent: (id) => api.getAgent(id),
  getProject: (id) => api.getProject(id),
});

// The four selectors that show a comparison's two runs elsewhere. Assigned
// once the bindings below exist; renderCompareSides runs before that.
let sideSelectors = null;

// syncFromContext keeps the console's older per-destination state in step
// with the context, whichever destination made the choice.
//
// The Runs destination keeps its own bounded browser (ADR 0050); when the
// agent or candidate moves somewhere else, the rows it holds were listed
// under the previous one, so they are dropped rather than shown under the
// new name — and re-read the next time Runs is open.
let runsNeedSync = false;

function syncFromContext() {
  const chosen = selection.selection;
  const projectID = chosen.project === null ? "" : chosen.project.id;
  if (projectID !== openedProject) {
    setProjectScope(projectID, chosen.project === null ? "" : chosen.project.label);
    openedAgent = "";
    openedAgentName = "";
    openedCandidate = "";
    hierarchy.truncate("agents");
    runsNeedSync = true;
  }
  const agentID = chosen.agent === null ? "" : chosen.agent.id;
  if (agentID !== openedAgent) {
    openedAgent = agentID;
    openedAgentName = chosen.agent === null ? "" : chosen.agent.label;
    openedCandidate = "";
    hierarchy.truncate("candidates");
    runsNeedSync = true;
    if (agentID !== "") {
      labels.remember("agent", agentID, openedAgentName);
    }
  } else if (chosen.agent !== null && chosen.agent.label !== openedAgentName) {
    openedAgentName = chosen.agent.label;
  }
  const candidateID = chosen.candidate === null ? "" : chosen.candidate.id;
  if (candidateID !== openedCandidate) {
    openedCandidate = candidateID;
    hierarchy.truncate("runs");
    runsNeedSync = true;
  }
  if (runsNeedSync && currentView === "view-runs") {
    void syncRunsHierarchy();
  } else if (runsNeedSync) {
    renderProjectsLevel();
  }
  drawCrumbs();
}

// syncRunsHierarchy reads, one level at a time, what the Runs destination
// needs to show the context's selection. Sequential, because the levels
// share one ownership surface and each read supersedes the one before.
async function syncRunsHierarchy() {
  runsNeedSync = false;
  if (openedProject !== "" && hierarchy.parents.agents !== openedProject) {
    await openLevel("agents", () => hierarchy.loadAgents(openedProject, ""));
  }
  if (openedAgent !== "" && hierarchy.parents.candidates !== openedAgent) {
    await openLevel("candidates", () => hierarchy.loadCandidates(openedAgent, ""));
  }
  if (openedCandidate !== "" && hierarchy.parents.runs !== openedCandidate) {
    runsScopeOpen = false;
    await openLevel("runs", () => hierarchy.loadRuns(openedCandidate, ""));
  }
  renderProjectsLevel();
}

selection.subscribe(syncFromContext);

const reportPaste = (error) => {
  showProblem(error.operational
    ? "The control plane could not answer."
    : error.message);
};

// requireField refuses an empty identifier with a sentence rather than a
// request, now that the field is usually filled by a selector and may sit in
// a closed disclosure where a browser's own "required" bubble cannot point.
function requireField(id, message) {
  if (value(id) !== "") {
    return true;
  }
  showProblem(message);
  return false;
}

// Live: watch one run.
bindSelector(byID("watch-run-select"), selection, {
  level: "run",
  label: "Evaluation run",
  hint: "Runs of the chosen candidate. Choose a project, agent and candidate in any destination.",
  input: byID("watch-run-id"),
  paste: false,
});

// Evidence: run history and provenance.
bindSelector(byID("evidence-run-select"), selection, {
  level: "run",
  label: "Evaluation run",
  input: byID("evidence-run-id"),
});

sideSelectors = {
  provenanceReference: bindSelector(byID("evidence-provenance-reference-select"), selection, {
    level: "run", label: "Reference run", local: true,
    onChosen: (option) => { byID("evidence-provenance-reference").value = option === null ? "" : option.id; },
  }),
  provenanceCandidate: bindSelector(byID("evidence-provenance-candidate-select"), selection, {
    level: "run", label: "Candidate run", local: true,
    onChosen: (option) => { byID("evidence-provenance-candidate").value = option === null ? "" : option.id; },
  }),
  promotionReference: bindSelector(byID("promotion-reference-pick"), selection, {
    level: "run", label: "Reference run", local: true,
    onChosen: (option) => { byID("promotion-reference").value = option === null ? "" : option.id; },
  }),
  promotionCandidate: bindSelector(byID("promotion-candidate-pick"), selection, {
    level: "run", label: "Candidate run", local: true,
    onChosen: (option) => { byID("promotion-candidate").value = option === null ? "" : option.id; },
  }),
};
renderCompareSides();

// Promotions: the project whose history is listed. Choosing one lists it.
bindSelector(byID("promotion-project-select"), selection, {
  level: "project",
  label: "Project",
  input: byID("promotion-list-project"),
  paste: true,
  onError: reportPaste,
  onChosen: (option) => {
    if (option !== null && currentView === "view-promotion") {
      resetPromotionHistory();
      void loadPromotionPage(option.id, "", 1, null);
    }
  },
});

// Manage: the parent each creation form needs, and the run lifecycle acts on.
bindSelector(byID("agent-project-select"), selection, {
  level: "project",
  label: "Project",
  hint: "The project the new agent belongs to.",
  input: byID("agent-project-id"),
});
bindSelector(byID("candidate-agent-select"), selection, {
  level: "agent",
  label: "Agent",
  hint: "The agent this candidate is a version of.",
  input: byID("candidate-agent-id"),
});
bindSelector(byID("run-candidate-select"), selection, {
  level: "candidate",
  label: "Candidate",
  hint: "The candidate version this run executes.",
  input: byID("run-candidate-id"),
});
bindSelector(byID("run-environment-select"), selection, {
  level: "environment",
  label: "Environment",
  hint: "An environment the project owns. Its reference is what the run records.",
  onChosen: (option) => { byID("run-environment").value = option === null ? "" : option.id; },
});
bindSelector(byID("lifecycle-run-select"), selection, {
  level: "run",
  label: "Run",
  hint: "The run the lifecycle controls below act on.",
  onChosen: (option) => {
    if (option !== null) {
      void loadRun(option.id, null);
    }
  },
});


// Overview (task 099).
bindSelector(byID("overview-project-select"), selection, {
  level: "project", label: "Project", paste: true, onError: reportPaste,
});
bindSelector(byID("overview-agent-select"), selection, {
  level: "agent", label: "Agent", paste: true, onError: reportPaste,
});
bindSelector(byID("overview-candidate-select"), selection, {
  level: "candidate", label: "Candidate", paste: true, onError: reportPaste,
});

overview = createOverview({
  selection,
  hosts: {
    scope: byID("overview-scope"),
    live: byID("overview-live"),
    runs: byID("overview-runs"),
    evidence: byID("overview-evidence"),
    verdicts: byID("overview-verdicts"),
    environments: byID("overview-environments"),
  },
  getProgress: (runID) => api.getProgress(runID),
  runBehaviors: (runID, after) => api.runBehaviors(runID, after),
  listPromotions: (projectID, after) => api.listPromotions(projectID, after),
  liveCards: () => liveModel.cards.ordered(),
  labelForScope,
  compareSides: () => projectScope.sides,
  nav: {
    openRun: (runID) => { void openRunDetail(runID); },
    openLiveScope: (key) => {
      liveModel.select(key);
      inspectedEdgeID = "";
      void refreshAuthoritative();
      openView("view-live");
      drawAll();
    },
    useInCompare: (side, run) => { assignSide(side, run); },
    openCompare: () => { openView("view-compare"); onEnterView("view-compare"); },
    openTraces: (runID) => { void openRunView(runID, "trace", ""); },
    watchRun: (runID) => {
      byID("watch-run-id").value = runID;
      openView("view-live");
      byID("watch-start").focus();
    },
    openPromotion: (promotionID) => {
      openView("view-promotion");
      onEnterView("view-promotion");
      showPromotionSection("promotion-history-section");
      void openPromotion(promotionID, null);
    },
  },
});
byID("overview-refresh").addEventListener("click", () => overview.refresh());

// The promotion target is still a native select: every environment the
// project has, from the context's whole collection, with nothing filtered.
selection.subscribe((what) => {
  if (what !== "environments" && what !== "project") {
    return;
  }
  const page = selection.pages.environments;
  if (selection.selection.project === null) {
    render.clear(promotionTarget);
    const placeholder = render.element("option", null, "Choose a project to list its environments");
    placeholder.value = "";
    promotionTarget.append(placeholder);
    return;
  }
  if (page.loaded) {
    render.renderEnvironmentOptions(promotionTarget, { environments: page.rows });
  }
});

// ---------------------------------------------------------------------
// Startup
// ---------------------------------------------------------------------

// The Live Observatory opens itself, with no identifier and no user action.
//
// One unfiltered subscription and, once it hands over, exactly one page of
// GET /v1/projects. That is the entire startup budget: no child level is
// fetched, no continuation is followed, and there is no timer anywhere in this
// bundle that would fetch anything later.
drawAll();
renderProjectsLevel();
liveSession.watch("");

// A run named in the fragment is prefilled, not auto-watched: opening a stream
// because of a URL would start network activity nobody asked for.
const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
const initialRun = fragment.get("run");
if (initialRun !== null && initialRun !== "") {
  byID("watch-run-id").value = initialRun;
  byID("open-run-id").value = initialRun;
}
