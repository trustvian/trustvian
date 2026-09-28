package trustvianprocessor_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	trustvianprocessor "trustvian-processor"
)

// TestSetAttributesFromResult builds a Result via a real Engine.Analyze
// call (this module cannot hand-construct one — Result's Anomaly/Trust/
// Fingerprint fields are internal/ types under the core module,
// unreachable from here, which is itself proof the module boundary
// this package's README documents is real, not just asserted) and
// confirms every trustvian.* key lands on the destination map with the
// value actually on that Result.
func TestSetAttributesFromResult(t *testing.T) {
	engine := trustvian.NewEngine()
	ev := event.Event{
		ID:        "evt-1",
		Timestamp: time.Now(),
		Actor: event.Actor{
			ID:                 "svc-payment",
			Type:               event.ActorTypeService,
			IdentityConfidence: 0.95,
		},
		Operation: event.Operation{Category: event.OperationCategoryHTTP, Name: "POST /payment"},
		Target:    event.Target{Name: "payment-db"},
		Context:   event.Context{Environment: "production"},
	}
	result, err := engine.Analyze(context.Background(), ev)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	attrs := pcommon.NewMap()
	trustvianprocessor.SetAttributesFromResult(attrs, result)

	checks := []struct {
		key  string
		want any
	}{
		{"trustvian.anomaly.score", result.Anomaly.Score},
		{"trustvian.trust.score", result.Trust.Score},
		{"trustvian.risk.level", string(result.Trust.Risk)},
		{"trustvian.decision", string(result.Decision)},
		{"trustvian.fingerprint.id", result.Fingerprint.ID},
		// Task 075. This Result's Event never passed through EventFromSpan, so
		// nothing recorded a fidelity on it and it gets "transport" — the honest
		// answer rather than a default, since nothing proved a semantic identity.
		{"trustvian.fidelity", string(event.FidelityTransport)},
	}
	for _, c := range checks {
		v, ok := attrs.Get(c.key)
		if !ok {
			t.Errorf("attribute %q missing", c.key)
			continue
		}
		switch want := c.want.(type) {
		case float64:
			if got := v.Double(); got != want {
				t.Errorf("%s = %v, want %v", c.key, got, want)
			}
		case string:
			if got := v.Str(); got != want {
				t.Errorf("%s = %q, want %q", c.key, got, want)
			}
		}
	}
	// Exact, not a minimum. This guard exists to stop undocumented attributes
	// accumulating — trustvian.behavior.id is the one it was written against — so a
	// new attribute belongs in `checks` above with its reason, which is what makes
	// adding one deliberate rather than silent.
	if attrs.Len() != len(checks) {
		var got []string
		for k := range attrs.All() {
			got = append(got, k)
		}
		sort.Strings(got)
		t.Errorf("attrs.Len() = %d, want exactly %d.\n  got: %v\nA new attribute "+
			"must be listed in this test's `checks` with its reason; an unlisted "+
			"one is the drift this assertion prevents.", attrs.Len(), len(checks), got)
	}
	if _, ok := attrs.Get("trustvian.behavior.id"); ok {
		t.Error("trustvian.behavior.id must not be set — see this module's README for why it's deliberately omitted")
	}
}

// TestSetAttributesFromResultDoesNotClearExistingAttributes proves
// SetAttributesFromResult adds to a span's existing attribute map
// rather than replacing it — the correct behavior for a processor that
// must not discard whatever attributes arrived with the span.
func TestSetAttributesFromResultDoesNotClearExistingAttributes(t *testing.T) {
	engine := trustvian.NewEngine()
	result, err := engine.Analyze(context.Background(), event.Event{
		ID: "evt-2", Timestamp: time.Now(),
		Actor:     event.Actor{ID: "svc-x", Type: event.ActorTypeService, IdentityConfidence: 1},
		Operation: event.Operation{Category: event.OperationCategoryHTTP, Name: "GET /x"},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	attrs := pcommon.NewMap()
	attrs.PutStr("http.request.method", "GET")

	trustvianprocessor.SetAttributesFromResult(attrs, result)

	if v, ok := attrs.Get("http.request.method"); !ok || v.Str() != "GET" {
		t.Error("SetAttributesFromResult must not remove pre-existing attributes")
	}
	if _, ok := attrs.Get("trustvian.decision"); !ok {
		t.Error("trustvian.decision missing after SetAttributesFromResult")
	}
}
