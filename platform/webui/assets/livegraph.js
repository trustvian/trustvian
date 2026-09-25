// SVG rendering for the Live view.
//
// Hand-built with createElementNS and textContent, never innerHTML and never a
// library. The topology is bounded at a few dozen nodes by construction, which
// is far below where a visualization dependency earns itself, and ADR 0036's
// no-build-step property is worth more here than any layout algorithm would
// be.
//
// Every string drawn comes from the API and is therefore untrusted. It reaches
// the DOM as a text node or an attribute value set through setAttribute — SVG
// text is not an exemption from the escaping rule, and neither is a title, a
// tooltip or an accessible name.

import { scopeKey } from "./live.js";

const SVG_NS = "http://www.w3.org/2000/svg";

// Layout constants. A fixed left-to-right fan: the source column on the left,
// targets stacked on the right. Deterministic, dependency-free, and legible at
// the bounds this view enforces.
const ROW_HEIGHT = 34;
const TOP_PADDING = 28;
const SOURCE_X = 150;
const TARGET_X = 470;
const VIEW_WIDTH = 760;

// svg builds one SVG element from an explicit list of [name, value] pairs.
//
// Pairs rather than an object literal read with Object.entries, because
// enumerating an object's keys is forbidden in this bundle and the ban is
// worth keeping absolute. Its purpose is that authoritative display iterates
// an allowlist and never a server-supplied object (ADR 0036 §7) — every
// attribute list here is written in this file and none is server-supplied, but
// an exception in the rule is how a server object later gets iterated by
// something that looked similar.
function svg(tag, pairs = []) {
  const node = document.createElementNS(SVG_NS, tag);
  for (const pair of pairs) {
    node.setAttribute(pair[0], String(pair[1]));
  }
  return node;
}

// svgText appends one text node. The content is set through textContent, so a
// target named `</text><script>` is drawn as those characters.
function svgText(x, y, content, className, extra = []) {
  const node = svg("text", [["x", x], ["y", y], ["class", className], ...extra]);
  node.textContent = content;
  return node;
}

// decisionShortLabel gives every decision a text token, so state is never
// carried by colour alone.
//
// The mapping is presentational abbreviation of a value the server chose, not
// a judgement about it: nothing here decides that BLOCK is worse than ALLOW,
// and an unrecognized decision renders as itself rather than as a default.
function decisionShortLabel(decision) {
  switch (decision) {
    case "ALLOW":
      return "allow";
    case "OBSERVE_ONLY":
      return "observe";
    case "ALERT":
      return "alert";
    case "CHALLENGE":
      return "challenge";
    case "REQUIRE_APPROVAL":
      return "approval";
    case "BLOCK":
      return "block";
    case "":
      return "";
    default:
      return decision;
  }
}

// renderGraph draws one run's topology.
//
// The whole SVG is rebuilt per change rather than diffed. At 8 sources, 64
// targets and 128 edges that is a trivial amount of DOM, and a diffing layer
// would be the framework this page does not have. Rebuilding also guarantees
// no residue from a previously selected run survives into the new one.
export function renderGraph(host, graph, options = {}) {
  host.replaceChildren();

  if (graph === null || graph === undefined || graph.edges.size === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = options.emptyMessage ||
      "No behavior observed on this stream yet. A quiet agent draws a quiet graph.";
    host.append(empty);
    return;
  }

  const targets = Array.from(graph.targets.values());
  const sources = Array.from(graph.sources.values());
  const height = TOP_PADDING + Math.max(targets.length, sources.length, 1) * ROW_HEIGHT + 16;

  const root = svg("svg", [
    ["class", "graph"],
    ["viewBox", `0 0 ${VIEW_WIDTH} ${height}`],
    ["role", "group"],
    ["aria-label", "Behavior flow for the selected run"],
  ]);

  const targetY = new Map();
  targets.forEach((target, index) => {
    targetY.set(target.id, TOP_PADDING + index * ROW_HEIGHT);
  });
  const sourceY = new Map();
  sources.forEach((source, index) => {
    sourceY.set(source.id, TOP_PADDING + index * ROW_HEIGHT);
  });

  // Edges first, so nodes draw over their endpoints.
  for (const edge of graph.edges.values()) {
    const y1 = sourceY.get(edge.sourceID);
    const y2 = targetY.get(edge.targetID);
    if (y1 === undefined || y2 === undefined) {
      // An endpoint was evicted under a bound. The edge is not drawn, and the
      // omission is already counted in graph.saturation().
      continue;
    }
    root.append(renderEdge(edge, y1, y2, options));
  }

  for (const source of sources) {
    root.append(renderSourceNode(source, sourceY.get(source.id)));
  }
  for (const target of targets) {
    root.append(renderTargetNode(target, targetY.get(target.id)));
  }

  host.append(root);
}

function renderEdge(edge, y1, y2, options) {
  const group = svg("g", [["class", "edge"]]);
  if (edge.newBehavior) {
    group.classList.add("edge-new");
  }

  const midX = (SOURCE_X + TARGET_X) / 2;
  const path = svg("path", [
    ["class", "edge-line"],
    ["d", `M ${SOURCE_X} ${y1} C ${midX} ${y1}, ${midX} ${y2}, ${TARGET_X} ${y2}`],
    ["fill", "none"],
  ]);
  group.append(path);

  // The pulse is one animated dot per received frame, keyed by the pulse count
  // so a repeat restarts it. Under prefers-reduced-motion the stylesheet
  // removes the movement; the edge, its label and its state stay exactly as
  // legible, because nothing here is conveyed by motion alone.
  if (!options.reducedMotion) {
    const pulse = svg("circle", [["class", "edge-pulse"], ["r", 4]]);
    const motion = svg("animateMotion", [
      ["dur", "0.6s"],
      ["repeatCount", "1"],
      ["fill", "freeze"],
      ["path", `M ${SOURCE_X} ${y1} C ${midX} ${y1}, ${midX} ${y2}, ${TARGET_X} ${y2}`],
    ]);
    pulse.append(motion);
    group.append(pulse);
  }

  // The operation, exactly as the descriptor carries it.
  const label = [edge.operationCategory, edge.operationName]
    .filter((part) => part !== "")
    .join(" ");
  group.append(svgText(midX, Math.min(y1, y2) + Math.abs(y2 - y1) / 2 - 6,
    label, "edge-label", [["text-anchor", "middle"]]));

  // State as text, never as colour alone.
  const state = [decisionShortLabel(edge.decision), edge.riskLevel]
    .filter((part) => part !== "")
    .join(" · ");
  if (state !== "") {
    group.append(svgText(midX, Math.min(y1, y2) + Math.abs(y2 - y1) / 2 + 9,
      state, "edge-state", [["text-anchor", "middle"]]));
  }

  // One accessible name carrying everything the visual carries.
  const title = svg("title");
  title.textContent = describeEdge(edge);
  group.append(title);
  group.setAttribute("role", "img");
  group.setAttribute("aria-label", describeEdge(edge));
  group.setAttribute("tabindex", "0");
  return group;
}

// describeEdge is the accessible name and the tooltip, built from the same
// allowlisted fields the visual uses. A tooltip is not an exemption: it
// carries nothing the graph does not already show.
function describeEdge(edge) {
  const parts = [];
  const operation = [edge.operationCategory, edge.operationName]
    .filter((part) => part !== "").join(" ");
  parts.push(operation === "" ? "operation unknown" : operation);
  if (edge.newBehavior) {
    parts.push("new behavior");
  }
  if (edge.decision !== "") {
    parts.push(`decision ${edge.decision}`);
  }
  if (edge.riskLevel !== "") {
    parts.push(`risk ${edge.riskLevel}`);
  }
  parts.push(`${edge.pulses} observed on this stream`);
  return parts.join(", ");
}

function renderSourceNode(source, y) {
  const group = svg("g", [["class", "node node-source"]]);
  group.append(svg("rect", [
    ["class", "node-box"], ["x", 8], ["y", y - 13],
    ["width", SOURCE_X - 16], ["height", 26], ["rx", 4],
  ]));
  group.append(svgText(16, y + 5, source.id === "" ? "(unnamed agent)" : source.id, "node-label"));
  const title = svg("title");
  title.textContent = `Agent ${source.id}`;
  group.append(title);
  return group;
}

function renderTargetNode(target, y) {
  const group = svg("g", [["class", "node node-target"]]);
  if (target.newBehavior) {
    group.classList.add("node-new");
  }
  group.append(svg("rect", [
    ["class", "node-box"], ["x", TARGET_X], ["y", y - 13],
    ["width", VIEW_WIDTH - TARGET_X - 8], ["height", 26], ["rx", 4],
  ]));
  group.append(svgText(TARGET_X + 8, y + 5,
    target.name === "" ? "(unnamed target)" : target.name, "node-label"));

  // NEW as text, because colour alone is not a state.
  if (target.newBehavior) {
    group.append(svgText(VIEW_WIDTH - 16, y + 5, "NEW", "node-badge", [["text-anchor", "end"]]));
  }
  const title = svg("title");
  title.textContent = target.category === ""
    ? target.name
    : `${target.name} (${target.category})`;
  group.append(title);
  return group;
}

// ---------------------------------------------------------------------
// Scope cards
// ---------------------------------------------------------------------

// renderScopeCards draws the active scopes, most recently observed first.
//
// onSelect makes each card a button rather than a link: selecting a scope
// changes what this page draws and navigates nowhere.
export function renderScopeCards(host, cards, selectedKey, onSelect) {
  host.replaceChildren();

  if (cards.size === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent =
      "No activity on this stream yet. Runs appear here by themselves when telemetry arrives.";
    host.append(empty);
    return;
  }

  const list = document.createElement("ul");
  list.className = "cards";
  for (const card of cards.ordered()) {
    const item = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "card";
    const selected = card.key === selectedKey;
    if (selected) {
      button.classList.add("card-selected");
    }
    // aria-pressed rather than colour: which card is being drawn is state a
    // screen reader must be able to report.
    button.setAttribute("aria-pressed", selected ? "true" : "false");

    appendCardLine(button, "agent", card.scope.agent_id);
    appendCardLine(button, "run", card.scope.run_id);
    appendCardLine(button, "candidate", card.scope.candidate_id);
    appendCardLine(button, "project", card.scope.project_id);
    appendCardLine(button, "environment", card.scope.environment);

    const seen = document.createElement("span");
    seen.className = "card-seen";
    // "seen live" is load-bearing wording. This is a count of frames that
    // arrived on this connection, not the run's record count — that is
    // authoritative and comes from /v1.
    seen.textContent = `${card.seenLive} seen live`;
    button.append(seen);

    if (card.lastDecision !== "" || card.lastRisk !== "") {
      const state = document.createElement("span");
      state.className = "card-state";
      state.textContent = [decisionShortLabel(card.lastDecision), card.lastRisk]
        .filter((part) => part !== "").join(" · ");
      button.append(state);
    }

    button.addEventListener("click", () => onSelect(card.key));
    item.append(button);
    list.append(item);
  }
  host.append(list);
}

function appendCardLine(host, label, value) {
  if (value === undefined || value === "") {
    return;
  }
  const line = document.createElement("span");
  line.className = "card-line";
  const name = document.createElement("span");
  name.className = "card-key";
  name.textContent = `${label} `;
  line.append(name);
  const text = document.createElement("span");
  text.className = "card-value";
  text.textContent = value;
  line.append(text);
  host.append(line);
}

// ---------------------------------------------------------------------
// Saturation and evidence
// ---------------------------------------------------------------------

// renderLiveNotices states each bound that was hit, and states the platform's
// own evidence bound separately.
//
// The separation is the requirement. A saturated viewport means the browser
// chose not to draw everything; behavior_complete: false means the platform's
// behavioral evidence itself saturated at 512 distinct behaviors. Reporting
// one as the other would misdescribe the run.
export function renderLiveNotices(host, model) {
  host.replaceChildren();

  const notices = [];
  if (model.cards.saturated) {
    notices.push(
      `Showing the ${model.cards.capacity} most recently active scopes. ` +
      `${model.cards.evicted} more became active and are not listed — this is a ` +
      `display limit, not the whole set of active work.`,
    );
  }
  for (const hit of model.graph === null ? [] : model.graph.saturation()) {
    notices.push(
      `The graph is showing at most ${hit.limit} ${hit.bound}; ${hit.omitted} ` +
      `observed ${hit.bound === "edges" ? "edges were" : "nodes were"} not drawn. ` +
      `This is a display limit. The run's own evidence is unaffected.`,
    );
  }
  if (model.graph !== null && model.graph.behaviorComplete === false) {
    notices.push(
      "The platform reports this run's behavioral evidence as incomplete: it " +
      "reached its distinct-behavior limit. This is a statement about the " +
      "recorded evidence, not about what the graph chose to draw.",
    );
  }

  for (const text of notices) {
    const node = document.createElement("p");
    node.className = "notice";
    // A text marker as well as the style, so the state is not carried by
    // colour alone.
    const mark = document.createElement("span");
    mark.className = "notice-mark";
    mark.textContent = "! ";
    node.append(mark);
    node.append(document.createTextNode(text));
    host.append(node);
  }
}

// ---------------------------------------------------------------------
// Hierarchy browsing
// ---------------------------------------------------------------------

// renderHierarchyLevel draws one bounded page of one level.
//
// Every row is a button that costs exactly one request when pressed, and the
// continuation is an explicit affordance rather than something followed
// automatically. A page is never implied to be the whole level: when
// next_after is present the count line says so in words.
export function renderHierarchyLevel(host, level) {
  host.replaceChildren();

  const heading = document.createElement("h4");
  heading.textContent = level.title;
  host.append(heading);

  if (level.rows.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = level.emptyMessage;
    host.append(empty);
    return;
  }

  const list = document.createElement("ul");
  list.className = "hierarchy";
  for (const row of level.rows) {
    const item = document.createElement("li");
    if (level.onOpen) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "link-button";
      button.textContent = row.id;
      button.setAttribute("aria-label", `${level.openLabel} ${row.id}`);
      button.addEventListener("click", () => level.onOpen(row.id));
      item.append(button);
    } else {
      const text = document.createElement("span");
      text.textContent = row.id;
      item.append(text);
    }
    if (row.detail) {
      const detail = document.createElement("span");
      detail.className = "hierarchy-detail";
      detail.textContent = ` ${row.detail}`;
      item.append(detail);
    }
    list.append(item);
  }
  host.append(list);

  const state = document.createElement("p");
  state.className = "note-inline";
  if (level.nextAfter) {
    state.textContent =
      `Showing ${level.rows.length}. More exist — nothing is loaded until you ask.`;
    host.append(state);

    const more = document.createElement("button");
    more.type = "button";
    more.textContent = "More";
    more.setAttribute("aria-label", `Load more ${level.title.toLowerCase()}`);
    more.addEventListener("click", () => level.onMore(level.nextAfter));
    host.append(more);
    return;
  }
  state.textContent = `Showing all ${level.rows.length}.`;
  host.append(state);
}

// selectedScopeSummary names the run the graph is currently drawing.
export function renderSelectedScope(host, card) {
  host.replaceChildren();
  if (card === undefined) {
    host.append(document.createTextNode("No run selected."));
    return;
  }
  const label = document.createElement("span");
  label.textContent = "Drawing ";
  host.append(label);
  const run = document.createElement("strong");
  run.textContent = card.scope.run_id || "(unnamed run)";
  host.append(run);
  host.append(document.createTextNode(
    ` — agent ${card.scope.agent_id || "?"}, candidate ${card.scope.candidate_id || "?"}.`));
}

// scopeKeyOf is re-exported so app.js can identify a scope without importing
// two modules for one function.
export { scopeKey as scopeKeyOf };
