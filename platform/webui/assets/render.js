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
});

const labelFor = (key) => (Object.hasOwn(LABELS, key) ? LABELS[key] : key);

// ---------------------------------------------------------------------
// Primitives
// ---------------------------------------------------------------------

// element builds a node with optional text.
//
// The only way text enters the DOM in this application.
export function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text !== undefined && text !== null) {
    node.textContent = String(text);
  }
  return node;
}

export function clear(node) {
  // replaceChildren() with no arguments, rather than innerHTML = "". Same
  // result, and it keeps the forbidden property out of the source entirely so
  // the guard stays absolute instead of carrying an exception.
  node.replaceChildren();
}

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
      tr.append(element("td", null, value));
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
export function renderGate(target, gate) {
  const verdict = typeof gate.verdict === "string" ? gate.verdict : "";
  const upper = verdict.toUpperCase();

  const banner = element("p", "gate");
  // Not colour alone: the mark and the word both carry it, so the state
  // survives a monochrome display and a colour-blind reader.
  banner.append(element("span", "gate-mark", upper === "PASS" ? "✓" : "✗"));
  banner.append(element("span", "gate-word", `Gate: ${upper === "" ? "—" : upper}`));
  banner.classList.add(upper === "PASS" ? "gate-pass" : "gate-fail");
  target.append(banner);

  const rows = [];
  for (const check of GATE_CHECKS) {
    const value = gate[check.key];
    if (value === undefined || value === null) {
      rows.push([check.label, "—", "—", "—"]);
      continue;
    }
    rows.push([
      check.label,
      display(value.actual),
      display(value[check.bound]),
      value.passed === true ? "pass" : "fail",
    ]);
  }
  target.append(table(["Check", "Actual", "Bound", "Result"], rows, "Gate checks"));
}

export function renderDiff(target, diff) {
  target.append(element("h4", null, "Behavior diff"));
  target.append(definitionList(pick(diff, DIFF_FIELDS)));

  const deltas = Array.isArray(diff.deltas) ? diff.deltas : [];
  if (deltas.length === 0) {
    target.append(emptyState("No behavioral deltas."));
    return;
  }
  const rows = deltas.map((delta) => [
    display(delta.change),
    display(delta.fingerprint_id),
    behaviorSummary(delta.behavior),
    display(delta.reference_observations),
    display(delta.candidate_observations),
  ]);
  target.append(
    table(
      ["Change", "Fingerprint", "Behavior", "Reference", "Candidate"],
      rows,
      `${deltas.length} delta(s)`,
    ),
  );
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

export function renderComparison(target, response) {
  clear(target);
  target.append(
    element("p", "note-inline", `Reference ${display(response.reference_run_id)} → candidate ${display(response.candidate_run_id)}`),
  );
  if (response.gate !== undefined && response.gate !== null) {
    renderGate(target, response.gate);
  }
  if (response.behavior_diff !== undefined && response.behavior_diff !== null) {
    renderDiff(target, response.behavior_diff);
  }
  if (response.scorecard !== undefined && response.scorecard !== null) {
    renderScorecard(target, response.scorecard);
  }
}
