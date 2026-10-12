package main

// trustvian eval report: GET /v1/scenario-executions/{id}/report (task 107).
//
// Prints the report — result document version 2 — exactly as the control
// plane returned it, in both output modes. The human mode adds nothing
// computed: every display string a reader needs is already in the document,
// produced by the control plane. The Markdown rendering comes from the one
// renderer (task 107-B), not from here.
//
// A report is a read, not a gate: it exits 0 whatever the recorded verdict
// says, as the rest of the eval family outside compare, compare-repeated and
// run does.

import (
	"context"
	"strings"
	"time"
)

func runEvalReport(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval report")
	common := registerCommonFlags(fs)

	// The execution id is the one positional argument, before or after the
	// flags. Anything else positional is a mistake, refused as every
	// platform leaf refuses one.
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if id == "" && fs.NArg() > 0 {
		id = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return usageFailure(s, evalUsage, err)
		}
	}
	if err := requireNoArgs(fs); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if id == "" {
		return usageFailure(s, evalUsage, usageErrorf("eval report requires an execution id"))
	}
	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "scenario-executions", id, "report")
		},
		writeStatusDocument)
}
