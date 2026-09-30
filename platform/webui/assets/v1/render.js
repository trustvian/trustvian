// DOM rendering for server-supplied values.
//
// Two rules, and every function here exists to keep them.
//
// 1. Server text never becomes markup. Values reach the DOM through
//    textContent and elements through createElement. innerHTML, outerHTML,
//    insertAdjacentHTML and document.write do not appear in this file or any
//    other shipped asset, and a Go test fails the build if they do. A local
//    control plane still returns strings a developer typed in another window,
//    and those strings are untrusted input like any other.
//
// 2. Fields are allowlisted, never enumerated. There is no
//    Object.entries/keys/for-in walk over a server value anywhere below.
//
// The second rule is a privacy boundary rather than a style preference. Tasks
// 050 and 059 deliberately keep prompts, completions, tool arguments,
// Event.Attributes, PolicyReason, contributors and the raw DecisionRecord out
// of what the platform exposes, and the compatibility contract lets /v1 add
// fields at any time. A renderer that walked whatever object it received would
// put the next such field on screen without anyone deciding to — and deciding
// to show something is exactly what the boundary consists of.

// ---------------------------------------------------------------------
// Field allowlists
//
// Parsed by platform/webui/assets_test.go, which asserts no sensitive or
// additive field name can appear here. Keep the literal-array shape: the test
// reads these, and a computed list would defeat it.
// ---------------------------------------------------------------------

import { element, clear } from "../core/dom.js";

export const PROJECT_FIELDS = Object.freeze([
  "id",
  "name",
]);

export const AGENT_FIELDS = Object.freeze([
  "id",
  "project_id",
  "name",
]);

export const CANDIDATE_FIELDS = Object.freeze([
  "id",
  "agent_id",
]);

export const CANDIDATE_METADATA_FIELDS = Object.freeze([
  "label",
  "source_ref",
  "artifact_digest",
  "model",
  "toolset_digest",
  "config_digest",
]);

export const RUN_FIELDS = Object.freeze([
  "id",
  "candidate_id",
  "environment",
  "behavioral_profile",
  "status",
  "created_at",
  "started_at",
  "finished_at",
  "failure_reason",
]);

export const PROGRESS_FIELDS = Object.freeze([
  "run_id",
  "status",
  "record_count",
  "behavior_observation_count",
  "distinct_behavior_count",
  "behavior_complete",
  "next_ingest_sequence",
]);

// OBSERVATION_FIELDS is task 059's realtime projection and nothing more.
export const OBSERVATION_FIELDS = Object.freeze([
  "sequence",
  "new_behavior",
  "decision",
  "risk_level",
  "approval_status",
  "trust_score",
  "anomaly_score",
  "anomaly_confidence",
  "record_count",
  "behavior_complete",
]);

export const BEHAVIOR_FIELDS = Object.freeze([
  "actor_type",
  "operation_category",
  "operation_name",
  "target_name",
  "target_category",
  "environment",
]);

export const DIFF_FIELDS = Object.freeze([
  "reference_observation_count",
  "candidate_observation_count",
  "added_count",
  "removed_count",
  "shared_count",
  // Task 083's counting correction (ADR 0052). Two units, both named:
  // added_count is behavioral identities and added_change_count is the
  // changes they amount to. correlation_state says whether the fold was
  // available, and a reader needs it to tell "one act" from "no parentage".
  "added_change_count",
  "correlation_state",
  "counting_policy_version",
]);

export const DELTA_FIELDS = Object.freeze([
  "change",
  "fingerprint_id",
  "reference_observations",
  "candidate_observations",
]);

export const DECISION_CLASSES = Object.freeze([
  "allow",
  "observe_only",
  "alert",
  "challenge",
  "require_approval",
  "block",
]);

export const RISK_CLASSES = Object.freeze([
  "low",
  "medium",
  "high",
  "critical",
]);

export const METRIC_NAMES = Object.freeze([
  "identity_confidence",
  "anomaly_score",
  "anomaly_confidence",
  "trust_score",
  "context_risk",
]);

// GATE_CHECKS names the five checks task 056 always returns, with the bound
// field each carries. All five are rendered on every comparison: a FAIL that
// showed only the failing check would throw away the evidence the gate exists
// to produce.
export const GATE_CHECKS = Object.freeze([
  Object.freeze({ key: "reference_evidence", label: "Reference evidence", bound: "minimum" }),
  Object.freeze({ key: "candidate_evidence", label: "Candidate evidence", bound: "minimum" }),
  Object.freeze({ key: "added_behaviors", label: "Added behaviors", bound: "maximum" }),
  Object.freeze({ key: "block_decisions", label: "Block decisions", bound: "maximum" }),
  Object.freeze({ key: "critical_risk_observations", label: "Critical risk observations", bound: "maximum" }),
]);

// EVIDENCE_OBSERVATION_FIELDS is what task 076's views may show of one retained
// observation.
//
// A subset of task 067's retention contract, and the two exclusions are
// deliberate:
//
//   - **`policy_reason` is absent.** It is the one free-text field on the
//     retained row, it is producer-supplied, and task 059 already keeps it off
//     the realtime projection for that reason. `policy_rule` names which rule
//     decided and is an identifier, which is the explanatory half without the
//     free text. The Go guard over this file forbids `policy_reason` outright.
//   - **`behavior` is absent** because it is a nested descriptor rather than a
//     scalar; it is rendered through BEHAVIOR_FIELDS, which is its own
//     allowlist.
//
// There is no fidelity or behavioral-layer field, because task 067 retained
// neither — see FIDELITY_NOT_RETAINED in trace.js.
export const EVIDENCE_OBSERVATION_FIELDS = Object.freeze([
  "sequence",
  "event_id",
  "timestamp",
  "actor_id",
  "actor_type",
  "identity_confidence",
  "fingerprint_id",
  "new_behavior",
  "anomaly_score",
  "anomaly_confidence",
  "trust_score",
  "context_risk",
  "risk_level",
  "decision",
  "policy_rule",
  "matched_default",
  "trace_id",
  "span_id",
  "session_id",
  "delegated_from",
  "approval_status",
  "parent_span_id",
  "span_lineage",
  "duration_nanos",
  "span_status",
]);





// GATE_CHECK_EVIDENCE says which resolution route answers each gate check.
//
// **Navigation metadata, not resolution logic.** It records only whether a check
// counts behaviors or observations — which is what decides the URL to ask — and
// it decides nothing about the answer. Every status, count, side and verdict on
// screen is the server's, including `aggregate_only`: the two evidence checks are
// sent to the behaviors route and the control plane is what reports that they
// have no per-observation evidence to link to. This page never claims that on
// its own, because it is a fact about the check's meaning rather than about its
// name.
export const GATE_CHECK_EVIDENCE = Object.freeze([
  Object.freeze({ key: "reference_evidence", route: "behaviors" }),
  Object.freeze({ key: "candidate_evidence", route: "behaviors" }),
  Object.freeze({ key: "added_behaviors", route: "behaviors" }),
  Object.freeze({ key: "block_decisions", route: "observations" }),
  Object.freeze({ key: "critical_risk_observations", route: "observations" }),
]);

// evidenceRouteFor returns the route a gate check resolves through, or "" for a
// name this page does not publish.
export function evidenceRouteFor(check) {
  for (const entry of GATE_CHECK_EVIDENCE) {
    if (entry.key === check) {
      return entry.route;
    }
  }
  return "";
}

// Human labels for allowlisted keys. Absent keys fall back to the raw key,
// which is already known-safe because it came from an allowlist above rather
// than from the server.
const LABELS = Object.freeze({
  id: "ID",
  name: "Name",
  project_id: "Project ID",
  agent_id: "Agent ID",
  candidate_id: "Candidate ID",
  run_id: "Run ID",
  environment: "Environment",
  behavioral_profile: "Behavioral profile",
  status: "Status",
  created_at: "Created",
  started_at: "Started",
  finished_at: "Finished",
  failure_reason: "Failure reason",
  record_count: "Records",
  behavior_observation_count: "Behavior observations",
  distinct_behavior_count: "Distinct behaviors",
  behavior_complete: "Behavior complete",
  next_ingest_sequence: "Next sequence",
  label: "Label",
  source_ref: "Source ref",
  artifact_digest: "Artifact digest",
  model: "Model",
  toolset_digest: "Toolset digest",
  config_digest: "Config digest",
  sequence: "Sequence",
  new_behavior: "New",
  decision: "Decision",
  risk_level: "Risk",
  approval_status: "Approval",
  trust_score: "Trust",
  anomaly_score: "Anomaly",
  anomaly_confidence: "Confidence",
  actor_type: "Actor type",
  operation_category: "Operation category",
  operation_name: "Operation",
  target_name: "Target",
  target_category: "Target category",
  change: "Change",
  fingerprint_id: "Fingerprint",
  reference_observations: "Reference observations",
  candidate_observations: "Candidate observations",
  reference_observation_count: "Reference observations",
  candidate_observation_count: "Candidate observations",
  added_count: "Added",
  removed_count: "Removed",
  shared_count: "Shared",
  event_id: "Event ID",
  timestamp: "Recorded at",
  actor_id: "Actor",
  identity_confidence: "Identity confidence",
  context_risk: "Context risk",
  policy_rule: "Policy rule",
  matched_default: "Matched default",
  trace_id: "Trace",
  span_id: "Span",
  session_id: "Session",
  delegated_from: "Delegated from",
  parent_span_id: "Parent span",
  span_lineage: "Lineage",
  duration_nanos: "Duration (ns)",
  span_status: "Span status",
  reference_run_id: "Reference run",
  candidate_run_id: "Candidate run",
  check: "Gate check",
  behavior: "Behavior",
  presence: "Presence",
  status: "Status",
  side: "Side",
  recorded_count: "Recorded count",
  exhaustive: "Exhaustive",
  state: "Retained history",
  retained_count: "Retained observations",
  complete: "Complete",
  history_state: "Retained history",
});

const labelFor = (key) => (Object.hasOwn(LABELS, key) ? LABELS[key] : key);

// ---------------------------------------------------------------------
// Primitives
// ---------------------------------------------------------------------

// element builds a node with optional text.
//
// The only way text enters the DOM in this application.
// element and clear live in core/dom.js, which is the bottom of the stack and
// knows nothing about Trustvian. Re-exported here because this module is what
// the /v1 surface imports, and a second import line in every caller would buy
// nothing.
export { element, clear };

// display formats one allowlisted value as text.
//
// Absent stays absent. Task 053 built the aggregate around the difference
// between unknown and zero, and rendering a missing value as 0 would turn "we
// have no evidence" into "we measured none".
function display(value) {
  if (value === undefined || value === null || value === "") {
    return "—";
  }
  if (value === true) {
    return "yes";
  }
  if (value === false) {
    return "no";
  }
  // Numbers from the API are ratios and scores, already JSON numbers. uint64
  // counters arrive as strings and are passed through untouched, never parsed:
  // JavaScript cannot hold them.
  return String(value);
}

// pick reads only allowlisted keys, in allowlist order.
//
// Iterates the allowlist, never the object. That direction is the mechanism: a
// field the server adds is not in the list, so it cannot be reached.
function pick(source, fields) {
  const pairs = [];
  if (source === undefined || source === null || typeof source !== "object") {
    return pairs;
  }
  for (const key of fields) {
    pairs.push([labelFor(key), display(source[key])]);
  }
  return pairs;
}

// displayValue is display(), exported for the evidence surface.
//
// One formatting rule for the whole application rather than two: the absent /
// zero distinction below is a correctness property, and a second renderer with
// its own idea of what "—" means is how a missing value starts reading as a
// measured one.
export function displayValue(value) {
  return display(value);
}

// retainedValue reads one field of a retained observation, through the
// allowlist.
//
// **The allowlist is the gate, not a comment.** A key it does not name returns
// the absent marker rather than the value, so a field `/v1` gains later cannot
// reach a cell by somebody writing `observation.new_thing` — which is the
// failure mode a declared-but-unconsulted list would leave wide open. Every
// evidence cell goes through here.
export function retainedValue(observation, key) {
  if (observation === undefined || observation === null || !EVIDENCE_OBSERVATION_FIELDS.includes(key)) {
    return display(undefined);
  }
  return display(observation[key]);
}

// definitionList renders label/value pairs.
export function definitionList(pairs) {
  const list = element("dl", "kv");
  for (const [label, value] of pairs) {
    list.append(element("dt", null, label));
    list.append(element("dd", null, value));
  }
  return list;
}

// table renders a bounded set of rows.
export function table(headers, rows, caption) {
  const wrapper = element("div", "scroll");
  const node = element("table");
  if (caption) {
    node.append(element("caption", null, caption));
  }
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
      // A cell may carry a control rather than text — a promotion row's
      // "Open" button, for instance. Appended as the node it is; anything
      // else still goes through element(), which sets textContent and never
      // parses markup.
      if (value instanceof Node) {
        const cell = element("td");
        cell.append(value);
        tr.append(cell);
      } else {
        tr.append(element("td", null, value));
      }
    }
    body.append(tr);
  }
  node.append(body);
  wrapper.append(node);
  return wrapper;
}

export function emptyState(message) {
  return element("p", "empty", message);
}

export function errorState(error) {
  const node = element("div", "err-box");
  node.setAttribute("role", "alert");
  const parts = [];
  if (error.status) {
    parts.push(`HTTP ${error.status}`);
  }
  if (error.code) {
    parts.push(error.code);
  }
  if (parts.length > 0) {
    node.append(element("p", "err-meta", parts.join(" · ")));
  }
  // The server's message, as text. No stack trace and no internal detail: this
  // is the API's envelope rendered, not a debugger.
  node.append(element("p", "err-msg", error.message));
  return node;
}

// ---------------------------------------------------------------------
// Entities
// ---------------------------------------------------------------------

export function renderProject(target, project) {
  clear(target);
  target.append(element("h3", null, "Project"));
  target.append(definitionList(pick(project, PROJECT_FIELDS)));
}

export function renderAgent(target, agent) {
  clear(target);
  target.append(element("h3", null, "Agent"));
  target.append(definitionList(pick(agent, AGENT_FIELDS)));
}

export function renderCandidate(target, candidate) {
  clear(target);
  target.append(element("h3", null, "Candidate"));
  target.append(definitionList(pick(candidate, CANDIDATE_FIELDS)));
  target.append(element("h4", null, "Metadata"));
  target.append(definitionList(pick(candidate.metadata, CANDIDATE_METADATA_FIELDS)));
}

export function renderRun(target, run) {
  clear(target);
  target.append(element("h3", null, "Run"));
  target.append(definitionList(pick(run, RUN_FIELDS)));
}

export function renderProgress(target, progress) {
  clear(target);
  target.append(definitionList(pick(progress, PROGRESS_FIELDS)));
}

// renderSnapshot shows the authoritative pair together.
export function renderSnapshot(target, run, progress) {
  clear(target);
  const grid = element("div", "grid");
  const left = element("div");
  left.append(element("h4", null, "Run"));
  left.append(definitionList(pick(run, RUN_FIELDS)));
  const right = element("div");
  right.append(element("h4", null, "Progress"));
  right.append(definitionList(pick(progress, PROGRESS_FIELDS)));
  grid.append(left, right);
  target.append(grid);
}

// behaviorSummary renders a descriptor as one compact line.
function behaviorSummary(behavior) {
  if (behavior === undefined || behavior === null || typeof behavior !== "object") {
    return "—";
  }
  const parts = [];
  for (const key of BEHAVIOR_FIELDS) {
    const value = behavior[key];
    if (value !== undefined && value !== null && value !== "") {
      parts.push(String(value));
    }
  }
  return parts.length === 0 ? "—" : parts.join(" · ");
}

// renderObservationRows renders the bounded live viewport.
export function renderObservationRows(target, rows) {
  clear(target);
  if (rows.length === 0) {
    target.append(emptyState("No observations on this stream."));
    return;
  }
  const headers = ["Seq", "New", "Decision", "Risk", "Trust", "Anomaly", "Conf.", "Behavior"];
  const body = [];
  // Newest first: the interesting end of a viewport is the recent end.
  for (let i = rows.length - 1; i >= 0; i -= 1) {
    const row = rows[i];
    body.push([
      display(row.sequence),
      display(row.new_behavior),
      display(row.decision),
      display(row.risk_level),
      display(row.trust_score),
      display(row.anomaly_score),
      display(row.anomaly_confidence),
      behaviorSummary(row.behavior),
    ]);
  }
  target.append(table(headers, body, `Showing ${rows.length} of at most 100 rows`));
}

// ---------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------

// renderGate shows the server's verdict and all five checks.
//
// The verdict is response.gate.verdict, read and displayed. Nothing here
// compares an actual against a bound to decide it — the arithmetic belongs to
// task 056, once, and a second implementation in a browser would be a rule
// that can disagree with the one that matters.
export function renderGate(target, gate, options = {}) {
  const verdict = typeof gate.verdict === "string" ? gate.verdict : "";
  const upper = verdict.toUpperCase();

  const banner = element("p", "gate");
  // Not colour alone: the mark and the word both carry it, so the state
  // survives a monochrome display and a colour-blind reader.
  banner.append(element("span", "gate-mark", upper === "PASS" ? "✓" : "✗"));
  banner.append(element("span", "gate-word", `Gate: ${upper === "" ? "—" : upper}`));
  banner.classList.add(upper === "PASS" ? "gate-pass" : "gate-fail");
  target.append(banner);

  // A control per check, when a caller supplied one. Offered on every check
  // rather than only the failing ones: which checks have evidence behind them is
  // the control plane's answer, and hiding the control where this page guessed
  // there was nothing to see would be that answer moved into a browser.
  const resolve = typeof options.onResolveCheck === "function"
    ? options.onResolveCheck
    : null;

  const headers = ["Check", "Actual", "Bound", "Result"];
  if (resolve !== null) {
    headers.push("Evidence");
  }

  const rows = [];
  for (const check of GATE_CHECKS) {
    const value = gate[check.key];
    if (value === undefined || value === null) {
      const absent = [check.label, "—", "—", "—"];
      if (resolve !== null) {
        absent.push("—");
      }
      rows.push(absent);
      continue;
    }
    const row = [
      check.label,
      display(value.actual),
      display(value[check.bound]),
      value.passed === true ? "pass" : "fail",
    ];
    if (resolve !== null) {
      row.push(resolve(check.key, check.label));
    }
    rows.push(row);
  }
  target.append(table(headers, rows, "Gate checks"));
}

export function renderDiff(target, diff, options = {}) {
  target.append(element("h4", null, "Behavior diff"));
  target.append(definitionList(pick(diff, DIFF_FIELDS)));

  const deltas = Array.isArray(diff.deltas) ? diff.deltas : [];
  if (deltas.length === 0) {
    target.append(emptyState("No behavioral deltas."));
    return;
  }

  // One or more controls per delta, when a caller supplied a builder. A shared
  // behavior gets one per side and no default — see evidence.js, which builds
  // them, for why defaulting to the candidate is the failure this whole path
  // exists to avoid.
  const controls = typeof options.deltaControls === "function"
    ? options.deltaControls
    : null;

  const headers = ["Change", "Fingerprint", "Behavior", "Reference", "Candidate"];
  if (controls !== null) {
    headers.push("Evidence");
  }

  const rows = deltas.map((delta) => {
    const row = [
      display(delta.change),
      display(delta.fingerprint_id),
      behaviorSummary(delta.behavior),
      display(delta.reference_observations),
      display(delta.candidate_observations),
    ];
    if (controls !== null) {
      row.push(controls(delta));
    }
    return row;
  });
  target.append(table(headers, rows, `${deltas.length} delta(s)`));
}

// rate renders one count/total pair as text.
function rate(value) {
  if (value === undefined || value === null || typeof value !== "object") {
    return "—";
  }
  return `${display(value.count)} / ${display(value.total)}`;
}

export function renderScorecard(target, scorecard) {
  target.append(element("h4", null, "Scorecard"));

  const decisions = scorecard.decisions;
  if (decisions !== undefined && decisions !== null) {
    const rows = DECISION_CLASSES.map((name) => [
      labelFor(name) === name ? name : labelFor(name),
      rate(decisions[name] ? decisions[name].reference : null),
      rate(decisions[name] ? decisions[name].candidate : null),
    ]);
    target.append(table(["Decision", "Reference", "Candidate"], rows, "Decisions"));
  }

  const risks = scorecard.risks;
  if (risks !== undefined && risks !== null) {
    const rows = RISK_CLASSES.map((name) => [
      name,
      rate(risks[name] ? risks[name].reference : null),
      rate(risks[name] ? risks[name].candidate : null),
    ]);
    target.append(table(["Risk", "Reference", "Candidate"], rows, "Risk levels"));
  }

  const metrics = scorecard.metrics;
  if (metrics !== undefined && metrics !== null) {
    const rows = METRIC_NAMES.map((name) => {
      const metric = metrics[name];
      if (metric === undefined || metric === null) {
        return [name, "—", "—", "—"];
      }
      // mean_known is honoured rather than ignored: an unknown mean is absent,
      // and showing 0 would be a measurement nobody took.
      const referenceMean = metric.reference && metric.reference.mean_known === true
        ? display(metric.reference.mean)
        : "—";
      const candidateMean = metric.candidate && metric.candidate.mean_known === true
        ? display(metric.candidate.mean)
        : "—";
      const delta = metric.mean_delta_known === true ? display(metric.mean_delta) : "—";
      return [name, referenceMean, candidateMean, delta];
    });
    target.append(
      table(["Metric", "Reference mean", "Candidate mean", "Δ mean"], rows, "Metrics"),
    );
  }
}

export function renderComparison(target, response, options = {}) {
  clear(target);
  target.append(
    element("p", "note-inline", `Reference ${display(response.reference_run_id)} → candidate ${display(response.candidate_run_id)}`),
  );
  if (response.gate !== undefined && response.gate !== null) {
    renderGate(target, response.gate, options);
  }
  if (response.behavior_diff !== undefined && response.behavior_diff !== null) {
    renderDiff(target, response.behavior_diff, options);
  }
  if (response.scorecard !== undefined && response.scorecard !== null) {
    renderScorecard(target, response.scorecard);
  }
}

// ---------------------------------------------------------------------
// Promotions (task 066)
// ---------------------------------------------------------------------

// environmentPosition renders one decision-time environment snapshot.
//
// The rank is displayed because the server recorded it, not because this page
// does anything with it. Nothing here compares two ranks: which environment
// may promote toward which is `CanPromote`, it lives in the platform, and the
// browser only ever reads the answer.
function environmentPosition(position) {
  if (position === undefined || position === null || typeof position !== "object") {
    return "—";
  }
  return `${display(position.ref)} (rank ${display(position.rank)}, revision ${display(position.revision)})`;
}

// renderPromotion renders one recorded decision.
//
// The wording is factual and deliberately narrow. `accepted` and `rejected`
// are the platform's two outcomes; neither means deployed, released, safe or
// unsafe, and this page says so in as many words rather than leaving a reader
// to assume. The gate verdict is shown beside the outcome because they are
// different statements about different things.
export function renderPromotion(target, promotion) {
  clear(target);

  const outcome = typeof promotion.outcome === "string" ? promotion.outcome : "";
  const banner = element("p", "gate");
  // Not colour alone: the mark and the word both carry the state.
  banner.append(element("span", "gate-mark", outcome === "accepted" ? "✓" : "✗"));
  banner.append(element(
    "span", "gate-word",
    `Decision: ${outcome === "" ? "—" : outcome}`,
  ));
  banner.classList.add(outcome === "accepted" ? "gate-pass" : "gate-fail");
  target.append(banner);

  target.append(element(
    "p", "note-inline",
    "Trustvian recorded this decision. Nothing was deployed, and this says nothing about where the candidate is running.",
  ));

  target.append(definitionList([
    ["Promotion", display(promotion.id)],
    ["Project", display(promotion.project_id)],
    ["Candidate", display(promotion.candidate_id)],
    ["Reference run", display(promotion.reference_run_id)],
    ["Candidate run", display(promotion.candidate_run_id)],
    ["From", environmentPosition(promotion.source_environment)],
    ["To", environmentPosition(promotion.target_environment)],
    ["Decided at", display(promotion.decided_at)],
  ]));

  if (promotion.gate_result !== undefined && promotion.gate_result !== null) {
    target.append(element("h4", null, "Gate result recorded with this decision"));
    target.append(element(
      "p", "note-inline",
      "The result the decision consumed, as it was recorded. It is not recomputed on read.",
    ));
    renderGate(target, promotion.gate_result);
  }
}

// runLink builds the control that opens one evaluation run.
//
// A button rather than an anchor, and that is the point: the promotion record
// is not the source of truth for the run it names. Activating this re-reads
// GET /v1/evaluation-runs/{id} through the page's existing run path, so what
// is shown is the run's own current state rather than a copy frozen into a
// decision. There is no href, no navigation and no external origin.
//
// An absent identifier yields plain text, not a control that would fail.
function runLink(runID, onOpenRun) {
  if (typeof runID !== "string" || runID === "") {
    return display(runID);
  }
  if (typeof onOpenRun !== "function") {
    return runID;
  }

  const wrapper = element("span", "run-cell");
  wrapper.append(element("span", "run-id", runID));

  const button = element("button", "link-button", "Open");
  button.type = "button";
  // The label alone reads as "Open" out of context, so the accessible name
  // carries which run it opens.
  button.setAttribute("aria-label", `Open evaluation run ${runID}`);
  button.addEventListener("click", () => onOpenRun(runID));
  wrapper.append(button);
  return wrapper;
}

// renderPromotionList renders one bounded page of history.
//
// Server order is preserved exactly. Re-sorting an audit history in the
// browser would present a sequence the platform did not record.
//
// One page, and only one: the caller decides whether to ask for another. This
// never accumulates pages, so the memory a history costs is a page rather than
// a project's whole audit trail.
//
// `options.onOpenRun` is optional. When supplied, each row's two run
// identifiers become controls that open the run through the page's existing
// run path — a promotion is only investigable if its evidence is reachable
// from it.
export function renderPromotionList(target, response, options) {
  clear(target);

  const onOpenRun = options && typeof options.onOpenRun === "function"
    ? options.onOpenRun
    : undefined;
  const rows = Array.isArray(response.promotions) ? response.promotions : [];
  if (rows.length === 0) {
    target.append(emptyState("No promotion decisions recorded for this project."));
    return;
  }

  target.append(table(
    ["Promotion", "Outcome", "From", "To", "Gate", "Reference run", "Candidate run", "Decided at"],
    rows.map((row) => [
      display(row.id),
      display(row.outcome),
      display(row.source_environment ? row.source_environment.ref : undefined),
      display(row.target_environment ? row.target_environment.ref : undefined),
      display(row.gate_result ? row.gate_result.verdict : undefined),
      runLink(row.reference_run_id, onOpenRun),
      runLink(row.candidate_run_id, onOpenRun),
      display(row.decided_at),
    ]),
    "Recorded promotion decisions",
  ));
}

// renderPromotionPageState describes where in the history this page sits.
//
// Separate from the rows because the continuation controls are part of the
// shell rather than of any page, and because a reload forgets the position —
// nothing about it is stored in the browser.
export function renderPromotionPageState(target, pageNumber, hasMore) {
  clear(target);
  target.append(document.createTextNode(
    hasMore
      ? `Page ${pageNumber}. More decisions exist.`
      : `Page ${pageNumber}. End of history.`,
  ));
}

// renderEnvironmentOptions fills a select with a project's environments.
//
// Every environment the API returned is offered. Which ones are valid targets
// is the server's decision, and a promotion toward an invalid one is refused
// with its own message — filtering here would mean comparing ranks in the
// browser, which is exactly what must not happen.
export function renderEnvironmentOptions(select, collection) {
  clear(select);
  // The whole collection, assembled by api.listAllEnvironments from however
  // many bounded pages the server needed. A migrated project may hold more
  // than the creation cap, so the target on page 2 has to be offered like any
  // other.
  const rows = Array.isArray(collection.environments) ? collection.environments : [];

  const placeholder = element("option", null, rows.length === 0
    ? "No environments in this project"
    : "Choose a target environment");
  placeholder.value = "";
  select.append(placeholder);

  for (const row of rows) {
    if (typeof row.ref !== "string" || row.ref === "") {
      continue;
    }
    const rank = row.rank === undefined || row.rank === null ? "unranked" : `rank ${row.rank}`;
    const option = element("option", null, `${row.ref} — ${display(row.name)} (${rank}, ${display(row.status)})`);
    option.value = row.ref;
    select.append(option);
  }
}
