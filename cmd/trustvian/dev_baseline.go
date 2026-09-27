package main

// The engine's learned baseline under `trustvian dev`, and the lock that keeps
// one writer on it.
//
// The first version of the generated Collector configuration had no `storage:`
// block, so the engine used its in-memory default. With one Collector per run,
// that meant the learned baseline was discarded every time a run ended — and the
// design's own claim that "two runs of one candidate share a learned baseline"
// was false. Worse, it made the two engine-evidence gates inert: with an empty
// baseline every behavior is novel and anomaly *confidence* is zero, so trust is
// never penalized, no BLOCK is ever decided, and MaxBlockDecisions and
// MaxCriticalRiskObservations can only ever read zero. A gate that cannot fail is
// not a gate.
//
// So dev configures a file store, one file per behavioral profile. Which brings
// the rule that comes with it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// baselineFilePrefix names the per-profile baseline file.
//
// One file per profile, not one per run: the profile *is* the learning scope, and
// the whole point is that two runs of one candidate meet the same baseline.
const baselineFilePrefix = "baseline-"

// baselineLockSuffix marks the lock beside the baseline it protects.
const baselineLockSuffix = ".lock"

// baselineLock is a held claim on one profile's baseline file.
type baselineLock struct {
	path     string
	lockPath string
	held     bool
}

// acquireBaseline claims the baseline file for one profile.
//
// **The file store has no cross-process locking.** `docs/storage-guide.md` says to
// use it for a single-process deployment, and README says plainly that two
// instances with two separate files hold two different baselines. Two *writers* on
// one file is worse than either: the store reads, modifies and rewrites a snapshot,
// so concurrent runs would silently discard each other's learning and leave a file
// whose contents belong to neither.
//
// Nothing in the file store detects that, so dev refuses it here. A lock file
// holding the owner's pid, created exclusively, with a stale one reclaimed only
// when its owner is gone.
func acquireBaseline(stateDir, profile string) (*baselineLock, error) {
	name := baselineFilePrefix + baselineFileSegment(profile) + ".json"
	lock := &baselineLock{
		path:     filepath.Join(stateDir, name),
		lockPath: filepath.Join(stateDir, name+baselineLockSuffix),
	}

	// Validated before it reaches the generated configuration, like every other
	// substituted scalar: a path that would need quoting is refused rather than
	// rewritten, because a rewritten path is a different file.
	if err := validateCollectorScalar("baseline path", lock.path); err != nil {
		return nil, err
	}

	if err := lock.claim(); err != nil {
		return nil, err
	}
	return lock, nil
}

// claim creates the lock file, or explains who holds it.
func (b *baselineLock) claim() error {
	file, err := os.OpenFile(b.lockPath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY, devStateFileMode)
	if err == nil {
		fmt.Fprintf(file, "%d\n", os.Getpid())
		file.Close()
		b.held = true
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("claiming the baseline file: %w", err)
	}

	owner, readErr := b.owner()
	if readErr == nil && processAliveForBaseline(owner) {
		return fmt.Errorf(
			"another trustvian dev run (pid %d) is already learning into %s.\n\n"+
				"The engine's file store has no cross-process locking, so two runs "+
				"writing one\nbaseline would discard each other's learning. Wait for it, "+
				"or give this run its\nown learning scope:\n\n"+
				"  trustvian dev --candidate <other-id> -- <command>",
			owner, b.path)
	}

	// Stale: the owner is gone, or the file never carried a readable pid. Taking
	// it over is safe because nothing is writing the baseline.
	if err := os.Remove(b.lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clearing a stale baseline lock: %w", err)
	}
	return b.claim()
}

// owner reads the pid out of an existing lock.
func (b *baselineLock) owner() (int, error) {
	raw, err := os.ReadFile(b.lockPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

// release drops the claim.
//
// Only when this process holds it: a run that refused to start because another
// held the lock must not delete that other run's claim on its way out.
func (b *baselineLock) release() {
	if !b.held {
		return
	}
	b.held = false
	_ = os.Remove(b.lockPath)
}

// baselineFileSegment makes a profile usable as a filename.
//
// The profile is a caller-owned identifier and may contain characters a filename
// cannot — `git:abc1234+dirty` has a colon, which is a path separator on some
// systems and legal on others. Mapped rather than refused, because unlike an
// identity value this is not the thing being identified: the profile still travels
// to the control plane exactly as derived, and only the file it happens to be
// stored in is renamed.
//
// Distinct profiles must not collide, so the mapping is injective for the
// characters it changes: each replaced byte becomes its own two-hex-digit escape
// rather than a shared underscore.
func baselineFileSegment(profile string) string {
	var b strings.Builder
	for i := range len(profile) {
		c := profile[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	return b.String()
}
