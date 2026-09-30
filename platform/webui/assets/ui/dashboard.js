// The console's presentation primitives: tables, strips, pick lists,
// breadcrumbs and the contextual detail panel.
//
// This module draws. It reads no route, holds no cursor and decides nothing
// about what a record means — a caller hands it rows the server returned and a
// callback per affordance, and gets back DOM.
//
// Two rules it shares with render.js, for the same reasons:
//
//   1. Every node is built with createElement and filled with textContent.
//      No markup is ever parsed, so a value the control plane returns cannot
//      become executable DOM however it is spelled.
//
//   2. A value reaches a cell because a column named the key. Nothing here
//      iterates a server object, so a field /v1 gains later appears nowhere
//      until somebody adds a column for it.
//
// It also never converts a uint64 to a Number. Counters, sequences and
// durations arrive as decimal strings and are formatted as strings, because
// JavaScript has no integer type that holds 18446744073709551615.

import { element, clear } from "../core/dom.js";
import { ABSENT as ABSENT_MARKER, decisionEdge, riskIsSevere } from "../core/format.js";
import { icon } from "./icons.js";
import { emptyState, inlineEmpty, skeleton } from "./feedback.js";

// ---------------------------------------------------------------------
// Value formatting
// ---------------------------------------------------------------------
//
// All of it lives in core/format.js, which knows nothing about the DOM and
// can be reasoned about — and tested — without one. Re-exported so a view
// composing a table has a single import.

export {
  shortTime,
  durationText,
  decisionEdge,
  decisionClass,
  riskClass,
  riskIsSevere,
  ABSENT,
} from "../core/format.js";

// ---------------------------------------------------------------------
// Controls
// ---------------------------------------------------------------------

// identChip renders one identifier as the control that follows it.
//
// The shape is the promise: every chip on this page goes somewhere. An
// identifier with nowhere to go is rendered flat instead, so the affordance
// never lies about what a click will do.
export function identChip(text, onOpen, label) {
  if (text === undefined || text === null || text === "" || text === ABSENT_MARKER) {
    return element("span", "ident-flat", ABSENT_MARKER);
  }
  if (typeof onOpen !== "function") {
    return element("span", "ident-flat", text);
  }
  const chip = element("button", "ident-chip", text);
  chip.type = "button";
  if (label) {
    chip.setAttribute("aria-label", `${label} ${text}`);
  }
  chip.addEventListener("click", (event) => {
    // The row beneath is a click target too, and this control is the more
    // specific answer to the same gesture.
    event.stopPropagation();
    onOpen(text);
  });
  return chip;
}

// verdict renders a decision or a risk level as a mark beside its word.
//
// The word is the server's and carries the meaning on its own — this adds a
// glyph so a reader scanning a long column finds the rows that matter without
// reading every one, and a colour so they find them faster still. Remove both
// and the table is still correct, which is the test for whether either was
// allowed to be added.
export function verdict(word, edge, className) {
  const host = element("span", className ? `verdict ${className}` : "verdict");
  if (edge !== "") {
    host.append(icon(edge));
  }
  host.append(element("span", null, word));
  return host;
}

// ---------------------------------------------------------------------
// Data tables
// ---------------------------------------------------------------------

// dataTable draws one bounded page as the view's primary navigation.
//
// Rows are how a reader moves: the leading cell holds a real button, which is
// what makes a row reachable and operable from the keyboard, and a click
// anywhere on the row is the pointer shorthand for pressing it. Both run the
// same callback, so there is one way in with two ways to ask for it.
//
// Columns are declared, never derived. `spec.columns` is a list of
// { key, label, align, cell } and a cell function returns a string or a Node.
export function dataTable(host, spec) {
  clear(host);

  // Loading and empty are different answers and must not look the same. A
  // table still fetching and a table with nothing in it both used to render
  // one line of italic text, so a reader could not tell "wait" from "there
  // is nothing here".
  if (spec.loading) {
    skeleton(host, spec.skeletonRows, spec.columns.length);
    return;
  }

  const rows = Array.isArray(spec.rows) ? spec.rows : [];
  if (rows.length === 0) {
    host.append(emptyState(
      spec.emptyTitle || spec.emptyMessage || "Nothing to show.",
      spec.emptyHint,
      spec.emptyIcon,
    ));
    return;
  }

  const table = element("table", "data-table");
  if (spec.caption) {
    table.append(element("caption", null, spec.caption));
  }

  const head = element("thead");
  const headRow = element("tr");
  for (const column of spec.columns) {
    const cell = element("th", column.align === "right" ? "num" : null, column.label);
    cell.setAttribute("scope", "col");
    headRow.append(cell);
  }
  head.append(headRow);
  table.append(head);

  const body = element("tbody");
  for (const row of rows) {
    const key = spec.keyOf(row);
    const tr = element("tr");
    const classes = ["row-open"];
    if (spec.edgeOf) {
      const edge = spec.edgeOf(row);
      if (edge !== "") {
        classes.push(`edge-${edge}`);
      }
    }
    if (spec.selected !== undefined && spec.selected !== "" && spec.selected === key) {
      classes.push("row-selected");
      tr.setAttribute("aria-selected", "true");
    }
    tr.className = classes.join(" ");
    tr.dataset.rowKey = key;

    for (const column of spec.columns) {
      const produced = column.cell(row);
      const cell = element("td", column.align === "right" ? "num" : column.className || null);
      if (produced instanceof Node) {
        cell.append(produced);
      } else {
        cell.textContent = produced === undefined || produced === null || produced === ""
          ? "—"
          : String(produced);
      }
      tr.append(cell);
    }

    if (typeof spec.onOpen === "function") {
      tr.addEventListener("click", () => spec.onOpen(row, tr));
    }
    body.append(tr);
  }
  table.append(body);
  host.append(table);
}

// focusRow returns focus to a selected row's own control.
//
// Closing the detail panel must not send a reader back to the top of the
// table: the row they were reading is where they were, and it is where the
// next keystroke should land.
//
// The row is found by comparing the key rather than by building an attribute
// selector out of it. An identifier is server-supplied text that may contain
// a quote or a backslash, and a selector assembled from one is a selector the
// server can shape.
export function focusRow(host, key) {
  for (const row of host.querySelectorAll("tr[data-row-key]")) {
    if (row.dataset.rowKey !== key) {
      continue;
    }
    const control = row.querySelector("button");
    if (control !== null) {
      control.focus();
    }
    return;
  }
}

// markSelected moves the selection without redrawing the table.
//
// Redrawing would lose the reader's scroll position and, on a page they had
// paged forward to, their place in the collection. Selection is a class.
export function markSelected(host, key) {
  for (const row of host.querySelectorAll("tr[data-row-key]")) {
    const active = row.dataset.rowKey === key;
    row.classList.toggle("row-selected", active);
    if (active) {
      row.setAttribute("aria-selected", "true");
    } else {
      row.removeAttribute("aria-selected");
    }
  }
}

// ---------------------------------------------------------------------
// Summary strip
// ---------------------------------------------------------------------

// summaryStrip renders a handful of authoritative figures.
//
// Every item is a value the server returned. Nothing is computed from what is
// on screen and nothing decorative is added: a figure here is evidence, and a
// strip that counted its own rows would report how much it drew rather than
// how much the run observed.
export function summaryStrip(host, items) {
  clear(host);
  for (const item of items) {
    if (item.value === undefined || item.value === null || item.value === "") {
      continue;
    }
    const cell = element("div", "strip-item");
    cell.append(element("span", "strip-key", item.key));
    const value = element("span", `strip-value ${item.className || ""}`.trim(), item.value);
    cell.append(value);
    host.append(cell);
  }
}

// ---------------------------------------------------------------------
// Pick lists
// ---------------------------------------------------------------------

// pickList draws one bounded level as a visible list of choices.
//
// Visible rather than collapsed: a reader can see how many options exist and
// which one is chosen without opening anything. The continuation stays an
// explicit control, because one press is one request and this page never
// follows a cursor on its own.
export function pickList(host, moreButton, spec) {
  clear(host);

  if (spec.loading) {
    skeleton(host, 3, 1);
    moreButton.hidden = true;
    return;
  }
  if (!spec.level.loaded) {
    host.append(inlineEmpty(spec.pendingMessage));
    moreButton.hidden = true;
    return;
  }
  if (spec.level.rows.length === 0) {
    host.append(inlineEmpty(spec.emptyMessage));
    moreButton.hidden = true;
    return;
  }

  for (const row of spec.level.rows) {
    const item = element("button", "pick-item");
    item.type = "button";
    item.setAttribute("aria-pressed", spec.selected === row.id ? "true" : "false");
    item.append(element("span", null, spec.labelOf(row)));
    const detail = spec.detailOf(row);
    if (detail) {
      item.append(element("span", "pick-id", detail));
    }
    item.addEventListener("click", () => spec.onOpen(row));
    host.append(item);
  }

  moreButton.hidden = spec.level.nextAfter === "";
}

// ---------------------------------------------------------------------
// Breadcrumbs
// ---------------------------------------------------------------------

// renderCrumbs draws the current location as the path that reached it.
//
// Every step but the last is a control, so a reader can go back up the
// selection they made rather than starting again from the sidebar.
export function renderCrumbs(host, trail) {
  clear(host);
  trail.forEach((step, index) => {
    if (index > 0) {
      host.append(element("span", "crumb-sep", "›"));
    }
    if (typeof step.onOpen === "function" && index < trail.length - 1) {
      const crumb = element("button", "crumb", step.label);
      crumb.type = "button";
      crumb.addEventListener("click", step.onOpen);
      host.append(crumb);
    } else {
      host.append(element("span", "crumb crumb-current", step.label));
    }
  });
}

// ---------------------------------------------------------------------
// Contextual detail
// ---------------------------------------------------------------------

// detailGroup renders one titled block of label/value pairs.
export function detailGroup(title, pairs) {
  const group = element("div", "detail-group");
  group.append(element("p", "detail-group-title", title));
  const list = element("dl", "kv");
  for (const [label, value] of pairs) {
    list.append(element("dt", null, label));
    list.append(element("dd", null, value));
  }
  group.append(list);
  return group;
}

// detailRefs renders the correlation references an observation recorded.
//
// Each reference that can be followed is a chip, and each that cannot is
// flat. What decides that is whether the caller supplied somewhere to go, not
// what the value looks like.
export function detailRefs(title, refs) {
  const group = element("div", "detail-group");
  group.append(element("p", "detail-group-title", title));
  const host = element("div", "detail-refs");
  for (const ref of refs) {
    const line = element("div", "detail-ref");
    line.append(element("span", "detail-ref-key", ref.key));
    line.append(identChip(ref.value, ref.onOpen, ref.key));
    host.append(line);
  }
  group.append(host);
  return group;
}
