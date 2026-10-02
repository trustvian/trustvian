//go:build !windows

package main

// A deadline ends a workload (task 078 suites): SIGTERM to its process group,
// SIGKILL after the grace, the leader reaped and nothing left of its group —
// and the session never reads the result as success, even when the workload
// traps the termination and exits 0.

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// superviseUntilDeadline runs script under a relay whose deadline is cancelled
// once ready exists, and returns the outcome, the relay and how long the
// supervision took after the deadline.
func superviseUntilDeadline(t *testing.T, dir, ready string, grace time.Duration, args ...string,
) (childOutcome, *signalRelay, time.Duration) {
	t.Helper()
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))
	relay := newSignalRelay()
	t.Cleanup(relay.Stop)
	deadline, expire := context.WithCancel(context.Background())
	defer expire()
	stopWatching := relay.watchDeadline(deadline, grace)
	defer stopWatching()

	done := make(chan childOutcome, 1)
	go func() {
		done <- superviseChild(streams{out: io_Discard{}, err: io_Discard{}}, args, nil, relay)
	}()
	waitForFile(t, ready)
	expired := time.Now()
	expire()
	select {
	case outcome := <-done:
		return outcome, relay, time.Since(expired)
	case <-time.After(30 * time.Second):
		t.Fatal("the workload outlived its deadline")
		return childOutcome{}, nil, 0
	}
}

// requireDead polls, bounded, until pid no longer exists.
func requireDead(t *testing.T, pid int, what string) {
	t.Helper()
	limit := time.Now().Add(10 * time.Second)
	for time.Now().Before(limit) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("%s %d survived the deadline", what, pid)
}

// A hung workload with a descendant that ignores SIGTERM: the leader stops on
// SIGTERM, and the descendant is killed with the group rather than surviving
// the scenario.
func TestADeadlineEndsAHungWorkloadAndItsDescendants(t *testing.T) {
	requireUnix(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "hang.sh")
	writeScript(t, script, `
sh -c 'trap "" TERM; printf $$ > "$1"; while :; do sleep 0.05; done' _ "$1" &
while [ ! -s "$1" ]; do sleep 0.01; done
printf ready > "$2"
while :; do sleep 0.05; done
`)
	pidFile, ready := filepath.Join(dir, "descendant.pid"), filepath.Join(dir, "ready")

	outcome, relay, _ := superviseUntilDeadline(t, dir, ready, 300*time.Millisecond,
		"sh", script, pidFile, ready)
	if !relay.Expired() {
		t.Fatal("the relay does not report the deadline")
	}
	if outcome.code == exitDevOK {
		t.Errorf("a workload ended by its deadline reported exit 0")
	}
	requireDead(t, readPID(t, pidFile), "a descendant ignoring SIGTERM")
}

// A workload that traps SIGTERM and exits 0 has not succeeded: the session
// fails the run and reports an operational status.
func TestADeadlineIsNotSuccessWhenTheWorkloadTrapsAndExitsZero(t *testing.T) {
	requireUnix(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "trap.sh")
	writeScript(t, script, `
trap 'exit 0' TERM
printf ready > "$1"
while :; do sleep 0.05; done
`)
	ready := filepath.Join(dir, "ready")
	outcome, relay, _ := superviseUntilDeadline(t, dir, ready, 5*time.Second, "sh", script, ready)
	if outcome.code != exitDevOK {
		t.Fatalf("fixture: the trapping workload exited %d, want 0", outcome.code)
	}
	session := &devSession{s: streams{out: io_Discard{}, err: io_Discard{}}, relay: relay}
	if code := session.finish(outcome); code != exitDevOperational {
		t.Errorf("finish after a deadline = %d, want %d: a trapped exit 0 is not success",
			code, exitDevOperational)
	}
}

// A workload that ignores SIGTERM entirely is killed after the grace, not
// left running.
func TestADeadlineEscalatesToSIGKILLAfterTheGrace(t *testing.T) {
	requireUnix(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "stubborn.sh")
	writeScript(t, script, `
trap '' TERM
printf ready > "$1"
while :; do sleep 0.05; done
`)
	ready := filepath.Join(dir, "ready")
	grace := 200 * time.Millisecond
	outcome, relay, took := superviseUntilDeadline(t, dir, ready, grace, "sh", script, ready)
	if !relay.Expired() || !outcome.signaled || outcome.signal != syscall.SIGKILL {
		t.Errorf("outcome %+v; want the workload killed by SIGKILL", outcome)
	}
	if took < grace {
		t.Errorf("killed after %v, before the %v grace", took, grace)
	}
}

// No deadline, no watcher: `trustvian dev`'s own case is untouched.
func TestNoDeadlineWatchesNothing(t *testing.T) {
	relay := newSignalRelay()
	defer relay.Stop()
	relay.watchDeadline(nil, time.Second)()
	if relay.Expired() || relay.Context().Err() != nil {
		t.Error("a nil deadline expired the session")
	}
}

// Once the leader is reaped, no SIGKILL escalation remains scheduled against
// its process-group id, which the system is then free to reuse for an
// unrelated process — the next suite member's, for instance.
func TestADeadlineLeavesNoEscalationPendingAfterTheReap(t *testing.T) {
	requireUnix(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "trap.sh")
	writeScript(t, script, `
trap 'exit 0' TERM
printf ready > "$1"
while :; do sleep 0.05; done
`)
	ready := filepath.Join(dir, "ready")
	_, relay, _ := superviseUntilDeadline(t, dir, ready, time.Hour, "sh", script, ready)
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.escalation != nil || relay.target != nil {
		t.Errorf("after the reap: escalation %v, target %v; want neither", relay.escalation, relay.target)
	}
}
