package localruntime

// Backend composition at the runtime's own boundary.
//
// Task 064 adds a second persistence backend, and this is the only layer that
// knows which one is running. Everything the tests below assert is about that
// choice being made correctly, refused loudly, and invisible above.
//
// The PostgreSQL cases are gated on TRUSTVIAN_TEST_POSTGRES_DSN, so
// `go test ./...` still needs no database. The SQLite cases — which are the ones
// that protect `make local` — always run.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const runtimePostgresDSNEnv = "TRUSTVIAN_TEST_POSTGRES_DSN"

// leakSentinel is distinctive enough that finding it anywhere is proof.
const leakSentinel = "TRUSTVIAN_SECRET_MUST_NOT_LEAK"

// TestDefaultBackendIsSQLite is the zero-configuration guarantee.
//
// `make local` passes no backend, and must get SQLite with a file under the
// state directory. This is the property Task 064 is least allowed to break.
func TestDefaultBackendIsSQLite(t *testing.T) {
	stateDir := t.TempDir()
	rt := startRuntime(t, stateDir)

	if rt.Backend() != BackendSQLite {
		t.Errorf("Backend() = %q, want %q with no configuration", rt.Backend(), BackendSQLite)
	}
	if _, err := os.Stat(filepath.Join(stateDir, DatabaseFileName)); err != nil {
		t.Errorf("the default backend did not create %s: %v", DatabaseFileName, err)
	}
	// StateSummary names the file, which is what trustvian-local prints.
	if !strings.Contains(rt.StateSummary(), DatabaseFileName) {
		t.Errorf("StateSummary() = %q, want the database path", rt.StateSummary())
	}
}

// TestExplicitSQLiteBackendIsAccepted covers naming the default.
func TestExplicitSQLiteBackendIsAccepted(t *testing.T) {
	rt, err := Start(t.Context(), Options{
		StateDir: t.TempDir(),
		Backend:  BackendSQLite,
	})
	if err != nil {
		t.Fatalf("Start(sqlite) error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	if rt.Backend() != BackendSQLite {
		t.Errorf("Backend() = %q, want %q", rt.Backend(), BackendSQLite)
	}
}

// TestUnknownBackendFailsClosed is the rule that matters most after the default.
//
// A deployment that asked for a shared database and silently got a local file
// would look healthy while losing every other process's state. So an
// unrecognized name is refused, and never falls back.
func TestUnknownBackendFailsClosed(t *testing.T) {
	for _, backend := range []string{"mysql", "Postgres", "POSTGRES", "sqlite3", "postgresql", " postgres"} {
		t.Run(backend, func(t *testing.T) {
			stateDir := t.TempDir()
			rt, err := Start(t.Context(), Options{StateDir: stateDir, Backend: backend})
			if err == nil {
				_ = rt.Stop(context.Background())
				t.Fatalf("Start(%q) succeeded; an unknown backend must fail", backend)
			}
			if !errors.Is(err, ErrBackendConfiguration) {
				t.Errorf("error = %v, want ErrBackendConfiguration", err)
			}
			// It must not have fallen back and created a SQLite file.
			if _, statErr := os.Stat(filepath.Join(stateDir, DatabaseFileName)); statErr == nil {
				t.Error("a refused backend still created a SQLite database; nothing may " +
					"fall back from an explicit selection")
			}
			// Nor advertised an endpoint.
			assertNoUsableDiscovery(t, stateDir)
		})
	}
}

// TestPostgresBackendWithoutDSNFailsBeforeTheListener covers incomplete
// configuration.
func TestPostgresBackendWithoutDSNFailsBeforeTheListener(t *testing.T) {
	cases := []struct {
		name    string
		options *PostgresOptions
	}{
		{"no options block", nil},
		{"empty DSN", &PostgresOptions{DSN: ""}},
		{"whitespace DSN", &PostgresOptions{DSN: "   "}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := t.TempDir()
			rt, err := Start(t.Context(), Options{
				StateDir: stateDir,
				Backend:  BackendPostgres,
				Postgres: tt.options,
			})
			if err == nil {
				_ = rt.Stop(context.Background())
				t.Fatal("Start succeeded without a DSN")
			}
			if !errors.Is(err, ErrBackendConfiguration) {
				t.Errorf("error = %v, want ErrBackendConfiguration", err)
			}
			assertNoUsableDiscovery(t, stateDir)
			if _, statErr := os.Stat(filepath.Join(stateDir, DatabaseFileName)); statErr == nil {
				t.Error("a refused PostgreSQL selection created a SQLite database")
			}
		})
	}
}

// TestPostgresOptionsWithoutSelectionAreRefused catches the other half of the
// mistake: supplying a DSN and forgetting to select the backend.
//
// Silently ignoring it would start SQLite while an operator believed they had
// configured a shared database — the same failure as a fallback, arrived at from
// the opposite direction.
func TestPostgresOptionsWithoutSelectionAreRefused(t *testing.T) {
	stateDir := t.TempDir()
	rt, err := Start(t.Context(), Options{
		StateDir: stateDir,
		Postgres: &PostgresOptions{DSN: "postgres://user:" + leakSentinel + "@127.0.0.1:1/db"},
	})
	if err == nil {
		_ = rt.Stop(context.Background())
		t.Fatal("Start ignored PostgreSQL options and started SQLite")
	}
	if !errors.Is(err, ErrBackendConfiguration) {
		t.Errorf("error = %v, want ErrBackendConfiguration", err)
	}
	if strings.Contains(err.Error(), leakSentinel) {
		t.Errorf("the refusal leaked the DSN: %v", err)
	}
}

// TestRuntimeErrorsNeverCarryTheDSN is the composition-level redaction test.
//
// The store's own errors are already redacted; this covers the layer above,
// where a wrapper that added the configuration back would undo that. Every
// failure path that can see a DSN is driven here.
func TestRuntimeErrorsNeverCarryTheDSN(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		{"unparseable", "postgres://user:" + leakSentinel + "@:::/?x"},
		{"unreachable host", "postgres://user:" + leakSentinel + "@127.0.0.1:1/db?sslmode=disable&connect_timeout=1"},
		{"keyword form", "host=127.0.0.1 port=1 user=u password=" + leakSentinel + " dbname=d sslmode=disable connect_timeout=1"},
		{"not a URL", "://" + leakSentinel},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := t.TempDir()
			rt, err := Start(t.Context(), Options{
				StateDir: stateDir,
				Backend:  BackendPostgres,
				Postgres: &PostgresOptions{DSN: tt.dsn, ConnectTimeout: 2 * time.Second},
			})
			if err == nil {
				_ = rt.Stop(context.Background())
				t.Fatal("Start succeeded with an unusable DSN")
			}

			if strings.Contains(err.Error(), leakSentinel) {
				t.Errorf("the error leaked the DSN secret: %v", err)
			}
			for _, fragment := range []string{"password=", "user:", "@127.0.0.1", "dbname="} {
				if strings.Contains(err.Error(), fragment) {
					t.Errorf("the error echoed DSN fragment %q: %v", fragment, err)
				}
			}

			// A failed PostgreSQL start must not leave a usable endpoint behind,
			// and must not have silently created a SQLite file instead.
			assertNoUsableDiscovery(t, stateDir)
			if _, statErr := os.Stat(filepath.Join(stateDir, DatabaseFileName)); statErr == nil {
				t.Error("a failed PostgreSQL start created a SQLite database")
			}
		})
	}
}

// assertNoUsableDiscovery proves nothing advertised an endpoint.
//
// A discovery file is how every local client finds the runtime, so a failed
// start that left one behind would point the CLI, the TUI and the WebUI at a
// port nobody is serving.
func assertNoUsableDiscovery(t *testing.T, stateDir string) {
	t.Helper()

	path := filepath.Join(stateDir, DiscoveryFileName)
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return // nothing was advertised, which is the expected outcome
	}
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}

	// If a file exists at all, it must not describe a reachable endpoint.
	discovery, err := ReadDiscovery(path)
	if err != nil {
		return // unusable, which is acceptable
	}
	host := strings.TrimPrefix(discovery.APIURL, "http://")
	conn, dialErr := net.DialTimeout("tcp", host, 500*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatalf("a failed start advertised a reachable endpoint at %s (file: %s)",
			discovery.APIURL, payload)
	}
}

// TestDiscoveryNeverCarriesBackendConfiguration keeps the file at two fields.
//
// runtime.json is project-local and world-readable. A DSN there would be worse
// than no credential at all, because it looks like security.
func TestDiscoveryNeverCarriesBackendConfiguration(t *testing.T) {
	dsn := os.Getenv(runtimePostgresDSNEnv)
	if strings.TrimSpace(dsn) == "" {
		// The SQLite case still proves the schema, which is the part that could
		// regress without PostgreSQL present.
		rt := startRuntime(t, t.TempDir())
		assertDiscoveryHasOnlyTwoFields(t, rt)
		return
	}

	rt, err := Start(t.Context(), Options{
		StateDir: t.TempDir(),
		Backend:  BackendPostgres,
		Postgres: &PostgresOptions{DSN: isolatedRuntimeSchema(t, dsn)},
	})
	if err != nil {
		t.Fatalf("Start(postgres) error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	assertDiscoveryHasOnlyTwoFields(t, rt)

	// And nothing the runtime reports about itself is a connection string.
	for _, reported := range []string{rt.Backend(), rt.StateSummary(), rt.APIURL(), rt.WebURL()} {
		for _, fragment := range []string{"password", "@", "dbname", "sslmode", "user="} {
			if strings.Contains(reported, fragment) && !strings.HasPrefix(reported, "http://127.0.0.1") {
				t.Errorf("a reported value looks like configuration: %q", reported)
			}
		}
	}
}

func assertDiscoveryHasOnlyTwoFields(t *testing.T, rt *Runtime) {
	t.Helper()
	payload, err := os.ReadFile(rt.DiscoveryPath())
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	text := string(payload)
	for _, forbidden := range []string{"dsn", "DSN", "password", "postgres://", "backend", "database_url"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("runtime.json contains %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"version"`) || !strings.Contains(text, `"api_url"`) {
		t.Errorf("runtime.json lost a required field: %s", text)
	}
}

// ---------------------------------------------------------------------
// Real PostgreSQL, through the runtime
// ---------------------------------------------------------------------

// isolatedRuntimeSchema gives a runtime test its own PostgreSQL schema.
//
// Same reasoning as the store's own tests: `go test ./...` runs package binaries
// concurrently, so a shared schema would let one package's migration collide
// with another's reads. The name is built here from the test's name, never from
// caller input.
func isolatedRuntimeSchema(t *testing.T, dsn string) string {
	t.Helper()

	var name strings.Builder
	name.WriteString("tvrt_")
	for _, r := range strings.ToLower(t.Name()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			name.WriteRune(r)
		default:
			name.WriteByte('_')
		}
	}
	schema := name.String() + "_" + strconvItoa(time.Now().UnixNano())
	if len(schema) > 60 {
		schema = schema[len(schema)-60:]
	}

	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot reach PostgreSQL to create a test schema: %v", err)
	}
	defer admin.Close()

	// The identifier is quoted and was built above from a constrained alphabet
	// over this test's own name. No caller input reaches it.
	if _, err := admin.Exec(context.Background(),
		`CREATE SCHEMA IF NOT EXISTS "`+schema+`"`); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		// Best effort: the schema name is unique, so a failed drop cannot poison
		// another test.
		_, _ = cleanup.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})

	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + schema
}

func strconvItoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}

// TestRuntimeOnPostgresServesTheRealAPI is the runtime-level smoke.
//
// Everything below goes through the real /v1 surface rather than calling the
// store, because the claim being tested is that the whole composition works on
// the shared backend — not that the store does.
func TestRuntimeOnPostgresServesTheRealAPI(t *testing.T) {
	dsn := os.Getenv(runtimePostgresDSNEnv)
	if strings.TrimSpace(dsn) == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL runtime test", runtimePostgresDSNEnv)
	}
	isolated := isolatedRuntimeSchema(t, dsn)
	stateDir := t.TempDir()

	rt, err := Start(t.Context(), Options{
		StateDir: stateDir,
		Backend:  BackendPostgres,
		Postgres: &PostgresOptions{DSN: isolated},
	})
	if err != nil {
		t.Fatalf("Start(postgres) error = %v", err)
	}

	if rt.Backend() != BackendPostgres {
		t.Fatalf("Backend() = %q, want %q", rt.Backend(), BackendPostgres)
	}
	// PostgreSQL mode creates no SQLite file.
	if _, statErr := os.Stat(filepath.Join(stateDir, DatabaseFileName)); statErr == nil {
		t.Error("PostgreSQL mode created a SQLite database file")
	}

	client := &runtimeAPIClient{t: t, baseURL: rt.APIURL()}

	// The WebUI is served identically — a browser cannot tell which backend is
	// underneath, which is the point.
	if status, body := client.get("/"); status != 200 || !strings.Contains(body, "<!doctype html>") {
		t.Errorf("GET / on PostgreSQL returned %d", status)
	}

	client.post("/v1/projects", `{"id":"proj-pg","name":"Checkout"}`, 201)
	client.post("/v1/agents", `{"id":"agent-pg","project_id":"proj-pg","name":"Agent"}`, 201)
	client.post("/v1/candidates",
		`{"id":"cand-pg","agent_id":"agent-pg","metadata":{"label":"v2"}}`, 201)
	client.post("/v1/evaluation-runs",
		`{"id":"run-pg","candidate_id":"cand-pg","environment":"local","behavioral_profile":"checkout"}`, 201)
	client.post("/v1/evaluation-runs/run-pg/start", "", 200)

	status, body := client.get("/v1/evaluation-runs/run-pg")
	if status != 200 {
		t.Fatalf("GET run status = %d", status)
	}
	if !strings.Contains(body, `"status":"running"`) {
		t.Errorf("run body = %s", body)
	}
	// No PostgreSQL detail reaches a caller.
	assertNoBackendDetail(t, body)

	status, progress := client.get("/v1/evaluation-runs/run-pg/progress")
	if status != 200 {
		t.Fatalf("GET progress status = %d", status)
	}
	// Counters are decimal strings on this backend too.
	if !strings.Contains(progress, `"record_count":"0"`) {
		t.Errorf("progress body = %s", progress)
	}
	assertNoBackendDetail(t, progress)

	// A refusal must be the API's envelope, not a driver message.
	status, refused := client.get("/v1/evaluation-runs/absent")
	if status != 404 {
		t.Errorf("GET absent run status = %d, want 404", status)
	}
	assertNoBackendDetail(t, refused)

	client.post("/v1/evaluation-runs/run-pg/complete", "", 200)

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// --- restart against the same database ---
	restarted, err := Start(t.Context(), Options{
		StateDir: stateDir,
		Backend:  BackendPostgres,
		Postgres: &PostgresOptions{DSN: isolated},
	})
	if err != nil {
		t.Fatalf("restart error = %v", err)
	}
	t.Cleanup(func() { _ = restarted.Stop(context.Background()) })

	after := &runtimeAPIClient{t: t, baseURL: restarted.APIURL()}
	status, body = after.get("/v1/evaluation-runs/run-pg")
	if status != 200 {
		t.Fatalf("after restart, GET run status = %d", status)
	}
	if !strings.Contains(body, `"status":"completed"`) {
		t.Errorf("state did not survive the restart: %s", body)
	}
}

// assertNoBackendDetail proves no PostgreSQL internal reached the wire.
func assertNoBackendDetail(t *testing.T, body string) {
	t.Helper()
	lowered := strings.ToLower(body)
	for _, forbidden := range []string{
		"sqlstate", "pgx", "pq:", "postgres", "postgresql",
		"platform_evaluation_runs", "platform_projects", "constraint",
		"relation ", "column ", "dsn", "sslmode", "search_path",
		"select ", "insert into", "update ", "for update",
	} {
		if strings.Contains(lowered, forbidden) {
			t.Errorf("a /v1 response exposed backend detail %q: %s", forbidden, body)
		}
	}
}

// runtimeAPIClient is a minimal HTTP helper against the real listener.
type runtimeAPIClient struct {
	t       *testing.T
	baseURL string
}

func (c *runtimeAPIClient) get(path string) (int, string) {
	c.t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), "GET", c.baseURL+path, nil)
	if err != nil {
		c.t.Fatalf("NewRequest: %v", err)
	}
	return c.do(request)
}

func (c *runtimeAPIClient) post(path, body string, wantStatus int) {
	c.t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(context.Background(), "POST", c.baseURL+path, reader)
	if err != nil {
		c.t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	status, got := c.do(request)
	if status != wantStatus {
		c.t.Fatalf("POST %s status = %d, want %d: %s", path, status, wantStatus, got)
	}
}

func (c *runtimeAPIClient) do(request *http.Request) (int, string) {
	c.t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", request.Method, request.URL.Path, err)
	}
	defer response.Body.Close()
	payload := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, readErr := response.Body.Read(buf)
		payload = append(payload, buf[:n]...)
		if readErr != nil || len(payload) > 1<<20 {
			break
		}
	}
	return response.StatusCode, string(payload)
}
