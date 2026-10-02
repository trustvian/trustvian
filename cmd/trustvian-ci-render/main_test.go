package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func cliArgs(dir, exitCode string, extra ...string) []string {
	return append([]string{"--artifact-dir", dir, "--head-sha", fixtureHead, "--repository", "trustvian/trustvian",
		"--run-id", "1000000001", "--run-attempt", "1", "--exit-code", exitCode}, extra...)
}

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		artifact string
		code     string
		want     int
		stdout   string
	}{
		{"a PASS", "pass", "0", exitRendered, "gate — PASS"},
		{"a FAIL", "fail", "1", exitRendered, "gate — FAIL"},
		{"a suite", "suite-error", "3", exitRendered, "suite — CLI exit code 3"},
		{"exit 2", "usage", "2", exitNoVerdict, "no verdict"},
		{"exit 3", "operational", "3", exitNoVerdict, "no verdict"},
		{"no exit code", "pass", "", exitNoVerdict, "no verdict"},
		{"an artifact for another exit", "pass", "1", exitRejected, "no verdict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(cliArgs(filepath.Join("testdata", "artifacts", tt.artifact), tt.code), &stdout, &stderr)
			if got != tt.want {
				t.Errorf("exit %d, want %d; stderr %s", got, tt.want, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.stdout) {
				t.Errorf("stdout lacks %q:\n%s", tt.stdout, stdout.String())
			}
			if got != exitRendered && !strings.HasPrefix(stderr.String(), "trustvian-ci-render: no verdict: ") {
				t.Errorf("the reason is not on stderr: %q", stderr.String())
			}
		})
	}
}

// The expected context is the caller's, and a malformed one renders nothing:
// there is no commit to name and no link to trust.
func TestRunRefusesAMalformedContext(t *testing.T) {
	dir := filepath.Join("testdata", "artifacts", "pass")
	tests := []struct {
		name string
		args []string
	}{
		{"no flags", nil},
		{"no exit-code flag", cliArgs(dir, "0")[:10]},
		{"a short head", replaceArg(cliArgs(dir, "0"), "--head-sha", "0123abc")},
		{"a head between the two formats", replaceArg(cliArgs(dir, "0"), "--head-sha", fixtureHead+"0123456789")},
		{"an upper-case head", replaceArg(cliArgs(dir, "0"), "--head-sha", strings.ToUpper(fixtureHead))},
		{"a repository without an owner", replaceArg(cliArgs(dir, "0"), "--repository", "trustvian")},
		{"a dot-dot repository", replaceArg(cliArgs(dir, "0"), "--repository", "../trustvian")},
		{"a repository with a path", replaceArg(cliArgs(dir, "0"), "--repository", "a/b/c")},
		{"a markdown repository", replaceArg(cliArgs(dir, "0"), "--repository", "a/b)[x](y")},
		{"a zero run id", replaceArg(cliArgs(dir, "0"), "--run-id", "0")},
		{"a run id with a path", replaceArg(cliArgs(dir, "0"), "--run-id", "1/../../x")},
		{"an attempt that is not a number", replaceArg(cliArgs(dir, "0"), "--run-attempt", "first")},
		{"an exit code that is not a number", cliArgs(dir, "zero")},
		{"an exit code over 255", cliArgs(dir, "256")},
		{"an http server", cliArgs(dir, "0", "--server-url", "http://github.com")},
		{"a server with a path", cliArgs(dir, "0", "--server-url", "https://github.com/evil")},
		{"a server with a query", cliArgs(dir, "0", "--server-url", "https://github.com?x")},
		{"an empty artifact dir", replaceArg(cliArgs(dir, "0"), "--artifact-dir", "")},
		{"a stray argument", append(cliArgs(dir, "0"), "extra")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tt.args, &stdout, &stderr); got != exitUsage {
				t.Errorf("exit %d, want %d", got, exitUsage)
			}
			if stdout.Len() != 0 {
				t.Errorf("rendered with a malformed context:\n%s", stdout.String())
			}
		})
	}
	// An enterprise server is a host, and it is what the link names.
	var stdout bytes.Buffer
	if got := run(cliArgs(dir, "0", "--server-url", "https://github.example.com:8443"), &stdout, &bytes.Buffer{}); got != exitRendered ||
		!strings.Contains(stdout.String(), "(https://github.example.com:8443/trustvian/trustvian/actions/runs/1000000001/attempts/1)") {
		t.Errorf("exit %d:\n%s", got, stdout.String())
	}
}

func replaceArg(args []string, flag, value string) []string {
	out := append([]string{}, args...)
	for i := range out {
		if out[i] == flag {
			out[i+1] = value
		}
	}
	return out
}

// The renderer can execute nothing and reach nothing: its sources import no
// process, network, plugin or unsafe package, and it is built from the
// standard library alone.
func TestRendererImportsOnlyWhatItNeeds(t *testing.T) {
	allowed := map[string]bool{
		"bytes": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true, "errors": true,
		"flag": true, "fmt": true, "io": true, "io/fs": true, "os": true, "path/filepath": true,
		"regexp": true, "strconv": true, "strings": true, "unicode": true, "unicode/utf8": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !allowed[path] {
				t.Errorf("%s imports %s", file, path)
			}
		}
	}

	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Fields(string(out)) {
		if line != "github.com/trustvian/trustvian/cmd/trustvian-ci-render" {
			t.Errorf("the renderer depends on %s, which is not the standard library", line)
		}
	}
}

// Counts, limits and thresholds are text from the document, never numbers
// this renderer computes with: nothing outside main.go — which parses only
// the caller's --exit-code — converts text to a number.
func TestRendererParsesNoCount(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "main.go" {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"strconv.", "Sscan", "ParseUint", "ParseInt", "Atoi"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s uses %s", file, banned)
			}
		}
	}
}
