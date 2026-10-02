//go:build !windows

package main

// The suite's lifecycle against the real control plane and real processes:
// the completion/deadline race in both orders, and one real SIGINT or
// SIGTERM reaching the active workload exactly once.

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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// oneRun is scenarioFor at runs: 1, k = 1.
func oneRun(name, body string) string {
	body = strings.Replace(body, "runs: 2", "runs: 1", 1)
	body = strings.Replace(body, "added_candidate_presence_minimum: 2", "added_candidate_presence_minimum: 1", 1)
	return namedScenario(name, body)
}

func executionStatus(t *testing.T, apiURL, id string) string {
	t.Helper()
	response, err := http.Get(apiURL + "/v1/scenario-executions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var stored struct {
		Execution struct {
			Status string `json:"status"`
		} `json:"execution"`
	}
	_ = json.NewDecoder(response.Body).Decode(&stored)
	return stored.Execution.Status
}

// raceProxy forwards to the real control plane, except scenario-execution
// completions, which it makes race the client:
//
//	commit: the completion reaches the server and commits; its answer is
//	        held until the client has given up, and never delivered.
//	hold:   the completion is held, unforwarded, until the client has
//	        failed the execution; then it is forwarded and must be refused.
type raceProxy struct {
	server   *httptest.Server
	upstream *url.URL
	// prefixes maps an execution-id prefix to its mode. Execution ids carry
	// a random suffix, so each id is learned from its begin request.
	prefixes map[string]string

	mu           sync.Mutex
	modes        map[string]string
	failed       map[string]chan struct{}
	lateStatuses map[string]int
	settled      map[string]chan struct{}
}

func newRaceProxy(t *testing.T, apiURL string, prefixes map[string]string) *raceProxy {
	t.Helper()
	upstream, err := url.Parse(apiURL)
	if err != nil {
		t.Fatal(err)
	}
	p := &raceProxy{upstream: upstream, prefixes: prefixes, modes: map[string]string{},
		failed: map[string]chan struct{}{}, lateStatuses: map[string]int{}, settled: map[string]chan struct{}{}}
	forward := httputil.NewSingleHostReverseProxy(upstream)
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/scenario-executions" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var begin struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(body, &begin)
			for prefix, mode := range p.prefixes {
				if strings.HasPrefix(begin.ID, prefix) {
					p.mu.Lock()
					p.modes[begin.ID] = mode
					p.failed[begin.ID] = make(chan struct{})
					p.settled[begin.ID] = make(chan struct{})
					p.mu.Unlock()
				}
			}
		}
		const prefix = "/v1/scenario-executions/"
		id := strings.TrimPrefix(r.URL.Path, prefix)
		id = strings.TrimSuffix(strings.TrimSuffix(id, "/complete"), "/fail")
		p.mu.Lock()
		mode := p.modes[id]
		failedC, settledC := p.failed[id], p.settled[id]
		p.mu.Unlock()
		switch {
		case mode != "" && strings.HasSuffix(r.URL.Path, "/complete"):
			body, _ := io.ReadAll(r.Body)
			if mode == "hold" {
				select {
				case <-failedC:
				case <-time.After(60 * time.Second):
				}
			}
			status := p.upstreamPost(t, r.URL.Path, body)
			p.mu.Lock()
			p.lateStatuses[id] = status
			p.mu.Unlock()
			close(settledC)
			<-r.Context().Done() // the client never hears back
		case mode != "" && strings.HasSuffix(r.URL.Path, "/fail"):
			recorder := httptest.NewRecorder()
			forward.ServeHTTP(recorder, r)
			if recorder.Code == 200 {
				select {
				case <-failedC:
				default:
					close(failedC)
				}
			}
			for k, v := range recorder.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
		default:
			forward.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *raceProxy) upstreamPost(t *testing.T, path string, body []byte) int {
	response, err := http.Post(p.upstream.String()+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Errorf("forwarding the completion: %v", err)
		return 0
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestSuiteCompletionRaceAgainstTheRealServer(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()
	script := writeScenarioWorkload(t, binaries)
	shScript := fmt.Sprintf("sh, %q", script)

	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.yaml": oneRun("race-commit", scenarioFor(script, shScript, "2")),
		"b.yaml": oneRun("race-hold", scenarioFor(script, shScript, "2")),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	proxy := newRaceProxy(t, runtime.apiURL, map[string]string{
		"scn-race-commit-": "commit", "scn-race-hold-": "hold",
	})

	code, doc, stderr := runSuiteForReal(t, binaries, proxy.url(), home, workload, dir,
		"--scenario-timeout", "8s")
	if code != exitOperational || len(doc.Members) != 2 {
		t.Fatalf("exit %d, members %+v\n%s", code, doc.Members, stderr)
	}
	commit, hold := doc.Members[0], doc.Members[1]
	if commit.Error == nil || commit.Error.Code != "completed_without_response" {
		t.Errorf("commit-first member = %+v; want completed_without_response", commit)
	}
	if hold.Error == nil || hold.Error.Code != "scenario_timeout" {
		t.Errorf("deadline-first member = %+v; want scenario_timeout", hold)
	}
	proxy.waitSettled(t)
	if got := executionStatus(t, runtime.apiURL, commit.ExecutionID); got != "completed" {
		t.Errorf("commit-first execution is %q; the report says completed", got)
	}
	if got := executionStatus(t, runtime.apiURL, hold.ExecutionID); got != "failed" {
		t.Errorf("deadline-first execution is %q, want failed", got)
	}
	if status := proxy.lateStatus(hold.ExecutionID); status != http.StatusConflict {
		t.Errorf("the late completion of a failed execution answered %d, want 409", status)
	}

	// What `last` selects agrees with what the suite reported: the execution
	// reported completed is a reference, the one reported timed out is not.
	code, doc, stderr = runSuiteForReal(t, binaries, runtime.apiURL, home, workload, dir,
		"--scenario-timeout", "60s", "--reference", "last")
	if len(doc.Members) != 2 || doc.Members[0].Outcome != outcomePass ||
		doc.Members[1].Error == nil || doc.Members[1].Error.Code != "not_found" {
		t.Errorf("reuse: exit %d, members %+v\n%s", code, doc.Members, stderr)
	}
}

// One real signal to a suite reaches its active workload once — SIGTERM to
// its process group, from the suite's single owner — and never as a second
// signal from a second subscriber.
func TestOneSuiteSignalReachesTheWorkloadOnce(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()
	script := writeScenarioWorkload(t, binaries)
	shScript := fmt.Sprintf("sh, %q", script)

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			work := t.TempDir()
			received, ready := filepath.Join(work, "signals"), filepath.Join(work, "ready")
			recorder := filepath.Join(work, "record.sh")
			// The handlers only record: the workload keeps running, so every
			// signal that reaches it is recorded, until the deadline
			// machinery's SIGKILL — which cannot be trapped — ends it after
			// the grace. An exit inside a handler would let the first of two
			// pending signals hide the second.
			if err := os.WriteFile(recorder, []byte(`#!/bin/sh
trap 'echo INT >> "$1"' INT
trap 'echo TERM >> "$1"' TERM
printf ready > "$2"
while :; do sleep 0.05; done
`), 0o700); err != nil {
				t.Fatal(err)
			}
			name := "signal-" + strings.ToLower(strings.TrimPrefix(sig.String(), "SIG"))
			active := strings.Replace(oneRun(name+"-a", scenarioFor(script, shScript, "2")),
				fmt.Sprintf("reference:\n  command: [sh, %q]", script),
				fmt.Sprintf("reference:\n  command: [sh, %q, %q, %q]", recorder, received, ready), 1)
			dir := t.TempDir()
			for file, body := range map[string]string{
				"a.yaml": active,
				"b.yaml": oneRun(name+"-b", scenarioFor(script, shScript, "2")),
				"c.yaml": oneRun(name+"-c", scenarioFor(script, shScript, "2")),
			} {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var stdout, stderr bytes.Buffer
			cmd := exec.Command(devBinary(t), "eval", "run", "--suite", dir, "--scenario-timeout", "2m",
				"--fail-fast", "--api-url", runtime.apiURL, "--collector-bin", binaries.collector, "--json")
			cmd.Dir = workload.dir
			cmd.Env = append(os.Environ(), "HOME="+home)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitForFile(t, ready)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(60 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("the suite did not stop after %s\n%s", sig, stderr.String())
			}

			if code := cmd.ProcessState.ExitCode(); code != exitOperational {
				t.Errorf("exit %d, want 3\n%s", code, stderr.String())
			}
			var doc suiteDocument
			if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
				t.Fatalf("stdout: %v\n%s", err, stdout.Bytes())
			}
			if len(doc.Members) != 3 || doc.Members[0].Error == nil || doc.Members[0].Error.Code != "cancelled" ||
				doc.Members[1].SkippedReason != skippedCancelled || doc.Members[2].SkippedReason != skippedCancelled {
				t.Errorf("members %+v", doc.Members)
			}
			if got := executionStatus(t, runtime.apiURL, doc.Members[0].ExecutionID); got != "failed" {
				t.Errorf("the cancelled execution is %q, want failed", got)
			}
			signals, _ := os.ReadFile(received)
			if got := strings.Fields(string(signals)); len(got) != 1 || got[0] != "TERM" {
				t.Errorf("the workload received %v; want exactly one SIGTERM from the suite's one owner", got)
			}
			if strings.Contains(stderr.String(), "candidate repetition") {
				t.Errorf("a workload ran after the cancellation:\n%s", stderr.String())
			}
		})
	}
}

func (p *raceProxy) url() string { return p.server.URL }

func (p *raceProxy) lateStatus(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lateStatuses[id]
}

func (p *raceProxy) waitSettled(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	var waits []chan struct{}
	for _, c := range p.settled {
		waits = append(waits, c)
	}
	p.mu.Unlock()
	for _, c := range waits {
		select {
		case <-c:
		case <-time.After(60 * time.Second):
			t.Fatal("a raced completion never settled")
		}
	}
}
