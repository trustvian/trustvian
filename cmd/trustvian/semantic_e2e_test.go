//go:build !windows

package main

// Task 075 end to end, against the real binaries.
//
// Every other test of the semantic mapping works on a span this repository built:
// internal/semconv's tests use plain maps, and both adapters' tests construct
// spans directly. None of them proves that a *producer* using the OpenTelemetry
// SDK, exporting over OTLP, through a real trustvian-collector, into a real
// control plane, yields behaviors named by tool.
//
// That is what the acceptance criteria are about, and it is the one thing those
// tests structurally cannot show. Two halves, both required:
//
//	criterion 1   a producer emitting a supported convention yields behavior
//	              naming the tool rather than the transport
//	criterion 2   a producer emitting none sees **no change whatsoever**
//
// The second is why processor/cmd/agent-producer has a transport mode. A control
// that only ran the new shape would prove the feature works and say nothing about
// whether it broke the old one.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// semanticSpanCount is how many spans agent-producer emits per run.
//
// Six: two passes over its three-tool cycle, so every tool appears more than once
// and a per-behavior count of 2 is distinguishable from a count of 1. Small,
// because the assertion is about names rather than volume.
const semanticSpanCount = 6

// The content agent-producer deliberately emits. Kept in sync with its own
// constants by TestAgentProducerEmitsTheCanariesThisTestLooksFor below, because a
// privacy assertion against a canary the producer stopped sending would pass while
// proving nothing.
var semanticContentCanaries = []string{
	"CANARY-PROMPT-8fa3-do-not-propagate",
	"CANARY-COMPLETION-8fa3-do-not-propagate",
	"CANARY-TOOL-ARGUMENT-8fa3-do-not-propagate",
}

// runAgentProducerUnderDev performs one evaluation run of the fixture producer.
//
// Through `trustvian dev --instrumentation existing`, which is the mode a workload
// that already sends OpenTelemetry uses — and what this producer is. dev injects
// nothing; the producer's own SDK does the exporting.
func runAgentProducerUnderDev(t *testing.T, binaries realBinaries, apiURL, home string,
	workload *e2eFixture, mode, candidate, runID string) {
	t.Helper()

	// The producer reads its endpoint from its own variable and speaks gRPC, so a
	// small script maps dev's advertised gRPC endpoint onto it — the same shape
	// the existing journey test uses, and the case a workload configuring its
	// exporter in code presents.
	script := filepath.Join(t.TempDir(), "workload.sh")
	body := fmt.Sprintf(`#!/bin/sh
OTLP_ENDPOINT="$TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT" \
MODE=%s \
SPAN_COUNT=%d \
SERVICE_NAME=%s \
ENVIRONMENT=%s \
exec %q
`, mode, semanticSpanCount, e2eServiceName, devEnvironmentDefault, binaries.agentProducer)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing the workload script: %v", err)
	}

	stdout := filepath.Join(t.TempDir(), "dev-"+mode+".out")
	outFile, err := os.Create(stdout)
	if err != nil {
		t.Fatalf("creating the output file: %v", err)
	}
	defer outFile.Close()

	cmd := exec.Command(devBinary(t),
		"dev",
		"--api-url", apiURL,
		"--candidate", candidate,
		"--run-id", runID,
		"--instrumentation", "existing",
		"--collector-bin", binaries.collector,
		"--", "sh", script)
	cmd.Dir = workload.dir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		envServiceName+"="+e2eServiceName,
	)
	cmd.Stdout = outFile
	cmd.Stderr = outFile

	if err := cmd.Run(); err != nil {
		t.Fatalf("trustvian dev (%s) failed: %v\n%s", mode, err, readWholeFile(t, stdout))
	}
}

// behaviorsOf reads one run's behavior set back from the control plane.
//
// By comparing the run against itself, which is the only route /v1 offers today:
// progress returns counts, and POST /v1/evaluations/compare is the one route that
// returns behaviors at all. Task 078's first slice proposes a run-scoped route
// that would replace this.
func behaviorsOf(t *testing.T, apiURL, runID string) map[string]semanticBehavior {
	t.Helper()

	const unlimited = "18446744073709551615" // math.MaxUint64, as /v1 spells uint64
	body, err := json.Marshal(map[string]any{
		"reference_run_id": runID,
		"candidate_run_id": runID,
		"gate_limits": map[string]any{
			"max_added_behaviors":            unlimited,
			"max_block_decisions":            unlimited,
			"max_critical_risk_observations": unlimited,
		},
	})
	if err != nil {
		t.Fatalf("encoding the comparison: %v", err)
	}

	response, err := http.Post(apiURL+"/v1/evaluations/compare",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST compare: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("reading the comparison: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("compare answered %d: %s", response.StatusCode, payload)
	}

	// The Go field is named Diff, not after the control-plane type: the CLI must
	// consume a response rather than reproduce the platform's vocabulary, and
	// cli_architecture_test.go enforces that on this package's sources.
	var decoded struct {
		Diff struct {
			Deltas []struct {
				Behavior struct {
					OperationCategory string `json:"operation_category"`
					OperationName     string `json:"operation_name"`
					TargetName        string `json:"target_name"`
					ActorType         string `json:"actor_type"`
				} `json:"behavior"`
				ReferenceObservations string `json:"reference_observations"`
			} `json:"deltas"`
		} `json:"behavior_diff"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decoding the comparison: %v\n%s", err, payload)
	}

	out := make(map[string]semanticBehavior, len(decoded.Diff.Deltas))
	for _, delta := range decoded.Diff.Deltas {
		count, _ := strconv.Atoi(delta.ReferenceObservations)
		key := delta.Behavior.OperationCategory + " · " + delta.Behavior.OperationName
		out[key] = semanticBehavior{
			Category:     delta.Behavior.OperationCategory,
			Name:         delta.Behavior.OperationName,
			Target:       delta.Behavior.TargetName,
			ActorType:    delta.Behavior.ActorType,
			Observations: count,
		}
	}
	return out
}

type semanticBehavior struct {
	Category     string
	Name         string
	Target       string
	ActorType    string
	Observations int
}

// TestSemanticTelemetryYieldsBehaviorsNamedByTool is acceptance criterion 1.
func TestSemanticTelemetryYieldsBehaviorsNamedByTool(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()

	runAgentProducerUnderDev(t, binaries, runtime.apiURL, home, workload,
		"semantic", "semantic-candidate", "semantic-run")

	progress := runtime.progress(t, "semantic-run")
	if progress.Status != "completed" {
		t.Fatalf("run status = %q, want completed", progress.Status)
	}
	if progress.RecordCount != strconv.Itoa(semanticSpanCount) {
		t.Fatalf("run holds %s records, want %d", progress.RecordCount, semanticSpanCount)
	}

	behaviors := behaviorsOf(t, runtime.apiURL, "semantic-run")

	// The three tools the producer used, named as tools. This is the whole task:
	// the span name was "POST /v1/tools" on every one of them.
	for _, tool := range []string{"crm_lookup", "knowledge_search", "export_customer"} {
		key := "tool · " + tool
		got, ok := behaviors[key]
		if !ok {
			t.Errorf("no behavior %q; got %v", key, keysOf(behaviors))
			continue
		}
		// The transport target survives beside the semantic name, which is the
		// richest row the mapping produces.
		if got.Target != "tools.localhost" {
			t.Errorf("%s target = %q, want tools.localhost", key, got.Target)
		}
		// The producer named its agent, so the actor was established.
		if got.ActorType != "ai_agent" {
			t.Errorf("%s actor_type = %q, want ai_agent", key, got.ActorType)
		}
		if got.Observations != semanticSpanCount/3 {
			t.Errorf("%s observed %d times, want %d",
				key, got.Observations, semanticSpanCount/3)
		}
	}

	// And nothing named by transport: a behavior called "POST /v1/tools" would
	// mean the convention was not read on some span.
	for key := range behaviors {
		if strings.Contains(key, "POST") {
			t.Errorf("a transport-named behavior survived: %q", key)
		}
	}
}

// TestTransportTelemetryIsUnchanged is acceptance criterion 2 — the half that
// matters most, because it is about not having broken anything.
func TestTransportTelemetryIsUnchanged(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()

	runAgentProducerUnderDev(t, binaries, runtime.apiURL, home, workload,
		"transport", "transport-candidate", "transport-run")

	progress := runtime.progress(t, "transport-run")
	if progress.RecordCount != strconv.Itoa(semanticSpanCount) {
		t.Fatalf("run holds %s records, want %d", progress.RecordCount, semanticSpanCount)
	}

	behaviors := behaviorsOf(t, runtime.apiURL, "transport-run")

	// One behavior, not three: every span is the same POST to the same host,
	// which is exactly the flattening task 075 exists to relieve — and exactly
	// what must still happen when no convention is emitted.
	if len(behaviors) != 1 {
		t.Fatalf("transport telemetry produced %d behaviors, want 1: %v",
			len(behaviors), keysOf(behaviors))
	}

	got, ok := behaviors["http · POST /v1/tools"]
	if !ok {
		t.Fatalf("the behavior is not named by transport: %v", keysOf(behaviors))
	}
	if got.Target != "tools.localhost" {
		t.Errorf("target = %q, want tools.localhost", got.Target)
	}
	// The actor was NOT upgraded: no agent identity was emitted, so a plain
	// service stays a service. This is the assertion that fails if anyone makes
	// the actor-type upgrade fire on a convention's mere presence.
	if got.ActorType != "service" {
		t.Errorf("actor_type = %q, want service — nothing established an agent identity",
			got.ActorType)
	}
	if got.Observations != semanticSpanCount {
		t.Errorf("observed %d times, want %d", got.Observations, semanticSpanCount)
	}
}

// TestSemanticPathLeaksNoContentEndToEnd is the privacy claim against a real
// pipeline.
//
// The unit and integration tests assert it against spans this repository built.
// This asserts it against telemetry that travelled OTLP, through a Collector, into
// a control plane — the path where a leak would actually happen.
func TestSemanticPathLeaksNoContentEndToEnd(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()

	runAgentProducerUnderDev(t, binaries, runtime.apiURL, home, workload,
		"semantic", "privacy-candidate", "privacy-run")

	bodies := map[string]string{
		"progress":   rawBody(t, runtime.apiURL+"/v1/evaluation-runs/privacy-run/progress"),
		"the run":    rawBody(t, runtime.apiURL+"/v1/evaluation-runs/privacy-run"),
		"comparison": fmt.Sprintf("%v", behaviorsOf(t, runtime.apiURL, "privacy-run")),
	}

	for _, canary := range semanticContentCanaries {
		for name, body := range bodies {
			if strings.Contains(body, canary) {
				t.Errorf("content reached %s: %s", name, canary)
			}
		}
	}

	// And the behavior survived, so absence above is not absence of everything.
	if !strings.Contains(bodies["comparison"], "export_customer") {
		t.Errorf("the comparison lost the tool name:\n%s", bodies["comparison"])
	}
}

// TestAgentProducerEmitsTheCanariesThisTestLooksFor keeps the fixture and the
// assertion honest.
//
// A privacy test whose canary the producer stopped sending passes while proving
// nothing — the same failure mode as the PolicyReason probe in slice 3. This reads
// the producer's source and requires every canary asserted above to appear in it.
func TestAgentProducerEmitsTheCanariesThisTestLooksFor(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(root, "processor", "cmd", "agent-producer", "main.go"))
	if err != nil {
		t.Fatalf("reading the producer: %v", err)
	}
	for _, canary := range semanticContentCanaries {
		if !strings.Contains(string(source), canary) {
			t.Errorf("agent-producer no longer emits %q, so the privacy assertion "+
				"that looks for it proves nothing", canary)
		}
	}
}

// rawBody fetches one response as text.
//
// Raw rather than decoded, because the question is whether a canary appears
// anywhere in the bytes — a structured read would only look where somebody
// thought to.
func rawBody(t *testing.T, url string) string {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return string(payload)
}

func keysOf(m map[string]semanticBehavior) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
