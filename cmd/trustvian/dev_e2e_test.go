//go:build !windows

package main

// The task 077 journey, end to end, against the real binaries.
//
// Every other dev test uses a fake: a script that publishes a discovery file, the
// test binary pretending to be a Collector, an httptest server answering /v1.
// Those prove dev's supervision, which is what they are for. None of them proves
// the *composition* — that the configuration dev generates is one the real
// Collector accepts, that the run it provisions is one the real control plane
// accepts evidence for, and that evidence actually arrives.
//
// So this test builds trustvian-local, trustvian-collector and a producer from
// the sibling modules and runs the whole thing. It is slow and it is the only
// test that can fail for the reasons that matter most.
//
// Four things are proven, in order, because each depends on the one before:
//
//  1. records land in the run, including the span exported last
//  2. the printed Web URL serves that run with no identifier typed (task 074)
//  3. a second run of one candidate meets a learned baseline
//  4. the workload's repository is byte-identical afterwards

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// e2eSpanCount is how many spans the producer emits per run.
//
// Small, because the assertion is "every one of them arrived" rather than
// anything about volume — and the last of them is the span exported immediately
// before the workload exits, which is the ordering this test exists to check.
const e2eSpanCount = 5

// e2eUnlimited is the largest gate limit the API accepts, as a string.
//
// The limits cross /v1 as strings like every other uint64, and the handler
// rejects a comparison whose limits are exceeded — so this test, which reads the
// comparison only for its metric summaries, asks for the widest one there is.
const e2eUnlimited = "18446744073709551615" // math.MaxUint64

// realBinaries are the three executables the journey needs.
type realBinaries struct {
	local     string
	collector string
	producer  string
}

var (
	buildOnce sync.Once
	built     realBinaries
	buildErr  error
)

// buildRealBinaries compiles the helpers once per test run.
//
// From the sibling modules with GOWORK=off, exactly as `make dev-binaries` does,
// so what this test runs is what a developer following the documentation runs.
func buildRealBinaries(t *testing.T) realBinaries {
	t.Helper()
	buildOnce.Do(func() {
		goTool, err := exec.LookPath("go")
		if err != nil {
			buildErr = fmt.Errorf("no Go toolchain: %w", err)
			return
		}
		root, err := repositoryRoot()
		if err != nil {
			buildErr = err
			return
		}
		// Not t.TempDir(): this is shared by every subtest through sync.Once, and
		// a per-test directory would be removed while another still needed it.
		outputDir, err := os.MkdirTemp("", "trustvian-e2e-bin")
		if err != nil {
			buildErr = err
			return
		}

		targets := []struct{ module, pkg, name string }{
			{"platform", "./cmd/trustvian-local", "trustvian-local"},
			{"processor", "./cmd/trustvian-collector", "trustvian-collector"},
			{"processor", "./cmd/demo-producer", "demo-producer"},
		}
		for _, target := range targets {
			output := filepath.Join(outputDir, target.name)
			cmd := exec.Command(goTool, "build", "-o", output, target.pkg)
			cmd.Dir = filepath.Join(root, target.module)
			// GOWORK=off so the sibling module resolves its own requirements, the
			// way check-platform-boundary and the release build already do.
			cmd.Env = append(os.Environ(), "GOWORK=off")
			if combined, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("building %s: %w\n%s", target.name, err, combined)
				return
			}
		}
		built = realBinaries{
			local:     filepath.Join(outputDir, "trustvian-local"),
			collector: filepath.Join(outputDir, "trustvian-collector"),
			producer:  filepath.Join(outputDir, "demo-producer"),
		}
	})

	if buildErr != nil {
		// Skipped rather than failed only when the toolchain cannot build them at
		// all. A compile error in the helpers is a real failure and says so.
		if strings.Contains(buildErr.Error(), "no Go toolchain") {
			t.Skipf("cannot build the real binaries: %v", buildErr)
		}
		t.Fatalf("cannot build the real binaries: %v", buildErr)
	}
	return built
}

func TestDevJourneyAgainstRealBinaries(t *testing.T) {
	binaries := buildRealBinaries(t)

	// The control plane is started separately and outlives dev, so the run can be
	// inspected after dev has torn its own composition down. dev attaches with
	// --api-url, which is also task 078's CI path.
	runtime := startRealRuntime(t, binaries.local)

	workload := newE2EFixture(t)
	before := hashTree(t, workload.dir)

	home := t.TempDir()
	const candidate = "e2e-candidate"

	// ---- 1. records land in the run, including the last span ----
	firstRun := "e2e-run-1"
	runDevForReal(t, binaries, runtime.apiURL, home, workload, candidate, firstRun)

	progress := runtime.progress(t, firstRun)
	if progress.Status != "completed" {
		t.Fatalf("run status = %q, want completed", progress.Status)
	}
	// Every span, not merely some: the producer exports synchronously, so the
	// last span leaves the workload immediately before it exits. A teardown that
	// stopped the Collector too early, or moved the run terminal before the
	// Collector had posted, would lose exactly that one.
	if progress.RecordCount != strconv.Itoa(e2eSpanCount) {
		t.Fatalf("run holds %s records, want %d — a span exported just before the "+
			"workload exited did not land", progress.RecordCount, e2eSpanCount)
	}
	// Every record also became a behavior observation, so the evidence the gates
	// read is the evidence that arrived, not a subset the processor dropped.
	if progress.BehaviorObservationCount != strconv.Itoa(e2eSpanCount) {
		t.Errorf("run holds %s behavior observations, want %d",
			progress.BehaviorObservationCount, e2eSpanCount)
	}
	if !progress.BehaviorComplete {
		t.Errorf("behavior_complete = false, want true")
	}

	// ---- 2. the printed Web URL serves the run, with no identifier typed ----
	//
	// The task 074 journey: a browser opens the printed URL and finds the work
	// without being told an identifier. Asserted through the same bounded
	// collection routes the WebUI uses at startup.
	runtime.assertWebUIServesTheRun(t, firstRun)

	// ---- 3. a second run of one candidate meets a learned baseline ----
	secondRun := "e2e-run-2"
	runDevForReal(t, binaries, runtime.apiURL, home, workload, candidate, secondRun)

	// The assertion that fails if the engine store regresses to the in-memory
	// default. The first run does not report exactly zero — confidence also grows
	// *within* a run, because the producer repeats a behavior — so the measurement
	// that distinguishes a persisted baseline from a discarded one is that the
	// second run starts from what the first learned and therefore reports strictly
	// more. With an in-memory store both runs meet an empty baseline and report
	// the same number.
	first := runtime.anomalyConfidenceMean(t, firstRun, firstRun)
	second := runtime.anomalyConfidenceMean(t, firstRun, secondRun)
	if second <= 0 {
		t.Fatalf("the second run's anomaly confidence is %v, want above 0 — the "+
			"engine had no learned baseline at all, so the engine-evidence gates "+
			"cannot fire", second)
	}
	if second <= first {
		t.Fatalf("anomaly confidence did not grow between runs (%v then %v) — the "+
			"learned baseline did not persist, so every run of one candidate meets "+
			"an empty one and the engine-evidence gates read zero forever",
			first, second)
	}

	// ---- 4. the repository is byte-identical ----
	after := hashTree(t, workload.dir)
	assertTreesIdentical(t, before, after)
}

// ---------------------------------------------------------------------
// Running dev for real
// ---------------------------------------------------------------------

func runDevForReal(t *testing.T, binaries realBinaries, apiURL, home string,
	workload *e2eFixture, candidate, runID string) {
	t.Helper()

	// The producer reads its endpoint from its own variable and speaks gRPC, so a
	// two-line shell script maps dev's advertised gRPC endpoint onto it. That is
	// the case a workload which configures its exporter in code presents, and the
	// reason both receivers are enabled.
	script := filepath.Join(t.TempDir(), "workload.sh")
	body := fmt.Sprintf(`#!/bin/sh
OTLP_ENDPOINT="$TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT" \
SPAN_COUNT=%d \
SERVICE_NAME=%s \
ENVIRONMENT=%s \
exec %q
`, e2eSpanCount, e2eServiceName, devEnvironmentDefault, binaries.producer)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing the workload script: %v", err)
	}

	stdout := filepath.Join(t.TempDir(), "dev.out")
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
		// The agent's identity comes from the workload's own declaration, which is
		// what the processor derives the actor from.
		envServiceName+"="+e2eServiceName,
	)
	cmd.Stdout = outFile
	cmd.Stderr = outFile

	if err := cmd.Run(); err != nil {
		t.Fatalf("trustvian dev failed: %v\n%s", err, readWholeFile(t, stdout))
	}
	if output := readWholeFile(t, stdout); !strings.Contains(output, "complete") {
		t.Fatalf("dev did not report the run complete:\n%s", output)
	}
}

// e2eServiceName is the actor the producer declares and dev provisions as an
// Agent. They must agree or every record is refused.
const e2eServiceName = "e2e-support-agent"

// devBinary builds the CLI under test once.
var (
	devBinaryOnce sync.Once
	devBinaryPath string
	devBinaryErr  error
)

func devBinary(t *testing.T) string {
	t.Helper()
	devBinaryOnce.Do(func() {
		goTool, err := exec.LookPath("go")
		if err != nil {
			devBinaryErr = err
			return
		}
		root, err := repositoryRoot()
		if err != nil {
			devBinaryErr = err
			return
		}
		dir, err := os.MkdirTemp("", "trustvian-e2e-cli")
		if err != nil {
			devBinaryErr = err
			return
		}
		devBinaryPath = filepath.Join(dir, "trustvian")
		cmd := exec.Command(goTool, "build", "-o", devBinaryPath, "./cmd/trustvian")
		cmd.Dir = root
		if combined, err := cmd.CombinedOutput(); err != nil {
			devBinaryErr = fmt.Errorf("building trustvian: %w\n%s", err, combined)
		}
	})
	if devBinaryErr != nil {
		t.Fatalf("cannot build the CLI: %v", devBinaryErr)
	}
	return devBinaryPath
}

// repositoryRoot locates the module root from the test's working directory.
func repositoryRoot() (string, error) {
	// The test runs in cmd/trustvian, so the root is two levels up. Resolved and
	// verified rather than assumed, so a future move fails loudly.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("no go.mod at %s: %w", root, err)
	}
	return root, nil
}

// ---------------------------------------------------------------------
// The real control plane
// ---------------------------------------------------------------------

type realRuntime struct {
	apiURL string
}

func startRealRuntime(t *testing.T, binary string) *realRuntime {
	t.Helper()

	stateDir := t.TempDir()
	logPath := filepath.Join(stateDir, "control-plane.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("creating the log: %v", err)
	}
	t.Cleanup(func() { logFile.Close() })

	cmd := exec.Command(binary, "--state-dir", stateDir, "--listen", "127.0.0.1:0")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the control plane: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	discovery := filepath.Join(stateDir, localDiscoveryFile)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if url, err := readLocalDiscovery(discovery); err == nil {
			return &realRuntime{apiURL: url}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the control plane never published an endpoint\n%s", readWholeFile(t, logPath))
	return nil
}

// e2eProgress is the part of the progress response this test reads.
//
// The counts are strings, as every uint64 crossing /v1 is — decoding them as
// numbers is what this struct got wrong first, and it is worth keeping the type
// honest rather than teaching the test a second encoding.
type e2eProgress struct {
	Status                   string `json:"status"`
	RecordCount              string `json:"record_count"`
	BehaviorObservationCount string `json:"behavior_observation_count"`
	BehaviorComplete         bool   `json:"behavior_complete"`
}

func (c *realRuntime) progress(t *testing.T, runID string) e2eProgress {
	t.Helper()
	var progress e2eProgress
	c.getJSON(t, "/v1/evaluation-runs/"+runID+"/progress", &progress)
	return progress
}

// assertWebUIServesTheRun is the task 074 journey.
//
// Nothing is typed: the collections route is what the WebUI reads at startup, so
// finding the run through it is the same discovery a browser performs.
func (c *realRuntime) assertWebUIServesTheRun(t *testing.T, runID string) {
	t.Helper()

	// The page itself, at the printed Web URL — the same origin as the API.
	response, err := http.Get(c.apiURL + "/")
	if err != nil {
		t.Fatalf("GET the Web URL: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the Web URL answered %d, want 200", response.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("reading the page: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(page)), "<html") {
		t.Errorf("the Web URL did not serve a page:\n%.200s", page)
	}

	// And the hierarchy is discoverable without an identifier: projects, then the
	// run underneath, exactly as the browser descends.
	var projects struct {
		Projects []struct{ ID string } `json:"projects"`
	}
	c.getJSON(t, "/v1/projects", &projects)
	if len(projects.Projects) == 0 {
		t.Fatal("the projects collection is empty; nothing would be discoverable " +
			"in the browser without typing an identifier")
	}

	found := false
	for _, project := range projects.Projects {
		var agents struct {
			Agents []struct{ ID string } `json:"agents"`
		}
		c.getJSON(t, "/v1/projects/"+project.ID+"/agents", &agents)
		for _, agent := range agents.Agents {
			var candidates struct {
				Candidates []struct{ ID string } `json:"candidates"`
			}
			c.getJSON(t, "/v1/agents/"+agent.ID+"/candidates", &candidates)
			for _, candidate := range candidates.Candidates {
				var runs struct {
					Runs []struct{ ID string } `json:"evaluation_runs"`
				}
				c.getJSON(t, "/v1/candidates/"+candidate.ID+"/evaluation-runs", &runs)
				for _, run := range runs.Runs {
					if run.ID == runID {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Errorf("run %s was not reachable by descending from the projects "+
			"collection; the browser could not find it without an identifier", runID)
	}
}

// anomalyConfidenceMean reads the candidate side's mean from a comparison.
//
// Through `eval compare` rather than by reading records, because the platform
// deliberately retains no raw event history — the scorecard is where per-signal
// summaries live.
func (c *realRuntime) anomalyConfidenceMean(t *testing.T, reference, candidate string) float64 {
	t.Helper()

	body := map[string]any{
		"reference_run_id": reference,
		"candidate_run_id": candidate,
		// Strings, like every other uint64 crossing /v1, and deliberately
		// permissive: this comparison is read for its metric summaries, not for a
		// verdict, so no limit may turn a passing comparison into an error.
		"gate_limits": map[string]any{
			"max_added_behaviors":            e2eUnlimited,
			"max_block_decisions":            e2eUnlimited,
			"max_critical_risk_observations": e2eUnlimited,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding the comparison: %v", err)
	}

	response, err := http.Post(c.apiURL+"/v1/evaluations/compare",
		"application/json", strings.NewReader(string(encoded)))
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

	var decoded struct {
		Scorecard struct {
			Metrics struct {
				AnomalyConfidence struct {
					Candidate struct {
						Mean float64 `json:"mean"`
					} `json:"candidate"`
				} `json:"anomaly_confidence"`
			} `json:"metrics"`
		} `json:"scorecard"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decoding the comparison: %v\n%s", err, payload)
	}
	return decoded.Scorecard.Metrics.AnomalyConfidence.Candidate.Mean
}

func (c *realRuntime) getJSON(t *testing.T, path string, into any) {
	t.Helper()
	response, err := http.Get(c.apiURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s answered %d: %s", path, response.StatusCode, payload)
	}
	if err := json.Unmarshal(payload, into); err != nil {
		t.Fatalf("decoding %s: %v\n%s", path, err, payload)
	}
}

// ---------------------------------------------------------------------
// The fixture repository, and proving dev never writes to it
// ---------------------------------------------------------------------

type e2eFixture struct {
	dir string
}

func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not installed: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.txt"),
		[]byte("a fixture workload\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir,
			"-c", "user.email=test@example.invalid",
			"-c", "user.name=Test",
			"-c", "commit.gpgsign=false"}, args...)
		if combined, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, combined)
		}
	}
	run("init", "--quiet")
	run("add", "-A")
	run("commit", "--quiet", "-m", "fixture")

	return &e2eFixture{dir: dir}
}

// hashTree records every file in a directory, content and all.
//
// Every file, including untracked ones and everything under .git: acceptance
// criterion 2 says the repository is byte-identical, and "we only checked the
// tracked files" is how a wrapper gets away with dropping a directory in
// somebody's project.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// A symlink's target is the content that matters; reading through it could
		// hash something outside the tree.
		if entry.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			hashes[relative] = "symlink:" + target
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		hashes[relative] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hashing %s: %v", root, err)
	}
	return hashes
}

func assertTreesIdentical(t *testing.T, before, after map[string]string) {
	t.Helper()

	var added, removed, changed []string
	for path, hash := range after {
		previous, existed := before[path]
		switch {
		case !existed:
			added = append(added, path)
		case previous != hash:
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, still := after[path]; !still {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)

	if len(added) > 0 {
		t.Errorf("dev added %d file(s) to the workload's repository: %v", len(added), added)
	}
	if len(removed) > 0 {
		t.Errorf("dev removed %d file(s) from the workload's repository: %v",
			len(removed), removed)
	}
	if len(changed) > 0 {
		t.Errorf("dev changed %d file(s) in the workload's repository: %v",
			len(changed), changed)
	}
}

func readWholeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(could not read %s: %v)", path, err)
	}
	return string(data)
}
