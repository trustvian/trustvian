package trustvian_test

// Task 051: a learning scope partitions learned history without touching
// behavioral identity.
//
// The whole design rests on one separation, and these tests exist to make it
// impossible to break quietly:
//
//	Fingerprint identity    — what the actor did
//	Learning-state identity — which history that observation belongs to
//
// So the central assertion is a pair, not a single check. Two scopes must
// reach *different* learned evidence from the *same* fingerprint. A change
// that leaked scope into the fingerprint would still isolate learning —
// and would be wrong, because every new scope would make an actor's whole
// repertoire look brand new. TestScopesShareFingerprintIdentity is what
// catches that.
//
// These drive real Engines over a shared store rather than constructing
// baselines directly: scope selection is Engine configuration, and testing
// it below the Engine would skip the part that decides.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/store"
)

// scopeClock spaces timestamps realistically. See paymentEventAt's comment:
// a tight loop's microsecond gaps look nothing like a stable rate to the
// frequency-deviation signal.
type scopeClock struct{ t time.Time }

func newScopeClock() *scopeClock {
	return &scopeClock{t: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)}
}

func (c *scopeClock) next() time.Time {
	c.t = c.t.Add(90 * time.Second)
	return c.t
}

// scopeEvent is one actor doing one thing. Everything that could plausibly
// be mistaken for a scope selector — session, trace, delegation, attributes
// — is filled in, so tests asserting those do *not* select a scope have
// something real to vary.
func scopeEvent(id string, ts time.Time) event.Event {
	return event.Event{
		ID:        id,
		Timestamp: ts,
		Actor:     event.Actor{ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
		Operation: event.Operation{Category: event.OperationCategoryTool, Name: "shell.execute"},
		Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
		Context: event.Context{
			Environment: "production",
			SessionID:   "sess-default",
			TraceID:     "trace-default",
		},
	}
}

// train runs n analyze/observe cycles through e and returns the last result.
func train(t *testing.T, e *trustvian.Engine, clock *scopeClock, n int) trustvian.Result {
	t.Helper()
	ctx := context.Background()
	var last trustvian.Result
	for i := range n {
		res, err := e.Analyze(ctx, scopeEvent(fmt.Sprintf("evt-%d", i), clock.next()))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		if _, err := e.Observe(ctx, res); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		last = res
	}
	return last
}

// TestScopesLearnIndependently is the task's central behavioral claim: one
// actor, one environment, one behavior, two scopes, two histories.
func TestScopesLearnIndependently(t *testing.T) {
	shared := store.NewInMemory()
	clock := newScopeClock()

	trained := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope("scope-a"))
	cold := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope("scope-b"))

	train(t, trained, clock, 20)

	matured, err := trained.Analyze(context.Background(), scopeEvent("evt-mature", clock.next()))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	fresh, err := cold.Analyze(context.Background(), scopeEvent("evt-fresh", clock.next()))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if matured.Anomaly.Confidence <= fresh.Anomaly.Confidence {
		t.Fatalf("scope-a confidence %v is not above cold scope-b's %v — the scopes are sharing learned state",
			matured.Anomaly.Confidence, fresh.Anomaly.Confidence)
	}
	if fresh.Anomaly.Confidence != 0 {
		t.Errorf("scope-b confidence = %v, want 0: a never-observed scope must start cold", fresh.Anomaly.Confidence)
	}

	// The scope reaches the key the engine actually read and will write.
	if matured.BaselineKey.Scope != "scope-a" || fresh.BaselineKey.Scope != "scope-b" {
		t.Errorf("BaselineKey scopes = %q / %q, want scope-a / scope-b", matured.BaselineKey.Scope, fresh.BaselineKey.Scope)
	}
	if matured.BaselineKey.ActorID != fresh.BaselineKey.ActorID || matured.BaselineKey.Environment != fresh.BaselineKey.Environment {
		t.Error("actor and environment must be identical across scopes; only Scope may differ")
	}
}

// TestScopesShareFingerprintIdentity is the other half, and the one that
// would fail if scope ever leaked into behavioral identity.
func TestScopesShareFingerprintIdentity(t *testing.T) {
	clock := newScopeClock()
	ev := scopeEvent("evt-identity", clock.next())

	var ids []string
	var behaviors []trustvian.StableFeatures
	for _, scope := range []string{"", "scope-a", "scope-b", "candidate/9f3c1a"} {
		e := trustvian.NewEngine(trustvian.WithLearningScope(scope))
		res, err := e.Analyze(context.Background(), ev)
		if err != nil {
			t.Fatalf("Analyze() in scope %q error = %v", scope, err)
		}
		ids = append(ids, res.Fingerprint.ID)
		behaviors = append(behaviors, res.DecisionRecord().Behavior)
	}

	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[0] {
			t.Fatalf("FingerprintID differs across scopes: %q vs %q — scope has entered behavioral identity", ids[i], ids[0])
		}
		if behaviors[i] != behaviors[0] {
			t.Fatalf("StableFeatures differ across scopes:\n %+v\nvs %+v", behaviors[i], behaviors[0])
		}
	}
}

// TestSameScopeSharesLearnedState: partitioning is by scope, not by Engine
// instance. Two engines configured alike are one profile.
func TestSameScopeSharesLearnedState(t *testing.T) {
	shared := store.NewInMemory()
	clock := newScopeClock()

	first := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope("shared"))
	second := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope("shared"))

	train(t, first, clock, 15)

	res, err := second.Analyze(context.Background(), scopeEvent("evt-second", clock.next()))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if res.Anomaly.Confidence == 0 {
		t.Fatal("second engine saw a cold baseline: identically-scoped engines must share learned state")
	}
}

// TestDefaultScopeIsTheEmptyString pins the migration story. An Engine built
// without the option must land exactly where every pre-scope baseline
// already lives.
func TestDefaultScopeIsTheEmptyString(t *testing.T) {
	shared := store.NewInMemory()
	clock := newScopeClock()

	legacy := trustvian.NewEngine(trustvian.WithStore(shared))
	train(t, legacy, clock, 12)

	// An engine that names the default scope explicitly finds the same
	// state — which is what makes a pre-scope baseline readable.
	explicit := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope(""))
	res, err := explicit.Analyze(context.Background(), scopeEvent("evt-explicit", clock.next()))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if res.Anomaly.Confidence == 0 {
		t.Fatal(`WithLearningScope("") found no state: the default scope is not the empty string`)
	}
	if res.BaselineKey.Scope != "" {
		t.Errorf("default BaselineKey.Scope = %q, want empty", res.BaselineKey.Scope)
	}
}

// TestScopeComposesWithEnvironment: scope is an additional dimension, not a
// replacement for one.
func TestScopeComposesWithEnvironment(t *testing.T) {
	shared := store.NewInMemory()
	clock := newScopeClock()
	ctx := context.Background()

	identities := []struct{ scope, environment string }{
		{"scope-a", "production"},
		{"scope-a", "staging"},
		{"scope-b", "production"},
	}

	// Train only the first.
	target := identities[0]
	e := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope(target.scope))
	for i := range 20 {
		ev := scopeEvent(fmt.Sprintf("evt-%d", i), clock.next())
		ev.Context.Environment = target.environment
		res, err := e.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		if _, err := e.Observe(ctx, res); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	seen := map[string]bool{}
	for _, id := range identities {
		probe := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope(id.scope))
		ev := scopeEvent("evt-probe", clock.next())
		ev.Context.Environment = id.environment
		res, err := probe.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}

		key := res.BaselineKey
		if seen[key.Scope+"\x00"+key.Environment] {
			t.Fatalf("%+v is not a distinct learned identity", key)
		}
		seen[key.Scope+"\x00"+key.Environment] = true

		trained := id == target
		if got := res.Anomaly.Confidence > 0; got != trained {
			t.Errorf("(%s, %s): has learned history = %v, want %v",
				id.scope, id.environment, got, trained)
		}
	}
}

// TestEventContentCannotSelectScope is the security assertion.
//
// If telemetry could choose a profile, whoever emits events could append to
// whichever history a later decision depends on, or escape an established
// baseline by starting a fresh one. Scope is Engine configuration, and there
// is no code path from event content to Key.Scope — these are the fields an
// attacker would reach for.
func TestEventContentCannotSelectScope(t *testing.T) {
	shared := store.NewInMemory()
	clock := newScopeClock()
	ctx := context.Background()

	engine := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope("trusted"))
	train(t, engine, clock, 20)

	hostile := []struct {
		name   string
		mutate func(*event.Event)
	}{
		{"different session", func(e *event.Event) { e.Context.SessionID = "sess-attacker" }},
		{"different trace", func(e *event.Event) { e.Context.TraceID = "trace-attacker" }},
		{"delegation claim", func(e *event.Event) { e.Context.DelegatedFrom = "agent-orchestrator" }},
		{"scope in attributes", func(e *event.Event) {
			e.Attributes = map[string]any{"learning_scope": "attacker-scope", "scope": "attacker-scope"}
		}},
		{"candidate metadata in attributes", func(e *event.Event) {
			e.Attributes = map[string]any{
				"candidate_id": "cand-attacker", "evaluation_run_id": "run-1", "project_id": "proj-1",
				"git_sha": "9f3c1a", "baseline_key": "attacker-scope",
			}
		}},
	}

	for _, tt := range hostile {
		t.Run(tt.name, func(t *testing.T) {
			ev := scopeEvent("evt-hostile", clock.next())
			tt.mutate(&ev)

			res, err := engine.Analyze(ctx, ev)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			if res.BaselineKey.Scope != "trusted" {
				t.Fatalf("BaselineKey.Scope = %q, want %q — event content selected a learning scope",
					res.BaselineKey.Scope, "trusted")
			}
			// And it reached the engine's own trained history, rather than
			// a fresh one the event steered it into.
			if res.Anomaly.Confidence == 0 {
				t.Error("the event was analyzed against a cold baseline: it escaped the configured scope")
			}
		})
	}
}

// TestObserveReportsCapacityRefusal closes the accuracy gap task 049
// recorded. A scope that exists to be measured cannot silently stop learning
// while reporting that it did.
func TestObserveReportsCapacityRefusal(t *testing.T) {
	const capacity = 512
	ctx := context.Background()
	clock := newScopeClock()
	backing := store.NewInMemory()
	engine := trustvian.NewEngine(trustvian.WithStore(backing), trustvian.WithLearningScope("filling"))

	// Distinct operation names produce distinct fingerprints — the exact
	// shape ADR 0019 bounds.
	distinct := func(i int, ts time.Time) event.Event {
		ev := scopeEvent(fmt.Sprintf("evt-%d", i), ts)
		ev.Operation.Name = fmt.Sprintf("shell.execute.%d", i)
		return ev
	}

	var last trustvian.Result
	for i := range capacity {
		res, err := engine.Analyze(ctx, distinct(i, clock.next()))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		learned, err := engine.Observe(ctx, res)
		if err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		if !learned {
			t.Fatalf("observation %d reported learned == false below capacity", i)
		}
		last = res
	}

	fingerprintsBefore := knownFingerprints(t, backing, last)

	t.Run("unknown fingerprint at capacity is refused and reported", func(t *testing.T) {
		res, err := engine.Analyze(ctx, distinct(capacity+1, clock.next()))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		learned, err := engine.Observe(ctx, res)
		if err != nil {
			t.Fatalf("Observe() error = %v — capacity refusal must not be an error", err)
		}
		if learned {
			t.Fatal("Observe() reported learned == true for a fingerprint admission control refused")
		}
		if got := knownFingerprints(t, backing, res); got != fingerprintsBefore {
			t.Errorf("fingerprint count changed from %d to %d: the bound must refuse, never evict",
				fingerprintsBefore, got)
		}
	})

	t.Run("known fingerprint keeps learning at capacity", func(t *testing.T) {
		res, err := engine.Analyze(ctx, distinct(0, clock.next()))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		learned, err := engine.Observe(ctx, res)
		if err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		if !learned {
			t.Fatal("Observe() reported learned == false for a fingerprint the baseline already knows")
		}
	})
}

// TestCapacityIsPerScope: filling one profile must not consume another's.
func TestCapacityIsPerScope(t *testing.T) {
	const capacity = 512
	ctx := context.Background()
	clock := newScopeClock()
	shared := store.NewInMemory()

	distinct := func(i int, ts time.Time) event.Event {
		ev := scopeEvent(fmt.Sprintf("evt-%d", i), ts)
		ev.Operation.Name = fmt.Sprintf("shell.execute.%d", i)
		return ev
	}

	full := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope("full"))
	for i := range capacity {
		res, err := full.Analyze(ctx, distinct(i, clock.next()))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		if _, err := full.Observe(ctx, res); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	// Confirm the premise: this scope really is full.
	res, err := full.Analyze(ctx, distinct(capacity+1, clock.next()))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if learned, _ := full.Observe(ctx, res); learned {
		t.Fatal("premise broken: the filling scope is not actually at capacity")
	}

	// Same actor, same environment, same store, different scope.
	roomy := trustvian.NewEngine(trustvian.WithStore(shared), trustvian.WithLearningScope("roomy"))
	for i := range 5 {
		res, err := roomy.Analyze(ctx, distinct(capacity+100+i, clock.next()))
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		learned, err := roomy.Observe(ctx, res)
		if err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		if !learned {
			t.Fatalf("scope %q refused observation %d: capacity is being shared across scopes", "roomy", i)
		}
	}
}

// knownFingerprints reports how many fingerprint identities the baseline at
// res.BaselineKey holds. Read straight from the store rather than inferred
// from anomaly evidence: "did the bound evict anything" is a question about
// stored state, and inferring it from a score would test the wrong thing.
func knownFingerprints(t *testing.T, s store.Store, res trustvian.Result) int {
	t.Helper()
	bl, ok := s.Get(context.Background(), res.BaselineKey)
	if !ok {
		t.Fatalf("no baseline stored for %+v", res.BaselineKey)
	}
	return len(bl.Fingerprints)
}

// TestDecisionRecordGainsNoScopeOrPlatformFields guards task 050's boundary.
// Scope exists internally now, which is exactly the reason to assert it did
// not drift onto the public record.
func TestDecisionRecordGainsNoScopeOrPlatformFields(t *testing.T) {
	clock := newScopeClock()
	engine := trustvian.NewEngine(trustvian.WithLearningScope("scope-should-not-appear"))

	res, err := engine.Analyze(context.Background(), scopeEvent("evt-boundary", clock.next()))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	raw, err := json.Marshal(res.DecisionRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if strings.Contains(string(raw), "scope-should-not-appear") {
		t.Errorf("the learning scope leaked into DecisionRecord JSON:\n%s", raw)
	}
	for _, forbidden := range []string{
		"scope", "learning_scope", "candidate_id", "evaluation_run_id",
		"project_id", "profile_id", "agent_id", "tenant",
	} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Errorf("DecisionRecord JSON contains %q, which task 050's boundary excludes:\n%s", forbidden, raw)
		}
	}
}
