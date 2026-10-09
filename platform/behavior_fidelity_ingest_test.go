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
