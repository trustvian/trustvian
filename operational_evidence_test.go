package trustvian_test

// Operational evidence on the record boundary (task 084): the projection, the
// wire, and the invariant that none of it is behavioral identity.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

func operationalEvent(id string, exec event.Execution, ctxFields event.Context) event.Event {
	ctxFields.Environment = "local"
	return event.Event{
		ID:        id,
		Timestamp: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
		Actor: event.Actor{
			ID: "support-agent", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryTool, Name: "export_customer",
			Direction: event.DirectionOutbound,
		},
		Target:    event.Target{Name: "export.localhost"},
		Context:   ctxFields,
		Execution: exec,
	}
}

func recordFor(t *testing.T, ev event.Event) trustvian.DecisionRecord {
	t.Helper()
	result, err := trustvian.NewEngine().Analyze(context.Background(), ev)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	return result.DecisionRecord()
}

// TestRecordCarriesDurationAvailability is acceptance criterion 2: present when
// stated, explicitly absent when not, and never defaulted.
func TestRecordCarriesDurationAvailability(t *testing.T) {
	tests := []struct {
		name string
		exec event.Execution
		want string
	}{
		{"a measured duration", event.Execution{DurationNanos: 1_500_000, DurationObserved: true}, "1500000"},
		{"a measured zero", event.Execution{DurationNanos: 0, DurationObserved: true}, "0"},
		{"unavailable", event.Execution{}, ""},
		{"unavailable with a stale value", event.Execution{DurationNanos: 99, DurationObserved: false}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := recordFor(t, operationalEvent("evt", tt.exec, event.Context{}))
			if record.DurationNanos != tt.want {
				t.Errorf("DurationNanos = %q, want %q", record.DurationNanos, tt.want)
			}

			nanos, observed := record.DurationNanosValue()
			if observed != (tt.want != "") {
				t.Errorf("DurationNanosValue observed = %v, want %v", observed, tt.want != "")
			}
			if observed && nanos != tt.exec.DurationNanos {
				t.Errorf("DurationNanosValue = %d, want %d", nanos, tt.exec.DurationNanos)
			}
		})
	}
}

// TestMeasuredZeroSurvivesTheWire is the case a naive omitempty would destroy.
func TestMeasuredZeroSurvivesTheWire(t *testing.T) {
	record := recordFor(t, operationalEvent("evt",
		event.Execution{DurationObserved: true}, event.Context{}))

	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded trustvian.DecisionRecord
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	nanos, observed := decoded.DurationNanosValue()
	if !observed {
		t.Fatalf("a measured zero came back unavailable:\n%s", encoded)
	}
	if nanos != 0 {
		t.Errorf("nanos = %d, want 0", nanos)
	}
}

// TestUnavailableDurationIsOmitted keeps the unavailable state indistinguishable
// from absence, which is what makes an older consumer correct.
func TestUnavailableDurationIsOmitted(t *testing.T) {
	record := recordFor(t, operationalEvent("evt", event.Execution{}, event.Context{}))
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"duration_nanos", "span_status", "parent_span_id", "span_lineage"} {
		if _, present := generic[key]; present {
			t.Errorf("%s is present for an observation that stated none: %s", key, encoded)
		}
	}
}

// TestOlderRecordWithoutTheFieldsStaysValid is the compatibility direction that
// matters: a record written before this task decodes and reads as unavailable.
func TestOlderRecordWithoutTheFieldsStaysValid(t *testing.T) {
	const older = `{
		"event_id":"evt-1","timestamp":"2026-09-29T12:00:00Z",
		"actor_id":"a","actor_type":"service","identity_confidence":1,
		"environment":"local",
		"behavior":{"actor_type":"service","operation_category":"http",
		            "operation_name":"GET","environment":"local"},
		"fingerprint_id":"fp","anomaly_score":0,"anomaly_confidence":0,
		"trust_score":1,"risk_level":"low","context_risk":0,
		"decision":"allow","policy_reason":"r","matched_default":true,
		"trace_id":"t","span_id":"s"
	}`
	var record trustvian.DecisionRecord
	if err := json.Unmarshal([]byte(older), &record); err != nil {
		t.Fatalf("a pre-084 record no longer decodes: %v", err)
	}
	if _, observed := record.DurationNanosValue(); observed {
		t.Error("a record with no duration reports one")
	}
	if record.SpanStatus != event.StatusUnavailable {
		t.Errorf("SpanStatus = %q, want unavailable", record.SpanStatus)
	}
	if record.SpanLineage != event.LineageUnspecified {
		t.Errorf("SpanLineage = %q, want unspecified", record.SpanLineage)
	}
	if record.ParentSpanID != "" {
		t.Errorf("ParentSpanID = %q, want empty", record.ParentSpanID)
	}
}

// TestCorrelationRoundTrips covers child, root and unavailable lineage.
func TestCorrelationRoundTrips(t *testing.T) {
	tests := []struct {
		name    string
		ctx     event.Context
		lineage event.SpanLineage
		parent  string
	}{
		{"a child names its parent",
			event.Context{SpanLineage: event.LineageChild, ParentSpanID: "0102030405060708"},
			event.LineageChild, "0102030405060708"},
		{"a root names none",
			event.Context{SpanLineage: event.LineageRoot},
			event.LineageRoot, ""},
		{"nothing established parentage",
			event.Context{},
			event.LineageUnspecified, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := recordFor(t, operationalEvent("evt", event.Execution{}, tt.ctx))
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded trustvian.DecisionRecord
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if decoded.SpanLineage != tt.lineage {
				t.Errorf("SpanLineage = %q, want %q", decoded.SpanLineage, tt.lineage)
			}
			if decoded.ParentSpanID != tt.parent {
				t.Errorf("ParentSpanID = %q, want %q", decoded.ParentSpanID, tt.parent)
			}
		})
	}
}

// TestOperationalEvidenceIsNeverBehavioralIdentity is acceptance criterion 4,
// and the invariant the whole design rests on.
func TestOperationalEvidenceIsNeverBehavioralIdentity(t *testing.T) {
	variants := []struct {
		name string
		exec event.Execution
		ctx  event.Context
	}{
		{"nothing stated", event.Execution{}, event.Context{}},
		{"a fast call", event.Execution{DurationNanos: 1, DurationObserved: true}, event.Context{}},
		{"a slow call", event.Execution{DurationNanos: 4_000_000_000, DurationObserved: true}, event.Context{}},
		{"a failed call", event.Execution{Status: event.StatusError}, event.Context{}},
		{"a successful call", event.Execution{Status: event.StatusOK}, event.Context{}},
		{"an unset call", event.Execution{Status: event.StatusUnset}, event.Context{}},
		{"a root", event.Execution{}, event.Context{SpanLineage: event.LineageRoot}},
		{"a child", event.Execution{},
			event.Context{SpanLineage: event.LineageChild, ParentSpanID: "aabbccddeeff0011"}},
		{"a different parent", event.Execution{},
			event.Context{SpanLineage: event.LineageChild, ParentSpanID: "1100ffeeddccbbaa"}},
	}

	var baseID string
	var baseBehavior trustvian.StableFeatures
	for i, v := range variants {
		record := recordFor(t, operationalEvent("evt", v.exec, v.ctx))
		if i == 0 {
			baseID, baseBehavior = record.FingerprintID, record.Behavior
			continue
		}
		if record.FingerprintID != baseID {
			t.Errorf("%s changed the fingerprint: %s != %s", v.name, record.FingerprintID, baseID)
		}
		if record.Behavior != baseBehavior {
			t.Errorf("%s changed StableFeatures:\n got %+v\nwant %+v",
				v.name, record.Behavior, baseBehavior)
		}
	}
}

// TestOperationalEvidenceDoesNotChangeDecisions is acceptance criterion 5.
func TestOperationalEvidenceDoesNotChangeDecisions(t *testing.T) {
	bare := trustvian.NewEngine()
	loaded := trustvian.NewEngine()

	for i := range 12 {
		id := "evt-" + string(rune('a'+i))

		a, err := bare.Analyze(context.Background(),
			operationalEvent(id, event.Execution{}, event.Context{}))
		if err != nil {
			t.Fatalf("Analyze(bare) error = %v", err)
		}
		if _, err := bare.Observe(context.Background(), a); err != nil {
			t.Fatalf("Observe(bare) error = %v", err)
		}

		b, err := loaded.Analyze(context.Background(), operationalEvent(id,
			event.Execution{DurationNanos: uint64(i) * 1_000_000, DurationObserved: true,
				Status: event.StatusError},
			event.Context{SpanLineage: event.LineageChild, ParentSpanID: "aabbccddeeff0011"}))
		if err != nil {
			t.Fatalf("Analyze(loaded) error = %v", err)
		}
		if _, err := loaded.Observe(context.Background(), b); err != nil {
			t.Fatalf("Observe(loaded) error = %v", err)
		}

		x, y := a.DecisionRecord(), b.DecisionRecord()
		if x.FingerprintID != y.FingerprintID {
			t.Fatalf("record %d: fingerprint diverged", i)
		}
		if x.AnomalyScore != y.AnomalyScore || x.TrustScore != y.TrustScore || x.Decision != y.Decision {
			t.Fatalf("record %d: decision evidence diverged: anomaly %v/%v trust %v/%v decision %q/%q",
				i, x.AnomalyScore, y.AnomalyScore, x.TrustScore, y.TrustScore, x.Decision, y.Decision)
		}
	}
}
