package main

// Signal handling for `trustvian dev`, owned by the whole session rather than by
// the child.
//
// The first version installed the handler inside superviseChild, which is after
// the control plane and the Collector have started. A Ctrl-C during composition
// therefore killed dev with the default disposition and left two helper processes
// running — and because each is in its own process group, the terminal's own
// SIGINT never reached them. The developer got their shell back and two orphans
// holding ports.
//
// So the handler is installed before anything is started, and everything that can
// block during composition takes the context it cancels.

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// signalRelay traps SIGINT and SIGTERM for the whole session.
//
// One place decides what a signal means: cancel composition, forward to the
// workload if one is running, and remember that it happened so the session can
// tell "the developer stopped this" from "the workload failed".
type signalRelay struct {
	ctx    context.Context
	cancel context.CancelFunc

	signals chan os.Signal
	done    chan struct{}

	mu sync.Mutex
	// target is the workload, while one is running. Signals reach it only
	// through here, so there is exactly one forwarding path.
	target *os.Process
	// received is the first signal seen, or 0.
	received syscall.Signal
	// forwarded records that at least one signal was passed to a workload.
	// This is what makes Ctrl-C distinguishable from a crash.
	forwarded bool
}

// newSignalRelay installs the handler and starts relaying.
//
// Called before any process is started. Between Notify and the first thing dev
// launches there is no window where the default disposition applies.
func newSignalRelay() *signalRelay {
	ctx, cancel := context.WithCancel(context.Background())
	relay := &signalRelay{
		ctx:     ctx,
		cancel:  cancel,
		signals: make(chan os.Signal, signalQueueDepth),
		done:    make(chan struct{}),
	}
	signal.Notify(relay.signals, os.Interrupt, syscall.SIGTERM)
	go relay.run()
	return relay
}

func (r *signalRelay) run() {
	defer close(r.done)
	for received := range r.signals {
		sig, ok := received.(syscall.Signal)
		if !ok {
			continue
		}

		r.mu.Lock()
		if r.received == 0 {
			r.received = sig
		}
		target := r.target
		if target != nil {
			r.forwarded = true
		}
		r.mu.Unlock()

		// Composition stops at the first signal. Cancelling every time is
		// harmless and keeps the ordering obvious.
		r.cancel()

		// Forwarded every time, never absorbed: a developer pressing Ctrl-C
		// twice means it, and a workload that ignores the first must still see
		// the second.
		if target != nil {
			forwardSignal(target, sig)
		}
	}
}

// Context is cancelled by the first signal.
//
// Everything that can block during composition — waiting for the control plane
// to publish an endpoint, waiting for the Collector to receive, provisioning over
// HTTP — takes this, so a signal stops the wait rather than being noticed after
// it.
func (r *signalRelay) Context() context.Context { return r.ctx }

// setTarget makes the workload the forwarding destination.
func (r *signalRelay) setTarget(process *os.Process) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target = process

	// A signal that arrived before the workload started is delivered now rather
	// than dropped. Without this, a Ctrl-C in the window between the relay
	// seeing it and exec completing would leave a workload running that the
	// developer had already asked to stop.
	if r.received != 0 && process != nil {
		r.forwarded = true
		forwardSignal(process, r.received)
	}
}

// clearTarget stops forwarding, once the workload is reaped.
func (r *signalRelay) clearTarget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target = nil
}

// Received reports the first signal seen, or 0.
func (r *signalRelay) Received() syscall.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.received
}

// Forwarded reports whether a signal reached a workload.
//
// The session uses this to decide what a signal death means: a workload dev
// signalled was stopped by the developer, and a workload that died from a signal
// nobody sent it crashed.
func (r *signalRelay) Forwarded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.forwarded
}

// Interrupted reports whether any signal was seen.
func (r *signalRelay) Interrupted() bool { return r.Received() != 0 }

// Stop uninstalls the handler.
//
// Called after teardown, not before it: shutting down still has work to do, and a
// second Ctrl-C during teardown should still reach the helpers rather than
// killing dev with the default disposition and orphaning them.
func (r *signalRelay) Stop() {
	signal.Stop(r.signals)
	close(r.signals)
	<-r.done
	r.cancel()
}

// signalExitStatus is what dev exits with when a signal ended the session before
// a workload could report its own status.
//
// 128 + signal, the same encoding a shell uses and the same one a signalled
// workload produces — so a script sees one meaning for "stopped by a signal"
// whether the signal arrived during composition or during the run.
func signalExitStatus(sig syscall.Signal) int {
	return signalExitCode(sig)
}
