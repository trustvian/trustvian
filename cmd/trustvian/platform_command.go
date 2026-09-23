package main

// The shape every platform leaf command shares.
//
// Small helpers rather than a command framework: there are four families and
// sixteen leaves, and a generic dispatcher would be more code than the thing
// it abstracts. The existing CLI uses flag; this stays with it.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"slices"
	"time"
)

// newFlagSet builds a flag set that reports errors through the caller.
//
// Output is discarded so the CLI prints one usage block of its own rather than
// flag's default, which would land on stderr before the command decides what
// to say.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseFlags parses arguments and rejects anything left over.
//
// One function rather than a Parse call followed by a separate check, because
// the separate check is the kind a new command forgets: it compiles, it works
// for every correct invocation, and it fails only on a typo — which is exactly
// when silence is most expensive. Nothing here can parse without it.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	return requireNoArgs(fs)
}

// requireNoArgs rejects trailing positional arguments.
//
// Every platform leaf takes its input through flags, so a positional argument
// is always a mistake — a shell-quoting slip, a stray filename, a flag whose
// leading dashes were lost. Ignoring it silently is what turns
//
//	trustvian eval compare … accidental-garbage
//
// into a real gate verdict a CI job then acts on. A typo must never be
// mistaken for a PASS or a FAIL.
func requireNoArgs(fs *flag.FlagSet) error {
	if fs.NArg() != 0 {
		return usageErrorf("unexpected positional argument %q", fs.Arg(0))
	}
	return nil
}

// requireAll checks several required flags, reporting them in a stable order.
//
// Sorted rather than map order, so the same wrong invocation produces the same
// message every time — a diagnostic that reorders itself between runs is hard
// to match in a test and harder to search for in a CI log.
func requireAll(fs *flag.FlagSet, values map[string]string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := requireFlag(name, values[name]); err != nil {
			return err
		}
	}
	return nil
}

// requireJSONBody rejects a 2xx response that is not a JSON document.
//
// Every /v1 operation the CLI calls answers with a JSON body, so a successful
// status carrying something else is a server the client does not understand —
// not a success with unusual content.
//
// This runs before either output mode. Without it the two disagreed: human
// mode failed on a malformed body because its renderer decoded one, while
// --json copied the bytes through and exited 0. A machine-readable mode that
// reports success for arbitrary bytes is worse than one that has no
// validation at all, because the exit code says the output is trustworthy.
//
// Syntax only. Nothing here inspects fields, so an additive server change
// still passes untouched.
func requireJSONBody(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return operationalErrorf("server returned a successful status with an empty body")
	}
	if !json.Valid(body) {
		return operationalErrorf("server response is not valid JSON")
	}
	return nil
}

// runLeaf performs the shape every platform leaf command shares.
func runLeaf(
	s streams, common commonFlags, usage string, timeout time.Duration,
	exchange func(context.Context, *platformClient) (apiResult, error),
	render func(io.Writer, []byte) error,
) int {
	// Explicit --api-url, else the local runtime's discovery file. A missing
	// or broken runtime is operational, not usage: after task 062 the command
	// itself is valid and the environment is what failed.
	client, err := resolveAPIURL(*common.apiURL, timeout)
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, usage, err)
		}
		return emitError(s, *common.json, err)
	}

	result, err := exchange(context.Background(), client)
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, usage, err)
		}
		return emitError(s, *common.json, err)
	}
	if err := checkStatus(result); err != nil {
		return emitError(s, *common.json, err)
	}
	if err := requireJSONBody(result.body); err != nil {
		return emitError(s, *common.json, err)
	}

	if err := emitSuccess(s, *common.json, result.body, func(w io.Writer) error {
		return render(w, result.body)
	}); err != nil {
		return emitError(s, *common.json, err)
	}
	return exitOK
}
