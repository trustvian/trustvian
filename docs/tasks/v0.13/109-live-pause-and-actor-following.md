# 109 — Live Observatory: pause, resume and following one actor

Status: Implemented (Phase B) — see [What shipped](#what-shipped)
Milestone: [`v0.13.0`](../../ROADMAP.md#v0130--proposed); accepted by the maintainer
Depends on: [059](../v1.0/059-realtime-infrastructure.md),
[074](../v1.0/074-zero-input-live-behavior-webui.md),
[067](../v1.0/067-event-history-capability-boundary.md) (all implemented)
Decision records: [ADR 0032](../../adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md),
[ADR 0036](../../adr/0036-webui-is-a-same-origin-adapter-over-v1.md)
Found by: the quickstarts' live mode (three workers under one run)

**Implementation starts after `v0.12.0` ships.** This file is the whole
contract for it. The engine, `StableFeatures`, fingerprints and baseline keys
are unchanged.

## Developer problem

The quickstarts' live mode runs three workers, each its own process with its
own `service.name`, under one `trustvian dev` run. The LangGraph quickstart
measured a 5½-minute run on `v0.11.0`: 76 tickets, about 1,000 observations
(about 3 frames per second), 13 behaviors. What it reports:

> **One agent in the live view, three actors in the evidence.** The live
> view's Agent, its Behavior flow and every Live stream row name the run's
> agent, `support-fleet`. Which worker did what is in each observation's
> `actor_id` (`support-agent`, `billing-agent`, `accounts-agent`), which
> `GET /v1/evaluation-runs/<run>/observations` returns.

Two things are wrong with the live picture:

1. **Actors cannot be told apart live.** The frame carries the run's scope and
   the behavior's `StableFeatures`, but not the observation's `actor_id`. The
   graph's source is the scope's agent, so every actor is drawn as that one
   agent. Worse, an edge is keyed by `fingerprint_id` alone and its decision and
   risk are "latest wins", so when two actors perform the same behavior, one
   actor's decision overwrites the other's on the shared edge.
2. **A reader cannot stop the stream to read it.** Pulses keep arriving, rows
   scroll out of the 100-row timeline, and a `NEW` behavior can open itself in
   the inspector while you are reading something else.

## Current state, verified against `main` (`a3dcaf9`)

| Fact | Evidence |
|---|---|
| The realtime observation carries the scope, `StableFeatures` (which include `actor_type`), fidelity, layer, decision, scores and `new_behavior`, and no `actor_id` | `platform/realtime.go:121-164` (`RealtimeObservation`); published in `platform/controlplane.go:1087-1134`; wire shape `platform/httpapi/dto.go:643-676` (`realtimeObservationDTO`), mapped in `platform/httpapi/mapping.go:262-305` |
| The record the frame is published from has `ActorID` and `ActorType` | `decision_record.go:52-56`; the retained observation keeps both (`platform/observation.go:82-83`) and `GET /v1/evaluation-runs/{id}/observations` returns them as `actor_id`, `actor_type` (`platform/httpapi/dto.go:1005-1006`) |
| The record's actor type and the behavior's actor type are stored as two fields | `platform/observation_columns.go:135-141` (`actor_type`, `behavior_actor_type`) |
| The record's `ActorID` has no length bound at ingest; every other string on the frame is held to 256 bytes | `event/event.go:77-80` checks only that it is non-empty; `validateBehaviorIdentity` bounds operation and target names with `validateText` (`platform/behavior.go:799-840`, `platform/domain.go:64`, `568-590`) |
| `RealtimeFilter` is three optional identifiers, validated with `validateID`, matched against the immutable scope only | `platform/realtime.go:187-242` |
| The Live page holds one unfiltered subscription | `platform/webui/assets/app.js:876-879`; `TestLiveSubscribesUnfilteredAndWithNoRunID` |
| The graph's source is `scope.agent_id`; an edge is keyed by `fingerprint_id` | `platform/webui/assets/live/model.js:183-188` |
| The canvas draws exactly one source node, labelled with the agent's name | `platform/webui/assets/live/graph.js:184-200`, `246-259`; label from `app.js:723-728` |
| Timeline rows name the agent, at most 100 of them | `platform/webui/assets/live/timeline.js:13`, `116`, `166` |
| A reconnect clears the cards, the graph, the timeline segment and the inspector | `platform/webui/assets/app.js:981-992` |
| The selected run's counts are one `GET …/progress` per selection, never accumulated from frames | `platform/webui/assets/app.js:832-871`; `docs/webui.md` "The header is operational" |
| Bounds: 16 cards, 8 sources, 64 targets, 128 edges, 100 timeline rows, 64 frames buffered during resync | `live/model.js:28-31`, `live/timeline.js:13`, `v1/realtime.js:19`; `docs/webui.md:306-314`; `TestLiveBoundsAreTheDocumentedValues` |
| There is no pause control | no source under `platform/webui/assets/` |

## Scope

### 1. The actor on the frame

**Decision 1: the realtime observation gains two fields, `actor_id` and
`actor_type`, copied from the record that was ingested.** In
`RealtimeObservation`, `realtimeObservationDTO` and the mapping between them,
next to `fingerprint_id`:

```json
"observation": {
  "sequence": "412",
  "actor_id": "billing-agent",
  "actor_id_state": "carried",
  "actor_type": "service",
  "fingerprint_id": "…",
  "behavior": { "actor_type": "service", "operation_category": "tool", … },
  …
}
```

- **Source.** `record.ActorID` and `record.ActorType` in `publishObservation`,
  the same values the retained observation stores. Nothing is read from the
  database per frame, and nothing is derived.
- **`actor_type` is the record's, not the behavior's.** The behavior's
  `actor_type` stays where it is, inside `behavior`, as identity. The two are
  equal in genuine engine output and are stored as two columns already. The
  frame mirrors the observations route, which also returns both.
- **Always present.** `actor_id` and `actor_id_state` are emitted on every
  observation frame, like `fidelity`, so a consumer never decides what an
  absent field meant.
- **Bounded.** `actor_id` is the only string on the frame whose length nothing
  at ingest bounds. ADR 0032's worst-case memory, subscribers × queue × frame,
  is a constant only while every frame is. So the frame carries the actor id
  when it is at most 256 bytes, the bound every other string on the frame
  already meets (`maxIdentifierLength`), with `actor_id_state: "carried"`.
  Above it, `actor_id` is `""` and `actor_id_state` is
  `"exceeds_frame_bound"`. The id is never truncated: two ids that agree on
  their first 256 bytes would become one actor. The retained observation still
  has the full value. With the bound, the worst case grows by at most
  64 × 64 × (256 bytes and the field names), about 1.1 MiB, and stays a
  constant.
- **Additive.** Two new JSON fields on an existing event, which
  `docs/compatibility.md` already allows ("Realtime SSE JSON field names …
  New fields — consumers must tolerate unknown fields"). No event name, filter
  name or existing field changes.

**Why this is display metadata, not identity.** The actor id is put on the
frame so a live view can say *who* did what. Nothing in 109 adds it to
`StableFeatures`, a fingerprint, a baseline key or a scope. The engine
already keys its baseline by `(Scope, ActorID, Environment)`
(`internal/baseline/baseline.go:242-246`), and that stays as it is: the frame
reports the actor the engine already used and changes no key. The Live view
groups frames by an id the server stated, which is equality, not a computation.
The fingerprint stays the behavior's identity across actors: the same behavior
from two actors is one fingerprint, and §2 draws it that way.

**Privacy.** The actor id is a record field, not content. It is already
returned by the observations route to the same same-origin caller. Task 050's
boundary is unchanged: no attribute, argument, prompt or completion reaches the
frame. `actor_type: user` means the id may name a person, which is
already true on the observations route. 109 widens where it is shown, not who
may read it.

### 2. The graph's source is the actor

**Decision 2: inside the selected run, the graph draws one source per actor,
and an edge per (actor, behavior).**

- The source key is the frame's `actor_id`. Frames with
  `actor_id_state: "exceeds_frame_bound"` share one source labelled
  **"actor id not carried (over 256 bytes)"**. It says why, and never claims to
  be one actor.
- **Label.** When the actor id equals the scope's `agent_id`, the source is
  labelled exactly as today: the agent's name from the label cache, falling
  back to its id. Otherwise the label is the actor id, with the actor type in
  the node's accessible name and title. Actors are not platform entities, so
  there is no name to look up.
- **Bounds.** The existing 8-source bound, least-recently-observed eviction,
  and the existing statement: "source nodes: 8, N not drawn". Edges count
  against the existing 128-edge bound, which a run with many actors reaches
  sooner, since a behavior shared by `A` actors is `A` edges. That is stated
  the same way; neither bound changes.
- **Edges.** The edge key is the pair (actor key, `fingerprint_id`), built as a
  fixed-order JSON array like `scopeKey`, so no delimiter can collide.
  Decision, risk, scores, fidelity and layer stay "latest wins", now per actor.
  That removes the mixing described above: the engine decides per observation,
  under that actor's baseline, and each edge now shows its own actor's latest
  answer.
- **Layout.** The fixed left-to-right fan keeps its shape: sources stacked on
  the left in least-recently-observed order, targets on the right. One source
  is drawn where the single agent node is today.
- **`NEW` stays the run-level fact the frame states.** `new_behavior` means
  "this fingerprint was not already represented in the run's evidence". The
  badge goes on the edge of the frame that carried it, and on its target, as
  today. A later frame of the same behavior from another actor carries
  `new_behavior: false`, and that actor's edge has no badge. Nothing in the
  view says "new for this actor"; the inspector's sentence is unchanged.

**The single-actor picture is unchanged.** For a run with one actor whose
`service.name` equals dev's `--agent` (dev's default), the actor id equals
the scope's `agent_id`. The view then draws one source with today's label,
the same edges, targets, badges, accessible names and timeline rows. A test
proves it (§6).

### 3. Following one actor

**Decision 3: the rail lists the selected run's actors seen live in this view,
and selecting one narrows the view to that actor's frames.**

- **The list.** Under the selected run's card, an **Actors seen live** list:
  one row per actor key, with the actor id (or the bound label above), the
  actor type of its most recent frame, its last-seen time, and its frame count
  labelled *seen live*. Ordered least-recently-seen last. The heading says
  these are frames this connection received, not the run's actors. The run's
  actors are in its observations, and the row links to the run's
  **Observations** tab.
- **Bound.** At most **16** actors, the same as the scope cards. That is twice
  the graph's 8 sources, so an actor whose source the graph evicted can still
  be followed. Past 16, the least recently seen row is evicted and the list
  says "16 actors listed, N not listed". It is cleared when the selected run
  changes and on every reconnect, like the graph.
- **Narrowing.** Selecting an actor narrows three things to frames whose actor
  key equals it: the graph draws only that actor's source and edges, the
  timeline shows only its rows, and an inspector open on another actor's edge
  closes. It is a display filter by equality over what the view already holds,
  the same kind as the Projects and Runs filter boxes. It sends no request and
  computes nothing. Frames from other actors keep folding into the model while
  you follow one, so **All actors** returns to the full run at once.
- **What narrowing cannot show.** The timeline keeps 100 rows across all
  actors, so a quiet actor may have fewer rows on screen than its frame count.
  The narrowed timeline says "N of this actor's frames are in the last 100
  rows". If the graph evicted the actor's source, the narrowed graph says so
  and draws again on its next frame.
- **Ephemeral.** The chosen actor is not persisted. A reload starts on all
  actors, like follow and pin.
- **The header** keeps naming the run's agent, and adds "Following
  `billing-agent`" while an actor is chosen. Counts in the header stay the
  run's, from `…/progress`. The header never shows a per-actor count, because
  no `/v1` route returns one.

**Decision 4: `RealtimeFilter` does not gain an `ActorID` in 109.**

1. **An actor is not scope.** The filter matches the immutable hierarchy each
   event carries (ADR 0032 § 15). The actor is a field of one observation:
   lifecycle and `status_changed` events have none. An actor filter would first
   have to decide whether a narrowed stream still receives its run's
   completion, and either answer adds a semantic to the wire contract.
2. **The Live view would not call it.** The view holds one unfiltered
   subscription (`TestLiveSubscribesUnfilteredAndWithNoRunID`). Narrowing on
   the server would mean resubscribing on every actor change. A new connection
   resynchronizes and clears the viewport, so **All actors** could not return
   to the frames just shown. `.claude/rules/go.md` says not to add a knob
   without a caller.
3. **The isolation it would buy is not needed here.** ADR 0032 § 15 filters
   before enqueue so a busy run cannot fill a quiet subscriber's queue. Within
   one run, the measured rate is about 3 frames a second against a 64-frame
   queue.

Revisit it when a consumer that tails one actor exists, such as a CLI tail.
That change would then keep `validateID` and the 256-byte bound, as the three
identifiers do, and state the lifecycle rule.

### 4. Pause and resume

**Decision 5: pause freezes everything the Live view draws from frames, keeps
the connection, and buffers up to 1,024 observation frames.**

- **What pause freezes:** the graph, the timeline, the inspector, the actor
  list and the scope cards, plus the Overview's *Activity now* panel, which
  reads the same model. The cards are included because a card moving while the
  graph stands still would put two moments on one screen. Pulses stop.
  Following mode does not move the selection while paused.
- **What keeps working:** the connection, the connection chip, its backoff
  and resync, and `status_changed` handling for the Status view. Pausing never
  closes the `EventSource`, so pausing is never a disconnect. While paused you
  can still click an edge, a timeline row, a run card or an actor row, over the
  frozen state. Choosing another run card applies the existing rule (its
  topology starts empty) and draws its buffered frames on resume.
- **The header** shows `PAUSED` as a word in the connection area, beside the
  chip, which still reports the connection truthfully: `LIVE · PAUSED`. It
  also shows "N frames since pause · seen live, all runs" and "M in this run".
  These are frame counts, labelled the way cards label theirs. The count is not
  in a live region.
- **The buffer.** Observation frames that arrive while paused are kept, in
  order, up to **1,024**. Lifecycle frames do nothing to the view today
  (`onLifecycle` is a no-op), so they are not buffered; `status_changed` is
  delivered as normal. The rationale is measured: the quickstart produced about
  3 frames a second, so 1,024 frames is about five minutes of that fleet, and
  a 30-second pause is about 90. The bound is a fixed constant, stated in the
  bounds table and checked by `TestLiveBoundsAreTheDocumentedValues`.
- **Overrun is stated, never silent.** If frame 1,025 arrives while paused, the
  buffer is discarded and counting continues. The header says "Pause buffer
  full (1,024): this view will start again from now when you resume". On
  resume the viewport is cleared as after a reconnect, and the view says
  "Paused longer than this view holds: N frames were not kept. Retained
  history is in Evidence." This is the rule the resync buffer already follows:
  abandon and start again rather than drop a frame and draw the rest as
  complete. Folding only part of the pause would mislabel what is drawn, and a
  `NEW` behavior in the dropped part would vanish.
- **A reconnect while paused** discards the buffer, because frames from two
  connections cannot be joined. The frozen picture stays on screen, labelled
  "from the previous connection", and buffering restarts on the new
  connection. On resume the view applies the reset `onConnect` would have
  applied, then folds the new connection's buffered frames.

**Decision 6: resume folds the buffered frames without animation, re-reads
progress, and does not re-read the behaviors.**

- **No replay.** No pulse is drawn for a buffered frame. A pulse reports that a
  frame arrived *now*, and these did not. The buffered frames are folded into
  the model in arrival order, which updates edges, badges, the actor list and
  the timeline the same way they would have been updated live. The view is
  then redrawn once, with no animation, and continues live. "No replay" here
  means no pulse is replayed and nothing is fetched to reconstruct history.
- **Authoritative re-read:** one `GET /v1/evaluation-runs/{id}/progress` for
  the selected run, the read the header already relies on, so the record
  count, distinct-behavior count and completeness are the database's after a
  pause.
- **The behaviors are not re-read.** `GET /v1/evaluation-runs/{id}/behaviors`
  carries no actor and no frame. Drawing edges from it would put lines on the
  canvas with no frame behind them, and could not place them under an actor
  source. The header's authoritative distinct-behavior count, beside the
  graph, already shows when the graph lacks behaviors. Its **Behaviors** tab
  is one click away.
- **The inspector.** If a buffered frame is a `NEW` behavior and no edge is
  being inspected, the inspector opens it after the redraw, as the live rule
  does. Otherwise what you were reading stays open.
- **Announcement.** Pausing and resuming each produce one polite announcement:
  "Live view paused. The connection stays open." and "Live view resumed. N
  frames arrived while paused." (or the overrun sentence above). Each frame is
  not announced, matching the timeline's existing rule.

**Ephemeral.** The pause state is not persisted. A reload starts live, like
follow and pin.

**Accessibility.**

- One **Pause** button in the Live header, with `aria-pressed` reflecting the
  state. Its visible label stays "Pause". The state is carried by
  `aria-pressed`, by the `PAUSED` word and by the frames-since-pause line,
  never by colour alone.
- The keyboard shortcut is <kbd>P</kbd>, only while the Live view is shown, and
  only with no modifier key. It never fires while focus is in an `input`,
  `textarea`, `select` or contenteditable element, or in the selector's paste
  field.
- The announcements go to a polite live region. The header's existing
  `role="status"` region keeps carrying connection state.

### 5. What it is not

- **No history scrubbing or replay.** Retained history stays in Evidence (task
  067), and the view still says so.
- **No per-actor aggregate** beyond the frame counts shown, which are labelled
  *seen live*. No per-actor behavior count, rate, risk summary or trend.
- **No new verdict or score**, and no colour that means "this actor is bad".
- **No server-side actor filter** (decision 4) and no new route.
- **No change to the single-run watch table** ("Watching one run"). It keeps
  its 100-row viewport. Pause there is not part of 109.

## Tests (Phase B)

The WebUI has no JavaScript test runner. Its tests are Go assertions over the
served sources (`platform/webui/*_test.go`). Behaviour that only a browser can
show is checked in headless Chrome, and the PR records each check.

1. **Contract.** `realtimeObservationDTO` carries `actor_id`, `actor_id_state`
   and `actor_type` on every observation frame, equal to the ingested record's.
   A 256-byte id is carried. A 257-byte id gives `""` with
   `exceeds_frame_bound`, and the retained observation still has all 257
   bytes. Lifecycle and `status_changed` frames carry no actor.
   `TestSSECarriesNoRawEventPayload` still passes with the new fields.
2. **Identity untouched.** Two actors with the same behavior produce frames
   with the same `fingerprint_id` and the same `behavior`. No baseline key,
   fingerprint or `StableFeatures` value differs from `main` for the same
   input.
3. **Single actor unchanged.** The same frame sequence from one actor whose id
   equals the scope's `agent_id` renders the same canvas as `main`: node count,
   edge count, every text node, every accessible name and every `NEW` badge.
   Internal key attributes are excluded.
4. **Actor bounds.** The source bound (8) and the actor list bound (16) evict
   least-recently-seen, and both state saturation with the count not shown.
   The values are added to `TestLiveBoundsAreTheDocumentedValues`.
5. **Narrowing.** Selecting an actor sends no request; the graph, timeline and
   inspector show only its frames; **All actors** restores the rest without a
   request; another actor's frame arriving while narrowed changes nothing
   visible.
6. **The shared behavior.** Two actors, one fingerprint, two edges to one
   target. Each edge shows its own actor's decision. Only the edge whose frame
   said `new_behavior: true` carries `NEW`, and the header's behavior count is
   the progress route's.
7. **Pause keeps the connection.** Pausing closes no `EventSource` and opens no
   new one, and the chip keeps reporting the stream's real state.
8. **Resume re-reads and does not replay.** Resume makes exactly one
   `…/progress` request and no `…/behaviors` request, creates no pulse element,
   and draws the buffered frames' edges and rows.
9. **Overrun is stated.** With a smaller bound injected (the existing pattern
   for bounds), frame bound + 1 discards the buffer, shows the overrun line, and
   on resume clears the viewport and states the number of frames not kept.
10. **Reconnect while paused** keeps the frozen picture labelled as the
    previous connection's, and resume applies the reset.
11. **Keyboard and screen reader.** <kbd>P</kbd> toggles pause on Live, and does
    nothing in a text field or with a modifier. The button's `aria-pressed`
    follows the state. Pause and resume each produce one polite announcement
    and frames produce none.
12. **Layouts.** Desktop and 390 px wide, light and dark: the actor list,
    `PAUSED` and the frames-since-pause line are readable, and nothing relies on
    colour alone.
13. **`TestHTTPAdapterDoesNotComputeEvaluationLogic`** is extended: the
    realtime mapping may not read the store or the observation history to fill
    a frame, so the actor can only come from the published event.

## Acceptance criteria

1. In the quickstarts' live mode, three actors in one run appear as three
   sources and three rows in **Actors seen live**, each labelled by its
   `service.name`.
2. Following one actor narrows the graph, timeline and inspector to it, and
   **All actors** restores the run, with no request either way.
3. Pausing for 30 seconds keeps the connection, shows `PAUSED` and a growing
   frames-since-pause count, and resuming re-reads progress, redraws the
   buffered frames with no pulse, and continues live.
4. A run with one actor named like the agent looks the same as on `main`.
5. Every number on the screen is a `/v1` field or a labelled frame count, and
   every bound in `docs/webui.md` states its saturation.
6. `docs/webui.md`, `docs/compatibility.md` and the CHANGELOG describe the new
   fields, the list, pause and the bounds.

## Risks

- **One fingerprint drawn from several sources reads as several behaviors.**
  It is one behavior performed by several actors, and the drawing must not
  imply otherwise. Mitigations:
  - the edge's operation label and target are identical across actors;
  - the inspector names the fingerprint and lists the other actors this view
    has drawn it from, labelled *seen live*;
  - the canvas legend says "one line per actor and behavior";
  - the header's behavior count is the progress route's distinct count, never
    a count of lines.
- **Edge pressure.** A behavior shared by every actor costs one edge per actor,
  so 128 edges are reached sooner. On the quickstart's 13 behaviors and 3
  actors, Phase B measures the edge count and reports it. If the bound saturates
  in ordinary use, that is stated, not hidden, and raising it is a separate
  decision.
- **A long actor id.** Covered by the 256-byte frame bound and its stated
  state. Whether ingest should refuse such ids is a different decision, not
  taken here.
- **Pause reads as safety.** A paused view could be mistaken for a paused
  agent. `PAUSED` sits beside `LIVE`, and the announcement says the connection
  stays open. Nothing about the agent or the run changes.

## Phase B

Phase B is built from this file as merged, one commit per layer:

1. the frame fields;
2. the graph source per actor;
3. the actor list and the narrowing;
4. pause and resume;
5. the docs: `docs/webui.md`, `docs/compatibility.md`, CHANGELOG and "What
   shipped".

It is measured with the quickstarts' live mode, three actors in one run. The
measurement records: the actors listed, following one, and a 30-second pause
with its frames-since-pause count and what the redraw showed.

## What shipped

Phase B, in five commits, one per layer, with decisions 1–6 as written. The
engine, `StableFeatures`, fingerprints, baseline keys and `RealtimeFilter`
are unchanged, and no route was added. Each departure below was taken while
building, and none changes a decision.

| Specified | Shipped | Why |
|---|---|---|
| Decision 1: `actor_type` is the record's, copied | Copied only when it is one of the six `event.ActorType` values; otherwise `""`, never substituted | Ingest validates the behavior's actor type but not the record's (`platform/behavior.go:811`; nothing checks `record.ActorType`). A 5,000-byte record `actor_type` was accepted at ingest, so copying it would put an unbounded string on every frame, against decision 1's own bound. Genuine engine output always carries a recognized type, because `event.Validate` refuses any other. Reported before building; the maintainer said to continue, and this was the recommended option |
| "Current state" `file:line` references, verified at `a3dcaf9` | Re-verified on `main` at `97e6dbe` | Moved since: `httpapi/dto.go` 650-683 and 1012-1013, `httpapi/mapping.go` 265-306, `app.js` 884 and 3656 (the subscription), 744-749 (the label), 997-1009 (the reconnect reset), 841-878 (progress), `live/model.js` 181-186, `live/graph.js` 158, 204 and 250-263, and the bounds table at `docs/webui.md` 340-350. Every other row held |
| The **Actors seen live** list "under the selected run's card" | Below the run cards, outside their listbox | The cards are `role="option"` in a listbox; toggle buttons nested in an option would not be reachable as buttons |
| "the row links to the run's Observations tab" | One link under the list's heading, "The run's observations name every actor." | No `/v1` route narrows observations by actor, so every row would open the same page |
| Layout: "the fixed left-to-right fan keeps its shape" | It does. With more than one source, a line's operation label sits where the lines meet the target, and its decision and risk stay at the line's middle | At the middle, three actors' labels for one behavior were drawn at three points and read as three behaviors. At the target, one behavior's labels coincide. One source draws exactly as before |
| Frame without actor fields | Drawn as the scope's agent | A server older than 109 sends none; that is how such a frame was always drawn |

The counts the view adds are frame counts and arrival times, labelled the way
[task 110](110-browser-computes-nothing.md)'s D6 asks: an actor row says
"N seen live" and "last arrival", the pause line says "seen live, all runs",
the narrowed timeline says "frames seen live", and the actor list's bound
says "not listed" and "a display limit".

### Measured: three actors in one run

The QuickStart agent as three workers under one `trustvian dev` run
(`OTEL_SERVICE_NAME=support-fleet`), each worker with its own
`OTEL_SERVICE_NAME`, built from this branch, read in headless Chrome over the
DevTools protocol:

| | Measured |
|---|---|
| Rate, workers pausing 1 s between runs | 14.7 frames a second (147 in 10 s) |
| Actors seen live | `billing-agent` 88, `support-agent` 55, `accounts-agent` 55 seen live, each `ai_agent`, each its own source |
| Lines on the QuickStart's 6 behaviors | 16 (5 + 5 + 6), against the 128-edge bound |
| Following `billing-agent` | 1 source, 6 lines; 45 timeline rows, "45 of this actor's 97 frames seen live are in the last 100 rows"; header "Following billing-agent"; inspector closed. No request |
| **All actors** | 3 sources, 16 lines. No request |
| A 30-second pause | 487 frames since pause, 487 in this run; canvas unchanged and no pulse throughout; chip `Live` beside `PAUSED` |
| Resume | 0 pulses; "Live view resumed. 487 frames arrived while paused."; one `…/progress` read, which moved the header's Observations from 55 to 798; no `…/behaviors` read |
| Overrun, workers not pausing | 45.7 frames a second; the buffer filled after 21 s ("1030 frames since pause … Pause buffer full (1,024)"); resume said "Paused longer than this view holds: 1126 frames were not kept. Retained history is in Evidence." and drew from then on |
| One actor named like the agent | The QuickStart agent's candidate run drew a canvas byte-identical, internal key attributes removed, to `main` at `97e6dbe`'s |

The rate is about five times the 3 frames a second this specification
assumed, so 1,024 frames hold about 70 seconds of this fleet rather than five
minutes, and the overrun is reachable in ordinary use by a fast producer. It
is stated, as decision 5 requires; raising the bound is a separate decision.
No other bound saturated.

