package status

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func TestNilTrackerRecordsNothing(t *testing.T) {
	var tracker *Tracker
	tracker.ObserveScope("svc", SDK{}, Scope{}, 3, t0)
	producers, truncated := tracker.Producers(t0)
	if len(producers) != 0 || truncated {
		t.Fatalf("nil tracker reported %v, %v", producers, truncated)
	}
}

func TestTrackerProducers(t *testing.T) {
	tests := []struct {
		name    string
		observe func(*Tracker)
		want    []Producer
		trunc   bool
	}{
		{
			name: "counts accumulate per service and scopes are deduplicated and ordered",
			observe: func(tr *Tracker) {
				sdk := SDK{Name: "opentelemetry", Language: "python", Version: "1.27.0"}
				tr.ObserveScope("support-agent", sdk, Scope{Name: "openinference.openai", Version: "0.1"}, 2, t0)
				tr.ObserveScope("support-agent", sdk, Scope{Name: "httpx", Version: "0.2"}, 3, t0.Add(time.Second))
				tr.ObserveScope("support-agent", sdk, Scope{Name: "httpx", Version: "0.2"}, 1, t0.Add(2*time.Second))
				tr.ObserveScope("", SDK{}, Scope{}, 4, t0)
			},
			want: []Producer{
				{ServiceName: "", Spans: "4", LastSeenAgeMS: "5000", Scopes: []Scope{{}}, SDK: SDK{}},
				{
					ServiceName: "support-agent", Spans: "6", LastSeenAgeMS: "3000",
					Scopes: []Scope{{Name: "httpx", Version: "0.2"}, {Name: "openinference.openai", Version: "0.1"}},
					SDK:    SDK{Name: "opentelemetry", Language: "python", Version: "1.27.0"},
				},
			},
		},
		{
			name: "an empty batch is not a producer",
			observe: func(tr *Tracker) {
				tr.ObserveScope("quiet", SDK{}, Scope{}, 0, t0)
			},
			want: []Producer{},
		},
		{
			name: "control characters and invalid bytes are replaced, not dropped",
			observe: func(tr *Tracker) {
				tr.ObserveScope("bad\x1bname\xff", SDK{Name: "a\nb"}, Scope{Name: "s\x00"}, 1, t0)
			},
			want: []Producer{{
				ServiceName: "bad�name�", Spans: "1", LastSeenAgeMS: "5000",
				Scopes: []Scope{{Name: "s�"}}, SDK: SDK{Name: "a�b"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewTracker()
			tt.observe(tr)
			got, trunc := tr.Producers(t0.Add(5 * time.Second))
			if trunc != tt.trunc {
				t.Fatalf("truncated = %v, want %v", trunc, tt.trunc)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("producers:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestTrackerBoundsAreReportedNotSilent(t *testing.T) {
	tr := NewTracker()
	for i := range MaxProducers + 3 {
		tr.ObserveScope(strings.Repeat("p", i+1), SDK{}, Scope{}, 1, t0)
	}
	for i := range MaxScopes + 2 {
		tr.ObserveScope("p", SDK{}, Scope{Name: strings.Repeat("s", i+1)}, 1, t0)
	}
	producers, truncated := tr.Producers(t0)
	if len(producers) != MaxProducers || !truncated {
		t.Fatalf("got %d producers, truncated=%v; want %d and true", len(producers), truncated, MaxProducers)
	}
	for _, p := range producers {
		if p.ServiceName == "p" {
			if len(p.Scopes) != MaxScopes || !p.ScopesTruncated {
				t.Fatalf("scopes: got %d, truncated=%v", len(p.Scopes), p.ScopesTruncated)
			}
		}
	}
}

func TestSanitizeBoundsLengthAtARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	got := Sanitize(long)
	if len(got) > maxText || !strings.HasPrefix(long, got) || len(got)%2 != 0 {
		t.Fatalf("Sanitize cut %d bytes badly: %q", len(got), got[len(got)-4:])
	}
	if Sanitize("plain") != "plain" {
		t.Fatal("a clean value changed")
	}
}

func TestReporterSendsImmediatelyThenOnEachInterval(t *testing.T) {
	var mu sync.Mutex
	var bodies []Report
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var report Report
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Errorf("report is not JSON: %v", err)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type %q", got)
		}
		mu.Lock()
		bodies = append(bodies, report)
		paths = append(paths, r.URL.EscapedPath())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"1","disposition":"accepted"}`)
	}))
	defer server.Close()

	base, _ := url.Parse(server.URL)
	reporter, err := NewReporter(base, "dev/one", 20*time.Millisecond,
		func(time.Time) Report { return Report{UptimeMS: "7", Producers: []Producer{}} }, nil)
	if err != nil {
		t.Fatal(err)
	}
	reporter.Start()
	reporter.Start() // ignored
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := reporter.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 3 {
		t.Fatalf("got %d reports, want at least 3", len(bodies))
	}
	for i, report := range bodies {
		if report.Version != WireVersion || report.CollectorID != "dev/one" ||
			report.Instance != reporter.Instance() || report.Sequence != formatUint(uint64(i+1)) {
			t.Fatalf("report %d identity: %+v", i, report)
		}
		if paths[i] != "/v1/collectors/dev%2Fone/status" {
			t.Fatalf("path %q does not escape the collector id", paths[i])
		}
	}
}

func TestReporterWarnsOncePerFailureStreak(t *testing.T) {
	var fail sync.Mutex
	failing := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fail.Lock()
		defer fail.Unlock()
		if failing {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	base, _ := url.Parse(server.URL)
	var warnings []error
	reporter, err := NewReporter(base, "c", time.Hour,
		func(time.Time) Report { return Report{} }, func(err error) { warnings = append(warnings, err) })
	if err != nil {
		t.Fatal(err)
	}
	reporter.send()
	reporter.send()
	if len(warnings) != 1 {
		t.Fatalf("two failures produced %d warnings, want 1", len(warnings))
	}
	fail.Lock()
	failing = false
	fail.Unlock()
	reporter.send()
	fail.Lock()
	failing = true
	fail.Unlock()
	reporter.send()
	if len(warnings) != 2 {
		t.Fatalf("a new failure streak produced %d warnings in total, want 2", len(warnings))
	}
}

func TestReporterStopIsBoundedAndSafeUnstarted(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:1")
	reporter, err := NewReporter(base, "c", time.Hour, func(time.Time) Report { return Report{} }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.Stop(context.Background()); err != nil {
		t.Fatalf("stopping an unstarted reporter: %v", err)
	}
	if err := reporter.Stop(context.Background()); err != nil {
		t.Fatalf("stopping twice: %v", err)
	}
}

// TestReporterStopAbandonsAHungRequest proves a control plane that never
// answers cannot hold Collector shutdown: Stop cancels the in-flight POST.
func TestReporterStopAbandonsAHungRequest(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	base, _ := url.Parse(server.URL)
	reporter, err := NewReporter(base, "c", time.Hour, func(time.Time) Report { return Report{} }, nil)
	if err != nil {
		t.Fatal(err)
	}
	reporter.Start()
	time.Sleep(50 * time.Millisecond) // the first POST is now hanging
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := reporter.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop waited %s on a hung request", elapsed)
	}
}

func TestNewReporterRefusesIncompleteInput(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:1")
	snapshot := func(time.Time) Report { return Report{} }
	for name, build := range map[string]func() (*Reporter, error){
		"no url":      func() (*Reporter, error) { return NewReporter(nil, "c", time.Second, snapshot, nil) },
		"no id":       func() (*Reporter, error) { return NewReporter(base, "", time.Second, snapshot, nil) },
		"no interval": func() (*Reporter, error) { return NewReporter(base, "c", 0, snapshot, nil) },
		"no snapshot": func() (*Reporter, error) { return NewReporter(base, "c", time.Second, nil, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := build(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := NewReporter(base, "c", time.Second, snapshot, nil); err != nil {
		t.Fatalf("a complete reporter was refused: %v", err)
	}
}

func TestTrackerEvaluatedSections(t *testing.T) {
	tr := NewTracker()
	for range 3 {
		tr.ObserveEvaluated(Evaluated{Semantic: true, Model: true, Operation: "llama3.2", Target: "ollama"})
	}
	tr.ObserveEvaluated(Evaluated{Semantic: true, Model: true, Operation: "gpt-x", Target: ""})
	tr.ObserveEvaluated(Evaluated{Semantic: true, Operation: "export_customer", Target: "export.localhost"})
	for _, op := range []string{"POST /a", "POST /b", "GET /c", "POST /a"} {
		tr.ObserveEvaluated(Evaluated{Operation: op, Target: "api.example.com"})
	}
	tr.ObserveEvaluated(Evaluated{Operation: "SELECT", Target: ""})

	models, truncated := tr.Models()
	gotModels, _ := json.Marshal(models)
	if want := `[{"provider":"","model":"gpt-x","calls":"1"},{"provider":"ollama","model":"llama3.2","calls":"3"}]`; string(gotModels) != want || truncated {
		t.Fatalf("models %s (truncated %v), want %s", gotModels, truncated, want)
	}
	if got := tr.Fidelity(); got.Semantic != "5" || got.Transport != "5" {
		t.Fatalf("fidelity %+v", got)
	}
	targets, truncated := tr.TransportTargets()
	gotTargets, _ := json.Marshal(targets)
	want := `[{"target":"","spans":"1","distinct_operations":"1","operations_saturated":false},` +
		`{"target":"api.example.com","spans":"4","distinct_operations":"3","operations_saturated":false}]`
	if string(gotTargets) != want || truncated {
		t.Fatalf("targets %s (truncated %v), want %s", gotTargets, truncated, want)
	}
	// Operation names are counted, never reported.
	if strings.Contains(string(gotTargets), "POST") {
		t.Fatal("an operation name reached the report")
	}
}

func TestTrackerEvaluatedBounds(t *testing.T) {
	tr := NewTracker()
	for i := range MaxModels + 2 {
		tr.ObserveEvaluated(Evaluated{Semantic: true, Model: true, Operation: strings.Repeat("m", i+1)})
	}
	for i := range MaxTransportTargets + 2 {
		tr.ObserveEvaluated(Evaluated{Operation: "op", Target: strings.Repeat("t", i+1)})
	}
	for i := range MaxOperationsPerTarget + 5 {
		tr.ObserveEvaluated(Evaluated{Operation: strings.Repeat("o", i+1), Target: "t"})
	}
	if models, truncated := tr.Models(); len(models) != MaxModels || !truncated {
		t.Fatalf("models %d truncated=%v", len(models), truncated)
	}
	targets, truncated := tr.TransportTargets()
	if len(targets) != MaxTransportTargets || !truncated {
		t.Fatalf("targets %d truncated=%v", len(targets), truncated)
	}
	for _, target := range targets {
		if target.Target == "t" {
			if target.DistinctOperations != formatUint(MaxOperationsPerTarget) || !target.OperationsSaturated {
				t.Fatalf("saturation: %+v", target)
			}
			// Spans keep counting past the operation bound: only the distinct
			// count saturates.
			if target.Spans != formatUint(MaxOperationsPerTarget+5+1) {
				t.Fatalf("spans: %+v", target)
			}
		}
	}
	// A known model keeps counting at the bound.
	tr.ObserveEvaluated(Evaluated{Semantic: true, Model: true, Operation: "m"})
	models, _ := tr.Models()
	for _, m := range models {
		if m.Model == "m" && m.Calls != "2" {
			t.Fatalf("a known model stopped counting at the bound: %+v", m)
		}
	}
}

func TestTrackerLearning(t *testing.T) {
	tr := NewTracker()
	tr.ObserveLearning(true, "allow", false)
	tr.ObserveLearning(false, "block", false)
	tr.ObserveLearning(false, "block", false)
	tr.ObserveLearning(false, "allow", false)
	tr.ObserveLearning(false, "allow", true) // a failure is not "not learned"
	got, _ := json.Marshal(tr.Learning())
	want := `{"learned":"1","not_learned":"3","observe_errors":"1",` +
		`"not_learned_by_decision":[{"decision":"allow","count":"1"},{"decision":"block","count":"2"}]}`
	if string(got) != want {
		t.Fatalf("learning %s, want %s", got, want)
	}

	// An unexpected decision cannot grow the map past its bound, and every
	// not-learned outcome that is grouped stays in the total.
	bounded := NewTracker()
	for i := range MaxDecisions + 4 {
		bounded.ObserveLearning(false, strings.Repeat("d", i+1), false)
	}
	learning := bounded.Learning()
	if len(learning.NotLearnedByDecision) != MaxDecisions || learning.NotLearned != formatUint(MaxDecisions) {
		t.Fatalf("bounded learning: %+v", learning)
	}
}
