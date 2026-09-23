package main

// Project commands.
//
// Mechanical: parse flags, one request, render. No list, update or delete —
// task 058 owns no such routes, and faking a list client-side would invent
// ordering, paging and scoping semantics the API has not decided.

import (
	"context"
	"fmt"
	"io"
	"time"
)

const projectUsage = `usage:
  trustvian project create --id <id> --name <name> [--api-url <url>] [--json]
  trustvian project get    --id <id> [--api-url <url>] [--json]` + apiURLNote

func runProject(s streams, args []string, timeout time.Duration) int {
	if len(args) == 0 {
		return usageFailure(s, projectUsage, fmt.Errorf("project requires a subcommand"))
	}
	switch args[0] {
	case "create":
		return runProjectCreate(s, args[1:], timeout)
	case "get":
		return runProjectGet(s, args[1:], timeout)
	default:
		return usageFailure(s, projectUsage, fmt.Errorf("unknown project command %q", args[0]))
	}
}

func runProjectCreate(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("project create")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "caller-owned project identifier (required)")
	name := fs.String("name", "", "human-readable project name (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, projectUsage, err)
	}
	if err := requireAll(fs, map[string]string{"id": *id, "name": *name}); err != nil {
		return usageFailure(s, projectUsage, err)
	}

	return runLeaf(s, common, projectUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, map[string]string{"id": *id, "name": *name}, "projects")
		},
		func(w io.Writer, body []byte) error {
			var dto projectDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderProject(w, dto)
		})
}

func runProjectGet(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("project get")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "project identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, projectUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, projectUsage, err)
	}

	return runLeaf(s, common, projectUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "projects", *id)
		},
		func(w io.Writer, body []byte) error {
			var dto projectDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderProject(w, dto)
		})
}
