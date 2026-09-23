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
	"sync"
	"testing"
	"time"

	"trustvian-platform/webui"
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

// TestDiscoveryMustBeExactlyOneJSONDocument closes the parser gap.
//
// json.Decoder reads one value and stops, so a file whose first object parsed
// was accepted regardless of what followed it. Unmarshal requires the whole
// payload to be one value, while still tolerating unknown fields inside it and
// whitespace after it.
func TestDiscoveryMustBeExactlyOneJSONDocument(t *testing.T) {
	const first = `{"version":"1","api_url":"http://127.0.0.1:1"}`
	const second = `{"version":"1","api_url":"http://127.0.0.1:2"}`

	tests := []struct {
		name    string
		content string
		wantURL string // empty means the read must fail
	}{
		{"single document", first, "http://127.0.0.1:1"},
		{"trailing newline", first + "\n", "http://127.0.0.1:1"},
		{"trailing whitespace", first + " \t\n\r\n", "http://127.0.0.1:1"},
		{"leading whitespace", "\n  " + first, "http://127.0.0.1:1"},

		{"second document", first + "\n" + second, ""},
		{"second document on one line", first + second, ""},
		{"trailing garbage", first + "garbage", ""},
		{"trailing garbage after newline", first + "\ngarbage", ""},
		{"array wrapper", "[" + first + "]", ""},
		{"empty file", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), DiscoveryFileName)
			if err := os.WriteFile(path, []byte(tt.content), discoveryFileMode); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			discovery, err := ReadDiscovery(path)
			if tt.wantURL == "" {
				if err == nil {
					t.Fatalf("accepted %q as a discovery file", tt.content)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadDiscovery: %v", err)
			}
			if discovery.APIURL != tt.wantURL {
				t.Errorf("api_url = %q, want %q", discovery.APIURL, tt.wantURL)
			}
		})
	}
}

// TestDiscoveryBoundAppliesToBytesRead proves the cap is on the payload.
//
// The stat check is a courtesy: a file can grow between stat and read, and
// metadata is not what the parser consumes. The read itself is limited.
func TestDiscoveryBoundAppliesToBytesRead(t *testing.T) {
	build := func(t *testing.T, total int) string {
		t.Helper()
		const prefix = `{"version":"1","api_url":"http://127.0.0.1:1","pad":"`
		const suffix = `"}`
		padding := total - len(prefix) - len(suffix)
		if padding < 0 {
			t.Fatalf("total %d is below the envelope size", total)
		}
		content := prefix + strings.Repeat("x", padding) + suffix
		if len(content) != total {
			t.Fatalf("built %d bytes, want %d", len(content), total)
		}
		return content
	}

	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), DiscoveryFileName)
		if err := os.WriteFile(path, []byte(build(t, MaxDiscoveryFileBytes)),
			discoveryFileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if _, err := ReadDiscovery(path); err != nil {
			t.Fatalf("a file exactly at the limit was refused: %v", err)
		}
	})

	t.Run("one byte over is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), DiscoveryFileName)
		if err := os.WriteFile(path, []byte(build(t, MaxDiscoveryFileBytes+1)),
			discoveryFileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if _, err := ReadDiscovery(path); err == nil {
			t.Fatal("a file one byte over the limit was accepted")
		}
	})

	// That case alone would also pass without any length check: truncating a
	// padded object at the bound leaves unterminated JSON, so the refusal
	// could be a parse error wearing a size error's clothes. Whitespace
	// padding truncates into something that parses, so only comparing the
	// bytes actually read can reject it.
	t.Run("oversized whitespace padding is refused, not silently truncated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), DiscoveryFileName)
		content := `{"version":"1","api_url":"http://127.0.0.1:1"}` +
			strings.Repeat(" ", MaxDiscoveryFileBytes)
		if err := os.WriteFile(path, []byte(content), discoveryFileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := ReadDiscovery(path)
		if err == nil {
			t.Fatal("an oversized discovery file was truncated and accepted")
		}
		if !strings.Contains(err.Error(), "limit") {
			t.Errorf("error does not name the size bound: %v", err)
		}
	})
}

// TestReadDiscoveryEnforcesTheLocalRuntimeURLPolicy makes the validation part
// of the read, so every caller holds a URL that has already been proven local.
func TestReadDiscoveryEnforcesTheLocalRuntimeURLPolicy(t *testing.T) {
	tests := []struct {
		name   string
		apiURL string
		valid  bool
	}{
		{"loopback ipv4", "http://127.0.0.1:54321", true},
		{"loopback ipv4 range", "http://127.9.9.9:8080", true},
		{"loopback ipv6", "http://[::1]:54321", true},
		{"trailing slash", "http://127.0.0.1:54321/", true},

		{"remote host", "http://attacker.example:80", false},
		{"public address", "http://203.0.113.5:8080", false},
		{"unspecified address", "http://0.0.0.0:8080", false},
		{"hostname that resolves to loopback", "http://localhost:8080", false},
		{"https", "https://127.0.0.1:54321", false},
		{"no scheme", "127.0.0.1:54321", false},
		{"credentials", "http://user:pass@127.0.0.1:54321", false},
		{"path", "http://127.0.0.1:54321/v1", false},
		{"query", "http://127.0.0.1:54321/?redirect=x", false},
		{"fragment", "http://127.0.0.1:54321/#x", false},
		{"no port", "http://127.0.0.1", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), DiscoveryFileName)
			content, err := json.Marshal(Discovery{Version: DiscoveryVersion, APIURL: tt.apiURL})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if err := os.WriteFile(path, content, discoveryFileMode); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			_, err = ReadDiscovery(path)
			if tt.valid && err != nil {
				t.Fatalf("a valid local runtime URL was refused: %v", err)
			}
			if !tt.valid && err == nil {
				t.Fatalf("%q was accepted as a local runtime endpoint", tt.apiURL)
			}
		})
	}
}

// countingDialer replaces the liveness probe's dialer and records every
// address it was asked to contact.
func countingDialer(t *testing.T) *[]string {
	t.Helper()
	var (
		mu        sync.Mutex
		addresses []string
	)
	original := dialRuntime
	dialRuntime = func(network, address string, timeout time.Duration) (net.Conn, error) {
		mu.Lock()
		addresses = append(addresses, address)
		mu.Unlock()
		return original(network, address, timeout)
	}
	t.Cleanup(func() { dialRuntime = original })
	return &addresses
}

// TestStartDoesNotProbeUntrustedDiscoveryAddresses is the outbound-connection
// boundary.
//
// The liveness probe exists to answer "is a runtime already serving here?".
// Before the address is validated it answered a different question: "connect
// to whatever this file names." A checked-out repository shipping its own
// .trustvian/runtime.json would then make `make local` open an outbound TCP
// connection to a host of the repository's choosing — the same crossing the
// root client already refuses.
func TestStartDoesNotProbeUntrustedDiscoveryAddresses(t *testing.T) {
	untrusted := []string{
		`{"version":"1","api_url":"http://attacker.example:80"}`,
		`{"version":"1","api_url":"http://203.0.113.5:8080"}`,
		`{"version":"1","api_url":"https://127.0.0.1:54321"}`,
		`{"version":"1","api_url":"http://localhost:8080"}`,
		`{"version":"1","api_url":"http://user:pass@127.0.0.1:54321"}`,
		`{"version":"2","api_url":"http://attacker.example:80"}`,
		`{"version":"1","api_url":"http://127.0.0.1:1"}` + `{"version":"1","api_url":"http://attacker.example:80"}`,
		`not json at all`,
	}

	for _, content := range untrusted {
		t.Run(content, func(t *testing.T) {
			dialled := countingDialer(t)

			stateDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(stateDir, DiscoveryFileName),
				[]byte(content), discoveryFileMode); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			// Startup still succeeds: an untrusted file is simply replaced.
			rt := startRuntime(t, stateDir)
			if rt.APIURL() == "" {
				t.Fatal("the runtime did not bind")
			}
			if len(*dialled) != 0 {
				t.Fatalf("the runtime dialled %v for a discovery file it should not trust",
					*dialled)
			}
		})
	}
}

// TestStartProbesAValidLoopbackDiscovery is the other half: validation must
// not have disabled the probe it guards.
func TestStartProbesAValidLoopbackDiscovery(t *testing.T) {
	// A loopback port nothing is listening on, so the probe fails and startup
	// proceeds — what matters is that the dial was attempted.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	deadAddr := probe.Addr().String()
	probe.Close()

	dialled := countingDialer(t)

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, DiscoveryFileName),
		[]byte(`{"version":"1","api_url":"http://`+deadAddr+`"}`),
		discoveryFileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	startRuntime(t, stateDir)

	if len(*dialled) != 1 || (*dialled)[0] != deadAddr {
		t.Fatalf("probe dialled %v, want exactly [%s]", *dialled, deadAddr)
	}
}

// ---------------------------------------------------------------------
// WebUI coexistence (task 063)
// ---------------------------------------------------------------------

// fetch issues one GET against the running runtime and returns the response.
func fetchPath(t *testing.T, rt *Runtime, method, path string) (int, http.Header, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, rt.APIURL()+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response.StatusCode, response.Header, string(body)
}

// TestWebUIAndAPIShareOneListener is the same-origin composition.
//
// One port for an unauthenticated service rather than two, and one origin for
// the page and the API it calls — which is what makes CORS unnecessary rather
// than merely unconfigured.
func TestWebUIAndAPIShareOneListener(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	// The shell.
	status, header, body := fetchPath(t, rt, "GET", "/")
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", status)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("GET / Content-Type = %q, want HTML", got)
	}
	if !strings.Contains(body, "<!doctype html>") {
		t.Error("GET / did not return the WebUI shell")
	}

	// Its assets, enumerated from what the handler actually serves rather than
	// from a filename list that could drift.
	for _, path := range webui.AssetPaths() {
		status, _, _ := fetchPath(t, rt, "GET", path)
		if status != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, status)
		}
	}

	// The API, unchanged.
	status, header, _ = fetchPath(t, rt, "GET", "/v1/projects/absent")
	if status != http.StatusNotFound {
		t.Errorf("GET /v1/projects/absent status = %d, want 404", status)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("an API 404 has Content-Type %q; the API still answers as the API", got)
	}

	// And exactly one listener is bound: the WebUI added no second port.
	if rt.WebURL() != rt.APIURL()+"/" {
		t.Errorf("WebURL = %q, APIURL = %q; they must be the same origin",
			rt.WebURL(), rt.APIURL())
	}
}

// TestUnknownAPIRoutesNeverReturnTheShell is the invariant that makes the
// composition safe.
//
// This is the mutation the spec names second. A mux that let /v1 fall through
// to "/" would answer every mistyped API path with 200 and a page full of
// markup — so a client's typo would look like success, and a JSON decoder would
// fail somewhere far from the cause.
//
// The API's own 404 is plain text (`404 page not found`) rather than the error
// envelope. That is pre-existing behaviour, verified rather than assumed, and
// task 063 preserves it instead of "improving" it into HTML or changing it into
// an envelope — either would be an API change this task has no mandate for.
func TestUnknownAPIRoutesNeverReturnTheShell(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	paths := []string{
		"/v1",
		"/v1/",
		"/v1/does-not-exist",
		"/v1/projects/x/nope",
		"/v1/evaluation-runs",
		"/v1/realtime/extra",
		// Adjacent to the prefix but not under it. These must reach the WebUI
		// and 404 there, not be captured by the API's subtree — a mux rule
		// registered as "/v1" without the trailing-slash companion would get
		// this wrong in one direction or the other.
		"/v1extra",
		"/v10/projects",
	}

	// Deliberately not tested here: "/v1/../". That path is equivalent to "/"
	// by URL semantics and is normalized before any routing happens, so
	// serving the shell for it is correct. Asserting otherwise would be
	// testing net/http's path cleaning, not this composition.

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			status, header, body := fetchPath(t, rt, "GET", path)

			if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "<html") {
				t.Fatalf("%s returned the WebUI shell (status %d)", path, status)
			}
			if status == http.StatusOK {
				t.Fatalf("%s returned 200; an unknown API route must not succeed", path)
			}
			if got := header.Get("Content-Type"); strings.HasPrefix(got, "text/html") {
				t.Errorf("%s answered with Content-Type %q", path, got)
			}
		})
	}
}

// TestAPIResponsesAreUnchangedByTheWebUI checks the real routes still behave.
//
// A browser UI landing must not move a status code, a body or a header that the
// CLI and TUI depend on.
func TestAPIResponsesAreUnchangedByTheWebUI(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	// A real create over the composed handler.
	request, err := http.NewRequestWithContext(t.Context(), "POST",
		rt.APIURL()+"/v1/projects",
		strings.NewReader(`{"id":"proj-web","name":"Checkout"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/projects: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/projects status = %d, want 201", response.StatusCode)
	}

	// And it reads back through the API, not through the UI.
	status, header, body := fetchPath(t, rt, "GET", "/v1/projects/proj-web")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/projects/proj-web status = %d, want 200", status)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", got)
	}
	var decoded struct {
		Version string `json:"version"`
		ID      string `json:"id"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode: %v (body %.120q)", err, body)
	}
	if decoded.Version != "1" || decoded.ID != "proj-web" || decoded.Name != "Checkout" {
		t.Errorf("unexpected project response: %+v", decoded)
	}
}

// TestWebUIResponsesCarryNoCORSHeader is the header that would change the
// security model.
//
// Same-origin means none is needed. Sending one is the single change that would
// let any page a developer visits reach this unauthenticated control plane.
func TestWebUIResponsesCarryNoCORSHeader(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	for _, path := range []string{"/", "/assets/app.js", "/nope", "/v1/projects/absent"} {
		_, header, _ := fetchPath(t, rt, "GET", path)
		for name := range header {
			if strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-") {
				t.Errorf("%s sent %s", path, name)
			}
		}
	}
}

// TestWebUICarriesSecurityHeadersThroughTheRuntime proves the policy survives
// composition rather than only existing in the handler's own test.
func TestWebUICarriesSecurityHeadersThroughTheRuntime(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	_, header, _ := fetchPath(t, rt, "GET", "/")
	policy := header.Get("Content-Security-Policy")
	if policy == "" {
		t.Fatal("no Content-Security-Policy through the composed runtime")
	}
	if policy != webui.ContentSecurityPolicy() {
		t.Errorf("policy through the runtime = %q, handler's = %q",
			policy, webui.ContentSecurityPolicy())
	}
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("policy contains %q", forbidden)
		}
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q; a cached shell could run against a restarted "+
			"runtime on a different port", got)
	}
}

// TestDiscoverySchemaIsUnchangedByTheWebUI keeps the file at two fields.
//
// The API URL and the WebUI origin are the same endpoint, so there is nothing
// for a third field to say — and adding one would be a schema change consumers
// would have to tolerate for no information.
func TestDiscoverySchemaIsUnchangedByTheWebUI(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	payload, err := os.ReadFile(rt.DiscoveryPath())
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if len(raw) != 2 {
		t.Errorf("discovery has %d fields (%v); task 063 adds none", len(raw), raw)
	}
	for _, field := range []string{"version", "api_url"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("discovery is missing %q", field)
		}
	}
	for _, unwanted := range []string{"web_url", "ui_url", "webui", "token"} {
		if _, ok := raw[unwanted]; ok {
			t.Errorf("discovery gained a %q field", unwanted)
		}
	}
}

// TestServerBoundsAreUnchangedByTheWebUI keeps SSE long-lived.
//
// WriteTimeout in particular: it is a total response deadline, and a browser
// EventSource is exactly as long-lived as the TUI's stream.
func TestServerBoundsAreUnchangedByTheWebUI(t *testing.T) {
	rt := startRuntime(t, t.TempDir())

	if rt.server.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v; a total write deadline would kill healthy SSE",
			rt.server.WriteTimeout)
	}
	if rt.server.ReadHeaderTimeout != readHeaderTimeout ||
		rt.server.ReadTimeout != readTimeout ||
		rt.server.IdleTimeout != idleTimeout ||
		rt.server.MaxHeaderBytes != maxHeaderBytes {
		t.Error("a task 062 server bound changed")
	}
}
