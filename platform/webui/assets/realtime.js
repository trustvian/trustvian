// Bounded realtime client over the /v1 SSE endpoint.
//
// Realtime notifies; the database decides. Nothing here is authoritative: every
// number the page displays as a count comes from an authoritative read, and
// this module's job is to say when to take one and to show a bounded view of
// what is arriving in between.
//
// The validation and bookkeeping are plain functions with no DOM and no
// network, so what they do is readable without running a browser. The session
// class below is the only part that touches EventSource.

// PENDING_MAX bounds frames buffered while an authoritative read is in flight,
// matching the server's per-subscriber queue.
//
// Overflow abandons the stream and resynchronizes rather than dropping a frame.
// The distinction is the whole point: a client that discarded a notification
// could not describe what it missed, so it must not claim continuity it does
// not have.
export const PENDING_MAX = 64;

// DISPLAY_MAX bounds visible observation rows, evicting the oldest.
//
// Deliberate truncation of a *display*. That this is a different kind of limit
// from PENDING_MAX is load-bearing: one would be data loss, the other is a
// viewport.
export const DISPLAY_MAX = 100;

// HANDSHAKE_TIMEOUT_MS bounds how long a connection may exist without becoming
// a Trustvian stream.
//
// EventSource surfaces heartbeat comments to the network layer but not as
// events, so a server sending only heartbeats holds a connection open forever
// while never synchronizing it — and no read-idle bound can see that, because
// bytes are arriving. Three different questions, not to be conflated:
//
//   connection establishment   did the HTTP request succeed?
//   handshake establishment    did it become a Trustvian stream?
//   active liveness            has an established stream gone quiet?
//
// This is the second. A valid stream_ready cancels it; heartbeats do not
// extend it.
export const HANDSHAKE_TIMEOUT_MS = 30000;

// BACKOFF_MS is the bounded reconnect sequence, reset after a successful
// handshake and resync. One timer at a time and no unbounded counter: this is
// one developer on loopback, and task 069 owns load behaviour.
export const BACKOFF_MS = Object.freeze([250, 500, 1000, 2000, 4000, 5000]);

// KNOWN_KINDS is what this page understands.
//
// EventSource only delivers named events a listener was registered for, so a
// future additive kind is ignored by construction rather than by a check that
// could be forgotten. That is the forward-compatibility rule the contract asks
// for, achieved structurally.
export const KNOWN_KINDS = Object.freeze([
  "evaluation_created",
  "evaluation_started",
  "observation",
  "evaluation_completed",
  "evaluation_failed",
  "evaluation_cancelled",
]);

// TERMINAL_KINDS end a run. Each triggers exactly one final authoritative read
// so closing counts come from the database.
export const TERMINAL_KINDS = Object.freeze([
  "evaluation_completed",
  "evaluation_failed",
  "evaluation_cancelled",
]);

export const isKnownKind = (kind) => KNOWN_KINDS.includes(kind);
export const isTerminalKind = (kind) => TERMINAL_KINDS.includes(kind);

// States the live view can be in. FAILED is terminal for a view that was never
// live; a view that has been live reconnects instead.
export const STATE = Object.freeze({
  IDLE: "idle",
  CONNECTING: "connecting",
  RESYNCING: "resyncing",
  LIVE: "live",
  RECONNECTING: "reconnecting",
  FAILED: "failed",
});

// parseFrame decodes one SSE data payload.
//
// Returns null rather than throwing: a malformed frame is a stream problem the
// caller decides about, not an exception to unwind through an event listener.
export function parseFrame(text) {
  try {
    const parsed = JSON.parse(text);
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      return null;
    }
    return parsed;
  } catch (ignored) {
    return null;
  }
}

// validateStreamReady enforces the handshake contract.
//
// All three values are fixed by task 059 and all three are checked. The bus
// retains no history, so a server claiming replay_available would be
// describing a capability that does not exist, and believing it would mean
// skipping the resync that makes the view correct.
export function validateStreamReady(payload) {
  return (
    payload !== null &&
    payload.version === "1" &&
    payload.replay_available === false &&
    payload.resync_required === true
  );
}

// validateEvent checks wire integrity for a known event.
//
// Structural only: version, the kind agreeing with the SSE event name, the
// scope naming the run actually being watched, and the correct payload half
// present. Not a second copy of domain validation — the control plane owns
// that, and duplicating it here would be a rule that can disagree with itself.
export function validateEvent(eventName, payload, watchedRunID) {
  if (payload === null) {
    return { ok: false, reason: "frame is not a JSON object" };
  }
  if (payload.version !== "1") {
    return { ok: false, reason: "unsupported payload version" };
  }
  if (payload.kind !== eventName) {
    // A frame whose kind disagrees with its event name is ambiguous about what
    // it is, and picking either reading would be a guess.
    return { ok: false, reason: "payload kind does not match the event name" };
  }
  const scope = payload.scope;
  if (scope === undefined || scope === null || typeof scope !== "object") {
    return { ok: false, reason: "event carries no scope" };
  }
  if (scope.run_id !== watchedRunID) {
    return { ok: false, reason: "event belongs to a different run" };
  }

  const hasObservation = payload.observation !== undefined && payload.observation !== null;
  const hasEvaluation = payload.evaluation !== undefined && payload.evaluation !== null;

  if (eventName === "observation") {
    if (!hasObservation || hasEvaluation) {
      return { ok: false, reason: "observation event carries the wrong payload half" };
    }
    return { ok: true, reason: "" };
  }
  if (!hasEvaluation || hasObservation) {
    return { ok: false, reason: "lifecycle event carries the wrong payload half" };
  }
  return { ok: true, reason: "" };
}

// PendingBuffer holds frames arriving during an authoritative read.
//
// push reports whether the frame was retained. false means the stream must be
// abandoned and resynchronized — never that a frame was quietly dropped.
export class PendingBuffer {
  constructor(capacity = PENDING_MAX) {
    this.capacity = capacity;
    this.frames = [];
    this.overflowed = false;
  }

  push(frame) {
    if (this.frames.length >= this.capacity) {
      this.overflowed = true;
      return false;
    }
    this.frames.push(frame);
    return true;
  }

  drain() {
    const drained = this.frames;
    this.frames = [];
    return drained;
  }

  get size() {
    return this.frames.length;
  }
}

// RowWindow is the bounded observation viewport.
//
// Oldest-first eviction, cleared on every stream restart so two disconnected
// streams are never joined into one apparent sequence.
export class RowWindow {
  constructor(capacity = DISPLAY_MAX) {
    this.capacity = capacity;
    this.rows = [];
  }

  add(row) {
    this.rows.push(row);
    while (this.rows.length > this.capacity) {
      this.rows.shift();
    }
  }

  clear() {
    this.rows = [];
  }

  get size() {
    return this.rows.length;
  }
}

// nextBackoff returns the delay for attempt n, capped.
export function nextBackoff(attempt) {
  const index = Math.min(attempt, BACKOFF_MS.length - 1);
  return BACKOFF_MS[Math.max(index, 0)];
}

// RealtimeSession owns one watched run.
//
// Exactly one EventSource and one generation are active at a time. Every async
// continuation re-checks its generation before touching state, so a late
// response from generation N cannot mutate generation N+1 — the ownership rule
// the TUI enforces with contexts, expressed here with a token because the
// browser has no cancellation primitive for an in-flight promise.
export class RealtimeSession {
  // deps: { getRun, getProgress, realtimePath, now } — injected so the session
  // has no import-time dependency on the transport it drives.
  constructor(deps, callbacks) {
    this.deps = deps;
    this.callbacks = callbacks;

    this.generation = 0;
    this.runID = "";
    this.source = null;
    this.handshakeTimer = null;
    this.reconnectTimer = null;
    this.pending = new PendingBuffer();
    this.rows = new RowWindow();

    this.state = STATE.IDLE;
    this.synchronized = false;
    this.everLive = false;
    this.attempt = 0;
    this.resyncInFlight = false;
    this.finalResyncPending = false;
  }

  // watch starts or restarts on a run, abandoning whatever came before.
  watch(runID) {
    this.stop();
    this.runID = runID;
    this.everLive = false;
    this.attempt = 0;
    this.connect();
  }

  // stop tears down everything this session owns.
  //
  // Called on quit, run change, manual reconnect and protocol failure. Every
  // timer and the socket are released here, so there is one teardown path
  // rather than one per caller.
  stop() {
    this.generation += 1;
    this.clearHandshakeTimer();
    this.clearReconnectTimer();
    this.closeSource();
    this.pending = new PendingBuffer();
    this.synchronized = false;
    this.resyncInFlight = false;
    this.finalResyncPending = false;
    this.rows.clear();
    this.emitRows();
    this.setState(STATE.IDLE);
  }

  closeSource() {
    if (this.source !== null) {
      // Closed here rather than left to the browser. EventSource's implicit
      // retry has no notion of protocol validity and would reconnect forever
      // to a server that never completes the handshake, which would make the
      // browser the authoritative reconnect state machine instead of this
      // code.
      this.source.close();
      this.source = null;
    }
  }

  clearHandshakeTimer() {
    if (this.handshakeTimer !== null) {
      clearTimeout(this.handshakeTimer);
      this.handshakeTimer = null;
    }
  }

  clearReconnectTimer() {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  setState(state) {
    this.state = state;
    this.callbacks.onState(state, { everLive: this.everLive, runID: this.runID });
  }

  emitRows() {
    this.callbacks.onRows(this.rows.rows);
  }

  // connect opens one generation.
  connect() {
    const generation = this.generation;
    this.synchronized = false;
    this.pending = new PendingBuffer();
    // Rows belong to a stream, not to a run: a new connection starts an empty
    // viewport so nothing implies continuity across the gap.
    this.rows.clear();
    this.emitRows();
    this.setState(STATE.CONNECTING);

    const source = new EventSource(this.deps.realtimePath(this.runID));
    this.source = source;

    this.handshakeTimer = setTimeout(() => {
      if (generation !== this.generation) {
        return;
      }
      this.failStream("the stream never completed its handshake");
    }, HANDSHAKE_TIMEOUT_MS);

    source.addEventListener("stream_ready", (event) => {
      if (generation !== this.generation) {
        return;
      }
      const payload = parseFrame(event.data);
      if (!validateStreamReady(payload)) {
        this.failStream("the stream sent an invalid stream_ready");
        return;
      }
      if (this.synchronized) {
        // A second handshake mid-stream is ambiguous about which synchronized
        // view is current.
        this.failStream("the stream sent a second stream_ready");
        return;
      }
      // The deadline has served its purpose and must not survive it: left
      // armed, it would tear down a healthy established stream.
      this.clearHandshakeTimer();
      this.synchronized = true;
      this.resync(generation, { final: false });
    });

    for (const kind of KNOWN_KINDS) {
      source.addEventListener(kind, (event) => {
        if (generation !== this.generation) {
          return;
        }
        this.onDomainEvent(generation, kind, event.data);
      });
    }

    source.onerror = () => {
      if (generation !== this.generation) {
        return;
      }
      // EventSource reports transport errors without distinguishing them;
      // whether this is retryable is decided by whether the view was ever
      // live, not by the browser.
      this.failStream("the stream disconnected");
    };
  }

  onDomainEvent(generation, kind, data) {
    if (!this.synchronized) {
      // A domain event before the handshake means the connection is not the
      // synchronized stream this page requires.
      this.failStream("a domain event arrived before stream_ready");
      return;
    }

    const payload = parseFrame(data);
    const check = validateEvent(kind, payload, this.runID);
    if (!check.ok) {
      this.failStream(check.reason);
      return;
    }

    if (this.resyncInFlight) {
      if (!this.pending.push({ kind, payload })) {
        // Overflow: abandon and resynchronize. Not a dropped frame.
        this.failStream("buffered more than " + PENDING_MAX + " frames during resync");
      }
      return;
    }
    this.apply(generation, kind, payload);
  }

  // apply folds one validated event into the view.
  apply(generation, kind, payload) {
    if (generation !== this.generation) {
      return;
    }
    if (kind === "observation") {
      this.rows.add(payload.observation);
      this.emitRows();
      return;
    }
    // Lifecycle events change status, and a terminal one is the trigger for
    // the single closing read.
    this.callbacks.onLifecycle(kind, payload.evaluation);
    if (isTerminalKind(kind)) {
      this.resync(generation, { final: true });
    }
  }

  // resync takes the authoritative snapshot.
  //
  // Subscription already happened — connect() opened the stream before this
  // runs. The reverse order would lose anything committed between the read and
  // the subscribe, which is why the ordering is a property of the design and
  // not an implementation detail.
  async resync(generation, { final }) {
    if (generation !== this.generation) {
      return;
    }
    if (this.resyncInFlight) {
      // A terminal event during the initial read must still get its closing
      // read; remembering it here is what makes the replay path below honour
      // it rather than swallow it.
      this.finalResyncPending = this.finalResyncPending || final;
      return;
    }

    this.resyncInFlight = true;
    if (!final) {
      this.setState(STATE.RESYNCING);
    }

    try {
      const run = await this.deps.getRun(this.runID);
      if (generation !== this.generation) {
        return;
      }
      const progress = await this.deps.getProgress(this.runID);
      if (generation !== this.generation) {
        return;
      }
      this.callbacks.onSnapshot(run, progress);
    } catch (error) {
      if (generation !== this.generation) {
        return;
      }
      this.resyncInFlight = false;
      this.failStream(`authoritative read failed: ${error.message}`);
      return;
    }

    this.resyncInFlight = false;

    // Replay what arrived while the read was in flight, in order.
    const buffered = this.pending.drain();
    for (const frame of buffered) {
      if (generation !== this.generation) {
        return;
      }
      this.apply(generation, frame.kind, frame.payload);
    }

    if (generation !== this.generation) {
      return;
    }

    if (this.finalResyncPending) {
      this.finalResyncPending = false;
      await this.resync(generation, { final: true });
      return;
    }

    // Synchronized and current: the backoff sequence starts over.
    this.attempt = 0;
    this.everLive = true;
    this.setState(STATE.LIVE);
  }

  // failStream abandons the current generation and decides what follows.
  //
  // everLive is the whole decision. A view that never synchronized has a
  // startup failure and stops; one that has been live retries, because a
  // transient disconnect is not a reason to stop watching a run.
  failStream(reason) {
    const wasEverLive = this.everLive;
    this.generation += 1;
    this.clearHandshakeTimer();
    this.closeSource();
    this.pending = new PendingBuffer();
    this.synchronized = false;
    this.resyncInFlight = false;
    this.finalResyncPending = false;
    this.rows.clear();
    this.emitRows();

    this.callbacks.onProblem(reason);

    if (!wasEverLive) {
      this.setState(STATE.FAILED);
      return;
    }

    const delay = nextBackoff(this.attempt);
    this.attempt += 1;
    this.setState(STATE.RECONNECTING);
    this.clearReconnectTimer();
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
  }

  // reconnectNow is the explicit user action.
  //
  // Present so the reconnect policy is never the browser's only answer: a
  // failed startup stops retrying, and this is how a developer asks again.
  reconnectNow() {
    if (this.runID === "") {
      return;
    }
    this.generation += 1;
    this.clearHandshakeTimer();
    this.clearReconnectTimer();
    this.closeSource();
    this.attempt = 0;
    this.connect();
  }
}
