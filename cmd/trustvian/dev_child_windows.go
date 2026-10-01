//go:build windows

package main

// Windows support for `trustvian dev`: refused, with a reason.
//
// The release matrix includes windows/amd64, so this command compiles here and
// has to behave predictably. It cannot behave *correctly*, and the honest
// response to that is a refusal rather than a wrapper that works until it
// matters.

import (
	"errors"
	"os"
	"os/exec"
)

// devPlatformSupported refuses on Windows, naming what cannot be delivered.
//
// Three facts, each independently fatal to the child-process discipline task
// 077 specifies:
//
//   - os.Process.Signal(os.Interrupt) is not implemented on Windows; the
//     runtime returns an error rather than delivering anything.
//   - SIGTERM cannot be delivered to another process at all. Stopping a child
//     means TerminateProcess, which is SIGKILL's semantics — no chance for the
//     workload to flush the telemetry this command exists to collect.
//   - There is no Setpgid. Orderly shutdown of a child's own descendants needs
//     a Job Object and GenerateConsoleCtrlEvent, which is a different process
//     model rather than a porting detail.
//
// Every line of the spec's discipline table would be partially true, and the
// spec says never a silent partial. So the command refuses before it starts
// anything, which is the one outcome that leaves nothing orphaned.
//
// Supporting Windows properly is its own task with its own process-model
// design. Until then WSL2 is the documented path, and the parts can still be
// run by hand.
func devPlatformSupported() error {
	return errors.New(
		"not supported on Windows.\n\n" +
			"dev supervises a child process and forwards SIGINT and SIGTERM to it, " +
			"and\nWindows has no equivalent delivery: a wrapper that could not stop " +
			"what it\nstarted would leave orphaned processes holding your ports.\n\n" +
			"Use WSL2, or run the parts separately — see\n" +
			"docs/local-development.md § Running the parts separately")
}

// applyChildProcessAttributes is unreachable on Windows.
//
// Present so the portable supervisor compiles for this platform. Nothing calls
// it: runDev refuses before a child is created, and a test asserts that.
func applyChildProcessAttributes(*exec.Cmd) {}

// forwardSignal is unreachable on Windows, for the same reason.
func forwardSignal(*os.Process, os.Signal) {}

// killProcessGroup is unreachable on Windows, for the same reason.
func killProcessGroup(*os.Process) {}

// childSpec mirrors the Unix shape so the portable supervisor compiles here.
type childSpec struct {
	command               []string
	env                   []string
	stdin, stdout, stderr *os.File
}

// startChild is unreachable on Windows: runDev refuses before a child is created.
// Present so the package compiles for this platform.
func startChild(spec childSpec) (*exec.Cmd, bool, error) {
	cmd := exec.Command(spec.command[0], spec.command[1:]...)
	cmd.Env = spec.env
	cmd.Stdout, cmd.Stderr = spec.stdout, spec.stderr
	if spec.stdin != nil {
		cmd.Stdin = spec.stdin
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	return cmd, false, nil
}

// processAliveForBaseline is unreachable on Windows: dev refuses before it would
// claim a baseline. Present so the package compiles for this platform.
func processAliveForBaseline(int) bool { return false }
