//go:build !windows

package main

// A git hook cannot hold a suite member's startup past its deadline. Real git,
// with a core.fsmonitor hook that keeps git's inherited stderr open: killing
// git alone would leave Output waiting for the hook.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestAGitHookCannotHoldStartupPastTheDeadline(t *testing.T) {
	requireUnix(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	work := t.TempDir()
	pidFile, ready := filepath.Join(work, "hook.pid"), filepath.Join(work, "hook.ready")
	hook := filepath.Join(work, "fsmonitor.sh")
	// The hook answers nothing and holds git's stderr — inherited, and a pipe
	// under Output — for thirty seconds.
	writeScript(t, hook, `printf $$ > "`+pidFile+`"
printf ready > "`+ready+`"
sleep 30
`)
	gitIn(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "f")
	gitIn(t, repo, "commit", "-q", "-m", "init")
	gitIn(t, repo, "config", "core.fsmonitor", hook)
	t.Cleanup(func() {
		if pid := readPath(t, pidFile); pid != "" {
			var n int
			for _, c := range pid {
				n = n*10 + int(c-'0')
			}
			_ = syscall.Kill(n, syscall.SIGKILL)
		}
	})
	t.Chdir(repo)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()
	markers := t.TempDir()
	collector := filepath.Join(markers, "collector.sh")
	writeScript(t, collector, `touch "`+filepath.Join(markers, "collector-ran")+`"`+"\n")

	// The deadline passes once the hook is running, never before.
	deadline, expire := context.WithCancel(context.Background())
	defer expire()
	expired := make(chan time.Time, 1)
	go func() {
		limit := time.Now().Add(30 * time.Second)
		for time.Now().Before(limit) {
			if _, err := os.Stat(ready); err == nil {
				expired <- time.Now()
				expire()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		expired <- time.Time{}
	}()

	done := make(chan devResult, 1)
	go func() {
		done <- composeAndRunResult(streams{out: io_Discard{}, err: io_Discard{}}, devConfig{
			command:      []string{"sh", "-c", `touch "` + filepath.Join(markers, "workload-ran") + `"`},
			apiURL:       server.URL,
			collectorBin: collector,
			agent:        "agent-x",
			deadline:     deadline,
		})
	}()
	var result devResult
	select {
	case result = <-done:
	case <-time.After(45 * time.Second):
		t.Fatal("startup never returned: the hook held it")
	}
	returned := time.Now()
	at := <-expired
	if at.IsZero() {
		t.Fatal("fixture: the fsmonitor hook never ran")
	}
	if took := returned.Sub(at); took > gitWaitDelay+time.Second {
		t.Errorf("startup returned %v after the deadline; the bound is %v plus overhead", took, gitWaitDelay)
	}
	if result.code != exitDevOperational || result.runCompleted {
		t.Errorf("result %+v; want an operational stop", result)
	}
	pid := readPID(t, pidFile)
	limit := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(limit) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("the fsmonitor hook %d outlived the cancelled query", pid)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d provisioning requests after the deadline", n)
	}
	for _, marker := range []string{"collector-ran", "workload-ran"} {
		if _, err := os.Stat(filepath.Join(markers, marker)); err == nil {
			t.Errorf("%s: something started after the deadline", marker)
		}
	}
}
