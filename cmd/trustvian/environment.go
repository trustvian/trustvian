package main

// Environment commands.
//
// Mechanical, like the other control-plane families: parse flags, issue
// requests, render. Two things here are not boilerplate and are worth reading.
//
// `list` follows the route's pages until there is no continuation, because a
// project migrated from an older schema may hold more environments than one
// page carries — and however many that is, since migration preserves what
// history referenced and caps nothing. There is therefore no ceiling on the
// number of pages followed; the loop is bounded by cursor progress instead,
// and a cursor that cannot progress is reported rather than retried. It asks
// for no larger a page than the API allows and lets the server decide the
// order.
//
// `list --json` is consequently the one place this CLI does not forward a
// server response verbatim: it emits one document for the completed
// collection. See listEnvironmentPages for exactly what that document
// preserves.
//
// Nothing here computes promotion precedence. Rank travels on every row and
// this file sorts by it for display, which is presentation; whether one
// environment may promote toward another is `CanPromote`, it lives in the
// platform, and no adapter reimplements it — see
// docs/adr/0039-environments-are-project-owned-ranked-references.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// maxEnvironmentPage mirrors the API's page bound.
//
// Duplicated rather than imported: this module must not import
// trustvian-platform (ADR 0033), so the number is stated here and the server
// remains the thing that enforces it. Asking for more is a 400, which is the
// server telling this constant it has drifted.
const maxEnvironmentPage = 64

const environmentUsage = `usage:
  trustvian env create   --project-id <id> --ref <ref> --name <name> [--rank <n>] [--api-url <url>] [--json]
  trustvian env get      --project-id <id> --ref <ref> [--api-url <url>] [--json]
  trustvian env list     --project-id <id> [--api-url <url>] [--json]
  trustvian env set      --project-id <id> --ref <ref> --revision <n> [--name <name>] [--rank <n> | --clear-rank] [--api-url <url>] [--json]
  trustvian env archive  --project-id <id> --ref <ref> --revision <n> [--api-url <url>] [--json]
  trustvian env activate --project-id <id> --ref <ref> --revision <n> [--api-url <url>] [--json]` + apiURLNote

func runEnvironment(s streams, args []string, timeout time.Duration) int {
	if len(args) == 0 {
		return usageFailure(s, environmentUsage, fmt.Errorf("env requires a subcommand"))
	}
	switch args[0] {
	case "create":
		return runEnvironmentCreate(s, args[1:], timeout)
	case "get":
		return runEnvironmentGet(s, args[1:], timeout)
	case "list":
		return runEnvironmentList(s, args[1:], timeout)
	case "set":
		return runEnvironmentSet(s, args[1:], timeout)
	case "archive":
		return runEnvironmentLifecycle(s, args[1:], timeout, "archive")
	case "activate":
		return runEnvironmentLifecycle(s, args[1:], timeout, "activate")
	default:
		return usageFailure(s, environmentUsage, fmt.Errorf("unknown env command %q", args[0]))
	}
}

func runEnvironmentCreate(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("env create")
	common := registerCommonFlags(fs)
	projectID := fs.String("project-id", "", "owning project identifier (required)")
	ref := fs.String("ref", "", "environment reference, unique within the project (required)")
	name := fs.String("name", "", "human-readable environment name (required)")
	rank := fs.Int("rank", 0, "promotion rank, 0-9999; omitted leaves the environment unranked")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, environmentUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"project-id": *projectID, "ref": *ref, "name": *name}); err != nil {
		return usageFailure(s, environmentUsage, err)
	}

	body := map[string]any{"project_id": *projectID, "ref": *ref, "name": *name}
	if flagWasSet(fs, "rank") {
		value, err := environmentRank(*rank)
		if err != nil {
			return usageFailure(s, environmentUsage, err)
		}
		body["rank"] = value
	}

	return runLeaf(s, common, environmentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, body, "environments")
		},
		renderEnvironmentBody)
}

func runEnvironmentGet(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("env get")
	common := registerCommonFlags(fs)
	projectID := fs.String("project-id", "", "owning project identifier (required)")
	ref := fs.String("ref", "", "environment reference (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, environmentUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"project-id": *projectID, "ref": *ref}); err != nil {
		return usageFailure(s, environmentUsage, err)
	}

	return runLeaf(s, common, environmentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "projects", *projectID, "environments", *ref)
		},
		renderEnvironmentBody)
}

func runEnvironmentSet(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("env set")
	common := registerCommonFlags(fs)
	projectID := fs.String("project-id", "", "owning project identifier (required)")
	ref := fs.String("ref", "", "environment reference (required)")
	revision := fs.Uint64("revision", 0, "the revision this change applies to (required)")
	name := fs.String("name", "", "new human-readable name")
	rank := fs.Int("rank", 0, "new promotion rank, 0-9999")
	clearRank := fs.Bool("clear-rank", false, "remove the promotion rank")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, environmentUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"project-id": *projectID, "ref": *ref}); err != nil {
		return usageFailure(s, environmentUsage, err)
	}
	if *revision == 0 {
		return usageFailure(s, environmentUsage,
			fmt.Errorf("--revision is required; it is the revision the caller read"))
	}

	setsRank := flagWasSet(fs, "rank")
	if setsRank && *clearRank {
		return usageFailure(s, environmentUsage,
			fmt.Errorf("--rank and --clear-rank cannot be combined"))
	}
	setsName := flagWasSet(fs, "name")
	if !setsName && !setsRank && !*clearRank {
		return usageFailure(s, environmentUsage,
			fmt.Errorf("nothing to change: pass --name, --rank, or --clear-rank"))
	}

	body := map[string]any{"revision": *revision}
	if setsName {
		body["name"] = *name
	}
	if setsRank {
		value, err := environmentRank(*rank)
		if err != nil {
			return usageFailure(s, environmentUsage, err)
		}
		body["rank"] = value
	}
	if *clearRank {
		body["clear_rank"] = true
	}

	return runLeaf(s, common, environmentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, body, "projects", *projectID, "environments", *ref, "configure")
		},
		renderEnvironmentBody)
}

func runEnvironmentLifecycle(s streams, args []string, timeout time.Duration, action string) int {
	fs := newFlagSet("env " + action)
	common := registerCommonFlags(fs)
	projectID := fs.String("project-id", "", "owning project identifier (required)")
	ref := fs.String("ref", "", "environment reference (required)")
	revision := fs.Uint64("revision", 0, "the revision this change applies to (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, environmentUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"project-id": *projectID, "ref": *ref}); err != nil {
		return usageFailure(s, environmentUsage, err)
	}
	if *revision == 0 {
		return usageFailure(s, environmentUsage,
			fmt.Errorf("--revision is required; it is the revision the caller read"))
	}

	return runLeaf(s, common, environmentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, map[string]any{"revision": *revision},
				"projects", *projectID, "environments", *ref, action)
		},
		renderEnvironmentBody)
}

// environmentRank validates a rank before it reaches the wire.
//
// The server validates it too; this turns a typo into a usage error with the
// command's own vocabulary rather than a round trip and a 400.
func environmentRank(rank int) (uint16, error) {
	if rank < 0 || rank > 9999 {
		return 0, fmt.Errorf("--rank must be between 0 and 9999")
	}
	return uint16(rank), nil
}

// ---------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------

// runEnvironmentList pages the collection to completion.
//
// The route is bounded per response rather than per project, because a
// migrated database may hold more environments than may now be created. A
// caller asking "what environments does this project have" wants the answer,
// not the first page of it, so this follows next_after until it is absent.
func runEnvironmentList(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("env list")
	common := registerCommonFlags(fs)
	projectID := fs.String("project-id", "", "owning project identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, environmentUsage, err)
	}
	if err := requireAll(fs, map[string]string{"project-id": *projectID}); err != nil {
		return usageFailure(s, environmentUsage, err)
	}

	var collected environmentCollection
	return runLeaf(s, common, environmentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return listEnvironmentPages(ctx, c, *projectID, &collected)
		},
		func(w io.Writer, _ []byte) error { return renderEnvironmentList(w, collected) })
}

// listEnvironmentPages walks the collection and returns one synthesized
// result carrying every row.
//
// This is the one place in the CLI where `--json` does not forward a server
// response verbatim, and the exception is deliberate. `env list` is not one
// request: the route is bounded per response, a migrated project may hold far
// more environments than one page carries, and a caller asking "what
// environments does this project have" wants the answer rather than the first
// page of it. Printing each page as it arrived would emit several JSON
// documents; printing only the first would answer a different question. So
// the traversal completes and one envelope is synthesized from it, with no
// next_after because there is nothing left to follow. See
// docs/tasks/v1.0/065-environment-model.md and docs/compatibility.md.
//
// What that envelope preserves matters as much as what it contains. Rows are
// forwarded exactly as the server sent them — json.RawMessage, never decoded
// and re-encoded — and so are top-level fields this build does not know
// about. Only the two fields the aggregation owns, "environments" and
// "next_after", are replaced. A newer server that adds a field to either
// level does not lose it here.
func listEnvironmentPages(
	ctx context.Context, c *platformClient, projectID string, collected *environmentCollection,
) (apiResult, error) {
	after := ""
	var last apiResult

	for {
		query := url.Values{}
		query.Set("limit", strconv.Itoa(maxEnvironmentPage))
		if after != "" {
			query.Set("after", after)
		}

		result, err := c.getQuery(ctx, query, "projects", projectID, "environments")
		if err != nil {
			return apiResult{}, err
		}
		// A non-2xx page is returned unchanged, so runLeaf reports the
		// server's own envelope exactly as a single-request command would.
		if result.status < 200 || result.status >= 300 {
			return result, nil
		}

		page, err := decodeEnvironmentPage(result.body)
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
		if err := validateEnvironmentCursor(after, page); err != nil {
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

// validateEnvironmentCursor refuses a continuation that cannot make progress.
//
// There is no cap on how many pages a traversal may take — a migrated project
// holds whatever its history referenced, and an arbitrary ceiling would turn
// a legitimately large project into a truncated answer that looked like a
// complete one. What bounds the loop instead is that every step must move
// strictly forward in the same byte order the route paginates in. A repeated
// cursor, a cursor that moves backwards, and a continuation offered on an
// empty page are all impossible from a correct server and all non-terminating
// if believed, so each is reported rather than followed.
func validateEnvironmentCursor(previous string, page environmentPageBody) error {
	if len(page.rows) == 0 {
		return operationalErrorf(
			"server offered cursor %q on an empty page of environments", page.nextAfter)
	}
	if previous != "" && page.nextAfter <= previous {
		return operationalErrorf(
			"environment listing cursor did not advance: %q followed %q", page.nextAfter, previous)
	}
	// The route's own contract: the cursor a page publishes is that page's
	// last ref. Anything else would resume somewhere this traversal cannot
	// reason about.
	if page.nextAfter != page.lastRef {
		return operationalErrorf(
			"environment listing cursor %q is not the page's last environment %q",
			page.nextAfter, page.lastRef)
	}
	return nil
}

// environmentPageBody is one decoded page: the fields the traversal reads,
// plus the whole envelope so nothing else is lost.
type environmentPageBody struct {
	envelope  map[string]json.RawMessage
	version   string
	projectID string
	rows      []json.RawMessage
	lastRef   string
	nextAfter string
}

func decodeEnvironmentPage(body []byte) (environmentPageBody, error) {
	var page environmentPageBody
	if err := decodeJSON(body, &page.envelope); err != nil {
		return environmentPageBody{}, err
	}
	if err := decodeEnvelopeString(page.envelope, "version", &page.version); err != nil {
		return environmentPageBody{}, err
	}
	if err := decodeEnvelopeString(page.envelope, "project_id", &page.projectID); err != nil {
		return environmentPageBody{}, err
	}
	if err := decodeEnvelopeString(page.envelope, "next_after", &page.nextAfter); err != nil {
		return environmentPageBody{}, err
	}
	if raw, present := page.envelope["environments"]; present {
		if err := decodeJSON(raw, &page.rows); err != nil {
			return environmentPageBody{}, err
		}
	}
	if len(page.rows) > 0 {
		var lastRow struct {
			Ref string `json:"ref"`
		}
		if err := decodeJSON(page.rows[len(page.rows)-1], &lastRow); err != nil {
			return environmentPageBody{}, err
		}
		page.lastRef = lastRow.Ref
	}
	return page, nil
}

// decodeEnvelopeString reads an optional string field, leaving it empty when
// absent and reporting a type mismatch rather than ignoring one.
func decodeEnvelopeString(envelope map[string]json.RawMessage, field string, into *string) error {
	raw, present := envelope[field]
	if !present || string(raw) == "null" {
		return nil
	}
	return decodeJSON(raw, into)
}

// environmentCollection accumulates a traversal into one envelope.
type environmentCollection struct {
	envelope  map[string]json.RawMessage
	projectID string
	version   string
	rows      []json.RawMessage
}

// absorb folds one page in, refusing pages that are not part of the same
// logical collection.
//
// Two pages disagreeing about project_id or version are not two halves of one
// answer, and concatenating them would produce a document describing
// something that never existed. The first page's envelope is the one kept,
// because that is the one whose unknown fields describe this collection.
func (c *environmentCollection) absorb(page environmentPageBody) error {
	if c.envelope == nil {
		c.envelope = page.envelope
		c.projectID = page.projectID
		c.version = page.version
	}
	if page.projectID != c.projectID {
		return operationalErrorf(
			"environment listing changed project mid-traversal: %q then %q",
			c.projectID, page.projectID)
	}
	if page.version != c.version {
		return operationalErrorf(
			"environment listing changed version mid-traversal: %q then %q",
			c.version, page.version)
	}
	c.rows = append(c.rows, page.rows...)
	return nil
}

// encode renders the completed collection.
//
// Everything the first page carried survives except the two fields this owns:
// "environments" becomes every row of every page, and "next_after" is removed
// because the traversal finished.
func (c *environmentCollection) encode() ([]byte, error) {
	envelope := make(map[string]json.RawMessage, len(c.envelope)+1)
	maps.Copy(envelope, c.envelope)
	delete(envelope, "next_after")

	rows := c.rows
	if rows == nil {
		rows = []json.RawMessage{}
	}
	encodedRows, err := json.Marshal(rows)
	if err != nil {
		return nil, operationalErrorf("encoding environment list: %v", err)
	}
	envelope["environments"] = encodedRows

	merged, err := json.Marshal(envelope)
	if err != nil {
		return nil, operationalErrorf("encoding environment list: %v", err)
	}
	return merged, nil
}

// ---------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------

type environmentDTO struct {
	ProjectID string  `json:"project_id"`
	Ref       string  `json:"ref"`
	Name      string  `json:"name"`
	Rank      *uint16 `json:"rank"`
	Status    string  `json:"status"`
	Revision  uint64  `json:"revision"`
}

func renderEnvironmentBody(w io.Writer, body []byte) error {
	var dto environmentDTO
	if err := decodeJSON(body, &dto); err != nil {
		return err
	}
	return renderEnvironment(w, dto)
}

func renderEnvironment(w io.Writer, e environmentDTO) error {
	fmt.Fprintf(w, "Environment %s\n", e.Ref)
	fmt.Fprintf(w, "Project: %s\n", e.ProjectID)
	fmt.Fprintf(w, "Name: %s\n", e.Name)
	fmt.Fprintf(w, "Rank: %s\n", environmentRankText(e.Rank))
	fmt.Fprintf(w, "Status: %s\n", e.Status)
	fmt.Fprintf(w, "Revision: %d\n", e.Revision)
	return nil
}

// renderEnvironmentList prints the collection in a stable display order.
//
// Ordering for a human to read, over the rank the server supplied. The route
// traverses by ref because ref is immutable and rank is not; presenting the
// result by rank is a display choice and changes nothing about which
// environment may promote toward which — that answer has one implementation,
// in the platform. This comparator is the only place the CLI looks at two
// ranks at once, and cli_architecture_test.go asserts it stays that way.
func renderEnvironmentList(w io.Writer, collected environmentCollection) error {
	rows := make([]environmentDTO, 0, len(collected.rows))
	for _, raw := range collected.rows {
		var dto environmentDTO
		if err := decodeJSON(raw, &dto); err != nil {
			return err
		}
		rows = append(rows, dto)
	}

	// Ranked first in rank order, unranked last, ref breaking every tie.
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if (left.Rank == nil) != (right.Rank == nil) {
			return right.Rank == nil
		}
		if left.Rank != nil && *left.Rank != *right.Rank {
			return *left.Rank < *right.Rank
		}
		return left.Ref < right.Ref
	})

	fmt.Fprintf(w, "Environments in %s: %d\n", collected.projectID, len(rows))
	for _, row := range rows {
		fmt.Fprintf(w, "  %-24s rank %-6s %-8s rev %d  %s\n",
			row.Ref, environmentRankText(row.Rank), row.Status, row.Revision, row.Name)
	}
	return nil
}

func environmentRankText(rank *uint16) string {
	if rank == nil {
		return "-"
	}
	return strconv.FormatUint(uint64(*rank), 10)
}
