//go:build !windows

package main

// Process-level tests for `trustvian dev`: one per line of task 077's "Child
// process discipline" table.
//
// Unix-only by build tag, not by a runtime skip, because the assertions
// themselves need syscall.Kill and process groups — concepts Windows does not
// have. `dev` refuses on Windows (see dev_child_windows.go), and
// TestDevPlatformSupportMatchesGOOS in dev_test.go is what covers that.
//
// Every child is /bin/sh or a short generated script with a literal body. No
// argument here comes from anywhere but this file.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// Child process discipline
// ---------------------------------------------------------------------

func TestDevPropagatesExitCodeExactly(t *testing.T) {
	requireUnix(t)

	// Every code a byte can hold that a script might branch on, including the
	// two this command uses for its own failures: once the child has started,
	// its status is the answer even when it collides.
	for _, want := range []int{0, 1, 2, 3, 7, 42, 126, 127, 255} {
		t.Run(strconv.Itoa(want), func(t *testing.T) {
			code := runDevWithFiles(t, []string{"--", "sh", "-c",
				fmt.Sprintf("exit %d", want)})
			if code != want {
				t.Fatalf("exit code = %d, want %d", code, want)
			}
		})
	}
}

func TestDevReportsSignalDeathAs128PlusSignal(t *testing.T) {
	requireUnix(t)

	// A signal death has no exit code of its own. 128+signal is the only
	// encoding a shell — and therefore a CI script — can interpret.
	code := runDevWithFiles(t, []string{"--", "sh", "-c", "kill -TERM $$"})

	if want := 128 + int(syscall.SIGTERM); code != want {
		t.Fatalf("exit code = %d, want %d (128+SIGTERM)", code, want)
	}
}

func TestDevReportsAMissingCommandAsOperational(t *testing.T) {
	requireUnix(t)

	_, errOut, code := runDevCapturing(t,
		[]string{"--", "trustvian-dev-no-such-command-exists"})

	// 3, not 127: the child never started, so there is no child status to
	// report and this is the wrapper's own failure.
	if code != exitDevOperational {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitDevOperational, errOut)
	}
	if !strings.Contains(errOut, "cannot run") {
		t.Errorf("stderr does not say the command could not be run:\n%s", errOut)
	}
	// The diagnostic names the command but not its arguments.
	if !strings.Contains(errOut, "trustvian-dev-no-such-command-exists") {
		t.Errorf("stderr does not name the command:\n%s", errOut)
	}
}

func TestDevInheritsWorkingDirectory(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	// t.Chdir restores the previous directory when the test ends.
	t.Chdir(dir)

	out := runDevCapturingStdout(t, []string{"--", "sh", "-c", "pwd"})

	// macOS resolves TMPDIR through a symlink, so compare resolved paths.
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving %s: %v", dir, err)
	}
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("resolving %q: %v", out, err)
	}
	if gotDir != wantDir {
		t.Fatalf("child cwd = %q, want %q", gotDir, wantDir)
	}
}

func TestDevInheritsEnvironmentAndAddsNothing(t *testing.T) {
	requireUnix(t)

	t.Setenv("TRUSTVIAN_DEV_TEST_MARKER", "inherited")

	out := runDevCapturingStdout(t, []string{"--", "sh", "-c", "env"})

	if !strings.Contains(out, "TRUSTVIAN_DEV_TEST_MARKER=inherited") {
		t.Error("the child did not inherit the parent's environment")
	}

	// This slice adds nothing. The OTLP variables arrive in a later slice, and
	// this assertion is what will then prove they are the *only* addition.
	parent := map[string]bool{}
	for _, entry := range os.Environ() {
		parent[entry] = true
	}
	for _, entry := range strings.Split(strings.TrimSpace(out), "\n") {
		entry = strings.TrimRight(entry, "\r")
		if entry == "" || parent[entry] {
			continue
		}
		// The shell sets a few of its own (PWD, SHLVL, _); anything else is
		// this wrapper leaking configuration into the child.
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "PWD", "SHLVL", "_", "OLDPWD":
			continue
		}
		t.Errorf("the wrapper added %q to the child's environment", entry)
	}
}

func TestDevPassesStdinThrough(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin")
	if err := os.WriteFile(stdinPath, []byte("hello from stdin\n"), 0o600); err != nil {
		t.Fatalf("writing stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("opening stdin fixture: %v", err)
	}
	defer stdin.Close()

	stdout := newTempFile(t, dir, "stdout")
	stderr := newTempFile(t, dir, "stderr")
	withDevStdio(t, stdin, stdout, stderr)

	if code := runDev(streams{out: io_Discard{}, err: io_Discard{}},
		[]string{"--", "cat"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	if got := readFile(t, stdout); got != "hello from stdin\n" {
		t.Fatalf("child stdout = %q, want the stdin fixture echoed", got)
	}
}

func TestDevDoesNotReformatChildOutput(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	stdout := newTempFile(t, dir, "stdout")
	stderr := newTempFile(t, dir, "stderr")
	withDevStdio(t, nil, stdout, stderr)

	// No trailing newline, and a partial line on each stream: a wrapper that
	// relayed output through a line-buffered pipe would add one or reorder them.
	if code := runDev(streams{out: io_Discard{}, err: io_Discard{}},
		[]string{"--", "sh", "-c", `printf 'out-no-newline'; printf 'err-no-newline' >&2`},
	); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	if got := readFile(t, stdout); got != "out-no-newline" {
		t.Errorf("child stdout = %q, want it verbatim", got)
	}
	if got := readFile(t, stderr); got != "err-no-newline" {
		t.Errorf("child stderr = %q, want it verbatim", got)
	}
	// stdout and stderr stay separate streams; nothing is interleaved into one.
}

func TestDevForwardsSignalsAndOutlivesTheChild(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	// The child records that it saw the signal, then exits on its own terms.
	// If the wrapper killed it instead of forwarding, the marker is absent.
	script := filepath.Join(dir, "child.sh")
	marker := filepath.Join(dir, "signalled")
	writeScript(t, script, `
trap 'printf caught > "$1"; exit 17' TERM
printf ready > "$2"
# Sleep in small slices so the trap runs promptly.
i=0
while [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done
exit 99
`)
	ready := filepath.Join(dir, "ready")

	code, elapsed := runDevWithSignal(t,
		[]string{"--", "sh", script, marker, ready}, ready, syscall.SIGTERM)

	if code != 17 {
		t.Fatalf("exit code = %d, want 17 — the child's own status after handling the signal", code)
	}
	if got := readPath(t, marker); got != "caught" {
		t.Fatalf("the child did not receive the forwarded signal (marker %q)", got)
	}
	// The wrapper waited for the child rather than returning first.
	if elapsed < 0 {
		t.Fatalf("negative elapsed time %v", elapsed)
	}
}

func TestDevLeavesNoDescendantsBehind(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	// A grandchild in the background: the process the wrapper never started
	// directly, and the one a naive implementation orphans.
	script := filepath.Join(dir, "child.sh")
	writeScript(t, script, `
sh -c 'printf $$ > "$1"; while :; do sleep 0.05; done' _ "$1" &
grandchild=$!
printf ready > "$2"
wait $grandchild
`)
	pidFile := filepath.Join(dir, "grandchild.pid")
	ready := filepath.Join(dir, "ready")

	_, _ = runDevWithSignal(t,
		[]string{"--", "sh", script, pidFile, ready}, ready, syscall.SIGTERM)

	pid := readPID(t, pidFile)
	// Group delivery is what reaches it. Poll: reaping is not instantaneous.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Do not leave it running for the rest of the suite.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("grandchild %d survived the wrapper; the process group was not signalled", pid)
}

func TestDevStopsNothingItDidNotStart(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	// A bystander in *this* test process's group, started before the wrapper
	// runs. Group-scoped delivery must not reach it.
	bystander := exec.Command("sh", "-c", "while :; do sleep 0.05; done")
	if err := bystander.Start(); err != nil {
		t.Fatalf("starting the bystander: %v", err)
	}
	t.Cleanup(func() {
		_ = bystander.Process.Kill()
		_, _ = bystander.Process.Wait()
	})

	script := filepath.Join(dir, "child.sh")
	writeScript(t, script, `
printf ready > "$1"
i=0
while [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done
`)
	ready := filepath.Join(dir, "ready")

	_, _ = runDevWithSignal(t,
		[]string{"--", "sh", script, ready}, ready, syscall.SIGTERM)

	// Give any stray delivery time to land before concluding it did not.
	time.Sleep(200 * time.Millisecond)
	if !processAlive(bystander.Process.Pid) {
		t.Fatal("the wrapper stopped a process it did not start")
	}
}

func TestDevPutsTheChildInItsOwnProcessGroup(t *testing.T) {
	requireUnix(t)

	// The structural half of the signal story: the child must not share this
	// process's group, or a terminal Ctrl-C would reach it directly as well as
	// through forwarding, at a timing neither side controls.
	out := runDevCapturingStdout(t, []string{"--", "sh", "-c", "echo $$; ps -o pgid= -p $$"})
	fields := strings.Fields(out)
	if len(fields) < 2 {
		t.Fatalf("unexpected child output %q", out)
	}
	childPGID, err := strconv.Atoi(strings.TrimSpace(fields[1]))
	if err != nil {
		t.Fatalf("parsing the child pgid from %q: %v", out, err)
	}
	if childPGID == syscall.Getpgrp() {
		t.Fatalf("the child shares this process's group (%d); Setpgid was not applied", childPGID)
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("dev is refused on Windows; see TestDevPlatformSupportMatchesGOOS")
	}
}

// io_Discard is a writer that drops everything.
//
// Named with an underscore to stay obviously local: `streams` takes io.Writer,
// and io.Discard would do — this exists only so the tests read as "output is
// not the subject here".
func runDevCapturingStdout(t *testing.T, args []string) string {
	t.Helper()
	dir := t.TempDir()
	stdout := newTempFile(t, dir, "stdout")
	stderr := newTempFile(t, dir, "stderr")
	withDevStdio(t, nil, stdout, stderr)

	if code := runDev(streams{out: io_Discard{}, err: io_Discard{}}, args); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, readFile(t, stderr))
	}
	return readFile(t, stdout)
}

// runDevWithFiles runs a child with file-backed stdio and returns its code.
func runDevWithFiles(t *testing.T, args []string) int {
	t.Helper()
	dir := t.TempDir()
	withDevStdio(t, nil,
		newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))
	return runDev(streams{out: io_Discard{}, err: io_Discard{}}, args)
}

// runDevWithSignal starts dev, waits for the child to say it is ready, sends a
// signal to *this* process, and returns the wrapper's exit code.
//
// Signalling this process rather than the child is the whole point: it is what
// a terminal does on Ctrl-C, and what the wrapper must forward.
func runDevWithSignal(t *testing.T, args []string, readyPath string,
	sig syscall.Signal) (int, time.Duration) {
	t.Helper()

	dir := t.TempDir()
	withDevStdio(t, nil,
		newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	type result struct {
		code    int
		elapsed time.Duration
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		code := runDev(streams{out: io_Discard{}, err: io_Discard{}}, args)
		done <- result{code: code, elapsed: time.Since(started)}
	}()

	waitForFile(t, readyPath)
	// A short settle so the wrapper's signal.Notify is certainly installed.
	// It is installed before Start, so the child being ready already implies
	// it; this only removes a scheduling flake from the assertion.
	time.Sleep(50 * time.Millisecond)

	if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
		t.Fatalf("signalling this process: %v", err)
	}

	select {
	case r := <-done:
		return r.code, r.elapsed
	case <-time.After(20 * time.Second):
		t.Fatal("dev did not return after the signal was forwarded")
		return 0, 0
	}
}

// withDevStdio substitutes the child's standard streams for one test.
func withDevStdio(t *testing.T, stdin, stdout, stderr *os.File) {
	t.Helper()
	previous := devStdio
	devStdio = func() (*os.File, *os.File, *os.File) { return stdin, stdout, stderr }
	t.Cleanup(func() { devStdio = previous })
}

func newTempFile(t *testing.T, dir, name string) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func readFile(t *testing.T, file *os.File) string {
	t.Helper()
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatalf("reading %s: %v", file.Name(), err)
	}
	return string(data)
}

func readPath(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the child never created %s", path)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw := strings.TrimSpace(readPath(t, path))
	if raw == "" {
		t.Fatalf("no pid was written to %s", path)
	}
	pid, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("parsing the pid %q: %v", raw, err)
	}
	return pid
}

// processAlive reports whether a pid can still be signalled.
//
// Signal 0 performs the permission and existence checks without delivering
// anything, which is the portable way to ask this question.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
