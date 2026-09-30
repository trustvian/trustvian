// What a surface shows when it is not showing records.
//
// Four states, and a view is always in exactly one of them: loading, empty,
// failed, or holding rows. Before this existed the first and the second were
// indistinguishable — a table that was still fetching and a table with nothing
// in it both rendered as the same line of italic text, so a reader could not
// tell "wait" from "there is nothing here".
//
// Nothing in this module reaches the control plane or knows what a run is. It
// is handed a message and returns DOM.

import { element, clear } from "../core/dom.js";
import { icon } from "./icons.js";

// skeleton draws the shape of the table that is coming.
//
// A shape rather than a spinner, so the page does not jump when the rows
// arrive and a reader can already see how wide the columns will be. It
// carries no data and is marked busy rather than read out: there is nothing
// here to announce yet, and announcing placeholder bars would be noise.
//
// The widths are a fixed pattern, not random. A random skeleton shimmers
// differently on every render, which reads as content changing.
const SKELETON_WIDTHS = Object.freeze(["18%", "14%", "22%", "20%", "10%", "8%"]);

export function skeleton(host, rows, columns) {
  clear(host);
  const wrap = element("div", "skeleton");
  wrap.setAttribute("aria-busy", "true");
  wrap.setAttribute("aria-live", "off");
  const count = Math.max(1, Math.min(rows === undefined ? 5 : rows, 12));
  const width = Math.max(1, Math.min(columns === undefined ? 6 : columns, SKELETON_WIDTHS.length));
  for (let row = 0; row < count; row += 1) {
    const line = element("div", "skeleton-row");
    for (let cell = 0; cell < width; cell += 1) {
      const bar = element("div", "skeleton-cell");
      bar.style.width = SKELETON_WIDTHS[cell];
      line.append(bar);
    }
    wrap.append(line);
  }
  host.append(wrap);
}

// emptyState says what is true and what to do about it.
//
// `title` states the absence as a fact; `hint` is the next action, and it is
// optional because sometimes there genuinely is none. Neither apologises: an
// empty collection is not a failure, and a screen that says "Oops!" about one
// is telling the reader something untrue.
export function emptyState(title, hint, iconName) {
  const host = element("div", "empty-state");
  host.append(icon(iconName || "empty"));
  host.append(element("p", "empty-state-title", title));
  if (hint) {
    host.append(element("p", "empty-state-hint", hint));
  }
  return host;
}

// inlineEmpty is the one-line form, for a pick list or a panel where the
// full block would be taller than the thing it sits in.
export function inlineEmpty(message) {
  return element("p", "empty", message);
}

// ---------------------------------------------------------------------
// Action feedback
// ---------------------------------------------------------------------

// Toasts report what happened, once, and disappear.
//
// Used only for an action whose result is otherwise invisible — a lifecycle
// transition the server accepted, a refused request. Never for a navigation,
// because the page changing is its own confirmation, and never for anything a
// reader can already see on screen.
//
// The region is polite rather than assertive: an action's outcome is worth
// hearing at the next pause, and interrupting a screen reader mid-sentence
// for "Promotion recorded" is worse than waiting.

const TOAST_MS = 5000;

let toastHost = null;

function host() {
  if (toastHost === null) {
    toastHost = document.getElementById("toasts");
  }
  return toastHost;
}

// notify shows one short message.
//
// `tone` is "done", "fail" or absent. It picks a colour and a mark; the text
// is what carries the meaning, which is why there is no tone that changes the
// wording.
export function notify(message, tone) {
  const region = host();
  if (region === null) {
    return;
  }
  const toast = element("div", tone === undefined ? "toast" : `toast toast-${tone}`);
  toast.append(icon(tone === "fail" ? "flag" : "allow"));
  toast.append(element("span", null, message));
  region.append(toast);
  window.setTimeout(() => toast.remove(), TOAST_MS);
}
