package main

// The `trustvian tui` command.
//
// Wiring only: validate flags, build the two clients, run the program, map the
// outcome to an exit code. Everything interesting is in tui_model.go, which is
// where it can be tested without a terminal.
//
// Read-only by construction. The three requests this command can make are the
// realtime subscription and the two authoritative reads below; there is no
// code path here that POSTs anything, and an architecture test enforces that.

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const tuiUsage = `usage:
  trustvian tui --run-id <id> [--api-url <url>]

Watch one evaluation run live. Requires a running control plane: start one
with ` + "`make local`" + ` and the endpoint is discovered automatically, or pass
--api-url to reach one elsewhere.

  q, ctrl-c   quit
  r           reconnect and resync now
  ?           toggle help`

func runTUI(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("tui")
	apiURL := fs.String("api-url", "",
		"base URL of the control-plane API (default: the local runtime in ./.trustvian)")
	runID := fs.String("run-id", "", "evaluation run to watch (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, tuiUsage, err)
	}
	// --api-url is no longer required: a local runtime advertises itself.
	// --run-id still is, because nothing can guess which run to watch.
	if err := requireFlag("run-id", *runID); err != nil {
		return usageFailure(s, tuiUsage, err)
	}

	// Authoritative reads reuse task 060's client unchanged: bounded bodies,
	// refused redirects, rejected credentials, a finite one-shot timeout.
	client, err := resolveAPIURL(*apiURL, flagWasSet(fs, "api-url"), timeout)
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, tuiUsage, err)
		}
		fmt.Fprintf(s.err, "trustvian: %v\n", err)
		return exitOperational
	}

	// The stream gets its own transport. Task 060's 30s total timeout is
	// correct for a one-shot request and would kill a healthy dashboard.
	opener := newSSEOpener(client.baseURL)

	// One context owns every network resource. Cancelling it on exit stops
	// the reader goroutine, closes the response body, and unblocks any read.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	model := newTUIModel(ctx, *runID, opener, &httpAuthoritativeReader{client: client})

	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithOutput(s.out))
	final, err := program.Run()
	if err != nil {
		fmt.Fprintf(s.err, "trustvian: %v\n", sanitizeTerminalText(err.Error()))
		return exitOperational
	}

	finished, ok := final.(*tuiModel)
	if !ok {
		return exitOperational
	}
	// The model already released its stream on the path that ended it; this
	// covers a quit while a stream is still open.
	finished.releaseStream()

	if finished.finalExitCode() == exitOperational && finished.lastError != "" {
		fmt.Fprintf(s.err, "trustvian: %s\n", finished.lastError)
	}
	return finished.finalExitCode()
}

// httpAuthoritativeReader is the durable half: two GETs, nothing else.
type httpAuthoritativeReader struct{ client *platformClient }

func (r *httpAuthoritativeReader) run(ctx context.Context, runID string) (evaluationRunDTO, error) {
	var dto evaluationRunDTO
	result, err := r.client.get(ctx, "evaluation-runs", runID)
	if err != nil {
		return dto, err
	}
	if err := checkStatus(result); err != nil {
		return dto, err
	}
	if err := requireJSONBody(result.body); err != nil {
		return dto, err
	}
	return dto, decodeJSON(result.body, &dto)
}

func (r *httpAuthoritativeReader) progress(ctx context.Context, runID string) (progressDTO, error) {
	var dto progressDTO
	result, err := r.client.get(ctx, "evaluation-runs", runID, "progress")
	if err != nil {
		return dto, err
	}
	if err := checkStatus(result); err != nil {
		return dto, err
	}
	if err := requireJSONBody(result.body); err != nil {
		return dto, err
	}
	return dto, decodeJSON(result.body, &dto)
}
