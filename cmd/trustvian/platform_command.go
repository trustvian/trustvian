package main

// The shape every platform leaf command shares.
//
// Small helpers rather than a command framework: there are four families and
// sixteen leaves, and a generic dispatcher would be more code than the thing
// it abstracts. The existing CLI uses flag; this stays with it.

import (
	"context"
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

// runLeaf performs the shape every platform leaf command shares.
func runLeaf(
	s streams, common commonFlags, usage string, timeout time.Duration,
	exchange func(context.Context, *platformClient) (apiResult, error),
	render func(io.Writer, []byte) error,
) int {
	client, err := newPlatformClient(*common.apiURL, timeout)
	if err != nil {
		return usageFailure(s, usage, err)
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

	if err := emitSuccess(s, *common.json, result.body, func(w io.Writer) error {
		return render(w, result.body)
	}); err != nil {
		return emitError(s, *common.json, err)
	}
	return exitOK
}
