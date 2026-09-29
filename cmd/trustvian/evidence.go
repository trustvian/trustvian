package main

// Evidence resolution: from a finding to the observations behind it (task 085).
//
// A thin HTTP adapter like every other command family here — it holds no
// resolution logic, decides nothing about which observations support a finding,
// and computes no count. The control plane owns all of it, and the architecture
// test enforces that. See docs/adr/0033-developer-cli-is-a-thin-http-adapter.md.
//
// Two subcommands rather than one, because a finding resolves along two
// different axes and each pages separately: `behaviors` answers *which
// behavioral identities*, `observations` answers *which retained observations*.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

const evidenceUsage = `usage:
  trustvian evidence behaviors    --reference-run <id> --candidate-run <id>
                                  (--check <name> | --behavior <fingerprint>)
                                  [--side reference|candidate]
                                  [--after <cursor>] [--limit <n>]
                                  [--api-url <url>] [--json]
  trustvian evidence observations --reference-run <id> --candidate-run <id>
                                  (--check <name> | --behavior <fingerprint>)
                                  [--side reference|candidate]
                                  [--after <cursor>] [--limit <n>]
                                  [--api-url <url>] [--json]

  --check is one of the gate's own check names, as the gate prints them:
    added_behaviors  block_decisions  critical_risk_observations
    reference_evidence  candidate_evidence

  --side is required for a behavior present in both runs, because both hold
  their own observations of it and neither is the obvious one. An added or
  removed behavior needs no side. A --check never takes one.` + apiURLNote

func runEvidence(s streams, args []string, timeout time.Duration) int {
	if len(args) == 0 {
		return usageFailure(s, evidenceUsage, fmt.Errorf("evidence requires a subcommand"))
	}
	switch args[0] {
	case "behaviors":
		return runEvidenceBehaviors(s, args[1:], timeout)
	case "observations":
		return runEvidenceObservations(s, args[1:], timeout)
	default:
		return usageFailure(s, evidenceUsage,
			fmt.Errorf("unknown evidence command %q", args[0]))
	}
}

// findingFlags are the reference every evidence subcommand takes.
type findingFlags struct {
	referenceRun *string
	candidateRun *string
	check        *string
	behavior     *string
	side         *string
	after        *string
	limit        *int
}

func registerFindingFlags(fs *flag.FlagSet) findingFlags {
	return findingFlags{
		referenceRun: fs.String("reference-run", "", "reference evaluation run (required)"),
		candidateRun: fs.String("candidate-run", "", "candidate evaluation run (required)"),
		check:        fs.String("check", "", "gate check name, as the gate prints it"),
		behavior:     fs.String("behavior", "", "fingerprint id of one behavioral identity"),
		side:         fs.String("side", "", "which run's evidence: reference or candidate"),
		after:        fs.String("after", "", "exclusive page cursor"),
		limit:        fs.Int("limit", 0, "page size, 1..64 (server default when omitted)"),
	}
}

// query renders the finding as the route's query string.
//
// The CLI checks that the two run identifiers are present, because a command
// missing a required flag is a usage error and belongs here. It deliberately
// does **not** check the check/behavior combination or the check name: which
// findings exist and which of them resolve is the control plane's knowledge,
// and duplicating it here would be the second place it could be wrong.
func (f findingFlags) query() (url.Values, error) {
	if err := requireAll(nil, map[string]string{
		"reference-run": *f.referenceRun,
		"candidate-run": *f.candidateRun,
	}); err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("reference_run_id", *f.referenceRun)
	query.Set("candidate_run_id", *f.candidateRun)
	if *f.check != "" {
		query.Set("check", *f.check)
	}
	if *f.behavior != "" {
		query.Set("behavior", *f.behavior)
	}
	// Forwarded rather than interpreted. Which behaviors need a side, and which
	// sides contradict a presence, is the control plane's knowledge — a second
	// copy here would be a second place it could be wrong.
	if *f.side != "" {
		query.Set("side", *f.side)
	}
	if *f.after != "" {
		query.Set("after", *f.after)
	}
	if *f.limit != 0 {
		query.Set("limit", strconv.Itoa(*f.limit))
	}
	return query, nil
}

func runEvidenceBehaviors(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("evidence behaviors")
	common := registerCommonFlags(fs)
	finding := registerFindingFlags(fs)

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evidenceUsage, err)
	}
	query, err := finding.query()
	if err != nil {
		return usageFailure(s, evidenceUsage, err)
	}

	return runLeaf(s, common, evidenceUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.getQuery(ctx, query, "evidence", "behaviors")
		}, renderFindingBehaviors)
}

func runEvidenceObservations(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("evidence observations")
	common := registerCommonFlags(fs)
	finding := registerFindingFlags(fs)

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evidenceUsage, err)
	}
	query, err := finding.query()
	if err != nil {
		return usageFailure(s, evidenceUsage, err)
	}

	return runLeaf(s, common, evidenceUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.getQuery(ctx, query, "evidence", "observations")
		}, renderFindingObservations)
}

// ---------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------

type findingHistoryDTO struct {
	State         string `json:"state"`
	RetainedCount string `json:"retained_count"`
	Complete      bool   `json:"complete"`
}

type findingBehaviorsDTO struct {
	Status        string `json:"status"`
	Side          string `json:"side"`
	RecordedCount string `json:"recorded_count"`
	Behaviors     []struct {
		FingerprintID string `json:"fingerprint_id"`
		Presence      string `json:"presence"`
		Behavior      struct {
			OperationCategory string `json:"operation_category"`
			OperationName     string `json:"operation_name"`
			TargetName        string `json:"target_name"`
		} `json:"behavior"`
		ReferenceCount string `json:"reference_count"`
		CandidateCount string `json:"candidate_count"`
	} `json:"behaviors"`
	History   findingHistoryDTO `json:"history"`
	NextAfter string            `json:"next_after"`
}

type findingObservationsDTO struct {
	Status        string `json:"status"`
	Side          string `json:"side"`
	RecordedCount string `json:"recorded_count"`
	Exhaustive    bool   `json:"exhaustive"`
	Observations  []struct {
		Sequence      string `json:"sequence"`
		Timestamp     string `json:"timestamp"`
		FingerprintID string `json:"fingerprint_id"`
		Decision      string `json:"decision"`
		RiskLevel     string `json:"risk_level"`
		TraceID       string `json:"trace_id"`
		SpanID        string `json:"span_id"`
		DurationNanos string `json:"duration_nanos"`
		SpanStatus    string `json:"span_status"`
		Behavior      struct {
			OperationCategory string `json:"operation_category"`
			OperationName     string `json:"operation_name"`
			TargetName        string `json:"target_name"`
		} `json:"behavior"`
	} `json:"observations"`
	History   findingHistoryDTO `json:"history"`
	NextAfter string            `json:"next_after"`
}

// statusNote turns a resolution status into the sentence a developer needs.
//
// The status describes the **finding**, not the page, so an empty page can
// legitimately arrive with `resolved` — the caller has read all the evidence
// rather than found none. Those two are the same zero rows on screen, so the
// note says which, and never prints "no supporting evidence" for a page that
// simply came after the last one.
//
// The empty and none_found cases read almost identically in the payload and
// mean opposite things, which is the whole reason the status exists.
func statusNote(status string, history findingHistoryDTO, rows int) string {
	switch status {
	case "resolved":
		if rows == 0 {
			return "no further evidence on this page — the finding's evidence is " +
				"on the pages before this cursor"
		}
		return ""
	case "none_found":
		return "no supporting evidence, and the retained history is complete — " +
			"so none exists"
	case "indeterminate":
		return "no supporting evidence found, but this run's retained history is " +
			history.State + " — absence establishes nothing here"
	case "aggregate_only":
		return "this check counts an absence; there is no per-observation " +
			"evidence to link to"
	default:
		return "unrecognized resolution status " + status
	}
}

func renderFindingBehaviors(w io.Writer, body []byte) error {
	var dto findingBehaviorsDTO
	if err := decodeJSON(body, &dto); err != nil {
		return err
	}

	fmt.Fprintf(w, "STATUS  %s\n", dto.Status)
	if dto.Side != "" {
		fmt.Fprintf(w, "SIDE    %s\n", dto.Side)
	}
	fmt.Fprintf(w, "RECORDED %s\n", dto.RecordedCount)
	if note := statusNote(dto.Status, dto.History, len(dto.Behaviors)); note != "" {
		fmt.Fprintf(w, "NOTE    %s\n", note)
	}

	if len(dto.Behaviors) > 0 {
		fmt.Fprintf(w, "\nBEHAVIORS (%d)\n", len(dto.Behaviors))
		for _, b := range dto.Behaviors {
			fmt.Fprintf(w, "  %-16s  %-8s  %s · %s%s\n",
				b.FingerprintID, b.Presence,
				b.Behavior.OperationCategory, b.Behavior.OperationName,
				renderTarget(b.Behavior.TargetName))
		}
	}
	renderHistoryLine(w, dto.History)
	if dto.NextAfter != "" {
		fmt.Fprintf(w, "\nmore: --after %s\n", dto.NextAfter)
	}
	return nil
}

func renderFindingObservations(w io.Writer, body []byte) error {
	var dto findingObservationsDTO
	if err := decodeJSON(body, &dto); err != nil {
		return err
	}

	fmt.Fprintf(w, "STATUS  %s\n", dto.Status)
	if dto.Side != "" {
		fmt.Fprintf(w, "SIDE    %s\n", dto.Side)
	}
	fmt.Fprintf(w, "RECORDED %s\n", dto.RecordedCount)
	// Stated on its own line because it is the difference between "these are
	// the matches" and "these are some of the matches". It describes the
	// history, not this page: a page can be empty because it came after the
	// last match while the history remains complete.
	fmt.Fprintf(w, "EXHAUSTIVE %t\n", dto.Exhaustive)
	if note := statusNote(dto.Status, dto.History, len(dto.Observations)); note != "" {
		fmt.Fprintf(w, "NOTE    %s\n", note)
	}

	if len(dto.Observations) > 0 {
		fmt.Fprintf(w, "\nOBSERVATIONS (%d)\n", len(dto.Observations))
		for _, o := range dto.Observations {
			fmt.Fprintf(w, "  seq %-6s %-30s %-8s %-8s %s · %s%s\n",
				o.Sequence, o.Timestamp, o.Decision, o.RiskLevel,
				o.Behavior.OperationCategory, o.Behavior.OperationName,
				renderTarget(o.Behavior.TargetName))
		}
	}
	renderHistoryLine(w, dto.History)
	if dto.NextAfter != "" {
		fmt.Fprintf(w, "\nmore: --after %s\n", dto.NextAfter)
	}
	return nil
}

func renderTarget(target string) string {
	if target == "" {
		return ""
	}
	return " → " + target
}

func renderHistoryLine(w io.Writer, history findingHistoryDTO) {
	if history.State == "" {
		return
	}
	fmt.Fprintf(w, "\nHISTORY %s (%s retained)\n", history.State, history.RetainedCount)
}
