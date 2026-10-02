//go:build !windows

package main

// Identity derivation respects a suite scenario's deadline: the repository
// inspection's git queries are cancelled when it passes, no later query
// starts, and nothing — no provisioning request, Collector or workload — is
// launched afterwards. Without a deadline, inspection is unchanged.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// slowGit puts a git first on PATH that records each invocation and takes
// delay seconds to answer.
func slowGit(t *testing.T, delay string) (count string) {
	t.Helper()
	bin := t.TempDir()
	count = filepath.Join(t.TempDir(), "git-calls")
	writeScript(t, filepath.Join(bin, "git"), `printf x >> "`+count+`"
exec sleep `+delay+`
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return count
}

func TestStartupStopsAtTheDeadlineDuringIdentityDerivation(t *testing.T) {
	requireUnix(t)
	count := slowGit(t, "2") // three queries would take six seconds
	t.Setenv("HOME", t.TempDir())

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()

	markers := t.TempDir()
	collector := filepath.Join(markers, "collector.sh")
	writeScript(t, collector, `touch "`+filepath.Join(markers, "collector-ran")+`"`+"\n")

	deadline, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := composeAndRunResult(streams{out: io_Discard{}, err: io_Discard{}}, devConfig{
		command:      []string{"sh", "-c", `touch "` + filepath.Join(markers, "workload-ran") + `"`},
		apiURL:       server.URL,
		collectorBin: collector,
		agent:        "agent-x",
		deadline:     deadline,
	})
	took := time.Since(start)

	if result.code != exitDevOperational || result.runCompleted {
		t.Errorf("result %+v; want an operational stop and no completed run", result)
	}
	if took > 1500*time.Millisecond {
		t.Errorf("startup took %v after a 300ms deadline; the git queries were not cancelled", took)
	}
	calls, _ := os.ReadFile(count)
	// At most the one in flight when the deadline passed — it may have been
	// killed before it could record itself — and none after it.
	if n := strings.Count(string(calls), "x"); n > 1 {
		t.Errorf("%d git queries started; want at most 1 — none after the deadline", n)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d provisioning requests were made after the deadline", n)
	}
	for _, marker := range []string{"collector-ran", "workload-ran"} {
		if _, err := os.Stat(filepath.Join(markers, marker)); err == nil {
			t.Errorf("%s: something was launched after the deadline", marker)
		}
	}
}

// Without a deadline, a slow repository is waited for as before — every
// query runs to its answer, bounded only by its per-query timeout.
func TestInspectionWithoutADeadlineIsUnchanged(t *testing.T) {
	requireUnix(t)
	count := slowGit(t, "0.2")
	info := inspectRepository(context.Background(), t.TempDir())
	calls, _ := os.ReadFile(count)
	if n := strings.Count(string(calls), "x"); n != 3 || !info.isRepository {
		t.Errorf("%d git queries (repository %v); want all 3 answered", n, info.isRepository)
	}
}
