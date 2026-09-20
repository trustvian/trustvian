package scripts_test

// Tests for backup-postgres.sh and restore-postgres.sh (task 044).
//
// Two tiers, chosen so the safety rules are enforced everywhere while the
// expensive proof runs where the tools exist:
//
//   - Always: every refusal that needs no database — missing arguments,
//     overwriting a previous backup, a corrupt or unverifiable artifact, an
//     unsafe target name. These need only bash and sha256sum/shasum.
//
//   - Opt-in (TRUSTVIAN_TEST_BACKUP_RESTORE=1 plus TRUSTVIAN_TEST_POSTGRES_DSN,
//     with pg_dump/pg_restore/psql on PATH at the server's major version):
//     real backups and restores, proving learned behavioral state survives by
//     BEHAVIOR rather than by row count, that an online backup under
//     concurrent writes is consistent, that failures fail closed, and — with
//     TRUSTVIAN_TEST_UPGRADE_FROM_BINARY — that upgrading from a real previous
//     release preserves learned state.
//
// The opt-in is a separate variable rather than "DSN set and tools found",
// because a runner with the DSN set but an older pg_dump would otherwise skip
// silently instead of failing where the proof was expected to run.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/store"
	"github.com/trustvian/trustvian/internal/store/postgres"
)

const (
	backupRestoreEnv = "TRUSTVIAN_TEST_BACKUP_RESTORE"
	postgresDSNEnv   = "TRUSTVIAN_TEST_POSTGRES_DSN"
	upgradeBinaryEnv = "TRUSTVIAN_TEST_UPGRADE_FROM_BINARY"
	upgradeFromEnv   = "TRUSTVIAN_TEST_UPGRADE_FROM_VERSION"
)

// ---------------------------------------------------------------------
// Script execution
// ---------------------------------------------------------------------

type scriptResult struct {
	output string // stdout and stderr, interleaved
	code   int
}

// runScript runs a script from this directory with a controlled
// environment: PATH and HOME from the test process, every inherited PG*
// variable removed (so a developer's own libpq settings cannot point a test
// at a real database), then env applied on top.
func runScript(t *testing.T, env map[string]string, name string, args ...string) scriptResult {
	t.Helper()

	path, err := filepath.Abs(name)
	if err != nil {
		t.Fatalf("filepath.Abs(%s): %v", name, err)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Env = scriptEnv(env)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running %s: %v", name, err)
		}
		code = exitErr.ExitCode()
	}
	return scriptResult{output: out.String(), code: code}
}

func scriptEnv(env map[string]string) []string {
	var base []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PG") {
			continue
		}
		base = append(base, kv)
	}
	for k, v := range env {
		base = append(base, k+"="+v)
	}
	return base
}

// writeSums writes a SHA256SUMS file in sha256sum's own format for the named
// files in dir — what backup-postgres.sh produces.
func writeSums(t *testing.T, dir string, files ...string) {
	t.Helper()
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), f)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write SHA256SUMS: %v", err)
	}
}

// currentSchemaManifestLine is the MANIFEST line a backup taken by this
// build carries. Derived from postgres.SchemaVersion rather than written as
// a literal: task 051 moved the schema from 1 to 2, and every hardcoded copy
// of that number became a false assertion at once.
var currentSchemaManifestLine = fmt.Sprintf("trustvian_schema_version=%d", postgres.SchemaVersion)

const validManifest = `format=trustvian-postgres-backup
format_version=1
created_at=2026-09-17T04:00:00Z
dump_file=trustvian.dump
dump_format=custom
trustvian_schema_version=1
trustvian_version=unknown
postgres_server_version=17.6
pg_dump_version=17.6
`

// fakeBackup builds a structurally complete backup directory whose checksums
// are valid. Its dump is not a real archive, which is fine: every refusal it
// is used for must happen before pg_restore is ever reached.
func fakeBackup(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "backup")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trustvian.dump"), []byte("PGDMP not-a-real-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(validManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSums(t, dir, "trustvian.dump", "MANIFEST")
	return dir
}

// ---------------------------------------------------------------------
// Always: refusals that need no database
// ---------------------------------------------------------------------

func TestBackupScriptRefusesUnsafeInvocations(t *testing.T) {
	existing := t.TempDir() // exists by construction

	tests := []struct {
		name     string
		env      map[string]string
		args     []string
		wantCode int
		wantMsg  string
	}{
		{
			name:     "no arguments",
			wantCode: 2,
			wantMsg:  "usage:",
		},
		{
			name:     "unknown argument",
			args:     []string{"--output", filepath.Join(t.TempDir(), "b"), "--dsn", "postgres://x"},
			wantCode: 2,
			wantMsg:  "unknown argument",
		},
		{
			name:     "existing output is never overwritten",
			env:      map[string]string{"PGDATABASE": "trustvian"},
			args:     []string{"--output", existing},
			wantCode: 1,
			wantMsg:  "refusing to overwrite a previous backup",
		},
		{
			name:     "missing parent directory",
			env:      map[string]string{"PGDATABASE": "trustvian"},
			args:     []string{"--output", filepath.Join(t.TempDir(), "missing", "b")},
			wantCode: 1,
			wantMsg:  "does not exist",
		},
		{
			name:     "database must be named explicitly",
			args:     []string{"--output", filepath.Join(t.TempDir(), "b")},
			wantCode: 1,
			wantMsg:  "PGDATABASE is not set",
		},
		{
			name:     "release version must be exact",
			env:      map[string]string{"PGDATABASE": "trustvian"},
			args:     []string{"--output", filepath.Join(t.TempDir(), "b"), "--trustvian-version", "latest"},
			wantCode: 1,
			wantMsg:  "exact release version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runScript(t, tt.env, "backup-postgres.sh", tt.args...)
			if got.code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d; output:\n%s", got.code, tt.wantCode, got.output)
			}
			if !strings.Contains(got.output, tt.wantMsg) {
				t.Errorf("output does not mention %q:\n%s", tt.wantMsg, got.output)
			}
		})
	}

	// The refused overwrite left the existing directory untouched.
	entries, err := os.ReadDir(existing)
	if err != nil || len(entries) != 0 {
		t.Errorf("existing output directory was modified: entries=%v err=%v", entries, err)
	}
}

func TestRestoreScriptRefusesUnverifiableBackups(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, dir string) // applied to a valid fake backup
		env     map[string]string
		target  string
		wantMsg string
	}{
		{
			name:    "missing backup directory",
			mutate:  func(t *testing.T, dir string) { mustRemoveAll(t, dir) },
			wantMsg: "does not exist",
		},
		{
			name:    "missing dump",
			mutate:  func(t *testing.T, dir string) { mustRemove(t, filepath.Join(dir, "trustvian.dump")) },
			wantMsg: "backup is incomplete",
		},
		{
			name:    "missing checksum file",
			mutate:  func(t *testing.T, dir string) { mustRemove(t, filepath.Join(dir, "SHA256SUMS")) },
			wantMsg: "backup is incomplete",
		},
		{
			name: "corrupted dump",
			mutate: func(t *testing.T, dir string) {
				flipByte(t, filepath.Join(dir, "trustvian.dump"))
			},
			wantMsg: "checksum verification FAILED",
		},
		{
			name: "modified manifest",
			mutate: func(t *testing.T, dir string) {
				p := filepath.Join(dir, "MANIFEST")
				data, _ := os.ReadFile(p)
				data = bytes.Replace(data, []byte("trustvian_schema_version=1"), []byte("trustvian_schema_version=2"), 1)
				mustWrite(t, p, data)
			},
			wantMsg: "checksum verification FAILED",
		},
		{
			name: "checksum file that covers only the manifest",
			mutate: func(t *testing.T, dir string) {
				flipByte(t, filepath.Join(dir, "trustvian.dump"))
				writeSums(t, dir, "MANIFEST")
			},
			wantMsg: "must list exactly",
		},
		{
			name: "unsupported manifest format version",
			mutate: func(t *testing.T, dir string) {
				p := filepath.Join(dir, "MANIFEST")
				data, _ := os.ReadFile(p)
				mustWrite(t, p, bytes.Replace(data, []byte("format_version=1"), []byte("format_version=9"), 1))
				writeSums(t, dir, "trustvian.dump", "MANIFEST")
			},
			wantMsg: "unsupported manifest format_version",
		},
		{
			name:    "target name that is not a plain identifier",
			target:  "trustvian host=elsewhere",
			wantMsg: "must be a plain database name",
		},
		{
			name:    "target that is the PGDATABASE database",
			env:     map[string]string{"PGDATABASE": "trustvian"},
			target:  "trustvian",
			wantMsg: "most likely the active one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeBackup(t)
			if tt.mutate != nil {
				tt.mutate(t, dir)
			}
			target := tt.target
			if target == "" {
				target = "trustvian_restore_test"
			}
			got := runScript(t, tt.env, "restore-postgres.sh", "--backup", dir, "--target-db", target)
			if got.code != 1 {
				t.Fatalf("exit code = %d, want 1; output:\n%s", got.code, got.output)
			}
			if !strings.Contains(got.output, tt.wantMsg) {
				t.Errorf("output does not mention %q:\n%s", tt.wantMsg, got.output)
			}
			if strings.Contains(got.output, "restoring into") {
				t.Errorf("restore began despite the refusal:\n%s", got.output)
			}
		})
	}

	t.Run("missing arguments", func(t *testing.T) {
		got := runScript(t, nil, "restore-postgres.sh", "--backup", fakeBackup(t))
		if got.code != 2 || !strings.Contains(got.output, "usage:") {
			t.Errorf("exit=%d, want 2 with usage; output:\n%s", got.code, got.output)
		}
	})
}

func mustRemove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}

func mustRemoveAll(t *testing.T, p string) {
	t.Helper()
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func flipByte(t *testing.T, p string) {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xFF
	mustWrite(t, p, data)
}

// ---------------------------------------------------------------------
// Opt-in: a real PostgreSQL server and client tools
// ---------------------------------------------------------------------

// pgServer is a PostgreSQL server reachable through libpq environment
// variables derived from TRUSTVIAN_TEST_POSTGRES_DSN.
type pgServer struct {
	dsn      *url.URL
	password string
	env      map[string]string // host/port/user/password/sslmode, no database
}

func requireBackupRestore(t *testing.T) *pgServer {
	t.Helper()
	if os.Getenv(backupRestoreEnv) != "1" {
		t.Skipf("%s not set to 1; skipping backup/restore integration test (see docs/operations.md)", backupRestoreEnv)
	}
	raw := os.Getenv(postgresDSNEnv)
	if raw == "" {
		t.Fatalf("%s=1 requires %s", backupRestoreEnv, postgresDSNEnv)
	}
	for _, tool := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s=1 requires %s on PATH: %v", backupRestoreEnv, tool, err)
		}
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("%s must be a postgres:// URL for these tests", postgresDSNEnv)
	}
	password, _ := u.User.Password()
	env := map[string]string{
		"PGHOST":     u.Hostname(),
		"PGUSER":     u.User.Username(),
		"PGPASSWORD": password,
	}
	if port := u.Port(); port != "" {
		env["PGPORT"] = port
	}
	if mode := u.Query().Get("sslmode"); mode != "" {
		env["PGSSLMODE"] = mode
	}
	return &pgServer{dsn: u, password: password, env: env}
}

// with returns the server's libpq environment plus extra.
func (s *pgServer) with(extra map[string]string) map[string]string {
	out := make(map[string]string, len(s.env)+len(extra))
	maps.Copy(out, s.env)
	maps.Copy(out, extra)
	return out
}

// dsnFor returns a DSN for database db on this server.
func (s *pgServer) dsnFor(db string) string {
	u := *s.dsn
	u.Path = "/" + db
	return u.String()
}

// psql runs sql against db and returns trimmed, unaligned output.
func (s *pgServer) psql(t *testing.T, db, sql string) string {
	t.Helper()
	cmd := exec.Command("psql", "-w", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", sql)
	cmd.Env = scriptEnv(s.with(map[string]string{"PGDATABASE": db}))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql on %s failed: %v\n%s", db, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (s *pgServer) maintenanceDB() string {
	return strings.TrimPrefix(s.dsn.Path, "/")
}

// createDatabase creates a uniquely-named, empty database and drops it when
// the test ends — FORCE, so a quarantined database or a lingering pool
// connection cannot leak it.
func (s *pgServer) createDatabase(t *testing.T, role string) string {
	t.Helper()
	name := fmt.Sprintf("tvbr_%s_%d_%d", role, os.Getpid(), time.Now().UnixNano())
	s.psql(t, s.maintenanceDB(), "CREATE DATABASE "+name)
	t.Cleanup(func() {
		cmd := exec.Command("psql", "-w", "-X", "-q", "-c", "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		cmd.Env = scriptEnv(s.with(map[string]string{"PGDATABASE": s.maintenanceDB()}))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("cleanup: drop %s: %v\n%s", name, err, out)
		}
	})
	return name
}

func (s *pgServer) allowsConnections(t *testing.T, db string) bool {
	t.Helper()
	return s.psql(t, s.maintenanceDB(), "SELECT datallowconn FROM pg_database WHERE datname = '"+db+"'") == "t"
}

// backup runs backup-postgres.sh against db into a new directory and fails
// the test unless it succeeds.
func (s *pgServer) backup(t *testing.T, db string, extraArgs ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "backup")
	got := runScript(t, s.with(map[string]string{"PGDATABASE": db}), "backup-postgres.sh",
		append([]string{"--output", dir}, extraArgs...)...)
	if got.code != 0 {
		t.Fatalf("backup-postgres.sh exit %d:\n%s", got.code, got.output)
	}
	s.assertNoPasswordIn(t, "backup output", got.output)
	return dir
}

// restore runs restore-postgres.sh and returns its result without judging it.
func (s *pgServer) restore(t *testing.T, backupDir, target string) scriptResult {
	t.Helper()
	got := runScript(t, s.with(nil), "restore-postgres.sh", "--backup", backupDir, "--target-db", target)
	s.assertNoPasswordIn(t, "restore output", got.output)
	return got
}

// assertNoPasswordIn checks that the server password never appears in text.
// A password too short or too generic to be distinctive (CI's throwaway
// "trustvian") would match ordinary words like the dump file name, so the
// check is skipped for one — the dedicated CI job uses a distinctive value.
func (s *pgServer) assertNoPasswordIn(t *testing.T, what, text string) {
	t.Helper()
	if len(s.password) < 12 || strings.Contains("trustvian_baseline trustvian_schema_version trustvian.dump", s.password) {
		return
	}
	if strings.Contains(text, s.password) {
		t.Errorf("%s contains the database password", what)
	}
}

// openStore compiles a PostgreSQL store through the public config boundary —
// the path every runtime takes, including its Migrate step.
func openStore(t *testing.T, dsn string) (store.Store, error) {
	t.Helper()
	s, err := config.CompileStorage(config.StorageConfig{
		Version:  config.StorageSchemaVersionV1,
		Type:     config.StorageTypePostgres,
		Postgres: &config.PostgresStorageConfig{DSN: dsn},
	})
	if err != nil {
		return nil, err
	}
	if c, ok := s.(io.Closer); ok {
		t.Cleanup(func() { _ = c.Close() })
	}
	return s, nil
}

func mustOpenStore(t *testing.T, dsn string) store.Store {
	t.Helper()
	s, err := openStore(t, dsn)
	if err != nil {
		t.Fatalf("CompileStorage: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------
// Deterministic behavior
// ---------------------------------------------------------------------

var recoveryActors = []string{"svc-payments", "svc-ledger", "agent-reconciler"}

const recoveryEnvironment = "production"

// recoveryEvent exercises several behavioral dimensions — distinct
// operations (transitions and trigrams), varying latency, occasional errors —
// so a restore that preserved fingerprints but dropped sequence or timing
// state would change the probe results below.
func recoveryEvent(actor string, step int, ts time.Time) event.Event {
	operations := []string{"SELECT accounts", "UPDATE balance", "SELECT ledger"}
	return event.Event{
		ID:        fmt.Sprintf("%s-%d", actor, step),
		Timestamp: ts,
		Actor: event.Actor{
			ID:                 actor,
			Type:               event.ActorTypeService,
			IdentityConfidence: 0.95,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryDB,
			Name:     operations[step%len(operations)],
		},
		Target:     event.Target{Name: "payment-db"},
		Context:    event.Context{Environment: recoveryEnvironment},
		Attributes: map[string]any{"duration_ms": 10 + step%5, "error": step%17 == 0},
	}
}

var recoveryEpoch = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

func recoveryCorpus(steps int) []event.Event {
	var events []event.Event
	for _, actor := range recoveryActors {
		for i := range steps {
			events = append(events, recoveryEvent(actor, i, recoveryEpoch.Add(time.Duration(i)*time.Minute)))
		}
	}
	return events
}

// probeEvents are analyzed, never observed: one routine event per actor, and
// one it has never done — a sensitive target at an unusual hour.
func probeEvents() []event.Event {
	var probes []event.Event
	at := recoveryEpoch.Add(24 * time.Hour)
	for _, actor := range recoveryActors {
		probes = append(probes, recoveryEvent(actor, 1, at))
		novel := recoveryEvent(actor, 2, at.Add(15*time.Hour))
		novel.ID += "-novel"
		novel.Operation = event.Operation{Category: event.OperationCategoryDB, Name: "DROP TABLE accounts"}
		novel.Target = event.Target{Name: "secrets-vault"}
		probes = append(probes, novel)
	}
	return probes
}

func learn(t *testing.T, engine *trustvian.Engine, events []event.Event) {
	t.Helper()
	ctx := context.Background()
	for _, ev := range events {
		res, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze(%s): %v", ev.ID, err)
		}
		if _, err := engine.Observe(ctx, res); err != nil {
			t.Fatalf("Observe(%s): %v", ev.ID, err)
		}
	}
}

// outcome is everything a decision is made of. Compared with DeepEqual, so a
// difference in any contributor, score, or explanation fails.
type outcome struct {
	Anomaly     any
	Trust       any
	Decision    string
	Explanation any
}

func analyzeProbes(t *testing.T, engine *trustvian.Engine) []outcome {
	t.Helper()
	var out []outcome
	for _, ev := range probeEvents() {
		res, err := engine.Analyze(context.Background(), ev)
		if err != nil {
			t.Fatalf("Analyze(%s): %v", ev.ID, err)
		}
		out = append(out, outcome{res.Anomaly, res.Trust, string(res.Decision), res.Explanation})
	}
	return out
}

func observationTotal(bl baseline.Baseline) uint64 {
	var total uint64
	for _, fs := range bl.Fingerprints {
		total += fs.Count
	}
	return total
}

// baselineJSON returns the canonical encoding of an actor's stored baseline,
// and whether one exists.
func baselineJSON(t *testing.T, s store.Store, actor string) (string, uint64, bool) {
	t.Helper()
	bl, ok := s.Get(context.Background(), baseline.Key{ActorID: actor, Environment: recoveryEnvironment})
	raw, err := json.Marshal(bl)
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	return string(raw), observationTotal(bl), ok
}

// ---------------------------------------------------------------------
// Opt-in tests
// ---------------------------------------------------------------------

// TestBackupRestorePreservesLearnedBehavior is the recovery proof: state
// learned through the real gated Analyze+Observe loop, backed up, and
// restored into a fresh database must make a new Engine behave identically —
// and not merely identically to a cold start.
func TestBackupRestorePreservesLearnedBehavior(t *testing.T) {
	pg := requireBackupRestore(t)
	const steps = 90 // ~30 per fingerprint: past maturity

	src := pg.createDatabase(t, "src")
	srcStore := mustOpenStore(t, pg.dsnFor(src))
	learn(t, trustvian.NewEngine(trustvian.WithStore(srcStore)), recoveryCorpus(steps))

	wantProbes := analyzeProbes(t, trustvian.NewEngine(trustvian.WithStore(srcStore)))

	// --- backup, taken with the source store still open (online) ---
	dir := pg.backup(t, src, "--trustvian-version", "v0.0.0-test")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("backup directory mode = %o, want 700", perm)
	}
	for _, f := range []string{"trustvian.dump", "MANIFEST", "SHA256SUMS"} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("backup is missing %s: %v", f, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", f, perm)
		}
	}

	manifest, _ := os.ReadFile(filepath.Join(dir, "MANIFEST"))
	for _, want := range []string{
		"format=trustvian-postgres-backup",
		currentSchemaManifestLine,
		"trustvian_version=v0.0.0-test",
		"dump_format=custom",
	} {
		if !bytes.Contains(manifest, []byte(want)) {
			t.Errorf("MANIFEST lacks %q:\n%s", want, manifest)
		}
	}
	// No connection detail of any kind: compare every manifest value against
	// the host, user, database, and password this backup was taken with.
	sensitive := map[string]string{"host": pg.env["PGHOST"], "user": pg.env["PGUSER"], "database": src, "password": pg.password}
	for line := range strings.SplitSeq(strings.TrimSpace(string(manifest)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		for what, v := range sensitive {
			if v != "" && value == v {
				t.Errorf("MANIFEST %s records the connection %s", key, what)
			}
		}
		if strings.Contains(value, "://") {
			t.Errorf("MANIFEST %s looks like a connection string", key)
		}
	}

	// --- restore into a fresh database ---
	dst := pg.createDatabase(t, "dst")
	got := pg.restore(t, dir, dst)
	if got.code != 0 || !strings.Contains(got.output, "RESTORE VERIFIED") {
		t.Fatalf("restore exit %d:\n%s", got.code, got.output)
	}

	// --- Trustvian starts on it (Migrate is the compatibility authority) ---
	dstStore := mustOpenStore(t, pg.dsnFor(dst))

	for _, actor := range recoveryActors {
		want, wantCount, _ := baselineJSON(t, srcStore, actor)
		gotJSON, gotCount, ok := baselineJSON(t, dstStore, actor)
		if !ok {
			t.Fatalf("restored database has no baseline for %s", actor)
		}
		if gotCount != uint64(steps) || wantCount != uint64(steps) {
			t.Errorf("%s observations: source %d, restored %d, want %d", actor, wantCount, gotCount, steps)
		}
		if gotJSON != want {
			t.Errorf("%s restored baseline differs from the source", actor)
		}
	}

	restoredProbes := analyzeProbes(t, trustvian.NewEngine(trustvian.WithStore(dstStore)))
	if !reflect.DeepEqual(restoredProbes, wantProbes) {
		t.Errorf("restored engine decides differently from the source:\n got %+v\nwant %+v", restoredProbes, wantProbes)
	}

	// Non-vacuous: two empty baselines would also agree. A cold engine must
	// not, or the comparison above proved nothing about learned state.
	coldProbes := analyzeProbes(t, trustvian.NewEngine())
	if reflect.DeepEqual(coldProbes, restoredProbes) {
		t.Fatal("restored behavior equals cold-start behavior — the comparison is vacuous")
	}

	// Learning resumes from the restored state rather than restarting.
	learn(t, trustvian.NewEngine(trustvian.WithStore(dstStore)), []event.Event{
		recoveryEvent(recoveryActors[0], steps, recoveryEpoch.Add(time.Duration(steps)*time.Minute)),
	})
	if _, n, _ := baselineJSON(t, dstStore, recoveryActors[0]); n != uint64(steps)+1 {
		t.Errorf("after one more observation the restored count is %d, want %d", n, steps+1)
	}
	if _, n, _ := baselineJSON(t, srcStore, recoveryActors[0]); n != uint64(steps) {
		t.Errorf("the source changed after restoring elsewhere: count %d, want %d", n, steps)
	}
}

// TestBackupDuringConcurrentWritesIsConsistent takes an online backup while
// observations commit continuously. pg_dump's single snapshot must produce a
// database where every row is whole, derived columns agree with the stored
// baseline, and each actor's restored count lies between what had been
// acknowledged before the backup started and after it finished. Writes after
// the snapshot are expected to be absent; that is the recovery point, not a
// defect.
func TestBackupDuringConcurrentWritesIsConsistent(t *testing.T) {
	pg := requireBackupRestore(t)

	src := pg.createDatabase(t, "live")
	srcStore := mustOpenStore(t, pg.dsnFor(src))
	engine := trustvian.NewEngine(trustvian.WithStore(srcStore))

	actors := make([]string, 8)
	acked := make([]atomic.Uint64, len(actors))
	for i := range actors {
		actors[i] = fmt.Sprintf("writer-%d", i)
		for step := range 5 {
			learn(t, engine, []event.Event{recoveryWriterEvent(actors[i], step)})
		}
		acked[i].Store(5)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writeErr atomic.Value
	for i := range actors {
		wg.Go(func() {
			ctx := context.Background()
			for step := 5; ; step++ {
				select {
				case <-stop:
					return
				default:
				}
				res, err := engine.Analyze(ctx, recoveryWriterEvent(actors[i], step))
				if err == nil {
					var learned bool
					learned, err = engine.Observe(ctx, res)
					if err == nil && learned {
						acked[i].Add(1)
					}
				}
				if err != nil {
					writeErr.Store(err)
					return
				}
			}
		})
	}

	// Let writers reach steady state, then snapshot the lower bound.
	time.Sleep(200 * time.Millisecond)
	before := make([]uint64, len(actors))
	for i := range actors {
		before[i] = acked[i].Load()
	}

	dir := pg.backup(t, src)

	close(stop)
	wg.Wait()
	if err, _ := writeErr.Load().(error); err != nil {
		t.Fatalf("concurrent writer failed: %v", err)
	}
	after := make([]uint64, len(actors))
	var duringBackup uint64
	for i := range actors {
		after[i] = acked[i].Load()
		duringBackup += after[i] - before[i]
	}
	if duringBackup == 0 {
		t.Fatal("no observation committed while the backup ran — the test did not exercise concurrency")
	}
	t.Logf("%d observations committed between the lower-bound sample and writer shutdown", duringBackup)

	dst := pg.createDatabase(t, "snap")
	if got := pg.restore(t, dir, dst); got.code != 0 {
		t.Fatalf("restore exit %d:\n%s", got.code, got.output)
	}
	dstStore := mustOpenStore(t, pg.dsnFor(dst))

	derived := map[string]uint64{}
	for line := range strings.SplitSeq(pg.psql(t, dst, "SELECT actor_id || '|' || observation_count FROM trustvian_baseline"), "\n") {
		actor, count, ok := strings.Cut(line, "|")
		if !ok {
			t.Fatalf("unexpected psql row %q", line)
		}
		n, err := strconv.ParseUint(count, 10, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		derived[actor] = n
	}

	for i, actor := range actors {
		key := baseline.Key{ActorID: actor, Environment: recoveryEnvironment}
		bl, ok := dstStore.Get(context.Background(), key)
		if !ok {
			t.Fatalf("%s: restored baseline missing or undecodable", actor)
		}
		n := observationTotal(bl)
		if n < before[i] || n > after[i] {
			t.Errorf("%s: restored %d observations, want within [%d, %d]", actor, n, before[i], after[i])
		}
		if derived[actor] != n {
			t.Errorf("%s: derived observation_count %d disagrees with stored baseline %d — row is not transactionally whole", actor, derived[actor], n)
		}
	}
}

func recoveryWriterEvent(actor string, step int) event.Event {
	return recoveryEvent(actor, step, recoveryEpoch.Add(time.Duration(step)*time.Second))
}

// TestBackupRestoreFailClosed covers the failure cases an operator can
// actually hit. Every one must end in an explicit error, and none may leave
// something Trustvian would start against as if it were valid.
func TestBackupRestoreFailClosed(t *testing.T) {
	pg := requireBackupRestore(t)

	src := pg.createDatabase(t, "fsrc")
	srcStore := mustOpenStore(t, pg.dsnFor(src))
	learn(t, trustvian.NewEngine(trustvian.WithStore(srcStore)), recoveryCorpus(10))
	good := pg.backup(t, src)

	t.Run("backup of a database without Trustvian tables is refused", func(t *testing.T) {
		empty := pg.createDatabase(t, "notv")
		out := filepath.Join(t.TempDir(), "b")
		got := runScript(t, pg.with(map[string]string{"PGDATABASE": empty}), "backup-postgres.sh", "--output", out)
		if got.code == 0 || !strings.Contains(got.output, "does not contain Trustvian's tables") {
			t.Fatalf("exit %d:\n%s", got.code, got.output)
		}
		assertNothingWritten(t, out)
	})

	t.Run("unreachable server fails without leaking the password", func(t *testing.T) {
		const secret = "tv-unreachable-S3cr3t-Pa55word"
		out := filepath.Join(t.TempDir(), "b")
		got := runScript(t, map[string]string{
			"PGHOST": "127.0.0.1", "PGPORT": "1", "PGUSER": "trustvian",
			"PGPASSWORD": secret, "PGDATABASE": "trustvian", "PGCONNECT_TIMEOUT": "3",
		}, "backup-postgres.sh", "--output", out)
		if got.code == 0 || !strings.Contains(got.output, "cannot connect") {
			t.Fatalf("exit %d:\n%s", got.code, got.output)
		}
		if strings.Contains(got.output, secret) {
			t.Error("output contains the password")
		}
		assertNothingWritten(t, out)
	})

	t.Run("non-empty target is refused and left untouched", func(t *testing.T) {
		got := pg.restore(t, good, src)
		if got.code == 0 || !strings.Contains(got.output, "is not empty") {
			t.Fatalf("exit %d:\n%s", got.code, got.output)
		}
		if !pg.allowsConnections(t, src) {
			t.Error("a refused target was quarantined — refusals must not modify it")
		}
		if n := pg.psql(t, src, "SELECT count(*) FROM trustvian_baseline"); n != "3" {
			t.Errorf("refused target now has %s baselines, want 3", n)
		}
	})

	t.Run("target in use is refused", func(t *testing.T) {
		busy := pg.createDatabase(t, "busy")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		holder := exec.CommandContext(ctx, "psql", "-w", "-X", "-q", "-c", "SELECT pg_sleep(60)")
		holder.Env = scriptEnv(pg.with(map[string]string{"PGDATABASE": busy}))
		if err := holder.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { cancel(); _ = holder.Wait() }()

		deadline := time.Now().Add(10 * time.Second)
		for pg.psql(t, pg.maintenanceDB(), "SELECT count(*) FROM pg_stat_activity WHERE datname = '"+busy+"'") == "0" {
			if time.Now().After(deadline) {
				t.Fatal("session holder never connected")
			}
			time.Sleep(50 * time.Millisecond)
		}

		got := pg.restore(t, good, busy)
		if got.code == 0 || !strings.Contains(got.output, "other session") {
			t.Fatalf("exit %d:\n%s", got.code, got.output)
		}
	})

	t.Run("pg_restore failure commits nothing and quarantines the target", func(t *testing.T) {
		broken := copyBackup(t, good)
		dump := filepath.Join(broken, "trustvian.dump")
		data, _ := os.ReadFile(dump)
		// Cut the tail, not the middle: the archive's table of contents
		// (every CREATE TABLE) comes first and the data blocks last, so
		// this damage surfaces only AFTER the schema has been created. That
		// is what makes the test prove atomicity — truncating into the table
		// of contents fails before anything executes, and would pass even
		// without --single-transaction.
		mustWrite(t, dump, data[:len(data)-24])
		// Re-checksummed: the damage predates the checksum, so only
		// pg_restore itself can catch it.
		writeSums(t, broken, "trustvian.dump", "MANIFEST")

		target := pg.createDatabase(t, "trunc")
		got := pg.restore(t, broken, target)
		if got.code == 0 || !strings.Contains(got.output, "RESTORE FAILED") {
			t.Fatalf("exit %d:\n%s", got.code, got.output)
		}
		if strings.Contains(got.output, "RESTORE VERIFIED") {
			t.Fatal("a failed restore was reported verified")
		}
		assertQuarantinedAndEmpty(t, pg, target)
	})

	t.Run("manifest and restored schema disagree", func(t *testing.T) {
		mismatched := copyBackup(t, good)
		p := filepath.Join(mismatched, "MANIFEST")
		data, _ := os.ReadFile(p)
		mustWrite(t, p, bytes.Replace(data,
			[]byte(currentSchemaManifestLine),
			fmt.Appendf(nil, "trustvian_schema_version=%d", postgres.SchemaVersion+1), 1))
		writeSums(t, mismatched, "trustvian.dump", "MANIFEST")

		target := pg.createDatabase(t, "mism")
		got := pg.restore(t, mismatched, target)
		if got.code == 0 || !strings.Contains(got.output, "schema version matches the manifest") {
			t.Fatalf("exit %d:\n%s", got.code, got.output)
		}
		if pg.allowsConnections(t, target) {
			t.Error("target that failed verification was not quarantined")
		}
	})

	t.Run("restored database at a newer schema version is refused by Trustvian", func(t *testing.T) {
		target := pg.createDatabase(t, "newer")
		if got := pg.restore(t, good, target); got.code != 0 {
			t.Fatalf("restore exit %d:\n%s", got.code, got.output)
		}
		pg.psql(t, target, fmt.Sprintf("UPDATE trustvian_schema_version SET version = %d", postgres.SchemaVersion+1))

		_, err := openStore(t, pg.dsnFor(target))
		if !errors.Is(err, postgres.ErrSchemaVersionMismatch) {
			t.Fatalf("CompileStorage error = %v, want ErrSchemaVersionMismatch", err)
		}
	})
}

func assertNothingWritten(t *testing.T, out string) {
	t.Helper()
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("output %s exists after a failed backup", out)
	}
	entries, _ := os.ReadDir(filepath.Dir(out))
	for _, e := range entries {
		t.Errorf("failed backup left %s behind", e.Name())
	}
}

// assertQuarantinedAndEmpty proves a failed restore target cannot be started
// against, and that the single transaction really committed nothing.
func assertQuarantinedAndEmpty(t *testing.T, pg *pgServer, db string) {
	t.Helper()
	if pg.allowsConnections(t, db) {
		t.Fatal("failed restore target still accepts connections")
	}
	if _, err := openStore(t, pg.dsnFor(db)); err == nil {
		t.Fatal("Trustvian started against a quarantined, failed restore target")
	}
	pg.psql(t, pg.maintenanceDB(), "ALTER DATABASE "+db+" WITH ALLOW_CONNECTIONS true")
	if n := pg.psql(t, db, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'"); n != "0" {
		t.Errorf("failed restore left %s relation(s) behind — it was not atomic", n)
	}
}

func copyBackup(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"trustvian.dump", "MANIFEST", "SHA256SUMS"} {
		data, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dst, f), data)
	}
	return dst
}

// TestUpgradeFromPreviousReleasePreservesLearnedState drives the documented
// upgrade and rollback procedures with a REAL previous release: the CLI
// binary named by TRUSTVIAN_TEST_UPGRADE_FROM_BINARY, which CI builds from
// the v0.8.0 tag. No fixture is fabricated — the "old" state is whatever that
// release actually writes.
func TestUpgradeFromPreviousReleasePreservesLearnedState(t *testing.T) {
	pg := requireBackupRestore(t)
	oldBin := os.Getenv(upgradeBinaryEnv)
	if oldBin == "" {
		t.Skipf("%s not set; skipping the upgrade test (see docs/operations.md)", upgradeBinaryEnv)
	}
	if _, err := os.Stat(oldBin); err != nil {
		t.Fatalf("%s: %v", upgradeBinaryEnv, err)
	}
	fromVersion := os.Getenv(upgradeFromEnv)

	work := t.TempDir()
	newBin := filepath.Join(work, "trustvian-current")
	if out, err := exec.Command("go", "build", "-o", newBin, "../cmd/trustvian").CombinedOutput(); err != nil {
		t.Fatalf("build current CLI: %v\n%s", err, out)
	}

	corpus := writeEvents(t, work, "corpus.json", recoveryCorpus(90))
	later := writeEvents(t, work, "later.json", []event.Event{
		recoveryEvent(recoveryActors[0], 90, recoveryEpoch.Add(90*time.Minute)),
		recoveryEvent(recoveryActors[0], 91, recoveryEpoch.Add(91*time.Minute)),
	})
	probes := writeEvents(t, work, "probes.json", probeEvents())

	// cli runs `<bin> analyze|baseline build --storage-config <db.yaml> <file>`.
	cli := func(bin, db string, args ...string) scriptResult {
		t.Helper()
		cfg := filepath.Join(work, db+".yaml")
		if _, err := os.Stat(cfg); err != nil {
			content := fmt.Sprintf("version: v1\ntype: postgres\npostgres:\n  dsn: %q\n", pg.dsnFor(db))
			mustWrite(t, cfg, []byte(content))
		}
		subcommand, file := args[:len(args)-1], args[len(args)-1]
		cmd := exec.Command(bin, append(subcommand, "--storage-config", cfg, file)...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		code := 0
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run %s: %v", bin, err)
			}
			code = exitErr.ExitCode()
		}
		return scriptResult{output: out.String(), code: code}
	}
	mustCLI := func(bin, db string, args ...string) string {
		t.Helper()
		got := cli(bin, db, args...)
		if got.code != 0 {
			t.Fatalf("%s %v on %s exit %d:\n%s", filepath.Base(bin), args, db, got.code, got.output)
		}
		return got.output
	}

	// 1. The previous release learns, and its analysis is recorded.
	prod := pg.createDatabase(t, "prod")
	mustCLI(oldBin, prod, "baseline", "build", corpus)
	preUpgrade := mustCLI(oldBin, prod, "analyze", probes)

	// 2. Pre-upgrade backup.
	var backupArgs []string
	if fromVersion != "" {
		backupArgs = []string{"--trustvian-version", fromVersion}
	}
	preUpgradeBackup := pg.backup(t, prod, backupArgs...)

	// 3. In-place upgrade: the current release starts on the old database.
	upgraded := mustOpenStore(t, pg.dsnFor(prod))
	if got := mustCLI(newBin, prod, "analyze", probes); got != preUpgrade {
		t.Errorf("current release analyzes the upgraded database differently:\n got:\n%s\nwant:\n%s", got, preUpgrade)
	}

	// Reference: what the current release learns from the same corpus.
	ref := pg.createDatabase(t, "ref")
	mustCLI(newBin, ref, "baseline", "build", corpus)
	refStore := mustOpenStore(t, pg.dsnFor(ref))
	for _, actor := range recoveryActors {
		want, _, _ := baselineJSON(t, refStore, actor)
		got, n, ok := baselineJSON(t, upgraded, actor)
		if !ok || n == 0 {
			t.Fatalf("%s: no learned state after upgrade", actor)
		}
		if got != want {
			t.Errorf("%s: state written by the previous release differs from what this release learns from the same corpus", actor)
		}
	}
	if cold := mustCLI(newBin, pg.createDatabase(t, "cold"), "analyze", probes); cold == preUpgrade {
		t.Fatal("upgraded analysis equals cold-start analysis — the comparison is vacuous")
	}

	// 4. Binary-only downgrade, now that the upgrade crosses a schema
	// version. Until task 051 both releases sat on schema version 1, so the
	// old binary could still read a database the new one had opened and this
	// step asserted exactly that. Learning scopes moved the schema to
	// version 2, and the old binary must now refuse it.
	//
	// That refusal is the feature, not a regression: `v0.8.0` cannot see the
	// `scope` column, so proceeding would mean reading a table whose row
	// identity it does not understand. Asserting it here is what keeps step 6
	// honest — restoring the pre-upgrade backup is the *only* rollback path
	// across this change, which is why that backup is mandatory rather than
	// advisory (see docs/operations.md § Upgrading into v1.0).
	if got := cli(oldBin, prod, "analyze", probes); got.code == 0 {
		t.Errorf("previous release accepted a database upgraded past its schema version:\n%s", got.output)
	} else if !strings.Contains(got.output, "schema version mismatch") {
		t.Errorf("previous release failed for the wrong reason: exit %d:\n%s", got.code, got.output)
	}

	// 5. The new release keeps learning on top of the old state.
	_, beforeCount, _ := baselineJSON(t, upgraded, recoveryActors[0])
	mustCLI(newBin, prod, "baseline", "build", later)
	_, afterCount, _ := baselineJSON(t, upgraded, recoveryActors[0])
	if afterCount != beforeCount+2 {
		t.Errorf("post-upgrade learning: count %d -> %d, want +2", beforeCount, afterCount)
	}

	// 6. Rollback: restore the pre-upgrade backup into a clean database and
	// run the previous release against it.
	rollback := pg.createDatabase(t, "rollback")
	if got := pg.restore(t, preUpgradeBackup, rollback); got.code != 0 {
		t.Fatalf("rollback restore exit %d:\n%s", got.code, got.output)
	}
	if got := mustCLI(oldBin, rollback, "analyze", probes); got != preUpgrade {
		t.Errorf("rollback does not reproduce pre-upgrade analysis:\n got:\n%s\nwant:\n%s", got, preUpgrade)
	}

	// 7. Across a schema-version change, neither release adopts state it does
	// not understand: binary-only downgrade is refused, which is why the
	// pre-upgrade backup is mandatory.
	future := pg.createDatabase(t, "future")
	if got := pg.restore(t, preUpgradeBackup, future); got.code != 0 {
		t.Fatalf("restore exit %d:\n%s", got.code, got.output)
	}
	pg.psql(t, future, fmt.Sprintf("UPDATE trustvian_schema_version SET version = %d", postgres.SchemaVersion+1))
	for _, bin := range []string{oldBin, newBin} {
		got := cli(bin, future, "analyze", probes)
		if got.code == 0 || !strings.Contains(got.output, "schema version mismatch") {
			t.Errorf("%s on a newer schema: exit %d, output:\n%s", filepath.Base(bin), got.code, got.output)
		}
	}
}

func writeEvents(t *testing.T, dir, name string, events []event.Event) string {
	t.Helper()
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	mustWrite(t, p, data)
	return p
}
