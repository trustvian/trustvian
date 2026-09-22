package platform

// Realtime: bounded, ephemeral notification over committed state.
//
//	durable state is authoritative
//	realtime is notification
//
// That direction never reverses. The bus holds what has not yet been delivered
// and nothing else — no replay buffer, no offsets, no retention. A subscriber
// that falls behind is disconnected and recovers by resynchronizing from the
// authoritative control plane.
//
// Realtime is also advisory: Publish reports what happened and returns no
// error, because a committed mutation must never be reported as failed just
// because a notification could not be delivered.
//
// See docs/adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

const (
	// realtimeQueueCapacity bounds one subscriber's undelivered events.
	//
	// Fixed rather than configurable: an option accepting MaxInt would let a
	// caller opt out of the bound while this code still claimed to have one.
	// Tests reach the internal constructor for smaller values.
	realtimeQueueCapacity = 64

	// realtimeMaxSubscribers bounds live subscriptions.
	//
	// A bounded queue with unlimited subscribers is still unbounded memory:
	// the cost is subscribers x queue, so both halves are capped.
	realtimeMaxSubscribers = 64
)

var (
	// ErrRealtimeClosed reports a bus that has been closed.
	ErrRealtimeClosed = errors.New("platform: realtime bus is closed")

	// ErrRealtimeCapacity reports the subscriber limit.
	//
	// An existing subscription is never evicted to admit a new one: a caller
	// should not be able to disconnect somebody else's stream by connecting.
	ErrRealtimeCapacity = errors.New("platform: realtime subscriber limit reached")
)

// ---------------------------------------------------------------------
// Event model
// ---------------------------------------------------------------------

// RealtimeEventKind is what happened.
//
// Only semantics the platform can prove. There is deliberately no
// policy_violation (a blocked decision is the policy engine working, and
// "violation" implies severity nothing models), no baseline_update (the
// platform does not own learned core state), and no gate_update (a gate
// result is a derived read under caller-supplied limits, not durable state).
type RealtimeEventKind string

const (
	RealtimeEvaluationCreated   RealtimeEventKind = "evaluation_created"
	RealtimeEvaluationStarted   RealtimeEventKind = "evaluation_started"
	RealtimeObservationRecorded RealtimeEventKind = "observation"
	RealtimeEvaluationCompleted RealtimeEventKind = "evaluation_completed"
	RealtimeEvaluationFailed    RealtimeEventKind = "evaluation_failed"
	RealtimeEvaluationCancelled RealtimeEventKind = "evaluation_cancelled"
)

// RealtimeScope is the immutable hierarchy an event belongs to.
//
// Carried on every event so a subscriber can filter without reading the
// database. Sound because the hierarchy cannot change: task 052 offers no
// ChangeAgent, so a candidate's agent and an agent's project are fixed once
// created.
type RealtimeScope struct {
	ProjectID   ProjectID
	AgentID     AgentID
	CandidateID CandidateID
	RunID       EvaluationRunID

	Environment       EnvironmentRef
	BehavioralProfile BehavioralProfileRef
}

// RealtimeEvaluation is the factual lifecycle state of a run.
//
// No passed, safe or promotable field. Completion means execution ended —
// task 052 drew that line and task 056 kept it.
type RealtimeEvaluation struct {
	Status        RunStatus
	CreatedAt     time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	FailureReason string
}

// RealtimeObservation is a bounded projection of one applied record.
//
// Not the record itself. Streaming DecisionRecord would couple realtime to
// that compatibility surface, enlarge every queue with a Contributors slice no
// live consumer reads, and reopen the boundary task 050 drew.
//
// Absent on purpose: Event.Attributes, tool arguments, prompts, completions,
// arbitrary metadata, contributors, PolicyReason, and the raw record.
type RealtimeObservation struct {
	// Sequence and RecordCount come from the commit that produced them, so an
	// event never describes a state assembled from two different moments.
	Sequence    uint64
	RecordCount uint64

	// BehaviorComplete is false once the collector saturated at 512 distinct
	// behaviors. Behavioral evidence then no longer describes the whole run.
	BehaviorComplete bool

	FingerprintID string
	Behavior      trustvian.StableFeatures

	Decision       string
	RiskLevel      string
	ApprovalStatus event.ApprovalStatus

	TrustScore        float64
	AnomalyScore      float64
	AnomalyConfidence float64

	// NewBehavior reports that this fingerprint was not already represented
	// in the run's evidence before this record.
	//
	// True even when the snapshot had already saturated and cannot retain it:
	// that is a live factual observation, and nothing is evicted to make room.
	NewBehavior bool
}

// RealtimeEvent is one notification.
//
// Fixed-shape, with unused halves left zero rather than making the type
// variant — a closed struct is what keeps queue memory predictable and the
// wire schema reviewable. No maps, no unbounded slices, no interface payload.
type RealtimeEvent struct {
	Kind  RealtimeEventKind
	Scope RealtimeScope

	Evaluation  RealtimeEvaluation
	Observation RealtimeObservation
}

// RealtimeFilter narrows a subscription.
//
// An empty field is unconstrained; populated fields are ANDed. Three
// identifiers and nothing else: a regex or expression language would put a
// small query language on the wire contract and move matching into an
// unbounded surface.
type RealtimeFilter struct {
	ProjectID ProjectID
	AgentID   AgentID
	RunID     EvaluationRunID
}

// validate bounds the identifiers a subscription retains.
//
// A bounded subscriber count is not by itself a bounded memory claim: 64
// subscriptions each holding a megabyte of caller-supplied filter text is
// still attacker-controlled process memory. The filter is retained for the
// life of the subscription, so it has to be bounded on the way in.
//
// The same rules the domain already applies to identifiers, not a second
// policy: these values are opaque identifiers and nothing about being a
// filter makes them different ones.
//
// Empty stays valid and means unconstrained.
func (f RealtimeFilter) validate() error {
	for _, dimension := range []struct {
		field string
		value string
	}{
		{"realtime project filter", string(f.ProjectID)},
		{"realtime agent filter", string(f.AgentID)},
		{"realtime run filter", string(f.RunID)},
	} {
		if dimension.value == "" {
			continue
		}
		if err := validateID(dimension.field, dimension.value); err != nil {
			return err
		}
	}
	return nil
}

// matches reports whether an event satisfies the filter.
func (f RealtimeFilter) matches(scope RealtimeScope) bool {
	if f.ProjectID != "" && f.ProjectID != scope.ProjectID {
		return false
	}
	if f.AgentID != "" && f.AgentID != scope.AgentID {
		return false
	}
	if f.RunID != "" && f.RunID != scope.RunID {
		return false
	}
	return true
}

// RealtimePublishResult is what one publish did.
//
// A result rather than an error, deliberately. A caller must not be able to
// treat delivery as something to handle: the mutation already committed, and
// turning a notification problem into an operation failure would make a client
// retry a write that landed.
type RealtimePublishResult struct {
	// Delivered is how many subscriptions accepted the event.
	Delivered int

	// Disconnected is how many were removed because their queue was full.
	Disconnected int

	// Closed reports that the bus was already closed, so nothing was
	// delivered. Diagnostic only.
	Closed bool
}

// ---------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------

// RealtimePublisher accepts notifications about committed state.
//
// The control plane holds this. A transport must not: one that could publish
// could fabricate state a subscriber would believe.
type RealtimePublisher interface {
	Publish(event RealtimeEvent) RealtimePublishResult
}

// RealtimeSubscriber hands out bounded streams.
//
// A transport holds this and nothing more.
type RealtimeSubscriber interface {
	Subscribe(ctx context.Context, filter RealtimeFilter) (RealtimeSubscription, error)
}

// RealtimeSubscription is one bounded stream.
type RealtimeSubscription interface {
	// Events is closed when the subscription ends — by Close, by context
	// cancellation, by the bus closing, or because the subscriber fell behind.
	Events() <-chan RealtimeEvent

	// Close unsubscribes. Idempotent.
	Close() error
}

// ---------------------------------------------------------------------
// In-memory bus
// ---------------------------------------------------------------------

// InMemoryRealtimeBus is the local reference implementation.
//
// No dependency and no broker: this needs in-process notification with a
// bounded queue, and ADR 0023 requires a measurement before adding a broker.
// There is none to offer.
//
// Safe for concurrent use. There is no goroutine per publish, so nothing can
// reorder events; the only goroutine per subscription watches its context and
// performs no delivery.
type InMemoryRealtimeBus struct {
	queueCapacity  int
	maxSubscribers int

	mu          sync.Mutex
	closed      bool
	nextID      uint64
	subscribers map[uint64]*busSubscription
}

var (
	_ RealtimePublisher  = (*InMemoryRealtimeBus)(nil)
	_ RealtimeSubscriber = (*InMemoryRealtimeBus)(nil)
)

// NewInMemoryRealtimeBus returns a bus with the default bounds.
func NewInMemoryRealtimeBus() *InMemoryRealtimeBus {
	return newRealtimeBus(realtimeQueueCapacity, realtimeMaxSubscribers)
}

// newRealtimeBus is the internal constructor tests use for small bounds.
//
// Unexported so the bounds cannot be widened from outside: a public option
// accepting MaxInt would let a caller remove the bound this type exists for.
func newRealtimeBus(queueCapacity, maxSubscribers int) *InMemoryRealtimeBus {
	if queueCapacity < 1 {
		queueCapacity = 1
	}
	if maxSubscribers < 1 {
		maxSubscribers = 1
	}
	return &InMemoryRealtimeBus{
		queueCapacity:  queueCapacity,
		maxSubscribers: maxSubscribers,
		subscribers:    make(map[uint64]*busSubscription),
	}
}

// busSubscription is one registered stream.
type busSubscription struct {
	id     uint64
	bus    *InMemoryRealtimeBus
	filter RealtimeFilter
	events chan RealtimeEvent

	cancelWatch context.CancelFunc

	// closeOnce guards the channel close, so a Close racing a slow-consumer
	// disconnect or a bus close cannot close it twice.
	closeOnce sync.Once
}

func (s *busSubscription) Events() <-chan RealtimeEvent { return s.events }

// Close unsubscribes, idempotently.
func (s *busSubscription) Close() error {
	s.bus.remove(s.id)
	return nil
}

// finish closes the stream exactly once. The caller holds the bus lock, which
// is what makes this safe against a concurrent Publish: Publish is the only
// sender, and it cannot be sending while the lock is held here.
func (s *busSubscription) finish() {
	s.closeOnce.Do(func() {
		if s.cancelWatch != nil {
			s.cancelWatch()
		}
		close(s.events)
	})
}

// Subscribe registers a bounded stream.
//
// At the subscriber limit this fails rather than evicting anyone. Cancelling
// ctx unsubscribes.
func (b *InMemoryRealtimeBus) Subscribe(
	ctx context.Context, filter RealtimeFilter,
) (RealtimeSubscription, error) {
	if ctx == nil {
		return nil, errors.New("platform: realtime subscribe requires a context")
	}
	// Before the lock, before a slot, before a queue: an invalid filter must
	// cost nothing. Validating here rather than in a transport is what keeps
	// the bound true for a future CLI, TUI or any other direct caller.
	if err := filter.validate(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrRealtimeClosed
	}
	if len(b.subscribers) >= b.maxSubscribers {
		b.mu.Unlock()
		return nil, fmt.Errorf("%w: %d subscribers", ErrRealtimeCapacity, b.maxSubscribers)
	}

	b.nextID++
	subscription := &busSubscription{
		id:     b.nextID,
		bus:    b,
		filter: filter,
		events: make(chan RealtimeEvent, b.queueCapacity),
	}

	// One goroutine per subscription, doing no delivery work: it waits for
	// cancellation and unsubscribes. Bounded by maxSubscribers, and released
	// by finish().
	watchCtx, cancel := context.WithCancel(ctx)
	subscription.cancelWatch = cancel
	b.subscribers[subscription.id] = subscription
	b.mu.Unlock()

	go func() {
		<-watchCtx.Done()
		b.remove(subscription.id)
	}()

	return subscription, nil
}

// Publish fans one event out to matching subscriptions.
//
// Never blocks and never returns an error. A subscription whose queue is full
// is removed and its stream closed: blocking would add a stalled client's
// latency to every committed mutation, growing the queue would be unbounded
// memory, and dropping silently would leave a client believing it has a
// complete stream when it does not.
//
// O(S) with S bounded by maxSubscribers, and no historical term.
func (b *InMemoryRealtimeBus) Publish(e RealtimeEvent) RealtimePublishResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return RealtimePublishResult{Closed: true}
	}

	var result RealtimePublishResult
	var saturated []*busSubscription

	for _, subscription := range b.subscribers {
		// Filtering before enqueue, not in the transport. Otherwise a busy run
		// would fill the queue of a subscriber watching a quiet one, and the
		// isolation the bounds exist for would be nominal.
		if !subscription.filter.matches(e.Scope) {
			continue
		}
		select {
		case subscription.events <- e:
			result.Delivered++
		default:
			saturated = append(saturated, subscription)
		}
	}

	for _, subscription := range saturated {
		delete(b.subscribers, subscription.id)
		subscription.finish()
		result.Disconnected++
	}
	return result
}

// remove unsubscribes one stream if it is still registered.
func (b *InMemoryRealtimeBus) remove(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subscription, live := b.subscribers[id]
	if !live {
		return
	}
	delete(b.subscribers, id)
	subscription.finish()
}

// Close disconnects every subscription. Idempotent.
func (b *InMemoryRealtimeBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}
	b.closed = true
	for id, subscription := range b.subscribers {
		delete(b.subscribers, id)
		subscription.finish()
	}
	return nil
}

// subscriberCount is for tests: it proves slots are reclaimed rather than
// leaked.
func (b *InMemoryRealtimeBus) subscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

// knows reports whether a collector already holds this fingerprint.
//
// Used to decide RealtimeObservation.NewBehavior before a record is folded in,
// from the trusted snapshot the collector was restored from. Unexported: it
// answers a question about bounded evidence, not a capability callers need.
func (c *BehaviorCollector) knows(fingerprintID string) bool {
	if c == nil || !c.bound {
		return false
	}
	_, present := c.entries[fingerprintID]
	return present
}
