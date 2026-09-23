package main

// Candidate commands.
//
// Metadata is a fixed set of descriptive strings, not an arbitrary map: task
// 052 chose named fields so nothing can smuggle event attributes, prompts or
// tool arguments into the control plane, and an open map here would undo that
// on its first use.
//
// The CLI also inspects nothing to fill them in. No git call, no digest
// computation, no build introspection — a value the CLI derived would claim a
// provenance it cannot actually vouch for, and the caller already knows what
// they built.

import (
	"context"
	"fmt"
	"io"
	"time"
)

const candidateUsage = `usage:
  trustvian candidate create --id <id> --agent-id <id>
                             [--label <text>] [--source-ref <ref>]
                             [--artifact-digest <digest>] [--model <name>]
                             [--toolset-digest <digest>] [--config-digest <digest>]
                             [--api-url <url>] [--json]
  trustvian candidate get    --id <id> [--api-url <url>] [--json]` + apiURLNote

func runCandidate(s streams, args []string, timeout time.Duration) int {
	if len(args) == 0 {
		return usageFailure(s, candidateUsage, fmt.Errorf("candidate requires a subcommand"))
	}
	switch args[0] {
	case "create":
		return runCandidateCreate(s, args[1:], timeout)
	case "get":
		return runCandidateGet(s, args[1:], timeout)
	default:
		return usageFailure(s, candidateUsage, fmt.Errorf("unknown candidate command %q", args[0]))
	}
}

func runCandidateCreate(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("candidate create")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "caller-owned candidate identifier (required)")
	agentID := fs.String("agent-id", "", "owning agent identifier (required)")

	label := fs.String("label", "", "human-readable label")
	sourceRef := fs.String("source-ref", "", "source reference, such as a commit or tag")
	artifactDigest := fs.String("artifact-digest", "", "digest of the built artifact")
	model := fs.String("model", "", "model identifier")
	toolsetDigest := fs.String("toolset-digest", "", "digest of the toolset")
	configDigest := fs.String("config-digest", "", "digest of the configuration")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, candidateUsage, err)
	}
	if err := requireAll(fs, map[string]string{"id": *id, "agent-id": *agentID}); err != nil {
		return usageFailure(s, candidateUsage, err)
	}

	// Built as a typed value rather than a map so the wire field names live in
	// one place and an unset field is simply absent.
	metadata := candidateMetadataDTO{
		Label:          *label,
		SourceRef:      *sourceRef,
		ArtifactDigest: *artifactDigest,
		Model:          *model,
		ToolsetDigest:  *toolsetDigest,
		ConfigDigest:   *configDigest,
	}

	return runLeaf(s, common, candidateUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, createCandidateBody{
				ID: *id, AgentID: *agentID, Metadata: metadata}, "candidates")
		},
		func(w io.Writer, body []byte) error {
			var dto candidateDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderCandidate(w, dto)
		})
}

type createCandidateBody struct {
	ID       string               `json:"id"`
	AgentID  string               `json:"agent_id"`
	Metadata candidateMetadataDTO `json:"metadata"`
}

func runCandidateGet(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("candidate get")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "candidate identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, candidateUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, candidateUsage, err)
	}

	return runLeaf(s, common, candidateUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "candidates", *id)
		},
		func(w io.Writer, body []byte) error {
			var dto candidateDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderCandidate(w, dto)
		})
}
