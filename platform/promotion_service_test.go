package platform_test

// The control plane's half of task 066: the ordered preconditions, and the
// two outcomes.
//
// What these prove beyond the obvious: a structural failure writes nothing
// while a gate FAIL writes a record, the comparison is reused rather than
// copied, and every identity on the stored decision came from evidence the
// service loaded.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	platform "trustvian-platform"
)

var decisionAt = time.Date(2026, 3, 1, 9, 14, 22, 481_000_321, time.UTC)

// promotionFixture seeds one project with a forward-ordered environment pair
// and two completed runs of one agent.
type promotionFixture struct {
	*controlPlaneFixture
}

func newPromotionFixture(t *testing.T, candidateOperations []string) *promotionFixture {
	t.Helper()
	f := &promotionFixture{newFixture(t)}

	f.completeEvaluation(t, "run-ref", "cand-ref", []string{"read"})
	f.completeEvaluation(t, "run-can", "cand-can", candidateOperations)

	// The source is the environment both runs name; the target is forward of
	// it. Both must be ranked and active for CanPromote to be true.
	f.rankEnvironment(t, fixtureEnvironment, 30)
	f.createRankedEnvironment(t, "production", 40)
	return f
}

func (f *promotionFixture) rankEnvironment(
	t *testing.T, ref platform.EnvironmentRef, rank uint16,
) {
	t.Helper()
	current, err := f.plane.Environment(t.Context(), "proj-1", ref)
	if err != nil {
		t.Fatalf("Environment(%s) error = %v", ref, err)
	}
	if _, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", ref,
		platform.ConfigureEnvironmentRequest{
			Revision: current.Revision(), Rank: &rank,
		}); err != nil {
		t.Fatalf("ConfigureEnvironment(%s) error = %v", ref, err)
	}
}

func (f *promotionFixture) createRankedEnvironment(
	t *testing.T, ref platform.EnvironmentRef, rank uint16,
) {
	t.Helper()
	env, err := platform.NewRankedEnvironment(ref, "proj-1", string(ref), rank)
	if err != nil {
		t.Fatalf("NewRankedEnvironment() error = %v", err)
	}
	if err := f.plane.CreateEnvironment(t.Context(), env); err != nil {
		t.Fatalf("CreateEnvironment(%s) error = %v", ref, err)
	}
}

func promotionRequest(limits platform.EvaluationGateLimits) platform.PromotionRequest {
	return platform.PromotionRequest{
		ID:                "promo-1",
		ReferenceRunID:    "run-ref",
		CandidateRunID:    "run-can",
		TargetEnvironment: "production",
		GateLimits:        limits,
	}
}

func permissiveGateLimits() platform.EvaluationGateLimits {
	return permissivePolicy().Limits()
}

// A forward promotion whose gate passes is recorded as accepted, and every
// derived field is the one the evidence implies.
func TestPromoteRecordsAnAcceptedDecision(t *testing.T) {
	f := newPromotionFixture(t, []string{"read"})

	promotion, err := f.plane.Promote(t.Context(), promotionRequest(permissiveGateLimits()), decisionAt)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}

	if promotion.Outcome() != platform.PromotionAccepted {
		t.Errorf("outcome = %q, want accepted", promotion.Outcome())
	}
	if promotion.GateResult().Verdict() != platform.GateVerdictPass {
		t.Errorf("stored verdict = %q, want pass", promotion.GateResult().Verdict())
	}
	if promotion.ProjectID() != "proj-1" || promotion.CandidateID() != "cand-can" {
		t.Errorf("derived identity = %s/%s, want proj-1/cand-can",
			promotion.ProjectID(), promotion.CandidateID())
	}
	if promotion.Source().Ref != fixtureEnvironment || promotion.Target().Ref != "production" {
		t.Errorf("environments = %s → %s, want %s → production",
			promotion.Source().Ref, promotion.Target().Ref, fixtureEnvironment)
	}
	if promotion.Source().Rank != 30 || promotion.Target().Rank != 40 {
		t.Errorf("ranks = %d → %d, want 30 → 40",
			promotion.Source().Rank, promotion.Target().Rank)
	}

	// And it is durable, byte-identical.
	stored, err := f.plane.Promotion(t.Context(), "promo-1")
	if err != nil {
		t.Fatalf("Promotion() error = %v", err)
	}
	if stored.Outcome() != promotion.Outcome() ||
		!stored.DecidedAt().Equal(promotion.DecidedAt()) ||
		stored.GateResult().AddedBehaviors() != promotion.GateResult().AddedBehaviors() {
		t.Error("the stored decision differs from the one returned")
	}
}

// A gate FAIL is a decision, not an error: it is recorded, and it is recorded
// as rejected.
func TestPromoteRecordsARejectedDecision(t *testing.T) {
	// The candidate exhibits a behavior the reference never did, which a
	// zero added-behavior limit refuses.
	f := newPromotionFixture(t, []string{"read", "export"})

	promotion, err := f.plane.Promote(t.Context(),
		promotionRequest(platform.EvaluationGateLimits{}), decisionAt)
	if err != nil {
		t.Fatalf("a gate FAIL must be a recorded decision, not an error: %v", err)
	}
	if promotion.Outcome() != platform.PromotionRejected {
		t.Errorf("outcome = %q, want rejected", promotion.Outcome())
	}
	if promotion.GateResult().Verdict() != platform.GateVerdictFail {
		t.Errorf("verdict = %q, want fail", promotion.GateResult().Verdict())
	}
	if promotion.GateResult().AddedBehaviors().Passed {
		t.Error("the added-behavior check passed; the fixture did not produce a new behavior")
	}
	if _, err := f.plane.Promotion(t.Context(), "promo-1"); err != nil {
		t.Errorf("a rejected decision must be durable: %v", err)
	}
}

// Every structural precondition, and none of them writes a row.
func TestPromoteStructuralFailuresWriteNothing(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, f *promotionFixture) platform.PromotionRequest
		wantErr error
	}{
		{
			name: "malformed promotion id",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				request := promotionRequest(permissiveGateLimits())
				request.ID = "promo\n1"
				return request
			},
			wantErr: platform.ErrInvalidID,
		},
		{
			name: "the same run on both sides",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				request := promotionRequest(permissiveGateLimits())
				request.ReferenceRunID = request.CandidateRunID
				return request
			},
			wantErr: platform.ErrInvalidID,
		},
		{
			name: "candidate run is not completed",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				f.seedRunning(t, "run-open")
				request := promotionRequest(permissiveGateLimits())
				request.CandidateRunID = "run-open"
				return request
			},
			wantErr: platform.ErrEvaluationState,
		},
		{
			name: "reference run does not exist",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				request := promotionRequest(permissiveGateLimits())
				request.ReferenceRunID = "run-absent"
				return request
			},
			wantErr: platform.ErrStoreNotFound,
		},
		{
			name: "target environment does not exist",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				request := promotionRequest(permissiveGateLimits())
				request.TargetEnvironment = "nowhere"
				return request
			},
			wantErr: platform.ErrStoreNotFound,
		},
		{
			name: "target is not forward of the source",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				// Demote the target below the source.
				f.rankEnvironment(t, "production", 10)
				return promotionRequest(permissiveGateLimits())
			},
			wantErr: platform.ErrPromotionOrder,
		},
		{
			name: "target is archived",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				current, err := f.plane.Environment(t.Context(), "proj-1", "production")
				if err != nil {
					t.Fatalf("Environment() error = %v", err)
				}
				if _, err := f.plane.ArchiveEnvironment(
					t.Context(), "proj-1", "production", current.Revision()); err != nil {
					t.Fatalf("ArchiveEnvironment() error = %v", err)
				}
				return promotionRequest(permissiveGateLimits())
			},
			wantErr: platform.ErrPromotionOrder,
		},
		{
			name: "source is unranked",
			arrange: func(t *testing.T, f *promotionFixture) platform.PromotionRequest {
				current, err := f.plane.Environment(t.Context(), "proj-1", fixtureEnvironment)
				if err != nil {
					t.Fatalf("Environment() error = %v", err)
				}
				if _, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", fixtureEnvironment,
					platform.ConfigureEnvironmentRequest{
						Revision: current.Revision(), ClearRank: true,
					}); err != nil {
					t.Fatalf("ConfigureEnvironment() error = %v", err)
				}
				return promotionRequest(permissiveGateLimits())
			},
			wantErr: platform.ErrPromotionOrder,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPromotionFixture(t, []string{"read"})
			request := tt.arrange(t, f)

			if _, err := f.plane.Promote(t.Context(), request, decisionAt); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Promote() error = %v, want %v", err, tt.wantErr)
			}
			// No decision was reached, so none is recorded. Checked through
			// the project's history rather than by identifier, because a
			// malformed identifier is refused by the read path too and would
			// mask an actual write.
			page, err := f.plane.ProjectPromotions(t.Context(), "proj-1", "", 64)
			if err != nil {
				t.Fatalf("ProjectPromotions() error = %v", err)
			}
			if len(page) != 0 {
				t.Errorf("a structural failure wrote %d promotions, want none", len(page))
			}
		})
	}
}

// Two runs of different agents in one project are a valid comparison and an
// invalid promotion. This is the narrower rule task 065 said a promotion
// workflow would add alongside itself.
func TestPromoteRequiresOneAgent(t *testing.T) {
	f := &promotionFixture{newFixture(t)}
	ctx := t.Context()

	f.completeEvaluation(t, "run-ref", "cand-ref", []string{"read"})
	f.completeEvaluation(t, "run-can", "cand-can", []string{"read"})
	f.rankEnvironment(t, fixtureEnvironment, 30)
	f.createRankedEnvironment(t, "production", 40)

	// A second agent in the same project, whose candidate runs the reference.
	otherAgent, _ := platform.NewAgent("agent-2", "proj-1", "Other agent")
	otherCandidate, _ := platform.NewCandidate("cand-other", "agent-2",
		platform.CandidateMetadata{Label: "v1"})
	if err := f.plane.CreateAgent(ctx, otherAgent); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	if err := f.plane.CreateCandidate(ctx, otherCandidate); err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	f.completeEvaluationForCandidate(t, "run-other", "cand-other", []string{"read"})

	request := promotionRequest(permissiveGateLimits())
	request.ReferenceRunID = "run-other"
	if _, err := f.plane.Promote(ctx, request, decisionAt); !errors.Is(
		err, platform.ErrPromotionScope) {
		t.Fatalf("cross-agent promotion error = %v, want ErrPromotionScope", err)
	}

	// And the comparison itself is still allowed, so this task tightened
	// promotion without tightening CompareEvaluations.
	if _, err := f.plane.CompareEvaluations(
		ctx, "run-other", "run-can", permissiveGateLimits()); err != nil {
		t.Errorf("CompareEvaluations across two agents error = %v, want it still allowed", err)
	}
}

// Stage skipping is permitted: forward is forward.
func TestPromoteAllowsStageSkipping(t *testing.T) {
	f := newPromotionFixture(t, []string{"read"})
	f.createRankedEnvironment(t, "sandbox", 35) // an intermediate rank, skipped

	promotion, err := f.plane.Promote(t.Context(),
		promotionRequest(permissiveGateLimits()), decisionAt)
	if err != nil {
		t.Fatalf("Promote() over an intermediate rank error = %v", err)
	}
	if promotion.Target().Ref != "production" {
		t.Errorf("target = %s, want production", promotion.Target().Ref)
	}
}

// Two decisions about the same candidate, source and target both persist:
// the tuple is legitimately repeatable and only the identifier is identity.
func TestPromoteRecordsTwoDecisionsAboutOneTuple(t *testing.T) {
	f := newPromotionFixture(t, []string{"read"})
	ctx := t.Context()

	first := promotionRequest(permissiveGateLimits())
	second := promotionRequest(platform.EvaluationGateLimits{})
	second.ID = "promo-2"

	if _, err := f.plane.Promote(ctx, first, decisionAt); err != nil {
		t.Fatalf("first Promote() error = %v", err)
	}
	if _, err := f.plane.Promote(ctx, second, decisionAt.Add(time.Minute)); err != nil {
		t.Fatalf("second Promote() error = %v", err)
	}

	page, err := f.plane.ProjectPromotions(ctx, "proj-1", "", 64)
	if err != nil {
		t.Fatalf("ProjectPromotions() error = %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("history holds %d decisions, want 2", len(page))
	}
}

// A duplicate identifier is a conflict, and the first decision stands.
func TestPromoteRefusesADuplicateIdentifier(t *testing.T) {
	f := newPromotionFixture(t, []string{"read"})
	ctx := t.Context()

	if _, err := f.plane.Promote(ctx, promotionRequest(permissiveGateLimits()), decisionAt); err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if _, err := f.plane.Promote(ctx, promotionRequest(permissiveGateLimits()),
		decisionAt.Add(time.Hour)); !errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Fatalf("duplicate Promote() error = %v, want ErrStoreAlreadyExists", err)
	}

	stored, err := f.plane.Promotion(ctx, "promo-1")
	if err != nil {
		t.Fatalf("Promotion() error = %v", err)
	}
	if !stored.DecidedAt().Equal(decisionAt) {
		t.Errorf("decided at = %v, want the first decision's %v", stored.DecidedAt(), decisionAt)
	}
}

// Configuration that moves after a decision leaves the record untouched. The
// snapshot is what the decision was made against, and it does not follow the
// environment.
func TestRecordedPromotionSurvivesEnvironmentChanges(t *testing.T) {
	f := newPromotionFixture(t, []string{"read"})
	ctx := t.Context()

	promotion, err := f.plane.Promote(ctx, promotionRequest(permissiveGateLimits()), decisionAt)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}

	// Re-rank, rename and archive the target afterwards.
	current, _ := f.plane.Environment(ctx, "proj-1", "production")
	renamed := "Production EU"
	next, err := f.plane.ConfigureEnvironment(ctx, "proj-1", "production",
		platform.ConfigureEnvironmentRequest{Revision: current.Revision(), Name: &renamed})
	if err != nil {
		t.Fatalf("ConfigureEnvironment() error = %v", err)
	}
	if _, err := f.plane.ArchiveEnvironment(ctx, "proj-1", "production", next.Revision()); err != nil {
		t.Fatalf("ArchiveEnvironment() error = %v", err)
	}

	stored, err := f.plane.Promotion(ctx, "promo-1")
	if err != nil {
		t.Fatalf("Promotion() after environment changes error = %v", err)
	}
	if stored.Target() != promotion.Target() {
		t.Errorf("target position = %+v, want the decision-time snapshot %+v",
			stored.Target(), promotion.Target())
	}
	if stored.Outcome() != promotion.Outcome() {
		t.Error("the recorded outcome changed when the environment did")
	}

	// And the stored revision now differs from the environment's current one,
	// which is the detectability the field exists for.
	live, _ := f.plane.Environment(ctx, "proj-1", "production")
	if live.Revision() == stored.Target().Revision {
		t.Error("the environment's revision did not advance; the test proves nothing")
	}
}

// completeEvaluationForCandidate drives one run of an already-seeded candidate.
func (f *promotionFixture) completeEvaluationForCandidate(
	t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID,
	operations []string,
) {
	t.Helper()
	ctx := t.Context()

	run, err := platform.NewEvaluationRun(
		runID, candidateID, fixtureEnvironment, fixtureProfile, aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := f.plane.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	mustStart(t, f.controlPlaneFixture, runID)
	for i, op := range operations {
		if _, err := f.ingest(t, runID, uint64(i+1),
			ingestRecord(fmt.Sprintf("%s-evt-%d", runID, i), "fp-"+op, op)); err != nil {
			t.Fatalf("ingest error = %v", err)
		}
	}
	if _, err := f.plane.CompleteEvaluationRun(ctx, runID, aggEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}
}
