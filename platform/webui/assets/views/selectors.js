// Selectors bound to the selection context (task 098).
//
// `ui/selector.js` draws a combobox and `views/context.js` holds what is
// chosen; this is the glue, and the only place that knows which page a level
// is chosen from and how a record is described to a reader.
//
// Two kinds of binding:
//
//   - **Context-bound** — choosing here is choosing for the whole console.
//     The project, agent, candidate and run a form needs are the ones the
//     reader is already looking at, and a choice made in one destination is
//     the preselection in every other.
//   - **Local** — the options come from the context, the choice does not go
//     back into it. A promotion's reference and candidate run are two runs,
//     not "the" run; an environment chosen as a run's target is the form's.
//
// Either way the identifier the form sends is written into the form's own
// field, which stays as the paste path. Nothing about the request changes.

import { shortTime } from "../core/format.js";
import { createSelector } from "../ui/selector.js";

const PAGE_FOR = Object.freeze({
  project: "projects",
  agent: "agents",
  candidate: "candidates",
  run: "runs",
  environment: "environments",
});

const PARENT_OF = Object.freeze({
  project: "",
  agent: "project",
  candidate: "agent",
  run: "project",
  environment: "project",
});

const NEEDS = Object.freeze({
  agent: "Choose a project first.",
  candidate: "Choose an agent first.",
  run: "Choose a project first.",
  environment: "Choose a project first.",
});

const EMPTY = Object.freeze({
  project: "No projects yet.",
  agent: "This project has no agents.",
  candidate: "This agent has no candidates.",
  run: "No runs in this scope yet.",
  environment: "This project has no environments.",
});

// describe turns one record into what a selector shows: the name a reader
// recognises, a line of context, and the identifier. Only named fields are
// read, so a field /v1 adds later never reaches a selector by accident.
export function describe(level, row) {
  if (row === null || row === undefined) {
    return null;
  }
  switch (level) {
    case "project":
    case "agent":
      return { id: row.id, name: row.name || row.id, detail: "", row };
    case "candidate": {
      const metadata = row.metadata || {};
      const detail = [metadata.model, metadata.source_ref]
        .filter((part) => typeof part === "string" && part !== "").join(" · ");
      return { id: row.id, name: metadata.label || row.id, detail, row };
    }
    case "run": {
      const detail = [row.status, row.environment, row.created_at ? `created ${shortTime(row.created_at)}` : ""]
        .filter((part) => typeof part === "string" && part !== "").join(" · ");
      return { id: row.id, name: row.id, detail, row };
    }
    case "environment": {
      const rank = row.rank === undefined || row.rank === null ? "unranked" : `rank ${row.rank}`;
      const detail = [rank, row.status].filter((part) => typeof part === "string" && part !== "").join(" · ");
      return { id: row.ref, name: row.name || row.ref, detail, row: { ...row, id: row.ref } };
    }
    default:
      return null;
  }
}

// runScopeText states the scope and order a run list was read in (task 101).
// The list is newest first across the deepest chosen level, and saying which
// level is what keeps a project-wide list from reading as one candidate's.
export function runScopeText(scope) {
  if (!scope || scope.level === "") {
    return "";
  }
  return `Newest first in ${scope.level} ${scope.label}`;
}

function selectedFrom(level, entry) {
  if (entry === null || entry === undefined) {
    return null;
  }
  const described = describe(level, entry.row);
  const name = entry.label || (described ? described.name : "") || entry.id;
  return {
    id: entry.id,
    name,
    note: entry.preselected ? "Chosen for you: the only one." : "",
  };
}

// bindSelector connects one selector host to the context.
//
// options: {
//   level,            project | agent | candidate | run | environment
//   label, hint,
//   local,            true for a choice that stays in this form
//   input,            the form's own identifier field, kept in step
//   paste,            offer the selector's own paste path (context-bound only)
//   onChosen(option), after a choice; option is null when cleared
//   onError(error),   a paste the server refused
// }
export function bindSelector(host, context, options) {
  const level = options.level;
  const pageName = PAGE_FOR[level];
  let localChoice = null;
  // The identifier this binding last wrote into the form's field. The field is
  // only written when the context's choice changes, so a value pasted there —
  // or prefilled from the URL — survives every unrelated notification (a page
  // arriving, a different level changing).
  let lastPushed = null;

  const selector = createSelector(host, {
    label: options.label,
    hint: options.hint,
    kind: level,
    emptyText: EMPTY[level],
    onOpen: () => { void context.ensure(pageName); },
    onMore: () => { void context.loadMore(pageName); },
    onChoose: (option) => {
      if (options.local || level === "environment") {
        localChoice = option;
        render();
        if (typeof options.onChosen === "function") {
          options.onChosen(option);
        }
        return;
      }
      void context.choose(level, option.row);
      if (typeof options.onChosen === "function") {
        options.onChosen(option);
      }
    },
    onClear: () => {
      if (options.local || level === "environment") {
        localChoice = null;
        render();
      } else {
        context.clear(level);
      }
      if (typeof options.onChosen === "function") {
        options.onChosen(null);
      }
    },
    onPaste: options.paste && !options.local && level !== "environment"
      ? async (text) => {
        const result = await context.adopt(level, text);
        if (!result.ok && result.error && typeof options.onError === "function") {
          options.onError(result.error);
        }
      }
      : undefined,
  });

  function parentMissing() {
    const parentLevel = PARENT_OF[level];
    return parentLevel !== "" && context.selection[parentLevel] === null;
  }

  function currentChoice() {
    if (options.local || level === "environment") {
      return localChoice === null ? null : { id: localChoice.id, name: localChoice.name, note: "" };
    }
    return selectedFrom(level, context.selection[level]);
  }

  function render() {
    const page = context.pages[pageName];
    const missing = parentMissing();
    const chosen = currentChoice();
    selector.update({
      options: missing ? [] : page.rows.map((row) => describe(level, row)).filter((o) => o !== null && o.id),
      loading: page.loading,
      error: page.error ? (page.error.message || String(page.error)) : "",
      whole: page.loaded && page.nextAfter === "",
      hasMore: page.nextAfter !== "",
      capped: page.capped === true,
      disabled: missing,
      disabledText: NEEDS[level] || "",
      selected: chosen,
      scopeText: level === "run" ? runScopeText(context.runScope) : "",
    });
    if (options.input && !options.local) {
      const value = chosen === null ? "" : chosen.id;
      // Written on a change of choice only: to the new identifier, or cleared
      // when a choice this binding had written was itself cleared.
      if (value !== lastPushed) {
        if (value !== "" || lastPushed !== null) {
          options.input.value = value;
        }
        lastPushed = value === "" ? null : value;
      }
    }
  }

  // A local choice belongs to the parent it was listed under: a run chosen
  // for a promotion under one project is not a run of the next one.
  let parentSeen = parentKey();
  function parentKey() {
    const project = context.selection.project;
    return project === null ? "" : project.id;
  }

  context.subscribe(() => {
    const key = parentKey();
    if ((options.local || level === "environment") && key !== parentSeen) {
      localChoice = null;
      if (typeof options.onChosen === "function") {
        options.onChosen(null);
      }
    }
    parentSeen = key;
    render();
  });
  render();

  return Object.freeze({
    render,
    get choice() { return currentChoice(); },
    // setLocal records a local choice made elsewhere — a run assigned under
    // Compare is the natural default for the promotion form.
    setLocal(row) {
      localChoice = row === null ? null : describe(level === "environment" ? "environment" : level, row);
      render();
    },
    focus() { selector.focus(); },
  });
}
