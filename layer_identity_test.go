package trustvian_test

// The behavioral layer is a classification, never behavioral identity (task 083,
// ADR 0047).
//
// This is the assertion that makes the whole design safe. A display label that
// leaked into StableFeatures would re-fingerprint behaviors the moment a producer
// upgraded its instrumentation, discarding learned baselines and reporting the
// reset as new behavior — which is the failure the layer exists to avoid rather
// than to cause.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// layerEvent is one event with a chosen layer attribute and nothing else varied.
func layerEvent(id string, layer event.Layer) event.Event {
	attrs := map[string]any{}
	if layer != event.LayerUnspecified {
		attrs[event.AttrLayer] = string(layer)
	}
	return event.Event{
		ID:        id,
		Timestamp: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
		Actor: event.Actor{
			ID:                 "support-agent",
			Type:               event.ActorTypeAIAgent,
			IdentityConfidence: 0.9,
		},
		Operation: event.Operation{
			Category:  event.OperationCategoryTool,
			Name:      "export_customer",
			Direction: event.DirectionOutbound,
		},
		Target:     event.Target{Name: "export.localhost"},
		Attributes: attrs,
		Context:    event.Context{Environment: "local"},
	}
}

// TestLayerNeverReachesBehavioralIdentity varies only the layer and requires the
// fingerprint and the whole StableFeatures tuple to be unchanged.
func TestLayerNeverReachesBehavioralIdentity(t *testing.T) {
	engine := trustvian.NewEngine()

	layers := []event.Layer{
		event.LayerUnspecified,
		event.LayerModel,
		event.LayerTool,
		event.LayerRetrieval,
		event.LayerTransport,
	}

	var baseID string
	var baseBehavior trustvian.StableFeatures
	for i, layer := range layers {
		result, err := engine.Analyze(context.Background(), layerEvent("evt", layer))
		if err != nil {
			t.Fatalf("Analyze(layer=%q) error = %v", layer, err)
		}
		record := result.DecisionRecord()
		if i == 0 {
			baseID, baseBehavior = record.FingerprintID, record.Behavior
			continue
		}
		if record.FingerprintID != baseID {
			t.Errorf("layer %q changed the fingerprint: %s != %s (baseline reset)",
				layer, record.FingerprintID, baseID)
		}
		if record.Behavior != baseBehavior {
			t.Errorf("layer %q changed StableFeatures:\n got %+v\nwant %+v",
				layer, record.Behavior, baseBehavior)
		}
	}
}

// TestLayerIsNotOnTheDecisionRecord pins the boundary the other way: the layer
// travels beside a record, never inside it, so nothing can fingerprint it later
// by accident.
func TestLayerIsNotOnTheDecisionRecord(t *testing.T) {
	engine := trustvian.NewEngine()
	result, err := engine.Analyze(context.Background(), layerEvent("evt", event.LayerTool))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	encoded, err := json.Marshal(result.DecisionRecord())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, needle := range []string{"behavior_layer", "trustvian.behavior.layer", "\"layer\""} {
		if strings.Contains(string(encoded), needle) {
			t.Errorf("DecisionRecord carries %q; the layer must ride beside the record, "+
				"not inside it:\n%s", needle, encoded)
		}
	}
}

// TestLayerAttributeDoesNotDisturbLearning walks the gated learning loop with a
// layer present and requires the baseline to mature exactly as it would without
// one.
func TestLayerAttributeDoesNotDisturbLearning(t *testing.T) {
	withLayer := trustvian.NewEngine()
	without := trustvian.NewEngine()

	var lastWith, lastWithout trustvian.Result
	for i := range 12 {
		id := "evt-" + string(rune('a'+i))

		plain := layerEvent(id, event.LayerUnspecified)
		delete(plain.Attributes, event.AttrLayer)
		r1, err := without.Analyze(context.Background(), plain)
		if err != nil {
			t.Fatalf("Analyze(plain) error = %v", err)
		}
		if _, err := without.Observe(context.Background(), r1); err != nil {
			t.Fatalf("Observe(plain) error = %v", err)
		}

		r2, err := withLayer.Analyze(context.Background(), layerEvent(id, event.LayerTool))
		if err != nil {
			t.Fatalf("Analyze(layered) error = %v", err)
		}
		if _, err := withLayer.Observe(context.Background(), r2); err != nil {
			t.Fatalf("Observe(layered) error = %v", err)
		}
		lastWith, lastWithout = r2, r1
	}

	a, b := lastWith.DecisionRecord(), lastWithout.DecisionRecord()
	if a.FingerprintID != b.FingerprintID {
		t.Errorf("fingerprint diverged after learning: %s != %s", a.FingerprintID, b.FingerprintID)
	}
	if a.AnomalyScore != b.AnomalyScore || a.TrustScore != b.TrustScore {
		t.Errorf("scores diverged: anomaly %v/%v trust %v/%v",
			a.AnomalyScore, b.AnomalyScore, a.TrustScore, b.TrustScore)
	}
	if a.Decision != b.Decision {
		t.Errorf("decision diverged: %q != %q", a.Decision, b.Decision)
	}
}
