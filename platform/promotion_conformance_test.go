package platform

// Promotion storage, proven identically on both backends.
//
// The interesting cases are the two that only a store can get wrong: a
// historical gate result that must come back verbatim, and a decision that
// must not commit against environment configuration that moved under it.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

// seedPromotionFixture builds a project, an agent, two candidates and a
// forward-ordered environment pair, and returns a promotion ready to store.
func seedPromotionFixture(t testing.TB, store Store, projectID string) Promotion {
	t.Helper()
	ctx := context.Background()

	seedProject(t, store, projectID)
	agent, err := NewAgent(AgentID("agent-"+projectID), ProjectID(projectID), "Agent")
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if err := store.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	for _, id := range []string{"cand-ref-" + projectID, "cand-can-" + projectID} {
		candidate, err := NewCandidate(CandidateID(id), agent.ID(), CandidateMetadata{Label: "v1"})
		if err != nil {
			t.Fatalf("NewCandidate() error = %v", err)
		}
		if err := store.CreateCandidate(ctx, candidate); err != nil {
			t.Fatalf("seed candidate %s: %v", id, err)
		}
	}
	for _, env := range []struct {
		ref  string
		rank uint16
	}{{"staging", 30}, {"production", 40}} {
		value, err := NewRankedEnvironment(
			EnvironmentRef(env.ref), ProjectID(projectID), env.ref, env.rank)
		if err != nil {
			t.Fatalf("NewRankedEnvironment() error = %v", err)
		}
		if err := store.CreateEnvironment(ctx, value); err != nil {
			t.Fatalf("seed environment %s: %v", env.ref, err)
		}
	}
	return promotionFor(t, store, projectID, "promo-1")
}

// promotionFor builds one storable decision against the seeded environments.
func promotionFor(t testing.TB, store Store, projectID, id string) Promotion {
	t.Helper()
	ctx := context.Background()

	source, err := store.Environment(ctx, ProjectID(projectID), "staging")
	if err != nil {
		t.Fatalf("Environment(staging) error = %v", err)
	}
	target, err := store.Environment(ctx, ProjectID(projectID), "production")
	if err != nil {
		t.Fatalf("Environment(production) error = %v", err)
	}

	gate, err := restoreEvaluationGateResult(
		EvaluationRunID("run-ref-"+projectID), CandidateID("cand-ref-"+projectID),
		EvaluationRunID("run-can-"+projectID), CandidateID("cand-can-"+projectID),
		"staging",
		MinimumCountGate{Actual: 412, Minimum: 1, Passed: true},
		MinimumCountGate{Actual: 388, Minimum: 1, Passed: true},
		MaximumCountGate{Actual: 5, Maximum: math.MaxUint64, Passed: true},
		MaximumCountGate{Actual: 0, Maximum: 0, Passed: true},
		MaximumCountGate{Actual: 0, Maximum: 0, Passed: true},
		GateVerdictPass,
	)
	if err != nil {
		t.Fatalf("restoreEvaluationGateResult() error = %v", err)
	}

	promotion, err := NewPromotion(PromotionDecision{
		ID: PromotionID(id), Source: source, Target: target,
		GateResult: gate,
		DecidedAt:  time.Date(2026, 3, 1, 9, 14, 22, 481_000_321, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewPromotion() error = %v", err)
	}
	return promotion
}

func conformPromotions(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	promotion := seedPromotionFixture(t, store, "proj-1")

	if err := store.CreatePromotion(ctx, promotion); err != nil {
		t.Fatalf("CreatePromotion() error = %v", err)
	}

	stored, err := store.Promotion(ctx, "promo-1")
	if err != nil {
		t.Fatalf("Promotion() error = %v", err)
	}

	// Every field, including nanoseconds and the whole gate result.
	if stored.ProjectID() != promotion.ProjectID() ||
		stored.CandidateID() != promotion.CandidateID() ||
		stored.ReferenceRunID() != promotion.ReferenceRunID() ||
		stored.CandidateRunID() != promotion.CandidateRunID() ||
		stored.Source() != promotion.Source() ||
		stored.Target() != promotion.Target() ||
		stored.GateLimits() != promotion.GateLimits() ||
		stored.Outcome() != promotion.Outcome() {
		t.Errorf("stored = %+v, want the value written", stored)
	}
	if !stored.DecidedAt().Equal(promotion.DecidedAt()) {
		t.Errorf("decided at = %v, want %v with nanoseconds intact",
			stored.DecidedAt(), promotion.DecidedAt())
	}
	if stored.GateResult() != promotion.GateResult() {
		t.Errorf("gate result = %+v, want the one the decision consumed",
			stored.GateResult())
	}

	// A duplicate identifier is refused, and the first decision stands.
	if err := store.CreatePromotion(ctx, promotion); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate CreatePromotion() error = %v, want ErrStoreAlreadyExists", err)
	}

	// A missing promotion is not found rather than empty.
	if _, err := store.Promotion(ctx, "promo-absent"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("Promotion(absent) error = %v, want ErrStoreNotFound", err)
	}
}

// The store commits only against the environment state the decision used.
func conformPromotionStaleness(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()

	mutate := func(t *testing.T, store Store, ref EnvironmentRef, change func(Environment) Environment) {
		t.Helper()
		current, err := store.Environment(ctx, "proj-1", ref)
		if err != nil {
			t.Fatalf("Environment(%s) error = %v", ref, err)
		}
		if err := store.UpdateEnvironment(ctx, current, change(current)); err != nil {
			t.Fatalf("UpdateEnvironment(%s) error = %v", ref, err)
		}
	}

	tests := []struct {
		name  string
		stale func(t *testing.T, store Store)
	}{
		{"source archived", func(t *testing.T, store Store) {
			mutate(t, store, "staging", func(e Environment) Environment {
				archived, _ := e.Archive()
				return archived
			})
		}},
		{"target archived", func(t *testing.T, store Store) {
			mutate(t, store, "production", func(e Environment) Environment {
				archived, _ := e.Archive()
				return archived
			})
		}},
		{"target re-ranked below the source", func(t *testing.T, store Store) {
			mutate(t, store, "production", func(e Environment) Environment {
				ranked, _ := e.WithRank(10)
				return ranked
			})
		}},
		// A rename changes nothing CanPromote reads and still invalidates the
		// attempt: the revision is the binding, and deciding which fields
		// matter would put a configuration comparison outside CanPromote.
		{"source renamed", func(t *testing.T, store Store) {
			mutate(t, store, "staging", func(e Environment) Environment {
				renamed, _ := e.Rename("Staging EU")
				return renamed
			})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := open(t)
			promotion := seedPromotionFixture(t, store, "proj-1")

			tt.stale(t, store)

			if err := store.CreatePromotion(ctx, promotion); !errors.Is(err, ErrStoreConflict) {
				t.Fatalf("CreatePromotion() error = %v, want ErrStoreConflict", err)
			}
			if _, err := store.Promotion(ctx, "promo-1"); !errors.Is(err, ErrStoreNotFound) {
				t.Errorf("a stale decision was committed: %v", err)
			}
		})
	}

	// Nothing changed: it commits, carrying the current revisions.
	t.Run("unchanged commits", func(t *testing.T) {
		store := open(t)
		promotion := seedPromotionFixture(t, store, "proj-1")
		if err := store.CreatePromotion(ctx, promotion); err != nil {
			t.Fatalf("CreatePromotion() error = %v", err)
		}
	})

	// And a retry after a conflict succeeds against the new configuration,
	// proving the conflict is a retryable race rather than a dead end.
	t.Run("retry after conflict", func(t *testing.T) {
		store := open(t)
		stale := seedPromotionFixture(t, store, "proj-1")

		mutate(t, store, "production", func(e Environment) Environment {
			renamed, _ := e.Rename("Production EU")
			return renamed
		})
		if err := store.CreatePromotion(ctx, stale); !errors.Is(err, ErrStoreConflict) {
			t.Fatalf("stale CreatePromotion() error = %v, want ErrStoreConflict", err)
		}

		retried := promotionFor(t, store, "proj-1", "promo-1")
		if err := store.CreatePromotion(ctx, retried); err != nil {
			t.Fatalf("retried CreatePromotion() error = %v", err)
		}
		stored, err := store.Promotion(ctx, "promo-1")
		if err != nil {
			t.Fatalf("Promotion() error = %v", err)
		}
		if stored.Target().Revision == stale.Target().Revision {
			t.Error("the retry recorded the stale revision")
		}
	})
}

func conformPromotionPaging(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedPromotionFixture(t, store, "proj-1")
	seedPromotionFixture(t, store, "proj-2")

	// Identifiers chosen so locale-aware collation would order them
	// differently from byte order — the proof that COLLATE "C" is doing its
	// job on PostgreSQL.
	ids := []string{"promo-B", "promo-a", "promo-C", "promo-b"}
	for _, id := range ids {
		if err := store.CreatePromotion(ctx, promotionFor(t, store, "proj-1", id)); err != nil {
			t.Fatalf("CreatePromotion(%s) error = %v", id, err)
		}
	}

	page, err := store.ProjectPromotions(ctx, "proj-1", "", MaxPromotionPage)
	if err != nil {
		t.Fatalf("ProjectPromotions() error = %v", err)
	}
	// The fixture builds a promotion but stores none, so the page holds
	// exactly the four written above.
	if len(page) != len(ids) {
		t.Fatalf("page holds %d promotions, want %d", len(page), len(ids))
	}
	for i := 1; i < len(page); i++ {
		if string(page[i-1].ID()) >= string(page[i].ID()) {
			t.Fatalf("ordering is not byte-ascending: %q then %q",
				page[i-1].ID(), page[i].ID())
		}
	}

	// Exclusive cursor, and the limit is honoured.
	after, err := store.ProjectPromotions(ctx, "proj-1", page[0].ID(), 2)
	if err != nil {
		t.Fatalf("ProjectPromotions(after) error = %v", err)
	}
	if len(after) != 2 || after[0].ID() != page[1].ID() {
		t.Errorf("cursor page = %d rows starting %q, want 2 starting %q",
			len(after), after[0].ID(), page[1].ID())
	}

	// The bound is exactly MaxPromotionPage at the store edge.
	if _, err := store.ProjectPromotions(ctx, "proj-1", "", MaxPromotionPage); err != nil {
		t.Errorf("limit %d error = %v, want the documented maximum accepted",
			MaxPromotionPage, err)
	}
	for _, limit := range []int{0, -1, MaxPromotionPage + 1, 500} {
		if _, err := store.ProjectPromotions(ctx, "proj-1", "", limit); !errors.Is(err, ErrInvalidID) {
			t.Errorf("limit %d error = %v, want it refused", limit, err)
		}
	}

	// Scope: another project's history never appears.
	other, err := store.ProjectPromotions(ctx, "proj-2", "", MaxPromotionPage)
	if err != nil {
		t.Fatalf("ProjectPromotions(proj-2) error = %v", err)
	}
	if len(other) != 0 {
		t.Errorf("proj-2 holds %d promotions, want none of proj-1's", len(other))
	}

	// A project that does not exist is not found; one with no history is
	// empty rather than missing.
	if _, err := store.ProjectPromotions(ctx, "absent", "", 10); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("ProjectPromotions(absent) error = %v, want ErrStoreNotFound", err)
	}
}

// A project larger than one page enumerates completely, with no duplicate and
// no omission.
func conformPromotionPagingEnumeratesEverything(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedPromotionFixture(t, store, "proj-1")

	const total = 130
	for i := range total {
		id := fmt.Sprintf("promo-%04d", i)
		if err := store.CreatePromotion(ctx, promotionFor(t, store, "proj-1", id)); err != nil {
			t.Fatalf("CreatePromotion(%s) error = %v", id, err)
		}
	}

	seen := map[PromotionID]int{}
	after := PromotionID("")
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("pagination did not terminate")
		}
		rows, err := store.ProjectPromotions(ctx, "proj-1", after, MaxPromotionPage)
		if err != nil {
			t.Fatalf("ProjectPromotions() error = %v", err)
		}
		for _, row := range rows {
			seen[row.ID()]++
		}
		if len(rows) < MaxPromotionPage {
			break
		}
		after = rows[len(rows)-1].ID()
	}

	if len(seen) != total {
		t.Fatalf("enumerated %d promotions, want %d", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("%s appeared %d times in one traversal", id, count)
		}
	}
}
