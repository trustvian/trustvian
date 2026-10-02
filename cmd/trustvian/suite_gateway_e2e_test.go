//go:build !windows

package main

// A gateway's 502 after a completion is reconciled with the real control
// plane, not taken for a refusal: the CLI reports what the server holds, and
// what `last` reuses agrees with it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// gatewayTransport is the transport of an ordinary reverse proxy, failing as
// a gateway does for the executions it is told about:
//
//	commit:      the completion reaches the server and commits; the upstream
//	             response is then lost, so the proxy answers 502.
//	before:      the completion never reaches the server; 502.
//	unavailable: as commit, and every later request for the execution
//	             fails too, so its state cannot be read.
type gatewayTransport struct {
	prefixes map[string]string

	mu    sync.Mutex
	modes map[string]string
	lost  map[string]bool
}

var errUpstreamLost = errors.New("upstream connection lost")

func (g *gatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/scenario-executions" {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var begin struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &begin)
		for prefix, mode := range g.prefixes {
			if strings.HasPrefix(begin.ID, prefix) {
				g.mu.Lock()
				g.modes[begin.ID] = mode
				g.mu.Unlock()
			}
		}
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/scenario-executions/")
	id = strings.TrimSuffix(strings.TrimSuffix(id, "/complete"), "/fail")
	g.mu.Lock()
	mode, lost := g.modes[id], g.lost[id]
	g.mu.Unlock()
	if lost {
		return nil, errUpstreamLost
	}
	if mode != "" && strings.HasSuffix(r.URL.Path, "/complete") {
		if mode == "before" {
			return nil, errUpstreamLost
		}
		response, err := http.DefaultTransport.RoundTrip(r)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		if mode == "unavailable" {
			g.mu.Lock()
			g.lost[id] = true
			g.mu.Unlock()
		}
		return nil, errUpstreamLost
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestGatewayErrorsAfterCompletionAreReconciled(t *testing.T) {
	binaries := buildRealBinaries(t)
	runtime := startRealRuntime(t, binaries.local)
	workload := newE2EFixture(t)
	home := t.TempDir()
	script := writeScenarioWorkload(t, binaries)
	shScript := fmt.Sprintf("sh, %q", script)

	upstream, err := url.Parse(runtime.apiURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = &gatewayTransport{
		prefixes: map[string]string{"scn-gw-commit-": "commit", "scn-gw-before-": "before",
			"scn-gw-unavailable-": "unavailable"},
		modes: map[string]string{}, lost: map[string]bool{},
	}
	proxy.ErrorLog = nil
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.yaml": oneRun("gw-commit", scenarioFor(script, shScript, "2")),
		"b.yaml": oneRun("gw-before", scenarioFor(script, shScript, "2")),
		"c.yaml": oneRun("gw-unavailable", scenarioFor(script, shScript, "2")),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, doc, stderr := runSuiteForReal(t, binaries, gateway.URL, home, workload, dir,
		"--scenario-timeout", "60s")
	if code != exitOperational || len(doc.Members) != 3 {
		t.Fatalf("exit %d, members %+v\n%s", code, doc.Members, stderr)
	}
	committed, before, unavailable := doc.Members[0], doc.Members[1], doc.Members[2]

	// Committed behind a 502: the member says so, the server agrees, and the
	// execution is the reference it says it is.
	if committed.Error == nil || committed.Error.Code != "completed_without_response" ||
		*committed.ExitCode != exitOperational || committed.Result != nil ||
		!strings.Contains(committed.Error.Message, committed.ExecutionID) {
		t.Errorf("committed member = %+v", committed)
	}
	if got := executionStatus(t, runtime.apiURL, committed.ExecutionID); got != "completed" {
		t.Errorf("committed execution is %q", got)
	}
	// A 502 before any commit: the fail is accepted, the original error is
	// kept, and the execution is failed.
	if before.Error == nil || before.Error.Code != "operational" ||
		!strings.Contains(before.Error.Message, "502") {
		t.Errorf("failure-first member = %+v", before)
	}
	if got := executionStatus(t, runtime.apiURL, before.ExecutionID); got != "failed" {
		t.Errorf("failure-first execution is %q, want failed", got)
	}
	// Nothing answers afterwards: unknown, and not claimed to be anything.
	if unavailable.Error == nil || unavailable.Error.Code != "execution_state_unknown" {
		t.Errorf("unavailable member = %+v", unavailable)
	}
	// No diagnostic claims an execution stayed running when it completed.
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "stays running") &&
			(strings.Contains(line, committed.ExecutionID) || strings.Contains(line, unavailable.ExecutionID)) {
			t.Errorf("false diagnostic: %s", line)
		}
	}

	// What `last` reuses agrees with what was reported.
	reuse := t.TempDir()
	for name, body := range map[string]string{
		"a.yaml": oneRun("gw-commit", scenarioFor(script, shScript, "2")),
		"b.yaml": oneRun("gw-before", scenarioFor(script, shScript, "2")),
	} {
		if err := os.WriteFile(filepath.Join(reuse, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, doc, stderr = runSuiteForReal(t, binaries, runtime.apiURL, home, workload, reuse,
		"--scenario-timeout", "60s", "--reference", "last")
	if len(doc.Members) != 2 || doc.Members[0].Outcome != outcomePass ||
		doc.Members[1].Error == nil || doc.Members[1].Error.Code != "not_found" {
		t.Errorf("reuse: members %+v\n%s", doc.Members, stderr)
	}
}
