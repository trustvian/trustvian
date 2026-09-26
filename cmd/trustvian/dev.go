package main

// `trustvian dev` runs one workload under Trustvian.
//
// This slice is the wrapper and nothing else: it launches a command, behaves
// like a well-mannered parent process, and propagates the child's status. The
// control plane, the Collector, provisioning and instrumentation ownership
// arrive in later slices, and the command says so rather than pretending to
// compose a runtime it does not yet start.
//
// The wrapper half is deliberately first. Everything else this command will do
// hangs off a correctly supervised child, and a wrapper that loses an exit code
// or orphans a process is not repairable by adding features to it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// devUsage is printed on a usage error and by --help.
//
// It documents `--` as required rather than optional. See splitDevArgs.
const devUsage = `usage:
  trustvian dev [options] -- <command> [args...]

Runs a command under Trustvian. The command is not modified and gains no
Trustvian dependency: everything is composed around it.

  --                  required separator; everything after it is the command

Options:
  -h, --help          print this message

Exit status is the command's own, so this wrapper is transparent to scripts.
Before the command starts, 2 means the invocation was wrong and 3 means this
wrapper could not start it.

Not yet composed by this build: the local control plane, the OTLP receiver,
automatic provisioning and instrumentation ownership. See
docs/tasks/v1.0/077-unified-otlp-local-dev-runtime.md.`

// runDev is the `dev` family's entry point.
//
// Returns the process exit code. It never returns an error for the caller to
// map, because this command's exit status is the child's and must not pass
// through main's error -> 1 mapping: 1 is a legitimate exit code for a
// workload, and `eval compare` already gives it a meaning of its own.
func runDev(s streams, args []string) int {
	// Platform support is settled before anything else happens. On a platform
	// where the child cannot be supervised correctly, the honest answer is to
	// refuse rather than to start something that cannot be stopped.
	if err := devPlatformSupported(); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevUsage
	}

	before, command, ok := splitDevArgs(args)
	if !ok {
		// --help is the one case where a missing -- is not a mistake.
		if hasHelpFlag(before) {
			fmt.Fprintln(s.out, devUsage)
			return exitDevOK
		}
		fmt.Fprintf(s.err,
			"trustvian dev: a -- separator is required, so the command is "+
				"unambiguous\n\n%s\n", devUsage)
		return exitDevUsage
	}
	if hasHelpFlag(before) {
		fmt.Fprintln(s.out, devUsage)
		return exitDevOK
	}

	fs := newFlagSet("dev")
	if err := fs.Parse(before); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n\n%s\n", err, devUsage)
		return exitDevUsage
	}
	// Positional arguments before -- are always a mistake: this command takes
	// its own input through flags and the workload's through --. Accepting a
	// stray one silently is how `trustvian dev python agent.py` would run
	// something nobody asked for.
	if fs.NArg() != 0 {
		fmt.Fprintf(s.err,
			"trustvian dev: unexpected argument %q before --; the command goes "+
				"after --\n\n%s\n", fs.Arg(0), devUsage)
		return exitDevUsage
	}

	if len(command) == 0 {
		fmt.Fprintf(s.err,
			"trustvian dev: no command after --\n\n%s\n", devUsage)
		return exitDevUsage
	}

	return superviseChild(s, command)
}

// splitDevArgs divides this command's own arguments from the workload's.
//
// The split is done here rather than by flag.Parse, which also stops at --,
// because the boundary has to be *required*. Without it,
//
//	trustvian dev python agent.py --json
//
// is ambiguous: --json could be the workload's or a future flag of this
// command's, and the answer would change the day such a flag is added. A
// required separator makes every existing invocation keep meaning what it
// meant.
//
// Only the first -- is consumed. A second one belongs to the workload and is
// passed through, because `sh -c ... -- --flag` is a legitimate thing to run.
func splitDevArgs(args []string) (before, command []string, ok bool) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

// hasHelpFlag reports whether help was asked for before the separator.
//
// Checked separately from flag parsing so `trustvian dev --help` works without
// a -- the user has no reason to type when they are asking what to type.
func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return true
		}
		// Anything after the first non-flag token is not ours to interpret.
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return false
}

// Exit codes for `dev`.
//
// This command is a **documented exception** to the control-plane families:
// its exit status is the child's, whatever that is, because a wrapper that
// rewrote its child's status would be unusable in a script. See
// docs/compatibility.md § CLI.
//
// The two codes below therefore only apply *before* the child starts. Once it
// has started, its status is the answer — including 2 and 3, which then mean
// whatever the workload means by them. That ambiguity is inherent to wrapping
// and is the same one `env`, `nice` and `timeout` carry; the alternative is
// discarding the child's status, which is worse.
const (
	exitDevOK    = 0
	exitDevUsage = 2
	// exitDevOperational reports that this wrapper could not start the child
	// at all — a command that does not exist, or one that cannot be executed.
	exitDevOperational = 3
)

// devCommandName renders a command for a diagnostic.
//
// Only the base name of argv[0] plus the argument count: a full command line
// can carry a token, a DSN or a fixture path, and an error message is not a
// place to widen what this tool reveals.
func devCommandName(command []string) string {
	if len(command) == 0 {
		return "(none)"
	}
	name := filepath.Base(command[0])
	if len(command) == 1 {
		return name
	}
	return fmt.Sprintf("%s (+%d arguments)", name, len(command)-1)
}

// devStdio is the child's standard streams.
//
// os.Stdin/Stdout/Stderr directly, never a pipe: the child's output must not be
// captured, interleaved or reformatted, and a pipe would make this process
// responsible for relaying it — which is where wrappers introduce buffering
// that reorders a workload's own output.
//
// A function rather than a constant so tests can substitute files and still
// exercise the real inheritance path.
var devStdio = func() (stdin, stdout, stderr *os.File) {
	return os.Stdin, os.Stdout, os.Stderr
}
