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
// See docs/adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	platform "trustvian-platform"
)

// defaultHeartbeatInterval keeps a quiet stream observably alive.
//
// A dead connection is otherwise indistinguishable from an evaluation that
// happens not to be producing records. Injectable so tests do not wait on a
// real clock.
const defaultHeartbeatInterval = 15 * time.Second

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

	// Streaming must be possible before a healthy stream is claimed.
	flusher, streamable := w.(http.Flusher)
	if !streamable {
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
		switch {
		case errors.Is(err, platform.ErrRealtimeCapacity):
			h.writeError(w, apiError{
				status: http.StatusServiceUnavailable, code: codeRealtimeUnavailable,
				message: "too many realtime subscribers"})
		case errors.Is(err, platform.ErrRealtimeClosed):
			h.writeError(w, apiError{
				status: http.StatusServiceUnavailable, code: codeRealtimeUnavailable,
				message: "realtime is shutting down"})
		default:
			h.writeError(w, apiError{
				status: http.StatusInternalServerError, code: codeInternal,
				message: "internal server error"})
		}
		return
	}
	// Idempotent, and the only place the subscription is released: a client
	// that disconnects must not leave a registration behind.
	defer subscription.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	// Last-Event-ID is deliberately ignored, and no id: field is emitted.
	// The bus retains no history, so honoring the header would promise a
	// replay that cannot happen — every connection must resynchronize.
	if !writeSSEEvent(w, flusher, "stream_ready", streamReadyPayload{
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
			if !writeSSEEvent(w, flusher, string(event.Kind), newRealtimeEventPayload(event)) {
				return
			}

		case <-heartbeat.C:
			// A comment frame, not a synthetic domain event: a heartbeat has
			// no domain meaning and must not look like one.
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent emits one frame, reporting whether the stream is still usable.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, name string, payload any) bool {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := w.Write([]byte("event: " + name + "\ndata: ")); err != nil {
		return false
	}
	if _, err := w.Write(encoded); err != nil {
		return false
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
