// Package localruntime composes the local control plane into a running
// server.
//
// Lifecycle only. It wires exactly one store, bus, control plane, handler and
// listener, and owns the order they start and stop in. It makes no decisions:
// ingest, diff, scorecard, gate and realtime publication all stay where they
// already are, because a composition root that starts deciding things is how a
// second service is born.
//
// It lives in the platform module rather than beside the CLI because the root
// module must not import trustvian-platform — see
// docs/adr/0035-local-runtime-composes-platform-without-reversing-modules.md.
// The CLI and TUI find this server through a discovery file, not a Go import.
//
// Signals are not handled here. This package takes a context and owns
// resources; the executable owns the process.
package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

const (
	// DefaultStateDir is project-local on purpose.
	//
	// Visible to ls, removable with rm -rf, and isolated per working
	// directory without anyone configuring that. Hidden global state is how a
	// developer ends up debugging a run belonging to a different project.
	DefaultStateDir = ".trustvian"

	// DatabaseFileName is the control plane's durable state. Not the engine's
	// baseline store: different lifecycle, different owner, different
	// correctness properties.
	DatabaseFileName = "platform.db"

	// DiscoveryFileName advertises the bound endpoint to local clients.
	DiscoveryFileName = "runtime.json"

	// DiscoveryVersion is the schema version clients validate.
	DiscoveryVersion = "1"

	// DefaultListenAddress is loopback with an OS-assigned port.
	//
	// Ephemeral because freezing a number for the sake of examples would turn
	// an arbitrary choice into a compatibility surface — and would collide
	// with whatever the developer is already running.
	DefaultListenAddress = "127.0.0.1:0"

	// MaxDiscoveryFileBytes bounds what a client will read back. Two fields
	// need far less, and the file is not necessarily trustworthy.
	MaxDiscoveryFileBytes = 4 << 10

	stateDirMode      = 0o700
	discoveryFileMode = 0o600

	// Server bounds. WriteTimeout is deliberately absent — see Options.
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 64 << 10

	// shutdownTimeout bounds graceful shutdown before connections are closed
	// outright.
	shutdownTimeout = 5 * time.Second

	// livenessProbeTimeout bounds the check for an existing runtime in the
	// same state directory. A dial, not an API call: asking the application
	// whether it is alive would be a mutation-shaped question.
	livenessProbeTimeout = 500 * time.Millisecond
)

// Discovery is the published endpoint file.
//
// Two fields. No token, no secret, no identifiers, no database path — it says
// only that a local runtime announced this endpoint, and finding it proves
// nothing about who may use it. What protects the endpoint is loopback.
type Discovery struct {
	Version string `json:"version"`
	APIURL  string `json:"api_url"`
}

// Options configure one runtime.
type Options struct {
	// StateDir defaults to DefaultStateDir.
	StateDir string

	// ListenAddress defaults to DefaultListenAddress. Loopback only.
	ListenAddress string
}

// Runtime is a started local control plane.
type Runtime struct {
	apiURL   string
	stateDir string
	dbPath   string

	store    *platform.SQLiteStore
	bus      *platform.InMemoryRealtimeBus
	listener net.Listener
	server   *http.Server

	// serveDone closes when Serve returns; serveErr is written before it
	// closes and read only after.
	//
	// A broadcast rather than a channel carrying the value, because both Wait
	// and Stop observe it. An earlier version sent the error on a buffered
	// channel that each of them received from — so Wait consumed it and Stop
	// then blocked forever, deadlocking the ordinary Ctrl-C path.
	serveDone chan struct{}
	serveErr  error

	// stopOnce makes shutdown idempotent: the executable calls Stop on the
	// signal path, and a caller may reasonably call it again.
	stopOnce sync.Once
	stopErr  error
}

// Start brings the whole local platform up, fail-closed.
//
// The order is load-bearing: discovery is published only after the listener is
// bound, so a client that finds the file finds an endpoint that exists. If any
// step fails, everything opened before it is closed.
func Start(ctx context.Context, options Options) (*Runtime, error) {
	stateDir := options.StateDir
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	listenAddress := options.ListenAddress
	if listenAddress == "" {
		listenAddress = DefaultListenAddress
	}

	if err := validateLoopbackAddress(listenAddress); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, stateDirMode); err != nil {
		return nil, fmt.Errorf("creating state directory: %w", err)
	}

	discoveryPath := filepath.Join(stateDir, DiscoveryFileName)
	if err := refuseIfRuntimeIsLive(discoveryPath); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(stateDir, DatabaseFileName)
	store, err := platform.OpenSQLiteStore(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening platform database: %w", err)
	}

	bus := platform.NewInMemoryRealtimeBus()

	// One bus, published to by the control plane and subscribed to by the
	// transport. Neither side gains the other's authority.
	plane, err := platform.NewControlPlane(store, store, store,
		platform.WithRealtimePublisher(bus))
	if err != nil {
		bus.Close()
		store.Close()
		return nil, fmt.Errorf("constructing control plane: %w", err)
	}

	handler, err := httpapi.NewHandler(plane, httpapi.WithRealtimeSubscriber(bus))
	if err != nil {
		bus.Close()
		store.Close()
		return nil, fmt.Errorf("constructing HTTP handler: %w", err)
	}

	// Listen before serving so the chosen port is known, and known before
	// anything is advertised.
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		bus.Close()
		store.Close()
		return nil, fmt.Errorf("binding %s: %w", listenAddress, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// WriteTimeout is deliberately zero. It is a *total* response
		// deadline, and task 059's SSE streams are long-lived by design with
		// their own finite per-write deadline via
		// ResponseController.SetWriteDeadline. A server-wide write timeout
		// would eventually kill a healthy dashboard for being healthy.
	}

	runtime := &Runtime{
		apiURL:    "http://" + listener.Addr().String(),
		stateDir:  stateDir,
		dbPath:    dbPath,
		store:     store,
		bus:       bus,
		listener:  listener,
		server:    server,
		serveDone: make(chan struct{}),
	}

	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		runtime.serveErr = err
		close(runtime.serveDone)
	}()

	if err := writeDiscovery(discoveryPath, runtime.apiURL); err != nil {
		// Half-discoverable is not a state worth running in.
		runtime.Stop(context.Background())
		return nil, fmt.Errorf("publishing runtime discovery: %w", err)
	}
	return runtime, nil
}

// APIURL is the bound endpoint, with the port the OS actually chose.
func (r *Runtime) APIURL() string { return r.apiURL }

// DatabasePath is the platform store's location.
func (r *Runtime) DatabasePath() string { return r.dbPath }

// DiscoveryPath is where the endpoint was advertised.
func (r *Runtime) DiscoveryPath() string {
	return filepath.Join(r.stateDir, DiscoveryFileName)
}

// Wait blocks until the server stops. Safe to call alongside Stop.
func (r *Runtime) Wait() error {
	<-r.serveDone
	return r.serveErr
}

// Stop shuts everything down in an order that lets it finish.
//
// The bus closes first so in-flight SSE handlers return instead of holding
// graceful shutdown open for the length of their own streams — a dashboard
// connected at Ctrl-C should not delay exit.
func (r *Runtime) Stop(ctx context.Context) error {
	r.stopOnce.Do(func() {
		// The bus first, so in-flight SSE handlers return instead of holding
		// graceful shutdown open for the length of their own streams.
		r.bus.Close()

		shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()

		if err := r.server.Shutdown(shutdownCtx); err != nil {
			// Graceful shutdown ran out of time; stop waiting for connections
			// that are not finishing.
			r.stopErr = r.server.Close()
		}
		<-r.serveDone

		if err := r.store.Close(); err != nil && r.stopErr == nil {
			r.stopErr = err
		}
		removeOwnedDiscovery(r.DiscoveryPath(), r.apiURL)
	})
	return r.stopErr
}

// ---------------------------------------------------------------------
// Listen address
// ---------------------------------------------------------------------

// ErrListenAddress marks a refused --listen value.
//
// A sentinel rather than message matching: the caller classifies this as a
// usage error, and that classification should not depend on wording.
var ErrListenAddress = errors.New("invalid listen address")

// validateLoopbackAddress refuses anything that is not numeric loopback.
//
// This runtime is unauthenticated, so loopback is not a convenience default —
// it is the entire security boundary. Hostnames are refused even when they
// would resolve to loopback: resolution is not proof, and what "localhost"
// means is the resolver's opinion.
func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q must be host:port: %v", ErrListenAddress, address, err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("%w: %q has an invalid port", ErrListenAddress, address)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf(
			"%w: %q must be a numeric loopback address such as 127.0.0.1 or [::1]; "+
				"this runtime is unauthenticated and only binds loopback", ErrListenAddress, address)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf(
			"%w: %q is not loopback; this runtime is unauthenticated "+
				"and must not be reachable from a network", ErrListenAddress, address)
	}
	return nil
}

// ---------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------

// writeDiscovery publishes the endpoint atomically.
//
// Temp file, complete bounded JSON, mode, close, rename — so no reader can
// observe a partial file, and no client can act on half an endpoint.
func writeDiscovery(path, apiURL string) error {
	encoded, err := json.Marshal(Discovery{Version: DiscoveryVersion, APIURL: apiURL})
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	temp, err := os.CreateTemp(filepath.Dir(path), DiscoveryFileName+".*")
	if err != nil {
		return err
	}
	tempName := temp.Name()

	if err := temp.Chmod(discoveryFileMode); err != nil {
		temp.Close()
		os.Remove(tempName)
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

// removeOwnedDiscovery deletes the file only if it still describes this
// runtime.
//
// A process shutting down late must not delete the endpoint a newer runtime
// has already published — that would leave a working server undiscoverable,
// which is worse than leaving a stale file behind.
func removeOwnedDiscovery(path, apiURL string) {
	discovery, err := ReadDiscovery(path)
	if err != nil {
		// Unreadable or already gone: not ours to clean up.
		return
	}
	if discovery.APIURL != apiURL {
		return
	}
	os.Remove(path)
}

// refuseIfRuntimeIsLive avoids replacing a runtime that is still serving.
//
// A bounded TCP dial, not an API call: asking the application whether it is
// alive would be a mutation-shaped question. A malformed, stale or unreachable
// file is replaced freely. No locking — task 069 owns multi-node concerns, and
// this is single-node development.
func refuseIfRuntimeIsLive(discoveryPath string) error {
	discovery, err := ReadDiscovery(discoveryPath)
	if err != nil {
		return nil
	}
	parsed, err := url.Parse(discovery.APIURL)
	if err != nil || parsed.Host == "" {
		return nil
	}

	conn, err := net.DialTimeout("tcp", parsed.Host, livenessProbeTimeout)
	if err != nil {
		return nil
	}
	conn.Close()
	return fmt.Errorf(
		"a local runtime already appears to be serving %s from this state directory; "+
			"stop it first, or use a different --state-dir", discovery.APIURL)
}

// ReadDiscovery reads and validates a discovery file under a size bound.
//
// Shared with the root-module clients' resolver by contract rather than by
// import: they cannot import this package, so both sides implement the same
// bounded, version-checked read. Unknown fields inside version 1 are tolerated
// so the file can grow; an unknown version fails closed.
func ReadDiscovery(path string) (Discovery, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Discovery{}, err
	}
	if info.Size() > MaxDiscoveryFileBytes {
		return Discovery{}, fmt.Errorf(
			"runtime discovery file is %d bytes, over the %d byte limit",
			info.Size(), MaxDiscoveryFileBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return Discovery{}, err
	}
	defer file.Close()

	// Bounded again while reading: the stat above is a check, not a guarantee,
	// since the file can change between the two.
	var discovery Discovery
	decoder := json.NewDecoder(io.LimitReader(file, MaxDiscoveryFileBytes+1))
	if err := decoder.Decode(&discovery); err != nil {
		return Discovery{}, fmt.Errorf("runtime discovery file is not valid JSON")
	}
	if discovery.Version != DiscoveryVersion {
		return Discovery{}, fmt.Errorf(
			"runtime discovery version %q is not supported", discovery.Version)
	}
	if discovery.APIURL == "" {
		return Discovery{}, errors.New("runtime discovery file carries no api_url")
	}
	return discovery, nil
}
