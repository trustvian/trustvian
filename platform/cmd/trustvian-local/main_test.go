package main

import (
	"bytes"
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
