package trustvian

import (
	"github.com/trustvian/trustvian/internal/anomaly"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/policy"
	"github.com/trustvian/trustvian/internal/store"
	"github.com/trustvian/trustvian/internal/trust"
)

// Option configures an Engine at construction time.
type Option func(*Engine)

// WithStore replaces the Engine's Baseline persistence. The default is an
// in-memory store (see store.NewInMemory): baselines do not survive
// process restarts unless a persistent Store is supplied.
func WithStore(s store.Store) Option {
	return func(e *Engine) { e.store = s }
}

// WithPolicy replaces the Engine's Policy. The default policy has no
// rules and an ObserveOnly fallback — a conservative starting point that
// never silently allows, and never blocks, until rules are configured.
func WithPolicy(p policy.Policy) Option {
	return func(e *Engine) { e.policy = p }
}

// WithAnomalyConfig replaces the thresholds and weights Score combines
// anomaly signals with. See anomaly.DefaultConfig for the baseline
// values.
func WithAnomalyConfig(cfg anomaly.Config) Option {
	return func(e *Engine) { e.anomalyConfig = cfg }
}

// WithTrustConfig replaces the RiskLevel bucket thresholds Trust is
// evaluated with. See trust.DefaultConfig for the baseline values.
func WithTrustConfig(cfg trust.Config) Option {
	return func(e *Engine) { e.trustConfig = cfg }
}

// WithContextRisk replaces the function used to derive a deterministic
// context-risk penalty from an event's stable features — a statement
// that some kinds of operation are inherently sensitive regardless of
// how familiar they have become. The returned value is clamped to
// [0, 1] and reduces the trust score multiplicatively; the default
// always returns 0, applying no penalty.
//
// This is the one behavioral input Trustvian does not learn: a context
// risk is configuration, not history, which is why repeating a
// sensitive operation never erodes it.
//
// fn must be pure and safe for concurrent use — Engine calls it on
// every Analyze, from whatever goroutine called Analyze.
func WithContextRisk(fn func(StableFeatures) float64) Option {
	return func(e *Engine) {
		e.contextRisk = func(sf features.StableFeatures) float64 {
			return fn(publicStableFeatures(sf))
		}
	}
}

// WithLearningScope partitions the Engine's learned state. Two engines
// with different scopes never share a Baseline, even for the same actor
// in the same environment: each accumulates its own history, and each
// starts cold.
//
//	engine := trustvian.NewEngine(trustvian.WithLearningScope("reference"))
//
// The default is "" — the scope every Engine used before this option
// existed, and the one every baseline persisted before it already
// belongs to. Omitting the option changes nothing about how an Engine
// behaves or what state it finds.
//
// A scope is an opaque namespace. Trustvian never parses it, derives
// nothing from its contents, and gives no string special meaning: a
// caller that wants "candidate/<sha>" may use it, and the engine sees a
// namespace. What the scope *means* belongs to whoever chose it.
//
// It is not behavioral identity. The same event analyzed under two
// scopes produces the same StableFeatures and the same Fingerprint ID;
// only the learned history it is compared against differs. Scope decides
// which history an observation belongs to, never what the actor did — a
// scope that entered behavioral identity would make an actor's whole
// repertoire look brand new every time a caller opened a new one. See
// docs/adr/0024-learning-scope-is-a-baseline-key-dimension.md.
//
// Scope is configuration, deliberately not an Event field, and cannot
// be derived from SessionID, TraceID, or Attributes. If telemetry could
// select a profile, whoever emits events could choose which learned
// history they train — appending to a profile a later decision depends
// on, or escaping an established baseline by starting a fresh one. The
// Engine's operator chooses; event producers do not.
//
// Fixed at construction, like every other Engine option. Each scoped
// baseline carries its own independent fingerprint capacity: filling one
// scope does not consume another's.
func WithLearningScope(scope string) Option {
	return func(e *Engine) { e.learningScope = scope }
}

// publicStableFeatures converts the internal feature representation
// into the public one handed to a WithContextRisk callback. Feature
// derivation itself stays in internal/features — this is a projection
// at the public boundary, not a second implementation of it.
func publicStableFeatures(sf features.StableFeatures) StableFeatures {
	return StableFeatures{
		ActorType:         sf.ActorType,
		OperationCategory: sf.OperationCategory,
		OperationName:     sf.OperationName,
		TargetName:        sf.TargetName,
		TargetCategory:    sf.TargetCategory,
		Environment:       sf.Environment,
	}
}
