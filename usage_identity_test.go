package trustvian_test

// Token usage and the HTTP status code are evidence, never behavioral identity
// (task 087). The same act with or without them is the same behavior, decided
// the same way, and its DecisionRecord is byte-identical: a usage attribute that
// reached StableFeatures would re-fingerprint every model call whose response
// length changed.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

func modelCallEvent(attrs map[string]any) event.Event {
	return event.Event{
		ID:        "evt-model",
		Timestamp: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
		Actor:     event.Actor{ID: "support-agent", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
		Operation: event.Operation{
			Category: event.OperationCategoryExternal, Name: "llama3.2", Direction: event.DirectionOutbound,
		},
		Target:     event.Target{Name: "ollama"},
		Attributes: attrs,
		Context:    event.Context{Environment: "local"},
	}
}

func TestUsageAndStatusNeverReachBehavioralIdentity(t *testing.T) {
	variants := []map[string]any{
		nil,
		{"gen_ai.usage.input_tokens": int64(120), "gen_ai.usage.output_tokens": int64(30)},
		{"llm.token_count.prompt": int64(9000), "llm.token_count.completion": int64(1),
			"llm.token_count.total": int64(9001)},
		{"http.response.status_code": int64(429), "http.status_code": int64(500)},
	}
	var base []byte
	for i, attrs := range variants {
		// A fresh engine each time, so learning from one variant cannot make
		// the next look different for a reason other than its attributes.
		result, err := trustvian.NewEngine().Analyze(context.Background(), modelCallEvent(attrs))
		if err != nil {
			t.Fatalf("Analyze(variant %d) error = %v", i, err)
		}
		encoded, err := json.Marshal(result.DecisionRecord())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			base = encoded
			continue
		}
		if string(encoded) != string(base) {
			t.Errorf("variant %d changed the DecisionRecord:\n got %s\nwant %s", i, encoded, base)
		}
	}
}
