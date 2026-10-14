// Command persistent-baseline demonstrates v0.8's first slice
// (docs/archive/tasks/v0.8/034-production-store-contract-and-public-boundary.md):
// selecting a durable Store through public configuration alone, so a
// learned baseline survives a process restart.
//
// Before task 034 this program could not be written by an external
// consumer at all. store.Store lives in internal/store and its methods
// reference internal types, so an outside caller could neither
// implement the interface nor call store.NewFileStore — and no exported
// function returned one. Every external deployment was therefore pinned
// to the default in-memory store, losing all learned behavior on
// restart. config.StorageConfig + config.CompileStorage close that gap,
// the same way config.CompilePolicy (v0.5) and config.CompileAnomaly
// (v0.7) closed it for Policy and anomaly scoring — see
// docs/adr/0018-production-store-boundary-and-postgresql-direction.md.
//
// The "restart" here is a genuine one in every sense that matters for
// persistence: the first Engine is discarded entirely and a second is
// built from scratch against the same state file, reading it off disk.
// See README.md for real captured output.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
)

func paymentEvent(id string, ts time.Time) event.Event {
	return event.Event{
		ID:        id,
		Timestamp: ts,
		Actor: event.Actor{
			ID:                 "svc-payment",
			Type:               event.ActorTypeService,
			IdentityConfidence: 0.95,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryDB,
			Name:     "SELECT accounts",
		},
		Target:     event.Target{Name: "payment-db"},
		Context:    event.Context{Environment: "production"},
		Attributes: map[string]any{"duration_ms": 12},
	}
}

// newEngineOn builds an Engine backed by the file store at path, using
// only public API — this function is the whole point of the example.
func newEngineOn(path string) (*trustvian.Engine, error) {
	cfg := config.StorageConfig{
		Version: config.StorageSchemaVersionV1,
		Type:    config.StorageTypeFile,
		File:    &config.FileStorageConfig{Path: path},
	}

	// CompileStorage validates the config and opens the store, reading
	// any existing state off disk. A failure here is fatal by design:
	// it never falls back to a non-durable store behind your back.
	s, err := config.CompileStorage(cfg)
	if err != nil {
		return nil, err
	}
	return trustvian.NewEngine(trustvian.WithStore(s)), nil
}

func main() {
	dir, err := os.MkdirTemp("", "trustvian-persistent-baseline-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)
	statePath := filepath.Join(dir, "baseline.json")

	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func() time.Time { clock = clock.Add(time.Minute); return clock }

	// --- First process: learn. ---
	first, err := newEngineOn(statePath)
	if err != nil {
		log.Fatalf("build first engine: %v", err)
	}

	for i := 1; i <= 12; i++ {
		result, err := first.Analyze(ctx, paymentEvent(fmt.Sprintf("warm-up-%d", i), tick()))
		if err != nil {
			log.Fatalf("analyze warm-up %d: %v", i, err)
		}
		if _, err := first.Observe(ctx, result); err != nil {
			log.Fatalf("observe warm-up %d: %v", i, err)
		}
		if i == 1 || i == 12 {
			fmt.Printf("process 1, event %2d: confidence=%.2f trust=%.2f\n",
				i, result.Anomaly.Confidence, result.Trust.Score)
		}
	}

	info, err := os.Stat(statePath)
	if err != nil {
		log.Fatalf("stat state file: %v", err)
	}
	fmt.Printf("\nstate file written: %d bytes\n\n", info.Size())

	// --- Restart: discard that Engine entirely, build a new one on the
	// same file. Nothing is carried over in memory. ---
	first = nil

	second, err := newEngineOn(statePath)
	if err != nil {
		log.Fatalf("build second engine: %v", err)
	}

	result, err := second.Analyze(ctx, paymentEvent("after-restart", tick()))
	if err != nil {
		log.Fatalf("analyze after restart: %v", err)
	}

	fmt.Println("process 2 (fresh Engine, same state file):")
	fmt.Println(result.Explain())
	if result.Anomaly.Confidence > 0 {
		fmt.Printf("\nBaseline survived the restart — confidence %.2f, not 0.00.\n", result.Anomaly.Confidence)
	}
}
