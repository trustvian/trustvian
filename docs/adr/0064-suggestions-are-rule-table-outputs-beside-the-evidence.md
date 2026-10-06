# 0064 — Suggestions are rule-table outputs beside the evidence, never verdicts

**Status:** Proposed (task [105](../tasks/v0.12/105-pipeline-status-surface.md),
[106](../tasks/v0.12/106-frequency-evidence.md) and
[107](../tasks/v0.12/107-change-impact-view-and-report.md); accepted when the
first of them lands). Builds on
[ADR 0028](0028-scorecards-are-fixed-shape-comparative-evidence.md),
[ADR 0029](0029-hard-gates-use-explicit-integer-evidence.md) and
[ADR 0036](0036-webui-is-a-same-origin-adapter-over-v1.md), and changes none of
them.

## Context

`v0.12.0` — Change Impact — adds two surfaces that are only useful if they say
what a developer should look at next:

- **Pipeline status** (task 105). "No producer has reported for 30 seconds" is
  a fact; "check `OTEL_EXPORTER_OTLP_ENDPOINT`" is what a developer who has
  never configured an exporter needs to read.
- **A comparison** (tasks 106 and 107). "`export_customer` present 0/10 → 10/10"
  is a fact; "if this was expected, add it to the scenario; otherwise
  investigate before promoting" is the step that follows.

The repository's position is *evidence, not verdicts*. Scorecards stop one step
short of acceptability (ADR 0028). A gate is a fixed set of named integer checks
whose `PASS` means exactly that those checks passed and nothing else (ADR 0029
§ 10). No surface carries an aggregate health score — `v0.11.0`'s exclusions say
so, and so does the WebUI product model. And the browser holds no authority: it
renders `/v1` and derives nothing (ADR 0036, criterion 19).

A sentence telling a developer what to do next looks like it violates all three.
It is advice about the evidence, so it is easy to read as a judgement of it. If
it were computed in the browser, it would be a second engine. And a "suggestion"
that could turn a FAIL into an acceptable result would be a compensating path,
which ADR 0029 § 9 forbids.

This record decides what a suggestion is allowed to be, so tasks 105–107 do not
each decide it differently.

## Decision

### 1. A suggestion is the output of a named rule in a documented table

Each surface that emits suggestions has **one rule table**, specified in its
task file, implemented in one place on the control plane, and tested row by
row. A row has:

| Field | Meaning |
|---|---|
| `rule` | a stable identifier, such as `status.no_producer` or `compare.added_in_all_runs` |
| `rule_version` | an integer, incremented whenever the condition or the text changes |
| condition | a predicate over integer or categorical evidence already present in the same response, with no other input |
| `evidence` | the names and values of the fields the condition read, copied from the same response |
| `text` | a fixed sentence template; the only substitutions are values from `evidence` |

A suggestion on the wire is exactly those fields:

```json
{
  "rule": "compare.added_in_all_runs",
  "rule_version": 1,
  "evidence": {"behavior": "tool · export_customer", "candidate_runs_present": "10", "runs": "10"},
  "text": "export_customer was added in 10/10 candidate runs. If this was expected, add it to the scenario; otherwise investigate before promoting."
}
```

No suggestion exists outside a rule. The text is a template, not generated
prose. A rule that cannot cite the evidence it read is incomplete, in the same
way as a contributor without a detail (SECURITY.md, *explainability is
enforced*).

### 2. Suggestions live in their own field and never touch a verdict

Suggestions are carried in a separate `suggestions[]` array, next to the
evidence and the gate result and never inside them.

- **No gate reads a suggestion.** No rule reads a gate's *configuration*. A rule
  may read a check's recorded outcome as evidence (for example "this check
  FAILed"), but its output never feeds back into any check.
- **A suggestion cannot change an exit code**, a verdict, a promotion or a
  stored record. `trustvian eval run` exits by its gate, as before. `trustvian
  status` and `dev --check` exit `0` whatever they suggest, and `3` only on
  operational failure.
- **Removing every suggestion changes nothing else in the response.** A test
  asserts this per surface: the response with the rule table emptied is
  byte-identical except for `suggestions`.

This is what keeps the mechanism compatible with ADR 0029. A gate's verdict is
still computed only from its own named integer checks. A suggestion is
commentary that a gate cannot read.

### 3. Rules read integer or categorical evidence, and no rule produces a score

A condition is a comparison over fields that ADR 0029 would accept as gate
evidence: integer counts, integer permille, closed categorical values (fidelity,
layer, status class, presence classification) and stated absence. **No rule
weights, sums or combines evidence into a number**, and no rule emits one. There
is no "risk level" and no "health" for the comparison or the pipeline. Two rules
that both fire produce two suggestions. Neither is ranked above the other except
by the table's fixed order.

Unavailable evidence never satisfies a condition. If a rule needs status codes
and the run has none, the rule does not fire. It does not treat "none recorded"
as zero. Whether the absence itself deserves a suggestion is a separate row with
its own name.

### 4. The control plane computes suggestions; every adapter renders them

Suggestions are computed by the control plane, in the same response as the
evidence they cite. The WebUI, the CLI, the TUI and the CI renderer **render the
`text` field they were given and compute nothing**. A browser that evaluated a
rule would be a second implementation of it, with its own chance to disagree
about a page boundary or an absent field. ADR 0036 already forbids that, and the
existing architecture test (`TestHTTPAdapterDoesNotComputeEvaluationLogic`) and
WebUI tests are extended to cover it.

### 5. The table is part of the contract and is versioned like one

Rule identifiers are stable. A rule's condition or text changes only together
with its `rule_version`. Removing a rule is a documented change. A consumer such
as a CI comment may quote suggestions, but must not branch on their `text`. It
may branch on `rule`.

## Why this is compatible with "evidence, not verdicts"

A verdict is a judgement about the subject: *this candidate is acceptable*,
*this pipeline is healthy*. A suggestion as defined here is a deterministic
pointer from a named fact to a named next step: *this fact holds, and this is
where people usually look when it does*. Four properties keep it on the evidence
side of that line:

1. It is a **pure function of evidence already shown** in the same response, so
   a reader can always check it against the numbers next to it.
2. It **asserts nothing the evidence does not**. The text names the condition
   that fired, for example "added in 10/10 runs". It never says "this regression
   is serious".
3. It **has no authority**. No gate, exit code, promotion or stored record
   depends on it, and the tests prove that by removal.
4. It **is not aggregated**. Nothing counts suggestions, scores them or rolls
   them into a status colour.

## Alternatives considered

**No suggestions; the developer reads the evidence.** This is the purest
reading of "evidence, not verdicts". It fails the developer `v0.12.0` is for:
the person whose Live view is empty because the exporter points at the wrong
port. The evidence for that ("0 producers") is accurate and does not help.

**Suggestions computed in the browser.** Rejected under ADR 0036. Every adapter
would carry its own copy of the rule table, and the CLI, the CI comment and the
browser could disagree about the same comparison.

**Suggestions as gate checks with an `advisory` marker.** Rejected. ADR 0029's
fixed check set is a schema change and a reviewed policy decision per check. An
advisory check is still a check, and a reader of a gate result would reasonably
assume it took part in the verdict. Task 078's `advisory: fresh scope` marker
annotates a check that *does* affect the verdict. It is not a precedent for one
that does not.

**A severity or priority on each suggestion.** Rejected. A severity is a
judgement (ADR 0029 § 6 forbids judgement names for exactly this reason), and
ordering by severity is a score in disguise. The table's fixed order is
documented and is the only ordering.

**Generated or templated prose with free substitution.** Rejected. Every
substitution is a field in `evidence`, so the text cannot say anything the
evidence does not.

## Consequences

- Each of tasks 105, 106 and 107 carries a rule table with one test per row:
  the condition fires on the evidence it names, does not fire when that
  evidence is absent, and changes nothing else in the response.
- Behavioral descriptors that appear in `evidence` or `text` are
  producer-supplied strings. Every renderer treats them as untrusted input, as
  the CI renderer already does (task 079).
- A future surface that wants suggestions (for example 092's analytics) cites
  this record and brings its own table. It does not add rules to another
  surface's table.
- If a team asks for a suggestion to become a gate, that is a new named gate
  with its own evidence contract and review under ADR 0029 § 7. It is not a
  configuration change to this mechanism.
