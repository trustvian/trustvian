// Bounded durable discovery, and the names the live surfaces borrow from it.
//
// Two jobs that share one cache, because they are the same reads:
//
//   1. Browsing the durable hierarchy — Projects → Agents → Candidates → Runs
//      — one bounded page per user action. This is what a reload with no live
//      traffic falls back on, and it is the reason browser storage is
//      unnecessary rather than merely discouraged.
//
//   2. Turning identifiers into names for the live surfaces. A card says
//      "support-agent" rather than an opaque string when a name is known, and
//      the identifier when it is not. Rendering never waits for a name.
//
// The startup budget is one page of GET /v1/projects and nothing else. A
// bounded route does not make a bounded workflow: 64 projects × 64 agents × 64
// candidates × 64 runs is sixteen million rows across a quarter of a million
// requests, during which the resync buffer holds 64 frames and overflows into
// a resync loop that worsens with database size.

// MAX_LABEL_CACHE bounds the name cache.
//
// A cache is unbounded memory with a helpful name unless something evicts it.
// 256 is far above the sixteen scopes the rail can show and far below anything
// that matters, and eviction is least-recently-used.
export const MAX_LABEL_CACHE = 256;

// LabelCache maps an identifier to a display name, and remembers what it has
// already asked for so a name is never fetched twice.
//
// Critically, it is asked *per scope*, not per observation: a busy agent
// produces many frames for one scope, and a lookup per frame would be a
// request storm caused by watching. resolve() is a no-op for an identifier
// already known or already in flight.
export class LabelCache {
  constructor(deps, capacity = MAX_LABEL_CACHE) {
    this.deps = deps;
    this.capacity = capacity;
    this.names = new Map();
    this.inFlight = new Set();
    this.onChange = () => {};
  }

  // labelFor returns the known name, or "" when none is resolved.
  //
  // Never a promise and never a placeholder that later mutates in place:
  // callers render what is known now and re-render when onChange fires.
  labelFor(kind, id) {
    if (!id) {
      return "";
    }
    const key = `${kind}:${id}`;
    const name = this.names.get(key);
    if (name === undefined) {
      return "";
    }
    // Touch for LRU.
    this.names.delete(key);
    this.names.set(key, name);
    return name;
  }

  remember(kind, id, name) {
    if (!id || !name) {
      return;
    }
    const key = `${kind}:${id}`;
    this.names.delete(key);
    this.names.set(key, name);
    while (this.names.size > this.capacity) {
      this.names.delete(this.names.keys().next().value);
    }
  }

  // resolveAgent reads one agent's authoritative detail, once.
  //
  // One bounded by-id read per newly seen agent — not a walk, and not per
  // event. A failure is swallowed on purpose: a name is a convenience, and a
  // card that rendered an identifier because a lookup failed is still correct
  // and still useful. Surfacing it would put an error banner on the page every
  // time an agent was deleted.
  async resolveAgent(agentID) {
    if (!agentID) {
      return;
    }
    const key = `agent:${agentID}`;
    if (this.names.has(key) || this.inFlight.has(key)) {
      return;
    }
    this.inFlight.add(key);
    try {
      const agent = await this.deps.getAgent(agentID);
      if (agent && typeof agent.name === "string" && agent.name !== "") {
        this.remember("agent", agentID, agent.name);
        this.onChange();
      }
    } catch (ignored) {
      // The identifier remains the label. Nothing is broken.
    } finally {
      this.inFlight.delete(key);
    }
  }
}

// HierarchyBrowser holds one bounded page per level and the cursor after it.
//
// Each level is replaced rather than accumulated, so browsing a large
// hierarchy costs one page of memory per level however far somebody walks.
export class HierarchyBrowser {
  constructor(deps) {
    this.deps = deps;
    this.levels = {
      projects: emptyLevel(),
      agents: emptyLevel(),
      candidates: emptyLevel(),
      runs: emptyLevel(),
    };
    this.parents = { agents: "", candidates: "", runs: "" };
  }

  // loadProjects reads one page of the root. This is the whole automatic
  // startup budget, and it is also what the reconnect snapshot performs.
  async loadProjects(after) {
    const response = await this.deps.listProjects(after);
    this.levels.projects = levelFrom(response, "projects");
    // Descending resets what is below, so a stale child list can never appear
    // to belong to a parent somebody has since moved away from.
    this.levels.agents = emptyLevel();
    this.levels.candidates = emptyLevel();
    this.levels.runs = emptyLevel();
    this.parents = { agents: "", candidates: "", runs: "" };
    return this.levels.projects;
  }

  async loadAgents(projectID, after) {
    const response = await this.deps.listProjectAgents(projectID, after);
    this.levels.agents = levelFrom(response, "agents");
    this.parents.agents = projectID;
    this.levels.candidates = emptyLevel();
    this.levels.runs = emptyLevel();
    this.parents.candidates = "";
    this.parents.runs = "";
    return this.levels.agents;
  }

  async loadCandidates(agentID, after) {
    const response = await this.deps.listAgentCandidates(agentID, after);
    this.levels.candidates = levelFrom(response, "candidates");
    this.parents.candidates = agentID;
    this.levels.runs = emptyLevel();
    this.parents.runs = "";
    return this.levels.candidates;
  }

  async loadRuns(candidateID, after) {
    const response = await this.deps.listCandidateRuns(candidateID, after);
    this.levels.runs = levelFrom(response, "evaluation_runs");
    this.parents.runs = candidateID;
    return this.levels.runs;
  }
}

function emptyLevel() {
  return { rows: [], nextAfter: "", loaded: false };
}

function levelFrom(response, key) {
  return {
    rows: Array.isArray(response[key]) ? response[key] : [],
    nextAfter: typeof response.next_after === "string" ? response.next_after : "",
    loaded: true,
  };
}

// renderLevel draws one bounded page as a list of selectable rows.
//
// Every row costs exactly one request when pressed, and the continuation is an
// explicit affordance rather than something followed automatically. A page is
// never implied to be the whole level: where more exists, the footer says so
// in words.
export function renderLevel(host, spec) {
  host.replaceChildren();

  const heading = document.createElement("h4");
  heading.className = "level-title";
  heading.textContent = spec.title;
  host.append(heading);

  if (!spec.level.loaded) {
    const hint = document.createElement("p");
    hint.className = "empty";
    hint.textContent = spec.pendingMessage;
    host.append(hint);
    return;
  }

  if (spec.level.rows.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = spec.emptyMessage;
    host.append(empty);
    return;
  }

  const list = document.createElement("ul");
  list.className = "level";
  for (const row of spec.level.rows) {
    const item = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "level-row";
    if (spec.selected && spec.selected === row.id) {
      button.classList.add("level-row-selected");
      button.setAttribute("aria-current", "true");
    }

    const name = document.createElement("span");
    name.className = "level-name";
    name.textContent = spec.labelOf(row);
    button.append(name);

    const detail = spec.detailOf(row);
    if (detail !== "") {
      const sub = document.createElement("span");
      sub.className = "level-detail";
      sub.textContent = detail;
      button.append(sub);
    }

    button.setAttribute("aria-label", `${spec.openLabel} ${spec.labelOf(row)}`);
    button.addEventListener("click", () => spec.onOpen(row));
    item.append(button);
    list.append(item);
  }
  host.append(list);

  const footer = document.createElement("p");
  footer.className = "note-inline";
  if (spec.level.nextAfter) {
    footer.textContent = `Showing ${spec.level.rows.length}. More exist.`;
    host.append(footer);
    const more = document.createElement("button");
    more.type = "button";
    more.className = "link-button";
    more.textContent = "Load more";
    more.setAttribute("aria-label", `Load more ${spec.title.toLowerCase()}`);
    more.addEventListener("click", () => spec.onMore(spec.level.nextAfter));
    host.append(more);
    return;
  }
  footer.textContent = `Showing all ${spec.level.rows.length}.`;
  host.append(footer);
}

// renderOptions fills a <select> from a discovered collection.
//
// The point of this is that a developer picks a project they can see rather
// than copying an identifier out of a log. The identifier remains the value —
// the server's contract is unchanged — but it stops being what a person has
// to know.
export function renderOptions(select, rows, spec) {
  const previous = select.value;
  select.replaceChildren();

  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = rows.length === 0 ? spec.emptyLabel : spec.placeholder;
  select.append(placeholder);

  for (const row of rows) {
    if (typeof row.id !== "string" || row.id === "") {
      continue;
    }
    const option = document.createElement("option");
    option.value = row.id;
    const detail = spec.detailOf ? spec.detailOf(row) : "";
    option.textContent = detail === "" ? row.id : `${spec.labelOf(row)} — ${detail}`;
    select.append(option);
  }
  // A refreshed list must not silently change what was chosen.
  if (previous !== "" && Array.from(select.options).some((o) => o.value === previous)) {
    select.value = previous;
  }
}
