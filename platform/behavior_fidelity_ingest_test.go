package platform

// Task 081 through the real ingest path: what the envelope stated is what the
// stored entry counts, across a restart, and a contradictory pair is refused
// before anything is written.

import (
	"errors"
	"strconv"
	"testing"

	"github.com/trustvian/trustvian/event"
)

func TestIngestCountsTheEnvelopesFidelityAndLayer(t *testing.T) {
	store, path := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range []struct {
		f event.Fidelity
		l event.Layer
	}{
		{event.FidelitySemantic, event.LayerModel},
		{event.FidelitySemantic, event.LayerModel},
		{event.FidelityTransport, event.LayerTransport},
		{"", event.LayerUnspecified},                      // eval ingest, or the in-process SDK path
		{event.FidelityTransport, event.LayerUnspecified}, // fidelity alone: the pair is not stated
	} {
		if _, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
			RunID: run.ID(), Sequence: uint64(i + 1), BehavioralProfile: run.BehavioralProfile(),
			Fidelity: step.f, BehaviorLayer: step.l,
			Record: operationalTestRecord(run, "e"+strconv.Itoa(i), "1000", event.StatusOK),
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	want := BehaviorFidelity{Semantic: 2, Transport: 1, Unrecorded: 2,
		LayerModel: 2, LayerTransport: 1, LayerUnrecorded: 2}
	_, snapshot, err := store.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Entries()[0].Fidelity; got != want {
		t.Fatalf("stored %+v, want %+v", got, want)
	}
	store.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, after, err := reopened.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Entries()[0].Fidelity; got != want {
		t.Fatalf("after restart %+v, want %+v", got, want)
	}
	if level, mixed := after.Entries()[0].Fidelity.Reported(); level != FidelityLevelTransport || !mixed {
		t.Fatalf("reported %s, mixed %v", level, mixed)
	}
}

// capturingPublisher keeps every realtime event it is handed.
type capturingPublisher struct{ events []RealtimeEvent }

func (p *capturingPublisher) Publish(e RealtimeEvent) RealtimePublishResult {
	p.events = append(p.events, e)
	return RealtimePublishResult{}
}

// TestUnstatedFidelityIsUnrecordedInEvidence pins the control plane's two
// readings of one record whose envelope states no fidelity (finding F11 of
// #175): the realtime observation shows it as transport, and the behavior's
// persisted fidelity counts it unrecorded. They differ on purpose — a live view
// must always name a level, and evidence must not claim one nothing stated —
// and they never meet a record from the Trustvian Collector, which always
// states one. That half is pinned in the processor module by
// TestAnnotationsNeverLeaveFidelityUnstated and
// TestEveryEnvelopeStatesACountablePair; see processor/internal/evaluation's
// Annotations. A change to either reading has to revisit both.
func TestUnstatedFidelityIsUnrecordedInEvidence(t *testing.T) {
	store, _ := testStore(t)
	run := seedRunningRun(t, store)
	live := &capturingPublisher{}
	plane, err := NewControlPlane(store, store, store, WithRealtimePublisher(live))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
		RunID: run.ID(), Sequence: 1, BehavioralProfile: run.BehavioralProfile(),
		Record: operationalTestRecord(run, "e0", "1000", event.StatusOK),
	}); err != nil {
		t.Fatal(err)
	}

	_, snapshot, err := store.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatal(err)
	}
	stored := snapshot.Entries()[0].Fidelity
	if stored != unrecordedFidelity(1) {
		t.Fatalf("evidence counted an unstated fidelity as %+v, want it unrecorded", stored)
	}
	if level, _ := stored.Reported(); level != FidelityLevelUnrecorded {
		t.Fatalf("evidence reported an unstated fidelity as %s, want %s", level, FidelityLevelUnrecorded)
	}

	var observed []event.Fidelity
	for _, e := range live.events {
		if e.Kind == RealtimeObservationRecorded {
			observed = append(observed, e.Observation.Fidelity)
		}
	}
	if len(observed) != 1 || observed[0] != event.FidelityTransport {
		t.Fatalf("realtime observations carried fidelity %q, want one reading %q", observed, event.FidelityTransport)
	}
}

func TestIngestRefusesAContradictoryPairBeforeWriting(t *testing.T) {
	store, _ := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	for i, pair := range [][2]string{{"transport", "model"}, {"transport", "tool"}, {"transport", "retrieval"},
		{"semantic", "transport"}} {
		_, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
			// Sequence 1 every time: a refused record advances nothing.
			RunID: run.ID(), Sequence: 1, BehavioralProfile: run.BehavioralProfile(),
			Fidelity: event.Fidelity(pair[0]), BehaviorLayer: event.Layer(pair[1]),
			Record: operationalTestRecord(run, "e"+strconv.Itoa(i), "", event.StatusOK),
		})
		if !errors.Is(err, ErrInvalidDecisionRecord) || !errors.Is(err, ErrInvalidFidelityPair) {
			t.Fatalf("%v: error = %v, want an invalid record naming the pair", pair, err)
		}
	}
	if _, _, err := store.EvaluationEvidence(t.Context(), run.ID()); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("a refused record left evidence behind: %v", err)
	}
}

// TestInProcessObservationsStayUnrecorded: Observe, the path with no envelope,
// counts nothing it was not told.
func TestInProcessObservationsStayUnrecorded(t *testing.T) {
	run := operationalRun(t)
	c, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := c.Observe(operationalTestRecord(run, "e"+strconv.Itoa(i), "", event.StatusOK)); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.Snapshot().Entries()[0].Fidelity; got != unrecordedFidelity(2) {
		t.Fatalf("in-process counts %+v", got)
	}
}

// TestFidelityAndLayerAreNotBehavioralIdentity: the same records under every
// countable pair produce the same fingerprint, the same StableFeatures and the
// same entry in all but the counts. identity_fields_test.go pins the types.
func TestFidelityAndLayerAreNotBehavioralIdentity(t *testing.T) {
	run := operationalRun(t)
	var first BehaviorEntry
	for i, pair := range [][2]string{{"semantic", "model"}, {"semantic", "tool"}, {"transport", "transport"}, {"", ""}} {
		c, err := NewBehaviorCollector(run)
		if err != nil {
			t.Fatal(err)
		}
		facts := OperationalFacts{Fidelity: event.Fidelity(pair[0]), Layer: event.Layer(pair[1])}
		if err := c.ObserveOperational(operationalTestRecord(run, "e1", "", event.StatusOK), facts); err != nil {
			t.Fatal(err)
		}
		entry := c.Snapshot().Entries()[0]
		if i == 0 {
			first = entry
			continue
		}
		if entry.FingerprintID != first.FingerprintID || entry.Behavior != first.Behavior ||
			entry.Observations != first.Observations || entry.Operational != first.Operational {
			t.Fatalf("pair %v changed the entry's identity: %+v vs %+v", pair, entry, first)
		}
		if entry.Fidelity == first.Fidelity {
			t.Fatalf("pair %v was not counted", pair)
		}
	}
}
