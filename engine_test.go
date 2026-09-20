package trustvian_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/anomaly"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/policy"
	"github.com/trustvian/trustvian/internal/store"
	"github.com/trustvian/trustvian/internal/trust"
)

func paymentEvent(latencyMS float64, id string) event.Event {
	return paymentEventAt(latencyMS, id, time.Now())
}

// paymentEventAt is paymentEvent with an explicit Timestamp. Tests that
// call it repeatedly to build up a mature baseline (see
// TestObserveLearnsOnlyFromEligibleDecisions and
// TestAnalyzeNormalBehaviorIsAllowed) must space those timestamps
// realistically (e.g. via a fixed clock stepped by a constant interval,
// not consecutive time.Now() calls within a tight Go loop) — the
// frequency_deviation signal (task 004) treats the wall-clock gap between
// consecutive calls as the actor's inter-request rate, and a tight loop's
// microsecond-scale, GC/scheduler-jittery gaps look nothing like a stable
// rate even though the loop is "doing the same thing" every iteration.
func paymentEventAt(latencyMS float64, id string, ts time.Time) event.Event {
	return event.Event{
		ID:        id,
		Timestamp: ts,
		Actor: event.Actor{
			ID:                 "svc-payment",
			Type:               event.ActorTypeService,
			IdentityConfidence: 0.98,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryDB,
			Name:     "SELECT accounts",
		},
		Target:  event.Target{Name: "payment-db"},
		Context: event.Context{Environment: "production"},
		Attributes: map[string]any{
			"duration_ms": latencyMS,
		},
	}
}

// riskGatedPolicy blocks on high risk, alerts on medium, and otherwise
// allows — a realistic minimal policy for end-to-end tests.
func riskGatedPolicy() policy.Policy {
	return policy.Policy{
		Rules: []policy.Rule{
			{Name: "block-high-risk", When: policy.Condition{MinRiskLevel: trust.RiskHigh}, Action: policy.DecisionBlock, Reason: "risk too high"},
			{Name: "alert-medium-risk", When: policy.Condition{MinRiskLevel: trust.RiskMedium}, Action: policy.DecisionAlert, Reason: "elevated risk"},
		},
		DefaultAction: policy.DecisionAllow,
		DefaultReason: "risk within tolerance",
	}
}

func TestAnalyzeInvalidEventReturnsError(t *testing.T) {
	engine := trustvian.NewEngine()

	_, err := engine.Analyze(context.Background(), event.Event{})
	if err == nil {
		t.Fatalf("Analyze() error = nil for an invalid (zero-value) event, want an error")
	}
}

func TestAnalyzeIsReadOnly(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))
	ctx := context.Background()

	for i := range 10 {
		if _, err := engine.Analyze(ctx, paymentEvent(10, "evt")); err != nil {
			t.Fatalf("Analyze() call %d: error = %v", i, err)
		}
	}

	// Never called Observe: a fresh Analyze should behave exactly as the
	// very first one did (cold start), proving Analyze never wrote to
	// the Baseline.
	result, err := engine.Analyze(ctx, paymentEvent(10, "evt-final"))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Anomaly.Confidence != 0 {
		t.Fatalf("Anomaly.Confidence = %v after 10 Analyze-only calls, want 0 (Analyze must never learn)", result.Anomaly.Confidence)
	}
}

func TestObserveLearnsOnlyFromEligibleDecisions(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))
	ctx := context.Background()

	// Build up a mature, familiar baseline via Analyze+Observe: these
	// events are unremarkable, so risk stays low. Decision may
	// transiently be ALERT rather than ALLOW early on (partial maturity
	// still contributes some novelty signal) — that is expected and must
	// still be eligible for learning, or the fingerprint could never
	// mature past it. Only that it eventually settles to ALLOW matters.
	var result trustvian.Result
	var err error
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 30 {
		clock = clock.Add(time.Second)
		result, err = engine.Analyze(ctx, paymentEventAt(10, "warm-up", clock))
		if err != nil {
			t.Fatalf("Analyze() call %d: error = %v", i, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe() call %d: error = %v", i, err)
		}
	}

	mature, err := engine.Analyze(ctx, paymentEventAt(10, "mature-check", clock.Add(time.Second)))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if mature.Anomaly.Confidence < 0.99 {
		t.Fatalf("Anomaly.Confidence = %v after 30 eligible Observe calls, want ~1", mature.Anomaly.Confidence)
	}
	if mature.Decision != policy.DecisionAllow {
		t.Fatalf("Decision = %q once fully mature, want %q (any transient ALERT during warm-up must settle)", mature.Decision, policy.DecisionAllow)
	}

	// Now feed a wildly anomalous event for a brand-new actor/target
	// that should be BLOCKed, and confirm Observe does NOT learn from
	// it: the fingerprint must stay entirely unknown afterward.
	blockedEvent := event.Event{
		ID:        "attack",
		Timestamp: time.Now(),
		Actor:     event.Actor{ID: "svc-payment", Type: event.ActorTypeService, IdentityConfidence: 0.1},
		Operation: event.Operation{Category: event.OperationCategoryExternal, Name: "POST /exfiltrate"},
		Target:    event.Target{Name: "unknown-external-host"},
		Context:   event.Context{Environment: "production"},
	}
	blockedResult, err := engine.Analyze(ctx, blockedEvent)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if blockedResult.Decision != policy.DecisionBlock {
		t.Fatalf("Decision = %q, want %q (test setup expects this event to be blocked)", blockedResult.Decision, policy.DecisionBlock)
	}
	learned, err := engine.Observe(ctx, blockedResult)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if learned {
		t.Fatalf("Observe() learned = true for a BLOCKed event, want false")
	}

	recheck, err := engine.Analyze(ctx, blockedEvent)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if recheck.Anomaly.Confidence != 0 {
		t.Fatalf("Anomaly.Confidence = %v after Observing a BLOCKed event, want 0 (must not have learned)", recheck.Anomaly.Confidence)
	}
}

func TestAnalyzeColdStartDoesNotFalselyBlock(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	// First-ever event from a high-identity-confidence actor, no
	// history, no context risk: cold start should not push this into
	// BLOCK territory end-to-end.
	result, err := engine.Analyze(context.Background(), paymentEvent(10, "first-ever"))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Anomaly.Confidence != 0 {
		t.Fatalf("Anomaly.Confidence = %v for a first-ever event, want 0", result.Anomaly.Confidence)
	}
	if result.Decision == policy.DecisionBlock {
		t.Fatalf("Decision = %q for a benign first-ever event from a trusted identity, want anything but BLOCK", result.Decision)
	}
}

func TestAnalyzeSensitiveTargetFloorEndToEnd(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.SensitiveTargetFloor = map[string]float64{"secrets-manager": 0.9}

	secretsEvent := func(id string) event.Event {
		return event.Event{
			ID:        id,
			Timestamp: time.Now(),
			Actor:     event.Actor{ID: "svc-payment", Type: event.ActorTypeService, IdentityConfidence: 0.99},
			Operation: event.Operation{Category: event.OperationCategoryExternal, Name: "GET /secret"},
			Target:    event.Target{Name: "secrets-manager"},
			Context:   event.Context{Environment: "production"},
			Attributes: map[string]any{
				"duration_ms": float64(5),
			},
		}
	}

	// Make the fingerprint maximally familiar by seeding the store
	// directly, not through engine.Observe's gated learning loop: a
	// SensitiveTargetFloor this high keeps this exact scenario at BLOCK
	// even once mature (that's what this test is checking), and BLOCK is
	// correctly ineligible for learning — so the gated loop could never
	// reach full maturity here by construction. That's Observe's
	// anti-poisoning gate working as intended, not a way to build this
	// fixture.
	sample := secretsEvent("seed")
	feat := features.Extract(sample)
	fp := fingerprint.Compute(feat.Stable)
	key := baseline.Key{ActorID: sample.Actor.ID, Environment: sample.Context.Environment}
	seededStore := store.NewInMemory()
	for range 50 {
		if _, _, err := seededStore.Observe(ctx, key, fp, feat.Volatile, time.Now()); err != nil {
			t.Fatalf("seed Observe() error = %v", err)
		}
	}

	engine := trustvian.NewEngine(
		trustvian.WithStore(seededStore),
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(anomalyCfg),
	)

	final, err := engine.Analyze(ctx, secretsEvent("final"))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if final.Anomaly.Confidence < 0.99 {
		t.Fatalf("Anomaly.Confidence = %v, want ~1 (this scenario is deliberately maximally familiar)", final.Anomaly.Confidence)
	}
	if final.Decision != policy.DecisionBlock {
		t.Fatalf("Decision = %q for a sensitive-target access, want %q despite full familiarity", final.Decision, policy.DecisionBlock)
	}
	if final.Explanation.Reason == "" {
		t.Fatalf("Explanation.Reason is empty")
	}
}

func TestAnalyzeNormalBehaviorIsAllowed(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))
	ctx := context.Background()

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 30 {
		clock = clock.Add(time.Second)
		result, err := engine.Analyze(ctx, paymentEventAt(10, "warm-up", clock))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	result, err := engine.Analyze(ctx, paymentEventAt(10, "steady-state", clock.Add(time.Second)))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Decision != policy.DecisionAllow {
		t.Fatalf("Decision = %q for behavior matching a mature baseline, want %q", result.Decision, policy.DecisionAllow)
	}
	if result.Trust.Risk != trust.RiskLow {
		t.Fatalf("Trust.Risk = %q, want %q", result.Trust.Risk, trust.RiskLow)
	}
}

// TestAnalyzeOrdinaryCadenceJitterDoesNotElevateRisk is the end-to-end
// counterpart to internal/anomaly's TestScoreFrequencyDeviationZScorePath.
// TestAnalyzeNormalBehaviorIsAllowed above learns from a *perfectly* flat
// one-second cadence, which no real caller produces; this one learns from
// a cadence with ordinary ±3ms jitter and then analyzes an event 5ms off
// the learned mean. Under the pre-fix defaults that ordinary event scored
// frequency_deviation ~0.79 × weight 0.6, reaching RiskMedium and firing
// the alert rule — roughly one in every five to ten wholly unremarkable
// events. It stays RiskLow now that FrequencyWeight defaults to 0.
func TestAnalyzeOrdinaryCadenceJitterDoesNotElevateRisk(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))
	ctx := context.Background()

	// A fixed pattern, not real randomness, so this test is exactly
	// reproducible — it asserts on a z-score, which is meaningless if
	// the learned stddev drifts between runs.
	jitterMS := []int{3, -2, 1, -3, 2, 0, -1, 3, -3, 1, 2, -2, 0, -1, 1}

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 50 {
		result, err := engine.Analyze(ctx, paymentEventAt(10, fmt.Sprintf("poll-%d", i), clock))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		clock = clock.Add(10*time.Second + time.Duration(jitterMS[i%len(jitterMS)])*time.Millisecond)
	}

	// One more entirely ordinary event: on cadence, 5ms off the learned
	// mean — well within what this actor's own jitter already produces.
	result, err := engine.Analyze(ctx, paymentEventAt(10, "ordinary", clock.Add(5*time.Millisecond)))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Trust.Risk != trust.RiskLow {
		t.Fatalf("Trust.Risk = %q for an ordinary on-cadence event with realistic jitter, want %q (anomaly %.3f, contributors %+v)",
			result.Trust.Risk, trust.RiskLow, result.Anomaly.Score, result.Anomaly.Contributors)
	}
	if result.Decision != policy.DecisionAllow {
		t.Fatalf("Decision = %q for an ordinary on-cadence event with realistic jitter, want %q", result.Decision, policy.DecisionAllow)
	}
}

func TestNewEngineDefaultsProduceValidResults(t *testing.T) {
	engine := trustvian.NewEngine() // no options at all

	result, err := engine.Analyze(context.Background(), paymentEvent(10, "defaults"))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Decision == "" {
		t.Fatalf("Decision is empty with default configuration")
	}
	if result.Explanation.Reason == "" {
		t.Fatalf("Explanation.Reason is empty with default configuration")
	}
}

func TestWithStoreUsesProvidedStore(t *testing.T) {
	custom := &countingStore{}
	engine := trustvian.NewEngine(trustvian.WithStore(custom))
	ctx := context.Background()

	result, err := engine.Analyze(ctx, paymentEvent(10, "evt"))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if custom.gets != 1 {
		t.Fatalf("custom store Get calls = %d, want 1 (Analyze must use the configured Store)", custom.gets)
	}

	if _, err := engine.Observe(ctx, result); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if custom.observes != 1 {
		t.Fatalf("custom store Observe calls = %d, want 1", custom.observes)
	}
}

// secretsToolPolicy blocks AI agents from touching tools whose
// tool.category attribute is "secrets" — the spec's own worked
// example for Condition.Attributes matching.
func secretsToolPolicy() policy.Policy {
	return policy.Policy{
		Rules: []policy.Rule{
			{
				Name: "block-ai-agent-secrets-access",
				When: policy.Condition{
					ActorType:  event.ActorTypeAIAgent,
					Attributes: map[string]string{"tool.category": "secrets"},
				},
				Action: policy.DecisionBlock,
				Reason: "AI agents may not access secrets-category tools",
			},
		},
		DefaultAction: policy.DecisionAllow,
		DefaultReason: "no matching rule",
	}
}

func TestAnalyzeMatchesAttributeConditionEndToEnd(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithPolicy(secretsToolPolicy()))
	ctx := context.Background()

	ev := event.Event{
		ID:        "evt-secrets",
		Timestamp: time.Now(),
		Actor: event.Actor{
			ID:                 "agent-1",
			Type:               event.ActorTypeAIAgent,
			IdentityConfidence: 0.9,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryTool,
			Name:     "read-secret",
		},
		Target:  event.Target{Name: "secrets-manager"},
		Context: event.Context{Environment: "production"},
		Attributes: map[string]any{
			"tool.category": "secrets",
		},
	}

	result, err := engine.Analyze(ctx, ev)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Decision != policy.DecisionBlock {
		t.Fatalf("Decision = %q, want %q for an AI agent hitting a tool.category=secrets attribute", result.Decision, policy.DecisionBlock)
	}
}

// TestAnalyzeNegativeDurationDoesNotCorruptTrustScore is a security
// regression test (docs/tasks/012-security-tests.md): a negative
// duration_ms attribute is adversarial/malformed input that isn't
// rejected by event.Validate (only IdentityConfidence and enum fields are
// checked there), so it flows through features.Extract into
// internal/anomaly's latency z-score math as a negative time.Duration.
//
// This was traced through empirically rather than assumed safe: a
// negative current latency against a baseline with non-zero variance
// still yields a large but finite z-score ((currentNS-mean)/stddev is a
// well-defined finite division whenever stddev != 0, regardless of the
// sign of currentNS), which min(z/threshold, 1) then clamps to the
// signal's normal [0,1] range exactly like any other extreme deviation.
// No NaN or Inf propagates into Anomaly.Score or Trust.Score. This test
// pins that finding down as a regression rather than leaving it an
// implicit assumption.
func TestAnalyzeNegativeDurationDoesNotCorruptTrustScore(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	// Warm up with varying (but bounded) latencies so the baseline
	// accumulates non-zero LatencyVariance — a constant latency would
	// take latencySignal's nearZeroStdDev branch instead, which never
	// exercises the division this test is targeting.
	latencies := []float64{10, 12, 9, 11, 8, 13, 10, 9, 12, 11, 10, 9, 13, 8, 11, 10, 12, 9, 11, 10, 8, 13, 9, 11, 10, 12, 9, 11, 10, 12}
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, l := range latencies {
		clock = clock.Add(time.Second)
		result, err := engine.Analyze(ctx, paymentEventAt(l, fmt.Sprintf("warm-up-%d", i), clock))
		if err != nil {
			t.Fatalf("Analyze() warm-up %d: error = %v", i, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe() warm-up %d: error = %v", i, err)
		}
	}

	// A negative duration_ms is not something a well-behaved producer
	// sends, but Validate does not reject it — this event must still be
	// handled safely all the way through Trust.Score.
	clock = clock.Add(time.Second)
	result, err := engine.Analyze(ctx, paymentEventAt(-500, "negative-duration", clock))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if math.IsNaN(result.Anomaly.Score) || math.IsInf(result.Anomaly.Score, 0) {
		t.Fatalf("Anomaly.Score = %v for a negative duration_ms, want a finite value in [0,1]", result.Anomaly.Score)
	}
	if result.Anomaly.Score < 0 || result.Anomaly.Score > 1 {
		t.Fatalf("Anomaly.Score = %v out of [0,1] range", result.Anomaly.Score)
	}
	if math.IsNaN(result.Trust.Score) || math.IsInf(result.Trust.Score, 0) {
		t.Fatalf("Trust.Score = %v for a negative duration_ms, want a finite value in [0,1]", result.Trust.Score)
	}
	if result.Trust.Score < 0 || result.Trust.Score > 1 {
		t.Fatalf("Trust.Score = %v out of [0,1] range", result.Trust.Score)
	}
}

// TestAnalyzeCrossActorIsolation proves, end-to-end through Engine, what
// baseline.Key's composite (ActorID, Environment) shape only implies by
// construction: two actors that produce an otherwise identical stable
// feature shape (same operation, same target, same environment) never
// share Baseline state. actor-a is matured over 30 observations; if
// actor-b's first-ever event for the exact same shape came back with any
// non-zero Confidence, that would mean actor-a's history leaked across
// the actor boundary.
func TestAnalyzeCrossActorIsolation(t *testing.T) {
	ctx := context.Background()
	e := trustvian.NewEngine()

	shape := func(actorID string) event.Event {
		return event.Event{
			ID: actorID + "-evt", Timestamp: time.Now(),
			Actor:     event.Actor{ID: actorID, Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryHTTP, Name: "GET /shared"},
			Target:    event.Target{Name: "shared-target"},
			Context:   event.Context{Environment: "prod"},
		}
	}

	for range 30 {
		r, err := e.Analyze(ctx, shape("actor-a"))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := e.Observe(ctx, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	// actor-b has never been observed for this identical shape — it must
	// still register full categorical novelty, proving actor-a's 30
	// observations never leaked into actor-b's Baseline.
	rB, err := e.Analyze(ctx, shape("actor-b"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rB.Anomaly.Confidence != 0 {
		t.Errorf("actor-b Confidence = %v, want 0 (no baseline should exist yet — cross-actor leak?)", rB.Anomaly.Confidence)
	}
}

// TestAnalyzeLargeAttributesMapDoesNotPanic is a resource-exhaustion
// smoke test (docs/tasks/012-security-tests.md): a producer sending an
// Attributes map with an unusually large number of keys must not panic
// or error Analyze — only duration_ms/error are ever read out of it, so
// cost should stay proportional to what's actually consumed, not to the
// map's total size.
func TestAnalyzeLargeAttributesMapDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	e := trustvian.NewEngine()
	attrs := make(map[string]any, 100000)
	for i := range 100000 {
		attrs[fmt.Sprintf("key-%d", i)] = i
	}
	ev := event.Event{
		ID: "evt", Timestamp: time.Now(),
		Actor:      event.Actor{ID: "a", Type: event.ActorTypeService, IdentityConfidence: 1},
		Operation:  event.Operation{Category: event.OperationCategoryHTTP, Name: "GET /x"},
		Attributes: attrs,
	}
	if _, err := e.Analyze(ctx, ev); err != nil {
		t.Fatalf("Analyze() error = %v, want nil (large Attributes must not error or panic)", err)
	}
}

// TestFingerprintFloodStaysBoundedEndToEnd replaces this file's former
// TestObserveUnboundedFingerprintsDoesNotPanic, whose name and assertion
// both described the defect task 046 fixed: it drove 5,000 distinct
// fingerprints through the engine and asserted only that nothing panicked,
// which an unbounded implementation satisfies exactly as well as a bounded
// one.
//
// The scenario is the one that matters in production: a single actor whose
// operation name varies on every call — trivially arranged by anything that
// puts an identifier in a route — cannot grow its learned state without
// limit. This asserts the bound itself, through the real gated
// Analyze+Observe loop rather than against internal/baseline directly, so
// it also proves the pipeline keeps working while admission is refused:
// every event is still analyzed, still decided, and never errors.
func TestFingerprintFloodStaysBoundedEndToEnd(t *testing.T) {
	ctx := context.Background()
	st := store.NewInMemory()
	e := trustvian.NewEngine(trustvian.WithStore(st))

	key := baseline.Key{ActorID: "actor-flood", Environment: "prod"}

	const flood = 5000
	for i := range flood {
		ev := event.Event{
			ID: fmt.Sprintf("evt-%d", i), Timestamp: time.Now(),
			Actor:     event.Actor{ID: "actor-flood", Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryHTTP, Name: fmt.Sprintf("GET /x/%d", i)},
			Context:   event.Context{Environment: "prod"},
		}
		r, err := e.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze() at i=%d: %v", i, err)
		}
		if r.Decision == "" {
			t.Fatalf("Analyze() at i=%d produced no decision", i)
		}
		if _, err := e.Observe(ctx, r); err != nil {
			t.Fatalf("Observe() at i=%d: %v", i, err)
		}
	}

	bl, ok := st.Get(ctx, key)
	if !ok {
		t.Fatal("no baseline was learned at all")
	}
	// 512 is internal/baseline's maxFingerprints. Stated as a literal here
	// on purpose: this is the externally observable guarantee, and a change
	// to it should fail a test that reads like the promise being made.
	if got := len(bl.Fingerprints); got != 512 {
		t.Fatalf("len(Fingerprints) = %d after %d distinct fingerprints, want 512", got, flood)
	}
}

// TestAnalyzeTransitionDeviationEndToEnd is v0.6's own foundational
// end-to-end test — the security scenario docs/tasks/025-sequence-analysis-foundation.md
// exists to make detectable: authenticate -> read -> update is this
// actor's normal path; authenticate -> read -> delete has never
// happened, even though "delete" itself is independently familiar (via
// a different predecessor, authenticate). Built through the real,
// gated Analyze+Observe loop — not a directly-seeded store — per
// .claude/rules/testing.md's "end-to-end tests are load-bearing"
// convention: every step here is ALLOW-shaped and genuinely
// constructible through the gated loop, so it must be, not seeded
// around it.
func TestAnalyzeTransitionDeviationEndToEnd(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.TransitionWeight = 0.7 // opt-in: default is 0

	actorEvent := func(id, operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        id,
			Timestamp: ts,
			Actor:     event.Actor{ID: "svc-crm", Type: event.ActorTypeService, IdentityConfidence: 0.98},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "customer-db"},
			Context:   event.Context{Environment: "production"},
			Attributes: map[string]any{
				"duration_ms": float64(5),
			},
		}
	}

	engine := trustvian.NewEngine(
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(anomalyCfg),
	)

	analyzeAndObserve := func(t *testing.T, id, operation string, ts time.Time) trustvian.Result {
		t.Helper()
		result, err := engine.Analyze(ctx, actorEvent(id, operation, ts))
		if err != nil {
			t.Fatalf("Analyze(%s) error = %v", operation, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe(%s) error = %v", operation, err)
		}
		return result
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time {
		now = now.Add(time.Second)
		return now
	}

	// Familiarize "delete" itself via an unrelated predecessor
	// (authenticate), so it is not novel on its own — isolating what
	// this test actually checks: the *transition* into it, not the
	// destination's own maturity.
	for i := range 25 {
		analyzeAndObserve(t, fmt.Sprintf("auth-%d", i), "authenticate", step())
		analyzeAndObserve(t, fmt.Sprintf("delete-seed-%d", i), "DELETE customer", step())
	}

	// This actor's actual normal path: read -> update, many times over.
	for i := range 25 {
		analyzeAndObserve(t, fmt.Sprintf("read-%d", i), "SELECT customer", step())
		analyzeAndObserve(t, fmt.Sprintf("update-%d", i), "UPDATE customer", step())
	}

	// One more read, observed, to become the real predecessor for the
	// event actually under test.
	analyzeAndObserve(t, "read-final", "SELECT customer", step())

	// read -> delete: never observed for this actor, even though
	// "delete" is independently familiar.
	deleteResult, err := engine.Analyze(ctx, actorEvent("delete-final", "DELETE customer", step()))
	if err != nil {
		t.Fatalf("Analyze(delete) error = %v", err)
	}

	var gotTransition, gotNovelty bool
	for _, c := range deleteResult.Anomaly.Contributors {
		switch c.Name {
		case "transition_deviation":
			gotTransition = true
		case "categorical_novelty":
			gotNovelty = true
		}
	}
	if !gotTransition {
		t.Fatalf("Anomaly.Contributors = %+v, want transition_deviation for the never-observed read->delete transition", deleteResult.Anomaly.Contributors)
	}
	if gotNovelty {
		t.Fatalf("Anomaly.Contributors = %+v, want no categorical_novelty — \"delete\" itself is independently familiar via authenticate->delete", deleteResult.Anomaly.Contributors)
	}

	// The actor's actual normal path, analyzed the same way, must not
	// carry transition_deviation.
	analyzeAndObserve(t, "read-final-2", "SELECT customer", step())
	familiarResult, err := engine.Analyze(ctx, actorEvent("update-familiar", "UPDATE customer", step()))
	if err != nil {
		t.Fatalf("Analyze(familiar update) error = %v", err)
	}
	for _, c := range familiarResult.Anomaly.Contributors {
		if c.Name == "transition_deviation" {
			t.Fatalf("Anomaly.Contributors = %+v, want no transition_deviation for the familiar read->update transition", familiarResult.Anomaly.Contributors)
		}
	}
}

// TestAnalyzeTransitionRarityEndToEnd is task 026's own central
// integration proof: a real actor whose normal path (read -> update)
// is common, and whose read -> delete path is rare-but-not-unseen
// (both destinations independently mature via other predecessors, so
// categorical_novelty/transition_deviation do not mask the rarity
// signal specifically under test), built entirely through the real,
// gated Analyze+Observe loop, flows all the way through Trust and
// Policy to a stricter Decision than the common path receives.
func TestAnalyzeTransitionRarityEndToEnd(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.TransitionRarityWeight = 0.9
	anomalyCfg.MinTransitionObservations = 20

	actorEvent := func(id, operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        id,
			Timestamp: ts,
			Actor:     event.Actor{ID: "svc-crm", Type: event.ActorTypeService, IdentityConfidence: 0.98},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "customer-db"},
			Context:   event.Context{Environment: "production"},
			Attributes: map[string]any{
				"duration_ms": float64(5),
			},
		}
	}

	engine := trustvian.NewEngine(
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(anomalyCfg),
	)

	analyzeAndObserve := func(t *testing.T, id, operation string, ts time.Time) trustvian.Result {
		t.Helper()
		result, err := engine.Analyze(ctx, actorEvent(id, operation, ts))
		if err != nil {
			t.Fatalf("Analyze(%s) error = %v", operation, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe(%s) error = %v", operation, err)
		}
		return result
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time {
		now = now.Add(time.Second)
		return now
	}

	// Independently familiarize "delete" via authenticate -> delete,
	// so it is not novel on its own — isolating rarity specifically.
	for i := range 25 {
		analyzeAndObserve(t, fmt.Sprintf("auth-%d", i), "authenticate", step())
		analyzeAndObserve(t, fmt.Sprintf("delete-seed-%d", i), "DELETE customer", step())
	}

	// This actor's real distribution: read -> update 95 times (common),
	// read -> delete 5 times (rare) — 100 total outgoing transitions
	// from read, well past MinTransitionObservations.
	for i := range 95 {
		analyzeAndObserve(t, fmt.Sprintf("read-common-%d", i), "SELECT customer", step())
		analyzeAndObserve(t, fmt.Sprintf("update-%d", i), "UPDATE customer", step())
	}
	for i := range 4 { // the 5th read->delete is the event under test
		analyzeAndObserve(t, fmt.Sprintf("read-rare-%d", i), "SELECT customer", step())
		analyzeAndObserve(t, fmt.Sprintf("delete-rare-%d", i), "DELETE customer", step())
	}

	// One more read, observed, as the real predecessor for the events
	// actually under test.
	analyzeAndObserve(t, "read-final", "SELECT customer", step())

	rareResult, err := engine.Analyze(ctx, actorEvent("delete-final", "DELETE customer", step()))
	if err != nil {
		t.Fatalf("Analyze(rare delete) error = %v", err)
	}
	var rareRarity float64
	foundRarity := false
	for _, c := range rareResult.Anomaly.Contributors {
		if c.Name == "transition_rarity" {
			rareRarity = c.Value
			foundRarity = true
		}
		if c.Name == "transition_deviation" {
			t.Fatalf("Anomaly.Contributors = %+v, want no transition_deviation — read->delete has been observed (5/100), just rarely", rareResult.Anomaly.Contributors)
		}
	}
	if !foundRarity {
		t.Fatalf("Anomaly.Contributors = %+v, want transition_rarity for a ~5%% transition frequency", rareResult.Anomaly.Contributors)
	}
	// The exact count is not asserted here: riskGatedPolicy() means some
	// warm-up observations may transiently score high enough to be
	// BLOCKed/ineligible for learning (see eligibleForLearning), so the
	// precise accumulated denominator is not deterministic from this
	// test's own setup alone. The exact formula is proven, with a
	// controlled fixture, by TestScoreMatchesDocumentedFormulaForTransitionRarity
	// in internal/anomaly/anomaly_test.go — this test only needs the
	// qualitative property that a ~1-in-20 transition reads as clearly
	// rare.
	if rareRarity < 0.5 {
		t.Fatalf("transition_rarity Value = %v, want > 0.5 (a roughly 1-in-20 transition should read as clearly rare)", rareRarity)
	}

	// The common path, analyzed the same way, must carry a much lower
	// (or absent) transition_rarity, and a strictly lower Trust.Score-derived
	// risk than the rare path.
	analyzeAndObserve(t, "read-final-2", "SELECT customer", step())
	commonResult, err := engine.Analyze(ctx, actorEvent("update-common", "UPDATE customer", step()))
	if err != nil {
		t.Fatalf("Analyze(common update) error = %v", err)
	}
	var commonRarity float64
	for _, c := range commonResult.Anomaly.Contributors {
		if c.Name == "transition_rarity" {
			commonRarity = c.Value
		}
	}
	if commonRarity >= rareRarity {
		t.Fatalf("common transition_rarity (%v) >= rare transition_rarity (%v), want strictly less", commonRarity, rareRarity)
	}
	if !rareResult.Trust.Risk.AtLeast(commonResult.Trust.Risk) {
		t.Errorf("rare path Risk = %q, common path Risk = %q — want the rare path at least as risky", rareResult.Trust.Risk, commonResult.Trust.Risk)
	}
}

// TestAnalyzeTransitionRarityCrossActorIsolation proves Actor A's
// learned transition frequency never leaks into Actor B's scoring for
// the nominally identical transition — the same isolation guarantee
// TestAnalyzeCrossActorIsolation already proves for categorical
// novelty, restated here for the new, actor-scoped
// OutgoingTransitionTotal/PredecessorCounts state.
func TestAnalyzeTransitionRarityCrossActorIsolation(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.TransitionRarityWeight = 0.9
	anomalyCfg.MinTransitionObservations = 20

	engine := trustvian.NewEngine(
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(anomalyCfg),
	)

	shape := func(actorID, operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        actorID + "-" + operation + "-" + ts.String(),
			Timestamp: ts,
			Actor:     event.Actor{ID: actorID, Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "shared-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// actor-a: read -> update is extremely common (30 times).
	for range 30 {
		r, err := engine.Analyze(ctx, shape("actor-a", "read", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := engine.Observe(ctx, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
		r, err = engine.Analyze(ctx, shape("actor-a", "update", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := engine.Observe(ctx, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	// actor-b has never been observed at all — the identical
	// read -> update transition, for actor-b, must show no rarity
	// evidence (no predecessor exists yet for a fresh actor).
	rB, err := engine.Analyze(ctx, shape("actor-b", "read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := engine.Observe(ctx, rB); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	rB2, err := engine.Analyze(ctx, shape("actor-b", "update", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasResultSignal(rB2, "transition_rarity") {
		t.Errorf("actor-b Contributors = %+v, want no transition_rarity — actor-a's 30 observations must not leak into actor-b's baseline", rB2.Anomaly.Contributors)
	}
}

// TestAnalyzeTransitionRarityScoresBeforeLearning proves Analyze never
// mutates OutgoingTransitionTotal/PredecessorCounts — the transition-
// rarity analogue of TestAnalyzeIsReadOnly. Calling Analyze many times
// on the same rare transition, without ever calling Observe, must
// produce the exact same rarity reading every time.
func TestAnalyzeTransitionRarityScoresBeforeLearning(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.TransitionRarityWeight = 0.9
	anomalyCfg.MinTransitionObservations = 20

	engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(anomalyCfg))

	shape := func(operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        operation + "-" + ts.String(),
			Timestamp: ts,
			Actor:     event.Actor{ID: "svc-order-only-learning", Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "order-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// Build a real 95/5 split via the gated loop, exactly as the main
	// end-to-end test does.
	for range 19 {
		r, err := engine.Analyze(ctx, shape("read", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		engine.Observe(ctx, r)
		r, err = engine.Analyze(ctx, shape("update", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		engine.Observe(ctx, r)
	}
	r, err := engine.Analyze(ctx, shape("read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	engine.Observe(ctx, r)
	r, err = engine.Analyze(ctx, shape("delete", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	engine.Observe(ctx, r)

	r, err = engine.Analyze(ctx, shape("read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	engine.Observe(ctx, r)

	// Now Analyze the rare (read -> delete) transition repeatedly,
	// without ever calling Observe again.
	eventTime := step()
	var firstRarity float64
	for i := range 10 {
		result, err := engine.Analyze(ctx, shape("delete", eventTime))
		if err != nil {
			t.Fatalf("Analyze() call %d: error = %v", i, err)
		}
		var rarity float64
		found := false
		for _, c := range result.Anomaly.Contributors {
			if c.Name == "transition_rarity" {
				rarity = c.Value
				found = true
			}
		}
		if !found {
			t.Fatalf("call %d: Contributors = %+v, want transition_rarity", i, result.Anomaly.Contributors)
		}
		if i == 0 {
			firstRarity = rarity
		} else if diff := rarity - firstRarity; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("call %d: transition_rarity Value = %v, want %v (unchanged across repeated Analyze-only calls — Analyze must never learn)", i, rarity, firstRarity)
		}
	}
}

// TestObserveTransitionRarityLearnsOnlyFromEligibleDecisions is task
// 026's own poisoning-guard regression test: an attacker repeating a
// BLOCKed transition must never be able to inflate
// OutgoingTransitionTotal/PredecessorCounts and "wear down" the
// transition-rarity signal, mirroring
// TestObserveLearnsOnlyFromEligibleDecisions's existing proof for the
// rest of the baseline. This is not new logic — it falls out entirely
// from Engine.Observe's existing eligibleForLearning gate, which
// Baseline.Observe (and therefore this task's new counters) sits
// behind unconditionally; this test exists to prove that inheritance
// held, not to add a new mechanism.
func TestObserveTransitionRarityLearnsOnlyFromEligibleDecisions(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	readEvent := func(id string, ts time.Time) event.Event {
		return event.Event{
			ID: id, Timestamp: ts,
			Actor:     event.Actor{ID: "svc-poison-test", Type: event.ActorTypeService, IdentityConfidence: 0.98},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: "SELECT accounts"},
			Target:    event.Target{Name: "accounts-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, err := engine.Analyze(ctx, readEvent("read-1", now))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := engine.Observe(ctx, r); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// A wildly anomalous event that should be BLOCKed, immediately
	// following the read above.
	blocked := event.Event{
		ID:        "attack",
		Timestamp: now.Add(time.Second),
		Actor:     event.Actor{ID: "svc-poison-test", Type: event.ActorTypeService, IdentityConfidence: 0.1},
		Operation: event.Operation{Category: event.OperationCategoryExternal, Name: "POST /exfiltrate"},
		Target:    event.Target{Name: "unknown-external-host"},
		Context:   event.Context{Environment: "production"},
	}
	blockedResult, err := engine.Analyze(ctx, blocked)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if blockedResult.Decision != policy.DecisionBlock {
		t.Fatalf("Decision = %q, want %q (test setup expects this event to be blocked)", blockedResult.Decision, policy.DecisionBlock)
	}

	// Repeat the BLOCKed transition many times — an attacker trying to
	// "train" read -> attack into looking common.
	for i := range 50 {
		attempt := blocked
		attempt.ID = fmt.Sprintf("attack-%d", i)
		attempt.Timestamp = now.Add(time.Duration(i+2) * time.Second)
		result, err := engine.Analyze(ctx, attempt)
		if err != nil {
			t.Fatalf("Analyze() attempt %d: error = %v", i, err)
		}
		if result.Decision != policy.DecisionBlock {
			t.Fatalf("attempt %d: Decision = %q, want %q", i, result.Decision, policy.DecisionBlock)
		}
		learned, err := engine.Observe(ctx, result)
		if err != nil {
			t.Fatalf("Observe() attempt %d: error = %v", i, err)
		}
		if learned {
			t.Fatalf("attempt %d: Observe() learned = true for a BLOCKed event, want false", i)
		}
	}

	// The read -> attack transition must still read as entirely
	// unseen: 50 repeated BLOCKed attempts must have contributed zero
	// transition observations.
	recheck, err := engine.Analyze(ctx, blocked)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !hasResultSignal(recheck, "transition_deviation") {
		t.Errorf("Contributors = %+v, want transition_deviation still present (the transition must still read as entirely unseen after 50 BLOCKed attempts)", recheck.Anomaly.Contributors)
	}
	if hasResultSignal(recheck, "transition_rarity") {
		t.Errorf("Contributors = %+v, want no transition_rarity — a BLOCKed transition must never accumulate enough learned observations to become \"rare but seen\"", recheck.Anomaly.Contributors)
	}
}

// TestAnalyzeNGramEndToEnd is task 027's own central integration proof,
// built entirely through the real, gated Analyze+Observe loop (per
// .claude/rules/testing.md's "end-to-end tests are load-bearing"
// convention), reproducing the task's own canonical example:
// authenticate -> read_customer is a familiar pairwise transition (via
// one warm-up path), read_customer -> export_customer is *also* a
// familiar pairwise transition (via a *different* warm-up path), yet
// the complete sequence authenticate -> read_customer -> export_customer
// has never occurred — proving ngram_deviation detects this
// higher-order novelty through the full
// Event -> Engine -> Result -> Trust -> Policy -> Decision pipeline,
// not just in isolated unit tests.
func TestAnalyzeNGramEndToEnd(t *testing.T) {
	ctx := context.Background()

	actorEvent := func(id, operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        id,
			Timestamp: ts,
			Actor:     event.Actor{ID: "svc-crm", Type: event.ActorTypeService, IdentityConfidence: 0.98},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "customer-db"},
			Context:   event.Context{Environment: "production"},
			Attributes: map[string]any{
				"duration_ms": float64(5),
			},
		}
	}

	// Warm up with NGramWeight at its default (0) — the identical
	// "ships opt-in" precedent every other v0.6 signal already
	// established, applied here for a structural reason specific to
	// this signal: unlike transition_rarity (gated by minimum support,
	// so inert during early warm-up), ngram_deviation fires at full
	// strength on the *first* occurrence of any genuinely new 3-gram —
	// which is exactly what legitimate warm-up naturally produces.
	// Enabling the weight during warm-up would make the signal under
	// test BLOCK its own warm-up data (verified empirically: every
	// warm-up step introducing a fresh predecessor scored RiskCritical
	// and was excluded from learning), which is a warm-up-fixture
	// artifact, not something about the underlying detector this test
	// needs to prove. A shared store.Store lets two Engine instances
	// (identical policy, differing only in NGramWeight) observe the
	// same evolving Baseline: warmupEngine learns network the same way
	// any deployment's real early traffic would, without NGramWeight
	// paying its own opt-in cost during exactly that period; engine
	// then scores the events actually under test with the signal
	// enabled — the "operator raises the weight once calibrated"
	// sequence this codebase already documents for every prior
	// opt-in signal, just exercised across two Engine values sharing
	// one Store rather than one Engine value whose Config field
	// changes over time (which the immutable-Engine, functional-options
	// design does not support, deliberately).
	sharedStore := store.NewInMemory()
	warmupEngine := trustvian.NewEngine(trustvian.WithStore(sharedStore), trustvian.WithPolicy(riskGatedPolicy()))

	scoredCfg := anomaly.DefaultConfig()
	scoredCfg.NGramWeight = 0.9
	engine := trustvian.NewEngine(
		trustvian.WithStore(sharedStore),
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(scoredCfg),
	)

	analyzeAndObserve := func(t *testing.T, id, operation string, ts time.Time) trustvian.Result {
		t.Helper()
		result, err := warmupEngine.Analyze(ctx, actorEvent(id, operation, ts))
		if err != nil {
			t.Fatalf("Analyze(%s) error = %v", operation, err)
		}
		if _, err := warmupEngine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe(%s) error = %v", operation, err)
		}
		return result
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// Familiarize authenticate -> read_customer, via
	// authenticate -> read_customer -> other_end (never export_customer).
	for i := range 20 {
		analyzeAndObserve(t, fmt.Sprintf("auth-%d", i), "authenticate", step())
		analyzeAndObserve(t, fmt.Sprintf("read-a-%d", i), "read_customer", step())
		analyzeAndObserve(t, fmt.Sprintf("other-end-%d", i), "other_end", step())
	}

	// Familiarize read_customer -> export_customer, via
	// other_start -> read_customer -> export_customer (never preceded
	// by authenticate).
	for i := range 20 {
		analyzeAndObserve(t, fmt.Sprintf("other-start-%d", i), "other_start", step())
		analyzeAndObserve(t, fmt.Sprintf("read-b-%d", i), "read_customer", step())
		analyzeAndObserve(t, fmt.Sprintf("export-seed-%d", i), "export_customer", step())
	}

	// Position the real history window at (authenticate, read_customer).
	analyzeAndObserve(t, "auth-final", "authenticate", step())
	analyzeAndObserve(t, "read-final", "read_customer", step())

	exportResult, err := engine.Analyze(ctx, actorEvent("export-final", "export_customer", step()))
	if err != nil {
		t.Fatalf("Analyze(export) error = %v", err)
	}
	if hasResultSignal(exportResult, "transition_deviation") {
		t.Fatalf("Contributors = %+v, want no transition_deviation — read_customer->export_customer is a familiar pairwise transition", exportResult.Anomaly.Contributors)
	}
	if !hasResultSignal(exportResult, "ngram_deviation") {
		t.Fatalf("Contributors = %+v, want ngram_deviation — the complete 3-gram authenticate->read_customer->export_customer was never observed", exportResult.Anomaly.Contributors)
	}

	// The alternative continuation actually trained for this exact
	// window (authenticate, read_customer) -> other_end must show no
	// such novelty, for comparison.
	otherEndResult, err := engine.Analyze(ctx, actorEvent("other-end-check", "other_end", step()))
	if err != nil {
		t.Fatalf("Analyze(other_end) error = %v", err)
	}
	if hasResultSignal(otherEndResult, "ngram_deviation") {
		t.Fatalf("Contributors = %+v, want no ngram_deviation — authenticate->read_customer->other_end has been observed 20 times", otherEndResult.Anomaly.Contributors)
	}

	if !exportResult.Trust.Risk.AtLeast(otherEndResult.Trust.Risk) {
		t.Errorf("export path Risk = %q, other_end path Risk = %q — want the novel-3-gram path at least as risky", exportResult.Trust.Risk, otherEndResult.Trust.Risk)
	}
}

// TestAnalyzeNGramCrossActorIsolation mirrors
// TestAnalyzeTransitionRarityCrossActorIsolation one level up: one
// actor's learned 3-gram history must never leak into a different
// actor's scoring for the nominally identical sequence.
func TestAnalyzeNGramCrossActorIsolation(t *testing.T) {
	ctx := context.Background()

	shape := func(actorID, operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        actorID + "-" + operation + "-" + ts.String(),
			Timestamp: ts,
			Actor:     event.Actor{ID: actorID, Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "shared-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	// See TestAnalyzeNGramEndToEnd's own comment for why warm-up uses
	// NGramWeight's default (0) and only the final scored call raises
	// it, via a second Engine sharing the same Store.
	sharedStore := store.NewInMemory()
	warmupEngine := trustvian.NewEngine(trustvian.WithStore(sharedStore), trustvian.WithPolicy(riskGatedPolicy()))
	scoredCfg := anomaly.DefaultConfig()
	scoredCfg.NGramWeight = 0.9
	engine := trustvian.NewEngine(
		trustvian.WithStore(sharedStore),
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(scoredCfg),
	)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	analyzeAndObserve := func(actorID, operation string) trustvian.Result {
		r, err := warmupEngine.Analyze(ctx, shape(actorID, operation, step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := warmupEngine.Observe(ctx, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
		return r
	}

	// actor-a: authenticate -> read -> update, trained 20 times.
	for range 20 {
		analyzeAndObserve("actor-a", "authenticate")
		analyzeAndObserve("actor-a", "read")
		analyzeAndObserve("actor-a", "update")
	}

	// actor-b has never been observed at all. Its own
	// authenticate -> read -> update *is* genuinely novel for actor-b's
	// own, fresh baseline — ngram_deviation firing here is correct, not
	// a bug (a 3-gram cannot be non-novel until it has actually been
	// observed for *this* actor). The isolation property this test
	// actually proves is narrower and more precise: the signal's Value
	// must read as *maximally* novel (1), not some intermediate value —
	// which is only possible if actor-a's 20 real observations of the
	// nominally identical sequence contributed nothing to actor-b's own
	// TrigramCounts.
	warmupR, err := warmupEngine.Analyze(ctx, shape("actor-b", "authenticate", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	warmupEngine.Observe(ctx, warmupR)
	warmupR, err = warmupEngine.Analyze(ctx, shape("actor-b", "read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	warmupEngine.Observe(ctx, warmupR)

	rB, err := engine.Analyze(ctx, shape("actor-b", "update", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	var ngramValue float64
	found := false
	for _, c := range rB.Anomaly.Contributors {
		if c.Name == "ngram_deviation" {
			ngramValue, found = c.Value, true
		}
	}
	if !found {
		t.Fatalf("actor-b Contributors = %+v, want ngram_deviation (a genuinely novel 3-gram for actor-b's own baseline)", rB.Anomaly.Contributors)
	}
	if ngramValue != 1 {
		t.Errorf("actor-b ngram_deviation Value = %v, want exactly 1 — anything less would mean actor-a's 20 observations leaked into actor-b's baseline", ngramValue)
	}
	if hasResultSignal(rB, "ngram_rarity") {
		t.Errorf("actor-b Contributors = %+v, want no ngram_rarity (mutually exclusive with ngram_deviation)", rB.Anomaly.Contributors)
	}
}

// TestAnalyzeNGramScoresBeforeLearning proves Analyze never mutates
// TrigramCounts/TrigramContinuationTotal — the 3-gram analogue of
// TestAnalyzeTransitionRarityScoresBeforeLearning. Calling Analyze many
// times on the same novel 3-gram, without ever calling Observe, must
// produce the exact same reading every time.
func TestAnalyzeNGramScoresBeforeLearning(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.NGramWeight = 0.9

	engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(anomalyCfg))

	shape := func(operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        operation + "-" + ts.String(),
			Timestamp: ts,
			Actor:     event.Actor{ID: "svc-ngram-only-learning", Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "order-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	for range 3 {
		r, err := engine.Analyze(ctx, shape("authenticate", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		engine.Observe(ctx, r)
	}
	r, err := engine.Analyze(ctx, shape("read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	engine.Observe(ctx, r)

	// Now Analyze the novel (authenticate, ..., read) -> delete 3-gram
	// repeatedly, without ever calling Observe again.
	eventTime := step()
	var results []bool
	for range 10 {
		result, err := engine.Analyze(ctx, shape("delete", eventTime))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		results = append(results, hasResultSignal(result, "ngram_deviation"))
	}
	for i, fired := range results {
		if !fired {
			t.Fatalf("call %d: ngram_deviation fired = false, want true (unchanged across repeated Analyze-only calls — Analyze must never learn)", i)
		}
	}
}

// TestObserveNGramLearnsOnlyFromEligibleDecisions is task 027's own
// poisoning-guard regression test, using the exact scenario the task's
// own brief names: a normal path (authenticate -> read -> update) and a
// malicious path (authenticate -> export -> delete) that gets BLOCKed.
// Repeatedly replaying the malicious sequence must never normalize it —
// this is not new logic, it falls out entirely from Engine.Observe's
// existing eligibleForLearning gate, which Baseline.Observe (and
// therefore this task's new TrigramCounts/TrigramContinuationTotal
// state) sits behind unconditionally; this test exists to prove that
// inheritance held, not to add a new mechanism.
func TestObserveNGramLearnsOnlyFromEligibleDecisions(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	normalEvent := func(id, operation string, ts time.Time) event.Event {
		return event.Event{
			ID: id, Timestamp: ts,
			Actor:     event.Actor{ID: "svc-ngram-poison-test", Type: event.ActorTypeService, IdentityConfidence: 0.98},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "customer-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Establish the history window at (authenticate, export) — the
	// setup step before the malicious "delete" completes the 3-gram
	// under test. "export" itself is unremarkable here (it is only the
	// *final* delete step that is wildly anomalous).
	r, err := engine.Analyze(ctx, normalEvent("auth-1", "authenticate", now))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := engine.Observe(ctx, r); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	r, err = engine.Analyze(ctx, normalEvent("export-1", "export_customer", now.Add(time.Second)))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := engine.Observe(ctx, r); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// A wildly anomalous event, immediately following authenticate ->
	// export_customer — the malicious 3-gram's completion.
	blocked := event.Event{
		ID:        "attack",
		Timestamp: now.Add(2 * time.Second),
		Actor:     event.Actor{ID: "svc-ngram-poison-test", Type: event.ActorTypeService, IdentityConfidence: 0.1},
		Operation: event.Operation{Category: event.OperationCategoryExternal, Name: "POST /exfiltrate"},
		Target:    event.Target{Name: "unknown-external-host"},
		Context:   event.Context{Environment: "production"},
	}
	blockedResult, err := engine.Analyze(ctx, blocked)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if blockedResult.Decision != policy.DecisionBlock {
		t.Fatalf("Decision = %q, want %q (test setup expects this event to be blocked)", blockedResult.Decision, policy.DecisionBlock)
	}

	// Repeat the BLOCKed 3-gram many times — an attacker trying to
	// "train" authenticate -> export_customer -> attack into looking
	// familiar.
	for i := range 50 {
		attempt := blocked
		attempt.ID = fmt.Sprintf("attack-%d", i)
		attempt.Timestamp = now.Add(time.Duration(i+3) * time.Second)
		result, err := engine.Analyze(ctx, attempt)
		if err != nil {
			t.Fatalf("Analyze() attempt %d: error = %v", i, err)
		}
		if result.Decision != policy.DecisionBlock {
			t.Fatalf("attempt %d: Decision = %q, want %q", i, result.Decision, policy.DecisionBlock)
		}
		learned, err := engine.Observe(ctx, result)
		if err != nil {
			t.Fatalf("Observe() attempt %d: error = %v", i, err)
		}
		if learned {
			t.Fatalf("attempt %d: Observe() learned = true for a BLOCKed event, want false", i)
		}
	}

	// The 3-gram must still read as entirely unseen: 50 repeated
	// BLOCKed attempts must have contributed zero learned 3-gram
	// observations.
	recheck, err := engine.Analyze(ctx, blocked)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !hasResultSignal(recheck, "ngram_deviation") {
		t.Errorf("Contributors = %+v, want ngram_deviation still present (the 3-gram must still read as entirely unseen after 50 BLOCKed attempts)", recheck.Anomaly.Contributors)
	}
	if hasResultSignal(recheck, "ngram_rarity") {
		t.Errorf("Contributors = %+v, want no ngram_rarity — a BLOCKed 3-gram must never accumulate enough learned observations to become \"rare but seen\"", recheck.Anomaly.Contributors)
	}
}

// TestAnalyzeMarkovCrossActorIsolation mirrors
// TestAnalyzeTransitionRarityCrossActorIsolation one level over:
// markov_surprisal reads the identical PredecessorCounts/
// OutgoingTransitionTotal state transition_rarity does, scoped by the
// same baseline.Key{ActorID, Environment} — one actor's learned
// transition frequency must never leak into a different actor's
// scoring for the nominally identical transition.
func TestAnalyzeMarkovCrossActorIsolation(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.MarkovWeight = 0.9

	engine := trustvian.NewEngine(
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(anomalyCfg),
	)

	shape := func(actorID, operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        actorID + "-" + operation + "-" + ts.String(),
			Timestamp: ts,
			Actor:     event.Actor{ID: actorID, Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "shared-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// actor-a: read -> update is extremely common (30 times).
	for range 30 {
		r, err := engine.Analyze(ctx, shape("actor-a", "read", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := engine.Observe(ctx, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
		r, err = engine.Analyze(ctx, shape("actor-a", "update", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := engine.Observe(ctx, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	// actor-b has never been observed at all — the identical
	// read -> update transition, for actor-b, must show no Markov
	// evidence (no predecessor exists yet for a fresh actor).
	rB, err := engine.Analyze(ctx, shape("actor-b", "read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := engine.Observe(ctx, rB); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	rB2, err := engine.Analyze(ctx, shape("actor-b", "update", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasResultSignal(rB2, "markov_surprisal") {
		t.Errorf("actor-b Contributors = %+v, want no markov_surprisal — actor-a's 30 observations must not leak into actor-b's baseline", rB2.Anomaly.Contributors)
	}
}

// TestAnalyzeMarkovScoresBeforeLearning proves Analyze never mutates
// PredecessorCounts/OutgoingTransitionTotal — the markov_surprisal
// analogue of TestAnalyzeTransitionRarityScoresBeforeLearning. Calling
// Analyze many times on the same rare transition, without ever calling
// Observe, must produce the exact same surprisal reading every time.
func TestAnalyzeMarkovScoresBeforeLearning(t *testing.T) {
	ctx := context.Background()
	anomalyCfg := anomaly.DefaultConfig()
	anomalyCfg.MarkovWeight = 0.9
	anomalyCfg.MinTransitionObservations = 20

	engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(anomalyCfg))

	shape := func(operation string, ts time.Time) event.Event {
		return event.Event{
			ID:        operation + "-" + ts.String(),
			Timestamp: ts,
			Actor:     event.Actor{ID: "svc-markov-only-learning", Type: event.ActorTypeService, IdentityConfidence: 1},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: operation},
			Target:    event.Target{Name: "order-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// Build a real 19/1 split via the gated loop, exactly as the
	// transition-rarity test does.
	for range 19 {
		r, err := engine.Analyze(ctx, shape("read", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		engine.Observe(ctx, r)
		r, err = engine.Analyze(ctx, shape("update", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		engine.Observe(ctx, r)
	}
	r, err := engine.Analyze(ctx, shape("read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	engine.Observe(ctx, r)
	r, err = engine.Analyze(ctx, shape("delete", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	engine.Observe(ctx, r)

	r, err = engine.Analyze(ctx, shape("read", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	engine.Observe(ctx, r)

	// Now Analyze the rare (read -> delete) transition repeatedly,
	// without ever calling Observe again.
	eventTime := step()
	var firstSurprisal float64
	for i := range 10 {
		result, err := engine.Analyze(ctx, shape("delete", eventTime))
		if err != nil {
			t.Fatalf("Analyze() call %d: error = %v", i, err)
		}
		var surprisal float64
		found := false
		for _, c := range result.Anomaly.Contributors {
			if c.Name == "markov_surprisal" {
				surprisal, found = c.Value, true
			}
		}
		if !found {
			t.Fatalf("call %d: Contributors = %+v, want markov_surprisal", i, result.Anomaly.Contributors)
		}
		if i == 0 {
			firstSurprisal = surprisal
		} else if diff := surprisal - firstSurprisal; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("call %d: markov_surprisal Value = %v, want %v (unchanged across repeated Analyze-only calls — Analyze must never learn)", i, surprisal, firstSurprisal)
		}
	}
}

// TestObserveMarkovLearnsOnlyFromEligibleDecisions is task 028's own
// poisoning-guard regression test, mirroring
// TestObserveTransitionRarityLearnsOnlyFromEligibleDecisions one level
// over: not new logic — markov_surprisal reads the identical
// PredecessorCounts/OutgoingTransitionTotal state, which already sits
// behind Engine.Observe's eligibleForLearning gate; this test exists
// to prove that inheritance held for the new signal specifically.
func TestObserveMarkovLearnsOnlyFromEligibleDecisions(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	readEvent := func(id string, ts time.Time) event.Event {
		return event.Event{
			ID: id, Timestamp: ts,
			Actor:     event.Actor{ID: "svc-markov-poison-test", Type: event.ActorTypeService, IdentityConfidence: 0.98},
			Operation: event.Operation{Category: event.OperationCategoryDB, Name: "SELECT accounts"},
			Target:    event.Target{Name: "accounts-db"},
			Context:   event.Context{Environment: "production"},
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, err := engine.Analyze(ctx, readEvent("read-1", now))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := engine.Observe(ctx, r); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// A wildly anomalous event that should be BLOCKed, immediately
	// following the read above.
	blocked := event.Event{
		ID:        "attack",
		Timestamp: now.Add(time.Second),
		Actor:     event.Actor{ID: "svc-markov-poison-test", Type: event.ActorTypeService, IdentityConfidence: 0.1},
		Operation: event.Operation{Category: event.OperationCategoryExternal, Name: "POST /exfiltrate"},
		Target:    event.Target{Name: "unknown-external-host"},
		Context:   event.Context{Environment: "production"},
	}
	blockedResult, err := engine.Analyze(ctx, blocked)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if blockedResult.Decision != policy.DecisionBlock {
		t.Fatalf("Decision = %q, want %q (test setup expects this event to be blocked)", blockedResult.Decision, policy.DecisionBlock)
	}

	// Repeat the BLOCKed transition many times — an attacker trying to
	// "train" read -> attack into looking common.
	for i := range 50 {
		attempt := blocked
		attempt.ID = fmt.Sprintf("attack-%d", i)
		attempt.Timestamp = now.Add(time.Duration(i+2) * time.Second)
		result, err := engine.Analyze(ctx, attempt)
		if err != nil {
			t.Fatalf("Analyze() attempt %d: error = %v", i, err)
		}
		if result.Decision != policy.DecisionBlock {
			t.Fatalf("attempt %d: Decision = %q, want %q", i, result.Decision, policy.DecisionBlock)
		}
		learned, err := engine.Observe(ctx, result)
		if err != nil {
			t.Fatalf("Observe() attempt %d: error = %v", i, err)
		}
		if learned {
			t.Fatalf("attempt %d: Observe() learned = true for a BLOCKed event, want false", i)
		}
	}

	// The read -> attack transition must still read as entirely
	// unseen: 50 repeated BLOCKed attempts must have contributed zero
	// transition observations.
	recheck, err := engine.Analyze(ctx, blocked)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !hasResultSignal(recheck, "transition_deviation") {
		t.Errorf("Contributors = %+v, want transition_deviation still present (the transition must still read as entirely unseen after 50 BLOCKed attempts)", recheck.Anomaly.Contributors)
	}
	if hasResultSignal(recheck, "markov_surprisal") {
		t.Errorf("Contributors = %+v, want no markov_surprisal — a BLOCKed transition must never accumulate enough learned observations to become \"familiar but rare\"", recheck.Anomaly.Contributors)
	}
}

// --- v0.7 task 014: AI Agent Event/Context Foundation ---
//
// None of the tests below add a new detector, a new baseline field, or
// a new pipeline stage. Each one drives the *existing* Engine (the
// exact same one every prior test in this file uses) with
// agent-shaped Events (Actor.Type = ActorTypeAIAgent, Operation.Category
// = OperationCategoryTool, Context.SessionID/DelegatedFrom set) — the
// direct proof that "AI Agent support" is richer event semantics
// through the existing engine, not a second security engine. See
// docs/adr/0014-ai-agents-as-first-class-behavioral-actors.md.

// agentEvent builds an AI-agent-shaped Event for these tests: a fixed
// actor, environment, and session, varying only the tool operation and
// timestamp — mirroring paymentEventAt's own shape one level up.
func agentEvent(actorID, sessionID, tool string, ts time.Time) event.Event {
	return event.Event{
		ID:        actorID + "-" + tool + "-" + ts.String(),
		Timestamp: ts,
		Actor:     event.Actor{ID: actorID, Type: event.ActorTypeAIAgent, IdentityConfidence: 0.95},
		Operation: event.Operation{Category: event.OperationCategoryTool, Name: tool},
		Target:    event.Target{Name: tool},
		Context:   event.Context{Environment: "production", SessionID: sessionID},
	}
}

// TestAnalyzeAgentSessionIDDoesNotExplodeBaseline is task 014's own
// mandatory cardinality proof (§23/§44 of the task brief): 1,000
// events with unique SessionIDs but otherwise identical behavior must
// accumulate into exactly one Baseline entry under one Fingerprint —
// not 1,000 separate ones — proving Context.SessionID never enters
// baseline.Key or Fingerprint identity, empirically, not just by
// reading the source.
func TestAnalyzeAgentSessionIDDoesNotExplodeBaseline(t *testing.T) {
	ctx := context.Background()
	agentStore := store.NewInMemory()
	engine := trustvian.NewEngine(trustvian.WithStore(agentStore))

	const observations = 1000
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var lastResult trustvian.Result
	for i := range observations {
		now = now.Add(time.Second)
		sessionID := fmt.Sprintf("session-%d", i) // 1,000 distinct session IDs
		result, err := engine.Analyze(ctx, agentEvent("customer-support-agent-42", sessionID, "search", now))
		if err != nil {
			t.Fatalf("Analyze() call %d: error = %v", i, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe() call %d: error = %v", i, err)
		}
		if i > 0 && result.BaselineKey != lastResult.BaselineKey {
			t.Fatalf("call %d: BaselineKey = %+v, want %+v (unchanged — SessionID must not affect it)", i, result.BaselineKey, lastResult.BaselineKey)
		}
		if i > 0 && result.Fingerprint.ID != lastResult.Fingerprint.ID {
			t.Fatalf("call %d: Fingerprint.ID = %q, want %q (unchanged — SessionID must not affect it)", i, result.Fingerprint.ID, lastResult.Fingerprint.ID)
		}
		lastResult = result
	}

	// Confirm, directly from the store, that all 1,000 observations
	// landed in a single Fingerprint entry's Count — not spread across
	// 1,000 distinct baseline entries.
	bl, ok := agentStore.Get(ctx, lastResult.BaselineKey)
	if !ok {
		t.Fatalf("Get(%+v) ok = false, want true", lastResult.BaselineKey)
	}
	if got := len(bl.Fingerprints); got != 1 {
		t.Fatalf("len(Baseline.Fingerprints) = %d, want 1 — 1,000 distinct SessionIDs must not create 1,000 distinct Fingerprints", got)
	}
	if got := bl.Fingerprints[lastResult.Fingerprint.ID].Count; got != observations {
		t.Fatalf("Fingerprints[%q].Count = %d, want %d — all observations must accumulate into the same entry", lastResult.Fingerprint.ID, got, observations)
	}
}

// TestAnalyzeAgentToolNoveltyDetectedByExistingEngine proves task
// 014's central "no new detector needed" claim (§16/§45 of the task
// brief): an agent that has only ever used search/read/summarize, then
// invokes shell.execute, is flagged by the *existing*
// categorical_novelty/transition_deviation signals — no agent-specific
// detector exists or is added.
func TestAnalyzeAgentToolNoveltyDetectedByExistingEngine(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	for i := range 20 {
		for _, tool := range []string{"search", "read", "summarize"} {
			result, err := engine.Analyze(ctx, agentEvent("agent-1", fmt.Sprintf("session-%d", i), tool, step()))
			if err != nil {
				t.Fatalf("Analyze(%s) error = %v", tool, err)
			}
			if _, err := engine.Observe(ctx, result); err != nil {
				t.Fatalf("Observe(%s) error = %v", tool, err)
			}
		}
	}

	shellResult, err := engine.Analyze(ctx, agentEvent("agent-1", "session-attack", "shell.execute", step()))
	if err != nil {
		t.Fatalf("Analyze(shell.execute) error = %v", err)
	}
	if !hasResultSignal(shellResult, "categorical_novelty") {
		t.Fatalf("Contributors = %+v, want categorical_novelty — shell.execute has never been observed for this agent", shellResult.Anomaly.Contributors)
	}
	if !hasResultSignal(shellResult, "transition_deviation") {
		t.Fatalf("Contributors = %+v, want transition_deviation — no predecessor has ever led to shell.execute", shellResult.Anomaly.Contributors)
	}

	familiarResult, err := engine.Analyze(ctx, agentEvent("agent-1", "session-normal", "search", step()))
	if err != nil {
		t.Fatalf("Analyze(search) error = %v", err)
	}
	if !shellResult.Trust.Risk.AtLeast(familiarResult.Trust.Risk) {
		t.Errorf("shell.execute Risk = %q, familiar search Risk = %q — want the novel tool at least as risky", shellResult.Trust.Risk, familiarResult.Trust.Risk)
	}
}

// TestAnalyzeAgentToolSequenceNoveltyDetectedByExistingEngine is task
// 014's mandatory critical semantic test (§19/§46 of the task brief),
// mirroring task 027's own
// TestScoreNGramDeviationDetectsNovelTrigramDespiteFamiliarPairwiseTransitions
// with agent-shaped events: search -> read -> summarize is trained as
// this agent's normal path; search -> secret.read and
// secret.read -> external.post are *each* trained as familiar pairwise
// transitions, via different contexts, so neither individual hop is
// novel; the complete sequence search -> secret.read -> external.post
// is never trained as one continuous path. The existing v0.6
// ngram_deviation signal must still detect the higher-order novelty —
// the direct proof that v0.6 sequence analysis, unmodified, already
// works for AI-agent tool sequences.
func TestAnalyzeAgentToolSequenceNoveltyDetectedByExistingEngine(t *testing.T) {
	ctx := context.Background()

	// See TestAnalyzeNGramEndToEnd's own comment (task 027) for why
	// warm-up uses NGramWeight's default (0) and only the final scored
	// call raises it, via a second Engine sharing one Store.
	sharedStore := store.NewInMemory()
	warmupEngine := trustvian.NewEngine(trustvian.WithStore(sharedStore), trustvian.WithPolicy(riskGatedPolicy()))
	scoredCfg := anomaly.DefaultConfig()
	scoredCfg.NGramWeight = 0.9
	engine := trustvian.NewEngine(
		trustvian.WithStore(sharedStore),
		trustvian.WithPolicy(riskGatedPolicy()),
		trustvian.WithAnomalyConfig(scoredCfg),
	)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	analyzeAndObserve := func(t *testing.T, sessionID, tool string, ts time.Time) trustvian.Result {
		t.Helper()
		result, err := warmupEngine.Analyze(ctx, agentEvent("agent-1", sessionID, tool, ts))
		if err != nil {
			t.Fatalf("Analyze(%s) error = %v", tool, err)
		}
		if _, err := warmupEngine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe(%s) error = %v", tool, err)
		}
		return result
	}

	// This agent's actual normal path: search -> read -> summarize,
	// repeated many times.
	for i := range 20 {
		s := fmt.Sprintf("normal-%d", i)
		analyzeAndObserve(t, s, "search", step())
		analyzeAndObserve(t, s, "read", step())
		analyzeAndObserve(t, s, "summarize", step())
	}

	// Familiarize search -> secret.read as a pairwise transition, via
	// search -> secret.read -> audit_log (never external.post).
	for i := range 20 {
		s := fmt.Sprintf("secret-seed-%d", i)
		analyzeAndObserve(t, s, "search", step())
		analyzeAndObserve(t, s, "secret.read", step())
		analyzeAndObserve(t, s, "audit_log", step())
	}

	// Familiarize secret.read -> external.post as a pairwise transition,
	// via a *different* predecessor: notify -> secret.read -> external.post.
	for i := range 20 {
		s := fmt.Sprintf("post-seed-%d", i)
		analyzeAndObserve(t, s, "notify", step())
		analyzeAndObserve(t, s, "secret.read", step())
		analyzeAndObserve(t, s, "external.post", step())
	}

	// Position the real history window at (search, secret.read) — the
	// exact attack sequence's first two steps, never trained as a
	// continuous path with external.post next.
	analyzeAndObserve(t, "attack", "search", step())
	analyzeAndObserve(t, "attack", "secret.read", step())

	attackResult, err := engine.Analyze(ctx, agentEvent("agent-1", "attack", "external.post", step()))
	if err != nil {
		t.Fatalf("Analyze(external.post) error = %v", err)
	}
	if hasResultSignal(attackResult, "transition_deviation") {
		t.Fatalf("Contributors = %+v, want no transition_deviation — secret.read->external.post is a familiar pairwise transition", attackResult.Anomaly.Contributors)
	}
	if !hasResultSignal(attackResult, "ngram_deviation") {
		t.Fatalf("Contributors = %+v, want ngram_deviation — search->secret.read->external.post was never observed as a complete sequence, even though both individual hops are familiar", attackResult.Anomaly.Contributors)
	}

	// The trained continuation for this exact window (search,
	// secret.read) -> audit_log must show no such novelty, for
	// comparison.
	auditResult, err := engine.Analyze(ctx, agentEvent("agent-1", "attack", "audit_log", step()))
	if err != nil {
		t.Fatalf("Analyze(audit_log) error = %v", err)
	}
	if hasResultSignal(auditResult, "ngram_deviation") {
		t.Fatalf("Contributors = %+v, want no ngram_deviation — search->secret.read->audit_log has been observed 20 times", auditResult.Anomaly.Contributors)
	}
}

// TestAnalyzeAgentCrossActorIsolation mirrors
// TestAnalyzeNGramCrossActorIsolation for agent actors specifically:
// one agent's learned tool-use history must never leak into a
// different agent's scoring for the nominally identical tool.
func TestAnalyzeAgentCrossActorIsolation(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// agent-a: shell.execute is extremely common (30 times).
	for i := range 30 {
		s := fmt.Sprintf("session-%d", i)
		r, err := engine.Analyze(ctx, agentEvent("agent-a", s, "shell.execute", step()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := engine.Observe(ctx, r); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	// agent-b has never been observed at all — the identical
	// shell.execute tool call, for agent-b, must still read as
	// maximally novel.
	rB, err := engine.Analyze(ctx, agentEvent("agent-b", "session-1", "shell.execute", step()))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !hasResultSignal(rB, "categorical_novelty") {
		t.Errorf("agent-b Contributors = %+v, want categorical_novelty — agent-a's 30 observations must not leak into agent-b's baseline", rB.Anomaly.Contributors)
	}
	var noveltyValue float64
	for _, c := range rB.Anomaly.Contributors {
		if c.Name == "categorical_novelty" {
			noveltyValue = c.Value
		}
	}
	if noveltyValue != 1 {
		t.Errorf("agent-b categorical_novelty Value = %v, want exactly 1 — anything less would mean agent-a's history leaked into agent-b's baseline", noveltyValue)
	}
}

// TestAnalyzeAgentDelegationContextScoredIdentically proves
// Context.DelegatedFrom is scored by the exact same pipeline as any
// other event — no delegation-specific anomaly rule is invented (task
// 014 §48/§37 of the brief: "detection comes later," this task only
// carries the metadata). Two otherwise-identical events, differing
// only in DelegatedFrom, must produce byte-for-byte identical Anomaly/
// Trust/Decision — proving DelegatedFrom has no scoring effect yet,
// while still being present on the Event for a future policy/detector
// to read.
func TestAnalyzeAgentDelegationContextScoredIdentically(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()))

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	withDelegation := agentEvent("agent-1", "session-1", "search", now)
	withDelegation.Context.DelegatedFrom = "orchestrator-agent"

	withoutDelegation := agentEvent("agent-1", "session-1", "search", now)
	withoutDelegation.ID = withoutDelegation.ID + "-b" // distinct Event.ID only

	resultWith, err := engine.Analyze(ctx, withDelegation)
	if err != nil {
		t.Fatalf("Analyze(with delegation): %v", err)
	}
	resultWithout, err := engine.Analyze(ctx, withoutDelegation)
	if err != nil {
		t.Fatalf("Analyze(without delegation): %v", err)
	}

	if resultWith.Fingerprint.ID != resultWithout.Fingerprint.ID {
		t.Errorf("Fingerprint.ID differs with/without DelegatedFrom: %q vs %q", resultWith.Fingerprint.ID, resultWithout.Fingerprint.ID)
	}
	if resultWith.Anomaly.Score != resultWithout.Anomaly.Score {
		t.Errorf("Anomaly.Score differs with/without DelegatedFrom: %v vs %v", resultWith.Anomaly.Score, resultWithout.Anomaly.Score)
	}
	if resultWith.Trust.Score != resultWithout.Trust.Score {
		t.Errorf("Trust.Score differs with/without DelegatedFrom: %v vs %v", resultWith.Trust.Score, resultWithout.Trust.Score)
	}
	if resultWith.Decision != resultWithout.Decision {
		t.Errorf("Decision differs with/without DelegatedFrom: %q vs %q", resultWith.Decision, resultWithout.Decision)
	}
}

// --- Task 030: Approval-Aware Policy Semantics ---
//
// These tests prove the central architectural claim task 030 exists to
// establish: "behaviorally normal" and "policy-authorized" are
// different questions. No new anomaly signal, Baseline field, or
// pipeline stage is added — approval enforcement is entirely a Policy
// concern, expressed with the existing Rule/Condition/Unless
// mechanism. See docs/adr/0015-approval-as-policy-evidence-not-behavioral-anomaly.md.

// approvalGatedPolicy requires approval for shell.execute (via the
// canonical When+Unless pattern — see
// docs/tasks/030-approval-aware-policy-semantics.md), then falls back
// to riskGatedPolicy's own risk-based rules for everything else. The
// approval rule is ordered first and matches only on
// Operation/Target — never on Actor.Type — so it applies identically
// regardless of which kind of actor performs the operation.
func approvalGatedPolicy() policy.Policy {
	return policy.Policy{
		Rules: []policy.Rule{
			{
				Name:   "shell-execute-requires-approval",
				When:   policy.Condition{OperationCategory: event.OperationCategoryTool, TargetName: "shell.execute"},
				Unless: &policy.Condition{ApprovalStatus: event.ApprovalApproved},
				Action: policy.DecisionBlock,
				Reason: "shell.execute requires approval; approval evidence was not Approved",
			},
			{Name: "block-high-risk", When: policy.Condition{MinRiskLevel: trust.RiskHigh}, Action: policy.DecisionBlock, Reason: "risk too high"},
			{Name: "alert-medium-risk", When: policy.Condition{MinRiskLevel: trust.RiskMedium}, Action: policy.DecisionAlert, Reason: "elevated risk"},
		},
		DefaultAction: policy.DecisionAllow,
		DefaultReason: "risk within tolerance",
	}
}

// toolEvent builds a tool-call Event for a given actor type/ID and
// approval status — deliberately independent of agentEvent above, so
// these tests can exercise both AI-agent and non-agent actors against
// the identical approval-aware policy.
func toolEvent(actorType event.ActorType, actorID, tool string, approval event.ApprovalStatus, ts time.Time) event.Event {
	return event.Event{
		ID:        actorID + "-" + tool + "-" + ts.String(),
		Timestamp: ts,
		Actor:     event.Actor{ID: actorID, Type: actorType, IdentityConfidence: 0.95},
		Operation: event.Operation{Category: event.OperationCategoryTool, Name: tool},
		Target:    event.Target{Name: tool},
		Context:   event.Context{Environment: "production", ApprovalStatus: approval},
	}
}

// TestAnalyzeAgentApprovalPolicyAllowsApprovedDeniesUnapproved is task
// 030's mandatory AI-agent integration test (§37 of the task brief):
// shell.execute is trained until it is behaviorally familiar (low
// risk), yet the configured Policy still requires approval for it —
// Approved is allowed, Denied is blocked, demonstrating that
// behaviorally normal does not imply policy-authorized.
func TestAnalyzeAgentApprovalPolicyAllowsApprovedDeniesUnapproved(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(approvalGatedPolicy()))

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// Train shell.execute as this agent's familiar, ordinary behavior —
	// approval is not yet a concern during warm-up (Unspecified would
	// fail the requirement, but we only need Trust.Risk to settle
	// below "high" here; the training loop's own calls are not
	// asserted on).
	for range 30 {
		ev := toolEvent(event.ActorTypeAIAgent, "agent-1", "shell.execute", event.ApprovalApproved, step())
		result, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze (warm-up) error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe (warm-up) error = %v", err)
		}
	}

	approved := toolEvent(event.ActorTypeAIAgent, "agent-1", "shell.execute", event.ApprovalApproved, step())
	resultApproved, err := engine.Analyze(ctx, approved)
	if err != nil {
		t.Fatalf("Analyze(Approved) error = %v", err)
	}
	if resultApproved.Trust.Risk.AtLeast(trust.RiskHigh) {
		t.Fatalf("Trust.Risk = %q after 30 repetitions, want below %q — shell.execute should be behaviorally familiar by now", resultApproved.Trust.Risk, trust.RiskHigh)
	}
	if resultApproved.Decision != policy.DecisionAllow {
		t.Errorf("Decision (Approved) = %q, want %q", resultApproved.Decision, policy.DecisionAllow)
	}

	denied := toolEvent(event.ActorTypeAIAgent, "agent-1", "shell.execute", event.ApprovalDenied, step())
	resultDenied, err := engine.Analyze(ctx, denied)
	if err != nil {
		t.Fatalf("Analyze(Denied) error = %v", err)
	}
	if resultDenied.Decision != policy.DecisionBlock {
		t.Errorf("Decision (Denied) = %q, want %q — behaviorally familiar must not override a missing approval", resultDenied.Decision, policy.DecisionBlock)
	}
}

// TestAnalyzeApprovalPolicyBehavioralScoreIndependence is task 030's
// mandatory architectural regression (§36 of the task brief): for
// otherwise-identical events differing only in ApprovalStatus,
// Anomaly.Score and Trust.Score must be byte-for-byte identical — only
// the Policy Decision may differ. Approval evidence must never leak
// into behavioral scoring.
func TestAnalyzeApprovalPolicyBehavioralScoreIndependence(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(approvalGatedPolicy()))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	approved := toolEvent(event.ActorTypeAIAgent, "agent-2", "shell.execute", event.ApprovalApproved, now)
	resultApproved, err := engine.Analyze(ctx, approved)
	if err != nil {
		t.Fatalf("Analyze(Approved) error = %v", err)
	}

	denied := toolEvent(event.ActorTypeAIAgent, "agent-2", "shell.execute", event.ApprovalDenied, now)
	denied.ID += "-denied" // distinct Event.ID only
	resultDenied, err := engine.Analyze(ctx, denied)
	if err != nil {
		t.Fatalf("Analyze(Denied) error = %v", err)
	}

	if resultApproved.Anomaly.Score != resultDenied.Anomaly.Score {
		t.Errorf("Anomaly.Score differs by ApprovalStatus alone: %v (Approved) vs %v (Denied)", resultApproved.Anomaly.Score, resultDenied.Anomaly.Score)
	}
	if resultApproved.Trust.Score != resultDenied.Trust.Score {
		t.Errorf("Trust.Score differs by ApprovalStatus alone: %v (Approved) vs %v (Denied)", resultApproved.Trust.Score, resultDenied.Trust.Score)
	}
	if resultApproved.Fingerprint.ID != resultDenied.Fingerprint.ID {
		t.Errorf("Fingerprint.ID differs by ApprovalStatus alone: %q (Approved) vs %q (Denied)", resultApproved.Fingerprint.ID, resultDenied.Fingerprint.ID)
	}
	if resultApproved.Decision == resultDenied.Decision {
		t.Errorf("Decision = %q for both Approved and Denied, want them to differ — the approval rule must still discriminate", resultApproved.Decision)
	}
}

// TestAnalyzeApprovalPolicyGenericNotHardCodedToAIAgent is task 030's
// mandatory non-agent-genericity test (§16/§38 of the task brief):
// approvalGatedPolicy's rule matches only on Operation/Target, never
// on Actor.Type, so a plain "service" actor performing the identical
// operation must be gated identically to an AI agent — proving the
// policy primitive is domain-generic, not implicitly coupled to
// ActorTypeAIAgent.
func TestAnalyzeApprovalPolicyGenericNotHardCodedToAIAgent(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(approvalGatedPolicy()))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	denied := toolEvent(event.ActorTypeService, "deploy-service", "shell.execute", event.ApprovalDenied, now)
	resultDenied, err := engine.Analyze(ctx, denied)
	if err != nil {
		t.Fatalf("Analyze(Denied) error = %v", err)
	}
	if resultDenied.Decision != policy.DecisionBlock {
		t.Errorf("service actor, Denied: Decision = %q, want %q — approval enforcement must not be implicitly coupled to ActorTypeAIAgent", resultDenied.Decision, policy.DecisionBlock)
	}

	approved := toolEvent(event.ActorTypeService, "deploy-service", "shell.execute", event.ApprovalApproved, now)
	approved.ID += "-approved"
	resultApproved, err := engine.Analyze(ctx, approved)
	if err != nil {
		t.Fatalf("Analyze(Approved) error = %v", err)
	}
	if resultApproved.Decision != policy.DecisionAllow {
		t.Errorf("service actor, Approved: Decision = %q, want %q", resultApproved.Decision, policy.DecisionAllow)
	}
}

// --- Task 031: Delegation Behavioral Semantics ---
//
// These tests prove delegation_deviation is behavioral evidence only,
// never authorization or authenticated provenance: a familiar
// delegator is not thereby authorized, an unfamiliar one is not
// thereby malicious, and the signal never touches Fingerprint identity
// or entangles with approval policy. See
// docs/adr/0016-delegation-as-behavioral-evidence-not-provenance.md.

// delegationGatedConfig enables DelegationWeight — every other test in
// this section constructs its own Engine with this Config, mirroring
// how the task 014/030 sections above construct their own policy
// fixtures locally rather than sharing a package-level default.
func delegationGatedConfig() anomaly.Config {
	cfg := anomaly.DefaultConfig()
	cfg.DelegationWeight = 0.7
	return cfg
}

// TestAnalyzeDelegationNoveltyDetectedByExistingSignal is task 031's
// own foundation test (§42/§43 of the task brief), mirroring task
// 014's TestAnalyzeAgentToolNoveltyDetectedByExistingEngine shape: a
// familiar delegator produces no delegation_deviation; a delegator
// this actor has never seen does, and reads at least as risky.
func TestAnalyzeDelegationNoveltyDetectedByExistingSignal(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()), trustvian.WithAnomalyConfig(delegationGatedConfig()))

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// agent-b's normal path: delegated from agent-a, repeatedly.
	for i := range 20 {
		ev := agentEvent("agent-b", fmt.Sprintf("session-%d", i), "search", step())
		ev.Context.DelegatedFrom = "agent-a"
		result, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe error = %v", err)
		}
	}

	familiar := agentEvent("agent-b", "session-familiar", "search", step())
	familiar.Context.DelegatedFrom = "agent-a"
	familiarResult, err := engine.Analyze(ctx, familiar)
	if err != nil {
		t.Fatalf("Analyze(familiar) error = %v", err)
	}
	if hasResultSignal(familiarResult, "delegation_deviation") {
		t.Fatalf("Contributors = %+v, want no delegation_deviation for a delegator observed 20 times", familiarResult.Anomaly.Contributors)
	}

	novel := agentEvent("agent-b", "session-novel", "search", step())
	novel.Context.DelegatedFrom = "agent-x"
	novelResult, err := engine.Analyze(ctx, novel)
	if err != nil {
		t.Fatalf("Analyze(novel) error = %v", err)
	}
	if !hasResultSignal(novelResult, "delegation_deviation") {
		t.Fatalf("Contributors = %+v, want delegation_deviation for a never-observed delegator", novelResult.Anomaly.Contributors)
	}
	if !novelResult.Trust.Risk.AtLeast(familiarResult.Trust.Risk) {
		t.Errorf("novel-delegator Risk = %q, familiar-delegator Risk = %q — want the novel delegator at least as risky", novelResult.Trust.Risk, familiarResult.Trust.Risk)
	}

	// Behavioral evidence only, not an authorization/malice verdict —
	// this test asserts anomaly evidence and relative risk, never that
	// either Decision is ALLOW or BLOCK by itself; that remains
	// Policy's call, proven separately by
	// TestAnalyzeDelegationApprovalIndependence below.
}

// TestAnalyzeDelegationScoreBeforeLearn is task 031's mandatory
// score-before-learn regression (§25/§45 of the task brief): Analyze
// alone (no Observe) must never make a delegator look familiar on a
// later call — Engine.Analyze is documented as read-only, and this
// proves it holds for delegation specifically.
func TestAnalyzeDelegationScoreBeforeLearn(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()), trustvian.WithAnomalyConfig(delegationGatedConfig()))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ev := func(id string, ts time.Time) event.Event {
		e := agentEvent("agent-c", "session-1", "search", ts)
		e.ID = id
		e.Context.DelegatedFrom = "agent-x"
		return e
	}

	first, err := engine.Analyze(ctx, ev("call-1", now))
	if err != nil {
		t.Fatalf("Analyze (1st) error = %v", err)
	}
	if !hasResultSignal(first, "delegation_deviation") {
		t.Fatalf("Contributors = %+v, want delegation_deviation on the first call", first.Anomaly.Contributors)
	}

	// No Observe call in between — Analyze must not have learned
	// anything on its own.
	second, err := engine.Analyze(ctx, ev("call-2", now.Add(time.Second)))
	if err != nil {
		t.Fatalf("Analyze (2nd) error = %v", err)
	}
	if !hasResultSignal(second, "delegation_deviation") {
		t.Fatalf("Contributors = %+v, want delegation_deviation still present on the second call — Analyze alone must never learn", second.Anomaly.Contributors)
	}
}

// TestAnalyzeDelegationPoisoningIneligibleEventsDoNotTrain is task
// 031's mandatory poisoning regression (§26/§46 of the task brief):
// repeated BLOCKed delegation from a never-approved delegator must
// never become "familiar" through repetition alone — the identical
// eligibleForLearning gate every other behavioral dimension already
// obeys.
func TestAnalyzeDelegationPoisoningIneligibleEventsDoNotTrain(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()), trustvian.WithAnomalyConfig(delegationGatedConfig()))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// Mature this actor's fingerprint first (no delegation involved),
	// so Anomaly.Confidence is 1 once the attack begins below — cold
	// start (Confidence 0) would otherwise zero out
	// delegation_deviation's contribution to Trust via
	// effectiveAnomaly = Score*Confidence, and the attack would never
	// reach BLOCK in the first place, defeating the point of this
	// test.
	for range 25 {
		ev := agentEvent("agent-d", "session-warmup", "search", step())
		result, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze (warm-up) error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe (warm-up) error = %v", err)
		}
	}

	var lastResult trustvian.Result
	for range 30 {
		ev := agentEvent("agent-d", "session-attack", "search", step())
		ev.Context.DelegatedFrom = "agent-x"
		result, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze error = %v", err)
		}
		if result.Decision != policy.DecisionBlock {
			t.Fatalf("Decision = %q, want %q — this test requires every attempt to be learning-ineligible", result.Decision, policy.DecisionBlock)
		}
		learned, err := engine.Observe(ctx, result)
		if err != nil {
			t.Fatalf("Observe error = %v", err)
		}
		if learned {
			t.Fatalf("Observe() learned = true, want false for a BLOCKed decision")
		}
		lastResult = result
	}

	if !hasResultSignal(lastResult, "delegation_deviation") {
		t.Fatalf("Contributors = %+v, want delegation_deviation to remain present after 30 repeated, ineligible attempts", lastResult.Anomaly.Contributors)
	}
}

// TestAnalyzeDelegationActorIsolation is task 031's mandatory
// cross-actor regression (§27/§47 of the task brief): agent-e's
// delegation history from agent-a must not leak into agent-f's own
// baseline.
func TestAnalyzeDelegationActorIsolation(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()), trustvian.WithAnomalyConfig(delegationGatedConfig()))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	for i := range 20 {
		ev := agentEvent("agent-e", fmt.Sprintf("session-%d", i), "search", step())
		ev.Context.DelegatedFrom = "agent-a"
		result, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe error = %v", err)
		}
	}

	// agent-f has never been observed at all — the identical
	// delegator (agent-a), for agent-f, must still read as novel.
	other := agentEvent("agent-f", "session-1", "search", step())
	other.Context.DelegatedFrom = "agent-a"
	otherResult, err := engine.Analyze(ctx, other)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if !hasResultSignal(otherResult, "delegation_deviation") {
		t.Errorf("Contributors = %+v, want delegation_deviation — agent-e's history must not leak into agent-f's baseline", otherResult.Anomaly.Contributors)
	}
}

// TestAnalyzeDelegationMissingDelegationUnaffected is task 031's
// mandatory backward-compatibility regression (§23/§48 of the task
// brief): an event with no DelegatedFrom at all — the vast majority of
// v0.1-v0.6 traffic — must produce no delegation_deviation and no
// Decision change, even with DelegationWeight enabled.
func TestAnalyzeDelegationMissingDelegationUnaffected(t *testing.T) {
	ctx := context.Background()
	withDelegation := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()), trustvian.WithAnomalyConfig(delegationGatedConfig()))
	withoutDelegation := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy())) // DelegationWeight defaults to 0
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ev := paymentEventAt(10, "evt-1", now) // no Context.DelegatedFrom

	got, err := withDelegation.Analyze(ctx, ev)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if hasResultSignal(got, "delegation_deviation") {
		t.Fatalf("Contributors = %+v, want no delegation_deviation when the event carries no DelegatedFrom", got.Anomaly.Contributors)
	}

	want, err := withoutDelegation.Analyze(ctx, ev)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if got.Anomaly.Score != want.Anomaly.Score || got.Decision != want.Decision {
		t.Errorf("enabling DelegationWeight changed a non-delegated event's result: Score %v->%v, Decision %q->%q", want.Anomaly.Score, got.Anomaly.Score, want.Decision, got.Decision)
	}
}

// TestAnalyzeDelegationFingerprintStability proves DelegatedFrom never
// affects Fingerprint identity through the full engine, with
// DelegationWeight actually enabled this time (task 014's own
// TestAnalyzeAgentDelegationContextScoredIdentically predates this
// signal's existence and runs with the default engine, where
// DelegationWeight is 0).
func TestAnalyzeDelegationFingerprintStability(t *testing.T) {
	ctx := context.Background()
	engine := trustvian.NewEngine(trustvian.WithPolicy(riskGatedPolicy()), trustvian.WithAnomalyConfig(delegationGatedConfig()))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	withA := agentEvent("agent-g", "session-1", "search", now)
	withA.Context.DelegatedFrom = "agent-a"
	resultA, err := engine.Analyze(ctx, withA)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}

	withX := agentEvent("agent-g", "session-1", "search", now)
	withX.ID += "-x"
	withX.Context.DelegatedFrom = "agent-x"
	resultX, err := engine.Analyze(ctx, withX)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}

	if resultA.Fingerprint.ID != resultX.Fingerprint.ID {
		t.Errorf("Fingerprint.ID differs by DelegatedFrom alone: %q vs %q", resultA.Fingerprint.ID, resultX.Fingerprint.ID)
	}
}

// TestAnalyzeDelegationApprovalIndependence is task 031's mandatory
// orthogonality regression (§29/§49/§30 of the task brief): delegation
// behavioral evidence and approval policy evidence must not entangle.
// Case 1: familiar delegation + approval denied -> the approval rule
// still fires on its own terms, low delegation evidence notwithstanding.
// Case 2: novel delegation + approval approved -> the approval rule is
// satisfied (suppressed) on its own terms, elevated delegation evidence
// notwithstanding.
func TestAnalyzeDelegationApprovalIndependence(t *testing.T) {
	ctx := context.Background()
	// A policy whose only rule is the approval requirement — no
	// risk-gate fallback — isolates this test to exactly the
	// interaction it means to prove, uncomplicated by a novel
	// delegator also tripping some unrelated risk-based rule.
	approvalOnly := policy.Policy{
		Rules: []policy.Rule{
			{
				Name:   "shell-execute-requires-approval",
				When:   policy.Condition{OperationCategory: event.OperationCategoryTool, TargetName: "shell.execute"},
				Unless: &policy.Condition{ApprovalStatus: event.ApprovalApproved},
				Action: policy.DecisionBlock,
				Reason: "shell.execute requires approval",
			},
		},
		DefaultAction: policy.DecisionAllow,
		DefaultReason: "no approval requirement configured",
	}
	engine := trustvian.NewEngine(trustvian.WithPolicy(approvalOnly), trustvian.WithAnomalyConfig(delegationGatedConfig()))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := func() time.Time { now = now.Add(time.Second); return now }

	// Train agent-h's familiar delegator, agent-a.
	for i := range 20 {
		ev := agentEvent("agent-h", fmt.Sprintf("session-%d", i), "shell.execute", step())
		ev.Context.DelegatedFrom = "agent-a"
		ev.Context.ApprovalStatus = event.ApprovalApproved
		result, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze (warm-up) error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe (warm-up) error = %v", err)
		}
	}

	// Case 1: familiar delegation + approval denied.
	case1 := agentEvent("agent-h", "session-case1", "shell.execute", step())
	case1.Context.DelegatedFrom = "agent-a"
	case1.Context.ApprovalStatus = event.ApprovalDenied
	result1, err := engine.Analyze(ctx, case1)
	if err != nil {
		t.Fatalf("Analyze (case 1) error = %v", err)
	}
	if hasResultSignal(result1, "delegation_deviation") {
		t.Errorf("case 1: Contributors = %+v, want no delegation_deviation for a familiar delegator", result1.Anomaly.Contributors)
	}
	if result1.Decision != policy.DecisionBlock {
		t.Errorf("case 1: Decision = %q, want %q — the approval requirement must fire regardless of familiar delegation", result1.Decision, policy.DecisionBlock)
	}

	// Case 2: novel delegation + approval approved.
	case2 := agentEvent("agent-h", "session-case2", "shell.execute", step())
	case2.Context.DelegatedFrom = "agent-x"
	case2.Context.ApprovalStatus = event.ApprovalApproved
	result2, err := engine.Analyze(ctx, case2)
	if err != nil {
		t.Fatalf("Analyze (case 2) error = %v", err)
	}
	if !hasResultSignal(result2, "delegation_deviation") {
		t.Errorf("case 2: Contributors = %+v, want delegation_deviation for a never-observed delegator", result2.Anomaly.Contributors)
	}
	if result2.Decision != policy.DecisionAllow {
		t.Errorf("case 2: Decision = %q, want %q — the approval requirement must be satisfied regardless of novel delegation", result2.Decision, policy.DecisionAllow)
	}
}

func hasResultSignal(result trustvian.Result, name string) bool {
	for _, c := range result.Anomaly.Contributors {
		if c.Name == name {
			return true
		}
	}
	return false
}
