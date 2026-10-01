//go:build !windows

package main

// A suite member's run is finalized within its scenario's deadline (task 078
// suites): a completion request that stalls past the deadline, or past a
// suite cancellation, is cancelled, never reported as success, and the run is
// failed under a fresh, bounded context.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stallingServer holds every run completion until the request is
// cancelled, and records what it saw.
type stallingServer struct {
	server *httptest.Server

	mu                  sync.Mutex
	completionCancelled bool
	failReasons         []string
	completions         int
}

func newStallingServer(t *testing.T) *stallingServer {
	t.Helper()
	cp := &stallingServer{}
	cp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/complete"):
			cp.mu.Lock()
			cp.completions++
			cp.mu.Unlock()
			select {
			case <-r.Context().Done():
				cp.mu.Lock()
				cp.completionCancelled = true
				cp.mu.Unlock()
			case <-time.After(30 * time.Second):
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"version":"1","status":"completed"}`))
			}
		case strings.HasSuffix(r.URL.Path, "/fail"):
			body := make([]byte, 512)
			n, _ := r.Body.Read(body)
			cp.mu.Lock()
			cp.failReasons = append(cp.failReasons, string(body[:n]))
			cp.mu.Unlock()
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"version":"1","status":"failed"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(cp.server.Close)
	return cp
}

func finishUnderDeadline(t *testing.T, cp *stallingServer, deadline context.Context,
	end func()) (int, *devSession, time.Duration) {
	t.Helper()
	provision, err := newProvisioner(cp.server.URL, devIdentity{Run: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	relay := newSignalRelay()
	t.Cleanup(relay.Stop)
	session := &devSession{
		s:     streams{out: io_Discard{}, err: io_Discard{}},
		relay: relay, identity: devIdentity{Run: "run-1"},
		config:    devConfig{deadline: deadline},
		provision: provision, runStarted: true,
	}
	// End the deadline only once the completion request is in flight, so
	// the test does not depend on how long the request takes to arrive.
	go func() {
		for {
			cp.mu.Lock()
			started := cp.completions > 0
			cp.mu.Unlock()
			if started {
				end()
				return
			}
			select {
			case <-deadline.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	code := session.finish(childOutcome{code: exitDevOK})
	return code, session, time.Since(start)
}

func TestFinalizationIsCancelledWhenTheScenarioDeadlinePasses(t *testing.T) {
	for _, tc := range []struct {
		name string
		// make returns the scenario context and what ends it once the
		// completion request is in flight.
		make func() (context.Context, func())
		want error
	}{
		{"deadline", func() (context.Context, func()) {
			// The server never answers, so this deadline is what ends the
			// request, whenever the request arrives.
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			t.Cleanup(cancel)
			return ctx, func() {}
		}, context.DeadlineExceeded},
		{"suite cancellation", func() (context.Context, func()) {
			return context.WithCancel(context.Background())
		}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := newStallingServer(t)
			deadline, end := tc.make()
			code, session, took := finishUnderDeadline(t, cp, deadline, end)
			if code != exitDevOperational {
				t.Errorf("finish = %d, want %d: a completion that did not finish is not success",
					code, exitDevOperational)
			}
			if session.runCompleted {
				t.Error("the run was reported completed after the deadline")
			}
			if took > 10*time.Second {
				t.Errorf("finalization took %v; the stalled completion was not cancelled", took)
			}
			if !errors.Is(deadline.Err(), tc.want) {
				t.Errorf("scenario context ended with %v, want %v: the runner classifies on it",
					deadline.Err(), tc.want)
			}
			cp.mu.Lock()
			defer cp.mu.Unlock()
			if !cp.completionCancelled {
				t.Error("the completion request was not cancelled")
			}
			if len(cp.failReasons) != 1 || !strings.Contains(cp.failReasons[0], "deadline") {
				t.Errorf("fail requests %q; want the run failed once, for the deadline", cp.failReasons)
			}
		})
	}
}

// Within the deadline, finalization is exactly what it was.
func TestFinalizationWithinTheDeadlineCompletesTheRun(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"version":"1"}`))
	}))
	defer cp.Close()
	provision, err := newProvisioner(cp.URL, devIdentity{Run: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	relay := newSignalRelay()
	defer relay.Stop()
	deadline, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	session := &devSession{s: streams{out: io_Discard{}, err: io_Discard{}}, relay: relay,
		identity: devIdentity{Run: "run-1"}, config: devConfig{deadline: deadline},
		provision: provision, runStarted: true}
	if code := session.finish(childOutcome{code: exitDevOK}); code != exitDevOK || !session.runCompleted {
		t.Errorf("finish = %d, completed %v; want 0 and completed", code, session.runCompleted)
	}
}
