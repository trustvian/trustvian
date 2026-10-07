# 0065 — Collectors report pipeline status; the control plane holds it in memory

**Status:** Accepted (task [105](../tasks/v0.12/105-pipeline-status-surface.md)).
Builds on [ADR 0031](0031-control-plane-owns-ingest-and-http-is-an-adapter.md),
[ADR 0032](0032-realtime-is-bounded-ephemeral-not-authoritative.md) and
[ADR 0038](0038-collector-evaluation-ingest-is-an-http-adapter.md), and changes
none of them.

## Context

The commonest first experience of Trustvian is an empty Live view, and the
causes have different fixes:

- the exporter points at the wrong port;
- the producer set no `service.name`;
- the telemetry arrived only at HTTP fidelity;
- the Collector never started.

Task 105 adds a status surface to tell those cases apart.

Everything the control plane knew before task 105 arrived inside an ingest
envelope. A span that never became a record — it mapped to no valid Event,
its actor was unbound, nothing sent it at all — left no trace there. The facts
that explain an empty view exist only inside the Collector.

The engine runs inside the Collector too, and exposes no statistics accessor.
The milestone keeps the engine unchanged.

## Decision

### 1. The Collector pushes; nothing pulls

A processor with a `status:` block posts one report to
`POST /v1/collectors/{collector_id}/status` at Start and then every interval
(10 s by default). It sends from one goroutine, one request at a time, with no
queue and no retry. A failed report is superseded by the next, because every
counter is cumulative since the process started. A slow or absent control plane
therefore costs the Collector one bounded request per interval, and Shutdown
cancels a request in flight.

Push rather than pull because the control plane must not reach into a Collector.
Collectors bind no API listener beyond health, may sit behind NAT in a shared
deployment, and are not the control plane's to enumerate. Pull would also need
the control plane to know where every Collector is, which nothing records.

### 2. Status is configured separately from evaluation

The `status:` block is independent of `evaluation:`. `trustvian dev --check`
starts a status-only Collector, so checking the pipeline creates no evaluation
run and takes no baseline lock. A Collector that feeds a run need not report
status at all.

### 3. The control plane holds the latest report per Collector, in memory

The registry keeps one report per `collector_id`, bounded at 16 Collectors. A
Collector silent for 5 minutes is forgotten. Nothing is persisted, and a
restart forgets everything. That is ADR 0032's realtime position applied to a
second kind of notification-grade state: a reader re-establishes it by asking
again, and no gate, comparison or promotion reads it.

- A late report with an older sequence from the same process is ignored. A
  delayed request must not roll counters back.
- A report from a new process (a new `instance`) replaces its predecessor
  whatever its sequence, so a stable `collector_id` across restarts works.
- At the bound, a new Collector is refused rather than another evicted. A
  reporter must not be able to hide another by connecting.

### 4. Ages, not timestamps

A report carries the Collector's uptime and each producer's time since its last
span, and the control plane converts both against its own clock at receipt. A
Collector with a skewed clock cannot make a producer look fresh or stale.

### 5. Every derived answer is the control plane's

Whether a Collector is reporting or stale, which view an interface opens on
(`landing`), and every suggestion (ADR 0064) are computed by the control plane
at read time and published in `GET /v1/status`. The CLI prints the document,
and the WebUI renders it.

### 6. Changes are notified, idleness is not

A report that changes what a reader would see publishes a `status_changed`
realtime event with an empty scope. That covers a new Collector, a new process,
or any count, section or name, but not a report that moves only its sequence,
uptime or ages. An empty scope reaches only unfiltered streams, so run-scoped
watchers and the TUI never see it. A Collector going stale publishes nothing,
because nothing reports it. The next read says so.

## Alternatives considered

**Derive status from ingest alone.** Rejected. Spans that never became records
are the main thing the surface must explain, and ingest cannot see them. It
would also require an evaluation run before the pipeline could be checked.

**Persist status.** Rejected. Status describes a pipeline now, not evidence about
an actor. A persisted history would invite a "pipeline health over time" view,
which is analytics (092) and a health score both at once. It would also add a
schema step and a retention question for state with no durable consumer.

**Expose Collector status from the Collector's own HTTP port, and have the
control plane or the browser pull it.** Rejected for the reasons in § 1, and
because a browser reaching a Collector directly would be a second origin and a
second authority.

**Add an engine statistics accessor in this task.** Deferred. Baseline count,
maturity and fingerprint admission against the 512 bound need a core change.
The roadmap requires that to be reviewed on its own terms. The status document
reports the engine as unavailable and states why. The processor reports what
`Observe` returned, grouped by decision, and infers nothing about admission.

## Consequences

- Status is only as fresh as the last report: a Collector that dies looks
  *reporting* for up to 30 s and *stale* for 5 minutes, and the page states its
  read time.
- A Collector without a `status:` block, including any built before task 105,
  is invisible on the surface. Its spans still reach evaluation as before.
  What the control plane does see is its own ingest: it keeps the time of its
  last committed ingest record in memory (never persisted, so `GET /v1/status`
  still reads no store) and publishes it as `last_ingest_at`. Records arriving
  within the fresh window make `landing` `live`, and rule 1 then says records
  are arriving from a Collector with no `status:` block rather than asking
  whether `trustvian dev` runs. Like the reports, a restart forgets it.
- `service.name`, instrumentation scope and `telemetry.sdk.*` are displayed for
  the first time. They are held in memory and never persisted, so `v0.11.0`'s
  "service names stay unretained" still holds of retention.
- The 256 KiB request bound applies to reports. The processor shortens an
  oversized report, scope lists first, and marks what it shortened.
- **The surface is advisory and, on loopback, spoofable.** The report route is
  unauthenticated, like ingest, until task 070. A local caller can post a report
  under a known `collector_id` with a fresh `instance` and replace the real
  Collector's entry, or hold the 16 slots with invented Collectors. That can
  mislead a reader of the Status view. It cannot reach a decision, a baseline, a
  gate or a promotion, because nothing reads status but the status document
  itself, and the `landing` field only chooses a view.
- A producer controls the names it reports. The Collector keys every reported
  name by its sanitized form, so names that differ only in control bytes or past
  the 256-byte bound collapse into one entry rather than producing a duplicate
  that would make the control plane refuse every later report.
- A future engine accessor would fill the engine section and let suggestion
  rule `status.admission_near_bound` fire. It is a separately reviewed change.
