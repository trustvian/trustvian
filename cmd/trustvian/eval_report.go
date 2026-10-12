package main

// trustvian eval report: GET /v1/scenario-executions/{id}/report (task 107).
//
// Prints the report — result document version 2 — exactly as the control
// plane returned it, in both output modes. The human mode adds nothing
// computed: every display string a reader needs is already in the document,
// produced by the control plane.
//
// --format md renders the same document as Markdown through internal/cirender,
// the one renderer the pull request comment uses (task 107-B): the bytes are
// trustvian-ci-render --document's for the same document, and the comment's
// with the commit, run link and exit code added after the heading.
//
// A report is a read, not a gate: it exits 0 whatever the recorded verdict
// says, as the rest of the eval family outside compare, compare-repeated and
// run does.

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/trustvian/trustvian/internal/cirender"
)

func runEvalReport(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval report")
	common := registerCommonFlags(fs)
	format := fs.String("format", "", "md renders the report as the pull request comment's Markdown")

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
	render := writeStatusDocument
	switch *format {
	case "", "json":
	case "md":
		if *common.json {
			return usageFailure(s, evalUsage, usageErrorf("--format md and --json are two different outputs"))
		}
		render = writeReportMarkdown
	default:
		return usageFailure(s, evalUsage, usageErrorf("--format is md or json, not %q", *format))
	}
	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "scenario-executions", id, "report")
		},
		render)
}

// writeReportMarkdown writes the report's Markdown, exactly as the renderer
// produced it. A document the renderer refuses, or one whose evidence cannot
// fit, still prints its no-verdict rendering, and is an operational failure:
// the control plane served something the contract does not allow.
func writeReportMarkdown(w io.Writer, body []byte) error {
	out := cirender.RenderDocument(body, cirender.DefaultLimits)
	if _, err := io.WriteString(w, out.Markdown); err != nil {
		return operationalErrorf("writing output: %v", err)
	}
	if out.State == cirender.StateNoVerdict {
		return operationalErrorf("the report could not be rendered: %s", out.Reason)
	}
	return nil
}
