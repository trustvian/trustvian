package main

// The environment `trustvian dev` hands the workload.
//
// Two rules govern everything here.
//
// **The inherited environment is extended, never replaced.** The workload's own
// configuration — its PATH, its virtualenv, its API keys — is its business, and
// a wrapper that constructed a clean environment would break programs for
// reasons they could never diagnose. dev appends.
//
// **What is inherited is snapshotted before anything is set.** dev sets OTEL_*
// variables, so slice 4's instrumentation-ownership evidence has to be read from
// what the *developer* provided rather than from what dev provided a moment
// earlier. Otherwise `auto` would detect dev's own configuration and conclude
// the workload is already instrumented — a wrong answer that looks right.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// OpenTelemetry environment variables dev sets for the workload.
//
// Named as constants rather than written inline so the set is enumerable: the
// test that proves dev adds nothing else compares the child's environment
// against exactly this list, which is a stronger statement than "we did not mean
// to add anything".
const (
	envOTLPEndpoint  = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPProtocol  = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envTracesExport  = "OTEL_TRACES_EXPORTER"
	envMetricsExport = "OTEL_METRICS_EXPORTER"
	envLogsExport    = "OTEL_LOGS_EXPORTER"
	envSemconvOptIn  = "OTEL_SEMCONV_STABILITY_OPT_IN"
	envBatchDelay    = "OTEL_BSP_SCHEDULE_DELAY"

	// envGRPCEndpoint names the gRPC receiver, and it is dev's own variable
	// rather than an OpenTelemetry one.
	//
	// It exists because of a limitation dynamic ports impose. OTLP needs a port
	// per protocol, but OTEL_EXPORTER_OTLP_ENDPOINT carries exactly one endpoint
	// — so the standard environment can steer an auto-configuring SDK to one
	// protocol and no more. dev chooses HTTP there, which is what Python
	// zero-code instrumentation uses.
	//
	// A workload that builds a gRPC exporter in code reads neither variable and
	// would send to whatever it was compiled with — 4317 by convention, where
	// nothing is listening, and the export fails in its own log while dev looks
	// healthy. The gRPC receiver is enabled so that case *can* work, and this
	// variable is how such a workload is told where. Nothing is required to read
	// it; it is there so the answer exists rather than being unobtainable.
	envGRPCEndpoint = "TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT"

	// envResourceAttributes carries deployment.environment.name.
	envResourceAttributes = "OTEL_RESOURCE_ATTRIBUTES"
)

// otlpProtocol is HTTP rather than gRPC.
//
// The generated Collector configuration enables the HTTP receiver only, so this
// has to agree with it. HTTP because it is the smaller surface to expose on
// loopback and because it needs no h2c negotiation to debug when it goes wrong.
const otlpProtocol = "http/protobuf"

// batchScheduleDelayMillis shortens the SDK's batch flush interval.
//
// The default is five seconds. A developer running a short workload would
// otherwise wait most of it out after the process ends, and a workload that
// exits before a flush produces no evidence at all. 200ms is short enough to
// feel immediate and long enough that a chatty agent still batches.
const batchScheduleDelayMillis = "200"

// devEnvironment is the inherited environment plus dev's additions.
//
// snapshot is what the developer provided, kept so slice 4 can read evidence
// from it. additions are dev's, applied over the snapshot.
type devEnvironment struct {
	snapshot  map[string]string
	additions map[string]string
}

// captureEnvironment snapshots the inherited environment.
//
// Called before any variable is set. The snapshot is the trust boundary for
// instrumentation ownership: everything in it came from the developer or their
// shell, and nothing in it came from dev.
func captureEnvironment() *devEnvironment {
	snapshot := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		snapshot[name] = value
	}
	return &devEnvironment{snapshot: snapshot, additions: make(map[string]string)}
}

// Inherited reports what the developer provided for one variable.
//
// The only way slice 4 reads evidence, so that no code path can accidentally
// consult the mutated environment instead.
func (e *devEnvironment) Inherited(name string) (string, bool) {
	value, ok := e.snapshot[name]
	return value, ok
}

// routeOTLP points the workload's telemetry at dev's Collector.
//
// The endpoint is set even when the developer already had one: dev started a
// Collector for this run, and telemetry that went somewhere else would leave the
// run with no evidence while everything appeared to work. The inherited value is
// still visible through Inherited, which is where slice 4 reads it as evidence
// that the workload is instrumented at all.
func (e *devEnvironment) routeOTLP(endpoint, grpcEndpoint string) {
	e.additions[envOTLPEndpoint] = endpoint
	e.additions[envOTLPProtocol] = otlpProtocol
	e.additions[envGRPCEndpoint] = grpcEndpoint

	// Traces only. Metrics and logs exporters are switched off rather than left
	// at their defaults: an SDK that defaults to exporting them would send them
	// to this endpoint, where the pipeline has no receiver for them, and the
	// failures land in the workload's own log as errors it did not cause.
	e.additions[envTracesExport] = "otlp"
	e.additions[envMetricsExport] = "none"
	e.additions[envLogsExport] = "none"

	e.additions[envBatchDelay] = batchScheduleDelayMillis

	// Not optional, and not cosmetic. Without it, instrumentation emits the
	// legacy http.url and http.method attributes, while Trustvian's adapter
	// reads server.address and http.request.method — so every span arrives with
	// an empty target and the distinct behaviors observed collapse into fewer
	// than there are. The demo's prototype documents this as mandatory and its
	// contract test asserts the variable is present.
	//
	// Appended to any inherited value rather than replacing it: the variable is
	// a comma-separated opt-in list, and a developer who opted into something
	// else should keep it.
	e.additions[envSemconvOptIn] = appendCSV(e.snapshot[envSemconvOptIn], "http")
}

// declareIdentity makes the workload's telemetry describe the run dev
// provisioned.
//
// Two attributes, and both are load-bearing for reasons that are not obvious.
//
// **deployment.environment.name is not optional.** The engine fills
// Event.Context.Environment from it, and the platform refuses a record whose
// environment differs from the run's EnvironmentRef
// (platform/behavior.go, ErrBehaviorEnvironmentMismatch). A run created as
// "local" plus a workload emitting no such attribute produces *zero usable
// evidence*: every record is refused, one at a time, while everything else looks
// healthy. Appended to any inherited value rather than replacing it.
//
// **service.name is set only when the workload declares none.** The processor
// derives Actor.ID from it, so dev overriding a declared value would relabel
// somebody else's telemetry — and "read what their instrumentation already
// emits" is the product principle this command exists to honor. When the
// workload is silent, dev exports the agent it provisioned so the two cannot
// disagree.
func (e *devEnvironment) declareIdentity(identity devIdentity) error {
	if !identity.AgentFromTelemetry {
		e.additions[envServiceName] = identity.Agent
	}

	// A conflicting declared environment is refused rather than overridden or
	// quietly appended. The run's environment and the telemetry's must agree, and
	// the developer is the one who can decide which is right — appending a second
	// value would leave the SDK to pick, and overriding would discard a
	// deliberate choice.
	if inherited, ok := e.Inherited(envResourceAttributes); ok {
		if declared, found := resourceAttribute(inherited, deploymentEnvironmentKey); found &&
			declared != identity.Environment {
			return fmt.Errorf(
				"the workload declares %s=%s in %s, but this run is provisioned in "+
					"environment %q.\n\nThey have to agree: the platform refuses a record "+
					"whose environment\ndiffers from its run's. Either drop the attribute, or "+
					"run with\n\n  trustvian dev --environment %s -- <command>",
				deploymentEnvironmentKey, declared, envResourceAttributes,
				identity.Environment, declared)
		}
	}
	e.additions[envResourceAttributes] = appendResourceAttribute(
		e.snapshot[envResourceAttributes],
		deploymentEnvironmentKey, identity.Environment)
	return nil
}

// deploymentEnvironmentKey is the resource attribute the engine reads the
// environment from.
const deploymentEnvironmentKey = "deployment.environment.name"

// resourceAttribute reads one key out of an OTEL_RESOURCE_ATTRIBUTES value.
//
// The format is comma-separated key=value pairs. Whitespace around either side is
// tolerated because shells and Makefiles introduce it.
func resourceAttribute(attributes, key string) (string, bool) {
	for _, pair := range strings.Split(attributes, ",") {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		if strings.TrimSpace(name) == key {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// appendResourceAttribute adds a key=value pair, leaving the rest untouched.
func appendResourceAttribute(attributes, key, value string) string {
	pair := key + "=" + value
	if attributes == "" {
		return pair
	}
	if existing, found := resourceAttribute(attributes, key); found && existing == value {
		return attributes
	}
	return attributes + "," + pair
}

// Environ renders the environment for exec.
//
// Inherited entries first, dev's additions last, so a later duplicate wins —
// which is how every Unix exec resolves one. Sorted for determinism, so two runs
// of the same invocation hand the child the same environment and a test can
// compare them.
func (e *devEnvironment) Environ() []string {
	merged := make(map[string]string, len(e.snapshot)+len(e.additions))
	for name, value := range e.snapshot {
		merged[name] = value
	}
	for name, value := range e.additions {
		merged[name] = value
	}

	entries := make([]string, 0, len(merged))
	for name, value := range merged {
		entries = append(entries, fmt.Sprintf("%s=%s", name, value))
	}
	sort.Strings(entries)
	return entries
}

// Added reports the names dev set, for diagnostics and for the test that proves
// the set is exactly what it claims.
func (e *devEnvironment) Added() []string {
	names := make([]string, 0, len(e.additions))
	for name := range e.additions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// appendCSV adds a value to a comma-separated list without duplicating it.
//
// Order preserved and the existing content untouched: this is somebody else's
// configuration being extended, not replaced.
func appendCSV(existing, value string) string {
	if existing == "" {
		return value
	}
	for _, part := range strings.Split(existing, ",") {
		if strings.TrimSpace(part) == value {
			return existing
		}
	}
	return existing + "," + value
}
