// Change Impact (task 107-C): one scenario execution's report, in one table
// per section — the evidence the pull request comment and `trustvian eval
// report --format md` show, from the same document.
//
// The document is the scenario execution report route's, result document
// version 2 (docs/compatibility.md). **Every figure on this page is a string
// or integer the document carries**: presence ("2/3"), calls per run
// ("1–4"), means ("2.5") and ratios ("1.2×") are the control plane's display
// strings, counts are its canonical decimals, deltas are its signed decimals.
// Nothing here divides, rounds, adds, compares two counts or composes a
// verdict. Each such value is placed in an element whose `data-source` names
// the member it came from, so a test can walk the rendered page against the
// document; every other digit on the page belongs to a closed-vocabulary
// label (`data-vocab`) — a latency bucket's name, an HTTP status class.
//
// Labels for closed vocabularies come from this file's tables. A value outside
// them is shown as itself, never mapped to a neighbour.

import { element, clear } from "../core/dom.js";
import { shortTime } from "../core/format.js";
import { createSurface } from "../core/ownership.js";
import { emptyState, inlineEmpty, skeleton } from "../ui/feedback.js";
import { readClock } from "./overview-model.js";

const NOT_AVAILABLE = "not available";

const SAME_LABEL = { true: "same", false: "differs", not_recorded: "not recorded" };
const INPUTS_LABEL = { declared: "declared", not_declared: "none declared", not_recorded: "not recorded" };
const CLASS_LABEL = {
  added: "+ added", removed: "− removed", neither: "unclassified", not_recorded: "not recorded",
};
const REASON_LABEL = {
  reference_unavailable: "not available on the reference side",
  candidate_unavailable: "not available on the candidate side",
  both_unavailable: "not available on either side",
};
const CHECK_LABEL = {
  reference_repetitions_completed: "Reference repetitions completed",
  candidate_repetitions_completed: "Candidate repetitions completed",
  repetitions_failing_minimum_evidence: "Repetitions failing minimum evidence",
  repeatedly_added_behaviors: "Repeatedly added behaviors",
  worst_candidate_block_decisions: "Worst candidate block decisions per run",
  worst_candidate_critical_risk_observations: "Worst candidate critical-risk observations per run",
  min_candidate_frequency: "Minimum candidate presence of reference behaviors",
  max_lost_behaviors: "Lost behaviors",
  max_calls_per_run: "Calls per run",
  max_llm_calls_per_run: "Model calls per run",
};
const RULE_LABEL = { equals: "equals", at_most: "at most", at_least: "at least" };
const STATE_LABEL = { not_evaluated: "not configured", deferred: "deferred" };
const TARGET_OUTCOME_LABEL = { pass: "pass", fail: "FAIL", not_observed: "not observed" };
const BUCKET_LABEL = {
  le_1ms: "≤1 ms", le_5ms: "≤5 ms", le_10ms: "≤10 ms", le_50ms: "≤50 ms", le_100ms: "≤100 ms",
  le_250ms: "≤250 ms", le_500ms: "≤500 ms", le_1000ms: "≤1 s", le_2500ms: "≤2.5 s",
  le_10000ms: "≤10 s", gt_10000ms: "over 10 s",
};

const labelOf = (table, value) =>
  (typeof value === "string" && Object.prototype.hasOwnProperty.call(table, value) ? table[value] : String(value));

// ---------------------------------------------------------------- cells

// src is one value from the document, carrying the member it came from.
function src(value, path, className) {
  const span = element("span", className ? `src ${className}` : "src",
    value === undefined || value === null ? NOT_AVAILABLE : String(value));
  span.dataset.source = path;
  return span;
}

// vocab is a closed-vocabulary label that happens to contain digits.
function vocab(text) {
  const span = element("span", "vocab", text);
  span.dataset.vocab = "";
  return span;
}

// line joins nodes and strings into one cell.
function line(...parts) {
  const span = element("span", "cell-line");
  for (const part of parts) {
    span.append(part instanceof Node ? part : document.createTextNode(String(part)));
  }
  return span;
}

function grid(headers, rows, caption) {
  const wrapper = element("div", "scroll");
  const node = element("table", "impact-table");
  node.append(element("caption", null, caption));
  const head = element("thead");
  const headRow = element("tr");
  for (const header of headers) {
    const cell = element("th", null, header);
    cell.setAttribute("scope", "col");
    headRow.append(cell);
  }
  head.append(headRow);
  node.append(head);
  const body = element("tbody");
  for (const row of rows) {
    const tr = element("tr");
    for (const value of row) {
      const cell = element("td");
      if (value instanceof Node) {
        cell.append(value);
      } else if (value !== undefined && value !== null) {
        cell.textContent = String(value);
      }
      tr.append(cell);
    }
    body.append(tr);
  }
  node.append(body);
  wrapper.append(node);
  return wrapper;
}

function heading(target, text) {
  target.append(element("h3", "impact-section", text));
}

// ---------------------------------------------------------------- sections

function sideValue(side, member, base) {
  if (side === undefined || side === null || typeof side[member] !== "string") {
    return "not recorded";
  }
  return src(side[member], `${base}.${member}`, "mono");
}

function renderSameness(target, comparison) {
  heading(target, "Sameness");
  const same = comparison.sameness || {};
  const base = "comparison.sameness";
  const prompt = (side, name) => {
    if (!side || !side.prompt_ref) {
      return "not recorded";
    }
    return line(src(side.prompt_ref.name, `${base}.${name}.prompt_ref.name`, "mono"), " ",
      src(side.prompt_ref.digest, `${base}.${name}.prompt_ref.digest`, "mono"));
  };
  const inputs = (side, name) => {
    if (!side) {
      return "not recorded";
    }
    const state = labelOf(INPUTS_LABEL, side.inputs);
    if (typeof side.input_digest !== "string") {
      return state;
    }
    return line(state, " ", src(side.input_digest, `${base}.${name}.input_digest`, "mono"));
  };
  const answer = (member) => {
    const node = element("span", `same same-${String(same[member]).replace(/[^a-z_]/g, "")}`,
      labelOf(SAME_LABEL, same[member]));
    return node;
  };
  target.append(grid(["", "Reference", "Candidate", "Same"], [
    ["Scenario", sideValue(same.reference, "scenario_digest", `${base}.reference`),
      sideValue(same.candidate, "scenario_digest", `${base}.candidate`), answer("same_scenario")],
    ["Inputs", inputs(same.reference, "reference"), inputs(same.candidate, "candidate"), answer("same_inputs")],
    ["Model", sideValue(same.reference, "model", `${base}.reference`),
      sideValue(same.candidate, "model", `${base}.candidate`), answer("same_model")],
    ["Prompt reference", prompt(same.reference, "reference"), prompt(same.candidate, "candidate"),
      answer("same_prompt_ref")],
  ], "Whether both sides ran the same scenario, inputs, model and prompt"));
  const warnings = Array.isArray(comparison.warnings) ? comparison.warnings : [];
  if (warnings.length === 0) {
    target.append(element("p", "impact-note", "No sameness warning."));
    return;
  }
  const list = element("ul", "impact-warnings");
  warnings.forEach((warning, i) => {
    const item = element("li", "impact-warning");
    item.append(element("strong", null, "Warning "));
    item.append(src(warning.code, `comparison.warnings[${i}].code`, "mono"));
    item.append(document.createTextNode(": "));
    item.append(src(warning.text, `comparison.warnings[${i}].text`));
    list.append(item);
  });
  target.append(list);
  target.append(element("p", "impact-note", "A warning is beside the gate: no check reads it."));
}

function describe(behavior) {
  const b = behavior || {};
  const operation = [b.operation_category, b.operation_name].filter((part) => typeof part === "string" && part !== "").join("/");
  return typeof b.target_name === "string" && b.target_name !== "" ? `${operation} → ${b.target_name}` : operation;
}

// behaviorCell names a behavior by its descriptor's own strings.
function behaviorCell(behavior, base) {
  const b = behavior || {};
  const parts = [];
  if (typeof b.operation_category === "string") {
    parts.push(src(b.operation_category, `${base}.operation_category`));
  }
  if (typeof b.operation_name === "string" && b.operation_name !== "") {
    parts.push("/", src(b.operation_name, `${base}.operation_name`));
  }
  if (typeof b.target_name === "string" && b.target_name !== "") {
    parts.push(" → ", src(b.target_name, `${base}.target_name`));
  }
  const name = line(...parts);
  name.classList.add("behavior-name");
  return name;
}

function fidelityCell(fidelity, base) {
  if (fidelity === undefined || fidelity === null) {
    return "—";
  }
  const parts = [];
  if (fidelity.mixed === true) {
    parts.push(element("strong", "fidelity-mixed", "mixed "));
  }
  parts.push(src(fidelity.level, `${base}.level`), " · semantic ", src(fidelity.semantic, `${base}.semantic`),
    " · transport ", src(fidelity.transport, `${base}.transport`),
    " · unrecorded ", src(fidelity.unrecorded, `${base}.unrecorded`));
  return line(...parts);
}

function callsCell(display, side, base) {
  return line(src(display[`${side}_calls_per_run`], `${base}.${side}_calls_per_run`), ", mean ",
    src(display[`${side}_calls_per_run_mean`], `${base}.${side}_calls_per_run_mean`));
}

// presentInCell lists the repetitions that observed a behavior. Each opens
// its observations through task 085 for the run pair of that repetition: the
// reader chooses the repetition; nothing here picks a representative run.
function presentInCell(row, i, controls) {
  const box = element("span", "present-in");
  const present = row.present_in || {};
  for (const side of ["reference", "candidate"]) {
    const entries = Array.isArray(present[side]) ? present[side] : [];
    entries.forEach((entry, j) => {
      const path = `comparison.behaviors[${i}].present_in.${side}[${j}]`;
      const label = line(side === "reference" ? "R" : "C", src(entry.index, `${path}.index`));
      if (controls && typeof controls.openRepetition === "function") {
        const button = element("button", "link-button rep-chip");
        button.type = "button";
        button.append(label);
        button.setAttribute("aria-label",
          `Observations of ${describe(row.behavior)} in ${side} repetition ${entry.index}, run ${entry.run_id}`);
        button.addEventListener("click", () => controls.openRepetition(row, side, entry));
        box.append(button);
      } else {
        box.append(label);
      }
    });
  }
  return box;
}

function renderBehaviors(target, comparison, controls) {
  heading(target, "Behaviors");
  const rows = Array.isArray(comparison.behaviors) ? comparison.behaviors : [];
  if (rows.length === 0) {
    target.append(inlineEmpty("The report lists no behaviors."));
    return;
  }
  target.append(grid(
    ["Behavior", "Reference", "Candidate", "Calls per run, reference", "Calls per run, candidate",
      "Classification", "Lost", "Fidelity, reference", "Fidelity, candidate", "Observed in"],
    rows.map((row, i) => {
      const base = `comparison.behaviors[${i}]`;
      const display = row.display || {};
      const cls = element("span", `class class-${String(row.classification).replace(/[^a-z_]/g, "")}`,
        labelOf(CLASS_LABEL, row.classification));
      return [
        line(behaviorCell(row.behavior, `${base}.behavior`), " ",
          src(row.fingerprint_id, `${base}.fingerprint_id`, "mono fp")),
        src(display.reference_presence, `${base}.display.reference_presence`),
        src(display.candidate_presence, `${base}.display.candidate_presence`),
        callsCell(display, "reference", `${base}.display`),
        callsCell(display, "candidate", `${base}.display`),
        cls,
        row.lost === true ? element("strong", "lost", "lost") : "no",
        fidelityCell(row.reference_fidelity, `${base}.reference_fidelity`),
        fidelityCell(row.candidate_fidelity, `${base}.candidate_fidelity`),
        presentInCell(row, i, controls),
      ];
    }),
    "Every behavior, with presence k/N, calls per run, classification, lost and fidelity",
  ));
}

function targetName(row, base) {
  const name = typeof row.target_name === "string" && row.target_name !== ""
    ? src(row.target_name, `${base}.target_name`, "mono") : element("em", null, "no target");
  if (typeof row.target_category === "string" && row.target_category !== "") {
    return line(name, " (", src(row.target_category, `${base}.target_category`), ")");
  }
  return name;
}

// sideAvailable is a section's own statement about one side.
function sideAvailable(section, side) {
  return !(section && (section.reason === "both_unavailable" || section.reason === `${side}_unavailable`));
}

function latencyCell(section, side, base) {
  if (!sideAvailable(section, side)) {
    return NOT_AVAILABLE;
  }
  const s = section[side];
  const parts = [];
  (Array.isArray(s.buckets) ? s.buckets : []).forEach((bucket, k) => {
    if (bucket.count !== "0") {
      if (parts.length > 0) {
        parts.push(" · ");
      }
      parts.push(vocab(labelOf(BUCKET_LABEL, bucket.bound)), " ", src(bucket.count, `${base}.${side}.buckets[${k}].count`));
    }
  });
  parts.push(" (observed ", src(s.observed, `${base}.${side}.observed`),
    ", unobserved ", src(s.unobserved, `${base}.${side}.unobserved`),
    ", ", src(s.runs_with_evidence, `${base}.${side}.runs_with_evidence`), " runs)");
  return line(...parts);
}

function statusCell(section, side, base) {
  if (!sideAvailable(section, side)) {
    return NOT_AVAILABLE;
  }
  const s = section[side];
  const parts = [];
  for (const cls of ["1xx", "2xx", "3xx", "4xx", "5xx", "unavailable"]) {
    if (parts.length > 0) {
      parts.push(" · ");
    }
    parts.push(vocab(cls), " ", src(s.http_status[cls], `${base}.${side}.http_status.${cls}`));
  }
  return line(...parts);
}

function spanCell(section, side, base) {
  if (!sideAvailable(section, side)) {
    return NOT_AVAILABLE;
  }
  const s = section[side];
  return line("ok ", src(s.span_status.ok, `${base}.${side}.span_status.ok`),
    " · error ", src(s.span_status.error, `${base}.${side}.span_status.error`),
    " · unset ", src(s.span_status.unset, `${base}.${side}.span_status.unset`));
}

function renderTargets(target, comparison) {
  heading(target, "Targets");
  const rows = Array.isArray(comparison.targets) ? comparison.targets : [];
  if (rows.length === 0) {
    target.append(inlineEmpty("The report lists no targets."));
    return;
  }
  target.append(grid(["Target", "Calls per run, reference", "Calls per run, candidate", "Call ratio"],
    rows.map((row, i) => {
      const base = `comparison.targets[${i}]`;
      const display = row.display || {};
      return [targetName(row, base), callsCell(display, "reference", `${base}.display`),
        callsCell(display, "candidate", `${base}.display`),
        src(display.call_ratio, `${base}.display.call_ratio`)];
    }),
    "Calls per run and the candidate-to-reference call ratio, per target"));

  const detail = [];
  rows.forEach((row, i) => {
    const base = `comparison.targets[${i}].operational`;
    const op = row.operational || {};
    for (const side of ["reference", "candidate"]) {
      const errors = op.errors || {};
      detail.push([
        targetName(row, `comparison.targets[${i}]`), side,
        latencyCell(op.latency || {}, side, `${base}.latency`),
        spanCell(errors, side, `${base}.errors`),
        statusCell(errors, side, `${base}.errors`),
        sideAvailable(errors, side) ? src(errors[side].http_429, `${base}.errors.${side}.http_429`) : NOT_AVAILABLE,
      ]);
    }
    const delta = op.errors && op.errors.delta;
    if (delta) {
      detail.push([targetName(row, `comparison.targets[${i}]`), "change", "",
        line("ok ", src(delta.span_status.ok, `${base}.errors.delta.span_status.ok`),
          " · error ", src(delta.span_status.error, `${base}.errors.delta.span_status.error`)),
        line(vocab("2xx"), " ", src(delta.http_status["2xx"], `${base}.errors.delta.http_status.2xx`),
          " · ", vocab("4xx"), " ", src(delta.http_status["4xx"], `${base}.errors.delta.http_status.4xx`),
          " · ", vocab("5xx"), " ", src(delta.http_status["5xx"], `${base}.errors.delta.http_status.5xx`)),
        src(delta.http_429, `${base}.errors.delta.http_429`)]);
    }
  });
  target.append(element("p", "impact-note",
    "Each side summed over its completed repetitions. Latency buckets with no observation are not listed. " +
    "A side the evidence does not cover reads not available, and a section that cannot be compared shows no change."));
  target.append(grid(["Target", "Side", "Latency", "Span status", "HTTP status", "429"], detail,
    "Latency, status and 429s per target and side"));
}

function renderTokensAndCost(target, comparison) {
  heading(target, "Tokens and cost");
  const op = comparison.operational || {};
  const tokens = op.tokens || {};
  const base = "comparison.operational.tokens";
  if (tokens.comparable !== true) {
    target.append(element("p", "impact-note",
      `Tokens: ${labelOf(REASON_LABEL, tokens.reason)}; no change is shown.`));
  }
  const cell = (side, member) => (sideAvailable(tokens, side)
    ? src(tokens[side][member], `${base}.${side}.${member}`) : NOT_AVAILABLE);
  const change = (member) => (tokens.delta ? src(tokens.delta[member], `${base}.delta.${member}`) : "");
  target.append(grid(["", "Reference", "Candidate", "Change"], [
    ["Input tokens", cell("reference", "input"), cell("candidate", "input"), change("input")],
    ["Output tokens", cell("reference", "output"), cell("candidate", "output"), change("output")],
    ["Unsplit tokens", cell("reference", "unsplit"), cell("candidate", "unsplit"), change("unsplit")],
    ["Observations with usage", cell("reference", "observed"), cell("candidate", "observed"), change("observed")],
    ["Observations without usage", cell("reference", "unobserved"), cell("candidate", "unobserved"), change("unobserved")],
  ], "Token usage per side"));

  const cost = op.cost;
  if (cost === undefined || cost === null) {
    target.append(element("p", "impact-note",
      op.cost_reason === "pricing_not_configured"
        ? "Cost: not available — no pricing table is configured."
        : "Cost: not available."));
    return;
  }
  const cb = "comparison.operational.cost";
  const priced = element("p", "impact-note");
  priced.append(document.createTextNode("Cost in micro-"),
    src(cost.currency, `${cb}.currency`), document.createTextNode(", priced by "),
    src(cost.pricing_version, `${cb}.pricing_version`, "mono"), document.createTextNode(" (digest "),
    src(cost.pricing_digest, `${cb}.pricing_digest`, "mono"), document.createTextNode(", source "),
    src(cost.source, `${cb}.source`), document.createTextNode(")."));
  target.append(priced);
  const costCell = (side) => (cost[side] && typeof cost[side].cost_micros === "string"
    ? src(cost[side].cost_micros, `${cb}.${side}.cost_micros`) : NOT_AVAILABLE);
  target.append(grid(["", "Reference", "Candidate", "Change"], [
    ["Cost", costCell("reference"), costCell("candidate"),
      cost.delta ? src(cost.delta.cost_micros, `${cb}.delta.cost_micros`) : ""],
    ["Priced tokens", src(cost.reference.priced_tokens, `${cb}.reference.priced_tokens`),
      src(cost.candidate.priced_tokens, `${cb}.candidate.priced_tokens`), ""],
    ["Unpriced tokens", src(cost.reference.unpriced_tokens, `${cb}.reference.unpriced_tokens`),
      src(cost.candidate.unpriced_tokens, `${cb}.candidate.unpriced_tokens`), ""],
  ], "Cost per side, with its pricing"));
  if (cost.comparable !== true) {
    target.append(element("p", "impact-note", `Cost is ${labelOf(REASON_LABEL, cost.reason)}; no change is shown.`));
  }
}

function outcomeCell(passed) {
  return passed === true ? element("span", "outcome-pass", "pass") : element("strong", "outcome-fail", "FAIL");
}

function renderGate(target, comparison) {
  heading(target, "Gate");
  const gate = comparison.gate || {};
  if (gate.state !== "recorded") {
    const box = element("p", "gate gate-unrecorded");
    box.append(element("span", "gate-word", "Gate: not recorded."));
    box.append(document.createTextNode(
      " This execution completed before its gate result was kept (schema 14). No check, limit or " +
      "verdict is shown, and none is derived here."));
    target.append(box);
    return;
  }
  const rows = [];
  (Array.isArray(gate.checks) ? gate.checks : []).forEach((check, i) => {
    const base = `comparison.gate.checks[${i}]`;
    rows.push([labelOf(CHECK_LABEL, check.name), src(check.actual, `${base}.actual`),
      labelOf(RULE_LABEL, check.rule), src(check.bound, `${base}.bound`), outcomeCell(check.passed),
      check.advisory === "fresh_scope" ? "fresh scope" : ""]);
  });
  (Array.isArray(gate.frequency_checks) ? gate.frequency_checks : []).forEach((check, i) => {
    const base = `comparison.gate.frequency_checks[${i}]`;
    const label = labelOf(CHECK_LABEL, check.name);
    if (check.state !== "evaluated") {
      const note = check.state === "deferred"
        ? line("missing evidence: ", src(check.missing_evidence, `${base}.missing_evidence`)) : "";
      rows.push([label, "", "", "", element("span", `state-${check.state}`, labelOf(STATE_LABEL, check.state)), note]);
      return;
    }
    if (typeof check.actual === "string") {
      rows.push([label, src(check.actual, `${base}.actual`), labelOf(RULE_LABEL, check.rule),
        src(check.bound, `${base}.bound`), outcomeCell(check.passed), ""]);
      return;
    }
    rows.push([label, "", "", "", outcomeCell(check.passed), "per target, below"]);
    (Array.isArray(check.targets) ? check.targets : []).forEach((t, k) => {
      const tb = `${base}.targets[${k}]`;
      rows.push([line(`${label} · `, src(t.target, `${tb}.target`, "mono")), src(t.actual, `${tb}.actual`),
        labelOf(RULE_LABEL, check.rule), src(t.max, `${tb}.max`),
        element(t.outcome === "fail" ? "strong" : "span", `target-${t.outcome}`, labelOf(TARGET_OUTCOME_LABEL, t.outcome)), ""]);
    });
  });
  target.append(grid(["Check", "Actual", "Rule", "Bound", "Outcome", "Note"], rows,
    "Every gate check, as the execution recorded it when it completed"));
}

function renderSuggestions(target, comparison) {
  heading(target, "Suggestions");
  const rows = Array.isArray(comparison.suggestions) ? comparison.suggestions : [];
  if (rows.length === 0) {
    target.append(inlineEmpty("No suggestion."));
    return;
  }
  target.append(element("p", "impact-note", "Rule outputs beside the evidence; none changes the verdict."));
  const list = element("ul", "impact-suggestions");
  rows.forEach((s, i) => {
    const item = element("li");
    item.append(src(s.rule, `comparison.suggestions[${i}].rule`, "mono"), document.createTextNode(": "),
      src(s.text, `comparison.suggestions[${i}].text`));
    const evidence = element("span", "suggestion-evidence");
    (Array.isArray(s.evidence) ? s.evidence : []).forEach((e, k) => {
      evidence.append(document.createTextNode(k === 0 ? " Evidence: " : " · "),
        src(e.name, `comparison.suggestions[${i}].evidence[${k}].name`, "mono"), document.createTextNode(" "),
        src(e.value, `comparison.suggestions[${i}].evidence[${k}].value`, "mono"));
    });
    item.append(evidence);
    list.append(item);
  });
  target.append(list);
  if (comparison.suggestions_truncated === true) {
    target.append(element("p", "impact-note", "The control plane listed only the first suggestions; more applied."));
  }
}

// renderReport renders one result document version 2. `controls.openRepetition`
// opens one repetition's observations of a behavior (task 085).
export function renderReport(target, report, controls) {
  clear(target);
  const comparison = report.comparison || {};
  const gate = comparison.gate || {};
  const banner = element("p", "gate");
  if (gate.state === "recorded") {
    const pass = gate.verdict === "pass";
    banner.classList.add(pass ? "gate-pass" : "gate-fail");
    banner.append(element("span", "gate-mark", pass ? "✓" : "✗"));
    banner.append(element("span", "gate-word", "Verdict: "), src(gate.verdict, "comparison.gate.verdict", "verdict"));
    banner.append(document.createTextNode(", as the execution recorded it when it completed."));
  } else {
    banner.classList.add("gate-unrecorded");
    banner.append(element("span", "gate-word", "Gate not recorded."));
  }
  target.append(banner);

  const ident = element("p", "impact-ident");
  ident.append(document.createTextNode("Scenario "), src(report.scenario && report.scenario.name, "scenario.name", "mono"),
    document.createTextNode(" · runs "), src(report.scenario && report.scenario.runs, "scenario.runs"),
    document.createTextNode(" · execution "), src(report.execution_id, "execution_id", "mono"));
  if (report.reference && typeof report.reference.execution_id === "string") {
    ident.append(document.createTextNode(" · reference side reused, execution "),
      src(report.reference.execution_id, "reference.execution_id", "mono"), document.createTextNode(" ("),
      src(report.reference.mode, "reference.mode"), document.createTextNode(")"));
  }
  target.append(ident);

  renderSameness(target, comparison);
  renderBehaviors(target, comparison, controls);
  renderTargets(target, comparison);
  renderTokensAndCost(target, comparison);
  renderGate(target, comparison);
  renderSuggestions(target, comparison);

  const producers = element("p", "impact-note");
  producers.append(document.createTextNode("Produced by trustvian "),
    src(report.producers && report.producers.cli_version, "producers.cli_version", "mono"),
    document.createTextNode(" and control plane "),
    src(report.producers && report.producers.control_plane_version, "producers.control_plane_version", "mono"),
    document.createTextNode("."));
  target.append(producers);
}

// ---------------------------------------------------------------- the surface

// localNote is the page's own fact — when it read something — marked so, as
// the one kind of figure on this destination that is not the document's.
function localNote(text) {
  const note = element("p", "panel-meta", text);
  note.dataset.local = "";
  return note;
}

// createChangeImpact owns the execution list and the open report. Reads
// happen when the destination is entered, when the context's project or agent
// changes while it is open, on Refresh and on Load next page — never on a
// timer.
export function createChangeImpact(deps) {
  const { selection, hosts, controls } = deps;
  const now = typeof deps.now === "function" ? deps.now : () => Date.now();
  const listSurface = createSurface("impact.list");
  const reportSurface = createSurface("impact.report");

  let visible = false;
  let list = { key: "", rows: [], next: "", loading: false, error: null, readAt: 0 };
  let open = { id: "", report: null, loading: false, error: null, readAt: 0 };

  const scope = () => {
    const chosen = selection.selection;
    return {
      projectID: chosen.project === null ? "" : chosen.project.id,
      agentID: chosen.agent === null ? "" : chosen.agent.id,
    };
  };
  const scopeKey = () => JSON.stringify(scope());

  async function loadList(more) {
    const { projectID, agentID } = scope();
    const key = scopeKey();
    const ticket = listSurface.begin(key);
    const after = more ? list.next : "";
    list = more ? { ...list, loading: true, error: null }
      : { key, rows: [], next: "", loading: projectID !== "", error: null, readAt: 0 };
    drawList();
    if (projectID === "") {
      return;
    }
    try {
      const page = await deps.listExecutions(projectID, agentID ? { agentID } : {}, after);
      if (!listSurface.owns(ticket)) {
        return;
      }
      const rows = Array.isArray(page.executions) ? page.executions : [];
      list = {
        key, rows: more ? list.rows.concat(rows) : rows,
        next: typeof page.next_after === "string" ? page.next_after : "",
        loading: false, error: null, readAt: now(),
      };
    } catch (error) {
      if (!listSurface.owns(ticket)) {
        return;
      }
      list = { ...list, loading: false, error };
    }
    drawList();
  }

  async function openReport(id) {
    const ticket = reportSurface.begin(id);
    open = { id, report: null, loading: true, error: null, readAt: 0 };
    drawReport();
    try {
      const report = await deps.getReport(id);
      if (!reportSurface.owns(ticket)) {
        return;
      }
      open = { id, report, loading: false, error: null, readAt: now() };
      if (typeof deps.onReport === "function") {
        deps.onReport(report);
      }
    } catch (error) {
      if (!reportSurface.owns(ticket)) {
        return;
      }
      open = { id, report: null, loading: false, error, readAt: now() };
    }
    drawList();
    drawReport();
  }

  function drawList() {
    const host = hosts.list;
    clear(host);
    if (scope().projectID === "") {
      host.append(inlineEmpty("Choose a project to list its recorded scenario executions."));
      return;
    }
    if (list.loading && list.rows.length === 0) {
      const box = element("div");
      skeleton(box, 3, 4);
      host.append(box);
      return;
    }
    if (list.error) {
      const box = element("div", "result result-error");
      box.append(element("p", "err-msg", list.error.message || String(list.error)));
      host.append(box);
      return;
    }
    if (list.rows.length === 0) {
      host.append(emptyState("No scenario executions",
        "A report belongs to a scenario execution: run one with trustvian eval run.", "compare"));
      return;
    }
    host.append(element("p", "impact-note",
      `${list.next !== "" ? "The newest page of" : "Every"} recorded execution in this scope, newest first.`));
    host.append(localNote(readClock(list.readAt)));
    const ul = element("ul", "impact-executions");
    for (const row of list.rows) {
      const item = element("li", row.id === open.id ? "impact-execution selected" : "impact-execution");
      const button = element("button", "ident-chip", row.scenario_name || row.id);
      button.type = "button";
      button.setAttribute("aria-label", `Open the report of execution ${row.id}`);
      if (row.status !== "completed") {
        button.disabled = true;
        button.setAttribute("aria-label", `Execution ${row.id} is ${row.status}; only a completed execution has a report`);
      }
      button.addEventListener("click", () => { void openReport(row.id); });
      item.append(button);
      item.append(element("span", "exec-id mono", row.id));
      item.append(element("span", `exec-status status-${String(row.status).replace(/[^a-z]/g, "")}`, row.status || NOT_AVAILABLE));
      item.append(element("span", "exec-verdict", row.verdict || "—"));
      item.append(element("span", "exec-time", shortTime(row.started_at)));
      ul.append(item);
    }
    host.append(ul);
    if (list.next !== "") {
      const more = element("button", "more-inline", list.loading ? "Loading…" : "Load next page");
      more.type = "button";
      more.disabled = list.loading;
      more.addEventListener("click", () => { void loadList(true); });
      host.append(more);
    }
  }

  function drawReport() {
    const host = hosts.report;
    if (open.id === "") {
      clear(host);
      host.append(inlineEmpty("Choose a completed scenario execution to read its report."));
      return;
    }
    if (open.loading) {
      clear(host);
      host.append(inlineEmpty(`Reading the report of ${open.id}…`));
      return;
    }
    if (open.error) {
      clear(host);
      const box = element("div", "result result-error");
      box.setAttribute("role", "alert");
      box.append(element("p", "err-msg", open.error.message || String(open.error)));
      host.append(box);
      return;
    }
    renderReport(host, open.report, controls);
    host.prepend(localNote(`Report ${readClock(open.readAt)}.`));
  }

  selection.subscribe((what) => {
    if (visible && (what === "project" || what === "agent")) {
      void loadList(false);
    }
  });

  return Object.freeze({
    enter() {
      visible = true;
      if (list.key !== scopeKey()) {
        void loadList(false);
      } else {
        drawList();
      }
      drawReport();
    },
    leave() {
      visible = false;
    },
    refresh() {
      void loadList(false);
      if (open.id !== "") {
        void openReport(open.id);
      }
    },
    // open reads one execution's report, from a link elsewhere on the page.
    open(id) {
      void openReport(id);
    },
  });
}
