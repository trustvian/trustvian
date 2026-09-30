# Task 096 — Record-first admin console for the browser surface

**Status:** Implemented
**Depends on:** 074 (zero-input live behavior WebUI), 076 (behavioral evidence
explorer), 085 (evidence resolution)
**Decision records:**
[ADR 0050](../../adr/0050-the-browser-surface-is-a-record-first-admin-console.md) (information architecture),
[ADR 0051](../../adr/0051-the-browser-bundle-is-a-layered-design-system.md) (the bundle and the design system)

## Problem

Task 074 removed the identifier form from the front door and
[ADR 0041](../../adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
added the bounded collection routes that make the durable hierarchy
discoverable. Neither changed what happened once a developer stopped watching
and started investigating. Every surface behind Live was still assembled from
inputs:

```text
Compare      two <select> menus, each backed by a text input holding a run id
Evidence     a run id, a view menu, and an identifier field for the narrowing
Promotions   a project id typed in before any history is listed
Investigate  four columns of names, and a run's detail underneath them
```

Replacing a text box with a menu had made identifiers *discoverable*. It had
not made the records *browsable*. A menu hides how many options exist, shows
one at a time, says nothing about any of them, and returns the reader to the
start if they choose wrong. Deciding between two runs meant reading two opaque
strings without seeing either run's status, candidate or timing.

## Scope

The information architecture and the presentation. **No capability is added
and none is removed**, no route changes, and no bound moves.

## Acceptance criteria

1. A developer can discover a run, inspect an observation, follow its trace and
   session, select two runs for comparison, and open supporting evidence using
   visible navigation and clicks — without typing an identifier or traversing a
   chain of menus.
2. A persistent sidebar carries one destination per capability `/v1` serves,
   and none for anything that does not exist yet. The current destination and
   the scoped project are always visible.
3. Projects, runs, observations, behaviors and the comparison's candidates are
   each a table of records the server returned. Clicking a row opens it.
4. Selecting an observation opens its recorded decision, risk, scores,
   duration, span status and correlation references beside the table. Session,
   trace and behavior references are controls. The selected row stays marked,
   the table is not redrawn, and closing the panel returns focus to that row.
5. A comparison's sides are assigned from the run table. Both chosen runs are
   shown in full, and the comparison is enabled only when the selection is
   valid. No identifier field and no menu for either side.
6. Bounded pagination and server-side narrowing are unchanged. The startup
   budget is still one page of `GET /v1/projects`, a continuation is still an
   explicit control, and a table states whether it shows the whole collection
   or one page of it.
7. Only real data. No decorative metric, invented chart, fabricated activity or
   placeholder destination. Every figure on a summary strip is one the server
   returned, never one counted from the rows on screen.
8. The surface works at desktop and narrow widths, keeps a visible focus ring,
   respects `prefers-reduced-motion`, and conveys no state by colour alone.

## What shipped

**Destinations.** Live, Projects, Runs, Compare, Evidence, Promotions, Manage.
A run's workspace is opened from a row rather than being a destination of its
own; the sidebar stays on Runs, which is where the breadcrumb returns.

**`dashboard.js`**, a new presentation module: data tables with clickable rows
and decision edges, summary strips, pick lists, breadcrumbs, identifier chips
and the contextual detail panel. It draws and nothing else — no request, no
cursor, no accumulation — and a guard asserts it contains no `await`, no
`fetch(` and no `api.`.

**Runs** is reached through a visible list of the project's agents and a
visible list of their candidates, because `/v1` publishes no runs-by-project
collection and synthesising one is the crawl ADR 0041 exists to prevent.

**The observation table and detail panel** replace the five-view evidence form
as the normal way into retained history; the Evidence destination keeps the
finding and provenance surfaces it owns, reached from a comparison.

**Compare** assigns sides from rows and renders both in full.

**Manage** keeps every create form, the whole run lifecycle and open-by-ID.
Forms are the right shape there: those are inputs, not navigation.

## Guards

Restated for the new shell rather than deleted:

- `TestLiveIsTheDefaultLandingView` — exactly one destination current on load,
  exactly one view visible, and both are Live.
- `TestEveryManualCapabilityIsStillReachable` — every form, lifecycle control
  and result target survives, every destination has a view and every view
  declares the destination that owns it, so a placeholder entry cannot be added
  without the view it claims to open.
- `TestFrontDoorIsNotAForm` — no entity identifier field in the landing
  viewport, the primary navigation is the product's rather than the database's,
  and each collection has a table host.
- `TestLiveDescentIsLazyAndOnePagePerAction` — one request per action, no
  render pass follows a cursor, every continuation is a control of its own.
- `TestHeaderCountsAreAuthoritative` — both summary strips read progress from
  `/v1` and derive no figure from the rows drawn.
- `TestSubtabSwitchersAreScopedToTheirPanel` — four views hold sub-sections
  now, which is what makes the scoping load-bearing.

Added:

- `TestComparisonSidesAreAssignedFromRows` — no identifier field or menu for
  either side, both sides rendered, the request built from the assigned rows,
  the submit gated, and a run compared against itself refused.

One pre-existing helper, `functionBodyForTest`, was found to stop at the first
nested block, so several guards had been asserting over half a function body.
It is unchanged and `wholeFunctionBodyForTest` was added beside it, matching
braces over the noise-stripped source and slicing the comment-stripped one.

## Second pass — the design system

The first pass changed the information architecture and left the bundle as it
was: fifteen modules in one flat directory, one 1,400-line stylesheet, and a
set of conventions held together by one author's memory. Three costs followed,
each of them visible on screen.

| Problem | What a reader saw | What it is now |
|---|---|---|
| Values named at every use | Light and dark drifted; a grey might be *the* muted grey or a near miss | Only `tokens.css` names a raw value, both schemes in one block, enforced by test |
| Loading indistinguishable from empty | One line of italic text either way; silence between a click and its answer | Four distinct states, with a static skeleton in the shape of the rows coming |
| Severity had to be read | A `block`/`critical` row weighed the same as `allow`/`low` | A gutter, a tint and a mark — with the word still carrying the meaning alone |
| No layer to violate | A presentational helper could reach a route and nothing said so | Five layers, dependencies only downward, enforced by test |

### Additional acceptance criteria

9. The assets are a layered tree, and the layers depend only downward:
   `core/` → nothing, `v1/` → `core`, `ui/` → `core`, `live/` → `live`,
   `views/` → `core`/`v1`/`ui`, and `app.js` as the only composition root.
   **`ui/` may not import `v1/`.**
10. Only `styles/tokens.css` names a raw colour, size, space, radius or
    duration, and it declares both colour schemes.
11. Loading, empty, failed and populated are four visibly different answers.
    A loading table shows the shape of what is coming; an empty one names the
    next action; a refusal reads as a refusal.
12. Severity is carried by a gutter, a tint *and* a word. Removing the first
    two leaves the table correct.
13. Icons are inline SVG built through `createElementNS`, hidden from
    assistive technology, and every referenced name is declared. No icon
    font, no sprite file, no parsed markup.
14. Nothing loops. The skeleton is static, like everything else on the page.

### Additional guards

- `TestOnlyTheTokenSheetNamesARawValue` — no hex, `rgb(` or `hsl(` outside
  `tokens.css`, and `tokens.css` defines a dark scheme.
- `TestAssetLayersDependOnlyDownward` — the table above, per module, plus a
  check that no declared layer is empty.
- `TestEveryIconNameResolves` — every `data-icon` and `icon("…")` is
  declared, glyphs are `aria-hidden`, and the module builds rather than
  parses.
- `TestLoadingIsNotTheSameAnswerAsEmpty` — `dataTable` checks loading before
  empty (the reverse always wins, since a loading table has no rows), every
  collection has a flag, every flag is cleared in a `finally`, and the empty
  state offers a next action.

All four were verified to fail when the property they name is broken.

`readAsset` now resolves a module's base name to its path, so the ~110
existing guards name a module rather than a location and survived the move
unchanged; `TestAssetBaseNamesAreUnique` is what that rests on. Guards about
"the styles" read every sheet rather than whichever file a rule used to be
in, which is strictly stronger. Two harnesses that copy assets to a temporary
directory reproduce the tree instead of flattening it, because a flat copy
runs modules whose relative imports mean something other than what ships.

## Out of scope

Search that needs to see rows the page has not fetched; any new `/v1` route;
saved investigations (095); analytics across runs (092).
