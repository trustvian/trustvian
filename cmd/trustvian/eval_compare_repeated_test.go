package main

// trustvian eval compare-repeated, task 086: the runs and limits reach the
// route as given, both output modes print the server's body unchanged — the
// sameness block included — and the exit code is the verdict's.

import (
	"fmt"
	"slices"
	"testing"
)

func compareRepeatedReply(verdict string) string {
	return fmt.Sprintf(`{"version":"1","runs":2,"gate":{"checks":[],"verdict":%q},`+
		`"sameness":{"same_scenario":"true","same_inputs":"true","same_model":"false","same_prompt_ref":"not_recorded",`+
		`"reference":{"inputs":"not_declared","model":"llama3.2"},"candidate":{"inputs":"not_declared","model":"gemma3:4b"}},`+
		`"warnings":[{"code":"model_differs","text":"different"}],"extra":"kept"}`, verdict)
}

var compareRepeatedArgs = []string{"eval", "compare-repeated",
	"--reference-run", "e1-candidate-2", "--reference-run", "e1-candidate-1",
	"--candidate-run", "e2-candidate-1", "--candidate-run", "e2-candidate-2",
	"--added-candidate-presence-minimum", "2", "--added-reference-presence-maximum", "0",
	"--max-repeated-added-behaviors", "0", "--max-block-decisions-per-run", "0",
	"--max-critical-risk-observations-per-run", "18446744073709551615"}

func TestEvalCompareRepeatedForwardsAndPrintsTheBody(t *testing.T) {
	for _, tt := range []struct {
		verdict string
		exit    int
		mode    []string
	}{{"pass", exitOK, nil}, {"fail", exitGateFail, nil}, {"pass", exitOK, []string{"--json"}}} {
		api := newFakeAPI(t)
		body := compareRepeatedReply(tt.verdict)
		api.reply(200, body)
		args := append(append(append([]string{}, compareRepeatedArgs...), "--min-candidate-frequency", "1",
			"--max-calls-per-run", "crm.internal=6", "--max-calls-per-run", "kb=1=2",
			"--max-llm-calls-per-run", "40", "--api-url", api.url()),
			tt.mode...)
		result := runPlatformCLI(t, args...)
		result.mustExit(t, tt.exit, "eval compare-repeated "+tt.verdict)

		request := api.only()
		if request.method != "POST" || request.escapedPath != "/v1/evaluations/compare-repeated" {
			t.Fatalf("request = %s %s", request.method, request.escapedPath)
		}
		want := `{"reference_run_ids":["e1-candidate-2","e1-candidate-1"],` +
			`"candidate_run_ids":["e2-candidate-1","e2-candidate-2"],"gate_limits":{` +
			`"added_candidate_presence_minimum":"2","added_reference_presence_maximum":"0",` +
			`"max_repeated_added_behaviors":"0","max_block_decisions_per_run":"0",` +
			`"max_critical_risk_observations_per_run":"18446744073709551615","min_candidate_frequency":"1",` +
			`"max_calls_per_run":[{"target":"crm.internal","max":"6"},{"target":"kb=1","max":"2"}],` +
			`"max_llm_calls_per_run":"40"}}`
		if string(request.body) != want {
			t.Fatalf("request body\n%s\nwant\n%s", request.body, want)
		}
		if result.stdout != body+"\n" {
			t.Fatalf("stdout %q is not the server's body", result.stdout)
		}
	}
}

func TestEvalCompareRepeatedRefusesWhatItCannotForward(t *testing.T) {
	for name, args := range map[string][]string{
		"no candidate run": {"eval", "compare-repeated", "--reference-run", "r",
			"--added-candidate-presence-minimum", "1"},
		"a missing limit":       slices.Clone(compareRepeatedArgs[:len(compareRepeatedArgs)-2]),
		"a malformed target":    append(append([]string{}, compareRepeatedArgs...), "--max-calls-per-run", "crm"),
		"a non-canonical bound": append(append([]string{}, compareRepeatedArgs...), "--max-calls-per-run", "crm=07"),
	} {
		t.Run(name, func(t *testing.T) {
			result := runPlatformCLI(t, append(slices.Clone(args), "--api-url", "http://127.0.0.1:1")...)
			result.mustExit(t, exitUsage, name)
		})
	}
}

// TestEvalCompareRepeatedRefusesAnUnknownVerdict before writing anything.
func TestEvalCompareRepeatedRefusesAnUnknownVerdict(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, compareRepeatedReply("maybe"))
	result := runPlatformCLI(t, append(append([]string{}, compareRepeatedArgs...), "--api-url", api.url())...)
	result.mustExit(t, exitOperational, "unknown verdict")
	if result.stdout != "" {
		t.Fatalf("stdout = %q", result.stdout)
	}
}
