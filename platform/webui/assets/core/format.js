// Turning a recorded value into something a column can hold.
//
// Presentation only, and two rules run through all of it:
//
//   **Absent stays absent.** A value the server did not send renders as the
//   absent marker, never as 0, never as blank. Task 053 built the aggregate
//   around the difference between unknown and zero, and a cell that showed a
//   missing value as 0 would turn "we have no evidence" into "we measured
//   none".
//
//   **A uint64 is never a Number.** Counters, sequences and durations arrive
//   as decimal strings and are formatted as strings. JavaScript has no
//   integer type that holds 18446744073709551615, and a figure that is
//   quietly wrong in its last digits is worse than a long one.

// ABSENT is what every surface prints for a value that was not sent.
export const ABSENT = "—";

// A strict RFC 3339 shape. Matching is the whole condition: a timestamp this
// does not recognise is rendered verbatim rather than reshaped on a guess.
const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/;

// shortTime abbreviates a timestamp for a dense column.
//
// The full value is never lost: the detail panel shows what the server sent,
// character for character. This is the column's abbreviation of it, and only
// of a value whose shape is known.
export function shortTime(value) {
  if (typeof value !== "string") {
    return ABSENT;
  }
  const parts = RFC3339.exec(value);
  if (parts === null) {
    return value === "" ? ABSENT : value;
  }
  return `${parts[2]}-${parts[3]} ${parts[4]}:${parts[5]}:${parts[6]}`;
}

// DURATION_UNITS picks the unit from how many digits the count has.
//
// `shift` is how far the decimal point moves left to reach that unit, and
// `keep` is how many places after it are worth showing.
const DURATION_UNITS = Object.freeze([
  Object.freeze({ digits: 4, unit: "ns", shift: 0, keep: 0 }),
  Object.freeze({ digits: 7, unit: "µs", shift: 3, keep: 1 }),
  Object.freeze({ digits: 10, unit: "ms", shift: 6, keep: 1 }),
  Object.freeze({ digits: Infinity, unit: "s", shift: 9, keep: 3 }),
]);

// durationText renders a nanosecond count in a unit that shows it.
//
// Done by moving a decimal point through the digit string, never by dividing.
//
// The unit follows the magnitude for the same reason: a 41-microsecond span
// printed as "0.000s" reads as a span that took no time, which is a different
// claim from the one the record makes.
export function durationText(nanos) {
  if (typeof nanos !== "string" || !/^\d+$/.test(nanos)) {
    return ABSENT;
  }
  const digits = nanos.replace(/^0+(?=\d)/, "");
  for (const scale of DURATION_UNITS) {
    if (digits.length >= scale.digits) {
      continue;
    }
    if (scale.shift === 0) {
      return `${digits}${scale.unit}`;
    }
    const padded = digits.padStart(scale.shift + 1, "0");
    const whole = padded.slice(0, padded.length - scale.shift);
    const fraction = padded.slice(padded.length - scale.shift, padded.length - scale.shift + scale.keep);
    return `${whole}.${fraction}${scale.unit}`;
  }
  return digits;
}

// DECISION_EDGES groups the decisions the server publishes into the three
// severities a reader can tell apart at a glance.
//
// The grouping is presentational and the word in the row is not: the cell
// always carries the decision the server returned, spelled its way. This only
// decides which of three rules runs down the row's leading edge, which tint
// sits behind it, and which mark stands beside the word.
const DECISION_EDGES = Object.freeze([
  Object.freeze({ decision: "allow", edge: "allow" }),
  Object.freeze({ decision: "observe_only", edge: "allow" }),
  Object.freeze({ decision: "alert", edge: "flag" }),
  Object.freeze({ decision: "challenge", edge: "flag" }),
  Object.freeze({ decision: "require_approval", edge: "flag" }),
  Object.freeze({ decision: "block", edge: "block" }),
]);

export function decisionEdge(decision) {
  for (const entry of DECISION_EDGES) {
    if (entry.decision === decision) {
      return entry.edge;
    }
  }
  return "";
}

export function decisionClass(decision) {
  const edge = decisionEdge(decision);
  return edge === "" ? "" : `decision-${edge}`;
}

const RISK_LEVELS = Object.freeze(["low", "medium", "high", "critical"]);

export function riskClass(level) {
  return RISK_LEVELS.includes(level) ? `risk-${level}` : "";
}

// riskIsSevere says whether a risk level is one a reader must not miss.
//
// Used to decide emphasis, never to decide anything about the record. The
// level itself is the server's and is always shown as the word it sent.
export function riskIsSevere(level) {
  return level === "high" || level === "critical";
}
