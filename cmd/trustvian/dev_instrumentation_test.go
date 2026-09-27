//go:build !windows

package main

// Tests for instrumentation ownership.
//
// The fail-closed case is the regression test for the whole section: it is the one
// that fails if anyone decides absence of evidence is good enough to inject on.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// Mode parsing
// ---------------------------------------------------------------------

func TestParseInstrumentationMode(t *testing.T) {
	tests := []struct {
		value string
		want  instrumentationMode
		bad   bool
	}{
		{value: "", want: modeAuto},
		{value: "auto", want: modeAuto},
		{value: "existing", want: modeExisting},
		{value: "none", want: modeNone},
		// Reserved, so the name cannot be reused for something else.
		{value: "python-zero-code", want: modePythonZeroCode},
		{value: "existng", bad: true},
		{value: "EXISTING", bad: true},
		{value: "yes", bad: true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseInstrumentationMode(tt.value)
			if tt.bad {
				if err == nil {
					t.Fatalf("%q was accepted as %q", tt.value, got)
				}
				// A typo must not silently select a mode.
				if !strings.Contains(err.Error(), "existing, none or auto") {
					t.Errorf("the error does not name the modes: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q was refused: %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("%q parsed as %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The evidence list, one case each
// ---------------------------------------------------------------------

func TestAutoAcceptsEachFormOfPositiveEvidence(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		command []string
		want    string
	}{
		{
			name: "OTEL_TRACES_EXPORTER",
			env:  map[string]string{envTracesExport: "otlp"},
			want: "$" + envTracesExport,
		},
		{
			name: "OTEL_EXPORTER_OTLP_ENDPOINT",
			env:  map[string]string{envOTLPEndpoint: "http://collector.invalid:4318"},
			want: "$" + envOTLPEndpoint,
		},
		{
			name: "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
			env:  map[string]string{envOTLPTracesEndpoint: "http://collector.invalid/v1/traces"},
			want: "$" + envOTLPTracesEndpoint,
		},
		{
			name: "OTEL_SERVICE_NAME",
			env:  map[string]string{envServiceName: "support-agent"},
			want: "$" + envServiceName,
		},
		{
			name: "OTEL_RESOURCE_ATTRIBUTES",
			env:  map[string]string{envResourceAttributes: "service.name=support-agent"},
			want: "$" + envResourceAttributes,
		},
		{
			name:    "opentelemetry-instrument in argv[0]",
			command: []string{"/venv/bin/opentelemetry-instrument", "python", "agent.py"},
			want:    "opentelemetry-instrument",
		},
		{
			name: "a -javaagent OpenTelemetry jar",
			command: []string{"java",
				"-javaagent:/opt/otel/opentelemetry-javaagent.jar", "-jar", "app.jar"},
			want: "-javaagent:",
		},
		{
			name: "NODE_OPTIONS requiring an @opentelemetry package",
			env: map[string]string{
				envNodeOptions: "--require @opentelemetry/auto-instrumentations-node/register",
			},
			want: "$NODE_OPTIONS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := tt.command
			if command == nil {
				command = []string{"python", "agent.py"}
			}
			owner, err := resolveOwnership(modeAuto, environmentWith(t, tt.env), command)
			if err != nil {
				t.Fatalf("auto refused despite %s: %v", tt.name, err)
			}
			if owner.mode != modeExisting {
				t.Fatalf("auto resolved to %q, want %q", owner.mode, modeExisting)
			}
			// The banner says why, so the evidence has to be reported.
			if !strings.Contains(owner.evidence, tt.want) {
				t.Errorf("evidence = %q, want it to mention %q", owner.evidence, tt.want)
			}
		})
	}
}

func TestAutoIgnoresEvidenceThatIsNotEvidence(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		command []string
	}{
		{
			// An empty value is not a declaration.
			name: "an empty OTEL variable",
			env:  map[string]string{envTracesExport: "   "},
		},
		{
			// -javaagent: is used by profilers and coverage tools. The flag alone
			// proves nothing about OpenTelemetry.
			name:    "a -javaagent that is not OpenTelemetry",
			command: []string{"java", "-javaagent:/opt/jacoco/jacocoagent.jar", "-jar", "app.jar"},
		},
		{
			// --require is used for all sorts of things.
			name: "NODE_OPTIONS requiring something else",
			env:  map[string]string{envNodeOptions: "--require ts-node/register"},
		},
		{
			name: "an unrelated variable",
			env:  map[string]string{"PYTHONPATH": "/app"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := tt.command
			if command == nil {
				command = []string{"python", "agent.py"}
			}
			if _, err := resolveOwnership(modeAuto, environmentWith(t, tt.env), command); err == nil {
				t.Fatalf("%s was accepted as evidence", tt.name)
			}
		})
	}
}

// TestAutoFailsClosedWithNoEvidence is the regression test for the section.
//
// It is the test that fails if anyone decides absence of evidence is good enough
// to inject on. The cost of that decision is a workload observed twice, which
// reads as an actor doing everything twice — the exact novelty Trustvian exists to
// notice, manufactured by the tool meant to notice it.
func TestAutoFailsClosedWithNoEvidence(t *testing.T) {
	_, err := resolveOwnership(modeAuto, environmentWith(t, map[string]string{}),
		[]string{"python", "agent.py"})
	if err == nil {
		t.Fatal("auto selected a mode with no evidence")
	}

	message := err.Error()
	for _, want := range []string{
		"cannot establish instrumentation ownership",
		"absence of evidence is not evidence",
		"nothing was launched",
		"--instrumentation existing",
		"--instrumentation none",
		"python-zero-code",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not contain %q:\n%s", want, message)
		}
	}
}

// ---------------------------------------------------------------------
// A disabled SDK is the opposite of evidence
// ---------------------------------------------------------------------

func TestADisabledSDKStopsRatherThanRouting(t *testing.T) {
	// Routing a switched-off SDK produces a run with no records, which the
	// minimum-evidence gates then fail for a reason that looks nothing like the
	// cause. So dev says what is actually wrong.
	for _, mode := range []instrumentationMode{modeAuto, modeExisting} {
		t.Run(string(mode), func(t *testing.T) {
			environment := environmentWith(t, map[string]string{
				envSDKDisabled: "true",
				// Evidence is present, so this is not the no-evidence path.
				envTracesExport: "otlp",
			})
			_, err := resolveOwnership(mode, environment, []string{"python", "agent.py"})
			if err == nil {
				t.Fatal("dev routed telemetry to a disabled SDK")
			}
			if !errors.Is(err, errSDKDisabled) {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(err.Error(), "emit nothing") {
				t.Errorf("the message does not say the workload emits nothing:\n%v", err)
			}
		})
	}
}

func TestADisabledSDKIsAllowedWithNone(t *testing.T) {
	// `none` makes no claim about the workload's SDK: telemetry reaches the
	// Collector another way, and a disabled in-process SDK may be exactly why.
	environment := environmentWith(t, map[string]string{envSDKDisabled: "true"})
	owner, err := resolveOwnership(modeNone, environment, []string{"python", "agent.py"})
	if err != nil {
		t.Fatalf("none was refused: %v", err)
	}
	if owner.mode != modeNone {
		t.Fatalf("mode = %q, want %q", owner.mode, modeNone)
	}
}

func TestOnlyTrueDisablesTheSDK(t *testing.T) {
	// The specification says these are case-insensitive true/false. Guessing at
	// "1" or "yes" would turn an unrelated value into a refusal.
	for _, value := range []string{"true", "TRUE", " True "} {
		if !isTruthy(value) {
			t.Errorf("%q was not read as true", value)
		}
	}
	for _, value := range []string{"false", "1", "yes", "", "0"} {
		if isTruthy(value) {
			t.Errorf("%q was read as true", value)
		}
	}
}

// ---------------------------------------------------------------------
// The reserved mode
// ---------------------------------------------------------------------

func TestPythonZeroCodeIsReservedAndRefused(t *testing.T) {
	_, err := resolveOwnership(modePythonZeroCode,
		environmentWith(t, map[string]string{}), []string{"python", "agent.py"})
	if err == nil {
		t.Fatal("python-zero-code was accepted by this build")
	}
	if !errors.Is(err, errPythonZeroCodeUnavailable) {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"not available in this build", "compatibility check"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not contain %q:\n%v", want, err)
		}
	}
}

// ---------------------------------------------------------------------
// What each mode does to the child's environment
// ---------------------------------------------------------------------

func TestExistingInjectsNothingBeyondRouting(t *testing.T) {
	// The guarantee `existing` makes, asserted by difference rather than by
	// intent: the delta between the parent's environment and the child's is
	// exactly the documented set, and no injection variable is in it.
	t.Setenv(envTracesExport, "otlp")
	t.Setenv("PATH", os.Getenv("PATH"))

	environment := captureEnvironment()
	identity := devIdentity{
		Project: "p", Agent: "a", AgentName: "a", Candidate: "c",
		Environment: "local", Run: "r", Profile: "c",
	}
	if err := environment.declareIdentity(identity); err != nil {
		t.Fatalf("declareIdentity: %v", err)
	}
	environment.routeOTLP("http://127.0.0.1:4318", "127.0.0.1:4317")

	allowed := map[string]bool{
		envOTLPEndpoint: true, envOTLPProtocol: true, envTracesExport: true,
		envMetricsExport: true, envLogsExport: true, envSemconvOptIn: true,
		envBatchDelay: true, envGRPCEndpoint: true,
		envOTLPTracesEndpoint: true, envOTLPTracesProtocol: true,
		envOTLPHeaders: true, envOTLPTracesHeaders: true,
		envResourceAttributes: true, envServiceName: true,
	}
	for _, name := range environment.Added() {
		if !allowed[name] {
			t.Errorf("existing set %s, which is not in the documented set", name)
		}
	}

	// And specifically none of the ways a wrapper injects instrumentation.
	for _, forbidden := range []string{
		"PYTHONPATH", "PYTHONSTARTUP", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES",
		"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "NODE_OPTIONS",
	} {
		if _, set := environment.additions[forbidden]; set {
			t.Errorf("existing injected instrumentation through %s", forbidden)
		}
	}
}

func TestExistingAddsNoWrapperToArgv(t *testing.T) {
	// The other half of "injects nothing": the command is run as given. A wrapper
	// in argv is how a workload acquires a second instrumentation stack.
	out := runDevCapturingStdout(t, []string{"--", "sh", "-c", `printf '%s' "$0"`})
	if got := strings.TrimSpace(out); got != "sh" {
		t.Fatalf("argv[0] = %q, want %q; something was prepended to the command", got, "sh")
	}
}

func TestNoneRoutesNothing(t *testing.T) {
	owner := ownership{mode: modeNone}
	if owner.routesOTLP() {
		t.Fatal("none routes OTLP; dev was told to manage no instrumentation")
	}
	owner = ownership{mode: modeExisting}
	if !owner.routesOTLP() {
		t.Fatal("existing does not route OTLP, which is the one thing it does")
	}
}

// ---------------------------------------------------------------------
// Composition: a refusal starts and provisions nothing
// ---------------------------------------------------------------------

func TestARefusedOwnershipStartsNothingAndProvisionsNothing(t *testing.T) {
	// Ownership is resolved before anything is started, so a refusal leaves no
	// helper running and no evaluation run to fail. A fake API that records every
	// call proves the second half.
	api := newFakeDevAPI(t)
	defer api.Close()

	// A helper path that would fail if it were ever executed.
	t.Setenv(localRuntimeBinaryEnv, filepath.Join(t.TempDir(), "must-not-run"))
	t.Setenv(collectorBinaryEnv, filepath.Join(t.TempDir(), "must-not-run"))
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))

	marker := filepath.Join(dir, "workload-ran")
	var errOut strings.Builder
	// Identity is explicit; instrumentation deliberately is not, so auto has to
	// refuse.
	code := runDev(streams{out: io_Discard{}, err: &errOut},
		[]string{"--project", "p", "--agent", "a", "--candidate", "c",
			"--api-url", api.URL, "--", "sh", "-c", "printf ran > " + marker})

	if code != exitDevUsage {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitDevUsage, errOut.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the workload ran despite a refused ownership")
	}
	if len(api.postOrder()) != 0 {
		t.Errorf("a refusal provisioned %v", api.postOrder())
	}
	if !strings.Contains(errOut.String(), "cannot establish instrumentation ownership") {
		t.Errorf("stderr is not the ownership refusal:\n%s", errOut.String())
	}
}

func TestExplicitExistingNeedsNoEvidence(t *testing.T) {
	// The point of an explicit mode: a developer who knows their workload is
	// instrumented should not have to satisfy a heuristic.
	owner, err := resolveOwnership(modeExisting,
		environmentWith(t, map[string]string{}), []string{"python", "agent.py"})
	if err != nil {
		t.Fatalf("explicit existing was refused: %v", err)
	}
	if owner.mode != modeExisting {
		t.Fatalf("mode = %q, want %q", owner.mode, modeExisting)
	}
	// No evidence is reported, because none was needed.
	if owner.evidence != "" {
		t.Errorf("evidence = %q, want empty for an explicit mode", owner.evidence)
	}
}
