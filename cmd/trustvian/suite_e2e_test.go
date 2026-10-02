//go:build !windows

package main

// trustvian eval run --suite against the real binaries (task 078), model-free:
// the deterministic agent-producer, the real Collector and the real control
// plane. One suite mixes a PASS, a gate FAIL, a crashing scenario and a hung
// one whose descendant ignores SIGTERM; a second suite reuses recorded
// references with --reference last.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func runSuiteForReal(t *testing.T, binaries realBinaries, apiURL, home string,
	workload *e2eFixture, dir string, extra ...string) (int, suiteDocument, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(devBinary(t), append([]string{"eval", "run", "--suite", dir,
		"--api-url", apiURL, "--collector-bin", binaries.collector, "--json"}, extra...)...)
	cmd.Dir = workload.dir
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running the suite: %v", err)
	}
	var doc suiteDocument
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := dec.Decode(&doc); err != nil || dec.More() {
		t.Fatalf("stdout is not one suite document (%v):\n%s\nstderr:\n%s", err, stdout.Bytes(), stderr.String())
	}
	return code, doc, stderr.String()
}

func namedScenario(name, body string) string {
	return strings.Replace(body, "name: e2e-scenario", "name: "+name, 1)
}

func TestSuiteAgainstRealBinaries(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()
	script := writeScenarioWorkload(t, binaries)
	shScript := fmt.Sprintf("sh, %q", script)

	// A hung reference: it starts a descendant that ignores SIGTERM, then
	// loops forever. Only the deadline ends it.
	work := t.TempDir()
	pidFile := filepath.Join(work, "descendant.pid")
	hang := filepath.Join(work, "hang.sh")
	if err := os.WriteFile(hang, []byte(`#!/bin/sh
sh -c 'trap "" TERM; printf $$ > "$1"; while :; do sleep 0.1; done' _ "$1" &
while :; do sleep 0.1; done
`), 0o700); err != nil {
		t.Fatal(err)
	}
	crash := strings.Replace(scenarioFor(script, shScript, "2"),
		fmt.Sprintf("candidate:\n  command: [%s]", shScript), `candidate:
  command: [sh, -c, "exit 5"]`, 1)
	hung := strings.Replace(scenarioFor(script, shScript, "2"),
		fmt.Sprintf("reference:\n  command: [sh, %q]", script),
		fmt.Sprintf("reference:\n  command: [sh, %q, %q]", hang, pidFile), 1)
	if crash == scenarioFor(script, shScript, "2") || hung == scenarioFor(script, shScript, "2") {
		t.Fatal("fixture: a scenario edit did not apply")
	}

	dir := t.TempDir()
	for name, body := range map[string]string{
		"a-pass.yaml":  namedScenario("alpha", scenarioFor(script, shScript, "2")),
		"b-fail.yaml":  namedScenario("bravo", scenarioFor(script, shScript, "3")),
		"c-crash.yaml": namedScenario("charlie", crash),
		"d-hang.yaml":  namedScenario("delta", hung),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code, doc, stderr := runSuiteForReal(t, binaries, runtime.apiURL, home, workload, dir,
		"--scenario-timeout", "8s")
	var got []string
	for _, m := range doc.Members {
		label := m.Outcome
		if m.Error != nil {
			label += "/" + m.Error.Code
		}
		got = append(got, m.File+":"+label)
	}
	want := []string{"a-pass.yaml:pass", "b-fail.yaml:fail", "c-crash.yaml:error/repetition_failed",
		"d-hang.yaml:error/scenario_timeout"}
	if !slices.Equal(got, want) {
		t.Fatalf("members %v, want %v\nstderr:\n%s", got, want, stderr)
	}
	if code != exitOperational || !doc.Complete || doc.ExitCode != exitOperational {
		t.Errorf("exit %d (document %d, complete %v); want 3", code, doc.ExitCode, doc.Complete)
	}
	if strings.Contains(fmt.Sprint(doc), workloadStdoutMarker) {
		t.Error("workload output reached the suite document")
	}
	if n := strings.Count(stderr, workloadStdoutMarker); n == 0 {
		t.Error("workload output did not reach stderr")
	}

	// The hung scenario's execution is recorded failed, and its descendant
	// did not survive the deadline.
	hungExecution := doc.Members[3].ExecutionID
	response, err := http.Get(runtime.apiURL + "/v1/scenario-executions/" + hungExecution)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Execution struct {
			Status string `json:"status"`
		} `json:"execution"`
	}
	_ = json.NewDecoder(response.Body).Decode(&stored)
	response.Body.Close()
	if stored.Execution.Status != "failed" {
		t.Errorf("hung execution %s is %q, want failed", hungExecution, stored.Execution.Status)
	}
	pid := readPID(t, pidFile)
	limit := time.Now().Add(15 * time.Second)
	for processAlive(pid) && time.Now().Before(limit) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the hung scenario's descendant %d survived its deadline", pid)
	}

	// Reuse: --reference last for each scenario that completed, candidates only.
	reuse := t.TempDir()
	for name, body := range map[string]string{
		"a.yaml": namedScenario("alpha", scenarioFor(script, shScript, "3")),
		"b.yaml": namedScenario("bravo", scenarioFor(script, shScript, "2")),
	} {
		if err := os.WriteFile(filepath.Join(reuse, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, doc, stderr = runSuiteForReal(t, binaries, runtime.apiURL, home, workload, reuse,
		"--scenario-timeout", "60s", "--reference", "last")
	if code != exitGateFail || len(doc.Members) != 2 ||
		doc.Members[0].Outcome != outcomeFail || doc.Members[1].Outcome != outcomePass {
		t.Fatalf("reuse suite: exit %d, members %+v\nstderr:\n%s", code, doc.Members, stderr)
	}
	if n := strings.Count(stderr, "trustvian eval run: reference repetition"); n != 0 {
		t.Errorf("%d reference workloads ran with --reference last", n)
	}
	if n := strings.Count(stderr, "trustvian eval run: candidate repetition"); n != 4 {
		t.Errorf("%d candidate workloads ran, want 2 per scenario", n)
	}
	for _, m := range doc.Members {
		var result struct {
			Reference *referenceProvenance `json:"reference"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil || result.Reference == nil ||
			result.Reference.Mode != "last" {
			t.Errorf("%s did not reuse a recorded reference: %s", m.File, m.Result)
		}
	}
}
