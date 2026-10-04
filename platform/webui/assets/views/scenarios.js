// Evidence → Scenarios (task 102): recorded scenario executions, found
// without copying an identifier out of a CI log.
//
// Discovery, inspection and a reference check — nothing executes here. A
// scenario runs a developer's command, and doing that from a page would make
// the local control plane an executor; the browser hands over the CLI command
// to copy instead.
//
// Whether an execution can be reused as a reference is the control plane's
// answer (`/reference-check`, which runs the same validation `eval run
// --reference` does). The page renders that answer and its stated conditions,
// and never derives eligibility from a status it can see.

import { element, clear } from "../core/dom.js";
import { createSurface } from "../core/ownership.js";
import { shortTime } from "../core/format.js";
import { detailGroup, identChip } from "../ui/dashboard.js";
import { emptyState, inlineEmpty, skeleton, notify } from "../ui/feedback.js";
import { icon } from "../ui/icons.js";

const MAX_EXECUTION_ROWS = 512;
const NOT_AVAILABLE = "not available";

// shellQuote makes a value safe to paste into a POSIX shell. Identifiers are
// caller-chosen and may contain anything printable; a plain one is left bare
// so the common command reads naturally.
export function shellQuote(value) {
  const text = String(value);
  if (/^[A-Za-z0-9._:@/+=-]+$/.test(text)) {
    return text;
  }
  return `'${text.replace(/'/g, "'\\''")}'`;
}

// referenceCommand is the CLI invocation that reuses an execution as the
// reference side. The scenario file is the developer's and is left as a
// placeholder: the platform records a scenario's name, not its file.
//
// `--reference=<id>` rather than `--reference <id>`: an identifier that begins
// with a dash is then a value, never a flag.
export function referenceCommand(executionID) {
  return `trustvian eval run --scenario <scenario.yaml> --reference=${shellQuote(executionID)}`;
}

// executionMatches is the search box: over the loaded rows only.
export function executionMatches(row, query) {
  const needle = String(query || "").trim().toLowerCase();
  if (needle === "") {
    return true;
  }
  return [row.id, row.scenario_name, row.environment, row.agent_id, row.status]
    .some((part) => typeof part === "string" && part.toLowerCase().includes(needle));
}

async function copy(text, what) {
  try {
    await navigator.clipboard.writeText(text);
    notify(`Copied ${what}.`, "done");
  } catch (ignored) {
    notify(`Could not copy; the ${what} is shown on the page.`, "fail");
  }
}

export function createScenarios(deps) {
  const { selection, hosts, nav } = deps;
  const listSurface = createSurface("scenarios.list");
  const detailSurface = createSurface("scenarios.detail");
  const checkSurface = createSurface("scenarios.check");

  let visible = false;
  let key = "";
  let filterProject = "";
  let environment = "";
  let query = "";
  let list = emptyList();
  let selectedID = "";
  let detail = { id: "", execution: null, loading: false, error: null };
  let check = { id: "", answer: null, loading: false, error: null };

  function emptyList() {
    return { rows: [], nextAfter: "", loaded: false, loading: false, error: null, readAt: 0 };
  }

  const projectID = () => (selection.selection.project === null ? "" : selection.selection.project.id);
  const agentID = () => (selection.selection.agent === null ? "" : selection.selection.agent.id);
  const filterKey = () => [projectID(), agentID(), environment].join("\u0000");

  // ---------------------------------------------------------------- reads

  async function loadList(after) {
    if (projectID() === "") {
      return;
    }
    const ticket = listSurface.begin(filterKey());
    list = { ...list, loading: true, error: null };
    drawList();
    try {
      const response = await deps.listExecutions(projectID(), { agentID: agentID(), environment }, after);
      if (!listSurface.owns(ticket)) {
        return;
      }
      const rows = Array.isArray(response.executions) ? response.executions : [];
      const merged = (after === "" ? [] : list.rows).concat(rows);
      list = {
        rows: merged,
        nextAfter: typeof response.next_after === "string" ? response.next_after : "",
        loaded: true,
        loading: false,
        error: null,
        readAt: deps.now(),
      };
    } catch (error) {
      if (!listSurface.owns(ticket)) {
        return;
      }
      list = { ...list, loading: false, error };
    }
    drawList();
  }

  async function openDetail(id) {
    selectedID = id;
    const ticket = detailSurface.begin(id);
    checkSurface.retarget(id);
    check = { id, answer: null, loading: false, error: null };
    detail = { id, execution: null, loading: true, error: null };
    markSelected();
    showPanel();
    try {
      const response = await deps.getExecution(id);
      if (!detailSurface.owns(ticket)) {
        return;
      }
      detail = { id, execution: response.execution || null, loading: false, error: null };
    } catch (error) {
      if (!detailSurface.owns(ticket)) {
        return;
      }
      detail = { id, execution: null, loading: false, error };
    }
    drawPanel();
  }

  async function runCheck(id) {
    const ticket = checkSurface.begin(id);
    check = { id, answer: null, loading: true, error: null };
    drawPanel();
    try {
      const answer = await deps.checkReference(id);
      if (!checkSurface.owns(ticket)) {
        return;
      }
      check = { id, answer, loading: false, error: null };
    } catch (error) {
      if (!checkSurface.owns(ticket)) {
        return;
      }
      check = { id, answer: null, loading: false, error };
    }
    drawPanel();
  }

  // ----------------------------------------------------------------- draw

  function drawFilters() {
    const host = hosts.filters;
    clear(host);
    const scope = element("p", "region-note");
    if (projectID() === "") {
      scope.append(document.createTextNode("Choose a project — in any destination — to list its recorded executions."));
      host.append(scope);
      return;
    }
    const project = selection.selection.project.label || projectID();
    const agent = selection.selection.agent;
    scope.append(document.createTextNode(agent === null
      ? `Executions of every agent in ${project}, newest first by start time.`
      : `Executions of agent ${agent.label || agent.id} in ${project}, newest first by start time.`));
    host.append(scope);

    const row = element("div", "toolbar");
    const envLabel = element("label", "field");
    envLabel.append(element("span", null, "Environment"));
    const envSelect = element("select");
    envSelect.id = "scenarios-environment";
    const any = element("option", null, "Any environment");
    any.value = "";
    envSelect.append(any);
    const envPage = selection.pages.environments;
    for (const env of envPage.rows) {
      const option = element("option", null, env.name ? `${env.name} (${env.ref})` : env.ref);
      option.value = env.ref;
      envSelect.append(option);
    }
    envSelect.value = environment;
    envSelect.addEventListener("change", () => {
      environment = envSelect.value;
      retarget();
      void loadList("");
    });
    envLabel.append(envSelect);
    row.append(envLabel);

    const search = element("label", "search");
    search.append(icon("search"));
    search.append(element("span", "search-label", "Filter loaded executions"));
    const input = element("input");
    input.type = "search";
    input.placeholder = "Scenario, status or ID";
    input.autocomplete = "off";
    input.value = query;
    input.addEventListener("input", () => {
      query = input.value;
      drawRows();
    });
    search.append(input);
    row.append(search);
    host.append(row);
  }

  function drawList() {
    drawRows();
    const pager = hosts.pager;
    clear(pager);
    pager.hidden = !(list.nextAfter !== "" && list.rows.length < MAX_EXECUTION_ROWS);
    if (!pager.hidden) {
      const more = element("button", null, list.loading ? "Loading…" : "Load next page");
      more.type = "button";
      more.disabled = list.loading;
      more.addEventListener("click", () => { void loadList(list.nextAfter); });
      pager.append(more);
    }
  }

  function drawRows() {
    const host = hosts.table;
    clear(host);
    if (projectID() === "") {
      host.append(inlineEmpty("No project chosen."));
      return;
    }
    if (list.loading && !list.loaded) {
      skeleton(host, 4, 5);
      return;
    }
    if (list.error) {
      const fail = element("div", "result");
      fail.append(element("p", "err-msg", list.error.message || String(list.error)));
      host.append(fail);
      return;
    }
    if (list.rows.length === 0) {
      host.append(emptyState("No recorded executions",
        "Every trustvian eval run against this control plane is recorded here as it starts.", "runs"));
      return;
    }
    const shown = list.rows.filter((row) => executionMatches(row, query));
    const status = element("p", "toolbar-note table-note");
    const scope = list.nextAfter === "" ? `${list.rows.length} in all` : `${list.rows.length} loaded; more exist`;
    status.textContent = `${query.trim() === "" ? scope : `${shown.length} of ${scope} match`} · read at ${
      new Date(list.readAt).toLocaleTimeString()}`;
    host.append(status);

    const table = element("table", "data-table");
    const head = element("thead");
    const headRow = element("tr");
    for (const label of ["Scenario", "Status", "Verdict", "Runs", "Environment", "Agent", "Started", "Reused"]) {
      headRow.append(element("th", null, label));
    }
    head.append(headRow);
    table.append(head);
    const body = element("tbody");
    for (const row of shown) {
      const tr = element("tr", "row-open");
      tr.dataset.executionId = row.id;
      if (row.id === selectedID) {
        tr.classList.add("row-selected");
      }
      const first = element("td");
      const open = element("button", "ident-chip", row.scenario_name || row.id);
      open.type = "button";
      open.setAttribute("aria-label", `Open execution ${row.id} of scenario ${row.scenario_name}`);
      open.addEventListener("click", (event) => {
        event.stopPropagation();
        void openDetail(row.id);
      });
      first.append(open);
      first.append(element("span", "exec-id", row.id));
      tr.append(first);
      tr.append(element("td", `exec-status status-${String(row.status).replace(/[^a-z]/g, "")}`, row.status || NOT_AVAILABLE));
      tr.append(element("td", null, row.verdict || (row.status === "completed" ? NOT_AVAILABLE : "—")));
      tr.append(element("td", "ident", String(row.runs)));
      tr.append(element("td", null, row.environment));
      tr.append(element("td", "ident", row.agent_id));
      tr.append(element("td", "ident", shortTime(row.started_at)));
      tr.append(element("td", "ident", row.reference_execution_id || "—"));
      tr.addEventListener("click", () => { void openDetail(row.id); });
      body.append(tr);
    }
    table.append(body);
    host.append(table);
    if (shown.length === 0) {
      host.append(inlineEmpty("No loaded execution matches. The filter is over loaded executions only."));
    }
  }

  function markSelected() {
    for (const tr of hosts.table.querySelectorAll("tr[data-execution-id]")) {
      tr.classList.toggle("row-selected", tr.dataset.executionId === selectedID);
    }
  }

  function showPanel() {
    hosts.panel.hidden = false;
    hosts.layout.classList.add("has-detail");
    drawPanel();
  }

  function closePanel(returnFocus) {
    if (hosts.panel.hidden) {
      return;
    }
    hosts.panel.hidden = true;
    hosts.layout.classList.remove("has-detail");
    clear(hosts.panelBody);
    if (returnFocus) {
      const row = hosts.table.querySelector(`tr[data-execution-id="${CSS.escape(selectedID)}"] .ident-chip`);
      if (row !== null) {
        row.focus();
      }
    }
  }

  function drawPanel() {
    if (hosts.panel.hidden) {
      return;
    }
    const body = hosts.panelBody;
    clear(body);
    hosts.panelTitle.textContent = `Execution ${detail.id}`;
    if (detail.loading) {
      skeleton(body, 5, 2);
      return;
    }
    if (detail.error) {
      const fail = element("div", "result");
      fail.append(element("p", "err-msg", detail.error.message || String(detail.error)));
      body.append(fail);
      return;
    }
    const e = detail.execution;
    if (e === null) {
      return;
    }
    body.append(detailGroup("Scenario", [
      ["Scenario", e.scenario_name || NOT_AVAILABLE],
      ["Status", e.status || NOT_AVAILABLE],
      ["Verdict", e.verdict || (e.status === "completed" ? NOT_AVAILABLE : "not completed")],
      ["Repetitions per side", String(e.runs)],
      ["Reused reference of", e.reference_execution_id || "ran its own reference side"],
    ]));
    body.append(detailGroup("Scope", [
      ["Project", e.project_id],
      ["Agent", e.agent_id],
      ["Environment", e.environment],
    ]));
    body.append(detailGroup("Time", [
      ["Started", e.started_at || NOT_AVAILABLE],
      ["Finished", e.finished_at || "not finished"],
      ["Completion order", e.completion_sequence ? `#${e.completion_sequence} in the project` : "not completed"],
    ]));

    const reps = Array.isArray(e.repetitions) ? e.repetitions : [];
    const repGroup = element("div", "detail-group");
    repGroup.append(element("p", "detail-group-title", "Repetitions"));
    if (reps.length === 0) {
      repGroup.append(inlineEmpty(e.status === "completed"
        ? "No repetitions recorded."
        : "Repetitions are recorded when an execution completes."));
    } else {
      const list = element("div", "detail-refs");
      for (const rep of reps) {
        const line = element("div", "detail-ref");
        line.append(element("span", "detail-ref-key", `${rep.side} ${rep.index}`));
        line.append(identChip(rep.run_id, () => nav.openRun(rep.run_id), "Open run"));
        list.append(line);
      }
      repGroup.append(list);
    }
    body.append(repGroup);

    // The reference check: the server's answer, asked for explicitly.
    const refGroup = element("div", "detail-group reference-check");
    refGroup.append(element("p", "detail-group-title", "Reuse as a reference"));
    if (check.id !== e.id || (!check.loading && check.answer === null && check.error === null)) {
      refGroup.append(element("p", "note",
        "Asks the control plane whether eval run --reference would accept this execution, using its own validation."));
      const ask = element("button", "btn-quiet", "Check eligibility");
      ask.type = "button";
      ask.addEventListener("click", () => { void runCheck(e.id); });
      refGroup.append(ask);
    } else if (check.loading) {
      refGroup.append(element("p", "empty", "Checking…"));
    } else if (check.error) {
      const fail = element("div", "result");
      fail.append(element("p", "err-msg", check.error.message || String(check.error)));
      refGroup.append(fail);
    } else {
      const answer = check.answer;
      const verdictLine = element("p", answer.usable ? "reference-usable" : "reference-unusable",
        answer.usable ? "Usable as a reference." : "Not usable as a reference.");
      refGroup.append(verdictLine);
      if (!answer.usable && answer.reason) {
        refGroup.append(element("p", "note", answer.reason));
      }
      refGroup.append(element("p", "note",
        `Holds for a scenario with runs: ${answer.runs}, in project ${answer.project_id} and environment ${answer.environment}. ` +
        "A scenario that differs on any of these is refused by the same validation."));
      if (answer.usable) {
        const command = referenceCommand(e.id);
        refGroup.append(element("code", "cli-command", command));
        const copyCommand = element("button", null, "Copy CLI command");
        copyCommand.type = "button";
        copyCommand.addEventListener("click", () => { void copy(command, "CLI command"); });
        refGroup.append(copyCommand);
      }
    }
    body.append(refGroup);

    const copyID = element("button", "link-button", "Copy execution ID");
    copyID.type = "button";
    copyID.addEventListener("click", () => { void copy(e.id, "execution ID"); });
    body.append(copyID);
  }

  // ------------------------------------------------------------- lifecycle

  // retarget drops everything listed under a previous filter: rows, the
  // selection, the open detail and its check.
  function retarget() {
    key = filterKey();
    listSurface.retarget(key);
    detailSurface.retarget("");
    checkSurface.retarget("");
    list = emptyList();
    selectedID = "";
    detail = { id: "", execution: null, loading: false, error: null };
    check = { id: "", answer: null, loading: false, error: null };
    closePanel(false);
  }

  hosts.panelClose.addEventListener("click", () => closePanel(true));
  hosts.panel.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      closePanel(true);
    }
  });
  hosts.table.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !hosts.panel.hidden) {
      event.preventDefault();
      closePanel(true);
    }
  });

  selection.subscribe((what) => {
    if (what !== "project" && what !== "agent" && what !== "environments") {
      return;
    }
    // An environment belongs to its project: cleared when the project
    // changes, kept when the same project is announced again.
    if (what === "project" && projectID() !== filterProject) {
      environment = "";
      filterProject = projectID();
    }
    if (!visible) {
      return;
    }
    if (filterKey() !== key) {
      retarget();
      drawFilters();
      drawList();
      void loadList("");
    } else {
      drawFilters();
    }
  });

  return Object.freeze({
    enter() {
      visible = true;
      void selection.ensure("environments");
      if (filterKey() !== key) {
        retarget();
      }
      drawFilters();
      drawList();
      if (!list.loaded && !list.loading) {
        void loadList("");
      }
    },
    leave() {
      visible = false;
    },
  });
}
