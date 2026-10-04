# 104 — The Runs Destination Reads the Shared Context

Status: Planned
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [098](098-selection-based-context-workflows.md),
[101](101-recency-ordered-run-discovery.md)

## Problem

Task 098 left the Runs destination with its own bounded browser and its own
agent/candidate state, kept in step with the shared selection context by hand
in `app.js`. Two sources of truth for one selection is where a desync starts.

## Scope

- Runs' agent list, candidate list and run table read the context's pages;
  choosing in them is choosing in the context. The mirrored variables and the
  manual synchronization are removed.
- The run table reads task 101's recency collection at the deepest chosen
  scope, so a project or an agent shows its recent runs before a candidate is
  chosen.
- No duplicate fetch: a page the context already holds for the same parent is
  not read again.

## Tests

- Context-driven navigation: a choice in Overview or Manage appears under Runs
  and the reverse; changing a parent clears dependents; a stale page is
  discarded; no feedback loop between subscribers.

## Acceptance criteria

1. One source of truth for project, agent, candidate and run.
2. Existing Runs behavior — filters, status chips, pagination, the run
   workspace and its details panel — is preserved.
