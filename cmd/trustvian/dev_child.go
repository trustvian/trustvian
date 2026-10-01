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

	// signal is the signal that killed it, when signaled.
	signal syscall.Signal

	// terminalHandover reports that the workload's process group was the
	// terminal's foreground group.
	//
	// This decides what a signal death means. With the handover in effect the
	// terminal delivers Ctrl-C to the *workload's* group and dev — now in a
	// background group — never sees it. So a developer's interrupt arrives as a
	// SIGINT death with nothing forwarded, which without this flag is
	// indistinguishable from the workload being killed by something else.
	terminalHandover bool

	// startFailed reports that the command could not be executed at all — not
	// found, not executable, a bad interpreter. No workload ran, so there is no
	// workload status, and the run's failure reason must not claim one.
	startFailed bool
}

// ok reports a clean exit.
func (o childOutcome) ok() bool { return o.code == exitDevOK && !o.startFailed }

// stoppedByDeveloper reports that the workload ended because the developer said
// so, rather than because anything went wrong.
//
// Two routes reach this, and both are the same event seen from different sides.
//
// Without the terminal handover, dev is in the foreground group: the terminal
// signals dev, dev forwards, and forwarded is the record of it.
//
// With the handover, the workload's group is the foreground group: the terminal
// signals the workload directly and dev never sees it. So the evidence is the
// handover plus the signal itself — SIGINT is Ctrl-C and SIGHUP is the terminal
// going away, and neither is a workload failing.
//
// SIGQUIT is deliberately not in that set even though Ctrl-\ produces it. Its
// convention is "stop and dump state because something is wrong", which is a
// different statement from "I have seen enough".
func (o childOutcome) stoppedByDeveloper(forwarded bool) bool {
	if !o.signaled {
		return false
	}
	if forwarded {
		return true
	}
	if !o.terminalHandover {
		return false
	}
	return o.signal == syscall.SIGINT || o.signal == syscall.SIGHUP
}

// superviseChild runs one command to completion and reports what happened.
//
// environment is the workload's, already assembled: the inherited environment
// plus whatever dev added. A nil environment means "inherit exactly", which is
// what the wrapper tests use to assert that nothing is added when nothing is
// composed.
//
// The working directory and streams are inherited: dev has no reason to change
// either, and a knob that could would be a knob that eventually does. The one
// exception is superviseChildTo's stdout, which `trustvian dev` never sets.
func superviseChild(s streams, command []string, environment *devEnvironment,
	relay *signalRelay) childOutcome {
	return superviseChildTo(s, command, environment, relay, nil)
}

// superviseChildTo is superviseChild with the workload's standard output sent
// to stdout instead of dev's own; nil inherits dev's, which is what `trustvian
// dev` always does.
//
// The one caller that passes a file is `eval run` (task 078). Its stdout
// carries exactly one result document, and the workload's lines must not reach
// it — redirecting the in-process streams does not redirect a child, which
// inherits descriptors, not writers. Still a file, never a pipe, for the reason
// devStdio gives; stdin and stderr, and with them the terminal handover and
// signal delivery, are unchanged.
func superviseChildTo(s streams, command []string, environment *devEnvironment,
	relay *signalRelay, stdoutOverride *os.File) childOutcome {
	return superviseChildWith(s, command, environment, relay, childStreams{stdout: stdoutOverride})
}

// childStreams overrides the workload's inherited streams for one invocation.
type childStreams struct {
	// stdout, when set, replaces dev's own standard output.
	stdout *os.File
	// detachStdin gives the workload no standard input — /dev/null, as exec
	// does for a nil reader — and therefore never the terminal.
	detachStdin bool
}

// superviseChildWith is superviseChild with per-invocation stream overrides.
func superviseChildWith(s streams, command []string, environment *devEnvironment,
	relay *signalRelay, overrides childStreams) childOutcome {
	stdin, stdout, stderr := devStdio()
	if overrides.stdout != nil {
		stdout = overrides.stdout
	}
	if overrides.detachStdin {
		stdin = nil
	}

	// Extended, never replaced. Explicit rather than relying on exec's defaults
	// so that adding a variable cannot accidentally construct a fresh
	// environment instead of extending the developer's.
	env := os.Environ()
	if environment != nil {
		env = environment.Environ()
	}

	cmd, handover, err := startChild(childSpec{
		command: command,
		env:     env,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
	})
	if err != nil {
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

	waitErr := cmd.Wait()
	if relay != nil {
		// Cleared at once, not only by the deferred call: that stops a
		// pending deadline escalation before anything below can race it.
		relay.clearTarget()
		if relay.Expired() {
			// The deadline ended this workload. Its leader is reaped;
			// anything left in its group — a descendant that ignored SIGTERM,
			// or one that outlived a leader that trapped it — is killed now
			// rather than outliving the scenario.
			killProcessGroup(cmd.Process)
		}
	}
	outcome := childExitOutcome(s, command, waitErr)
	outcome.terminalHandover = handover
	return outcome
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
		return childOutcome{
			code:     signalExitCode(status.Signal()),
			signaled: true,
			signal:   status.Signal(),
		}
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
