// The Traces destination (task 100): a run's traces, one trace's evaluated
// actions as a waterfall, and one action's details beside it.
//
// The interaction follows Grafana Tempo and Jaeger — a searchable list, a
// hierarchical timeline with durations and status, a details panel that opens
// on the right without disturbing what is behind it — adapted to what
// Trustvian retains. That adaptation is stated on the page, not hidden:
//
//   - A row is an **evaluated action** — an observation Trustvian made a
//     decision about — not a span. Spans the engine never evaluated were never
//     retained, so a trace here is the evaluated part of a trace.
//   - Hierarchy is the recorded parent span reference, through buildTraceTree,
//     and nothing else. A parent this page does not hold is "unresolved",
//     never promoted to a root.
//   - Bars are placed by the producer's timestamp and sized by the measured
//     duration. An unmeasured duration is a marker, not a zero-width bar.
//   - No attribute, event, link, service name or content exists to show.
//
// Three surfaces, each with its own ownership ticket: the trace list, the
// open trace's page, and nothing else — the details panel reads no network,
// it shows a row already on screen. Selecting a row never redraws the list or
// the waterfall, so the selected trace, the filter and both scroll positions
// survive opening and closing the panel.

import { element, clear } from "../core/dom.js";
import { createSurface } from "../core/ownership.js";
import { detailGroup, detailRefs, verdict, decisionEdge, decisionClass, riskClass, riskIsSevere } from "../ui/dashboard.js";
import { emptyState, inlineEmpty, skeleton } from "../ui/feedback.js";
import { icon } from "../ui/icons.js";
import { notify } from "../ui/feedback.js";
import * as render from "../v1/render.js";
import { parentStateText, spanStatusLabel, spanStatusClass, newBehaviorLabel } from "./trace.js";
import { buildWaterfall, filterTraces, nextIndex, offsetText } from "./waterfall.js";

const MAX_TRACE_ROWS = 512;

function behaviorText(behavior, key) {
  if (behavior === undefined || behavior === null || !render.BEHAVIOR_FIELDS.includes(key)) {
    return render.displayValue(undefined);
  }
  return render.displayValue(behavior[key]);
}

// field reads one retained observation field through the allowlist, so a
// field /v1 adds later cannot reach this view by being named here by mistake.
function field(observation, key) {
  if (observation === null || observation === undefined || !render.EVIDENCE_OBSERVATION_FIELDS.includes(key)) {
    return undefined;
  }
  return observation[key];
}

function actionName(observation) {
  const operation = behaviorText(observation.behavior, "operation_name");
  const target = behaviorText(observation.behavior, "target_name");
  const absent = render.displayValue(undefined);
  if (operation !== absent && target !== absent) {
    return `${operation} → ${target}`;
  }
  if (operation !== absent) {
    return operation;
  }
  return target !== absent ? target : "operation not recorded";
}

async function copy(text, what) {
  try {
    await navigator.clipboard.writeText(text);
    notify(`Copied ${what}.`, "done");
  } catch (ignored) {
    notify(`Could not copy; the ${what} is ${text}.`, "fail");
  }
}

export function createTraces(deps) {
  const { selection, hosts, nav } = deps;

  const listSurface = createSurface("traces.list");
  const traceSurface = createSurface("traces.trace");

  let visible = false;
  let runID = "";
  let list = emptyList();
  let filter = "";
  let activeTrace = -1;
  let trace = emptyTrace();
  let pendingTraceID = "";
  let selected = -1;
  let rows = [];
  let rowNodes = [];
  let panelOpen = false;

  function emptyList() {
    return { rows: [], nextAfter: "", loaded: false, loading: false, error: null, history: null, capped: false };
  }
  function emptyTrace() {
    return { id: "", page: null, pageNumber: 0, after: "", loading: false, error: null };
  }

  const contextRunID = () => (selection.selection.run === null ? "" : selection.selection.run.id);

  // ---------------------------------------------------------------- reads

  async function loadList(after) {
    if (runID === "") {
      return;
    }
    const ticket = listSurface.begin(runID);
    list = { ...list, loading: true, error: null };
    drawList();
    try {
      const response = await deps.runTraces(runID, after);
      if (!listSurface.owns(ticket)) {
        return;
      }
      const page = Array.isArray(response.traces) ? response.traces : [];
      const merged = (after === "" ? [] : list.rows).concat(page);
      const nextAfter = typeof response.next_after === "string" ? response.next_after : "";
      list = {
        rows: merged,
        nextAfter,
        loaded: true,
        loading: false,
        error: null,
        history: response,
        capped: nextAfter !== "" && merged.length >= MAX_TRACE_ROWS,
      };
    } catch (error) {
      if (!listSurface.owns(ticket)) {
        return;
      }
      list = { ...list, loading: false, error };
    }
    drawList();
    // A trace asked for by name (from an observation or the Overview) opens
    // once the list that contains it is known — or directly, if it is not on
    // this page, because the trace read does not need the list.
    if (pendingTraceID !== "") {
      const wanted = pendingTraceID;
      pendingTraceID = "";
      void openTrace(wanted, "", 1);
    }
  }

  async function openTrace(traceID, after, pageNumber) {
    if (runID === "" || traceID === "") {
      return;
    }
    const ticket = traceSurface.begin(`${runID}\u0000${traceID}`);
    closePanel(false);
    trace = { id: traceID, page: null, pageNumber, after, loading: true, error: null };
    markActiveTrace();
    drawTrace();
    try {
      const page = await deps.runObservations(runID, { traceID }, after);
      if (!traceSurface.owns(ticket)) {
        return;
      }
      trace = { id: traceID, page, pageNumber, after, loading: false, error: null };
    } catch (error) {
      if (!traceSurface.owns(ticket)) {
        return;
      }
      trace = { ...trace, loading: false, error };
    }
    drawTrace();
  }

  // ----------------------------------------------------------- the list

  function drawList() {
    const host = hosts.list;
    clear(host);
    if (runID === "") {
      host.append(inlineEmpty("Choose a run to list its traces."));
      return;
    }
    const search = element("label", "search trace-search");
    search.append(icon("search"));
    search.append(element("span", "search-label", "Filter traces"));
    const input = element("input");
    input.type = "search";
    input.placeholder = "Trace ID";
    input.autocomplete = "off";
    input.value = filter;
    input.addEventListener("input", () => {
      filter = input.value;
      drawListRows();
    });
    search.append(input);
    host.append(search);

    const status = element("p", "trace-list-status");
    status.id = "traces-list-status";
    status.setAttribute("role", "status");
    host.append(status);

    const box = element("div", "trace-list-rows");
    box.id = "traces-list-rows";
    host.append(box);

    const more = element("button", "btn-quiet trace-more", "Load next page");
    more.type = "button";
    more.hidden = list.nextAfter === "" || list.capped;
    more.disabled = list.loading;
    more.addEventListener("click", () => { void loadList(list.nextAfter); });
    host.append(more);
    drawListRows();
  }

  function drawListRows() {
    const box = hosts.list.querySelector("#traces-list-rows");
    const status = hosts.list.querySelector("#traces-list-status");
    if (box === null || status === null) {
      return;
    }
    clear(box);
    if (list.loading && !list.loaded) {
      skeleton(box, 5, 2);
      box.setAttribute("aria-busy", "true");
      status.textContent = "Reading traces…";
      return;
    }
    box.removeAttribute("aria-busy");
    if (list.error) {
      const fail = element("div", "result");
      fail.append(element("p", "err-msg", list.error.message || String(list.error)));
      box.append(fail);
      status.textContent = "";
      return;
    }
    const shown = filterTraces(list.rows, filter);
    const history = list.history || {};
    const scope = list.nextAfter === "" ? `${list.rows.length} trace(s)` : `${list.rows.length} loaded; more exist`;
    const historyNote = history.history_state === "complete"
      ? "from the run's whole retained history"
      : (history.history_state ? `from ${history.history_state} retained history` : "");
    status.textContent = [filter.trim() === "" ? scope : `${shown.length} of ${scope} match`, historyNote]
      .filter((part) => part !== "").join(" · ");

    if (list.rows.length === 0) {
      box.append(emptyState("No traces in this run",
        "No retained observation carries a trace identifier. Actions without one are still under Runs → Observations.",
        "trace"));
      return;
    }
    const listbox = element("div", "trace-options");
    listbox.setAttribute("role", "listbox");
    listbox.setAttribute("aria-label", "Traces");
    shown.forEach((row, index) => {
      const option = element("div", "trace-option");
      option.setAttribute("role", "option");
      option.tabIndex = -1;
      option.dataset.traceId = row.trace_id;
      option.setAttribute("aria-selected", row.trace_id === trace.id ? "true" : "false");
      option.append(element("span", "trace-option-id", row.trace_id));
      const meta = element("span", "trace-option-meta");
      meta.append(element("span", null, `${row.observations} action(s)`));
      meta.append(element("span", null, `from #${row.first_sequence}`));
      if (row.error_spans !== "0" && typeof row.error_spans === "string") {
        meta.append(element("span", "trace-option-errors", `${row.error_spans} error status`));
      }
      option.append(meta);
      option.addEventListener("click", () => { void openTrace(row.trace_id, "", 1); });
      option.addEventListener("keydown", (event) => {
        const options = Array.from(listbox.querySelectorAll(".trace-option"));
        const at = options.indexOf(option);
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          void openTrace(row.trace_id, "", 1);
          return;
        }
        const next = nextIndex(at, options.length, event.key);
        if (next !== at && next >= 0) {
          event.preventDefault();
          options[next].focus();
        }
      });
      listbox.append(option);
      if (index === 0) {
        option.tabIndex = 0;
      }
    });
    if (shown.length === 0) {
      listbox.append(element("p", "empty", "No loaded trace matches. The filter is over loaded traces only."));
    }
    box.append(listbox);
    markActiveTrace();
  }

  function markActiveTrace() {
    for (const option of hosts.list.querySelectorAll(".trace-option")) {
      const isActive = option.dataset.traceId === trace.id;
      option.setAttribute("aria-selected", isActive ? "true" : "false");
      if (isActive) {
        for (const other of hosts.list.querySelectorAll(".trace-option")) {
          other.tabIndex = -1;
        }
        option.tabIndex = 0;
      }
    }
  }

  // -------------------------------------------------------- the waterfall

  function drawTrace() {
    const host = hosts.waterfall;
    // The panel describes a row of the waterfall being replaced.
    closePanel(false);
    clear(host);
    rows = [];
    rowNodes = [];
    selected = -1;
    if (trace.id === "") {
      host.append(emptyState("Choose a trace",
        runID === "" ? "Choose a run first." : "Pick one from the list to see its evaluated actions as a timeline.",
        "trace"));
      return;
    }

    const head = element("div", "waterfall-head");
    const title = element("h2", "waterfall-title");
    title.append(element("span", null, "Trace "));
    title.append(element("code", "waterfall-trace-id", trace.id));
    head.append(title);
    const copyID = element("button", "link-button", "Copy trace ID");
    copyID.type = "button";
    copyID.addEventListener("click", () => { void copy(trace.id, "trace ID"); });
    head.append(copyID);
    host.append(head);

    if (trace.loading) {
      const box = element("div");
      skeleton(box, 6, 3);
      host.append(box);
      return;
    }
    if (trace.error) {
      const fail = element("div", "result");
      fail.append(element("p", "err-msg", trace.error.message || String(trace.error)));
      host.append(fail);
      return;
    }

    const page = trace.page || {};
    const observations = Array.isArray(page.observations) ? page.observations : [];
    const layout = buildWaterfall(observations);
    rows = layout.rows;

    const summary = element("p", "waterfall-summary");
    summary.append(element("span", null, `${rows.length} evaluated action(s) on page ${trace.pageNumber}`));
    summary.append(element("span", null, `window ${offsetText(layout.windowNanos).replace("+", "")}`));
    if (layout.untimed > 0) {
      summary.append(element("span", null, `${layout.untimed} without a readable timestamp`));
    }
    host.append(summary);

    const notes = element("ul", "waterfall-notes");
    notes.append(element("li", null,
      "Each row is an action Trustvian evaluated. Spans it did not evaluate were not retained, so this is not the complete distributed trace."));
    notes.append(element("li", null,
      "Nesting is the recorded parent span reference only. Bars start at the producer's timestamp (span start, for OTLP) and are as long as the measured duration; clocks of different producers can disagree."));
    if (page.next_after || trace.pageNumber > 1) {
      notes.append(element("li", null,
        "This trace continues on another page. A parent on another page shows as unresolved here."));
    }
    host.append(notes);

    if (rows.length === 0) {
      host.append(emptyState("No actions on this page",
        "The retained history no longer holds this trace on this page.", "trace"));
      host.append(pager(page));
      return;
    }

    const scale = element("div", "waterfall-scale");
    scale.setAttribute("aria-hidden", "true");
    scale.append(element("span", "waterfall-scale-name", "Action"));
    const ticks = element("span", "waterfall-scale-ticks");
    ticks.append(element("span", "waterfall-tick", "0"));
    ticks.append(element("span", "waterfall-tick", offsetText(layout.windowNanos)));
    scale.append(ticks);
    host.append(scale);

    const box = element("div", "waterfall-rows");
    box.setAttribute("role", "listbox");
    box.setAttribute("aria-label", `Evaluated actions in trace ${trace.id}`);
    rows.forEach((row, index) => {
      const node = drawRow(row, index);
      rowNodes.push(node);
      box.append(node);
    });
    if (rowNodes.length > 0) {
      rowNodes[0].tabIndex = 0;
    }
    host.append(box);
    host.append(pager(page));
  }

  function drawRow(row, index) {
    const observation = row.observation;
    // Severity is the edge rule and the word; the action's name stays ink, so
    // a red name never reads as a verdict on its own.
    const edge = decisionEdge(field(observation, "decision"));
    const node = element("div", edge === "" ? "waterfall-row" : `waterfall-row edge-${edge}`);
    node.setAttribute("role", "option");
    node.setAttribute("aria-selected", "false");
    node.tabIndex = -1;
    const name = actionName(observation);
    node.setAttribute("aria-label",
      `${name}, decision ${field(observation, "decision") || "not recorded"}, ${row.durationText}, status ${spanStatusLabel(observation)}`);

    const label = element("div", "waterfall-label");
    // Indentation is the recorded depth, already bounded by TRACE_MAX_DEPTH.
    label.style.paddingLeft = `${row.depth * 0.85}rem`;
    label.append(element("span", "waterfall-name", name));
    const tags = element("span", "waterfall-tags");
    tags.append(verdict(render.displayValue(field(observation, "decision")), decisionEdge(field(observation, "decision")),
      decisionClass(field(observation, "decision"))));
    if (riskIsSevere(field(observation, "risk_level"))) {
      tags.append(verdict(render.displayValue(field(observation, "risk_level")), "flag", riskClass(field(observation, "risk_level"))));
    }
    if (field(observation, "span_status") === "error") {
      tags.append(element("span", "waterfall-status status-error", "error"));
    }
    if (row.state !== "root" && row.state !== "child") {
      tags.append(element("span", "waterfall-state", row.state));
    }
    label.append(tags);
    node.append(label);

    const track = element("div", "waterfall-track");
    if (row.offset === null) {
      track.append(element("span", "waterfall-untimed", "no timestamp"));
    } else if (row.measured) {
      const bar = element("span", `waterfall-bar ${spanStatusClass(observation)}`);
      bar.style.left = `${row.left * 100}%`;
      bar.style.width = `max(2px, ${row.width * 100}%)`;
      track.append(bar);
    } else {
      const marker = element("span", "waterfall-marker");
      marker.style.left = `${row.left * 100}%`;
      track.append(marker);
    }
    node.append(track);
    node.append(element("span", "waterfall-duration", row.measured ? row.durationText : "not measured"));

    node.addEventListener("click", () => {
      select(index, true);
      openPanel();
    });
    node.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        select(index, true);
        openPanel();
        return;
      }
      if (event.key === "Escape" && panelOpen) {
        event.preventDefault();
        closePanel(true);
        return;
      }
      const next = nextIndex(index, rowNodes.length, event.key);
      if (next !== index && next >= 0) {
        event.preventDefault();
        select(next, true);
        // The panel follows the selection while it is open, as a reader
        // stepping through a trace expects.
        if (panelOpen) {
          drawPanel();
        }
      }
    });
    return node;
  }

  function pager(page) {
    const bar = element("div", "pager waterfall-pager");
    if (trace.pageNumber > 1) {
      const first = element("button", "btn-quiet", "First page");
      first.type = "button";
      first.addEventListener("click", () => { void openTrace(trace.id, "", 1); });
      bar.append(first);
    }
    if (typeof page.next_after === "string" && page.next_after !== "") {
      const next = element("button", null, "Next page of this trace");
      next.type = "button";
      next.addEventListener("click", () => { void openTrace(trace.id, page.next_after, trace.pageNumber + 1); });
      bar.append(next);
    }
    if (bar.childElementCount === 0) {
      bar.hidden = true;
    }
    return bar;
  }

  // select marks one row without redrawing anything, so the scroll position
  // of the waterfall and the list are the reader's.
  function select(index, focus) {
    if (index < 0 || index >= rowNodes.length) {
      return;
    }
    if (selected >= 0 && rowNodes[selected]) {
      rowNodes[selected].setAttribute("aria-selected", "false");
      rowNodes[selected].classList.remove("is-selected");
    }
    // One tab stop in the waterfall: the selected row (roving tabindex).
    for (const other of rowNodes) {
      other.tabIndex = -1;
    }
    selected = index;
    const node = rowNodes[index];
    node.setAttribute("aria-selected", "true");
    node.classList.add("is-selected");
    node.tabIndex = 0;
    if (focus) {
      node.focus({ preventScroll: false });
      if (typeof node.scrollIntoView === "function") {
        node.scrollIntoView({ block: "nearest" });
      }
    }
  }

  // ------------------------------------------------------- the details panel

  function openPanel() {
    if (selected < 0) {
      return;
    }
    panelOpen = true;
    drawPanel();
    hosts.panel.hidden = false;
    hosts.layout.classList.add("has-detail");
    // Beside the waterfall, focus stays on the row so the arrow keys keep
    // stepping through the trace with the panel following. On a narrow
    // screen the panel covers the waterfall, so focus goes where the reader
    // now is.
    if (deps.isNarrow()) {
      hosts.panelClose.focus();
    }
  }

  function closePanel(returnFocus) {
    if (!panelOpen) {
      return;
    }
    panelOpen = false;
    hosts.panel.hidden = true;
    hosts.layout.classList.remove("has-detail");
    clear(hosts.panelBody);
    if (returnFocus && selected >= 0 && rowNodes[selected]) {
      rowNodes[selected].focus({ preventScroll: true });
    }
  }

  function drawPanel() {
    const row = rows[selected];
    if (row === undefined) {
      return;
    }
    const o = row.observation;
    const body = hosts.panelBody;
    clear(body);
    hosts.panelTitle.textContent = actionName(o);

    body.append(detailGroup("Decision", [
      ["Decision", render.retainedValue(o, "decision")],
      ["Risk level", render.retainedValue(o, "risk_level")],
      ["Policy rule", render.retainedValue(o, "policy_rule")],
      ["Matched default", render.retainedValue(o, "matched_default")],
      ["Approval status", render.retainedValue(o, "approval_status")],
    ]));
    body.append(detailGroup("Timing", [
      ["Timestamp", render.retainedValue(o, "timestamp")],
      ["From trace start", offsetText(row.offset)],
      ["Duration", row.durationText],
      ["Span status", spanStatusLabel(o)],
    ]));
    body.append(detailGroup("Structure", [
      ["Parent", parentStateText(row.state)],
      ["Span lineage", render.retainedValue(o, "span_lineage")],
      ["Depth", row.depthClamped ? `${row.depth} (clamped)` : String(row.depth)],
      ["Duplicate span id", row.duplicateSpan ? "yes — more than one retained action carries it" : "no"],
    ]));
    body.append(detailGroup("Behavior", [
      ["Operation", behaviorText(o.behavior, "operation_name")],
      ["Category", behaviorText(o.behavior, "operation_category")],
      ["Target", behaviorText(o.behavior, "target_name")],
      ["Target category", behaviorText(o.behavior, "target_category")],
      ["New to this run", newBehaviorLabel(o)],
      ["Actor", render.retainedValue(o, "actor_id")],
    ]));
    body.append(detailGroup("Scores", [
      ["Trust", render.retainedValue(o, "trust_score")],
      ["Anomaly", render.retainedValue(o, "anomaly_score")],
      ["Anomaly confidence", render.retainedValue(o, "anomaly_confidence")],
      ["Context risk", render.retainedValue(o, "context_risk")],
      ["Identity confidence", render.retainedValue(o, "identity_confidence")],
    ]));
    body.append(detailRefs("Identity", [
      { key: "Sequence", value: render.retainedValue(o, "sequence") },
      { key: "Span", value: render.retainedValue(o, "span_id") },
      { key: "Parent span", value: render.retainedValue(o, "parent_span_id") },
      {
        key: "Session",
        value: render.retainedValue(o, "session_id"),
        onOpen: (value) => nav.openSession(runID, value),
      },
      {
        key: "Behavior",
        value: render.retainedValue(o, "fingerprint_id"),
        onOpen: (value) => nav.openBehavior(runID, value),
      },
      { key: "Delegated from", value: render.retainedValue(o, "delegated_from") },
      { key: "Event", value: render.retainedValue(o, "event_id") },
    ]));
    body.append(element("p", "note",
      "Not retained, so not shown: attributes, events, links, service names, prompts, tool arguments and results."));
  }

  // ------------------------------------------------------------- lifecycle

  // retarget follows the context's run. A different run abandons both reads
  // and clears everything about the previous run — list, filter, trace,
  // selection and panel — so nothing on screen belongs to a run the reader
  // has left.
  function retarget(nextRunID) {
    if (nextRunID === runID) {
      return false;
    }
    runID = nextRunID;
    // A trace asked for under the previous run is not a trace of this one.
    pendingTraceID = "";
    listSurface.retarget(runID);
    traceSurface.retarget(runID);
    list = emptyList();
    filter = "";
    trace = emptyTrace();
    closePanel(false);
    return true;
  }

  function drawAll() {
    drawList();
    drawTrace();
  }

  hosts.panelClose.addEventListener("click", () => closePanel(true));
  hosts.panel.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      closePanel(true);
    }
  });

  selection.subscribe((what) => {
    if (what !== "run" && what !== "candidate" && what !== "agent" && what !== "project") {
      return;
    }
    if (retarget(contextRunID()) && visible) {
      drawAll();
      void loadList("");
    } else if (visible) {
      drawList();
    }
  });

  return Object.freeze({
    // enter shows the destination, optionally asking for one trace by name.
    enter(traceID) {
      visible = true;
      const changed = retarget(contextRunID());
      if (typeof traceID === "string" && traceID !== "" && runID !== "") {
        pendingTraceID = traceID;
      }
      drawAll();
      if (changed || !list.loaded) {
        void loadList("");
      } else if (pendingTraceID !== "") {
        const wanted = pendingTraceID;
        pendingTraceID = "";
        void openTrace(wanted, "", 1);
      }
    },
    leave() {
      visible = false;
    },
    // closePanel is exported for the global Escape handler.
    closePanel() {
      closePanel(true);
    },
  });
}
