package main

// Task 106 in `eval run`: the scenario's frequency limits reach the control
// plane exactly as written, and the configured checks are rendered.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEvalRunForwardsTheFrequencyLimits(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	scenario := runScenarioYAML + `  min_candidate_frequency: 3
  max_lost_behaviors: 0
  max_calls_per_run:
    - {target: crm.internal, max: 6}
`
	if code, _, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, scenario)); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	var body struct {
		GateLimits map[string]json.RawMessage `json:"gate_limits"`
	}
	if err := json.Unmarshal(api.request(t, "/complete"), &body); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"min_candidate_frequency": `"3"`,
		"max_lost_behaviors":      `"0"`,
		"max_calls_per_run":       `[{"target":"crm.internal","max":"6"}]`,
	} {
		if got := string(body.GateLimits[key]); got != want {
			t.Errorf("gate_limits.%s = %s, want %s", key, got, want)
		}
	}

	// Omitted limits are not sent at all: the control plane then evaluates
	// nothing, exactly as before task 106.
	plain := newScenarioAPI(t, "pass")
	if code, _, _ := runScenario(t, &recorder{}, plain.url(), "--scenario", writeScenario(t, runScenarioYAML)); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if raw := string(plain.request(t, "/complete")); strings.Contains(raw, "min_candidate_frequency") ||
		strings.Contains(raw, "max_lost_behaviors") || strings.Contains(raw, "max_calls_per_run") {
		t.Errorf("omitted limits were sent: %s", raw)
	}
}

func TestEvalRunRendersTheConfiguredFrequencyChecks(t *testing.T) {
	reply := strings.Replace(repeatedReply("fail"), `"verdict":"fail"}`, `"verdict":"fail",
	  "frequency_checks":[
	    {"name":"min_candidate_frequency","state":"not_evaluated"},
	    {"name":"max_lost_behaviors","state":"deferred","missing_evidence":"no completed candidate repetition"},
	    {"name":"max_calls_per_run","state":"evaluated","rule":"at_most","passed":false,"targets":[
	      {"target":"crm.internal","actual":"9","max":"6","outcome":"fail"},
	      {"target":"typo.host","actual":"0","max":"1","outcome":"not_observed"}]}]}`, 1)
	var c repeatedDTO
	if err := json.Unmarshal([]byte(reply), &c); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := renderScenarioResult(&out, "support", "scn-test", nil, nil, c); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FAIL max lost behaviors: deferred — no completed candidate repetition",
		"FAIL max calls per run",
		"crm.internal: 9 (<= 6)   fail",
		"typo.host: 0 (<= 1)   not observed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "min candidate frequency") {
		t.Error("a check the scenario did not configure was rendered")
	}
}
