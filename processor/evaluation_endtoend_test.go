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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	t *testing.T, apiURL, runID, profile string, required *bool,
) *trustvianprocessor.Config {
	t.Helper()
	return &trustvianprocessor.Config{
		Evaluation: &trustvianprocessor.EvaluationConfig{
			APIURL:            apiURL,
			RunID:             runID,
			BehavioralProfile: profile,
			Required:          required,
			PendingStatePath:  filepath.Join(t.TempDir(), "pending.json"),
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
	// A run may only name an environment its project owns (task 065).
	postJSON(t, apiURL, "/v1/environments", map[string]string{
		"project_id": "proj-e2e", "ref": environment, "name": "Local"})
	postJSON(t, apiURL, "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "cand-e2e",
		"environment": environment, "behavioral_profile": profile})
	postJSON(t, apiURL, "/v1/evaluation-runs/"+runID+"/start", nil)

	required := true
	cfg := evaluationConfigFor(t, apiURL, runID, profile, &required)

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
	postJSON(t, apiURL, "/v1/environments", map[string]string{
		"project_id": "p", "ref": "local", "name": "Local"})
	postJSON(t, apiURL, "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "c",
		"environment": "local", "behavioral_profile": profile})
	postJSON(t, apiURL, "/v1/evaluation-runs/"+runID+"/start", nil)

	required := true
	consume := func() {
		cfg := evaluationConfigFor(t, apiURL, runID, profile, &required)
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

// lossyProxy forwards /v1 to the real control plane and destroys the
// response to the first POST it is told to lose — after the upstream has
// already answered, so the record is durably committed and only the reply is
// gone.
//
// This is the one seam that cannot be faked. Every other test in this
// repository asserts the reconciliation against a stub that implements the
// replay rule as this module understands it; this one asserts it against the
// control plane's own RecordDigest, which is the only authority on whether a
// re-presented record is recognized as the same one.
type lossyProxy struct {
	*httptest.Server
	upstream string
	lose     atomic.Bool

	// loseAll destroys every record response rather than the next one —
	// what a restart test needs, since the sink's own in-call
	// reconciliation would otherwise settle the record before the process
	// could be discarded with it pending.
	loseAll atomic.Bool
	lost    atomic.Int64
}

func newLossyProxy(t *testing.T, upstream string) *lossyProxy {
	t.Helper()
	p := &lossyProxy{upstream: upstream}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		forward, err := http.NewRequestWithContext(
			r.Context(), r.Method, p.upstream+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		forward.Header = r.Header.Clone()
		response, err := http.DefaultClient.Do(forward)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/records") &&
			(p.loseAll.Load() || p.lose.CompareAndSwap(true, false)) {
			// The upstream has committed and answered. Destroying the
			// connection here is precisely the failure the Collector cannot
			// distinguish from a record that never arrived.
			p.lost.Add(1)
			hijackAndClose(w)
			return
		}

		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(p.Close)
	return p
}

// TestEndToEndLostResponseIsReplayedUpstream drives the blocker's
// exact scenario through the real chain: the record commits at sequence 1,
// its response is destroyed, the sink re-presents the same record at the
// same sequence, and the control plane's own digest rule replays it.
//
// The assertions that matter are the run's own counters. record_count must
// be 2 for two spans, never 3 — ADR 0026 makes a duplicate record count
// twice on purpose, so an evidence inflation would be visible here and
// nowhere else.
func TestEndToEndLostResponseIsReplayedUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test builds and runs the platform runtime")
	}

	apiURL := startLocalRuntime(t)
	const runID, profile = "run-lost-response", "support-lost-response"

	postJSON(t, apiURL, "/v1/projects", map[string]string{"id": "p", "name": "P"})
	postJSON(t, apiURL, "/v1/agents", map[string]string{
		"id": "a", "project_id": "p", "name": "A"})
	postJSON(t, apiURL, "/v1/candidates", map[string]any{
		"id": "c", "agent_id": "a", "metadata": map[string]string{}})
	postJSON(t, apiURL, "/v1/environments", map[string]string{
		"project_id": "p", "ref": "local", "name": "Local"})
	postJSON(t, apiURL, "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "c",
		"environment": "local", "behavioral_profile": profile})
	postJSON(t, apiURL, "/v1/evaluation-runs/"+runID+"/start", nil)

	proxy := newLossyProxy(t, apiURL)
	required := true
	cfg := evaluationConfigFor(t, proxy.URL, runID, profile, &required)

	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cfg)
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}

	// Lose the first record's response, after the control plane commits it.
	proxy.lose.Store(true)
	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v; the control plane's replay rule must resolve this", err)
	}
	if proxy.lost.Load() != 1 {
		t.Fatal("the proxy never destroyed a response; this test proved nothing")
	}

	// A second, different span must take sequence 2 and land normally.
	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "knowledge.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v; sequence 2 must be free and uncontested", err)
	}

	progress := readProgress(t, apiURL, runID)
	if progress.RecordCount != "2" {
		t.Errorf("record_count = %q, want 2 — a replay must not add a second record", progress.RecordCount)
	}
	if progress.DistinctBehaviorCount != 2 {
		t.Errorf("distinct_behavior_count = %d, want 2", progress.DistinctBehaviorCount)
	}
	if progress.NextIngestSequence != "3" {
		t.Errorf("next_ingest_sequence = %q, want 3", progress.NextIngestSequence)
	}
}

// TestEndToEndRestartCompletesAPendingRecord drives the restart path through
// the real chain: the control plane commits the record, every reply is
// destroyed, the processor is discarded with the record unsettled, and a
// second one starts over the same pending state.
//
// The stub servers elsewhere implement the replay rule as this module
// understands it. This asserts it against the control plane's own
// RecordDigest — the only authority on whether a re-presented record is
// recognized as the same one — using the run's own counters, where a second
// copy would show up as record_count 3 rather than 2.
func TestEndToEndRestartCompletesAPendingRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test builds and runs the platform runtime")
	}

	apiURL := startLocalRuntime(t)
	const runID, profile = "run-restart", "support-restart"

	postJSON(t, apiURL, "/v1/projects", map[string]string{"id": "p", "name": "P"})
	postJSON(t, apiURL, "/v1/agents", map[string]string{
		"id": "a", "project_id": "p", "name": "A"})
	postJSON(t, apiURL, "/v1/candidates", map[string]any{
		"id": "c", "agent_id": "a", "metadata": map[string]string{}})
	postJSON(t, apiURL, "/v1/environments", map[string]string{
		"project_id": "p", "ref": "local", "name": "Local"})
	postJSON(t, apiURL, "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "c",
		"environment": "local", "behavioral_profile": profile})
	postJSON(t, apiURL, "/v1/evaluation-runs/"+runID+"/start", nil)

	// One pending state file, inherited by the second processor exactly as a
	// restarted container inherits its volume.
	pending := filepath.Join(t.TempDir(), "pending.json")
	required := true
	withPending := func(apiURL string) *trustvianprocessor.Config {
		cfg := evaluationConfigFor(t, apiURL, runID, profile, &required)
		cfg.Evaluation.PendingStatePath = pending
		return cfg
	}

	proxy := newLossyProxy(t, apiURL)
	proxy.loseAll.Store(true)

	first, err := newTestProcessorWithConfig(t, consumertest.NewNop(), withPending(proxy.URL))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := first.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err == nil {
		t.Fatal("ConsumeTraces() error = nil, want the unresolved ingest surfaced")
	}
	if proxy.lost.Load() == 0 {
		t.Fatal("the proxy never destroyed a response; this test proved nothing")
	}

	progress := readProgress(t, apiURL, runID)
	if progress.RecordCount != "1" || progress.NextIngestSequence != "2" {
		t.Fatalf("progress = %+v, want one record and next sequence 2 — the control plane committed "+
			"before the reply was destroyed", progress)
	}

	// The first processor is discarded with that record unsettled. The
	// second talks to the control plane directly.
	second, err := newTestProcessorWithConfig(t, consumertest.NewNop(), withPending(apiURL))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if got := readProgress(t, apiURL, runID); got.RecordCount != "1" || got.NextIngestSequence != "2" {
		t.Errorf("progress after recovery = %+v, want one record and next sequence 2 — "+
			"re-presenting the record must replay, not add a second copy", got)
	}

	// And the run continues from the server's cursor.
	if err := second.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "knowledge.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	progress = readProgress(t, apiURL, runID)
	if progress.RecordCount != "2" {
		t.Errorf("record_count = %q, want 2", progress.RecordCount)
	}
	if progress.NextIngestSequence != "3" {
		t.Errorf("next_ingest_sequence = %q, want 3", progress.NextIngestSequence)
	}
	if progress.DistinctBehaviorCount != 2 {
		t.Errorf("distinct_behavior_count = %d, want 2", progress.DistinctBehaviorCount)
	}
}
