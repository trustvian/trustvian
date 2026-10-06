package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestStatusPrintsTheDocumentUnchanged is the contract `trustvian status`
// shares with `dev --check` and the WebUI: the bytes printed are the bytes
// served, in either output mode, so the three can never disagree.
func TestStatusPrintsTheDocumentUnchanged(t *testing.T) {
	const document = `{"version":"1","read_at":"2026-10-06T09:00:00Z","collectors":[],` +
		`"engine":{"state":"unavailable","reason":"x"},"a_future_field":{"kept":true}}`
	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(http.StatusOK, document+"\n")
			result := runPlatformCLI(t, append(args, "--api-url", api.url())...)
			result.mustExit(t, exitOK, "status")
			if result.stdout != document+"\n" {
				t.Fatalf("stdout:\n got %q\nwant %q", result.stdout, document+"\n")
			}
			request := api.only()
			if request.method != http.MethodGet || request.escapedPath != "/v1/status" {
				t.Fatalf("request %s %s", request.method, request.escapedPath)
			}
		})
	}
}

func TestStatusExitCodes(t *testing.T) {
	t.Run("an unreachable control plane is operational", func(t *testing.T) {
		result := runPlatformCLI(t, "status", "--api-url", "http://127.0.0.1:1")
		result.mustExit(t, exitOperational, "unreachable")
		if result.stdout != "" {
			t.Fatalf("stdout carried output on failure: %q", result.stdout)
		}
	})
	t.Run("a server error is operational", func(t *testing.T) {
		api := newFakeAPI(t)
		api.reply(http.StatusInternalServerError, `{"version":"1","error":{"code":"internal","message":"x"}}`)
		runPlatformCLI(t, "status", "--api-url", api.url()).mustExit(t, exitOperational, "500")
	})
	t.Run("a positional argument is a usage error", func(t *testing.T) {
		runPlatformCLI(t, "status", "extra", "--api-url", "http://127.0.0.1:1").mustExit(t, exitUsage, "usage")
	})
}
