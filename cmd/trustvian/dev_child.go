package main

// Child process supervision for `trustvian dev`.
//
// The portable half. Process-group placement and signal delivery differ enough
// between platforms that they live in dev_child_unix.go and
// dev_child_windows.go; everything about *order* is here, because the order is
// the contract:
//
//	launch -> forward signals for as long as it runs -> reap -> exit with its status
//
// The runtime shuts down after the child, never before it. That is what makes
// Ctrl-C return the shell with the child's status intact rather than with this
// process's.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// superviseChild runs one command to completion and returns its exit code.
//
// environment is the workload's, already assembled: the inherited environment
// plus whatever dev added. A nil environment means "inherit exactly", which is
// what the wrapper tests use to assert that nothing is added when nothing is
// composed.
//
// The working directory and streams are inherited unconditionally and are not
// parameters: dev has no reason to change either, and a knob that could would
// be a knob that eventually does.
func superviseChild(s streams, command []string, environment *devEnvironment) int {
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
	// Platform-specific: on Unix this puts the child in its own process group,
	// so the terminal's Ctrl-C reaches this process and is forwarded
	// deliberately rather than racing a direct delivery to the child.
	applyChildProcessAttributes(cmd)

	// Signals are trapped *before* the child exists. Between Start and the
	// first Notify a Ctrl-C would otherwise kill this process and leave the
	// child running, which is the orphan this whole function exists to prevent.
	signals := make(chan os.Signal, signalQueueDepth)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: cannot run %s: %v\n",
			devCommandName(command), err)
		return exitDevOperational
	}

	// Reaping happens in a goroutine so the signal loop can keep forwarding
	// while the child is still running. Buffered, so the goroutine never
	// blocks if the loop has already returned.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	for {
		select {
		case received := <-signals:
			// Forwarded, never absorbed, and forwarded *every* time: a
			// developer pressing Ctrl-C twice means it, and a workload that
			// ignores the first signal must still see the second.
			//
			// No escalation to SIGKILL after a timeout. Killing the child would
			// make the exit status this wrapper's invention rather than the
			// child's, and the child's status is the one thing a script reads.
			forwardSignal(cmd.Process, received)
		case err := <-waited:
			return childExitCode(s, command, err)
		}
	}
}

// signalQueueDepth buffers signals so none is dropped while one is forwarded.
//
// signal.Notify drops a signal when the channel is not ready, and an unbuffered
// channel is not ready for the fraction of a second the loop spends in
// forwardSignal. Small on purpose: this is a jitter buffer, not a queue, and a
// developer holding Ctrl-C does not need every repeat delivered.
const signalQueueDepth = 4

// childExitCode turns a Wait result into the code this process exits with.
//
// Three cases, and the third is the one wrappers get wrong.
func childExitCode(s streams, command []string, err error) int {
	if err == nil {
		return exitDevOK
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// Not the child reporting a status — this process failed to wait for
		// it. Operational, and distinct from any code the child could have
		// produced, because no status was observed at all.
		fmt.Fprintf(s.err, "trustvian dev: waiting for %s: %v\n",
			devCommandName(command), err)
		return exitDevOperational
	}

	// A signal death has no exit code of its own: ExitCode() reports -1.
	// Reported as 128+signal, which is the convention every shell uses and
	// therefore the only encoding a script can interpret. Silently returning
	// -1, or collapsing it to 1, would make "the workload was killed"
	// indistinguishable from "the workload failed".
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return signalExitCode(status.Signal())
	}

	code := exitErr.ExitCode()
	if code < 0 {
		// Defensive: a platform that reports neither a code nor a signal.
		// Operational rather than a fabricated status.
		fmt.Fprintf(s.err,
			"trustvian dev: %s terminated without a reportable status\n",
			devCommandName(command))
		return exitDevOperational
	}
	return code
}

// signalExitCode encodes a signal death the way a shell does.
//
// 128 + signal number. Exit codes are a byte, so a signal above 127 would wrap;
// no such signal exists on the platforms this supports, and the clamp is here
// so a future one cannot silently alias a real exit code.
func signalExitCode(sig syscall.Signal) int {
	n := int(sig)
	if n < 0 || n > 127 {
		return exitDevOperational
	}
	return 128 + n
}
