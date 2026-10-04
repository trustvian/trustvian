# 098 — Selection-Based Context Workflows

Status: Implemented
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [074](../v1.0/074-zero-input-live-behavior-webui.md),
[096](../v1.0/096-record-first-admin-console.md), [097](097-theme-preference.md)

## Problem

Task 096 made browsing click-only, but a set of forms still asks for an
identifier the console already knows or could list. An audit of every field
that takes a project, agent, candidate, environment, run, execution or
reference identifier:

| Field | Today | Data that can populate it |
|---|---|---|
| Live → Watch one run | typed run id | candidate's runs |
| Evidence → Run history: run | typed run id | candidate's runs |
| Evidence → Run history: behavior | typed fingerprint | the run's behaviors |
| Evidence → Run history: trace | typed trace id | the run's traces (task 100) |
| Evidence → Run history: session | typed session id | **no collection** — reached from an observation row |
| Evidence → Provenance: both runs | typed, filled from Compare | Compare's chosen sides |
| Promotions → list: project | typed, filled from sidebar | the scoped project |
| Promotions → open: promotion | typed promotion id | the history page's rows |
| Promotions → record: both runs | `<select>` of one page + typed id | candidate's runs |
| Promotions → record: project | typed, filled from sidebar | the scoped project |
| Manage → agent: project | typed, filled from sidebar | projects |
| Manage → candidate: agent | typed, filled from Runs | the project's agents |
| Manage → run: candidate | typed, filled from Runs | the agent's candidates |
| Manage → run: environment | typed reference | the project's environments |
| Manage → lifecycle | whichever run was last opened | candidate's runs |
| Manage → Open by ID | typed, on purpose | — this *is* the paste path |

Scenario executions (`--reference <execution>`) are not reachable from the
WebUI at all, and `/v1` publishes no execution collection; this task adds
neither. Fields that **create** something — a new project, agent, candidate,
run or promotion identifier, a name, a behavioral profile, candidate metadata,
a failure reason — are genuine inputs and stay text.

## Scope

- One **context**: project → agent → candidate → run, plus the project's
  environments. Choosing a parent clears every dependent selection, abandons
  their in-flight reads, and never lets a response for the old parent populate
  the new one.
- A reusable, accessible **searchable selector**: names first, identifier
  beside it, a copy control, keyboard navigation, loading/empty/error states,
  an explicit "Load next page", and an **Advanced: paste an ID** path.
- Every row of the audit above that has data uses it. Context is carried
  between views: Runs, Overview, Traces, Evidence, Promotions and Manage read
  the same selection, and a choice made in one is preselected in the others.
- **Preselection only when unambiguous**: when a whole collection (no
  continuation) holds exactly one entry, it is selected; a page with more is
  never guessed from.

## Technical requirements

1. The context is DOM-free state (`views/context.js`) with ownership tickets
   per level, testable under node like `run-state.js` and `project-scope.js`.
2. The selector is presentation (`ui/selector.js`): it draws options it is
   given and reports intent; it issues no request (the `ui/` layer may not
   import `v1/`).
3. Each list read is one bounded page of an existing collection route. No new
   route, no unbounded traversal; a selector holds what was paged in and says
   whether that is the whole collection.
4. Pasted identifiers are sent exactly as typed and validated by the server.

## Tests

- Changing project clears agent, candidate, run and environments; changing
  agent clears candidate and run; changing candidate clears run.
- A response for a superseded parent is discarded, and does not clear the
  loading state of the current one.
- Preselection fires for a whole one-entry collection and never for a page
  with a continuation or for more than one entry.
- The selector's filtering, keyboard model and paste path.

## Acceptance criteria

1. Every audited field with an available collection is a searchable selector;
   the remaining identifier fields are an explicit paste path or a creation
   field.
2. Options show names and context, with the identifier visible and copyable.
3. A dependent selector is disabled until its parent is chosen, and clears
   when its parent changes.
4. A selection made in one destination is preselected in the others.
5. No view shows a previous selection's records under the current selection.

## What shipped

- `views/context.js` (shared context, ownership per page, preselection, paste
  adoption), `ui/selector.js` (the combobox), `views/selectors.js` (bindings).
- Every row of the audit with a collection is a selector, except the
  run-history **behavior** and **trace** identifiers, which arrive with task
  100's trace investigation (the trace collection is that task's route). The
  **session** identifier has no collection and stays a paste field, filled from
  an observation's row.
- The Runs destination keeps its own bounded browser (ADR 0050) and is kept in
  step with the context in both directions; rows listed under a previous agent
  or candidate are dropped, not relabelled.
