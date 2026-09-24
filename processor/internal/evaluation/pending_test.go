package evaluation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

// The journal's durability contract.
//
// Both of its operations are durability operations, and both have a second
// half that is easy to leave out: fsyncing a file's contents says nothing
// about the directory entry naming it, so a rename or an unlink that reached
// only the page cache is undone by a host that loses power. These tests pin
// that the parent directory is synced after both, and that a failure to
// prove it is reported rather than assumed away.
//
// They assert the semantics — what is on disk afterwards, and what the
// caller is told — rather than counting syscalls. The one seam is the
// directory sync itself, because a temporary directory cannot be made to
// lose a rename on demand.

// syncRecorder stands in for the directory sync, recording what it was asked
// to sync and optionally failing.
type syncRecorder struct {
	mu    sync.Mutex
	dirs  []string
	failN int // fail the Nth call (1-indexed); 0 never fails
	calls int
}

func (r *syncRecorder) sync(dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.dirs = append(r.dirs, dir)
	if r.failN != 0 && r.calls == r.failN {
		return errors.New("directory sync failed")
	}
	return nil
}

func (r *syncRecorder) synced() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dirs...)
}

func testEntry(state pendingState) pendingEntry {
	return pendingEntry{
		Version: pendingVersion, RunID: "run-1", Sequence: "1", State: state,
		Record:   trustvian.DecisionRecord{EventID: "A"},
		Learning: learningFor("A"),
	}
}

// TestJournalWriteSyncsTheParentDirectory: the entry lands atomically, no
// temp file is left behind, and the directory holding the new name is
// synced — without which the rename is not durable against a host crash,
// only against a process one.
func TestJournalWriteSyncsTheParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")
	j, err := newJournal(path)
	if err != nil {
		t.Fatalf("newJournal() error = %v", err)
	}
	recorder := &syncRecorder{}
	j.syncDir = recorder.sync

	if err := j.write(testEntry(statePosting)); err != nil {
		t.Fatalf("write() error = %v", err)
	}

	if got := recorder.synced(); len(got) != 1 || got[0] != dir {
		t.Errorf("synced %v, want exactly [%s] — the directory entry is the part a rename adds", got, dir)
	}
	entry, found, err := j.load()
	if err != nil || !found {
		t.Fatalf("load() = (found %t, %v), want the entry back", found, err)
	}
	if entry.Sequence != "1" || entry.State != statePosting {
		t.Errorf("entry = %+v, want sequence 1 posting", entry)
	}
	// Nothing but the entry: a temp file left behind would be a second
	// candidate for a reader that guessed.
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(names) != 1 || names[0].Name() != "pending.json" {
		got := make([]string, 0, len(names))
		for _, n := range names {
			got = append(got, n.Name())
		}
		t.Errorf("directory holds %v, want only pending.json", got)
	}
}

// TestJournalClearSyncsTheParentDirectory: releasing an entry is a durability
// operation too. An unlink that a host crash undoes brings back a note for a
// record that is already settled.
func TestJournalClearSyncsTheParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")
	j, _ := newJournal(path)
	recorder := &syncRecorder{}
	j.syncDir = recorder.sync

	if err := j.write(testEntry(stateConfirmed)); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	if err := j.clear(); err != nil {
		t.Fatalf("clear() error = %v", err)
	}

	if got := recorder.synced(); len(got) != 2 || got[1] != dir {
		t.Errorf("synced %v, want the directory synced after the remove as well as the rename", got)
	}
	if _, found, _ := j.load(); found {
		t.Error("clear() left the entry in place")
	}

	// Clearing what is already gone still syncs: a previous clear may be
	// exactly what left it missing, and that removal may be what is unproven.
	if err := j.clear(); err != nil {
		t.Fatalf("clear() on a missing entry error = %v, want nil", err)
	}
	if got := len(recorder.synced()); got != 3 {
		t.Errorf("synced %d times, want 3 — a no-op remove is still a durability point", got)
	}
}

// TestJournalWriteReportsAnUnprovenRename is the honest half of the failure
// story: the rename happened, and whether it survives a host crash is now
// unknown. It is reported, and it is not reported as "the write did not
// happen" — the entry is on disk, and a caller that assumed otherwise would
// send a record with no durable note of it.
func TestJournalWriteReportsAnUnprovenRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")
	j, _ := newJournal(path)
	j.syncDir = (&syncRecorder{failN: 1}).sync

	err := j.write(testEntry(statePosting))
	if err == nil {
		t.Fatal("write() error = nil, want the unproven durability surfaced")
	}
	if !strings.Contains(err.Error(), "renamed into place") {
		t.Errorf("error = %q, want it to say the rename happened", err.Error())
	}
	if _, found, loadErr := j.load(); loadErr != nil || !found {
		t.Errorf("load() = (found %t, %v), want the entry present — the rename did happen",
			found, loadErr)
	}
}

// TestJournalClearReportsAnUnprovenRemoval: same shape at the other end. The
// file is gone from this process's view; whether it stays gone is unproven,
// and the caller is told rather than left to assume.
func TestJournalClearReportsAnUnprovenRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")
	j, _ := newJournal(path)
	recorder := &syncRecorder{failN: 2}
	j.syncDir = recorder.sync

	if err := j.write(testEntry(stateConfirmed)); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	err := j.clear()
	if err == nil {
		t.Fatal("clear() error = nil, want the unproven removal surfaced")
	}
	if !strings.Contains(err.Error(), "removed") {
		t.Errorf("error = %q, want it to say the removal happened", err.Error())
	}
	if _, found, _ := j.load(); found {
		t.Error("the entry is still present; the remove itself should have succeeded")
	}
}

// TestSyncDirectoryOnARealDirectory exercises the shipped implementation
// rather than the seam, so a platform where it cannot work says so here
// rather than in production. On Windows it is a documented no-op (see
// pending_sync_windows.go) and this simply passes.
func TestSyncDirectoryOnARealDirectory(t *testing.T) {
	if err := syncDirectory(t.TempDir()); err != nil {
		t.Fatalf("syncDirectory() error = %v", err)
	}
}

// entryOfEncodedSize builds an entry whose JSON encoding is exactly size
// bytes, by padding the learning payload.
//
// Sizes are derived rather than assumed: the payload is plain ASCII inside a
// JSON string, so each byte added to it adds exactly one byte to the
// encoding, and the difference from a one-byte payload gives the overhead
// without this test having to know anything about the entry's shape.
func entryOfEncodedSize(t *testing.T, state pendingState, size int) pendingEntry {
	t.Helper()
	entry := testEntry(state)
	entry.Learning = []byte(`{"pad":""}`)
	base, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	pad := size - len(base)
	if pad < 0 {
		t.Fatalf("an entry cannot be smaller than its own %d byte envelope", len(base))
	}
	entry.Learning = []byte(`{"pad":"` + strings.Repeat("x", pad) + `"}`)

	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(encoded) != size {
		t.Fatalf("built an entry of %d bytes, want %d", len(encoded), size)
	}
	return entry
}

// TestJournalRefusesAnEntryItsReaderWouldReject is the invariant the two
// bounds exist to hold together: what this writer accepts, this reader
// recovers. A file over the limit is not "not one this wrote" — without the
// write-side check it is exactly what this wrote, and what the next process
// then refuses to read.
//
// The limit is approached from both sides rather than with an arbitrary
// large payload, so the two ends cannot drift apart unnoticed.
func TestJournalRefusesAnEntryItsReaderWouldReject(t *testing.T) {
	// A posting entry is checked in the form it will take once confirmed,
	// because that is the write that has to succeed after the record is
	// already in the run.
	largest := maxPendingFile - stateHeadroom(statePosting)

	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "at the limit", size: largest},
		{name: "one byte over", size: largest + 1, wantErr: true},
		{name: "far over", size: maxPendingFile * 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pending.json")
			j, err := newJournal(path)
			if err != nil {
				t.Fatalf("newJournal() error = %v", err)
			}

			entry := entryOfEncodedSize(t, statePosting, tt.size)
			err = j.write(entry)

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("write() error = %v, want an entry at the limit accepted", err)
				}
				got, found, loadErr := j.load()
				if loadErr != nil || !found {
					t.Fatalf("load() = (found %t, %v), want the entry this writer accepted",
						found, loadErr)
				}
				if string(got.Learning) != string(entry.Learning) {
					t.Error("the recovered learning payload differs from the written one")
				}
				// And the confirmed rewrite — the one that happens after the
				// control plane has the record — must fit too.
				confirmed := entry
				confirmed.State = stateConfirmed
				if err := j.write(confirmed); err != nil {
					t.Errorf("write(confirmed) error = %v; an entry that fit on the way in must "+
						"still fit once the record is in the run, or it could never be settled", err)
				}
				return
			}

			if err == nil {
				t.Fatal("write() error = nil, want an entry its own reader would reject refused")
			}
			if !strings.Contains(err.Error(), "limit") {
				t.Errorf("error = %q, want it to name the limit", err.Error())
			}
			// Nothing written, and nothing left behind: no entry, and no
			// temp file for a reader to find either.
			if _, found, loadErr := j.load(); found || loadErr != nil {
				t.Errorf("load() = (found %t, %v), want nothing to recover", found, loadErr)
			}
			names, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir() error = %v", err)
			}
			if len(names) != 0 {
				left := make([]string, 0, len(names))
				for _, n := range names {
					left = append(left, n.Name())
				}
				t.Errorf("directory holds %v, want nothing — the check precedes the temp file", left)
			}
		})
	}
}
