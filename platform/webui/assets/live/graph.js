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
//     <g class="nodes">    the actors and their targets
//     <g class="pulses">   one transient element per received observation
//
// Every string drawn comes from the API and is untrusted. It reaches the DOM
// as a text node or through setAttribute. SVG text is not an exemption from
// the escaping rule, and neither is a <title>.

const SVG_NS = "http://www.w3.org/2000/svg";

// Layout. A fixed left-to-right fan: the run's actors stacked on the left, one
// source each (task 109), their targets stacked on the right. A run with one
// actor draws one source where the single agent node always was. Deterministic, dependency-free, and legible at the
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
// The cases are the values the stream carries: policy's own lowercase wire
// strings (internal/policy), which the realtime projection copies verbatim.
// Any other spelling here is a token and a class that never apply.
//
// Presentational abbreviation of a value the server chose, not a judgement
// about it: nothing here decides that block is worse than allow, and an
// unrecognized decision renders as itself rather than as a default. Ranking
// them would be policy, and policy is server-side.
export function decisionToken(decision) {
  switch (decision) {
    case "allow": return "allow";
    case "observe_only": return "observe";
    case "alert": return "alert";
    case "challenge": return "challenge";
    case "require_approval": return "approval";
    case "block": return "block";
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
  "allow", "observe_only", "alert", "challenge", "require_approval", "block",
]);
const RISK_CLASSES = new Set(["low", "medium", "high", "critical"]);

export function decisionClass(decision) {
  return DECISION_CLASSES.has(decision) ? `d-${decision}` : "";
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
    this.geometry = { sources: new Map(), targets: new Map() };
    // How each drawn source is named, so a refresh can describe its lines
    // without asking the caller again.
    this.sourceNames = new Map();
  }

  // clear empties the canvas and forgets what it drew.
  clear(message) {
    this.host.replaceChildren();
    this.root = null;
    this.renderedScope = "";
    this.renderedEdges = "";
    this.geometry = { sources: new Map(), targets: new Map() };
    this.sourceNames = new Map();
    if (message) {
      const empty = document.createElement("p");
      empty.className = "canvas-empty";
      empty.textContent = message;
      this.host.append(empty);
    }
  }

  // render draws the topology of one run.
  //
  // graph is a RunGraph, or null when nothing is selected. nameSource maps a
  // source to { label, title, actor }: the agent's human-facing name (or its
  // identifier — a name is never waited for) when the actor is the run's
  // agent, and otherwise the actor id the frame stated. actor is "" for the
  // agent's own source, so its lines are described exactly as before.
  //
  // actor narrows the drawing to one source by its key (task 109): that
  // actor's source, its lines and the targets they reach. "" draws them all.
  // When the narrowed actor's source was evicted under the source bound,
  // nothing is drawn, and the notices say why.
  render(graph, nameSource, actor = "") {
    if (graph === null || graph === undefined || graph.edges.size === 0) {
      this.clear("");
      return;
    }
    const view = drawnTopology(graph, actor);
    if (view.sources.length === 0) {
      this.clear("");
      return;
    }

    // A signature of what the topology is, not of what state it carries. When
    // it is unchanged only the state classes are refreshed, so an edge that
    // pulses fifty times is laid out once.
    const signature = topologySignature(view);
    if (this.root !== null && this.renderedScope === graph.scopeKey &&
        this.renderedEdges === signature) {
      this.refreshState(graph);
      return;
    }

    this.host.replaceChildren();
    // Least recently observed first, top to bottom: the model's own order.
    const { sources, targets, edges } = view;
    // The taller column sets the height, and the shorter one is centred
    // against it. With one source this is exactly the single agent node's
    // position: centred on the targets.
    const rows = Math.max(targets.length, sources.length);
    const height = Math.max(MIN_HEIGHT, TOP_PADDING + rows * ROW_HEIGHT + 20);

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

    const targetOffset = ((rows - targets.length) * ROW_HEIGHT) / 2;
    const sourceOffset = (Math.max(0, rows - 1) - Math.max(0, sources.length - 1)) * ROW_HEIGHT / 2;
    this.geometry = { sources: new Map(), targets: new Map() };
    targets.forEach((target, index) => {
      this.geometry.targets.set(target.id, TOP_PADDING + targetOffset + index * ROW_HEIGHT);
    });
    sources.forEach((source, index) => {
      this.geometry.sources.set(source.id, TOP_PADDING + sourceOffset + index * ROW_HEIGHT);
    });
    this.sourceNames = new Map(sources.map((source) => [source.id, nameSource(source)]));

    // Each source's drawn targets, for its "N targets" line.
    const reached = new Map(sources.map((source) => [source.id, new Set()]));
    for (const edge of edges) {
      const y1 = this.geometry.sources.get(edge.sourceID);
      const y2 = this.geometry.targets.get(edge.targetID);
      if (y1 === undefined || y2 === undefined) {
        // An endpoint was evicted under a bound. The edge is not drawn, and
        // the omission is already counted in graph.saturation().
        continue;
      }
      reached.get(edge.sourceID).add(edge.targetID);
      this.edgeLayer.append(this.buildEdge(edge, y1, y2, sources.length > 1));
    }
    for (const source of sources) {
      this.nodeLayer.append(this.buildSourceNode(source, this.sourceNames.get(source.id),
        this.geometry.sources.get(source.id), reached.get(source.id).size));
    }
    for (const target of targets) {
      this.nodeLayer.append(this.buildTargetNode(target, this.geometry.targets.get(target.id)));
    }

    this.host.append(this.root);
    this.renderedScope = graph.scopeKey;
    this.renderedEdges = signature;
    this.refreshState(graph);
  }

  // shared is true when the drawing has more than one source. The operation
  // label then sits where the lines meet their target rather than at each
  // line's middle, so one behavior performed by several actors reads as one
  // label over converging lines rather than as several behaviors (task 109).
  // Each line's own decision and risk stay at its middle. One source draws
  // exactly as before.
  buildEdge(edge, y1, y2, shared = false) {
    const group = svg("g", [
      ["class", "edge"],
      ["data-edge", edge.id],
      ["data-fingerprint", edge.fingerprintID],
      ["role", "button"],
      ["tabindex", "0"],
    ]);
    group.append(svg("path", [
      ["class", "edge-line"], ["d", edgePath(y1, y2)], ["fill", "none"],
    ]));

    const midX = (AGENT_X + TARGET_X) / 2;
    const midY = Math.min(y1, y2) + Math.abs(y2 - y1) / 2;
    group.append(shared
      ? svgText(TARGET_X - 10, y2 - 6, operationLabel(edge), "edge-op", [["text-anchor", "end"]])
      : svgText(midX, midY - 7, operationLabel(edge), "edge-op", [["text-anchor", "middle"]]));

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

  // buildSourceNode draws one actor. The run's own agent keeps the agent
  // node's label and title; any other actor is named by the id its frames
  // stated, with its actor type in the title, which is the node's accessible
  // name. Actors are not platform entities, so there is no name to look up.
  buildSourceNode(source, name, y, targetCount) {
    const group = svg("g", [["class", "node node-agent"], ["data-source", source.id]]);
    group.append(svg("rect", [
      ["class", "node-box"], ["x", 10], ["y", y - 19],
      ["width", AGENT_X - 20], ["height", 38], ["rx", 6],
    ]));
    group.append(svgText(22, y - 2, name.label === "" ? "(unnamed agent)" : name.label, "node-title"));
    group.append(svgText(22, y + 13,
      `${targetCount} ${targetCount === 1 ? "target" : "targets"}`, "node-sub"));
    const title = svg("title");
    title.textContent = name.title;
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
      const name = this.sourceNames.get(edge.sourceID);
      const described = describeEdge(edge, name === undefined ? "" : name.actor);
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
    const y1 = this.geometry.sources.get(edge.sourceID);
    const y2 = this.geometry.targets.get(edge.targetID);
    if (y1 === undefined || y2 === undefined) {
      return;
    }
    const dot = svg("circle", [["class", "pulse"], ["r", 4.5]]);
    const motion = svg("animateMotion", [
      ["dur", `${PULSE_MS}ms`],
      ["repeatCount", "1"],
      ["fill", "freeze"],
      ["path", edgePath(y1, y2)],
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
// exemption: it carries nothing the canvas does not already show. actor names
// the line's actor when it is not the run's own agent: two actors' lines for one
// behavior carry the same operation label, and only this tells them apart.
export function describeEdge(edge, actor = "") {
  const parts = [];
  const operation = operationLabel(edge);
  parts.push(operation === "" ? "operation unknown" : operation);
  if (actor !== "") {
    parts.push(`by ${actor}`);
  }
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

// drawnTopology is what one render draws: every source, line and target, or
// one actor's source, its lines and the targets they reach. Selection by
// equality over what the graph holds; nothing is computed.
function drawnTopology(graph, actor) {
  if (actor === "") {
    return {
      actor,
      sources: Array.from(graph.sources.values()),
      targets: Array.from(graph.targets.values()),
      edges: Array.from(graph.edges.values()),
    };
  }
  const source = graph.sources.get(actor);
  if (source === undefined) {
    return { actor, sources: [], targets: [], edges: [] };
  }
  const edges = Array.from(graph.edges.values()).filter((edge) => edge.sourceID === actor);
  const reached = new Set(edges.map((edge) => edge.targetID));
  return {
    actor,
    sources: [source],
    targets: Array.from(graph.targets.values()).filter((target) => reached.has(target.id)),
    edges,
  };
}

// topologySignature changes when the shape changes and not when state does.
function topologySignature(view) {
  const parts = [view.actor, ...view.sources.map((source) => source.id)];
  for (const edge of view.edges) {
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
