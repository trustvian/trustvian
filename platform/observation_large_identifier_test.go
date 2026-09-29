package platform

// Correlation identifiers longer than a PostgreSQL B-tree key.
//
// trace_id and session_id are validated nowhere on the ingest path, and
// fingerprint_id's 256-byte bound is skipped once a run saturates — so the
// platform already accepts records carrying values of any length the 256 KiB
// request body allows. Indexing those values directly would have let such a
// record fail on insert and roll back its whole ingest, on PostgreSQL only,
// because a B-tree entry over roughly 2704 bytes is refused outright.
//
// The fix indexes a fixed-width digest and stores the value whole. These tests
// are what keeps that true: they were verified against an index built on the raw
// columns, where the PostgreSQL half fails with
// "index row size ... exceeds btree version 4 maximum" and SQLite passes —
// the two backends disagreeing about which records are ingestable.

import (
	"strings"
	"testing"
)

// postgresBTreeKeyLimit is the entry size PostgreSQL refuses beyond, near
// enough: a third of an 8 KiB page, less tuple overhead.
//
// Named rather than inlined so the sizes below are visibly chosen to exceed a
// real limit instead of being arbitrarily large.
const postgresBTreeKeyLimit = 2704

// incompressibleIdentifier builds a deterministic, poorly compressible string.
//
// Deterministic because a test that generates different data each run reports a
// different thing each run. Poorly compressible because the failure being
// guarded against is a *stored* size limit, and PostgreSQL compresses a long
// value before storing it — `strings.Repeat("a", 4096)` compresses to almost
// nothing and would sail under the limit it is supposed to exceed, so the test
// would pass without testing anything.
//
// A 64-bit xorshift over a 64-character alphabet: no dependency, no global
// source, identical on every platform and every run.
func incompressibleIdentifier(seed uint64, length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

	state := seed | 1 // xorshift is degenerate at zero
	out := make([]byte, length)
	for i := range out {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		out[i] = alphabet[state&0x3f]
	}
	return string(out)
}

// The generator has to actually defeat compression, or every test built on it is
// quietly vacuous.
func TestIncompressibleIdentifierIsNotTrivialData(t *testing.T) {
	const length = 4096
	value := incompressibleIdentifier(0x9E3779B97F4A7C15, length)

	if len(value) != length {
		t.Fatalf("length = %d, want %d", len(value), length)
	}
	if len(value) <= postgresBTreeKeyLimit {
		t.Fatalf("value is %d bytes, which does not exceed the %d-byte B-tree "+
			"limit it exists to exceed", len(value), postgresBTreeKeyLimit)
	}

	// Deterministic across calls.
	if value != incompressibleIdentifier(0x9E3779B97F4A7C15, length) {
		t.Error("the generator is not deterministic")
	}
	// Distinct across seeds, so trace and session cannot collide by accident.
	if value == incompressibleIdentifier(0x517CC1B727220A95, length) {
		t.Error("two seeds produced the same value")
	}

	// A crude compressibility proxy: distinct byte values and no long runs.
	// strings.Repeat would score 1 distinct byte; this should use most of the
	// alphabet.
	seen := map[byte]bool{}
	for i := range len(value) {
		seen[value[i]] = true
	}
	if len(seen) < 32 {
		t.Errorf("value uses only %d distinct bytes; it would compress below the "+
			"limit this test needs it to exceed", len(seen))
	}
	if strings.Contains(value, strings.Repeat(string(value[0]), 8)) {
		t.Error("value contains an 8-byte run of one character")
	}
}

// The full ingest path accepts a record whose trace and session identifiers are
// larger than a raw B-tree key, retains it, and returns both values exactly.
//
// Through IngestDecisionRecord rather than the store, because the contract being
// preserved is *which records the platform accepts* — a store-level test would
// not notice the ingest rolling back.
func TestLargeCorrelationIdentifiersSurviveTheIngestPath(t *testing.T) {
	store, _, plane, run := observationPlane(t)

	trace := incompressibleIdentifier(0x243F6A8885A308D3, 4096)
	session := incompressibleIdentifier(0x13198A2E03707344, 4096)

	record := observationTestRecord(run, "e1")
	record.TraceID = trace
	record.SessionID = session

	ingestOne(t, plane, run, 1, record)

	page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != 1 {
		t.Fatalf("retained %d observations, want 1", len(page.Observations))
	}

	got := page.Observations[0]
	if got.TraceID != trace {
		t.Errorf("trace id was not preserved: %d bytes back, %d in",
			len(got.TraceID), len(trace))
	}
	if got.SessionID != session {
		t.Errorf("session id was not preserved: %d bytes back, %d in",
			len(got.SessionID), len(session))
	}
	if page.History.State() != ObservationHistoryComplete {
		t.Errorf("history state = %v, want complete", page.History.State())
	}

	// The retry contract is unaffected by the size of what was stored.
	replay, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
		RunID:             run.ID(),
		Sequence:          1,
		BehavioralProfile: run.BehavioralProfile(),
		Record:            record,
	})
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if replay.Disposition != IngestReplayed {
		t.Errorf("retry Disposition = %v, want replayed", replay.Disposition)
	}
	if seq := retainedSequences(t, store, run.ID()); len(seq) != 1 {
		t.Errorf("retained sequences = %v after a retry, want one row", seq)
	}
}

// A digest narrows; it does not identify. Any lookup through one of the key
// indexes must compare the original value too.
//
// Demonstrated without manufacturing a SHA-256 collision, which is not
// available: a row is written whose stored key is another value's digest, which
// is indistinguishable from a collision to any query. The key-only predicate
// matches it and the key-and-value predicate does not — which is the whole rule,
// and the reason it is stated on observationCorrelationColumns rather than left
// for task 085 to infer.
func TestObservationDigestLookupRequiresTheOriginalValue(t *testing.T) {
	store, _, plane, run := observationPlane(t)
	ctx := t.Context()

	wanted := "trace-the-caller-is-looking-for"
	other := "trace-belonging-to-a-different-observation"

	record := observationTestRecord(run, "e1")
	record.TraceID = other
	ingestOne(t, plane, run, 1, record)

	// Stand in for a collision: give the stored row the key of the trace a
	// caller will search for, while its value stays the other one.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE `+tableObservations+` SET trace_key = ? WHERE run_id = ?`,
		observationDigestKey(wanted), string(run.ID())); err != nil {
		t.Fatalf("plant the colliding key: %v", err)
	}

	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+tableObservations+` WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", where, err)
		}
		return n
	}

	if got := count(`run_id = ? AND trace_key = ?`,
		string(run.ID()), observationDigestKey(wanted)); got != 1 {
		t.Fatalf("the planted row is not reachable by key alone (%d rows); the rest "+
			"of this test would prove nothing", got)
	}
	if got := count(`run_id = ? AND trace_key = ? AND trace_id = ?`,
		string(run.ID()), observationDigestKey(wanted), wanted); got != 0 {
		t.Errorf("a key-and-value lookup returned %d rows for a trace no observation "+
			"carries; the value predicate is what makes a digest index safe", got)
	}

	// And the page read refuses the row outright, because a stored key that
	// disagrees with the value beside it was not written by this code.
	if _, err := store.RunObservations(ctx, run.ID(), 0, MaxListPage); err == nil {
		t.Error("a row whose index key does not match its value was returned as " +
			"though it were sound")
	}
}

// The indexes must key on the digest columns, and never on the values.
//
// The conformance suite catches a regression here functionally, but only when a
// PostgreSQL DSN is configured — and the failure it produces is a size error
// three layers down. This is the same defect stated where it is introduced, with
// no database required.
func TestObservationIndexesKeyOnDigestsNotValues(t *testing.T) {
	store, _, _, _ := observationPlane(t)

	definitions := map[string]string{}
	rows, err := store.db.QueryContext(t.Context(),
		`SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ?`,
		tableObservations)
	if err != nil {
		t.Fatalf("read index definitions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var sql *string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		if sql != nil {
			definitions[name] = *sql
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read index definitions: %v", err)
	}

	for _, index := range []struct {
		name, key, value string
	}{
		{indexObservationsByFingerprint, "fingerprint_key", "fingerprint_id"},
		{indexObservationsByTrace, "trace_key", "trace_id"},
		{indexObservationsBySession, "session_key", "session_id"},
	} {
		definition, ok := definitions[index.name]
		if !ok {
			t.Errorf("%s does not exist", index.name)
			continue
		}
		if !strings.Contains(definition, index.key) {
			t.Errorf("%s does not key on %s: %s", index.name, index.key, definition)
		}
		// The raw column must not be in the key. PostgreSQL refuses a B-tree
		// entry over ~2704 bytes at INSERT time, and none of these values has a
		// length bound the ingest path enforces — so indexing one turns an
		// already-accepted record into a failed ingest, on one backend only.
		if strings.Contains(definition, index.value) {
			t.Errorf("%s indexes the unbounded value %s rather than its digest; a "+
				"record the platform accepts would fail to persist on PostgreSQL: %s",
				index.name, index.value, definition)
		}
	}

	// And parent_span_id stays unindexed — task 084's rule, unchanged by adding
	// key columns.
	for name, definition := range definitions {
		if strings.Contains(definition, "parent_span_id") {
			t.Errorf("%s indexes parent_span_id, which is trace-scoped and must not "+
				"be a lookup key: %s", name, definition)
		}
	}
}
