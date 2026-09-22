package main

// Agent commands.
//
// An agent belongs to exactly one project, and that hierarchy is immutable:
// task 052 offers no ChangeAgent, so there is nothing to update here.

import (
	"context"
	"fmt"
	"io"
	"time"
)

const agentUsage = `usage:
  trustvian agent create --api-url <url> --id <id> --project-id <id> --name <name> [--json]
  trustvian agent get    --api-url <url> --id <id> [--json]`

func runAgent(s streams, args []string, timeout time.Duration) int {
	if len(args) == 0 {
		return usageFailure(s, agentUsage, fmt.Errorf("agent requires a subcommand"))
	}
	switch args[0] {
	case "create":
		return runAgentCreate(s, args[1:], timeout)
	case "get":
		return runAgentGet(s, args[1:], timeout)
	default:
		return usageFailure(s, agentUsage, fmt.Errorf("unknown agent command %q", args[0]))
	}
}

func runAgentCreate(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("agent create")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "caller-owned agent identifier (required)")
	projectID := fs.String("project-id", "", "owning project identifier (required)")
	name := fs.String("name", "", "human-readable agent name (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, agentUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"id": *id, "project-id": *projectID, "name": *name}); err != nil {
		return usageFailure(s, agentUsage, err)
	}

	return runLeaf(s, common, agentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, map[string]string{
				"id": *id, "project_id": *projectID, "name": *name}, "agents")
		},
		func(w io.Writer, body []byte) error {
			var dto agentDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderAgent(w, dto)
		})
}

func runAgentGet(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("agent get")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "agent identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, agentUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, agentUsage, err)
	}

	return runLeaf(s, common, agentUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "agents", *id)
		},
		func(w io.Writer, body []byte) error {
			var dto agentDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderAgent(w, dto)
		})
}
