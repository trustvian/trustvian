package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInvocationErrors keeps bad flags at exit 2 and off the filesystem.
func TestInvocationErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"--nope"}},
		{"trailing argument", []string{"--state-dir", "x", "oops"}},
		{"bare positional", []string{"serve"}},

		// A refused listen address is the caller's mistake, not the
		// environment's, so it is usage rather than operational.
		{"all interfaces", []string{"--listen", "0.0.0.0:0"}},
		{"all interfaces ipv6", []string{"--listen", "[::]:0"}},
		{"private address", []string{"--listen", "192.168.1.10:0"}},
		{"hostname", []string{"--listen", "localhost:0"}},
		{"no port", []string{"--listen", "127.0.0.1"}},

		// A pricing table that cannot be used is refused before anything
		// starts, never ignored (task 087).
		{"missing pricing file", []string{"--pricing", "does-not-exist.yaml"}},
		{"invalid pricing file", []string{"--pricing", writeFile(t, "version: 2\n")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"--state-dir", filepath.Join(dir, ".trustvian")}, tt.args...)

			var stdout, stderr bytes.Buffer
			code := run(args, &stdout, &stderr)

			if code != exitUsage {
				t.Fatalf("exit = %d, want %d; stderr = %s", code, exitUsage, stderr.String())
			}
			if code == 1 {
				t.Fatal("exit 1 means gate FAIL and must never come from the runtime")
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if stderr.Len() == 0 {
				t.Error("stderr is empty; a refused invocation must be diagnosed")
			}
		})
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pricing.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestUsageTextDoesNotOfferRemoteExposure guards against a flag that would
// undo the only security boundary this runtime has.
func TestUsageTextDoesNotOfferRemoteExposure(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, forbidden := range []string{
		"allow-remote", "insecure", "--public", "0.0.0.0", "token", "api-key", "tls",
	} {
		if strings.Contains(strings.ToLower(string(source)), forbidden) {
			t.Errorf("main.go mentions %q; this runtime is unauthenticated and "+
				"loopback-only by design", forbidden)
		}
	}
}

// TestStartupOutputAdvertisesTheWebUI covers the one user-visible change task
// 063 makes to this executable.
//
// The Web URL is the same origin as the API — the UI shares the listener — but
// it gets its own line anyway. A developer looking for somewhere to click
// should not have to work out that the API endpoint is also a web page.
func TestStartupOutputAdvertisesTheWebUI(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(source)

	for _, required := range []string{`"Web:   %s\n"`, "runtime.WebURL()"} {
		if !strings.Contains(text, required) {
			t.Errorf("main.go does not print the Web URL (missing %s)", required)
		}
	}
	// Order matters for readability: API, then Web, then State.
	api := strings.Index(text, `"API:   %s\n"`)
	web := strings.Index(text, `"Web:   %s\n"`)
	state := strings.Index(text, `"State: %s\n"`)
	if api < 0 || web < 0 || state < 0 || !(api < web && web < state) {
		t.Error("the startup lines are not in API → Web → State order")
	}
}

// TestNoBrowserIsLaunched keeps startup inert.
//
// A security tool that opens windows by itself is a surprise, and an --open
// flag would need OS-specific launching this task has no reason to carry.
func TestNoBrowserIsLaunched(t *testing.T) {
	// Comments stripped: main.go states in prose that there is no --open flag,
	// and that statement must not be what trips the check that there isn't one.
	// Weakening the pattern instead would stop it catching a real flag.
	text := strings.ToLower(goSourceWithoutComments(t, "main.go"))

	for _, forbidden := range []string{
		"exec.command", "xdg-open", "rundll32", "browser.open", "webbrowser",
		// The flag itself, as it would actually be registered.
		`"open"`, `"browser"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("main.go contains %q outside a comment; no browser is launched "+
				"and no --open flag exists", forbidden)
		}
	}
}

// goSourceWithoutComments returns a Go file's source with comments blanked.
//
// Byte ranges are blanked rather than removed so reported positions still line
// up with the file.
func goSourceWithoutComments(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, body, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}

	stripped := []byte(string(body))
	base := fset.File(parsed.Pos()).Base()
	for _, group := range parsed.Comments {
		for i := int(group.Pos()) - base; i < int(group.End())-base && i < len(stripped); i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	return string(stripped)
}

// --version prints the version this binary reports as a producer: the release
// version a release build is stamped with, or Go's build information
// (finding F7 of #175).
func TestVersionFlagPrintsTheReleaseVersion(t *testing.T) {
	for _, tt := range []struct{ stamped, want string }{
		{"v0.12.1", "trustvian-local v0.12.1"},
		{"", "trustvian-local "},
	} {
		version = tt.stamped
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--version"}, &stdout, &stderr); code != exitOK {
			t.Fatalf("--version exited %d: %s", code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), tt.want) {
			t.Errorf("stamped %q: --version printed %q, want it to start with %q", tt.stamped, stdout.String(), tt.want)
		}
	}
	version = ""
}
