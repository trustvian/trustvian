// What the Overview computes, and only that (task 099).
//
// Every function here is a pure reduction over rows the server returned, with
// the scope of the answer written into its result: a count over one page says
// it is a count over one page. Nothing here combines readings into a score,
// ranks a candidate, or turns an absence into a zero.
//
// No DOM and no requests, so each reduction is tested as arithmetic.

// RUN_STATUSES are the lifecycle states a run row reports, in lifecycle
// order. A status this page does not know is counted under its own word
// rather than folded into one it does.
export const RUN_STATUSES = Object.freeze(["created", "running", "completed", "failed", "cancelled"]);

// statusBreakdown counts the rows on one page by status.
//
// Returns [{ status, count }] in lifecycle order, then any unknown statuses
// in the order first seen, and the total. Statuses with no rows are omitted:
// a zero bar for a status nothing reported would be a figure nobody measured.
export function statusBreakdown(rows) {
  const counts = new Map();
  for (const row of Array.isArray(rows) ? rows : []) {
    const status = typeof row.status === "string" && row.status !== "" ? row.status : "not reported";
    counts.set(status, (counts.get(status) || 0) + 1);
  }
  const ordered = [];
  for (const status of RUN_STATUSES) {
    if (counts.has(status)) {
      ordered.push({ status, count: counts.get(status) });
      counts.delete(status);
    }
  }
  for (const [status, count] of counts) {
    ordered.push({ status, count });
  }
  const total = ordered.reduce((sum, entry) => sum + entry.count, 0);
  return { entries: ordered, total };
}

// timeOf reads an RFC 3339 timestamp as milliseconds, or null.
//
// Parsed rather than compared as text: the server trims trailing fractional
// zeros, so two timestamps do not sort lexically (ADR 0041 § 2).
function timeOf(value) {
  if (typeof value !== "string" || value === "") {
    return null;
  }
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? null : ms;
}

// newestFirst orders one page of runs by creation time, newest first, ties
// broken by identifier so the order is deterministic. A run with no creation
// time sorts last rather than being given one.
export function newestFirst(rows) {
  return (Array.isArray(rows) ? rows : []).slice().sort((a, b) => {
    const ta = timeOf(a.created_at);
    const tb = timeOf(b.created_at);
    if (ta !== tb) {
      if (ta === null) {
        return 1;
      }
      if (tb === null) {
        return -1;
      }
      return tb - ta;
    }
    return String(a.id) < String(b.id) ? -1 : (String(a.id) > String(b.id) ? 1 : 0);
  });
}

const DECIMAL = /^(0|[1-9][0-9]*)$/;

// compareDecimal orders two canonical decimal strings numerically without
// converting either to a JavaScript number, which cannot hold a uint64.
export function compareDecimal(a, b) {
  const okA = typeof a === "string" && DECIMAL.test(a);
  const okB = typeof b === "string" && DECIMAL.test(b);
  if (!okA || !okB) {
    return okA === okB ? 0 : (okA ? 1 : -1);
  }
  if (a.length !== b.length) {
    return a.length - b.length;
  }
  return a < b ? -1 : (a > b ? 1 : 0);
}

// decimalRatio is value / max as a fraction in [0, 1], for drawing a bar.
//
// A drawing aid and nothing else: the exact decimal is always printed beside
// the bar. Computed from the leading nine digits of each, aligned to the
// larger's length, by digit accumulation — never by coercing the counter —
// so it is exact for every value a page will realistically hold and
// monotonic beyond.
export function decimalRatio(value, max) {
  if (typeof value !== "string" || typeof max !== "string"
    || !DECIMAL.test(value) || !DECIMAL.test(max) || max === "0") {
    return 0;
  }
  if (compareDecimal(value, max) >= 0) {
    return 1;
  }
  const width = max.length;
  const padded = "0".repeat(width - value.length) + value;
  const take = Math.min(width, 9);
  const lead = (text) => {
    let n = 0;
    for (const ch of text.slice(0, take)) {
      n = n * 10 + (ch.charCodeAt(0) - 48);
    }
    return n;
  };
  const top = lead(max);
  return top === 0 ? 0 : lead(padded) / top;
}

// topBehaviors orders one page of behaviors by their authoritative
// observation count, highest first, and keeps `limit` of them. The count is
// the server's per-behavior figure, never the rows drawn.
export function topBehaviors(rows, limit) {
  return (Array.isArray(rows) ? rows : [])
    .slice()
    .sort((a, b) => {
      const byCount = compareDecimal(b.observations, a.observations);
      if (byCount !== 0) {
        return byCount;
      }
      return String(a.fingerprint_id) < String(b.fingerprint_id) ? -1 : 1;
    })
    .slice(0, limit);
}

// verdictTally counts recorded promotion outcomes on one page, in the
// server's own words. Unknown outcomes are kept under their word.
export function verdictTally(rows) {
  const counts = new Map();
  for (const row of Array.isArray(rows) ? rows : []) {
    const outcome = typeof row.outcome === "string" && row.outcome !== "" ? row.outcome : "not reported";
    counts.set(outcome, (counts.get(outcome) || 0) + 1);
  }
  return Array.from(counts, ([outcome, count]) => ({ outcome, count }));
}

// readClock states when a panel was read, as a wall-clock time.
//
// Absolute rather than "5s ago": nothing on this page runs a timer, so a
// relative age would be true for one second and then quietly wrong for as
// long as the page stayed open.
export function readClock(readAt) {
  if (typeof readAt !== "number" || readAt <= 0) {
    return "not read yet";
  }
  const at = new Date(readAt);
  const pad = (n) => String(n).padStart(2, "0");
  return `read at ${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`;
}
