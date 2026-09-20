// This file is task 048's external-consumer proof, in the same shape as
// examples/persistent-baseline/main_test.go is task 034's: examples/ is
// a separate Go module tied to the repository only by a `replace`
// directive, so Go's internal/ import restriction applies here exactly
// as it does to a third-party consumer.
//
// Two of the five Engine options — WithTrustConfig and WithContextRisk
// — could not be called from outside the module before this task. Their
// parameter types were internal, and no public path produced a value of
// either. They were exported and unusable. A test that compiles at all
// is most of the claim; the assertions below cover the rest.
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
)

// TestExternalConsumerCanConfigureEveryOption compiles and runs the
// full construction path. Every option is supplied from public API.
func TestExternalConsumerCanConfigureEveryOption(t *testing.T) {
	engine, closeStore, err := newEngine(t.TempDir() + "/baseline.json")
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	defer closeStore()

	ctx := context.Background()
	result, err := engine.Analyze(ctx, secretsRead())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if _, err := engine.Observe(ctx, result); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	// The configured context-risk callback ran and its value reached
	// the trust stage.
	if result.Trust.ContextRisk != 0.6 {
		t.Fatalf("Trust.ContextRisk = %v, want 0.6 — the callback did not reach the engine", result.Trust.ContextRisk)
	}
	if result.Decision == "" {
		t.Fatal("Analyze() produced no decision")
	}
}

// TestExternalConsumerCanProduceDecisionRecord is task 050's boundary proof:
// a separate module obtains the public projection of an analysis, marshals it
// with the standard library, and reads its fields — all without naming an
// internal type. This is the path a control plane takes to persist, stream,
// or serve a decision.
func TestExternalConsumerCanProduceDecisionRecord(t *testing.T) {
	engine, closeStore, err := newEngine(t.TempDir() + "/baseline.json")
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	defer closeStore()

	result, err := engine.Analyze(context.Background(), secretsRead())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	// Declaring the variable is the part that was impossible before: a
	// consumer could read Result's fields but could not name a type to hold
	// the projection.
	var record trustvian.DecisionRecord = result.DecisionRecord()

	if record.EventID != "evt-1" || record.ActorID != "svc-billing" {
		t.Fatalf("record identity not projected: %+v", record)
	}
	if record.FingerprintID == "" {
		t.Error("FingerprintID is empty")
	}
	if record.Behavior.TargetName != "secrets-manager" {
		t.Errorf("Behavior.TargetName = %q, want secrets-manager", record.Behavior.TargetName)
	}
	if record.ContextRisk != 0.6 {
		t.Errorf("ContextRisk = %v, want 0.6", record.ContextRisk)
	}
	if len(record.Contributors) == 0 {
		t.Error("no contributors: the record cannot explain its own score")
	}
	if record.PolicyReason == "" {
		t.Error("PolicyReason is empty")
	}

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var back trustvian.DecisionRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if back.FingerprintID != record.FingerprintID || len(back.Contributors) != len(record.Contributors) {
		t.Fatalf("round trip lost data:\n got %+v\nwant %+v", back, record)
	}
}

// TestExternalConsumerCanCompileTrustConfig covers the trust facade on
// its own, including the defaults case an adopter hits first.
func TestExternalConsumerCanCompileTrustConfig(t *testing.T) {
	if _, err := config.CompileTrust(config.TrustConfig{Version: config.TrustSchemaVersionV1}); err != nil {
		t.Fatalf("CompileTrust() with defaults: %v", err)
	}

	medium, high, critical := 0.2, 0.45, 0.8
	cfg, err := config.CompileTrust(config.TrustConfig{
		Version:           config.TrustSchemaVersionV1,
		MediumThreshold:   &medium,
		HighThreshold:     &high,
		CriticalThreshold: &critical,
	})
	if err != nil {
		t.Fatalf("CompileTrust() error = %v", err)
	}

	// The compiled value is usable where the option expects it, without
	// this module ever naming its type.
	_ = trustvian.NewEngine(trustvian.WithTrustConfig(cfg))
}

// TestExternalConsumerCanWriteContextRiskCallback proves the callback
// signature is expressible here — the concrete gap this task closed —
// and that it receives the event's stable dimensions.
func TestExternalConsumerCanWriteContextRiskCallback(t *testing.T) {
	var seen trustvian.StableFeatures

	engine := trustvian.NewEngine(trustvian.WithContextRisk(func(sf trustvian.StableFeatures) float64 {
		seen = sf
		return 0
	}))

	ev := event.Event{
		ID:        "evt-probe",
		Timestamp: time.Now(),
		Actor:     event.Actor{ID: "svc-a", Type: event.ActorTypeService, IdentityConfidence: 1},
		Operation: event.Operation{Category: event.OperationCategoryDB, Name: "SELECT customers"},
		Target:    event.Target{Name: "customer-pii", Category: event.TargetCategoryDatabase},
		Context:   event.Context{Environment: "staging"},
	}
	if _, err := engine.Analyze(context.Background(), ev); err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	want := trustvian.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryDB,
		OperationName:     "SELECT customers",
		TargetName:        "customer-pii",
		TargetCategory:    event.TargetCategoryDatabase,
		Environment:       "staging",
	}
	if seen != want {
		t.Fatalf("callback received %+v, want %+v", seen, want)
	}
}

// TestBuiltInPersistenceNeedsNoCustomStore: an external consumer
// selects a durable backend through configuration. Implementing a
// custom store is not required, and is not a supported extension point.
func TestBuiltInPersistenceNeedsNoCustomStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.StorageConfig
	}{
		{"memory", config.StorageConfig{Version: config.StorageSchemaVersionV1, Type: config.StorageTypeMemory}},
		{"file", config.StorageConfig{
			Version: config.StorageSchemaVersionV1,
			Type:    config.StorageTypeFile,
			File:    &config.FileStorageConfig{Path: t.TempDir() + "/state.json"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := config.CompileStorage(tc.cfg)
			if err != nil {
				t.Fatalf("CompileStorage() error = %v", err)
			}
			_ = trustvian.NewEngine(trustvian.WithStore(store))
		})
	}
}
