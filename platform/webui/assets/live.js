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
// Inside a fixed run the fingerprint is a sufficient edge key, because the run
// is already fixed. Observations for any other scope are not drawn here at
// all; they update their own card.
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

    const behavior = observation.behavior || {};
    const edgeID = observation.fingerprint_id || "";
    if (edgeID === "") {
      return null;
    }
    const sourceID = scope.agent_id || "";
    const targetID = targetKey(behavior);

    const source = this.touchSource(sourceID, at);
    if (source === null) {
      return null;
    }
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
        sourceID,
        targetID,
        // Exactly the descriptor's own strings. No inference, no prettifying,
        // no mapping to a friendlier verb: if telemetry proves only
        // "POST -> export.localhost", that is what is drawn. Semantic fidelity
        // is task 075's, and inventing a name here would be the browser
        // asserting something no evidence supports.
        operationCategory: behavior.operation_category || "",
        operationName: behavior.operation_name || "",
        actorType: behavior.actor_type || "",
        environment: behavior.environment || "",
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
    // Sticky: a behavior that was new when first observed stays labelled new
    // for this viewport, because the badge describes the observation that
    // introduced it rather than the most recent repeat.
    if (observation.new_behavior === true) {
      edge.newBehavior = true;
      target.newBehavior = true;
    }
    return edge;
  }

  touchSource(id, at) {
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
    const source = { id, lastActivity: at };
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
    // Once a developer picks a card, a newly active scope raises its own card
    // but must not steal the graph they are reading.
    this.selectionIsExplicit = false;
    this.graph = null;
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

    if (this.selectedKey === "" && !this.selectionIsExplicit) {
      this.select(card.key, { explicit: false });
    }

    if (card.key !== this.selectedKey) {
      return { card, edge: null };
    }
    if (this.graph === null || this.graph.scopeKey !== this.selectedKey) {
      this.graph = new RunGraph(this.selectedKey, this.bounds);
    }
    return { card, edge: this.graph.add(observation, scope, at) };
  }

  // select changes which run the graph draws.
  //
  // The previous run's topology is discarded rather than kept beside the new
  // one: a graph that retained it would be a combined multi-run graph, which
  // is exactly what fingerprint collisions make unsound.
  select(key, { explicit = true } = {}) {
    if (explicit) {
      this.selectionIsExplicit = true;
    }
    if (key === this.selectedKey) {
      return;
    }
    this.selectedKey = key;
    this.graph = key === "" ? null : new RunGraph(key, this.bounds);
  }

  selectedCard() {
    return this.selectedKey === "" ? undefined : this.cards.get(this.selectedKey);
  }

  // reset clears everything one connection owned.
  //
  // Called on every connect, because a card and an edge are both statements
  // about the current stream. The selected key survives as an intent, so the
  // same run re-selects itself if it is still active, but no observation does.
  reset() {
    this.cards.clear();
    this.graph = null;
  }
}
