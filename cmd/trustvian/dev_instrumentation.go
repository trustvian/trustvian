package main

// Instrumentation ownership for `trustvian dev`.
//
// The question this answers is narrow and the cost of guessing wrong is high: is
// the workload already going to initialize OpenTelemetry, or does dev need to
// attach something?
//
// There is no general way to know. A program can initialize the SDK from
// application code, a framework's startup path, a Java agent, site customization,
// an environment-driven loader, or a mechanism that does not exist yet — all
// *after* it starts, which is after the only moment dev could inspect it. So dev
// never reasons "I detected nothing, therefore there is nothing, therefore I will
// inject". That inference is wrong precisely when it is most expensive: a workload
// that instruments itself a moment later then has two stacks, every action is
// observed twice, and duplicate spans are a behavioral lie — one actor appearing
// to do everything twice, which is exactly the novelty Trustvian exists to notice.
//
// Hence positive evidence only, and a refusal when there is none.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// instrumentationMode is who owns the workload's OpenTelemetry setup.
type instrumentationMode string

const (
	// modeAuto resolves conservatively, or stops.
	modeAuto instrumentationMode = "auto"

	// modeExisting routes OTLP and injects nothing.
	modeExisting instrumentationMode = "existing"

	// modeNone manages no instrumentation at all, not even routing.
	modeNone instrumentationMode = "none"

	// modePythonZeroCode is reserved, and refused by this build.
	//
	// Named and parsed rather than omitted, so the name cannot be reused for
	// something else and a caller who asks for it gets a straight answer instead
	// of "unknown mode". See task 077 open question 2.
	modePythonZeroCode instrumentationMode = "python-zero-code"
)

// ownership is the resolved answer for one invocation.
type ownership struct {
	mode instrumentationMode

	// evidence is why auto chose existing, for the banner. Empty when the mode
	// was given explicitly.
	evidence string
}

// routesOTLP reports whether dev configures the workload's exporter.
func (o ownership) routesOTLP() bool { return o.mode == modeExisting }

// parseInstrumentationMode validates the flag.
//
// An unknown value is a usage error naming the modes, never a silent fallback to
// auto: a typo that quietly selected a mode would be the one thing this whole
// file exists to prevent.
func parseInstrumentationMode(value string) (instrumentationMode, error) {
	switch instrumentationMode(value) {
	case modeAuto, modeExisting, modeNone:
		return instrumentationMode(value), nil
	case modePythonZeroCode:
		return modePythonZeroCode, nil
	case "":
		return modeAuto, nil
	default:
		return "", fmt.Errorf(
			"unknown instrumentation mode %q; choose existing, none or auto", value)
	}
}

// resolveOwnership decides who owns instrumentation, or refuses.
//
// environment must be the pre-mutation snapshot. This is the subtlety that makes
// the whole mode meaningful: dev sets OTEL_* variables itself, so evidence read
// after that would be dev detecting its own configuration and concluding the
// workload is instrumented — a wrong answer that looks right.
func resolveOwnership(mode instrumentationMode, environment *devEnvironment,
	command []string) (ownership, error) {
	// A disabled SDK is checked first, and for every mode that would route.
	//
	// It is not weak evidence — it is the opposite of evidence. A workload whose
	// SDK is switched off will emit nothing, so routing telemetry to it produces
	// an evaluation run with no records, which the minimum-evidence gates then
	// fail for reasons that look nothing like the cause.
	if mode != modeNone {
		if value, ok := environment.Inherited(envSDKDisabled); ok && isTruthy(value) {
			return ownership{}, errSDKDisabled
		}
	}

	switch mode {
	case modePythonZeroCode:
		return ownership{}, errPythonZeroCodeUnavailable

	case modeExisting, modeNone:
		return ownership{mode: mode}, nil

	case modeAuto:
		if evidence, found := findInstrumentationEvidence(environment, command); found {
			return ownership{mode: modeExisting, evidence: evidence}, nil
		}
		// The zero-code branch the spec describes would be evaluated here. It is
		// deferred with the mode, so auto has exactly two outcomes in this build:
		// existing, or stop.
		return ownership{}, newNoEvidenceError(command)
	}

	return ownership{}, fmt.Errorf("unhandled instrumentation mode %q", mode)
}

// envSDKDisabled switches the SDK off, per the OpenTelemetry specification.
const envSDKDisabled = "OTEL_SDK_DISABLED"

// Environment variables whose presence is evidence of an SDK that will configure
// itself from the environment.
//
// Each one is read by an auto-configuring SDK, so a workload whose environment
// carries it has an SDK to configure. None of them is set by dev before this
// check runs.
const (
	envNodeOptions = "NODE_OPTIONS"
)

// findInstrumentationEvidence looks for proof that the workload is instrumented.
//
// Deliberately not exhaustive, and the spec says so. No list covers every
// instrumentation setup, which is exactly why auto fails closed rather than
// falling through to injection when the list finds nothing.
func findInstrumentationEvidence(environment *devEnvironment,
	command []string) (string, bool) {
	// Environment, from the developer's snapshot only.
	for _, name := range []string{
		envTracesExport,
		envOTLPEndpoint,
		envOTLPTracesEndpoint,
		envServiceName,
		envResourceAttributes,
	} {
		if value, ok := environment.Inherited(name); ok && strings.TrimSpace(value) != "" {
			return "$" + name + " is set", true
		}
	}

	if len(command) == 0 {
		return "", false
	}

	// A recognized zero-code wrapper in argv[0]. The workload is being started
	// *through* instrumentation, which is as positive as evidence gets.
	if filepath.Base(command[0]) == "opentelemetry-instrument" {
		return "the command runs through opentelemetry-instrument", true
	}

	for _, arg := range command {
		// A Java agent naming an OpenTelemetry jar. The name is checked as well as
		// the flag, because -javaagent: is used by profilers and coverage tools
		// that have nothing to do with OpenTelemetry.
		if after, ok := strings.CutPrefix(arg, "-javaagent:"); ok {
			jar := strings.ToLower(filepath.Base(after))
			if strings.Contains(jar, "opentelemetry") {
				return "a -javaagent: OpenTelemetry agent is on the command line", true
			}
		}
	}

	// NODE_OPTIONS requiring an OpenTelemetry package. Node's zero-code path is
	// `--require` of a registration module, so both halves are checked: --require
	// alone is used for all sorts of things.
	if value, ok := environment.Inherited(envNodeOptions); ok &&
		strings.Contains(value, "--require") &&
		strings.Contains(value, "@opentelemetry") {
		return "$NODE_OPTIONS requires an @opentelemetry package", true
	}

	return "", false
}

// isTruthy reads an OpenTelemetry boolean environment variable.
//
// The specification says these are case-insensitive "true"/"false". Anything else
// is not a disable, because guessing at "1" or "yes" would turn an unrelated
// value into a refusal.
func isTruthy(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// ---------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------

// devError carries a message that is already formatted for a developer.
//
// A type rather than errors.New so these can be built with the command in them
// while still being comparable with errors.Is where a test wants the class.
type devError struct {
	kind    string
	message string
}

func (e *devError) Error() string { return e.message }

// Is compares by kind, so a wrapped message with a command in it still matches.
func (e *devError) Is(target error) bool {
	other, ok := target.(*devError)
	return ok && other.kind == e.kind
}

// errPythonZeroCodeUnavailable refuses the reserved mode.
var errPythonZeroCodeUnavailable = &devError{
	kind: "python-zero-code-unavailable",
	message: "instrumentation mode python-zero-code is not available in this build.\n\n" +
		"It will attach a managed Python OpenTelemetry runtime to the workload after\n" +
		"proving the interpreter can load it — a virtualenv, a different Python\n" +
		"version, a conda environment and a system interpreter are all normal and all\n" +
		"break naive injection, so the mode waits for that compatibility check rather\n" +
		"than half-attaching. See task 077 open question 2.\n\n" +
		"For now:\n\n" +
		"  --instrumentation existing   your workload already sends OpenTelemetry\n" +
		"  --instrumentation none       dev manages no instrumentation at all",
}

// errSDKDisabled refuses to route telemetry to an SDK that is switched off.
var errSDKDisabled = &devError{
	kind: "sdk-disabled",
	message: "the workload's environment sets " + envSDKDisabled + "=true, so its\n" +
		"OpenTelemetry SDK will emit nothing.\n\n" +
		"Routing a disabled SDK to a Collector produces an evaluation run with no\n" +
		"records, which then fails the minimum-evidence gates for a reason that looks\n" +
		"nothing like the cause. So nothing was launched.\n\n" +
		"Unset " + envSDKDisabled + ", or run with --instrumentation none if the\n" +
		"telemetry reaches the Collector some other way.",
}

// newNoEvidenceError is the stop message for auto with nothing to go on.
//
// This is the message the whole section exists to produce, so it says what was
// looked for, why absence is not an answer, and what to do — rather than
// reporting a failure the developer cannot act on.
func newNoEvidenceError(command []string) error {
	return &devError{
		kind: "no-instrumentation-evidence",
		message: fmt.Sprintf(
			"cannot establish instrumentation ownership.\n\n"+
				"--instrumentation auto found no positive evidence that\n\n"+
				"    %s\n\n"+
				"already initializes OpenTelemetry, and absence of evidence is not evidence\n"+
				"of absence: a program that initializes the SDK a moment after it starts\n"+
				"cannot be detected beforehand. Attaching a second instrumentation stack to\n"+
				"one that exists would report every action twice, which reads as an actor\n"+
				"behaving strangely.\n\n"+
				"So nothing was launched. Choose explicitly:\n\n"+
				"  --instrumentation existing   your workload already sends OpenTelemetry;\n"+
				"                               dev configures OTLP and injects nothing\n"+
				"  --instrumentation none       dev manages no instrumentation at all;\n"+
				"                               telemetry reaches the Collector another way\n\n"+
				"  --instrumentation python-zero-code\n"+
				"                               not available in this build (task 077 open\n"+
				"                               question 2); it will attach a managed Python\n"+
				"                               runtime after compatibility checks",
			devCommandName(command)),
	}
}
