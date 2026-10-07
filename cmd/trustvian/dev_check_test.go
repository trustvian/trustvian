//go:build !windows

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStatusPlane is a control plane that serves GET /v1/status and records
// every other request, so a test can prove `dev --check` creates nothing.
type fakeStatusPlane struct {
	mu       sync.Mutex
	reported bool
	others   []string
}

func newFakeStatusPlane(t *testing.T) (*fakeStatusPlane, string) {
	t.Helper()
	plane := &fakeStatusPlane{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plane.mu.Lock()
		defer plane.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/status":
			w.Header().Set("Content-Type", "application/json")
			if plane.reported {
				fmt.Fprint(w, `{"version":"1","collectors":[{"collector_id":"dev-check","instance":"00000000000000fe"}]}`)
			} else {
				fmt.Fprint(w, `{"version":"1","collectors":[]}`)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/collectors/dev-check/status":
			_, _ = io.Copy(io.Discard, r.Body)
			plane.reported = true
			fmt.Fprint(w, `{"version":"1","disposition":"accepted"}`)
		default:
			plane.others = append(plane.others, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return plane, server.URL
}

func setUpDevCheck(t *testing.T, mode string) string {
	t.Helper()
	withFakeCollector(t, mode)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	return home
}

func TestDevCheckPrintsTheDocumentAfterTheFirstReport(t *testing.T) {
	home := setUpDevCheck(t, "report-status")
	plane, url := newFakeStatusPlane(t)

	var out, errOut strings.Builder
	start := time.Now()
	code := runDev(streams{out: &out, err: &errOut}, []string{"--check", "--api-url", url})
	if code != exitDevOK {
		t.Fatalf("exit %d\nstderr: %s", code, errOut.String())
	}
	if elapsed := time.Since(start); elapsed >= devCheckWait {
		t.Fatalf("the check waited the whole bound (%s) although a report arrived", elapsed)
	}
	if want := `{"version":"1","collectors":[{"collector_id":"dev-check","instance":"00000000000000fe"}]}` + "\n"; out.String() != want {
		t.Fatalf("stdout:\n got %q\nwant %q", out.String(), want)
	}

	// Nothing but status was touched: no project, no candidate, no run.
	plane.mu.Lock()
	others := plane.others
	plane.mu.Unlock()
	if len(others) != 0 {
		t.Fatalf("dev --check made requests beyond status: %v", others)
	}

	// The Collector it started feeds no run and keeps no baseline.
	configs, _ := filepath.Glob(filepath.Join(home, ".trustvian", "dev", "*", collectorConfigFile))
	if len(configs) != 1 {
		t.Fatalf("collector configs: %v", configs)
	}
	raw, err := os.ReadFile(configs[0])
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	for _, absent := range []string{"evaluation:", "storage:", "run_id"} {
		if strings.Contains(config, absent) {
			t.Errorf("the check's Collector config declares %q:\n%s", absent, config)
		}
	}
	if !strings.Contains(config, `collector_id: "dev-check"`) {
		t.Errorf("the check's Collector is not named dev-check:\n%s", config)
	}
}

func TestDevCheckPrintsTheDocumentAndExitsOperationalWhenNoReportArrives(t *testing.T) {
	setUpDevCheck(t, "")
	_, url := newFakeStatusPlane(t)
	previous := devCheckWait
	devCheckWait = 300 * time.Millisecond
	t.Cleanup(func() { devCheckWait = previous })

	var out, errOut strings.Builder
	code := runDev(streams{out: &out, err: &errOut}, []string{"--check", "--api-url", url})
	// The pipeline was not checked, so the exit is operational, but the
	// document is still printed as the control plane said it: no collector.
	if code != exitDevOperational {
		t.Fatalf("exit %d, want %d\nstderr: %s", code, exitDevOperational, errOut.String())
	}
	if !strings.Contains(errOut.String(), "did not report status") {
		t.Errorf("stderr does not name the missing report: %q", errOut.String())
	}
	if out.String() != `{"version":"1","collectors":[]}`+"\n" {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestDevCheckUsageAndOperationalFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"a command after --", []string{"--check", "--", "python", "agent.py"}, exitDevUsage},
		{"a bare separator", []string{"--check", "--"}, exitDevUsage},
		{"an identity flag that does not apply", []string{"--check", "--project", "p"}, exitDevUsage},
		{"a positional argument", []string{"--check", "extra"}, exitDevUsage},
		{"an unreachable control plane", []string{"--check", "--api-url", "http://127.0.0.1:1"}, exitDevOperational},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setUpDevCheck(t, "")
			var out, errOut strings.Builder
			if code := runDev(streams{out: &out, err: &errOut}, tt.args); code != tt.want {
				t.Fatalf("exit %d, want %d\nstderr: %s", code, tt.want, errOut.String())
			}
			if out.String() != "" {
				t.Fatalf("stdout carried output on failure: %q", out.String())
			}
		})
	}
}

func TestHasCheckFlagStopsAtTheSeparator(t *testing.T) {
	if hasCheckFlag([]string{"--", "tool", "--check"}) {
		t.Fatal("a workload's own --check was read as dev's")
	}
	if !hasCheckFlag([]string{"--api-url", "x", "--check"}) {
		t.Fatal("--check before the separator was missed")
	}
}
