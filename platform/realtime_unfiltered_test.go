package platform

// An unfiltered realtime subscription, which is what task 074's Live view
// opens.
//
// The capability already existed: RealtimeFilter treats an empty value as
// unconstrained on every dimension, and its doc comment says so. Nothing in
// task 074 changes the bus, the route, the filters, the event kinds or the
// payload. These are regression tests rather than new behaviour — the Live
// view's entire discovery story rests on "subscribe with nothing set and
// receive everything", and that property now has a consumer whose correctness
// depends on it.
//
// The filtered cases are asserted alongside, because the risk of adding a
// consumer for the unfiltered path is that somebody later "simplifies" the
// filter and breaks the three subscriptions that do constrain.

import (
	"testing"
	"time"
)

// drain collects up to want events, failing rather than blocking forever.
func drain(t *testing.T, subscription RealtimeSubscription, want int) []RealtimeEvent {
	t.Helper()
	received := make([]RealtimeEvent, 0, want)
	deadline := time.After(2 * time.Second)
	for len(received) < want {
		select {
		case event, open := <-subscription.Events():
			if !open {
				t.Fatalf("subscription closed after %d of %d events", len(received), want)
			}
			received = append(received, event)
		case <-deadline:
			t.Fatalf("received %d of %d events before the deadline", len(received), want)
		}
	}
	return received
}

// TestUnfilteredSubscriptionReceivesEveryScope is the Live view's premise.
//
// Two projects, two agents, two runs, one subscription and no filter. If any
// dimension were treated as "must match" when empty, the view would silently
// show a subset of local activity and a developer would conclude their agent
// was not being observed.
func TestUnfilteredSubscriptionReceivesEveryScope(t *testing.T) {
	bus := testBus(t, 16, 4)

	subscription, err := bus.Subscribe(t.Context(), RealtimeFilter{})
	if err != nil {
		t.Fatalf("Subscribe() with an empty filter error = %v", err)
	}
	defer subscription.Close()

	scopes := []RealtimeScope{
		scopeFor("proj-a", "agent-a", "run-1"),
		scopeFor("proj-a", "agent-b", "run-2"),
		scopeFor("proj-b", "agent-c", "run-3"),
		scopeFor("proj-b", "agent-c", "run-4"),
	}
	for i, scope := range scopes {
		result := bus.Publish(observationEvent(scope, uint64(i+1)))
		if result.Delivered != 1 {
			t.Fatalf("scope %d delivered to %d subscribers, want 1", i, result.Delivered)
		}
	}

	received := drain(t, subscription, len(scopes))
	for i, event := range received {
		if event.Scope != scopes[i] {
			t.Errorf("event %d scope = %+v, want %+v", i, event.Scope, scopes[i])
		}
	}

	// Named explicitly: the card set a Live view builds comes from these
	// scopes alone, with no /v1 read per event.
	projects := map[ProjectID]bool{}
	agents := map[AgentID]bool{}
	runs := map[EvaluationRunID]bool{}
	for _, event := range received {
		projects[event.Scope.ProjectID] = true
		agents[event.Scope.AgentID] = true
		runs[event.Scope.RunID] = true
	}
	if len(projects) != 2 || len(agents) != 3 || len(runs) != 4 {
		t.Errorf("distinct scopes = %d projects, %d agents, %d runs; want 2, 3, 4",
			len(projects), len(agents), len(runs))
	}
}

// TestFilteredSubscriptionsStillConstrain is the other half.
//
// Adding an unfiltered consumer must not make the three filters decorative.
// Each is asserted to receive exactly its own dimension's traffic.
func TestFilteredSubscriptionsStillConstrain(t *testing.T) {
	scopes := []RealtimeScope{
		scopeFor("proj-a", "agent-a", "run-1"),
		scopeFor("proj-a", "agent-b", "run-2"),
		scopeFor("proj-b", "agent-c", "run-3"),
	}

	tests := []struct {
		name   string
		filter RealtimeFilter
		want   []RealtimeScope
	}{
		{"unconstrained", RealtimeFilter{}, scopes},
		{"by project", RealtimeFilter{ProjectID: "proj-a"}, scopes[:2]},
		{"by agent", RealtimeFilter{AgentID: "agent-b"}, scopes[1:2]},
		{"by run", RealtimeFilter{RunID: "run-3"}, scopes[2:]},
		{"by project and agent", RealtimeFilter{ProjectID: "proj-a", AgentID: "agent-a"}, scopes[:1]},
		{"contradictory", RealtimeFilter{ProjectID: "proj-a", RunID: "run-3"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := testBus(t, 16, 4)
			subscription, err := bus.Subscribe(t.Context(), tt.filter)
			if err != nil {
				t.Fatalf("Subscribe(%+v) error = %v", tt.filter, err)
			}
			defer subscription.Close()

			for i, scope := range scopes {
				bus.Publish(observationEvent(scope, uint64(i+1)))
			}

			if len(tt.want) == 0 {
				select {
				case event := <-subscription.Events():
					t.Fatalf("a contradictory filter received %+v", event.Scope)
				case <-time.After(100 * time.Millisecond):
				}
				return
			}
			received := drain(t, subscription, len(tt.want))
			for i, event := range received {
				if event.Scope != tt.want[i] {
					t.Errorf("event %d scope = %+v, want %+v", i, event.Scope, tt.want[i])
				}
			}
			select {
			case extra := <-subscription.Events():
				t.Errorf("filter %+v also received %+v", tt.filter, extra.Scope)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// TestUnfilteredAndFilteredSubscribersCoexist covers the deployment the Live
// view actually creates: a browser watching everything beside a TUI or a
// second tab watching one run.
func TestUnfilteredAndFilteredSubscribersCoexist(t *testing.T) {
	bus := testBus(t, 16, 4)

	everything, err := bus.Subscribe(t.Context(), RealtimeFilter{})
	if err != nil {
		t.Fatalf("Subscribe(unfiltered) error = %v", err)
	}
	defer everything.Close()

	oneRun, err := bus.Subscribe(t.Context(), RealtimeFilter{RunID: "run-2"})
	if err != nil {
		t.Fatalf("Subscribe(run-2) error = %v", err)
	}
	defer oneRun.Close()

	first := scopeFor("proj-a", "agent-a", "run-1")
	second := scopeFor("proj-a", "agent-a", "run-2")

	if result := bus.Publish(observationEvent(first, 1)); result.Delivered != 1 {
		t.Errorf("run-1 delivered to %d subscribers, want 1 (the unfiltered one)",
			result.Delivered)
	}
	if result := bus.Publish(observationEvent(second, 2)); result.Delivered != 2 {
		t.Errorf("run-2 delivered to %d subscribers, want 2", result.Delivered)
	}

	if got := drain(t, everything, 2); got[0].Scope != first || got[1].Scope != second {
		t.Errorf("the unfiltered subscriber saw %+v, want both scopes in order", got)
	}
	if got := drain(t, oneRun, 1); got[0].Scope != second {
		t.Errorf("the run-2 subscriber saw %+v, want run-2 only", got[0].Scope)
	}
}
