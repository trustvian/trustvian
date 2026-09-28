package otel_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/anomaly"
	"github.com/trustvian/trustvian/internal/fingerprint"
	trustvianotel "github.com/trustvian/trustvian/internal/otel"
	"github.com/trustvian/trustvian/internal/policy"
	"github.com/trustvian/trustvian/internal/trust"
)

// findAttr looks up key in attrs, returning its Value and whether it
// was found — a small test-only helper, not part of the adapter itself.
func findAttr(attrs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// decisionCoveringPolicy gates on Trust.Risk exactly like
// engine_test.go's riskGatedPolicy, extended to also reach
// REQUIRE_APPROVAL and BLOCK so this package's round-trip/attribute
// tests can exercise all four representative decisions the task calls
// for, not just ALLOW/ALERT.
func decisionCoveringPolicy() policy.Policy {
	return policy.Policy{
		Rules: []policy.Rule{
			{Name: "block-critical-risk", When: policy.Condition{MinRiskLevel: trust.RiskCritical}, Action: policy.DecisionBlock, Reason: "risk critical"},
			{Name: "approval-high-risk", When: policy.Condition{MinRiskLevel: trust.RiskHigh}, Action: policy.DecisionRequireApproval, Reason: "risk high"},
			{Name: "alert-medium-risk", When: policy.Condition{MinRiskLevel: trust.RiskMedium}, Action: policy.DecisionAlert, Reason: "risk medium"},
		},
		DefaultAction: policy.DecisionAllow,
		DefaultReason: "risk within tolerance",
	}
}

// coldStartEvent builds a first-ever-seen event for a fixed fingerprint
// shape, varying only Actor.IdentityConfidence. On a brand-new
// fingerprint, Anomaly.Confidence is 0, so effectiveAnomaly is 0 and
// TrustScore reduces to IdentityConfidence*(1-ContextRisk) —
// IdentityConfidence alone steers which RiskLevel bucket the event
// lands in under decisionCoveringPolicy's DefaultConfig thresholds,
// with no baseline warm-up loop required.
func coldStartEvent(id string, identityConfidence float64) event.Event {
	return event.Event{
		ID:        id,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Actor: event.Actor{
			ID:                 "svc-" + id,
			Type:               event.ActorTypeService,
			IdentityConfidence: identityConfidence,
		},
		Operation: event.Operation{Category: event.OperationCategoryHTTP, Name: "GET /x/" + id},
		Target:    event.Target{Name: "target-" + id},
		Context:   event.Context{Environment: "production"},
	}
}

// TestAttributesFromResultCoversAllDecisions builds a Result via a real
// Engine.Analyze call for each of ALLOW, ALERT, REQUIRE_APPROVAL, and
// BLOCK, converts each to attributes, and asserts every trustvian.*
// output key is present with the value actually on that Result — not a
// hand-computed expectation, so a formula change elsewhere would be
// caught here too.
func TestAttributesFromResultCoversAllDecisions(t *testing.T) {
	tests := []struct {
		name               string
		identityConfidence float64
		wantDecision       policy.Decision
	}{
		{"allow", 1.0, policy.DecisionAllow},
		{"alert", 0.7, policy.DecisionAlert},
		{"require_approval", 0.45, policy.DecisionRequireApproval},
		{"block", 0.2, policy.DecisionBlock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := trustvian.NewEngine(trustvian.WithPolicy(decisionCoveringPolicy()))
			ev := coldStartEvent(tt.name, tt.identityConfidence)

			result, err := engine.Analyze(context.Background(), ev)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			if result.Decision != tt.wantDecision {
				t.Fatalf("test setup: Decision = %v, want %v (adjust identityConfidence)", result.Decision, tt.wantDecision)
			}

			attrs := trustvianotel.AttributesFromResult(result)

			checks := []struct {
				key  string
				want any
			}{
				{trustvianotel.AttrAnomalyScore, result.Anomaly.Score},
				{trustvianotel.AttrTrustScore, result.Trust.Score},
				{trustvianotel.AttrRiskLevel, string(result.Trust.Risk)},
				{trustvianotel.AttrDecision, string(result.Decision)},
				{trustvianotel.AttrFingerprintID, result.Fingerprint.ID},
				// Task 075. A Result built here never passed through
				// EventFromSpan, so it carries no recorded fidelity and gets
				// "transport" — which is the honest answer rather than a
				// default: nothing proved a semantic identity.
				{trustvianotel.AttrFidelity, string(event.FidelityTransport)},
			}
			for _, c := range checks {
				v, ok := findAttr(attrs, c.key)
				if !ok {
					t.Errorf("attribute %q missing from AttributesFromResult output", c.key)
					continue
				}
				switch want := c.want.(type) {
				case float64:
					if got := v.AsFloat64(); got != want {
						t.Errorf("%s = %v, want %v", c.key, got, want)
					}
				case string:
					if got := v.AsString(); got != want {
						t.Errorf("%s = %q, want %q", c.key, got, want)
					}
				}
			}
			// The set is exact, not a minimum. This guard exists to stop
			// undocumented attributes accumulating — trustvian.behavior.id is the
			// one it was originally written against — so a new attribute belongs
			// in `checks` above with its reason, which is what makes adding one a
			// deliberate act rather than a silent one.
			if len(attrs) != len(checks) {
				var got []string
				for _, a := range attrs {
					got = append(got, string(a.Key))
				}
				t.Errorf("AttributesFromResult returned %d attributes, want exactly %d.\n"+
					"  got: %v\nA new attribute must be added to this test's `checks` "+
					"with the reason it exists; an unlisted one is the drift this "+
					"assertion prevents.", len(attrs), len(checks), got)
			}
		})
	}
}

// TestAttributesFromResultBoundaryScores pins exact formatting at the
// score range's edges (0.0 and 1.0) via a hand-built Result — this is
// deliberately not routed through Engine.Analyze, since the point here
// is AttributesFromResult's own derivation/formatting at boundary
// values, not pipeline behavior (mirrors how internal/trust's own
// TestExplain tests use a hand-built Trust for the same reason).
func TestAttributesFromResultBoundaryScores(t *testing.T) {
	tests := []struct {
		name    string
		anomaly float64
		trustSc float64
	}{
		{"both zero", 0.0, 0.0},
		{"both one", 1.0, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := trustvian.Result{
				Anomaly:     anomaly.Anomaly{Score: tt.anomaly, FingerprintID: "fp-boundary"},
				Trust:       trust.Trust{Score: tt.trustSc, Risk: trust.RiskLow},
				Decision:    policy.DecisionAllow,
				Fingerprint: fingerprint.Fingerprint{ID: "fp-boundary"},
			}

			attrs := trustvianotel.AttributesFromResult(result)

			if v, ok := findAttr(attrs, trustvianotel.AttrAnomalyScore); !ok || v.AsFloat64() != tt.anomaly {
				t.Errorf("%s = %v, ok=%v, want %v", trustvianotel.AttrAnomalyScore, v, ok, tt.anomaly)
			}
			if v, ok := findAttr(attrs, trustvianotel.AttrTrustScore); !ok || v.AsFloat64() != tt.trustSc {
				t.Errorf("%s = %v, ok=%v, want %v", trustvianotel.AttrTrustScore, v, ok, tt.trustSc)
			}
		})
	}
}

// TestAttributesFromResultDoesNotMutateResult proves the guarantee
// stated in AttributesFromResult's doc comment, rather than leaving it
// an unverified claim — mirrors TestAnalyzeIsReadOnly's style in
// engine_test.go.
func TestAttributesFromResultDoesNotMutateResult(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithPolicy(decisionCoveringPolicy()))
	result, err := engine.Analyze(context.Background(), coldStartEvent("mut", 0.9))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	before := result

	_ = trustvianotel.AttributesFromResult(result)

	if !reflect.DeepEqual(result, before) {
		t.Fatalf("AttributesFromResult mutated its Result argument: before=%+v after=%+v", before, result)
	}
}

// TestAttributesFromResultRoundTripsThroughPipeline is the full
// composition check: a real span becomes an Event, is analyzed, and the
// resulting Result converts to attributes — with no manual glue code
// between any of the three steps. Mirrors
// TestEventFromSpanRoundTripsThroughPipeline's shape, extended one stage
// further.
func TestAttributesFromResultRoundTripsThroughPipeline(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(45 * time.Millisecond)

	span := recordSpan(t,
		testResource(t, "payment-gateway", "production"),
		trace.SpanKindServer,
		"POST /payment",
		[]attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String("POST"),
			semconv.ServicePeerNameKey.String("checkout-frontend"),
		},
		false, start, end,
	)

	ev := trustvianotel.EventFromSpan(span)

	engine := trustvian.NewEngine(trustvian.WithPolicy(decisionCoveringPolicy()))
	result, err := engine.Analyze(context.Background(), ev)
	if err != nil {
		t.Fatalf("Analyze() error = %v for an adapted span that should be a valid Event", err)
	}

	attrs := trustvianotel.AttributesFromResult(result)

	for _, key := range []string{
		trustvianotel.AttrAnomalyScore,
		trustvianotel.AttrTrustScore,
		trustvianotel.AttrRiskLevel,
		trustvianotel.AttrDecision,
		trustvianotel.AttrFingerprintID,
	} {
		if _, ok := findAttr(attrs, key); !ok {
			t.Errorf("round trip: attribute %q missing", key)
		}
	}
	if v, _ := findAttr(attrs, trustvianotel.AttrFingerprintID); v.AsString() != result.Fingerprint.ID {
		t.Errorf("round trip: %s = %q, want %q", trustvianotel.AttrFingerprintID, v.AsString(), result.Fingerprint.ID)
	}
}

// TestAttributesFromResultMalformedInboundOverrideFailsClosed proves
// the round trip fails safely, not silently, when inbound telemetry is
// malformed: an out-of-range trustvian.identity.confidence override
// (Validate() requires [0,1]) must stop the pipeline at Analyze with an
// error, never reach AttributesFromResult with a corrupted Result.
func TestAttributesFromResultMalformedInboundOverrideFailsClosed(t *testing.T) {
	span := recordSpan(t,
		testResource(t, "payment-gateway", "production"),
		trace.SpanKindServer,
		"POST /payment",
		[]attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String("POST"),
			attribute.Float64(trustvianotel.AttrIdentityConfidence, 5.0), // out of [0,1]
		},
		false, time.Now(), time.Now().Add(time.Millisecond),
	)

	ev := trustvianotel.EventFromSpan(span)

	engine := trustvian.NewEngine(trustvian.WithPolicy(decisionCoveringPolicy()))
	_, err := engine.Analyze(context.Background(), ev)
	if err == nil {
		t.Fatal("Analyze() error = nil for a malformed trustvian.identity.confidence override, want a Validate error")
	}
}
