package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	trustvian "github.com/trustvian/trustvian"
)

// The durable half of one record's delivery.
//
// A record's evidence lives in the control plane and its learning lives in
// the Engine's store. Neither can be written inside the other's transaction,
// so something has to say which of the two has happened when a process dies
// between them. That is all this file is: one entry, on disk, describing the
// single record a sink may have in flight.
//
// It is not a spool and not a queue. The sink's mutex means there is never
// more than one record in flight, so there is never more than one entry, and
// the entry is released as soon as its record's fate is settled.

const (
	// pendingVersion versions this file's own format, independently of the
	// ingest wire version. A reader that does not recognize it refuses
	// rather than guessing, because the alternative to guessing wrong is
	// re-presenting a record or re-applying learning.
	pendingVersion = "1"

	// maxPendingFile bounds what is read back. The entry is one record and
	// one Result; anything far larger is not a file this wrote.
	maxPendingFile = 4 << 20
)

// pendingState is how far one record's delivery had progressed.
//
// The two states exist to answer the only question a restart cannot
// otherwise answer: had the learning been applied? statePosting proves it
// had not — it is written before the request is sent and replaced the moment
// the control plane confirms the record — so a recovered posting entry can be
// completed exactly once. stateConfirmed proves the record is in the run but
// says nothing about the learning, and is therefore the one state where
// recovery must choose; see Sink.Initialize.
type pendingState string

const (
	statePosting   pendingState = "posting"
	stateConfirmed pendingState = "confirmed"
)

// pendingEntry is one record's intent: what was sent, under which sequence,
// and what must be learned from it once the control plane confirms it.
//
// Learning is carried as opaque JSON. This package delivers records; what
// the bytes mean is the caller's (see LearnFunc), which is what keeps the
// engine's types out of the sink and the sink's HTTP contract out of the
// engine.
type pendingEntry struct {
	Version  string                   `json:"version"`
	RunID    string                   `json:"run_id"`
	Sequence string                   `json:"sequence"`
	State    pendingState             `json:"state"`
	Record   trustvian.DecisionRecord `json:"record"`
	Learning json.RawMessage          `json:"learning"`
}

// journal stores exactly one pendingEntry at a fixed path.
type journal struct{ path string }

// newJournal validates the location before any span depends on it.
//
// The directory must already exist: creating it here would let a typo in a
// container's volume mount produce a perfectly working directory on the
// container's own writable layer, which survives exactly until the restart
// this file exists for.
func newJournal(path string) (*journal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("pending_state_path is required")
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("pending_state_path directory %s is not usable: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("pending_state_path parent %s is not a directory", dir)
	}
	return &journal{path: path}, nil
}

// write replaces the entry atomically.
//
// Temp file, sync, rename — the same sequence internal/store's FileStore
// uses, and for the same reason: a torn entry is worse than no entry,
// because a reader cannot tell a truncated record from a different one.
func (j *journal) write(entry pendingEntry) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	dir := filepath.Dir(j.path)
	tmp, err := os.CreateTemp(dir, ".trustvian-pending-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, j.path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// load reads the entry, reporting false when there is none.
//
// Every structural problem is an error rather than a silently ignored file.
// An unreadable entry means a record's fate is unknown and unknowable from
// here, and continuing past it would be the exact assumption this mechanism
// exists to remove.
func (j *journal) load() (pendingEntry, bool, error) {
	file, err := os.Open(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return pendingEntry{}, false, nil
	}
	if err != nil {
		return pendingEntry{}, false, fmt.Errorf("open %s: %w", j.path, err)
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxPendingFile+1))
	if err != nil {
		return pendingEntry{}, false, fmt.Errorf("read %s: %w", j.path, err)
	}
	if len(raw) > maxPendingFile {
		return pendingEntry{}, false, fmt.Errorf("%s exceeds the %d byte limit", j.path, maxPendingFile)
	}

	var entry pendingEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return pendingEntry{}, false, fmt.Errorf("%s is not a valid pending ingest state", j.path)
	}
	if entry.Version != pendingVersion {
		return pendingEntry{}, false, fmt.Errorf(
			"%s has version %q, this build writes %q", j.path, entry.Version, pendingVersion)
	}
	if entry.RunID == "" {
		return pendingEntry{}, false, fmt.Errorf("%s names no run", j.path)
	}
	if _, err := parseSequence(entry.Sequence); err != nil {
		return pendingEntry{}, false, fmt.Errorf("%s: %w", j.path, err)
	}
	switch entry.State {
	case statePosting, stateConfirmed:
	default:
		return pendingEntry{}, false, fmt.Errorf("%s has unrecognized state %q", j.path, entry.State)
	}
	if len(entry.Learning) == 0 {
		return pendingEntry{}, false, fmt.Errorf("%s carries no learning payload", j.path)
	}
	return entry, true, nil
}

// clear releases the entry. A missing file is already the desired state.
func (j *journal) clear() error {
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", j.path, err)
	}
	return nil
}
