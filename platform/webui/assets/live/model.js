// The zero-input Live view: active scope discovery and one run-scoped
// behavior graph.
//
// Everything here is a *current viewport*, never history. Realtime publishes
// replay_available: false and resync_required: true, so nothing on this page
// may imply that what is drawn is a record of what happened — a reconnect
// starts an empty graph, and no past flow is reconstructed from aggregates.
// Task 067 owns event history; this module owns what is happening now.
//
// Three rules the rest of the file exists to keep:
//
//   1. The browser decides nothing. Decision, risk, trust, anomaly and
//      new-behavior all arrive computed. Nothing here derives one.
//   2. fingerprint_id is behavioral identity, not global observation
//      identity. It is a valid edge key only inside one run.
//   3. Every collection is bounded, and every bound is stated when it is hit.
//
// See docs/tasks/v1.0/074-zero-input-live-behavior-webui.md.

// ---------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------

// Visualization bounds: the browser's rendering cost. A different kind of
// limit from the platform's behavior-evidence bound and from the realtime
// queue bound, and the three must never be reported as one another — see
// RunGraph.saturation and behaviorComplete below.
export const MAX_SCOPE_CARDS = 16;
export const MAX_SOURCE_NODES = 8;
export const MAX_TARGET_NODES = 64;
export const MAX_EDGES = 128;
// The selected run's actors listed as seen live (task 109): twice the graph's
// sources, so an actor whose source the graph evicted can still be followed.
export const MAX_ACTORS_LISTED = 16;

// ---------------------------------------------------------------------
// Scope identity
// ---------------------------------------------------------------------

// scopeKey builds a collision-free key for one realtime scope.
//
// JSON of a fixed field order, not a delimiter join. Joining on a separator
// makes ("a|b", "") and ("a", "b") the same key, and an identifier in this
// domain may legitimately contain almost any printable character.
// JSON.stringify escapes that problem away and the fixed array order keeps the
// key stable.
//
// All six dimensions participate. Two runs of one candidate, two candidates of
// one agent, and the same environment ref under two projects are each distinct
// scopes; a key that dropped a dimension would merge them.
export function scopeKey(scope) {
  return JSON.stringify([
    scope.project_id || "",
    scope.agent_id || "",
    scope.candidate_id || "",
    scope.run_id || "",
    scope.environment || "",
    scope.behavioral_profile || "",
  ]);
}

// targetKey identifies one target node, by the two dimensions that name it.
//
// Structured for the same reason scopeKey is: a target name may contain any
// printable character, so a concatenation could collide with a different pair.
function targetKey(behavior) {
  return JSON.stringify([behavior.target_name || "", behavior.target_category || ""]);
}

// actorKey identifies the graph source an observation is drawn from: the
// actor the frame names (task 109).
//
// The server states the actor; this only keys by it, which is equality, not a
// computation. Structured like scopeKey so no delimiter can collide.
//
//   - actor_id_state "carried": the frame's actor_id.
//   - "exceeds_frame_bound": one shared key for every actor whose id was too
//     long to travel. It is labelled as exactly that and never claims to be one
//     actor.
//   - absent: a server older than task 109 sent no actor. The source is the
//     scope's agent, which is what such a frame was always drawn as.
export function actorKey(observation, scope) {
  const state = observation.actor_id_state;
  if (state === "exceeds_frame_bound") {
    return JSON.stringify(["", "exceeds_frame_bound"]);
  }
  if (state === "carried" && typeof observation.actor_id === "string") {
    return JSON.stringify([observation.actor_id, "carried"]);
  }
  return JSON.stringify([scope.agent_id || "", "carried"]);
}

// OVER_BOUND_ACTOR_LABEL names the one source every over-bound actor shares.
// It says why, and never claims to be one actor.
export const OVER_BOUND_ACTOR_LABEL = "actor id not carried (over 256 bytes)";

// actorOf is the actor an observation names, in the shape a source keeps.
export function actorOf(observation, scope) {
  if (observation.actor_id_state === "exceeds_frame_bound") {
    return { actorID: "", overBound: true };
  }
  if (observation.actor_id_state === "carried" && typeof observation.actor_id === "string") {
    return { actorID: observation.actor_id, overBound: false };
  }
  return { actorID: scope.agent_id || "", overBound: false };
}

// edgeKey identifies one line: one actor performing one behavior. The same
// fingerprint from two actors is one behavior drawn twice, each line carrying
// its own actor's latest answer.
export function edgeKey(sourceID, fingerprintID) {
  return JSON.stringify([sourceID, fingerprintID]);
}

// ---------------------------------------------------------------------
// Active scope cards
// ---------------------------------------------------------------------

// ScopeCards holds the scopes seen on *this* connection, bounded and
// least-recently-observed evicted.
//
// Not a durable list: it starts empty on every connect, because a card is
// evidence that a frame arrived on this stream and nothing else. The count it
// holds is labelled "seen live" wherever it is rendered — an authoritative
// count comes from /v1.
export class ScopeCards {
  constructor(capacity = MAX_SCOPE_CARDS) {
    this.capacity = capacity;
    // Insertion-ordered Map, re-inserted on touch, so the first key is always
    // the least recently observed.
    this.cards = new Map();
    this.evicted = 0;
  }

  observe(scope, at) {
    const key = scopeKey(scope);
    let card = this.cards.get(key);
    if (card === undefined) {
      card = { key, scope, seenLive: 0, lastActivity: at, lastDecision: "", lastRisk: "" };
    } else {
      // Delete before set: Map preserves insertion order, so re-inserting is
      // what moves this card to the most-recently-observed end.
      this.cards.delete(key);
    }
    card.seenLive += 1;
    card.lastActivity = at;
    this.cards.set(key, card);

    while (this.cards.size > this.capacity) {
      const oldest = this.cards.keys().next().value;
      this.cards.delete(oldest);
      this.evicted += 1;
    }
    return card;
  }

  get(key) {
    return this.cards.get(key);
  }

  // ordered returns cards most-recently-observed first: the thing that just
  // moved is what a developer watching an agent is looking for.
  ordered() {
    return Array.from(this.cards.values()).reverse();
  }

  get size() {
    return this.cards.size;
  }

  get saturated() {
    return this.evicted > 0;
  }

  clear() {
    this.cards.clear();
    this.evicted = 0;
  }
}

// ---------------------------------------------------------------------
// Actors seen live in the selected run
// ---------------------------------------------------------------------

// SeenActors lists the actors whose frames this connection received for the
// selected run (task 109), bounded and least-recently-seen evicted.
//
// Not the run's actors: those are in its observations. Each row's count is
// frames that arrived here, labelled "seen live" wherever it is shown, and its
// time is the browser's arrival time. It is cleared with the graph — on a run
// change and on every reconnect.
export class SeenActors {
  constructor(capacity = MAX_ACTORS_LISTED) {
    this.capacity = capacity;
    // Insertion-ordered and re-inserted on touch, as ScopeCards is.
    this.actors = new Map();
    this.evicted = 0;
  }

  observe(key, actor, actorType, at) {
    let row = this.actors.get(key);
    if (row === undefined) {
      row = { key, actorID: actor.actorID, overBound: actor.overBound, actorType: "", seenLive: 0, lastSeen: at };
    } else {
      this.actors.delete(key);
    }
    row.seenLive += 1;
    row.lastSeen = at;
    row.actorType = actorType;
    this.actors.set(key, row);
    while (this.actors.size > this.capacity) {
      this.actors.delete(this.actors.keys().next().value);
      this.evicted += 1;
    }
    return row;
  }

  get(key) {
    return this.actors.get(key);
  }

  // ordered returns the most recently seen first, least recently seen last.
  ordered() {
    return Array.from(this.actors.values()).reverse();
  }

  get size() {
    return this.actors.size;
  }

  get saturated() {
    return this.evicted > 0;
  }
}

// ---------------------------------------------------------------------
// The run-scoped behavior graph
// ---------------------------------------------------------------------

// RunGraph is the topology of exactly one run.
//
// One run, and that is correctness rather than layout preference. A
// fingerprint identifies a behavioral shape derived from StableFeatures and
// deliberately carries no project, agent, candidate, run or actor — task 051
// was explicit that run and candidate metadata must never become fingerprint
// dimensions. So two unrelated agents doing the same thing produce the same
// fingerprint_id, and merging them would let one run's new_behavior, decision
// and risk overwrite another's.
//
// Inside a fixed run, a line is one actor performing one behavior: the edge key
// is (actor, fingerprint), and each actor is its own source (task 109). Keyed by
// the fingerprint alone, two actors doing the same thing shared one line and the
// later one's decision overwrote the earlier one's. The fingerprint stays the
// behavior's identity across actors — the same behavior from two actors is one
// fingerprint drawn as two lines to one target. Observations for any other
// scope are not drawn here at all; they update their own card.
export class RunGraph {
  constructor(key, bounds = {}) {
    this.scopeKey = key;
    this.maxSources = bounds.maxSources ?? MAX_SOURCE_NODES;
    this.maxTargets = bounds.maxTargets ?? MAX_TARGET_NODES;
    this.maxEdges = bounds.maxEdges ?? MAX_EDGES;

    this.sources = new Map();
    this.targets = new Map();
    this.edges = new Map();

    this.droppedSources = 0;
    this.droppedTargets = 0;
    this.droppedEdges = 0;

    // The run's actors seen live. Held by the graph so it is cleared exactly
    // when the graph is: on a run change and on every reconnect.
    this.actors = new SeenActors(bounds.maxActors ?? MAX_ACTORS_LISTED);

    // The platform's statement about its own evidence, carried through
    // unchanged. Not this graph's saturation, and never reported as it.
    this.behaviorComplete = true;
  }

  // add folds one observation into the topology and returns the edge to
  // animate, or null when a bound refused it.
  //
  // A refused observation still counted on its card and still appears in the
  // feed: what is lost is a line on a picture, not a fact.
  add(observation, scope, at) {
    if (observation.behavior_complete === false) {
      this.behaviorComplete = false;
    }
    // Every frame of the run lists its actor, including one a bound below
    // refuses to draw: what is lost there is a line, not the fact the actor
    // was seen.
    this.actors.observe(actorKey(observation, scope), actorOf(observation, scope),
      observation.actor_type || "", at);

    const behavior = observation.behavior || {};
    const fingerprintID = observation.fingerprint_id || "";
    if (fingerprintID === "") {
      return null;
    }
    const sourceID = actorKey(observation, scope);
    const edgeID = edgeKey(sourceID, fingerprintID);
    const targetID = targetKey(behavior);

    const source = this.touchSource(sourceID, actorOf(observation, scope), at);
    if (source === null) {
      return null;
    }
    // The actor type of the actor's most recent frame, read as sent.
    source.actorType = observation.actor_type || "";
    const target = this.touchTarget(targetID, behavior, at);
    if (target === null) {
      return null;
    }

    let edge = this.edges.get(edgeID);
    if (edge === undefined) {
      if (this.edges.size >= this.maxEdges) {
        this.droppedEdges += 1;
        if (!this.evictOldest(this.edges)) {
          return null;
        }
      }
      edge = {
        id: edgeID,
        fingerprintID,
        sourceID,
        targetID,
        // Exactly the descriptor's own strings. No inference, no prettifying,
        // no mapping to a friendlier verb: if telemetry proves only
        // "POST -> export.localhost", that is what is drawn.
        //
        // Task 075 landed, so the descriptor may now name a tool rather than a
        // transport — and the server says which, in `fidelity`. That value is
        // read and displayed, never derived: the browser cannot tell a tool name
        // from a span name by looking at it, and guessing would be the browser
        // asserting something no evidence supports.
        operationCategory: behavior.operation_category || "",
        operationName: behavior.operation_name || "",
        actorType: behavior.actor_type || "",
        environment: behavior.environment || "",
        // Carried on the edge as well as on the target node, because the
        // inspector and the timeline describe a behavior rather than a node
        // and would otherwise have to resolve a target key back to its name.
        targetName: behavior.target_name || "",
        targetCategory: behavior.target_category || "",
        // The server's own answer. Absent is read as "transport" rather than as
        // unknown, matching the wire contract: a producer that stated nothing
        // proved nothing.
        fidelity: observation.fidelity || "transport",
        // Which instrumentation layer supplied that identity. Unlike fidelity
        // there is no default: the server publishes "" for "not classified", and
        // substituting "transport" here would make the browser assert an identity
        // source the server did not state. Task 083.
        behaviorLayer: observation.behavior_layer || "",
        pulses: 0,
        newBehavior: false,
      };
      this.edges.set(edgeID, edge);
    } else {
      this.edges.delete(edgeID);
      this.edges.set(edgeID, edge);
    }

    edge.pulses += 1;
    edge.lastActivity = at;
    // Every one of these is the server's answer, read and stored. None is
    // computed, combined, thresholded or compared here.
    edge.decision = observation.decision || "";
    edge.riskLevel = observation.risk_level || "";
    edge.trustScore = observation.trust_score;
    edge.anomalyScore = observation.anomaly_score;
    edge.anomalyConfidence = observation.anomaly_confidence;
    // Latest wins, like every field above it. Not a conservative minimum across
    // observations: that would be the browser computing a summary, and each
    // value displayed has to be an answer the server actually gave.
    edge.fidelity = observation.fidelity || "transport";
    edge.behaviorLayer = observation.behavior_layer || "";
    // Sticky: a behavior that was new when first observed stays labelled new
    // for this viewport, because the badge describes the observation that
    // introduced it rather than the most recent repeat.
    //
    // It goes on the line of the frame that said so, and nowhere else. "New"
    // is the run-level fact the server stated; another actor's later frame of
    // the same behavior says false, and its line carries no badge. Nothing here
    // says "new for this actor".
    if (observation.new_behavior === true) {
      edge.newBehavior = true;
      target.newBehavior = true;
    }
    return edge;
  }

  touchSource(id, actor, at) {
    const existing = this.sources.get(id);
    if (existing !== undefined) {
      this.sources.delete(id);
      existing.lastActivity = at;
      this.sources.set(id, existing);
      return existing;
    }
    if (this.sources.size >= this.maxSources) {
      this.droppedSources += 1;
      if (!this.evictOldest(this.sources)) {
        return null;
      }
    }
    const source = {
      id,
      actorID: actor.actorID,
      overBound: actor.overBound,
      actorType: "",
      lastActivity: at,
    };
    this.sources.set(id, source);
    return source;
  }

  touchTarget(id, behavior, at) {
    const existing = this.targets.get(id);
    if (existing !== undefined) {
      this.targets.delete(id);
      existing.lastActivity = at;
      this.targets.set(id, existing);
      return existing;
    }
    if (this.targets.size >= this.maxTargets) {
      this.droppedTargets += 1;
      if (!this.evictOldest(this.targets)) {
        return null;
      }
    }
    const target = {
      id,
      name: behavior.target_name || "",
      category: behavior.target_category || "",
      lastActivity: at,
      newBehavior: false,
    };
    this.targets.set(id, target);
    return target;
  }

  // evictOldest removes the least recently observed entry.
  //
  // Least-recently-observed rather than oldest-created: what a developer is
  // watching is what is moving, and evicting by creation age would drop the
  // active edge to keep a quiet one.
  evictOldest(map) {
    const oldest = map.keys().next();
    if (oldest.done) {
      return false;
    }
    map.delete(oldest.value);
    return true;
  }

  // saturation names which bounds were hit and how much is not drawn.
  //
  // An array rather than a boolean: "the graph is incomplete" without saying
  // what is missing is the silent truncation this exists to prevent.
  saturation() {
    const hit = [];
    if (this.droppedSources > 0) {
      hit.push({ bound: "source nodes", limit: this.maxSources, omitted: this.droppedSources });
    }
    if (this.droppedTargets > 0) {
      hit.push({ bound: "target nodes", limit: this.maxTargets, omitted: this.droppedTargets });
    }
    if (this.droppedEdges > 0) {
      hit.push({ bound: "edges", limit: this.maxEdges, omitted: this.droppedEdges });
    }
    return hit;
  }
}

// ---------------------------------------------------------------------
// The view model
// ---------------------------------------------------------------------

// LiveModel owns the cards, the selection and the selected run's graph.
//
// Separated from rendering so the decisions — which scope a frame belongs to,
// whether it is drawn, when a bound is hit — are testable without a DOM, and
// so the renderer has no state of its own to disagree with.
export class LiveModel {
  constructor(bounds = {}) {
    this.bounds = bounds;
    this.cards = new ScopeCards(bounds.maxCards ?? MAX_SCOPE_CARDS);
    this.selectedKey = "";
    // Two selection modes, and the distinction is the whole reason a developer
    // can read a graph while several agents are working.
    //
    // Following: the most recently active scope is drawn, and each new scope
    // takes over. Pinned: the developer chose a run and it stays chosen, no
    // matter what else becomes active — a newly active scope raises its own
    // card and nothing more. Stealing focus mid-read is the failure this
    // exists to prevent.
    //
    // Ephemeral UI state. It is not persisted and a reload starts following
    // again, which is correct: nothing about which run somebody was reading is
    // platform state.
    this.following = true;
    this.graph = null;
    // The actor the view is narrowed to, by actor key; "" is all actors. A
    // display filter by equality over frames already held (task 109): it
    // sends no request and computes nothing, and frames from every actor keep
    // folding into the graph so All actors restores the run at once.
    // Ephemeral like the follow mode: a reload starts on all actors.
    this.followedActor = "";
  }

  // followActor narrows the view to one actor of the selected run, or back to
  // all of them with "".
  followActor(key) {
    this.followedActor = key;
  }

  // follow returns to auto-selection and immediately adopts the most recently
  // active scope, so the control does something visible when pressed.
  follow() {
    this.following = true;
    const newest = this.cards.ordered()[0];
    if (newest !== undefined) {
      this.applySelection(newest.key);
    }
  }

  // observe folds one frame in and reports what changed.
  //
  // edge is null when the frame belonged to a scope that is not selected,
  // which is the run-isolation rule in one line: another run's observation
  // updates its card and is not drawn.
  observe(scope, observation, at) {
    const card = this.cards.observe(scope, at);
    card.lastDecision = observation.decision || "";
    card.lastRisk = observation.risk_level || "";
    card.lastObservation = observation;

    // While following, the most recently active scope is the selected one —
    // which this frame just made this card. Pinned, nothing here changes the
    // selection, however loud another run gets.
    if (this.following && card.key !== this.selectedKey) {
      this.applySelection(card.key);
    }

    if (card.key !== this.selectedKey) {
      return { card, edge: null };
    }
    if (this.graph === null || this.graph.scopeKey !== this.selectedKey) {
      this.graph = new RunGraph(this.selectedKey, this.bounds);
    }
    return { card, edge: this.graph.add(observation, scope, at) };
  }

  // select is a developer's explicit choice, which pins it.
  //
  // Pressing Follow active is the only way back to auto-selection. A click is
  // an intent to read one thing, and an intent that evaporates on the next
  // frame is not one.
  select(key) {
    this.following = false;
    this.applySelection(key);
  }

  // applySelection changes which run the graph draws, without touching the
  // follow mode.
  //
  // The previous run's topology is discarded rather than kept beside the new
  // one: a graph that retained it would be a combined multi-run graph, which
  // is exactly what fingerprint collisions make unsound.
  applySelection(key) {
    if (key === this.selectedKey) {
      return;
    }
    this.selectedKey = key;
    this.graph = key === "" ? null : new RunGraph(key, this.bounds);
    // An actor belongs to one run; a new run starts on all actors.
    this.followedActor = "";
  }

  selectedCard() {
    return this.selectedKey === "" ? undefined : this.cards.get(this.selectedKey);
  }

  // reset clears everything one connection owned.
  //
  // Called on every connect, because a card and an edge are both statements
  // about the current stream. The selected key and the follow mode survive as
  // intent — a pinned run re-selects itself if it is still active — but no
  // observation does.
  reset() {
    this.cards.clear();
    this.graph = this.selectedKey === "" ? null : new RunGraph(this.selectedKey, this.bounds);
  }
}
