package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

// ingestServer is a minimal stand-in for the control plane's two ingest
// routes. It enforces the real contract — gap-free, strictly monotonic —
// because a test server that accepted anything would prove nothing about the
// cursor.
type ingestServer struct {
	*httptest.Server

	mu       sync.Mutex
	next     uint64
	status   string
	accepted []uint64
	requests atomic.Int64
}

// newIngestServer builds a stub whose run is already running — the common
// case every existing test in this file relies on. Use
// newIngestServerWithStatus directly for a test that needs a different one.
func newIngestServer(t *testing.T, startAt uint64) *ingestServer {
	t.Helper()
	return newIngestServerWithStatus(t, startAt, runStatusRunning)
}

func newIngestServerWithStatus(t *testing.T, startAt uint64, status string) *ingestServer {
	t.Helper()
	s := &ingestServer{next: startAt, status: status}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/progress"):
			s.mu.Lock()
			status := s.status
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "status": status,
			})
		case strings.HasSuffix(r.URL.Path, "/ingest-state"):
			s.mu.Lock()
			next := s.next
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "next_sequence": formatSequence(next),
			})
		case strings.HasSuffix(r.URL.Path, "/records"):
			var envelope struct {
				Sequence string `json:"sequence"`
			}
			_ = json.NewDecoder(r.Body).Decode(&envelope)
			seq, err := parseSequence(envelope.Sequence)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if seq != s.next {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1",
					"error": map[string]string{"code": "conflict",
						"message": fmt.Sprintf("sequence %d, expected %d", seq, s.next)},
				})
				return
			}
			s.accepted = append(s.accepted, seq)
			s.next = seq + 1
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "disposition": "applied",
				"next_sequence":     formatSequence(s.next),
				"record_count":      formatSequence(uint64(len(s.accepted))),
				"behavior_complete": true,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *ingestServer) acceptedSequences() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.accepted...)
}

func newTestSink(t *testing.T, apiURL string) *Sink {
	t.Helper()
	sink, err := New(apiURL, "run-1", "support-reference")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := sink.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return sink
}

func TestInitializeSeedsCursorFromServer(t *testing.T) {
	server := newIngestServer(t, 5)
	sink := newTestSink(t, server.URL)

	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{EventID: "e"}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	got := server.acceptedSequences()
	if len(got) != 1 || got[0] != 5 {
		t.Errorf("accepted = %v, want [5] — the cursor must resume from the server", got)
	}
}

// TestInitializeFailsWhenRunIsNotRunning covers Important finding I1:
// IngestDecisionRecord refuses every record for a run that is not
// RunRunning, so Initialize must refuse startup rather than come up clean
// and fail on the first span.
func TestInitializeFailsWhenRunIsNotRunning(t *testing.T) {
	server := newIngestServerWithStatus(t, 1, "pending")
	sink, err := New(server.URL, "run-1", "support-reference")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = sink.Initialize(context.Background())
	if err == nil {
		t.Fatal("Initialize() error = nil, want a refusal for a run that is not running")
	}
	if !strings.Contains(err.Error(), "pending") {
		t.Errorf("error = %q, want it to name the actual status %q", err.Error(), "pending")
	}
}

func TestRecordBeforeInitializeFails(t *testing.T) {
	server := newIngestServer(t, 1)
	sink, err := New(server.URL, "run-1", "support-reference")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{}); err == nil {
		t.Fatal("Record() error = nil, want a refusal before Initialize")
	}
}

func TestRecordAdvancesGapFree(t *testing.T) {
	server := newIngestServer(t, 1)
	sink := newTestSink(t, server.URL)

	for range 5 {
		if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{EventID: "e"}); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}
	want := []uint64{1, 2, 3, 4, 5}
	got := server.acceptedSequences()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("accepted = %v, want %v", got, want)
	}
}

// TestRecordConcurrentIsGapFree is the concurrency guarantee. The Collector
// may call ConsumeTraces from many goroutines; the ingest contract is
// strictly sequential, so two goroutines holding 5 and 6 would have the
// server reject 6 as a gap if it arrived first.
func TestRecordConcurrentIsGapFree(t *testing.T) {
	server := newIngestServer(t, 1)
	sink := newTestSink(t, server.URL)

	const goroutines, each = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*each)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if _, err := sink.Record(context.Background(),
					trustvian.DecisionRecord{EventID: "e"}); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Record() error = %v", err)
	}

	got := server.acceptedSequences()
	if len(got) != goroutines*each {
		t.Fatalf("accepted %d records, want %d", len(got), goroutines*each)
	}
	for i, seq := range got {
		if seq != uint64(i+1) {
			t.Fatalf("accepted[%d] = %d, want %d — the sequence has a gap", i, seq, i+1)
		}
	}
}

func TestRecordReportsReplayed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/progress") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "status": runStatusRunning})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ingest-state") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "next_sequence": "1"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "disposition": "replayed", "next_sequence": "2",
			"record_count": "1", "behavior_complete": true})
	}))
	defer server.Close()

	sink := newTestSink(t, server.URL)
	got, err := sink.Record(context.Background(), trustvian.DecisionRecord{})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if got != dispositionReplayed {
		t.Errorf("Record() = %q, want %q", got, dispositionReplayed)
	}
	// Criterion 4 covers both dispositions advancing the cursor to the
	// server's number, not just applied.
	if got := sink.nextSequence(); got != 2 {
		t.Errorf("cursor = %d after a replayed record, want 2", got)
	}
}

// TestRecordRejectsUnknownDisposition covers Review Focus item 3.
func TestRecordRejectsUnknownDisposition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/progress") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "status": runStatusRunning})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ingest-state") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "next_sequence": "1"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "disposition": "queued", "next_sequence": "2",
			"record_count": "1", "behavior_complete": true})
	}))
	defer server.Close()

	sink := newTestSink(t, server.URL)
	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{}); err == nil {
		t.Fatal("Record() error = nil, want an unrecognized disposition to fail closed")
	}
}

// TestRecordRejectsNonAdvancingSequence covers Review Focus item 2.
func TestRecordRejectsNonAdvancingSequence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/progress") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "status": runStatusRunning})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ingest-state") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "next_sequence": "4"})
			return
		}
		// Claims success but does not advance past the sequence just sent.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "disposition": "applied", "next_sequence": "4",
			"record_count": "1", "behavior_complete": true})
	}))
	defer server.Close()

	sink := newTestSink(t, server.URL)
	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{}); err == nil {
		t.Fatal("Record() error = nil, want a non-advancing cursor to fail closed")
	}
}

func TestRecordDoesNotAdvanceOnFailure(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/progress") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "status": runStatusRunning})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ingest-state") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "next_sequence": "1"})
			return
		}
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var envelope struct {
			Sequence string `json:"sequence"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		seq, _ := parseSequence(envelope.Sequence)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "disposition": "applied",
			"next_sequence": formatSequence(seq + 1),
			"record_count":  "1", "behavior_complete": true})
	}))
	defer server.Close()

	sink := newTestSink(t, server.URL)
	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	fail.Store(true)
	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{}); err == nil {
		t.Fatal("Record() error = nil, want the server failure surfaced")
	}

	// The cursor must still be at 2: a failed post consumes no sequence, or
	// the next successful one would leave a gap the run never recovers from.
	if got := sink.nextSequence(); got != 2 {
		t.Fatalf("cursor = %d after a failed post, want 2", got)
	}

	// And the next successful record must therefore be sequence 2.
	fail.Store(false)
	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if got := sink.nextSequence(); got != 3 {
		t.Errorf("cursor = %d, want 3 — the retry must reuse sequence 2", got)
	}
}
