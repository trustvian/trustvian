# 110 — The browser computes nothing: every figure it still derives, decided

Status: Specified; not implemented
Milestone: `v0.13.0` (proposed)
Depends on: [099](../v1.0/099-overview-dashboard.md),
[100](../v1.0/100-trace-investigation.md),
[074](../v1.0/074-zero-input-live-behavior-webui.md),
[101](../v0.11/101-recency-ordered-run-discovery.md) (all implemented)
Decision records: [ADR 0036](../../adr/0036-webui-is-a-same-origin-adapter-over-v1.md),
[ADR 0050](../../adr/0050-the-browser-surface-is-a-record-first-admin-console.md),
[ADR 0051](../../adr/0051-the-browser-bundle-is-a-layered-design-system.md),
[ADR 0063](../../adr/0063-recency-is-a-stored-sort-key-and-a-composite-cursor.md)
Found by: the `v0.12.0` documentation audit, finding F3 of #175

**The `v0.12.1` patch specifies this task and implements none of it.** Every
`/v1` field below is an additive `/v1` change, and additive `/v1` changes land
in a minor release (`v0.13.0`), never in a patch. The engine, `StableFeatures`,
fingerprints, baseline keys, verdicts, gates and suggestions are unchanged.

## Developer problem

The WebUI's stated principle is that the browser computes nothing: every number
on screen is a `/v1` field ([`docs/webui.md`](../../webui.md#the-overview),
ADRs 0036 and 0050), and tests hold it there for the parts that matter most:
`TestGateVerdictComesFromTheServer`, `TestUint64CountersAreNeverParsedAsNumbers`,
`TestHeaderCountsAreAuthoritative`, `TestStatusViewComputesNothing` and
`TestChangeImpactComputesNothing`.

The audit found the principle is not quite true. No verdict, gate, suggestion
or score is computed in the browser. But some figures are: the Overview's
runs-by-status bars and gate-verdict tally, its "top 8" behavior ranking, the
Traces waterfall's offsets and window length, and the Live view's per-card and
per-edge stream counts. `docs/webui.md` now names each one. Naming them is not
the same as deciding them.

Two kinds of figure are mixed up here, and 109 already drew the line between
them for frame counts:

- **A figure that summarizes the authoritative record belongs to the server.**
  "3 runs completed" claims something about the record. When the browser counts
  it from the rows it happened to load, the figure changes with the page size.
- **A figure that describes what this browser tab received or rendered may
  stay**, if its label says so. "12 seen live" is a true statement about the
  tab, and no server could make it. 109 kept per-actor frame counts on that
  basis: "These are frame counts, labelled the way cards label theirs."

110 lists every figure the browser derives and puts each one on one side of
that line.

## The exclusion rule

A figure on this list is anything the browser **prints** or **announces** that
is not a `/v1` field (or a constant) shown as-is. These are excluded, and the
static check in §4 enforces the same boundary:

1. **Formatting one server value.** Thousands separators, a unit change of one
   field (`formatDuration`'s nanosecond text to millisecond text,
   `views/trace.js:296-336`), `toFixed(2)` on a server score
   (`live/inspector.js:245`), and `count / total` printed from two server fields
   (`v1/render.js:719`). The value is unchanged; only its spelling is.
2. **Drawing geometry from server numbers that is never printed.** Bar lengths
   and positions in `aria-hidden` tracks: a score bar
   (`live/inspector.js:256`), the behavior bars' `decimalRatio`
   (`views/overview-model.js:95-115`, used at `views/overview.js:503`), and the
   graph's layout coordinates (`live/graph.js:98`, `176`, `189`, `226-227`).
   The printed number beside each is the server's.
3. **Client control state.** Reconnect backoff (`v1/realtime.js:228-229`,
   `566`), request generation counters, keyboard focus arithmetic, skeleton
   sizes (`ui/feedback.js:31-35`), and counts of the reader's own choices
   ("1 of 2 sides chosen", `views/overview.js:429`).
4. **Equality filtering over loaded rows.** The filter boxes. What they produce
   is a set of rows. Any count of those rows is in class E below.

A drawing derived from a browser-computed figure is not excluded by rule 2:
the waterfall's bar placement is listed, because its offsets are the browser's.

## Current state, verified against `main` (`79fe509`)

Every file is under `platform/webui/assets/`. A search over every script for
`.reduce(`, `.sort(`, `Math.*`, `Date.parse`/`Date.now`, division, counters
incremented on stream events and array or map sizes used as a displayed count
found these, and only these.

| # | Figure | Where it is computed | Where it is shown | Decision |
|---|---|---|---|---|
| A1 | Runs by status: a count per status, and their total | `statusBreakdown`, `views/overview-model.js:20-38` (`reduce` at `36`) | `views/overview.js:359-367`: "N run(s) on this page, by status", bar ratio `entry.count / breakdown.total` at `365` | **Moves to `/v1`** (D1) |
| A2 | Gate-verdict tally: a count per recorded outcome | `verdictTally`, `views/overview-model.js:135-142` | `views/overview.js:546-548`: "2 accepted · 1 rejected", with "on this page" only when more exist | **Moves to `/v1`** (D2) |
| A3 | The "top 8" behaviors by observation count | `topBehaviors`, `views/overview-model.js:120-131` (sort by `compareDecimal`); `TOP_BEHAVIORS = 8` at `views/overview.js:32` | `views/overview.js:483-505`: "Behaviors by observations, within the first page" | **Moves to `/v1`** (D3) |
| A4 | Runs newest first | `newestFirst`, `views/overview-model.js:55-70` | nowhere: no view calls it; only `platform/webui/overview_test.go:27` does | **Deleted** (D4) |
| B1 | Each action's offset from the earliest timestamp on the page | `buildWaterfall`, `views/waterfall.js:84-99` (`nanosBetween` at `60-62`) | "From trace start", `views/traces.js:576`; bar placement `views/traces.js:437`, `442` from `left`/`width` at `views/waterfall.js:121-122` | **Stays, relabelled** (D5) |
| B2 | The waterfall's window length | `views/waterfall.js:91-105`, `126` | "window 1.2 s", `views/traces.js:352`; scale tick `views/traces.js:381` | **Stays, relabelled** (D5) |
| B3 | Rows without a readable timestamp | `views/waterfall.js:106-110` | `views/traces.js:354` | Stays: already labelled "on page N" by its line (D5) |
| B4 | An action's depth in the trace tree | `views/trace.js:236-280` (clamped at `260`) | "Depth", `views/traces.js:583` | **Stays, relabelled** (D5) |
| C1 | Frames seen per scope card | `card.seenLive += 1`, `live/model.js:97` | `live/rail.js:100` ("seen live"), `134` ("observations seen on this stream"), `views/overview.js:311` ("seen live"), `app.js:804` ("Seen on this connection") | Stays: already labelled (D6) |
| C2 | Frames seen per edge | `edge.pulses += 1`, `live/model.js:245` | `live/inspector.js:166-168` ("on the current connection"), `live/graph.js:411` ("observed on this stream") | Stays: already labelled (D6) |
| C3 | Targets drawn from one agent node | `targets.length`, `live/graph.js:204` | "3 targets", `live/graph.js:258` | **Stays, relabelled** (D6) |
| C4 | Scopes, sources, targets and edges not drawn or not listed | `live/model.js:104`, `200`, `278`, `297`; `saturation()`, `live/model.js:331-343` | `live/rail.js:43-47`, `210-215` ("a display limit") | Stays: already labelled (D6) |
| C5 | Live cards beyond the Overview's eight | `cards.length - 8`, `views/overview.js:319-320` | "N more in Live." | **Stays, relabelled** (D6) |
| C6 | Rows in the Live timeline | `feed.rows.length`, `live/timeline.js:81` | "Live stream · current connection · showing N of at most 100" | Stays: already labelled (D6) |
| C7 | A Live row's time | `Date.now()` at arrival, `app.js:944` | the timeline's "Time" column, `live/timeline.js:87`, `115`, `184-188` | **Stays, relabelled** (D6) |
| E1 | Rows loaded from a paged collection | `rows.length` | `app.js:1181`, `1184`, `1213-1214`, `1296`, `1321`, `1394-1395`, `1425-1426`, `2004-2005`, `2302`, `2321`, `2345-2346`, `2697`; `views/evidence.js:709`; `views/scenarios.js:259-260`; `views/traces.js:234-238`, `351`; `views/overview.js:333`, `412-415`; `ui/selector.js:72-84`; `v1/render.js:570`, `711` | Stays: already labelled (D7) |
| E2 | Sidebar count badges | `level.rows.length` | `navCount`, `app.js:152-169`, called at `app.js:1217`, `1430`, with a bare number | **Stays, relabelled** (D7) |

Nothing in `views/change-impact.js`, `views/status.js`, `views/run-state.js`,
`views/context.js` or `views/selectors.js` computes a figure that is printed.

## Decisions

### D1 — Runs by status come from the server

**The figure summarizes the record**: how many runs in this scope are in each
lifecycle state. Today it counts the rows of one page of
`GET /v1/projects/{project_id}/evaluation-runs/recent`, so with 70 runs in a
project it says "64 run(s) on this page" and the bars describe an arbitrary
cut. The label is honest; the figure is still not the one a reader wants.

**`GET /v1/projects/{project_id}/evaluation-runs/recent` gains
`status_counts`**: an object keyed by run status, each value a canonical
decimal string, over every run in the requested scope (project, and the
optional `agent_id` and `candidate_id` narrowing the route already takes), plus
`run_count`, their sum, also a decimal string. Statuses with no runs are absent,
not `"0"`, for the reason `statusBreakdown` gives today: a zero bar for a status
nothing reported is a figure nobody measured. The counts are read in the same
transaction as the page, so they describe the same moment. They are the same on
every page of one scope. They do not depend on `after`.

- **Additive `/v1` change** in `v0.13.0`: one new field on an existing response
  (`docs/compatibility.md`, "Platform `/v1` JSON field names": new fields are
  allowed, consumers must tolerate them).
- **No schema step.** A `GROUP BY status` over the runs the route already
  resolves for the scope. ADR 0063's recency indexes
  (`platform/recency.go:146-147`) order runs; they do not count them, so this
  is a scan of the scope's runs and its cost grows with them. Phase B measures
  it on a 10,000-run project and records the figure. If that figure is
  unacceptable, a stored counter or a new index is a schema step and a
  separate decision, not part of 110.
- **The browser** prints `run_count` and each status's count, and draws each
  bar from `decimalRatio(count, run_count)` (exclusion rule 2). The label
  becomes "N runs in <scope>, by status". "On this page" disappears, because it
  is no longer true. `statusBreakdown` is deleted.

### D2 — The gate-verdict tally comes from the server

**The figure summarizes the record**, and it is a figure about gate verdicts,
which the browser has never been allowed to compute
(`TestGateVerdictComesFromTheServer`). Today it counts the first page of
`GET /v1/projects/{project_id}/promotions`, which is identifier-ordered, not
newest first. So "on this page" is a tally of arbitrary decisions.

**`GET /v1/projects/{project_id}/promotions` gains `outcome_counts`**: an
object keyed by each recorded outcome, in the server's own words, each value a
decimal string, over every promotion in the project, plus `promotion_count`.
The same rules as D1: same transaction, independent of `after`, absent rather
than `"0"`.

- **Additive `/v1` change** in `v0.13.0`.
- **No schema step.** A `GROUP BY` over the project's promotions, which the
  list already reads by project. Phase B measures it beside D1.
- **The browser** prints the counts as given, labelled "N gate decisions
  recorded in this project". `verdictTally` is deleted. The list below the tally
  keeps reading the first page and keeps saying it is identifier-ordered.

### D3 — The most-observed behaviors come from the server

**The ranking summarizes the record.** "Behaviors by observations" claims to
say which behaviors this run did most. Today it sorts the first page of
`GET /v1/evaluation-runs/{run_id}/behaviors`, which is ordered by
`fingerprint_id`. When a run has more than one page of behaviors, the eight
drawn are the most observed of an alphabetical slice. The heading says "within
the first page", which is true and not useful.

**`GET /v1/evaluation-runs/{run_id}/behaviors` gains an optional query
parameter, `order=observations_desc`.** Under it, rows are ordered by their
authoritative observation count, highest first, ties broken by `fingerprint_id`
ascending. The cursor is the composite (count, `fingerprint_id`), the pattern
ADR 0063 set for recency. The response states `order` as the recent-runs
response does (`"fingerprint_id_asc"` or `"observations_desc"`), so a stored
response cannot be mistaken for the other order. Without the parameter the
route behaves exactly as today.

- **Additive `/v1` change** in `v0.13.0`: a new optional query parameter and a
  new response field, both allowed by `docs/compatibility.md`.
- **No schema step.** A run holds at most 512 behavior entries
  (`maxBehaviorEntries`, `platform/behavior.go:42`), and
  `loadBehaviorEntries` already reads them whole (`platform/sqlite.go:3335-3341`).
  The order is a bounded sort at read time. No index is needed.
- **A live run's counts move.** Paging through `observations_desc` while the run
  is still ingesting can show a behavior twice or skip one whose count changed
  between pages. The first page is exact at the moment it was read. The route
  documents this, and the Overview reads only the first page.
- **The browser** requests the first page with `order=observations_desc` and
  `limit=8` and draws it in the order given. The heading becomes "Most observed
  behaviors in this run". `topBehaviors` and `compareDecimal`'s use for ranking
  are deleted; `compareDecimal` stays only if something else needs it.

### D4 — `newestFirst` is deleted

It sorts runs by creation time in the browser. Nothing calls it: the
recent-runs route already returns `created_at_desc` and says so
(`platform/httpapi/dto.go:920-921`). It is exported and tested
(`TestOverviewReductionsStateTheirScopeAndInventNothing`), so it looks like a
supported reduction. A browser-side sort of a server-ordered collection is
exactly what this task forbids. It and its test case are removed.

### D5 — The waterfall's figures stay, labelled as facts about the page

**These figures describe the page the browser holds, not the trace.** The
waterfall shows at most 64 actions per page (task 100). An offset is a
difference between two recorded timestamps on that page, and the window is the
span of those timestamps. On page 2 of a trace, "From trace start" is measured
from page 2's earliest action, not the trace's start, so today's label is wrong
there.

**Why not the server.** A server-computed trace start, or trace duration, would
summarize the record, and it would be read as latency. The documentation
already refuses that reading ([`docs/webui.md`](../../webui.md#investigating-a-trace):
"They place bars; they are not latency, and they never feed a decision"). The
producers' clocks can disagree, and nothing corrects for it. A figure that
looked authoritative would hide that. The offsets exist to place bars on one
page, so they stay on the page and say so.

**The label rule.** Every figure the browser derives from timestamps names the
page it was derived from:

- the details panel's "From trace start" becomes **"From the earliest timestamp
  on this page"**;
- the summary's "window 1.2 s" becomes **"1.2 s between the earliest and latest
  timestamps on this page"**;
- the tree's "Depth" becomes **"Depth on this page"**, because a parent on
  another page reads *unresolved* and starts its own depth count.

B3's count of rows without a readable timestamp already sits beside "N
evaluated action(s) on page P" and needs no change.

- **No `/v1` change, no schema step.** Wording only.
- The arithmetic stays exact to 2^53 nanoseconds, as
  [`docs/webui.md`](../../webui.md#large-counters) states.

### D6 — Live counts stay, labelled as seen by this connection

**These figures describe what this tab received**: frames on one connection,
bounded and cleared on reconnect (ADR 0032, task 074). The server cannot state
them, and the authoritative counts beside them come from
`GET /v1/evaluation-runs/{id}/progress` (`TestHeaderCountsAreAuthoritative`).
This is 109's decision for its per-actor frame counts, applied to the rest.

**The label rule.** A count accumulated from frames carries one of the phrases
**"seen live"**, **"on this connection"** or **"on this stream"** in its visible
text. When it is drawn in SVG, the phrase is in its accessible name and title.
A bound's count carries **"not drawn"** or **"not listed"** and **"a display
limit"**. A time taken from the browser's clock is labelled **arrival**.

Most already comply (C1, C2, C4, C6). Three do not:

- **C3**: the agent node's "3 targets" becomes **"3 targets drawn"**. It counts
  the targets the viewport holds after eviction, not the agent's targets.
- **C5**: "N more in Live." becomes **"N more seen live; open Live"**.
- **C7**: the timeline's "Time" column header becomes **"Arrived"**, with
  "time this browser received the frame" as its accessible description. The
  code already reads it that way (`formatClock`'s comment,
  `live/timeline.js:179-183`); the screen does not say so.

- **No `/v1` change, no schema step, no identity change.** Wording only.

### D7 — Loaded-row counts stay, labelled as loaded

**These figures describe what the browser loaded**: rows of a paged collection
whose contract has no total (`docs/compatibility.md`, "Platform collection
paging"). Adding a total to every collection would be a `COUNT(*)` per page
read and a change to the paging contract. Nothing needs it; D1 and D2 add
totals where a reader needs a summary.

**The label rule.** A count of loaded rows says **"loaded"** or **"on this
page"** while `next_after` is present. It may say **"in all"** or **"this is the
whole collection"** only when `next_after` is absent, because the server has
then stated that no row follows. The count then equals the collection by the
server's own statement, not by inference. The code already follows this rule
everywhere in E1.

**E2 does not.** A sidebar badge prints `level.rows.length` with no label. It
becomes: the number alone when the collection is whole, the number followed by
"+" when `next_after` is present, and an accessible name stating
"N projects loaded" or "N projects in all" (likewise for runs).

- **No `/v1` change, no schema step.** Wording only.

## Scope

1. The three additive `/v1` fields (D1, D2, D3), in the HTTP adapter and the
   platform reads behind them, with the CLI unchanged unless it already prints
   the same figure.
2. The Overview reading them, and `statusBreakdown`, `verdictTally`,
   `topBehaviors` and `newestFirst` deleted (D1-D4).
3. The wording changes of D5, D6 and D7.
4. The static checks in §4, which make the list above the complete list.
5. `docs/webui.md` ("Charts are a second rendering of printed numbers", the
   Overview's panel table, the waterfall paragraph, "Large counters"),
   `docs/compatibility.md` (the three fields) and the CHANGELOG.

## Non-goals

- **No new verdict, score, rate, trend or health figure** anywhere, in the
  browser or on the server.
- **No identity change.** No `StableFeatures`, fingerprint, baseline key or
  scope changes.
- **No change to the realtime frame**, and no new route. Each `/v1` change
  extends a route the Overview already reads.
- **No totals on paged collections in general** (D7).
- **No server-side trace duration** (D5).
- **No JavaScript test runner.** The WebUI's tests stay Go assertions over the
  served sources, as 109 also states.
- **Nothing in a patch release.** `v0.12.x` keeps the current behavior, which
  `docs/webui.md` describes accurately since #175.

## Tests

### 1. Server

1. **`status_counts`.** A project with 70 runs in three statuses, read in two
   pages: both pages carry the same `status_counts`, its values sum to
   `run_count`, `run_count` is `"70"`, and narrowing by `agent_id` and by
   `candidate_id` counts only that scope. A status with no runs is absent.
2. **`outcome_counts`.** A project with more than 64 promotions: both pages
   carry the same counts, they sum to `promotion_count`, and the keys are the
   recorded outcome words.
3. **`order=observations_desc`.** A run with more behaviors than one page:
   pages concatenate to every behavior exactly once, in count order with
   `fingerprint_id` ties ascending, while the run is not ingesting. Without the
   parameter, the response is byte-identical to `main` apart from `order`. An
   unknown `order` value is `400`.
4. **Counters stay decimal strings** on the wire, up to the uint64 maximum,
   using the store's existing large-counter test fixtures.
5. Each new field is present on SQLite and Postgres
   (`TRUSTVIAN_TEST_POSTGRES_DSN`), through the conformance suites.

### 2. Browser sources

6. `views/overview-model.js` defines no `statusBreakdown`, `verdictTally`,
   `topBehaviors` or `newestFirst`, and `views/overview.js` reads
   `status_counts`, `outcome_counts` and requests `order=observations_desc`.
   `TestOverviewReductionsStateTheirScopeAndInventNothing` is rewritten to
   assert this.
7. The wording of D5, D6 and D7 is present, as literal strings, in the files
   named for each.

### 3. In headless Chrome (recorded in the PR)

8. The Overview of a project with 70 runs shows "70 runs" and bars that sum to
   it. Page 2 of a trace shows "From the earliest timestamp on this page". A
   Live timeline header reads "Arrived". The sidebar badge of a collection with
   more than 64 rows shows "64+".

### 4. The static check that keeps it true

**`TestBrowserDerivesOnlyListedFigures`** in `platform/webui/assets_test.go`.
It scans every script asset (`scriptAssets`, comments stripped with
`stripJSNoise`) for computation markers: `.reduce(`, `.sort(`, `Math.`,
`Date.parse(`, `Date.now(`, `+= 1`, `++`, `.length` or `.size` inside a
template literal or `String(…)`, and division of two identifiers. Each hit must
fall inside a function named in an allowlist in the test. The allowlist is the
table above after 110, keyed by file and function, and each entry names its
class: `viewport` (D5-D7), `geometry` or `control` (the exclusion rule). A hit
outside the allowlist fails with the file, the line and a pointer to this task.
Adding an entry is a reviewed decision about one figure, which is the point:
a new browser-side figure can no longer arrive unnoticed.

**`TestViewportFiguresAreLabelled`** in the same file. For each `viewport`
entry, the strings that function renders contain one of that class's label
phrases (D5, D6 or D7). It is the label half of 109's rule, made a check.

The existing guards stay as they are: `TestGateVerdictComesFromTheServer`,
`TestUint64CountersAreNeverParsedAsNumbers`, `TestHeaderCountsAreAuthoritative`,
`TestStatusViewComputesNothing` and `TestChangeImpactComputesNothing`.

## Acceptance criteria

1. Every figure the browser prints is either a `/v1` field, formatted per the
   exclusion rule, or an allowlisted viewport figure whose label says what it
   describes.
2. The Overview's runs-by-status, gate-verdict tally and most-observed
   behaviors are the server's figures over the whole scope or run, and say
   nothing about "this page".
3. No figure about the record changes with the page size. Reading the Overview
   with `limit` at 1 and at 64 shows the same numbers.
4. `TestBrowserDerivesOnlyListedFigures` and `TestViewportFiguresAreLabelled`
   pass, and fail when a `.reduce(` is added to any view outside the allowlist
   (checked once by hand in the PR).
5. `docs/webui.md` no longer lists the Overview's tallies as browser
   computations, `docs/compatibility.md` lists the three new fields, and the
   CHANGELOG states them as additive `/v1` changes.

## What it does not change

| Decision | `/v1` | Schema step | Identity |
|---|---|---|---|
| D1 runs by status | `status_counts`, `run_count` on `GET /v1/projects/{project_id}/evaluation-runs/recent` | none | none |
| D2 gate-verdict tally | `outcome_counts`, `promotion_count` on `GET /v1/projects/{project_id}/promotions` | none | none |
| D3 most-observed behaviors | `order=observations_desc` and the `order` field on `GET /v1/evaluation-runs/{run_id}/behaviors` | none | none |
| D4 `newestFirst` | none | none | none |
| D5 waterfall | none | none | none |
| D6 Live counts | none | none | none |
| D7 loaded counts | none | none | none |

No verdict, gate, suggestion, score or fingerprint is touched by any of them.

## Phase B

Built from this file as merged, one commit per layer:

1. the three server fields, with their tests on both stores;
2. the Overview reading them, and the four browser reductions deleted;
3. the wording of D5, D6 and D7;
4. the two static checks;
5. the docs: `docs/webui.md`, `docs/compatibility.md`, CHANGELOG and "What
   shipped".

D1's cost is measured on a 10,000-run project, and the figure is recorded in
"What shipped".
