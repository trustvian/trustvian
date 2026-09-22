package platform

// Task 059: the realtime bus.
//
// Almost everything here is about a bound holding under pressure. A
// notification layer is easy to get right when nobody is slow; the interesting
// cases are a subscriber that stops reading, a busy run beside a quiet one, and
// close racing publish.
//
// No test uses a sleep as its correctness mechanism. Barriers and channel
// closes are what make the interleavings deterministic.

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// testBus is a bus with small bounds, so saturation is reachable without
// publishing thousands of events.
func testBus(t *testing.T, queueCapacity, maxSubscribers int) *InMemoryRealtimeBus {
	t.Helper()
	bus := newRealtimeBus(queueCapacity, maxSubscribers)
	t.Cleanup(func() { bus.Close() })
	return bus
}

// scopeFor builds a scope in one project/agent/run hierarchy.
func scopeFor(project ProjectID, agent AgentID, run EvaluationRunID) RealtimeScope {
	return RealtimeScope{
		ProjectID:   project,
		AgentID:     agent,
		CandidateID: "cand-1",
		RunID:       run,
		Environment: "staging",
	}
}

func observationEvent(scope RealtimeScope, sequence uint64) RealtimeEvent {
	return RealtimeEvent{
		Kind:        RealtimeObservationRecorded,
		Scope:       scope,
		Observation: RealtimeObservation{Sequence: sequence, RecordCount: sequence},
	}
}

// ---------------------------------------------------------------------
// Ordering and delivery
// ---------------------------------------------------------------------

func TestRealtimeDeliversInPublishOrder(t *testing.T) {
	bus := testBus(t, 8, 4)
	scope := scopeFor("proj-1", "agent-1", "run-1")

	subscription, err := bus.Subscribe(t.Context(), RealtimeFilter{})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	for sequence := uint64(1); sequence <= 4; sequence++ {
		if result := bus.Publish(observationEvent(scope, sequence)); result.Delivered != 1 {
			t.Fatalf("publish %d delivered %d, want 1", sequence, result.Delivered)
		}
	}

	for want := uint64(1); want <= 4; want++ {
		received := <-subscription.Events()
		if received.Observation.Sequence != want {
			t.Fatalf("received sequence %d, want %d; events were reordered",
				received.Observation.Sequence, want)
		}
	}
}

// ---------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------

func TestRealtimeFilterDimensions(t *testing.T) {
	tests := []struct {
		name   string
		filter RealtimeFilter
		scope  RealtimeScope
		want   bool
	}{
		{"empty matches everything", RealtimeFilter{}, scopeFor("p1", "a1", "r1"), true},
		{"project matches", RealtimeFilter{ProjectID: "p1"}, scopeFor("p1", "a1", "r1"), true},
		{"project excludes", RealtimeFilter{ProjectID: "p2"}, scopeFor("p1", "a1", "r1"), false},
		{"agent matches", RealtimeFilter{AgentID: "a1"}, scopeFor("p1", "a1", "r1"), true},
		{"agent excludes", RealtimeFilter{AgentID: "a2"}, scopeFor("p1", "a1", "r1"), false},
		{"run matches", RealtimeFilter{RunID: "r1"}, scopeFor("p1", "a1", "r1"), true},
		{"run excludes", RealtimeFilter{RunID: "r2"}, scopeFor("p1", "a1", "r1"), false},
		{
			name:   "dimensions are ANDed",
			filter: RealtimeFilter{ProjectID: "p1", AgentID: "a2"},
			scope:  scopeFor("p1", "a1", "r1"),
			want:   false,
		},
		{
			name:   "all dimensions match",
			filter: RealtimeFilter{ProjectID: "p1", AgentID: "a1", RunID: "r1"},
			scope:  scopeFor("p1", "a1", "r1"),
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.matches(tt.scope); got != tt.want {
				t.Errorf("matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Filtering happens before enqueue, so a busy run cannot fill the queue of a
// subscriber watching a different one. Without that, the per-subscriber bound
// would be nominal: isolation would depend on what everybody else is doing.
func TestRealtimeFilteringHappensBeforeEnqueue(t *testing.T) {
	const capacity = 4
	bus := testBus(t, capacity, 4)

	watchingA, err := bus.Subscribe(t.Context(), RealtimeFilter{RunID: "run-a"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	// Far more than the queue could hold, all for a run this subscriber does
	// not watch. Nothing is consumed in the meantime.
	scopeB := scopeFor("proj-1", "agent-1", "run-b")
	for sequence := uint64(1); sequence <= capacity*100; sequence++ {
		if result := bus.Publish(observationEvent(scopeB, sequence)); result.Disconnected != 0 {
			t.Fatalf("publish %d disconnected a subscriber watching another run", sequence)
		}
	}

	if bus.subscriberCount() != 1 {
		t.Fatalf("subscriberCount() = %d, want 1: unrelated traffic dropped the subscription",
			bus.subscriberCount())
	}

	// And it still receives its own event.
	scopeA := scopeFor("proj-1", "agent-1", "run-a")
	if result := bus.Publish(observationEvent(scopeA, 1)); result.Delivered != 1 {
		t.Fatalf("delivered %d, want 1", result.Delivered)
	}
	received := <-watchingA.Events()
	if received.Scope.RunID != "run-a" {
		t.Errorf("received scope %q, want run-a", received.Scope.RunID)
	}
}

// ---------------------------------------------------------------------
// Slow consumers
// ---------------------------------------------------------------------

// A subscriber that stops reading is disconnected, not tolerated. Blocking the
// publisher would add its latency to every committed mutation; dropping
// silently would leave it believing it has a complete stream.
//
// The interleaving is exact rather than timed: the fast subscriber is drained
// to empty with blocking receives before the overflowing event is published,
// so only the slow queue can be full at that moment.
func TestRealtimeSlowConsumerIsDisconnectedAndFastOneContinues(t *testing.T) {
	const capacity = 2
	bus := testBus(t, capacity, 4)
	scope := scopeFor("proj-1", "agent-1", "run-1")

	slow, err := bus.Subscribe(t.Context(), RealtimeFilter{})
	if err != nil {
		t.Fatalf("Subscribe(slow) error = %v", err)
	}
	fast, err := bus.Subscribe(t.Context(), RealtimeFilter{})
	if err != nil {
		t.Fatalf("Subscribe(fast) error = %v", err)
	}

	// Fill both queues exactly to capacity. Nothing is disconnected yet.
	for sequence := uint64(1); sequence <= capacity; sequence++ {
		result := bus.Publish(observationEvent(scope, sequence))
		if result.Delivered != 2 || result.Disconnected != 0 {
			t.Fatalf("publish %d = %+v, want 2 delivered", sequence, result)
		}
	}

	// Drain the fast subscriber to empty, synchronously.
	for want := uint64(1); want <= capacity; want++ {
		received := <-fast.Events()
		if received.Observation.Sequence != want {
			t.Fatalf("fast received %d, want %d", received.Observation.Sequence, want)
		}
	}

	// One more event. The slow queue is still full, the fast one is empty.
	result := bus.Publish(observationEvent(scope, capacity+1))
	if result.Disconnected != 1 {
		t.Fatalf("disconnected = %d, want exactly 1 (the slow subscriber)", result.Disconnected)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1 (the fast subscriber)", result.Delivered)
	}

	// The slow stream is closed, holding exactly what fit before saturation —
	// not silently trimmed, and not grown.
	var held int
	for range slow.Events() {
		held++
	}
	if held != capacity {
		t.Errorf("slow subscriber held %d events, want %d", held, capacity)
	}

	// The fast one is unaffected: still registered, still receiving.
	if bus.subscriberCount() != 1 {
		t.Fatalf("subscriberCount() = %d, want 1", bus.subscriberCount())
	}
	if received := <-fast.Events(); received.Observation.Sequence != capacity+1 {
		t.Errorf("fast received %d, want %d", received.Observation.Sequence, capacity+1)
	}
	bus.Publish(observationEvent(scope, capacity+2))
	if received := <-fast.Events(); received.Observation.Sequence != capacity+2 {
		t.Errorf("fast received %d, want %d", received.Observation.Sequence, capacity+2)
	}
}

// Publish must not wait for a stalled subscriber. Proven by publishing far
// more than a never-read queue could hold and observing that every call
// returns.
func TestRealtimePublishNeverWaitsForAStalledSubscriber(t *testing.T) {
	bus := testBus(t, 1, 4)
	scope := scopeFor("proj-1", "agent-1", "run-1")

	if _, err := bus.Subscribe(t.Context(), RealtimeFilter{}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sequence := uint64(1); sequence <= 1000; sequence++ {
			bus.Publish(observationEvent(scope, sequence))
		}
	}()

	// If Publish blocked on the stalled subscriber this would never close.
	<-done
}

// ---------------------------------------------------------------------
// Subscriber bound
// ---------------------------------------------------------------------

// A bounded queue with unlimited subscribers is still unbounded memory, so
// both halves are capped — and reaching the limit refuses the newcomer rather
// than evicting somebody else's stream.
func TestRealtimeSubscriberLimit(t *testing.T) {
	const maxSubscribers = 3
	bus := testBus(t, 4, maxSubscribers)

	subscriptions := make([]RealtimeSubscription, 0, maxSubscribers)
	for i := range maxSubscribers {
		subscription, err := bus.Subscribe(t.Context(), RealtimeFilter{})
		if err != nil {
			t.Fatalf("Subscribe(%d) error = %v", i, err)
		}
		subscriptions = append(subscriptions, subscription)
	}

	if _, err := bus.Subscribe(t.Context(), RealtimeFilter{}); !errors.Is(err, ErrRealtimeCapacity) {
		t.Fatalf("Subscribe() at capacity error = %v, want ErrRealtimeCapacity", err)
	}
	// Nobody was evicted to make room.
	if bus.subscriberCount() != maxSubscribers {
		t.Errorf("subscriberCount() = %d, want %d", bus.subscriberCount(), maxSubscribers)
	}

	// Closing one reclaims its slot rather than leaking it.
	if err := subscriptions[0].Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if bus.subscriberCount() != maxSubscribers-1 {
		t.Fatalf("subscriberCount() = %d, want %d", bus.subscriberCount(), maxSubscribers-1)
	}
	if _, err := bus.Subscribe(t.Context(), RealtimeFilter{}); err != nil {
		t.Fatalf("Subscribe() after a close error = %v", err)
	}
}

// ---------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------

func TestRealtimeCloseIsIdempotentAndCloseUnsubscribes(t *testing.T) {
	bus := testBus(t, 4, 4)

	subscription, err := bus.Subscribe(t.Context(), RealtimeFilter{})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	for range 3 {
		if err := subscription.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
	if _, open := <-subscription.Events(); open {
		t.Error("the stream is still open after Close")
	}
	if bus.subscriberCount() != 0 {
		t.Errorf("subscriberCount() = %d, want 0", bus.subscriberCount())
	}
}

// Cancelling the subscription context unsubscribes, so a caller that loses
// interest does not hold a slot.
func TestRealtimeContextCancellationUnsubscribes(t *testing.T) {
	bus := testBus(t, 4, 4)

	ctx, cancel := context.WithCancel(context.Background())
	subscription, err := bus.Subscribe(ctx, RealtimeFilter{})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	if bus.subscriberCount() != 1 {
		t.Fatalf("subscriberCount() = %d, want 1", bus.subscriberCount())
	}

	cancel()

	// The stream closing is the observable signal; no sleep needed.
	for range subscription.Events() {
	}
	if bus.subscriberCount() != 0 {
		t.Errorf("subscriberCount() = %d, want 0: the slot leaked", bus.subscriberCount())
	}
}

func TestRealtimeBusCloseDisconnectsEveryone(t *testing.T) {
	bus := newRealtimeBus(4, 4)
	scope := scopeFor("proj-1", "agent-1", "run-1")

	var subscriptions []RealtimeSubscription
	for range 3 {
		subscription, err := bus.Subscribe(context.Background(), RealtimeFilter{})
		if err != nil {
			t.Fatalf("Subscribe() error = %v", err)
		}
		subscriptions = append(subscriptions, subscription)
	}

	if err := bus.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// Idempotent.
	if err := bus.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	for i, subscription := range subscriptions {
		for range subscription.Events() {
		}
		if _, open := <-subscription.Events(); open {
			t.Errorf("subscription %d is still open after bus Close", i)
		}
	}

	if _, err := bus.Subscribe(context.Background(), RealtimeFilter{}); !errors.Is(err, ErrRealtimeClosed) {
		t.Errorf("Subscribe() after Close error = %v, want ErrRealtimeClosed", err)
	}
	// Publishing after close is safe and reports it.
	result := bus.Publish(observationEvent(scope, 1))
	if !result.Closed || result.Delivered != 0 {
		t.Errorf("Publish() after Close = %+v, want closed with no delivery", result)
	}
}

// ---------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------

// Publish, Subscribe, Close and bus Close all racing. The assertion is the
// absence of a panic, a deadlock or a race — run under -race this is the test
// that would catch a send on a closed channel.
func TestRealtimeConcurrentPublishSubscribeClose(t *testing.T) {
	bus := newRealtimeBus(2, 8)
	scope := scopeFor("proj-1", "agent-1", "run-1")

	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup

	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			for sequence := uint64(1); sequence <= 200; sequence++ {
				bus.Publish(observationEvent(scope, sequence))
			}
		}()
	}

	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			for range 50 {
				subscription, err := bus.Subscribe(context.Background(), RealtimeFilter{})
				if err != nil {
					continue // capacity or closed, both fine here
				}
				select {
				case <-subscription.Events():
				default:
				}
				subscription.Close()
			}
		}()
	}

	workers.Add(1)
	go func() {
		defer workers.Done()
		start.Wait()
		for range 20 {
			bus.Close()
		}
	}()

	start.Done()
	workers.Wait()

	if err := bus.Close(); err != nil {
		t.Errorf("final Close() error = %v", err)
	}
	if bus.subscriberCount() != 0 {
		t.Errorf("subscriberCount() = %d, want 0", bus.subscriberCount())
	}
}

// ---------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------

// Publish is O(S) with S bounded by the subscriber limit, and carries no
// historical term. These measure that shape; there is no latency gate.

func benchmarkPublish(b *testing.B, subscribers int, filter RealtimeFilter) {
	bus := newRealtimeBus(realtimeQueueCapacity, realtimeMaxSubscribers)
	defer bus.Close()

	scope := scopeFor("proj-1", "agent-1", "run-1")
	var drains sync.WaitGroup
	for range subscribers {
		subscription, err := bus.Subscribe(context.Background(), filter)
		if err != nil {
			b.Fatalf("Subscribe() error = %v", err)
		}
		drains.Add(1)
		go func() {
			defer drains.Done()
			for range subscription.Events() {
			}
		}()
	}

	b.ReportAllocs()
	for b.Loop() {
		bus.Publish(observationEvent(scope, 1))
	}
	b.StopTimer()
	bus.Close()
	drains.Wait()
}

func BenchmarkRealtimePublish1Subscriber(b *testing.B) {
	benchmarkPublish(b, 1, RealtimeFilter{})
}

func BenchmarkRealtimePublish16Subscribers(b *testing.B) {
	benchmarkPublish(b, 16, RealtimeFilter{})
}

func BenchmarkRealtimePublish64Subscribers(b *testing.B) {
	benchmarkPublish(b, realtimeMaxSubscribers, RealtimeFilter{})
}

// Filtered: every subscriber watches a run the events do not belong to, so
// this measures the matching cost without enqueue.
func BenchmarkRealtimeFilteredPublish64Subscribers(b *testing.B) {
	benchmarkPublish(b, realtimeMaxSubscribers, RealtimeFilter{RunID: "run-elsewhere"})
}
