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
// The pages each level's value is part of the parent key of — every one of
// them is dropped when the level changes. Runs appear under all three: the
// run list is the recency collection at the deepest chosen scope (task 101),
// so choosing an agent or a candidate changes which runs it holds.
const CHILD_PAGES = Object.freeze({
  project: ["agents", "environments", "runs"],
  agent: ["candidates", "runs"],
  candidate: ["runs"],
  run: [],
});

// The pages a fresh choice reads straight away, because preselection needs
// them or every form on the project does. Runs at project or agent scope are
// read when something asks for them — a selector opening, a destination being
// shown — so choosing a project and then an agent costs no run read that the
// next choice would throw away.
const EAGER_PAGES = Object.freeze({
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
  runs: "project",
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
    readAt: 0,
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

  const idOf = (level) => (selection[level] === null ? "" : selection[level].id);

  // parentIDOf is the key a page is listed under. For runs it is the whole
  // scope — project, agent and candidate — because the recency collection is
  // read at the deepest of them, and a page read for the project is not the
  // agent's page.
  const parentIDOf = (pageName) => {
    if (pageName === "runs") {
      return idOf("project") === ""
        ? ""
        : [idOf("project"), idOf("agent"), idOf("candidate")].join("\u0000");
    }
    const parentLevel = PARENT_OF[pageName];
    if (parentLevel === "") {
      return "";
    }
    return idOf(parentLevel);
  };

  // runScope says which scope the run page is read at, for labels: the level
  // and its name.
  const runScope = () => {
    for (const level of ["candidate", "agent", "project"]) {
      if (selection[level] !== null) {
        return { level, label: selection[level].label || selection[level].id };
      }
    }
    return { level: "", label: "" };
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
        return deps.listRecentRuns(idOf("project"), {
          agentID: idOf("agent"),
          candidateID: idOf("candidate"),
        }, after);
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
        // When this page was read, so a summary built on it can say how old
        // it is. Wall-clock, from the injected clock where a test supplies one.
        readAt: typeof deps.now === "function" ? deps.now() : Date.now(),
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
    const children = EAGER_PAGES[level];
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
    if (only !== null && page.parentID === parentIDOf(PAGE_FOR[childLevel])) {
      // Through set(), like any choice: the pages listed under the previous
      // scope — the run page above all — are dropped and their reads
      // abandoned. Assigning the selection directly left a project-wide run
      // read in flight to land as though it described the agent.
      set(childLevel, only, true);
      await descend(childLevel);
    }
  }

  // set records a selection and clears what depended on the previous one.
  // Re-choosing the current value is not a change and clears nothing.
  function set(level, row, preselected) {
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
      : { id, label: labelFor(level, row), row, preselected: preselected === true };
    notify(level);
    return true;
  }

  // While a choice is still preselecting below itself, the run scope is not
  // settled: a run read now would be for a scope the next preselection
  // replaces. Such reads are deferred, and made once the choice settles if
  // nothing has read them meanwhile.
  let settling = 0;
  const deferred = new Set();

  async function settle(work) {
    settling += 1;
    try {
      return await work();
    } finally {
      settling -= 1;
      if (settling === 0) {
        for (const pageName of Array.from(deferred)) {
          deferred.delete(pageName);
          void api.ensure(pageName);
        }
      }
    }
  }

  const api = {
    get selection() { return selection; },
    get pages() { return pages; },
    // The scope the run page is read at: { level, label }.
    get runScope() { return runScope(); },
    // The key the run page must carry to describe the current scope.
    get runsKey() { return parentIDOf("runs"); },

    // choose is a reader's choice: record it, then read what it opens up.
    //
    // A run chosen from a project- or agent-wide list belongs to a candidate
    // the context may not hold yet. Its parents are resolved from the record
    // (adopt), so the context stays a chain that exists rather than a run
    // filed under the wrong candidate.
    async choose(level, row) {
      if (level === "run" && row && typeof row.candidate_id === "string"
        && row.candidate_id !== idOf("candidate")) {
        const result = await api.adopt("run", row.id);
        return result.ok;
      }
      // The whole choice settles, set() included: set() notifies, and a view
      // that asks for runs from that notification must wait for the
      // preselection below to finish rather than read a scope it replaces.
      return settle(async () => {
        const changed = set(level, row);
        if (changed && selection[level] !== null) {
          await descend(level);
        }
        return changed;
      });
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
      // Held or in flight *for this scope*. A read in flight for another
      // scope does not count: this one supersedes it.
      if ((page.loading || page.loaded) && page.parentID === parentID) {
        return false;
      }
      // While a choice settles, the choice itself reads what it opens up;
      // a view asking from its subscriber waits, and is served once the
      // choice settles if the page is still not held.
      if (settling > 0) {
        deferred.add(pageName);
        return false;
      }
      // A page that failed for this parent stays failed until somebody asks
      // again (refresh): views call ensure() from their subscribers, and an
      // automatic retry there would turn a persistent error into a loop.
      if (page.error !== null && page.parentID === parentID) {
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
            // Once per page and key: runs are a child of every level, and a
            // second read of the same page would supersede the first.
            const page = pages[pageName];
            const current = page.parentID === parentIDOf(pageName) && (page.loaded || page.loading);
            if (!current) {
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
        readAt: typeof deps.now === "function" ? deps.now() : Date.now(),
      };
      notify("projects");
    },

    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
  return api;
}
