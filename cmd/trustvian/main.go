// Command trustvian is the Trustvian CLI.
//
// Two surfaces live in one binary, deliberately different because they serve
// different things.
//
// analyze, baseline and version run the behavioral engine in process against a
// file of events — released, offline, no network. Their behavior and exit
// codes are unchanged by anything below.
//
// project, agent, candidate and eval drive the local control plane over its
// versioned /v1 HTTP API. They are an adapter: they import nothing from the
// platform module and reproduce none of its decisions. See
// docs/adr/0033-developer-cli-is-a-thin-http-adapter.md.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}

	// The platform families return their own exit code and are dispatched
	// before the legacy switch, so they never pass through the error -> 1
	// mapping below. That mapping is released behavior for analyze and
	// baseline; routing new commands through it would silently give them a
	// meaning for exit 1 that eval compare needs for itself.
	if code, handled := runPlatform(
		streams{out: os.Stdout, err: os.Stderr}, args, platformRequestTimeout); handled {
		return code
	}

	var err error
	switch args[0] {
	case "analyze":
		err = runAnalyze(args[1:])
	case "baseline":
		err = runBaseline(args[1:])
	case "version", "--version", "-v":
		err = runVersion(os.Stdout, args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "trustvian: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "trustvian: %v\n", err)
		return 1
	}
	return 0
}

// runPlatform dispatches the control-plane families.
//
// Split out so tests can drive it with buffers and a short timeout without a
// process, and without a public --timeout flag that exists only for tests.
// Returns handled=false for anything it does not own, leaving the legacy
// switch untouched.
func runPlatform(s streams, args []string, timeout time.Duration) (int, bool) {
	switch args[0] {
	case "project":
		return runProject(s, args[1:], timeout), true
	case "agent":
		return runAgent(s, args[1:], timeout), true
	case "env":
		return runEnvironment(s, args[1:], timeout), true
	case "candidate":
		return runCandidate(s, args[1:], timeout), true
	case "eval":
		return runEval(s, args[1:], timeout), true
	case "tui":
		return runTUI(s, args[1:], timeout), true
	}
	return 0, false
}

func usage(w *os.File) {
	fmt.Fprintln(w, `Trustvian - behavioral security and trust engine

Usage:
  trustvian analyze [--config <path>] [--anomaly-config <path>] [--storage-config <path>] <events.json>
      Score each event and print a report
  trustvian baseline build [--config <path>] [--anomaly-config <path>] [--storage-config <path>] <events.json>
      Learn a baseline from a corpus of events
  trustvian version
      Print version, commit revision, and build platform

Control-plane commands (see docs/platform-cli.md):
  trustvian project    create|get
  trustvian agent      create|get
  trustvian candidate  create|get
  trustvian env        create|get|list|set|archive|activate
  trustvian eval       create|get|start|complete|fail|cancel|
                       progress|ingest-state|ingest|compare
      Drive a local control plane over its /v1 HTTP API. Each supports
      --json, which writes the API's own response to stdout, and takes an
      optional --api-url; without one, a runtime started by 'make local'
      is found through ./.trustvian/runtime.json.

  trustvian tui        --run-id <id> [--api-url <url>]
      Watch one evaluation run live in the terminal. Read-only; requires
      an already-running control plane.

Exit codes differ by command family. analyze, baseline and version keep
0 success / 1 failure / 2 usage. The control-plane commands and tui use
0 success / 2 usage / 3 API or network failure, and 'eval compare'
additionally uses 1 for a gate FAIL — only there. See
docs/compatibility.md.

--config <path> loads a schema-v1 YAML policy config (see config.LoadFile)
and uses it instead of the CLI's built-in default policy. Without it,
behavior is unchanged from before this flag existed.

--anomaly-config <path> loads a schema-v1 YAML anomaly config (see
config.LoadAnomalyFile) and uses it instead of the engine's built-in
default anomaly scoring (anomaly.DefaultConfig) — this is how v0.6/v0.7
signals (transition/n-gram/Markov/delegation deviation, all opt-in and
disabled by default) get enabled from the CLI. Without it, behavior is
unchanged from before this flag existed.

--storage-config <path> loads a schema-v1 YAML storage config (see
config.LoadStorageFile) selecting where learned baselines live — this is
how a baseline survives past a single command. Without it, the CLI uses
an in-memory store and learns nothing durable, unchanged from before
this flag existed. An explicitly requested store that cannot be opened
fails the command; it never silently falls back to memory.

<events.json> is a JSON array of events, e.g.:
  [{"id":"evt-1","timestamp":"2026-01-01T12:00:00Z","actor":{"id":"svc-payment","type":"service","identity_confidence":0.95},"operation":{"category":"http","name":"POST /payment"},"target":{"name":"payment-db"},"context":{"environment":"production"}}]`)
}
