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
                  [--backend sqlite|postgres]

Runs the local Trustvian control plane: persistence, realtime bus, and the /v1
HTTP API on a loopback listener. Clients in the same directory discover it
through <state-dir>/runtime.json and need no --api-url.

  --state-dir   project-local state directory (default .trustvian)
  --listen      loopback address to bind (default 127.0.0.1:0)
  --backend     persistence backend (default sqlite)

The default needs no configuration at all: SQLite, one file under --state-dir.

--backend postgres reads its connection string from the environment:

  ` + postgresDSNEnv + `   PostgreSQL connection string (required)

The DSN is read from the environment rather than a flag because it normally
carries a password, and a command line is visible to every process on the
machine through ps. It is never printed, never logged, and never written into
runtime.json.

Selecting an unknown backend, or postgres without a DSN, fails before the
listener binds. Nothing ever falls back to SQLite from a backend that was asked
for explicitly.

This runtime is unauthenticated and binds loopback only. Do not expose it.`

// postgresDSNEnv carries the PostgreSQL connection string.
//
// An environment variable rather than a flag: a DSN normally embeds a password,
// and argv is world-readable through ps on every platform this runs on. The
// engine's store takes the same value from a config file for the same reason —
// neither puts it on a command line.
const postgresDSNEnv = "TRUSTVIAN_PLATFORM_POSTGRES_DSN"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trustvian-local", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", localruntime.DefaultStateDir,
		"project-local state directory")
	listen := fs.String("listen", localruntime.DefaultListenAddress,
		"loopback address to bind")
	backend := fs.String("backend", "",
		"persistence backend: sqlite (default) or postgres")

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

	options := localruntime.Options{
		StateDir:      *stateDir,
		ListenAddress: *listen,
		Backend:       *backend,
	}
	// The DSN is attached only when PostgreSQL was asked for, so an environment
	// variable left set from something else cannot change which backend runs.
	if *backend == localruntime.BackendPostgres {
		options.Postgres = &localruntime.PostgresOptions{
			DSN: os.Getenv(postgresDSNEnv),
		}
	}

	runtime, err := localruntime.Start(ctx, options)
	if err != nil {
		// The error is printed as-is because every error this can return is
		// already free of the DSN: localruntime and the store both redact it,
		// and nothing here adds configuration back.
		fmt.Fprintf(stderr, "trustvian-local: %v\n", err)
		// A rejected listen address or a bad backend selection is the caller's
		// mistake; anything else is the environment's.
		if isListenAddressError(err) || isBackendConfigurationError(err) {
			return exitUsage
		}
		return exitOperational
	}

	fmt.Fprintf(stdout, "Trustvian local runtime\n")
	fmt.Fprintf(stdout, "API:   %s\n", runtime.APIURL())
	// The browser URL is the same origin as the API, because task 063's WebUI
	// shares this listener. Printed as its own line anyway: a developer
	// looking for somewhere to click should not have to infer that the API
	// endpoint is also a web page.
	fmt.Fprintf(stdout, "Web:   %s\n", runtime.WebURL())
	// The backend is named so an operator can confirm which one started, and
	// StateSummary is a safe description rather than a connection string.
	fmt.Fprintf(stdout, "Store: %s\n", runtime.Backend())
	fmt.Fprintf(stdout, "State: %s\n", runtime.StateSummary())
	fmt.Fprintf(stdout, "Local clients in this directory can now omit --api-url.\n")
	// No browser is launched. There is no --open flag and no OS-specific
	// launcher: a security tool that opens windows by itself is a surprise,
	// and the URL above is enough.
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

// isBackendConfigurationError reports whether startup failed because the
// backend selection was wrong — an unknown name, or postgres with no DSN.
//
// The caller's mistake, so exit 2 rather than 3. An unreachable database is the
// environment's and stays operational.
func isBackendConfigurationError(err error) bool {
	return err != nil && errors.Is(err, localruntime.ErrBackendConfiguration)
}
