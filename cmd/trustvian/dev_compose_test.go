//go:build !windows

package main

// Tests for what `trustvian dev` composes: the state directory, helper
// resolution, and control-plane supervision.
//
// The control plane is a **fake** trustvian-local — a short script that
// publishes a discovery file and waits. That is deliberate: what is under test
// here is dev's supervision (does it wait for discoverability, does it notice an
// early exit, does it stop what it started), not the platform's server, which is
// tested in the platform module against the real thing. A test that built and
// ran the real binary would be slower, would depend on a build artifact, and
// would fail for reasons that are not dev's.
//
// Unix-only by build tag for the same reason as dev_child_unix_test.go: these
// assertions need signals and process inspection.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// State directory
// ---------------------------------------------------------------------

func TestDevStateKeyIsDeterministicAndPathSpecific(t *testing.T) {
	first := devStateKey("/home/dev/project-a")
	again := devStateKey("/home/dev/project-a")
	other := devStateKey("/home/dev/project-b")

	if first != again {
		t.Errorf("the same directory produced two keys: %q and %q", first, again)
	}
	if first == other {
		t.Error("two different directories produced one key; runs would share state")
	}
	if len(first) != devStateKeyLength {
		t.Errorf("key length = %d, want %d", len(first), devStateKeyLength)
	}
	// A key is a filesystem name: it must not need escaping.
	if strings.ContainsAny(first, `/\ .:`) {
		t.Errorf("key %q contains a character that needs escaping in a path", first)
	}
}

func TestDevStateDirIsOutsideTheWorkloadRepository(t *testing.T) {
	home := t.TempDir()
	workload := t.TempDir()

	dir, err := devStateDir(home, workload)
	if err != nil {
		t.Fatalf("devStateDir: %v", err)
	}

	// The property acceptance criterion 2 rests on: nothing dev keeps is inside
	// the directory it was run in.
	if strings.HasPrefix(dir, workload) {
		t.Fatalf("state directory %q is inside the workload directory %q", dir, workload)
	}
	if !strings.HasPrefix(dir, home) {
		t.Fatalf("state directory %q is not under the home directory %q", dir, home)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the state directory was not created: %v", err)
	}
	if mode := info.Mode().Perm(); mode != devStateDirMode {
		t.Errorf("state directory mode = %04o, want %04o", mode, devStateDirMode)
	}

	// A hashed name is unreadable by design, so the directory says what it is
	// for. Without this a developer cleaning up cannot tell five hashes apart.
	recorded, err := os.ReadFile(filepath.Join(dir, devStatePathFile))
	if err != nil {
		t.Fatalf("reading the recorded workload path: %v", err)
	}
	if got := strings.TrimSpace(string(recorded)); got != workload {
		t.Errorf("recorded workload path = %q, want %q", got, workload)
	}
}

func TestDevStateDirRefusesARelativeWorkloadDirectory(t *testing.T) {
	// A relative path would key state to wherever the process happened to be,
	// so two runs of one project could land in two directories.
	if _, err := devStateDir(t.TempDir(), "relative/path"); err == nil {
		t.Fatal("a relative workload directory was accepted")
	}
}

func TestDevStateDirIsStableAcrossCalls(t *testing.T) {
	home := t.TempDir()
	workload := t.TempDir()

	first, err := devStateDir(home, workload)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := devStateDir(home, workload)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Fatalf("two calls gave two directories: %q and %q", first, second)
	}
}

// ---------------------------------------------------------------------
// Helper resolution
// ---------------------------------------------------------------------

func TestHelperResolutionPrefersTheFlag(t *testing.T) {
	dir := t.TempDir()
	flagged := writeFakeExecutable(t, dir, "flagged")
	onEnv := writeFakeExecutable(t, dir, "from-env")
	t.Setenv(localRuntimeBinaryEnv, onEnv)

	resolved, err := helper{
		name:      localRuntimeBinary,
		flagValue: flagged,
		flagName:  "--local-bin",
		envVar:    localRuntimeBinaryEnv,
	}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != flagged {
		t.Fatalf("resolved %q, want the flag's value %q", resolved, flagged)
	}
}

func TestHelperResolutionUsesTheEnvironmentWhenNoFlag(t *testing.T) {
	onEnv := writeFakeExecutable(t, t.TempDir(), "from-env")
	t.Setenv(localRuntimeBinaryEnv, onEnv)

	resolved, err := helper{
		name:     localRuntimeBinary,
		flagName: "--local-bin",
		envVar:   localRuntimeBinaryEnv,
	}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != onEnv {
		t.Fatalf("resolved %q, want %q", resolved, onEnv)
	}
}

func TestHelperResolutionRefusesAnUnusableExplicitPath(t *testing.T) {
	dir := t.TempDir()

	// A caller who named a file meant that file. Falling back to PATH would run
	// something they did not choose.
	notExecutable := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	for _, tt := range []struct{ name, path, want string }{
		{"missing", filepath.Join(dir, "absent"), "no such file"},
		{"a directory", dir, "is a directory"},
		{"not executable", notExecutable, "is not executable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := helper{
				name:      localRuntimeBinary,
				flagValue: tt.path,
				flagName:  "--local-bin",
				envVar:    localRuntimeBinaryEnv,
			}.resolve()
			if err == nil {
				t.Fatalf("%s was accepted", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
			// An explicit path that is wrong must not be reported as "not found",
			// which would send the reader looking in the wrong places.
			if errors.Is(err, errHelperNotFound) {
				t.Error("an unusable explicit path was reported as not found")
			}
		})
	}
}

func TestHelperNotFoundExplainsTheSituation(t *testing.T) {
	// No flag, no environment, and a name nothing on PATH has.
	t.Setenv(localRuntimeBinaryEnv, "")
	_, err := helper{
		name:     "trustvian-local-no-such-binary",
		flagName: "--local-bin",
		envVar:   localRuntimeBinaryEnv,
	}.resolve()
	if err == nil {
		t.Fatal("a nonexistent helper resolved")
	}
	if !errors.Is(err, errHelperNotFound) {
		t.Errorf("error does not wrap errHelperNotFound: %v", err)
	}

	message := err.Error()
	// The developer meeting this has probably never heard of either binary, so
	// the message says what they are, where it looked, and what to do.
	for _, want := range []string{
		"not part of the released",
		localRuntimeBinary,
		collectorBinary,
		"Searched:",
		"--local-bin",
		"$" + localRuntimeBinaryEnv,
		"$PATH",
		"make dev",
		"makes no network call",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, message)
		}
	}
}

// ---------------------------------------------------------------------
// Control-plane supervision
// ---------------------------------------------------------------------

func TestStartLocalRuntimeWaitsForDiscoverability(t *testing.T) {
	stateDir := t.TempDir()
	// Publishes only after a delay, so returning early would be visible: the
	// endpoint must exist by the time Start returns, not merely eventually.
	fake := fakeLocalRuntime(t, `
sleep 0.3
printf '{"version":"1","api_url":"http://127.0.0.1:%s"}\n' "$TV_FAKE_PORT" > "$2/runtime.json"
while :; do sleep 0.05; done
`)
	t.Setenv("TV_FAKE_PORT", "45671")

	runtime, err := startLocalRuntime(context.Background(), fake, stateDir)
	if err != nil {
		t.Fatalf("startLocalRuntime: %v", err)
	}
	defer runtime.stop()

	if got, want := runtime.APIURL(), "http://127.0.0.1:45671"; got != want {
		t.Errorf("APIURL = %q, want %q", got, want)
	}
	if got, want := runtime.WebURL(), "http://127.0.0.1:45671/"; got != want {
		t.Errorf("WebURL = %q, want %q", got, want)
	}
}

func TestStartLocalRuntimeReportsAnEarlyExitWithItsLog(t *testing.T) {
	stateDir := t.TempDir()
	// The common real failure: a port taken, a database unreadable. Polling
	// alone would wait the whole timeout and report a timeout instead of this.
	fake := fakeLocalRuntime(t, `
echo "trustvian-local: binding 127.0.0.1:0: address already in use" >&2
exit 3
`)

	_, err := startLocalRuntime(context.Background(), fake, stateDir)
	if err == nil {
		t.Fatal("a runtime that exited immediately was reported as started")
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("error does not say it exited during startup: %v", err)
	}
	// The reason is in its log, so the diagnostic quotes it.
	if !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("error does not quote the runtime's own log: %v", err)
	}
}

func TestStartLocalRuntimeTimesOutWhenNothingIsPublished(t *testing.T) {
	restore := runtimeReadyTimeout
	runtimeReadyTimeout = 400 * time.Millisecond
	t.Cleanup(func() { runtimeReadyTimeout = restore })

	stateDir := t.TempDir()
	fake := fakeLocalRuntime(t, `
echo "started but publishing nothing"
while :; do sleep 0.05; done
`)

	_, err := startLocalRuntime(context.Background(), fake, stateDir)
	if err == nil {
		t.Fatal("a runtime that never published an endpoint was reported as started")
	}
	if !strings.Contains(err.Error(), "did not publish an endpoint") {
		t.Errorf("error does not say nothing was published: %v", err)
	}
}

func TestStartLocalRuntimeStopsWhatItStartedOnFailure(t *testing.T) {
	restore := runtimeReadyTimeout
	runtimeReadyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { runtimeReadyTimeout = restore })

	stateDir := t.TempDir()
	pidFile := filepath.Join(stateDir, "fake.pid")
	fake := fakeLocalRuntime(t, `
printf $$ > "$2/fake.pid"
while :; do sleep 0.05; done
`)

	if _, err := startLocalRuntime(context.Background(), fake, stateDir); err == nil {
		t.Fatal("expected a timeout")
	}

	// A failed start must not leave a process running: the caller has no handle
	// to it, so nothing else could ever stop it.
	pid := readPID(t, pidFile)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the fake runtime (pid %d) survived a failed start", pid)
}

func TestStartLocalRuntimeIgnoresAStaleDiscoveryFile(t *testing.T) {
	stateDir := t.TempDir()
	// Port 1 is not something a test can bind, so nothing answers there: the
	// file is stale by definition and must be replaced, not trusted.
	writeDiscoveryFixture(t, stateDir, "http://127.0.0.1:1")

	fake := fakeLocalRuntime(t, `
printf '{"version":"1","api_url":"http://127.0.0.1:45672"}\n' > "$2/runtime.json"
while :; do sleep 0.05; done
`)

	runtime, err := startLocalRuntime(context.Background(), fake, stateDir)
	if err != nil {
		t.Fatalf("startLocalRuntime: %v", err)
	}
	defer runtime.stop()

	if got := runtime.APIURL(); got != "http://127.0.0.1:45672" {
		t.Fatalf("APIURL = %q; a stale discovery file was trusted", got)
	}
}

func TestTwoConcurrentRuntimesDoNotCollide(t *testing.T) {
	home := t.TempDir()
	// Two workload directories, as two checkouts would be.
	firstWorkload, secondWorkload := t.TempDir(), t.TempDir()

	firstState, err := devStateDir(home, firstWorkload)
	if err != nil {
		t.Fatalf("first state dir: %v", err)
	}
	secondState, err := devStateDir(home, secondWorkload)
	if err != nil {
		t.Fatalf("second state dir: %v", err)
	}
	if firstState == secondState {
		t.Fatal("two workload directories share one state directory")
	}

	fake := fakeLocalRuntime(t, `
printf '{"version":"1","api_url":"http://127.0.0.1:%s"}\n' "$3" > "$2/runtime.json"
while :; do sleep 0.05; done
`)

	first, err := startLocalRuntimeWithPort(fake, firstState, "45681")
	if err != nil {
		t.Fatalf("first runtime: %v", err)
	}
	defer first.stop()

	second, err := startLocalRuntimeWithPort(fake, secondState, "45682")
	if err != nil {
		t.Fatalf("second runtime: %v", err)
	}
	defer second.stop()

	if first.APIURL() == second.APIURL() {
		t.Fatalf("both runtimes advertised %s", first.APIURL())
	}
	// Each state directory holds its own endpoint, so a client in either
	// directory finds the right one.
	for _, pair := range []struct{ state, url string }{
		{firstState, first.APIURL()},
		{secondState, second.APIURL()},
	} {
		got, err := readLocalDiscovery(filepath.Join(pair.state, localDiscoveryFile))
		if err != nil {
			t.Fatalf("reading discovery in %s: %v", pair.state, err)
		}
		if got != pair.url {
			t.Errorf("discovery in %s = %q, want %q", pair.state, got, pair.url)
		}
	}
}

func TestLocalRuntimeStopIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	fake := fakeLocalRuntime(t, `
printf '{"version":"1","api_url":"http://127.0.0.1:45691"}\n' > "$2/runtime.json"
while :; do sleep 0.05; done
`)

	runtime, err := startLocalRuntime(context.Background(), fake, stateDir)
	if err != nil {
		t.Fatalf("startLocalRuntime: %v", err)
	}

	// The supervisor stops it on the normal path and a deferred stop may run
	// again; a second call must not block or panic.
	runtime.stop()
	runtime.stop()
}

// ---------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------

func TestTailLogBoundsAndAnnotates(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	if got := tailLog(empty); !strings.Contains(got, "wrote nothing") {
		t.Errorf("an empty log rendered as %q", got)
	}

	if got := tailLog(filepath.Join(dir, "absent.log")); !strings.Contains(got, "could not be read") {
		t.Errorf("a missing log rendered as %q", got)
	}

	big := filepath.Join(dir, "big.log")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", logTailBytes*3)), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	// Bounded: a runaway log must not become the whole error message.
	if got := tailLog(big); len(got) > logTailBytes+64 {
		t.Errorf("tailLog returned %d bytes, want at most about %d", len(got), logTailBytes)
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// fakeLocalRuntime writes a stand-in for trustvian-local.
//
// It is invoked exactly as the real one is — `--state-dir <dir> --listen <addr>`
// — so $2 is the state directory. The body decides what this fake does about
// publishing an endpoint, which is the only behavior dev depends on.
func fakeLocalRuntime(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-trustvian-local")
	// $1 --state-dir, $2 the directory, $3 --listen, $4 the address.
	script := "#!/bin/sh\nset -- \"$1\" \"$2\" \"${TV_FAKE_PORT:-45600}\"\n" + body
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake runtime: %v", err)
	}
	return path
}

// startLocalRuntimeWithPort runs a fake that reads its port from TV_FAKE_PORT.
//
// Set around the call rather than passed through startLocalRuntime's signature,
// which takes only what the real binary needs.
func startLocalRuntimeWithPort(binary, stateDir, port string) (*localRuntime, error) {
	previous, had := os.LookupEnv("TV_FAKE_PORT")
	os.Setenv("TV_FAKE_PORT", port)
	defer func() {
		if had {
			os.Setenv("TV_FAKE_PORT", previous)
			return
		}
		os.Unsetenv("TV_FAKE_PORT")
	}()
	return startLocalRuntime(context.Background(), binary, stateDir)
}

func writeDiscoveryFixture(t *testing.T, stateDir, apiURL string) {
	t.Helper()
	payload := fmt.Sprintf(`{"version":"1","api_url":%q}`+"\n", apiURL)
	path := filepath.Join(stateDir, localDiscoveryFile)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("writing the discovery fixture: %v", err)
	}
}

func writeFakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// ---------------------------------------------------------------------
// The composed path, end to end
// ---------------------------------------------------------------------

func TestDevComposesTheRuntimeBeforeLaunchingTheChild(t *testing.T) {
	api := newFakeDevAPI(t)
	defer api.Close()

	fake := fakeRuntimeServing(t, api.URL)
	t.Setenv(localRuntimeBinaryEnv, fake)
	withFakeCollector(t, "")
	// HOME decides where state lands, so the test's own home keeps this run out
	// of the developer's real ~/.trustvian.
	home := t.TempDir()
	t.Setenv("HOME", home)
	workload := t.TempDir()
	t.Chdir(workload)

	dir := t.TempDir()
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	var out, errOut strings.Builder
	code := runDev(streams{out: &out, err: &errOut},
		append(explicitIdentityFlags(), "--", "sh", "-c", "exit 6"))

	if code != 6 {
		t.Fatalf("exit code = %d, want the child's 6\nstderr: %s", code, errOut.String())
	}

	// The banner reports what was composed and where the state is, because a
	// hashed state path is unguessable and the endpoint is ephemeral.
	banner := out.String()
	for _, want := range []string{"Trustvian dev", api.URL, home, "OTLP", "gRPC",
		"test-project", "test-agent", "test-candidate"} {
		if !strings.Contains(banner, want) {
			t.Errorf("the banner does not mention %q:\n%s", want, banner)
		}
	}

	// Acceptance criterion 2, at its narrowest: the directory dev ran in is
	// untouched. The full byte-identical assertion over a fixture repository
	// arrives with the end-to-end test in slice 5.
	entries, err := os.ReadDir(workload)
	if err != nil {
		t.Fatalf("reading the workload directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("dev wrote into the workload directory: %v", names)
	}
}

func TestDevAttachesToAnExistingRuntimeWithoutStartingOne(t *testing.T) {
	// --api-url is the CI path task 078 needs: many runs against one control
	// plane. With it, dev must not start a runtime at all — so a helper that
	// would fail if executed proves nothing executed it.
	api := newFakeDevAPI(t)
	defer api.Close()

	t.Setenv(localRuntimeBinaryEnv, filepath.Join(t.TempDir(), "does-not-exist"))
	withFakeCollector(t, "")
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	var out, errOut strings.Builder
	code := runDev(streams{out: &out, err: &errOut},
		append(explicitIdentityFlags(), "--api-url", api.URL,
			"--", "sh", "-c", "exit 0"))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "did not start") {
		t.Errorf("the banner does not say it attached:\n%s", out.String())
	}
}

func TestDevFailsBeforeTheChildWhenTheRuntimeCannotStart(t *testing.T) {
	// The spec's rule: a workload with nowhere to report has produced nothing,
	// so it must never be launched. A child that would leave a marker proves it
	// was not run.
	fake := fakeLocalRuntime(t, `
echo "trustvian-local: refusing to start" >&2
exit 3
`)
	t.Setenv(localRuntimeBinaryEnv, fake)
	t.Setenv("HOME", t.TempDir())
	workload := t.TempDir()
	t.Chdir(workload)

	dir := t.TempDir()
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	marker := filepath.Join(dir, "child-ran")
	var errOut strings.Builder
	code := runDev(streams{out: io_Discard{}, err: &errOut},
		append(explicitIdentityFlags(), "--", "sh", "-c", "printf ran > "+marker))

	if code != exitDevOperational {
		t.Fatalf("exit code = %d, want %d", code, exitDevOperational)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the workload was launched even though the control plane failed to start")
	}
	if !strings.Contains(errOut.String(), "refusing to start") {
		t.Errorf("stderr does not quote the runtime's reason:\n%s", errOut.String())
	}
}

func TestDevReportsAMissingHelperAsOperational(t *testing.T) {
	// No helper, no environment override: the case an installed user meets,
	// since the release archive ships only the trustvian binary today.
	t.Setenv(localRuntimeBinaryEnv, "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	var errOut strings.Builder
	code := runDev(streams{out: io_Discard{}, err: &errOut},
		append(explicitIdentityFlags(), "--", "true"))

	if code != exitDevOperational {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitDevOperational, errOut.String())
	}
	if !strings.Contains(errOut.String(), "make dev") {
		t.Errorf("the message does not name the way forward:\n%s", errOut.String())
	}
}

func TestDevFailsBeforeTheChildWhenTheCollectorNeverReceives(t *testing.T) {
	restore := collectorReadyTimeout
	collectorReadyTimeout = 600 * time.Millisecond
	t.Cleanup(func() { collectorReadyTimeout = restore })

	api := newFakeDevAPI(t)
	defer api.Close()

	fake := fakeRuntimeServing(t, api.URL)
	t.Setenv(localRuntimeBinaryEnv, fake)
	// Live and configured, but no receiver bound: the state that must not be
	// treated as ready, because the workload's telemetry would go nowhere and
	// nothing would say so.
	withFakeCollector(t, "health-only")
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	marker := filepath.Join(dir, "child-ran")
	var errOut strings.Builder
	code := runDev(streams{out: io_Discard{}, err: &errOut},
		append(explicitIdentityFlags(), "--", "sh", "-c", "printf ran > "+marker))

	if code != exitDevOperational {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitDevOperational, errOut.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the workload was launched even though the collector could not receive")
	}
	if !strings.Contains(errOut.String(), "receiver never bound") {
		t.Errorf("stderr does not name the unbound receiver:\n%s", errOut.String())
	}
}

// explicitIdentityFlags names identity so a test does not depend on the
// directory being a git repository.
//
// Identity resolves before any process starts — a run that cannot be named
// deterministically should fail before a control plane, a Collector or a workload
// has been launched — so a test about composition has to get past it first.
// Derivation itself is covered in dev_identity_test.go.
func explicitIdentityFlags() []string {
	return []string{
		"--project", "test-project",
		"--agent", "test-agent",
		"--candidate", "test-candidate",
	}
}

// fakeRuntimeServing writes a fake trustvian-local that advertises a real
// server's endpoint.
//
// Needed once dev provisions over /v1: a discovery file naming a port nothing
// listens on was enough while dev only read the URL, and is not once dev makes
// requests against it.
func fakeRuntimeServing(t *testing.T, apiURL string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-trustvian-local")
	script := fmt.Sprintf(`#!/bin/sh
printf '{"version":"1","api_url":"%s"}\n' > "$2/runtime.json"
while :; do sleep 0.05; done
`, apiURL)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake runtime: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------
// Signals during composition
// ---------------------------------------------------------------------

func TestAnInterruptDuringCompositionLeavesNoHelperRunning(t *testing.T) {
	// The bug this replaces: the signal handler was installed inside
	// superviseChild, after the control plane and the Collector had started. A
	// Ctrl-C during composition killed dev with the default disposition and left
	// both helpers running — and because each is in its own process group, the
	// terminal's own SIGINT never reached them. Two orphans holding ports.
	restore := runtimeReadyTimeout
	runtimeReadyTimeout = 30 * time.Second
	t.Cleanup(func() { runtimeReadyTimeout = restore })

	stateDir := t.TempDir()
	pidFile := filepath.Join(stateDir, "helper.pid")
	// Records its pid, publishes nothing, and waits — so dev is still inside
	// composition when the signal arrives.
	fake := fakeLocalRuntime(t, `
printf $$ > "$2/helper.pid"
while :; do sleep 0.05; done
`)

	relay := newSignalRelay()
	defer relay.Stop()

	done := make(chan error, 1)
	go func() {
		_, err := startLocalRuntime(relay.Context(), fake, stateDir)
		done <- err
	}()

	waitForFile(t, pidFile)
	pid := readPID(t, pidFile)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("signalling this process: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("startup succeeded despite an interrupt")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the interrupt did not stop the wait; the context is not threaded through")
	}

	// And the helper it started is gone.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("the control plane (pid %d) survived an interrupt during composition", pid)
}

func TestSignalRelayForwardsToTheWorkloadAndRemembersIt(t *testing.T) {
	relay := newSignalRelay()
	defer relay.Stop()

	if relay.Interrupted() {
		t.Fatal("a fresh relay reports an interrupt")
	}
	if relay.Forwarded() {
		t.Fatal("a fresh relay reports a forwarded signal")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "child.sh")
	marker := filepath.Join(dir, "caught")
	writeScript(t, script, `
trap 'printf caught > "$1"; exit 0' TERM
printf ready > "$2"
i=0
while [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done
exit 99
`)
	ready := filepath.Join(dir, "ready")

	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	done := make(chan childOutcome, 1)
	go func() {
		done <- superviseChild(streams{out: io_Discard{}, err: io_Discard{}},
			[]string{"sh", script, marker, ready}, nil, relay)
	}()

	waitForFile(t, ready)
	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling this process: %v", err)
	}

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the workload never saw the forwarded signal")
	}

	if got := readPath(t, marker); got != "caught" {
		t.Errorf("the workload did not receive the signal (marker %q)", got)
	}
	// Forwarded is what lets the session tell Ctrl-C from a crash.
	if !relay.Forwarded() {
		t.Error("the relay did not record that it forwarded a signal")
	}
}

// ---------------------------------------------------------------------
// The baseline file and its single writer
// ---------------------------------------------------------------------

func TestBaselineIsAFilePerProfile(t *testing.T) {
	stateDir := t.TempDir()

	first, err := acquireBaseline(stateDir, "git:abc1234")
	if err != nil {
		t.Fatalf("acquireBaseline: %v", err)
	}
	defer first.release()

	// Distinct profiles get distinct files, so two candidates never share a
	// learned baseline.
	second, err := acquireBaseline(stateDir, "git:def5678")
	if err != nil {
		t.Fatalf("a second profile was refused: %v", err)
	}
	defer second.release()

	if first.path == second.path {
		t.Fatalf("two profiles share one baseline file: %s", first.path)
	}
	// And the file lives in dev's state directory, never in the repository.
	if filepath.Dir(first.path) != stateDir {
		t.Errorf("baseline path %q is not in the state directory %q", first.path, stateDir)
	}
}

func TestBaselineFileSegmentIsInjective(t *testing.T) {
	// Two profiles that differ only in a character the filename cannot hold must
	// not collapse into one file, or they would share a baseline.
	if a, b := baselineFileSegment("git:abc"), baselineFileSegment("git+abc"); a == b {
		t.Fatalf("two profiles produced one segment: %q", a)
	}
	if got := baselineFileSegment("git:abc1234+dirty"); strings.ContainsAny(got, ":+/\\") {
		t.Errorf("segment %q still contains a character a filename cannot hold", got)
	}
}

func TestASecondRunAgainstOneProfileIsRefused(t *testing.T) {
	// The file store has no cross-process locking, so two writers would discard
	// each other's learning and leave a file belonging to neither.
	stateDir := t.TempDir()

	held, err := acquireBaseline(stateDir, "git:abc1234")
	if err != nil {
		t.Fatalf("acquireBaseline: %v", err)
	}
	defer held.release()

	_, err = acquireBaseline(stateDir, "git:abc1234")
	if err == nil {
		t.Fatal("a second run against one profile was allowed")
	}
	for _, want := range []string{"already learning", "--candidate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q:\n%v", want, err)
		}
	}
}

func TestAStaleBaselineLockIsReclaimed(t *testing.T) {
	// A crashed run leaves a lock behind. Refusing forever would make the
	// developer delete a file they were never told about.
	stateDir := t.TempDir()
	first, err := acquireBaseline(stateDir, "git:abc1234")
	if err != nil {
		t.Fatalf("acquireBaseline: %v", err)
	}
	lockPath := first.lockPath

	// A pid that cannot be alive.
	if err := os.WriteFile(lockPath, []byte("999999999\n"), 0o600); err != nil {
		t.Fatalf("writing a stale lock: %v", err)
	}

	second, err := acquireBaseline(stateDir, "git:abc1234")
	if err != nil {
		t.Fatalf("a stale lock was not reclaimed: %v", err)
	}
	second.release()
}

func TestReleasingALockThisProcessDoesNotHoldLeavesItAlone(t *testing.T) {
	stateDir := t.TempDir()
	held, err := acquireBaseline(stateDir, "p")
	if err != nil {
		t.Fatalf("acquireBaseline: %v", err)
	}
	defer held.release()

	refused, err := acquireBaseline(stateDir, "p")
	if err == nil {
		t.Fatal("a second claim succeeded")
	}
	// The refused claim must not delete the holder's lock on its way out.
	if refused != nil {
		refused.release()
	}
	if _, err := os.Stat(held.lockPath); err != nil {
		t.Fatalf("the holder's lock was removed by a refused claim: %v", err)
	}
}
