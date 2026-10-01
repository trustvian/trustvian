package main

// trustvian eval run against the real binaries (task 078).
//
// The whole path, nothing faked: the CLI under test drives `trustvian dev`
// in-process for every repetition, each repetition starts the real Collector,
// the deterministic agent-producer emits GenAI tool spans, the real control
// plane ingests them, and POST /v1/evaluations/compare-repeated returns the
// verdict the exit code carries. Deterministic, so CI needs no model:
// agent-producer emits crm_lookup, knowledge_search, export_customer in turn,
// so SPAN_COUNT=2 never reaches export_customer and SPAN_COUNT=3 always does.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeScenarioWorkload(t *testing.T, binaries realBinaries) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "workload.sh")
	body := fmt.Sprintf(`#!/bin/sh
OTLP_ENDPOINT="$TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT" \
MODE=semantic \
SERVICE_NAME=%s \
ENVIRONMENT=%s \
exec %q
`, e2eServiceName, devEnvironmentDefault, binaries.agentProducer)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing the workload script: %v", err)
	}
	return script
}

func scenarioFor(script, referenceCommand, candidateSpans string) string {
	return fmt.Sprintf(`version: v1
name: e2e-scenario
runs: 2
instrumentation: existing
reference:
  command: [sh, %q]
  env: {SPAN_COUNT: "2", %s: %s}
candidate:
  command: [%s]
  env: {SPAN_COUNT: %q, %s: %s}
gate:
  added_candidate_presence_minimum: 2
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
`, script, envServiceName, e2eServiceName, referenceCommand, candidateSpans, envServiceName, e2eServiceName)
}

func runEvalRunForReal(t *testing.T, binaries realBinaries, apiURL, home string,
	workload *e2eFixture, scenario string) (int, []byte, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(devBinary(t), "eval", "run", "--scenario", path,
		"--api-url", apiURL, "--collector-bin", binaries.collector, "--json")
	cmd.Dir = workload.dir
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running eval run: %v", err)
	}
	return code, stdout.Bytes(), stderr.String()
}

type e2eRepeatedResult struct {
	ExecutionID string `json:"execution_id"`
	Producers   struct {
		CLIVersion    string `json:"cli_version"`
		ServerVersion string `json:"control_plane_version"`
	} `json:"producers"`
	Comparison struct {
		Repetitions []struct {
			RunID             string `json:"run_id"`
			BehavioralProfile string `json:"behavioral_profile"`
			Status            string `json:"status"`
		} `json:"repetitions"`
		Behaviors []struct {
			Behavior struct {
				OperationName string `json:"operation_name"`
			} `json:"behavior"`
			ReferenceRunsPresent string `json:"reference_runs_present"`
			CandidateRunsPresent string `json:"candidate_runs_present"`
			Classification       string `json:"classification"`
		} `json:"behaviors"`
		Gate struct {
			Checks []struct {
				Name     string `json:"name"`
				Advisory string `json:"advisory"`
			} `json:"checks"`
			Verdict string `json:"verdict"`
		} `json:"gate"`
	} `json:"comparison"`
}

func TestEvalRunAgainstRealBinaries(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()
	script := writeScenarioWorkload(t, binaries)
	shScript := fmt.Sprintf("sh, %q", script)

	t.Run("unchanged workload passes", func(t *testing.T) {
		code, out, stderr := runEvalRunForReal(t, binaries, runtime.apiURL, home, workload,
			scenarioFor(script, shScript, "2"))
		if code != exitOK {
			t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, out, stderr)
		}
	})

	t.Run("a candidate that gains a tool fails, and the server says why", func(t *testing.T) {
		code, out, stderr := runEvalRunForReal(t, binaries, runtime.apiURL, home, workload,
			scenarioFor(script, shScript, "3"))
		if code != exitGateFail {
			t.Fatalf("exit %d, want 1\nstdout: %s\nstderr: %s", code, out, stderr)
		}
		var result e2eRepeatedResult
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("stdout is not the result document: %v\n%s", err, out)
		}
		var export bool
		for _, b := range result.Comparison.Behaviors {
			if b.Behavior.OperationName == "export_customer" {
				export = true
				if b.ReferenceRunsPresent != "0" || b.CandidateRunsPresent != "2" || b.Classification != "added" {
					t.Errorf("export_customer = %s/%s %s, want 0/2 added",
						b.ReferenceRunsPresent, b.CandidateRunsPresent, b.Classification)
				}
			}
		}
		if !export {
			t.Errorf("export_customer is not in the behaviors: %s", out)
		}
		profiles := map[string]bool{}
		for _, r := range result.Comparison.Repetitions {
			if r.Status != "completed" {
				t.Errorf("repetition %s is %s", r.RunID, r.Status)
			}
			profiles[r.BehavioralProfile] = true
		}
		if len(result.Comparison.Repetitions) != 4 || len(profiles) != 4 {
			t.Errorf("%d repetitions under %d profiles, want 4 and 4",
				len(result.Comparison.Repetitions), len(profiles))
		}
		if checks := result.Comparison.Gate.Checks; len(checks) != 6 ||
			checks[4].Advisory != "fresh_scope" || checks[5].Advisory != "fresh_scope" {
			t.Errorf("checks = %+v; want six, with 5 and 6 advisory at N = 2", checks)
		}
		if result.ExecutionID == "" || result.Producers.CLIVersion == "" || result.Producers.ServerVersion == "" {
			t.Errorf("identity or producers missing: %+v", result)
		}
	})

	t.Run("a crashing candidate stops the scenario with exit 3", func(t *testing.T) {
		// The reference and candidate command lines are textually identical, so
		// the candidate's is the last occurrence; replacing the first would crash
		// the reference side and still exit 3.
		scenario := scenarioFor(script, shScript, "2")
		candidateLine := fmt.Sprintf("command: [%s]", shScript)
		at := strings.LastIndex(scenario, candidateLine)
		scenario = scenario[:at] + `command: [sh, -c, "exit 5"]` + scenario[at+len(candidateLine):]

		code, out, stderr := runEvalRunForReal(t, binaries, runtime.apiURL, home, workload, scenario)
		if code != exitOperational {
			t.Fatalf("exit %d, want 3\nstdout: %s\nstderr: %s", code, out, stderr)
		}
		for _, ran := range []string{"reference repetition 2 of 2", "candidate repetition 1 of 2"} {
			if !strings.Contains(stderr, ran) {
				t.Errorf("stderr does not show %q ran:\n%s", ran, stderr)
			}
		}
		if strings.Contains(stderr, "candidate repetition 2 of 2") {
			t.Errorf("the second candidate repetition ran after the first crashed:\n%s", stderr)
		}
		if len(bytes.TrimSpace(out)) != 0 && strings.Contains(string(out), `"verdict"`) {
			t.Errorf("a verdict was produced: %s", out)
		}
	})
}
