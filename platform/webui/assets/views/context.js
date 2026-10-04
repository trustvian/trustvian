// The selection context: which project, agent, candidate and run the console
// is about, and one shared page of options for each (task 098).
//
// Every destination that needs one of these used to keep its own copy, typed
// into a field or filled from whichever view was opened last. This holds the
// one copy and the rule that makes it safe to share: **a selection belongs to
// its parent.** Choosing a different project clears the agent, the candidate,
// the run and every option page below it, and abandons their reads; a
// response for a parent the reader has left is discarded, and it cannot lower
// the loading flag a newer read raised.
//
// No DOM. Requests are injected, so the races can be driven in a test with
// promises resolved out of order. Each level reads one bounded page of a
// collection route per action; nothing here follows a cursor by itself.

import { createSurface } from "../core/ownership.js";

// LEVELS in dependency order. A level's dependents are every level after it.
export const LEVELS = Object.freeze(["project", "agent", "candidate", "run"]);

// The option page each selection level is chosen from, and the page that
// becomes available once it is chosen. Environments hang off the project and
// have no selection of their own here: a form chooses one locally.
const PAGE_FOR = Object.freeze({
  project: "projects",
  agent: "agents",
  candidate: "candidates",
  run: "runs",
});
const CHILD_PAGES = Object.freeze({
  project: ["agents", "environments"],
  agent: ["candidates"],
  candidate: ["runs"],
  run: [],
});
export const PAGES = Object.freeze(["projects", "agents", "candidates", "runs", "environments"]);

// MAX_OPTION_ROWS bounds what a selector accumulates. Continuations append,
// because a list being chosen from is only useful whole, so the bound is on
// the total: eight pages of 64. Past it the selector says so and the paste
// path is the way to a record further along.
export const MAX_OPTION_ROWS = 512;

// The selection level whose value a page is listed under.
const PARENT_OF = Object.freeze({
  projects: "",
  agents: "project",
  candidates: "agent",
  runs: "candidate",
  environments: "project",
});

function emptyPage() {
  return {
    parentID: "",
    rows: [],
    nextAfter: "",
    loaded: false,
    loading: false,
    error: null,
    whole: false,
    capped: false,
  };
}

// labelFor names a row the way every selector shows it.
//
// The name the producer chose, never one invented here; the identifier when
// there is no name. A candidate's name is its metadata label.
export function labelFor(level, row) {
  if (row === null || row === undefined) {
    return "";
  }
  if (level === "candidate" && row.metadata && typeof row.metadata.label === "string"
    && row.metadata.label !== "") {
    return row.metadata.label;
  }
  if (typeof row.name === "string" && row.name !== "") {
    return row.name;
  }
  return typeof row.id === "string" ? row.id : "";
}

// unambiguousSingle returns the one row of a page that is the whole
// collection and holds exactly one entry, or null.
//
// A first page with a continuation is never "the only one", however few rows
// it carries: the next page may hold the one the reader wanted.
export function unambiguousSingle(page) {
  if (!page.loaded || page.nextAfter !== "" || page.rows.length !== 1) {
    return null;
  }
  return page.rows[0];
}

export function createSelectionContext(deps) {
  const selection = { project: null, agent: null, candidate: null, run: null };
  const pages = {};
  const surfaces = {};
  for (const name of PAGES) {
    pages[name] = emptyPage();
    surfaces[name] = createSurface(`context.${name}`);
  }
  const adoptSurface = createSurface("context.adopt");
  const listeners = new Set();

  const notify = (what) => {
    for (const listener of listeners) {
      listener(what);
    }
  };

  const parentIDOf = (pageName) => {
    const parentLevel = PARENT_OF[pageName];
    if (parentLevel === "") {
      return "";
    }
    const parent = selection[parentLevel];
    return parent === null ? "" : parent.id;
  };

  // resetPage empties a page and abandons its reads, because its parent is
  // no longer the one it was listed under.
  const resetPage = (pageName) => {
    pages[pageName] = emptyPage();
    surfaces[pageName].retarget(parentIDOf(pageName));
  };

  // clearBelow drops every selection after `level`, and every page listed
  // under any of them or under `level` itself.
  const clearBelow = (level) => {
    const index = LEVELS.indexOf(level);
    for (const dependent of LEVELS.slice(index + 1)) {
      selection[dependent] = null;
    }
    for (const owner of LEVELS.slice(index)) {
      for (const pageName of CHILD_PAGES[owner]) {
        resetPage(pageName);
      }
    }
  };

  const fetchPage = (pageName, parentID, after) => {
    switch (pageName) {
      case "projects":
        return deps.listProjects(after);
      case "agents":
        return deps.listProjectAgents(parentID, after);
      case "candidates":
        return deps.listAgentCandidates(parentID, after);
      case "runs":
        return deps.listCandidateRuns(parentID, after);
      case "environments":
        // The whole collection: task 065's cap governs creation, not
        // existence, and api.js owns the bounded traversal.
        return deps.listAllEnvironments(parentID);
      default:
        return Promise.reject(new Error(`unknown page ${pageName}`));
    }
  };

  const rowsOf = (pageName, response) => {
    const key = {
      projects: "projects",
      agents: "agents",
      candidates: "candidates",
      runs: "evaluation_runs",
      environments: "environments",
    }[pageName];
    const rows = response && Array.isArray(response[key]) ? response[key] : [];
    return rows;
  };

  // load reads one page for the page's current parent. `after` empty is a
  // fresh first page, which replaces; a continuation appends, because a
  // selector's options are a list the reader is choosing from, bounded by
  // the pages they asked for.
  async function load(pageName, after) {
    const parentID = parentIDOf(pageName);
    if (PARENT_OF[pageName] !== "" && parentID === "") {
      return false;
    }
    const ticket = surfaces[pageName].begin(parentID);
    pages[pageName] = {
      ...pages[pageName],
      parentID,
      loading: true,
      error: null,
    };
    notify(pageName);
    try {
      const response = await fetchPage(pageName, parentID, after);
      if (!surfaces[pageName].owns(ticket)) {
        return false;
      }
      const rows = rowsOf(pageName, response);
      const nextAfter = pageName !== "environments" && typeof response.next_after === "string"
        ? response.next_after
        : "";
      const previous = after === "" ? [] : pages[pageName].rows;
      pages[pageName] = {
        parentID,
        rows: previous.concat(rows),
        nextAfter,
        loaded: true,
        loading: false,
        error: null,
        whole: nextAfter === "",
        capped: nextAfter !== "" && previous.length + rows.length >= MAX_OPTION_ROWS,
      };
      notify(pageName);
      return true;
    } catch (error) {
      if (!surfaces[pageName].owns(ticket)) {
        return false;
      }
      pages[pageName] = { ...pages[pageName], loading: false, error };
      notify(pageName);
      return false;
    }
  }

  // descend reads the pages a fresh selection makes available and, where a
  // page turns out to be a whole collection of one, selects it — and keeps
  // going. Bounded by the depth of the hierarchy, and every read in it was
  // caused by the reader's choice.
  async function descend(level) {
    const children = CHILD_PAGES[level];
    await Promise.all(children.map((pageName) => load(pageName, "")));
    const childLevel = LEVELS[LEVELS.indexOf(level) + 1];
    if (childLevel === undefined) {
      return;
    }
    const page = pages[PAGE_FOR[childLevel]];
    if (selection[childLevel] !== null) {
      return;
    }
    const only = unambiguousSingle(page);
    if (only !== null && page.parentID === (selection[level] ? selection[level].id : "")) {
      selection[childLevel] = { id: only.id, label: labelFor(childLevel, only), row: only, preselected: true };
      notify(childLevel);
      await descend(childLevel);
    }
  }

  // set records a selection and clears what depended on the previous one.
  // Re-choosing the current value is not a change and clears nothing.
  function set(level, row) {
    const current = selection[level];
    const id = row === null || row === undefined ? "" : row.id;
    if ((current === null && id === "") || (current !== null && current.id === id)) {
      if (current !== null && row) {
        // Same identity, possibly a fuller row (a name arriving after a paste).
        selection[level] = { ...current, label: labelFor(level, row) || current.label, row };
        notify(level);
      }
      return false;
    }
    adoptSurface.retarget(`${level}\u0000${id}`);
    clearBelow(level);
    selection[level] = id === ""
      ? null
      : { id, label: labelFor(level, row), row, preselected: false };
    notify(level);
    return true;
  }

  return {
    get selection() { return selection; },
    get pages() { return pages; },

    // choose is a reader's choice: record it, then read what it opens up.
    async choose(level, row) {
      const changed = set(level, row);
      if (changed && selection[level] !== null) {
        await descend(level);
      }
      return changed;
    },

    // assume records a selection the reader made somewhere that already
    // read what it needed — opening a run row, say — without reading more.
    assume(level, row) {
      return set(level, row);
    },

    clear(level) {
      set(level, null);
    },

    // ensure reads a page's first page if nothing is held for its current
    // parent. Called when a selector is opened, so a list nobody looks at is
    // never fetched.
    async ensure(pageName) {
      const page = pages[pageName];
      const parentID = parentIDOf(pageName);
      if (page.loading || (page.loaded && page.parentID === parentID)) {
        return false;
      }
      return load(pageName, "");
    },

    async loadMore(pageName) {
      const page = pages[pageName];
      if (page.loading || page.nextAfter === "" || page.rows.length >= MAX_OPTION_ROWS) {
        return false;
      }
      return load(pageName, page.nextAfter);
    },

    // refresh re-reads a page from its start, replacing what is held.
    async refresh(pageName) {
      return load(pageName, "");
    },

    // adopt is the paste path. An identifier from a log names one record;
    // its parents are read from the record itself, so the context is always
    // a chain that exists rather than a run filed under the wrong candidate.
    // Bounded: at most one read per level above the pasted one.
    async adopt(level, id) {
      const trimmed = typeof id === "string" ? id.trim() : "";
      if (trimmed === "" || !LEVELS.includes(level)) {
        return { ok: false, error: null };
      }
      const ticket = adoptSurface.begin(`${level}\u0000${trimmed}`);
      try {
        const chain = {};
        let cursorLevel = level;
        let cursorID = trimmed;
        while (cursorLevel !== undefined) {
          const record = await ({
            run: deps.getRun,
            candidate: deps.getCandidate,
            agent: deps.getAgent,
            project: deps.getProject,
          })[cursorLevel](cursorID);
          if (!adoptSurface.owns(ticket)) {
            return { ok: false, error: null, stale: true };
          }
          chain[cursorLevel] = record;
          const parentField = { run: "candidate_id", candidate: "agent_id", agent: "project_id", project: "" }[cursorLevel];
          if (parentField === "") {
            break;
          }
          cursorID = record[parentField];
          cursorLevel = LEVELS[LEVELS.indexOf(cursorLevel) - 1];
        }
        // Applied top-down, so each level is set under the parent it belongs
        // to and set() clears anything that does not.
        for (const each of LEVELS) {
          if (chain[each] !== undefined) {
            set(each, chain[each]);
          }
          if (each === level) {
            break;
          }
        }
        for (const each of LEVELS.slice(0, LEVELS.indexOf(level) + 1)) {
          for (const pageName of CHILD_PAGES[each]) {
            if (!pages[pageName].loaded || pages[pageName].parentID !== parentIDOf(pageName)) {
              void load(pageName, "");
            }
          }
        }
        return { ok: true, error: null };
      } catch (error) {
        if (!adoptSurface.owns(ticket)) {
          return { ok: false, error: null, stale: true };
        }
        return { ok: false, error };
      }
    },

    // offerProjects shares a projects page another surface already read —
    // the startup snapshot — so opening the project selector costs nothing.
    offerProjects(level) {
      if (level === null || level === undefined || !level.loaded) {
        return;
      }
      if (pages.projects.loading) {
        return;
      }
      pages.projects = {
        parentID: "",
        rows: level.rows.slice(),
        nextAfter: level.nextAfter,
        loaded: true,
        loading: false,
        error: null,
        whole: level.nextAfter === "",
        capped: false,
      };
      notify("projects");
    },

    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}
