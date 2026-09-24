package main

// Environment commands.
//
// Mechanical, like the other control-plane families: parse flags, issue
// requests, render. Two things here are not boilerplate and are worth reading.
//
// `list` follows the route's pages until there is no continuation, because a
// project migrated from an older schema may hold more environments than one
// page carries. It asks for no larger a page than the API allows and lets the
// server decide the order.
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

// maxEnvironmentPages bounds a full traversal.
//
// A cursor that failed to advance would otherwise loop forever against a
// broken or hostile server. 256 pages is 16384 environments — far past any
// real project, and finite.
const maxEnvironmentPages = 256

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

	var collected environmentPage
	return runLeaf(s, common, environmentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return listEnvironmentPages(ctx, c, *projectID, &collected)
		},
		func(w io.Writer, _ []byte) error { return renderEnvironmentList(w, collected) })
}

// listEnvironmentPages walks the collection and returns one synthesized
// result carrying every row.
//
// The rows themselves are forwarded as the server sent them —
// json.RawMessage, never decoded and re-encoded — so a field a newer server
// adds survives `--json` instead of being silently dropped by this build's
// struct. The envelope around them is the route's own shape minus
// next_after, which is absent precisely because the traversal finished.
func listEnvironmentPages(
	ctx context.Context, c *platformClient, projectID string, collected *environmentPage,
) (apiResult, error) {
	after := ""
	var last apiResult

	for page := 0; ; page++ {
		if page >= maxEnvironmentPages {
			return apiResult{}, operationalErrorf(
				"environment listing did not finish within %d pages", maxEnvironmentPages)
		}

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

		var body environmentPage
		if err := decodeJSON(result.body, &body); err != nil {
			return apiResult{}, err
		}
		collected.Version = body.Version
		collected.ProjectID = body.ProjectID
		collected.Environments = append(collected.Environments, body.Environments...)
		last = result

		if body.NextAfter == "" {
			break
		}
		if body.NextAfter == after {
			return apiResult{}, operationalErrorf(
				"environment listing cursor did not advance past %q", after)
		}
		after = body.NextAfter
	}

	// One body for the whole collection, in the route's own shape. --json
	// forwards field names the server chose, and carries no next_after
	// because there is nothing left to follow.
	merged, err := json.Marshal(collected)
	if err != nil {
		return apiResult{}, operationalErrorf("encoding environment list: %v", err)
	}
	last.body = merged
	return last, nil
}

// environmentPage is the list route's envelope. Rows stay raw; see
// listEnvironmentPages.
type environmentPage struct {
	Version      string            `json:"version"`
	ProjectID    string            `json:"project_id"`
	Environments []json.RawMessage `json:"environments"`
	NextAfter    string            `json:"next_after,omitempty"`
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
func renderEnvironmentList(w io.Writer, page environmentPage) error {
	rows := make([]environmentDTO, 0, len(page.Environments))
	for _, raw := range page.Environments {
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

	fmt.Fprintf(w, "Environments in %s: %d\n", page.ProjectID, len(rows))
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
