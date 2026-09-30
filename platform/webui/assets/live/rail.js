// The active agent/run rail.
//
// Cards are created and updated from RealtimeScope alone, with no /v1 read per
// observation. Every card is a statement about the current connection: it says
// this scope produced a frame here, and nothing about what is durably stored.
//
// Names are used where a name has been resolved and identifiers otherwise.
// Rendering never waits for a name — a card that appeared only after a lookup
// returned would make the live view as slow as the slowest read, for a label.

import { decisionToken, decisionClass, riskClass } from "./graph.js";

// renderRail draws the active scopes, most recently observed first.
//
// options:
//   selectedKey  which card the canvas is drawing
//   following    whether selection is auto-following activity
//   labelFor     (scope) => display name, or "" when none is resolved yet
//   onSelect     (key) => pin that scope
//   onFollow     () => return to auto-follow
export function renderRail(host, cards, options) {
  host.replaceChildren();

  const ordered = cards.ordered();
  if (ordered.length === 0) {
    host.append(railEmptyState());
    return;
  }

  const list = document.createElement("ul");
  list.className = "rail-list";
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Active agents and runs");

  ordered.forEach((card, index) => {
    const item = document.createElement("li");
    item.setAttribute("role", "presentation");
    item.append(buildCard(card, index === 0, options));
    list.append(item);
  });
  host.append(list);

  if (cards.saturated) {
    host.append(saturationNote(
      `+${cards.evicted} more active scopes are not listed. Showing the ` +
      `${cards.capacity} most recently active — a display limit, not the whole ` +
      `set of active work.`));
  }

  host.append(followControl(options));
}

function buildCard(card, isNewest, options) {
  const selected = card.key === options.selectedKey;
  const button = document.createElement("button");
  button.type = "button";
  button.className = selected ? "rail-card rail-card-selected" : "rail-card";
  button.setAttribute("role", "option");
  button.setAttribute("aria-selected", selected ? "true" : "false");

  // A marker as well as the border: which card is being drawn must not depend
  // on seeing a colour or a one-pixel edge.
  const mark = document.createElement("span");
  mark.className = "rail-mark";
  mark.textContent = isNewest ? "●" : "○";
  mark.setAttribute("aria-hidden", "true");

  const title = document.createElement("span");
  title.className = "rail-title";
  const name = options.labelFor(card.scope);
  title.textContent = name === "" ? (card.scope.agent_id || "(unnamed agent)") : name;

  const head = document.createElement("span");
  head.className = "rail-head";
  head.append(mark, title);
  button.append(head);

  // Context a developer reads, in words. The opaque identifiers are technical
  // detail and live in the inspector, not in the visual hierarchy here.
  const context = [card.scope.environment, shortRunLabel(card.scope)]
    .filter((part) => part !== "" && part !== undefined);
  if (context.length > 0) {
    const line = document.createElement("span");
    line.className = "rail-context";
    line.textContent = context.join(" · ");
    button.append(line);
  }

  const state = document.createElement("span");
  state.className = "rail-state";
  const tokens = [];
  if (card.lastDecision !== "") {
    tokens.push(decisionToken(card.lastDecision));
  }
  if (card.lastRisk !== "") {
    tokens.push(card.lastRisk);
  }
  // "seen live" is load-bearing wording: this counts frames that arrived on
  // this connection, never the run's authoritative record count.
  tokens.push(`${card.seenLive} seen live`);
  state.textContent = tokens.join(" · ");
  const decision = decisionClass(card.lastDecision);
  if (decision !== "") {
    state.classList.add(decision);
  }
  const risk = riskClass(card.lastRisk);
  if (risk !== "") {
    state.classList.add(risk);
  }
  button.append(state);

  button.setAttribute("aria-label", railCardLabel(card, name, selected));
  button.addEventListener("click", () => options.onSelect(card.key));
  return button;
}

// shortRunLabel names the run in as few characters as stay unambiguous.
function shortRunLabel(scope) {
  const run = scope.run_id || "";
  if (run === "") {
    return "";
  }
  return run.length <= 22 ? run : `${run.slice(0, 19)}…`;
}

function railCardLabel(card, name, selected) {
  const parts = [name === "" ? card.scope.agent_id : name];
  if (card.scope.run_id) {
    parts.push(`run ${card.scope.run_id}`);
  }
  if (card.scope.environment) {
    parts.push(card.scope.environment);
  }
  parts.push(`${card.seenLive} observations seen on this stream`);
  if (selected) {
    parts.push("currently shown in the canvas");
  }
  return parts.join(", ");
}

function followControl(options) {
  const wrapper = document.createElement("p");
  wrapper.className = "rail-follow";

  const state = document.createElement("span");
  state.className = "rail-follow-state";
  state.textContent = options.following
    ? "Following newest activity"
    : "Pinned to your selection";
  wrapper.append(state);

  if (!options.following) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "link-button";
    button.textContent = "Follow active";
    button.addEventListener("click", () => options.onFollow());
    wrapper.append(button);
  }
  return wrapper;
}

// railEmptyState teaches rather than asking for an identifier.
//
// The old empty state was four text inputs, which told a developer that
// Trustvian needed something from them before it could be useful. It does
// not: it needs telemetry, and telemetry comes from running an agent.
export function railEmptyState() {
  const wrapper = document.createElement("div");
  wrapper.className = "rail-empty";

  const heading = document.createElement("p");
  heading.className = "rail-empty-title";
  heading.textContent = "Waiting for agent activity";
  wrapper.append(heading);

  const body = document.createElement("p");
  body.textContent =
    "Trustvian is connected, but no agent telemetry is arriving yet. " +
    "Run an instrumented agent and its activity appears here by itself — " +
    "there is nothing to type.";
  wrapper.append(body);
  return wrapper;
}

// saturationNote states a bound and what is not being shown.
export function saturationNote(text) {
  const node = document.createElement("p");
  node.className = "notice";
  const mark = document.createElement("span");
  mark.className = "notice-mark";
  mark.textContent = "! ";
  mark.setAttribute("aria-hidden", "true");
  node.append(mark, document.createTextNode(text));
  return node;
}

// renderCanvasNotices states each visualization bound that was hit, and the
// platform's own evidence bound separately.
//
// The separation is the requirement. A saturated viewport means the browser
// chose not to draw everything; behavior_complete: false means the platform's
// behavioral evidence itself saturated at 512 distinct behaviors. Reporting
// one as the other would misdescribe the run.
export function renderCanvasNotices(host, graph) {
  host.replaceChildren();
  if (graph === null || graph === undefined) {
    return;
  }
  for (const hit of graph.saturation()) {
    host.append(saturationNote(
      `Graph limited to ${hit.limit} ${hit.bound}. ${hit.omitted} additional ` +
      `observed ${hit.bound} are not currently drawn. This is a display limit; ` +
      `the run's own evidence is unaffected.`));
  }
  if (graph.behaviorComplete === false) {
    host.append(saturationNote(
      "The platform reports this run's behavioral evidence as incomplete: it " +
      "reached its distinct-behavior limit. This is a statement about the " +
      "recorded evidence, not about what the graph chose to draw."));
  }
}
