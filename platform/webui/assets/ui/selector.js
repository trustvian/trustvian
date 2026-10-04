// A searchable selector: choose a record by name, see its identifier (task 098).
//
// An ARIA 1.2 combobox — a text input that filters a listbox popup — because
// that is the pattern assistive technology already knows for "type to narrow,
// arrow to move, Enter to choose", and a native <select> can neither search
// nor show an identifier beside a name.
//
// Presentation only, like the rest of `ui/`. It draws the options it is
// handed and reports intent through callbacks: opening the list, choosing,
// asking for the next page, pasting. It issues no request and holds no
// cursor; whoever owns the data decides what a choice means. Every value is
// set with textContent, so a name the control plane returns cannot become
// markup.
//
// Filtering is over the options already loaded, and the footer says so: a
// filter that finds nothing on one page of a larger collection is not a
// statement that the record does not exist.

import { element, clear } from "../core/dom.js";
import { notify } from "./feedback.js";

let nextSelectorID = 0;

// filterOptions keeps the options whose name, detail or identifier contains
// the query, case-insensitively. Pure, so it is tested without a DOM.
export function filterOptions(options, query) {
  const needle = String(query || "").trim().toLowerCase();
  if (needle === "") {
    return options.slice();
  }
  return options.filter((option) => [option.name, option.detail, option.id]
    .some((part) => typeof part === "string" && part.toLowerCase().includes(needle)));
}

// moveActive is the keyboard model over a list of `count` options.
//
// -1 means nothing is active. Movement wraps at both ends, Home and End jump,
// and an empty list has nothing to make active.
export function moveActive(active, count, key) {
  if (count <= 0) {
    return -1;
  }
  switch (key) {
    case "ArrowDown":
      return active < 0 ? 0 : (active + 1) % count;
    case "ArrowUp":
      return active < 0 ? count - 1 : (active - 1 + count) % count;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return active;
  }
}

// statusText is the footer's sentence about what the options are.
export function statusText(state, shown) {
  if (state.disabled) {
    return state.disabledText || "Choose the level above first.";
  }
  if (state.error) {
    return `Could not load: ${state.error}`;
  }
  if (state.loading && state.total === 0) {
    return "Loading…";
  }
  if (state.total === 0) {
    return state.emptyText || "Nothing to choose from.";
  }
  const scope = state.whole
    ? `${state.total} in all`
    : `${state.total} loaded; more exist`;
  // An optional sentence about where the options come from, ahead of the
  // count, so "3 in all" is never read without its scope.
  const lead = state.scopeText ? `${state.scopeText} · ` : "";
  if (shown === state.total) {
    return state.capped
      ? `${lead}${scope}. Paste an ID to reach one further along.`
      : `${lead}${scope}.`;
  }
  return state.whole
    ? `${lead}${shown} of ${state.total} match.`
    : `${lead}${shown} of ${state.total} loaded match; more exist.`;
}

async function copyText(text, what) {
  try {
    await navigator.clipboard.writeText(text);
    notify(`Copied ${what} ${text}.`, "done");
  } catch (ignored) {
    notify(`Could not copy; the identifier is ${text}.`, "fail");
  }
}

// createSelector builds one selector inside `host`.
//
// spec: { label, hint, placeholder, kind, emptyText,
//         onOpen(), onChoose(option), onMore(), onClear(),
//         onPaste(text) — present only where a paste path belongs here }
// An option is { id, name, detail }. `name` is what a reader recognises;
// `id` is always shown beside it and is what Copy copies.
export function createSelector(host, spec) {
  nextSelectorID += 1;
  const base = `selector-${nextSelectorID}`;
  const kind = spec.kind || "record";

  let state = {
    options: [],
    loading: false,
    error: "",
    whole: false,
    hasMore: false,
    capped: false,
    disabled: false,
    disabledText: "",
    scopeText: "",
    emptyText: spec.emptyText || "",
    selected: null,
  };
  let open = false;
  let active = -1;
  let query = "";
  let visible = [];

  clear(host);
  host.classList.add("selector");

  const label = element("label", "selector-label", spec.label);
  label.htmlFor = `${base}-input`;
  host.append(label);

  const control = element("div", "selector-control");
  const input = element("input", "selector-input");
  input.type = "text";
  input.id = `${base}-input`;
  input.autocomplete = "off";
  input.spellcheck = false;
  input.setAttribute("role", "combobox");
  input.setAttribute("aria-autocomplete", "list");
  input.setAttribute("aria-expanded", "false");
  input.setAttribute("aria-controls", `${base}-list`);
  input.placeholder = spec.placeholder || `Search ${kind}s by name or ID`;
  control.append(input);

  const toggle = element("button", "selector-toggle", "▾");
  toggle.type = "button";
  toggle.tabIndex = -1;
  toggle.setAttribute("aria-label", `Show ${kind}s`);
  control.append(toggle);
  host.append(control);

  if (spec.hint) {
    const hint = element("p", "hint selector-hint", spec.hint);
    hint.id = `${base}-hint`;
    input.setAttribute("aria-describedby", hint.id);
    host.append(hint);
  }

  // The current choice, with its identifier as a first-class value: visible,
  // and copyable, because disambiguating two records with the same name is
  // exactly what an identifier is for.
  const current = element("div", "selector-current");
  current.setAttribute("aria-live", "polite");
  host.append(current);

  const popup = element("div", "selector-popup");
  popup.hidden = true;
  const list = element("ul", "selector-list");
  list.id = `${base}-list`;
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", spec.label);
  popup.append(list);
  const foot = element("div", "selector-foot");
  const status = element("span", "selector-status");
  status.setAttribute("role", "status");
  foot.append(status);
  const more = element("button", "selector-more", "Load next page");
  more.type = "button";
  foot.append(more);
  popup.append(foot);
  host.append(popup);

  if (typeof spec.onPaste === "function") {
    const paste = element("details", "selector-paste");
    paste.append(element("summary", null, "Advanced: paste an ID"));
    const row = element("div", "row");
    const pasteLabel = element("label", null, `${spec.label} ID`);
    const pasteInput = element("input");
    pasteInput.type = "text";
    pasteInput.id = `${base}-paste`;
    pasteInput.autocomplete = "off";
    pasteInput.spellcheck = false;
    pasteLabel.htmlFor = pasteInput.id;
    const use = element("button", "btn-quiet", "Use");
    use.type = "button";
    const submit = () => {
      const text = pasteInput.value.trim();
      if (text !== "") {
        spec.onPaste(text);
      }
    };
    use.addEventListener("click", submit);
    pasteInput.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        submit();
      }
    });
    row.append(pasteLabel, pasteInput, use);
    paste.append(row);
    paste.append(element("p", "hint",
      "For an identifier from a log, a CI job or the CLI. Sent exactly as typed; the server says whether it exists."));
    host.append(paste);
  }

  const optionID = (index) => `${base}-opt-${index}`;

  function drawCurrent() {
    clear(current);
    const chosen = state.selected;
    if (chosen === null) {
      return;
    }
    const line = element("span", "selector-chosen");
    line.append(element("span", "selector-chosen-name", chosen.name || chosen.id));
    if (chosen.name && chosen.name !== chosen.id) {
      line.append(element("code", "selector-chosen-id", chosen.id));
    }
    current.append(line);
    const copy = element("button", "link-button selector-copy", "Copy ID");
    copy.type = "button";
    copy.setAttribute("aria-label", `Copy ${kind} ID ${chosen.id}`);
    copy.addEventListener("click", () => { void copyText(chosen.id, `${kind} ID`); });
    current.append(copy);
    if (typeof spec.onClear === "function" && !state.disabled) {
      const reset = element("button", "link-button selector-clear", "Clear");
      reset.type = "button";
      reset.setAttribute("aria-label", `Clear the chosen ${kind}`);
      reset.addEventListener("click", () => spec.onClear());
      current.append(reset);
    }
    if (chosen.note) {
      current.append(element("span", "selector-note", chosen.note));
    }
  }

  function drawList() {
    visible = filterOptions(state.options, query);
    if (active >= visible.length) {
      active = visible.length - 1;
    }
    clear(list);
    visible.forEach((option, index) => {
      const item = element("li", "selector-option");
      item.id = optionID(index);
      item.setAttribute("role", "option");
      const isChosen = state.selected !== null && state.selected.id === option.id;
      item.setAttribute("aria-selected", isChosen ? "true" : "false");
      if (index === active) {
        item.classList.add("is-active");
      }
      item.append(element("span", "selector-option-name", option.name || option.id));
      if (option.detail) {
        item.append(element("span", "selector-option-detail", option.detail));
      }
      if (option.name && option.name !== option.id) {
        item.append(element("code", "selector-option-id", option.id));
      }
      // mousedown, not click, keeps focus in the input so the popup does not
      // close before the choice registers.
      item.addEventListener("mousedown", (event) => event.preventDefault());
      item.addEventListener("click", () => choose(option));
      list.append(item);
    });
    if (active >= 0 && visible[active] !== undefined) {
      input.setAttribute("aria-activedescendant", optionID(active));
      const node = list.children[active];
      if (node && typeof node.scrollIntoView === "function") {
        node.scrollIntoView({ block: "nearest" });
      }
    } else {
      input.removeAttribute("aria-activedescendant");
    }
    status.textContent = statusText({
      ...state,
      total: state.options.length,
    }, visible.length);
    more.hidden = !state.hasMore || state.capped || state.disabled;
    more.disabled = state.loading;
    list.setAttribute("aria-busy", state.loading ? "true" : "false");
  }

  function displayText() {
    return state.selected === null ? "" : (state.selected.name || state.selected.id);
  }

  function setOpen(next) {
    if (state.disabled && next) {
      return;
    }
    if (open === next) {
      return;
    }
    open = next;
    popup.hidden = !open;
    input.setAttribute("aria-expanded", open ? "true" : "false");
    host.classList.toggle("is-open", open);
    if (open) {
      query = "";
      active = -1;
      if (typeof spec.onOpen === "function") {
        spec.onOpen();
      }
      drawList();
    } else {
      query = "";
      active = -1;
      input.value = displayText();
      input.removeAttribute("aria-activedescendant");
    }
  }

  function choose(option) {
    setOpen(false);
    spec.onChoose(option);
  }

  input.addEventListener("focus", () => input.select());
  input.addEventListener("click", () => setOpen(true));
  input.addEventListener("input", () => {
    if (!open) {
      setOpen(true);
    }
    query = input.value;
    active = filterOptions(state.options, query).length > 0 ? 0 : -1;
    drawList();
  });
  input.addEventListener("keydown", (event) => {
    if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      if (!open) {
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          event.preventDefault();
          setOpen(true);
        }
        return;
      }
      event.preventDefault();
      active = moveActive(active, visible.length, event.key);
      drawList();
      return;
    }
    if (event.key === "Enter" && open) {
      event.preventDefault();
      if (active >= 0 && visible[active] !== undefined) {
        choose(visible[active]);
      }
      return;
    }
    if (event.key === "Escape") {
      if (open) {
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
      }
      return;
    }
    if (event.key === "Tab") {
      setOpen(false);
    }
  });
  toggle.addEventListener("mousedown", (event) => event.preventDefault());
  toggle.addEventListener("click", () => {
    setOpen(!open);
    input.focus();
  });
  more.addEventListener("mousedown", (event) => event.preventDefault());
  more.addEventListener("click", () => {
    if (typeof spec.onMore === "function") {
      spec.onMore();
    }
  });
  host.addEventListener("focusout", (event) => {
    if (event.relatedTarget === null || !host.contains(event.relatedTarget)) {
      setOpen(false);
    }
  });

  function update(next) {
    state = { ...state, ...next };
    input.disabled = state.disabled;
    // A disabled selector says why in the one place a reader looks.
    input.placeholder = state.disabled && state.disabledText
      ? state.disabledText
      : (spec.placeholder || `Search ${kind}s by name or ID`);
    host.classList.toggle("is-disabled", state.disabled);
    if (state.disabled) {
      setOpen(false);
    }
    if (!open) {
      input.value = displayText();
    }
    drawCurrent();
    if (open) {
      drawList();
    }
  }

  update({});

  return Object.freeze({
    update,
    focus() { input.focus(); },
    get input() { return input; },
  });
}
