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
	"strings"
	"sync"
	"time"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
	"trustvian-platform/webui"
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

// Backend names for Options.Backend.
//
// Mirrors config.StorageConfig's `type:` convention for the engine's store,
// including its rule that an unrecognized value is a validation error rather
// than something to fall back from.
const (
	// BackendSQLite is the local default. Selected by an empty Backend, so no
	// caller has to name it and `make local` needs no configuration at all.
	BackendSQLite = "sqlite"

	// BackendPostgres is the shared/deployed backend. Requires explicit
	// selection and a DSN.
	BackendPostgres = "postgres"
)

// Options configure one runtime.
type Options struct {
	// StateDir defaults to DefaultStateDir.
	StateDir string

	// ListenAddress defaults to DefaultListenAddress. Loopback only.
	ListenAddress string

	// Backend selects platform persistence. Empty means BackendSQLite.
	//
	// Empty rather than a named default on purpose: every existing caller, and
	// `make local`, keeps working without mentioning a backend. PostgreSQL is
	// reached only by asking for it.
	//
	// An unrecognized value is refused. It never falls back to SQLite — a
	// deployment that asked for a shared database and silently got a local file
	// would look healthy while losing every other process's state.
	Backend string

	// Postgres is required when Backend is BackendPostgres, ignored otherwise.
	Postgres *PostgresOptions
}

// PostgresOptions configure the shared backend.
//
// Deliberately the same three fields as config.PostgresStorageConfig: pgx
// exposes a large tuning surface, and each knob here has to justify itself as
// something an operator cannot set another way.
type PostgresOptions struct {
	// DSN is the connection string. Required.
	//
	// **A secret.** It normally embeds a password, so nothing in this package
	// logs it, returns it inside an error, prints it, or writes it into
	// runtime.json — including when it cannot be parsed, which is the one case
	// the driver's own message would otherwise echo verbatim.
	DSN string

	// MaxConnections caps the pool. Zero means the driver default.
	MaxConnections int32

	// ConnectTimeout bounds the startup connectivity check. Zero means the
	// store's default. This is what keeps fail-fast startup fast: unbounded, an
	// unreachable host would wait out the OS-level TCP timeout.
	ConnectTimeout time.Duration
}

// Runtime is a started local control plane.
type Runtime struct {
	apiURL   string
	stateDir string
	dbPath   string

	// store is the composite interface, not a concrete backend: composition is
	// the only layer that knows which one is underneath. Services still take
	// the narrow interfaces individually.
	store   platform.Store
	backend string

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
	// Configuration is validated before any directory, socket or connection
	// exists. A runtime that discovered its backend was misconfigured after
	// binding would have advertised an endpoint it cannot serve.
	backend, err := resolveBackend(options)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, stateDirMode); err != nil {
		return nil, fmt.Errorf("creating state directory: %w", err)
	}

	discoveryPath := filepath.Join(stateDir, DiscoveryFileName)
	if err := refuseIfRuntimeIsLive(discoveryPath); err != nil {
		return nil, err
	}

	// The SQLite path is still computed for either backend, because
	// DatabasePath() reports it and PostgreSQL mode simply never creates a file
	// there.
	dbPath := filepath.Join(stateDir, DatabaseFileName)

	store, err := openBackend(ctx, backend, dbPath, options.Postgres)
	if err != nil {
		return nil, err
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

	apiHandler, err := httpapi.NewHandler(plane, httpapi.WithRealtimeSubscriber(bus))
	if err != nil {
		bus.Close()
		store.Close()
		return nil, fmt.Errorf("constructing HTTP handler: %w", err)
	}

	handler, err := composeHandler(apiHandler)
	if err != nil {
		bus.Close()
		store.Close()
		return nil, fmt.Errorf("composing HTTP handler: %w", err)
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
		backend:   backend,
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

// WebURL is where a browser reaches the WebUI.
//
// Derived from the API URL with a trailing slash rather than stored, because
// they are the same origin by construction: task 063's UI shares this
// listener. If these could ever differ, the discovery file would need a second
// field — and it deliberately has none, because there is nothing to say.
func (r *Runtime) WebURL() string { return r.apiURL + "/" }

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

// ErrBackendConfiguration reports a persistence backend that cannot be used as
// configured: an unknown name, or PostgreSQL without a DSN.
//
// Separate from ErrListenAddress because the two are different operator
// mistakes, and trustvian-local maps them to different exit codes.
var ErrBackendConfiguration = errors.New("localruntime: persistence backend configuration is invalid")

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

// composeHandler routes one listener between the API and the WebUI.
//
// Task 063 adds a browser client, and it shares this listener rather than
// getting its own. One port for an unauthenticated service instead of two, the
// same origin for the page and the API it calls — which is what makes CORS
// unnecessary rather than merely unconfigured — and one discovery field that
// already locates both.
//
// The order matters and is the point of doing this here rather than inside
// either handler. "/v1" and "/v1/" are registered to the API, so an unknown
// API path keeps the API's own 404 instead of falling through to "/" and
// answering 200 with HTML. Both patterns are needed: Go's ServeMux treats the
// exact path and the subtree as different rules, and only the second matches
// descendants.
//
// The WebUI handler is constructed with no arguments and is handed nothing —
// see ADR 0036. This function is the only place the two adapters meet, and
// they meet as http.Handlers.
func composeHandler(apiHandler http.Handler) (http.Handler, error) {
	uiHandler, err := webui.NewHandler()
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// The versioned machine contract, first and unchanged.
	mux.Handle("/v1", apiHandler)
	mux.Handle("/v1/", apiHandler)
	// Everything else is the browser application, which serves its own fixed
	// set of paths and 404s the rest.
	mux.Handle("/", uiHandler)
	return mux, nil
}

// dialRuntime is the liveness probe's dialer.
//
// A package variable so tests can observe whether a dial was attempted at all.
// Unexported and never part of the API: the guarantee being tested is "this
// address was never contacted", which nothing else can demonstrate.
var dialRuntime = net.DialTimeout

// refuseIfRuntimeIsLive avoids replacing a runtime that is still serving.
//
// A bounded TCP dial, not an API call: asking the application whether it is
// alive would be a mutation-shaped question. A malformed, stale or unreachable
// file is replaced freely. No locking — task 069 owns multi-node concerns, and
// this is single-node development.
//
// The address is validated before anything is dialled. ReadDiscovery now
// enforces the full local-runtime URL policy, so a file cannot name a host of
// its choosing — otherwise a checked-out project containing a discovery file
// would make `make local` open an outbound connection to whatever it named,
// which is exactly the boundary the root client already refuses to cross.
func refuseIfRuntimeIsLive(discoveryPath string) error {
	discovery, err := ReadDiscovery(discoveryPath)
	if err != nil {
		// Unreadable, unsupported, or not a loopback runtime URL: nothing
		// worth probing, and the file may be replaced.
		return nil
	}
	parsed, err := url.Parse(discovery.APIURL)
	if err != nil || parsed.Host == "" {
		return nil
	}

	conn, err := dialRuntime("tcp", parsed.Host, livenessProbeTimeout)
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
	file, err := os.Open(path)
	if err != nil {
		return Discovery{}, err
	}
	defer file.Close()

	// The bound is on the bytes actually read, and only there. A stat-based
	// size check ahead of it could only agree with this one, except in the
	// window where the file grows between the two calls — where it is the
	// stat that is wrong. One guard, at the point the bytes are consumed.
	//
	// One byte past the limit is read deliberately, so "exactly at the limit"
	// and "over it" stay distinguishable instead of both arriving truncated.
	payload, err := io.ReadAll(io.LimitReader(file, MaxDiscoveryFileBytes+1))
	if err != nil {
		return Discovery{}, err
	}
	if len(payload) > MaxDiscoveryFileBytes {
		return Discovery{}, fmt.Errorf(
			"runtime discovery file is over the %d byte limit", MaxDiscoveryFileBytes)
	}

	// Unmarshal, not Decode: a decoder reads one value and stops, so a second
	// document or trailing junk would be accepted on the strength of the first
	// object. Unmarshal requires the payload to be exactly one JSON value,
	// while tolerating unknown fields inside it and trailing whitespace after.
	var discovery Discovery
	if err := json.Unmarshal(payload, &discovery); err != nil {
		return Discovery{}, errors.New("runtime discovery file is not exactly one JSON document")
	}
	if discovery.Version != DiscoveryVersion {
		return Discovery{}, fmt.Errorf(
			"runtime discovery version %q is not supported", discovery.Version)
	}
	// The complete contract, not just the envelope: every caller — the
	// liveness probe, ownership checks on shutdown, tests — then holds a URL
	// that has already been proven to be a local runtime's.
	if err := validateDiscoveryAPIURL(discovery.APIURL); err != nil {
		return Discovery{}, err
	}
	return discovery, nil
}

// validateDiscoveryAPIURL enforces the local-runtime URL policy.
//
// One validator for the whole module, matching the root client's rule
// semantically. The two cannot share code across the module boundary, and
// duplicating the contract in two places beats making an operational file into
// a public package — but within each module there is exactly one.
//
// A project directory can contain a discovery file, so this is what stops a
// checked-out repository turning `make local` into an outbound connection to
// an address of its choosing.
func validateDiscoveryAPIURL(raw string) error {
	if raw == "" {
		return errors.New("runtime discovery file carries no api_url")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("runtime discovery api_url is not a valid URL")
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf(
			"runtime discovery api_url scheme %q is not supported; a local runtime is http",
			parsed.Scheme)
	}
	if parsed.User != nil {
		return errors.New("runtime discovery api_url must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("runtime discovery api_url must not contain a query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("runtime discovery api_url must not contain a path")
	}

	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return errors.New("runtime discovery api_url must include a host and port")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("runtime discovery api_url has an invalid port")
	}
	// Numeric only. A hostname that resolves to loopback today is a name
	// someone else controls, and resolution is not proof.
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf(
			"runtime discovery api_url host %q is not a numeric loopback address", host)
	}
	return nil
}

// ---------------------------------------------------------------------
// Backend composition
// ---------------------------------------------------------------------

// resolveBackend validates the requested backend and returns its name.
//
// Fails closed on anything unrecognized. The alternative — treating an unknown
// name as "use the default" — is how a typo in a deployment's configuration
// becomes a control plane quietly running on a local file while every other
// process uses the shared database, each of them looking healthy.
//
// No part of the error mentions the DSN. There is nothing worth quoting from it
// and the habit of quoting it is what leaks one later.
func resolveBackend(options Options) (string, error) {
	switch options.Backend {
	case "", BackendSQLite:
		// Empty is SQLite, which is what makes `make local` need no
		// configuration. A Postgres block supplied alongside it is a
		// contradiction rather than something to silently ignore.
		if options.Postgres != nil && options.Backend == "" {
			return "", fmt.Errorf(
				"%w: PostgreSQL options were supplied without selecting the %q backend",
				ErrBackendConfiguration, BackendPostgres)
		}
		return BackendSQLite, nil

	case BackendPostgres:
		if options.Postgres == nil {
			return "", fmt.Errorf("%w: the %q backend requires connection options",
				ErrBackendConfiguration, BackendPostgres)
		}
		if strings.TrimSpace(options.Postgres.DSN) == "" {
			return "", fmt.Errorf("%w: the %q backend requires a database DSN",
				ErrBackendConfiguration, BackendPostgres)
		}
		return BackendPostgres, nil

	default:
		// The value is named because it came from configuration and is not a
		// secret; a DSN never would be.
		return "", fmt.Errorf("%w: unknown backend %q; use %q or %q",
			ErrBackendConfiguration, options.Backend, BackendSQLite, BackendPostgres)
	}
}

// openBackend opens the selected store.
//
// The only place in the repository that chooses between backends. Everything
// above it — the control plane, the HTTP adapter, the WebUI, the CLI and the
// TUI — receives a store and cannot tell which one it got.
func openBackend(
	ctx context.Context, backend, dbPath string, postgres *PostgresOptions,
) (platform.Store, error) {
	switch backend {
	case BackendSQLite:
		store, err := platform.OpenSQLiteStore(ctx, dbPath)
		if err != nil {
			return nil, fmt.Errorf("opening platform database: %w", err)
		}
		return store, nil

	case BackendPostgres:
		// OpenPostgresStore constructs the pool, proves connectivity with a
		// Ping, and creates, verifies or migrates the schema before returning.
		// So a store in hand means the database is usable, which is what lets
		// the listener below bind only after this succeeds.
		store, err := platform.OpenPostgresStore(ctx, platform.PostgresConfig{
			DSN:            postgres.DSN,
			MaxConnections: postgres.MaxConnections,
			ConnectTimeout: postgres.ConnectTimeout,
		})
		if err != nil {
			// Wrapped without re-introducing the DSN. The store's own errors are
			// already redacted; adding configuration back here would undo that.
			return nil, fmt.Errorf("opening platform database: %w", err)
		}
		return store, nil

	default:
		// Unreachable: resolveBackend ran first. Stated rather than panicking,
		// because a future caller could reorder them.
		return nil, fmt.Errorf("%w: unknown backend %q", ErrBackendConfiguration, backend)
	}
}

// Backend reports which persistence backend this runtime opened.
//
// For diagnostics and tests. Never carries a DSN, a host or a database name —
// the name alone is what an operator needs to confirm, and anything more would
// be a credential surface.
func (r *Runtime) Backend() string { return r.backend }

// StateSummary describes where state lives, safely.
//
// The SQLite path for a local runtime, and the backend name alone for
// PostgreSQL. Deliberately not the DSN, the host or the database: this string is
// printed at startup, and a startup line is exactly where a connection string
// would end up in a terminal, a log file and a screenshot.
func (r *Runtime) StateSummary() string {
	if r.backend == BackendPostgres {
		return "PostgreSQL (shared backend)"
	}
	return r.dbPath
}
