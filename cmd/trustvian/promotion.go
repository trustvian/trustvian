package main

// Promotion commands.
//
// A thin HTTP adapter, like every other control-plane family. It parses
// flags, issues one request, and renders what the server returned.
//
// Two things it deliberately does not do, both because the server owns them:
// it compares no environment ranks — `CanPromote` is the only implementation
// of promotion precedence and it lives in the platform — and it derives no
// outcome. `accepted` and `rejected` are read from the response, never
// computed from a gate result the CLI happens to be holding.
//
// One thing worth saying plainly in the output: Trustvian has recorded a
// decision. It has not deployed anything, and this family never says it has.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/url"
	"strconv"
	"time"
)

// maxPromotionPage mirrors the API's page bound.
//
// Duplicated rather than imported: this module must not import
// trustvian-platform (ADR 0033), so the number is stated here and the server
// remains the thing that enforces it. Asking for more is a 400, which is the
// server telling this constant it has drifted.
const maxPromotionPage = 64

const promotionUsage = `usage:
  trustvian promotion create --id <id> --reference-run <id> --candidate-run <id>
                             --target-environment <ref>
                             --max-added-behaviors <n>
                             --max-block-decisions <n>
                             --max-critical-risk-observations <n>
                             [--api-url <url>] [--json]
  trustvian promotion get    --id <id> [--api-url <url>] [--json]
  trustvian promotion list   --project-id <id> [--api-url <url>] [--json]` + apiURLNote

func runPromotion(s streams, args []string, timeout time.Duration) int {
	if len(args) == 0 {
		return usageFailure(s, promotionUsage, fmt.Errorf("promotion requires a subcommand"))
	}
	switch args[0] {
	case "create":
		return runPromotionCreate(s, args[1:], timeout)
	case "get":
		return runPromotionGet(s, args[1:], timeout)
	case "list":
		return runPromotionList(s, args[1:], timeout)
	default:
		return usageFailure(s, promotionUsage,
			fmt.Errorf("unknown promotion command %q", args[0]))
	}
}

// runPromotionCreate records one decision.
//
// The request carries no source environment, project, agent, candidate,
// verdict or timestamp: the server derives every one of them, and sending one
// is a 400 because `/v1` decodes strictly. That is the point — a caller
// cannot express an outcome it did not earn.
func runPromotionCreate(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("promotion create")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "caller-owned promotion identifier (required)")
	referenceRun := fs.String("reference-run", "", "reference evaluation run identifier (required)")
	candidateRun := fs.String("candidate-run", "", "candidate evaluation run identifier (required)")
	target := fs.String("target-environment", "", "environment to promote toward (required)")

	var addedBehaviors, blockDecisions, criticalRisk optionalUint64
	fs.Var(&addedBehaviors, "max-added-behaviors", "maximum added behaviors (required)")
	fs.Var(&blockDecisions, "max-block-decisions", "maximum block decisions (required)")
	fs.Var(&criticalRisk, "max-critical-risk-observations",
		"maximum critical-risk observations (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, promotionUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"id": *id, "reference-run": *referenceRun,
		"candidate-run": *candidateRun, "target-environment": *target}); err != nil {
		return usageFailure(s, promotionUsage, err)
	}

	// Every limit is required, and omitted is not zero. Zero is the strictest
	// possible limit, so defaulting to it would reject a caller's promotion
	// under a policy they never chose — and they would have no way to tell
	// that from a real regression.
	for _, limit := range []struct {
		name  string
		value *optionalUint64
	}{
		{"max-added-behaviors", &addedBehaviors},
		{"max-block-decisions", &blockDecisions},
		{"max-critical-risk-observations", &criticalRisk},
	} {
		if !limit.value.set {
			return usageFailure(s, promotionUsage, usageErrorf(
				"--%s is required; zero is a strict limit, not a default", limit.name))
		}
	}

	body := promotionCreateBody{
		ID:                *id,
		ReferenceRunID:    *referenceRun,
		CandidateRunID:    *candidateRun,
		TargetEnvironment: *target,
		GateLimits: gateLimitsBody{
			MaxAddedBehaviors:           addedBehaviors.canonical(),
			MaxBlockDecisions:           blockDecisions.canonical(),
			MaxCriticalRiskObservations: criticalRisk.canonical(),
		},
	}

	return runLeaf(s, common, promotionUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, body, "promotions")
		},
		renderPromotionBody)
}

func runPromotionGet(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("promotion get")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "promotion identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, promotionUsage, err)
	}
	if err := requireAll(fs, map[string]string{"id": *id}); err != nil {
		return usageFailure(s, promotionUsage, err)
	}

	return runLeaf(s, common, promotionUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "promotions", *id)
		},
		renderPromotionBody)
}

// runPromotionList pages a project's history to completion.
//
// Like `env list`, and for the same reason: the route is bounded per response,
// a project's history grows without limit, and a caller asking "what
// promotions has this project recorded" wants the answer rather than the first
// page of it.
func runPromotionList(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("promotion list")
	common := registerCommonFlags(fs)
	projectID := fs.String("project-id", "", "owning project identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, promotionUsage, err)
	}
	if err := requireAll(fs, map[string]string{"project-id": *projectID}); err != nil {
		return usageFailure(s, promotionUsage, err)
	}

	var collected promotionCollection
	return runLeaf(s, common, promotionUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return listPromotionPages(ctx, c, *projectID, &collected)
		},
		func(w io.Writer, _ []byte) error { return renderPromotionList(w, collected) })
}

// listPromotionPages walks the collection and returns one synthesized result
// carrying every row.
//
// The same stated exception `env list` documents: this is the second place in
// the CLI where `--json` does not forward one server response verbatim,
// because the command is not one request. Rows are forwarded as raw bytes and
// unknown envelope fields survive, so a field a newer server adds at either
// level is not lost. Only `promotions` and `next_after` are replaced.
//
// There is no ceiling on how many pages are followed — a project's history is
// whatever it recorded — so the loop is bounded by cursor progress instead,
// and a cursor that cannot progress is reported rather than retried.
func listPromotionPages(
	ctx context.Context, c *platformClient, projectID string, collected *promotionCollection,
) (apiResult, error) {
	after := ""
	var last apiResult

	for {
		query := url.Values{}
		query.Set("limit", strconv.Itoa(maxPromotionPage))
		if after != "" {
			query.Set("after", after)
		}

		result, err := c.getQuery(ctx, query, "projects", projectID, "promotions")
		if err != nil {
			return apiResult{}, err
		}
		if result.status < 200 || result.status >= 300 {
			return result, nil
		}

		page, err := decodePromotionPage(result.body)
		if err != nil {
			return apiResult{}, err
		}
		if err := collected.absorb(page); err != nil {
			return apiResult{}, err
		}
		last = result

		if page.nextAfter == "" {
			break
		}
		if err := validatePromotionCursor(after, page); err != nil {
			return apiResult{}, err
		}
		after = page.nextAfter
	}

	merged, err := collected.encode()
	if err != nil {
		return apiResult{}, err
	}
	last.body = merged
	return last, nil
}

// validatePromotionCursor refuses a continuation that cannot make progress.
//
// Each of these is impossible from a correct server and non-terminating if
// believed, so each is reported rather than followed.
func validatePromotionCursor(previous string, page promotionPageBody) error {
	if len(page.rows) == 0 {
		return operationalErrorf(
			"server offered cursor %q on an empty page of promotions", page.nextAfter)
	}
	if previous != "" && page.nextAfter <= previous {
		return operationalErrorf(
			"promotion listing cursor did not advance: %q followed %q", page.nextAfter, previous)
	}
	if page.nextAfter != page.lastID {
		return operationalErrorf(
			"promotion listing cursor %q is not the page's last promotion %q",
			page.nextAfter, page.lastID)
	}
	return nil
}

type promotionPageBody struct {
	envelope  map[string]json.RawMessage
	version   string
	projectID string
	rows      []json.RawMessage
	lastID    string
	nextAfter string
}

func decodePromotionPage(body []byte) (promotionPageBody, error) {
	var page promotionPageBody
	if err := decodeJSON(body, &page.envelope); err != nil {
		return promotionPageBody{}, err
	}
	for _, field := range []struct {
		name string
		into *string
	}{
		{"version", &page.version},
		{"project_id", &page.projectID},
		{"next_after", &page.nextAfter},
	} {
		if err := decodeEnvelopeString(page.envelope, field.name, field.into); err != nil {
			return promotionPageBody{}, err
		}
	}
	if raw, present := page.envelope["promotions"]; present {
		if err := decodeJSON(raw, &page.rows); err != nil {
			return promotionPageBody{}, err
		}
	}
	if len(page.rows) > 0 {
		var lastRow struct {
			ID string `json:"id"`
		}
		if err := decodeJSON(page.rows[len(page.rows)-1], &lastRow); err != nil {
			return promotionPageBody{}, err
		}
		page.lastID = lastRow.ID
	}
	return page, nil
}

// promotionCollection accumulates a traversal into one envelope.
type promotionCollection struct {
	envelope  map[string]json.RawMessage
	projectID string
	version   string
	rows      []json.RawMessage
}

func (c *promotionCollection) absorb(page promotionPageBody) error {
	if c.envelope == nil {
		c.envelope = page.envelope
		c.projectID = page.projectID
		c.version = page.version
	}
	if page.projectID != c.projectID {
		return operationalErrorf(
			"promotion listing changed project mid-traversal: %q then %q",
			c.projectID, page.projectID)
	}
	if page.version != c.version {
		return operationalErrorf(
			"promotion listing changed version mid-traversal: %q then %q",
			c.version, page.version)
	}
	c.rows = append(c.rows, page.rows...)
	return nil
}

func (c *promotionCollection) encode() ([]byte, error) {
	envelope := make(map[string]json.RawMessage, len(c.envelope)+1)
	maps.Copy(envelope, c.envelope)
	delete(envelope, "next_after")

	rows := c.rows
	if rows == nil {
		rows = []json.RawMessage{}
	}
	encodedRows, err := json.Marshal(rows)
	if err != nil {
		return nil, operationalErrorf("encoding promotion list: %v", err)
	}
	envelope["promotions"] = encodedRows

	merged, err := json.Marshal(envelope)
	if err != nil {
		return nil, operationalErrorf("encoding promotion list: %v", err)
	}
	return merged, nil
}

// ---------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------

type promotionEnvironmentDTO struct {
	Ref      string `json:"ref"`
	Rank     uint16 `json:"rank"`
	Revision string `json:"revision"`
}

type promotionDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`

	CandidateID          string `json:"candidate_id"`
	ReferenceCandidateID string `json:"reference_candidate_id"`

	ReferenceRunID string `json:"reference_run_id"`
	CandidateRunID string `json:"candidate_run_id"`

	SourceEnvironment promotionEnvironmentDTO `json:"source_environment"`
	TargetEnvironment promotionEnvironmentDTO `json:"target_environment"`

	GateLimits gateLimitsBody `json:"gate_limits"`
	GateResult gateDTO        `json:"gate_result"`

	Outcome   string `json:"outcome"`
	DecidedAt string `json:"decided_at"`
}

type promotionCreateBody struct {
	ID                string         `json:"id"`
	ReferenceRunID    string         `json:"reference_run_id"`
	CandidateRunID    string         `json:"candidate_run_id"`
	TargetEnvironment string         `json:"target_environment"`
	GateLimits        gateLimitsBody `json:"gate_limits"`
}

func renderPromotionBody(w io.Writer, body []byte) error {
	var dto promotionDTO
	if err := decodeJSON(body, &dto); err != nil {
		return err
	}
	return renderPromotion(w, dto)
}

// renderPromotion prints one recorded decision.
//
// The wording is deliberate and factual. "Recorded a promotion decision",
// never "deployed"; `accepted` and `rejected`, never "safe" or "unsafe"; and
// the gate's own PASS/FAIL beside them, because they are different statements
// about different things.
func renderPromotion(w io.Writer, p promotionDTO) error {
	fmt.Fprintf(w, "Promotion %s\n", p.ID)
	fmt.Fprintf(w, "Project: %s\n", p.ProjectID)
	fmt.Fprintf(w, "Candidate: %s\n", p.CandidateID)
	fmt.Fprintf(w, "Reference run: %s\n", p.ReferenceRunID)
	fmt.Fprintf(w, "Candidate run: %s\n", p.CandidateRunID)
	fmt.Fprintf(w, "From: %s rank %d (revision %s)\n",
		p.SourceEnvironment.Ref, p.SourceEnvironment.Rank, p.SourceEnvironment.Revision)
	fmt.Fprintf(w, "To:   %s rank %d (revision %s)\n",
		p.TargetEnvironment.Ref, p.TargetEnvironment.Rank, p.TargetEnvironment.Revision)
	fmt.Fprintf(w, "Outcome: %s\n", p.Outcome)
	fmt.Fprintf(w, "Gate: %s\n", verdictLabel(p.GateResult.Verdict))
	fmt.Fprintf(w, "Decided at: %s\n", p.DecidedAt)
	fmt.Fprintln(w, "Trustvian recorded this decision. Nothing was deployed.")

	fmt.Fprintln(w, "Gate checks:")
	renderMinimum(w, "Reference evidence", p.GateResult.ReferenceEvidence)
	renderMinimum(w, "Candidate evidence", p.GateResult.CandidateEvidence)
	renderMaximum(w, "Added behaviors", p.GateResult.AddedBehaviors)
	renderMaximum(w, "Block decisions", p.GateResult.BlockDecisions)
	renderMaximum(w, "Critical risk observations", p.GateResult.CriticalRiskObservations)
	return nil
}

// renderPromotionList prints a project's history, newest identifiers last.
//
// The server orders by identifier, and this preserves that order rather than
// re-sorting: a display that reordered an audit history would be inventing a
// sequence the platform did not record.
func renderPromotionList(w io.Writer, collected promotionCollection) error {
	rows := make([]promotionDTO, 0, len(collected.rows))
	for _, raw := range collected.rows {
		var dto promotionDTO
		if err := decodeJSON(raw, &dto); err != nil {
			return err
		}
		rows = append(rows, dto)
	}

	fmt.Fprintf(w, "Promotions in %s: %d\n", collected.projectID, len(rows))
	for _, row := range rows {
		fmt.Fprintf(w, "  %-28s %-9s %s → %s  gate %-4s  %s\n",
			row.ID, row.Outcome, row.SourceEnvironment.Ref, row.TargetEnvironment.Ref,
			verdictLabel(row.GateResult.Verdict), row.DecidedAt)
	}
	return nil
}
