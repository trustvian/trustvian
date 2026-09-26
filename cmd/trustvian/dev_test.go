package main

// Tests for `trustvian dev`'s wrapper behavior.
//
// One test per line of task 077's "Child process discipline" table, plus the
// argument-boundary rules. Process-level properties — signal delivery, orphan
// cleanup — are asserted by inspecting real processes, because they cannot be
// demonstrated any other way: a mock would assert that this package calls the
// functions it calls, not that a child actually stopped.
//
// The process-level half lives in dev_child_unix_test.go, behind a build tag:
// its assertions need syscall.Kill and process groups, which Windows does not
// have. What stays here is portable and runs on every supported platform.

import (
	"errors"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// ---------------------------------------------------------------------
// Argument boundary
// ---------------------------------------------------------------------

func TestSplitDevArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		before  []string
		command []string
		ok      bool
	}{
		{
			name:    "separator with a command",
			args:    []string{"--", "python", "agent.py"},
			before:  []string{},
			command: []string{"python", "agent.py"},
			ok:      true,
		},
		{
			name:    "flags before the separator",
			args:    []string{"--help", "--", "true"},
			before:  []string{"--help"},
			command: []string{"true"},
			ok:      true,
		},
		{
			// The property that makes -- required: a flag that collides with
			// one of trustvian's own must reach the child untouched.
			name:    "colliding flags reach the child",
			args:    []string{"--", "python", "agent.py", "--json", "--api-url", "x"},
			before:  []string{},
			command: []string{"python", "agent.py", "--json", "--api-url", "x"},
			ok:      true,
		},
		{
			// Only the first separator is consumed; `sh -c ... -- --flag` is a
			// legitimate command.
			name:    "a second separator belongs to the child",
			args:    []string{"--", "sh", "-c", "echo", "--", "--flag"},
			before:  []string{},
			command: []string{"sh", "-c", "echo", "--", "--flag"},
			ok:      true,
		},
		{
			name:    "separator with nothing after it",
			args:    []string{"--"},
			before:  []string{},
			command: []string{},
			ok:      true,
		},
		{
			name:   "no separator",
			args:   []string{"python", "agent.py"},
			before: []string{"python", "agent.py"},
			ok:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, command, ok := splitDevArgs(tt.args)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got, want := strings.Join(before, " "), strings.Join(tt.before, " "); got != want {
				t.Errorf("before = %q, want %q", got, want)
			}
			if !tt.ok {
				return
			}
			if got, want := strings.Join(command, " "), strings.Join(tt.command, " "); got != want {
				t.Errorf("command = %q, want %q", got, want)
			}
		})
	}
}

func TestDevRequiresSeparator(t *testing.T) {
	out, errOut, code := runDevCapturing(t, []string{"true"})

	if code != exitDevUsage {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitDevUsage, errOut)
	}
	if !strings.Contains(errOut, "-- separator is required") {
		t.Errorf("stderr does not explain the separator requirement:\n%s", errOut)
	}
	if out != "" {
		t.Errorf("a usage error wrote to stdout: %q", out)
	}
}

func TestDevRejectsArgumentsBeforeSeparator(t *testing.T) {
	_, errOut, code := runDevCapturing(t, []string{"stray", "--", "true"})

	if code != exitDevUsage {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitDevUsage, errOut)
	}
	if !strings.Contains(errOut, `unexpected argument "stray"`) {
		t.Errorf("stderr does not name the stray argument:\n%s", errOut)
	}
}

func TestDevRejectsEmptyCommand(t *testing.T) {
	_, errOut, code := runDevCapturing(t, []string{"--"})

	if code != exitDevUsage {
		t.Fatalf("exit code = %d, want %d", code, exitDevUsage)
	}
	if !strings.Contains(errOut, "no command after --") {
		t.Errorf("stderr does not say the command is missing:\n%s", errOut)
	}
}

func TestDevHelpGoesToStdoutAndSucceeds(t *testing.T) {
	// --help without a separator is not a mistake: it is what someone types to
	// find out that a separator is needed.
	out, errOut, code := runDevCapturing(t, []string{"--help"})

	if code != exitDevOK {
		t.Fatalf("exit code = %d, want %d", code, exitDevOK)
	}
	if !strings.Contains(out, "trustvian dev [options] -- <command>") {
		t.Errorf("stdout is not the usage block:\n%s", out)
	}
	if errOut != "" {
		t.Errorf("--help wrote to stderr: %q", errOut)
	}
}

// ---------------------------------------------------------------------
// Platform support
// ---------------------------------------------------------------------

func TestDevPlatformSupportMatchesGOOS(t *testing.T) {
	err := devPlatformSupported()

	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("dev must refuse on Windows rather than supervise a child it cannot signal")
		}
		for _, want := range []string{"not supported on Windows", "WSL2"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal does not mention %q:\n%s", want, err)
			}
		}
		return
	}
	if err != nil {
		t.Fatalf("dev is supported on %s but refused: %v", runtime.GOOS, err)
	}
}

// ---------------------------------------------------------------------
// Exit-code mapping, without a process
// ---------------------------------------------------------------------

func TestChildExitCodeClassification(t *testing.T) {
	s := streams{out: io_Discard{}, err: io_Discard{}}

	if got := childExitCode(s, []string{"true"}, nil); got != exitDevOK {
		t.Errorf("nil error mapped to %d, want %d", got, exitDevOK)
	}

	// A wait failure is not a child status: nothing was observed, so it must
	// not be reported as a code the child could have produced.
	got := childExitCode(s, []string{"true"}, errors.New("wait failed"))
	if got != exitDevOperational {
		t.Errorf("a wait failure mapped to %d, want %d", got, exitDevOperational)
	}
}

func TestSignalExitCodeEncoding(t *testing.T) {
	if got, want := signalExitCode(syscall.SIGINT), 128+int(syscall.SIGINT); got != want {
		t.Errorf("SIGINT -> %d, want %d", got, want)
	}
	if got, want := signalExitCode(syscall.SIGTERM), 128+int(syscall.SIGTERM); got != want {
		t.Errorf("SIGTERM -> %d, want %d", got, want)
	}
	// Out of range cannot alias a real exit code.
	if got := signalExitCode(syscall.Signal(200)); got != exitDevOperational {
		t.Errorf("an out-of-range signal -> %d, want %d", got, exitDevOperational)
	}
}

func TestDevCommandNameOmitsArguments(t *testing.T) {
	// A command line can carry a token, a DSN or a fixture path. A diagnostic
	// is not a place to widen what this tool reveals.
	got := devCommandName([]string{"/usr/bin/python3", "agent.py", "--secret", "hunter2"})
	if strings.Contains(got, "hunter2") || strings.Contains(got, "agent.py") {
		t.Fatalf("devCommandName leaked arguments: %q", got)
	}
	if !strings.Contains(got, "python3") {
		t.Errorf("devCommandName = %q, want the base command name", got)
	}
	if !strings.Contains(got, "+3 arguments") {
		t.Errorf("devCommandName = %q, want the argument count", got)
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------
type io_Discard struct{}

func (io_Discard) Write(p []byte) (int, error) { return len(p), nil }

// runDevCapturing runs dev with buffered streams and no real stdio.
func runDevCapturing(t *testing.T, args []string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut strings.Builder
	code = runDev(streams{out: &out, err: &errOut}, args)
	return out.String(), errOut.String(), code
}

// runDevCapturingStdout runs a child and returns what it wrote to stdout.
//
// Real files rather than pipes, because the child inherits the descriptor
// directly and a pipe would need a reader draining it to avoid a deadlock —
// which is the buffering this command must not introduce.
