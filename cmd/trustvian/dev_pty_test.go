//go:build linux || darwin

package main

// A pseudo-terminal, with no dependency outside the standard library.
//
// Needed because the defect this tests for is invisible without a real terminal:
// with Setpgid alone the child lands in a background process group, and a
// background process that reads the terminal is stopped by SIGTTIN. A pipe cannot
// reproduce that — only a tty can.
//
// golang.org/x/sys and github.com/creack/pty would each make this three lines, and
// neither may be added: the root module takes on no new dependency. So the two
// ioctls that open a pty are written out per platform, in openPTY below. It is
// about thirty lines and it is the whole reason the test can exist.

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestChildWithATerminalIsPlacedInTheForeground(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	// The workload reads from the terminal. In a background process group this
	// read raises SIGTTIN, the process stops, and dev waits forever — which is
	// exactly what `trustvian dev -- python -i` did before this.
	script := writeTerminalScript(t, `
read line
printf 'read:%s\n' "$line"
`)

	withDevStdio(t, slave, slave, slave)

	done := make(chan childOutcome, 1)
	go func() {
		done <- superviseChild(streams{out: io_Discard{}, err: io_Discard{}},
			[]string{"sh", script}, nil, nil)
	}()

	// Give the child a moment to reach its read, then answer it.
	time.Sleep(300 * time.Millisecond)
	if _, err := master.WriteString("hello\n"); err != nil {
		t.Fatalf("writing to the terminal: %v", err)
	}

	select {
	case outcome := <-done:
		if outcome.code != 0 {
			t.Fatalf("exit code = %d, want 0; the child could not read the terminal",
				outcome.code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the child never finished: a terminal read was stopped by SIGTTIN, " +
			"which is what the foreground handover exists to prevent")
	}
}

func TestChildWithoutATerminalKeepsItsOwnGroup(t *testing.T) {
	// The other half of the contract: with no terminal there is nothing to hand
	// over, and the child must still get its own process group so that signal
	// forwarding and group cleanup keep working.
	out := runDevCapturingStdout(t, []string{"--", "sh", "-c", "ps -o pgid= -p $$"})

	childPGID := strings.TrimSpace(out)
	if childPGID == "" {
		t.Fatalf("unexpected child output %q", out)
	}
	if childPGID == strings.TrimSpace(currentProcessGroup(t)) {
		t.Fatalf("the child shares this process's group (%s); Setpgid was not applied",
			childPGID)
	}
}

func TestLooksLikeTerminalDistinguishesAPipe(t *testing.T) {
	_, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	defer slave.Close()

	if !looksLikeTerminal(slave) {
		t.Error("a pty slave was not recognized as a character device")
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating a pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()
	if looksLikeTerminal(reader) {
		t.Error("a pipe was mistaken for a terminal")
	}
}

func TestANonTerminalCharacterDeviceStillRuns(t *testing.T) {
	// /dev/null is a character device and is not a controlling terminal, so the
	// mode check says yes and the handover fails. The retry is what keeps the
	// workload running instead of refusing to start — asserted here because the
	// mode check is deliberately inexact.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	stdout := newTempFile(t, dir, "stdout")
	withDevStdio(t, devNull, stdout, newTempFile(t, dir, "stderr"))

	outcome := superviseChild(streams{out: io_Discard{}, err: io_Discard{}},
		[]string{"sh", "-c", "echo ran"}, nil, nil)

	if outcome.startFailed {
		t.Fatal("a /dev/null stdin prevented the workload from starting")
	}
	if outcome.code != 0 {
		t.Fatalf("exit code = %d, want 0", outcome.code)
	}
	if got := strings.TrimSpace(readFile(t, stdout)); got != "ran" {
		t.Errorf("child stdout = %q, want %q", got, "ran")
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func writeTerminalScript(t *testing.T, body string) string {
	t.Helper()
	path := t.TempDir() + "/terminal.sh"
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("writing the script: %v", err)
	}
	return path
}

func currentProcessGroup(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("ps", "-o", "pgid=", "-p",
		itoa(syscall.Getpid())).Output()
	if err != nil {
		t.Fatalf("reading this process's group: %v", err)
	}
	return string(output)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
