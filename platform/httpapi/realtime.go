package httpapi

// Server-Sent Events over the realtime bus.
//
// Transport only. This handler subscribes, translates and streams; it holds a
// subscriber capability and never a publisher, because a transport that could
// publish could fabricate state a client would believe.
//
// SSE first because it is one GET over ordinary HTTP, needs no handshake or
// framing library, and browsers reconnect on their own. For a one-way
// notification stream that is the whole requirement — WebSockets would add
// bidirectional framing nothing here uses.
//
// Two different bounds meet in this file. The bus bounds queued events, which
// protects the publisher from a subscriber that reads slowly. A write deadline
// bounds each socket *delivery* — the write and the flush that pushes it —
// which protects this goroutine and its connection from a client that stops
// reading entirely. Neither substitutes for the other: a client can hold a
// healthy subscription and still never drain its socket.
//
// See docs/adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	platform "trustvian-platform"
)

const (
	// defaultHeartbeatInterval keeps a quiet stream observably alive.
	//
	// A dead connection is otherwise indistinguishable from an evaluation that
	// happens not to be producing records. Injectable so tests do not wait on
	// a real clock.
	defaultHeartbeatInterval = 15 * time.Second

	// defaultRealtimeWriteTimeout bounds how long any single SSE write may
	// block.
	//
	// It is not a heartbeat interval and carries no domain meaning: it exists
	// so a client that keeps a connection open and stops reading cannot hold
	// this goroutine and its socket buffers indefinitely. Without it, a
	// handler stuck inside Write cannot observe its subscription closing, its
	// context cancelling, or the bus disconnecting it — and because the bus
	// does free that subscriber's slot, the stalled connections would
	// accumulate *outside* the subscriber bound.
	defaultRealtimeWriteTimeout = 5 * time.Second

	// maxRealtimeWriteTimeout caps the configurable value.
	//
	// An option that accepted any duration could turn a bounded write into an
	// effectively unbounded one while the code still claimed a bound.
	maxRealtimeWriteTimeout = 30 * time.Second
)

// realtime streams events matching the request's filter.
//
// The handshake is sent *after* the subscription is registered. That order is
// the point of the resync protocol: fetching state and then subscribing leaves
// a window where a mutation lands between the two and is lost by both paths.
func (h *Handler) realtime(w http.ResponseWriter, r *http.Request) {
	if h.realtimeSubscriber == nil {
		h.writeError(w, apiError{
			status: http.StatusServiceUnavailable, code: codeRealtimeUnavailable,
			message: "realtime is not configured on this server"})
		return
	}

	// Everything that could make this stream unhealthy is checked before a
	// 200 is written. Once the status line is out, the only way to report a
	// problem is to hang up — so a stream is never claimed healthy and then
	// discovered to be impossible.
	// A capability check, not the flush mechanism. Actual flushing goes
	// through the controller below, because http.Flusher.Flush returns
	// nothing and a flush is exactly where a stalled socket surfaces.
	if _, streamable := w.(http.Flusher); !streamable {
		h.writeError(w, apiError{
			status: http.StatusInternalServerError, code: codeInternal,
			message: "internal server error"})
		return
	}

	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(h.realtimeWriteTimeout)); err != nil {
		// Deadlines unsupported. Continuing would leave every write
		// unbounded, which is precisely the condition this check exists to
		// prevent — so the stream is refused rather than silently downgraded.
		h.writeError(w, apiError{
			status: http.StatusInternalServerError, code: codeInternal,
			message: "internal server error"})
		return
	}

	filter := platform.RealtimeFilter{
		ProjectID: platform.ProjectID(r.URL.Query().Get("project_id")),
		AgentID:   platform.AgentID(r.URL.Query().Get("agent_id")),
		RunID:     platform.EvaluationRunID(r.URL.Query().Get("run_id")),
	}

	subscription, err := h.realtimeSubscriber.Subscribe(r.Context(), filter)
	if err != nil {
		h.writeError(w, realtimeSubscribeError(err))
		return
	}
	// Idempotent, and the only place the subscription is released: a client
	// that disconnects — or stalls until a write deadline expires — must not
	// leave a registration behind.
	defer subscription.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	writer := &sseWriter{
		w:          w,
		controller: controller,
		timeout:    h.realtimeWriteTimeout,
	}

	// Last-Event-ID is deliberately ignored, and no id: field is emitted.
	// The bus retains no history, so honoring the header would promise a
	// replay that cannot happen — every connection must resynchronize.
	if !writer.event("stream_ready", streamReadyPayload{
		Version:         WireVersion,
		ReplayAvailable: false,
		ResyncRequired:  true,
	}) {
		return
	}

	heartbeat := time.NewTicker(h.heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case event, open := <-subscription.Events():
			// Closed because the client went away, the bus shut down, or this
			// subscriber fell behind and was disconnected. In every case the
			// stream is over; the client reconnects and resyncs.
			if !open {
				return
			}
			if !writer.event(string(event.Kind), newRealtimeEventPayload(event)) {
				return
			}

		case <-heartbeat.C:
			// A comment frame, not a synthetic domain event: a heartbeat has
			// no domain meaning and must not look like one. Bounded by the
			// same deadline — an unbounded heartbeat would let a quiet client
			// stall here instead.
			if !writer.comment("keepalive") {
				return
			}
		}
	}
}

// realtimeSubscribeError maps a subscription failure onto the wire.
//
// A malformed filter is the client's to fix and is reported as such; capacity
// and shutdown are infrastructure conditions. Conflating them would tell a
// caller with a bad identifier that the server is unavailable.
func realtimeSubscribeError(err error) apiError {
	switch {
	case errors.Is(err, platform.ErrInvalidID):
		return apiError{
			status: http.StatusBadRequest, code: codeInvalidRequest,
			message: err.Error()}
	case errors.Is(err, platform.ErrRealtimeCapacity):
		return apiError{
			status: http.StatusServiceUnavailable, code: codeRealtimeUnavailable,
			message: "too many realtime subscribers"}
	case errors.Is(err, platform.ErrRealtimeClosed):
		return apiError{
			status: http.StatusServiceUnavailable, code: codeRealtimeUnavailable,
			message: "realtime is shutting down"}
	default:
		return apiError{
			status: http.StatusInternalServerError, code: codeInternal,
			message: "internal server error"}
	}
}

// sseWriter emits frames under a per-write deadline.
//
// The deadline is refreshed before every write because it is absolute: set
// once at connection time, a healthy long-lived stream would inherit an
// expired one and fail for no reason.
//
// Writes stay synchronous and ordered. Moving them to a goroutine or a queue
// would reintroduce the slow-consumer problem one layer down, with an
// unbounded buffer instead of a bounded one.
type sseWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

// event writes one named frame, reporting whether the stream is still usable.
func (s *sseWriter) event(name string, payload any) bool {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if !s.refresh() {
		return false
	}
	if _, err := s.w.Write([]byte("event: " + name + "\ndata: ")); err != nil {
		return false
	}
	if _, err := s.w.Write(encoded); err != nil {
		return false
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return false
	}
	return s.flush()
}

// comment writes a keepalive, under the same bound as a frame.
func (s *sseWriter) comment(text string) bool {
	if !s.refresh() {
		return false
	}
	if _, err := s.w.Write([]byte(": " + text + "\n\n")); err != nil {
		return false
	}
	return s.flush()
}

// flush pushes the frame and reports whether it actually left.
//
// Through the controller rather than http.Flusher, because Flusher.Flush
// returns nothing: a Write can succeed into a local buffer and the flush
// behind it can then hit the deadline, so discarding this error would let the
// handler carry on believing a frame was delivered that may never have been.
// That is the failure the deadline exists to surface, arriving through the
// half of the operation that was not watching for it.
//
// ResponseController.Flush prefers a FlushError() error implementation and
// falls back to a plain Flusher, which net/http's own response type provides
// — so no second capability negotiation is needed beyond the http.Flusher
// precondition the handler already checked before the 200.
func (s *sseWriter) flush() bool {
	return s.controller.Flush() == nil
}

// refresh extends the deadline for the delivery about to happen.
//
// One refresh per frame covers its write *and* its flush: they are one
// delivery attempt, and giving the flush its own window would let a client
// that stalls in exactly that half hold the connection for twice the bound.
func (s *sseWriter) refresh() bool {
	return s.controller.SetWriteDeadline(time.Now().Add(s.timeout)) == nil
}
