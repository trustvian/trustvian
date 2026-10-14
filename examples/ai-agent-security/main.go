// Command ai-agent-security ports
// docs/archive/tasks/v0.7/032-agent-security-scenario-validation.md's combined
// scenario — delegation + sequence + approval evidence, together — and
// docs/archive/tasks/v0.7/033-v07-stabilization-release-gate.md's own resolution of
// the public-API gap task 032 found: every capability below is
// configured through config.PolicyConfig/config.AnomalyConfig, both
// compiled with config.CompilePolicy/config.CompileAnomaly into the
// exact types trustvian.WithPolicy/WithAnomalyConfig accept. This file
// never imports internal/policy or internal/anomaly.
//
// An AI agent's normal path (search, delegated by a known orchestrator,
// approved) matures into familiar behavior; two sensitive-tool pairwise
// transitions are separately familiarized so neither looks unusual on
// its own. Then a combined attack is evaluated: delegated by an
// unfamiliar agent, completing the exact three-step sequence that was
// never trained as one continuous path, with approval missing —
// surfacing delegation_deviation and ngram_deviation together, and
// blocked by the approval policy regardless of either signal's own
// magnitude. See README.md for real captured output.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/alert"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
)

func agentEvent(id, tool, delegatedFrom string, approval event.ApprovalStatus, ts time.Time) event.Event {
	return event.Event{
		ID:        id,
		Timestamp: ts,
		Actor: event.Actor{
			ID:                 "support-agent-b",
			Type:               event.ActorTypeAIAgent,
			IdentityConfidence: 0.95,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryTool,
			Name:     tool,
		},
		Target:  event.Target{Name: tool},
		Context: event.Context{Environment: "production", DelegatedFrom: delegatedFrom, ApprovalStatus: approval},
	}
}

func main() {
	// Approval requirement: entirely declarative Policy config — the
	// same shape a trustvian.yaml file decodes into (see
	// docs/policy-guide.md § Loading a Policy from a YAML file). The
	// event never gets to declare its own requirement away.
	policyCfg := config.PolicyConfig{
		Version:         config.SchemaVersionV1,
		DefaultDecision: "allow",
		DefaultReason:   "no approval requirement configured for this operation",
		Rules: []config.PolicyRule{{
			Name: "external-post-requires-approval",
			When: config.PolicyCondition{
				OperationCategory: "tool",
				TargetName:        "external.post",
			},
			Unless: &config.PolicyCondition{
				ApprovalStatus: "approved",
			},
			Decision: "block",
			Reason:   "external.post requires approval; approval evidence was not Approved",
		}},
	}
	p, err := config.CompilePolicy(policyCfg)
	if err != nil {
		log.Fatalf("compile policy: %v", err)
	}

	// Delegation and sequence signals: entirely declarative Anomaly
	// config (docs/archive/tasks/v0.7/033-v07-stabilization-release-gate.md) — the
	// same public path that closed the gap task 032 found.
	anomalyCfg := config.AnomalyConfig{
		Version:          config.AnomalySchemaVersionV1,
		DelegationWeight: 0.7,
		NGramWeight:      0.9,
	}
	ac, err := config.CompileAnomaly(anomalyCfg)
	if err != nil {
		log.Fatalf("compile anomaly config: %v", err)
	}

	engine := trustvian.NewEngine(trustvian.WithPolicy(p), trustvian.WithAnomalyConfig(ac))
	ctx := context.Background()

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func() time.Time { clock = clock.Add(time.Minute); return clock }

	analyzeAndObserve := func(id, tool, delegatedFrom string, approval event.ApprovalStatus) {
		result, err := engine.Analyze(ctx, agentEvent(id, tool, delegatedFrom, approval, tick()))
		if err != nil {
			log.Fatalf("analyze %s: %v", id, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			log.Fatalf("observe %s: %v", id, err)
		}
	}

	// This agent's normal path: delegated by the known orchestrator,
	// approved throughout.
	for i := range 20 {
		analyzeAndObserve(fmt.Sprintf("normal-%d", i), "search", "orchestrator-agent", event.ApprovalApproved)
	}
	// Familiarize search -> secret.read via search -> secret.read ->
	// audit_log (never external.post) — the third, "safe" step keeps
	// this phase's own leftover history from silently teaching the
	// exact attack sequence below.
	for i := range 20 {
		id := fmt.Sprintf("familiar-read-%d", i)
		analyzeAndObserve(id+"-a", "search", "orchestrator-agent", event.ApprovalApproved)
		analyzeAndObserve(id+"-b", "secret.read", "orchestrator-agent", event.ApprovalApproved)
		analyzeAndObserve(id+"-c", "audit_log", "orchestrator-agent", event.ApprovalApproved)
	}
	// Familiarize secret.read -> external.post via a different
	// predecessor (notify, not search) — approved, so warm-up itself
	// is never blocked.
	for i := range 20 {
		id := fmt.Sprintf("familiar-post-%d", i)
		analyzeAndObserve(id+"-a", "notify", "orchestrator-agent", event.ApprovalApproved)
		analyzeAndObserve(id+"-b", "secret.read", "orchestrator-agent", event.ApprovalApproved)
		analyzeAndObserve(id+"-c", "external.post", "orchestrator-agent", event.ApprovalApproved)
	}
	// Position the 2-element history window at (search, secret.read).
	analyzeAndObserve("attack-setup-a", "search", "orchestrator-agent", event.ApprovalApproved)
	analyzeAndObserve("attack-setup-b", "secret.read", "orchestrator-agent", event.ApprovalApproved)

	fmt.Println("Baseline trained: search/secret.read/external.post are each individually")
	fmt.Println("familiar for this agent, delegated by orchestrator-agent, always approved.")
	fmt.Println()

	// The combined attack: delegated by an agent never seen before,
	// completing the exact three-step sequence that was never trained
	// as one continuous path, with no approval evidence.
	attack, err := engine.Analyze(ctx, agentEvent("attack", "external.post", "unknown-agent", event.ApprovalUnspecified, tick()))
	if err != nil {
		log.Fatalf("analyze attack: %v", err)
	}

	fmt.Println("Attack: delegated by unknown-agent, completes search -> secret.read ->")
	fmt.Println("external.post as one sequence for the first time, approval missing.")
	fmt.Println()
	fmt.Println(attack.Explain())
	fmt.Printf("Decision: %s (rule %q: %s)\n\n", attack.Decision, attack.Explanation.RuleName, attack.Explanation.Reason)

	// Alert Validation: the existing, unmodified alert package
	// represents this exactly like any other BLOCK — no
	// ApprovalAlertEngine/DelegationAlertEngine needed. Receiving
	// attack.Decision (already typed policy.Decision from a public
	// trustvian.Result field) and passing it straight into
	// alert.Condition works via Go's normal type inference, without
	// this file ever importing internal/policy.
	rules := []alert.Rule{
		{Name: "agent-security-violation", When: alert.Condition{Decision: attack.Decision}, Severity: alert.SeverityHigh},
	}
	if a, ok := alert.Evaluate(attack, rules); ok {
		fmt.Printf("Alert: %s severity=%s reasons=%v\n", a.ID, a.Severity, a.Reasons)
	}
}
