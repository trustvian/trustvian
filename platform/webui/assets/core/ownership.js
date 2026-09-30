// Request ownership: which in-flight request is still the one that matters.
//
// Every surface in this console reads asynchronously, and the reader can move
// while a read is in flight. Three ways that goes wrong, all observed:
//
//   1. A response for run A lands after the reader opened run B, and B's
//      table fills with A's rows.
//   2. Two requests for the same surface overlap; the slower one started
//      first, so it lands last and overwrites the newer answer.
//   3. An older request's `finally` clears the loading flag that a newer
//      request had just raised, so the skeleton disappears while the newer
//      read is still outstanding.
//
// The fix is the same one realtime.js already uses for the stream: take a
// token before the await, and check it immediately after. Nothing in between,
// because "in between" is where a stale response gets used.
//
// This is deliberately *not* built on AbortController. Cancellation is a
// request to stop that the network may decline: an abort can lose the race
// with a response already in the task queue, and a surface whose correctness
// depended on the abort winning would be right almost always. Aborting is a
// worthwhile optimisation on top and cannot be the mechanism. A token
// comparison has no race to lose.
//
// Nothing here knows what a run is. A surface is a name, a sequence number
// and a context key; what those mean is the caller's business.

// createSurface returns the ownership tracker for one independent surface.
//
// Independent is the operative word: the run table and the observation table
// hold separate trackers, so a fetch on one can never invalidate outstanding
// work on the other. Two surfaces that shared a tracker would cancel each
// other every time a reader used both.
export function createSurface(name) {
  let sequence = 0;
  let context = "";

  return Object.freeze({
    name,

    // begin issues a ticket for a request that is about to start.
    //
    // Issuing supersedes every outstanding ticket on this surface, which is
    // what makes a newer request win regardless of the order the responses
    // arrive in. `contextKey` is what the request is *about* — a run
    // identifier, a project, a filter — and omitting it means "the same
    // thing I was already about", which is the paging case.
    begin(contextKey) {
      sequence += 1;
      if (contextKey !== undefined) {
        context = contextKey;
      }
      return Object.freeze({ sequence, context });
    },

    // retarget abandons everything outstanding because the subject changed.
    //
    // Called when the reader moves — a different run, a different project, a
    // different filter — rather than when a new request starts. After this,
    // a response for the old subject is recognisably not current even though
    // no replacement request has been issued yet.
    retarget(contextKey) {
      sequence += 1;
      context = contextKey;
    },

    // owns reports whether a ticket is still the current one.
    //
    // Both halves matter. The sequence catches a superseded request; the
    // context catches a response whose subject the reader has since left,
    // including the case where the surface was retargeted and then returned
    // to the same subject — that is a different request and the old answer
    // is still stale.
    owns(ticket) {
      return ticket !== null
        && ticket !== undefined
        && ticket.sequence === sequence
        && ticket.context === context;
    },

    // context reports what this surface is currently about.
    contextKey() {
      return context;
    },
  });
}
