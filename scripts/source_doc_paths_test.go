//go:build !windows

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var docPath = regexp.MustCompile(`docs/[A-Za-z0-9_./-]+\.md`)

// The examples and the module files cite the task or document they come
// from, and those documents move: finished tasks go to docs/archive/tasks/.
// A citation that no longer resolves sends a reader nowhere (finding F17 of
// #175). Scoped to the files that finding named: the examples module and every
// go.mod. Other packages still cite pre-archive task paths.
func TestExamplesAndModuleFilesCiteExistingDocs(t *testing.T) {
	root := repositoryRootForIgnoreTest(t)
	cmd := exec.Command("git", "ls-files", "-z", "examples/*.go", "go.mod", "*/go.mod")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing files: %v", err)
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(files) < 4 {
		t.Fatalf("found only %d files; this test is not checking anything", len(files))
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, cited := range docPath.FindAllString(string(data), -1) {
			if _, err := os.Stat(filepath.Join(root, cited)); err != nil {
				t.Errorf("%s cites %s, which does not exist", file, cited)
			}
		}
	}
}
