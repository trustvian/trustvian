package main

// Locating the executables `trustvian dev` supervises.
//
// dev composes two processes it does not contain: the local control plane and
// the OTLP Collector. Both live in repository-internal modules — the root CLI
// must not import trustvian-platform (ADR 0022, 0033, 0035), and
// scripts/check-platform-boundary.sh is a release gate that checks the module
// graph — so they are separate binaries found on disk rather than packages
// linked in.
//
// That is the whole reason this file exists. The alternative, moving their code
// into the root module, is the boundary reversal ADR 0035 §1 was written to
// refuse, and it names this kind of pressure explicitly.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// helperNames are the executables dev needs, in the order it starts them.
const (
	localRuntimeBinary = "trustvian-local"
	collectorBinary    = "trustvian-collector"
)

// Environment variables that name a helper explicitly.
//
// The documented path from a repository checkout: `make dev` sets these, so
// repository layout knowledge stays in the Makefile rather than being guessed
// at by a released binary.
const (
	localRuntimeBinaryEnv = "TRUSTVIAN_LOCAL_BIN"
	collectorBinaryEnv    = "TRUSTVIAN_COLLECTOR_BIN"
)

// errHelperNotFound marks a helper that could not be located.
//
// A sentinel so the caller classifies this as operational without matching on
// wording.
var errHelperNotFound = errors.New("helper binary not found")

// helper describes one executable dev needs and how it may be named.
type helper struct {
	// name is the executable's base name, used for the PATH lookup and in
	// diagnostics.
	name string

	// role is what it does, for the error message. A developer meeting this
	// error has probably never heard of either binary.
	role string

	// flagValue is an explicit --local-bin / --collector-bin, or empty.
	flagValue string

	// flagName is what to call that flag in a diagnostic.
	flagName string

	// envVar names the environment variable that may point at it.
	envVar string
}

// resolve finds the executable, or explains every place it looked.
//
// Order, most explicit first:
//
//  1. the flag              a caller who said where it is
//  2. the environment       what `make dev` sets
//  3. beside this binary    what a release archive shipping all three would
//     satisfy with no configuration at all
//  4. $PATH                 an installation that put them there
//
// Step 3 is the one worth keeping even though no release ships that way today:
// it costs nothing now and is the whole mechanism if the archive ever grows.
// Nothing searches the working directory or infers a repository layout — a tool
// that guessed would find the wrong build of itself in someone's checkout.
func (h helper) resolve() (string, error) {
	if h.flagValue != "" {
		// An explicit path is used as given and is an error if unusable: a
		// caller who named a file meant that file, and falling back would run
		// something they did not choose.
		if err := executableAt(h.flagValue); err != nil {
			return "", fmt.Errorf("%s %s: %w", h.flagName, h.flagValue, err)
		}
		return h.flagValue, nil
	}

	searched := []string{
		fmt.Sprintf("  %-22s not given", h.flagName),
	}

	if fromEnv := os.Getenv(h.envVar); fromEnv != "" {
		if err := executableAt(fromEnv); err != nil {
			return "", fmt.Errorf("$%s %s: %w", h.envVar, fromEnv, err)
		}
		return fromEnv, nil
	}
	searched = append(searched, fmt.Sprintf("  $%-21s not set", h.envVar))

	if beside, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(beside), h.name)
		if executableAt(candidate) == nil {
			return candidate, nil
		}
		searched = append(searched, fmt.Sprintf("  %-22s alongside this executable — not found",
			filepath.Dir(beside)))
	}

	if onPath, err := exec.LookPath(h.name); err == nil {
		return onPath, nil
	}
	searched = append(searched, fmt.Sprintf("  %-22s not found", "$PATH"))

	return "", fmt.Errorf("%w\n\n%s", errHelperNotFound, h.notFoundMessage(searched))
}

// notFoundMessage explains the situation rather than just reporting it.
//
// A developer who installed the released binary has only `trustvian`, and the
// honest thing is to say so and name the way forward — not to hint that
// something is misconfigured.
func (h helper) notFoundMessage(searched []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cannot find %q.\n\n", h.name)
	b.WriteString("dev supervises two helper processes that are not part of the released\n")
	b.WriteString("trustvian binary:\n\n")
	fmt.Fprintf(&b, "  %-20s the local control plane\n", localRuntimeBinary)
	fmt.Fprintf(&b, "  %-20s the OTLP receiver and the Trustvian processor\n", collectorBinary)
	b.WriteString("\nSearched:\n")
	for _, line := range searched {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\nFrom a repository checkout:\n\n")
	b.WriteString("  make dev ARGS='-- python agent.py'\n\n")
	b.WriteString("Nothing is downloaded. trustvian dev makes no network call.")
	return b.String()
}

// executableAt reports whether a path names a file this process can execute.
//
// Checked before use rather than discovered at exec time, so a misconfigured
// path is reported as a misconfigured path instead of as a failed launch.
// A directory is rejected explicitly: exec's own error for one is
// "permission denied", which sends a reader looking at file modes.
func executableAt(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("is a directory, not an executable")
	}
	if info.Mode()&0o111 == 0 {
		return errors.New("is not executable")
	}
	return nil
}
