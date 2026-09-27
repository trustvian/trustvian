package main

// Child process supervision for `trustvian dev`.
//
// The portable half. Process-group placement, terminal handover and signal
// delivery differ enough between platforms that they live in dev_child_unix.go
// and dev_child_windows.go; what is here is the order, because the order is the
// contract:
//
//	launch -> reap -> report what happened to it
//
// Signals are not installed here. They belong to the whole session — a Ctrl-C
// during composition has to stop the control plane and the Collector too — so
// dev_signals.go owns the handler and this function only registers the child as
// its forwarding target.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// childOutcome is what happened to the workload.
//
// A struct rather than an int, because three different callers need three
// different facts from it: the exit status a script reads, whether a signal ended
// it, and whether the command ever started. Collapsing them into one code is what
// made "exited 3" the reason recorded for a workload that never ran.
type childOutcome struct {
	// code is the status dev exits with.
	code int

	// signaled reports that the workload died from a signal rather than
	// returning a status.
	signaled bool

	// startFailed reports that the command could not be executed at all — not
	// found, not executable, a bad interpreter. No workload ran, so there is no
	// workload status, and the run's failure reason must not claim one.
	startFailed bool
}

// ok reports a clean exit.
func (o childOutcome) ok() bool { return o.code == exitDevOK && !o.startFailed }

// superviseChild runs one command to completion and reports what happened.
//
// environment is the workload's, already assembled: the inherited environment
// plus whatever dev added. A nil environment means "inherit exactly", which is
// what the wrapper tests use to assert that nothing is added when nothing is
// composed.
//
// The working directory and streams are inherited unconditionally and are not
// parameters: dev has no reason to change either, and a knob that could would be
// a knob that eventually does.
func superviseChild(s streams, command []string, environment *devEnvironment,
	relay *signalRelay) childOutcome {
	stdin, stdout, stderr := devStdio()

	cmd := exec.Command(command[0], command[1:]...)
	// Extended, never replaced. Explicit rather than relying on exec's defaults
	// so that adding a variable cannot accidentally construct a fresh
	// environment instead of extending the developer's.
	if environment != nil {
		cmd.Env = environment.Environ()
	} else {
		cmd.Env = os.Environ()
	}
	cmd.Dir = ""
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := startWithTerminalHandover(cmd, stdin); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: cannot run %s: %v\n",
			devCommandName(command), err)
		return childOutcome{code: exitDevOperational, startFailed: true}
	}

	// Registered after Start, so the relay has a real process to signal — and
	// setTarget delivers a signal that arrived during exec rather than dropping
	// it.
	if relay != nil {
		relay.setTarget(cmd.Process)
		defer relay.clearTarget()
	}

	return childExitOutcome(s, command, cmd.Wait())
}

// signalQueueDepth buffers signals so none is dropped while one is forwarded.
//
// signal.Notify drops a signal when the channel is not ready, and an unbuffered
// channel is not ready for the fraction of a second the relay spends forwarding.
// Small on purpose: this is a jitter buffer, not a queue, and a developer holding
// Ctrl-C does not need every repeat delivered.
const signalQueueDepth = 4

// childExitOutcome turns a Wait result into what dev reports.
//
// Three cases, and the third is the one wrappers get wrong.
func childExitOutcome(s streams, command []string, err error) childOutcome {
	if err == nil {
		return childOutcome{code: exitDevOK}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// Not the workload reporting a status — this process failed to wait for
		// it. Operational, and distinct from any code the workload could have
		// produced, because no status was observed at all.
		fmt.Fprintf(s.err, "trustvian dev: waiting for %s: %v\n",
			devCommandName(command), err)
		return childOutcome{code: exitDevOperational}
	}

	// A signal death has no exit code of its own: ExitCode() reports -1.
	// Reported as 128+signal, which is the convention every shell uses and
	// therefore the only encoding a script can interpret. Silently returning -1,
	// or collapsing it to 1, would make "the workload was killed"
	// indistinguishable from "the workload failed".
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return childOutcome{code: signalExitCode(status.Signal()), signaled: true}
	}

	code := exitErr.ExitCode()
	if code < 0 {
		// Defensive: a platform that reports neither a code nor a signal.
		// Operational rather than a fabricated status.
		fmt.Fprintf(s.err,
			"trustvian dev: %s terminated without a reportable status\n",
			devCommandName(command))
		return childOutcome{code: exitDevOperational}
	}
	return childOutcome{code: code}
}

// signalExitCode encodes a signal death the way a shell does.
//
// 128 + signal number. Exit codes are a byte, so a signal above 127 would wrap;
// no such signal exists on the platforms this supports, and the clamp is here so
// a future one cannot silently alias a real exit code.
func signalExitCode(sig syscall.Signal) int {
	n := int(sig)
	if n < 0 || n > 127 {
		return exitDevOperational
	}
	return 128 + n
}
