package main

// Legacy exit-code regression.
//
// The existing tests assert "non-zero" in most places, which was enough while
// there was one meaning for non-zero. Task 060 gives exit 1 a second meaning
// on `eval compare`, so the legacy codes need pinning to exact values: the
// failure this guards against is a later refactor routing analyze or baseline
// through the new framework and quietly turning a 1 into a 3.
//
// These record what the CLI *does*, which is not quite the tidy story of
// "0 success / 1 runtime / 2 usage". Only top-level dispatch returns 2; a
// subcommand's own usage error has always returned 1. That is released
// behavior, and the point of this file is that it stays released behavior —
// not that it is the scheme anyone would design today.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyExitCodesAreUnchanged(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	malformed := filepath.Join(t.TempDir(), "malformed.json")
	writeFile(t, malformed, "{ this is not json")

	tests := []struct {
		name string
		args []string
		want int
	}{
		// Top-level dispatch: the only place 2 is produced today.
		{"no arguments", nil, 2},
		{"unknown command", []string{"nope"}, 2},
		{"help", []string{"help"}, 0},
		{"--help", []string{"--help"}, 0},

		// version
		{"version", []string{"version"}, 0},
		{"--version alias", []string{"--version"}, 0},
		{"-v alias", []string{"-v"}, 0},
		{"version with an extra argument", []string{"version", "extra"}, 1},

		// analyze: every failure is 1, including its own usage errors.
		{"analyze without a file", []string{"analyze"}, 1},
		{"analyze with an unknown flag", []string{"analyze", "--nope", "x.json"}, 1},
		{"analyze with a missing file", []string{"analyze", missing}, 1},
		{"analyze with malformed JSON", []string{"analyze", malformed}, 1},
		{"analyze with a missing config", []string{"analyze", "--config", missing, malformed}, 1},

		// baseline
		{"baseline without a subcommand", []string{"baseline"}, 1},
		{"baseline with a wrong subcommand", []string{"baseline", "rebuild"}, 1},
		{"baseline build without a file", []string{"baseline", "build"}, 1},
		{"baseline build with a missing file", []string{"baseline", "build", missing}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, code := captureOutput(t, func() int { return run(tt.args) })
			if code != tt.want {
				t.Fatalf("exit = %d, want %d; stderr = %q", code, tt.want, stderr)
			}
			// The specific regression: a legacy failure must never acquire the
			// new operational code.
			if code == exitOperational {
				t.Fatalf("a legacy command returned %d, which now means an API failure",
					exitOperational)
			}
		})
	}
}

// TestLegacySuccessPathsAreUnchanged covers the commands that must still work
// end to end, since adding a dispatch branch ahead of them is exactly how one
// would stop being reachable.
func TestLegacySuccessPathsAreUnchanged(t *testing.T) {
	events := filepath.Join(t.TempDir(), "events.json")
	writeFile(t, events, `[{"id":"evt-1","timestamp":"2026-01-01T12:00:00Z",`+
		`"actor":{"id":"svc-payment","type":"service","identity_confidence":0.95},`+
		`"operation":{"category":"http","name":"POST /payment"},`+
		`"target":{"name":"payment-db"},"context":{"environment":"production"}}]`)

	t.Run("analyze", func(t *testing.T) {
		stdout, stderr, code := captureOutput(t, func() int { return run([]string{"analyze", events}) })
		if code != 0 {
			t.Fatalf("exit = %d, want 0; stderr = %q", code, stderr)
		}
		if !strings.Contains(stdout, "Trustvian Behavioral Analysis") {
			t.Errorf("analyze output changed:\n%s", stdout)
		}
	})

	t.Run("baseline build", func(t *testing.T) {
		stdout, stderr, code := captureOutput(t, func() int {
			return run([]string{"baseline", "build", events})
		})
		if code != 0 {
			t.Fatalf("exit = %d, want 0; stderr = %q", code, stderr)
		}
		if !strings.Contains(stdout, "Trustvian Baseline Build") {
			t.Errorf("baseline output changed:\n%s", stdout)
		}
	})

	t.Run("legacy flags still parse", func(t *testing.T) {
		// Named explicitly so a rename shows up here rather than in a user's
		// script: these three are released operational surface.
		for _, flag := range []string{"--config", "--anomaly-config", "--storage-config"} {
			_, _, code := captureOutput(t, func() int {
				return run([]string{"analyze", flag, "/nonexistent.yaml", events})
			})
			// The flag is recognised; the file is not. Exit 1, not a usage
			// error about an unknown flag.
			if code != 1 {
				t.Errorf("analyze %s: exit = %d, want 1", flag, code)
			}
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
