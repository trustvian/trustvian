package main

// Where `trustvian dev` keeps its state, and why it is not in the workload's
// repository.
//
// `make local` and trustvian-local put `.trustvian/` in the working directory,
// and ADR 0035 §12 gives good reasons: visible to ls, removable with rm -rf,
// isolated per checkout without anyone configuring that.
//
// dev cannot do that. Its working directory is *the application's repository* —
// possibly one the developer does not own, possibly read-only, possibly a
// worktree they do not want dirtied — and task 077 acceptance criterion 2 says
// that repository is byte-identical afterwards, asserted by hashing a fixture
// project before and after a full run. A directory appearing in `git status` is
// a modification whether or not a tracked file changed.
//
// So dev keeps its state under the user's home, keyed by the absolute path of
// the directory it was run in. ADR 0035 §12's actual requirements survive:
// isolation per checkout is the path key, and removability is one rm -rf of a
// path dev prints on every start. What is traded away is `ls` finding it in the
// project, and printing the path is a better answer than a convention the
// developer has to know.
//
// trustvian-local and `make local` are unchanged. This is dev's decision alone.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// devStateRoot is under the user's home rather than a cache directory.
	//
	// Not os.UserCacheDir: a cache is something a system may delete, and this
	// holds evaluation evidence a developer may want to compare against
	// tomorrow. Not the OS temp directory, for the same reason.
	devStateRoot = ".trustvian"

	// devStateSubdir separates dev's per-workload state from anything else that
	// may want a home under ~/.trustvian.
	devStateSubdir = "dev"

	// devStatePathFile records which directory this state belongs to.
	//
	// A hashed directory name is unreadable by design — it has to be stable and
	// filesystem-safe — so the state directory says in plain text what it is
	// for. Without it, a developer cleaning up cannot tell which of five
	// hashes is the project they have finished with.
	devStatePathFile = "workload-path"

	// devStateKeyLength is how much of the digest names the directory.
	//
	// 12 hex characters, 48 bits. This is a collision-avoidance key for one
	// user's project directories, not a security boundary: nothing is
	// authenticated by it and nothing is protected by its length. Full-length
	// would make an already-unreadable path unreadable and longer.
	devStateKeyLength = 12

	devStateDirMode  = 0o700
	devStateFileMode = 0o600
)

// devStateDir returns the state directory for one workload directory, creating
// it if needed.
//
// workloadDir must be absolute and is used as given. The caller resolves it, so
// this function stays a pure function of its input plus the home directory —
// which is what makes it testable without changing the process's cwd.
func devStateDir(home, workloadDir string) (string, error) {
	if !filepath.IsAbs(workloadDir) {
		return "", fmt.Errorf("workload directory %q is not absolute", workloadDir)
	}

	dir := filepath.Join(home, devStateRoot, devStateSubdir, devStateKey(workloadDir))
	if err := os.MkdirAll(dir, devStateDirMode); err != nil {
		return "", fmt.Errorf("creating the state directory: %w", err)
	}

	// Rewritten every start rather than only on creation: a directory that
	// moved and was re-run should describe where it is now, and the cost is one
	// small write per invocation.
	pathFile := filepath.Join(dir, devStatePathFile)
	if err := os.WriteFile(pathFile, []byte(workloadDir+"\n"), devStateFileMode); err != nil {
		return "", fmt.Errorf("recording the workload path: %w", err)
	}
	return dir, nil
}

// devStateKey derives the per-workload directory name.
//
// A hash rather than a sanitized path: an absolute path contains separators,
// spaces and characters that differ in legality between filesystems, and every
// escaping scheme that keeps it readable also makes two different paths capable
// of colliding. A digest cannot.
//
// The same input always gives the same key, which is what makes a second run in
// the same directory find the same state — the isolation property ADR 0035 §12
// actually asked for.
func devStateKey(workloadDir string) string {
	sum := sha256.Sum256([]byte(workloadDir))
	return hex.EncodeToString(sum[:])[:devStateKeyLength]
}

// resolveWorkloadDir returns the absolute, symlink-resolved working directory.
//
// Symlinks are resolved so that /tmp and /private/tmp on macOS — the same
// directory by two names — produce one state key rather than two. Without it, a
// developer running dev through a symlinked path would silently get a second,
// empty control plane and wonder where their runs went.
func resolveWorkloadDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determining the working directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		// A directory that cannot be resolved is still usable; the key just
		// follows the unresolved name. Better than refusing to run.
		return cwd, nil
	}
	return resolved, nil
}
