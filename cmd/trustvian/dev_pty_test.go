//go:build linux || darwin

package main

// Terminal behavior, tested against a real pseudo-terminal.
//
// Two things make this harder than it looks, and both were got wrong first.
//
// **A pty is not a controlling terminal.** Opening /dev/ptmx and handing the slave
// to a child gives you a character device and nothing else: the kernel sends
// SIGTTIN and SIGINT to the foreground group *of the terminal's session*, and a
// pty this process merely opened belongs to no session. So a test that reads the
// terminal succeeds whether or not the handover happened, and writing 0x03 to the
// master delivers data rather than a signal. An earlier version of this file did
// both, and passed while proving nothing.
//
// **dev's production topology cannot be built in-process.** In production dev
// inherits the developer's session and controlling terminal and hands the terminal
// to the workload's group. `go test` has no controlling terminal at all, so
// Foreground fails with ENOTTY and dev's own retry silently takes the
// non-terminal path.
//
// So the test binary re-execs itself as a *terminal harness*: a new session
// (Setsid) that acquires the pty as its controlling terminal (Setctty), inside
// which dev runs exactly as it would in a shell. The harness is the developer's
// terminal. That is the only arrangement where these assertions mean anything.
//
// golang.org/x/sys and github.com/creack/pty would each shorten openPTY to three
// lines, and neither may be added: the root module takes on no new dependency.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Environment variables that put the re-executed test binary into harness mode.
const (
	terminalHarnessEnv       = "TRUSTVIAN_DEV_TERMINAL_HARNESS"
	terminalHarnessArgsEnv   = "TRUSTVIAN_DEV_TERMINAL_HARNESS_ARGS"
	terminalHarnessResultEnv = "TRUSTVIAN_DEV_TERMINAL_HARNESS_RESULT"
)

// harnessResult is what the harness reports back through a file.
//
// A file rather than stdout: stdout is the pty, which also carries the workload's
// own output and the terminal's echo of Ctrl-C, and parsing a result out of that
// would be guesswork.
type harnessResult struct {
	Code int `json:"code"`
}

// runTerminalHarness is the re-executed test binary acting as a terminal session.
//
// Called from TestMain before the testing framework starts. It runs dev with the
// arguments it was given, over the pty it was handed as stdio, and records the
// exit status.
func runTerminalHarness() int {
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(terminalHarnessArgsEnv)), &args); err != nil {
		fmt.Fprintf(os.Stderr, "harness: decoding arguments: %v\n", err)
		return 2
	}
	resultPath := os.Getenv(terminalHarnessResultEnv)

	// Unset before running dev, so nothing the harness starts inherits harness
	// mode. dev passes os.Environ() to its children and one of those children is
	// this same binary acting as the fake Collector — which would re-enter
	// runTerminalHarness, try to run dev again, and recurse. The first version did
	// exactly that, and the only symptom was an immediate EOF on the terminal.
	for _, name := range []string{
		terminalHarnessEnv, terminalHarnessArgsEnv, terminalHarnessResultEnv,
	} {
		if err := os.Unsetenv(name); err != nil {
			fmt.Fprintf(os.Stderr, "harness: unsetting %s: %v\n", name, err)
			return 2
		}
	}

	code := runDev(streams{out: os.Stdout, err: os.Stderr}, args)

	encoded, err := json.Marshal(harnessResult{Code: code})
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness: encoding the result: %v\n", err)
		return 2
	}
	if err := os.WriteFile(resultPath, encoded, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "harness: writing the result: %v\n", err)
		return 2
	}
	return 0
}

// ---------------------------------------------------------------------
// The defect the handover fixed
// ---------------------------------------------------------------------

func TestAWorkloadCanReadTheTerminal(t *testing.T) {
	// The original defect: Setpgid alone puts the workload in a *background*
	// process group, and a background process reading its session's controlling
	// terminal is stopped by SIGTTIN. `trustvian dev -- python -i` hung forever
	// with no output and no explanation.
	//
	// Inside the harness the pty really is the session's controlling terminal, so
	// this fails without the foreground handover rather than passing regardless.
	session := startTerminalSession(t, `
read line
printf 'read:%s\n' "$line"
`)

	session.awaitOutput(t, "Running")
	if _, err := session.master.WriteString("hello\n"); err != nil {
		t.Fatalf("writing to the terminal: %v", err)
	}

	result := session.wait(t, 40*time.Second)
	if result.Code != 0 {
		t.Fatalf("dev exited %d; the workload could not read the terminal", result.Code)
	}
	session.assertRunCompleted(t)
}

func TestCtrlCOnATerminalCompletesTheRun(t *testing.T) {
	// The regression the handover introduced. Once the workload's group is the
	// terminal's foreground group, the terminal delivers SIGINT to the workload and
	// dev never sees it — so relay.Forwarded() stays false, and the rule
	// "signalled with nothing forwarded means something else killed it" recorded a
	// developer's own Ctrl-C as a failed run.
	//
	// 0x03 on the master is the real thing: the line discipline turns ETX into
	// SIGINT for the foreground group, exactly as a keyboard does.
	session := startTerminalSession(t, `
printf 'workload-ready\n'
i=0
while [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done
`)

	session.awaitOutput(t, "workload-ready")
	if _, err := session.master.Write([]byte{0x03}); err != nil {
		t.Fatalf("writing Ctrl-C to the terminal: %v", err)
	}

	result := session.wait(t, 40*time.Second)

	// The workload's status, unmodified, as the contract says.
	if want := 128 + int(syscall.SIGINT); result.Code != want {
		t.Fatalf("dev exited %d, want %d (128+SIGINT)", result.Code, want)
	}
	// And the run is completed, not failed: the developer stopped watching, and
	// the evidence collected up to that point is real.
	session.assertRunCompleted(t)
}

// ---------------------------------------------------------------------
// What is testable without a session
// ---------------------------------------------------------------------

func TestLooksLikeTerminalDistinguishesAPipe(t *testing.T) {
	_, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	defer slave.Close()

	if !looksLikeTerminal(slave) {
		t.Error("a pty slave was not recognized as a character device")
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating a pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()
	if looksLikeTerminal(reader) {
		t.Error("a pipe was mistaken for a terminal")
	}
}

func TestANonTerminalCharacterDeviceStillRuns(t *testing.T) {
	// /dev/null is a character device and is not a controlling terminal, so the
	// mode check says yes and the handover fails with ENOTTY. The retry is what
	// keeps the workload running instead of refusing to start — asserted here
	// because the mode check is deliberately inexact.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	stdout := newTempFile(t, dir, "stdout")
	withDevStdio(t, devNull, stdout, newTempFile(t, dir, "stderr"))

	outcome := superviseChild(streams{out: io_Discard{}, err: io_Discard{}},
		[]string{"sh", "-c", "echo ran"}, nil, nil)

	if outcome.startFailed {
		t.Fatal("a /dev/null stdin prevented the workload from starting")
	}
	if outcome.code != 0 {
		t.Fatalf("exit code = %d, want 0", outcome.code)
	}
	// The retry took the non-terminal path, so no handover is claimed — which is
	// what keeps stoppedByDeveloper from treating a stray SIGINT as an interrupt.
	if outcome.terminalHandover {
		t.Error("a handover was claimed for a device that refused one")
	}
	if got := strings.TrimSpace(readFile(t, stdout)); got != "ran" {
		t.Errorf("child stdout = %q, want %q", got, "ran")
	}
}

func TestChildWithoutATerminalKeepsItsOwnGroup(t *testing.T) {
	// With no terminal there is nothing to hand over, and the workload must still
	// get its own process group so signal forwarding and group cleanup keep
	// working.
	out := runDevCapturingStdout(t, []string{"--", "sh", "-c", "ps -o pgid= -p $$"})

	childPGID := strings.TrimSpace(out)
	if childPGID == "" {
		t.Fatalf("unexpected child output %q", out)
	}
	if childPGID == strings.TrimSpace(currentProcessGroup(t)) {
		t.Fatalf("the workload shares this process's group (%s); Setpgid was not applied",
			childPGID)
	}
}

// TestStoppedByDeveloperCoversBothRoutes is the logic the pty tests exercise
// end to end, enumerated.
//
// Both routes are the same event seen from different sides, and the second one is
// the one that was missing: with the terminal handed over, dev sees no signal at
// all, so the handover plus the signal is the only evidence there is.
func TestStoppedByDeveloperCoversBothRoutes(t *testing.T) {
	tests := []struct {
		name      string
		outcome   childOutcome
		forwarded bool
		want      bool
	}{
		{
			name:      "dev forwarded it",
			outcome:   childOutcome{signaled: true, signal: syscall.SIGINT},
			forwarded: true,
			want:      true,
		},
		{
			name:    "terminal delivered SIGINT to the workload",
			outcome: childOutcome{signaled: true, signal: syscall.SIGINT, terminalHandover: true},
			want:    true,
		},
		{
			name:    "terminal went away",
			outcome: childOutcome{signaled: true, signal: syscall.SIGHUP, terminalHandover: true},
			want:    true,
		},
		{
			// Something else killed it. Not the developer, so a failure.
			name:    "killed with no handover and nothing forwarded",
			outcome: childOutcome{signaled: true, signal: syscall.SIGKILL},
			want:    false,
		},
		{
			// SIGQUIT is Ctrl-\ and is deliberately excluded: its convention is
			// "stop and dump state because something is wrong", which is a
			// different statement from "I have seen enough".
			name:    "SIGQUIT under a handover",
			outcome: childOutcome{signaled: true, signal: syscall.SIGQUIT, terminalHandover: true},
			want:    false,
		},
		{
			name:    "an ordinary non-zero exit",
			outcome: childOutcome{code: 1},
			want:    false,
		},
		{
			name:    "a clean exit",
			outcome: childOutcome{code: exitDevOK},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.outcome.stoppedByDeveloper(tt.forwarded); got != tt.want {
				t.Fatalf("stoppedByDeveloper = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The terminal session harness
// ---------------------------------------------------------------------

type terminalSession struct {
	master     *os.File
	harness    *exec.Cmd
	resultPath string
	api        *fakeDevAPI
	seen       strings.Builder
}

// startTerminalSession runs dev inside a new session owning a pty.
//
// Setsid makes the harness a session leader with no controlling terminal; Setctty
// with the child's own descriptor number then makes the pty that session's
// controlling terminal. Only then do SIGTTIN and Ctrl-C mean what they mean in a
// shell — which is the entire point of doing this rather than handing a child a
// character device and hoping.
func startTerminalSession(t *testing.T, workloadBody string) *terminalSession {
	t.Helper()

	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	t.Cleanup(func() { master.Close() })

	api := newFakeDevAPI(t)
	t.Cleanup(api.Close)

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "workload.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+workloadBody), 0o700); err != nil {
		t.Fatalf("writing the workload: %v", err)
	}
	resultPath := filepath.Join(dir, "result.json")

	args := append(explicitDevFlags(), "--api-url", api.URL, "--", "sh", script)
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding the arguments: %v", err)
	}

	harness := exec.Command(executable)
	harness.Env = append(os.Environ(),
		terminalHarnessEnv+"=1",
		terminalHarnessArgsEnv+"="+string(encodedArgs),
		terminalHarnessResultEnv+"="+resultPath,
		// The harness needs a Collector and a home of its own, exactly as the
		// in-process composition tests do.
		collectorBinaryEnv+"="+executable,
		fakeCollectorEnv+"=1",
		fakeCollectorModeEnv+"=",
		"HOME="+dir,
	)
	harness.Dir = dir
	harness.Stdin, harness.Stdout, harness.Stderr = slave, slave, slave
	// Ctty is the *child's* descriptor number, not this process's fd. slave is the
	// harness's stdin, so 0.
	harness.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	if err := harness.Start(); err != nil {
		t.Fatalf("starting the terminal harness: %v", err)
	}
	// The harness holds its own copy of the slave; this one is not needed and
	// keeping it open would hold the terminal after the harness exits.
	slave.Close()

	session := &terminalSession{
		master: master, harness: harness, resultPath: resultPath, api: api,
	}
	t.Cleanup(func() {
		if harness.Process != nil {
			_ = harness.Process.Kill()
			_, _ = harness.Process.Wait()
		}
	})
	return session
}

// awaitOutput reads the terminal until want appears.
func (s *terminalSession) awaitOutput(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	buf := make([]byte, 512)
	for time.Now().Before(deadline) {
		if strings.Contains(s.seen.String(), want) {
			return
		}
		if err := s.master.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatalf("setting a read deadline: %v", err)
		}
		n, err := s.master.Read(buf)
		if n > 0 {
			s.seen.Write(buf[:n])
			continue
		}
		if err != nil && !os.IsTimeout(err) {
			t.Fatalf("reading the terminal: %v\nsaw: %q", err, s.seen.String())
		}
	}
	t.Fatalf("the terminal never showed %q; saw %q", want, s.seen.String())
}

// wait reaps the harness and returns what it recorded.
func (s *terminalSession) wait(t *testing.T, timeout time.Duration) harnessResult {
	t.Helper()

	// Keep draining so the workload is never blocked writing to a full pty.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 512)
		for {
			_ = s.master.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, err := s.master.Read(buf)
			if n > 0 {
				s.seen.Write(buf[:n])
			}
			if err != nil && !os.IsTimeout(err) {
				return
			}
			select {
			case <-drained:
				return
			default:
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- s.harness.Wait() }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("the terminal harness did not exit; saw %q", s.seen.String())
	}

	raw, err := os.ReadFile(s.resultPath)
	if err != nil {
		t.Fatalf("the harness recorded no result: %v\nterminal showed: %q",
			err, s.seen.String())
	}
	var result harnessResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decoding the harness result: %v", err)
	}
	return result
}

// assertRunCompleted checks the run reached a terminal state of `completed`.
func (s *terminalSession) assertRunCompleted(t *testing.T) {
	t.Helper()
	completes, fails := 0, 0
	for _, path := range s.api.postOrder() {
		switch {
		case strings.HasSuffix(path, "/complete"):
			completes++
		case strings.HasSuffix(path, "/fail"):
			fails++
		}
	}
	if fails != 0 {
		t.Errorf("the run was failed %d times; a developer's interrupt is not a failure. "+
			"Terminal showed: %q", fails, s.seen.String())
	}
	if completes != 1 {
		t.Errorf("the run was completed %d times, want 1; order was %v",
			completes, s.api.postOrder())
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func currentProcessGroup(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("ps", "-o", "pgid=", "-p",
		fmt.Sprint(syscall.Getpid())).Output()
	if err != nil {
		t.Fatalf("reading this process's group: %v", err)
	}
	return string(output)
}
