# 0050 — The browser surface is a record-first admin console, and an identifier is never a prerequisite

**Status:** Accepted

## Context

[Task 074](../tasks/v1.0/074-zero-input-live-behavior-webui.md) removed the
identifier form from the *front door*: the page opens on Live, subscribes to
everything, and an agent that is working appears by itself.
[ADR 0041](0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
added the four bounded collection routes that make the durable hierarchy
discoverable after a reload.

Neither changed what happened once a developer stopped watching and started
*investigating*. Every surface behind Live was still a form:

```text
Compare      two <select> menus, each backed by a text input holding a run id
Evidence     a run id, a view menu, and an identifier field for the narrowing
Promotions   a project id typed in before any history is listed
Investigate  four columns of names, and a run's detail underneath them
```

The collection routes had made the identifiers *discoverable*, and the
surfaces had been fitted with menus so they no longer had to be typed. But a
menu is not a record. It hides how many options exist, shows one at a time,
says nothing about any of them, and puts the reader back where they started if
they pick the wrong one. Choosing between two runs meant opening a list,
reading two opaque strings, and committing to one without seeing its status,
its candidate or when it ran.

The observed failure is narrower than "the design is dated". It is this: the
surfaces that answer *which* — which run, which observation, which two runs to
compare — were built out of controls that answer *what value*. A text input
and a menu are both inputs. Neither is a way to look at records.

## Decision

### The workspace's primary surface is a table of real records, and a row is a control

Projects, runs, observations, behaviors and the comparison's candidates are
each a table of what the server returned. Clicking a row opens that record.
Every level that used to be a menu is now either a table or a visible list.

The leading cell of every row holds a real button, which is what makes a row
operable from the keyboard; a click anywhere on the row is the pointer
shorthand for pressing it. One way in, two ways to ask for it.

### An identifier renders as the control that follows it

A run, trace, session, span or behavioral fingerprint is drawn as a
monospaced chip, and every chip goes somewhere. An identifier with nowhere to
go renders flat instead, so the shape never promises a journey the page cannot
make.

This is the whole answer to "no typing". The acceptance criterion for the
console is that a developer can discover a run, inspect an observation, follow
its trace and session, choose two runs to compare, and open the evidence
behind a gate check, using visible navigation only.

### A comparison's two sides are assigned from rows, never named

The run table carries a **Reference** and a **Candidate** control on every
row. Both chosen runs are then rendered in full in labelled panels, and the
submit is disabled until two different runs are chosen. There is no identifier
field for either side and no menu standing in for one.

The identifier is still exactly what the server receives. What changed is that
nobody has to find one, copy one or recognise one.

### Menus survive only for secondary settings

A `<select>` remains where the choice is a setting rather than navigation: the
promotion form's target environment, and its run pickers, which are filled from
a comparison the reader has already made. Dropdowns are not how anyone moves
between projects, runs, observations or comparison sides.

### Visible does not mean loaded

Every bound ADR 0041 established is unchanged. One page per action, the
continuation is an explicit control, narrowing happens in storage before the
page bound, and the startup budget is still one page of `GET /v1/projects`.

The filter boxes narrow the rows already on screen and never request a page the
page was not going to fetch. Each says which of the two is happening — "this is
the whole collection" or "more exist" — because a table that silently showed
one page of a large collection would be lying by omission.

### A figure on a summary strip is one the server returned

The run workspace's strip reads its counts from
`GET /v1/evaluation-runs/{id}/progress`. Nothing on it is derived from the rows
on screen. Retained history is bounded and the authoritative counts are not, so
a strip that counted its own rows would report how much the page drew rather
than how much the run observed — which is the same distinction the Live strip
keeps between frames seen on a connection and a run's record count.

There is no decorative metric, no invented chart and no placeholder
destination anywhere in the shell.

## Alternatives considered

**Restyle the existing forms.** Better spacing, a nicer `<select>`, a sidebar
instead of tabs. This was tried first in spirit and is what the brief rejected:
it leaves the reader choosing between values they cannot see. The problem was
the control, not its appearance.

**One flat "all runs in this project" table.** The obvious shape, and the one
`/v1` cannot serve. There is no runs-by-project collection, and synthesising
one means walking every agent and every candidate — the crawl ADR 0041 exists
to prevent, now issued on page load. Two visible bounded lists above the run
table are the honest shape of the data, and being visible is what keeps them
from being a dropdown chain by another name.

**Client-side search over the whole database.** Would make one search box
answer every "which" question. It requires downloading the collections, which
is the same unbounded workflow. Search here finds visible records; the server
owns filtering that needs to see rows the page has not fetched.

**Row expansion instead of a side panel.** Cheaper to build, and it reflows the
table under the reader every time they open something. The contextual panel
keeps the table still, keeps the selected row marked, and returns focus to that
row when it closes.

## Consequences

The information architecture changed and the capabilities did not. Every form,
lifecycle control and result target that existed still exists; Manage keeps
them, and `TestEveryManualCapabilityIsStillReachable` keeps that honest.

`dashboard.js` joins the shipped modules as the presentation layer for tables,
strips, pick lists, breadcrumbs and the detail panel. It draws and nothing
else: it makes no request, holds no cursor, and a test asserts it contains no
`await`, no `fetch(` and no `api.`.

The guards that pinned the old tab shell were restated for the new one rather
than deleted — a default landing destination, a view per destination and a
destination per view, subtab switchers scoped to their view — and two were
added: one that the comparison's sides are assigned from rows, and one that
the run strip derives no figure from the rows it drew.

One existing test helper was found to be truncating at the first nested block,
so several guards had been passing over half a function body. It is unchanged,
and a brace-matching companion was added beside it for the guards that need a
whole body.

The privacy boundary is untouched. Every evidence cell still reads through
`retainedValue` and the allowlist, including the new detail panel's, so a field
`/v1` gains later reaches nothing until somebody adds a column for it.

CSP is untouched and still has no `unsafe-inline`, which means no web font:
the type system is the platform's own stacks, and the distinction it draws is
between prose and record rather than between display and body.
