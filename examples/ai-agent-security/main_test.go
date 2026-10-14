// This file is task 033's mandatory external-consumer proof (see
// docs/archive/tasks/v0.7/033-v07-stabilization-release-gate.md): examples/ is a
// genuinely separate Go module (examples/go.mod, with only a `replace`
// directive back to this repository — no shared import path with
// github.com/trustvian/trustvian's own internal/ tree), so a passing
// `go test ./...` run from here demonstrates, empirically, that an
// external OSS consumer can configure and exercise the v0.6/v0.7
// behavioral signals through public API alone. This file imports only
// event, config, and the root trustvian package — never internal/*,
// which Go's own internal/ import restriction would refuse to compile
// regardless of intent.
package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
)

// TestPublicConfigConfiguresDelegationAndNGramSignals proves, from
// outside the module, that config.AnomalyConfig actually influences
// real Engine output — not just that it parses/compiles without error.
// Mirrors this task's own "end-to-end configuration proof" requirement.
func TestPublicConfigConfiguresDelegationAndNGramSignals(t *testing.T) {
	anomalyCfg := config.AnomalyConfig{
		Version:          config.AnomalySchemaVersionV1,
		DelegationWeight: 0.7,
		NGramWeight:      0.9,
	}
	ac, err := config.CompileAnomaly(anomalyCfg)
	if err != nil {
		t.Fatalf("CompileAnomaly: %v", err)
	}

	engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(ac))
	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func() time.Time { clock = clock.Add(time.Minute); return clock }

	analyzeAndObserve := func(id, tool, delegatedFrom string) {
		t.Helper()
		result, err := engine.Analyze(ctx, agentEvent(id, tool, delegatedFrom, event.ApprovalApproved, tick()))
		if err != nil {
			t.Fatalf("Analyze(%s): %v", id, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe(%s): %v", id, err)
		}
	}

	for i := range 20 {
		analyzeAndObserve(fmt.Sprintf("normal-%d", i), "search", "orchestrator-agent")
	}
	for i := range 20 {
		id := fmt.Sprintf("familiar-read-%d", i)
		analyzeAndObserve(id+"-a", "search", "orchestrator-agent")
		analyzeAndObserve(id+"-b", "secret.read", "orchestrator-agent")
		analyzeAndObserve(id+"-c", "audit_log", "orchestrator-agent")
	}
	for i := range 20 {
		id := fmt.Sprintf("familiar-post-%d", i)
		analyzeAndObserve(id+"-a", "notify", "orchestrator-agent")
		analyzeAndObserve(id+"-b", "secret.read", "orchestrator-agent")
		analyzeAndObserve(id+"-c", "external.post", "orchestrator-agent")
	}
	analyzeAndObserve("attack-setup-a", "search", "orchestrator-agent")
	analyzeAndObserve("attack-setup-b", "secret.read", "orchestrator-agent")

	attack, err := engine.Analyze(ctx, agentEvent("attack", "external.post", "unknown-agent", event.ApprovalUnspecified, tick()))
	if err != nil {
		t.Fatalf("Analyze(attack): %v", err)
	}

	var sawDelegation, sawNGram bool
	for _, c := range attack.Anomaly.Contributors {
		switch c.Name {
		case "delegation_deviation":
			sawDelegation = c.Value > 0
		case "ngram_deviation":
			sawNGram = c.Value > 0
		}
	}
	if !sawDelegation {
		t.Errorf("Contributors = %+v, want delegation_deviation — public config → Engine → real signal did not occur", attack.Anomaly.Contributors)
	}
	if !sawNGram {
		t.Errorf("Contributors = %+v, want ngram_deviation — public config → Engine → real signal did not occur", attack.Anomaly.Contributors)
	}
}

// TestPublicConfigDisabledSignalMatchesExistingConventions proves
// DelegationWeight (and, for good measure, TransitionWeight) left at
// their public-config zero value produce byte-for-byte the same
// Anomaly.Score/Decision as the built-in default Engine — the
// disabled-signal semantics this task's own brief required preserving,
// verified through public API alone.
func TestPublicConfigDisabledSignalMatchesExistingConventions(t *testing.T) {
	ac, err := config.CompileAnomaly(config.AnomalyConfig{Version: config.AnomalySchemaVersionV1})
	if err != nil {
		t.Fatalf("CompileAnomaly: %v", err)
	}

	withExplicitZeroConfig := trustvian.NewEngine(trustvian.WithAnomalyConfig(ac))
	withDefaultConfig := trustvian.NewEngine()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ev := agentEvent("evt-1", "search", "orchestrator-agent", event.ApprovalApproved, now)

	got, err := withExplicitZeroConfig.Analyze(ctx, ev)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	want, err := withDefaultConfig.Analyze(ctx, ev)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if got.Anomaly.Score != want.Anomaly.Score || got.Decision != want.Decision {
		t.Errorf("a zero-value public AnomalyConfig changed engine output: Score %v->%v, Decision %q->%q", want.Anomaly.Score, got.Anomaly.Score, want.Decision, got.Decision)
	}
}
