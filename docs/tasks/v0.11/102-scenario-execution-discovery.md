# 102 — Scenario Execution Discovery and Reference Selection

Status: Implemented
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [078](../v1.0/078-behavioral-scenario-suites.md) (persisted
executions, ADR 0054), [101](101-recency-ordered-run-discovery.md) (the
schema v10 start key)

## Problem

`trustvian eval run --reference <execution>` reuses a recorded reference side,
but nothing lists recorded executions: the identifier has to be copied out of a
CI log or a previous terminal. The WebUI cannot show them at all.

## Scope

- `GET /v1/projects/{project_id}/scenario-executions` — newest first by start
  time, optional equality filters `agent_id`, `environment` and `scenario`,
  keyset cursor, `limit` 1..64. Each entry: id, scenario name, scope, status,
  start and finish time, repetition count, verdict when completed, and the
  execution it reused.
- `GET /v1/scenario-executions/{id}/reference-check` — runs the control
  plane's existing reference validation (`usableReference`, unchanged) for the
  execution against its own repetition count and scope, and reports usable or
  the server's reason. Nothing is re-implemented in the browser.
- A **Scenarios** section under Evidence: a selector scoped by the context's
  project and agent plus environment and scenario filters, a details panel,
  the reference check, and a **Copy CLI command** control for
  `trustvian eval run --scenario <file> --reference <id>`.

## Explicit exclusions

Starting a scenario from the browser. A scenario runs a developer's command;
doing that from a page would make the local control plane an executor and a new
security boundary. Out of scope.

## Tests

- Store: ordering, filters, cursor boundaries, equal start times, both
  backends.
- HTTP: shape, filters, 400/404, reference check usable and unusable
  (running, failed, missing run).
- Browser: eligibility shown only from the server's answer; copy text.

## Acceptance criteria

1. A developer can find a recorded execution by scenario, agent and
   environment without copying an identifier.
2. Each shows scenario name, status, times, repetition count and verdict.
3. An execution is offered as a reference only when the server's validation
   says it is usable, with the conditions (repetition count, project,
   environment) stated.
4. The CLI command is copyable; nothing executes from the browser.

## What shipped

- `platform/scenario_execution_list.go`: the list query over schema v10's
  start key, filter validation, and `CheckScenarioReference`, which calls
  `usableReference` with the execution's own `runs` and scope and maps its
  refusals to an answer rather than an error.
- `views/scenarios.js`: the table, details panel, reference check and copy;
  three ownership surfaces (list, detail, check), cleared when the filter
  changes.
