// The live event timeline.
//
// A bounded viewport over the current connection, and labelled as exactly
// that. It is not retained history: the realtime bus keeps none, stream_ready
// publishes replay_available: false, and task 067 owns the capability that
// would change this. Two connections are never concatenated into one apparent
// sequence — a reconnect starts a new segment and says so.

import { decisionToken, decisionClass, riskClass, operationLabel } from "./graph.js";

// TIMELINE_MAX is the existing DISPLAY_MAX, reused rather than re-chosen.
// A second feed bound would be two numbers meaning one thing.
export const TIMELINE_MAX = 100;

// TimelineFeed holds the rows and the segment markers between connections.
export class TimelineFeed {
  constructor(capacity = TIMELINE_MAX) {
    this.capacity = capacity;
    this.rows = [];
    this.segments = 0;
  }

  // add records one observation. scopeKey lets the view show only the rows
  // belonging to the selected run when a developer wants that, without a
  // second copy of the data.
  add(entry) {
    this.rows.push(entry);
    while (this.rows.length > this.capacity) {
      this.rows.shift();
    }
  }

  // breakSegment marks a reconnect.
  //
  // The rows are cleared rather than kept above a divider. Keeping them would
  // put two connections' observations in one list, which is the appearance of
  // history this view must not create — and the earlier rows describe a
  // stream that no longer exists.
  breakSegment() {
    this.rows = [];
    this.segments += 1;
  }

  clear() {
    this.rows = [];
    this.segments = 0;
  }

  get size() {
    return this.rows.length;
  }
}

// renderTimeline draws the feed newest-first.
//
// No aria-live. A busy agent produces many observations per second, and
// announcing each would make a screen reader unusable while conveying nothing
// a reader could follow. The table is reachable and readable on demand, and
// the connection state — which is the thing worth announcing — lives in the
// header's status region.
export function renderTimeline(host, feed, options = {}) {
  host.replaceChildren();

  if (feed.rows.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = "No observations on this connection yet.";
    host.append(empty);
    return;
  }

  const scroll = document.createElement("div");
  scroll.className = "timeline-scroll";

  const table = document.createElement("table");
  table.className = "timeline";

  const caption = document.createElement("caption");
  // Says what it is, every time it is drawn.
  caption.textContent =
    `Live stream · current connection · showing ${feed.rows.length} of at most ` +
    `${feed.capacity} observations. Not retained history.`;
  table.append(caption);

  const head = document.createElement("thead");
  const headRow = document.createElement("tr");
  for (const label of ["Time", "Agent", "Operation", "Target", "Decision", "Risk", ""]) {
    const cell = document.createElement("th");
    cell.scope = "col";
    cell.textContent = label;
    headRow.append(cell);
  }
  head.append(headRow);
  table.append(head);

  const body = document.createElement("tbody");
  for (let i = feed.rows.length - 1; i >= 0; i -= 1) {
    body.append(buildRow(feed.rows[i], options));
  }
  table.append(body);
  scroll.append(table);
  host.append(scroll);
}

function buildRow(entry, options) {
  const row = document.createElement("tr");
  row.className = "timeline-row";
  if (entry.newBehavior) {
    row.classList.add("timeline-new");
  }
  if (options.selectedFingerprint && entry.fingerprintID === options.selectedFingerprint) {
    row.classList.add("timeline-selected");
  }

  appendCell(row, formatClock(entry.at));
  appendCell(row, entry.agentLabel);
  appendCell(row, operationLabel(entry));
  appendCell(row, entry.targetName);

  const decision = appendCell(row, decisionToken(entry.decision));
  const decisionState = decisionClass(entry.decision);
  if (decisionState !== "") {
    decision.classList.add(decisionState);
  }

  const risk = appendCell(row, entry.riskLevel);
  const riskState = riskClass(entry.riskLevel);
  if (riskState !== "") {
    risk.classList.add(riskState);
  }

  // NEW as a word in its own column, never as a colour on the row alone.
  const badge = document.createElement("td");
  if (entry.newBehavior) {
    const mark = document.createElement("span");
    mark.className = "badge-new";
    mark.textContent = "NEW";
    badge.append(mark);
  }
  row.append(badge);

  if (options.onSelect) {
    row.tabIndex = 0;
    row.setAttribute("role", "button");
    row.setAttribute("aria-label", describeRow(entry));
    const select = () => options.onSelect(entry);
    row.addEventListener("click", select);
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        select();
      }
    });
  }
  return row;
}

function appendCell(row, text) {
  const cell = document.createElement("td");
  cell.textContent = text === undefined || text === "" ? "—" : text;
  row.append(cell);
  return cell;
}

function describeRow(entry) {
  const parts = [formatClock(entry.at), entry.agentLabel, operationLabel(entry), entry.targetName];
  if (entry.decision !== "") {
    parts.push(`decision ${entry.decision}`);
  }
  if (entry.riskLevel !== "") {
    parts.push(`risk ${entry.riskLevel}`);
  }
  if (entry.newBehavior) {
    parts.push("new behavior");
  }
  return parts.filter((part) => part !== "" && part !== undefined).join(", ");
}

// formatClock is wall-clock time of arrival at this browser.
//
// The browser's own clock, and labelled as arrival rather than as the moment
// the platform recorded anything. An observation carries no timestamp on this
// contract, so presenting one as authoritative would be inventing it.
export function formatClock(at) {
  const date = new Date(at);
  const pad = (value) => String(value).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
