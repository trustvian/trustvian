package httpapi_test

// POST /v1/evaluations/compare-repeated over /v1 (task 078).

import (
	"encoding/json"
	"fmt"
	"testing"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

// completeIsolatedRun drives one run under its own behavioral profile.
func (a *api) completeIsolatedRun(runID string, operations []string) {
	a.t.Helper()
	a.completeIsolatedRunIn(testEnvironment, runID, operations)
}

// completeIsolatedRunIn is completeIsolatedRun in a named environment. Outside
// the test environment a behavior's fingerprint is environment-specific, as
// the engine's is: environment is a fingerprint dimension.
func (a *api) completeIsolatedRunIn(environment, runID string, operations []string) {
	a.t.Helper()
	a.seedHierarchy()
	profile := runID + "-profile"
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "cand-1",
		"environment": environment, "behavioral_profile": profile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")
	for i, op := range operations {
		fingerprint := "fp-" + op
		record := apiRecord(fmt.Sprintf("%s-e%d", runID, i), fingerprint, op)
		if environment != testEnvironment {
			record.FingerprintID = fingerprint + "@" + environment
			record.Environment = environment
			record.Behavior.Environment = environment
		}
		body := envelope(uint64(i+1), record)
		body["behavioral_profile"] = profile
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records", body), 200, "ingest")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
}

func repeatedLimitsBody(k, j string) map[string]any {
	return map[string]any{
		"added_candidate_presence_minimum":       k,
		"added_reference_presence_maximum":       j,
		"max_repeated_added_behaviors":           "0",
		"max_block_decisions_per_run":            "0",
		"max_critical_risk_observations_per_run": "0",
	}
}

type repeatedBody struct {
	Version     string            `json:"version"`
	Runs        int               `json:"runs"`
	GateLimits  map[string]string `json:"gate_limits"`
	Repetitions []struct {
		Side              string `json:"side"`
		Index             int    `json:"index"`
		RunID             string `json:"run_id"`
		Status            string `json:"status"`
		BehavioralProfile string `json:"behavioral_profile"`
		RecordCount       string `json:"record_count"`
		BlockDecisions    string `json:"block_decisions"`
	} `json:"repetitions"`
	Behaviors []struct {
		FingerprintID        string `json:"fingerprint_id"`
		ReferenceRunsPresent string `json:"reference_runs_present"`
		CandidateRunsPresent string `json:"candidate_runs_present"`
		Classification       string `json:"classification"`
	} `json:"behaviors"`
	Gate struct {
		Checks []struct {
			Name     string  `json:"name"`
			Actual   string  `json:"actual"`
			Rule     string  `json:"rule"`
			Bound    string  `json:"bound"`
			Passed   bool    `json:"passed"`
			Advisory *string `json:"advisory"`
		} `json:"checks"`
		Verdict string `json:"verdict"`
	} `json:"gate"`
	Producer struct {
		ControlPlaneVersion string `json:"control_plane_version"`
	} `json:"producer"`
}

func TestCompareRepeatedOverHTTP(t *testing.T) {
	a := newAPI(t)
	for i := 1; i <= 3; i++ {
		a.completeIsolatedRun(fmt.Sprintf("ref-%d", i), []string{"read"})
	}
	a.completeIsolatedRun("can-1", []string{"read", "export"})
	a.completeIsolatedRun("can-2", []string{"read", "export"})
	a.completeIsolatedRun("can-3", []string{"read"})

	request := func(k string) []byte {
		r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
			"reference_run_ids": []string{"ref-1", "ref-2", "ref-3"},
			"candidate_run_ids": []string{"can-1", "can-2", "can-3"},
			"gate_limits":       repeatedLimitsBody(k, "0"),
		})
		a.mustStatus(r, 200, "compare-repeated")
		return r.Body.Bytes()
	}

	var body repeatedBody
	if err := json.Unmarshal(request("2"), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != httpapi.WireVersion || body.Runs != 3 || body.GateLimits["added_candidate_presence_minimum"] != "2" {
		t.Errorf("version/runs/k = %q/%d/%q", body.Version, body.Runs, body.GateLimits["added_candidate_presence_minimum"])
	}
	if len(body.Repetitions) != 6 || body.Repetitions[0].Side != "reference" || body.Repetitions[5].Side != "candidate" ||
		body.Repetitions[5].Index != 3 || body.Repetitions[0].BehavioralProfile != "ref-1-profile" {
		t.Errorf("repetitions = %+v", body.Repetitions)
	}
	presence := map[string][3]string{}
	for _, b := range body.Behaviors {
		presence[b.FingerprintID] = [3]string{b.ReferenceRunsPresent, b.CandidateRunsPresent, b.Classification}
	}
	if presence["fp-read"] != [3]string{"3", "3", "neither"} || presence["fp-export"] != [3]string{"0", "2", "added"} {
		t.Errorf("presence = %v", presence)
	}
	wantNames := []string{
		string(platform.CheckReferenceRepetitionsCompleted), string(platform.CheckCandidateRepetitionsCompleted),
		string(platform.CheckRepetitionsFailingMinimum), string(platform.CheckRepeatedlyAddedBehaviors),
		string(platform.CheckWorstCandidateBlockDecisions), string(platform.CheckWorstCandidateCriticalRiskCount),
	}
	if len(body.Gate.Checks) != 6 {
		t.Fatalf("%d checks, want 6", len(body.Gate.Checks))
	}
	for i, c := range body.Gate.Checks {
		if c.Name != wantNames[i] {
			t.Errorf("check %d = %s, want %s", i, c.Name, wantNames[i])
		}
		wantAdvisory := i >= 4
		if (c.Advisory != nil) != wantAdvisory || (wantAdvisory && *c.Advisory != "fresh_scope") {
			t.Errorf("check %s advisory = %v, want present=%v", c.Name, c.Advisory, wantAdvisory)
		}
	}
	if added := body.Gate.Checks[3]; added.Actual != "1" || added.Passed || body.Gate.Verdict != "fail" {
		t.Errorf("k = 2: added check %+v verdict %s; want 1 added, fail", added, body.Gate.Verdict)
	}
	if body.Producer.ControlPlaneVersion == "" {
		t.Error("producer version is empty")
	}

	var atThree repeatedBody
	if err := json.Unmarshal(request("3"), &atThree); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if atThree.Gate.Verdict != "pass" {
		t.Errorf("k = 3 (export in 2 of 3): verdict %s, want pass", atThree.Gate.Verdict)
	}
}

func TestCompareRepeatedRefusesBadRequests(t *testing.T) {
	a := newAPI(t)
	a.completeIsolatedRun("ref-1", []string{"read"})
	a.completeIsolatedRun("can-1", []string{"read"})
	ok := func() map[string]any {
		return map[string]any{
			"reference_run_ids": []string{"ref-1"}, "candidate_run_ids": []string{"can-1"},
			"gate_limits": repeatedLimitsBody("1", "0"),
		}
	}
	missingLimit := ok()
	delete(missingLimit["gate_limits"].(map[string]any), "added_reference_presence_maximum")
	malformed := ok()
	malformed["gate_limits"].(map[string]any)["max_repeated_added_behaviors"] = "01"
	kTooHigh := ok()
	kTooHigh["gate_limits"] = repeatedLimitsBody("2", "0")
	jEqualsK := ok()
	jEqualsK["gate_limits"] = repeatedLimitsBody("1", "1")
	unequal := ok()
	unequal["candidate_run_ids"] = []string{"can-1", "can-2"}
	unknownField := ok()
	unknownField["runs"] = 1
	missingRun := ok()
	missingRun["candidate_run_ids"] = []string{"no-such-run"}

	for name, tc := range map[string]struct {
		body   map[string]any
		status int
	}{
		"a missing limit":   {missingLimit, 400},
		"a malformed limit": {malformed, 400},
		"k above N":         {kTooHigh, 400},
		"j equal to k":      {jEqualsK, 400},
		"unequal sides":     {unequal, 400},
		"an unknown field":  {unknownField, 400},
		"a missing run":     {missingRun, 404},
	} {
		t.Run(name, func(t *testing.T) {
			a.mustStatus(a.do("POST", "/v1/evaluations/compare-repeated", tc.body), tc.status, name)
		})
	}
}

func TestCompareRepeatedRefusesASharedProfile(t *testing.T) {
	a := newAPI(t)
	a.completeRun("ref-1", "cand-1", []string{"read"}) // both under testProfile
	a.completeRun("can-1", "cand-2", []string{"read"})
	r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
		"reference_run_ids": []string{"ref-1"}, "candidate_run_ids": []string{"can-1"},
		"gate_limits": repeatedLimitsBody("1", "0"),
	})
	a.mustStatus(r, 400, "shared profile")
}

// Every repetition must run in one environment. The review's reproduction over
// /v1: the candidate's new export happens in staging in one repetition and in
// production in the other, each fingerprint present once, below k = 2 — a
// false PASS before this was refused. N = 1 is refused the same way.
func TestCompareRepeatedRefusesRepetitionsAcrossEnvironments(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "production", "name": "Production",
	}), 201, "create production")
	a.completeIsolatedRun("ref-1", []string{"read"})
	a.completeIsolatedRun("ref-2", []string{"read"})
	a.completeIsolatedRun("can-1", []string{"read", "export"})
	a.completeIsolatedRunIn("production", "can-2", []string{"read", "export"})

	for name, sides := range map[string][2][]string{
		"N = 2": {{"ref-1", "ref-2"}, {"can-1", "can-2"}},
		"N = 1": {{"ref-1"}, {"can-2"}},
	} {
		t.Run(name, func(t *testing.T) {
			k := fmt.Sprint(len(sides[0]))
			r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
				"reference_run_ids": sides[0], "candidate_run_ids": sides[1],
				"gate_limits": repeatedLimitsBody(k, "0"),
			})
			a.mustStatus(r, 400, "cross-environment repetitions")
			var body struct {
				Gate  *struct{} `json:"gate"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Gate != nil || body.Error.Code != "invalid_request" {
				t.Errorf("body = %s; want an invalid_request error and no gate", r.Body.Bytes())
			}
		})
	}
}
