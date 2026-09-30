// Honest rendering of recorded correlation, duration and status (task 076).
//
// Everything in this file is a pure function over the fields task 067 retained
// and task 084 put on the record. It computes no score, no verdict and no
// judgement; what it computes is *shape* — which retained parent reference
// points at something on this page — and it is separated out precisely so the
// shape can be tested against every degenerate parent reference a producer can
// emit without a browser.
//
// Three claims this file refuses to make, each of which a tree is tempting to
// make anyway:
//
//  1. **Parentage is never inferred.** Only `parent_span_id` establishes it.
//     Timestamps, sequence adjacency, operation names and ingestion order
//     establish nothing, and task 084 forbids reading them as if they did.
//  2. **A parent this page has not got is not a root.** A trace page is
//     bounded, a parent may have been sampled away, and a child may be
//     retained while its parent never was. All of those are distinct from
//     "the producer said this is a trace root", and collapsing them would
//     invent a tree that is shallower than the invocation was.
//  3. **Unavailable is not zero, and unset is not success.** Task 084 made
//     both distinctions on the record, and a renderer that flattened them
//     would let a run of entirely unstatused spans read as a clean one.
//
// Nothing here says why a model did anything. Span order is arrival order and
// structure is call structure; neither is reasoning, intent or plan, and no
// label below contains a causal connective.

// ---------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------

// TRACE_MAX_DEPTH bounds how deeply a tree is indented.
//
// A bound rather than a trust: a producer can emit an arbitrarily long parent
// chain, and indentation that followed it would push a row off the screen. At
// the bound the row is still drawn, still says what it is, and is marked as
// clamped — the same rule the live graph applies to its own saturation.
export const TRACE_MAX_DEPTH = 24;

// ---------------------------------------------------------------------
// Parent states
// ---------------------------------------------------------------------

// PARENT_STATES is every way a retained parent reference can relate to the page
// it was read with.
//
// Six values, and the reason there are six rather than two is that each of the
// last four looks exactly like one of the first two in a payload. A tree drawn
// from two states would have to pick one of the readings, and every choice is
// wrong about some real trace.
export const PARENT_STATES = Object.freeze({
  // The producer said this span starts the trace.
  ROOT: "root",
  // The parent reference names a span on this page, unambiguously.
  CHILD: "child",
  // The parent reference names nothing on this page. It may never have been
  // retained, it may have been sampled away, or it may be on a page not read.
  // Which of those is not knowable from here, and claiming one would be a
  // fact this page does not hold.
  UNRESOLVED: "unresolved",
  // Two or more retained observations carry the parent's span id, so the
  // reference does not identify one of them. A span id is unique only inside
  // its trace and nothing enforces that, which is why task 067 refused to make
  // one an identity.
  AMBIGUOUS: "ambiguous",
  // The span names itself as its parent.
  SELF: "self",
  // Following the parent chain returns to this span.
  CYCLE: "cycle",
  // The producer said "child" and named no parent, or established no lineage
  // at all. Distinct from ROOT: task 084 states that neither source format can
  // express "the producer does not know", so an unstated lineage means the
  // event did not come from a span rather than that it began the trace.
  UNSTATED: "unstated",
});

// PARENT_STATE_TEXT is the sentence each state renders as.
//
// Factual and deliberately flat. None of them explains why anything happened,
// and none of them contains "because", "decided to", "then chose" or any other
// connective that would turn observed structure into asserted intent.
const PARENT_STATE_TEXT = new Map([
  [PARENT_STATES.ROOT, "trace root, as recorded"],
  [PARENT_STATES.CHILD, "child of the span above"],
  [
    PARENT_STATES.UNRESOLVED,
    "parent not on this page — it may not have been retained, may have been " +
      "sampled away, or may be outside the rows read here",
  ],
  [
    PARENT_STATES.AMBIGUOUS,
    "parent span id is carried by more than one retained observation, so the " +
      "reference does not identify one of them",
  ],
  [PARENT_STATES.SELF, "recorded parent is this span itself"],
  [PARENT_STATES.CYCLE, "recorded parent chain returns to this span"],
  [
    PARENT_STATES.UNSTATED,
    "no parent recorded, and the lineage does not say this was a trace root",
  ],
]);

// parentStateText renders one state, or a refusal for a value it does not know.
export function parentStateText(state) {
  if (PARENT_STATE_TEXT.has(state)) {
    return PARENT_STATE_TEXT.get(state);
  }
  return "parent reference reported as a value this page does not recognize";
}

// ---------------------------------------------------------------------
// The tree
// ---------------------------------------------------------------------

// spanIndex maps each span id to the rows carrying it.
//
// A list per key rather than a single row, because nothing guarantees span ids
// are distinct — not the producer, and deliberately not task 067, which made
// `(run_id, sequence)` the identity for exactly this reason. Two rows under one
// key is the AMBIGUOUS state, not a state to resolve by taking the first.
function spanIndex(rows) {
  const index = new Map();
  for (let i = 0; i < rows.length; i += 1) {
    const span = typeof rows[i].span_id === "string" ? rows[i].span_id : "";
    if (span === "") {
      continue;
    }
    const existing = index.get(span);
    if (existing === undefined) {
      index.set(span, [i]);
    } else {
      existing.push(i);
    }
  }
  return index;
}

// classify decides one row's parent state and, when there is one, its parent.
//
// Returns the resolved parent index only for CHILD. Every other state leaves it
// at -1, which is what makes the row lay out at the top level while still
// saying that it is not a root.
function classify(row, selfIndex, index) {
  const span = typeof row.span_id === "string" ? row.span_id : "";
  const parent = typeof row.parent_span_id === "string" ? row.parent_span_id : "";
  const lineage = typeof row.span_lineage === "string" ? row.span_lineage : "";

  if (parent === "") {
    if (lineage === "root") {
      return { state: PARENT_STATES.ROOT, parentIndex: -1 };
    }
    // "child" with no parent id, and an unstated lineage, are both "this page
    // cannot place the row", and neither is a root.
    return { state: PARENT_STATES.UNSTATED, parentIndex: -1 };
  }
  if (span !== "" && parent === span) {
    return { state: PARENT_STATES.SELF, parentIndex: -1 };
  }

  const carriers = index.get(parent);
  if (carriers === undefined) {
    return { state: PARENT_STATES.UNRESOLVED, parentIndex: -1 };
  }
  if (carriers.length > 1) {
    return { state: PARENT_STATES.AMBIGUOUS, parentIndex: -1 };
  }
  if (carriers[0] === selfIndex) {
    // The only carrier is this row, which the span comparison above already
    // covers unless the span id is empty. Treated as self rather than as a
    // parent pointing at itself.
    return { state: PARENT_STATES.SELF, parentIndex: -1 };
  }
  return { state: PARENT_STATES.CHILD, parentIndex: carriers[0] };
}

// breakCycles turns any node whose parent chain returns to it into a top-level
// node marked CYCLE.
//
// Walked with a step bound of the row count rather than with a visited set per
// node: a chain longer than the page cannot be a chain inside the page, so the
// bound is exact and the walk cannot run away on a malicious reference graph.
function breakCycles(nodes) {
  for (let i = 0; i < nodes.length; i += 1) {
    let cursor = nodes[i].parentIndex;
    let steps = 0;
    while (cursor >= 0 && steps <= nodes.length) {
      if (cursor === i) {
        nodes[i].state = PARENT_STATES.CYCLE;
        nodes[i].parentIndex = -1;
        break;
      }
      cursor = nodes[cursor].parentIndex;
      steps += 1;
    }
    if (cursor >= 0 && steps > nodes.length) {
      // A chain longer than the page can only mean the walk is going round
      // something. Refused the same way rather than drawn.
      nodes[i].state = PARENT_STATES.CYCLE;
      nodes[i].parentIndex = -1;
    }
  }
}

// buildTraceTree orders one page of a trace's observations as a tree.
//
// **The output holds every input row exactly once**, which is the property that
// matters most: a tree that dropped a row whose parent it could not place would
// hide evidence, and one that repeated a row would overstate how much there is.
// Rows it cannot place appear at the top level carrying the state that says so.
//
// Order is a pre-order walk from the top-level rows in ingest-sequence order,
// which is the order task 067 retained them in. It is not a timeline: a parent
// span ends after the children it started, so arrival order and wall-clock
// order differ by design.
export function buildTraceTree(observations) {
  const rows = Array.isArray(observations) ? observations : [];
  const index = spanIndex(rows);

  const nodes = rows.map((row, i) => {
    const { state, parentIndex } = classify(row, i, index);
    const carriers = index.get(typeof row.span_id === "string" ? row.span_id : "");
    return {
      observation: row,
      state,
      parentIndex,
      // A duplicated span id is worth stating on the row that carries it, not
      // only on a child that could not resolve through it.
      duplicateSpan: carriers !== undefined && carriers.length > 1,
      children: [],
    };
  });

  breakCycles(nodes);

  for (let i = 0; i < nodes.length; i += 1) {
    if (nodes[i].parentIndex >= 0) {
      nodes[nodes[i].parentIndex].children.push(i);
    }
  }

  const out = [];
  const placed = new Array(nodes.length).fill(false);

  // An explicit stack rather than recursion: the depth is producer-controlled,
  // and a page of 64 rows chained end to end is enough to make recursion a
  // question about the JavaScript stack rather than about the evidence.
  const walk = (start) => {
    const stack = [{ i: start, depth: 0 }];
    while (stack.length > 0) {
      const { i, depth } = stack.pop();
      if (placed[i]) {
        continue;
      }
      placed[i] = true;
      out.push({
        observation: nodes[i].observation,
        state: nodes[i].state,
        duplicateSpan: nodes[i].duplicateSpan,
        depth: Math.min(depth, TRACE_MAX_DEPTH),
        depthClamped: depth > TRACE_MAX_DEPTH,
      });
      // Reversed, so children are emitted in ingest order once popped.
      for (let c = nodes[i].children.length - 1; c >= 0; c -= 1) {
        stack.push({ i: nodes[i].children[c], depth: depth + 1 });
      }
    }
  };

  for (let i = 0; i < nodes.length; i += 1) {
    if (nodes[i].parentIndex < 0) {
      walk(i);
    }
  }
  // Defensive: anything the walk did not reach is still evidence and is still
  // shown. Nothing should land here once cycles are broken, and a row silently
  // missing is the one outcome worth spending a loop to rule out.
  for (let i = 0; i < nodes.length; i += 1) {
    if (!placed[i]) {
      walk(i);
    }
  }
  return out;
}

// ---------------------------------------------------------------------
// Duration
// ---------------------------------------------------------------------

// DURATION_UNAVAILABLE is what a span with no measured duration renders as.
//
// Never `0`, and never "fast". Task 084 keeps a measured zero and an
// unavailable duration apart on the record because a span with no end timestamp
// did not take no time, and this is the other end of that distinction.
export const DURATION_UNAVAILABLE = "not available";

// nanosToMillisText converts canonical nanosecond text to millisecond text.
//
// String arithmetic, not numeric. A nanosecond count above 2^53 does not
// survive Number(), and the whole reason the API sends this field as text is
// that JavaScript cannot hold it — parsing it here to format it would undo that
// on the one client that needed the protection.
function nanosToMillisText(nanos) {
  const padded = nanos.length <= 6 ? "0".repeat(7 - nanos.length) + nanos : nanos;
  const whole = padded.slice(0, padded.length - 6);
  const fraction = padded.slice(padded.length - 6).replace(/0+$/, "");
  return fraction === "" ? whole : `${whole}.${fraction}`;
}

// formatDuration renders one observation's recorded duration.
//
// `duration_nanos` is absent exactly when nothing measured a duration, and
// present as "0" for a measured zero — the encoding the record uses, preserved
// rather than flattened.
export function formatDuration(observation) {
  const nanos = observation === null || observation === undefined
    ? undefined
    : observation.duration_nanos;
  if (typeof nanos !== "string" || nanos === "") {
    return { measured: false, text: DURATION_UNAVAILABLE };
  }
  if (!/^(0|[1-9][0-9]*)$/.test(nanos)) {
    // A value this contract would not have produced. Reported as unreadable
    // rather than coerced: coercing it would put a number on screen that no
    // observation recorded.
    return { measured: false, text: "recorded duration is not readable" };
  }
  if (nanos === "0") {
    return { measured: true, text: "0 ms (measured)" };
  }
  return { measured: true, text: `${nanosToMillisText(nanos)} ms` };
}

// ---------------------------------------------------------------------
// Span status
// ---------------------------------------------------------------------

// SPAN_STATUS_TEXT is task 084's four values, rendered without promoting any of
// them to success.
//
// OpenTelemetry's `UNSET` means the producer expressed no opinion and most
// instrumentation never sets `OK` at all, so counting either as success would
// let an entirely unstatused run read as a clean one. Only "ok" is success and
// only "error" is failure; the other two say what they are.
const SPAN_STATUS_TEXT = new Map([
  ["", "not available"],
  ["unset", "unset — the producer stated no status"],
  ["ok", "ok"],
  ["error", "error"],
]);

// spanStatusLabel renders one recorded status.
export function spanStatusLabel(observation) {
  const status = observation === null || observation === undefined
    ? ""
    : observation.span_status;
  const key = typeof status === "string" ? status : "";
  if (SPAN_STATUS_TEXT.has(key)) {
    return SPAN_STATUS_TEXT.get(key);
  }
  return "status reported as a value this page does not recognize";
}

// spanStatusClass maps a status to a style hook, with no default to success.
export function spanStatusClass(observation) {
  const status = observation === null || observation === undefined
    ? ""
    : observation.span_status;
  switch (status) {
    case "ok":
      return "status-ok";
    case "error":
      return "status-error";
    case "unset":
      return "status-unset";
    default:
      return "status-unavailable";
  }
}

// ---------------------------------------------------------------------
// Behavioral labels
// ---------------------------------------------------------------------

// NEW_TO_RUN and SEEN_IN_RUN are what `new_behavior` means, spelled out.
//
// It is whether this fingerprint had appeared in *this run* before this
// observation — the value the ingest path derived from the run's own snapshot.
// It is **not** novelty against a learned baseline, which is the anomaly score's
// question and a different measurement; labelling it "novel" or "unknown
// behavior" would answer the second with the first.
export const NEW_TO_RUN = "new to this run";
export const SEEN_IN_RUN = "already seen in this run";

// newBehaviorLabel renders the recorded new-to-this-run state.
export function newBehaviorLabel(observation) {
  const value = observation === null || observation === undefined
    ? undefined
    : observation.new_behavior;
  if (value === true) {
    return NEW_TO_RUN;
  }
  if (value === false) {
    return SEEN_IN_RUN;
  }
  return "not recorded";
}

// FIDELITY_NOT_RETAINED is why no historical view states a fidelity.
//
// Fidelity and behavioral layer travel beside a record at ingest and on the
// realtime stream, and task 067 retained neither — there is no column for them,
// so a page of retained history genuinely does not know whether an operation's
// identity came from agent-oriented telemetry or from its transport.
//
// The Live view still states fidelity, because realtime carries it. Historical
// views say this instead, which is the honest answer: inferring semantics from
// the shape of a descriptor is the fabrication task 075 refuses for operation
// names, and it would be the same fabrication here.
export const FIDELITY_NOT_RETAINED =
  "Fidelity and behavioral layer were not retained with this history, so this " +
  "view states neither. Operation and target are shown exactly as the " +
  "observation recorded them.";

// SEQUENCE_DEVIATION_NOT_RETAINED is why no historical view states one.
//
// The engine's sequence signals live in the anomaly contributors, and task 067
// deliberately excluded `Contributors` from the retained row — it is the one
// variable-length field on the record, and retaining it would make a row's size
// a function of producer behaviour. So nothing durable says whether an
// observation departed from a learned sequence, and this view says so rather
// than deriving a deviation from the order it happens to be reading.
export const SEQUENCE_DEVIATION_NOT_RETAINED =
  "No sequence-deviation evidence is retained per observation, so this view " +
  "makes no statement about whether the order departed from what was learned.";
