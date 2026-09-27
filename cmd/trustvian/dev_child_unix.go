//go:build !windows

package main

// Unix child-process placement and signal delivery for `trustvian dev`.
//
// Two decisions live here, and both are about who receives Ctrl-C.

import (
	"errors"
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

// childSpec is everything needed to build the workload's command.
//
// A spec rather than a pre-built exec.Cmd, because the terminal path may have to
// start a *second* command when the first attempt fails — and an exec.Cmd cannot
// be started twice, nor copied once started. An earlier version did
// `*cmd = *retry`, which duplicates a struct holding a live Process, an internal
// context, goroutine bookkeeping and parent-side pipe descriptors. It appeared to
// work and had no right to.
type childSpec struct {
	command               []string
	env                   []string
	stdin, stdout, stderr *os.File
}

// newCmd builds a fresh, unstarted command from the spec.
func (spec childSpec) newCmd() *exec.Cmd {
	cmd := exec.Command(spec.command[0], spec.command[1:]...)
	cmd.Env = spec.env
	cmd.Dir = ""
	cmd.Stdin = spec.stdin
	cmd.Stdout = spec.stdout
	cmd.Stderr = spec.stderr
	return cmd
}

// startChild starts the workload, giving it the terminal when there is one.
//
// Returns the command that actually started, and whether the terminal was handed
// over. The second value is not a detail: it changes what a signal death means.
//
// This is the correction to a real defect. Setpgid alone puts the child in a
// *background* process group, and a background process that reads the terminal
// receives SIGTTIN and stops. So `trustvian dev -- python -i`, or any workload
// that prompts, hung forever with no output and no explanation.
//
// Foreground fixes it: it places the child's group in the foreground of the
// controlling terminal, so reads succeed. It implies Setpgid, so everything the
// group-based signal forwarding and cleanup depend on still holds.
//
// **And it moves where the terminal sends Ctrl-C.** Once the child's group is the
// foreground group, SIGINT goes to the child and not to dev — dev is in a
// background group and sees nothing. That is why the handover is reported back:
// without it the session would record a developer's Ctrl-C as "died from a signal
// nobody forwarded", which is a failed run for a workload the developer simply
// stopped watching.
//
// The terminal is not handed back explicitly when the child exits. dev's own
// writes afterwards are unaffected: SIGTTOU is only sent for terminal *control*
// operations, and for output only when the terminal has TOSTOP set, which is not
// the default. The shell reclaims the terminal when dev exits, as it does for
// every foreground job.
//
// stdin deciding this is deliberate: it is the descriptor a workload would read a
// prompt from, so it is the one whose terminal matters.
func startChild(spec childSpec) (*exec.Cmd, bool, error) {
	if spec.stdin != nil && looksLikeTerminal(spec.stdin) {
		cmd := spec.newCmd()
		attrs := childProcessAttributes(cmd)
		attrs.Foreground = true
		attrs.Ctty = int(spec.stdin.Fd())

		if err := cmd.Start(); err == nil {
			return cmd, true, nil
		}

		// The handover failed. The likely cause is that the character device was
		// not a controlling terminal after all — a /dev/null stdin looks like one
		// to a mode check and is not one, and darwin reports that as ENODEV while
		// another platform may choose something else.
		//
		// So the retry is unconditional rather than gated on an errno list. An
		// enumeration would be wrong on the first platform that picks a different
		// one, and the cost of retrying is nil: a command that genuinely cannot be
		// executed fails the second attempt too, and it is the second attempt's
		// error the caller sees — which is the accurate one, because it was made
		// without the handover that might have been to blame.
		retry := spec.newCmd()
		applyChildProcessAttributes(retry)
		if err := retry.Start(); err != nil {
			return nil, false, err
		}
		return retry, false, nil
	}

	cmd := spec.newCmd()
	applyChildProcessAttributes(cmd)
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	return cmd, false, nil
}

// looksLikeTerminal reports whether a file is a character device.
//
// A mode check rather than an ioctl, so this file needs no unsafe pointer
// arithmetic and no per-OS ioctl numbers. It is not exact — /dev/null is a
// character device and not a terminal — which is why startChild retries instead of
// trusting it.
func looksLikeTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// childProcessAttributes returns the child's SysProcAttr, creating it if needed.
func childProcessAttributes(cmd *exec.Cmd) *syscall.SysProcAttr {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	return cmd.SysProcAttr
}

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
	childProcessAttributes(cmd).Setpgid = true
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

// processAliveForBaseline reports whether a pid can still be signalled.
//
// Signal 0 performs the existence and permission checks without delivering
// anything, which is the portable way to ask. A pid owned by another user answers
// EPERM, which means it exists — so a baseline lock held by another user's run is
// respected rather than stolen.
func processAliveForBaseline(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
