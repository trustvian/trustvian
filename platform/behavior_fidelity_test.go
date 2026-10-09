package platform

// Task 081: per-behavior fidelity and layer counts — the accepted envelope
// pairs, the fold, the disagreement rule, the invariants and the stored form.

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/trustvian/trustvian/event"
)

// TestOnlyContradictoryPairsAreRefused walks every fidelity × layer pair the
// envelope can spell, including absent, and where each accepted one is counted.
func TestOnlyContradictoryPairsAreRefused(t *testing.T) {
	refused := map[[2]string]bool{
		{"transport", "model"}: true, {"transport", "tool"}: true, {"transport", "retrieval"}: true,
		{"semantic", "transport"}: true,
	}
	counted := map[[2]string]BehaviorFidelity{
		{"transport", "transport"}: {Transport: 1, LayerTransport: 1},
		{"semantic", ""}:           {Semantic: 1, LayerUnclassified: 1},
		{"semantic", "model"}:      {Semantic: 1, LayerModel: 1},
		{"semantic", "tool"}:       {Semantic: 1, LayerTool: 1},
		{"semantic", "retrieval"}:  {Semantic: 1, LayerRetrieval: 1},
	}
	for _, f := range []event.Fidelity{"", event.FidelityTransport, event.FidelitySemantic} {
		for _, l := range []event.Layer{event.LayerUnspecified, event.LayerModel, event.LayerTool,
			event.LayerRetrieval, event.LayerTransport} {
			key := [2]string{string(f), string(l)}
			err := validateFidelityPair(f, l)
			if (err != nil) != refused[key] {
				t.Errorf("(%q, %q): error %v, want refused %v", f, l, err, refused[key])
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidFidelityPair) {
					t.Errorf("(%q, %q): error %v is not ErrInvalidFidelityPair", f, l, err)
				}
				continue
			}
			got, err := BehaviorFidelity{}.observe(f, l)
			if err != nil {
				t.Fatal(err)
			}
			want, ok := counted[key]
			if !ok {
				// Neither field, or only one: the pair is not stated.
				want = unrecordedFidelity(1)
			}
			if got != want {
				t.Errorf("(%q, %q) counted %+v, want %+v", f, l, got, want)
			}
			if err := validateBehaviorFidelity(got, 1); err != nil {
				t.Errorf("(%q, %q) broke an invariant: %v", f, l, err)
			}
		}
	}
}

func TestFidelityFoldCountsEachObservationOnceInEachGroup(t *testing.T) {
	var b BehaviorFidelity
	for _, step := range []struct {
		f event.Fidelity
		l event.Layer
	}{
		{event.FidelitySemantic, event.LayerModel}, {event.FidelitySemantic, event.LayerTool},
		{event.FidelitySemantic, event.LayerRetrieval}, {event.FidelitySemantic, event.LayerUnspecified},
		{event.FidelityTransport, event.LayerTransport}, {event.FidelityTransport, event.LayerTransport},
		{"", event.LayerUnspecified},
	} {
		next, err := b.observe(step.f, step.l)
		if err != nil {
			t.Fatal(err)
		}
		b = next
	}
	want := BehaviorFidelity{Semantic: 4, Transport: 2, Unrecorded: 1,
		LayerModel: 1, LayerTool: 1, LayerRetrieval: 1, LayerTransport: 2, LayerUnclassified: 1, LayerUnrecorded: 1}
	if b != want {
		t.Fatalf("counts %+v, want %+v", b, want)
	}
	if err := validateBehaviorFidelity(b, 7); err != nil {
		t.Fatalf("a fold broke an invariant: %v", err)
	}
	// Overflow is refused and leaves the counts unchanged.
	full := BehaviorFidelity{Transport: math.MaxUint64, LayerTransport: math.MaxUint64}
	if got, err := full.observe(event.FidelityTransport, event.LayerTransport); !errors.Is(err, ErrBehaviorOverflow) || got != full {
		t.Fatalf("overflow: %+v, %v", got, err)
	}
}

// TestTheDisagreementRuleReportsTheLowestLevelSeen: all-semantic,
// all-transport, mixed, unrecorded and migrated entries, and one transport
// observation in a thousand.
func TestTheDisagreementRuleReportsTheLowestLevelSeen(t *testing.T) {
	for _, tt := range []struct {
		name  string
		b     BehaviorFidelity
		level FidelityLevel
		mixed bool
	}{
		{"all semantic", BehaviorFidelity{Semantic: 5, LayerTool: 5}, FidelityLevelSemantic, false},
		{"all transport", BehaviorFidelity{Transport: 5, LayerTransport: 5}, FidelityLevelTransport, false},
		{"semantic and transport", BehaviorFidelity{Semantic: 2, Transport: 3, LayerModel: 2, LayerTransport: 3},
			FidelityLevelTransport, true},
		{"unrecorded", BehaviorFidelity{Unrecorded: 4, LayerUnrecorded: 4}, FidelityLevelUnrecorded, false},
		{"migrated", unrecordedFidelity(9), FidelityLevelUnrecorded, false},
		{"semantic and unrecorded", BehaviorFidelity{Semantic: 1, Unrecorded: 1, LayerTool: 1, LayerUnrecorded: 1},
			FidelityLevelSemantic, true},
		{"transport and unrecorded", BehaviorFidelity{Transport: 1, Unrecorded: 1, LayerTransport: 1, LayerUnrecorded: 1},
			FidelityLevelTransport, true},
		{"one transport in a thousand", BehaviorFidelity{Semantic: 999, Transport: 1, LayerModel: 999, LayerTransport: 1},
			FidelityLevelTransport, true},
		{"nothing", BehaviorFidelity{}, FidelityLevelUnrecorded, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if level, mixed := tt.b.Reported(); level != tt.level || mixed != tt.mixed {
				t.Fatalf("Reported() = %s, mixed %v; want %s, mixed %v", level, mixed, tt.level, tt.mixed)
			}
		})
	}
}

// TestRunsAreSummedThenClassified: two runs, each pure, sum to a mixed
// behavior. Classifying each run first would report two pure levels.
func TestRunsAreSummedThenClassified(t *testing.T) {
	semanticRun := BehaviorFidelity{Semantic: 3, LayerModel: 3}
	transportRun := BehaviorFidelity{Transport: 1, LayerTransport: 1}
	sum, err := semanticRun.Add(transportRun)
	if err != nil {
		t.Fatal(err)
	}
	if level, mixed := sum.Reported(); level != FidelityLevelTransport || !mixed {
		t.Fatalf("summed = %s, mixed %v; want transport, mixed", level, mixed)
	}
	if _, err := (BehaviorFidelity{Semantic: math.MaxUint64}).Add(BehaviorFidelity{Semantic: 1}); !errors.Is(err, ErrBehaviorOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestBehaviorFidelityInvariantsRefuseImpossibleCounts(t *testing.T) {
	for _, tt := range []struct {
		name     string
		b        BehaviorFidelity
		mentions string
	}{
		{"fidelity short of observations", BehaviorFidelity{Semantic: 1, LayerModel: 1}, "fidelity counts total 1"},
		{"semantic layers short", BehaviorFidelity{Semantic: 3, LayerModel: 2, LayerTransport: 1}, "semantic observations are 3"},
		{"transport layer short", BehaviorFidelity{Transport: 3, LayerTransport: 2, LayerUnrecorded: 1}, "transport layer counts 2"},
		{"unrecorded layer off", BehaviorFidelity{Unrecorded: 3, LayerUnrecorded: 7}, "unrecorded layer counts 7"},
		{"overflow", BehaviorFidelity{Semantic: math.MaxUint64, Transport: 4}, "overflow"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBehaviorFidelity(tt.b, 3)
			if err == nil || !strings.Contains(err.Error(), tt.mentions) {
				t.Fatalf("validate = %v, want one mentioning %q", err, tt.mentions)
			}
		})
	}
	if err := validateBehaviorFidelity(unrecordedFidelity(3), 3); err != nil {
		t.Fatalf("a migrated entry fails its own invariants: %v", err)
	}
}

func TestFidelityCountsEncodingRoundTripsAndRefusesDamage(t *testing.T) {
	b := BehaviorFidelity{Semantic: 4, Transport: 2, Unrecorded: 1, LayerModel: 1, LayerTool: 1,
		LayerRetrieval: 1, LayerTransport: 2, LayerUnclassified: 1, LayerUnrecorded: 1}
	text := encodeFidelityCounts(b)
	if text != "4,2,1,1,1,1,2,1,1" {
		t.Fatalf("encoded %q", text)
	}
	got, err := decodeFidelityCounts(text)
	if err != nil || got != b {
		t.Fatalf("decoded %+v, %v", got, err)
	}
	if max := encodeFidelityCounts(BehaviorFidelity{Semantic: math.MaxUint64}); !strings.HasPrefix(max, "18446744073709551615,") {
		t.Fatalf("max uint64 encoded %q", max)
	}
	for _, damaged := range []string{"", "4,2,1,1,1,1,2,1", "4,2,1,1,1,1,2,1,1,0", "04,2,1,1,1,1,2,1,1",
		"-4,2,1,1,1,1,2,1,1", "4,2,1,1,1,1,2,1,", "4, 2,1,1,1,1,2,1,1", "18446744073709551616,0,0,0,0,0,0,0,0"} {
		if _, err := decodeFidelityCounts(damaged); !errors.Is(err, ErrStoreCorrupt) {
			t.Errorf("decode(%q) = %v, want ErrStoreCorrupt", damaged, err)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = decodeFidelityCounts(text) }); allocs != 0 {
		t.Fatalf("decode allocates %v times per call; it runs for every entry on every ingest", allocs)
	}
}
