// The live behavior canvas: one run's topology as SVG.
//
// Built with createElementNS and textContent, never innerHTML and never a
// library. The topology is bounded at a few dozen nodes by construction, far
// below where a visualization dependency earns itself, and ADR 0036's
// no-build-step property is worth more here than any layout algorithm.
//
// Structure, so the layers compose predictably and a pulse never repaints a
// node:
//
//   <svg>
//     <g class="edges">    the persistent topology
//     <g class="nodes">    the agent and its targets
//     <g class="pulses">   one transient element per received observation
//
// Every string drawn comes from the API and is untrusted. It reaches the DOM
// as a text node or through setAttribute. SVG text is not an exemption from
// the escaping rule, and neither is a <title>.

const SVG_NS = "http://www.w3.org/2000/svg";

// Layout. A fixed left-to-right fan: the agent on the left, its targets
// stacked on the right. Deterministic, dependency-free, and legible at the
// bounds this view enforces — a force-directed layout would be a dependency
// bought for a picture that is never crowded.
const ROW_HEIGHT = 40;
const TOP_PADDING = 34;
const AGENT_X = 168;
const TARGET_X = 470;
const VIEW_WIDTH = 820;
const MIN_HEIGHT = 140;

// PULSE_MS matches the CSS transition. One traversal per observation, never
// repeating: the animation is a report that a frame arrived, so it ends when
// the report is delivered.
export const PULSE_MS = 620;

function svg(tag, pairs = []) {
  const node = document.createElementNS(SVG_NS, tag);
  for (const pair of pairs) {
    node.setAttribute(pair[0], String(pair[1]));
  }
  return node;
}

function svgText(x, y, content, className, extra = []) {
  const node = svg("text", [["x", x], ["y", y], ["class", className], ...extra]);
  node.textContent = content;
  return node;
}

// decisionToken gives every decision a short text token.
//
// Presentational abbreviation of a value the server chose, not a judgement
// about it: nothing here decides that BLOCK is worse than ALLOW, and an
// unrecognized decision renders as itself rather than as a default. Ranking
// them would be policy, and policy is server-side.
export function decisionToken(decision) {
  switch (decision) {
    case "ALLOW": return "allow";
    case "OBSERVE_ONLY": return "observe";
    case "ALERT": return "alert";
    case "CHALLENGE": return "challenge";
    case "REQUIRE_APPROVAL": return "approval";
    case "BLOCK": return "block";
    case "": return "";
    default: return decision;
  }
}

// stateClass turns a server value into a CSS class name from a closed set.
//
// Allowlisted rather than interpolated: a class built from an arbitrary server
// string would let a hostile value select a rule this stylesheet did not
// intend. An unknown value simply gets no state class and keeps its text.
const DECISION_CLASSES = new Set([
  "ALLOW", "OBSERVE_ONLY", "ALERT", "CHALLENGE", "REQUIRE_APPROVAL", "BLOCK",
]);
const RISK_CLASSES = new Set(["low", "medium", "high", "critical"]);

export function decisionClass(decision) {
  return DECISION_CLASSES.has(decision) ? `d-${decision.toLowerCase()}` : "";
}

export function riskClass(risk) {
  return RISK_CLASSES.has(risk) ? `r-${risk}` : "";
}

// edgePath is the curve an edge and its pulse share.
//
// One definition, because a pulse that travelled a different path from the
// line it is reporting on would be an animation about nothing.
function edgePath(y1, y2) {
  const mid = (AGENT_X + TARGET_X) / 2;
  return `M ${AGENT_X} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${TARGET_X} ${y2}`;
}

// operationLabel is the descriptor's own words, joined and nothing more.
//
// If telemetry proves only "POST" against "export.localhost", the label is
// "POST". It is never expanded into export_customer() — semantic fidelity is
// task 075's, and inventing a name here would assert something no evidence
// supports.
export function operationLabel(edge) {
  return [edge.operationCategory, edge.operationName]
    .filter((part) => part !== "")
    .join(" ");
}

// GraphCanvas owns one <svg> and updates it in place.
//
// In place rather than rebuilt per frame, because a pulse has to start from an
// element that already exists and survive the next observation. Rebuilding
// also restarts every CSS transition, which would make a busy agent flicker.
export class GraphCanvas {
  constructor(host, options = {}) {
    this.host = host;
    this.onSelectEdge = options.onSelectEdge || (() => {});
    this.reducedMotion = options.reducedMotion || false;

    this.root = null;
    this.edgeLayer = null;
    this.nodeLayer = null;
    this.pulseLayer = null;

    // Rendered identity, so an update can tell "same topology, new state" from
    // "different run" without consulting the model twice.
    this.renderedScope = "";
    this.renderedEdges = "";
    this.selectedEdgeID = "";
    this.geometry = { agentY: 0, targets: new Map() };
  }

  // clear empties the canvas and forgets what it drew.
  clear(message) {
    this.host.replaceChildren();
    this.root = null;
    this.renderedScope = "";
    this.renderedEdges = "";
    this.geometry = { agentY: 0, targets: new Map() };
    if (message) {
      const empty = document.createElement("p");
      empty.className = "canvas-empty";
      empty.textContent = message;
      this.host.append(empty);
    }
  }

  // render draws the topology of one run.
  //
  // graph is a RunGraph, or null when nothing is selected. agentLabel is the
  // human-facing name when one has been resolved and the agent's own
  // identifier otherwise — a name is never waited for.
  render(graph, agentLabel) {
    if (graph === null || graph === undefined || graph.edges.size === 0) {
      this.clear("");
      return;
    }

    // A signature of what the topology is, not of what state it carries. When
    // it is unchanged only the state classes are refreshed, so an edge that
    // pulses fifty times is laid out once.
    const signature = topologySignature(graph);
    if (this.root !== null && this.renderedScope === graph.scopeKey &&
        this.renderedEdges === signature) {
      this.refreshState(graph);
      return;
    }

    this.host.replaceChildren();
    const targets = Array.from(graph.targets.values());
    const height = Math.max(MIN_HEIGHT, TOP_PADDING + targets.length * ROW_HEIGHT + 20);

    this.root = svg("svg", [
      ["class", "canvas"],
      ["viewBox", `0 0 ${VIEW_WIDTH} ${height}`],
      ["role", "group"],
      ["aria-label", "Live behavior flow for the selected run"],
    ]);
    this.edgeLayer = svg("g", [["class", "edges"]]);
    this.nodeLayer = svg("g", [["class", "nodes"]]);
    this.pulseLayer = svg("g", [["class", "pulses"], ["aria-hidden", "true"]]);
    this.root.append(this.edgeLayer, this.nodeLayer, this.pulseLayer);

    const agentY = TOP_PADDING + Math.max(0, (targets.length - 1) * ROW_HEIGHT) / 2;
    this.geometry = { agentY, targets: new Map() };
    targets.forEach((target, index) => {
      this.geometry.targets.set(target.id, TOP_PADDING + index * ROW_HEIGHT);
    });

    for (const edge of graph.edges.values()) {
      const y = this.geometry.targets.get(edge.targetID);
      if (y === undefined) {
        // An endpoint was evicted under a bound. The edge is not drawn, and
        // the omission is already counted in graph.saturation().
        continue;
      }
      this.edgeLayer.append(this.buildEdge(edge, agentY, y));
    }
    this.nodeLayer.append(this.buildAgentNode(agentLabel, agentY, targets.length));
    for (const target of targets) {
      this.nodeLayer.append(this.buildTargetNode(target, this.geometry.targets.get(target.id)));
    }

    this.host.append(this.root);
    this.renderedScope = graph.scopeKey;
    this.renderedEdges = signature;
    this.refreshState(graph);
  }

  buildEdge(edge, y1, y2) {
    const group = svg("g", [
      ["class", "edge"],
      ["data-edge", edge.id],
      ["role", "button"],
      ["tabindex", "0"],
    ]);
    group.append(svg("path", [
      ["class", "edge-line"], ["d", edgePath(y1, y2)], ["fill", "none"],
    ]));

    const midX = (AGENT_X + TARGET_X) / 2;
    const midY = Math.min(y1, y2) + Math.abs(y2 - y1) / 2;
    group.append(svgText(midX, midY - 7, operationLabel(edge), "edge-op",
      [["text-anchor", "middle"]]));

    const state = svgText(midX, midY + 10, "", "edge-state", [["text-anchor", "middle"]]);
    group.append(state);

    const title = svg("title");
    group.append(title);

    const select = () => this.onSelectEdge(edge.id);
    group.addEventListener("click", select);
    group.addEventListener("keydown", (event) => {
      // Enter and Space, because this is a role="button" and a keyboard user
      // must reach every edge the pointer can.
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        select();
      }
    });
    return group;
  }

  buildAgentNode(label, y, targetCount) {
    const group = svg("g", [["class", "node node-agent"]]);
    group.append(svg("rect", [
      ["class", "node-box"], ["x", 10], ["y", y - 19],
      ["width", AGENT_X - 20], ["height", 38], ["rx", 6],
    ]));
    group.append(svgText(22, y - 2, label === "" ? "(unnamed agent)" : label, "node-title"));
    group.append(svgText(22, y + 13,
      `${targetCount} ${targetCount === 1 ? "target" : "targets"}`, "node-sub"));
    const title = svg("title");
    title.textContent = `Agent ${label}`;
    group.append(title);
    return group;
  }

  buildTargetNode(target, y) {
    const group = svg("g", [
      ["class", "node node-target"],
      ["data-target", target.id],
      ["tabindex", "0"],
      ["role", "img"],
    ]);
    group.append(svg("rect", [
      ["class", "node-box"], ["x", TARGET_X], ["y", y - 15],
      ["width", VIEW_WIDTH - TARGET_X - 12], ["height", 30], ["rx", 5],
    ]));
    group.append(svgText(TARGET_X + 12, y + 5,
      target.name === "" ? "(unnamed target)" : target.name, "node-label"));

    // NEW as a word, because colour alone is not a state.
    if (target.newBehavior) {
      group.classList.add("node-new");
      group.append(svgText(VIEW_WIDTH - 22, y + 5, "NEW", "node-badge",
        [["text-anchor", "end"]]));
    }
    const title = svg("title");
    title.textContent = target.category === ""
      ? target.name
      : `${target.name} (${target.category})`;
    group.append(title);
    group.setAttribute("aria-label", title.textContent);
    return group;
  }

  // refreshState updates what each edge says without relaying anything out.
  refreshState(graph) {
    if (this.root === null) {
      return;
    }
    for (const edge of graph.edges.values()) {
      const group = this.edgeLayer.querySelector(`[data-edge="${cssEscape(edge.id)}"]`);
      if (group === null) {
        continue;
      }
      group.setAttribute("class", edgeClasses(edge, edge.id === this.selectedEdgeID));

      const state = group.querySelector(".edge-state");
      if (state !== null) {
        state.textContent = [decisionToken(edge.decision), edge.riskLevel]
          .filter((part) => part !== "").join(" · ");
      }
      const title = group.querySelector("title");
      const described = describeEdge(edge);
      if (title !== null) {
        title.textContent = described;
      }
      group.setAttribute("aria-label", described);
    }
  }

  // pulse reports one received observation along its edge.
  //
  // One element per frame, removed when it finishes. Nothing loops, nothing
  // idles, and an agent that stops working leaves a still canvas — the
  // animation is evidence that something arrived, so no frame means no motion.
  //
  // Under reduced motion no pulse element is created at all. The edge still
  // flashes its state class change and the timeline still gains a row, so the
  // fact the pulse carried is never lost with it.
  pulse(edge) {
    if (this.root === null || this.reducedMotion) {
      return;
    }
    const y2 = this.geometry.targets.get(edge.targetID);
    if (y2 === undefined) {
      return;
    }
    const dot = svg("circle", [["class", "pulse"], ["r", 4.5]]);
    const motion = svg("animateMotion", [
      ["dur", `${PULSE_MS}ms`],
      ["repeatCount", "1"],
      ["fill", "freeze"],
      ["path", edgePath(this.geometry.agentY, y2)],
    ]);
    dot.append(motion);
    this.pulseLayer.append(dot);

    // Removed on its own completion, so the layer cannot accumulate. A
    // timeout as well, because an SVG animation in a background tab may never
    // fire its end event and a leaked node per observation is unbounded
    // growth with a pretty name.
    const remove = () => dot.remove();
    motion.addEventListener("endEvent", remove);
    setTimeout(remove, PULSE_MS + 200);

    const group = this.edgeLayer.querySelector(`[data-edge="${cssEscape(edge.id)}"]`);
    if (group !== null) {
      group.classList.remove("edge-firing");
      // Reading offsetWidth-equivalent on SVG: forcing a reflow so the class
      // re-application restarts the transition rather than being coalesced.
      void group.getBoundingClientRect();
      group.classList.add("edge-firing");
      setTimeout(() => group.classList.remove("edge-firing"), PULSE_MS);
    }
  }

  // highlight marks one edge as the inspected one.
  highlight(edgeID, graph) {
    this.selectedEdgeID = edgeID;
    if (graph !== null && graph !== undefined) {
      this.refreshState(graph);
    }
  }
}

function edgeClasses(edge, selected) {
  const classes = ["edge"];
  if (edge.newBehavior) {
    classes.push("edge-new");
  }
  const decision = decisionClass(edge.decision);
  if (decision !== "") {
    classes.push(decision);
  }
  const risk = riskClass(edge.riskLevel);
  if (risk !== "") {
    classes.push(risk);
  }
  if (selected) {
    classes.push("edge-selected");
  }
  return classes.join(" ");
}

// describeEdge is the accessible name and the tooltip.
//
// Built from the same allowlisted fields the visual uses. A tooltip is not an
// exemption: it carries nothing the canvas does not already show.
export function describeEdge(edge) {
  const parts = [];
  const operation = operationLabel(edge);
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

// topologySignature changes when the shape changes and not when state does.
function topologySignature(graph) {
  const parts = [];
  for (const edge of graph.edges.values()) {
    parts.push(`${edge.id}>${edge.targetID}${edge.newBehavior ? "!" : ""}`);
  }
  return parts.join("|");
}

// cssEscape makes a server-supplied identifier safe inside a selector.
//
// A fingerprint is hex today, but it is server-supplied and the selector is
// the one place an odd character would change what is matched rather than
// what is displayed. CSS.escape where the browser has it, and a conservative
// backslash escape where it does not.
function cssEscape(value) {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") {
    return CSS.escape(value);
  }
  return String(value).replace(/[^\w-]/g, (character) => `\\${character}`);
}
