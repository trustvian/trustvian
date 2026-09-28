package httpapi_test

// GET /v1/evaluation-runs/{run_id}/behaviors — task 078's first slice.
//
// The route publishes what a self-compare of the same run already returns,
// minus the scorecard and the gate. That equivalence is the point of the slice
// and is the first test here: it is what makes the route a *replacement* for
// the self-compare workaround rather than a second answer to the same question,
// and it is the test that fails if the two ever disagree.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

type runBehaviorsBody struct {
	Version   string `json:"version"`
	RunID     string `json:"run_id"`
	Complete  bool   `json:"complete"`
	Behaviors []struct {
		FingerprintID string `json:"fingerprint_id"`
		Behavior      struct {
			ActorType         string `json:"actor_type"`
			OperationCategory string `json:"operation_category"`
			OperationName     string `json:"operation_name"`
			TargetName        string `json:"target_name"`
		} `json:"behavior"`
		Observations string `json:"observations"`
	} `json:"behaviors"`
	NextAfter string `json:"next_after"`
}

func decodeRunBehaviors(t *testing.T, r *httptest.ResponseRecorder) runBehaviorsBody {
	t.Helper()
	var body runBehaviorsBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode behaviors: %v\n%s", err, r.Body.String())
	}
	return body
}

// TestRunBehaviorsMatchesASelfCompare is the equivalence the slice exists for.
//
// Identity for identity and count for count against POST /v1/evaluations/compare
// of the run with itself — the workaround this route replaces. A self-compare
// classifies every behavior as shared and reports the same observation count on
// both sides, so the reference count is the one to check against.
func TestRunBehaviorsMatchesASelfCompare(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-1", "cand-1", []string{"read", "write", "deploy"})

	compare := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-1",
		"candidate_run_id": "run-1",
		"gate_limits":      limitsBody("0", "0", "0"),
	})
	a.mustStatus(compare, 200, "self-compare")

	var comparison struct {
		Diff struct {
			Deltas []struct {
				FingerprintID         string `json:"fingerprint_id"`
				ReferenceObservations string `json:"reference_observations"`
			} `json:"deltas"`
		} `json:"behavior_diff"`
	}
	if err := json.Unmarshal(compare.Body.Bytes(), &comparison); err != nil {
		t.Fatalf("decode comparison: %v", err)
	}
	fromCompare := map[string]string{}
	for _, d := range comparison.Diff.Deltas {
		fromCompare[d.FingerprintID] = d.ReferenceObservations
	}
	if len(fromCompare) == 0 {
		t.Fatal("the self-compare returned no deltas, so this test would prove nothing")
	}

	response := a.do("GET", "/v1/evaluation-runs/run-1/behaviors", nil)
	a.mustStatus(response, 200, "behaviors")
	body := decodeRunBehaviors(t, response)

	fromRoute := map[string]string{}
	for _, b := range body.Behaviors {
		fromRoute[b.FingerprintID] = b.Observations
	}

	if len(fromRoute) != len(fromCompare) {
		t.Fatalf("behaviors returned %d identities, the self-compare %d:\n route:   %v\n compare: %v",
			len(fromRoute), len(fromCompare), fromRoute, fromCompare)
	}
	for id, want := range fromCompare {
		got, ok := fromRoute[id]
		if !ok {
			t.Errorf("behaviors omits %s, which the self-compare reports", id)
			continue
		}
		if got != want {
			t.Errorf("%s observations = %s, self-compare says %s", id, got, want)
		}
	}
}

// TestRunBehaviorsOrdersByFingerprintAscending pins the order the cursor
// depends on. An unsorted page would make `after` skip and repeat rows.
func TestRunBehaviorsOrdersByFingerprintAscending(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-1", "cand-1", []string{"deploy", "read", "write"})

	body := decodeRunBehaviors(t, a.do("GET", "/v1/evaluation-runs/run-1/behaviors", nil))
	if len(body.Behaviors) < 2 {
		t.Fatalf("want several behaviors to order, got %d", len(body.Behaviors))
	}
	for i := 1; i < len(body.Behaviors); i++ {
		if body.Behaviors[i-1].FingerprintID >= body.Behaviors[i].FingerprintID {
			t.Fatalf("not byte-ascending at %d: %q then %q",
				i, body.Behaviors[i-1].FingerprintID, body.Behaviors[i].FingerprintID)
		}
	}
}

// TestRunBehaviorsPagingFollowsTheCollectionRules is task 065's contract,
// reused rather than redesigned: `after` exclusive, `next_after` present
// exactly when another row follows, and a full page that ends the collection
// carrying no cursor.
func TestRunBehaviorsPagingFollowsTheCollectionRules(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-1", "cand-1", []string{"alpha", "beta", "gamma", "delta"})

	all := decodeRunBehaviors(t, a.do("GET", "/v1/evaluation-runs/run-1/behaviors", nil))
	if len(all.Behaviors) != 4 {
		t.Fatalf("want 4 behaviors, got %d", len(all.Behaviors))
	}
	if all.NextAfter != "" {
		t.Errorf("next_after = %q on a complete collection; a short page ends it",
			all.NextAfter)
	}

	first := decodeRunBehaviors(t,
		a.do("GET", "/v1/evaluation-runs/run-1/behaviors?limit=2", nil))
	if len(first.Behaviors) != 2 {
		t.Fatalf("limit=2 returned %d rows", len(first.Behaviors))
	}
	want := all.Behaviors[1].FingerprintID
	if first.NextAfter != want {
		t.Fatalf("next_after = %q, want the page's last id %q", first.NextAfter, want)
	}

	second := decodeRunBehaviors(t, a.do("GET",
		"/v1/evaluation-runs/run-1/behaviors?limit=2&after="+first.NextAfter, nil))
	if len(second.Behaviors) != 2 {
		t.Fatalf("second page returned %d rows", len(second.Behaviors))
	}
	// Exclusive: the cursor row must not appear again.
	if second.Behaviors[0].FingerprintID == first.NextAfter {
		t.Errorf("after is inclusive — %q appeared on both pages", first.NextAfter)
	}
	if second.NextAfter != "" {
		t.Errorf("next_after = %q after the last row", second.NextAfter)
	}

	// A full page that exactly ends the collection carries no cursor. This is
	// the case a limit+1 lookahead gets wrong, and the reason the handler asks
	// a second bounded question instead.
	exact := decodeRunBehaviors(t,
		a.do("GET", "/v1/evaluation-runs/run-1/behaviors?limit=4", nil))
	if exact.NextAfter != "" {
		t.Errorf("next_after = %q on a full page that ends the collection", exact.NextAfter)
	}
}

func TestRunBehaviorsRejectsLimitsOutsideTheBound(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-1", "cand-1", []string{"read"})

	for _, limit := range []string{"0", "-1", "65", "1000"} {
		t.Run("limit="+limit, func(t *testing.T) {
			r := a.do("GET", "/v1/evaluation-runs/run-1/behaviors?limit="+limit, nil)
			if r.Code != 400 {
				t.Fatalf("limit=%s status = %d, want 400", limit, r.Code)
			}
		})
	}
	for _, limit := range []string{"1", "64"} {
		t.Run("limit="+limit, func(t *testing.T) {
			a.mustStatus(a.do("GET", "/v1/evaluation-runs/run-1/behaviors?limit="+limit, nil),
				200, "limit "+limit)
		})
	}
}

// TestRunBehaviorsDistinguishesMissingFromEmpty is both halves on purpose.
//
// Collapsing the second into the first would make "nothing happened"
// indistinguishable from "no such run" — the distinction ADR 0029 § 2 insists
// on one layer down, and the reason an empty evaluation fails a gate rather
// than erroring.
func TestRunBehaviorsDistinguishesMissingFromEmpty(t *testing.T) {
	a := newAPI(t)

	t.Run("missing run is 404", func(t *testing.T) {
		r := a.do("GET", "/v1/evaluation-runs/no-such-run/behaviors", nil)
		if r.Code != 404 {
			t.Fatalf("status = %d, want 404\n%s", r.Code, r.Body.String())
		}
	})

	t.Run("run with no evidence is an empty 200", func(t *testing.T) {
		a.seedRunning("run-empty")
		r := a.do("GET", "/v1/evaluation-runs/run-empty/behaviors", nil)
		a.mustStatus(r, 200, "empty run")
		body := decodeRunBehaviors(t, r)
		if len(body.Behaviors) != 0 {
			t.Errorf("a run with no evidence returned %d behaviors", len(body.Behaviors))
		}
		if !body.Complete {
			t.Error("an empty run's evidence is complete — nothing was truncated")
		}
	})
}

// TestRunBehaviorsServesARunningRun records a deliberate difference from
// CompareEvaluations, which requires completion.
//
// A comparison of mutable evidence describes a moment that has already passed.
// This route makes no comparison and draws no conclusion, and tasks 079 and 080
// both want a run's behavior set while it is still accumulating.
func TestRunBehaviorsServesARunningRun(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-live")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-live/records",
		envelope(1, apiRecord("evt-1", "fp-read", "read"))), 200, "ingest")

	body := decodeRunBehaviors(t,
		a.do("GET", "/v1/evaluation-runs/run-live/behaviors", nil))
	if len(body.Behaviors) != 1 {
		t.Fatalf("a running run returned %d behaviors, want 1", len(body.Behaviors))
	}
}

// TestRunBehaviorsPublishesNoContent sweeps the response for the content the
// whole chain promises never to carry.
//
// Planted rather than assumed: the canary travels in TargetName, which is a
// StableFeatures dimension and provably reaches the wire, so a probe that found
// nothing would be testing the probe rather than the route.
func TestRunBehaviorsPublishesNoContent(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-1", "cand-1", []string{"read"})

	body := a.do("GET", "/v1/evaluation-runs/run-1/behaviors", nil).Body.String()

	for _, forbidden := range []string{
		"prompt", "completion", "argument", "tool_result", "request_body",
		"response_body", "gen_ai.prompt", "gen_ai.completion", "input.value",
		"output.value",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the response mentions %q:\n%s", forbidden, body)
		}
	}
}

// TestRunBehaviorsCarriesNoFidelityYet is a scheduled failure, not a permanent
// assertion — the same shape as TestFidelityIsNotPersistedYet.
//
// Task 075 shipped the fidelity indicator but did not persist it per behavior;
// that is task 081. Until then this route must not imply it, because inferring
// "this descriptor names a tool because it looks like one" is exactly the guess
// the indicator exists to remove. When 081 lands, replace this with the
// positive assertion that a behavior reports its fidelity.
func TestRunBehaviorsCarriesNoFidelityYet(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-1", "cand-1", []string{"read"})

	body := a.do("GET", "/v1/evaluation-runs/run-1/behaviors", nil).Body.String()
	if strings.Contains(body, `"fidelity"`) {
		t.Error("the behaviors route now carries fidelity — per-behavior fidelity " +
			"has landed, so replace this test with the positive assertion that a " +
			"behavior reports it")
	}
}

// TestRunBehaviorsReportsObservationCounts proves the count is per behavior and
// not a per-run total, by observing one behavior twice and another once.
func TestRunBehaviorsReportsObservationCounts(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	for i, op := range []string{"read", "read", "write"} {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
			envelope(uint64(i+1), apiRecord(fmt.Sprintf("evt-%d", i), "fp-"+op, op))),
			200, "ingest")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/complete", nil), 200, "complete")

	body := decodeRunBehaviors(t, a.do("GET", "/v1/evaluation-runs/run-1/behaviors", nil))
	counts := map[string]string{}
	for _, b := range body.Behaviors {
		counts[b.FingerprintID] = b.Observations
	}
	if counts["fp-read"] != "2" {
		t.Errorf("fp-read observations = %q, want \"2\"", counts["fp-read"])
	}
	if counts["fp-write"] != "1" {
		t.Errorf("fp-write observations = %q, want \"1\"", counts["fp-write"])
	}
}
