package main

// trustvian dev --check (task 105).
//
// Composes what `dev` composes — the control plane and a Collector — without
// starting a workload, waits for the Collector's first status report, and
// prints the control plane's status document: the same bytes `trustvian
// status` prints and the WebUI's Status view renders.
//
// It creates no evaluation run. The Collector it starts has a `status:` block
// and nothing else — no `evaluation:`, no baseline — so checking the pipeline
// leaves nothing behind in the database and takes no baseline lock. That is
// why the processor's status reporting is configured separately from its
// evaluation sink.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const (
	// devCheckCollectorID names the check's Collector, distinct from a running
	// `dev` session's own so a check attached to a shared control plane with
	// --api-url never replaces that session's entry.
	devCheckCollectorID = "dev-check"

	// devCheckPollInterval is how often the check re-reads the status document
	// while it waits. A bounded wait inside one command, not a client polling a
	// surface: it stops at the first report or at devCheckWait.
	devCheckPollInterval = 100 * time.Millisecond

	// devCheckRequestTimeout bounds one status read.
	devCheckRequestTimeout = 5 * time.Second
)

// devCheckWait bounds the wait for the first report. A variable so tests can
// shorten it.
var devCheckWait = 15 * time.Second

const devCheckUsage = `usage:
  trustvian dev --check [--api-url <url>] [--local-bin <path>] [--collector-bin <path>]

Starts the local control plane and a Collector exactly as 'trustvian dev'
does, without starting a workload or creating an evaluation run. Waits for
the Collector's first status report (at most 15 s), then prints the control
plane's status document — the same JSON 'trustvian status' prints — and
stops everything it started.

Exits 0 whenever the document was printed, whatever it says; 2 for a usage
error; 3 when the control plane or the Collector could not be started.`

// hasCheckFlag reports whether --check appears before the separator.
func hasCheckFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--check" || arg == "-check" || arg == "--check=true" {
			return true
		}
	}
	return false
}

func runDevCheck(s streams, args []string) int {
	if _, command, separated := splitDevArgs(args); separated {
		if len(command) > 0 {
			fmt.Fprintf(s.err, "trustvian dev: --check runs no command; remove everything after --\n\n%s\n",
				devCheckUsage)
		} else {
			fmt.Fprintf(s.err, "trustvian dev: --check takes no -- separator\n\n%s\n", devCheckUsage)
		}
		return exitDevUsage
	}

	fs := newFlagSet("dev --check")
	fs.Bool("check", false, "check the pipeline without running a workload")
	apiURL := fs.String("api-url", "", "attach to a control plane already running instead of starting one")
	localBin := fs.String("local-bin", "", "path to "+localRuntimeBinary)
	collectorBin := fs.String("collector-bin", "", "path to "+collectorBinary)
	if err := parseFlags(fs, args); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n\n%s\n", err, devCheckUsage)
		return exitDevUsage
	}

	relay := newSignalRelayOwning(true)
	defer relay.Stop()

	workloadDir, err := resolveWorkloadDir()
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: cannot determine your home directory: %v\n", err)
		return exitDevOperational
	}
	stateDir, err := devStateDir(home, workloadDir)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}

	var runtime *localRuntime
	var otlp *collector
	defer func() {
		// The Collector before the control plane, as dev tears down: the
		// Collector reports to it.
		if otlp != nil {
			otlp.stop()
		}
		if runtime != nil {
			runtime.stop()
		}
	}()

	url := *apiURL
	if url == "" {
		binary, err := helper{
			name: localRuntimeBinary, role: "the local control plane",
			flagValue: *localBin, flagName: "--local-bin", envVar: localRuntimeBinaryEnv,
		}.resolve()
		if err != nil {
			fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
			return exitDevOperational
		}
		runtime, err = startLocalRuntime(relay.Context(), binary, stateDir)
		if err != nil {
			fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
			return exitDevOperational
		}
		url = runtime.APIURL()
	}

	// Explicit either way: the URL is the runtime this check started or the
	// one --api-url named, never a discovery file's.
	client, err := resolveAPIURL(url, true, devCheckRequestTimeout)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevUsage
	}
	// The check's own Collector is recognized by an instance that was not
	// there before it started: on a shared control plane an earlier check may
	// still be held, and its report must not be mistaken for this one's.
	previous, _, err := devCheckInstance(relay.Context(), client)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: reading the status document: %v\n", err)
		return exitDevOperational
	}

	binary, err := helper{
		name: collectorBinary, role: "the OTLP receiver",
		flagValue: *collectorBin, flagName: "--collector-bin", envVar: collectorBinaryEnv,
	}.resolve()
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}
	otlp, err = startCollector(relay.Context(), binary, stateDir, collectorConfigData{
		APIURL: url, CollectorID: devCheckCollectorID,
	})
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}

	fmt.Fprintf(s.err, "trustvian dev --check: control plane %s, OTLP receivers %s and %s; "+
		"waiting for the Collector's first status report\n",
		url, otlp.OTLPEndpoint(), otlp.OTLPGRPCEndpoint())

	body, err := awaitDevCheckReport(relay.Context(), client, previous)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: reading the status document: %v\n", err)
		return exitDevOperational
	}
	if err := writeStatusDocument(s.out, body); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}
	return exitDevOK
}

// awaitDevCheckReport re-reads the status document until it holds a report
// from the check's own Collector, or devCheckWait passes. Either way the last
// document read is returned: a check whose Collector never reported prints a
// document saying so, which is the answer the check exists to give.
func awaitDevCheckReport(ctx context.Context, client *platformClient, previous string) ([]byte, error) {
	deadline := time.Now().Add(devCheckWait)
	for {
		instance, body, err := devCheckInstance(ctx, client)
		if err != nil {
			return nil, err
		}
		if (instance != "" && instance != previous) || !time.Now().Before(deadline) {
			return body, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(devCheckPollInterval):
		}
	}
}

// devCheckInstance reads the status document and returns the instance the
// check's Collector currently reports under, or "" when it is not held, along
// with the document's bytes.
func devCheckInstance(ctx context.Context, client *platformClient) (string, []byte, error) {
	result, err := client.get(ctx, "status")
	if err != nil {
		return "", nil, err
	}
	if err := checkStatus(result); err != nil {
		return "", nil, err
	}
	if err := requireJSONBody(result.body); err != nil {
		return "", nil, err
	}
	var document struct {
		Collectors []struct {
			CollectorID string `json:"collector_id"`
			Instance    string `json:"instance"`
		} `json:"collectors"`
	}
	if err := json.Unmarshal(result.body, &document); err != nil {
		return "", nil, operationalErrorf("the status document is not valid JSON for this endpoint")
	}
	for _, c := range document.Collectors {
		if c.CollectorID == devCheckCollectorID {
			return c.Instance, result.body, nil
		}
	}
	return "", result.body, nil
}
