package httpapi_test

// The promotion routes.
//
// Three routes, and two properties worth more than the shapes: a gate FAIL is
// a `201` because the platform decided, and every field the server derives is
// refused in a request body so no caller and no adapter bug can forward one.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	platform "trustvian-platform"
)

type promotionEnvironmentBody struct {
	Ref      string `json:"ref"`
	Rank     uint16 `json:"rank"`
	Revision string `json:"revision"`
}

type promotionBody struct {
	Version   string `json:"version"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`

	CandidateID          string `json:"candidate_id"`
	ReferenceCandidateID string `json:"reference_candidate_id"`

	ReferenceRunID string `json:"reference_run_id"`
	CandidateRunID string `json:"candidate_run_id"`

	SourceEnvironment promotionEnvironmentBody `json:"source_environment"`
	TargetEnvironment promotionEnvironmentBody `json:"target_environment"`

	GateLimits struct {
		MaxAddedBehaviors           string `json:"max_added_behaviors"`
		MaxBlockDecisions           string `json:"max_block_decisions"`
		MaxCriticalRiskObservations string `json:"max_critical_risk_observations"`
	} `json:"gate_limits"`

	GateResult struct {
		ReferenceEvidence struct {
			Actual  string `json:"actual"`
			Minimum string `json:"minimum"`
			Passed  bool   `json:"passed"`
		} `json:"reference_evidence"`
		AddedBehaviors struct {
			Actual  string `json:"actual"`
			Maximum string `json:"maximum"`
			Passed  bool   `json:"passed"`
		} `json:"added_behaviors"`
		Verdict string `json:"verdict"`
	} `json:"gate_result"`

	Outcome   string `json:"outcome"`
	DecidedAt string `json:"decided_at"`
}

type promotionListBody struct {
	Version    string          `json:"version"`
	ProjectID  string          `json:"project_id"`
	Promotions []promotionBody `json:"promotions"`
	NextAfter  string          `json:"next_after"`
}

func decodePromotion(t *testing.T, raw []byte) promotionBody {
	t.Helper()
	var body promotionBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding promotion response: %v (%s)", err, raw)
	}
	return body
}

// seedPromotable drives two completed runs of one agent and ranks a forward
// environment pair, which is the minimum a promotion needs.
func (a *api) seedPromotable() {
	a.t.Helper()
	a.completeRun("run-ref", "cand-1", []string{"read"})
	a.completeRun("run-can", "cand-2", []string{"read"})

	source := decodeEnvironment(a.t, a.do(
		"GET", "/v1/projects/proj-1/environments/"+testEnvironment, nil).Body.Bytes())
	rank := uint16(30)
	a.mustStatus(a.do("POST",
		"/v1/projects/proj-1/environments/"+testEnvironment+"/configure", map[string]any{
			"revision": source.Revision, "rank": rank,
		}), 200, "rank the source")

	a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "production", "name": "Production", "rank": 40,
	}), 201, "create the target")
}

func promotionRequestBody(id, added, block, critical string) map[string]any {
	return map[string]any{
		"id":                 id,
		"reference_run_id":   "run-ref",
		"candidate_run_id":   "run-can",
		"target_environment": "production",
		"gate_limits":        limitsBody(added, block, critical),
	}
}

func TestPromotionRoundTripOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.seedPromotable()
	maxUint := platform.FormatSequence(math.MaxUint64)

	r := a.do("POST", "/v1/promotions", promotionRequestBody("promo-1", maxUint, maxUint, maxUint))
	a.mustStatus(r, 201, "create promotion")
	created := decodePromotion(t, r.Body.Bytes())

	if created.Outcome != "accepted" || created.GateResult.Verdict != "pass" {
		t.Errorf("outcome = %q, verdict = %q, want accepted/pass",
			created.Outcome, created.GateResult.Verdict)
	}
	if created.ProjectID != "proj-1" || created.CandidateID != "cand-2" {
		t.Errorf("derived identity = %s/%s, want proj-1/cand-2",
			created.ProjectID, created.CandidateID)
	}
	if created.SourceEnvironment.Ref != testEnvironment || created.SourceEnvironment.Rank != 30 {
		t.Errorf("source = %+v, want %s rank 30", created.SourceEnvironment, testEnvironment)
	}
	if created.TargetEnvironment.Ref != "production" || created.TargetEnvironment.Rank != 40 {
		t.Errorf("target = %+v, want production rank 40", created.TargetEnvironment)
	}
	if created.GateResult.ReferenceEvidence.Actual == "" {
		t.Error("the response carries no gate result; a reader needs no second request")
	}

	// GET returns exactly what POST did — the record, not a re-derivation.
	g := a.do("GET", "/v1/promotions/promo-1", nil)
	a.mustStatus(g, 200, "get promotion")
	if fetched := decodePromotion(t, g.Body.Bytes()); fetched != created {
		t.Errorf("GET returned a different value than POST:\n got %+v\nwant %+v", fetched, created)
	}
}

// A gate FAIL is a recorded decision, so it is a 201 carrying `rejected`.
func TestPromotionGateFailIsRecordedNotRefused(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-ref", "cand-1", []string{"read"})
	a.completeRun("run-can", "cand-2", []string{"read", "export"})

	source := decodeEnvironment(t, a.do(
		"GET", "/v1/projects/proj-1/environments/"+testEnvironment, nil).Body.Bytes())
	rank := uint16(30)
	a.mustStatus(a.do("POST",
		"/v1/projects/proj-1/environments/"+testEnvironment+"/configure", map[string]any{
			"revision": source.Revision, "rank": rank,
		}), 200, "rank the source")
	a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "production", "name": "Production", "rank": 40,
	}), 201, "create the target")

	r := a.do("POST", "/v1/promotions", promotionRequestBody("promo-1", "0", "0", "0"))
	a.mustStatus(r, 201, "a gate FAIL is still a recorded decision")

	body := decodePromotion(t, r.Body.Bytes())
	if body.Outcome != "rejected" || body.GateResult.Verdict != "fail" {
		t.Errorf("outcome = %q, verdict = %q, want rejected/fail",
			body.Outcome, body.GateResult.Verdict)
	}
	if body.GateResult.AddedBehaviors.Passed {
		t.Error("the added-behavior check passed; the fixture produced no new behavior")
	}
	// And it is durable.
	a.mustStatus(a.do("GET", "/v1/promotions/promo-1", nil), 200, "the rejection is durable")
}

// Every field the server derives is refused, so no caller can express an
// outcome it did not earn.
func TestPromotionRequestRefusesServerDerivedFields(t *testing.T) {
	a := newAPI(t)
	a.seedPromotable()
	maxUint := platform.FormatSequence(math.MaxUint64)

	for _, field := range []string{
		"project_id", "agent_id", "candidate_id", "reference_candidate_id",
		"source_environment", "source_rank", "target_rank",
		"gate_verdict", "gate_result", "outcome", "decided_at",
	} {
		t.Run(field, func(t *testing.T) {
			body := promotionRequestBody("promo-"+field, maxUint, maxUint, maxUint)
			body[field] = "forged"
			r := a.do("POST", "/v1/promotions", body)
			a.mustStatus(r, 400, "derived field "+field)
			if code := errorCode(t, r); code != "invalid_request" {
				t.Errorf("code = %q, want invalid_request", code)
			}
		})
	}
}

func TestPromotionRequestValidation(t *testing.T) {
	a := newAPI(t)
	a.seedPromotable()
	maxUint := platform.FormatSequence(math.MaxUint64)

	tests := []struct {
		name   string
		mutate func(map[string]any)
		status int
		code   string
	}{
		{"malformed id", func(b map[string]any) { b["id"] = "promo\n1" }, 400, "invalid_request"},
		{"missing added limit", func(b map[string]any) {
			b["gate_limits"] = map[string]any{
				"max_block_decisions": maxUint, "max_critical_risk_observations": maxUint}
		}, 400, "invalid_request"},
		{"non-canonical limit", func(b map[string]any) {
			b["gate_limits"] = limitsBody("007", maxUint, maxUint)
		}, 400, "invalid_request"},
		{"same run on both sides", func(b map[string]any) {
			b["reference_run_id"] = b["candidate_run_id"]
		}, 400, "invalid_request"},
		{"missing run", func(b map[string]any) {
			b["reference_run_id"] = "run-absent"
		}, 404, "not_found"},
		{"missing target environment", func(b map[string]any) {
			b["target_environment"] = "nowhere"
		}, 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := promotionRequestBody("promo-"+strings.ReplaceAll(tt.name, " ", "-"),
				maxUint, maxUint, maxUint)
			tt.mutate(body)
			r := a.do("POST", "/v1/promotions", body)
			a.mustStatus(r, tt.status, tt.name)
			if code := errorCode(t, r); code != tt.code {
				t.Errorf("code = %q, want %q", code, tt.code)
			}
		})
	}
}

// A backward target is a conflict, because the configuration may change.
func TestPromotionOrderIsAConflict(t *testing.T) {
	a := newAPI(t)
	a.seedPromotable()
	maxUint := platform.FormatSequence(math.MaxUint64)

	// Demote the target below the source.
	target := decodeEnvironment(t, a.do(
		"GET", "/v1/projects/proj-1/environments/production", nil).Body.Bytes())
	rank := uint16(10)
	a.mustStatus(a.do("POST",
		"/v1/projects/proj-1/environments/production/configure", map[string]any{
			"revision": target.Revision, "rank": rank,
		}), 200, "demote the target")

	r := a.do("POST", "/v1/promotions", promotionRequestBody("promo-1", maxUint, maxUint, maxUint))
	a.mustStatus(r, 409, "backward promotion")
	if code := errorCode(t, r); code != "conflict" {
		t.Errorf("code = %q, want conflict", code)
	}
}

// A retried POST whose first attempt committed is a conflict, and the client
// reads back what was recorded.
func TestPromotionDuplicateIsAConflict(t *testing.T) {
	a := newAPI(t)
	a.seedPromotable()
	maxUint := platform.FormatSequence(math.MaxUint64)

	a.mustStatus(a.do("POST", "/v1/promotions",
		promotionRequestBody("promo-1", maxUint, maxUint, maxUint)), 201, "first")

	r := a.do("POST", "/v1/promotions", promotionRequestBody("promo-1", "0", "0", "0"))
	a.mustStatus(r, 409, "duplicate")
	if code := errorCode(t, r); code != "already_exists" {
		t.Errorf("code = %q, want already_exists", code)
	}

	// The first decision stands: the retry did not overwrite it.
	stored := decodePromotion(t, a.do("GET", "/v1/promotions/promo-1", nil).Body.Bytes())
	if stored.Outcome != "accepted" {
		t.Errorf("outcome = %q, want the first decision's accepted", stored.Outcome)
	}
}

func TestPromotionCollection(t *testing.T) {
	a := newAPI(t)
	a.seedPromotable()
	maxUint := platform.FormatSequence(math.MaxUint64)

	const total = 5
	for i := range total {
		a.mustStatus(a.do("POST", "/v1/promotions",
			promotionRequestBody(fmt.Sprintf("promo-%03d", i), maxUint, maxUint, maxUint)),
			201, "seed promotion")
	}

	// Default limit, full history, id-ascending.
	r := a.do("GET", "/v1/projects/proj-1/promotions", nil)
	a.mustStatus(r, 200, "list")
	var body promotionListBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding list: %v", err)
	}
	if len(body.Promotions) != total || body.NextAfter != "" {
		t.Fatalf("page = %d rows, next_after %q, want %d and no cursor",
			len(body.Promotions), body.NextAfter, total)
	}
	for i := 1; i < len(body.Promotions); i++ {
		if body.Promotions[i-1].ID >= body.Promotions[i].ID {
			t.Fatalf("not id-ascending: %q then %q",
				body.Promotions[i-1].ID, body.Promotions[i].ID)
		}
	}

	// A full page with more after it publishes a cursor; the last page does
	// not — and that cursor is the page's own last id.
	r = a.do("GET", "/v1/projects/proj-1/promotions?limit=2", nil)
	a.mustStatus(r, 200, "first page")
	var first promotionListBody
	if err := json.Unmarshal(r.Body.Bytes(), &first); err != nil {
		t.Fatalf("decoding page: %v", err)
	}
	if len(first.Promotions) != 2 || first.NextAfter != first.Promotions[1].ID {
		t.Fatalf("page = %d rows, next_after %q, want 2 and the page's last id",
			len(first.Promotions), first.NextAfter)
	}

	// Bad parameters are refused rather than clamped.
	for _, query := range []string{"?limit=65", "?limit=0", "?limit=-1", "?limit=abc",
		"?after=" + strings.Repeat("a", 257)} {
		r := a.do("GET", "/v1/projects/proj-1/promotions"+query, nil)
		a.mustStatus(r, 400, "list"+query)
	}

	// A project with no history is an empty array; a missing one is a 404.
	a.mustStatus(a.do("POST", "/v1/projects", map[string]string{
		"id": "proj-empty", "name": "Empty"}), 201, "create project")
	r = a.do("GET", "/v1/projects/proj-empty/promotions", nil)
	a.mustStatus(r, 200, "empty project")
	var empty promotionListBody
	if err := json.Unmarshal(r.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decoding empty list: %v", err)
	}
	if len(empty.Promotions) != 0 || empty.NextAfter != "" {
		t.Errorf("empty project = %+v, want no rows and no cursor", empty)
	}
	a.mustStatus(a.do("GET", "/v1/projects/absent/promotions", nil), 404, "missing project")
}

// A malformed promotion identifier in the path is rejected at the service
// boundary, so every transport benefits rather than only this one.
func TestPromotionPathIdentifierIsValidated(t *testing.T) {
	a := newAPI(t)
	r := a.do("GET", "/v1/promotions/"+strings.Repeat("p", 257), nil)
	a.mustStatus(r, 400, "over-length identifier")
	if code := errorCode(t, r); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}
