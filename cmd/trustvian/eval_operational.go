package main

// trustvian eval operational: GET /v1/evaluations/operational (task 087).
//
// Named for its route, as `eval compare` is for /v1/evaluations/compare: the
// route is the evaluations family's per-target operational read, and the CLI
// family mirrors the path segment rather than inventing a verb for it. It
// prints the response body unchanged in both output modes, as `trustvian
// status` does: the document is the only format there is, and a human
// rendering would be a second place to compute something.

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// runIDList is a repeatable run flag: one value per run, in the order given.
type runIDList []string

func (l *runIDList) String() string { return "" }

func (l *runIDList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func runEvalOperational(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval operational")
	common := registerCommonFlags(fs)
	var references, candidates runIDList
	fs.Var(&references, "reference-run", "reference evaluation run (required; repeat once per run)")
	fs.Var(&candidates, "candidate-run", "candidate evaluation run (required; repeat once per run)")
	after := fs.String("after", "", "exclusive page cursor, as next_after printed it")
	limit := fs.Int("limit", 0, "page size, 1..64 (server default when omitted)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if len(references) == 0 || len(candidates) == 0 {
		return usageFailure(s, evalUsage, usageErrorf("--reference-run and --candidate-run are required"))
	}

	// Forwarded, never checked against each other: which run lists can be
	// read together is the control plane's knowledge.
	query := url.Values{"reference_run_id": references, "candidate_run_id": candidates}
	if *after != "" {
		query.Set("after", *after)
	}
	if *limit != 0 {
		query.Set("limit", strconv.Itoa(*limit))
	}
	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.getQuery(ctx, query, "evaluations", "operational")
		},
		writeStatusDocument)
}
