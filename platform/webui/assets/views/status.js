// The Status destination (task 105): why the Live view is empty, or isn't.
//
// It renders GET /v1/status and nothing else. Every figure on it is a field of
// that document, shown as the server wrote it: this view counts nothing,
// compares nothing and decides nothing — not whether a Collector is stale, not
// whether a producer is active, not which view to open, and not what to
// suggest. The control plane computes all of those, and a second copy here
// would be a rule that can disagree with the one that matters.
//
// It is the landing view when nothing is active. The control plane says so in
// the document's `landing` field, and app.js reads that field from the one
// status request it makes at startup.
//
// Reads happen when the destination is opened, when Refresh is pressed, when
// the realtime stream says the document changed while it is open, and when the
// stream reconnects while it is open. There is no timer: a Collector that stops
// reporting becomes stale on the next read, and the page says when it was read.

import { element, clear } from "../core/dom.js";
import { shortTime } from "../core/format.js";
import { createSurface } from "../core/ownership.js";
import { emptyState, skeleton } from "../ui/feedback.js";
import { dataTable, summaryStrip, detailGroup } from "../ui/dashboard.js";

const NOT_REPORTED = "not reported by this Collector";
const NOT_STATED = "not stated";

// text renders a server string, saying so when it is empty rather than
// showing nothing a reader could mistake for zero.
function text(value, absent) {
  return typeof value === "string" && value !== "" ? value : absent || NOT_STATED;
}

function section(title, note, extraClass) {
  const host = element("section", extraClass ? `panel status-section ${extraClass}` : "panel status-section");
  const head = element("div", "panel-head");
  head.append(element("h3", "panel-title", title));
  if (note) {
    head.append(element("p", "panel-meta", note));
  }
  host.append(head);
  return host;
}

function boundNote(truncated, what, bound) {
  return truncated
    ? element("p", "panel-note status-truncated", `More ${what} than the bound of ${bound}; the rest are not listed.`)
    : null;
}

function appendIf(host, node) {
  if (node !== null) {
    host.append(node);
  }
}

// ---------------------------------------------------------------------
// Suggestions
// ---------------------------------------------------------------------

function renderSuggestions(host, doc) {
  const panel = section("Suggestions",
    "Each comes from a named rule over the evidence on this page, and changes nothing else.");
  const suggestions = Array.isArray(doc.suggestions) ? doc.suggestions : [];
  if (suggestions.length === 0) {
    panel.append(element("p", "panel-note", "No rule matched the evidence on this page."));
  }
  const list = element("ol", "status-suggestions");
  for (const s of suggestions) {
    const item = element("li", "status-suggestion");
    item.append(element("p", "status-suggestion-text", text(s.text)));
    const meta = element("p", "status-suggestion-rule");
    meta.append(element("code", null, text(s.rule)));
    meta.append(document.createTextNode(` · rule version ${String(s.rule_version)}`));
    item.append(meta);
    const evidence = Array.isArray(s.evidence) ? s.evidence : [];
    const pairs = evidence.map((e) => [text(e.name), text(e.value)]);
    if (pairs.length > 0) {
      item.append(detailGroup("Evidence", pairs));
    }
    list.append(item);
  }
  if (suggestions.length > 0) {
    panel.append(list);
  }
  appendIf(panel, boundNote(doc.suggestions_truncated === true, "suggestions", doc.bounds && doc.bounds.suggestions));
  host.append(panel);
}

// ---------------------------------------------------------------------
// One Collector
// ---------------------------------------------------------------------

const STATE_WORDS = Object.freeze({
  reporting: "Reporting",
  stale: "Stale — no report within the fresh window",
});

function renderProducers(host, collector, bounds) {
  const panel = section("Producers", "By service.name, as the Collector saw them.", "status-wide");
  const table = element("div", "table-host");
  panel.append(table);
  dataTable(table, {
    rows: collector.producers,
    keyOf: (row) => row.service_name,
    emptyTitle: "No producer has sent spans to this Collector.",
    emptyIcon: "empty",
    caption: `Producers seen by ${collector.collector_id}`,
    columns: [
      { key: "service", label: "service.name", cell: (row) => text(row.service_name, "(no service.name)") },
      { key: "spans", label: "Spans", align: "right", cell: (row) => row.spans },
      { key: "seen", label: "Last span", cell: (row) => shortTime(row.last_seen_at) },
      {
        key: "scopes", label: "Instrumentation scopes", className: "status-wrap",
        cell: (row) => (Array.isArray(row.scopes) && row.scopes.length > 0
          ? row.scopes.map((s) => (s.version ? `${text(s.name, "(unnamed)")} ${s.version}` : text(s.name, "(unnamed)"))).join(", ")
          : NOT_STATED) + (row.scopes_truncated ? " (more not listed)" : ""),
      },
      {
        key: "sdk", label: "SDK", className: "status-wrap",
        cell: (row) => [row.sdk && row.sdk.name, row.sdk && row.sdk.language, row.sdk && row.sdk.version]
          .filter((part) => typeof part === "string" && part !== "").join(" ") || NOT_STATED,
      },
    ],
  });
  appendIf(panel, boundNote(collector.producers_truncated === true, "producers", bounds.producers_per_collector));
  host.append(panel);
}

function renderModels(host, collector, bounds) {
  const panel = section("Model calls",
    "Display metadata from gen_ai.request.model or llm.model_name, with the provider the telemetry named.");
  const models = collector.models || {};
  if (models.reported !== true) {
    panel.append(element("p", "panel-note", NOT_REPORTED));
    host.append(panel);
    return;
  }
  const table = element("div", "table-host");
  panel.append(table);
  dataTable(table, {
    rows: models.calls,
    keyOf: (row) => `${row.provider}\u0000${row.model}`,
    emptyTitle: "No model call was named by the telemetry.",
    emptyHint: "A model call reached through plain HTTP is counted under transport targets below.",
    emptyIcon: "empty",
    caption: `Model calls seen by ${collector.collector_id}`,
    columns: [
      { key: "model", label: "Model", cell: (row) => text(row.model) },
      { key: "provider", label: "Provider", cell: (row) => text(row.provider) },
      { key: "calls", label: "Calls", align: "right", cell: (row) => row.calls },
    ],
  });
  appendIf(panel, boundNote(models.truncated === true, "models", bounds.models_per_collector));
  host.append(panel);
}

function renderFidelity(host, collector, bounds) {
  const panel = section("Fidelity",
    "Semantic: a convention named the operation. Transport: only the protocol and target were available.");
  const fidelity = collector.fidelity || {};
  if (fidelity.reported !== true) {
    panel.append(element("p", "panel-note", NOT_REPORTED));
  } else {
    const strip = element("div", "strip");
    summaryStrip(strip, [
      { key: "Semantic", value: fidelity.semantic },
      { key: "Transport", value: fidelity.transport },
    ]);
    panel.append(strip);
  }

  const targets = collector.transport_targets || {};
  panel.append(element("h4", "panel-subtitle", "Operations per HTTP target at transport fidelity"));
  if (targets.reported !== true) {
    panel.append(element("p", "panel-note", NOT_REPORTED));
    host.append(panel);
    return;
  }
  const table = element("div", "table-host");
  panel.append(table);
  dataTable(table, {
    rows: targets.targets,
    keyOf: (row) => row.target,
    emptyTitle: "No HTTP call to a named target arrived at transport fidelity.",
    emptyHint: "DB spans, convention spans this table does not map, and HTTP spans with no server.address are counted as transport above, never here.",
    emptyIcon: "empty",
    caption: `HTTP targets seen by ${collector.collector_id} at transport fidelity`,
    columns: [
      { key: "target", label: "Target", cell: (row) => text(row.target, "(no target)") },
      { key: "spans", label: "Spans", align: "right", cell: (row) => row.spans },
      {
        key: "operations", label: "Distinct operations", align: "right",
        cell: (row) => (row.operations_saturated ? `${row.distinct_operations} or more` : row.distinct_operations),
      },
    ],
  });
  panel.append(element("p", "panel-note",
    `Operation names are counted and never shown. Counting stops at ${bounds.operations_per_target} per target.`));
  appendIf(panel, boundNote(targets.truncated === true, "targets", bounds.transport_targets_per_collector));
  host.append(panel);
}

function renderActorsAndLearning(host, collector) {
  const panel = section("Actors and learning");
  const actors = collector.actors || {};
  panel.append(element("h4", "panel-subtitle", "How each span's actor was bound"));
  if (actors.reported !== true) {
    panel.append(element("p", "panel-note", NOT_REPORTED));
  } else {
    const strip = element("div", "strip");
    summaryStrip(strip, [
      { key: "trustvian.actor.id", value: actors.bound_by_override },
      { key: "service.name", value: actors.bound_by_service_name },
      { key: "Neither — not evaluated", value: actors.unbound, className: actors.unbound !== "0" ? "is-warn" : "" },
    ]);
    panel.append(strip);
  }

  const learning = collector.learning || {};
  panel.append(element("h4", "panel-subtitle", "What the engine's Observe reported"));
  if (learning.reported !== true) {
    panel.append(element("p", "panel-note", NOT_REPORTED));
    host.append(panel);
    return;
  }
  const strip = element("div", "strip");
  summaryStrip(strip, [
    { key: "Learned", value: learning.learned },
    { key: "Not learned", value: learning.not_learned },
    { key: "Observe errors", value: learning.observe_errors },
  ]);
  panel.append(strip);
  const byDecision = Array.isArray(learning.not_learned_by_decision) ? learning.not_learned_by_decision : [];
  if (byDecision.length > 0) {
    panel.append(detailGroup("Not learned, by the decision it followed",
      byDecision.map((row) => [text(row.decision), row.count])));
  }
  panel.append(element("p", "panel-note",
    "Observe does not say why it learned nothing: an ineligible decision, a declined write and a full " +
    "baseline look the same from here."));
  host.append(panel);
}

function renderCollector(host, collector, bounds) {
  const card = element("article", "status-collector");
  const head = element("div", "status-collector-head");
  head.append(element("h2", "status-collector-title", `Collector ${text(collector.collector_id)}`));
  head.append(element("span", `status-state status-state-${collector.state === "reporting" ? "reporting" : "stale"}`,
    STATE_WORDS[collector.state] || text(collector.state)));
  card.append(head);

  const endpoints = Array.isArray(collector.receiver_endpoints) && collector.receiver_endpoints.length > 0
    ? collector.receiver_endpoints.join(", ")
    : NOT_STATED;
  card.append(detailGroup("Collector", [
    ["Last report", shortTime(collector.last_report_at)],
    ["Started", shortTime(collector.started_at)],
    ["OTLP receivers", endpoints],
    ["Evaluation run", text(collector.evaluation_run_id, "none — this Collector feeds no run")],
    ["Process", text(collector.instance)],
  ]));

  const spans = collector.spans || {};
  const strip = element("div", "strip");
  summaryStrip(strip, [
    { key: "Spans received", value: spans.received },
    { key: "Evaluated", value: spans.evaluated },
    { key: "Did not map to an event", value: spans.invalid, className: spans.invalid !== "0" ? "is-warn" : "" },
    { key: "Analysis failed", value: spans.analyze_errors, className: spans.analyze_errors !== "0" ? "is-stop" : "" },
  ]);
  card.append(strip);

  const grid = element("div", "status-grid");
  renderProducers(grid, collector, bounds);
  renderModels(grid, collector, bounds);
  renderFidelity(grid, collector, bounds);
  renderActorsAndLearning(grid, collector);
  card.append(grid);
  host.append(card);
}

// ---------------------------------------------------------------------
// The document
// ---------------------------------------------------------------------

export function renderStatus(hosts, doc, onOpenLive) {
  clear(hosts.read);
  hosts.read.textContent = `Read ${shortTime(doc.read_at)} · status held since ${shortTime(doc.held_since)}, ` +
    "in memory: a restart of the control plane forgets it.";

  clear(hosts.landing);
  hosts.landing.hidden = doc.landing !== "live";
  if (doc.landing === "live") {
    hosts.landing.append(element("p", null, "Spans are arriving, so Live has something to show."));
    const open = element("button", "btn-quiet", "Open Live");
    open.type = "button";
    open.addEventListener("click", onOpenLive);
    hosts.landing.append(open);
  }

  clear(hosts.suggestions);
  renderSuggestions(hosts.suggestions, doc);

  clear(hosts.collectors);
  const bounds = doc.bounds || {};
  const collectors = Array.isArray(doc.collectors) ? doc.collectors : [];
  if (collectors.length === 0) {
    hosts.collectors.append(emptyState(
      "No Collector is reporting to this control plane.",
      "Start one with 'trustvian dev -- <command>', or check the pipeline with 'trustvian dev --check'.",
      "empty",
    ));
  }
  for (const collector of collectors) {
    renderCollector(hosts.collectors, collector, bounds);
  }

  clear(hosts.engine);
  const engine = doc.engine || {};
  hosts.engine.append(detailGroup("Engine", [
    ["State", text(engine.state)],
    ["Why", text(engine.reason)],
  ]));

  clear(hosts.bounds);
  hosts.bounds.textContent = `Bounds: ${bounds.collectors} Collectors; per Collector ${bounds.producers_per_collector} ` +
    `producers, ${bounds.models_per_collector} models and ${bounds.transport_targets_per_collector} transport targets; ` +
    `${bounds.scopes_per_producer} scopes per producer; ${bounds.suggestions} suggestions. A Collector is ` +
    `reporting within ${bounds.fresh_window_seconds} s of its last report and is forgotten after ` +
    `${bounds.expiry_window_seconds} s.`;
}

// createStatus wires the destination.
//
// deps.getStatus is the one read. visible tells the controller whether its
// destination is on screen, so a hint arriving while it is hidden marks the
// page stale instead of reading for nobody.
export function createStatus(deps) {
  const surface = createSurface("status");
  let doc = null;
  let stale = true;
  let loading = false;
  let again = false;

  function draw() {
    if (doc !== null) {
      renderStatus(deps.hosts, doc, deps.nav.openLive);
    }
  }

  async function read() {
    if (loading) {
      // One read at a time; a hint that arrives mid-read asks for exactly one
      // more, never a queue of them.
      again = true;
      return;
    }
    loading = true;
    again = false;
    const ticket = surface.begin("status");
    if (doc === null) {
      skeleton(deps.hosts.collectors, 4, 4);
    }
    try {
      const response = await deps.getStatus();
      if (!surface.owns(ticket)) {
        return;
      }
      doc = response;
      stale = false;
      draw();
    } catch (error) {
      if (!surface.owns(ticket)) {
        return;
      }
      clear(deps.hosts.collectors);
      const box = element("div", "result result-error");
      box.append(element("p", "err-msg", error && error.message ? error.message : String(error)));
      deps.hosts.collectors.append(box);
    } finally {
      loading = false;
    }
    if (again && deps.visible()) {
      await read();
    }
  }

  return Object.freeze({
    // seed adopts the document app.js read at startup, so opening this view
    // first costs no second request.
    seed(initial) {
      doc = initial;
      stale = false;
      draw();
    },
    enter() {
      if (stale || doc === null) {
        void read();
        return;
      }
      draw();
    },
    refresh() {
      void read();
    },
    // changed is the realtime hint: the document moved. Read it now if the
    // destination is on screen, otherwise on the next visit.
    changed() {
      stale = true;
      if (deps.visible()) {
        void read();
      }
    },
  });
}
