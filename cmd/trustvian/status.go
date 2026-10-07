package main

// trustvian status (task 105).
//
// One GET /v1/status, printed as the server sent it. The command adds nothing
// and decides nothing: whether a Collector is reporting, which producers are
// active and what to suggest are the control plane's answers, and this prints
// them so `trustvian status`, `trustvian dev --check` and the WebUI's Status
// view always show the same document.
//
// The JSON document is the only format in this slice. A human rendering, when
// one exists, will be drawn from the same document rather than from a second
// request.

import (
	"context"
	"fmt"
	"io"
	"time"
)

const statusUsage = `usage:
  trustvian status [--api-url <url>] [--json]

Prints the control plane's pipeline status: the Collectors reporting to it,
the producers they have seen, and what to check when nothing is arriving.
The output is the GET /v1/status document, unchanged. It exits 0 whenever
the document was read, whatever it says, and 3 when the control plane could
not be reached.` + apiURLNote

func runStatus(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("status")
	common := registerCommonFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, statusUsage, err)
	}
	return runLeaf(s, common, statusUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "status")
		},
		writeStatusDocument)
}

// writeStatusDocument prints the document as received. Both output modes print
// the same bytes, because the document is the only format there is.
func writeStatusDocument(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "%s\n", trimTrailingNewlines(body)); err != nil {
		return operationalErrorf("writing output: %v", err)
	}
	return nil
}

func trimTrailingNewlines(body []byte) []byte {
	for len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	return body
}
