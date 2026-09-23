package main

// The dashboard state machine.
//
// Deliberately separable from both the terminal and the network: Update is a
// pure function of (state, message), View has no side effects, and every
// effect is a tea.Cmd. That is what lets the ordering guarantees below be
// tested without a terminal and without sleeping.
//
// The invariant this file exists to hold:
//
//	subscribe → stream_ready → drain → fetch state → apply → replay buffered
//
// Fetching state and then subscribing leaves a window in which a committed
// mutation lands between the two and is seen by neither. See
// docs/adr/0034-tui-is-a-bounded-realtime-http-client.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tuiObservationCapacity bounds the displayed rows.
//
// A viewport, not evidence. Unlike the transport bound, exceeding this evicts
// the oldest row on purpose — the list is labelled "current stream" and task
// 067 owns history.
const tuiObservationCapacity = 100

// Reconnect backoff. Fixed and bounded: no unbounded counter, no jitter, and
// at most one pending timer.
var reconnectBackoff = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	4 * time.Second,
	5 * time.Second,
}

// connectionState is what the user is told about the stream.
type connectionState int

const (
	stateConnecting connectionState = iota
	stateResyncing
	stateLive
	stateReconnecting
	stateFatal
)

func (s connectionState) String() string {
	switch s {
	case stateConnecting:
		return "CONNECTING"
	case stateResyncing:
		return "RESYNCING"
	case stateLive:
		return "LIVE"
	case stateReconnecting:
		return "RECONNECTING"
	default:
		return "FAILED"
	}
}

// ---------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------

type streamOpenedMsg struct {
	stream *realtimeStream
	// generation guards against a late message from an abandoned stream
	// landing on the model after a newer one has started.
	generation int
}

type streamReadyMsg struct{ generation int }

type frameMsg struct {
	frame      realtimeFrame
	generation int
}

type streamEndedMsg struct {
	err        error
	generation int
}

type resyncDoneMsg struct {
	snapshot   authoritativeSnapshot
	generation int
}

type reconnectNowMsg struct{ generation int }

// fatalMsg ends the program with an operational exit.
type fatalMsg struct{ err error }

// authoritativeSnapshot is what the control plane said at the last resync.
type authoritativeSnapshot struct {
	run      evaluationRunDTO
	progress progressDTO
	taken    bool
}

// observationRow is one displayed live observation.
type observationRow struct {
	sequence    string
	newBehavior bool
	decision    string
	risk        string
	trust       float64
	anomaly     float64
	behavior    string
}

// ---------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------

type tuiModel struct {
	runID  string
	opener realtimeOpener
	reader authoritativeReader

	ctx context.Context

	state    connectionState
	snapshot authoritativeSnapshot

	// live holds rows observed since the current resync. Cleared on every
	// reconnect: joining two disconnected streams would assert a continuity
	// that does not exist.
	live []observationRow

	// pending holds frames that arrived between stream_ready and the snapshot
	// being applied. Bounded; overflow abandons the stream rather than
	// dropping a notification.
	pending []realtimeFrame

	stream     *realtimeStream
	generation int
	backoffAt  int

	// genCtx scopes every network operation belonging to the current
	// generation; genCancel abandons them.
	//
	// Generation numbers stop a stale *result* from being applied. They do
	// not stop the work: a blocked open or a blocked authoritative GET kept
	// running until it timed out or the program exited, so repeated `r`
	// accumulated sockets and goroutines whose answers were already destined
	// to be ignored. Cancelling is what makes "one active generation" true
	// rather than merely apparent.
	genCtx    context.Context
	genCancel context.CancelFunc

	// waiting is true while exactly one frame read is outstanding. The
	// invariant matters: two concurrent reads would interleave frames and
	// break the ordering the resync protocol depends on.
	waiting bool

	// everLive records whether a full stream_ready + resync ever completed.
	// Before that, a failure is fatal; after it, a failure is a reconnect.
	everLive bool

	// resyncPending guards the one-resync-per-terminal-event rule.
	resyncing bool

	width, height int
	showHelp      bool
	lastError     string
	quitting      bool
	exitCode      int
}

// authoritativeReader fetches durable state. An interface so the model can be
// driven without HTTP.
type authoritativeReader interface {
	run(ctx context.Context, runID string) (evaluationRunDTO, error)
	progress(ctx context.Context, runID string) (progressDTO, error)
}

func newTUIModel(ctx context.Context, runID string, opener realtimeOpener, reader authoritativeReader) *tuiModel {
	m := &tuiModel{
		runID:  runID,
		opener: opener,
		reader: reader,
		ctx:    ctx,
		state:  stateConnecting,
		width:  80,
		height: 24,
	}
	m.beginGeneration()
	return m
}

// beginGeneration abandons the previous generation's network work and starts
// a fresh scope for the next one.
//
// The context lives for the whole generation, not just the open: the SSE
// response body is read under it, so cancelling once open() returned would
// tear down the stream that just succeeded.
func (m *tuiModel) beginGeneration() {
	if m.genCancel != nil {
		m.genCancel()
	}
	m.genCtx, m.genCancel = context.WithCancel(m.ctx)
}

// endGeneration cancels the current scope without opening a new one. Used on
// quit and on terminal failure.
func (m *tuiModel) endGeneration() {
	if m.genCancel != nil {
		m.genCancel()
		m.genCancel = nil
	}
}

func (m *tuiModel) Init() tea.Cmd { return m.openStream() }

// openStream subscribes. Always before any authoritative read.
func (m *tuiModel) openStream() tea.Cmd {
	generation, ctx := m.generation, m.genCtx
	return func() tea.Msg {
		stream, err := m.opener.open(ctx, m.runID)
		if err != nil {
			return streamEndedMsg{err: err, generation: generation}
		}
		return streamOpenedMsg{stream: stream, generation: generation}
	}
}

// waitForFrame reads exactly one frame. Re-issued after each one, so never
// more than a single outstanding read.
func (m *tuiModel) waitForFrame() tea.Cmd {
	stream, generation := m.stream, m.generation
	return func() tea.Msg {
		frame, open := <-stream.frames
		if !open {
			<-stream.done
			return streamEndedMsg{err: stream.endReason(), generation: generation}
		}
		return frameMsg{frame: frame, generation: generation}
	}
}

// resync fetches the two authoritative reads.
//
// Runs as a command, concurrently with frame reading — that concurrency is the
// point. A client that stopped draining while this was in flight would fill
// the server's 64-event queue and be disconnected for being slow.
func (m *tuiModel) resync() tea.Cmd {
	generation, ctx := m.generation, m.genCtx
	runID := m.runID
	return func() tea.Msg {
		run, err := m.reader.run(ctx, runID)
		if err != nil {
			return streamEndedMsg{err: err, generation: generation}
		}
		progress, err := m.reader.progress(ctx, runID)
		if err != nil {
			return streamEndedMsg{err: err, generation: generation}
		}
		return resyncDoneMsg{
			snapshot:   authoritativeSnapshot{run: run, progress: progress, taken: true},
			generation: generation,
		}
	}
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case streamOpenedMsg:
		if msg.generation != m.generation {
			// An abandoned stream finished opening. Close it rather than
			// leaking the connection and its goroutine.
			msg.stream.close()
			return m, nil
		}
		m.stream = msg.stream
		m.waiting = true
		return m, m.waitForFrame()

	case frameMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.waiting = false
		return m.handleFrame(msg.frame)

	case resyncDoneMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		return m.applySnapshot(msg.snapshot)

	case streamEndedMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		return m.handleStreamEnd(msg.err)

	case reconnectNowMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		return m, m.reconnect()

	case fatalMsg:
		m.state = stateFatal
		m.lastError = sanitizeTerminalText(msg.err.Error())
		m.exitCode = exitOperational
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		m.exitCode = exitOK
		m.releaseStream()
		m.endGeneration()
		return m, tea.Quit
	case "r":
		// Cancels any pending backoff by advancing the generation, so a
		// scheduled reconnectNowMsg for the old generation is ignored.
		return m, m.reconnect()
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	}
	return m, nil
}

// handleFrame processes one SSE frame and re-arms the single outstanding read.
func (m *tuiModel) handleFrame(frame realtimeFrame) (tea.Model, tea.Cmd) {
	next := m.waitForFrame()
	m.waiting = true

	if frame.event == eventStreamReady {
		if m.state != stateConnecting && m.state != stateReconnecting {
			// A second handshake mid-stream means the connection is not what
			// this client thinks it is.
			return m.failStream(operationalErrorf("realtime sent a second stream_ready"))
		}
		if err := validateStreamReady(frame.data); err != nil {
			return m.failStream(err)
		}
		// The protocol is established, so the handshake deadline is released.
		// Only a *valid* handshake does this: an event merely named
		// stream_ready, or any other byte, leaves it armed.
		if m.stream != nil {
			m.stream.handshakeAccepted()
		}
		m.state = stateResyncing
		m.resyncing = true
		return m, tea.Batch(next, m.resync())
	}

	if m.state == stateConnecting || m.state == stateReconnecting {
		// Nothing may be consumed as if state were synchronized before the
		// handshake has been seen and validated.
		return m.failStream(operationalErrorf(
			"realtime sent %q before stream_ready", sanitizeTerminalText(frame.event)))
	}

	kind, envelope, err := classifyFrame(frame, m.runID)
	if err != nil {
		// A known event the client cannot parse means the stream is no longer
		// trustworthy. Discarding it silently would pretend nothing was lost.
		return m.failStream(err)
	}
	if kind == "" {
		// Unknown event name: additive kinds must be tolerated, so the stream
		// continues untouched.
		return m, next
	}

	if m.state == stateResyncing {
		if len(m.pending) >= tuiPendingEventCapacity {
			return m.failStream(errStreamOverflow)
		}
		m.pending = append(m.pending, frame)
		return m, next
	}

	cmd := m.applyEvent(kind, envelope)
	if cmd != nil {
		return m, tea.Batch(next, cmd)
	}
	return m, next
}

// applyEvent folds one decoded event into the live view.
//
// Returns a command only for a terminal lifecycle event, which earns exactly
// one authoritative read — event-driven, with nothing periodic after it.
func (m *tuiModel) applyEvent(kind string, envelope realtimeEnvelope) tea.Cmd {
	switch kind {
	case kindObservation:
		if envelope.Observation != nil {
			m.appendObservation(*envelope.Observation)
		}
	case kindEvaluationCreated, kindEvaluationStarted,
		kindEvaluationCompleted, kindEvaluationFailed, kindEvaluationCancelled:
		if envelope.Evaluation != nil {
			m.snapshot.run.Status = envelope.Evaluation.Status
			if envelope.Evaluation.FailureReason != "" {
				m.snapshot.run.FailureReason = envelope.Evaluation.FailureReason
			}
		}
		if isTerminalLifecycle(kind) && !m.resyncing {
			m.resyncing = true
			return m.resync()
		}
	}
	return nil
}

// appendObservation adds a row, evicting the oldest when full.
//
// Deliberate truncation: this is a display window. The transport bound behaves
// differently on purpose — it never drops.
func (m *tuiModel) appendObservation(observation realtimeObservation) {
	row := observationRow{
		sequence:    observation.Sequence,
		newBehavior: observation.NewBehavior,
		decision:    observation.Decision,
		risk:        observation.RiskLevel,
		trust:       observation.TrustScore,
		anomaly:     observation.AnomalyScore,
		behavior:    describeBehavior(observation.Behavior),
	}
	if len(m.live) == tuiObservationCapacity {
		m.live = append(m.live[:0], m.live[1:]...)
	}
	m.live = append(m.live, row)
}

// applySnapshot installs authoritative state and replays what was buffered.
func (m *tuiModel) applySnapshot(snapshot authoritativeSnapshot) (tea.Model, tea.Cmd) {
	m.snapshot = snapshot
	m.resyncing = false
	m.state = stateLive
	m.everLive = true
	m.backoffAt = 0
	m.lastError = ""

	// Frames that arrived while the reads were in flight. Applied after the
	// snapshot, which is why subscribing first loses nothing.
	pending := m.pending
	m.pending = nil

	// Commands are collected, not discarded. applyEvent returns one only for
	// a terminal lifecycle event, and that command *is* the final
	// authoritative read. Dropping it here meant a run that completed while
	// the first resync was still in flight kept the stale snapshot forever:
	// the status updated from the event, the counts never did, and
	// m.resyncing stayed true so nothing could re-arm it.
	var cmds []tea.Cmd
	for _, frame := range pending {
		kind, envelope, err := classifyFrame(frame, m.runID)
		if err != nil {
			return m.failStream(err)
		}
		if kind == "" {
			continue
		}
		if cmd := m.applyEvent(kind, envelope); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// At most one: applyEvent's own m.resyncing guard means the first
	// terminal event schedules the read and any later one finds it already
	// pending. tea.Batch(nil...) is a no-op, which is the common case.
	return m, tea.Batch(cmds...)
}

// handleStreamEnd decides between fatal and reconnect.
//
// Before the dashboard has ever been live, an unrecoverable failure exits:
// sitting forever on a quiet subscription to a typoed run ID is worse than
// failing. After it has been live, a drop is a reconnect — task 059
// disconnects slow consumers, and EOF never means the evaluation ended.
func (m *tuiModel) handleStreamEnd(err error) (tea.Model, tea.Cmd) {
	m.releaseStream()

	if !m.everLive {
		m.state = stateFatal
		m.lastError = sanitizeTerminalText(errorText(err))
		m.exitCode = exitOperational
		m.quitting = true
		m.endGeneration()
		return m, tea.Quit
	}

	m.state = stateReconnecting
	m.lastError = sanitizeTerminalText(errorText(err))
	return m, m.scheduleReconnect()
}

// failStream abandons the current connection for a protocol reason.
func (m *tuiModel) failStream(err error) (tea.Model, tea.Cmd) {
	return m.handleStreamEnd(err)
}

// scheduleReconnect arms the single pending timer.
func (m *tuiModel) scheduleReconnect() tea.Cmd {
	delay := reconnectBackoff[min(m.backoffAt, len(reconnectBackoff)-1)]
	m.backoffAt++

	m.generation++
	generation := m.generation
	// Cancels anything the failed generation still had in flight — typically
	// an authoritative read that outlived the stream it belonged to.
	m.beginGeneration()
	// Live rows are cleared here rather than on success: the screen must stop
	// implying the list is current the moment the stream is gone.
	m.live = nil
	m.pending = nil

	return tea.Tick(delay, func(time.Time) tea.Msg {
		return reconnectNowMsg{generation: generation}
	})
}

// reconnect abandons anything in flight and subscribes again immediately.
func (m *tuiModel) reconnect() tea.Cmd {
	m.releaseStream()
	m.generation++
	m.beginGeneration()
	m.live = nil
	m.pending = nil
	m.resyncing = false
	m.state = stateConnecting
	return m.openStream()
}

// releaseStream closes the current connection, if any.
func (m *tuiModel) releaseStream() {
	if m.stream != nil {
		m.stream.close()
		m.stream = nil
	}
	m.waiting = false
}

// ---------------------------------------------------------------------
// Frame decoding
// ---------------------------------------------------------------------

// validateStreamReady enforces the handshake before anything is trusted.
func validateStreamReady(data string) error {
	var ready streamReadyPayload
	if err := json.Unmarshal([]byte(data), &ready); err != nil {
		return operationalErrorf("realtime handshake is not valid JSON")
	}
	if ready.Version != wireVersion {
		return operationalErrorf(
			"realtime handshake version %q is not supported", sanitizeTerminalText(ready.Version))
	}
	if ready.ReplayAvailable {
		// Task 059 retains nothing. A server claiming replay is not the
		// server this client knows how to resynchronize against.
		return operationalErrorf("realtime claims replay is available; this client expects none")
	}
	if !ready.ResyncRequired {
		return operationalErrorf("realtime did not require resynchronization")
	}
	return nil
}

// classifyFrame decodes and structurally validates a domain frame.
//
// Returns an empty kind for an event name this build does not know, which the
// caller ignores: additive kinds must not break a stream, and an unknown one
// is not held to any shape. A *known* kind that fails validation returns an
// error, because silently discarding it would pretend the stream stayed
// complete when a frame was in fact dropped.
//
// The checks are wire structural integrity only — no lifecycle legality, no
// gate semantics, no field-by-field domain validation. That is the server's,
// and duplicating it here would be a second implementation that drifts.
func classifyFrame(frame realtimeFrame, watchedRunID string) (string, realtimeEnvelope, error) {
	switch frame.event {
	case kindObservation, kindEvaluationCreated, kindEvaluationStarted,
		kindEvaluationCompleted, kindEvaluationFailed, kindEvaluationCancelled:
	default:
		return "", realtimeEnvelope{}, nil
	}

	var envelope realtimeEnvelope
	if err := json.Unmarshal([]byte(frame.data), &envelope); err != nil {
		return "", realtimeEnvelope{}, operationalErrorf(
			"realtime %s event is not valid JSON", sanitizeTerminalText(frame.event))
	}
	if err := validateRealtimeEnvelope(frame.event, envelope, watchedRunID); err != nil {
		return "", realtimeEnvelope{}, err
	}
	return frame.event, envelope, nil
}

// validateRealtimeEnvelope checks a known event against what task 059 emits.
//
// Every failure here was previously accepted in silence: an observation with
// no observation payload did nothing, a mismatched kind was applied under the
// SSE name, and an event scoped to another run would have been rendered as
// this run's. None of those is an additive change a client should tolerate —
// each means the client and the server disagree about what arrived.
func validateRealtimeEnvelope(eventName string, envelope realtimeEnvelope, watchedRunID string) error {
	safeName := sanitizeTerminalText(eventName)

	if envelope.Version != wireVersion {
		return operationalErrorf(
			"realtime payload version %q is not supported", sanitizeTerminalText(envelope.Version))
	}
	// The SSE event name and the payload's own kind must agree. They are two
	// statements about the same thing, and a client that trusts one while
	// ignoring the other will eventually apply an event as the wrong type.
	if envelope.Kind != eventName {
		return operationalErrorf(
			"realtime %s event carries kind %q", safeName, sanitizeTerminalText(envelope.Kind))
	}
	// Scoped to the run this dashboard is watching. The subscription is
	// filtered server-side, so a mismatch means the filter did not hold —
	// rendering it would attribute another run's behavior to this one, which
	// is worse than showing nothing.
	if envelope.Scope.RunID != watchedRunID {
		return operationalErrorf(
			"realtime %s event is scoped to run %q, not the watched run",
			safeName, sanitizeTerminalText(envelope.Scope.RunID))
	}

	// Task 059 emits exactly the half that matches the kind, so requiring the
	// other half to be absent is a real check rather than a formality.
	switch eventName {
	case kindObservation:
		if envelope.Observation == nil {
			return operationalErrorf("realtime %s event carries no observation", safeName)
		}
		if envelope.Evaluation != nil {
			return operationalErrorf("realtime %s event also carries an evaluation", safeName)
		}
	default:
		if envelope.Evaluation == nil {
			return operationalErrorf("realtime %s event carries no evaluation", safeName)
		}
		if envelope.Observation != nil {
			return operationalErrorf("realtime %s event also carries an observation", safeName)
		}
	}
	return nil
}

// describeBehavior renders the stable behavioral shape compactly.
//
// Presentation only: nothing here recomputes a fingerprint or decides whether
// the behavior is acceptable.
func describeBehavior(behavior behaviorDescriptor) string {
	var builder strings.Builder
	builder.WriteString(behavior.OperationCategory)
	if behavior.OperationName != "" {
		builder.WriteString("/")
		builder.WriteString(behavior.OperationName)
	}
	target := behavior.TargetName
	if target == "" {
		target = behavior.TargetCategory
	}
	if target != "" {
		builder.WriteString(" → ")
		builder.WriteString(target)
	}
	if builder.Len() == 0 {
		return "(unspecified)"
	}
	return builder.String()
}

func errorText(err error) string {
	if err == nil {
		return "stream closed"
	}
	return err.Error()
}

// exitCodeForModel reports how the program should terminate.
func (m *tuiModel) finalExitCode() int { return m.exitCode }

var _ = fmt.Sprintf
