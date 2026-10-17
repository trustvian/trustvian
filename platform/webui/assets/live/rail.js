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
import { formatClock } from "./timeline.js";

// renderRail draws the active scopes, most recently observed first.
//
// options:
//   selectedKey  which card the canvas is drawing
//   following    whether selection is auto-following activity
//   labelFor     (scope) => display name, or "" when none is resolved yet
//   onSelect     (key) => pin that scope
//   onFollow     () => return to auto-follow
//   actors       the selected run's SeenActors, or null
//   followedActor  the actor key the view is narrowed to, "" for all
//   nameActor    (row) => { label, title } for one listed actor
//   onFollowActor  (key) => narrow to that actor, "" for all actors
//   onOpenObservations  () => open the selected run's Observations
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

  // Under the selected run's cards, the actors its frames named (task 109).
  // Outside the listbox: its rows are toggle buttons, not options of it.
  const selected = cards.get(options.selectedKey);
  if (selected !== undefined && options.actors && options.actors.size > 0) {
    host.append(actorList(options.actors, options));
  }

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

// actorList draws the selected run's actors seen live, most recently seen first,
// and the control back to all actors (task 109).
//
// Every figure in it is a frame count from this connection, labelled seen live,
// or an arrival time from this browser's clock, labelled as arrival. Nothing
// here is a per-actor aggregate of the run, which no /v1 route returns.
function actorList(actors, options) {
  const section = document.createElement("section");
  section.className = "rail-actors";
  section.setAttribute("aria-label", "Actors seen live in the selected run");

  const heading = document.createElement("p");
  heading.className = "rail-actors-title";
  heading.textContent = "Actors seen live";
  section.append(heading);

  const note = document.createElement("p");
  note.className = "rail-actors-note";
  note.append(document.createTextNode(
    "Actors named by the frames this connection received for the selected run, " +
    "not the run's actors. "));
  const observations = document.createElement("button");
  observations.type = "button";
  observations.className = "link-button";
  observations.textContent = "The run's observations name every actor.";
  observations.addEventListener("click", () => options.onOpenObservations());
  note.append(observations);
  section.append(note);

  const list = document.createElement("ul");
  list.className = "rail-actor-list";
  for (const row of actors.ordered()) {
    const item = document.createElement("li");
    item.append(actorRow(row, options));
    list.append(item);
  }
  section.append(list);

  if (actors.saturated) {
    section.append(saturationNote(
      `${actors.size} actors listed, ${actors.evicted} not listed: the least ` +
      `recently seen are evicted past ${actors.capacity}. A display limit, not ` +
      `the run's actors.`));
  }

  const all = document.createElement("button");
  all.type = "button";
  all.className = "btn-quiet rail-actors-all";
  all.textContent = "All actors";
  all.setAttribute("aria-pressed", options.followedActor === "" ? "true" : "false");
  all.addEventListener("click", () => options.onFollowActor(""));
  section.append(all);
  return section;
}

function actorRow(row, options) {
  const name = options.nameActor(row);
  const followed = row.key === options.followedActor;
  const button = document.createElement("button");
  button.type = "button";
  button.className = followed ? "rail-actor rail-actor-followed" : "rail-actor";
  // A toggle: pressed while the view follows this actor. Not carried by the
  // border alone.
  button.setAttribute("aria-pressed", followed ? "true" : "false");

  const title = document.createElement("span");
  title.className = "rail-actor-name";
  title.textContent = name.label === "" ? "(unnamed agent)" : name.label;
  button.append(title);

  const detail = document.createElement("span");
  detail.className = "rail-actor-detail";
  detail.textContent = [
    row.actorType,
    `last arrival ${formatClock(row.lastSeen)}`,
    `${row.seenLive} seen live`,
  ].filter((part) => part !== "").join(" · ");
  button.append(detail);

  button.setAttribute("aria-label", [
    name.label === "" ? "(unnamed agent)" : name.label,
    row.actorType,
    `last frame arrived ${formatClock(row.lastSeen)}`,
    `${row.seenLive} frames seen live on this connection`,
    followed ? "the view follows this actor" : "",
  ].filter((part) => part !== "").join(", "));
  button.addEventListener("click", () => options.onFollowActor(followed ? "" : row.key));
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
export function renderCanvasNotices(host, graph, followed = { key: "", label: "" }) {
  host.replaceChildren();
  if (graph === null || graph === undefined) {
    return;
  }
  // A followed actor whose source the graph evicted is not drawn, and the
  // view says so rather than showing an empty canvas as the actor's work.
  if (followed.key !== "" && !graph.sources.has(followed.key)) {
    host.append(saturationNote(
      `${followed.label} is not drawn: the graph evicted its source under the ` +
      `${graph.maxSources}-source display limit. It is drawn again on its next frame.`));
  }
  // The legend for a run with several actors (task 109). One fingerprint
  // performed by three actors is three lines to one target, and without this
  // the picture reads as three behaviors. A run with one actor draws as it
  // always did, so it gets no legend.
  if (graph.sources.size > 1) {
    const legend = document.createElement("p");
    legend.className = "region-note canvas-legend";
    legend.textContent =
      "One line per actor and behavior: a behavior two actors performed is one " +
      "fingerprint drawn twice, to one target.";
    host.append(legend);
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
