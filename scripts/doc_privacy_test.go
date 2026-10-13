// This file checks that no Markdown in the repository carries a path from the
// machine it was written on.
//
// Pasting real command output into a document is how the docs stay accurate,
// and it is also how a home directory (and with it a username) ends up
// published. Example output names a placeholder or uses ~ instead; see
// .claude/rules/docs.md.
package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// homePath matches a per-user home directory on macOS, Linux and Windows.
	homePath = regexp.MustCompile(`(?:/Users/|/home/|[A-Za-z]:\\+Users\\+)([^/\\\s"'` + "`" + `)]+)`)
	// tempPath matches a macOS per-user temporary directory, whose random
	// segment is stable for a machine.
	tempPath = regexp.MustCompile(`/var/folders/[^/\s]+/[^/\s]+`)
)

// placeholderUsers are the names a document may put in a home path: generic
// placeholders, and runner, the GitHub-hosted runner's real account.
var placeholderUsers = map[string]bool{
	"you": true, "user": true, "username": true, "me": true,
	"runner": true, "$USER": true, "${USER}": true, "<user>": true, "<you>": true,
}

// machinePaths returns every machine-specific path in content.
func machinePaths(content string) []string {
	var found []string
	for _, m := range homePath.FindAllStringSubmatch(content, -1) {
		if !placeholderUsers[m[1]] {
			found = append(found, m[0])
		}
	}
	return append(found, tempPath.FindAllString(content, -1)...)
}

func TestMarkdownCarriesNoMachinePaths(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--", "*.md").Output()
	if err != nil {
		t.Fatalf("listing tracked Markdown: %v", err)
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(files) == 0 || files[0] == "" {
		t.Fatal("no tracked Markdown found")
	}
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for i, line := range bytes.Split(content, []byte("\n")) {
			for _, p := range machinePaths(string(line)) {
				t.Errorf("%s:%d: machine-specific path %q; use ~ or a placeholder such as /Users/you", name, i+1, p)
			}
		}
	}
}

func TestMachinePaths(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"macOS home", "State /Users/alice/.trustvian/dev", []string{"/Users/alice"}},
		{"Linux home", "cd /home/bob/src", []string{"/home/bob"}},
		{"Windows home", `C:\Users\carol\AppData`, []string{`C:\Users\carol`}},
		{"macOS temp dir", "/var/folders/xy/abc123/T/go-build", []string{"/var/folders/xy/abc123"}},
		{"tilde", "~/.trustvian/dev/<hash>/", nil},
		{"placeholder", "/Users/you/.trustvian and /home/user/x", nil},
		{"GitHub runner", "/home/runner/work/trustvian", nil},
		{"env var", "/home/$USER/.config", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := machinePaths(tt.in)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("machinePaths(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
