# 0066 — A per-target call limit is one named check over a bounded list

**Status:** Accepted (task [106](../tasks/v0.12/106-frequency-evidence.md),
maintainer decisions D1 and D2). Narrowly amends
[ADR 0029](0029-hard-gates-use-explicit-integer-evidence.md) § 5 for one gate,
and changes nothing else in it.

## Context

Task 106 adds `max_calls_per_run`: per named target, the most calls any one
candidate repetition may make. "The knowledge base now gets 2.4× the traffic"
is the change a developer most wants to stop before it ships. The limit only
means something per target, and the targets are the developer's own hosts.

ADR 0029 § 5 rejects `map[string]threshold` and any gate over fields whose
semantics have not been approved. The concern is real. A string-keyed threshold
map is the first step toward a rule DSL: policy moves into an unbounded
surface, and a key can name a field nobody approved a gate over.

## Decision

`max_calls_per_run` is accepted as the one exception, in exactly this shape:

1. **The semantics are fixed and approved here.** The check value is "calls to
   this target in one candidate repetition", as the maximum over the
   repetitions. That is the same per-run maximum `max_block_decisions_per_run`
   uses, never a sum or a mean. The target is a target *name*, summed across
   target categories. No other field can be named. The key selects a target;
   it does not select a measurement.
2. **The list is bounded and validated.** It has 1 to 16 entries with unique,
   non-empty, printable targets of at most 255 bytes, and an integer maximum
   each. It is validated where the scenario is parsed and again by the control
   plane. An empty list, a duplicate or a 17th entry is refused rather than
   truncated.
3. **It is one named check.** The gate reports `max_calls_per_run` once, with
   each target's actual value, limit and outcome in configuration order. The
   check fails if any target fails.
4. **A target in neither side's evidence is `not_observed`, and fails the
   check.** Otherwise a misspelled target, the most likely configuration
   mistake, would pass silently and look like a limit that held.
5. **It is optional, like task 106's other two limits** (issue 131's
   precedent). Omitted means not evaluated, and the verdict is exactly what it
   was. With the evidence absent — no completed candidate repetition — the
   check is `deferred` and fails the verdict, naming the missing evidence
   (decision D1). It is never computed from a substitute.

Nothing else in § 5 changes. There is still no expression language, operator
string, field-name string or `[]GateRule`, and a second keyed limit needs its
own decision record.

## Alternatives considered

- **Wait for a per-target limit until a rule language exists.** That is the
  surface § 5 exists to prevent. The bounded list gives the one question a
  developer asks without opening it.
- **Treat `not_observed` as a pass.** A typo would then read as compliance,
  which is the failure ADR 0029 § 7 calls worse than a gate's absence.
- **A separate named gate per target.** The check set would then grow with
  configuration, which is what "a fixed gate set" rules out. One named check
  with ordered evidence keeps the set fixed.

## Consequences

- The repeated gate's checks gain a sibling field, `gate.frequency_checks`,
  with three entries in fixed order. It is a sibling because `checks` is
  exactly six and its published consumers refuse a seventh.
- A scenario can now fail on a target it names but never reaches. That is
  intended, and the output says `not_observed`.
- `max_llm_calls_per_run` is *not* covered by this record. It needs the
  per-behavior layer that task 081 persists, and is specified there (decision
  D3).
