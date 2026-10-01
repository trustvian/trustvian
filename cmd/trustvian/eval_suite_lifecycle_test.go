package main

// The suite's merge-readiness contracts: completion versus deadline is
// settled by the server; errors are byte-bounded; preflight distinguishes
// usage from operational failure; only suites are refused where deadlines
// cannot be enforced; and only suite members give up signal ownership.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// casAPI is a control plane holding scenario executions under the lifecycle
// ADR 0054 defines: completing and failing are both compare-and-swaps from
// running. Its completion can be made to race the client.
type casAPI struct {
	*fakeAPI

	mu     sync.Mutex
	state  map[string]string
	failed map[string]chan struct{}

	// commitFirst: the completion commits, then its response is held until
	// the client gives up. Otherwise the completion is held until the
	// execution has been failed, and only then attempts its swap.
	commitFirst bool
	arrived     chan struct{} // closed when the completion arrives
	settled     chan struct{} // closed when the held completion has decided
	refused     bool          // the held completion lost the swap
}

func newCASAPI(t *testing.T, commitFirst bool) *casAPI {
	t.Helper()
	api := &casAPI{fakeAPI: newFakeAPI(t), state: map[string]string{}, failed: map[string]chan struct{}{},
		commitFirst: commitFirst, arrived: make(chan struct{}), settled: make(chan struct{})}
	var arriveOnce sync.Once
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		id := strings.TrimPrefix(path, "/v1/scenario-executions/")
		id = strings.TrimSuffix(strings.TrimSuffix(id, "/complete"), "/fail")
		switch {
		case r.Method == http.MethodPost && path == "/v1/scenario-executions":
			var body struct {
				ID string `json:"id"`
			}
			got := api.captured()
			_ = json.Unmarshal(got[len(got)-1].body, &body)
			api.mu.Lock()
			api.state[body.ID] = "running"
			api.failed[body.ID] = make(chan struct{})
			api.mu.Unlock()
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"version":"1","execution":{"id":%q,"status":"running"}}`, body.ID)

		case strings.HasSuffix(path, "/complete"):
			arriveOnce.Do(func() { close(api.arrived) })
			if api.commitFirst {
				api.swap(id, "completed")
				close(api.settled)
				<-r.Context().Done() // the answer never reaches the client
				return
			}
			api.mu.Lock()
			failed := api.failed[id]
			api.mu.Unlock()
			select {
			case <-failed:
			case <-time.After(20 * time.Second):
			}
			if !api.swap(id, "completed") {
				api.mu.Lock()
				api.refused = true
				api.mu.Unlock()
				w.WriteHeader(409)
				fmt.Fprint(w, `{"version":"1","error":{"code":"conflict","message":"not running"}}`)
			} else {
				fmt.Fprintf(w, `{"version":"1","execution":{"id":%q},"comparison":%s}`, id, repeatedReply("pass"))
			}
			close(api.settled)

		case strings.HasSuffix(path, "/fail"):
			api.mu.Lock()
			current := api.state[id]
			api.mu.Unlock()
			if current == "completed" {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"version":"1","error":{"code":"conflict","message":"already completed"}}`)
				return
			}
			if api.swap(id, "failed") {
				api.mu.Lock()
				close(api.failed[id])
				api.mu.Unlock()
			}
			fmt.Fprintf(w, `{"version":"1","execution":{"id":%q,"status":"failed"}}`, id)

		case r.Method == http.MethodGet:
			api.mu.Lock()
			current := api.state[id]
			api.mu.Unlock()
			verdict := ""
			if current == "completed" {
				verdict = `,"verdict":"pass"`
			}
			fmt.Fprintf(w, `{"version":"1","execution":{"id":%q,"status":%q%s}}`, id, current, verdict)
		}
	})
	return api
}

// swap is the server's compare-and-swap from running.
func (a *casAPI) swap(id, to string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state[id] != "running" {
		return false
	}
	a.state[id] = to
	return true
}

func (a *casAPI) stateOf(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state[id]
}

// The race, both ways round, both under a deadline and under a suite
// cancellation. The member's report must agree with what the server holds,
// and a member reported timed out or cancelled is never a completed — so
// never a reusable — execution.
func TestSuiteCompletionRaceIsSettledByTheServer(t *testing.T) {
	for _, tc := range []struct {
		name        string
		commitFirst bool
		cancel      bool
		wantCode    string
		wantState   string
	}{
		{"completion commits, then the deadline passes", true, false, "completed_without_response", "completed"},
		{"the deadline passes before the completion commits", false, false, "scenario_timeout", "failed"},
		{"completion commits, then the suite is cancelled", true, true, "completed_without_response", "completed"},
		{"the suite is cancelled before the completion commits", false, true, "cancelled", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeSuite(t, map[string]string{"a.yaml": suiteScenario("alpha", 1)})
			api := newCASAPI(t, tc.commitFirst)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				go func() {
					select {
					case <-api.arrived:
						cancel()
					case <-time.After(20 * time.Second):
					}
				}()
			}
			timeout := "1s"
			if tc.cancel {
				timeout = "1m"
			}
			run := runSuite(t, &recorder{}, &scenarioAPI{fakeAPI: api.fakeAPI}, func(r *scenarioRunner) {
				r.suiteContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
			}, "--suite", dir, "--scenario-timeout", timeout)

			select {
			case <-api.settled:
			case <-time.After(20 * time.Second):
				t.Fatal("the held completion never decided")
			}
			m := run.doc.Members[0]
			if m.Error == nil || m.Error.Code != tc.wantCode || *m.ExitCode != exitOperational {
				t.Fatalf("member = %+v; want an operational %s", m, tc.wantCode)
			}
			if got := api.stateOf("scn-alpha"); got != tc.wantState {
				t.Errorf("the server holds %s, want %s", got, tc.wantState)
			}
			if (m.Error.Code == "scenario_timeout" || m.Error.Code == "cancelled") && api.stateOf("scn-alpha") == "completed" {
				t.Error("a member reported timed out or cancelled is a completed, reusable execution")
			}
			if !tc.commitFirst && !api.refused {
				t.Error("the late completion was not refused")
			}
			if run.code != exitOperational {
				t.Errorf("exit %d, want 3", run.code)
			}
		})
	}
}

// error.message is at most 1024 bytes of valid UTF-8, the marker included.
func TestMemberErrorMessagesAreByteBounded(t *testing.T) {
	const limit = maxMemberErrorBytes
	euro := "€" // three bytes
	for _, tc := range []struct {
		name, in  string
		unchanged bool
	}{
		{"short ASCII", "repetition 2 failed", true},
		{"exactly the limit in ASCII", strings.Repeat("a", limit), true},
		{"one past the limit in ASCII", strings.Repeat("a", limit+1), false},
		{"far past the limit in ASCII", strings.Repeat("a", 10*limit), false},
		{"multibyte ending exactly at the limit", strings.Repeat("a", limit-3) + euro, true},
		{"multibyte crossing the limit", strings.Repeat("a", limit-1) + euro, false},
		{"multibyte crossing the marker's room", strings.Repeat("a", limit-4) + euro + euro, false},
		{"very long multibyte", strings.Repeat("日本語", 2000), false},
		// A run of invalid bytes becomes one replacement character: valid,
		// bounded, and not shortened by truncation.
		{"invalid bytes", strings.Repeat("\xff", limit), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := boundText(tc.in, limit)
			if !utf8.ValidString(got) {
				t.Errorf("result is not valid UTF-8")
			}
			if len([]byte(got)) > limit {
				t.Errorf("result is %d bytes, over %d", len(got), limit)
			}
			if tc.unchanged && got != tc.in && utf8.ValidString(tc.in) {
				t.Errorf("a message within the bound was changed")
			}
			if !tc.unchanged && (!strings.HasSuffix(got, truncationMarker) || len(got) < limit-3) {
				t.Errorf("a shortened message does not say so")
			}
		})
	}
	for _, small := range []int{0, 1, 2, 3, 4} {
		if got := boundText(strings.Repeat("日", 10), small); len(got) > small || !utf8.ValidString(got) {
			t.Errorf("limit %d: %q", small, got)
		}
	}
}

// Preflight distinguishes the caller's mistake (2) from an environment that
// cannot be read (3); neither starts a workload or makes a request.
func TestSuitePreflightSeparatesUsageFromOperationalFailure(t *testing.T) {
	dir := writeSuite(t, map[string]string{"a.yaml": suiteScenario("alpha", 1)})
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"operational": {operationalErrorf("cannot determine the working directory"), exitOperational},
		"usage":       {usageErrorf("no agent identity"), exitUsage},
	} {
		t.Run(name, func(t *testing.T) {
			api := newScenarioAPI(t, "pass")
			rec := &recorder{}
			run := runSuite(t, rec, api, func(r *scenarioRunner) {
				r.scope = func(devConfig) (executionScope, error) { return executionScope{}, tc.err }
			}, "--suite", dir, "--scenario-timeout", "1m")
			if run.code != tc.want || len(rec.calls) != 0 || len(api.captured()) != 0 {
				t.Errorf("exit %d after %d workloads and %d requests; want %d, 0, 0",
					run.code, len(rec.calls), len(api.captured()), tc.want)
			}
		})
	}
}

// Where deadlines cannot be enforced, a suite is refused before anything;
// single-scenario mode keeps its earlier behavior and runs.
func TestOnlySuitesAreRefusedOnAnUnsupportedPlatform(t *testing.T) {
	unsupported := func(r *scenarioRunner) {
		r.platformSupported = func() error { return errors.New("not supported on Windows") }
	}
	dir := writeSuite(t, map[string]string{"a.yaml": suiteScenario("alpha", 1)})
	api := newScenarioAPI(t, "pass")
	rec := &recorder{}
	run := runSuite(t, rec, api, unsupported, "--suite", dir, "--scenario-timeout", "1m")
	if run.code != exitUsage || len(rec.calls) != 0 || len(api.captured()) != 0 {
		t.Errorf("suite: exit %d after %d workloads and %d requests; want 2, 0, 0",
			run.code, len(rec.calls), len(api.captured()))
	}

	single := &recorder{}
	singleAPI := newScenarioAPI(t, "pass")
	var out, errOut strings.Builder
	runner := scenarioRunner{run: single.run,
		scope:       func(devConfig) (executionScope, error) { return testScope, nil },
		executionID: func(string) string { return "scn-test" }, cliVersion: func() string { return "cli-test" },
		workloadStdout: nil}
	unsupported(&runner)
	code := runner.main(streams{out: &out, err: &errOut},
		[]string{"--api-url", singleAPI.url(), "--scenario", writeScenario(t, runScenarioYAML)}, testTimeout)
	if code != exitOK || len(single.calls) != 6 {
		t.Errorf("single scenario: exit %d after %d repetitions; want 0 and 6 — unchanged\n%s",
			code, len(single.calls), errOut.String())
	}
}

// Only suite members give up signal ownership and the terminal.
func TestOnlySuiteMembersYieldSignalsAndTheTerminal(t *testing.T) {
	dir := writeSuite(t, map[string]string{"a.yaml": suiteScenario("alpha", 1)})
	rec := &recorder{}
	runSuite(t, rec, newScenarioAPI(t, "pass"), nil, "--suite", dir, "--scenario-timeout", "1m")
	single := &recorder{}
	runScenario(t, single, newScenarioAPI(t, "pass").url(), "--scenario", writeScenario(t, runScenarioYAML))
	for _, c := range rec.calls {
		if !c.config.callerOwnsSignals || !c.config.detachStdin {
			t.Errorf("suite repetition %s keeps signals or the terminal", c.config.runID)
		}
	}
	for _, c := range single.calls {
		if c.config.callerOwnsSignals || c.config.detachStdin {
			t.Errorf("single-scenario repetition %s gave up signals or the terminal", c.config.runID)
		}
	}
}
