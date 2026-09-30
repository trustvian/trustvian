package main

// --max-added-behavior-changes (ADR 0052, issue 131) on the CLI.
//
// The CLI is a thin adapter, so what matters is the request it sends and the
// words it prints: an omitted flag must be an omitted field — never "0" — and
// a check the server did not evaluate must print neither PASS nor FAIL.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func sentLimits(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var request struct {
		GateLimits map[string]json.RawMessage `json:"gate_limits"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode request: %v (%s)", err, body)
	}
	return request.GateLimits
}

// changeComparisonBody is a comparison whose gate carries the given
// added_behavior_changes object.
func changeComparisonBody(verdict, check string) string {
	return fmt.Sprintf(`{
	  "version": "1",
	  "reference_run_id": "ref",
	  "candidate_run_id": "cand",
	  "behavior_diff": {"added_count": 2, "removed_count": 0, "shared_count": 1,
	    "added_change_count": 1, "correlation_state": "complete", "counting_policy_version": "1"},
	  "gate": {
	    "reference_evidence": {"actual":"10","minimum":"1","passed":true},
	    "candidate_evidence": {"actual":"12","minimum":"1","passed":true},
	    "added_behaviors": {"actual":"2","maximum":"5","passed":true},
	    "block_decisions": {"actual":"0","maximum":"0","passed":true},
	    "critical_risk_observations": {"actual":"0","maximum":"0","passed":true},
	    "added_behavior_changes": %s,
	    "verdict": "%s"
	  }
	}`, check, verdict)
}

func TestCompareSendsTheChangeLimitOnlyWhenGiven(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		want  string // raw JSON of the field, "" for absent
	}{
		{"omitted", nil, ""},
		{"zero", []string{"--max-added-behavior-changes", "0"}, `"0"`},
		{"uint64 max", []string{"--max-added-behavior-changes", "18446744073709551615"},
			`"18446744073709551615"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, changeComparisonBody(gateVerdictPass, `{"state":"not_evaluated"}`))
			runPlatformCLI(t, compareArgs(api.url(), tc.extra...)...).mustExit(t, exitOK, "compare")

			limits := sentLimits(t, api.only().body)
			got, present := limits["max_added_behavior_changes"]
			if tc.want == "" {
				if present {
					t.Errorf("an omitted flag sent max_added_behavior_changes = %s; "+
						"absent must not reach the server as a value", got)
				}
				return
			}
			if !present || string(got) != tc.want {
				t.Errorf("sent %s (present %v), want %s", got, present, tc.want)
			}
		})
	}
}

func TestPromotionCreateSendsTheChangeLimitOnlyWhenGiven(t *testing.T) {
	base := []string{
		"promotion", "create", "--id", "promo-1",
		"--reference-run", "ref", "--candidate-run", "cand",
		"--target-environment", "production",
		"--max-added-behaviors", "3", "--max-block-decisions", "0",
		"--max-critical-risk-observations", "0",
	}
	for _, tc := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"omitted", nil, ""},
		{"zero", []string{"--max-added-behavior-changes", "0"}, `"0"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(201, promotionReply(`null`, `{"state":"not_evaluated"}`))
			args := append(append([]string{}, base...), "--api-url", api.url())
			runPlatformCLI(t, append(args, tc.extra...)...).mustExit(t, exitOK, "promotion create")

			got, present := sentLimits(t, api.only().body)["max_added_behavior_changes"]
			if tc.want == "" && present {
				t.Errorf("an omitted flag sent %s", got)
			}
			if tc.want != "" && string(got) != tc.want {
				t.Errorf("sent %s, want %s", got, tc.want)
			}
		})
	}
}

// Malformed values are a usage error before anything is sent.
func TestChangeLimitFlagRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"-1", "01", "1.5", "", "18446744073709551616", "+1", "0x1"} {
		t.Run(value, func(t *testing.T) {
			api := newFakeAPI(t)
			result := runPlatformCLI(t, compareArgs(api.url(),
				"--max-added-behavior-changes", value)...)
			result.mustExit(t, exitUsage, "malformed --max-added-behavior-changes")
			if n := len(api.captured()); n != 0 {
				t.Errorf("a malformed limit sent %d requests", n)
			}
		})
	}
}

func TestCompareRendersEachChangeCheckState(t *testing.T) {
	for _, tc := range []struct {
		name, verdict, check string
		exit                 int
		want, forbid         string
	}{
		{"not evaluated", gateVerdictPass, `{"state":"not_evaluated"}`, exitOK,
			"---- Added behavior changes: not evaluated", "Added behavior changes: 0"},
		{"not recorded", gateVerdictPass, `{"state":"not_recorded"}`, exitOK,
			"---- Added behavior changes: not recorded", "PASS Added behavior changes"},
		{"passed", gateVerdictPass, `{"state":"evaluated","actual":"1","maximum":"1","passed":true,
			"correlation_state":"complete","counting_policy_version":"1"}`, exitOK,
			"PASS Added behavior changes: 1 (maximum 1)", "not evaluated"},
		{"failed, partial", gateVerdictFail, `{"state":"evaluated","actual":"4","maximum":"3","passed":false,
			"correlation_state":"partial","counting_policy_version":"1"}`, exitGateFail,
			"FAIL Added behavior changes: 4 (maximum 3; correlation partial, so this is the identity count)",
			"PASS Added behavior changes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, changeComparisonBody(tc.verdict, tc.check))
			result := runPlatformCLI(t, compareArgs(api.url())...)
			result.mustExit(t, tc.exit, "compare")
			if !strings.Contains(result.stdout, tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, result.stdout)
			}
			if strings.Contains(result.stdout, tc.forbid) {
				t.Errorf("output contains %q:\n%s", tc.forbid, result.stdout)
			}
		})
	}
}

// promotionReply is a stored decision with the given limit and check.
func promotionReply(limit, check string) string {
	return fmt.Sprintf(`{
	  "version": "1", "id": "promo-1", "project_id": "proj-1",
	  "candidate_id": "cand", "reference_candidate_id": "ref-cand",
	  "reference_run_id": "ref", "candidate_run_id": "cand",
	  "source_environment": {"ref": "staging", "rank": 30, "revision": "1"},
	  "target_environment": {"ref": "production", "rank": 40, "revision": "1"},
	  "gate_limits": {"max_added_behaviors": "3", "max_block_decisions": "0",
	    "max_critical_risk_observations": "0", "max_added_behavior_changes": %s},
	  "gate_result": {
	    "reference_evidence": {"actual":"10","minimum":"1","passed":true},
	    "candidate_evidence": {"actual":"12","minimum":"1","passed":true},
	    "added_behaviors": {"actual":"2","maximum":"3","passed":true},
	    "block_decisions": {"actual":"0","maximum":"0","passed":true},
	    "critical_risk_observations": {"actual":"0","maximum":"0","passed":true},
	    "added_behavior_changes": %s,
	    "verdict": "pass"
	  },
	  "outcome": "accepted", "decided_at": "2026-03-01T09:14:22Z"
	}`, limit, check)
}

func TestPromotionGetRendersAHistoricalCheckAsNotRecorded(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, promotionReply(`null`, `{"state":"not_recorded"}`))
	result := runPlatformCLI(t, "promotion", "get", "--id", "promo-1", "--api-url", api.url())
	result.mustExit(t, exitOK, "promotion get")
	if !strings.Contains(result.stdout,
		"Added behavior changes: not recorded (decided before this check existed)") {
		t.Errorf("a historical promotion's check is not rendered as not recorded:\n%s", result.stdout)
	}
}
