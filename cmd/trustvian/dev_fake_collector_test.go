//go:build !windows

package main

// A fake trustvian-collector, for the tests that exercise dev's composed path.
//
// The test binary re-executes itself. That is the idiomatic Go answer to "I need
// a child process that behaves a particular way", and here it is the only
// practical one: a fake Collector has to serve /livez over HTTP and bind two
// receiver ports, which a shell script cannot do portably, and compiling a
// separate helper would add a `go build` to every run of the suite.
//
// It reads the very configuration dev generated, which makes these tests assert
// something a hand-written fixture could not: that the document dev writes is one
// a Collector can actually find its ports in.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// fakeCollectorEnv makes the test binary behave as a Collector instead of a test.
//
// Checked in TestMain, before the testing framework runs anything.
const fakeCollectorEnv = "TRUSTVIAN_DEV_FAKE_COLLECTOR"

// fakeCollectorModeEnv selects how the fake misbehaves, for the failure paths.
const fakeCollectorModeEnv = "TRUSTVIAN_DEV_FAKE_COLLECTOR_MODE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeCollectorEnv) != "" {
		os.Exit(runFakeCollector())
	}
	os.Exit(m.Run())
}

// runFakeCollector serves what dev's readiness check looks for.
func runFakeCollector() int {
	configPath := ""
	for _, arg := range os.Args[1:] {
		if after, ok := strings.CutPrefix(arg, "--config="); ok {
			configPath = after
		}
	}
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "fake collector: no --config= argument")
		return 1
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake collector: reading %s: %v\n", configPath, err)
		return 1
	}
	ports := parseEndpointPorts(string(raw))
	if len(ports) != 3 {
		fmt.Fprintf(os.Stderr,
			"fake collector: expected 3 endpoints in the generated config, found %d\n",
			len(ports))
		return 1
	}
	// The template's order: http receiver, grpc receiver, processor health.
	httpPort, grpcPort, healthPort := ports[0], ports[1], ports[2]

	switch os.Getenv(fakeCollectorModeEnv) {
	case "refuse-config":
		fmt.Fprintln(os.Stderr, "fake collector: cannot unmarshal the configuration")
		return 1
	case "health-only":
		// Live, configured, and not receiving: the state a health-only readiness
		// check calls ready.
		grpcPort, httpPort = 0, 0
	}

	for _, port := range []int{httpPort, grpcPort} {
		if port == 0 {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake collector: binding %d: %v\n", port, err)
			return 1
		}
		defer listener.Close()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	healthListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", healthPort))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake collector: binding health %d: %v\n", healthPort, err)
		return 1
	}
	go http.Serve(healthListener, mux) //nolint:errcheck // the fake exits on a signal
	defer healthListener.Close()

	// Waits for SIGTERM, as the real Collector does, so dev's shutdown path is
	// what ends it.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	return 0
}

// endpointPattern matches the generated config's `endpoint: 127.0.0.1:PORT`
// lines, in document order.
var endpointPattern = regexp.MustCompile(`endpoint:\s*127\.0\.0\.1:(\d+)`)

func parseEndpointPorts(config string) []int {
	matches := endpointPattern.FindAllStringSubmatch(config, -1)
	ports := make([]int, 0, len(matches))
	for _, match := range matches {
		port, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		ports = append(ports, port)
	}
	return ports
}

// withFakeCollector points dev at the test binary as its Collector.
func withFakeCollector(t *testing.T, mode string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	t.Setenv(collectorBinaryEnv, executable)
	t.Setenv(fakeCollectorEnv, "1")
	t.Setenv(fakeCollectorModeEnv, mode)
}
