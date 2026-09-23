package trustvianprocessor

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
)

// componentType is this processor's registered component type — the
// "trustvian" in a Collector config's `processors: trustvian:` block.
var componentType = component.MustNewType("trustvian")

// NewFactory returns the component.Factory for this processor, for a
// Collector binary or builder manifest to register alongside its
// other receivers/processors/exporters.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		componentType,
		createDefaultConfig,
		processor.WithTraces(createTracesProcessor, component.StabilityLevelDevelopment),
	)
}

func createDefaultConfig() component.Config {
	return &Config{}
}

// createTracesProcessor constructs this processor's Engine, including
// compiling any configured Policy, before returning — a compile
// failure here fails the whole Collector's startup (this is called
// during pipeline graph construction, before Start), never a fallback
// to the default Policy at the first span. See newTrustvianProcessor.
//
// The error case returns a bare nil, not newTrustvianProcessor's own
// (*trustvianProcessor)(nil) forwarded directly: converting a nil
// pointer of a concrete type into the processor.Traces interface
// return value produces a *non-nil* interface (it carries type
// information, only the underlying value is nil) — Go's classic
// typed-nil-in-interface trap. A caller's `proc != nil` check would
// wrongly see a "processor" on a failed construction if this returned
// p directly on the error path.
func createTracesProcessor(_ context.Context, set processor.Settings, cfg component.Config, next consumer.Traces) (processor.Traces, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("trustvianprocessor: unexpected config type %T", cfg)
	}
	// Evaluation is validated here, one call before newTrustvianProcessor
	// runs — unlike Policy and Storage, which that function validates
	// itself, as the first thing it does (see decodePolicy/CompilePolicy
	// and decodeStorage/CompileStorage in processor.go). That is not an
	// inconsistency to fix: Policy and Storage validation is inseparable
	// from compiling a value the Engine actually needs (a policy.Policy,
	// an open Store), so it has to happen where the Engine is built.
	// EvaluationConfig.validate() is a pure shape check with no product to
	// hand the Engine, so it can run one call earlier — failing Collector
	// startup sooner is strictly better, and there is nothing here for
	// newTrustvianProcessor to do with the result.
	if c.Evaluation != nil {
		if err := c.Evaluation.validate(); err != nil {
			return nil, err
		}
	}
	p, err := newTrustvianProcessor(set.TelemetrySettings, next, c)
	if err != nil {
		return nil, err
	}
	return p, nil
}
