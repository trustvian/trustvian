package trustvian

import (
	"context"
	"fmt"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/anomaly"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/policy"
	"github.com/trustvian/trustvian/internal/store"
	"github.com/trustvian/trustvian/internal/trust"
)

// Engine is the composition root of the Trustvian pipeline: Event ->
// Features -> Fingerprint -> Baseline -> Anomaly -> Trust -> Policy ->
// Decision. Construct one with NewEngine; it is safe for concurrent use
// by multiple goroutines once constructed, since nothing mutates an
// Engine's fields after NewEngine returns.
//
// An Engine is fully configurable from outside this module. Four of the
// five options take a value produced by the config package, which is
// public:
//
//	policy, err := config.CompilePolicy(policyCfg)   // WithPolicy
//	store, err := config.CompileStorage(storageCfg)  // WithStore
//	anomalyCfg, err := config.CompileAnomaly(cfg)    // WithAnomalyConfig
//	trustCfg, err := config.CompileTrust(cfg)        // WithTrustConfig
//
// The fifth, WithContextRisk, takes a callback over StableFeatures,
// which is a public type in this package. No internal package needs to
// be imported to configure any of them.
//
// The option parameter types themselves are defined under internal/, so
// a caller receives those values and passes them on without naming
// them. That is deliberate: it keeps the engine's internal types free
// to evolve while giving callers a complete configuration path. The
// same arrangement lets a caller read every field of Result without
// importing anything beyond this package.
//
// Persistence is selected through config.StorageConfig from the
// backends Trustvian ships. Supplying a custom Store implementation is
// not a supported extension point — see docs/compatibility.md.
//
// A store compiled from configuration is owned by the caller, not by
// the Engine: an Engine never closes a store it was given. When the
// compiled store holds resources (a PostgreSQL pool, an open file),
// close it yourself:
//
//	if c, ok := store.(io.Closer); ok {
//		defer c.Close()
//	}
type Engine struct {
	store         store.Store
	policy        policy.Policy
	anomalyConfig anomaly.Config
	trustConfig   trust.Config
	contextRisk   func(features.StableFeatures) float64

	// learningScope partitions learned state. "" is the default scope,
	// which is what every Engine built before WithLearningScope existed
	// uses and what every pre-scope persisted baseline already is. See
	// WithLearningScope.
	learningScope string
}

// NewEngine constructs an Engine, applying opts in order over sane
// defaults: an in-memory Store, a no-rules/ObserveOnly-default Policy,
// anomaly.DefaultConfig, trust.DefaultConfig, and a ContextRisk function
// that always returns 0.
func NewEngine(opts ...Option) *Engine {
	e := &Engine{
		store: store.NewInMemory(),
		policy: policy.Policy{
			DefaultAction: policy.DecisionObserveOnly,
			DefaultReason: "no policy rules configured; observing by default",
		},
		anomalyConfig: anomaly.DefaultConfig(),
		trustConfig:   trust.DefaultConfig(),
		contextRisk:   func(features.StableFeatures) float64 { return 0 },
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Analyze runs ev through the full pipeline and returns a Result. It is
// read-only: it never modifies the Engine's Baseline. Call Observe with
// the returned Result to (conditionally) learn from ev.
func (e *Engine) Analyze(ctx context.Context, ev event.Event) (Result, error) {
	if err := ev.Validate(); err != nil {
		return Result{}, fmt.Errorf("trustvian: invalid event: %w", err)
	}

	feat := features.Extract(ev)
	fp := fingerprint.Compute(feat.Stable)
	// Scope comes from this Engine's configuration, never from ev. An
	// event producer must not be able to choose which learned history it
	// trains — see WithLearningScope and docs/SECURITY.md.
	key := baseline.Key{Scope: e.learningScope, ActorID: ev.Actor.ID, Environment: ev.Context.Environment}

	bl, _ := e.store.Get(ctx, key)
	an := anomaly.Score(feat, fp, bl, e.anomalyConfig)
	tr := trust.Compute(an, ev.Actor.IdentityConfidence, e.contextRisk(feat.Stable), e.trustConfig)
	pr := e.policy.Evaluate(policy.Input{Stable: feat.Stable, Trust: tr, Attributes: ev.Attributes, ApprovalStatus: ev.Context.ApprovalStatus})

	return Result{
		Event:       ev,
		Features:    feat,
		Fingerprint: fp,
		BaselineKey: key,
		Anomaly:     an,
		Trust:       tr,
		Decision:    pr.Decision,
		Explanation: pr.Explanation,
	}, nil
}

// eligibleForLearning reports whether a Decision indicates the event it
// was computed for is safe to fold into the Baseline. The dividing line
// is whether the action proceeded: ALLOW, OBSERVE_ONLY and ALERT all let
// the event through (ALERT only adds visibility), so learning from them
// is safe. CHALLENGE, REQUIRE_APPROVAL and BLOCK all hold or stop the
// action pending confirmation, so they are excluded.
//
// ALERT deliberately stays eligible, not just ALLOW/OBSERVE_ONLY: a
// brand-new but ultimately benign Fingerprint can transiently cross into
// ALERT-level risk purely from partial maturity (see
// TestObserveLearnsOnlyFromEligibleDecisions) — categorical novelty is
// still ramping down even though nothing is actually wrong. Excluding
// ALERT from learning would create a deadlock: the very observations
// needed to mature the Fingerprint past that transient risk are the ones
// being rejected for not being mature enough yet, so it would stay
// ALERT-flagged forever instead of settling once familiar.
func eligibleForLearning(d policy.Decision) bool {
	switch d {
	case policy.DecisionAllow, policy.DecisionObserveOnly, policy.DecisionAlert:
		return true
	default:
		return false
	}
}

// Observe conditionally applies result to the Engine's Baseline: it is a
// no-op unless result.Decision is eligible for learning (see
// eligibleForLearning). This is what makes Observe safe to call
// unconditionally after every Analyze — the gating that prevents
// baseline poisoning lives here, not in caller discipline.
//
// result must have been produced by this Engine's Analyze (or one
// configured identically); Observe trusts its Fingerprint, Features, and
// BaselineKey rather than recomputing them. BaselineKey carries the
// learning scope Analyze read from, so the write lands in exactly the
// history the analysis was made against — nothing is recomputed from
// event metadata, and no event field can redirect it.
//
// learned is the truth about what happened, not a restatement of
// eligibility. It is false when the decision was ineligible, false when
// the store declined the write (a frozen key), and false when the
// baseline is at its fingerprint capacity and this fingerprint is one
// it has never seen — that observation is refused, never evicting
// anything, and reporting it as learning would tell a caller measuring
// a baseline's growth that it grew when it did not. A known fingerprint
// keeps learning at capacity and still reports true. See
// docs/adr/0019-bounded-fingerprint-admission.md for the admission
// policy, which this does not change.
func (e *Engine) Observe(ctx context.Context, result Result) (learned bool, err error) {
	if !eligibleForLearning(result.Decision) {
		return false, nil
	}
	_, learned, err = e.store.Observe(ctx, result.BaselineKey, result.Fingerprint, result.Features.Volatile, result.Event.Timestamp)
	if err != nil {
		return false, err
	}
	return learned, nil
}
