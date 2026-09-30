package httpapi_test

// The optional counted-change limit over /v1 (ADR 0052, issue 131).
//
// The wire is where absent and zero are easiest to collapse: an omitted JSON
// field, a null and "0" all decode to something, and a serializer that wrote
// the zero value of an absent limit would turn "not evaluated" into "the
// strictest limit". These assert on the raw bytes as well as on decoded
// values, because a decoder can hide exactly that difference.

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	platform "trustvian-platform"
)

// oneActRuns completes a reference and a candidate that added one tool and
// the transport child it calls: two identities, one counted change.
func (a *api) oneActRuns() {
	a.t.Helper()
	a.completeSpanRun("run-ref", "cand-1", []trustvian.DecisionRecord{
		spanRecord("ref-1", "fp-read", "read", "trace-r", "span-r", ""),
	})
	a.completeSpanRun("run-can", "cand-2", []trustvian.DecisionRecord{
		spanRecord("can-1", "fp-read", "read", "trace-c", "span-c", ""),
		spanRecord("can-2", "fp-tool", "export_customer", "trace-c", "span-tool", "span-c"),
		spanRecord("can-3", "fp-http", "post_export", "trace-c", "span-http", "span-tool"),
	})
}

// seedOneActPromotable adds the ranked environment pair a promotion needs.
func (a *api) seedOneActPromotable() {
	a.t.Helper()
	a.oneActRuns()
	source := decodeEnvironment(a.t, a.do(
		"GET", "/v1/projects/proj-1/environments/"+testEnvironment, nil).Body.Bytes())
	a.mustStatus(a.do("POST",
		"/v1/projects/proj-1/environments/"+testEnvironment+"/configure", map[string]any{
			"revision": source.Revision, "rank": 30,
		}), 200, "rank the source")
	a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "production", "name": "Production", "rank": 40,
	}), 201, "create the target")
}

// limitsWithChanges is permissive on the three original limits and carries
// the optional one as given: absent when changes is nil, the raw JSON value
// otherwise.
func limitsWithChanges(changes any, present bool) map[string]any {
	maxUint := platform.FormatSequence(math.MaxUint64)
	body := limitsBody(maxUint, maxUint, maxUint)
	if present {
		body["max_added_behavior_changes"] = changes
	}
	return body
}

// changeCheck decodes gate.added_behavior_changes (or gate_result's) as raw
// keys, so an absent field and a zero field are different results.
func changeCheck(t *testing.T, raw []byte, gateKey string) map[string]json.RawMessage {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, raw)
	}
	var gate map[string]json.RawMessage
	if err := json.Unmarshal(envelope[gateKey], &gate); err != nil {
		t.Fatalf("decode %s: %v (%s)", gateKey, err, envelope[gateKey])
	}
	var check map[string]json.RawMessage
	if err := json.Unmarshal(gate["added_behavior_changes"], &check); err != nil {
		t.Fatalf("decode added_behavior_changes: %v (%s)", err, gate["added_behavior_changes"])
	}
	return check
}

func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not a JSON string: %s", raw)
	}
	return s
}

func TestCompareOptionalChangeLimitOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.oneActRuns()

	compare := func(limits map[string]any) []byte {
		t.Helper()
		r := a.do("POST", "/v1/evaluations/compare", map[string]any{
			"reference_run_id": "run-ref", "candidate_run_id": "run-can", "gate_limits": limits,
		})
		a.mustStatus(r, 200, "compare")
		return r.Body.Bytes()
	}

	// Omitted and null both mean absent: the check is not evaluated, and it
	// carries no outcome fields at all.
	for name, limits := range map[string]map[string]any{
		"omitted": limitsWithChanges(nil, false),
		"null":    limitsWithChanges(nil, true),
	} {
		t.Run(name, func(t *testing.T) {
			raw := compare(limits)
			check := changeCheck(t, raw, "gate")
			if rawString(t, check["state"]) != "not_evaluated" {
				t.Errorf("state = %s, want not_evaluated", check["state"])
			}
			for _, key := range []string{"actual", "maximum", "passed",
				"correlation_state", "counting_policy_version"} {
				if _, present := check[key]; present {
					t.Errorf("a check that did not run carries %q = %s", key, check[key])
				}
			}
			if !strings.Contains(string(raw), `"verdict":"pass"`) {
				t.Errorf("verdict moved when the new limit was omitted: %s", raw)
			}
		})
	}

	// Present: evaluated, against the folded count of one.
	for _, tc := range []struct {
		limit, passed, verdict string
	}{
		{"0", "false", "fail"},
		{"1", "true", "pass"},
		{platform.FormatSequence(math.MaxUint64), "true", "pass"},
	} {
		t.Run("limit "+tc.limit, func(t *testing.T) {
			raw := compare(limitsWithChanges(tc.limit, true))
			check := changeCheck(t, raw, "gate")
			if rawString(t, check["state"]) != "evaluated" ||
				rawString(t, check["actual"]) != "1" ||
				rawString(t, check["maximum"]) != tc.limit ||
				string(check["passed"]) != tc.passed ||
				rawString(t, check["correlation_state"]) != "complete" ||
				rawString(t, check["counting_policy_version"]) != platform.CountingPolicyVersion {
				t.Errorf("check = %v, want evaluated actual 1 maximum %s passed %s complete",
					check, tc.limit, tc.passed)
			}
			if !strings.Contains(string(raw), `"verdict":"`+tc.verdict+`"`) {
				t.Errorf("verdict is not %s: %s", tc.verdict, raw)
			}
		})
	}
}

// Malformed limits are refused with the same rule as the other three, and a
// refusal records nothing.
func TestCompareRejectsMalformedChangeLimits(t *testing.T) {
	a := newAPI(t)
	a.oneActRuns()
	for name, value := range map[string]any{
		"negative":      "-1",
		"leading zero":  "01",
		"leading plus":  "+1",
		"fraction":      "1.5",
		"empty":         "",
		"whitespace":    " 1",
		"overflow":      "18446744073709551616",
		"JSON number":   1,
		"JSON boolean":  true,
		"JSON object":   map[string]any{},
		"exponent":      "1e3",
		"hex":           "0x1",
		"unicode digit": "١",
	} {
		t.Run(name, func(t *testing.T) {
			r := a.do("POST", "/v1/evaluations/compare", map[string]any{
				"reference_run_id": "run-ref", "candidate_run_id": "run-can",
				"gate_limits": limitsWithChanges(value, true),
			})
			a.mustStatus(r, 400, "malformed max_added_behavior_changes")
		})
	}
}

// Absent and zero survive a promotion's write, its read and a restart as
// different values.
func TestPromotionPreservesAbsentVersusZeroChangeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	a := openAPI(t, path)
	a.seedOneActPromotable()

	type expectation struct {
		limit   string // raw JSON of gate_limits.max_added_behavior_changes
		state   string
		outcome string
	}
	want := map[string]expectation{
		"promo-absent": {"null", "not_evaluated", "accepted"},
		"promo-zero":   {`"0"`, "evaluated", "rejected"},
		"promo-one":    {`"1"`, "evaluated", "accepted"},
	}
	requests := map[string]map[string]any{
		"promo-absent": limitsWithChanges(nil, false),
		"promo-zero":   limitsWithChanges("0", true),
		"promo-one":    limitsWithChanges("1", true),
	}

	check := func(t *testing.T, id string, raw []byte) {
		t.Helper()
		var envelope struct {
			GateLimits map[string]json.RawMessage `json:"gate_limits"`
			Outcome    string                     `json:"outcome"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}
		limit, present := envelope.GateLimits["max_added_behavior_changes"]
		if !present || string(limit) != want[id].limit {
			t.Errorf("%s: gate_limits.max_added_behavior_changes = %s (present %v), want %s",
				id, limit, present, want[id].limit)
		}
		if got := rawString(t, changeCheck(t, raw, "gate_result")["state"]); got != want[id].state {
			t.Errorf("%s: state = %s, want %s", id, got, want[id].state)
		}
		if envelope.Outcome != want[id].outcome {
			t.Errorf("%s: outcome = %s, want %s", id, envelope.Outcome, want[id].outcome)
		}
	}

	created := map[string][]byte{}
	for id, limits := range requests {
		r := a.do("POST", "/v1/promotions", map[string]any{
			"id": id, "reference_run_id": "run-ref", "candidate_run_id": "run-can",
			"target_environment": "production", "gate_limits": limits,
		})
		a.mustStatus(r, 201, "create "+id)
		check(t, id, r.Body.Bytes())
		created[id] = r.Body.Bytes()

		got := a.do("GET", "/v1/promotions/"+id, nil)
		a.mustStatus(got, 200, "get "+id)
		if got.Body.String() != string(created[id]) {
			t.Errorf("%s: GET differs from the create response:\n%s\n%s",
				id, got.Body.String(), created[id])
		}
	}

	// Restart: the same database through a new handler.
	a.store.Close()
	reopened := openAPI(t, path)
	for id := range requests {
		got := reopened.do("GET", "/v1/promotions/"+id, nil)
		reopened.mustStatus(got, 200, "get after restart "+id)
		if got.Body.String() != string(created[id]) {
			t.Errorf("%s changed across a restart:\n%s\n%s", id, got.Body.String(), created[id])
		}
	}
}

// The check is published but not resolvable through the finding route, and
// the refusal says where its evidence is instead of calling it unknown.
func TestEvidenceRouteRefusesTheChangeCheckWithDirections(t *testing.T) {
	a := newAPI(t)
	a.oneActRuns()
	for _, route := range []string{"behaviors", "observations"} {
		r := a.do("GET", "/v1/evidence/"+route+
			"?reference_run_id=run-ref&candidate_run_id=run-can&check=added_behavior_changes", nil)
		a.mustStatus(r, 400, "resolve added_behavior_changes via "+route)
		body := r.Body.String()
		if !strings.Contains(body, "added_changes") {
			t.Errorf("%s refusal does not point at added_changes: %s", route, body)
		}
		if strings.Contains(body, "not a gate check") {
			t.Errorf("%s refusal calls a published check unknown: %s", route, body)
		}
	}
}
