// The geometry of a trace waterfall (task 100), as pure functions.
//
// Structure comes from buildTraceTree and from nothing else: the row order,
// depth and parent state are exactly the ones the Evidence tree draws, built
// only from the recorded parent span reference. This file adds *placement* —
// where along a time axis a row's bar starts and how long it is — and that is
// all it adds.
//
// Placement uses two recorded values and states what each one means:
//
//   - `timestamp` is the producer's clock. For OTLP ingest it is the span's
//     start time; a producer posting decision records directly may stamp the
//     decision instead. Bars from different producers can be skewed by their
//     clocks, and nothing here corrects for it.
//   - `duration_nanos` is a measured duration, or absent. An absent duration
//     is drawn as a marker at the start, never as a zero-width bar, because a
//     span with no end timestamp did not take no time (task 084).
//
// Neither is ever used to infer parentage, order or cause.
//
// Counters arrive as decimal strings and are never coerced to a JavaScript
// number wholesale. Offsets and durations are accumulated digit by digit,
// which is exact up to 2^53 ns (about 104 days) and only drives drawing; the
// text a reader sees is formatted from the original string.

import { buildTraceTree, formatDuration } from "./trace.js";

const DECIMAL = /^(0|[1-9][0-9]*)$/;
const RFC3339 = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/;

// digits accumulates a string of decimal digits.
function digits(text) {
  let n = 0;
  for (const ch of text) {
    n = n * 10 + (ch.charCodeAt(0) - 48);
  }
  return n;
}

// timestampNanos reads an RFC 3339 timestamp as nanoseconds since the epoch,
// split so neither half loses precision: whole seconds, and the nanosecond
// fraction. Returns null for anything it cannot read exactly.
export function timestampNanos(value) {
  if (typeof value !== "string") {
    return null;
  }
  const parts = RFC3339.exec(value);
  if (parts === null) {
    return null;
  }
  const ms = Date.parse(`${parts[1]}${parts[3]}`);
  if (Number.isNaN(ms)) {
    return null;
  }
  const fraction = (parts[2] || "").padEnd(9, "0");
  return { seconds: Math.floor(ms / 1000), nanos: digits(fraction) };
}

// nanosBetween is b − a in nanoseconds.
function nanosBetween(a, b) {
  return (b.seconds - a.seconds) * 1e9 + (b.nanos - a.nanos);
}

// durationNanos reads a measured duration, or null when none was measured or
// the value is not canonical decimal.
export function durationNanos(observation) {
  const raw = observation === null || observation === undefined ? undefined : observation.duration_nanos;
  if (typeof raw !== "string" || !DECIMAL.test(raw)) {
    return null;
  }
  return digits(raw);
}

// buildWaterfall lays out one page of a trace's retained observations.
//
// Returns { rows, windowNanos, timed, untimed }. Each row carries the tree's
// fields plus `offset` (ns from the earliest start on the page, or null),
// `duration` (ns or null), `measured`, `left` and `width` as fractions of the
// window, and the duration text exactly as the Evidence table formats it.
export function buildWaterfall(observations) {
  const tree = buildTraceTree(observations);
  const starts = tree.map((node) => timestampNanos(node.observation.timestamp));

  let origin = null;
  for (const start of starts) {
    if (start !== null && (origin === null || nanosBetween(start, origin) > 0)) {
      origin = start;
    }
  }

  let end = 0;
  const placed = tree.map((node, index) => {
    const start = starts[index];
    const offset = start === null || origin === null ? null : nanosBetween(origin, start);
    const duration = durationNanos(node.observation);
    if (offset !== null) {
      end = Math.max(end, offset + (duration === null ? 0 : duration));
    }
    return { node, offset, duration };
  });

  // A window of zero — one instantaneous span, or every start equal and
  // nothing measured — would divide by zero. One nanosecond keeps every
  // marker at the left edge, which is where they all genuinely are.
  const windowNanos = Math.max(end, 1);
  let untimed = 0;
  const rows = placed.map(({ node, offset, duration }) => {
    if (offset === null) {
      untimed += 1;
    }
    const formatted = formatDuration(node.observation);
    return {
      observation: node.observation,
      depth: node.depth,
      depthClamped: node.depthClamped,
      state: node.state,
      duplicateSpan: node.duplicateSpan,
      offset,
      duration,
      measured: duration !== null,
      left: offset === null ? null : offset / windowNanos,
      width: offset === null || duration === null ? 0 : Math.min(1, duration / windowNanos),
      durationText: formatted.text,
    };
  });
  return { rows, windowNanos: end, timed: rows.length - untimed, untimed };
}

// offsetText renders an offset from the trace's earliest start.
export function offsetText(nanos) {
  if (nanos === null || nanos === undefined) {
    return "not available";
  }
  if (nanos < 1000) {
    return `+${nanos} ns`;
  }
  if (nanos < 1e6) {
    return `+${(nanos / 1e3).toFixed(1)} µs`;
  }
  if (nanos < 1e9) {
    return `+${(nanos / 1e6).toFixed(1)} ms`;
  }
  return `+${(nanos / 1e9).toFixed(3)} s`;
}

// filterTraces narrows a loaded list of traces by identifier, for the search
// box. Over what is loaded only; the list says when that is not everything.
export function filterTraces(traces, query) {
  const needle = String(query || "").trim().toLowerCase();
  if (needle === "") {
    return traces.slice();
  }
  return traces.filter((trace) => typeof trace.trace_id === "string"
    && trace.trace_id.toLowerCase().includes(needle));
}

// nextIndex moves a keyboard selection through `count` rows. Unlike the
// selector's popup it does not wrap: a waterfall is a document, and the
// first and last rows are edges a reader should feel.
export function nextIndex(current, count, key) {
  if (count <= 0) {
    return -1;
  }
  switch (key) {
    case "ArrowDown":
      return current < 0 ? 0 : Math.min(count - 1, current + 1);
    case "ArrowUp":
      return current < 0 ? 0 : Math.max(0, current - 1);
    case "Home":
      return 0;
    case "End":
      return count - 1;
    case "PageDown":
      return current < 0 ? 0 : Math.min(count - 1, current + 10);
    case "PageUp":
      return current < 0 ? 0 : Math.max(0, current - 10);
    default:
      return current;
  }
}
