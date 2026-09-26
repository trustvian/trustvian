// The inspector: one observation's evidence, as the server reported it.
//
// Every value here is read and displayed. Nothing is computed, combined,
// thresholded, ranked or reduced — there is no aggregate "health score" and no
// browser-owned red/amber/green judgement, because five independent
// server-produced values collapsed into one colour would be this page
// inventing a verdict the platform never made.
//
// The field set is the existing allowlist and nothing beyond it. No prompt, no
// completion, no reasoning, no tool argument, no request or response body, no
// arbitrary attribute, no raw DecisionRecord. A richer-looking panel is not
// permission to widen the privacy surface; a detail drawer is not an
// exemption.

import { decisionToken, decisionClass, riskClass, operationLabel } from "./graph.js";

// METRICS are the four numeric readings, in a fixed order.
//
// Shown as the server's own numbers. A bar is drawn beside each because a
// proportion is easier to read than a decimal, and the number stays beside the
// bar — the bar is a second rendering of one value, never a substitute for it
// and never a rating of it.
const METRICS = Object.freeze([
  Object.freeze({ key: "trustScore", label: "Trust", help: "Trust score" }),
  Object.freeze({ key: "anomalyScore", label: "Anomaly", help: "Anomaly score" }),
  Object.freeze({
    key: "anomalyConfidence",
    label: "Confidence",
    help: "How much evidence backs the anomaly score",
  }),
]);

// renderInspector draws one edge's evidence, or an instruction when none is
// selected.
export function renderInspector(host, edge, context = {}) {
  host.replaceChildren();

  if (edge === null || edge === undefined) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = context.hasActivity
      ? "Select a behavior in the canvas or the timeline to inspect what Trustvian observed."
      : "Nothing observed yet. Evidence appears here once an agent starts working.";
    host.append(empty);
    return;
  }

  // The behavior, in the descriptor's own words.
  host.append(sectionHeading("Behavior"));

  const operation = document.createElement("p");
  operation.className = "inspect-operation";
  operation.textContent = operationLabel(edge) || "(no operation recorded)";
  host.append(operation);

  const target = document.createElement("p");
  target.className = "inspect-target";
  target.textContent = edge.targetName || "(no target recorded)";
  host.append(target);

  host.append(fieldList([
    ["Target category", edge.targetCategory],
    ["Actor type", edge.actorType],
    ["Environment", edge.environment],
  ]));

  // NEW is a state, and it is stated in words before anything else about it.
  if (edge.newBehavior) {
    const banner = document.createElement("p");
    banner.className = "inspect-new";
    const mark = document.createElement("span");
    mark.className = "badge-new";
    mark.textContent = "NEW";
    banner.append(mark);
    banner.append(document.createTextNode(
      " This behavior was not already represented in the run's evidence when " +
      "it was first observed."));
    host.append(banner);
  }

  host.append(sectionHeading("Observed state"));
  host.append(stateGrid(edge));
  host.append(metricList(edge));

  const observed = document.createElement("p");
  observed.className = "note-inline";
  observed.textContent =
    `${edge.pulses} ${edge.pulses === 1 ? "observation" : "observations"} of this ` +
    "behavior on the current connection. Authoritative counts come from /v1.";
  host.append(observed);

  // Identifiers last, as technical detail. They are not the visual hierarchy:
  // a developer reading this panel is looking at what the agent did, and the
  // opaque strings are what they need only when filing a bug or querying the
  // API.
  host.append(sectionHeading("Identifiers"));
  host.append(fieldList([
    ["Fingerprint", edge.id],
    ["Agent", context.agentID],
    ["Run", context.runID],
    ["Candidate", context.candidateID],
    ["Project", context.projectID],
  ], "mono"));
}

function sectionHeading(text) {
  const heading = document.createElement("h4");
  heading.className = "inspect-heading";
  heading.textContent = text;
  return heading;
}

// stateGrid shows decision and risk as words with their own state classes.
function stateGrid(edge) {
  const grid = document.createElement("div");
  grid.className = "inspect-state";

  grid.append(stateChip("Decision", decisionToken(edge.decision) || "—",
    decisionClass(edge.decision)));
  grid.append(stateChip("Risk", edge.riskLevel || "—", riskClass(edge.riskLevel)));
  return grid;
}

function stateChip(label, value, stateClass) {
  const chip = document.createElement("div");
  chip.className = "chip";
  if (stateClass !== "") {
    chip.classList.add(stateClass);
  }

  const name = document.createElement("span");
  name.className = "chip-label";
  name.textContent = label;
  chip.append(name);

  const text = document.createElement("span");
  text.className = "chip-value";
  text.textContent = value;
  chip.append(text);
  return chip;
}

// metricList renders the numeric readings with their exact values.
function metricList(edge) {
  const list = document.createElement("dl");
  list.className = "metrics";

  for (const metric of METRICS) {
    const value = edge[metric.key];
    const term = document.createElement("dt");
    term.textContent = metric.label;
    term.title = metric.help;
    list.append(term);

    const definition = document.createElement("dd");
    if (typeof value !== "number" || !Number.isFinite(value)) {
      definition.textContent = "—";
      list.append(definition);
      continue;
    }

    const text = document.createElement("span");
    text.className = "metric-value";
    // The server's number, rendered. Not rounded into a grade, not compared
    // against a threshold, not turned into a word.
    text.textContent = value.toFixed(2);
    definition.append(text);

    // A bar, drawn from the same number and labelled by it. These are 0..1
    // readings on this contract; anything outside that is clamped for
    // drawing only, and the printed value stays exact.
    const track = document.createElement("span");
    track.className = "metric-track";
    track.setAttribute("aria-hidden", "true");
    const fill = document.createElement("span");
    fill.className = "metric-fill";
    fill.style.width = `${Math.max(0, Math.min(1, value)) * 100}%`;
    track.append(fill);
    definition.append(track);

    list.append(definition);
  }
  return list;
}

function fieldList(pairs, className) {
  const list = document.createElement("dl");
  list.className = className ? `fields ${className}` : "fields";
  for (const [label, value] of pairs) {
    if (value === undefined || value === null || value === "") {
      continue;
    }
    const term = document.createElement("dt");
    term.textContent = label;
    list.append(term);
    const definition = document.createElement("dd");
    definition.textContent = value;
    list.append(definition);
  }
  return list;
}
