// Application wiring: forms, tabs, lifecycle actions and the live view.
//
// This file owns interaction. It holds no platform rules — whether a lifecycle
// transition is legal, whether a gate passes, whether evidence is sufficient —
// because all three belong to the control plane and a second copy in a browser
// would be a rule that can disagree with the one that matters.
//
// It also stores nothing. There is no localStorage, sessionStorage, IndexedDB
// or cookie: a reload legitimately forgets which IDs were open, and the
// database stays the only source of truth. The watched run in the URL fragment
// is navigation state, never read back as fact.

import * as api from "./api.js";
import * as render from "./render.js";
import { RealtimeSession, STATE } from "./realtime.js";
import { LiveModel } from "./live.js";
import { GraphCanvas, PULSE_MS } from "./graph.js";
import { renderRail, renderCanvasNotices } from "./rail.js";
import { TimelineFeed, renderTimeline } from "./timeline.js";
import { renderInspector } from "./inspector.js";
import { LabelCache, HierarchyBrowser, renderLevel, renderOptions } from "./discovery.js";

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

function setupTabs() {
  const tabs = Array.from(document.querySelectorAll(".tab"));
  const select = (tab) => {
    for (const other of tabs) {
      const panel = byID(other.dataset.panel);
      const active = other === tab;
      other.setAttribute("aria-selected", active ? "true" : "false");
      panel.hidden = !active;
    }
    tab.focus();
  };
  for (const tab of tabs) {
    tab.addEventListener("click", () => select(tab));
    tab.addEventListener("keydown", (event) => {
      // Arrow-key movement between tabs, so the section switcher is usable
      // without a pointer.
      const index = tabs.indexOf(tab);
      if (event.key === "ArrowRight") {
        select(tabs[(index + 1) % tabs.length]);
      } else if (event.key === "ArrowLeft") {
        select(tabs[(index - 1 + tabs.length) % tabs.length]);
      }
    });
  }
  return select;
}

const selectTab = setupTabs();

// Manage holds five sub-surfaces behind one tab.
//
// They used to be five top-level tabs, which put "Project", "Agent",
// "Candidate" and "Evaluation" in the primary navigation of a product whose
// job is watching an agent work. They are advanced controls: still here, still
// working, and one level down.
function setupManageSections() {
  const subtabs = Array.from(document.querySelectorAll(".subtab"));
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
  show("manage-open");
  return show;
}

const showManageSection = setupManageSections();

// openManage reveals one Manage subsection, for the create-then-show flows
// that used to jump to a top-level tab.
function openManage(sectionID) {
  selectTab(byID("tab-manage"));
  showManageSection(sectionID);
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
        render.renderRun(runResult, await call(openRunID));
        clearProblem();
        try {
          render.renderProgress(progressResult, await api.getProgress(openRunID));
        } catch (ignored) {
          // The transition succeeded; a failed progress read is reported by
          // the refresh action rather than masking the result above.
        }
      } catch (error) {
        report(runResult, error);
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
const headerAgent = byID("header-agent");
const headerScope = byID("header-scope");
const headerCounts = byID("header-counts");
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

function drawHeader() {
  const card = liveModel.selectedCard();
  if (card === undefined) {
    headerAgent.textContent = "No agent selected";
    headerScope.textContent = "";
    headerCounts.textContent = "Authoritative counts appear when a run is selected.";
    canvasScope.textContent = "No run selected.";
    return;
  }

  const name = labelForScope(card.scope);
  headerAgent.textContent = name || card.scope.agent_id || "(unnamed agent)";

  const context = [card.scope.environment, card.scope.candidate_id ? "candidate" : ""]
    .filter((part) => part !== "");
  headerScope.textContent = context.join(" · ");

  canvasScope.textContent = card.scope.run_id
    ? `Drawing run ${card.scope.run_id}.`
    : "Drawing the selected run.";

  headerCounts.textContent = authoritativeSummary(card);
}

// authoritativeSummary is the header's counter line.
//
// Every number in it comes from GET /v1/evaluation-runs/{id}/progress. The
// live-seen count is labelled as such and kept visually separate, because a
// frame count and a record count are different facts and a header that blurred
// them would be reporting the stream as the database.
function authoritativeSummary(card) {
  if (authoritative.runID !== card.scope.run_id || authoritative.recordCount === "") {
    return `${card.seenLive} seen live · authoritative counts loading…`;
  }
  const parts = [`${authoritative.recordCount} observations`];
  if (authoritative.distinctCount !== "") {
    parts.push(`${authoritative.distinctCount} behaviors`);
  }
  if (authoritative.complete === false) {
    parts.push("evidence incomplete");
  } else if (authoritative.complete === true) {
    parts.push("evidence complete");
  }
  return parts.join(" · ");
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
    },
    onRows: () => {
      // The timeline owns the feed, built from scoped observations. The
      // session's own row window carries no scope and would be a second,
      // poorer copy of the same data.
    },
    onSnapshot: (projects) => {
      renderProjectsLevel(projects);
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
// Bounded hierarchy discovery
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

async function loadRootProjects(after) {
  return hierarchy.loadProjects(after);
}

let openedProject = "";
let openedAgent = "";
let openedCandidate = "";

function renderProjectsLevel() {
  renderLevel(byID("level-projects"), {
    title: "Projects",
    level: hierarchy.levels.projects,
    selected: openedProject,
    pendingMessage: "Not loaded.",
    emptyMessage: "No projects exist yet.",
    openLabel: "Show agents of project",
    labelOf: (row) => row.name || row.id,
    detailOf: (row) => (row.name && row.name !== row.id ? row.id : ""),
    onOpen: (row) => {
      openedProject = row.id;
      // The promotion form needs a project to list target environments, and
      // this is the one a developer just chose. Filling it is a convenience;
      // the field stays editable and is still not sent with the decision.
      byID("promotion-project").value = row.id;
      byID("promotion-list-project").value = row.id;
      void openLevel("agents", () => hierarchy.loadAgents(row.id, ""));
    },
    onMore: (cursor) => { void openLevel("projects", () => hierarchy.loadProjects(cursor)); },
  });
  renderAgentsLevel();
  renderCandidatesLevel();
  renderRunsLevel();
  refreshCompareOptions();
}

function renderAgentsLevel() {
  renderLevel(byID("level-agents"), {
    title: "Agents",
    level: hierarchy.levels.agents,
    selected: openedAgent,
    pendingMessage: "Select a project.",
    emptyMessage: "This project has no agents.",
    openLabel: "Show candidates of agent",
    labelOf: (row) => row.name || row.id,
    detailOf: (row) => (row.name && row.name !== row.id ? row.id : ""),
    onOpen: (row) => {
      openedAgent = row.id;
      labels.remember("agent", row.id, row.name);
      void openLevel("candidates", () => hierarchy.loadCandidates(row.id, ""));
    },
    onMore: (cursor) => {
      void openLevel("agents", () => hierarchy.loadAgents(hierarchy.parents.agents, cursor));
    },
  });
}

function renderCandidatesLevel() {
  renderLevel(byID("level-candidates"), {
    title: "Candidates",
    level: hierarchy.levels.candidates,
    selected: openedCandidate,
    pendingMessage: "Select an agent.",
    emptyMessage: "This agent has no candidates.",
    openLabel: "Show runs of candidate",
    labelOf: (row) => (row.metadata && row.metadata.label ? row.metadata.label : row.id),
    detailOf: (row) => (row.metadata && row.metadata.label ? row.id : ""),
    onOpen: (row) => {
      openedCandidate = row.id;
      void openLevel("runs", () => hierarchy.loadRuns(row.id, ""));
    },
    onMore: (cursor) => {
      void openLevel("candidates",
        () => hierarchy.loadCandidates(hierarchy.parents.candidates, cursor));
    },
  });
}

function renderRunsLevel() {
  renderLevel(byID("level-runs"), {
    title: "Evaluation runs",
    level: hierarchy.levels.runs,
    pendingMessage: "Select a candidate.",
    emptyMessage: "This candidate has no runs.",
    openLabel: "Open evaluation run",
    labelOf: (row) => row.id,
    detailOf: (row) => [row.status, row.environment].filter((p) => p).join(" · "),
    onOpen: (row) => { void openRunDetail(row.id); },
    onMore: (cursor) => {
      void openLevel("runs", () => hierarchy.loadRuns(hierarchy.parents.runs, cursor));
    },
  });
  refreshCompareOptions();
}

// openLevel performs exactly one bounded request per user action.
async function openLevel(level, load) {
  try {
    await load();
    renderProjectsLevel();
    clearProblem();
  } catch (error) {
    report(byID(`level-${level}`), error);
  }
}

// openRunDetail reads one run's authoritative state. One bounded read.
async function openRunDetail(runID) {
  try {
    const run = await api.getRun(runID);
    render.renderRun(byID("investigate-run"), run);
    setOpenRun(runID);
    byID("watch-run-id").value = runID;
    clearProblem();
    try {
      render.renderProgress(byID("investigate-progress"), await api.getProgress(runID));
    } catch (progressError) {
      report(byID("investigate-progress"), progressError);
    }
  } catch (error) {
    report(byID("investigate-run"), error);
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

// Compare's run pickers, filled from whatever the hierarchy browser has
// loaded.
//
// The identifier is still the value the server receives — the contract is
// unchanged — but it stops being something a person has to find and copy. The
// text inputs remain, because a developer who already has an identifier from
// CI should not have to browse to it, and because a run outside the currently
// loaded page has to be reachable somehow.
const comparePickers = Object.freeze([
  Object.freeze({ select: "compare-reference-pick", input: "compare-reference" }),
  Object.freeze({ select: "compare-candidate-pick", input: "compare-candidate" }),
]);

function refreshCompareOptions() {
  const runs = hierarchy.levels.runs.rows;
  for (const picker of comparePickers.concat(promotionRunPickers())) {
    renderOptions(byID(picker.select), runs, {
      placeholder: "Choose a run",
      emptyLabel: "Browse to a candidate under Investigate",
      labelOf: (row) => row.id,
      detailOf: (row) => [row.status, row.environment].filter((p) => p).join(" · "),
    });
  }
}

for (const picker of comparePickers) {
  byID(picker.select).addEventListener("change", (event) => {
    if (event.target.value !== "") {
      byID(picker.input).value = event.target.value;
    }
  });
}

// Declared as a function because the promotion section is wired further down;
// referencing its constant here would read it before initialization.
function promotionRunPickers() {
  return [
    { select: "promotion-reference-pick", input: "promotion-reference" },
    { select: "promotion-candidate-pick", input: "promotion-candidate" },
  ];
}

const LIMIT_INPUTS = Object.freeze([
  Object.freeze({ id: "compare-max-added", key: "maxAddedBehaviors", label: "Max added behaviors" }),
  Object.freeze({ id: "compare-max-block", key: "maxBlockDecisions", label: "Max block decisions" }),
  Object.freeze({ id: "compare-max-critical", key: "maxCriticalRiskObservations", label: "Max critical risk observations" }),
]);

byID("form-compare").addEventListener("submit", async (event) => {
  event.preventDefault();
  await busy(event.submitter, async () => {
    const limits = {};
    for (const input of LIMIT_INPUTS) {
      const text = value(input.id);
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
        value("compare-reference"), value("compare-candidate"), limits,
      );
      // The verdict inside is the server's. This call renders it; it does not
      // recompute it from the limits above.
      render.renderComparison(compareResult, response);
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
// Promotion
// ---------------------------------------------------------------------

const promotionResult = byID("promotion-result");
const promotionHistory = byID("promotion-history");
const promotionTarget = byID("promotion-target");

// The promotion form's run pickers, filled from the same browsed page Compare
// uses. Task 066's rules are untouched: the source environment is still
// inferred by the server, no rank is compared here, and no verdict is derived.
const promotionPickers = Object.freeze([
  Object.freeze({ select: "promotion-reference-pick", input: "promotion-reference" }),
  Object.freeze({ select: "promotion-candidate-pick", input: "promotion-candidate" }),
]);

for (const picker of promotionPickers) {
  byID(picker.select).addEventListener("change", (event) => {
    if (event.target.value !== "") {
      byID(picker.input).value = event.target.value;
    }
  });
}

const PROMOTION_LIMIT_INPUTS = Object.freeze([
  Object.freeze({ id: "promotion-max-added", key: "maxAddedBehaviors", label: "Max added behaviors" }),
  Object.freeze({ id: "promotion-max-block", key: "maxBlockDecisions", label: "Max block decisions" }),
  Object.freeze({ id: "promotion-max-critical", key: "maxCriticalRiskObservations", label: "Max critical risk observations" }),
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
  await busy(event.submitter, async () => {
    const limits = {};
    for (const input of PROMOTION_LIMIT_INPUTS) {
      const text = value(input.id);
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

byID("form-promotion-get").addEventListener("submit", async (event) => {
  event.preventDefault();
  await busy(event.submitter, async () => {
    try {
      const response = await api.getPromotion(value("promotion-open-id"));
      render.renderPromotion(promotionResult, response);
      clearProblem();
    } catch (error) {
      report(promotionResult, error);
    }
  });
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

let promotionProject = "";
let promotionCursor = "";
let promotionPage = 0;

function resetPromotionHistory() {
  promotionProject = "";
  promotionCursor = "";
  promotionPage = 0;
  render.clear(promotionPageState);
  syncPromotionNav();
}

// loadPromotionPage reads exactly one page and replaces what is on screen.
//
// Replacement rather than append: the memory cost of history stays one page
// however far anyone reads, and there is no array quietly growing behind the
// view.
async function loadPromotionPage(projectID, after, pageNumber, submitter) {
  await busy(submitter, async () => {
    try {
      const response = await api.listPromotions(projectID, after);

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
      });
      promotionProject = projectID;
      promotionPage = pageNumber;

      // A continuation that cannot advance is a server fault, and following it
      // would turn "Load next page" into an infinite loop. The cursor is
      // dropped rather than followed — which also leaves Start over reachable,
      // because the cursor is broken and the project is not.
      const fault = api.promotionCursorFault(after, response);
      if (fault !== "") {
        promotionCursor = "";
        render.renderPromotionPageState(promotionPageState, pageNumber, false);
        showProblem(fault);
        return;
      }

      promotionCursor = typeof response.next_after === "string" ? response.next_after : "";
      render.renderPromotionPageState(promotionPageState, pageNumber, promotionCursor !== "");
      clearProblem();
    } catch (error) {
      // The cursor is not trustworthy after a failed read, but the project
      // still is: leaving Start over available is the difference between a
      // transient blip and having to retype the project.
      promotionCursor = "";
      promotionProject = projectID;
      report(promotionHistory, error);
    }
  });

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
  const loaded = promotionProject !== "";
  promotionNav.hidden = !loaded;
  promotionNextButton.disabled = promotionCursor === "";
}

byID("form-promotion-list").addEventListener("submit", async (event) => {
  event.preventDefault();
  resetPromotionHistory();
  await loadPromotionPage(value("promotion-list-project"), "", 1, event.submitter);
});

promotionNextButton.addEventListener("click", async (event) => {
  if (promotionProject === "" || promotionCursor === "") {
    return;
  }
  await loadPromotionPage(
    promotionProject, promotionCursor, promotionPage + 1, event.currentTarget);
});

// Start over re-reads the first page rather than remembering earlier ones.
// Reverse pagination would need an API semantic that does not exist, and
// keeping every visited page to walk backwards is the accumulation this shape
// exists to avoid.
byID("promotion-restart").addEventListener("click", async (event) => {
  if (promotionProject === "") {
    return;
  }
  await loadPromotionPage(promotionProject, "", 1, event.currentTarget);
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
