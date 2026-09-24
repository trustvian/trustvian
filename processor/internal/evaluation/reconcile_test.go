package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

// replayServer implements the ingest contract the control plane actually
// offers, including the part the recovery design depends on: a record
// re-presented at the sequence it already occupies replays when it is
// provably the same record, and conflicts when it is not.
//
// It is deliberately closer to platform.IngestDecisionRecord than the
// simpler stub in sink_test.go. A server that replayed anything would make
// every test here pass without proving that the *same* record came back.
type replayServer struct {
	*httptest.Server

	mu sync.Mutex

	// next is the sequence the server expects, and committed[i] is the
	// digest durably stored at sequence i+1 — the evidence a test inspects
	// to see whether a record was doubled or substituted.
	next      uint64
	committed []string

	// dropResponse names sequences whose response is destroyed after the
	// commit, once each unless dropAlways is set. This is the failure the
	// whole task is about: the server acted, the client never heard.
	dropResponse map[uint64]bool
	dropAlways   bool
	dropped      map[uint64]int

	// rejectFirst makes the first POST a definitive refusal — a well-formed
	// 4xx envelope, which is the control plane declining a record rather
	// than failing to report on one.
	rejectFirst bool

	posts int
}

func newReplayServer(t *testing.T) *replayServer {
	t.Helper()
	s := &replayServer{
		next:         1,
		dropResponse: map[uint64]bool{},
		dropped:      map[uint64]int{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *replayServer) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/progress"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "run_id": "run-1", "status": runStatusRunning})
		return
	case strings.HasSuffix(r.URL.Path, "/ingest-state"):
		s.mu.Lock()
		next := s.next
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "run_id": "run-1", "next_sequence": formatSequence(next)})
		return
	case !strings.HasSuffix(r.URL.Path, "/records"):
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var envelope struct {
		Sequence string          `json:"sequence"`
		Record   json.RawMessage `json:"record"`
	}
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sequence, err := parseSequence(envelope.Sequence)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// The encoded record stands in for the server's content digest: two
	// requests match here exactly when they carry the same record.
	digest := string(envelope.Record)

	s.mu.Lock()
	s.posts++
	first := s.posts == 1
	reject := s.rejectFirst && first

	switch {
	case reject:
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1",
			"error": map[string]string{
				"code": "invalid_record", "message": "the record was declined"},
		})
		return

	case sequence == s.next:
		s.committed = append(s.committed, digest)
		s.next++
		next := s.next
		count := uint64(len(s.committed))
		drop := s.shouldDropLocked(sequence)
		s.mu.Unlock()
		if drop {
			// Committed, then the reply is destroyed: the client cannot tell
			// this apart from a record that never arrived.
			hijackAndClose(w)
			return
		}
		writeDisposition(w, dispositionApplied, next, count)
		return

	case sequence+1 == s.next && s.committed[sequence-1] == digest:
		next := s.next
		count := uint64(len(s.committed))
		drop := s.shouldDropLocked(sequence)
		s.mu.Unlock()
		if drop {
			hijackAndClose(w)
			return
		}
		// The replay contract: the same record at a sequence it already
		// occupies changes nothing and reports the cursor as it stands.
		writeDisposition(w, dispositionReplayed, next, count)
		return

	default:
		expected := s.next
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1",
			"error": map[string]string{
				"code": "ingest_sequence",
				"message": fmt.Sprintf(
					"sequence %d was already accepted with different content, or leaves a gap; expected %d",
					sequence, expected)},
		})
		return
	}
}

// shouldDropLocked reports whether this response is destroyed, and is called
// with s.mu held.
func (s *replayServer) shouldDropLocked(sequence uint64) bool {
	if s.dropAlways {
		return true
	}
	if !s.dropResponse[sequence] {
		return false
	}
	if s.dropped[sequence] > 0 {
		return false
	}
	s.dropped[sequence]++
	return true
}

// hijackAndClose destroys the connection without writing a response, which
// is what a client sees as an EOF after its request was already delivered.
func hijackAndClose(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		panic("test server response writer does not support hijacking")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		panic(err)
	}
	_ = conn.Close()
}

func writeDisposition(w http.ResponseWriter, disposition string, next, count uint64) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": "1", "disposition": disposition,
		"next_sequence":     formatSequence(next),
		"record_count":      formatSequence(count),
		"behavior_complete": true,
	})
}

func (s *replayServer) durable() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.committed...)
}

func (s *replayServer) postCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts
}

// recordDigest renders a record the way replayServer sees it, so a test can
// say which record occupies a sequence.
func recordDigest(t *testing.T, record trustvian.DecisionRecord) string {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshalling record: %v", err)
	}
	return string(encoded)
}

// TestAmbiguousResponseLossReconcilesTheSameRecord is the regression test for
// the blocker this recovery mechanism exists for.
//
// The server commits sequence 1 and its response is destroyed. The client
// cannot tell that from a record that never arrived — and the wrong answer,
// treating it as "not applied", hands sequence 1 to the next record and
// deadlocks the run on a conflict the operator cannot undo.
//
// The right answer is the server's own replay contract: re-present the same
// record at the same sequence, let the digest prove it is the same one, and
// take the cursor from the reply.
func TestAmbiguousResponseLossReconcilesTheSameRecord(t *testing.T) {
	server := newReplayServer(t)
	server.dropResponse[1] = true

	sink := newTestSink(t, server.URL)
	if got := sink.nextSequence(); got != 1 {
		t.Fatalf("cursor = %d before any record, want 1", got)
	}

	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	// Record A: committed at sequence 1, response lost, reconciled inside
	// this same call. The caller sees success, because the record is there.
	disposition, err := sink.Record(context.Background(), recordA, learningFor("A"))
	if err != nil {
		t.Fatalf("Record(A) error = %v; a lost response must be reconciled, not surfaced", err)
	}
	if disposition != dispositionReplayed {
		t.Errorf("Record(A) = %q, want %q — the server recognized the same digest",
			disposition, dispositionReplayed)
	}
	if got := sink.nextSequence(); got != 2 {
		t.Errorf("cursor = %d after reconciliation, want 2 — the server's next_sequence", got)
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("sequence %d is still held after a successful reconciliation, want none", got)
	}

	// Record B takes sequence 2, not the sequence A is holding.
	disposition, err = sink.Record(context.Background(), recordB, learningFor("B"))
	if err != nil {
		t.Fatalf("Record(B) error = %v; sequence 2 must be free and uncontested", err)
	}
	if disposition != dispositionApplied {
		t.Errorf("Record(B) = %q, want %q", disposition, dispositionApplied)
	}
	if got := sink.nextSequence(); got != 3 {
		t.Errorf("cursor = %d, want 3", got)
	}

	// The evidence: two records, in order, neither doubled, and — the part
	// that proves no substitution — A still occupies the sequence it claimed.
	durable := server.durable()
	want := []string{recordDigest(t, recordA), recordDigest(t, recordB)}
	if len(durable) != len(want) {
		t.Fatalf("control plane holds %d records, want %d; a lost response must not duplicate evidence",
			len(durable), len(want))
	}
	for i := range want {
		if durable[i] != want[i] {
			t.Errorf("sequence %d holds %s, want %s — a record was substituted into a sequence it did not claim",
				i+1, durable[i], want[i])
		}
	}

	// Three POSTs: A, A again, B. The reconciliation is one extra attempt,
	// not a retry loop.
	if got := server.postCount(); got != 3 {
		t.Errorf("server saw %d POSTs, want 3 (A, A reconciled, B)", got)
	}
}

// TestUnresolvedRecordHoldsItsSequenceAcrossCalls covers the case the
// in-call reconciliation cannot finish: the control plane is unreachable for
// both attempts. The sequence must stay bound to that record — and must
// still be reconciled, not skipped, when the control plane comes back.
func TestUnresolvedRecordHoldsItsSequenceAcrossCalls(t *testing.T) {
	server := newReplayServer(t)
	server.dropAlways = true

	sink := newTestSink(t, server.URL)
	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	_, err := sink.Record(context.Background(), recordA, learningFor("A"))
	if err == nil {
		t.Fatal("Record(A) error = nil, want an unresolved outcome when nothing can be confirmed")
	}
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("error = %v, want it to report ErrUnresolved so a caller can tell it apart from a refusal", err)
	}
	if got := sink.nextSequence(); got != 1 {
		t.Errorf("cursor = %d after an unresolved record, want 1 — nothing may advance it but the server", got)
	}
	if got := sink.pendingSequence(); got != 1 {
		t.Errorf("held sequence = %d, want 1", got)
	}

	// The control plane comes back. Record B must not take sequence 1: A is
	// holding it, and A is reconciled first.
	server.mu.Lock()
	server.dropAlways = false
	server.mu.Unlock()

	if _, err := sink.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v", err)
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("sequence %d is still held, want none once it reconciled", got)
	}
	if got := sink.nextSequence(); got != 3 {
		t.Errorf("cursor = %d, want 3 — A at 1, B at 2", got)
	}

	durable := server.durable()
	want := []string{recordDigest(t, recordA), recordDigest(t, recordB)}
	if fmt.Sprint(durable) != fmt.Sprint(want) {
		t.Errorf("evidence = %v, want %v — A must keep sequence 1 and B must take 2", durable, want)
	}
}

// TestDefinitiveRefusalFreesTheSequence is the other half of the
// classification. A record the control plane declined is not there, so its
// sequence was never consumed and the next record takes it — holding it
// would strand the run behind a record the server will never accept.
func TestDefinitiveRefusalFreesTheSequence(t *testing.T) {
	server := newReplayServer(t)
	server.rejectFirst = true

	sink := newTestSink(t, server.URL)
	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	_, err := sink.Record(context.Background(), recordA, learningFor("A"))
	if err == nil {
		t.Fatal("Record(A) error = nil, want the refusal surfaced")
	}
	if errors.Is(err, ErrUnresolved) {
		t.Errorf("error = %v, want a definitive refusal, not an unresolved outcome", err)
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("sequence %d is held after a refusal, want none — the record was never applied", got)
	}
	if got := sink.nextSequence(); got != 1 {
		t.Errorf("cursor = %d, want 1", got)
	}
	// One POST, not two: a declined record is not re-presented.
	if got := server.postCount(); got != 1 {
		t.Errorf("server saw %d POSTs, want 1 — a definitive refusal must not be retried", got)
	}

	if _, err := sink.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v; sequence 1 must be free", err)
	}
	durable := server.durable()
	if len(durable) != 1 || durable[0] != recordDigest(t, recordB) {
		t.Errorf("evidence = %v, want only B at sequence 1", durable)
	}
}

// TestReconciliationIsGapFreeUnderConcurrency runs the recovery path under
// the concurrency the Collector actually has: many goroutines calling
// Record, with one response destroyed mid-run. Every record must land
// exactly once, in one unbroken sequence, with no record taking another's
// position. Run under -race, this is also the check that the pending state
// is only ever touched under the sink's own lock.
func TestReconciliationIsGapFreeUnderConcurrency(t *testing.T) {
	server := newReplayServer(t)
	for _, sequence := range []uint64{3, 7, 11} {
		server.dropResponse[sequence] = true
	}

	sink := newTestSink(t, server.URL)

	const goroutines, each = 8, 5
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*each)
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range each {
				record := trustvian.DecisionRecord{EventID: fmt.Sprintf("e-%d-%d", g, i)}
				if _, err := sink.Record(context.Background(), record,
					learningFor(record.EventID)); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Record() error = %v", err)
	}

	durable := server.durable()
	if len(durable) != goroutines*each {
		t.Fatalf("control plane holds %d records, want %d", len(durable), goroutines*each)
	}
	seen := make(map[string]int, len(durable))
	for _, digest := range durable {
		seen[digest]++
	}
	for digest, count := range seen {
		if count != 1 {
			t.Errorf("record %s appears %d times; reconciliation must not duplicate evidence", digest, count)
		}
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("sequence %d is still held after every call returned successfully", got)
	}
	if got, want := sink.nextSequence(), uint64(goroutines*each+1); got != want {
		t.Errorf("cursor = %d, want %d", got, want)
	}
}

// TestCanceledContextLeavesTheRecordPendingAndUnreused covers the shutdown
// and deadline path. A cancelled context cannot prove the request was never
// delivered — the cancellation may have raced a response — so the record
// keeps its sequence rather than being dropped, and no later record takes
// that sequence. Nothing is retried on a detached context: the reconciliation
// happens on the next call's, or not at all.
func TestCanceledContextLeavesTheRecordPendingAndUnreused(t *testing.T) {
	server := newReplayServer(t)
	sink := newTestSink(t, server.URL)

	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := sink.Record(ctx, recordA, learningFor("A"))
	if err == nil {
		t.Fatal("Record(A) error = nil, want the cancellation surfaced")
	}
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("error = %v, want ErrUnresolved; a cancellation does not prove the record is absent", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation preserved in the chain", err)
	}
	if got := sink.pendingSequence(); got != 1 {
		t.Errorf("held sequence = %d, want 1", got)
	}
	if got := server.durable(); len(got) != 0 {
		t.Fatalf("control plane holds %v, want nothing — the request was cancelled before it was sent", got)
	}

	// The next call reconciles A on its own context and only then sends B.
	if _, err := sink.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v", err)
	}
	durable := server.durable()
	want := []string{recordDigest(t, recordA), recordDigest(t, recordB)}
	if fmt.Sprint(durable) != fmt.Sprint(want) {
		t.Errorf("evidence = %v, want %v — a cancelled record is held, not dropped and not replaced",
			durable, want)
	}
}
