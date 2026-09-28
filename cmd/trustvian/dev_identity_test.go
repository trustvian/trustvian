//go:build !windows

package main

// Tests for identity derivation and the provisioning it feeds.
//
// The determinism assertions are the point of the file: a candidate that changed
// between runs would make every run look like a new actor, and the baseline would
// learn nothing. A structural test also proves no ephemeral value can reach
// candidate or agent identity, because that property has to survive people
// editing this code later.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 9, 27, 8, 53, 33, 0, time.UTC)

// ---------------------------------------------------------------------
// Derivation
// ---------------------------------------------------------------------

func TestIdentityIsDeterministicForOneCommit(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "support-agent"})

	first, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("first derivation: %v", err)
	}
	// A later wall clock must change only the run, never anything the baseline
	// keys on.
	second, err := deriveIdentity(devConfig{}, repo, environment,
		fixedTime.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("second derivation: %v", err)
	}

	if first.Project != second.Project {
		t.Errorf("project changed between runs: %q then %q", first.Project, second.Project)
	}
	if first.Agent != second.Agent {
		t.Errorf("agent changed between runs: %q then %q", first.Agent, second.Agent)
	}
	if first.Candidate != second.Candidate {
		t.Errorf("candidate changed between runs: %q then %q", first.Candidate, second.Candidate)
	}
	if first.Profile != second.Profile {
		t.Errorf("behavioral profile changed between runs: %q then %q",
			first.Profile, second.Profile)
	}
	// The run is the one value that must differ, because two invocations sharing
	// a run id would merge two executions' evidence into one.
	if first.Run == second.Run {
		t.Error("two invocations produced one run id")
	}
}

func TestCandidateComesFromTheCommit(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "a"})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}

	sha := gitOutput(t, repo, "rev-parse", "--short", "HEAD")
	if want := "git:" + sha; identity.Candidate != want {
		t.Fatalf("candidate = %q, want %q", identity.Candidate, want)
	}
	if identity.Dirty {
		t.Error("a clean worktree was reported dirty")
	}
	// The profile is the candidate, so two runs of one candidate share a learned
	// baseline — which is what makes "this behavior is new" mean anything on the
	// second run.
	if identity.Profile != identity.Candidate {
		t.Errorf("profile = %q, want the candidate %q", identity.Profile, identity.Candidate)
	}
}

func TestDirtyWorktreeIsVisiblyMarked(t *testing.T) {
	repo := newGitFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "uncommitted.txt"),
		[]byte("work in progress\n"), 0o600); err != nil {
		t.Fatalf("writing an uncommitted file: %v", err)
	}
	environment := environmentWith(t, map[string]string{envServiceName: "a"})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}

	if !identity.Dirty {
		t.Error("an uncommitted change was not detected")
	}
	if !strings.HasSuffix(identity.Candidate, "+dirty") {
		t.Errorf("candidate = %q; uncommitted work must be distinguishable from the commit",
			identity.Candidate)
	}
	// An untracked file counts: a new module the agent imports changes its
	// behavior as much as an edited one.
	clean := strings.TrimSuffix(identity.Candidate, "+dirty")
	if clean == identity.Candidate {
		t.Fatal("the marker is not a suffix")
	}
}

func TestMissingServiceNameStopsAndNamesTheFlag(t *testing.T) {
	repo := newGitFixture(t)
	// Nothing declares an agent. Inventing one would pollute a durable hierarchy
	// with a name indistinguishable afterwards from a real one.
	environment := environmentWith(t, map[string]string{})

	_, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err == nil {
		t.Fatal("an agent identity was invented")
	}
	for _, want := range []string{"--agent", envServiceName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q:\n%v", want, err)
		}
	}
}

func TestDeclaredServiceNameIsAdoptedNotOverridden(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "their-own-name"})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}
	if identity.Agent != "their-own-name" {
		t.Fatalf("agent = %q, want the workload's own declaration", identity.Agent)
	}
	if !identity.AgentFromTelemetry {
		t.Error("the agent was not recorded as coming from the workload")
	}

	// And dev does not re-export it: relabelling somebody else's telemetry is
	// what "read what their instrumentation already emits" forbids.
	if err := environment.declareIdentity(identity); err != nil {
		t.Fatalf("declareIdentity: %v", err)
	}
	if _, set := environment.additions[envServiceName]; set {
		t.Error("dev overrode a service name the workload had already declared")
	}
}

func TestFlaggedAgentIsExportedSoTheTwoAgree(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{})

	identity, err := deriveIdentity(devConfig{agent: "named-by-flag"}, repo,
		environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}
	if err := environment.declareIdentity(identity); err != nil {
		t.Fatalf("declareIdentity: %v", err)
	}

	// A provisioned Agent that never matches any telemetry is worse than none,
	// so dev exports what it provisioned.
	if got := environment.additions[envServiceName]; got != "named-by-flag" {
		t.Fatalf("%s = %q, want the provisioned agent", envServiceName, got)
	}
}

func TestNonRepositoryStopsAndNamesTheFlag(t *testing.T) {
	notARepo := t.TempDir()
	environment := environmentWith(t, map[string]string{envServiceName: "a"})

	_, err := deriveIdentity(devConfig{}, notARepo, environment, fixedTime)
	if err == nil {
		t.Fatal("a candidate identity was invented outside a repository")
	}
	if !strings.Contains(err.Error(), "--candidate") {
		t.Errorf("the message does not name --candidate:\n%v", err)
	}
}

func TestExplicitIdentityOverridesEverything(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "ignored"})

	identity, err := deriveIdentity(devConfig{
		project:     "my-project",
		agent:       "my-agent",
		candidate:   "my-candidate",
		environment: "sandbox",
		runID:       "my-run",
	}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}

	for _, tt := range []struct{ name, got, want string }{
		{"project", identity.Project, "my-project"},
		{"agent", identity.Agent, "my-agent"},
		{"candidate", identity.Candidate, "my-candidate"},
		{"environment", identity.Environment, "sandbox"},
		{"run", identity.Run, "my-run"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestIdentityRefusesAValueThatWouldBreakARouteOrTheConfig(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "a"})

	for _, tt := range []struct {
		name   string
		config devConfig
	}{
		{"a path separator in the agent", devConfig{agent: "team/agent"}},
		{"a newline in the candidate", devConfig{candidate: "c\nrun_id: other"}},
		{"a quote in the project", devConfig{project: `pro"ject`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := deriveIdentity(tt.config, repo, environment, fixedTime); err == nil {
				t.Fatal("the value was accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------
// The environment agreement that makes evidence usable at all
// ---------------------------------------------------------------------

func TestDeploymentEnvironmentIsDeclaredToTheWorkload(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "a"})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}
	if err := environment.declareIdentity(identity); err != nil {
		t.Fatalf("declareIdentity: %v", err)
	}

	// Without this attribute the engine reports an empty environment, the
	// platform refuses every record whose environment differs from the run's, and
	// the run ends with no usable evidence while everything looks healthy.
	declared, found := resourceAttribute(
		environment.additions[envResourceAttributes], deploymentEnvironmentKey)
	if !found {
		t.Fatalf("%s is absent from %s; every record would be refused",
			deploymentEnvironmentKey, envResourceAttributes)
	}
	if declared != identity.Environment {
		t.Errorf("%s = %q, want the run's environment %q",
			deploymentEnvironmentKey, declared, identity.Environment)
	}
}

func TestInheritedResourceAttributesArePreserved(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{
		envServiceName:        "a",
		envResourceAttributes: "team=payments,service.version=1.2.3",
	})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}
	if err := environment.declareIdentity(identity); err != nil {
		t.Fatalf("declareIdentity: %v", err)
	}

	value := environment.additions[envResourceAttributes]
	for _, want := range []string{"team=payments", "service.version=1.2.3"} {
		if !strings.Contains(value, want) {
			t.Errorf("%s = %q; the developer's own attribute %q was dropped",
				envResourceAttributes, value, want)
		}
	}
}

func TestAConflictingDeclaredEnvironmentIsRefused(t *testing.T) {
	repo := newGitFixture(t)
	// The workload says production; the run is provisioned in local. Appending a
	// second value would leave the SDK to choose, and overriding would discard a
	// deliberate declaration — so dev stops and lets the developer decide.
	environment := environmentWith(t, map[string]string{
		envServiceName:        "a",
		envResourceAttributes: deploymentEnvironmentKey + "=production",
	})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}
	err = environment.declareIdentity(identity)
	if err == nil {
		t.Fatal("a conflicting declared environment was accepted")
	}
	if !strings.Contains(err.Error(), "--environment production") {
		t.Errorf("the message does not offer the resolution:\n%v", err)
	}
}

func TestAMatchingDeclaredEnvironmentIsAccepted(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{
		envServiceName:        "a",
		envResourceAttributes: deploymentEnvironmentKey + "=local",
	})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}
	if err := environment.declareIdentity(identity); err != nil {
		t.Fatalf("an agreeing declaration was refused: %v", err)
	}
}

// ---------------------------------------------------------------------
// No ephemeral value reaches behavioral identity
// ---------------------------------------------------------------------

// TestNoEphemeralValueReachesCandidateOrAgentIdentity is structural on purpose.
//
// The property has to survive people editing this code later, and a value test
// only covers the paths it happens to exercise. A candidate keyed on a pid, a
// port or a timestamp would make every run a new actor — the exact failure task
// 051 kept candidate metadata out of fingerprint identity to prevent.
func TestNoEphemeralValueReachesCandidateOrAgentIdentity(t *testing.T) {
	source := readCode(t, "dev_identity.go")

	// The one permitted use of a clock is the generated run id, which is
	// correlation and never behavioral identity. Everything else is refused.
	assignments := regexp.MustCompile(
		`identity\.(Candidate|Agent|Project|Profile)\s*(=|\+=)\s*([^\n]*)`)
	for _, match := range assignments.FindAllStringSubmatch(source, -1) {
		field, expression := match[1], match[3]
		for _, ephemeral := range []string{
			"Getpid", "time.Now", "now.", "rand.", "os.Getppid", "Nanosecond", "Unix()",
		} {
			if strings.Contains(expression, ephemeral) {
				t.Errorf("identity.%s is assigned from %q; an ephemeral value in "+
					"behavioral identity makes every run a new actor",
					field, ephemeral)
			}
		}
	}

	// The clock reaches exactly one field.
	runAssignments := regexp.MustCompile(`identity\.Run\s*=`)
	if len(runAssignments.FindAllString(source, -1)) == 0 {
		t.Error("the run id is never assigned; this test is not checking what it thinks")
	}
}

// ---------------------------------------------------------------------
// Provisioning
// ---------------------------------------------------------------------

func TestProvisioningIsIdempotent(t *testing.T) {
	// Many dev runs share one control plane, so creating unconditionally would
	// answer 409 already_exists on the second run and take it down.
	server := newFakeDevAPI(t)
	defer server.Close()

	identity := devIdentity{
		Project: "p", ProjectName: "p", Agent: "a", AgentName: "a",
		Candidate: "c", Environment: "local", Run: "r", Profile: "c",
	}
	provision, err := newProvisioner(server.URL, identity)
	if err != nil {
		t.Fatalf("newProvisioner: %v", err)
	}

	for pass := range 2 {
		if err := provision.ensureHierarchy(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", pass+1, err)
		}
	}

	// Four objects, created once each despite two passes.
	for _, path := range []string{"/v1/projects", "/v1/environments", "/v1/agents", "/v1/candidates"} {
		if got := server.posts(path); got != 1 {
			t.Errorf("POST %s happened %d times, want 1", path, got)
		}
	}
}

func TestProvisioningCreatesTheEnvironmentBeforeTheRun(t *testing.T) {
	// A run may only name an environment its project already owns, and a new
	// environment is active on creation — which is the state a run requires.
	server := newFakeDevAPI(t)
	defer server.Close()

	identity := devIdentity{
		Project: "p", ProjectName: "p", Agent: "a", AgentName: "a",
		Candidate: "c", Environment: "local", Run: "r", Profile: "c",
	}
	provision, err := newProvisioner(server.URL, identity)
	if err != nil {
		t.Fatalf("newProvisioner: %v", err)
	}
	if err := provision.ensureHierarchy(context.Background()); err != nil {
		t.Fatalf("ensureHierarchy: %v", err)
	}
	if err := provision.startRun(context.Background()); err != nil {
		t.Fatalf("startRun: %v", err)
	}

	order := server.postOrder()
	assertBefore(t, order, "/v1/projects", "/v1/environments")
	assertBefore(t, order, "/v1/environments", "/v1/evaluation-runs")
	assertBefore(t, order, "/v1/agents", "/v1/candidates")
	assertBefore(t, order, "/v1/candidates", "/v1/evaluation-runs")
	// Created, then started.
	assertBefore(t, order, "/v1/evaluation-runs", "/v1/evaluation-runs/r/start")
}

func TestStartRunCarriesTheProfileAndEnvironment(t *testing.T) {
	server := newFakeDevAPI(t)
	defer server.Close()

	identity := devIdentity{
		Project: "p", Agent: "a", Candidate: "c",
		Environment: "local", Run: "r", Profile: "c",
	}
	provision, err := newProvisioner(server.URL, identity)
	if err != nil {
		t.Fatalf("newProvisioner: %v", err)
	}
	if err := provision.startRun(context.Background()); err != nil {
		t.Fatalf("startRun: %v", err)
	}

	body := server.body("/v1/evaluation-runs")
	// The profile travels beside every record and must match the run's own, or
	// ingest is refused.
	for key, want := range map[string]string{
		"id": "r", "candidate_id": "c", "environment": "local", "behavioral_profile": "c",
	} {
		if got, _ := body[key].(string); got != want {
			t.Errorf("%s = %v, want %q", key, body[key], want)
		}
	}
}

func TestAFailedWorkloadFailsTheRunRatherThanCompletingIt(t *testing.T) {
	server := newFakeDevAPI(t)
	defer server.Close()

	provision, err := newProvisioner(server.URL, devIdentity{Run: "r"})
	if err != nil {
		t.Fatalf("newProvisioner: %v", err)
	}
	if err := provision.failRun(context.Background(), 42); err != nil {
		t.Fatalf("failRun: %v", err)
	}

	// Completing it would leave evidence a later `eval compare` reads as a
	// finished evaluation, when the program actually crashed.
	if server.posts("/v1/evaluation-runs/r/complete") != 0 {
		t.Error("a failed workload completed its run")
	}
	if server.posts("/v1/evaluation-runs/r/fail") != 1 {
		t.Error("a failed workload did not fail its run")
	}
	reason, _ := server.body("/v1/evaluation-runs/r/fail")["reason"].(string)
	if !strings.Contains(reason, "42") {
		t.Errorf("the failure reason does not carry the exit status: %q", reason)
	}
}

func TestProvisioningReportsAServerFailureRatherThanCreatingTwice(t *testing.T) {
	// A 500 on the existence check is not absence. Creating in response would
	// make a second object, or hide a real fault behind a 409.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		t.Errorf("unexpected %s %s: the existence check failed, so nothing should be created",
			r.Method, r.URL.Path)
	}))
	defer server.Close()

	provision, err := newProvisioner(server.URL, devIdentity{Project: "p", ProjectName: "p"})
	if err != nil {
		t.Fatalf("newProvisioner: %v", err)
	}
	if err := provision.ensureHierarchy(context.Background()); err == nil {
		t.Fatal("a server error was treated as absence")
	}
}

// ---------------------------------------------------------------------
// Generated Collector configuration for a run
// ---------------------------------------------------------------------

func TestCollectorConfigCarriesTheEvaluationRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), collectorConfigFile)
	if err := writeCollectorConfig(path, collectorConfigData{
		OTLPHTTPPort: 1, OTLPGRPCPort: 2, HealthPort: 3,
		APIURL:  "http://127.0.0.1:9999",
		RunID:   "dev-git-abc1234-20260927T000000Z",
		Profile: "git:abc1234+dirty",
		// Paths, which are the values most likely to contain something awkward.
		PendingStatePath: filepath.Join(t.TempDir(), collectorPendingStateFile),
		BaselinePath:     filepath.Join(t.TempDir(), "baseline-git_3aabc1234.json"),
	}); err != nil {
		t.Fatalf("writeCollectorConfig: %v", err)
	}

	rendered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config: %v", err)
	}
	config := string(rendered)

	for _, want := range []string{
		`run_id: "dev-git-abc1234-20260927T000000Z"`,
		`behavioral_profile: "git:abc1234+dirty"`,
		`api_url: "http://127.0.0.1:9999"`,
		"required: true",
		// A file store, not the in-memory default. Without it the baseline is
		// discarded at every run's end, so two runs of one candidate never share
		// one — and the two engine-evidence gates can only ever read zero.
		"type: file",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("the config does not contain %s:\n%s", want, config)
		}
	}
}

func TestCollectorConfigRefusesAnIdentityThatWouldBreakIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), collectorConfigFile)
	err := writeCollectorConfig(path, collectorConfigData{
		OTLPHTTPPort: 1, OTLPGRPCPort: 2, HealthPort: 3,
		APIURL: "http://127.0.0.1:1", RunID: "run\"\nexporters: {}",
		Profile: "p", PendingStatePath: "/tmp/pending.json",
	})
	if err == nil {
		t.Fatal("a run id that would restructure the document was accepted")
	}
	// Nothing is written when the document would be malformed.
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("a malformed configuration was written to disk")
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// newGitFixture makes a repository with one commit.
func newGitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	// -c rather than a config file, so the test leaves no global state and works
	// on a machine with no committer identity configured.
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir,
			"-c", "user.email=test@example.invalid",
			"-c", "user.name=Test",
			"-c", "commit.gpgsign=false"}, args...)
		if output, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init", "--quiet")
	run("commit", "--quiet", "--allow-empty", "-m", "fixture")
	return dir
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return output
}

// environmentWith builds a devEnvironment whose snapshot is exactly the given
// variables.
//
// Constructed directly rather than through captureEnvironment, so a test does
// not inherit whatever OTEL_* the developer's own shell happens to carry.
func environmentWith(t *testing.T, variables map[string]string) *devEnvironment {
	t.Helper()
	snapshot := make(map[string]string, len(variables))
	for name, value := range variables {
		snapshot[name] = value
	}
	return &devEnvironment{snapshot: snapshot, additions: make(map[string]string)}
}

// fakeDevAPI records what dev asked of the /v1 API.
type fakeDevAPI struct {
	*httptest.Server

	mu     sync.Mutex
	seen   map[string]int
	order  []string
	bodies map[string]map[string]any
	// existing is the set of paths the fake reports as already present.
	existing map[string]bool
}

// newFakeAPI answers 404 for anything not yet created, and 200 after.
//
// That is the behavior provisioning is written against: GET decides, POST
// creates, and a second pass finds what the first made.
func newFakeDevAPI(t *testing.T) *fakeDevAPI {
	t.Helper()
	fake := &fakeDevAPI{
		seen:     map[string]int{},
		bodies:   map[string]map[string]any{},
		existing: map[string]bool{},
	}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			if fake.existing[r.URL.Path] {
				writeJSON(w, http.StatusOK, map[string]any{"id": "present"})
				return
			}
			writeJSON(w, http.StatusNotFound,
				map[string]any{"error": map[string]string{"code": "not_found", "message": "absent"}})
		case http.MethodPost:
			fake.seen[r.URL.Path]++
			fake.order = append(fake.order, r.URL.Path)

			var body map[string]any
			if decoded, err := decodeBody(r); err == nil {
				body = decoded
			}
			fake.bodies[r.URL.Path] = body

			// Creating something makes a later GET find it, which is what makes
			// the idempotence assertion meaningful.
			if id, ok := body["id"].(string); ok {
				fake.existing[r.URL.Path+"/"+id] = true
			}
			if ref, ok := body["ref"].(string); ok {
				if project, ok := body["project_id"].(string); ok {
					fake.existing["/v1/projects/"+project+"/environments/"+ref] = true
				}
			}
			writeJSON(w, http.StatusCreated, map[string]any{"id": "created"})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return fake
}

func (f *fakeDevAPI) posts(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[path]
}

func (f *fakeDevAPI) postOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

func (f *fakeDevAPI) body(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if body := f.bodies[path]; body != nil {
		return body
	}
	return map[string]any{}
}

func decodeBody(r *http.Request) (map[string]any, error) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func assertBefore(t *testing.T, order []string, first, second string) {
	t.Helper()
	firstIndex, secondIndex := -1, -1
	for i, path := range order {
		if path == first && firstIndex < 0 {
			firstIndex = i
		}
		if path == second && secondIndex < 0 {
			secondIndex = i
		}
	}
	if firstIndex < 0 {
		t.Errorf("%s was never posted; order was %v", first, order)
		return
	}
	if secondIndex < 0 {
		t.Errorf("%s was never posted; order was %v", second, order)
		return
	}
	if firstIndex > secondIndex {
		t.Errorf("%s came after %s; order was %v", first, second, order)
	}
}

// ---------------------------------------------------------------------
// --behavioral-profile (task 078)
// ---------------------------------------------------------------------

// TestBehavioralProfileDefaultsToTheCandidate is the additive half of the
// contract: omitted, dev behaves exactly as it did before the flag existed, so
// no existing invocation changes meaning.
func TestBehavioralProfileDefaultsToTheCandidate(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "support-agent"})

	identity, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}
	if identity.Profile != identity.Candidate {
		t.Errorf("profile = %q, want the candidate %q",
			identity.Profile, identity.Candidate)
	}
}

// TestBehavioralProfileOverridesWithoutTouchingTheCandidate is the property
// task 078 needs: a runner can isolate repetitions without allocating a
// candidate per repetition, which would change the identity of the thing under
// test to obtain an isolation property unrelated to identity.
func TestBehavioralProfileOverridesWithoutTouchingTheCandidate(t *testing.T) {
	repo := newGitFixture(t)
	environment := environmentWith(t, map[string]string{envServiceName: "support-agent"})

	base, err := deriveIdentity(devConfig{}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("baseline derivation: %v", err)
	}

	scoped, err := deriveIdentity(
		devConfig{behavioralProfile: "rep-3"}, repo, environment, fixedTime)
	if err != nil {
		t.Fatalf("deriveIdentity: %v", err)
	}

	if scoped.Profile != "rep-3" {
		t.Errorf("profile = %q, want %q", scoped.Profile, "rep-3")
	}
	if scoped.Candidate != base.Candidate {
		t.Errorf("the candidate moved with the profile: %q, want %q",
			scoped.Candidate, base.Candidate)
	}
	// Everything else the baseline keys on must be untouched too, or the flag
	// would be isolating more than the learning scope.
	if scoped.Project != base.Project || scoped.Agent != base.Agent ||
		scoped.Environment != base.Environment {
		t.Errorf("the profile flag moved another identity field:\n got  %+v\n want %+v",
			scoped, base)
	}
}

// TestBehavioralProfileIsValidatedLikeEveryOtherIdentity proves the new flag
// does not open a hole around validateIdentity — a ref with a path separator
// would change the shape of a /v1 route rather than name a resource.
func TestBehavioralProfileIsValidatedLikeEveryOtherIdentity(t *testing.T) {
	for _, ref := range []string{"rep/3", `rep\3`} {
		t.Run(ref, func(t *testing.T) {
			err := validateIdentity(devIdentity{
				Project: "p", Agent: "a", Candidate: "c",
				Environment: "local", Run: "r", Profile: ref,
			})
			if err == nil {
				t.Fatalf("profile %q was accepted", ref)
			}
			if !strings.Contains(err.Error(), "behavioral profile") {
				t.Errorf("the error does not name the field: %v", err)
			}
		})
	}
}

// TestBaselineLockFollowsTheProfile is why slice 2 needed no change to the
// locking mechanism: dev already keeps one baseline file per profile, so N
// sequential repetitions under N refs produce N files and no contention.
//
// Asserted rather than assumed, because the whole isolation property task 078
// depends on rests on it.
func TestBaselineLockFollowsTheProfile(t *testing.T) {
	stateDir := t.TempDir()

	first, err := acquireBaseline(stateDir, "rep-1")
	if err != nil {
		t.Fatalf("acquire rep-1: %v", err)
	}
	defer first.release()

	// A different profile is a different file, so this must not contend.
	second, err := acquireBaseline(stateDir, "rep-2")
	if err != nil {
		t.Fatalf("acquire rep-2 while rep-1 is held: %v — the lock is not "+
			"per profile, so repetitions cannot be isolated", err)
	}
	defer second.release()

	if first.path == second.path {
		t.Fatalf("both profiles resolved to one baseline file %q", first.path)
	}
}
