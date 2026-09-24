package trustvianprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"

	"trustvian-processor/internal/evaluation"
	"trustvian-processor/internal/health"
	"trustvian-processor/internal/metrics"
)

// Stats is a snapshot of this processor's observable counters: how
// many spans it has processed, how many didn't map to a valid Event or
// failed Analyze, and how many landed on each Decision — the
// concrete "observable" requirement task 009 names.
type Stats struct {
	SpansProcessed uint64
	Invalid        uint64
	AnalyzeErrors  uint64
	Decisions      map[string]uint64
}

// trustvianProcessor scores each span in a trace batch through a
// Trustvian Engine and enriches it with the resulting trustvian.*
// attributes, then forwards the (now-enriched) batch unchanged in
// shape to the next consumer in the pipeline.
type trustvianProcessor struct {
	engine *trustvian.Engine
	next   consumer.Traces
	logger *zap.Logger

	// closeStore releases the configured Store's resources on Shutdown —
	// the PostgreSQL backend owns a connection pool. Nil when the store
	// holds nothing releasable, which is every case except PostgreSQL, so
	// Shutdown must nil-check rather than assume.
	//
	// Held as a func rather than an io.Closer so the type assertion
	// happens once at construction, where config.CompileStorage's
	// documented lifecycle pattern belongs, instead of on every shutdown.
	closeStore func()

	// closeOnce guarantees the Store is closed exactly once, however many
	// times Shutdown is called. The Collector calls Shutdown once, but a
	// deferred cleanup alongside an explicit stop is an ordinary shape and
	// must not double-close a connection pool.
	closeOnce sync.Once

	// metrics instruments what Trustvian uniquely knows. Built from the
	// MeterProvider the Collector injects, so there is no global meter
	// state and the provider's lifecycle stays with the Collector — this
	// processor must never shut down a provider it did not create.
	metrics *metrics.Metrics

	// health is nil when no `health:` block was configured, which disables
	// the endpoints entirely. When present it is the single source of
	// lifecycle truth, consulted by the HTTP handler.
	health *health.Health

	// healthServer is the listener serving those endpoints, owned by this
	// component's Start/Shutdown so the Collector's own lifecycle drives it.
	healthServer *http.Server

	// evaluation is nil unless an `evaluation:` block was configured, which
	// is what keeps every existing deployment on exactly its previous span
	// path — no sink, no cursor, and no lock.
	//
	// When present it is the durable half of this processor: the span
	// enrichment beside it is advisory, and this is what an evaluation run
	// actually reads.
	evaluation *evaluation.Sink

	processed     atomic.Uint64
	invalid       atomic.Uint64
	analyzeErrors atomic.Uint64

	mu        sync.Mutex
	decisions map[string]uint64
}

// newTrustvianProcessor constructs the processor's Engine from cfg.
//
// cfg.Policy == nil (the field's zero value, meaning the Collector
// config had no `policy:` block at all) preserves this processor's
// original behavior exactly: trustvian.NewEngine() with no options,
// i.e. its own default Policy (no rules, ObserveOnly fallback).
//
// cfg.Policy != nil — including an explicitly empty `policy: {}` block
// — is treated as an explicit request for a configured Policy: it is
// decoded, validated, and compiled via decodePolicy/config.CompilePolicy,
// the exact same path the Go SDK and CLI use. Any failure in that path
// (a decode error, an invalid schema version, a missing default, an
// invalid decision/condition value) is returned here rather than
// falling back to the default Policy — this is what lets
// createTracesProcessor fail Collector startup outright on a bad
// explicit config instead of silently weakening it.
func newTrustvianProcessor(set component.TelemetrySettings, next consumer.Traces, cfg *Config) (*trustvianProcessor, error) {
	var opts []trustvian.Option
	if cfg.Policy != nil {
		pc, err := decodePolicy(cfg.Policy)
		if err != nil {
			return nil, err
		}
		p, err := config.CompilePolicy(pc)
		if err != nil {
			return nil, fmt.Errorf("trustvianprocessor: policy: %w", err)
		}
		opts = append(opts, trustvian.WithPolicy(p))
	}

	closeStore := func() {}

	// Held so readiness can probe it below. Typed `any` because the Store's
	// concrete type lives in the core module's internal/ tree and cannot be
	// named from this module; nil when no storage was configured, in which
	// case the Engine uses its own in-memory default and there is nothing
	// external to probe.
	var configuredStore any

	if cfg.Storage != nil {
		sc, err := decodeStorage(cfg.Storage)
		if err != nil {
			return nil, err
		}
		// config.CompileStorage validates, connects, and migrates. Every
		// failure returns a nil Store and an error, and returning that
		// error here is what makes the fail-closed contract survive
		// containerization: createTracesProcessor propagates it and the
		// Collector refuses to start. A Collector that fell back to the
		// in-memory store when its database was unreachable would keep
		// scoring spans against state that silently evaporates on exit —
		// see docs/SECURITY.md § Storage configuration.
		s, err := config.CompileStorage(sc)
		if err != nil {
			return nil, fmt.Errorf("trustvianprocessor: storage: %w", err)
		}
		configuredStore = s
		if c, ok := s.(io.Closer); ok {
			closeStore = func() { _ = c.Close() }
		}
		opts = append(opts, trustvian.WithStore(s))

		// The backend type, never the DSN. Knowing which store a Collector
		// came up on is the first thing an operator wants from its logs;
		// the connection string is a secret and must not appear in them.
		set.Logger.Info("trustvianprocessor: storage backend initialized",
			zap.String("type", string(sc.Type)))
	}

	// EvaluationConfig.validate() already ran one call earlier, in
	// createTracesProcessor (processor/factory.go), and is not repeated
	// here. Policy and Storage validate inside this function because their
	// validation is inseparable from compiling a value the Engine actually
	// needs — CompilePolicy produces the policy.Policy, CompileStorage opens
	// the Store. EvaluationConfig.validate() is a pure shape check that
	// produces nothing for the Engine to consume, so it can run one call
	// earlier and fail Collector startup sooner. newTrustvianProcessor is
	// unexported and reached only through the factory, so cfg has already
	// been validated by the time it reaches here.
	if cfg.Evaluation != nil {
		// The behavioral profile is the platform's name for a learning scope
		// (ADR 0024), so configuring one selects it here too. Without this, a
		// Collector with a durable store would train one baseline across
		// every candidate it evaluated, each teaching the next — the precise
		// failure task 051 exists to prevent.
		//
		// Scope and Environment are separate dimensions of baseline.Key, so
		// this changes which learned history the analysis is compared
		// against and changes no field the platform validates.
		opts = append(opts, trustvian.WithLearningScope(cfg.Evaluation.BehavioralProfile))
	}

	// The Collector always supplies a MeterProvider; a no-op one yields
	// no-op instruments, so nothing here branches on whether metrics are
	// "enabled". Instrumentation is a side effect, never a dependency.
	//
	// A construction failure is logged and the processor runs
	// uninstrumented rather than failing to start. Instrument names here
	// are constants, so a conformant SDK cannot reject them — but a
	// non-conformant one must not be able to stop a security component
	// from making decisions. metrics.Metrics's nil value records nothing
	// and is safe to call, which is what makes degrading this cheap.
	m, err := metrics.New(set.MeterProvider.Meter(metrics.ScopeName))
	if err != nil {
		m = nil
		set.Logger.Error("trustvianprocessor: metrics unavailable; continuing uninstrumented",
			zap.Error(err))
	}

	p := &trustvianProcessor{
		engine:     trustvian.NewEngine(opts...),
		next:       next,
		logger:     set.Logger,
		closeStore: closeStore,
		metrics:    m,
		decisions:  make(map[string]uint64),
	}

	// Constructed after p, because the sink is handed p.learn: the sink owns
	// the order in which a record is delivered, confirmed, learned from and
	// released, and that order is what keeps a durable baseline from holding
	// a record the run does not (see ADR 0038 §9 and §10). The engine stays
	// out of the sink — all it receives is an opaque payload to hand back.
	if cfg.Evaluation != nil {
		sink, sinkErr := evaluation.New(
			cfg.Evaluation.APIURL, cfg.Evaluation.RunID, cfg.Evaluation.BehavioralProfile,
			cfg.Evaluation.PendingStatePath, p.learn)
		if sinkErr != nil {
			return nil, fmt.Errorf("trustvianprocessor: evaluation: %w", sinkErr)
		}
		p.evaluation = sink

		// The run, the profile and where the pending state lives — never the
		// URL's userinfo, and there is none, because validate rejected it.
		set.Logger.Info("trustvianprocessor: evaluation ingest configured",
			zap.String("run_id", cfg.Evaluation.RunID),
			zap.String("behavioral_profile", cfg.Evaluation.BehavioralProfile),
			zap.String("pending_state_path", cfg.Evaluation.PendingStatePath))
	}

	if cfg.Health != nil {
		hc := cfg.Health.withDefaults()
		p.health = health.New(storeProbe(configuredStore), hc.ReadinessTimeout)
		p.healthServer = &http.Server{
			Addr:    hc.Endpoint,
			Handler: health.Handler(p.health, set.Logger.Sugar().Warnf),
			// Bounded header read, so a half-open probe connection cannot
			// pin a goroutine indefinitely.
			ReadHeaderTimeout: 5 * time.Second,
		}
	}

	return p, nil
}

// pinger is the optional store capability readiness needs, declared here in
// the consumer rather than imported.
//
// That is what makes this work at all: the Store arrives as an opaque value
// whose concrete type lives in the core module's internal/store/postgres,
// which this separate module cannot import. Go satisfies interfaces
// structurally, so asserting against a locally declared interface reaches
// the method with no shared type and no public API — the same shape the
// io.Closer lifecycle assertion above already uses.
type pinger interface {
	Ping(context.Context) error
}

// storeProbe returns the readiness probe for s, or nil when s has no
// external dependency to probe.
//
// nil is a correct answer, not a missing check: InMemory and FileStore have
// nothing that can become unavailable, so a runtime configured with either
// is ready as soon as it has started. Only a database-backed store can be
// configured-but-unusable, which is precisely the state v0.8's fail-closed
// contract requires readiness to report.
// s is typed `any` because its concrete type is unnameable here: that is
// the whole reason this indirection exists.
func storeProbe(s any) health.ProbeFunc {
	if p, ok := s.(pinger); ok {
		return p.Ping
	}
	return nil
}

// Capabilities reports that this processor mutates its input in place
// (it writes trustvian.* attributes directly onto each span) — the
// Collector's own contract for processors that don't copy their input
// before modifying it.
func (p *trustvianProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

// Start and Shutdown are no-ops: this processor holds no external
// connections, background goroutines, or resources beyond the Engine
// itself (which is fully synchronous — see docs/PERFORMANCE.md
// § Concurrency considerations in the core repository).
// Start brings up the health listener, if one is configured, and records
// that initialization finished.
//
// A bind failure is returned rather than logged: a health surface that
// silently failed to exist is worse than none, because the absence looks
// identical to a healthy runtime that nobody is probing.
func (p *trustvianProcessor) Start(ctx context.Context, _ component.Host) error {
	// Before the health listener, and deliberately before the early return
	// below: a Collector with no `health:` block is the common case, and an
	// initialization that sat behind that return would silently never run.
	//
	// Failing here is the fail-closed contract. A Collector that came up
	// without its ingest cursor would enrich spans while recording nothing,
	// and the absence looks identical to a healthy runtime nobody is
	// evaluating.
	if p.evaluation != nil {
		recovery, err := p.evaluation.Initialize(ctx)
		if err != nil {
			return fmt.Errorf("trustvianprocessor: evaluation: %w", err)
		}
		switch recovery.Outcome {
		case evaluation.RecoveryCompleted:
			// A previous process died with this record's fate unknown. The
			// control plane holds it, so its learning was applied here —
			// exactly once, because the pending state proved that process
			// never got that far — before any new span is analyzed.
			p.logger.Info("trustvianprocessor: finished a record left pending by a previous process",
				zap.String("run_id", p.evaluation.RunID()),
				zap.Uint64("sequence", recovery.Sequence),
				zap.String("disposition", recovery.Disposition))
		case evaluation.RecoveryDiscarded:
			// It never reached the run, so nothing was learned from it and
			// nothing is posted for it now. The span it came from was
			// dropped with its batch; this is that batch's loss being
			// visible rather than a new one.
			p.logger.Warn("trustvianprocessor: a record left pending by a previous process never "+
				"reached the run and was discarded; nothing was learned from it",
				zap.String("run_id", p.evaluation.RunID()),
				zap.Uint64("sequence", recovery.Sequence))
		case evaluation.RecoveryIndeterminate:
			// The one case a restart cannot settle: the record is in the run,
			// and whether the previous process applied its learning before
			// dying is unknowable. It is not applied again — an observation
			// applied twice would make a fingerprint look more familiar than
			// the evidence supports — and this line is what keeps that from
			// being silent.
			p.logger.Error("trustvianprocessor: a record the run holds may not have been learned from; "+
				"its learning was not applied again, because applying it twice cannot be ruled out",
				zap.String("run_id", p.evaluation.RunID()),
				zap.Uint64("sequence", recovery.Sequence))
		}
		p.logger.Info("trustvianprocessor: evaluation ingest ready",
			zap.String("run_id", p.evaluation.RunID()))
	}

	if p.healthServer == nil {
		return nil
	}

	// Listen synchronously so a bind error fails Start, then serve in the
	// background. ListenAndServe would report the same error only after
	// Start had already succeeded.
	listener, err := net.Listen("tcp", p.healthServer.Addr)
	if err != nil {
		return fmt.Errorf("trustvianprocessor: health endpoint %s: %w", p.healthServer.Addr, err)
	}

	go func() {
		if err := p.healthServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.logger.Error("trustvianprocessor: health server stopped", zap.Error(err))
		}
	}()

	// Only now is the runtime ready to be probed as running: readiness from
	// here on reflects the store rather than initialization.
	p.health.MarkRunning()
	p.logger.Info("trustvianprocessor: health endpoints listening",
		zap.String("endpoint", p.healthServer.Addr),
		zap.String("liveness", health.PathLive),
		zap.String("readiness", health.PathReady),
	)
	return nil
}

// Shutdown releases the configured Store's resources — for the PostgreSQL
// backend, its connection pool. Without this, stopping a Collector left
// server-side connections to be reaped by timeout rather than closed, and
// a container restart loop would accumulate them.
//
// It does not flush or finalize anything: Observe commits synchronously
// inside its own transaction, so there is never pending learned state to
// lose at shutdown. That property is what makes this a teardown rather than
// a drain.
//
// The drain itself belongs to the Collector, which stops components in
// topological order — receivers before processors — specifically "so that
// each component has a chance to drain to its consumer before the consumer
// is stopped" (service/internal/graph). New spans have therefore stopped
// arriving and in-flight ones have already passed through by the time this
// runs, so adding a drain mechanism here would duplicate the framework's
// and create a second shutdown owner.
//
// Ordering within this method is deliberate; see the numbered steps.
func (p *trustvianProcessor) Shutdown(ctx context.Context) error {
	// 1. Readiness first, before anything is torn down, so an external
	//    observer sees "not ready" rather than a refused connection. By the
	//    time the Collector calls this, it has already stopped the receivers
	//    and drained in-flight spans through this processor — see the
	//    ordering note on this method's doc comment.
	if p.health != nil {
		p.health.MarkDraining()
	}

	var errs error

	// 2. Then the health server, before the store: its readiness probe uses
	//    the store, so stopping it in the other order would let a probe race
	//    a closing connection pool.
	if p.healthServer != nil {
		if err := p.healthServer.Shutdown(ctx); err != nil {
			errs = errors.Join(errs, fmt.Errorf("health server shutdown: %w", err))
		}
	}

	// 3. Finally the store, exactly once.
	p.closeOnce.Do(func() {
		if p.closeStore != nil {
			p.closeStore()
		}
	})

	// Returned, not swallowed: a failure to release resources is
	// operationally relevant, and the Collector aggregates it.
	return errs
}

// ConsumeTraces scores every span in td and forwards td (now enriched
// in place) to the next consumer. A span that doesn't map to a
// Validate-passing Event, or whose Analyze call errors, is left
// un-enriched and counted, but never stops the batch: one malformed
// span must not drop every other span in the same trace.
//
// The one exception is an evaluation ingest failure that the sink could not
// resolve: td is abandoned rather than forwarded, and the method returns
// that (already permanent) error instead. A retried batch would re-analyze
// spans whose records already committed and resend them under new sequence
// numbers, silently doubling the evidence — so losing the rest of this batch
// is preferable to risking that.
//
// A lost response is not that failure. The sink re-presents the same record
// at the same sequence and the control plane replays it, so that case
// resolves inside Record and the batch continues; only a record whose fate
// is still unknown, or one the control plane declined outright, gets here.
func (p *trustvianProcessor) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	for _, rs := range td.ResourceSpans().All() {
		resourceAttrs := rs.Resource().Attributes()
		for _, ss := range rs.ScopeSpans().All() {
			spans := ss.Spans()
			for i := range spans.Len() {
				// Only an unresolved or declined evaluation ingest can return
				// an error here, and it is already permanent. The batch is
				// abandoned rather than forwarded: a retry would re-analyze
				// spans whose records already committed and resend them under
				// new sequence numbers, silently doubling the evidence.
				if err := p.processSpan(ctx, resourceAttrs, spans.At(i)); err != nil {
					return err
				}
			}
		}
	}
	return p.next.ConsumeTraces(ctx, td)
}

func (p *trustvianProcessor) processSpan(ctx context.Context, resourceAttrs pcommon.Map, span ptrace.Span) error {
	p.processed.Add(1)

	ev := EventFromSpan(resourceAttrs, span)
	if err := ev.Validate(); err != nil {
		p.invalid.Add(1)
		p.metrics.RecordAnalysis(ctx, metrics.OutcomeInvalidEvent, 0)
		p.logger.Debug("trustvianprocessor: span did not map to a valid Event",
			zap.String("span", span.Name()), zap.Error(err))
		return nil
	}

	// Measures Trustvian's own analysis only — the engine call and nothing
	// around it. Starting the clock earlier would fold in span mapping and
	// the Collector's own receive path, which the Collector already
	// measures and which would make this number mean something different
	// than its name says.
	start := time.Now()
	result, err := p.engine.Analyze(ctx, ev)
	analysisDuration := time.Since(start)

	if err != nil {
		p.analyzeErrors.Add(1)
		p.metrics.RecordAnalysis(ctx, metrics.OutcomeError, analysisDuration)
		p.logger.Warn("trustvianprocessor: Analyze failed",
			zap.String("span", span.Name()), zap.Error(err))
		return nil
	}
	p.metrics.RecordAnalysis(ctx, metrics.OutcomeAnalyzed, analysisDuration)

	SetAttributesFromResult(span.Attributes(), result)
	p.recordDecision(string(result.Decision))
	p.metrics.RecordDecision(ctx, string(result.Decision))

	// The record is projected from the Result already computed above — the
	// same one that produced the attributes written a few lines up. Analyze
	// is not run again, and nothing is rebuilt from those attributes: they
	// are a five-value subset of what a record carries, and reconstructing
	// from them would make an evaluation's evidence a function of the
	// enrichment format.
	if p.evaluation != nil {
		// The learning travels with the record rather than being applied
		// here. The sink writes both to its pending state before the request
		// leaves, and applies the learning only once the control plane has
		// confirmed the record — which is what keeps a durable baseline from
		// holding a record the run does not, across a crash as well as
		// within this call. See ADR 0038 §9.
		learning, err := json.Marshal(result)
		if err != nil {
			// Fail closed. A record whose learning cannot be made durable is
			// one a restart could not finish, and delivering it anyway is
			// precisely the divergence this design exists to prevent.
			p.logger.Error("trustvianprocessor: encoding the record's learning failed",
				zap.String("span", span.Name()), zap.Error(err))
			return consumererror.NewPermanent(
				fmt.Errorf("trustvianprocessor: evaluation ingest: encoding learning: %w", err))
		}

		ingestStart := time.Now()
		disposition, err := p.evaluation.Record(ctx, result.DecisionRecord(), learning)
		ingestDuration := time.Since(ingestStart)
		if err != nil {
			p.metrics.RecordEvaluationIngest(ctx, metrics.OutcomeError, ingestDuration)
			p.logger.Error("trustvianprocessor: evaluation ingest failed",
				zap.String("span", span.Name()),
				zap.String("run_id", p.evaluation.RunID()),
				zap.Bool("unresolved", errors.Is(err, evaluation.ErrUnresolved)),
				zap.Error(err))
			// Permanent, so the pipeline does not retry. See ConsumeTraces.
			return consumererror.NewPermanent(
				fmt.Errorf("trustvianprocessor: evaluation ingest: %w", err))
		}
		p.metrics.RecordEvaluationIngest(ctx, disposition, ingestDuration)
		// Observe already ran, inside Record, exactly once, and only because
		// the control plane confirmed the record.
		return nil
	}

	p.observe(ctx, span.Name(), result)
	return nil
}

// learn applies one record's learning, decoded from the payload the sink
// holds beside it.
//
// The live span and a recovery in a later process go through this same
// function, on the same bytes. Encoding and decoding the Result on the live
// path costs a JSON round trip per span and buys the property that matters:
// what a restart would apply is exactly what this process applies, so a
// payload that could not carry the learning fails on the first span rather
// than after a crash.
func (p *trustvianProcessor) learn(ctx context.Context, learning []byte) error {
	var result trustvian.Result
	if err := json.Unmarshal(learning, &result); err != nil {
		// Fail closed: this is the payload a restart depends on, and one
		// that cannot be read is one whose record can never be learned from.
		return fmt.Errorf("decoding the pending learning: %w", err)
	}
	p.observe(ctx, "evaluation ingest", result)
	return nil
}

// observe folds one Result into the baseline and records the outcome.
//
// Observe is always safe to call unconditionally — it is a no-op for any
// Decision that isn't learning-eligible (see the core repository's
// docs/SECURITY.md § baseline poisoning).
//
// A failure is reported and never fatal, which is deliberate and unchanged:
// the alternative is a store blip stopping a Collector from forwarding
// traces. With `evaluation:` configured it means the run can hold a record
// this baseline did not learn from — reported, in the direction that fails
// safe, since a fingerprint short one observation looks less familiar rather
// than more.
func (p *trustvianProcessor) observe(ctx context.Context, source string, result trustvian.Result) {
	observeStart := time.Now()
	learned, err := p.engine.Observe(ctx, result)
	observeDuration := time.Since(observeStart)

	switch {
	case err != nil:
		p.metrics.RecordObservation(ctx, metrics.OutcomeError, observeDuration)
		p.logger.Warn("trustvianprocessor: Observe failed",
			zap.String("span", source),
			zap.String("fingerprint", result.Fingerprint.ID),
			zap.Error(err))
	case learned:
		p.metrics.RecordObservation(ctx, metrics.OutcomeLearned, observeDuration)
	default:
		// The decision was not learning-eligible. Expected and common —
		// counted, never logged, because logging it would produce a line
		// per blocked action.
		p.metrics.RecordObservation(ctx, metrics.OutcomeNotEligible, observeDuration)
	}
}

func (p *trustvianProcessor) recordDecision(decision string) {
	p.mu.Lock()
	p.decisions[decision]++
	p.mu.Unlock()
}

// Stats returns a snapshot of this processor's observable counters.
func (p *trustvianProcessor) Stats() Stats {
	p.mu.Lock()
	decisions := make(map[string]uint64, len(p.decisions))
	for k, v := range p.decisions {
		decisions[k] = v
	}
	p.mu.Unlock()

	return Stats{
		SpansProcessed: p.processed.Load(),
		Invalid:        p.invalid.Load(),
		AnalyzeErrors:  p.analyzeErrors.Load(),
		Decisions:      decisions,
	}
}

var (
	_ consumer.Traces     = (*trustvianProcessor)(nil)
	_ component.Component = (*trustvianProcessor)(nil)
)
