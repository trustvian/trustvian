# 0046 — Trace backends are interoperability targets, not dependencies

**Status:** Proposed

This is the first ADR in this repository recorded as **Proposed** rather than
Accepted. It is written that way deliberately: the reasoning below is complete,
but no maintainer has ratified the decision, and
[the ADR index](README.md#status) defines Proposed as "under consideration, not
yet an architectural commitment". What would make it Accepted is stated at the
end.

## Context

[Task 082](../tasks/v1.0/082-agent-inspection-and-evaluation-depth.md) planned a
set of inspection and evaluation capabilities. Planning them raised a question
that outlives the planning: what relationship, if any, should Trustvian have with
an agent trace-and-evaluation backend — the class of system that stores spans,
renders waterfalls and scores model output?

Four answers were available, and they are genuinely different decisions rather
than degrees of one:

```text
1  ignore it            build every view Trustvian needs, acknowledge nothing
2  reuse its code       import or vendor an implementation
3  depend on it         require it at runtime for inspection capabilities
4  interoperate         exchange OTLP; neither side requires the other
```

Three facts constrain the answer, and all three were verified rather than
assumed.

**The licence classes are usually incompatible for distribution.** This
repository is Apache-2.0 (`LICENSE`). The mature systems in this category are
commonly source-available rather than OSI open source — Elastic License 2.0 and
Business Source License are the two shapes to expect. Vendoring or importing
source-available code into an Apache-2.0 distribution is not a thing this project
can do, whatever the engineering merit would be. That is a property of the
category, not of any one implementation, so it is recorded as one.

**The fan-out already exists and is already deliberate.** The Collector processor
sits in a pipeline and passes spans through, enriched; exporting the same stream
to a second destination is ordinary Collector configuration.
`docs/ROADMAP.md` states the position outright — Trustvian "sits beside a trace
backend rather than replacing one — the Collector fan-out that makes that
possible is deliberate and stays."

**The two kinds of system answer different questions.** This is the load-bearing
fact, and it is the same distinction
[task 076](../tasks/v1.0/076-behavioral-evidence-explorer.md) already draws
between an explorer and a trace viewer:

```text
a trace-and-eval backend asks    what happened inside this trace, and how
                                 good was the output?

Trustvian asks                   which observed actions constitute this
                                 actor's behavior, how do they differ from
                                 reference, and what decision follows?
```

The first needs prompts, completions, arguments, results and every attribute a
span carries. The second needs none of them — which is why *behavioral
observability does not require content observability* is a principle here and
not a limitation.

## Decision

**Option 4. A trace backend is an interoperability target. It is never a runtime
dependency, and never a source of code.**

Four rules follow.

### 1. No runtime dependency, and absence changes no result

No Trustvian capability may require a trace backend to be running. A behavioral
gate that needed one would make a security decision depend on an observability
deployment being healthy — so a missing backend produces no error, no degraded
verdict and no changed number. Task 082's item 090 carries this as an acceptance
criterion rather than an intention.

This is the same rule the roadmap already applies to analytical storage: "No
analytical store is a dependency of the engine."

### 2. Interoperability is OTLP, in the direction it already flows

The integration is a Collector configuration that fans one OTLP stream out to
Trustvian and to the trace backend. Trustvian exports no span it did not receive
and adds no exporter of its own; the second destination receives what the
producer already emitted.

**No backend is a privileged target.** Any OTLP consumer works the same way,
because the convention is what is shared rather than the vendor. A backend that
reads the same OpenInference attributes Trustvian reads
([ADR 0045](0045-conventions-are-read-frameworks-are-not.md)) needs no adaptation
in either direction, which is a property of the convention and not a
recommendation. The worked example in item 090 is chosen for being self-hostable
in one process, and choosing a different one costs a paragraph.

### 3. No code reuse, and the three relationships stay distinct

| Relationship | Permitted | Why |
|---|---|---|
| **Conceptual inspiration** | Yes | Reading how another system frames a problem, to recognize a gap in this one, costs nothing and hides nothing. Task 082 did it and says so |
| **Interoperability** | Yes | A documented OTLP recipe, with no dependency in either direction |
| **Code reuse** | **No** | Source-available licensing into an Apache-2.0 distribution. The licence settles it before the engineering question arises |

Conflating the first with the third is how a comparative reading turns into a
copied product, which is why they are tabulated rather than left to judgement.

### 4. No parity claim, in either direction

Trustvian does not claim to render a trace waterfall, and it does not claim a
trace backend can produce a behavioral gate. Documentation states which question
each side answers. A roadmap item justified by "another product has one" is
refused on that basis alone; every item in task 082 is justified by a gap in
Trustvian's own developer workflow, and two of them by a measurement.

## Alternatives considered

**Ignore it (option 1).** Rejected because it is not actually available. A
developer who already runs a trace backend will ask whether adopting Trustvian
means moving their traces, and the answer is no — leaving that undocumented does
not avoid the relationship, it just leaves the reader to guess at it.

**Reuse its code (option 2).** Refused by licence. Worth recording that the
licence refused it before the architecture had to: this project's dependency rule
already confines every third-party dependency to exactly one package and treats a
fourth as requiring justification, so an entire embedded platform would have been
a hard argument regardless.

**Depend on it (option 3).** Rejected on architecture, independently of licence.
It inverts the boundary ADR 0022 protects — the platform consumes the engine as
an ordinary dependency and the whole system consumes observability rather than
becoming it. A gate that could not compute without a trace backend would also
contradict the roadmap's own statement that Trustvian is a consumer of
observability, not a competitor to it.

**Build a trace viewer instead.** Not an alternative to this decision, and
already decided elsewhere: task 076 excludes a waterfall, a flamegraph, arbitrary
attribute search and a dependency map, because those are a trace tool's job. This
ADR is why that exclusion is comfortable rather than a gap.

## Consequences

- A developer keeps their existing trace backend, and adopting Trustvian costs no
  re-instrumentation and no trace migration. That claim is now demonstrable
  rather than asserted, which is what task 082's item 090 delivers.
- Trustvian's own inspection surfaces stay narrow on purpose. When a developer
  wants a waterfall, the answer is a trace backend beside Trustvian — and that is
  a supported configuration rather than an admission.
- **A fan-out sends the operator's raw spans, content included, to the second
  backend.** Trustvian neither reads nor stores those attributes, so its own
  posture is unchanged — but the configuration is not privacy-neutral for the
  operator, and the documentation must say so rather than presenting fan-out as
  free. This is why interoperability is an opt-in integration and not a default.
- No source-available code enters this repository. The existing module and
  dependency checks already prove it, so the rule needs no new enforcement.
- This ADR governs the category rather than one implementation. A second backend
  arriving needs no second decision.

## What would make this Accepted

A maintainer ratifying it. Nothing in the reasoning is waiting on a measurement
or a prototype — the licence classes, the existing fan-out and the question-scope
distinction are all established facts. Should task 082's item 089 ever move the
product boundary toward quality evaluation, rule 4's parity clause is the part of
this record to re-read; the other three rules are unaffected by that decision.
