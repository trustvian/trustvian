package trustvianprocessor_test

// The real chain, with nothing behavioral mocked:
//
//	real ptrace.Span → real trustvian processor → real Engine.Analyze
//	  → Result.DecisionRecord() → real loopback HTTP control plane
//	  → SQLite → evaluation progress
//
// It reaches a real control plane without importing the platform, because a
// test dependency reverses the module edge exactly as much as a shipped one —
// go.mod does not distinguish them. So the platform is built and run as a
// subprocess and driven over /v1: exec and JSON, never a Go import. That is
// the same boundary ADR 0035 keeps real locally, used here as a test seam.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"

	trustvianprocessor "trustvian-processor"
)

// platformModuleDir is the sibling module inside this repository.
const platformModuleDir = "../platform"

// waitFor polls until condition returns true, failing the test at the
// deadline. No sleep is a sequencing primitive anywhere in this file; each
// wait names the observable condition it is waiting for.
func waitFor(t *testing.T, what string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// startLocalRuntime builds and runs platform/cmd/trustvian-local, returning
// its discovered API URL.
func startLocalRuntime(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; the end-to-end test builds the platform runtime")
	}
	if _, err := os.Stat(filepath.Join(platformModuleDir, "go.mod")); err != nil {
		t.Skipf("platform module not present at %s: %v", platformModuleDir, err)
	}

	binary := filepath.Join(t.TempDir(), "trustvian-local")
	build := exec.Command("go", "build", "-o", binary, "./cmd/trustvian-local")
	build.Dir = platformModuleDir
	// GOWORK=off for the same reason CI uses it: a workspace resolves
	// dependencies the module does not declare.
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building trustvian-local: %v\n%s", err, out)
	}

	stateDir := filepath.Join(t.TempDir(), ".trustvian")
	runtime := exec.Command(binary, "--state-dir", stateDir)
	var runtimeOutput bytes.Buffer
	runtime.Stdout = &runtimeOutput
	runtime.Stderr = &runtimeOutput
	if err := runtime.Start(); err != nil {
		t.Fatalf("starting trustvian-local: %v", err)
	}
	t.Cleanup(func() {
		_ = runtime.Process.Kill()
		_, _ = runtime.Process.Wait()
		if t.Failed() {
			t.Logf("trustvian-local output:\n%s", runtimeOutput.String())
		}
	})

	// The endpoint is published only after the listener binds, which is what
	// makes the file itself the readiness signal.
	discovery := filepath.Join(stateDir, "runtime.json")
	var apiURL string
	waitFor(t, "the local runtime to publish "+discovery, 90*time.Second, func() bool {
		payload, err := os.ReadFile(discovery)
		if err != nil {
			return false
		}
		var published struct {
			Version string `json:"version"`
			APIURL  string `json:"api_url"`
		}
		if err := json.Unmarshal(payload, &published); err != nil {
			return false
		}
		if published.Version != "1" || published.APIURL == "" {
			return false
		}
		apiURL = published.APIURL
		return true
	})
	return apiURL
}

// postJSON issues one /v1 request and fails the test on a non-2xx response.
func postJSON(t *testing.T, apiURL, path string, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encoding %s: %v", path, err)
	}
	response, err := http.Post(apiURL+path, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// io.ReadAll, not a single fixed-size Read: a short read (common on
		// a chunked or slow response) would silently truncate the one
		// diagnostic this failure has, in the hardest test in this repo to
		// debug from CI output alone.
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("POST %s = HTTP %d: %s", path, response.StatusCode, body)
	}
}

type progressBody struct {
	Status                string `json:"status"`
	RecordCount           string `json:"record_count"`
	DistinctBehaviorCount int    `json:"distinct_behavior_count"`
	NextIngestSequence    string `json:"next_ingest_sequence"`
}

func readProgress(t *testing.T, apiURL, runID string) progressBody {
	t.Helper()
	response, err := http.Get(apiURL + "/v1/evaluation-runs/" + runID + "/progress")
	if err != nil {
		t.Fatalf("GET progress: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET progress = HTTP %d", response.StatusCode)
	}
	var body progressBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decoding progress: %v", err)
	}
	return body
}

// evaluationConfigFor builds the config these tests share. A function rather
// than a literal, so a field added later cannot be set in one test and
// forgotten in the other. It returns a pointer because component.Config is
// an interface and CreateTraces expects *Config.
func evaluationConfigFor(
	apiURL, runID, profile string, required *bool,
) *trustvianprocessor.Config {
	return &trustvianprocessor.Config{
		Evaluation: &trustvianprocessor.EvaluationConfig{
			APIURL:            apiURL,
			RunID:             runID,
			BehavioralProfile: profile,
			Required:          required,
		},
	}
}

func TestEndToEndSpanReachesEvaluationProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test builds and runs the platform runtime")
	}

	apiURL := startLocalRuntime(t)

	const (
		runID       = "run-e2e"
		profile     = "support-e2e"
		environment = "local"
	)

	postJSON(t, apiURL, "/v1/projects", map[string]string{
		"id": "proj-e2e", "name": "End to end"})
	postJSON(t, apiURL, "/v1/agents", map[string]string{
		"id": "agent-e2e", "project_id": "proj-e2e", "name": "Support agent"})
	postJSON(t, apiURL, "/v1/candidates", map[string]any{
		"id": "cand-e2e", "agent_id": "agent-e2e", "metadata": map[string]string{}})
	postJSON(t, apiURL, "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "cand-e2e",
		"environment": environment, "behavioral_profile": profile})
	postJSON(t, apiURL, "/v1/evaluation-runs/"+runID+"/start", nil)

	required := true
	cfg := evaluationConfigFor(apiURL, runID, profile, &required)

	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cfg)
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}

	// Three distinct targets, so the run accumulates three distinct
	// behaviors — the evidence a later comparison would diff.
	for _, target := range []string{"crm.localhost", "knowledge.localhost", "mail.localhost"} {
		if err := proc.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", target)); err != nil {
			t.Fatalf("ConsumeTraces(%s) error = %v", target, err)
		}
	}

	progress := readProgress(t, apiURL, runID)
	if progress.Status != "running" {
		t.Errorf("status = %q, want running", progress.Status)
	}
	if progress.RecordCount != "3" {
		t.Errorf("record_count = %q, want 3", progress.RecordCount)
	}
	if progress.DistinctBehaviorCount != 3 {
		t.Errorf("distinct_behavior_count = %d, want 3", progress.DistinctBehaviorCount)
	}
	if progress.NextIngestSequence != "4" {
		t.Errorf("next_ingest_sequence = %q, want 4", progress.NextIngestSequence)
	}

	postJSON(t, apiURL, "/v1/evaluation-runs/"+runID+"/complete", nil)
	if got := readProgress(t, apiURL, runID); got.Status != "completed" {
		t.Errorf("status after complete = %q, want completed", got.Status)
	}
}

// TestEndToEndResumesFromTheServerCursor proves a restarted Collector
// continues a running evaluation instead of restarting the count.
func TestEndToEndResumesFromTheServerCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test builds and runs the platform runtime")
	}

	apiURL := startLocalRuntime(t)
	const runID, profile = "run-resume", "support-resume"

	postJSON(t, apiURL, "/v1/projects", map[string]string{"id": "p", "name": "P"})
	postJSON(t, apiURL, "/v1/agents", map[string]string{
		"id": "a", "project_id": "p", "name": "A"})
	postJSON(t, apiURL, "/v1/candidates", map[string]any{
		"id": "c", "agent_id": "a", "metadata": map[string]string{}})
	postJSON(t, apiURL, "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "c",
		"environment": "local", "behavioral_profile": profile})
	postJSON(t, apiURL, "/v1/evaluation-runs/"+runID+"/start", nil)

	required := true
	consume := func() {
		cfg := evaluationConfigFor(apiURL, runID, profile, &required)
		proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cfg)
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		if err := proc.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", "crm.localhost")); err != nil {
			t.Fatalf("ConsumeTraces() error = %v", err)
		}
	}

	consume()
	consume() // a second processor, as a restarted Collector would be

	progress := readProgress(t, apiURL, runID)
	if progress.RecordCount != "2" {
		t.Errorf("record_count = %q, want 2; the second Collector restarted the count", progress.RecordCount)
	}
	if progress.NextIngestSequence != "3" {
		t.Errorf("next_ingest_sequence = %q, want 3", progress.NextIngestSequence)
	}
}
