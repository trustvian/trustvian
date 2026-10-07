package platform_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platform "trustvian-platform"
)

var statusEpoch = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func newStatusPlane(t *testing.T) *platform.ControlPlane {
	t.Helper()
	store, err := platform.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	return plane
}

func validReport(collector, instance string, sequence uint64) platform.CollectorStatusReport {
	return platform.CollectorStatusReport{
		CollectorID: collector,
		Instance:    instance,
		Sequence:    sequence,
		Uptime:      90 * time.Second,
		Spans:       platform.CollectorSpanCounts{Received: 10, Evaluated: 8, Invalid: 2},
		Producers: []platform.ProducerStatus{{
			ServiceName: "support-agent", Spans: 10, LastSeenAge: 4 * time.Second,
			Scopes: []platform.InstrumentationScope{{Name: "openinference", Version: "0.1"}},
			SDK:    platform.TelemetrySDK{Name: "opentelemetry", Language: "python", Version: "1.27.0"},
		}},
	}
}

const instanceA = "00000000000000aa"
const instanceB = "00000000000000bb"

func TestStatusReportValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*platform.CollectorStatusReport)
	}{
		{"empty collector id", func(r *platform.CollectorStatusReport) { r.CollectorID = "" }},
		{"short instance", func(r *platform.CollectorStatusReport) { r.Instance = "abc" }},
		{"uppercase instance", func(r *platform.CollectorStatusReport) { r.Instance = "00000000000000AA" }},
		{"sequence zero", func(r *platform.CollectorStatusReport) { r.Sequence = 0 }},
		{"negative uptime", func(r *platform.CollectorStatusReport) { r.Uptime = -time.Second }},
		{"evaluated beyond received", func(r *platform.CollectorStatusReport) { r.Spans.Evaluated = 11 }},
		{"invalid beyond received", func(r *platform.CollectorStatusReport) { r.Spans.Invalid = 11 }},
		{"too many endpoints", func(r *platform.CollectorStatusReport) {
			r.ReceiverEndpoints = []string{"a", "b", "c", "d", "e"}
		}},
		{"endpoint with a newline", func(r *platform.CollectorStatusReport) {
			r.ReceiverEndpoints = []string{"127.0.0.1:4318\n"}
		}},
		{"duplicate producer", func(r *platform.CollectorStatusReport) {
			r.Producers = append(r.Producers, r.Producers[0])
		}},
		{"control character in a service name", func(r *platform.CollectorStatusReport) {
			r.Producers[0].ServiceName = "svc\x1b[2J"
		}},
		{"oversized scope name", func(r *platform.CollectorStatusReport) {
			r.Producers[0].Scopes[0].Name = strings.Repeat("s", 257)
		}},
		{"too many scopes", func(r *platform.CollectorStatusReport) {
			r.Producers[0].Scopes = make([]platform.InstrumentationScope, platform.MaxStatusScopes+1)
		}},
		{"too many producers", func(r *platform.CollectorStatusReport) {
			r.Producers = nil
			for i := range platform.MaxStatusProducers + 1 {
				r.Producers = append(r.Producers, platform.ProducerStatus{ServiceName: fmt.Sprint("p", i)})
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plane := newStatusPlane(t)
			report := validReport("dev", instanceA, 1)
			tt.mutate(&report)
			_, err := plane.ReportCollectorStatus(t.Context(), report, statusEpoch)
			if !errors.Is(err, platform.ErrInvalidStatusReport) && !errors.Is(err, platform.ErrInvalidID) {
				t.Fatalf("error = %v, want an invalid-report error", err)
			}
			if got := plane.PipelineStatus(t.Context(), statusEpoch).Collectors; len(got) != 0 {
				t.Fatalf("a refused report was retained: %v", got)
			}
		})
	}
}

func TestStatusRegistryOrdering(t *testing.T) {
	tests := []struct {
		name   string
		steps  []platform.CollectorStatusReport
		want   []platform.StatusReportDisposition
		holdSq uint64
		holdIn string
	}{
		{
			name:   "a newer sequence replaces",
			steps:  []platform.CollectorStatusReport{validReport("dev", instanceA, 1), validReport("dev", instanceA, 2)},
			want:   []platform.StatusReportDisposition{platform.StatusReportAccepted, platform.StatusReportAccepted},
			holdSq: 2, holdIn: instanceA,
		},
		{
			name:   "a late, older report from the same process is ignored",
			steps:  []platform.CollectorStatusReport{validReport("dev", instanceA, 5), validReport("dev", instanceA, 4)},
			want:   []platform.StatusReportDisposition{platform.StatusReportAccepted, platform.StatusReportIgnored},
			holdSq: 5, holdIn: instanceA,
		},
		{
			name:   "a repeated sequence is ignored",
			steps:  []platform.CollectorStatusReport{validReport("dev", instanceA, 5), validReport("dev", instanceA, 5)},
			want:   []platform.StatusReportDisposition{platform.StatusReportAccepted, platform.StatusReportIgnored},
			holdSq: 5, holdIn: instanceA,
		},
		{
			name:   "a restarted process replaces its predecessor whatever its sequence",
			steps:  []platform.CollectorStatusReport{validReport("dev", instanceA, 50), validReport("dev", instanceB, 1)},
			want:   []platform.StatusReportDisposition{platform.StatusReportAccepted, platform.StatusReportAccepted},
			holdSq: 1, holdIn: instanceB,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plane := newStatusPlane(t)
			for i, report := range tt.steps {
				got, err := plane.ReportCollectorStatus(t.Context(), report, statusEpoch.Add(time.Duration(i)*time.Second))
				if err != nil {
					t.Fatal(err)
				}
				if got != tt.want[i] {
					t.Fatalf("step %d: disposition %q, want %q", i, got, tt.want[i])
				}
			}
			collectors := plane.PipelineStatus(t.Context(), statusEpoch.Add(5*time.Second)).Collectors
			if len(collectors) != 1 {
				t.Fatalf("collectors = %d", len(collectors))
			}
			if c := collectors[0].Report; c.Sequence != tt.holdSq || c.Instance != tt.holdIn {
				t.Fatalf("held %s/%d, want %s/%d", c.Instance, c.Sequence, tt.holdIn, tt.holdSq)
			}
		})
	}
}

func TestPipelineStatusPlacesReportsOnTheControlPlaneClock(t *testing.T) {
	plane := newStatusPlane(t)
	received := statusEpoch
	if _, err := plane.ReportCollectorStatus(t.Context(), validReport("dev", instanceA, 1), received); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		at    time.Time
		state platform.CollectorState
		held  bool
	}{
		{"at the report", received, platform.CollectorReporting, true},
		{"at the edge of the fresh window", received.Add(platform.StatusFreshWindow), platform.CollectorReporting, true},
		{"just past it", received.Add(platform.StatusFreshWindow + time.Millisecond), platform.CollectorStale, true},
		{"at expiry", received.Add(platform.StatusExpiry), platform.CollectorStale, true},
		{"past expiry", received.Add(platform.StatusExpiry + time.Millisecond), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := plane.PipelineStatus(t.Context(), tt.at)
			if !status.ReadAt.Equal(tt.at) {
				t.Fatalf("read at %s", status.ReadAt)
			}
			if status.Engine.State != platform.EngineUnavailable || status.Engine.Reason == "" {
				t.Fatalf("engine section: %+v", status.Engine)
			}
			if !tt.held {
				if len(status.Collectors) != 0 {
					t.Fatalf("an expired collector is still shown")
				}
				return
			}
			c := status.Collectors[0]
			if c.State != tt.state {
				t.Fatalf("state %q, want %q", c.State, tt.state)
			}
			// Ages convert against the time the report arrived, never the
			// time it was read: a stale report must not make its producers
			// look recent.
			if want := received.Add(-4 * time.Second); !c.ProducerLastSeen[0].Equal(want) {
				t.Fatalf("producer last seen %s, want %s", c.ProducerLastSeen[0], want)
			}
			if want := received.Add(-90 * time.Second); !c.StartedAt.Equal(want) {
				t.Fatalf("started at %s, want %s", c.StartedAt, want)
			}
		})
	}
}

func TestStatusCollectorBoundRefusesRatherThanEvicts(t *testing.T) {
	plane := newStatusPlane(t)
	for i := range platform.MaxStatusCollectors {
		if _, err := plane.ReportCollectorStatus(t.Context(),
			validReport(fmt.Sprint("c", i), instanceA, 1), statusEpoch); err != nil {
			t.Fatal(err)
		}
	}
	_, err := plane.ReportCollectorStatus(t.Context(), validReport("one-too-many", instanceA, 1), statusEpoch)
	if !errors.Is(err, platform.ErrStatusCollectorLimit) {
		t.Fatalf("error = %v, want the collector limit", err)
	}
	// A held collector keeps reporting at the bound.
	if _, err := plane.ReportCollectorStatus(t.Context(), validReport("c0", instanceA, 2), statusEpoch); err != nil {
		t.Fatalf("a held collector was refused at the bound: %v", err)
	}
	// Once the others expire, the newcomer's slot opens without evicting anyone live.
	later := statusEpoch.Add(platform.StatusExpiry + time.Second)
	if _, err := plane.ReportCollectorStatus(t.Context(), validReport("one-too-many", instanceA, 1), later); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestPipelineStatusIsACopy(t *testing.T) {
	plane := newStatusPlane(t)
	report := validReport("dev", instanceA, 1)
	if _, err := plane.ReportCollectorStatus(t.Context(), report, statusEpoch); err != nil {
		t.Fatal(err)
	}
	report.Producers[0].ServiceName = "mutated by the caller"
	got := plane.PipelineStatus(t.Context(), statusEpoch)
	got.Collectors[0].Report.Producers[0].Scopes[0].Name = "mutated by a reader"
	again := plane.PipelineStatus(t.Context(), statusEpoch)
	p := again.Collectors[0].Report.Producers[0]
	if p.ServiceName != "support-agent" || p.Scopes[0].Name != "openinference" {
		t.Fatalf("held state was reachable from outside: %+v", p)
	}
}

func TestLandingIsLiveOnlyWhileSomethingIsActive(t *testing.T) {
	tests := []struct {
		name    string
		report  func() platform.CollectorStatusReport
		readAt  time.Duration
		landing platform.Landing
	}{
		{"nothing has reported", nil, 0, platform.LandingStatus},
		{"a reporting collector with a fresh producer", func() platform.CollectorStatusReport {
			return validReport("dev", instanceA, 1)
		}, 0, platform.LandingLive},
		{"a reporting collector with no producer", func() platform.CollectorStatusReport {
			r := validReport("dev-check", instanceA, 1)
			r.Producers = nil
			return r
		}, 0, platform.LandingStatus},
		{"a reporting collector whose producer went quiet", func() platform.CollectorStatusReport {
			r := validReport("dev", instanceA, 1)
			r.Producers[0].LastSeenAge = platform.StatusFreshWindow + time.Second
			return r
		}, 0, platform.LandingStatus},
		{"a stale collector, whatever its producers said", func() platform.CollectorStatusReport {
			return validReport("dev", instanceA, 1)
		}, platform.StatusFreshWindow + time.Second, platform.LandingStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plane := newStatusPlane(t)
			if tt.report != nil {
				if _, err := plane.ReportCollectorStatus(t.Context(), tt.report(), statusEpoch); err != nil {
					t.Fatal(err)
				}
			}
			if got := plane.PipelineStatus(t.Context(), statusEpoch.Add(tt.readAt)).Landing; got != tt.landing {
				t.Fatalf("landing %q, want %q", got, tt.landing)
			}
		})
	}
}

// TestStatusChangesAreNotifiedOnlyWhenTheFactsMove: an idle Collector reports
// every interval, and only its sequence, uptime and ages move — none of which
// is a change a reader would see, so none wakes an open view.
func TestStatusChangesAreNotifiedOnlyWhenTheFactsMove(t *testing.T) {
	store, err := platform.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	publisher := &recordingPublisher{}
	plane, err := platform.NewControlPlane(store, store, store, platform.WithRealtimePublisher(publisher))
	if err != nil {
		t.Fatal(err)
	}

	step := func(report platform.CollectorStatusReport) {
		t.Helper()
		if _, err := plane.ReportCollectorStatus(t.Context(), report, statusEpoch); err != nil {
			t.Fatal(err)
		}
	}
	first := validReport("dev", instanceA, 1)
	step(first) // a new collector: a change

	idle := validReport("dev", instanceA, 2)
	idle.Uptime += 10 * time.Second
	idle.Producers[0].LastSeenAge += 10 * time.Second
	step(idle) // only the volatile fields moved: no change

	busier := validReport("dev", instanceA, 3)
	busier.Spans.Received, busier.Spans.Evaluated = 20, 18
	step(busier) // a count moved: a change

	step(validReport("dev", instanceA, 2)) // late and ignored: no change

	restarted := validReport("dev", instanceB, 1)
	restarted.Spans = busier.Spans
	step(restarted) // a new process: a change, because the instance moved

	var kinds []string
	for _, e := range publisher.captured() {
		if e.Kind != platform.RealtimeStatusChanged || e.Status.CollectorID != "dev" {
			t.Fatalf("unexpected event %+v", e)
		}
		if e.Scope != (platform.RealtimeScope{}) {
			t.Fatalf("a status event carries a scope, so a filtered stream could match it: %+v", e.Scope)
		}
		kinds = append(kinds, string(e.Kind))
	}
	if len(kinds) != 3 {
		t.Fatalf("published %d status events, want 3 (new collector, moved count, new process)", len(kinds))
	}
}

// TestStatusEventsReachOnlyUnfilteredStreams: the TUI and the single-run watch
// subscribe narrowed to a run, and a status notification is not about any run.
func TestStatusEventsReachOnlyUnfilteredStreams(t *testing.T) {
	bus := platform.NewInMemoryRealtimeBus()
	t.Cleanup(func() { _ = bus.Close() })
	unfiltered, err := bus.Subscribe(t.Context(), platform.RealtimeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byRun, err := bus.Subscribe(t.Context(), platform.RealtimeFilter{RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	byProject, err := bus.Subscribe(t.Context(), platform.RealtimeFilter{ProjectID: "proj-1"})
	if err != nil {
		t.Fatal(err)
	}

	result := bus.Publish(platform.RealtimeEvent{
		Kind: platform.RealtimeStatusChanged, Status: platform.RealtimeStatusChange{CollectorID: "dev"}})
	if result.Delivered != 1 {
		t.Fatalf("delivered to %d subscriptions, want only the unfiltered one", result.Delivered)
	}
	if e := <-unfiltered.Events(); e.Kind != platform.RealtimeStatusChanged {
		t.Fatalf("unfiltered stream got %v", e.Kind)
	}
	for name, sub := range map[string]platform.RealtimeSubscription{"run": byRun, "project": byProject} {
		select {
		case e := <-sub.Events():
			t.Fatalf("the %s-filtered stream received %v", name, e.Kind)
		default:
		}
	}
}
