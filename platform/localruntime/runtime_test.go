package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// startRuntime brings one up in a temp state directory on an ephemeral port.
func startRuntime(t *testing.T, stateDir string) *Runtime {
	t.Helper()
	rt, err := Start(t.Context(), Options{StateDir: stateDir})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { rt.Stop(context.Background()) })
	return rt
}

// TestListenAddressMustBeLoopback is the security boundary.
//
// This runtime is unauthenticated, so loopback is not a convenience default —
// it is the only thing preventing an open control plane on a network.
func TestListenAddressMustBeLoopback(t *testing.T) {
	tests := []struct {
		name    string
		address string
		wantErr bool
	}{
		{"ipv4 loopback ephemeral", "127.0.0.1:0", false},
		{"ipv4 loopback fixed", "127.0.0.1:1", false},
		{"ipv6 loopback", "[::1]:0", false},
		{"other loopback address", "127.0.0.2:0", false},

		{"all interfaces", "0.0.0.0:0", true},
		{"all interfaces ipv6", "[::]:0", true},
		{"private range", "192.168.1.10:0", true},
		{"other private range", "10.0.0.5:0", true},
		{"public address", "203.0.113.1:0", true},
		// Refused even though it usually resolves to loopback: resolution is
		// not proof, and what localhost means is the resolver's opinion.
		{"hostname localhost", "localhost:0", true},
		{"arbitrary hostname", "example.test:0", true},
		{"no port", "127.0.0.1", true},
		{"invalid port", "127.0.0.1:notaport", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLoopbackAddress(tt.address)
			if tt.wantErr != (err != nil) {
				t.Fatalf("validateLoopbackAddress(%q) error = %v, wantErr = %v",
					tt.address, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrListenAddress) {
				t.Errorf("error does not wrap ErrListenAddress: %v", err)
			}
		})
	}
}

// TestStartBindsLoopbackAndPublishesDiscovery covers the startup contract.
func TestStartBindsLoopbackAndPublishesDiscovery(t *testing.T) {
	stateDir := t.TempDir()
	rt := startRuntime(t, stateDir)

	// The advertised endpoint is numeric loopback with a real port.
	parsed := mustParseHost(t, rt.APIURL())
	ip := net.ParseIP(parsed)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("API URL host %q is not numeric loopback", parsed)
	}
	if strings.HasSuffix(rt.APIURL(), ":0") {
		t.Fatal("the advertised port is 0; the bound port must be published")
	}

	// Discovery exists, is version 1, and names the bound endpoint.
	discovery, err := ReadDiscovery(rt.DiscoveryPath())
	if err != nil {
		t.Fatalf("ReadDiscovery: %v", err)
	}
	if discovery.Version != DiscoveryVersion {
		t.Errorf("version = %q", discovery.Version)
	}
	if discovery.APIURL != rt.APIURL() {
		t.Errorf("discovery %q != runtime %q", discovery.APIURL, rt.APIURL())
	}

	// It carries no secret: two fields and nothing else.
	raw, err := os.ReadFile(rt.DiscoveryPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("discovery is not JSON: %v", err)
	}
	if len(fields) != 2 {
		t.Errorf("discovery has %d fields, want exactly version and api_url: %v",
			len(fields), fields)
	}
	for _, forbidden := range []string{"token", "secret", "api_key", "password", "db", "database"} {
		if _, present := fields[forbidden]; present {
			t.Errorf("discovery carries a %q field; it is not authentication", forbidden)
		}
	}

	// And the endpoint actually serves.
	response, err := http.Get(rt.APIURL() + "/v1/projects/missing")
	if err != nil {
		t.Fatalf("the advertised endpoint is not reachable: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 from a live control plane", response.StatusCode)
	}
}

// TestDiscoveryIsPublishedOnlyAfterBind proves the ordering.
//
// A client that finds the file must find an endpoint that exists; publishing
// first would advertise something unbindable.
func TestDiscoveryIsPublishedOnlyAfterBind(t *testing.T) {
	stateDir := t.TempDir()

	// Hold the only port the runtime is allowed to use, so bind must fail.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer blocker.Close()

	_, err = Start(t.Context(), Options{
		StateDir:      stateDir,
		ListenAddress: blocker.Addr().String(),
	})
	if err == nil {
		t.Fatal("Start succeeded against an occupied port")
	}

	// No discovery file may exist after a failed bind.
	if _, statErr := os.Stat(filepath.Join(stateDir, DiscoveryFileName)); statErr == nil {
		t.Fatal("a discovery file was published despite the listener failing to bind")
	}
}

// TestStateDirectoryLayout pins what the runtime creates.
func TestStateDirectoryLayout(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "nested", ".trustvian")
	rt := startRuntime(t, stateDir)

	if _, err := os.Stat(rt.DatabasePath()); err != nil {
		t.Fatalf("platform database was not created: %v", err)
	}
	if filepath.Base(rt.DatabasePath()) != DatabaseFileName {
		t.Errorf("database name = %q", filepath.Base(rt.DatabasePath()))
	}

	if runtime.GOOS == "windows" {
		// POSIX mode bits are not implemented identically; the layout is the
		// claim here, not the permissions.
		return
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != stateDirMode {
		t.Errorf("state dir mode = %o, want %o", mode, stateDirMode)
	}
	discoveryInfo, err := os.Stat(rt.DiscoveryPath())
	if err != nil {
		t.Fatalf("Stat discovery: %v", err)
	}
	if mode := discoveryInfo.Mode().Perm(); mode != discoveryFileMode {
		t.Errorf("discovery mode = %o, want %o", mode, discoveryFileMode)
	}
}

// TestPlatformDatabaseIsNotTheEngineBaselineStore keeps the two persistence
// concerns apart.
//
// They have different lifecycles and different correctness properties, and
// merging them would be easy and quietly wrong.
func TestPlatformDatabaseIsNotTheEngineBaselineStore(t *testing.T) {
	rt := startRuntime(t, t.TempDir())
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	raw, err := os.ReadFile(rt.DatabasePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// SQLite keeps CREATE TABLE text in its schema, so a baseline table would
	// be visible in the file itself.
	content := strings.ToLower(string(raw))
	for _, forbidden := range []string{"baseline", "fingerprint_state", "actor_baseline"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("the platform database contains %q; engine baseline storage "+
				"must stay separate", forbidden)
		}
	}
	// And it does hold platform state.
	if !strings.Contains(content, "platform_schema_version") {
		t.Error("the platform database is missing its own schema table")
	}
}

// TestStopClosesEverything proves shutdown releases each resource.
func TestStopClosesEverything(t *testing.T) {
	stateDir := t.TempDir()
	rt := startRuntime(t, stateDir)
	apiURL := rt.APIURL()

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Listener closed: nothing answers, and the port can be rebound.
	if _, err := http.Get(apiURL + "/v1/projects/x"); err == nil {
		t.Error("the endpoint still answers after Stop")
	}
	// Discovery removed.
	if _, err := os.Stat(filepath.Join(stateDir, DiscoveryFileName)); !os.IsNotExist(err) {
		t.Errorf("discovery file survived shutdown: %v", err)
	}
	// Store closed: reopening the same file must succeed.
	reopened, err := startRuntimeErr(t, stateDir)
	if err != nil {
		t.Fatalf("the database is still locked after Stop: %v", err)
	}
	reopened.Stop(context.Background())

	// Stop is safe to call twice.
	if err := rt.Stop(context.Background()); err != nil {
		t.Errorf("second Stop returned %v", err)
	}
}

func startRuntimeErr(t *testing.T, stateDir string) (*Runtime, error) {
	t.Helper()
	return Start(context.Background(), Options{StateDir: stateDir})
}

// TestStaleShutdownDoesNotDeleteANewerRuntime is the ownership rule.
//
// A process shutting down late must not erase the endpoint a newer runtime
// already published — that would leave a working server undiscoverable.
func TestStaleShutdownDoesNotDeleteANewerRuntime(t *testing.T) {
	stateDir := t.TempDir()
	first := startRuntime(t, stateDir)

	// A newer runtime replaces the file.
	const newerURL = "http://127.0.0.1:65000"
	if err := writeDiscovery(first.DiscoveryPath(), newerURL); err != nil {
		t.Fatalf("writeDiscovery: %v", err)
	}

	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	discovery, err := ReadDiscovery(first.DiscoveryPath())
	if err != nil {
		t.Fatalf("the newer runtime's discovery file was deleted: %v", err)
	}
	if discovery.APIURL != newerURL {
		t.Errorf("api_url = %q, want the newer %q", discovery.APIURL, newerURL)
	}
}

// TestStartRefusesToReplaceALiveRuntime avoids two servers sharing a database.
func TestStartRefusesToReplaceALiveRuntime(t *testing.T) {
	stateDir := t.TempDir()
	first := startRuntime(t, stateDir)

	_, err := Start(t.Context(), Options{StateDir: stateDir})
	if err == nil {
		t.Fatal("a second runtime started against a live one in the same state directory")
	}
	if !strings.Contains(err.Error(), "already appears to be serving") {
		t.Errorf("error does not explain the conflict: %v", err)
	}

	// The live runtime is untouched.
	discovery, readErr := ReadDiscovery(first.DiscoveryPath())
	if readErr != nil || discovery.APIURL != first.APIURL() {
		t.Errorf("the live runtime's discovery was disturbed: %v %+v", readErr, discovery)
	}
}

// TestStartReplacesAStaleDiscoveryFile is the other half: a crashed runtime
// must not block the next one forever.
func TestStartReplacesAStaleDiscoveryFile(t *testing.T) {
	stateDir := t.TempDir()

	// A port nothing is listening on.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	deadURL := "http://" + probe.Addr().String()
	probe.Close()

	if err := os.MkdirAll(stateDir, stateDirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := writeDiscovery(filepath.Join(stateDir, DiscoveryFileName), deadURL); err != nil {
		t.Fatalf("writeDiscovery: %v", err)
	}

	rt := startRuntime(t, stateDir)
	if rt.APIURL() == deadURL {
		t.Fatal("the new runtime reused the stale endpoint")
	}
	discovery, err := ReadDiscovery(rt.DiscoveryPath())
	if err != nil {
		t.Fatalf("ReadDiscovery: %v", err)
	}
	if discovery.APIURL != rt.APIURL() {
		t.Errorf("stale discovery was not replaced: %q", discovery.APIURL)
	}
}

// TestReadDiscoveryBounds covers size, version and additive fields.
func TestReadDiscoveryBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DiscoveryFileName)

	write := func(content string) {
		if err := os.WriteFile(path, []byte(content), discoveryFileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	t.Run("additive fields tolerated", func(t *testing.T) {
		write(`{"version":"1","api_url":"http://127.0.0.1:1","future":{"a":1}}`)
		if _, err := ReadDiscovery(path); err != nil {
			t.Fatalf("additive fields must be tolerated: %v", err)
		}
	})

	t.Run("unknown version fails closed", func(t *testing.T) {
		write(`{"version":"2","api_url":"http://127.0.0.1:1"}`)
		if _, err := ReadDiscovery(path); err == nil {
			t.Fatal("an unknown version was accepted")
		}
	})

	t.Run("oversized file refused", func(t *testing.T) {
		write(`{"version":"1","api_url":"http://127.0.0.1:1","pad":"` +
			strings.Repeat("x", MaxDiscoveryFileBytes) + `"}`)
		if _, err := ReadDiscovery(path); err == nil {
			t.Fatal("an oversized discovery file was accepted")
		}
	})

	t.Run("missing api_url refused", func(t *testing.T) {
		write(`{"version":"1"}`)
		if _, err := ReadDiscovery(path); err == nil {
			t.Fatal("a discovery file without api_url was accepted")
		}
	})
}

// TestServerHasNoTotalWriteTimeout guards the SSE-compatible configuration.
//
// WriteTimeout is a total response deadline, and task 059's streams are
// long-lived by design with their own per-write bound. A server-wide value
// would eventually kill a healthy dashboard.
func TestServerHasNoTotalWriteTimeout(t *testing.T) {
	rt := startRuntime(t, t.TempDir())
	if rt.server.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v; a total write deadline would kill healthy SSE streams",
			rt.server.WriteTimeout)
	}
	// The other bounds do exist.
	if rt.server.ReadHeaderTimeout == 0 || rt.server.ReadTimeout == 0 ||
		rt.server.IdleTimeout == 0 || rt.server.MaxHeaderBytes == 0 {
		t.Error("request-side server bounds are missing")
	}
}

// TestRealtimeSubscriptionIsWired proves publisher and subscriber both reached
// the same bus.
func TestRealtimeSubscriptionIsWired(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	request, err := http.NewRequestWithContext(t.Context(), "GET",
		rt.APIURL()+"/v1/realtime?run_id=run-1", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("realtime request: %v", err)
	}
	defer response.Body.Close()

	// A missing subscriber would answer 503 realtime_unavailable.
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<10))
		t.Fatalf("realtime status = %d (%s); the subscriber is not wired",
			response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
}

func mustParseHost(t *testing.T, rawURL string) string {
	t.Helper()
	trimmed := strings.TrimPrefix(rawURL, "http://")
	host, _, err := net.SplitHostPort(trimmed)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", trimmed, err)
	}
	return host
}

// TestShutdownClosesTheBusBeforeWaitingForConnections proves the ordering
// claim, which nothing else does.
//
// Found by a surviving mutation: with no *active* SSE connection at shutdown,
// closing the bus first is unobservable, so every other test passed with the
// ordering removed. An open stream is what makes the difference measurable —
// the bus close is what lets that handler return, and without it
// server.Shutdown waits the full graceful bound before giving up.
func TestShutdownClosesTheBusBeforeWaitingForConnections(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, "GET",
		rt.APIURL()+"/v1/realtime?run_id=run-1", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("realtime: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("realtime status = %d", response.StatusCode)
	}

	// Read the handshake so the handler is genuinely streaming, not merely
	// accepted — otherwise the connection might not be holding anything.
	buffer := make([]byte, 1)
	if _, err := response.Body.Read(buffer); err != nil {
		t.Fatalf("reading the stream: %v", err)
	}

	started := time.Now()
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(started)

	// With the bus closed first the handler returns and shutdown is prompt.
	// Without it, graceful shutdown waits out the whole shutdownTimeout before
	// falling back to Close — a 5s floor against this 2s bound.
	if elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v with an open SSE stream; the realtime bus must "+
			"close first so in-flight handlers can exit", elapsed)
	}
}
