//go:build !windows

package main

// Tests for the OTLP path `trustvian dev` composes.
//
// The two-step readiness check is tested directly against real listeners rather
// than through a fake Collector process: what matters is that step 2 exists and
// fires when the health port is up and a receiver port is not, and a real
// listener is the only honest way to produce that state.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// Generated configuration
// ---------------------------------------------------------------------

func TestCollectorConfigRendersBothReceiversAndNoTelemetryBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, collectorConfigFile)

	if err := writeCollectorConfig(path, collectorConfigData{
		OTLPHTTPPort: 4318, OTLPGRPCPort: 4317, HealthPort: 13133,
	}); err != nil {
		t.Fatalf("writeCollectorConfig: %v", err)
	}

	rendered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the rendered config: %v", err)
	}
	config := string(rendered)

	for _, want := range []string{
		"endpoint: 127.0.0.1:4318",
		"endpoint: 127.0.0.1:4317",
		"endpoint: 127.0.0.1:13133",
		"processors: [trustvian]",
		"verbosity: basic",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("the config does not contain %q:\n%s", want, config)
		}
	}

	// trustvian-collector's minimal telemetry factory has configuration type
	// struct{}, so it accepts no keys under service.telemetry and rejects the
	// whole document if any appear. This cost one real startup failure to find.
	if strings.Contains(config, "telemetry:") {
		t.Errorf("the config declares service.telemetry, which this Collector refuses:\n%s", config)
	}

	// Verbosity stays basic: detailed would print span attributes into a log,
	// and this command must not become a way to read prompt content.
	if strings.Contains(config, "verbosity: detailed") {
		t.Error("the debug exporter is verbose enough to print span attributes")
	}
}

func TestCollectorConfigGoesToTheStateDirectory(t *testing.T) {
	// The configuration is a file dev writes, so it is the most obvious way this
	// command could end up writing into someone's repository.
	home, workload := t.TempDir(), t.TempDir()
	stateDir, err := devStateDir(home, workload)
	if err != nil {
		t.Fatalf("devStateDir: %v", err)
	}

	if err := writeCollectorConfig(filepath.Join(stateDir, collectorConfigFile),
		collectorConfigData{OTLPHTTPPort: 1, OTLPGRPCPort: 2, HealthPort: 3}); err != nil {
		t.Fatalf("writeCollectorConfig: %v", err)
	}

	entries, err := os.ReadDir(workload)
	if err != nil {
		t.Fatalf("reading the workload directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the collector config reached the workload directory: %d entries", len(entries))
	}
}

func TestValidateCollectorScalarRefusesUnsafeValues(t *testing.T) {
	// Nothing substitutes a string yet. This is the rule slice 3's run id and
	// behavioral profile will go through, written while the reason is in view.
	for _, tt := range []struct{ name, value string }{
		{"empty", ""},
		{"a newline", "run\n- evil: true"},
		{"a carriage return", "run\rid"},
		{"a quote", `run"id`},
		{"a backslash", `run\id`},
		{"a control character", "run\x01id"},
		{"leading whitespace", " run"},
		{"trailing whitespace", "run "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateCollectorScalar("run_id", tt.value); err == nil {
				t.Fatalf("%q was accepted", tt.value)
			}
		})
	}

	// Identity values that are ordinary pass through unchanged. `git:43af19c` is
	// the candidate format slice 3 derives from a commit, and an earlier rule
	// that refused every colon rejected it — which is why the template quotes
	// what it substitutes rather than guessing which punctuation YAML minds.
	for _, ok := range []string{
		"run-1", "git:43af19c", "git:43af19c+dirty",
		"dev-candidate-20260927T000000Z", "project/agent#2",
	} {
		if err := validateCollectorScalar("run_id", ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

// ---------------------------------------------------------------------
// Port reservation
// ---------------------------------------------------------------------

func TestReservePortsReturnsDistinctFreePorts(t *testing.T) {
	ports, err := reservePorts(3)
	if err != nil {
		t.Fatalf("reservePorts: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("got %d ports, want 3", len(ports))
	}

	seen := map[int]bool{}
	for _, port := range ports {
		if port <= 0 || port > 65535 {
			t.Errorf("port %d is not a usable port number", port)
		}
		if seen[port] {
			t.Errorf("port %d was returned twice; the Collector would fight itself for it", port)
		}
		seen[port] = true
	}

	// Closed by the time they are returned, so the Collector can bind them.
	for _, port := range ports {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Errorf("port %d was still held after reservation: %v", port, err)
			continue
		}
		listener.Close()
	}
}

// ---------------------------------------------------------------------
// Two-step readiness
// ---------------------------------------------------------------------

func TestAwaitReadySucceedsWhenHealthAndBothReceiversAreUp(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()

	httpPort, httpClose := listenOnFreePort(t)
	defer httpClose()
	grpcPort, grpcClose := listenOnFreePort(t)
	defer grpcClose()

	c := &collector{httpPort: httpPort, grpcPort: grpcPort,
		logPath: writeLogFixture(t, "ready"), waited: make(chan error, 1)}

	if err := c.awaitReady(portOf(t, health.URL)); err != nil {
		t.Fatalf("awaitReady: %v", err)
	}
}

func TestAwaitReadyFailsWhenAReceiverPortNeverBinds(t *testing.T) {
	restore := collectorReadyTimeout
	collectorReadyTimeout = 400 * time.Millisecond
	t.Cleanup(func() { collectorReadyTimeout = restore })

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()

	// Health is up; the HTTP receiver is not. This is the exact state a
	// health-only check would call ready — and the telemetry would then go
	// nowhere with nothing reporting it. The demo prototype hit this, which is
	// why step 2 exists.
	unbound := freePortNumber(t)
	grpcPort, grpcClose := listenOnFreePort(t)
	defer grpcClose()

	c := &collector{httpPort: unbound, grpcPort: grpcPort,
		logPath: writeLogFixture(t, "live but not receiving"), waited: make(chan error, 1)}

	err := c.awaitReady(portOf(t, health.URL))
	if err == nil {
		t.Fatal("awaitReady succeeded while a receiver port was unbound")
	}
	if !strings.Contains(err.Error(), "a receiver never bound") {
		t.Errorf("error does not name the unbound receiver: %v", err)
	}
	// Retryable: the port was lost to a race, not misconfigured.
	if !errors.Is(err, errCollectorPortTaken) {
		t.Errorf("error is not classified as a port collision: %v", err)
	}
	if !strings.Contains(err.Error(), "live but not receiving") {
		t.Errorf("error does not quote the collector's log: %v", err)
	}
}

func TestAwaitReadyFailsFastWhenTheCollectorExits(t *testing.T) {
	restore := collectorReadyTimeout
	collectorReadyTimeout = 10 * time.Second
	t.Cleanup(func() { collectorReadyTimeout = restore })

	c := &collector{httpPort: freePortNumber(t), grpcPort: freePortNumber(t),
		logPath: writeLogFixture(t, "cannot unmarshal the configuration"),
		waited:  make(chan error, 1)}
	// The process is already gone: a refused configuration exits rather than
	// serving. Waiting out the full timeout would report a timeout instead of
	// the reason, which is in the log.
	c.waited <- errors.New("exit status 1")

	started := time.Now()
	err := c.awaitReady(freePortNumber(t))
	if err == nil {
		t.Fatal("awaitReady succeeded for a collector that had exited")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("awaitReady took %v; it should notice the exit immediately", elapsed)
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("error does not say it exited: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal the configuration") {
		t.Errorf("error does not quote the collector's own reason: %v", err)
	}
}

func TestProbeHealthRequiresHTTPOK(t *testing.T) {
	// Anything but 200 is not ready. A Collector that answers 503 on /livez is
	// telling the truth about itself and must not be treated as up.
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		if probeHealth(portOf(t, server.URL)) {
			t.Errorf("status %d was treated as live", status)
		}
		server.Close()
	}
}

func TestCollectorStopIsIdempotent(t *testing.T) {
	// The same bug the runtime had: stop consumes from the channel Wait writes
	// to, so a second call must not wait out both grace periods.
	c := &collector{waited: make(chan error, 1)}
	c.stop()
	started := time.Now()
	c.stop()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the second stop took %v; stopOnce is not doing its job", elapsed)
	}
}

// ---------------------------------------------------------------------
// The workload's environment
// ---------------------------------------------------------------------

func TestRouteOTLPAddsExactlyTheDocumentedVariables(t *testing.T) {
	environment := captureEnvironment()
	environment.routeOTLP("http://127.0.0.1:4318", "127.0.0.1:4317")

	want := []string{
		envBatchDelay, envOTLPEndpoint, envOTLPProtocol, envLogsExport,
		envMetricsExport, envSemconvOptIn, envTracesExport, envGRPCEndpoint,
	}
	got := environment.Added()
	if len(got) != len(want) {
		t.Fatalf("dev added %d variables, want %d:\n got  %v\n want %v",
			len(got), len(want), got, want)
	}
	for _, name := range want {
		if !containsString(got, name) {
			t.Errorf("dev did not set %s", name)
		}
	}
}

func TestRouteOTLPPreservesTheInheritedEnvironment(t *testing.T) {
	t.Setenv("TRUSTVIAN_DEV_TEST_KEEP", "kept")
	environment := captureEnvironment()
	environment.routeOTLP("http://127.0.0.1:4318", "127.0.0.1:4317")

	entries := environment.Environ()
	if !containsString(entries, "TRUSTVIAN_DEV_TEST_KEEP=kept") {
		t.Error("an inherited variable was lost; the environment was replaced rather than extended")
	}
	if !containsString(entries, envOTLPEndpoint+"=http://127.0.0.1:4318") {
		t.Error("the OTLP endpoint was not applied")
	}
}

func TestSemanticConventionOptInIsAppendedNotReplaced(t *testing.T) {
	// The variable is a comma-separated opt-in list. A developer who opted into
	// something else keeps it; dev adds what the evidence needs.
	t.Setenv(envSemconvOptIn, "database")
	environment := captureEnvironment()
	environment.routeOTLP("http://127.0.0.1:4318", "127.0.0.1:4317")

	value := environment.additions[envSemconvOptIn]
	if !strings.Contains(value, "database") {
		t.Errorf("%s = %q; the developer's own opt-in was dropped", envSemconvOptIn, value)
	}
	if !strings.Contains(value, "http") {
		t.Errorf("%s = %q; without http, every span arrives with an empty target",
			envSemconvOptIn, value)
	}
}

func TestSemanticConventionOptInIsNotDuplicated(t *testing.T) {
	t.Setenv(envSemconvOptIn, "http")
	environment := captureEnvironment()
	environment.routeOTLP("http://127.0.0.1:4318", "127.0.0.1:4317")

	if got := environment.additions[envSemconvOptIn]; got != "http" {
		t.Errorf("%s = %q, want %q", envSemconvOptIn, got, "http")
	}
}

func TestInheritedReadsTheSnapshotNotTheAdditions(t *testing.T) {
	// The property slice 4's instrumentation evidence depends on. If Inherited
	// saw dev's own additions, `auto` would detect dev's configuration and
	// conclude the workload is already instrumented.
	if _, err := os.LookupEnv(envOTLPEndpoint); err {
		t.Skip("the test environment already sets " + envOTLPEndpoint)
	}
	environment := captureEnvironment()
	if _, present := environment.Inherited(envOTLPEndpoint); present {
		t.Fatalf("%s was in the snapshot before dev set anything", envOTLPEndpoint)
	}

	environment.routeOTLP("http://127.0.0.1:4318", "127.0.0.1:4317")

	if _, present := environment.Inherited(envOTLPEndpoint); present {
		t.Fatalf("%s appeared in the snapshot after dev set it; evidence would be dev's own",
			envOTLPEndpoint)
	}
}

func TestEnvironIsDeterministic(t *testing.T) {
	environment := captureEnvironment()
	environment.routeOTLP("http://127.0.0.1:4318", "127.0.0.1:4317")

	first := strings.Join(environment.Environ(), "\n")
	second := strings.Join(environment.Environ(), "\n")
	if first != second {
		t.Error("two renderings of one environment differ; map order is leaking")
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func listenOnFreePort(t *testing.T) (int, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	return listener.Addr().(*net.TCPAddr).Port, func() { listener.Close() }
}

// freePortNumber returns a port nothing is listening on.
//
// Bound and immediately released, which is exactly the advisory reservation the
// Collector path uses — so a test asserting "this port is not bound" is testing
// the same condition production meets.
func freePortNumber(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatalf("splitting %q: %v", rawURL, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parsing port %q: %v", port, err)
	}
	return number
}

func writeLogFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), collectorLogFile)
	if err := os.WriteFile(path, []byte(contents+"\n"), 0o600); err != nil {
		t.Fatalf("writing the log fixture: %v", err)
	}
	return path
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
