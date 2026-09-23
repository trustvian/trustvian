package main

// Protocol validation, bounds, terminal safety, and pure model behavior.
//
// Everything here runs without a terminal and most of it without a socket:
// Update is a function of (state, message), so the interesting transitions can
// be driven directly.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------
// stream_ready validation
// ---------------------------------------------------------------------

// TestStreamReadyValidation refuses to treat a connection as synchronized
// unless the handshake says exactly what task 059 promises.
func TestStreamReadyValidation(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{"valid", `{"version":"1","replay_available":false,"resync_required":true}`, false},
		{"additive field tolerated",
			`{"version":"1","replay_available":false,"resync_required":true,"future":{"a":1}}`, false},

		{"replay claimed",
			`{"version":"1","replay_available":true,"resync_required":true}`, true},
		{"resync not required",
			`{"version":"1","replay_available":false,"resync_required":false}`, true},
		{"missing version",
			`{"replay_available":false,"resync_required":true}`, true},
		{"unknown version",
			`{"version":"2","replay_available":false,"resync_required":true}`, true},
		{"malformed JSON", `{"version":`, true},
		{"empty", ``, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStreamReady(tt.data)
			if tt.wantErr != (err != nil) {
				t.Fatalf("validateStreamReady(%s) error = %v, wantErr = %v", tt.data, err, tt.wantErr)
			}
		})
	}
}

// TestObservationBeforeStreamReadyIsRejected stops the client consuming events
// as if state were already synchronized.
func TestObservationBeforeStreamReadyIsRejected(t *testing.T) {
	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateConnecting

	_, _ = model.handleFrame(realtimeFrame{
		event: kindObservation, data: observationFrame("1", false)})

	if model.state == stateLive || model.state == stateResyncing {
		t.Fatalf("state = %v; an observation before stream_ready must invalidate the stream",
			model.state)
	}
	if len(model.live) != 0 {
		t.Errorf("live rows = %d, want 0", len(model.live))
	}
}

// ---------------------------------------------------------------------
// Event-kind compatibility
// ---------------------------------------------------------------------

// TestUnknownEventKindIsTolerated protects additive server changes.
func TestUnknownEventKindIsTolerated(t *testing.T) {
	kind, _, err := classifyFrame(realtimeFrame{
		event: "future_event_type",
		data:  `{"version":"1","kind":"future_event_type","future":true}`,
	}, "run-42")
	if err != nil {
		t.Fatalf("an unknown event kind must not fail the stream: %v", err)
	}
	if kind != "" {
		t.Errorf("kind = %q, want empty so the caller ignores it", kind)
	}
}

// TestMalformedKnownEventInvalidatesStream is the other half.
//
// Discarding it silently would pretend the stream stayed complete, which is
// precisely the claim the client cannot make.
func TestMalformedKnownEventInvalidatesStream(t *testing.T) {
	tests := []struct {
		name  string
		frame realtimeFrame
	}{
		{"not JSON", realtimeFrame{event: kindObservation, data: "not-json"}},
		{"truncated", realtimeFrame{event: kindObservation, data: `{"version":"1"`}},
		{"unknown payload version", realtimeFrame{
			event: kindObservation,
			data:  `{"version":"2","kind":"observation","observation":{"sequence":"1"}}`}},
		{"missing version", realtimeFrame{
			event: kindEvaluationStarted, data: `{"kind":"evaluation_started"}`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := classifyFrame(tt.frame, "run-42"); err == nil {
				t.Fatal("a malformed known event must invalidate the stream")
			}
		})
	}
}

// TestUnknownObservationFieldsAreIgnored is the privacy guarantee.
//
// The realtime projection excludes prompts, tool arguments and attributes on
// purpose. A field the client does not know must be neither rendered nor
// retained — and there is no endpoint that would return the raw record.
func TestUnknownObservationFieldsAreIgnored(t *testing.T) {
	const secret = "SECRET_PROMPT_DO_NOT_RENDER"
	data := fmt.Sprintf(`{"version":"1","kind":"observation","scope":{"run_id":"run-42"},`+
		`"observation":{"sequence":"7","record_count":"7","fingerprint_id":"fp-1",`+
		`"behavior":{"operation_category":"tool","operation_name":"shell.execute",`+
		`"target_name":"build-host"},"decision":"allow","risk_level":"low",`+
		`"trust_score":0.9,"anomaly_score":0.1,"new_behavior":false,`+
		`"prompt":%q,"attributes":{"tool_argument":%q}}}`, secret, secret)

	kind, envelope, err := classifyFrame(realtimeFrame{event: kindObservation, data: data}, "run-42")
	if err != nil {
		t.Fatalf("classifyFrame: %v", err)
	}
	if kind != kindObservation {
		t.Fatalf("kind = %q", kind)
	}

	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateLive
	model.snapshot.taken = true
	model.applyEvent(kind, envelope)

	view := model.View()
	if strings.Contains(view, secret) {
		t.Fatalf("an unknown observation field reached the view:\n%s", view)
	}
	if strings.Contains(fmt.Sprintf("%+v", model.live), secret) {
		t.Fatal("an unknown observation field was retained in model state")
	}
}

// ---------------------------------------------------------------------
// SSE parser
// ---------------------------------------------------------------------

func TestSSEParserReadsTheServerContract(t *testing.T) {
	raw := ": keepalive\n\n" +
		"event: stream_ready\ndata: {\"version\":\"1\"}\n\n" +
		"id: 7\nevent: observation\ndata: {\"a\":1}\ndata: {\"b\":2}\n\n" +
		"unknown-field: ignored\nevent: evaluation_started\ndata: {}\n\n"

	parser := newSSEParser(strings.NewReader(raw))

	first, err := parser.next()
	if err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if first.event != "stream_ready" || first.data != `{"version":"1"}` {
		t.Errorf("first = %+v", first)
	}

	second, err := parser.next()
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}
	// Repeated data lines join with a newline, per SSE. The id line is
	// ignored entirely — task 059 has no replay, so it is not a resume token.
	if second.event != "observation" || second.data != "{\"a\":1}\n{\"b\":2}" {
		t.Errorf("second = %+v", second)
	}

	third, err := parser.next()
	if err != nil {
		t.Fatalf("third frame: %v", err)
	}
	if third.event != "evaluation_started" {
		t.Errorf("third = %+v", third)
	}

	if _, err := parser.next(); !errors.Is(err, io.EOF) {
		t.Errorf("end of stream error = %v, want EOF", err)
	}
}

// TestSSEParserBoundsInput proves a hostile or broken server cannot make the
// client allocate without limit.
func TestSSEParserBoundsInput(t *testing.T) {
	t.Run("line at the limit is accepted", func(t *testing.T) {
		// "data: " plus payload, kept just inside the line bound.
		payload := strings.Repeat("x", maxSSELineBytes-len("data: ")-1)
		parser := newSSEParser(strings.NewReader("event: observation\ndata: " + payload + "\n\n"))
		frame, err := parser.next()
		if err != nil {
			t.Fatalf("a line at the bound must be accepted: %v", err)
		}
		if len(frame.data) != len(payload) {
			t.Errorf("data length = %d, want %d", len(frame.data), len(payload))
		}
	})

	t.Run("endless line fails boundedly", func(t *testing.T) {
		// A reader that never ends: if the parser grew to fit, this would
		// never return. The bound is what makes it fail instead of hang.
		endless := io.MultiReader(
			strings.NewReader("event: observation\ndata: "),
			endlessReader{})
		parser := newSSEParser(endless)

		done := make(chan error, 1)
		go func() { _, err := parser.next(); done <- err }()

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("an endless line must fail")
			}
			if !strings.Contains(err.Error(), "exceeds") {
				t.Errorf("error = %v, want a bound diagnostic", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the parser did not stop on an endless line; the bound is missing")
		}
	})

	t.Run("frame over the limit fails", func(t *testing.T) {
		// Many data lines, each within the line bound, summing past the frame
		// bound — the case a per-line check alone would miss.
		var builder strings.Builder
		builder.WriteString("event: observation\n")
		chunk := strings.Repeat("y", 8<<10)
		for range (maxSSEFrameBytes / len(chunk)) + 2 {
			builder.WriteString("data: " + chunk + "\n")
		}
		builder.WriteString("\n")

		parser := newSSEParser(strings.NewReader(builder.String()))
		if _, err := parser.next(); err == nil {
			t.Fatal("a frame over the bound must fail")
		}
	})
}

// endlessReader never stops producing non-newline bytes.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'z'
	}
	return len(p), nil
}

// ---------------------------------------------------------------------
// Bounded display window
// ---------------------------------------------------------------------

// TestObservationWindowEvictsOldest is deliberate UI truncation.
//
// Unlike the transport bound, which never drops, this window is a viewport.
// The distinction is the point: one would be data loss, the other is a screen.
func TestObservationWindowEvictsOldest(t *testing.T) {
	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateLive

	const extra = 25
	for i := range tuiObservationCapacity + extra {
		model.appendObservation(realtimeObservation{Sequence: fmt.Sprint(i + 1)})
	}

	if len(model.live) != tuiObservationCapacity {
		t.Fatalf("live rows = %d, want the capacity %d", len(model.live), tuiObservationCapacity)
	}
	// Oldest gone, newest kept.
	if model.live[0].sequence != fmt.Sprint(extra+1) {
		t.Errorf("oldest retained row = %q, want %d", model.live[0].sequence, extra+1)
	}
	if last := model.live[len(model.live)-1].sequence; last != fmt.Sprint(tuiObservationCapacity+extra) {
		t.Errorf("newest row = %q, want %d", last, tuiObservationCapacity+extra)
	}
}

// ---------------------------------------------------------------------
// Terminal safety
// ---------------------------------------------------------------------

// TestSanitizeTerminalText neutralizes what a terminal would execute.
func TestSanitizeTerminalText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		safe  bool
	}{
		{"plain", "run-42", true},
		{"unicode is preserved", "café ✓ 日本語", true},
		{"clear screen", "\x1b[2J", false},
		{"window title OSC", "\x1b]0;owned\x07", false},
		{"bare BEL", "ding\x07", false},
		{"carriage return", "a\rb", false},
		{"newline", "a\nb", false},
		{"tab", "a\tb", false},
		{"NUL", "a\x00b", false},
		{"DEL", "a\x7fb", false},
		{"C1 CSI", "a\u009bb", false},
		{"C1 OSC", "a\u009db", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTerminalText(tt.input)
			if tt.safe {
				if got != tt.input {
					t.Errorf("safe text was altered: %q -> %q", tt.input, got)
				}
				return
			}
			for _, r := range got {
				if isTerminalControl(r) {
					t.Fatalf("control %U survived sanitization of %q", r, tt.input)
				}
			}
		})
	}
}

// TestRemoteTextCannotEmitControlSequences is the mandatory injection
// regression, driven through every field a server controls.
func TestRemoteTextCannotEmitControlSequences(t *testing.T) {
	const payload = "\x1b[2J\x1b]0;owned\x07"

	model := newTUIModel(context.Background(), "run-42"+payload, nil, nil)
	model.state = stateLive
	model.width, model.height = 120, 40
	model.snapshot = authoritativeSnapshot{
		taken: true,
		run: evaluationRunDTO{
			ID:                "run-42" + payload,
			CandidateID:       "cand" + payload,
			Environment:       "local" + payload,
			BehavioralProfile: "profile" + payload,
			Status:            "failed" + payload,
			FailureReason:     "reason" + payload,
		},
		progress: progressDTO{
			RecordCount:        "18" + payload,
			NextIngestSequence: "19" + payload,
		},
	}
	model.appendObservation(realtimeObservation{
		Sequence:  "18" + payload,
		Decision:  "allow" + payload,
		RiskLevel: "medium" + payload,
		Behavior: behaviorDescriptor{
			OperationCategory: "tool" + payload,
			OperationName:     "shell.execute" + payload,
			TargetName:        "build-host" + payload,
		},
	})
	model.lastError = sanitizeTerminalText("boom" + payload)

	view := model.View()

	for _, forbidden := range []struct {
		name string
		b    byte
	}{
		{"ESC", 0x1b}, {"BEL", 0x07}, {"DEL", 0x7f}, {"NUL", 0x00},
	} {
		if strings.IndexByte(view, forbidden.b) >= 0 {
			t.Errorf("%s from server text reached the rendered view", forbidden.name)
		}
	}
	// The screen still shows something useful — sanitizing must not blank it.
	if !strings.Contains(view, "Trustvian") {
		t.Errorf("view lost its content:\n%q", view)
	}
}

// ---------------------------------------------------------------------
// Pure model behavior
// ---------------------------------------------------------------------

func TestTUIModelKeybindings(t *testing.T) {
	t.Run("q quits with 0", func(t *testing.T) {
		model := newTUIModel(context.Background(), "run-42", nil, nil)
		_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		if cmd == nil {
			t.Fatal("q produced no command")
		}
		if !model.quitting || model.finalExitCode() != exitOK {
			t.Errorf("quitting = %v, exit = %d, want true/0", model.quitting, model.finalExitCode())
		}
	})

	t.Run("ctrl-c quits with 0", func(t *testing.T) {
		model := newTUIModel(context.Background(), "run-42", nil, nil)
		_, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if !model.quitting || model.finalExitCode() != exitOK {
			t.Errorf("quitting = %v, exit = %d", model.quitting, model.finalExitCode())
		}
	})

	t.Run("? toggles help", func(t *testing.T) {
		model := newTUIModel(context.Background(), "run-42", nil, nil)
		_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
		if !model.showHelp {
			t.Error("? did not enable help")
		}
		_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
		if model.showHelp {
			t.Error("? did not toggle help back off")
		}
	})

	t.Run("no lifecycle mutation keys", func(t *testing.T) {
		// The TUI is read-only; s/c/f must do nothing at all.
		for _, key := range []rune{'s', 'c', 'f', 'd', 'x'} {
			model := newTUIModel(context.Background(), "run-42", nil, nil)
			before := model.state
			_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			if cmd != nil {
				t.Errorf("key %q produced a command; the TUI must not mutate", key)
			}
			if model.state != before {
				t.Errorf("key %q changed state", key)
			}
		}
	})
}

// TestTUIRendersAtAnyViewport covers resize, including degenerate sizes a
// detached session or a mid-resize terminal can report.
func TestTUIRendersAtAnyViewport(t *testing.T) {
	sizes := []struct{ w, h int }{
		{0, 0}, {1, 1}, {10, 3}, {19, 5}, {20, 6}, {40, 10}, {80, 24}, {500, 400},
	}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			model := newTUIModel(context.Background(), "run-42", nil, nil)
			model.snapshot = authoritativeSnapshot{taken: true,
				run:      evaluationRunDTO{ID: "run-42", Status: "running"},
				progress: progressDTO{RecordCount: "18", DistinctBehaviorCount: 11}}
			model.state = stateLive
			for i := range tuiObservationCapacity {
				model.appendObservation(realtimeObservation{Sequence: fmt.Sprint(i)})
			}

			_, _ = model.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})

			// The assertion is that this does not panic and returns something.
			view := model.View()
			if view == "" {
				t.Error("View returned nothing")
			}
		})
	}
}

// TestTUIViewHasNoSideEffects keeps rendering pure.
func TestTUIViewHasNoSideEffects(t *testing.T) {
	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateLive
	model.snapshot = authoritativeSnapshot{taken: true,
		run: evaluationRunDTO{ID: "run-42", Status: "running"}}
	model.appendObservation(realtimeObservation{Sequence: "1"})

	before := fmt.Sprintf("%v|%v|%d|%d|%v",
		model.state, model.snapshot, len(model.live), len(model.pending), model.showHelp)
	for range 5 {
		_ = model.View()
	}
	after := fmt.Sprintf("%v|%v|%d|%d|%v",
		model.state, model.snapshot, len(model.live), len(model.pending), model.showHelp)

	if before != after {
		t.Errorf("View mutated the model:\nbefore %s\nafter  %s", before, after)
	}
}

// TestLifecycleStatusRendering shows the server's status and nothing more.
func TestLifecycleStatusRendering(t *testing.T) {
	tests := []struct {
		kind   string
		status string
	}{
		{kindEvaluationCreated, "pending"},
		{kindEvaluationStarted, "running"},
		{kindEvaluationCompleted, "completed"},
		{kindEvaluationFailed, "failed"},
		{kindEvaluationCancelled, "cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			model := newTUIModel(context.Background(), "run-42", nil, nil)
			model.state = stateLive
			model.snapshot.taken = true
			model.width, model.height = 80, 24
			// resyncing set so a terminal kind does not try to call a nil reader.
			model.resyncing = true

			model.applyEvent(tt.kind, realtimeEnvelope{
				Version:    wireVersion,
				Kind:       tt.kind,
				Evaluation: &realtimeEvaluation{Status: tt.status},
			})

			if model.snapshot.run.Status != tt.status {
				t.Errorf("status = %q, want %q", model.snapshot.run.Status, tt.status)
			}
			view := model.View()
			if !strings.Contains(view, tt.status) {
				t.Errorf("view does not show %q:\n%s", tt.status, view)
			}
			// No verdict-shaped words: completion means execution finished.
			for _, forbidden := range []string{"safe", "passed", "promotable", "PASS", "FAIL"} {
				if strings.Contains(view, forbidden) {
					t.Errorf("view claims %q, which the data does not say", forbidden)
				}
			}
		})
	}
}

// TestDescribeBehaviorIsPresentationOnly renders the stable shape without
// interpreting it.
func TestDescribeBehaviorIsPresentationOnly(t *testing.T) {
	tests := []struct {
		name string
		in   behaviorDescriptor
		want string
	}{
		{"full", behaviorDescriptor{OperationCategory: "tool",
			OperationName: "shell.execute", TargetName: "build-host"},
			"tool/shell.execute → build-host"},
		{"target category fallback", behaviorDescriptor{OperationCategory: "http",
			OperationName: "POST /pay", TargetCategory: "database"},
			"http/POST /pay → database"},
		{"category only", behaviorDescriptor{OperationCategory: "http"}, "http"},
		{"empty", behaviorDescriptor{}, "(unspecified)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeBehavior(tt.in); got != tt.want {
				t.Errorf("describeBehavior() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNewBehaviorLabelIsFactual guards the wording.
//
// A new behavior is a shape this run had not produced before. It is not
// unsafe, bad, a violation or a regression, and the dashboard must not say so.
func TestNewBehaviorLabelIsFactual(t *testing.T) {
	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateLive
	model.snapshot.taken = true
	model.width, model.height = 100, 30
	model.appendObservation(realtimeObservation{
		Sequence: "1", NewBehavior: true, Decision: "block", RiskLevel: "critical",
		Behavior: behaviorDescriptor{OperationCategory: "tool", OperationName: "rm"}})

	view := model.View()
	for _, forbidden := range []string{
		"unsafe", "violation", "regression", "bad", "danger", "ALERT!",
	} {
		if strings.Contains(strings.ToLower(view), strings.ToLower(forbidden)) {
			t.Errorf("view interprets a new behavior as %q", forbidden)
		}
	}
	// The server's own values are shown.
	if !strings.Contains(view, "BLOCK") || !strings.Contains(view, "critical") {
		t.Errorf("view dropped the server's decision or risk:\n%s", view)
	}
}

// ---------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------

// TestTUIMissingRuntimeIsOperational mirrors the CLI classification.
//
// --run-id is still required — nothing can guess which run to watch — but
// --api-url is not, so its absence is an environment problem rather than an
// invocation one.
func TestTUIMissingRuntimeIsOperational(t *testing.T) {
	t.Chdir(t.TempDir())

	result := runPlatformCLI(t, "tui", "--run-id", "run-42")
	result.mustExit(t, exitOperational, "tui without a runtime")

	if result.code == exitGateFail {
		t.Fatal("the TUI produced exit 1, which means gate FAIL")
	}
	if !strings.Contains(result.stderr, "no local Trustvian runtime") {
		t.Errorf("stderr does not explain the missing runtime:\n%s", result.stderr)
	}
}

// TestTUIUsageErrors keeps invocation problems at exit 2 and off the network.
func TestTUIUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no flags", nil},
		{"missing run id", []string{"--api-url", "http://127.0.0.1:1"}},
		{"unknown flag", []string{"--api-url", "http://127.0.0.1:1", "--run-id", "r", "--nope"}},
		{"trailing argument", []string{"--api-url", "http://127.0.0.1:1", "--run-id", "r", "oops"}},
		{"relative url", []string{"--api-url", "/v1", "--run-id", "run-42"}},
		{"credentials in url", []string{"--api-url", "http://u:p@example.test", "--run-id", "r"}},
		// Flags that belong to later tasks must not exist.
		{"token flag", []string{"--api-url", "http://127.0.0.1:1", "--run-id", "r",
			"--token", "x"}},
		{"db flag", []string{"--api-url", "http://127.0.0.1:1", "--run-id", "r", "--db", "x"}},
		{"replay flag", []string{"--api-url", "http://127.0.0.1:1", "--run-id", "r",
			"--replay"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No discovery file, so --api-url genuinely has no fallback.
			t.Chdir(t.TempDir())

			result := runPlatformCLI(t, append([]string{"tui"}, tt.args...)...)
			result.mustExit(t, exitUsage, tt.name)
			if result.stdout != "" {
				t.Errorf("stdout = %q, want empty", result.stdout)
			}
			// Exit 1 belongs to eval compare alone.
			if result.code == exitGateFail {
				t.Fatal("the TUI returned exit 1, which means gate FAIL")
			}
		})
	}
}

// TestPendingBufferBoundIsEnforcedByTheModel isolates the model-side bound.
//
// Found by a surviving mutation. The end-to-end overflow test passes even with
// this check deleted, because the transport channel is bounded too and fails
// first — the outer layer masks the inner one. Driving handleFrame directly,
// with no stream behind it, is the only way to prove the model itself refuses
// to grow.
//
// The two bounds are not redundant: the channel protects the socket reader,
// this protects the model when frames arrive faster than a resync completes.
func TestPendingBufferBoundIsEnforcedByTheModel(t *testing.T) {
	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateResyncing
	model.resyncing = true

	frame := realtimeFrame{event: kindObservation, data: observationFrame("1", false)}

	// Exactly at the bound: still buffering, nothing lost.
	for range tuiPendingEventCapacity {
		_, _ = model.handleFrame(frame)
	}
	if len(model.pending) != tuiPendingEventCapacity {
		t.Fatalf("pending = %d at the bound, want %d",
			len(model.pending), tuiPendingEventCapacity)
	}
	if model.state != stateResyncing {
		t.Fatalf("state = %v at the bound; the client must still be resyncing", model.state)
	}

	// One more must abandon the stream rather than drop the frame.
	_, _ = model.handleFrame(frame)

	if model.state == stateResyncing {
		t.Fatal("the model kept resyncing past its pending bound; a frame was dropped silently")
	}
	if len(model.pending) > tuiPendingEventCapacity {
		t.Errorf("pending grew to %d, past the %d bound",
			len(model.pending), tuiPendingEventCapacity)
	}
}

// TestKnownEventStructuralValidation is the wire-integrity table.
//
// Every row here was previously accepted in silence. An observation with no
// observation payload did nothing at all; a mismatched kind was applied under
// the SSE name; an event scoped to another run would have been rendered as
// this run's. None of those is an additive change a client should absorb —
// each means the client and the server disagree about what arrived, and
// continuing would present an incomplete stream as a complete one.
func TestKnownEventStructuralValidation(t *testing.T) {
	const watched = "run-42"

	tests := []struct {
		name    string
		frame   realtimeFrame
		wantErr bool
	}{
		{
			name: "well-formed observation",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"kind":"observation","scope":{"run_id":"run-42"},
				"observation":{"sequence":"1"}}`},
		},
		{
			name: "well-formed lifecycle",
			frame: realtimeFrame{event: kindEvaluationStarted, data: `{"version":"1",
				"kind":"evaluation_started","scope":{"run_id":"run-42"},
				"evaluation":{"status":"running"}}`},
		},
		{
			name: "additive fields inside version 1 are tolerated",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"kind":"observation","scope":{"run_id":"run-42","future":"x"},
				"observation":{"sequence":"1","future_field":{"a":1}},"extra":true}`},
		},

		{
			name: "observation with no observation payload",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"kind":"observation","scope":{"run_id":"run-42"}}`},
			wantErr: true,
		},
		{
			name: "observation carrying only an evaluation",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"kind":"observation","scope":{"run_id":"run-42"},
				"evaluation":{"status":"running"}}`},
			wantErr: true,
		},
		{
			name: "lifecycle with no evaluation payload",
			frame: realtimeFrame{event: kindEvaluationCompleted, data: `{"version":"1",
				"kind":"evaluation_completed","scope":{"run_id":"run-42"}}`},
			wantErr: true,
		},
		{
			name: "lifecycle carrying only an observation",
			frame: realtimeFrame{event: kindEvaluationCompleted, data: `{"version":"1",
				"kind":"evaluation_completed","scope":{"run_id":"run-42"},
				"observation":{"sequence":"1"}}`},
			wantErr: true,
		},
		{
			name: "event name and payload kind disagree",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"kind":"evaluation_completed","scope":{"run_id":"run-42"},
				"observation":{"sequence":"1"}}`},
			wantErr: true,
		},
		{
			name: "missing kind",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"scope":{"run_id":"run-42"},"observation":{"sequence":"1"}}`},
			wantErr: true,
		},
		{
			name: "missing scope run id",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"kind":"observation","scope":{},"observation":{"sequence":"1"}}`},
			wantErr: true,
		},
		{
			name: "wrong scope run id",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"1",
				"kind":"observation","scope":{"run_id":"run-99"},
				"observation":{"sequence":"1"}}`},
			wantErr: true,
		},
		{
			name: "unsupported payload version",
			frame: realtimeFrame{event: kindObservation, data: `{"version":"2",
				"kind":"observation","scope":{"run_id":"run-42"},
				"observation":{"sequence":"1"}}`},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, _, err := classifyFrame(tt.frame, watched)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("a malformed known event was accepted (kind = %q)", kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("a well-formed event was rejected: %v", err)
			}
			if kind != tt.frame.event {
				t.Errorf("kind = %q, want %q", kind, tt.frame.event)
			}
		})
	}
}

// TestUnknownEventKindsStayForwardCompatible keeps the other side of the
// contract: an event name this build does not know is held to no shape at all.
func TestUnknownEventKindsStayForwardCompatible(t *testing.T) {
	tests := []realtimeFrame{
		{event: "future_event_type", data: `{"version":"1","kind":"future_event_type"}`},
		// Deliberately violating every structural rule above — none applies to
		// an unknown kind, because this build cannot know what shape it has.
		{event: "future_event_type", data: `{"version":"9","scope":{"run_id":"run-99"}}`},
		{event: "policy_something", data: `not even json`},
		{event: "another_future", data: ``},
	}

	for _, frame := range tests {
		t.Run(frame.event+"/"+frame.data, func(t *testing.T) {
			kind, _, err := classifyFrame(frame, "run-42")
			if err != nil {
				t.Fatalf("an unknown event kind must never fail the stream: %v", err)
			}
			if kind != "" {
				t.Errorf("kind = %q, want empty so the caller ignores it", kind)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Blocker 4: abandoning a generation cancels its work
// ---------------------------------------------------------------------

// blockingOpener records the context of every open and never returns until it
// is cancelled.
type blockingOpener struct {
	mu       sync.Mutex
	contexts []context.Context
	started  chan struct{}
}

func newBlockingOpener() *blockingOpener {
	return &blockingOpener{started: make(chan struct{}, 16)}
}

func (o *blockingOpener) open(ctx context.Context, runID string) (*realtimeStream, error) {
	o.mu.Lock()
	o.contexts = append(o.contexts, ctx)
	o.mu.Unlock()

	select {
	case o.started <- struct{}{}:
	default:
	}

	<-ctx.Done()
	return nil, operationalErrorf("open cancelled: %v", ctx.Err())
}

func (o *blockingOpener) contextAt(t *testing.T, i int) context.Context {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.contexts) <= i {
		t.Fatalf("open %d never started; %d so far", i, len(o.contexts))
	}
	return o.contexts[i]
}

func (o *blockingOpener) openCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.contexts)
}

// blockingReader blocks inside the authoritative read, recording its context.
type blockingReader struct {
	mu       sync.Mutex
	contexts []context.Context
	release  chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{release: make(chan struct{})}
}

func (r *blockingReader) run(ctx context.Context, runID string) (evaluationRunDTO, error) {
	r.mu.Lock()
	r.contexts = append(r.contexts, ctx)
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return evaluationRunDTO{}, operationalErrorf("run read cancelled: %v", ctx.Err())
	case <-r.release:
		return evaluationRunDTO{ID: runID, Status: "running"}, nil
	}
}

func (r *blockingReader) progress(ctx context.Context, runID string) (progressDTO, error) {
	select {
	case <-ctx.Done():
		return progressDTO{}, operationalErrorf("progress read cancelled: %v", ctx.Err())
	case <-r.release:
		return progressDTO{RunID: runID}, nil
	}
}

func (r *blockingReader) contextAt(t *testing.T, i int) context.Context {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.contexts) <= i {
		t.Fatalf("authoritative read %d never started; %d so far", i, len(r.contexts))
	}
	return r.contexts[i]
}

func waitCancelled(t *testing.T, ctx context.Context, what string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("%s was never cancelled; obsolete work keeps running", what)
	}
}

// TestReconnectCancelsBlockedOpen proves generation numbers are not enough.
//
// They stop a stale *result* being applied. They do not stop the work: before
// this, a blocked open kept its socket and goroutine alive until it timed out
// or the program exited, so repeated `r` accumulated connections whose answers
// were already destined to be discarded.
func TestReconnectCancelsBlockedOpen(t *testing.T) {
	opener := newBlockingOpener()
	model := newTUIModel(context.Background(), "run-42", opener, newBlockingReader())

	// Init's open blocks.
	go func() { model.Init()() }()
	select {
	case <-opener.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first open never started")
	}
	first := opener.contextAt(t, 0)

	// Manual reconnect abandons that generation.
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	waitCancelled(t, first, "the abandoned open")

	// Only now does the replacement start, under a live context of its own.
	go func() { cmd() }()
	select {
	case <-opener.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement open never started")
	}
	second := opener.contextAt(t, 1)
	if second.Err() != nil {
		t.Fatalf("the new generation's context is already cancelled: %v", second.Err())
	}

	model.endGeneration()
	waitCancelled(t, second, "the final open")
}

// TestReconnectCancelsBlockedAuthoritativeRead covers the other in-flight
// operation.
func TestReconnectCancelsBlockedAuthoritativeRead(t *testing.T) {
	reader := newBlockingReader()
	model := newTUIModel(context.Background(), "run-42", newBlockingOpener(), reader)
	model.state = stateResyncing

	// Start a resync that blocks inside the run read.
	go func() { model.resync()() }()

	deadline := time.Now().Add(5 * time.Second)
	for reader.contextCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	first := reader.contextAt(t, 0)

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	waitCancelled(t, first, "the abandoned authoritative read")

	model.endGeneration()
}

func (r *blockingReader) contextCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.contexts)
}

// TestRepeatedReconnectDoesNotAccumulateWork drives several `r` presses
// against blocking fakes and checks cancellation directly rather than timing.
func TestRepeatedReconnectDoesNotAccumulateWork(t *testing.T) {
	opener := newBlockingOpener()
	model := newTUIModel(context.Background(), "run-42", opener, newBlockingReader())

	go func() { model.Init()() }()
	select {
	case <-opener.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first open never started")
	}

	const presses = 5
	for i := range presses {
		previous := opener.contextAt(t, i)

		_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		waitCancelled(t, previous, fmt.Sprintf("open %d", i))

		go func() { cmd() }()
		select {
		case <-opener.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("replacement open %d never started", i+1)
		}
	}

	// Every generation but the current one is cancelled: at most one live.
	live := 0
	for i := range opener.openCount() {
		if opener.contextAt(t, i).Err() == nil {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("%d open contexts are still live after %d reconnects, want exactly 1",
			live, presses)
	}

	model.endGeneration()
}

// TestStaleGenerationResultCannotMutateState keeps the original guard honest:
// cancellation is additional to the generation check, not a replacement.
func TestStaleGenerationResultCannotMutateState(t *testing.T) {
	model := newTUIModel(context.Background(), "run-42", newBlockingOpener(), newBlockingReader())
	staleGeneration := model.generation

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	// A result from the abandoned generation arrives late.
	_, _ = model.Update(resyncDoneMsg{
		snapshot: authoritativeSnapshot{taken: true,
			run: evaluationRunDTO{ID: "run-42", Status: "STALE"}},
		generation: staleGeneration,
	})
	if model.snapshot.taken {
		t.Fatal("a stale generation's snapshot was applied")
	}

	_, _ = model.Update(frameMsg{generation: staleGeneration,
		frame: realtimeFrame{event: kindObservation, data: observationFrame("99", true)}})
	if len(model.live) != 0 {
		t.Fatal("a stale generation's frame was displayed")
	}

	model.endGeneration()
}
