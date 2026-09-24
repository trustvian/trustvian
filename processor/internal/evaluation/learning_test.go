package evaluation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

// What happens when the *local* half fails.
//
// The control plane accepting a record and the Engine learning from it are
// two durable writes in two systems, and the second one can fail after the
// first succeeded — a FileStore that updated its in-memory baseline and then
// could not flush it, a database error on either side of a commit. From
// here, "the learning failed" and "the learning may have happened" are the
// same observation.
//
// So the entry survives. Deleting it would leave the run holding a record
// with nothing anywhere to say its learning was never established, which is
// precisely the divergence the pending state exists to prevent — and it is
// the state the previous implementation reached, because the processor
// logged Observe failures and returned nil.

// failingLearner fails every call after the first n succeed.
type failingLearner struct {
	recorder learnRecorder
	after    int
	err      error
}

func (f *failingLearner) learn(ctx context.Context, learning []byte) error {
	if len(f.recorder.calls()) >= f.after {
		return f.err
	}
	return f.recorder.learn(ctx, learning)
}

// TestLearningFailureKeepsTheConfirmedEntry is the blocker in one test: a
// store failure must not end with the record in the run and no note that its
// learning was never confirmed.
func TestLearningFailureKeepsTheConfirmedEntry(t *testing.T) {
	server := newReplayServer(t)
	path := filepath.Join(t.TempDir(), "pending.json")
	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	learner := &failingLearner{err: errors.New("store unavailable")}
	sink, err := New(server.URL, "run-1", "support-reference", path, learner.learn)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := sink.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	_, err = sink.Record(context.Background(), recordA, learningFor("A"))
	if err == nil {
		t.Fatal("Record() error = nil, want the learning failure surfaced")
	}
	if !errors.Is(err, ErrLearningIndeterminate) {
		t.Errorf("error = %v, want ErrLearningIndeterminate: the record is in the run and its "+
			"learning is not established", err)
	}
	if !strings.Contains(err.Error(), "store unavailable") {
		t.Errorf("error = %q, want the store's own failure preserved", err.Error())
	}

	// The control plane's side happened and is untouched by the local
	// failure: exactly one record, cursor moved.
	if got := server.durable(); len(got) != 1 || got[0] != recordDigest(t, recordA) {
		t.Fatalf("evidence = %v, want exactly A", got)
	}

	// And the note that says so is still there, carrying everything a
	// restart needs to reason about it.
	entry := loadEntry(t, path)
	if entry.State != stateConfirmed {
		t.Errorf("pending state = %q, want %q — the run holds the record", entry.State, stateConfirmed)
	}
	if entry.Sequence != "1" || entry.RunID != "run-1" {
		t.Errorf("entry = run %s sequence %s, want run-1 sequence 1", entry.RunID, entry.Sequence)
	}
	if entry.Record.EventID != "A" || string(entry.Learning) != string(learningFor("A")) {
		t.Errorf("entry carries record %q / learning %s, want A and its own learning",
			entry.Record.EventID, entry.Learning)
	}

	// No later record may step over it. Re-learning could double an
	// observation and abandoning it could lose one; neither is this span's
	// decision to make.
	postsBefore := server.postCount()
	_, err = sink.Record(context.Background(), recordB, learningFor("B"))
	if err == nil {
		t.Fatal("Record(B) error = nil, want the sink to refuse while the record is unsettled")
	}
	if !errors.Is(err, ErrLearningIndeterminate) {
		t.Errorf("error = %v, want ErrLearningIndeterminate", err)
	}
	if got := server.postCount(); got != postsBefore {
		t.Errorf("server saw %d further POSTs, want 0 — nothing may be sent past an unsettled record",
			got-postsBefore)
	}
	if got := loadEntry(t, path); got.Record.EventID != "A" {
		t.Errorf("the pending entry now holds %q, want A — a second record must not take its place",
			got.Record.EventID)
	}

	// Restarting is what settles it: the record is in the run, its learning
	// is indeterminate, and it is not applied again.
	restarted := &learnRecorder{}
	second, err := New(server.URL, "run-1", "support-reference", path, restarted.learn)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	recovery, err := second.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if recovery.Outcome != RecoveryIndeterminate || recovery.Sequence != 1 {
		t.Errorf("recovery = %+v, want sequence 1 reported indeterminate", recovery)
	}
	if got := restarted.calls(); len(got) != 0 {
		t.Errorf("learning applied = %v on recovery, want none — applying it twice cannot be ruled out", got)
	}
	if got := len(server.durable()); got != 1 {
		t.Errorf("evidence holds %d records after recovery, want 1", got)
	}
	if got := second.nextSequence(); got != 2 {
		t.Errorf("cursor = %d, want 2 — the server's own number", got)
	}

	// The run continues from there.
	if _, err := second.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v after recovery", err)
	}
	if got := len(server.durable()); got != 2 {
		t.Errorf("evidence holds %d records, want 2", got)
	}
	if got := restarted.calls(); len(got) != 1 || got[0] != string(learningFor("B")) {
		t.Errorf("learning applied = %v, want only B's — A's was never established and is not invented", got)
	}
}

// TestConfirmedRecoveryRequiresTheVeryNextSequence is blocker 2. A process
// that died holding sequence N cannot have produced N+1, so a run expecting
// anything else was written by something this Collector cannot account for.
func TestConfirmedRecoveryRequiresTheVeryNextSequence(t *testing.T) {
	tests := []struct {
		name       string
		serverNext uint64
		wantErr    bool
	}{
		{name: "the very next sequence", serverNext: 6},
		{name: "two records past ours", serverNext: 7, wantErr: true},
		{name: "many records past ours", serverNext: 12, wantErr: true},
		{name: "still expecting ours", serverNext: 5, wantErr: true},
		{name: "behind ours", serverNext: 3, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newReplayServer(t)
			server.next = tt.serverNext
			for range tt.serverNext - 1 {
				server.committed = append(server.committed, "written-by-someone")
			}
			path := filepath.Join(t.TempDir(), "pending.json")

			j, _ := newJournal(path)
			if err := j.write(pendingEntry{
				Version: pendingVersion, RunID: "run-1", Sequence: "5", State: stateConfirmed,
				Record:   trustvian.DecisionRecord{EventID: "A"},
				Learning: learningFor("A"),
			}); err != nil {
				t.Fatalf("write() error = %v", err)
			}

			learner := &learnRecorder{}
			sink, err := New(server.URL, "run-1", "support-reference", path, learner.learn)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			postsBefore := server.postCount()
			recovery, err := sink.Initialize(context.Background())

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Initialize() error = %v, want the indeterminate recovery", err)
				}
				if recovery.Outcome != RecoveryIndeterminate || recovery.Sequence != 5 {
					t.Errorf("recovery = %+v, want sequence 5 indeterminate", recovery)
				}
				if got := sink.nextSequence(); got != 6 {
					t.Errorf("cursor = %d, want 6", got)
				}
				if pendingExists(t, path) {
					t.Error("the entry was not released after a settled recovery")
				}
				return
			}

			if err == nil {
				t.Fatal("Initialize() error = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), "5") || !strings.Contains(err.Error(), "6") {
				t.Errorf("error = %q, want it to name the sequence and the one the run should expect",
					err.Error())
			}
			if got := learner.calls(); len(got) != 0 {
				t.Errorf("learning applied = %v, want none", got)
			}
			if got := server.postCount(); got != postsBefore {
				t.Errorf("server saw %d POSTs, want none", got-postsBefore)
			}
			if !pendingExists(t, path) {
				t.Error("the entry was discarded; it is the only record that it existed")
			}
			// An uninitialized sink accepts nothing, so a refused startup
			// cannot be ignored into a running Collector.
			if _, err := sink.Record(context.Background(),
				trustvian.DecisionRecord{EventID: "B"}, learningFor("B")); err == nil {
				t.Error("Record() error = nil after a refused Initialize, want the sink unusable")
			}
		})
	}
}

// TestUnsettledSequenceIsNotStrandedByTheCursor covers the invariant that
// attempt() advances the cursor before settle() finishes: if settling fails,
// the in-memory cursor is already past the record while the record is still
// unsettled at its own sequence.
//
// The next call must resume that record where it is, not carry on from the
// cursor — otherwise sequence N is silently skipped and the run holds a
// record nothing learned from.
func TestUnsettledSequenceIsNotStrandedByTheCursor(t *testing.T) {
	server := newReplayServer(t)
	path := filepath.Join(t.TempDir(), "pending.json")
	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	sink, learner := newTestSinkWith(t, server.URL, path)

	// Fail the directory sync of the *confirmed* write only: the record is
	// accepted, the cursor moves to 2, and settling cannot complete.
	recorder := &syncRecorder{failN: 2}
	sink.journal.syncDir = recorder.sync

	_, err := sink.Record(context.Background(), recordA, learningFor("A"))
	if err == nil {
		t.Fatal("Record(A) error = nil, want the unprovable pending state surfaced")
	}
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("error = %v, want ErrUnresolved — the record's delivery is confirmed but its "+
			"pending state is not", err)
	}
	if got := sink.nextSequence(); got != 2 {
		t.Errorf("cursor = %d, want 2 — it came from the server's own reply", got)
	}
	if got := sink.pendingSequence(); got != 1 {
		t.Errorf("held sequence = %d, want 1 — the record is still unsettled at its own sequence", got)
	}
	if got := len(learner.calls()); got != 0 {
		t.Errorf("learning applied %d times, want 0 — nothing may be learned before the state is durable", got)
	}

	// The next call resumes sequence 1 rather than taking 2 for B.
	sink.journal.syncDir = (&syncRecorder{}).sync
	if _, err := sink.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v", err)
	}

	durable := server.durable()
	want := []string{recordDigest(t, recordA), recordDigest(t, recordB)}
	if fmt.Sprint(durable) != fmt.Sprint(want) {
		t.Errorf("evidence = %v, want %v — B must not take the sequence A is holding", durable, want)
	}
	if got := learner.calls(); len(got) != 2 ||
		got[0] != string(learningFor("A")) || got[1] != string(learningFor("B")) {
		t.Errorf("learning applied = %v, want A's then B's, once each", got)
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("held sequence = %d, want none", got)
	}
}

// TestUnprovenPendingWriteSendsNothing: a posting entry whose durability
// cannot be established is not a licence to send the record. The sequence
// stays free and nothing reaches the run.
func TestUnprovenPendingWriteSendsNothing(t *testing.T) {
	server := newReplayServer(t)
	path := filepath.Join(t.TempDir(), "pending.json")
	sink, learner := newTestSinkWith(t, server.URL, path)
	sink.journal.syncDir = (&syncRecorder{failN: 1}).sync

	_, err := sink.Record(context.Background(),
		trustvian.DecisionRecord{EventID: "A"}, learningFor("A"))
	if err == nil {
		t.Fatal("Record() error = nil, want the unprovable pending state surfaced")
	}
	if errors.Is(err, ErrUnresolved) || errors.Is(err, ErrLearningIndeterminate) {
		t.Errorf("error = %v, want a definitive failure; nothing was sent", err)
	}
	if got := server.postCount(); got != 0 {
		t.Errorf("server saw %d POSTs, want 0", got)
	}
	if got := sink.nextSequence(); got != 1 {
		t.Errorf("cursor = %d, want 1 — an unsent record consumes no sequence", got)
	}
	if got := len(learner.calls()); got != 0 {
		t.Errorf("learning applied %d times, want 0", got)
	}
}

// TestUnprovenReleaseIsReportedAndRecoverable: both halves are done and only
// the release is unproven. It is reported, and the sink keeps working — a
// stale confirmed entry tells the next process the learning is
// indeterminate, which is conservative rather than wrong.
func TestUnprovenReleaseIsReportedAndRecoverable(t *testing.T) {
	server := newReplayServer(t)
	path := filepath.Join(t.TempDir(), "pending.json")
	sink, learner := newTestSinkWith(t, server.URL, path)
	sink.journal.syncDir = (&syncRecorder{failN: 3}).sync // posting, confirmed, then the release

	_, err := sink.Record(context.Background(),
		trustvian.DecisionRecord{EventID: "A"}, learningFor("A"))
	if err == nil {
		t.Fatal("Record() error = nil, want the unprovable release surfaced")
	}
	if got := len(learner.calls()); got != 1 {
		t.Errorf("learning applied %d times, want 1 — it did happen", got)
	}
	if got := len(server.durable()); got != 1 {
		t.Errorf("evidence holds %d records, want 1", got)
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("held sequence = %d, want none — both halves are done", got)
	}

	// The next record proceeds normally, overwriting whatever the failed
	// release left behind.
	sink.journal.syncDir = (&syncRecorder{}).sync
	if _, err := sink.Record(context.Background(),
		trustvian.DecisionRecord{EventID: "B"}, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v", err)
	}
	if got := len(server.durable()); got != 2 {
		t.Errorf("evidence holds %d records, want 2", got)
	}
}

// TestOversizedLearningSendsNothing proves the write-ahead ordering still
// holds when the intent itself cannot be represented.
//
// A record whose pending entry does not fit the journal is one a restart
// could never recover, so it must not reach the control plane: the entry
// comes first precisely so that nothing is ever in flight without a durable
// note of it, and "the note cannot be written" is the same answer as "the
// note could not be made durable".
//
// The failure is definitive and must stay that way. No request was sent, no
// evidence was accepted and no learning was attempted, so it is neither an
// unresolved delivery nor an indeterminate learning — and the sequence it
// did not consume belongs to the next record.
func TestOversizedLearningSendsNothing(t *testing.T) {
	server := newReplayServer(t)
	path := filepath.Join(t.TempDir(), "pending.json")
	sink, learner := newTestSinkWith(t, server.URL, path)

	oversized := []byte(`{"pad":"` + strings.Repeat("x", maxPendingFile) + `"}`)
	_, err := sink.Record(context.Background(),
		trustvian.DecisionRecord{EventID: "A"}, oversized)
	if err == nil {
		t.Fatal("Record() error = nil, want an entry its own reader would reject refused")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %q, want it to name the limit", err.Error())
	}
	if errors.Is(err, ErrUnresolved) || errors.Is(err, ErrLearningIndeterminate) {
		t.Errorf("error = %v, want a definitive failure: nothing was sent, nothing was accepted, "+
			"and nothing was learned", err)
	}

	if got := server.postCount(); got != 0 {
		t.Errorf("server saw %d POSTs, want 0 — the intent must be durable before the request", got)
	}
	if got := len(server.durable()); got != 0 {
		t.Errorf("evidence holds %d records, want 0", got)
	}
	if got := len(learner.calls()); got != 0 {
		t.Errorf("learning applied %d times, want 0", got)
	}
	if got := sink.nextSequence(); got != 1 {
		t.Errorf("cursor = %d, want 1 — an unsent record consumes no sequence", got)
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("held sequence = %d, want none — nothing is in flight", got)
	}
	if pendingExists(t, path) {
		t.Error("a pending entry was left behind for a record that was never sent")
	}

	// The sequence it did not take is still there for the next record.
	recordB := trustvian.DecisionRecord{EventID: "B"}
	if _, err := sink.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v; sequence 1 must still be usable", err)
	}
	durable := server.durable()
	if len(durable) != 1 || durable[0] != recordDigest(t, recordB) {
		t.Errorf("evidence = %v, want only B at sequence 1", durable)
	}
	if got := learner.calls(); len(got) != 1 || got[0] != string(learningFor("B")) {
		t.Errorf("learning applied = %v, want only B's", got)
	}
}
