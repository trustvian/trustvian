package main

// Rendering, and the sanitizer every remote string passes through.
//
// A terminal is an execution surface. Identifiers, operation and target names,
// failure reasons and environment refs all originate outside this process and
// all reach the screen, and an ESC sequence in any of them can move the
// cursor, clear the display, retitle the window, emit a clickable OSC
// hyperlink, or repaint in a color that hides the rest of the dashboard.
//
// So: the framework may emit control sequences; data may not. Every value that
// came from the server goes through sanitizeTerminalText first. This is the
// one place the TUI is meaningfully more dangerous than the CLI, whose output
// is mostly the server's JSON going to a pipe rather than composed into a live
// screen.
//
// View has no side effects. Rendering cost is bounded by the viewport and by
// tuiObservationCapacity, both finite.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sanitizeTerminalText neutralizes control characters from untrusted text.
//
// Replaced rather than dropped, so a stripped sequence is visible as having
// been there instead of silently changing the text. Ordinary Unicode is left
// alone: this is about control, not about ASCII.
func sanitizeTerminalText(text string) string {
	if text == "" {
		return ""
	}
	// Fast path: most strings are clean, and this avoids a copy for them.
	if !needsSanitizing(text) {
		return text
	}

	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range text {
		switch {
		case r == utf8.RuneError:
			// Invalid UTF-8 already decoded to the replacement rune; keep it
			// rather than emitting raw bytes the terminal would interpret.
			builder.WriteRune('�')
		case isTerminalControl(r):
			builder.WriteRune('�')
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func needsSanitizing(text string) bool {
	for _, r := range text {
		if isTerminalControl(r) || r == utf8.RuneError {
			return true
		}
	}
	return false
}

// isTerminalControl covers C0 (including ESC and BEL), DEL, and C1.
//
// C1 matters because a terminal in 8-bit mode treats U+0080–U+009F as control
// introducers — 0x9B is CSI, which is ESC[ without the ESC.
func isTerminalControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// truncate bounds a cell's width so one long remote string cannot push the
// layout apart. Counts runes, not bytes.
func truncate(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + "…"
}

// field renders one label/value line with the value sanitized.
func field(label, value string) string {
	return fmt.Sprintf("%s: %s\n", label, sanitizeTerminalText(value))
}

// View renders the dashboard. No side effects, no network, no mutation.
func (m *tuiModel) View() string {
	if m.quitting && m.state == stateFatal {
		// The program is exiting with an operational code; the diagnostic is
		// printed by the command, not left on a torn-down screen.
		return ""
	}
	// A terminal can legitimately be reported as zero-sized during a resize or
	// in a detached session. Rendering a full table into it is pointless and
	// indexing into it is a panic, so say the minimum instead.
	if m.width < 20 || m.height < 6 {
		return "trustvian: terminal too small\n"
	}

	var out strings.Builder
	out.WriteString(m.renderHeader())
	out.WriteString("\n")
	out.WriteString(m.renderSnapshot())
	out.WriteString("\n")
	out.WriteString(m.renderObservations())
	if m.showHelp {
		out.WriteString("\n")
		out.WriteString(m.renderHelp())
	}
	return out.String()
}

func (m *tuiModel) renderHeader() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Trustvian — Evaluation %s\n", sanitizeTerminalText(m.runID))

	status := m.state.String()
	if m.state == stateReconnecting && m.lastError != "" {
		// Already sanitized when stored, and truncated so a long server
		// message cannot dominate the screen.
		status = fmt.Sprintf("%s — %s", status, truncate(m.lastError, max(m.width-20, 10)))
	}
	fmt.Fprintf(&out, "Connection: %s\n", status)
	return out.String()
}

func (m *tuiModel) renderSnapshot() string {
	if !m.snapshot.taken {
		return "Waiting for authoritative state…\n"
	}
	run, progress := m.snapshot.run, m.snapshot.progress

	var out strings.Builder
	out.WriteString(field("Candidate", run.CandidateID))
	out.WriteString(field("Environment", run.Environment))
	out.WriteString(field("Behavioral profile", run.BehavioralProfile))
	out.WriteString(field("Status", run.Status))
	if run.FailureReason != "" {
		out.WriteString(field("Failure reason", run.FailureReason))
	}
	out.WriteString("\n")

	// Labelled as a snapshot because that is what it is: these numbers move
	// only at a resync. Deriving them from the observation stream would make
	// them look continuous while being wrong after any gap.
	out.WriteString("Authoritative snapshot\n")
	out.WriteString(field("Records", progress.RecordCount))
	fmt.Fprintf(&out, "Distinct behaviors: %d\n", progress.DistinctBehaviorCount)
	out.WriteString(field("Behavior evidence", completeness(progress.BehaviorComplete)))
	out.WriteString(field("Next sequence", progress.NextIngestSequence))
	return out.String()
}

func (m *tuiModel) renderObservations() string {
	var out strings.Builder
	// "current stream", never "history": the window is cleared on reconnect
	// and holds at most tuiObservationCapacity rows. Task 067 owns history.
	out.WriteString("Live observations — current stream\n")

	if len(m.live) == 0 {
		out.WriteString("  (none yet)\n")
		return out.String()
	}

	out.WriteString("SEQ      NEW  DECISION  RISK      TRUST  ANOMALY  BEHAVIOR\n")

	// Bounded by the viewport, and the collection is finite regardless: a
	// very tall terminal cannot make this traverse more than capacity.
	rows := m.live
	visible := max(m.height-14, 1)
	if len(rows) > visible {
		rows = rows[len(rows)-visible:]
	}

	behaviorWidth := max(m.width-52, 10)
	for _, row := range rows {
		fmt.Fprintf(&out, "%-8s %-4s %-9s %-9s %-6.2f %-8.2f %s\n",
			truncate(sanitizeTerminalText(row.sequence), 8),
			newLabel(row.newBehavior),
			truncate(sanitizeTerminalText(strings.ToUpper(row.decision)), 9),
			truncate(sanitizeTerminalText(row.risk), 9),
			row.trust,
			row.anomaly,
			truncate(sanitizeTerminalText(row.behavior), behaviorWidth))
	}
	return out.String()
}

// newLabel is factual. A new behavior is not unsafe, bad, a violation or a
// regression — it is a shape this run had not produced before.
func newLabel(isNew bool) string {
	if isNew {
		return "yes"
	}
	return "no"
}

func (m *tuiModel) renderHelp() string {
	return "q quit · r reconnect and resync · ? help\n" +
		"Read-only. Rows show this stream only; the snapshot is authoritative.\n"
}
