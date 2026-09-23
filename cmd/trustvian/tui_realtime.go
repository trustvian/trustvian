package main

// The realtime half of the dashboard: one SSE connection, bounded.
//
// Two things here are deliberately not what a general-purpose client would do.
//
// The stream gets its own HTTP transport. Task 060's client carries a 30s
// total timeout, which is correct for a one-shot request and fatal for a
// long-lived dashboard — it would kill a perfectly healthy stream on a
// schedule. The bounds here are on establishing the connection, not on how
// long it may stay open.
//
// The reader goroutine drains the socket continuously into a bounded channel
// rather than reading on demand. Task 059 disconnects a subscriber whose queue
// fills, so a client that paused reading while its own authoritative HTTP was
// in flight would be dropped for being slow — turning a slow resync into a
// reconnect loop that makes the slow resync worse.
//
// See docs/adr/0034-tui-is-a-bounded-realtime-http-client.md.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// tuiPendingEventCapacity bounds frames held between subscribing and
	// applying the authoritative snapshot.
	//
	// Matched to task 059's per-subscriber queue: if more than this arrives
	// while a resync is in flight, the server was about to disconnect us
	// anyway, and a bigger local buffer would only delay finding out.
	tuiPendingEventCapacity = 64

	// maxSSELineBytes and maxSSEFrameBytes bound what one frame may cost.
	//
	// A client that grows a buffer to whatever the server sends has no memory
	// bound at all — it has the server's good behaviour, which is not a
	// property worth assuming even locally.
	maxSSELineBytes  = 64 << 10
	maxSSEFrameBytes = 64 << 10

	// Connection-establishment bounds. Deliberately not a total timeout: the
	// whole point of this connection is to stay open.
	sseDialTimeout           = 10 * time.Second
	sseTLSHandshakeTimeout   = 10 * time.Second
	sseResponseHeaderTimeout = 30 * time.Second

	// sseIdleConnPoolTimeout bounds how long an *unused* keep-alive
	// connection lingers in the transport pool.
	//
	// Named for what it does, because the previous name invited exactly the
	// wrong conclusion: http.Transport.IdleConnTimeout has nothing to do with
	// an active response body. A live SSE connection that stops delivering
	// bytes is not idle by this definition and is never touched by it. The
	// active-stream bound is sseReadIdleTimeout below.
	sseIdleConnPoolTimeout = 30 * time.Second

	// maxSSEErrorBodyBytes caps a non-200 diagnostic body.
	maxSSEErrorBodyBytes = 8 << 10

	// sseErrorBodyReadTimeout bounds how long that diagnostic may take.
	//
	// ResponseHeaderTimeout has already been satisfied by the time a non-200
	// status is known, and there is deliberately no total client timeout — so
	// a server that sends 404 headers and then never finishes its body left
	// startup blocked indefinitely. io.LimitReader bounds bytes, not time.
	//
	// A few seconds is plenty: the body is at most 8 KiB of error text from a
	// server that has already responded. This is a diagnostic, and failing
	// without it beats hanging for it.
	sseErrorBodyReadTimeout = 5 * time.Second

	// sseHandshakeTimeout bounds waiting for a valid stream_ready.
	//
	// Distinct from sseReadIdleTimeout, and the distinction is the whole
	// point: heartbeat comments are real activity, so they refresh the idle
	// watchdog — which means a server that sends nothing but heartbeats and
	// never completes the protocol kept the watchdog satisfied forever while
	// the dashboard sat at CONNECTING with no frame to act on.
	//
	// This deadline asks a different question — "did the server establish the
	// protocol?" — and heartbeat traffic does not answer it. Tens of seconds
	// is generous for a local control plane under load while staying finite.
	// Released the moment a valid handshake is accepted, so it can never
	// disconnect an established stream.
	sseHandshakeTimeout = 30 * time.Second

	// sseReadIdleTimeout bounds silence on an *active* stream.
	//
	// Without it a dashboard could sit at LIVE forever against a connection
	// that stopped delivering: no bytes, no error, no EOF — a half-open TCP
	// connection produces exactly that, and the screen keeps showing state it
	// can no longer refresh. A stale dashboard that looks current is the one
	// failure this interface must not have.
	//
	// 60s is four times task 059's 15s heartbeat, so three consecutive
	// heartbeats must be missed before the client gives up. That is
	// deliberately generous: this is a liveness tolerance, not a polling
	// interval, and a false reconnect costs a resync while a missed
	// disconnect costs a lie on screen. The TUI does not generate or expect a
	// specific heartbeat cadence — any byte refreshes it.
	sseReadIdleTimeout = 60 * time.Second
)

// realtimeFrame is one parsed SSE frame.
type realtimeFrame struct {
	event string
	data  string
}

// streamReadyPayload is the handshake task 059 sends first.
type streamReadyPayload struct {
	Version         string `json:"version"`
	ReplayAvailable bool   `json:"replay_available"`
	ResyncRequired  bool   `json:"resync_required"`
}

// realtimeEnvelope is the shared shape of every domain frame.
//
// Decoded leniently: the compatibility contract requires clients to tolerate
// additive fields, and an unknown one must not turn a usable event into a
// broken stream.
type realtimeEnvelope struct {
	Version     string               `json:"version"`
	Kind        string               `json:"kind"`
	Scope       realtimeScope        `json:"scope"`
	Evaluation  *realtimeEvaluation  `json:"evaluation,omitempty"`
	Observation *realtimeObservation `json:"observation,omitempty"`
}

type realtimeScope struct {
	ProjectID         string `json:"project_id"`
	AgentID           string `json:"agent_id"`
	CandidateID       string `json:"candidate_id"`
	RunID             string `json:"run_id"`
	Environment       string `json:"environment"`
	BehavioralProfile string `json:"behavioral_profile"`
}

type realtimeEvaluation struct {
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	FailureReason string `json:"failure_reason"`
}

// realtimeObservation is task 059's bounded projection, and all of it.
//
// No attributes, tool arguments, prompts, completions, contributors or policy
// reason: those are absent from the wire on purpose, and the TUI has no
// endpoint that would return them.
type realtimeObservation struct {
	Sequence         string `json:"sequence"`
	RecordCount      string `json:"record_count"`
	BehaviorComplete bool   `json:"behavior_complete"`

	FingerprintID string             `json:"fingerprint_id"`
	Behavior      behaviorDescriptor `json:"behavior"`

	Decision       string `json:"decision"`
	RiskLevel      string `json:"risk_level"`
	ApprovalStatus string `json:"approval_status"`

	TrustScore        float64 `json:"trust_score"`
	AnomalyScore      float64 `json:"anomaly_score"`
	AnomalyConfidence float64 `json:"anomaly_confidence"`

	NewBehavior bool `json:"new_behavior"`
}

type behaviorDescriptor struct {
	ActorType         string `json:"actor_type"`
	OperationCategory string `json:"operation_category"`
	OperationName     string `json:"operation_name"`
	TargetName        string `json:"target_name"`
	TargetCategory    string `json:"target_category"`
	Environment       string `json:"environment"`
}

// Realtime event kinds, as task 059 emits them.
const (
	kindEvaluationCreated   = "evaluation_created"
	kindEvaluationStarted   = "evaluation_started"
	kindObservation         = "observation"
	kindEvaluationCompleted = "evaluation_completed"
	kindEvaluationFailed    = "evaluation_failed"
	kindEvaluationCancelled = "evaluation_cancelled"

	eventStreamReady = "stream_ready"
)

// isTerminalLifecycle reports whether a kind ends the run.
//
// These are the only kinds that trigger an authoritative read outside a
// reconnect, and they trigger exactly one: final counts are worth a read, and
// nothing periodic follows it.
func isTerminalLifecycle(kind string) bool {
	switch kind {
	case kindEvaluationCompleted, kindEvaluationFailed, kindEvaluationCancelled:
		return true
	}
	return false
}

// realtimeStream is one live subscription.
//
// Single owner: whoever calls openRealtimeStream owns close, and close is
// idempotent. The reader goroutine never outlives it.
type realtimeStream struct {
	frames chan realtimeFrame
	cancel context.CancelFunc
	body   io.ReadCloser

	// done closes when the reader has stopped; reason says why. Read only
	// after done is closed, which is the only ordering this needs.
	done   chan struct{}
	reason error

	// idle distinguishes "went silent" from "read failed". Set by the
	// watchdog before it cancels, so the read error it provokes is reported
	// as what it actually was.
	idle atomic.Bool

	// handshakeFailed marks the protocol deadline expiring, for the same
	// reason: the read error it causes should say what actually happened.
	handshakeFailed atomic.Bool

	// handshakeTimer is armed at open and released once a valid stream_ready
	// is accepted. Owned by this stream: armed before the reader starts,
	// stopped once through handshakeAccepted.
	handshakeTimer *time.Timer
	handshakeOnce  sync.Once
}

// handshakeAccepted releases the protocol deadline.
//
// Called by the model after validateStreamReady succeeds — not on any byte,
// and not on an event merely *named* stream_ready. The deadline must not stay
// armed past synchronization or it would eventually disconnect a healthy
// dashboard.
func (s *realtimeStream) handshakeAccepted() {
	s.handshakeOnce.Do(func() {
		if s.handshakeTimer != nil {
			s.handshakeTimer.Stop()
		}
	})
}

// errStreamOverflow means the client could not keep up with its own stream.
//
// Not a dropped frame: the stream is abandoned and resynchronized instead,
// because a client that discarded a notification cannot know what it missed.
var errStreamOverflow = errors.New("realtime stream outpaced the dashboard")

// errStreamHandshakeTimeout means the protocol never started.
//
// Reported separately from silence because the connection was not silent: it
// delivered bytes, just never the handshake. Both are unusable, and both are
// fatal before the dashboard has ever synchronized.
var errStreamHandshakeTimeout = errors.New("realtime stream did not complete its handshake")

// errStreamIdle means the connection went silent without closing.
//
// Reported distinctly from a read error because it is not one: nothing
// failed, nothing arrived, and the client stopped waiting. Treated exactly
// like an EOF by the model — before first sync it is fatal, after it is a
// reconnect.
var errStreamIdle = errors.New("realtime stream went silent")

// realtimeOpener opens a subscription. An interface so tests can drive the
// model's state machine without a socket.
type realtimeOpener interface {
	open(ctx context.Context, runID string) (*realtimeStream, error)
}

// sseOpener is the real implementation over HTTP.
type sseOpener struct {
	baseURL *url.URL
	client  *http.Client

	// idleTimeout is the active-stream bound. A field rather than a constant
	// so tests can shorten it; no CLI flag exists for it, because a liveness
	// tolerance is not something a user should have to reason about.
	idleTimeout time.Duration

	// handshakeTimeout bounds waiting for a valid stream_ready, and
	// errorBodyTimeout bounds reading a non-200 diagnostic. Fields for the
	// same reason: injectable by tests, invisible to users.
	handshakeTimeout time.Duration
	errorBodyTimeout time.Duration
}

// newSSEOpener builds the streaming client.
//
// Separate from task 060's client on purpose — see this file's header.
func newSSEOpener(base *url.URL) *sseOpener {
	return &sseOpener{
		baseURL:          base,
		idleTimeout:      sseReadIdleTimeout,
		handshakeTimeout: sseHandshakeTimeout,
		errorBodyTimeout: sseErrorBodyReadTimeout,
		client: &http.Client{
			// No Timeout: a total response deadline would kill a healthy
			// dashboard. Every bound below is on establishing the connection.
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: sseDialTimeout}).DialContext,
				TLSHandshakeTimeout:   sseTLSHandshakeTimeout,
				ResponseHeaderTimeout: sseResponseHeaderTimeout,
				IdleConnTimeout:       sseIdleConnPoolTimeout,
				MaxIdleConns:          2,
				MaxIdleConnsPerHost:   2,
				ForceAttemptHTTP2:     false,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Same reasoning as task 060: a subscription addressed to one
				// host must not silently become a subscription to another.
				return fmt.Errorf("refusing to follow redirect to %s", req.URL.Redacted())
			},
		},
	}
}

func (o *sseOpener) open(ctx context.Context, runID string) (*realtimeStream, error) {
	target := *o.baseURL
	target.Path, target.RawPath = apiPath("realtime")
	// Escaped by url.Values: a caller-supplied identifier must not be able to
	// add a second query parameter or change the subscription's scope.
	target.RawQuery = url.Values{"run_id": []string{runID}}.Encode()

	streamCtx, cancel := context.WithCancel(ctx)

	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		cancel()
		return nil, operationalErrorf("building realtime request: %v", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Cache-Control", "no-cache")
	// Deliberately no Last-Event-ID. Task 059 retains no history, so sending a
	// cursor would ask for a replay that cannot happen — and accepting one
	// back would be the start of treating the stream as durable.

	response, err := o.client.Do(request)
	if err != nil {
		cancel()
		return nil, operationalErrorf("connecting to realtime: %v", unwrapURLError(err))
	}

	if response.StatusCode != http.StatusOK {
		// Bounded in time as well as bytes. time.AfterFunc cancels the
		// request context, which aborts the read in progress — so the read
		// happens on this goroutine with nothing detached behind it, and the
		// timer has exactly one owner.
		deadline := time.AfterFunc(o.errorBodyTimeout, cancel)
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxSSEErrorBodyBytes))
		deadline.Stop()

		response.Body.Close()
		cancel()
		return nil, realtimeStatusError(response.StatusCode, body)
	}
	if mediaType := response.Header.Get("Content-Type"); !isEventStream(mediaType) {
		response.Body.Close()
		cancel()
		return nil, operationalErrorf(
			"realtime returned content-type %q, want text/event-stream", mediaType)
	}

	stream := &realtimeStream{
		frames: make(chan realtimeFrame, tuiPendingEventCapacity),
		cancel: cancel,
		body:   response.Body,
		done:   make(chan struct{}),
	}

	// The protocol deadline. Armed before the reader starts so it cannot be
	// assigned concurrently, and stopped by handshakeAccepted.
	stream.handshakeTimer = time.AfterFunc(o.handshakeTimeout, func() {
		stream.handshakeFailed.Store(true)
		cancel()
	})

	// One watchdog for the one active stream, fed by a capacity-1 signal.
	// Not a timer per frame and not a queue that grows with traffic: a busy
	// stream coalesces into the single pending slot, and the watchdog resets
	// once per wake rather than once per byte.
	activity := &activityReader{inner: response.Body, signal: make(chan struct{}, 1)}
	go watchStreamIdle(streamCtx, activity.signal, o.idleTimeout, func() {
		stream.idle.Store(true)
		cancel()
	})

	go stream.read(activity)
	return stream, nil
}

// activityReader refreshes a liveness signal as bytes arrive.
//
// Heartbeat comments count. They never become domain events, but they are
// exactly what task 059 sends to prove a quiet stream is alive, so a client
// that only counted events would disconnect healthy idle evaluations.
type activityReader struct {
	inner  io.Reader
	signal chan struct{}
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.inner.Read(p)
	if n > 0 {
		select {
		case a.signal <- struct{}{}:
		default:
			// A refresh is already pending; one is as good as many.
		}
	}
	return n, err
}

// watchStreamIdle cancels the stream when nothing has arrived for too long.
//
// Exits on context cancellation, so closing the stream stops it — there is no
// path where this goroutine outlives the connection it watches.
func watchStreamIdle(ctx context.Context, signal <-chan struct{}, timeout time.Duration, onIdle func()) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-signal:
			timer.Stop()
			timer.Reset(timeout)
		case <-timer.C:
			onIdle()
			return
		}
	}
}

// realtimeStatusError turns a non-200 subscription into a diagnostic.
func realtimeStatusError(status int, body []byte) error {
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != "" {
		return operationalErrorf("realtime unavailable: %s: %s",
			envelope.Error.Code, envelope.Error.Message)
	}
	return operationalErrorf("realtime returned HTTP %d", status)
}

func isEventStream(mediaType string) bool {
	media, _, _ := strings.Cut(mediaType, ";")
	return strings.EqualFold(strings.TrimSpace(media), "text/event-stream")
}

// read drains the socket into the bounded channel until something stops it.
//
// Continuously, not on demand: see this file's header for why pausing here
// gets the subscriber disconnected.
func (s *realtimeStream) read(source io.Reader) {
	// Whatever ends this stream, the protocol deadline is done with.
	defer s.handshakeAccepted()

	// Defers run last-in-first-out, so: close the body, then the frame
	// channel, then done. That order matters — a reader blocked on frames
	// must observe the close and only then find done already closed, or it
	// waits forever for a goroutine that has already stopped.
	defer close(s.done)
	defer close(s.frames)
	defer s.body.Close()

	parser := newSSEParser(source)
	for {
		frame, err := parser.next()
		if err != nil {
			switch {
			case s.handshakeFailed.Load():
				s.reason = errStreamHandshakeTimeout
			case s.idle.Load():
				s.reason = errStreamIdle
			default:
				s.reason = err
			}
			return
		}
		select {
		case s.frames <- frame:
		default:
			// The dashboard is behind its own stream. Abandoning and
			// resyncing is the only honest option: dropping the frame would
			// leave a gap the client cannot describe, and blocking here would
			// stop draining and get us disconnected anyway.
			s.reason = errStreamOverflow
			return
		}
	}
}

// close releases everything this stream owns. Idempotent.
func (s *realtimeStream) close() {
	s.cancel()
	<-s.done
}

// endReason reports why the reader stopped. Valid once done is closed.
func (s *realtimeStream) endReason() error { return s.reason }

// ---------------------------------------------------------------------
// SSE parsing
// ---------------------------------------------------------------------

// sseParser reads the subset of SSE task 059 emits.
//
// Not a browser EventSource clone: no retry field, no id handling, no
// reconnection policy. The parser's job is to turn bytes into frames and to
// refuse to grow while doing it.
type sseParser struct {
	reader *bufio.Reader

	event string
	data  strings.Builder
}

func newSSEParser(r io.Reader) *sseParser {
	return &sseParser{reader: bufio.NewReaderSize(r, 4<<10)}
}

// next returns the next domain frame, skipping comments and blank noise.
func (p *sseParser) next() (realtimeFrame, error) {
	for {
		line, err := p.readLine()
		if err != nil {
			return realtimeFrame{}, err
		}

		switch {
		case line == "":
			// Frame terminator. A blank line with no data is a heartbeat
			// separator or padding, not an event.
			if p.data.Len() == 0 && p.event == "" {
				continue
			}
			frame := realtimeFrame{event: p.event, data: p.data.String()}
			p.event = ""
			p.data.Reset()
			if frame.event == "" {
				// SSE defaults an unnamed event to "message"; task 059 always
				// names its events, so an unnamed one is not ours to interpret.
				continue
			}
			return frame, nil

		case strings.HasPrefix(line, ":"):
			// Comment. The server's heartbeat arrives here and means the
			// connection is alive — not that anything happened, and not a
			// signal to go fetch anything.
			continue
		}

		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			p.event = value
		case "data":
			// Repeated data lines are joined with newlines, per SSE.
			if p.data.Len() > 0 {
				p.data.WriteByte('\n')
			}
			if p.data.Len()+len(value) > maxSSEFrameBytes {
				return realtimeFrame{}, operationalErrorf(
					"realtime frame exceeds %d bytes", maxSSEFrameBytes)
			}
			p.data.WriteString(value)
		case "id":
			// Ignored, never stored, never echoed. Task 059 has no replay, so
			// an id is not a resume token and treating it as one would be the
			// first step toward believing the stream is durable.
			continue
		default:
			// Unknown field. SSE says ignore; so do we.
			continue
		}
	}
}

// readLine reads one line under a hard cap.
//
// bufio.Reader.ReadString would grow to whatever arrives. This reads in
// buffer-sized pieces and fails once the total passes the bound, so a server
// sending an endless line costs a fixed amount of memory rather than all of it.
func (p *sseParser) readLine() (string, error) {
	var builder strings.Builder
	for {
		chunk, isPrefix, err := p.reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			return "", err
		}
		if builder.Len()+len(chunk) > maxSSELineBytes {
			return "", operationalErrorf("realtime line exceeds %d bytes", maxSSELineBytes)
		}
		builder.Write(chunk)
		if !isPrefix {
			return builder.String(), nil
		}
	}
}
