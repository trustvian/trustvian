//go:build !windows

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
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const workloadStdoutMarker = "tv-e2e-workload-stdout-line"

func writeScenarioWorkload(t *testing.T, binaries realBinaries) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "workload.sh")
	// The first line is the workload's own stdout, which must reach eval run's
	// stderr and never its stdout.
	body := fmt.Sprintf(`#!/bin/sh
echo %s
OTLP_ENDPOINT="$TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT" \
MODE=semantic \
SERVICE_NAME=%s \
ENVIRONMENT=%s \
exec %q
`, workloadStdoutMarker, e2eServiceName, devEnvironmentDefault, binaries.agentProducer)
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
	workload *e2eFixture, scenario string, extra ...string) (int, []byte, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(devBinary(t), "eval", "run", "--scenario", path,
		"--api-url", apiURL, "--collector-bin", binaries.collector, "--json")
	cmd.Args = append(cmd.Args, extra...)
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

	t.Run("unchanged workload passes, and stdout is the result document alone", func(t *testing.T) {
		code, out, stderr := runEvalRunForReal(t, binaries, runtime.apiURL, home, workload,
			scenarioFor(script, shScript, "2"))
		if code != exitOK {
			t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, out, stderr)
		}
		// Exactly one JSON value and nothing before or after it: a workload
		// line ahead of the document is what the child inheriting this
		// process's stdout produced.
		decoder := json.NewDecoder(bytes.NewReader(out))
		var document e2eRepeatedResult
		if err := decoder.Decode(&document); err != nil {
			t.Fatalf("stdout does not start with the result document: %v\n%s", err, out)
		}
		if _, err := decoder.Token(); err != io.EOF {
			t.Errorf("stdout continues after the result document (%v):\n%s", err, out)
		}
		if document.Comparison.Gate.Verdict != "pass" || strings.Contains(string(out), workloadStdoutMarker) {
			t.Errorf("verdict %q; workload output on stdout: %v\n%s", document.Comparison.Gate.Verdict,
				strings.Contains(string(out), workloadStdoutMarker), out)
		}
		if n := strings.Count(stderr, workloadStdoutMarker); n != 4 {
			t.Errorf("the workload's stdout line reached stderr %d times, want once per repetition (4):\n%s", n, stderr)
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

	t.Run("a run that cannot be completed stops the scenario with exit 3", func(t *testing.T) {
		// The real control plane behind a proxy that refuses the first
		// completion it is asked for, as a control plane briefly unavailable
		// would. The workload itself succeeds.
		target, err := url.Parse(runtime.apiURL)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		var (
			mu          sync.Mutex
			completions []string
		)
		front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/complete") {
				mu.Lock()
				completions = append(completions, r.URL.Path)
				first := len(completions) == 1
				mu.Unlock()
				if first {
					http.Error(w, `{"version":"1","error":{"code":"unavailable","message":"unavailable"}}`,
						http.StatusServiceUnavailable)
					return
				}
			}
			proxy.ServeHTTP(w, r)
		}))
		t.Cleanup(front.Close)

		code, out, stderr := runEvalRunForReal(t, binaries, front.URL, home, workload,
			scenarioFor(script, shScript, "2"))
		if code != exitOperational {
			t.Fatalf("exit %d, want 3: a run left uncompleted is not a behavioral FAIL\nstdout: %s\nstderr: %s",
				code, out, stderr)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(completions) != 1 {
			t.Errorf("%d completions were requested, want 1: the scenario must stop at the first", len(completions))
		}
		if strings.Contains(stderr, "reference repetition 2 of 2") || !strings.Contains(stderr, "could not be completed") {
			t.Errorf("stderr:\n%s", stderr)
		}
		if strings.Contains(string(out), `"verdict"`) {
			t.Errorf("a verdict was produced: %s", out)
		}
	})
}

// The recorded-reference journey against the real binaries: an execution is
// persisted, the control plane restarts on the same state, and a later
// invocation reuses that execution's reference side — by `last` and by id —
// running only its own N candidate repetitions. A missing reference stops
// before any workload.
func TestEvalRunReusesARecordedReferenceAcrossARestart(t *testing.T) {
	binaries := buildRealBinaries(t)
	stateDir := t.TempDir()
	workload := newE2EFixture(t)
	home := t.TempDir()
	script := writeScenarioWorkload(t, binaries)
	shScript := fmt.Sprintf("sh, %q", script)

	type document struct {
		ExecutionID string `json:"execution_id"`
		Reference   *struct {
			Mode        string `json:"mode"`
			ExecutionID string `json:"execution_id"`
		} `json:"reference"`
		Comparison struct {
			Repetitions []struct {
				Side  string `json:"side"`
				RunID string `json:"run_id"`
			} `json:"repetitions"`
			Gate struct {
				Verdict string `json:"verdict"`
			} `json:"gate"`
		} `json:"comparison"`
	}
	decode := func(t *testing.T, out []byte) document {
		t.Helper()
		var doc document
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("stdout is not the result document: %v\n%s", err, out)
		}
		return doc
	}
	referenceRuns := func(doc document) []string {
		var out []string
		for _, r := range doc.Comparison.Repetitions {
			if r.Side == "reference" {
				out = append(out, r.RunID)
			}
		}
		return out
	}

	// Persist: a self-contained execution, then stop the control plane.
	first, stop := startRealRuntimeAt(t, binaries.local, stateDir)
	code, out, stderr := runEvalRunForReal(t, binaries, first.apiURL, home, workload,
		scenarioFor(script, shScript, "2"))
	if code != exitOK {
		t.Fatalf("recording execution: exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	recorded := decode(t, out)
	recordedRefs := referenceRuns(recorded)
	if len(recordedRefs) != 2 {
		t.Fatalf("recorded execution has reference runs %v", recordedRefs)
	}
	stop()

	// Restart on the same state.
	runtime, _ := startRealRuntimeAt(t, binaries.local, stateDir)

	t.Run("last reuses the recorded reference and runs only the candidates", func(t *testing.T) {
		code, out, stderr := runEvalRunForReal(t, binaries, runtime.apiURL, home, workload,
			scenarioFor(script, shScript, "3"), "--reference", "last")
		if code != exitGateFail {
			t.Fatalf("exit %d, want 1 (export_customer is new against the reused reference)\n"+
				"stdout: %s\nstderr: %s", code, out, stderr)
		}
		doc := decode(t, out)
		if doc.Reference == nil || doc.Reference.Mode != "last" ||
			doc.Reference.ExecutionID != recorded.ExecutionID || doc.ExecutionID == recorded.ExecutionID {
			t.Errorf("identity %q, reference %+v; want a new execution reusing %s",
				doc.ExecutionID, doc.Reference, recorded.ExecutionID)
		}
		if got := referenceRuns(doc); !slices.Equal(got, recordedRefs) {
			t.Errorf("reference runs %v, want the recorded %v", got, recordedRefs)
		}
		if n := strings.Count(stderr, "trustvian eval run: reference repetition"); n != 0 {
			t.Errorf("%d reference workloads ran with --reference; want none", n)
		}
		if n := strings.Count(stderr, "trustvian eval run: candidate repetition"); n != 2 {
			t.Errorf("%d candidate workloads ran; want exactly 2", n)
		}
	})

	t.Run("an explicit id reuses the same reference", func(t *testing.T) {
		code, out, stderr := runEvalRunForReal(t, binaries, runtime.apiURL, home, workload,
			scenarioFor(script, shScript, "2"), "--reference", recorded.ExecutionID)
		if code != exitOK {
			t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, out, stderr)
		}
		doc := decode(t, out)
		if doc.Reference == nil || doc.Reference.Mode != "execution" ||
			!slices.Equal(referenceRuns(doc), recordedRefs) {
			t.Errorf("reference %+v, runs %v", doc.Reference, referenceRuns(doc))
		}
	})

	t.Run("a missing reference stops before any workload", func(t *testing.T) {
		code, out, stderr := runEvalRunForReal(t, binaries, runtime.apiURL, home, workload,
			scenarioFor(script, shScript, "2"), "--reference", "scn-no-such-execution")
		if code != exitOperational {
			t.Fatalf("exit %d, want 3\nstdout: %s\nstderr: %s", code, out, stderr)
		}
		if strings.Contains(stderr, "trustvian eval run: candidate repetition") ||
			strings.Contains(stderr, workloadStdoutMarker) {
			t.Errorf("a workload ran against a missing reference:\n%s", stderr)
		}
	})
}
