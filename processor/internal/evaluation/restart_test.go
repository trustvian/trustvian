package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

// The restart half of the ambiguous-delivery contract.
//
// Within one process, an unknown outcome is reconciled before Record
// returns. Across a restart there is no process left to reconcile it — so
// the question these tests ask is the one the sink's in-memory state cannot
// answer after a crash: had the control plane taken the record, and had
// anything been learned from it?
//
// The answer is durable, written before the request leaves, and these tests
// exercise both directions of it. They are what a design that learned from
// an unconfirmed record would fail: with a durable baseline, that learning
// outlives the process, and nothing afterwards can tell it apart from
// learning the run's evidence accounts for.

// loadEntry reads the pending state a test expects to be there.
func loadEntry(t *testing.T, path string) pendingEntry {
	t.Helper()
	j, err := newJournal(path)
	if err != nil {
		t.Fatalf("newJournal() error = %v", err)
	}
	entry, found, err := j.load()
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if !found {
		t.Fatalf("no pending state at %s, want one", path)
	}
	return entry
}

func pendingExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}

// TestUnresolvedRecordIsNotLearnedFrom is the rule the restart cases rest
// on: an unknown outcome means unknown, so nothing is learned from that
// record yet — and the fact that it was in flight is on disk before the
// request could have reached anyone.
//
// A sink that learned here would be correct only until the process died: a
// durable baseline would then hold a record the run may never have received.
func TestUnresolvedRecordIsNotLearnedFrom(t *testing.T) {
	server := newReplayServer(t)
	server.dropAlways = true
	path := filepath.Join(t.TempDir(), "pending.json")

	sink, learner := newTestSinkWith(t, server.URL, path)
	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}

	_, err := sink.Record(context.Background(), recordA, learningFor("A"))
	if err == nil {
		t.Fatal("Record() error = nil, want an unresolved outcome")
	}
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("error = %v, want ErrUnresolved", err)
	}
	if got := learner.calls(); len(got) != 0 {
		t.Errorf("learning was applied %v times for a record nobody confirmed, want none", got)
	}

	entry := loadEntry(t, path)
	if entry.State != statePosting {
		t.Errorf("pending state = %q, want %q — the state a restart completes exactly once",
			entry.State, statePosting)
	}
	if entry.Sequence != "1" || entry.RunID != "run-1" {
		t.Errorf("pending entry = run %s sequence %s, want run-1 sequence 1", entry.RunID, entry.Sequence)
	}
	if entry.Record.EventID != "A" {
		t.Errorf("pending record = %q, want A — the exact record that claimed the sequence", entry.Record.EventID)
	}
	if string(entry.Learning) != string(learningFor("A")) {
		t.Errorf("pending learning = %s, want %s", entry.Learning, learningFor("A"))
	}
}

// TestRestartDiscardsARecordTheServerNeverReceived is restart case B: the
// request never arrived. The run still expects that very sequence, so the
// two halves already agree — at nothing — and the record is discarded rather
// than learned from or posted after the fact.
//
// Nothing is learned from it because nothing ever was: a posting entry is
// replaced the instant the control plane confirms a record, so its presence
// proves the previous process had not learned from it. That is the property
// a sink observing unconfirmed records would break, and a durable baseline
// would then carry that learning past this restart.
func TestRestartDiscardsARecordTheServerNeverReceived(t *testing.T) {
	server := newReplayServer(t)
	path := filepath.Join(t.TempDir(), "pending.json")
	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	// Process 1: the context is already cancelled, so the request provably
	// never reaches the server — the harness proves it by the empty evidence
	// below, not by trusting the client's own report.
	first, firstLearner := newTestSinkWith(t, server.URL, path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := first.Record(ctx, recordA, learningFor("A")); err == nil {
		t.Fatal("Record(A) error = nil, want the cancellation surfaced")
	}
	if got := server.durable(); len(got) != 0 {
		t.Fatalf("control plane holds %v, want nothing", got)
	}
	if got := firstLearner.calls(); len(got) != 0 {
		t.Fatalf("learning was applied %v before any confirmation, want none", got)
	}

	// Process 1 disappears here. Process 2 inherits only the pending file.
	second := &learnRecorder{}
	sink, err := New(server.URL, "run-1", "support-reference", path, second.learn)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	recovery, err := sink.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if recovery.Outcome != RecoveryDiscarded || recovery.Sequence != 1 {
		t.Errorf("recovery = %+v, want sequence 1 discarded — the record had never arrived", recovery)
	}
	if got := second.calls(); len(got) != 0 {
		t.Errorf("learning applied = %v, want none; the run does not hold that record", got)
	}
	if pendingExists(t, path) {
		t.Error("the pending state survived a completed recovery")
	}
	if got := server.postCount(); got != 0 {
		t.Errorf("server saw %d POSTs during recovery, want 0", got)
	}

	// The sequence the discarded record held goes to the next one.
	if _, err := sink.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v", err)
	}
	durable := server.durable()
	if len(durable) != 1 || durable[0] != recordDigest(t, recordB) {
		t.Errorf("evidence = %v, want only B at sequence 1", durable)
	}
	if got := second.calls(); len(got) != 1 || got[0] != string(learningFor("B")) {
		t.Errorf("learning applied = %v, want exactly [%s]", got, learningFor("B"))
	}
	if got := sink.nextSequence(); got != 2 {
		t.Errorf("cursor = %d, want 2", got)
	}
}

// TestRestartFinishesARecordTheServerAlreadyHeld is restart case A, the hard
// one: the control plane committed the record and the reply was destroyed,
// and the process died before it could reconcile.
//
// The second process re-presents the same record at the same sequence. The
// server answers replayed — no second record, no conflict — and only then is
// the learning applied, exactly once, because the pending state proves the
// first process never got that far.
func TestRestartFinishesARecordTheServerAlreadyHeld(t *testing.T) {
	server := newReplayServer(t)
	server.dropAlways = true // every reply is destroyed, including the retry's
	path := filepath.Join(t.TempDir(), "pending.json")
	recordA := trustvian.DecisionRecord{EventID: "A", Decision: "allow"}
	recordB := trustvian.DecisionRecord{EventID: "B", Decision: "block"}

	first, firstLearner := newTestSinkWith(t, server.URL, path)
	if _, err := first.Record(context.Background(), recordA, learningFor("A")); err == nil {
		t.Fatal("Record(A) error = nil, want an unresolved outcome")
	}
	if got := len(server.durable()); got != 1 {
		t.Fatalf("control plane holds %d records, want 1 — it committed before the reply was lost", got)
	}
	if got := firstLearner.calls(); len(got) != 0 {
		t.Fatalf("learning was applied %v while the outcome was unknown, want none", got)
	}

	// Process 1 disappears. The control plane is reachable again.
	server.mu.Lock()
	server.dropAlways = false
	server.mu.Unlock()

	second := &learnRecorder{}
	sink, err := New(server.URL, "run-1", "support-reference", path, second.learn)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	recovery, err := sink.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if recovery.Outcome != RecoveryCompleted || recovery.Sequence != 1 ||
		recovery.Disposition != dispositionReplayed {
		t.Errorf("recovery = %+v, want sequence 1 completed as replayed — the control plane already had it",
			recovery)
	}
	if got := second.calls(); len(got) != 1 || got[0] != string(learningFor("A")) {
		t.Fatalf("learning applied = %v, want exactly [%s] — once, and only now", got, learningFor("A"))
	}
	if got := len(server.durable()); got != 1 {
		t.Errorf("control plane holds %d records, want 1 — a replay must not duplicate evidence", got)
	}
	if pendingExists(t, path) {
		t.Error("the pending state survived a completed recovery")
	}

	if _, err := sink.Record(context.Background(), recordB, learningFor("B")); err != nil {
		t.Fatalf("Record(B) error = %v", err)
	}
	if got := sink.nextSequence(); got != 3 {
		t.Errorf("cursor = %d, want 3 — A at 1, B at 2", got)
	}
	durable := server.durable()
	want := []string{recordDigest(t, recordA), recordDigest(t, recordB)}
	if fmt.Sprint(durable) != fmt.Sprint(want) {
		t.Errorf("evidence = %v, want %v", durable, want)
	}
}

// TestRestartDoesNotRelearnAConfirmedRecord covers the window recovery
// cannot settle: the previous process died after the control plane confirmed
// the record and before the sink released it, so whether the learning was
// applied is unknowable.
//
// It is not applied again. A missing observation makes a fingerprint look
// less familiar, which fails safe; a doubled one makes it look more familiar
// than the evidence supports, which is a silent weakening. The caller is
// told, which is what keeps it from being silent.
func TestRestartDoesNotRelearnAConfirmedRecord(t *testing.T) {
	server := newReplayServer(t)
	server.next = 2
	server.committed = []string{"already-applied"}
	path := filepath.Join(t.TempDir(), "pending.json")

	j, err := newJournal(path)
	if err != nil {
		t.Fatalf("newJournal() error = %v", err)
	}
	if err := j.write(pendingEntry{
		Version: pendingVersion, RunID: "run-1", Sequence: "1", State: stateConfirmed,
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
	recovery, err := sink.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if recovery.Outcome != RecoveryIndeterminate || recovery.Sequence != 1 {
		t.Errorf("recovery = %+v, want sequence 1 reported as indeterminate", recovery)
	}
	if got := learner.calls(); len(got) != 0 {
		t.Errorf("learning applied = %v, want none — applying it twice cannot be ruled out", got)
	}
	if pendingExists(t, path) {
		t.Error("the confirmed entry was not released")
	}
	if got := sink.nextSequence(); got != 2 {
		t.Errorf("cursor = %d, want 2 — the server's own number", got)
	}
	if got := len(server.durable()); got != 1 {
		t.Errorf("control plane holds %d records, want 1 — a confirmed entry must not be re-presented", got)
	}
}

// TestRestartRefusesPendingStateFromAnotherRun fails closed rather than
// discarding it. The entry describes a record whose fate is unsettled for
// that other run, which this process cannot settle; deleting it would
// destroy the only evidence the record was ever in flight.
func TestRestartRefusesPendingStateFromAnotherRun(t *testing.T) {
	server := newReplayServer(t)
	path := filepath.Join(t.TempDir(), "pending.json")

	j, _ := newJournal(path)
	if err := j.write(pendingEntry{
		Version: pendingVersion, RunID: "run-other", Sequence: "1", State: statePosting,
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
	_, err = sink.Initialize(context.Background())
	if err == nil {
		t.Fatal("Initialize() error = nil, want a refusal for another run's pending state")
	}
	if !strings.Contains(err.Error(), "run-other") || !strings.Contains(err.Error(), "run-1") {
		t.Errorf("error = %q, want it to name both runs", err.Error())
	}
	if !pendingExists(t, path) {
		t.Error("the other run's pending state was discarded; it is the only record that it existed")
	}
	if got := len(server.durable()); got != 0 {
		t.Errorf("control plane holds %d records, want 0 — nothing may be posted into this run", got)
	}
}

// TestRestartRefusesUnreadablePendingState: a file that cannot be understood
// describes a record whose fate is unknown and unknowable from here, so
// starting past it would be exactly the assumption this mechanism removes.
func TestRestartRefusesUnreadablePendingState(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"not JSON", "{not json"},
		{"unknown version", `{"version":"99","run_id":"run-1","sequence":"1","state":"posting","learning":{}}`},
		{"unknown state", `{"version":"1","run_id":"run-1","sequence":"1","state":"halfway","learning":{}}`},
		{"non-canonical sequence", `{"version":"1","run_id":"run-1","sequence":"01","state":"posting","learning":{}}`},
		{"no learning", `{"version":"1","run_id":"run-1","sequence":"1","state":"posting"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newReplayServer(t)
			path := filepath.Join(t.TempDir(), "pending.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			sink, err := New(server.URL, "run-1", "support-reference", path,
				func(context.Context, []byte) error { return nil })
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, err := sink.Initialize(context.Background()); err == nil {
				t.Fatal("Initialize() error = nil, want a refusal")
			}
		})
	}
}

// TestPendingStateWriteFailureSendsNothing is the write-ahead promise: no
// request is ever in flight without a durable note of it. If the note cannot
// be written, the record is not sent, so its sequence is untouched and the
// next record takes it.
func TestPendingStateWriteFailureSendsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block a write")
	}
	server := newReplayServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")

	sink, learner := newTestSinkWith(t, server.URL, path)

	// Read-only from here: the entry's temp file cannot be created, so the
	// intent cannot be made durable.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := sink.Record(context.Background(),
		trustvian.DecisionRecord{EventID: "A"}, learningFor("A"))
	if err == nil {
		t.Fatal("Record() error = nil, want the unwritable pending state surfaced")
	}
	if errors.Is(err, ErrUnresolved) {
		t.Errorf("error = %v, want a definitive failure; nothing was sent", err)
	}
	if got := server.postCount(); got != 0 {
		t.Errorf("server saw %d POSTs, want 0 — the intent must be durable first", got)
	}
	if got := sink.nextSequence(); got != 1 {
		t.Errorf("cursor = %d, want 1 — an unsent record consumes no sequence", got)
	}
	if got := sink.pendingSequence(); got != 0 {
		t.Errorf("held sequence = %d, want none", got)
	}
	if got := learner.calls(); len(got) != 0 {
		t.Errorf("learning applied = %v, want none", got)
	}
}

// TestLearningFollowsConfirmationOnly is the whole rule in one table: the
// control plane's answer decides whether anything is learned.
func TestLearningFollowsConfirmationOnly(t *testing.T) {
	tests := []struct {
		name      string
		configure func(s *replayServer)
		wantLearn int
		wantErr   bool
	}{
		{
			name:      "applied",
			configure: func(*replayServer) {},
			wantLearn: 1,
		},
		{
			name:      "reconciled after a lost response",
			configure: func(s *replayServer) { s.dropResponse[1] = true },
			wantLearn: 1,
		},
		{
			name:      "declined",
			configure: func(s *replayServer) { s.rejectFirst = true },
			wantLearn: 0,
			wantErr:   true,
		},
		{
			name:      "never confirmed",
			configure: func(s *replayServer) { s.dropAlways = true },
			wantLearn: 0,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newReplayServer(t)
			tt.configure(server)
			path := filepath.Join(t.TempDir(), "pending.json")
			sink, learner := newTestSinkWith(t, server.URL, path)

			_, err := sink.Record(context.Background(),
				trustvian.DecisionRecord{EventID: "A"}, learningFor("A"))
			if tt.wantErr != (err != nil) {
				t.Fatalf("Record() error = %v, want an error: %t", err, tt.wantErr)
			}
			if got := len(learner.calls()); got != tt.wantLearn {
				t.Errorf("learning applied %d times, want %d", got, tt.wantLearn)
			}
		})
	}
}

// TestRecordRefusesAnEmptyLearningPayload: a confirmed record whose learning
// cannot be applied — now or after a restart — is the divergence this design
// prevents, so it is refused before anything is sent.
func TestRecordRefusesAnEmptyLearningPayload(t *testing.T) {
	server := newReplayServer(t)
	sink := newTestSink(t, server.URL)

	if _, err := sink.Record(context.Background(), trustvian.DecisionRecord{EventID: "A"}, nil); err == nil {
		t.Fatal("Record() error = nil, want a record with no learning refused")
	}
	if got := server.postCount(); got != 0 {
		t.Errorf("server saw %d POSTs, want 0", got)
	}
}

// TestPendingEntryRoundTrip pins the durable format itself: what is written
// is what comes back, including the opaque learning payload.
func TestPendingEntryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	j, err := newJournal(path)
	if err != nil {
		t.Fatalf("newJournal() error = %v", err)
	}
	if _, found, err := j.load(); err != nil || found {
		t.Fatalf("load() on a fresh path = (found %t, %v), want (false, nil)", found, err)
	}

	want := pendingEntry{
		Version: pendingVersion, RunID: "run-1", Sequence: "7", State: statePosting,
		Record:   trustvian.DecisionRecord{EventID: "A", FingerprintID: "fp-1"},
		Learning: json.RawMessage(`{"deep":{"value":1}}`),
	}
	if err := j.write(want); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	got, found, err := j.load()
	if err != nil || !found {
		t.Fatalf("load() = (found %t, %v), want (true, nil)", found, err)
	}
	if got.RunID != want.RunID || got.Sequence != want.Sequence || got.State != want.State {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
	if got.Record.EventID != want.Record.EventID || got.Record.FingerprintID != want.Record.FingerprintID {
		t.Errorf("record = %+v, want %+v", got.Record, want.Record)
	}
	if string(got.Learning) != string(want.Learning) {
		t.Errorf("learning = %s, want %s", got.Learning, want.Learning)
	}

	if err := j.clear(); err != nil {
		t.Fatalf("clear() error = %v", err)
	}
	if _, found, _ := j.load(); found {
		t.Error("clear() left the entry in place")
	}
	// Clearing what is already gone is the desired state, not an error.
	if err := j.clear(); err != nil {
		t.Errorf("clear() on a missing entry error = %v, want nil", err)
	}
}

// TestNewRefusesAnUnusablePendingStatePath fails at construction rather than
// on the first span: a path that cannot work is a Collector that must not
// come up.
func TestNewRefusesAnUnusablePendingStatePath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"missing directory", filepath.Join(t.TempDir(), "nope", "pending.json")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New("http://127.0.0.1:1", "run-1", "p", tt.path,
				func(context.Context, []byte) error { return nil })
			if err == nil {
				t.Fatal("New() error = nil, want an unusable pending state path refused")
			}
		})
	}
}

// TestRestartRefusesWhenTheRunMovedOnWithoutUs: a cursor the pending record
// cannot account for — behind it, or more than one record past it — means
// another writer has been in this run. Guessing which records are whose is
// how evidence and learning diverge for good, so recovery refuses instead.
func TestRestartRefusesWhenTheRunMovedOnWithoutUs(t *testing.T) {
	tests := []struct {
		name       string
		serverNext uint64
	}{
		{"two records past ours", 4},
		{"behind ours", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newReplayServer(t)
			server.next = tt.serverNext
			for range tt.serverNext - 1 {
				server.committed = append(server.committed, "someone-else")
			}
			path := filepath.Join(t.TempDir(), "pending.json")

			j, _ := newJournal(path)
			if err := j.write(pendingEntry{
				Version: pendingVersion, RunID: "run-1", Sequence: "2", State: statePosting,
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
			if _, err := sink.Initialize(context.Background()); err == nil {
				t.Fatal("Initialize() error = nil, want a refusal")
			}
			if got := learner.calls(); len(got) != 0 {
				t.Errorf("learning applied = %v, want none", got)
			}
			if !pendingExists(t, path) {
				t.Error("the pending state was discarded; it is the only record of the unsettled record")
			}
		})
	}
}

// TestRestartRefusesAForeignRecordAtOurSequence is the other half of the
// cursor check. The cursor moved by exactly one, but what sits there is a
// different record — so re-presenting ours conflicts, and recovery fails
// closed rather than treating someone else's record as proof of ours.
func TestRestartRefusesAForeignRecordAtOurSequence(t *testing.T) {
	server := newReplayServer(t)
	server.next = 2
	server.committed = []string{"a-different-record"}
	path := filepath.Join(t.TempDir(), "pending.json")

	j, _ := newJournal(path)
	if err := j.write(pendingEntry{
		Version: pendingVersion, RunID: "run-1", Sequence: "1", State: statePosting,
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
	if _, err := sink.Initialize(context.Background()); err == nil {
		t.Fatal("Initialize() error = nil, want the conflict surfaced")
	}
	if got := learner.calls(); len(got) != 0 {
		t.Errorf("learning applied = %v, want none — that sequence holds someone else's record", got)
	}
	if got := len(server.durable()); got != 1 {
		t.Errorf("control plane holds %d records, want 1 — recovery must add none", got)
	}
}

// TestRestartRefusesAConfirmedRecordTheRunDoesNotHold: a confirmed entry is
// only written after the control plane accepted the record, so a run still
// expecting that sequence contradicts it — a restored backup, or a run
// identifier something else is using. Neither half can be trusted from here.
func TestRestartRefusesAConfirmedRecordTheRunDoesNotHold(t *testing.T) {
	server := newReplayServer(t) // expects sequence 1, holds nothing
	path := filepath.Join(t.TempDir(), "pending.json")

	j, _ := newJournal(path)
	if err := j.write(pendingEntry{
		Version: pendingVersion, RunID: "run-1", Sequence: "1", State: stateConfirmed,
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
	if _, err := sink.Initialize(context.Background()); err == nil {
		t.Fatal("Initialize() error = nil, want the contradiction surfaced")
	}
	if got := learner.calls(); len(got) != 0 {
		t.Errorf("learning applied = %v, want none", got)
	}
}
