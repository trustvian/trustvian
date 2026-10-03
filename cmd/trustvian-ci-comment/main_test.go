package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunRefusesMalformedInput(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "body.md")
	if err := os.WriteFile(body, []byte("rendering\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.md")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), maxCommentBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(dir, "invalid.md")
	if err := os.WriteFile(invalid, []byte("bad \xff byte"), 0o644); err != nil {
		t.Fatal(err)
	}
	valid := []string{"--repository", "trustvian/trustvian", "--pull-request", "7", "--head-sha", headSHA,
		"--marker-id", "trustvian-run", "--body-file", body}
	with := func(flag, value string) []string {
		out := append([]string{}, valid...)
		for i := range out {
			if out[i] == flag {
				out[i+1] = value
			}
		}
		return out
	}
	goodEnv := map[string]string{"GITHUB_TOKEN": testToken, "GITHUB_API_URL": "https://api.github.com"}
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"no flags", nil, goodEnv},
		{"a stray argument", append(append([]string{}, valid...), "extra"), goodEnv},
		{"a testToken flag", append(append([]string{}, valid...), "--testToken", testToken), goodEnv},
		{"a repository without an owner", with("--repository", "trustvian"), goodEnv},
		{"a dot-dot repository", with("--repository", "../trustvian"), goodEnv},
		{"a zero pull request", with("--pull-request", "0"), goodEnv},
		{"a pull request with a path", with("--pull-request", "7/../8"), goodEnv},
		{"a short head", with("--head-sha", "0123abc"), goodEnv},
		{"a marker id that closes the comment", with("--marker-id", "x-->"), goodEnv},
		{"a marker id with a space", with("--marker-id", "a b"), goodEnv},
		{"no body file", with("--body-file", ""), goodEnv},
		{"a missing body file", with("--body-file", filepath.Join(dir, "absent")), goodEnv},
		{"a body over the limit", with("--body-file", big), goodEnv},
		{"a body that is not UTF-8", with("--body-file", invalid), goodEnv},
		{"no testToken", valid, map[string]string{"GITHUB_API_URL": "https://api.github.com"}},
		{"an http API", valid, map[string]string{"GITHUB_TOKEN": testToken, "GITHUB_API_URL": "http://api.github.com"}},
		{"an API with a path", valid, map[string]string{"GITHUB_TOKEN": testToken, "GITHUB_API_URL": "https://ghe.example/api/v3"}},
		{"an API with credentials", valid, map[string]string{"GITHUB_TOKEN": testToken, "GITHUB_API_URL": "https://u:p@api.github.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			client := &http.Client{Transport: refuseTransport{t}}
			got := run(tt.args, func(k string) string { return tt.env[k] }, &stdout, &stderr, client,
				func(time.Duration) {})
			if got != exitUsage {
				t.Errorf("exit %d, want %d\n%s", got, exitUsage, stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), testToken) {
				t.Error("the testToken reached the output")
			}
		})
	}
}

// refuseTransport fails the test if a malformed invocation reaches the
// network at all.
type refuseTransport struct{ t *testing.T }

func (r refuseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("a malformed invocation sent %s %s", req.Method, req.URL)
	return nil, http.ErrHandlerTimeout
}

// The poster may reach GitHub and nothing else: its sources import no
// process, plugin, reflection or unsafe package, and it is built from the
// standard library alone.
func TestPosterImportsOnlyWhatItNeeds(t *testing.T) {
	allowed := map[string]bool{
		"bytes": true, "context": true, "encoding/json": true, "errors": true, "flag": true, "fmt": true,
		"io": true, "net/http": true, "os": true, "regexp": true, "strconv": true, "strings": true,
		"time": true, "unicode/utf8": true,
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
	for _, dep := range strings.Fields(string(out)) {
		if dep != "github.com/trustvian/trustvian/cmd/trustvian-ci-comment" {
			t.Errorf("the poster depends on %s, which is not the standard library", dep)
		}
	}
}
