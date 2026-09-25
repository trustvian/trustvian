package platform_test

// The promotion domain: construction, derivation, and the two invariants that
// make a recorded decision trustworthy.
//
// What these prove beyond the obvious: the outcome has no input and cannot
// disagree with the evidence, and every identity field is derived from values
// the service loaded rather than accepted from a caller.

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

var promotionEpoch = time.Date(2026, 3, 1, 9, 14, 22, 481_000_321, time.UTC)

// promotionGate builds a gate result over a controllable comparison.
//
// It goes through EvaluateEvaluationGate rather than a fixture literal,
// because the bound marker is what NewPromotion fails closed on and a
// hand-built value would not carry it.
func promotionGate(t *testing.T, limits platform.EvaluationGateLimits) platform.EvaluationGateResult {
	t.Helper()
	return promotionGateWithAddedBehavior(t, limits, false)
}

// promotionGateWithAddedBehavior builds a gate result over a controllable
// comparison, optionally giving the candidate one behavior the reference never
// showed so a strict limit can fail on something real.
func promotionGateWithAddedBehavior(
	t *testing.T, limits platform.EvaluationGateLimits, added bool,
) platform.EvaluationGateResult {
	t.Helper()
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("e1", "fp-1", "read", "staging",
		"allow", "low", event.ApprovalNotRequired, 0.9))
	candidate.feed(t, scorecardRecord("e2", "fp-1", "read", "staging",
		"allow", "low", event.ApprovalNotRequired, 0.9))
	if added {
		candidate.feed(t, scorecardRecord("e3", "fp-2", "write", "staging",
			"allow", "low", event.ApprovalNotRequired, 0.9))
	}
	card := scorecardOf(t, reference, candidate)
	return gateOf(t, card, platform.NewEvaluationGatePolicy(limits))
}

// promotionEnvironments builds a forward-ordered pair in one project.
func promotionEnvironments(t *testing.T) (source, target platform.Environment) {
	t.Helper()
	source = rankedEnvironment(t, "staging", "proj-1", 30)
	target = rankedEnvironment(t, "production", "proj-1", 40)
	return source, target
}

func mustPromotion(t *testing.T, d platform.PromotionDecision) platform.Promotion {
	t.Helper()
	promotion, err := platform.NewPromotion(d)
	if err != nil {
		t.Fatalf("NewPromotion() error = %v", err)
	}
	return promotion
}

func TestNewPromotionDerivesEveryField(t *testing.T) {
	source, target := promotionEnvironments(t)
	gate := promotionGate(t, permissivePolicy().Limits())

	promotion := mustPromotion(t, platform.PromotionDecision{
		ID: "promo-1", Source: source, Target: target,
		GateResult: gate, DecidedAt: promotionEpoch,
	})

	// Nothing below was supplied. Every one came out of the two environments
	// and the gate result, which is what makes the identity unforgeable.
	if promotion.ProjectID() != source.ProjectID() {
		t.Errorf("project = %q, want it derived from the source environment", promotion.ProjectID())
	}
	if promotion.CandidateID() != gate.CandidateCandidateID() {
		t.Errorf("candidate = %q, want the candidate run's candidate", promotion.CandidateID())
	}
	if promotion.ReferenceRunID() != gate.ReferenceRunID() ||
		promotion.CandidateRunID() != gate.CandidateRunID() {
		t.Errorf("run identifiers = %q/%q, want them derived from the gate result",
			promotion.ReferenceRunID(), promotion.CandidateRunID())
	}
	if promotion.Source().Ref != "staging" || promotion.Source().Rank != 30 {
		t.Errorf("source position = %+v, want staging rank 30", promotion.Source())
	}
	if promotion.Target().Ref != "production" || promotion.Target().Rank != 40 {
		t.Errorf("target position = %+v, want production rank 40", promotion.Target())
	}
	if promotion.Source().Revision != source.Revision() ||
		promotion.Target().Revision != target.Revision() {
		t.Error("environment revisions were not snapshotted from the loaded values")
	}

	// The limits come out of the gate result's three maximums, not from a
	// second copy the caller could have disagreed with.
	limits := promotion.GateLimits()
	if limits.MaxAddedBehaviors != gate.AddedBehaviors().Maximum ||
		limits.MaxBlockDecisions != gate.BlockDecisions().Maximum ||
		limits.MaxCriticalRiskObservations != gate.CriticalRiskObservations().Maximum {
		t.Errorf("limits = %+v, want the gate result's own maximums", limits)
	}
	if !promotion.DecidedAt().Equal(promotionEpoch) {
		t.Errorf("decided at = %v, want %v", promotion.DecidedAt(), promotionEpoch)
	}
}

// The outcome has no input, and cannot contradict the evidence.
func TestPromotionOutcomeIsDerivedFromTheVerdict(t *testing.T) {
	source, target := promotionEnvironments(t)

	tests := []struct {
		name   string
		limits platform.EvaluationGateLimits
		want   platform.PromotionOutcome
	}{
		{"permissive limits pass", permissivePolicy().Limits(), platform.PromotionAccepted},
		{"strict limits fail", platform.EvaluationGateLimits{}, platform.PromotionRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := promotionGateWithAddedBehavior(
				t, tt.limits, tt.want == platform.PromotionRejected)
			promotion := mustPromotion(t, platform.PromotionDecision{
				ID: "promo-1", Source: source, Target: target,
				GateResult: gate, DecidedAt: promotionEpoch,
			})
			if promotion.Outcome() != tt.want {
				t.Errorf("outcome = %q, want %q for verdict %q",
					promotion.Outcome(), tt.want, gate.Verdict())
			}
			if promotion.GateResult().Verdict() != gate.Verdict() {
				t.Error("the stored gate result is not the one the decision consumed")
			}
		})
	}
}

func TestNewPromotionValidation(t *testing.T) {
	source, target := promotionEnvironments(t)
	gate := promotionGate(t, permissivePolicy().Limits())
	long := strings.Repeat("p", 257)

	tests := []struct {
		name     string
		decision platform.PromotionDecision
		wantErr  error
	}{
		{"empty id", platform.PromotionDecision{
			Source: source, Target: target, GateResult: gate, DecidedAt: promotionEpoch,
		}, platform.ErrInvalidID},
		{"id over the length bound", platform.PromotionDecision{
			ID: platform.PromotionID(long), Source: source, Target: target,
			GateResult: gate, DecidedAt: promotionEpoch,
		}, platform.ErrInvalidID},
		{"id with a control character", platform.PromotionDecision{
			ID: "promo\n1", Source: source, Target: target,
			GateResult: gate, DecidedAt: promotionEpoch,
		}, platform.ErrInvalidID},
		{"zero decided at", platform.PromotionDecision{
			ID: "promo-1", Source: source, Target: target, GateResult: gate,
		}, platform.ErrInvalidTimestamp},
		{"unbound gate result", platform.PromotionDecision{
			ID: "promo-1", Source: source, Target: target, DecidedAt: promotionEpoch,
		}, platform.ErrInvalidGateEvidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := platform.NewPromotion(tt.decision); !errors.Is(err, tt.wantErr) {
				t.Errorf("NewPromotion() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// The gate result must gate the comparison being recorded, not a different one.
func TestNewPromotionRefusesMismatchedGateIdentity(t *testing.T) {
	_, target := promotionEnvironments(t)
	gate := promotionGate(t, permissivePolicy().Limits()) // environment "staging"

	// A source whose ref is not the environment the gate result names.
	elsewhere := rankedEnvironment(t, "sandbox", "proj-1", 20)
	if _, err := platform.NewPromotion(platform.PromotionDecision{
		ID: "promo-1", Source: elsewhere, Target: target,
		GateResult: gate, DecidedAt: promotionEpoch,
	}); !errors.Is(err, platform.ErrInvalidGateEvidence) {
		t.Errorf("mismatched environment error = %v, want ErrInvalidGateEvidence", err)
	}
}

// Ordering is asked of the one primitive, and every pair CanPromote refuses is
// refused here.
func TestNewPromotionRefusesEveryPairCanPromoteDoes(t *testing.T) {
	gate := promotionGate(t, permissivePolicy().Limits())
	staging := rankedEnvironment(t, "staging", "proj-1", 30)
	unranked := mustEnvironment(t, "staging", "proj-1", "Staging")
	archived, _ := staging.Archive()
	production := rankedEnvironment(t, "production", "proj-1", 40)
	archivedTarget, _ := production.Archive()
	unrankedTarget := mustEnvironment(t, "production", "proj-1", "Production")
	otherProject := rankedEnvironment(t, "production", "proj-2", 40)

	tests := []struct {
		name           string
		source, target platform.Environment
	}{
		{"unranked source", unranked, production},
		{"unranked target", staging, unrankedTarget},
		{"archived source", archived, production},
		{"archived target", staging, archivedTarget},
		{"equal ranks", staging, rankedEnvironment(t, "staging-eu", "proj-1", 30)},
		{"backward", production, staging},
		{"same environment", staging, staging},
		{"different projects", staging, otherProject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := platform.NewPromotion(platform.PromotionDecision{
				ID: "promo-1", Source: tt.source, Target: tt.target,
				GateResult: gate, DecidedAt: promotionEpoch,
			}); !errors.Is(err, platform.ErrPromotionOrder) {
				t.Errorf("NewPromotion() error = %v, want ErrPromotionOrder", err)
			}
		})
	}
}

// A forward jump over an intermediate rank is permitted: forward is forward,
// and whether a workflow allows the jump is a policy nobody has asked for.
func TestNewPromotionAllowsStageSkipping(t *testing.T) {
	dev := rankedEnvironment(t, "staging", "proj-1", 10)
	production := rankedEnvironment(t, "production", "proj-1", 40)
	gate := promotionGate(t, permissivePolicy().Limits())

	promotion := mustPromotion(t, platform.PromotionDecision{
		ID: "promo-skip", Source: dev, Target: production,
		GateResult: gate, DecidedAt: promotionEpoch,
	})
	if promotion.Source().Rank != 10 || promotion.Target().Rank != 40 {
		t.Errorf("skipped promotion = %+v → %+v, want rank 10 toward 40",
			promotion.Source(), promotion.Target())
	}
}

// A Promotion is finished when it exists: no transition, no setter, no
// lifecycle. Asserted structurally so a later With…/Set… cannot appear
// quietly.
func TestPromotionHasNoMutator(t *testing.T) {
	forbidden := []string{"Update", "Set", "With", "Archive", "Activate", "Cancel"}
	value := platform.Promotion{}
	typ := reflect.TypeOf(value)
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		for _, prefix := range forbidden {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("Promotion has method %s; a recorded decision is immutable", name)
			}
		}
	}
}

// EnvironmentPosition carries exactly three fields, so a later Name, Status or
// Endpoint fails here first.
func TestEnvironmentPositionFieldCount(t *testing.T) {
	typ := reflect.TypeOf(platform.EnvironmentPosition{})
	want := map[string]bool{"Ref": true, "Rank": true, "Revision": true}
	if typ.NumField() != len(want) {
		t.Fatalf("EnvironmentPosition has %d fields, want %d", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		if !want[typ.Field(i).Name] {
			t.Errorf("unexpected field %s; a position is a snapshot, not an environment",
				typ.Field(i).Name)
		}
	}
}
