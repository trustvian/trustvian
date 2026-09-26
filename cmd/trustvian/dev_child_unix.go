//go:build !windows

package main

// Unix child-process placement and signal delivery for `trustvian dev`.
//
// Two decisions live here, and both are about who receives Ctrl-C.

import (
	"os"
	"os/exec"
	"syscall"
)

// devPlatformSupported reports nil on Unix.
//
// Everything the child-process discipline promises — forwarding SIGINT and
// SIGTERM, outliving the child, leaving nothing orphaned — is implementable
// here, so there is nothing to refuse.
func devPlatformSupported() error { return nil }

// applyChildProcessAttributes puts the child in its own process group.
//
// Setpgid is the load-bearing choice, and it is counter-intuitive: it *stops*
// the terminal delivering Ctrl-C to the child.
//
// That is the point. In the same group as this process, a Ctrl-C reaches the
// child directly *and* reaches this process, which then forwards a second one —
// so the child sees the signal twice, at a timing neither side controls. Worse,
// the child can die before this process has installed its own handler, and the
// exit status a script reads then depends on a race.
//
// In its own group, exactly one path exists: the terminal signals this process,
// and this process forwards deliberately. That is what makes the behavior
// testable rather than incidental.
//
// The cost is that forwarding is now mandatory. A signal this process fails to
// pass on is a signal the child never sees, which is why the supervisor traps
// signals before Start rather than after it.
func applyChildProcessAttributes(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// forwardSignal delivers one signal to the child's whole process group.
//
// To the group, not the process: a workload that spawned its own children —
// `sh -c`, a language runtime, a test runner — has descendants that must stop
// too, and group delivery is how a shell already does this. The spec's
// "never beyond normal group semantics" is exactly this: the group this process
// created for the child, and nothing else.
//
// Errors are ignored deliberately. The only ones reachable here are "the group
// no longer exists", which means the child has already exited and the reaper is
// about to report its status, and "not permitted", which a process cannot
// encounter against a group it created. Reporting either would print a
// diagnostic at the exact moment a developer pressed Ctrl-C expecting silence.
func forwardSignal(process *os.Process, received os.Signal) {
	if process == nil {
		return
	}
	sig, ok := received.(syscall.Signal)
	if !ok {
		// Not a signal that can be delivered numerically. Fall back to the
		// process alone rather than dropping it.
		_ = process.Signal(received)
		return
	}
	// A negative pid addresses the process group, as kill(2) defines it.
	if err := syscall.Kill(-process.Pid, sig); err != nil {
		// The group is gone or was never created; the process itself may still
		// be reachable.
		_ = process.Signal(received)
	}
}
