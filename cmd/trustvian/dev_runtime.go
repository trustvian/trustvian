package main

// Supervising the local control plane for `trustvian dev`.
//
// dev starts trustvian-local as a child process and finds its endpoint through
// the discovery file that binary already publishes — the same contract-not-import
// boundary ADR 0035 §9 established for `make local`. Nothing here imports the
// platform, and nothing here reimplements it: the runtime owns the store, the
// bus, the control plane and the listener, and dev owns only when it starts and
// when it stops.
//
// Two asymmetries with the workload child are deliberate:
//
//   - the runtime's output goes to a log file, not to the terminal. The
//     workload's stdout is the developer's; a server's startup banner
//     interleaved into it is noise they did not ask for, and it would break the
//     "output is not reformatted or interleaved" guarantee for the one stream
//     that matters.
//   - the runtime may be escalated to SIGKILL after a grace period. The
//     workload never is, because its exit status is the contract; the runtime's
//     status is nobody's contract, and a control plane that refuses to stop must
//     not hold a developer's shell open.

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// runtimeLogFile keeps the control plane's own output out of the workload's.
	runtimeLogFile = "local-runtime.log"

	// runtimePollInterval is how often the discovery file is checked.
	runtimePollInterval = 25 * time.Millisecond

	// logTailBytes bounds what a diagnostic quotes from the runtime's log.
	logTailBytes = 4 << 10

	// devLivenessProbeTimeout bounds the dial that distinguishes a live runtime
	// from a stale discovery file.
	devLivenessProbeTimeout = 500 * time.Millisecond
)

// Waiting bounds, as variables so tests can shorten them.
//
// Variables rather than constants because the alternative is a test that waits
// out a 30-second timeout to prove a timeout happens, which is how a suite
// becomes something nobody runs. Never reassigned outside tests.
var (
	// runtimeReadyTimeout bounds waiting for the endpoint to be published.
	//
	// Generous: the first start on a cold SQLite file runs migrations. A
	// developer waiting is better than a timeout that fires on a slow laptop and
	// reports a failure that did not happen.
	runtimeReadyTimeout = 30 * time.Second

	// runtimeStopGrace is how long a SIGTERM'd runtime has to exit before it is
	// killed. Long enough for a SQLite close and an HTTP shutdown, short enough
	// that Ctrl-C feels immediate.
	runtimeStopGrace = 8 * time.Second
)

// errRuntimeAlreadyServing reports that a live runtime holds the state
// directory, so this invocation must attach rather than start a second one.
//
// A sentinel so the caller classifies it without matching on wording.
var errRuntimeAlreadyServing = errors.New(
	"a local control plane is already serving this state directory")

// localRuntime is a supervised trustvian-local process.
type localRuntime struct {
	cmd     *exec.Cmd
	apiURL  string
	logPath string

	// waited carries Wait's result exactly once, so stop can reap without
	// racing a reaper that may already be running.
	waited chan error

	// stopOnce makes shutdown idempotent, the same way localruntime's own Stop
	// is. Not a tidiness measure: stop() consumes from waited, so a second call
	// would find the channel empty and wait out both timeouts — sixteen seconds
	// of nothing, on the ordinary path where a deferred stop follows an explicit
	// one. A test measures this.
	stopOnce sync.Once
}

// startLocalRuntime launches the control plane and waits until it is
// discoverable.
//
// Discoverable, not merely started: trustvian-local publishes its endpoint only
// after the listener is bound (ADR 0035), so the presence of a usable discovery
// file is proof the API exists. Returning earlier would hand the caller a URL
// that nothing is listening on.
func startLocalRuntime(binary, stateDir string) (*localRuntime, error) {
	discoveryPath := filepath.Join(stateDir, localDiscoveryFile)
	// A stale file from a previous crashed run would be read as this run's
	// endpoint. Removed before starting, so what is found afterwards was
	// published by the process just launched.
	//
	// Safe because trustvian-local refuses to start against a *live* runtime in
	// the same state directory: if one is serving, the check below reports that
	// rather than this removal hiding it.
	if err := removeStaleDiscovery(discoveryPath); err != nil {
		return nil, err
	}

	logPath := filepath.Join(stateDir, runtimeLogFile)
	logFile, err := os.OpenFile(logPath,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, devStateFileMode)
	if err != nil {
		return nil, fmt.Errorf("opening the runtime log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(binary,
		"--state-dir", stateDir,
		// Loopback with an OS-assigned port. Never a fixed number: a developer
		// whose port is taken should not have to work around a default that
		// existed for documentation (ADR 0035 §8).
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own process group, for the same reason the workload gets one: signals
	// reach it because dev sent them, not because a terminal did.
	applyChildProcessAttributes(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", filepath.Base(binary), err)
	}

	runtime := &localRuntime{cmd: cmd, logPath: logPath, waited: make(chan error, 1)}
	go func() { runtime.waited <- cmd.Wait() }()

	apiURL, err := runtime.awaitDiscovery(discoveryPath)
	if err != nil {
		// Nothing half-started is left behind: the process is stopped before
		// the error reaches the caller.
		runtime.stop()
		return nil, err
	}
	runtime.apiURL = apiURL
	return runtime, nil
}

// awaitDiscovery polls for a published endpoint, watching for an early exit.
//
// Both conditions matter. Polling alone would wait the full timeout for a
// runtime that died in the first 50ms — the common case when a port is taken or
// a database is unreadable — and report a timeout instead of the reason, which
// is in the log this function quotes.
func (r *localRuntime) awaitDiscovery(discoveryPath string) (string, error) {
	deadline := time.Now().Add(runtimeReadyTimeout)

	for {
		if url, err := readLocalDiscovery(discoveryPath); err == nil {
			return url, nil
		}

		select {
		case err := <-r.waited:
			// Put it back: stop() reads this channel too, and a value consumed
			// here would deadlock it.
			r.waited <- err
			return "", fmt.Errorf(
				"the local control plane exited during startup: %w\n%s",
				err, tailLog(r.logPath))
		default:
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf(
				"the local control plane did not publish an endpoint within %s\n%s",
				runtimeReadyTimeout, tailLog(r.logPath))
		}
		time.Sleep(runtimePollInterval)
	}
}

// APIURL is the bound control-plane endpoint.
func (r *localRuntime) APIURL() string { return r.apiURL }

// WebURL is where a browser reaches the WebUI.
//
// Derived rather than read from a second discovery field, because they are the
// same origin by construction: task 063's WebUI shares the API listener, and the
// discovery file deliberately has nothing to say about it.
func (r *localRuntime) WebURL() string { return r.apiURL + "/" }

// stop shuts the runtime down, gracefully first.
//
// SIGTERM to the group, then a bounded wait, then SIGKILL. The runtime removes
// its own discovery file on a clean exit, so the graceful path is what leaves
// the state directory in a state the next run can trust.
//
// Idempotent and safe to call after a failed start.
func (r *localRuntime) stop() {
	r.stopOnce.Do(r.stopOnceBody)
}

func (r *localRuntime) stopOnceBody() {
	if r.cmd == nil || r.cmd.Process == nil {
		return
	}

	forwardSignal(r.cmd.Process, syscall.SIGTERM)

	select {
	case <-r.waited:
		return
	case <-time.After(runtimeStopGrace):
	}

	// It did not stop. Killing it is acceptable here in a way it never is for
	// the workload: no script reads this process's exit status, and leaving it
	// holding a port and a SQLite lock would break the next run.
	_ = r.cmd.Process.Kill()
	select {
	case <-r.waited:
	case <-time.After(runtimeStopGrace):
		// Unreapable. Reported by the caller rather than silently ignored,
		// because a process this one started and cannot account for is exactly
		// what the spec's cleanup rule is about.
	}
}

// removeStaleDiscovery deletes a discovery file left by a dead runtime.
//
// Only when nothing answers at the address it names: a file describing a live
// runtime is left alone so that trustvian-local's own refusal — "a local runtime
// already appears to be serving this state directory" — is what the developer
// sees, rather than this function quietly clearing the way and producing two
// control planes on one database.
func removeStaleDiscovery(path string) error {
	url, err := readLocalDiscovery(path)
	if err != nil {
		// Missing, malformed, or not a loopback runtime URL: nothing to
		// preserve. A malformed file is removed so the next read is this run's.
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("removing a stale discovery file: %w", removeErr)
		}
		return nil
	}

	if devEndpointIsLive(url) {
		return fmt.Errorf("%w: %s\nStop it, or attach to it with --api-url %s",
			errRuntimeAlreadyServing, url, url)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing a stale discovery file: %w", err)
	}
	return nil
}

// tailLog quotes the end of a log file for a diagnostic.
//
// Bounded, and indented so it reads as quoted output rather than as this
// process's own message. An empty or unreadable log says so: "the runtime
// exited and wrote nothing" is information.
func tailLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "  (its log could not be read)"
	}
	if len(data) == 0 {
		return "  (it wrote nothing to its log)"
	}
	if len(data) > logTailBytes {
		data = data[len(data)-logTailBytes:]
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// devEndpointIsLive reports whether something answers at a discovered endpoint.
//
// A bounded TCP dial, never an API call: asking the control plane whether it is
// alive would be a mutation-shaped question, and this is the same probe
// localruntime makes for the same reason (ADR 0035 §10c). The address has
// already passed readLocalDiscovery's loopback policy, so a checked-out
// repository cannot make this dial an arbitrary host.
func devEndpointIsLive(apiURL string) bool {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, devLivenessProbeTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
