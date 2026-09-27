// This file tests .gitignore itself, because a wrong pattern there fails
// silently and in the worst possible way: `git add` reports success, the file
// never enters the index, and the omission surfaces later as a build that works
// on one machine and not in CI.
//
// It exists because of a real one. A commit added the unanchored pattern
// `trustvian` to ignore the root-level build output. Without a leading slash a
// gitignore pattern matches every path component with that name at any depth, so
// it also matched `cmd/trustvian/` — the package that holds the CLI's entire
// source. Every existing file there stayed tracked, so nothing appeared wrong;
// only *new* files were ignored, which is the version of this bug that survives
// review.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sourceRoots are checked even when they hold no tracked Go file.
//
// The directory set is otherwise derived from what is tracked, which keeps it
// from going stale — but a derived set cannot notice a root that has become
// empty, and these three are the module trees.
var sourceRoots = []string{"cmd", "platform", "processor"}

// probeName is the file that does not exist, in a directory that does.
//
// The probe has to be an untracked path: `git check-ignore` consults the index by
// default, so it reports a *tracked* file as not ignored no matter what patterns
// match it. Asking about a tracked file would therefore pass while every new file
// beside it was being ignored — which is exactly the bug this test is about.
const probeName = "probe_new_source_file.go"

func TestNoTrackedSourceDirectoryIsIgnored(t *testing.T) {
	root := repositoryRootForIgnoreTest(t)

	directories := map[string]bool{}
	for _, name := range sourceRoots {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("source root %s is missing: %v", name, err)
		}
		directories[name] = true
	}
	for _, dir := range trackedGoDirectories(t, root) {
		directories[dir] = true
	}

	probes := make([]string, 0, len(directories))
	for dir := range directories {
		probes = append(probes, filepath.ToSlash(filepath.Join(dir, probeName)))
	}
	sort.Strings(probes)

	// -v names the file, line and pattern responsible, so a failure says what to
	// change rather than only that something is wrong.
	cmd := exec.Command("git", "check-ignore", "-v", "--stdin")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(strings.Join(probes, "\n") + "\n")
	output, err := cmd.Output()

	// Exit 1 with no output is the passing case: no path was ignored. Exit 0 means
	// at least one was, and anything else is the command itself failing.
	if err == nil {
		t.Fatalf("a new source file would be silently ignored in %d location(s):\n%s\n"+
			"Each line is <ignore file>:<line>:<pattern>\\t<path>. A pattern with no "+
			"leading slash\nmatches that name at any depth — anchor it, as /bin/ and "+
			"/trustvian are.",
			len(strings.Split(strings.TrimSpace(string(output)), "\n")), output)
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running git check-ignore: %v", err)
	}
	if exit.ExitCode() != 1 {
		t.Fatalf("git check-ignore exited %d: %v\n%s\n%s",
			exit.ExitCode(), err, output, exit.Stderr)
	}
}

// TestTheRootBuildOutputIsStillIgnored is the other half.
//
// Anchoring the pattern must not stop it doing its job: `go build` in the
// repository root writes an executable named `trustvian`, and a stray build
// artifact in `git status` is what the entry is for.
func TestTheRootBuildOutputIsStillIgnored(t *testing.T) {
	root := repositoryRootForIgnoreTest(t)

	for _, path := range []string{"trustvian", "bin/trustvian"} {
		cmd := exec.Command("git", "check-ignore", "-v", "--no-index", path)
		cmd.Dir = root
		output, err := cmd.Output()
		if err != nil {
			t.Errorf("%s is not ignored, so a build artifact would show up in "+
				"git status: %v\n%s", path, err, output)
		}
	}
}

// trackedGoDirectories lists every directory holding a tracked Go file.
//
// Derived rather than hard-coded: a list would have to be updated by whoever adds
// a package, which is whoever is least likely to be thinking about .gitignore.
func trackedGoDirectories(t *testing.T, root string) []string {
	t.Helper()

	cmd := exec.Command("git", "ls-files", "-z", "*.go")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing tracked Go files: %v", err)
	}

	seen := map[string]bool{}
	for _, file := range strings.Split(strings.TrimRight(string(output), "\x00"), "\x00") {
		if file == "" {
			continue
		}
		if dir := filepath.Dir(file); dir != "." {
			seen[dir] = true
		}
	}
	if len(seen) == 0 {
		t.Fatal("no tracked Go files found; this test is not checking anything")
	}

	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	return dirs
}

// repositoryRootForIgnoreTest resolves the repository these tests run against.
//
// The test binary runs with its package directory as the working directory, and
// the repository root is one level up — the same assumption check-modules.sh
// makes about its own location.
func repositoryRootForIgnoreTest(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}

	// A missing prerequisite is a skip on a developer's machine and a failure in
	// CI, where both are guaranteed. A guard that can skip in CI guards nothing —
	// which is the same lesson as cmd/trustvian's end-to-end test.
	unavailable := func(format string, args ...any) {
		t.Helper()
		if os.Getenv("GITHUB_ACTIONS") != "" {
			t.Fatalf("this test must not skip in CI — "+format, args...)
		}
		t.Skipf(format, args...)
	}
	if _, err := exec.LookPath("git"); err != nil {
		unavailable("git is not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err != nil {
		unavailable("no .gitignore at %s: %v", root, err)
	}
	// Not a git checkout — a release tarball, say. Nothing to check.
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		unavailable("%s is not a git checkout: %v", root, err)
	}
	return root
}
