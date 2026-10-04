# 099 — Overview Dashboard

Status: Implemented
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [097](097-theme-preference.md), [098](098-selection-based-context-workflows.md)

## Problem

Every destination answers one question well and none answers "where should I
look first?". A developer arriving with a candidate to judge has to visit
Runs, a run's workspace, Compare and Promotions to assemble a picture the
console could put on one page from data it already serves.

## What the data supports

| Summary | Source | Honest scope |
|---|---|---|
| Activity now | the Live connection's scope cards | this connection since it last synchronized |
| Runs and their status | `GET /v1/candidates/{id}/evaluation-runs` | one page of the chosen candidate |
| Evidence of the newest run on that page | `GET /v1/evaluation-runs/{id}/progress` | authoritative counts for one run |
| Behaviors of that run | `GET /v1/evaluation-runs/{id}/behaviors` | one page, with authoritative per-behavior counts |
| Gate verdicts | `GET /v1/projects/{id}/promotions` | the first page of recorded decisions, in identifier order — not newest first |
| Environments | `GET /v1/projects/{id}/environments` | the whole collection |

**Not supported, and not shown:** a project-wide list of recent runs (no such
collection — [task 101](101-recency-ordered-run-discovery.md)), a decision or
risk distribution across a run (only a bounded page of retained history
exists, and a page is not a distribution), any trend over time, and any
aggregate "health" score.

## Scope

- A new **Overview** destination under *Observe*. Live stays the landing view.
- Context from task 098; summaries scoped to it, each saying what it covers.
- Freshness: every panel states when it was read; one **Refresh** reads again.
  No timer and no polling.
- Drill-down: every figure and row opens the record behind it (run workspace,
  Compare with sides preselected, Traces, Promotions, Live).
- Loading, empty and error states per panel, independently.

## Technical requirements

1. Each panel holds its own ownership surface, so one slow read never blocks or
   overwrites another, and a context change discards every outstanding read.
2. Status counts are computed only over the page on screen and labelled so.
3. A missing count reads *not available*, never `0`.
4. Charts are a second rendering of printed numbers and convey nothing by
   colour alone.

## Tests

- The status breakdown counts exactly the rows given and labels its scope.
- A late response for a previous context is discarded per panel.
- Gate verdicts are the server's `outcome` and `gate_result.verdict` strings,
  not derived.

## Acceptance criteria

1. With a project, agent and candidate chosen, Overview shows live activity,
   run status counts, the newest run's evidence, its behaviors, recent gate
   verdicts and environments, each with its scope and read time.
2. Every summary links to the record behind it.
3. Nothing is fabricated: absent data reads *not available*, and nothing
   counted from a page claims to describe more than that page.
4. Each panel has distinct loading, empty and error states.
5. The layout works at desktop and narrow widths in both themes.

## What shipped

- `views/overview-model.js` (pure reductions: status breakdown, newest-first,
  decimal comparison and bar ratio without numeric coercion, top behaviors,
  verdict tally, read clock) and `views/overview.js` (panels, per-panel
  ownership surfaces).
- The evidence panel follows the run the reader chose when it belongs to the
  chosen candidate, and otherwise the newest on the page; its title says which.
- Freshness is an absolute read time: with no timer on the page, a relative
  "5s ago" would be wrong a second later.
