//go:build !windows

package main

// A bare command name resolves against the workload's PATH, not dev's (task
// 078). A scenario side that sets PATH to its own virtualenv must run that
// virtualenv's executable; exec.Command alone resolves against this process's
// PATH before the child's environment is assigned.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const sameNamedTool = "tv-same-named-tool"

// toolDirs creates parent, reference and candidate directories, each holding
// an executable named sameNamedTool that writes its own label to $OUT, and an
// empty directory that holds none. The parent's is put first on this
// process's PATH.
func toolDirs(t *testing.T) (parent, reference, candidate, empty string) {
	t.Helper()
	root := t.TempDir()
	for _, label := range []string{"parent", "reference", "candidate", "empty"} {
		dir := filepath.Join(root, label)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if label != "empty" {
			writeScript(t, filepath.Join(dir, sameNamedTool), "printf "+label+` > "$OUT"`+"\n")
		}
	}
	t.Setenv("PATH", filepath.Join(root, "parent")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(root, "parent"), filepath.Join(root, "reference"),
		filepath.Join(root, "candidate"), filepath.Join(root, "empty")
}

func runSameNamedTool(t *testing.T, command string, env map[string]string) (childOutcome, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "ran")
	withDevStdio(t, nil, newTempFile(t, dir, "stdout"), newTempFile(t, dir, "stderr"))
	merged := map[string]string{"OUT": out}
	for name, value := range env {
		merged[name] = value
	}
	config := devConfig{env: merged}
	outcome := superviseChild(streams{out: io_Discard{}, err: io_Discard{}},
		[]string{command}, config.inheritedEnvironment(), nil)
	return outcome, readPath(t, out)
}

func TestABareCommandResolvesAgainstEachSidesPath(t *testing.T) {
	_, reference, candidate, empty := toolDirs(t)

	for _, tc := range []struct {
		name    string
		command string
		env     map[string]string
		want    string
	}{
		{"no override inherits the parent's PATH", sameNamedTool, nil, "parent"},
		{"the reference side's PATH", sameNamedTool, map[string]string{"PATH": reference}, "reference"},
		{"the candidate side's PATH", sameNamedTool, map[string]string{"PATH": candidate}, "candidate"},
		{"an explicit path is used as given", filepath.Join(candidate, sameNamedTool),
			map[string]string{"PATH": reference}, "candidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, ran := runSameNamedTool(t, tc.command, tc.env)
			if outcome.code != 0 || ran != tc.want {
				t.Errorf("exit %d, ran the %q executable; want 0 and %q", outcome.code, ran, tc.want)
			}
		})
	}

	t.Run("absent from the side's PATH is not found, even where the parent has it", func(t *testing.T) {
		outcome, ran := runSameNamedTool(t, sameNamedTool, map[string]string{"PATH": empty})
		if !outcome.startFailed || ran != "" {
			t.Errorf("startFailed %v, ran %q; want a start failure and nothing run", outcome.startFailed, ran)
		}
	})
}

// exec's safety rule survives the replacement lookup: a match found through a
// relative PATH entry — the current directory — is refused, not run.
func TestLookPathInRefusesAMatchRelativeToTheCurrentDirectory(t *testing.T) {
	_, reference, _, _ := toolDirs(t)
	t.Chdir(reference)
	for _, path := range []string{".", ":", "relative/../."} {
		if _, err := lookPathIn(sameNamedTool, []string{"PATH=" + path}); !errors.Is(err, exec.ErrDot) {
			t.Errorf("PATH=%q: error = %v, want exec.ErrDot", path, err)
		}
	}
	if got, err := lookPathIn(sameNamedTool, []string{"PATH=" + reference}); err != nil ||
		got != filepath.Join(reference, sameNamedTool) {
		t.Errorf("absolute entry: %q, %v", got, err)
	}
	if _, err := lookPathIn(sameNamedTool, nil); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("no PATH at all: error = %v, want exec.ErrNotFound", err)
	}
	// A directory or a non-executable file of that name is skipped.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a", sameNamedTool), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sameNamedTool), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := lookPathIn(sameNamedTool, []string{"PATH=" + filepath.Join(dir, "a") + ":" + dir + ":" + reference})
	if err != nil || got != filepath.Join(reference, sameNamedTool) {
		t.Errorf("skipping a directory and a non-executable: %q, %v", got, err)
	}
}

// An execute bit is not permission to execute. The review's reproduction: the
// first PATH entry holds a 0601 file this user owns — executable by others,
// not by its owner — and the second a 0700 one. exec.LookPath skips the first;
// so must the workload's lookup, rather than choosing it and failing to start.
func TestLookupSkipsAMatchThisUserCannotExecute(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may execute a file with any execute bit, so 0601 is runnable")
	}
	_, _, _, empty := toolDirs(t)
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(t, filepath.Join(first, sameNamedTool), `printf first > "$OUT"`+"\n")
	writeScript(t, filepath.Join(second, sameNamedTool), `printf second > "$OUT"`+"\n")
	if err := os.Chmod(filepath.Join(first, sameNamedTool), 0o601); err != nil {
		t.Fatal(err)
	}
	searchPath := first + string(os.PathListSeparator) + second

	// The standard lookup is the reference this one must agree with.
	t.Setenv("PATH", searchPath)
	want, err := exec.LookPath(sameNamedTool)
	if err != nil || want != filepath.Join(second, sameNamedTool) {
		t.Fatalf("exec.LookPath = %q, %v; the fixture is not the reproduction", want, err)
	}
	if got, err := lookPathIn(sameNamedTool, []string{"PATH=" + searchPath}); err != nil || got != want {
		t.Errorf("lookPathIn = %q, %v; want %q, as exec.LookPath chooses", got, err, want)
	}
	toolDirs(t) // the parent's PATH again holds a runnable tool of that name

	outcome, ran := runSameNamedTool(t, sameNamedTool, map[string]string{"PATH": searchPath})
	if outcome.code != 0 || ran != "second" {
		t.Errorf("exit %d, ran %q; want 0 and the second entry's executable", outcome.code, ran)
	}

	// Only the unusable match on the side's PATH: a clean start failure, and
	// the parent's runnable executable of the same name is not a fallback.
	outcome, ran = runSameNamedTool(t, sameNamedTool,
		map[string]string{"PATH": first + string(os.PathListSeparator) + empty})
	if !outcome.startFailed || ran != "" {
		t.Errorf("startFailed %v, ran %q; want a start failure and nothing run", outcome.startFailed, ran)
	}
	if _, err := lookPathIn(sameNamedTool, []string{"PATH=" + first}); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("only an unusable match: error = %v, want exec.ErrNotFound", err)
	}
}
