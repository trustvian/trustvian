// Command configured-engine shows every supported way to configure a
// Trustvian Engine from outside the module, and proves the path works.
//
// examples/ is a separate Go module whose only tie to the repository is
// a `replace` directive, so Go's own internal/ import restriction
// applies here exactly as it does to any third-party consumer. If this
// program compiles, a third-party consumer can write it.
//
// The five options are configured here through public API alone:
//
//	WithPolicy         <- config.CompilePolicy
//	WithStore          <- config.CompileStorage
//	WithAnomalyConfig  <- config.CompileAnomaly
//	WithTrustConfig    <- config.CompileTrust
//	WithContextRisk    <- a callback over trustvian.StableFeatures
//
// Note what the program does *not* do: it never names the types those
// options accept. It receives each compiled value and passes it
// straight on. That is the supported pattern — the option parameter
// types live under internal/ so they stay free to evolve, while the
// config package gives callers a complete way to produce them.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
)

func main() {
	dir, err := os.MkdirTemp("", "trustvian-configured-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	engine, closeStore, err := newEngine(filepath.Join(dir, "baseline.json"))
	if err != nil {
		log.Fatal(err)
	}
	// The caller owns what it compiled. An Engine never closes a store
	// it was handed, because it did not open it.
	defer closeStore()

	ctx := context.Background()
	result, err := engine.Analyze(ctx, secretsRead())
	if err != nil {
		log.Fatal(err)
	}
	if _, err := engine.Observe(ctx, result); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("decision=%s risk=%s trust=%.2f context_risk=%.2f\n",
		result.Decision, result.Trust.Risk, result.Trust.Score, result.Trust.ContextRisk)

	// The public projection a platform persists, streams, or serves over an
	// API. Note what this module never does: name an internal type, or reach
	// into Result's stage structs to build a payload of its own.
	record := result.DecisionRecord()
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", encoded)
}

// newEngine builds a fully configured Engine and returns a function the
// caller must invoke to release the store's resources.
func newEngine(statePath string) (*trustvian.Engine, func(), error) {
	policy, err := config.CompilePolicy(config.PolicyConfig{
		Version:         config.SchemaVersionV1,
		DefaultDecision: "observe_only",
		DefaultReason:   "no rule matched",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("policy: %w", err)
	}

	store, err := config.CompileStorage(config.StorageConfig{
		Version: config.StorageSchemaVersionV1,
		Type:    config.StorageTypeFile,
		File:    &config.FileStorageConfig{Path: statePath},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("storage: %w", err)
	}

	anomalyCfg, err := config.CompileAnomaly(config.AnomalyConfig{
		Version: config.AnomalySchemaVersionV1,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("anomaly: %w", err)
	}

	// Residual distrust of 0.4 or more is now High rather than 0.5 —
	// a deployment that wants to escalate earlier than the defaults.
	high := 0.4
	trustCfg, err := config.CompileTrust(config.TrustConfig{
		Version:       config.TrustSchemaVersionV1,
		HighThreshold: &high,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("trust: %w", err)
	}

	engine := trustvian.NewEngine(
		trustvian.WithPolicy(policy),
		trustvian.WithStore(store),
		trustvian.WithAnomalyConfig(anomalyCfg),
		trustvian.WithTrustConfig(trustCfg),
		trustvian.WithContextRisk(sensitiveTargets),
	)

	closeStore := func() {
		if c, ok := store.(io.Closer); ok {
			_ = c.Close()
		}
	}
	return engine, closeStore, nil
}

// sensitiveTargets is a context-risk callback: some destinations are
// inherently sensitive however routine they become. This is the one
// behavioral input Trustvian does not learn, which is why repetition
// never erodes it.
func sensitiveTargets(sf trustvian.StableFeatures) float64 {
	switch sf.TargetName {
	case "secrets-manager":
		return 0.6
	case "customer-pii":
		return 0.4
	default:
		return 0
	}
}

func secretsRead() event.Event {
	return event.Event{
		ID:        "evt-1",
		Timestamp: time.Now(),
		Actor:     event.Actor{ID: "svc-billing", Type: event.ActorTypeService, IdentityConfidence: 0.95},
		Operation: event.Operation{Category: event.OperationCategoryHTTP, Name: "GET /v1/secret"},
		Target:    event.Target{Name: "secrets-manager", Category: event.TargetCategoryExternal},
		Context:   event.Context{Environment: "production"},
	}
}
