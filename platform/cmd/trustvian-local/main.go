// Command trustvian-local runs the integrated local control plane.
//
// A repository-internal executable, not a shipped artifact: `trustvian` is the
// released binary, and this is what `make local` starts so the CLI and TUI
// have something to talk to.
//
// It owns flags, signals and process exit. Everything else — the store, bus,
// control plane, handler, listener and their order — belongs to
// localruntime. It contains no commands of its own: `project create` already
// exists in `trustvian`, and a second implementation would drift.
//
// Foreground only. No background mode, PID file, systemd unit, launchd job or
// Windows service; the shell owns backgrounding.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"trustvian-platform/localruntime"
)

// Exit codes match the platform command families: 0 clean, 2 invocation, 3
// operational. Never 1 — that means gate FAIL for `trustvian eval compare`,
// and nothing here may be mistaken for a policy result.
const (
	exitOK          = 0
	exitUsage       = 2
	exitOperational = 3
)

const usage = `usage:
  trustvian-local [--state-dir <dir>] [--listen <loopback-address>]

Runs the local Trustvian control plane: SQLite, realtime bus, and the /v1 HTTP
API on a loopback listener. Clients in the same directory discover it through
<state-dir>/runtime.json and need no --api-url.

  --state-dir   project-local state directory (default .trustvian)
  --listen      loopback address to bind (default 127.0.0.1:0)

This runtime is unauthenticated and binds loopback only. Do not expose it.`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trustvian-local", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", localruntime.DefaultStateDir,
		"project-local state directory")
	listen := fs.String("listen", localruntime.DefaultListenAddress,
		"loopback address to bind")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "trustvian-local: %v\n%s\n", err, usage)
		return exitUsage
	}
	// No positional arguments: every input is a flag, so a leftover is always
	// a mistake and must not be ignored.
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "trustvian-local: unexpected argument %q\n%s\n",
			fs.Arg(0), usage)
		return exitUsage
	}

	// One cancellation path, owned here. The runtime package takes a context
	// and never installs a signal handler of its own.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime, err := localruntime.Start(ctx, localruntime.Options{
		StateDir:      *stateDir,
		ListenAddress: *listen,
	})
	if err != nil {
		fmt.Fprintf(stderr, "trustvian-local: %v\n", err)
		// A rejected listen address is the caller's mistake; anything else is
		// the environment's.
		if isListenAddressError(err) {
			return exitUsage
		}
		return exitOperational
	}

	fmt.Fprintf(stdout, "Trustvian local runtime\n")
	fmt.Fprintf(stdout, "API:   %s\n", runtime.APIURL())
	fmt.Fprintf(stdout, "State: %s\n", runtime.DatabasePath())
	fmt.Fprintf(stdout, "Local clients in this directory can now omit --api-url.\n")
	fmt.Fprintf(stdout, "Press Ctrl-C to stop.\n")

	// Either a signal or a server failure ends the wait.
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Wait() }()

	var result int
	select {
	case <-ctx.Done():
		fmt.Fprintf(stdout, "\nShutting down…\n")
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintf(stderr, "trustvian-local: server failed: %v\n", err)
			result = exitOperational
		}
	}

	// Shutdown runs under a fresh context: the signal that triggered it has
	// already cancelled the one above, and a cancelled context would skip the
	// graceful phase entirely.
	if err := runtime.Stop(context.Background()); err != nil {
		fmt.Fprintf(stderr, "trustvian-local: shutdown: %v\n", err)
		if result == exitOK {
			result = exitOperational
		}
	}
	return result
}

// isListenAddressError reports whether startup failed because --listen was
// refused, which is the caller's mistake rather than the environment's.
func isListenAddressError(err error) bool {
	return err != nil && errors.Is(err, localruntime.ErrListenAddress)
}
