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
      selectTab(byID("tab-project"));
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
      selectTab(byID("tab-agent"));
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
      selectTab(byID("tab-candidate"));
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
      selectTab(byID("tab-evaluation"));
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
// Live view
// ---------------------------------------------------------------------

const liveState = byID("live-state");
const liveSnapshot = byID("live-snapshot");
const liveRows = byID("live-rows");
const watchReconnect = byID("watch-reconnect");
const watchStop = byID("watch-stop");

const STATE_TEXT = Object.freeze({
  [STATE.IDLE]: "Not watching.",
  [STATE.CONNECTING]: "Connecting — waiting for the stream to synchronize.",
  [STATE.RESYNCING]: "Synchronized. Reading authoritative state…",
  [STATE.LIVE]: "Live.",
  [STATE.RECONNECTING]: "Disconnected. Reconnecting…",
  [STATE.FAILED]: "Failed. The stream never synchronized; use Reconnect now to try again.",
});

const session = new RealtimeSession(
  {
    getRun: (id) => api.getRun(id),
    getProgress: (id) => api.getProgress(id),
    realtimePath: (id) => api.realtimePath(id),
  },
  {
    onState: (state) => {
      liveState.textContent = STATE_TEXT[state] || state;
      const watching = state !== STATE.IDLE;
      watchStop.disabled = !watching;
      watchReconnect.disabled = !watching;
    },
    onRows: (rows) => render.renderObservationRows(liveRows, rows),
    onSnapshot: (run, progress) => {
      render.renderSnapshot(liveSnapshot, run, progress);
      // The live view and the evaluation panel describe the same run, so an
      // authoritative read refreshes both rather than letting one go stale.
      if (openRunID === session.runID) {
        render.renderRun(runResult, run);
        render.renderProgress(progressResult, progress);
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
// Startup
// ---------------------------------------------------------------------

byID("conn-status").textContent = "Ready · same-origin /v1";

// A run named in the fragment is prefilled, not auto-watched: opening a stream
// because of a URL would start network activity nobody asked for.
const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
const initialRun = fragment.get("run");
if (initialRun !== null && initialRun !== "") {
  byID("watch-run-id").value = initialRun;
  byID("open-run-id").value = initialRun;
}
