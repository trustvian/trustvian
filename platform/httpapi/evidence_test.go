package httpapi_test

// The /v1 evidence-resolution routes, task 085.
//
// The route's own contract is what is asserted here — the finding travels in
// the query string so the URL is the citable link, the status and availability
// fields survive to the wire, and a malformed finding is a 400 rather than a
// 500. What the resolution *means* is asserted in the platform package.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

type evidenceBehaviorsBody struct {
	Version string `json:"version"`
	Finding struct {
		ReferenceRunID string `json:"reference_run_id"`
		CandidateRunID string `json:"candidate_run_id"`
		Check          string `json:"check"`
		Behavior       string `json:"behavior"`
	} `json:"finding"`
	Status        string `json:"status"`
	Side          string `json:"side"`
	RecordedCount string `json:"recorded_count"`
	Behaviors     []struct {
		FingerprintID string `json:"fingerprint_id"`
		Presence      string `json:"presence"`
	} `json:"behaviors"`
	History struct {
		State    string `json:"state"`
		Complete bool   `json:"complete"`
	} `json:"history"`
	NextAfter string `json:"next_after"`
}

type evidenceObservationsBody struct {
	Version       string `json:"version"`
	Status        string `json:"status"`
	Side          string `json:"side"`
	RecordedCount string `json:"recorded_count"`
	Exhaustive    bool   `json:"exhaustive"`
	Observations  []struct {
		Sequence      string `json:"sequence"`
		EventID       string `json:"event_id"`
		FingerprintID string `json:"fingerprint_id"`
		Decision      string `json:"decision"`
		RiskLevel     string `json:"risk_level"`
	} `json:"observations"`
	History struct {
		State    string `json:"state"`
		Complete bool   `json:"complete"`
	} `json:"history"`
	NextAfter string `json:"next_after"`
}

func decodeInto(t *testing.T, r *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(r.Body.Bytes(), into); err != nil {
		t.Fatalf("decode: %v (%s)", err, r.Body.String())
	}
}

// evidenceAPI seeds a completed comparison over the HTTP surface.
func evidenceAPI(t *testing.T) *api {
	t.Helper()
	a := newAPI(t)
	a.seedHierarchy()

	for _, runID := range []string{"run-ref", "run-cand"} {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
			"id": runID, "candidate_id": "cand-1",
			"environment": testEnvironment, "behavioral_profile": testProfile,
		}), 201, "create "+runID)
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")
	}

	ingest := func(runID string, sequence uint64, record trustvian.DecisionRecord) {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records",
			envelope(sequence, record)), 200, "ingest "+record.EventID)
	}

	ingest("run-ref", 1, evidenceAPIRecord("r1", "fp-shared", "list_customers", "allow", "low"))

	ingest("run-cand", 1, evidenceAPIRecord("c1", "fp-shared", "list_customers", "allow", "low"))
	ingest("run-cand", 2, evidenceAPIRecord("c2", "fp-export", "export_customer", "block", "critical"))
	ingest("run-cand", 3, evidenceAPIRecord("c3", "fp-delete", "delete_customer", "block", "low"))

	for _, runID := range []string{"run-ref", "run-cand"} {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
	}
	return a
}

func evidenceAPIRecord(eventID, fingerprintID, operation, decision, risk string) trustvian.DecisionRecord {
	record := apiRecord(eventID, fingerprintID, operation)
	record.Decision = decision
	record.RiskLevel = risk
	record.TraceID = "trace-" + eventID
	record.SpanID = "span-" + eventID
	record.DurationNanos = "1500"
	record.SpanStatus = event.StatusOK
	return record
}

const evidenceFinding = "reference_run_id=run-ref&candidate_run_id=run-cand"

// added_behaviors resolves to its contributing identities over /v1.
func TestEvidenceBehaviorsRoute(t *testing.T) {
	a := evidenceAPI(t)

	response := a.do("GET",
		"/v1/evidence/behaviors?"+evidenceFinding+"&check=added_behaviors", nil)
	a.mustStatus(response, 200, "behaviors")

	var body evidenceBehaviorsBody
	decodeInto(t, response, &body)

	if body.Status != "resolved" {
		t.Errorf("status = %q, want resolved", body.Status)
	}
	if body.Side != "candidate" {
		t.Errorf("side = %q, want candidate", body.Side)
	}
	if body.RecordedCount != "2" {
		t.Errorf("recorded_count = %q, want 2", body.RecordedCount)
	}
	if len(body.Behaviors) != 2 {
		t.Fatalf("returned %d behaviors, want 2", len(body.Behaviors))
	}
	for _, b := range body.Behaviors {
		if b.Presence != "added" {
			t.Errorf("%s presence = %q, want added", b.FingerprintID, b.Presence)
		}
	}
	// The finding is echoed, so a stored response reconstructs its own link.
	if body.Finding.ReferenceRunID != "run-ref" || body.Finding.Check != "added_behaviors" {
		t.Errorf("finding echo = %+v", body.Finding)
	}
	if !body.History.Complete {
		t.Error("history.complete = false on a fully retained run")
	}
}

// A counting check resolves to the observations that carried it.
func TestEvidenceObservationsRoute(t *testing.T) {
	a := evidenceAPI(t)

	response := a.do("GET",
		"/v1/evidence/observations?"+evidenceFinding+"&check=block_decisions", nil)
	a.mustStatus(response, 200, "observations")

	var body evidenceObservationsBody
	decodeInto(t, response, &body)

	if body.Status != "resolved" {
		t.Errorf("status = %q, want resolved", body.Status)
	}
	if body.RecordedCount != "2" {
		t.Errorf("recorded_count = %q, want 2", body.RecordedCount)
	}
	if !body.Exhaustive {
		t.Error("exhaustive = false on complete history")
	}
	if len(body.Observations) != 2 {
		t.Fatalf("returned %d observations, want 2", len(body.Observations))
	}
	for _, o := range body.Observations {
		if o.Decision != "block" {
			t.Errorf("observation %s has decision %q", o.EventID, o.Decision)
		}
	}
}

// One behavioral identity resolves to its own observations, on the side its
// presence names.
func TestEvidenceObservationsByBehavior(t *testing.T) {
	a := evidenceAPI(t)

	response := a.do("GET",
		"/v1/evidence/observations?"+evidenceFinding+"&behavior=fp-export", nil)
	a.mustStatus(response, 200, "observations by behavior")

	var body evidenceObservationsBody
	decodeInto(t, response, &body)

	if body.Side != "candidate" {
		t.Errorf("side = %q, want candidate", body.Side)
	}
	if len(body.Observations) != 1 {
		t.Fatalf("returned %d observations, want 1", len(body.Observations))
	}
	if body.Observations[0].FingerprintID != "fp-export" {
		t.Errorf("observation carries fingerprint %q", body.Observations[0].FingerprintID)
	}
}

// The evidence checks report aggregate_only rather than inventing a set.
func TestEvidenceAggregateOnlyChecks(t *testing.T) {
	a := evidenceAPI(t)

	for _, check := range []string{"reference_evidence", "candidate_evidence"} {
		t.Run(check, func(t *testing.T) {
			response := a.do("GET",
				"/v1/evidence/observations?"+evidenceFinding+"&check="+check, nil)
			a.mustStatus(response, 200, check)

			var body evidenceObservationsBody
			decodeInto(t, response, &body)
			if body.Status != "aggregate_only" {
				t.Errorf("status = %q, want aggregate_only", body.Status)
			}
			if len(body.Observations) != 0 {
				t.Errorf("returned %d observations for an aggregate-only check",
					len(body.Observations))
			}
		})
	}
}

// Malformed findings are 400 with the platform's error envelope, never 500.
func TestEvidenceRouteRefusesMalformedFindings(t *testing.T) {
	a := evidenceAPI(t)

	for _, query := range []string{
		evidenceFinding,
		evidenceFinding + "&check=added_behaviors&behavior=fp-export",
		evidenceFinding + "&check=invented_check",
		evidenceFinding + "&behavior=fp-never-observed",
		evidenceFinding + "&check=block_decisions&side=candidate",
		evidenceFinding + "&behavior=fp-shared&side=sideways",
		evidenceFinding + "&check=added_behaviors",
		"reference_run_id=run-ref&candidate_run_id=run-cand&check=block_decisions&limit=0",
		"reference_run_id=run-ref&candidate_run_id=run-cand&check=block_decisions&after=01",
	} {
		t.Run(query, func(t *testing.T) {
			response := a.do("GET", "/v1/evidence/observations?"+query, nil)
			if response.Code != 400 {
				t.Fatalf("status = %d, want 400; body = %s",
					response.Code, response.Body.String())
			}
			if code := errorCode(t, response); code == "" {
				t.Error("error envelope carries no code")
			}
		})
	}

	// A run that does not exist stays a 404: a typo in a run identifier and a
	// malformed finding are different failures.
	missing := a.do("GET",
		"/v1/evidence/observations?reference_run_id=run-ref&candidate_run_id=no-such&check=block_decisions", nil)
	a.mustStatus(missing, 404, "unknown run")
}

// Paging works over the route, with next_after present exactly when another row
// follows.
func TestEvidenceRoutePaging(t *testing.T) {
	a := evidenceAPI(t)

	first := a.do("GET",
		"/v1/evidence/observations?"+evidenceFinding+"&check=block_decisions&limit=1", nil)
	a.mustStatus(first, 200, "first page")

	var page evidenceObservationsBody
	decodeInto(t, first, &page)
	if len(page.Observations) != 1 {
		t.Fatalf("first page holds %d observations, want 1", len(page.Observations))
	}
	if page.NextAfter == "" {
		t.Fatal("next_after is absent although another block observation follows")
	}

	second := a.do("GET", fmt.Sprintf(
		"/v1/evidence/observations?%s&check=block_decisions&limit=1&after=%s",
		evidenceFinding, page.NextAfter), nil)
	a.mustStatus(second, 200, "second page")

	var next evidenceObservationsBody
	decodeInto(t, second, &next)
	if len(next.Observations) != 1 {
		t.Fatalf("second page holds %d observations, want 1", len(next.Observations))
	}
	if next.Observations[0].Sequence == page.Observations[0].Sequence {
		t.Error("the second page repeated the first page's observation")
	}
	if next.NextAfter != "" {
		t.Errorf("next_after = %q on the last page", next.NextAfter)
	}
}

// Paging past the last match must not report that the finding has no evidence.
//
// The regression this guards: the status used to be derived from the page
// length, so the request a developer makes after reading all the evidence said
// there was none.
func TestEvidenceRouteExhaustedPageStillReportsResolved(t *testing.T) {
	a := evidenceAPI(t)

	first := a.do("GET",
		"/v1/evidence/observations?"+evidenceFinding+"&check=block_decisions", nil)
	a.mustStatus(first, 200, "all matches")

	var all evidenceObservationsBody
	decodeInto(t, first, &all)
	if len(all.Observations) != 2 {
		t.Fatalf("returned %d observations, want 2", len(all.Observations))
	}
	last := all.Observations[len(all.Observations)-1].Sequence

	beyond := a.do("GET", fmt.Sprintf(
		"/v1/evidence/observations?%s&check=block_decisions&after=%s",
		evidenceFinding, last), nil)
	a.mustStatus(beyond, 200, "beyond the last match")

	var page evidenceObservationsBody
	decodeInto(t, beyond, &page)
	if len(page.Observations) != 0 {
		t.Fatalf("page past the last match holds %d rows", len(page.Observations))
	}
	if page.Status != "resolved" {
		t.Errorf("status = %q on an exhausted page, want resolved: the finding's "+
			"evidence did not stop existing because the cursor moved past it",
			page.Status)
	}
	if page.NextAfter != "" {
		t.Errorf("next_after = %q on an exhausted page", page.NextAfter)
	}

	// The same for behaviors.
	behaviors := a.do("GET",
		"/v1/evidence/behaviors?"+evidenceFinding+"&check=added_behaviors&after=fp-export", nil)
	a.mustStatus(behaviors, 200, "beyond the last behavior")

	var behaviorPage evidenceBehaviorsBody
	decodeInto(t, behaviors, &behaviorPage)
	if len(behaviorPage.Behaviors) != 0 {
		t.Fatalf("page past the last behavior holds %d entries",
			len(behaviorPage.Behaviors))
	}
	if behaviorPage.Status != "resolved" {
		t.Errorf("behaviors status = %q on an exhausted page, want resolved",
			behaviorPage.Status)
	}
}

// A shared behavior with no side is refused by both routes, with the platform's
// own error envelope.
func TestEvidenceRouteRequiresASideForSharedBehaviors(t *testing.T) {
	a := evidenceAPI(t)

	for _, route := range []string{"observations", "behaviors"} {
		t.Run(route, func(t *testing.T) {
			response := a.do("GET",
				"/v1/evidence/"+route+"?"+evidenceFinding+"&behavior=fp-shared", nil)
			if response.Code != 400 {
				t.Fatalf("status = %d, want 400; body = %s",
					response.Code, response.Body.String())
			}
			if code := errorCode(t, response); code != "invalid_request" {
				t.Errorf("error code = %q, want invalid_request", code)
			}
			if !strings.Contains(response.Body.String(), "reference or candidate") {
				t.Errorf("the diagnostic does not say what to supply: %s",
					response.Body.String())
			}
		})
	}

	// With a side, each returns its own run's evidence.
	for _, tc := range []struct {
		side      string
		wantCount int
		wantEvent string
	}{
		{"reference", 1, "r1"},
		{"candidate", 1, "c1"},
	} {
		t.Run("side="+tc.side, func(t *testing.T) {
			response := a.do("GET",
				"/v1/evidence/observations?"+evidenceFinding+
					"&behavior=fp-shared&side="+tc.side, nil)
			a.mustStatus(response, 200, tc.side)

			var body evidenceObservationsBody
			decodeInto(t, response, &body)
			if body.Side != tc.side {
				t.Errorf("side = %q, want %q", body.Side, tc.side)
			}
			if len(body.Observations) != tc.wantCount {
				t.Fatalf("returned %d observations, want %d",
					len(body.Observations), tc.wantCount)
			}
			if got := body.Observations[0].EventID; got != tc.wantEvent {
				t.Errorf("observation = %q, want %q", got, tc.wantEvent)
			}
		})
	}

	// And the behaviors route accepts the side too.
	behaviors := a.do("GET",
		"/v1/evidence/behaviors?"+evidenceFinding+"&behavior=fp-shared&side=reference", nil)
	a.mustStatus(behaviors, 200, "behaviors with a side")

	var body evidenceBehaviorsBody
	decodeInto(t, behaviors, &body)
	if body.Side != "reference" {
		t.Errorf("behaviors side = %q, want reference", body.Side)
	}
}
