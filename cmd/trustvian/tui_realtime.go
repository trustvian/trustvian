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
	sseIdleConnTimeout       = 30 * time.Second
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
}

// errStreamOverflow means the client could not keep up with its own stream.
//
// Not a dropped frame: the stream is abandoned and resynchronized instead,
// because a client that discarded a notification cannot know what it missed.
var errStreamOverflow = errors.New("realtime stream outpaced the dashboard")

// realtimeOpener opens a subscription. An interface so tests can drive the
// model's state machine without a socket.
type realtimeOpener interface {
	open(ctx context.Context, runID string) (*realtimeStream, error)
}

// sseOpener is the real implementation over HTTP.
type sseOpener struct {
	baseURL *url.URL
	client  *http.Client
}

// newSSEOpener builds the streaming client.
//
// Separate from task 060's client on purpose — see this file's header.
func newSSEOpener(base *url.URL) *sseOpener {
	return &sseOpener{
		baseURL: base,
		client: &http.Client{
			// No Timeout: a total response deadline would kill a healthy
			// dashboard. Every bound below is on establishing the connection.
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: sseDialTimeout}).DialContext,
				TLSHandshakeTimeout:   sseTLSHandshakeTimeout,
				ResponseHeaderTimeout: sseResponseHeaderTimeout,
				IdleConnTimeout:       sseIdleConnTimeout,
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
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
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
	go stream.read()
	return stream, nil
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
func (s *realtimeStream) read() {
	// Defers run last-in-first-out, so: close the body, then the frame
	// channel, then done. That order matters — a reader blocked on frames
	// must observe the close and only then find done already closed, or it
	// waits forever for a goroutine that has already stopped.
	defer close(s.done)
	defer close(s.frames)
	defer s.body.Close()

	parser := newSSEParser(s.body)
	for {
		frame, err := parser.next()
		if err != nil {
			s.reason = err
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
