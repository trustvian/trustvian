# 0069 — A completed execution records the gate it consumed

**Status:** Accepted, 2026-10-11 (task
[107](../tasks/v0.12/107-change-impact-view-and-report.md), maintainer decision
D10). Amends [ADR 0054](0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md)
§ 1 and § 4 in one respect, stated below, and changes nothing else in them.

## Context

Task 107 needs one report per comparison, the same for the WebUI, the CLI and
the pull-request comment. A comparison has no identity: `CompareEvaluations`
and `CompareRepeatedEvaluations` derive the diff, the checks and the verdict on
demand, from limits the caller supplies. Scenario executions are the one
persisted thing a comparison belongs to (ADR 0054).

ADR 0054 § 1 stored an execution's verdict and left the limits out
deliberately (§ 4): limits are not persisted, so nothing can be inherited by a
later execution reusing the reference side. The consequence is that a recorded
verdict cannot be explained afterwards. Re-deriving the gate at read time would
need the caller to supply the same limits again, and today's code could reach a
different verdict than the one recorded. Task 066 already settled that for
promotions: a decision keeps the gate evidence it consumed.

## Decision

1. **Every completion records its gate result, in the completion
   transaction.** Schema 14 adds one nullable column, `gate_snapshot`, to
   `platform_scenario_executions` on both backends. It holds:
   - the limits the gate was evaluated under, the optional ones included;
   - every entry of `gate.checks` and `gate.frequency_checks`, with its state,
     actual value, bound, outcome, per-target outcomes and missing evidence;
   - the verdict.

   It is written by the same `UPDATE` that sets the verdict. There is no
   committed completion without it.
2. **The stored form is fixed-shape canonical text.** Every field is always
   present, in one order, with no maps; counts are canonical decimals. On
   restore the text is decoded strictly, the record is re-validated, and it is
   re-encoded: text that is not byte-identical to its re-encoding is
   corruption. Validation re-proves the record against itself (each outcome is
   what its own actual and bound give; each bound is the limit it names; each
   frequency state agrees with whether its limit was supplied; the verdict is
   what the checks give) and against its row (the verdict column and N).
3. **A report reads the record and never re-evaluates the gate.** An execution
   completed before schema 14 has none, and its report says `not_recorded`.
   Nothing is backfilled: no gate was kept when it completed, and deriving one
   now from today's code is exactly the drift task 066 prevents.
4. **The amendment to ADR 0054.** § 1's "nothing else is stored … the gate
   limits" becomes "the limits are stored as part of the execution's own gate
   result". § 4's consequence is unchanged: **the current scenario's limits
   always apply.** A recorded limit describes the decision it belongs to, and
   nothing reads it when resolving or reusing a reference.

## Alternatives considered

- **Persist every comparison as its own entity**, with an id and a
  `/v1/comparisons/{id}` route. A new aggregate, a new table and a retention
  question, for a need the persisted execution already meets.
- **Re-derive the report at read time from limits passed in.** No schema step,
  but the report could disagree with the recorded verdict, and "the same
  report everywhere" would depend on every caller supplying the same limits.
- **Store only the limits.** The verdict could then be recomputed, but by
  today's code, not the code that decided. Keeping the checks is what makes
  the record evidence rather than a recipe.
- **Columns per check.** Six fixed checks fit columns; four optional checks
  with up to sixteen targets each do not, and a child table would be one more
  table to migrate, back up and keep consistent for a value read only whole.

## Consequences

- The report route keys on the execution and serves the recorded gate.
- An execution carries at most about 12 KiB of gate text (sixteen
  255-byte targets, every optional check, a long deferral); without optional
  limits it is about 1.6 KiB.
- Executions completed before schema 14 report evidence that needs no limit —
  presence, frequency, fidelity, operational sections, sameness, suggestions —
  and say `not_recorded` for the gate, the limits and each classification,
  which needs k and j.
- The behavior rows of a report are still reduced from the recorded runs by
  today's code. Only the gate is a record. If a future change to the reduction
  disagreed with a recorded check's actual value, the report would show both,
  side by side, rather than hide either.
