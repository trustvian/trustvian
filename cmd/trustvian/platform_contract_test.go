package main

// The contracts a CI job depends on: exit codes, bounds, and the two
// compatibility properties that fail silently when broken.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// eval compare exit codes
// ---------------------------------------------------------------------

func compareArgs(apiURL string, extra ...string) []string {
	return append([]string{
		"eval", "compare", "--api-url", apiURL,
		"--reference-run", "ref", "--candidate-run", "cand",
		"--max-added-behaviors", "0", "--max-block-decisions", "0",
		"--max-critical-risk-observations", "0",
	}, extra...)
}

// TestCompareExitCodeMatrix is the load-bearing test of this task.
//
// Exit 1 must mean gate FAIL and nothing else. CI scripts are written as
// "non-zero means the gate failed"; if a 409 or a timeout could also produce
// 1, a broken network would be reported to a team as a policy violation — or,
// in the direction that actually hurts, they learn to ignore the code that
// means their agent regressed.
func TestCompareExitCodeMatrix(t *testing.T) {
	tests := []struct {
		name     string
		wantExit int
		// serve replaces the canned reply when set.
		serve http.HandlerFunc
		reply *struct {
			status int
			body   string
		}
		extraArgs []string
		omitLimit string
	}{
		{
			name: "gate pass", wantExit: exitOK,
			reply: &struct {
				status int
				body   string
			}{200, comparisonBody(gateVerdictPass, 0, "0")},
		},
		{
			name: "gate fail", wantExit: exitGateFail,
			reply: &struct {
				status int
				body   string
			}{200, comparisonBody(gateVerdictFail, 3, "0")},
		},

		// Every API condition is operational. None may collapse into 1.
		{
			name: "HTTP 400", wantExit: exitOperational,
			reply: &struct {
				status int
				body   string
			}{400, `{"version":"1","error":{"code":"invalid_request","message":"bad"}}`},
		},
		{
			name: "HTTP 404", wantExit: exitOperational,
			reply: &struct {
				status int
				body   string
			}{404, `{"version":"1","error":{"code":"not_found","message":"missing"}}`},
		},
		{
			name: "HTTP 409", wantExit: exitOperational,
			reply: &struct {
				status int
				body   string
			}{409, `{"version":"1","error":{"code":"conflict","message":"still running"}}`},
		},
		{
			name: "HTTP 500", wantExit: exitOperational,
			reply: &struct {
				status int
				body   string
			}{500, `{"version":"1","error":{"code":"internal","message":"internal server error"}}`},
		},
		{
			name: "malformed response", wantExit: exitOperational,
			reply: &struct {
				status int
				body   string
			}{200, `{"gate": THIS IS NOT JSON`},
		},
		{
			name: "empty response", wantExit: exitOperational,
			reply: &struct {
				status int
				body   string
			}{200, ``},
		},

		// Local invocation problems are usage, never gate or operational.
		{
			name: "unknown flag", wantExit: exitUsage, extraArgs: []string{"--nope"},
		},
		{
			name: "negative limit", wantExit: exitUsage,
			extraArgs: []string{"--max-added-behaviors", "-1"},
		},
		{
			name: "overflowing limit", wantExit: exitUsage,
			extraArgs: []string{"--max-added-behaviors", "18446744073709551616"},
		},
		{
			name: "missing required limit", wantExit: exitUsage, omitLimit: "--max-block-decisions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			if tt.reply != nil {
				api.reply(tt.reply.status, tt.reply.body)
			}
			if tt.serve != nil {
				api.serve(tt.serve)
			}

			args := compareArgs(api.url(), tt.extraArgs...)
			if tt.omitLimit != "" {
				args = withoutFlag(args, tt.omitLimit)
			}

			result := runPlatformCLI(t, args...)
			result.mustExit(t, tt.wantExit, tt.name)

			// The specific inversion this table exists to prevent.
			if tt.wantExit != exitGateFail && result.code == exitGateFail {
				t.Fatalf("%s produced exit 1, which means gate FAIL", tt.name)
			}
		})
	}
}

// withoutFlag drops a flag and its value from an argument list.
func withoutFlag(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			i++ // skip its value
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// TestCompareTimeoutIsOperational keeps a hung server out of the gate result.
func TestCompareTimeoutIsOperational(t *testing.T) {
	release := make(chan struct{})

	api := newFakeAPI(t)
	// Registered *after* newFakeAPI so it runs *before* the server's own
	// cleanup: t.Cleanup is LIFO, and httptest.Server.Close blocks until every
	// in-flight handler returns. Releasing second deadlocks the whole package
	// until the test binary's timeout — which is how this was found.
	t.Cleanup(func() { close(release) })

	api.serve(func(w http.ResponseWriter, r *http.Request) {
		<-release // never replies within the command's timeout
	})

	result := runPlatformCLITimeout(t, 100*time.Millisecond, compareArgs(api.url())...)
	result.mustExit(t, exitOperational, "timed-out compare")
	if result.stdout != "" {
		t.Errorf("stdout = %q, want empty on a timeout", result.stdout)
	}
}

// TestCompareRedirectIsRefused proves a mutation cannot be re-addressed.
func TestCompareRedirectIsRefused(t *testing.T) {
	destination := newFakeAPI(t)
	destination.reply(200, comparisonBody(gateVerdictPass, 0, "0"))

	origin := newFakeAPI(t)
	origin.serve(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.url()+"/v1/evaluations/compare", http.StatusFound)
	})

	result := runPlatformCLI(t, compareArgs(origin.url())...)
	result.mustExit(t, exitOperational, "redirected compare")

	// The point: a comparison that "passed" somewhere else must not be
	// reported as this comparison passing.
	if got := destination.captured(); len(got) != 0 {
		t.Fatalf("redirect destination received %d requests, want 0", len(got))
	}
	if result.code == exitOK || result.code == exitGateFail {
		t.Fatal("a refused redirect produced a gate result")
	}
}

// ---------------------------------------------------------------------
// Gate limits
// ---------------------------------------------------------------------

// TestCompareGateLimitsAreRequired proves omitted is not zero.
//
// Zero is the strictest limit there is. Defaulting to it would fail a build
// under a policy the caller never chose, and — worse — would look exactly like
// a real regression.
func TestCompareGateLimitsAreRequired(t *testing.T) {
	limits := []string{
		"--max-added-behaviors", "--max-block-decisions", "--max-critical-risk-observations",
	}

	t.Run("all omitted", func(t *testing.T) {
		api := newFakeAPI(t)
		args := compareArgs(api.url())
		for _, limit := range limits {
			args = withoutFlag(args, limit)
		}
		runPlatformCLI(t, args...).mustExit(t, exitUsage, "no limits")
		if got := api.captured(); len(got) != 0 {
			t.Fatalf("server received %d requests, want 0", len(got))
		}
	})

	for _, omitted := range limits {
		t.Run("omitted "+omitted, func(t *testing.T) {
			api := newFakeAPI(t)
			runPlatformCLI(t, withoutFlag(compareArgs(api.url()), omitted)...).
				mustExit(t, exitUsage, omitted)
			if got := api.captured(); len(got) != 0 {
				t.Fatalf("server received %d requests, want 0", len(got))
			}
		})
	}
}

// TestCompareLimitValues pins what the wire carries for accepted values.
func TestCompareLimitValues(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantExit int
		wantWire string
	}{
		{"explicit zero", "0", exitOK, "0"},
		{"one", "1", exitOK, "1"},
		{"max uint64", "18446744073709551615", exitOK, "18446744073709551615"},

		{"negative", "-1", exitUsage, ""},
		{"overflow", "18446744073709551616", exitUsage, ""},
		{"non-numeric", "many", exitUsage, ""},
		{"leading zero is not canonical", "007", exitUsage, ""},
		{"explicit plus is not canonical", "+1", exitUsage, ""},
		{"empty", "", exitUsage, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, comparisonBody(gateVerdictPass, 0, "0"))

			args := withoutFlag(compareArgs(api.url()), "--max-added-behaviors")
			args = append(args, "--max-added-behaviors", tt.value)

			runPlatformCLI(t, args...).mustExit(t, tt.wantExit, tt.name)
			if tt.wantExit != exitOK {
				if got := api.captured(); len(got) != 0 {
					t.Fatalf("server received %d requests for a rejected value", len(got))
				}
				return
			}

			// Canonical decimal text, not a JSON number: uint64 exceeds what a
			// double holds exactly, and MaxUint64 would round.
			var body struct {
				GateLimits struct {
					MaxAddedBehaviors any `json:"max_added_behaviors"`
				} `json:"gate_limits"`
			}
			if err := json.Unmarshal(api.only().body, &body); err != nil {
				t.Fatalf("request body: %v", err)
			}
			got, isString := body.GateLimits.MaxAddedBehaviors.(string)
			if !isString {
				t.Fatalf("max_added_behaviors = %T, want a JSON string", body.GateLimits.MaxAddedBehaviors)
			}
			if got != tt.wantWire {
				t.Errorf("max_added_behaviors = %q, want %q", got, tt.wantWire)
			}
		})
	}
}

// TestCompareTrustsTheServersVerdict is the second-gate-implementation guard.
//
// The fixture is deliberately absurd: the server reports 7 added behaviors
// under a maximum of 0 and still returns PASS. No consistent local gate could
// produce that, so any CLI that computed its own answer must disagree — which
// is exactly what makes this catch one. The CLI's job is to report what the
// authority said, not to correct it.
func TestCompareTrustsTheServersVerdict(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, comparisonBody(gateVerdictPass, 7, "0"))

	result := runPlatformCLI(t, compareArgs(api.url())...)
	result.mustExit(t, exitOK, "server said pass with contradictory evidence")

	// And the contradictory evidence is printed rather than hidden.
	if !strings.Contains(result.stdout, "+7") {
		t.Errorf("stdout did not report the server's added count:\n%s", result.stdout)
	}
	if !strings.Contains(result.stdout, "PASS") {
		t.Errorf("stdout did not report the server's verdict:\n%s", result.stdout)
	}

	// And the inverse: failing evidence with a FAIL verdict exits 1.
	failing := newFakeAPI(t)
	failing.reply(200, comparisonBody(gateVerdictFail, 0, "99"))
	runPlatformCLI(t, compareArgs(failing.url())...).
		mustExit(t, exitGateFail, "server said fail with passing-looking evidence")
}

// ---------------------------------------------------------------------
// Forward compatibility
// ---------------------------------------------------------------------

// TestIngestPreservesUnknownRecordFields is the DecisionRecord guard.
//
// This fails the moment someone "tidies" eval ingest into decoding the record
// as trustvian.DecisionRecord. That change compiles, passes every test that
// checks only known fields, and silently truncates records from any newer
// producer — with no error anywhere, on either side.
func TestIngestPreservesUnknownRecordFields(t *testing.T) {
	record := `{
	  "event_id": "evt-1",
	  "timestamp": "2026-01-01T12:00:00Z",
	  "actor_id": "svc-payment",
	  "fingerprint_id": "fp-1",
	  "trust_score": 0.81,
	  "risk_level": "low",
	  "future_field_for_test": {"preserve": true, "nested": ["a", "b"]}
	}`
	path := writeRecordFile(t, record)

	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","disposition":"applied","next_sequence":"8",`+
		`"record_count":"7","behavior_complete":false}`)

	runPlatformCLI(t, "eval", "ingest", "--api-url", api.url(), "--id", "run-42",
		"--sequence", "7", "--behavioral-profile", "checkout-agent", "--record", path).
		mustExit(t, exitOK, "ingest")

	var envelope struct {
		Record struct {
			EventID     string `json:"event_id"`
			FutureField struct {
				Preserve bool     `json:"preserve"`
				Nested   []string `json:"nested"`
			} `json:"future_field_for_test"`
		} `json:"record"`
	}
	if err := json.Unmarshal(api.only().body, &envelope); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if envelope.Record.EventID != "evt-1" {
		t.Errorf("known field lost: event_id = %q", envelope.Record.EventID)
	}
	if !envelope.Record.FutureField.Preserve {
		t.Fatal("future_field_for_test.preserve was stripped before transmission")
	}
	if len(envelope.Record.FutureField.Nested) != 2 {
		t.Errorf("future_field_for_test.nested = %v, want two elements",
			envelope.Record.FutureField.Nested)
	}
}

// TestUnknownResponseFieldsAreTolerated covers the other direction.
//
// The compatibility contract requires clients to accept fields they do not
// know. A CLI that rejected them would make every additive server change a
// breaking one, inverting the contract it exists to consume.
func TestUnknownResponseFieldsAreTolerated(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		body      string
		wantHuman string
	}{
		{
			name: "top level",
			args: []string{"eval", "get", "--id", "run-42"},
			body: `{"version":"1","id":"run-42","status":"running",
			        "future_field":{"some":"value"}}`,
			wantHuman: "run-42",
		},
		{
			name: "nested in a run response",
			args: []string{"eval", "get", "--id", "run-42"},
			body: `{"version":"1","id":"run-42","status":"running",
			        "timings":{"future_nested":{"deep":[1,2,3]}}}`,
			wantHuman: "running",
		},
		{
			name: "nested in a gate response",
			args: compareArgs("", ""),
			body: `{"version":"1","reference_run_id":"ref","candidate_run_id":"cand",
			        "behavior_diff":{"added_count":0,"removed_count":0,"shared_count":4},
			        "gate":{"verdict":"pass","future_check":{"passed":true},
			                "reference_evidence":{"actual":"1","minimum":"1","passed":true},
			                "candidate_evidence":{"actual":"1","minimum":"1","passed":true},
			                "added_behaviors":{"actual":"0","maximum":"0","passed":true},
			                "block_decisions":{"actual":"0","maximum":"0","passed":true},
			                "critical_risk_observations":{"actual":"0","maximum":"0","passed":true}}}`,
			wantHuman: "PASS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, tt.body)

			args := tt.args
			if args[1] == "compare" {
				args = compareArgs(api.url())
			} else {
				args = append(append([]string{}, args...), "--api-url", api.url())
			}

			// Human mode still works.
			human := runPlatformCLI(t, args...)
			human.mustExit(t, exitOK, tt.name+" human")
			if !strings.Contains(human.stdout, tt.wantHuman) {
				t.Errorf("human stdout missing %q:\n%s", tt.wantHuman, human.stdout)
			}

			// JSON mode preserves the unknown field, semantically.
			machine := runPlatformCLI(t, append(args, "--json")...)
			machine.mustExit(t, exitOK, tt.name+" json")

			var got, want any
			if err := json.Unmarshal([]byte(machine.stdout), &got); err != nil {
				t.Fatalf("--json stdout is not JSON: %v\n%s", err, machine.stdout)
			}
			if err := json.Unmarshal([]byte(tt.body), &want); err != nil {
				t.Fatalf("fixture is not JSON: %v", err)
			}
			if mustJSON(got) != mustJSON(want) {
				t.Errorf("--json output lost fields:\ngot  %s\nwant %s",
					mustJSON(got), mustJSON(want))
			}
		})
	}
}

// ---------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------

// TestResponseSizeLimit proves the read is bounded, at the boundary.
func TestResponseSizeLimit(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		wantExit int
	}{
		{"exactly at the limit", maxPlatformResponseBody, exitOK},
		{"one byte over the limit", maxPlatformResponseBody + 1, exitOperational},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, paddedRunResponse(t, tt.size))

			result := runPlatformCLI(t, "eval", "get", "--api-url", api.url(), "--id", "run-42")
			result.mustExit(t, tt.wantExit, tt.name)

			if tt.wantExit != exitOK {
				// Bounded diagnostic: the oversized body must not be echoed.
				if len(result.stderr) > 4096 {
					t.Errorf("diagnostic is %d bytes; it should not include the body", len(result.stderr))
				}
				if result.stdout != "" {
					t.Errorf("stdout = %d bytes, want empty", len(result.stdout))
				}
			}
		})
	}
}

// TestResponseReadStopsAtTheBound proves the read itself is bounded, not just
// checked afterwards.
//
// Found by a surviving mutation. Deleting the io.LimitReader left the length
// check in place, so the previous test still saw exit 3 and passed — while the
// CLI would have buffered the entire body into memory before rejecting it. The
// length check is about the contract; the LimitReader is about not being
// handed ten gigabytes by a broken or hostile server, and only this test
// covers that.
//
// Same shape as the outer-layer-masks-inner-layer problem that has recurred
// through this milestone: asserting the outcome is not asserting the mechanism.
func TestResponseReadStopsAtTheBound(t *testing.T) {
	const (
		chunk     = 1 << 20
		maxChunks = 64 // 64 MiB, sixteen times the 4 MiB bound
	)

	written := make(chan int, 1)

	api := newFakeAPI(t)
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)

		payload := make([]byte, chunk)
		for i := range payload {
			payload[i] = 'x'
		}
		sent := 0
		for range maxChunks {
			n, err := w.Write(payload)
			sent += n
			if err != nil {
				break // the client hung up, which is the point
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		written <- sent
	})

	result := runPlatformCLI(t, "eval", "get", "--api-url", api.url(), "--id", "run-42")
	result.mustExit(t, exitOperational, "oversized streamed response")

	select {
	case sent := <-written:
		// Generous margin: the bound is 4 MiB and the server offered 64 MiB,
		// so socket buffering cannot account for the difference.
		if sent >= maxChunks*chunk {
			t.Fatalf("the server wrote all %d bytes; the client read an unbounded body", sent)
		}
		t.Logf("client stopped after the server had written %d bytes (bound is %d)",
			sent, maxPlatformResponseBody)
	case <-time.After(30 * time.Second):
		t.Fatal("the server never finished writing")
	}
}

// paddedRunResponse builds a valid run response of exactly size bytes.
func paddedRunResponse(t *testing.T, size int) string {
	t.Helper()
	prefix := `{"version":"1","id":"run-42","status":"running","pad":"`
	suffix := `"}`
	padding := size - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatalf("size %d is too small", size)
	}
	body := prefix + strings.Repeat("x", padding) + suffix
	if len(body) != size {
		t.Fatalf("built %d bytes, want %d", len(body), size)
	}
	return body
}

// TestIngestRequestSizeLimit proves the cap counts the envelope, not the file.
//
// A record file just under the limit still produces an over-limit request once
// the envelope's own fields are added. Checking the file alone would build a
// request the server is guaranteed to reject.
func TestIngestRequestSizeLimit(t *testing.T) {
	tests := []struct {
		name       string
		recordSize int
		wantExit   int
		wantSent   bool
	}{
		{"comfortably within the limit", 1024, exitOK, true},
		{"file at the limit, envelope over it", maxPlatformRequestBody, exitUsage, false},
		{"file over the limit", maxPlatformRequestBody + 1, exitUsage, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeRecordFile(t, paddedRecord(t, tt.recordSize))

			api := newFakeAPI(t)
			api.reply(200, `{"version":"1","disposition":"applied","next_sequence":"2",`+
				`"record_count":"1","behavior_complete":false}`)

			result := runPlatformCLI(t, "eval", "ingest", "--api-url", api.url(),
				"--id", "run-42", "--sequence", "1",
				"--behavioral-profile", "checkout-agent", "--record", path)
			result.mustExit(t, tt.wantExit, tt.name)

			sent := len(api.captured()) > 0
			if sent != tt.wantSent {
				t.Fatalf("request sent = %v, want %v", sent, tt.wantSent)
			}
		})
	}
}

func paddedRecord(t *testing.T, size int) string {
	t.Helper()
	prefix := `{"event_id":"evt-1","pad":"`
	suffix := `"}`
	padding := size - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatalf("size %d is too small", size)
	}
	record := prefix + strings.Repeat("x", padding) + suffix
	if len(record) != size {
		t.Fatalf("built %d bytes, want %d", len(record), size)
	}
	return record
}

// TestIngestSequenceValidation pins the sequence contract.
func TestIngestSequenceValidation(t *testing.T) {
	tests := []struct {
		name     string
		sequence []string // empty means the flag is omitted entirely
		wantExit int
		wantWire string
	}{
		{"one", []string{"--sequence", "1"}, exitOK, "1"},
		{"max uint64", []string{"--sequence", "18446744073709551615"}, exitOK,
			"18446744073709551615"},

		{"omitted", nil, exitUsage, ""},
		{"zero", []string{"--sequence", "0"}, exitUsage, ""},
		{"negative", []string{"--sequence", "-1"}, exitUsage, ""},
		{"overflow", []string{"--sequence", "18446744073709551616"}, exitUsage, ""},
		{"non-numeric", []string{"--sequence", "seven"}, exitUsage, ""},
	}

	path := writeRecordFile(t, `{"event_id":"evt-1"}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, `{"version":"1","disposition":"applied","next_sequence":"2",`+
				`"record_count":"1","behavior_complete":false}`)

			args := append([]string{"eval", "ingest", "--api-url", api.url(), "--id", "run-42",
				"--behavioral-profile", "checkout-agent", "--record", path}, tt.sequence...)

			runPlatformCLI(t, args...).mustExit(t, tt.wantExit, tt.name)
			if tt.wantExit != exitOK {
				return
			}

			var envelope struct {
				Sequence any `json:"sequence"`
			}
			if err := json.Unmarshal(api.only().body, &envelope); err != nil {
				t.Fatalf("request body: %v", err)
			}
			got, isString := envelope.Sequence.(string)
			if !isString {
				t.Fatalf("sequence = %T, want a JSON string", envelope.Sequence)
			}
			if got != tt.wantWire {
				t.Errorf("sequence = %q, want %q", got, tt.wantWire)
			}
		})
	}
}

// TestIngestRecordFileValidation rejects malformed input locally.
func TestIngestRecordFileValidation(t *testing.T) {
	api := newFakeAPI(t)

	tests := []struct {
		name    string
		content string
		missing bool
	}{
		{name: "missing file", missing: true},
		{name: "empty file", content: ""},
		{name: "not JSON", content: "this is not json"},
		{name: "truncated JSON", content: `{"event_id":`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "record.json")
			if !tt.missing {
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			before := len(api.captured())
			runPlatformCLI(t, "eval", "ingest", "--api-url", api.url(), "--id", "run-42",
				"--sequence", "1", "--behavioral-profile", "p", "--record", path).
				mustExit(t, exitUsage, tt.name)
			if len(api.captured()) != before {
				t.Fatal("a request was sent for an invalid record file")
			}
		})
	}
}

// ---------------------------------------------------------------------
// API URL
// ---------------------------------------------------------------------

func TestAPIURLValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"loopback with port", "http://127.0.0.1:1234", false},
		{"https host", "https://example.test", false},
		{"trailing slash", "http://127.0.0.1:1234/", false},

		{"empty", "", true},
		{"relative", "/v1", true},
		{"host and port without a scheme", "127.0.0.1:1234", true},
		{"ftp scheme", "ftp://example.test", true},
		{"file scheme", "file:///etc/passwd", true},
		{"userinfo", "http://user:hunter2@example.test", true},
		{"username only", "http://user@example.test", true},
		{"query", "http://example.test?a=b", true},
		{"fragment", "http://example.test#frag", true},
		{"missing host", "http://", true},
		{"base path", "http://example.test/api", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseAPIURL(tt.raw)
			if tt.wantErr != (err != nil) {
				t.Fatalf("parseAPIURL(%q) error = %v, wantErr = %v", tt.raw, err, tt.wantErr)
			}
			if err != nil && exitCodeFor(err) != exitUsage {
				t.Errorf("exit = %d, want %d (usage)", exitCodeFor(err), exitUsage)
			}
		})
	}
}

// TestAPIURLCredentialsAreNotEchoed keeps a password out of the diagnostic.
//
// Rejecting the URL and then printing it would defeat the rejection: the
// secret would land in shell history, CI logs and the terminal scrollback,
// which is the exposure the refusal exists to prevent.
func TestAPIURLCredentialsAreNotEchoed(t *testing.T) {
	const password = "hunter2-DO-NOT-LEAK"

	result := runPlatformCLI(t, "project", "get", "--id", "proj-1",
		"--api-url", "http://user:"+password+"@example.test")
	result.mustExit(t, exitUsage, "credentials in --api-url")

	if strings.Contains(result.stdout+result.stderr, password) {
		t.Fatalf("the rejected password appeared in output:\nstdout: %s\nstderr: %s",
			result.stdout, result.stderr)
	}
}

// TestPathIDsAreEscaped proves an identifier cannot reshape a route.
func TestPathIDsAreEscaped(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantPath string
	}{
		{"ordinary", "run-42", "/v1/evaluation-runs/run-42"},
		{"slash", "a/b", "/v1/evaluation-runs/a%2Fb"},
		{"traversal", "../../admin", "/v1/evaluation-runs/..%2F..%2Fadmin"},
		{"space", "a b", "/v1/evaluation-runs/a%20b"},
		{"question mark", "a?b", "/v1/evaluation-runs/a%3Fb"},
		{"hash", "a#b", "/v1/evaluation-runs/a%23b"},
		{"percent", "a%2e", "/v1/evaluation-runs/a%252e"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, `{"version":"1","id":"x","status":"pending"}`)

			runPlatformCLI(t, "eval", "get", "--api-url", api.url(), "--id", tt.id).
				mustExit(t, exitOK, tt.name)

			if got := api.only().escapedPath; got != tt.wantPath {
				t.Errorf("path = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Stream and output contracts
// ---------------------------------------------------------------------

// TestStreamContract makes shell automation predictable.
//
// The rule is one sentence: results on stdout, diagnostics on stderr. A
// pipeline redirecting stdout to a file must collect evidence or nothing —
// never an error message where JSON was expected.
func TestStreamContract(t *testing.T) {
	t.Run("success writes only stdout", func(t *testing.T) {
		api := newFakeAPI(t)
		api.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

		result := runPlatformCLI(t, "project", "get", "--api-url", api.url(), "--id", "proj-1")
		result.mustExit(t, exitOK, "project get")
		if result.stdout == "" {
			t.Error("stdout is empty on success")
		}
		if result.stderr != "" {
			t.Errorf("stderr = %q, want empty on success", result.stderr)
		}
	})

	t.Run("gate fail writes evidence to stdout", func(t *testing.T) {
		api := newFakeAPI(t)
		api.reply(200, comparisonBody(gateVerdictFail, 3, "0"))

		result := runPlatformCLI(t, append(compareArgs(api.url()), "--json")...)
		result.mustExit(t, exitGateFail, "failing gate")

		// A FAIL is a result, not an error: CI needs the comparison to publish
		// alongside the failure, and stderr would separate verdict from reason.
		if result.stderr != "" {
			t.Errorf("stderr = %q, want empty on gate FAIL", result.stderr)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil {
			t.Fatalf("gate FAIL stdout is not the comparison JSON: %v\n%s", err, result.stdout)
		}
	})

	t.Run("API error writes only stderr", func(t *testing.T) {
		api := newFakeAPI(t)
		api.reply(404, `{"version":"1","error":{"code":"not_found","message":"no such run"}}`)

		result := runPlatformCLI(t, "eval", "get", "--api-url", api.url(), "--id", "run-42")
		result.mustExit(t, exitOperational, "missing run")
		if result.stdout != "" {
			t.Errorf("stdout = %q, want empty on an API error", result.stdout)
		}
		if !strings.Contains(result.stderr, "not_found") {
			t.Errorf("stderr did not carry the error code:\n%s", result.stderr)
		}
	})

	t.Run("usage writes only stderr", func(t *testing.T) {
		result := runPlatformCLI(t, "eval", "get", "--api-url", "http://127.0.0.1:1")
		result.mustExit(t, exitUsage, "missing --id")
		if result.stdout != "" {
			t.Errorf("stdout = %q, want empty on a usage error", result.stdout)
		}
		if !strings.Contains(result.stderr, "usage:") {
			t.Errorf("stderr did not include usage:\n%s", result.stderr)
		}
	})
}

// TestJSONErrorEnvelopeGoesToStderrVerbatim proves there is no second error
// schema: a tool that already parses API errors parses the CLI's.
func TestJSONErrorEnvelopeGoesToStderrVerbatim(t *testing.T) {
	const envelope = `{"version":"1","error":{"code":"conflict","message":"run is not running"}}`

	api := newFakeAPI(t)
	api.reply(409, envelope)

	result := runPlatformCLI(t, "eval", "start", "--api-url", api.url(), "--id", "run-42", "--json")
	result.mustExit(t, exitOperational, "conflict")

	if result.stdout != "" {
		t.Errorf("stdout = %q, want empty", result.stdout)
	}
	var got, want any
	if err := json.Unmarshal([]byte(result.stderr), &got); err != nil {
		t.Fatalf("stderr is not the raw envelope: %v\n%s", err, result.stderr)
	}
	if err := json.Unmarshal([]byte(envelope), &want); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if mustJSON(got) != mustJSON(want) {
		t.Errorf("envelope altered:\ngot  %s\nwant %s", mustJSON(got), mustJSON(want))
	}
}

// TestJSONModeForwardsTheServerBody covers each representative category.
func TestJSONModeForwardsTheServerBody(t *testing.T) {
	recordPath := writeRecordFile(t, `{"event_id":"evt-1"}`)

	tests := []struct {
		name string
		args []string
		body string
	}{
		{
			name: "create",
			args: []string{"project", "create", "--id", "proj-1", "--name", "Checkout"},
			body: `{"version":"1","id":"proj-1","name":"Checkout","extra":{"a":1}}`,
		},
		{
			name: "progress",
			args: []string{"eval", "progress", "--id", "run-42"},
			body: `{"version":"1","run_id":"run-42","status":"running","record_count":"18",` +
				`"behavior_observation_count":"18","distinct_behavior_count":11,` +
				`"behavior_complete":true,"next_ingest_sequence":"19","extra":"kept"}`,
		},
		{
			name: "ingest",
			args: []string{"eval", "ingest", "--id", "run-42", "--sequence", "7",
				"--behavioral-profile", "p", "--record", recordPath},
			body: `{"version":"1","disposition":"applied","next_sequence":"8",` +
				`"record_count":"7","behavior_complete":false,"extra":[1,2]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, tt.body)

			result := runPlatformCLI(t, append(append([]string{}, tt.args...),
				"--api-url", api.url(), "--json")...)
			result.mustExit(t, exitOK, tt.name)

			var got, want any
			if err := json.Unmarshal([]byte(result.stdout), &got); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, result.stdout)
			}
			if err := json.Unmarshal([]byte(tt.body), &want); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			if mustJSON(got) != mustJSON(want) {
				t.Errorf("body altered:\ngot  %s\nwant %s", mustJSON(got), mustJSON(want))
			}

			// No CLI-authored wrapper fields.
			var top map[string]any
			if err := json.Unmarshal([]byte(result.stdout), &top); err == nil {
				for _, forbidden := range []string{"cli_version", "trustvian_cli", "result", "data"} {
					if _, present := top[forbidden]; present {
						t.Errorf("--json output added a wrapper field %q", forbidden)
					}
				}
			}
		})
	}
}

// TestCompareHumanRenderingUsesServerValues proves rendering, not computation.
func TestCompareHumanRenderingUsesServerValues(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, comparisonBody(gateVerdictFail, 2, "0"))

	result := runPlatformCLI(t, compareArgs(api.url())...)
	result.mustExit(t, exitGateFail, "failing comparison")

	for _, want := range []string{
		"ref", "cand", // both run identifiers
		"+2", "-1", "9 shared", // the diff the server reported
		"Reference evidence", "Candidate evidence",
		"Added behaviors", "Block decisions", "Critical risk observations",
		"FAIL",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("human output missing %q:\n%s", want, result.stdout)
		}
	}
}

// TestUnknownSubcommandsAreUsageErrors keeps typos out of the operational
// exit code, so a script cannot mistake them for a server problem.
func TestUnknownSubcommandsAreUsageErrors(t *testing.T) {
	families := map[string][]string{
		"project":   {"list", "delete", ""},
		"agent":     {"list", "update", ""},
		"candidate": {"list", "promote", ""},
		// Explicitly including the commands later milestones own: they must
		// read as "not a command", never as a broken server.
		"eval": {"watch", "tui", "serve", "run-local", "promote", "list", ""},
		// "promote" and "delete" are the two an operator would reach for
		// first, and task 065 implements neither: promotion is task 066's,
		// and an environment is archived rather than deleted.
		"env": {"promote", "delete", "rank", ""},
		// "approve" and "delete" are the two an operator would reach for
		// first, and task 066 implements neither: a promotion records no
		// approval, and a recorded decision is append-only.
		"promotion": {"approve", "delete", "update", "rollback", ""},
	}

	for family, subcommands := range families {
		for _, sub := range subcommands {
			name := family + " " + sub
			t.Run(strings.TrimSpace(name), func(t *testing.T) {
				args := []string{family}
				if sub != "" {
					args = append(args, sub)
				}
				runPlatformCLI(t, args...).mustExit(t, exitUsage, name)
			})
		}
	}
}

// TestMissingRuntimeIsOperationalNotUsage pins the classification task 062
// changed.
//
// Before local startup existed, omitting --api-url was an invocation error.
// Now a local runtime advertises itself, so the command is valid and the
// environment is what failed — reporting usage would tell a developer to fix
// something they wrote correctly. Exit 3, and never exit 1.
func TestMissingRuntimeIsOperationalNotUsage(t *testing.T) {
	commands := [][]string{
		{"project", "get", "--id", "proj-1"},
		{"project", "create", "--id", "proj-1", "--name", "n"},
		{"agent", "get", "--id", "agent-1"},
		{"candidate", "get", "--id", "cand-1"},
		{"eval", "get", "--id", "run-42"},
		{"eval", "start", "--id", "run-42"},
		{"eval", "progress", "--id", "run-42"},
		{"eval", "ingest-state", "--id", "run-42"},
		{"env", "get", "--project-id", "proj-1", "--ref", "staging"},
		{"env", "list", "--project-id", "proj-1"},
		{"env", "create", "--project-id", "proj-1", "--ref", "staging", "--name", "Staging"},
		{"env", "set", "--project-id", "proj-1", "--ref", "staging", "--revision", "1", "--name", "S"},
		{"env", "archive", "--project-id", "proj-1", "--ref", "staging", "--revision", "1"},
		{"env", "activate", "--project-id", "proj-1", "--ref", "staging", "--revision", "1"},
		{"promotion", "get", "--id", "promo-1"},
		{"promotion", "list", "--project-id", "proj-1"},
		{"promotion", "create", "--id", "promo-1", "--reference-run", "run-ref",
			"--candidate-run", "run-can", "--target-environment", "production",
			"--max-added-behaviors", "0", "--max-block-decisions", "0",
			"--max-critical-risk-observations", "0"},
	}

	for _, args := range commands {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			// An empty directory, so discovery is definitively absent rather
			// than absent by accident of where the test happened to run.
			t.Chdir(t.TempDir())

			result := runPlatformCLI(t, args...)
			result.mustExit(t, exitOperational, fmt.Sprint(args))

			if result.code == exitGateFail {
				t.Fatal("a missing runtime produced exit 1, which means gate FAIL")
			}
			if result.stdout != "" {
				t.Errorf("stdout = %q, want empty", result.stdout)
			}
			if !strings.Contains(result.stderr, "no local Trustvian runtime") {
				t.Errorf("stderr does not explain the missing runtime:\n%s", result.stderr)
			}
		})
	}
}

// TestExplicitInvalidAPIURLIsStillUsage keeps the other half of the
// distinction: the user supplied the input, so the input is what is wrong.
func TestExplicitInvalidAPIURLIsStillUsage(t *testing.T) {
	tests := []struct{ name, apiURL string }{
		{"relative", "/v1"},
		{"no scheme", "127.0.0.1:1234"},
		{"ftp", "ftp://example.test"},
		{"credentials", "http://u:p@example.test"},
		{"query", "http://example.test?a=b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			runPlatformCLI(t, "eval", "get", "--id", "run-42", "--api-url", tt.apiURL).
				mustExit(t, exitUsage, tt.name)
		})
	}
}

// ---------------------------------------------------------------------
// Verdict vocabulary
// ---------------------------------------------------------------------

// comparisonWithVerdict builds a well-formed comparison carrying any verdict,
// including ones the CLI must refuse to interpret.
func comparisonWithVerdict(verdict string) string {
	return fmt.Sprintf(`{
	  "version": "1",
	  "reference_run_id": "ref",
	  "candidate_run_id": "cand",
	  "behavior_diff": {"added_count": 0, "removed_count": 0, "shared_count": 4},
	  "gate": {
	    "reference_evidence": {"actual":"10","minimum":"1","passed":true},
	    "candidate_evidence": {"actual":"12","minimum":"1","passed":true},
	    "added_behaviors": {"actual":"0","maximum":"0","passed":true},
	    "block_decisions": {"actual":"0","maximum":"0","passed":true},
	    "critical_risk_observations": {"actual":"0","maximum":"0","passed":true},
	    "verdict": %q
	  }
	}`, verdict)
}

// comparisonWithoutVerdict omits the field entirely, so it decodes to "".
func comparisonWithoutVerdict() string {
	return `{
	  "version": "1",
	  "reference_run_id": "ref",
	  "candidate_run_id": "cand",
	  "behavior_diff": {"added_count": 0, "removed_count": 0, "shared_count": 4},
	  "gate": {
	    "reference_evidence": {"actual":"10","minimum":"1","passed":true},
	    "candidate_evidence": {"actual":"12","minimum":"1","passed":true},
	    "added_behaviors": {"actual":"0","maximum":"0","passed":true},
	    "block_decisions": {"actual":"0","maximum":"0","passed":true},
	    "critical_risk_observations": {"actual":"0","maximum":"0","passed":true}
	  }
	}`
}

// TestCompareOnlyExplicitFailIsExitOne is the tightened core of the exit
// contract.
//
// The earlier implementation was "pass is 0, everything else is 1", which
// reported a verdict the CLI could not interpret as a policy violation. A
// build failed for something that never happened — and the wrong-case "PASS"
// case is the one that shows how ordinary the mistake is.
//
// Exit 1 now requires the server to have said "fail", explicitly.
func TestCompareOnlyExplicitFailIsExitOne(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantExit int
	}{
		{"pass", comparisonWithVerdict("pass"), exitOK},
		{"fail", comparisonWithVerdict("fail"), exitGateFail},

		{"unknown", comparisonWithVerdict("unknown"), exitOperational},
		{"pending", comparisonWithVerdict("pending"), exitOperational},
		{"empty string", comparisonWithVerdict(""), exitOperational},
		{"missing field", comparisonWithoutVerdict(), exitOperational},
		{"wrong case PASS", comparisonWithVerdict("PASS"), exitOperational},
		{"wrong case FAIL", comparisonWithVerdict("FAIL"), exitOperational},
		{"typo", comparisonWithVerdict("passs"), exitOperational},
		{"leading space", comparisonWithVerdict(" pass"), exitOperational},
		{"future verdict", comparisonWithVerdict("conditional_pass"), exitOperational},
	}

	for _, tt := range tests {
		for _, mode := range []string{"human", "json"} {
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				api := newFakeAPI(t)
				api.reply(200, tt.body)

				args := compareArgs(api.url())
				if mode == "json" {
					args = append(args, "--json")
				}
				result := runPlatformCLI(t, args...)
				result.mustExit(t, tt.wantExit, tt.name+"/"+mode)

				// The invariant, stated directly: nothing but an explicit
				// "fail" may produce the CI gate-failure code.
				if tt.wantExit != exitGateFail && result.code == exitGateFail {
					t.Fatalf("verdict %s produced exit 1, which means gate FAIL", tt.name)
				}

				if tt.wantExit != exitOperational {
					return
				}
				// An uninterpretable response is not publishable evidence. If
				// it reached stdout, a pipeline redirecting stdout to a file
				// would keep the artifact and lose the retraction.
				if result.stdout != "" {
					t.Errorf("stdout = %q, want empty for an unsupported verdict", result.stdout)
				}
				if result.stderr == "" {
					t.Error("stderr is empty; an unsupported verdict must be diagnosed")
				}
			})
		}
	}
}

// TestCompareVerdictVocabularyIsNotArithmetic keeps the fix narrow.
//
// Only the verdict *word* is validated. The CLI still does not check whether
// the verdict agrees with the counts — that would be the second gate
// implementation ADR 0033 rules out.
func TestCompareVerdictVocabularyIsNotArithmetic(t *testing.T) {
	t.Run("impossible evidence with an explicit pass still exits 0", func(t *testing.T) {
		api := newFakeAPI(t)
		api.reply(200, comparisonBody(gateVerdictPass, 7, "0"))
		runPlatformCLI(t, compareArgs(api.url())...).
			mustExit(t, exitOK, "7 added under a maximum of 0, verdict pass")
	})

	t.Run("passing-looking evidence with an explicit fail still exits 1", func(t *testing.T) {
		api := newFakeAPI(t)
		api.reply(200, comparisonBody(gateVerdictFail, 0, "99"))
		runPlatformCLI(t, compareArgs(api.url())...).
			mustExit(t, exitGateFail, "0 added under a maximum of 99, verdict fail")
	})
}

// ---------------------------------------------------------------------
// Successful bodies must be JSON, in both modes
// ---------------------------------------------------------------------

// TestSuccessfulBodyMustBeJSON closes the gap between the two output modes.
//
// --json used to copy a 2xx body through untouched and exit 0, while human
// mode failed on the same response because its renderer had to decode one. A
// machine-readable mode that reports success for arbitrary bytes is worse than
// one with no validation, because the exit code claims the output is usable.
func TestSuccessfulBodyMustBeJSON(t *testing.T) {
	recordPath := writeRecordFile(t, `{"event_id":"evt-1"}`)

	commands := []struct {
		name   string
		args   []string
		status int
	}{
		{"eval get", []string{"eval", "get", "--id", "run-42"}, 200},
		{"project create", []string{"project", "create", "--id", "p1", "--name", "n"}, 201},
		{"agent get", []string{"agent", "get", "--id", "a1"}, 200},
		{"candidate get", []string{"candidate", "get", "--id", "c1"}, 200},
		{"eval progress", []string{"eval", "progress", "--id", "run-42"}, 200},
		{"eval start", []string{"eval", "start", "--id", "run-42"}, 200},
		{"eval ingest", []string{"eval", "ingest", "--id", "run-42", "--sequence", "1",
			"--behavioral-profile", "p", "--record", recordPath}, 200},
	}

	bodies := []struct {
		name string
		body string
	}{
		{"plain text", "not-json"},
		{"truncated JSON", `{"version":"1"`},
		{"HTML error page", "<html><body>502 Bad Gateway</body></html>"},
		{"empty", ""},
		{"whitespace only", "   \n\t "},
	}

	for _, command := range commands {
		for _, body := range bodies {
			for _, mode := range []string{"human", "json"} {
				t.Run(command.name+"/"+body.name+"/"+mode, func(t *testing.T) {
					api := newFakeAPI(t)
					api.reply(command.status, body.body)

					args := append(append([]string{}, command.args...), "--api-url", api.url())
					if mode == "json" {
						args = append(args, "--json")
					}

					result := runPlatformCLI(t, args...)
					result.mustExit(t, exitOperational, command.name+"/"+body.name+"/"+mode)

					if result.stdout != "" {
						t.Errorf("stdout = %q, want empty for a malformed success body", result.stdout)
					}
					if result.stderr == "" {
						t.Error("stderr is empty; a malformed success body must be diagnosed")
					}
				})
			}
		}
	}
}

// TestCompareSuccessfulBodyMustBeJSON applies the same rule on the CI path,
// where an exit code is read as a policy answer.
func TestCompareSuccessfulBodyMustBeJSON(t *testing.T) {
	for _, body := range []struct{ name, content string }{
		{"plain text", "not-json"},
		{"truncated", `{"gate":{"verdict":"pass"`},
		{"empty", ""},
	} {
		for _, mode := range []string{"human", "json"} {
			t.Run(body.name+"/"+mode, func(t *testing.T) {
				api := newFakeAPI(t)
				api.reply(200, body.content)

				args := compareArgs(api.url())
				if mode == "json" {
					args = append(args, "--json")
				}
				result := runPlatformCLI(t, args...)
				result.mustExit(t, exitOperational, body.name+"/"+mode)

				if result.code == exitGateFail {
					t.Fatal("a malformed body produced exit 1, which means gate FAIL")
				}
				if result.stdout != "" {
					t.Errorf("stdout = %q, want empty", result.stdout)
				}
			})
		}
	}
}

// TestValidJSONWithUnknownFieldsStillSucceeds is the other half of the JSON
// gate: it checks syntax and nothing else, so an additive server change still
// passes through untouched.
func TestValidJSONWithUnknownFieldsStillSucceeds(t *testing.T) {
	const body = `{"version":"1","id":"run-1","status":"running",` +
		`"future_field":{"preserve":true,"nested":[1,2,3]}}`

	api := newFakeAPI(t)
	api.reply(200, body)

	human := runPlatformCLI(t, "eval", "get", "--api-url", api.url(), "--id", "run-1")
	human.mustExit(t, exitOK, "human mode with an unknown field")
	if !strings.Contains(human.stdout, "run-1") {
		t.Errorf("human output missing the known field:\n%s", human.stdout)
	}

	machine := runPlatformCLI(t, "eval", "get", "--api-url", api.url(), "--id", "run-1", "--json")
	machine.mustExit(t, exitOK, "json mode with an unknown field")

	var got, want any
	if err := json.Unmarshal([]byte(machine.stdout), &got); err != nil {
		t.Fatalf("--json stdout is not JSON: %v\n%s", err, machine.stdout)
	}
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if mustJSON(got) != mustJSON(want) {
		t.Errorf("the JSON gate altered the body:\ngot  %s\nwant %s", mustJSON(got), mustJSON(want))
	}

	// And the bytes are forwarded, not rebuilt: a re-marshal would reorder or
	// reformat, and would drop anything the CLI's DTO does not name.
	if !strings.Contains(machine.stdout, `"future_field"`) {
		t.Error("--json output lost the unknown field entirely")
	}
}

// ---------------------------------------------------------------------
// Positional arguments
// ---------------------------------------------------------------------

// TestTrailingPositionalArgumentsAreRejected stops a typo from looking like a
// result.
//
// Every platform leaf takes its input through flags, so a leftover argument is
// always a mistake — a lost dash, a stray filename, a shell-quoting slip.
// Ignoring it sent the request anyway, which on eval compare meant an
// invocation the user got wrong still produced a PASS or a FAIL that CI acted
// on.
func TestTrailingPositionalArgumentsAreRejected(t *testing.T) {
	recordPath := writeRecordFile(t, `{"event_id":"evt-1"}`)

	commands := []struct {
		name string
		args []string
	}{
		{"project create", []string{"project", "create", "--id", "p1", "--name", "n"}},
		{"project get", []string{"project", "get", "--id", "p1"}},
		{"agent create", []string{"agent", "create", "--id", "a1", "--project-id", "p1", "--name", "n"}},
		{"agent get", []string{"agent", "get", "--id", "a1"}},
		{"candidate create", []string{"candidate", "create", "--id", "c1", "--agent-id", "a1"}},
		{"candidate get", []string{"candidate", "get", "--id", "c1"}},
		{"eval create", []string{"eval", "create", "--id", "r1", "--candidate-id", "c1",
			"--environment", "local", "--behavioral-profile", "p"}},
		{"eval get", []string{"eval", "get", "--id", "r1"}},
		{"eval start", []string{"eval", "start", "--id", "r1"}},
		{"eval complete", []string{"eval", "complete", "--id", "r1"}},
		{"eval cancel", []string{"eval", "cancel", "--id", "r1"}},
		{"eval fail", []string{"eval", "fail", "--id", "r1"}},
		{"eval progress", []string{"eval", "progress", "--id", "r1"}},
		{"eval ingest-state", []string{"eval", "ingest-state", "--id", "r1"}},
		{"eval ingest", []string{"eval", "ingest", "--id", "r1", "--sequence", "1",
			"--behavioral-profile", "p", "--record", recordPath}},
		{"eval compare", []string{"eval", "compare", "--reference-run", "ref",
			"--candidate-run", "cand", "--max-added-behaviors", "0",
			"--max-block-decisions", "0", "--max-critical-risk-observations", "0"}},
	}

	// Deliberately ordinary-looking. The dangerous trailing argument is not a
	// weird one — it is the plausible one nobody looks at twice.
	//
	// "-- foo" and a bare "-" are here because both reach fs.Args() and would
	// otherwise be ignored just like a plain word. A bare trailing "--" is
	// deliberately absent: flag consumes it as the POSIX end-of-options
	// marker, leaving no positional argument, and rejecting it would refuse a
	// legitimate invocation. TestBareDoubleDashIsNotAPositionalArgument pins
	// that distinction.
	extras := []string{"foo", "record.json", "run-42", "0", "-", "--|foo"}

	for _, command := range commands {
		for _, extra := range extras {
			t.Run(command.name+"/"+extra, func(t *testing.T) {
				api := newFakeAPI(t)
				api.reply(200, `{"version":"1","gate":{"verdict":"pass"}}`)

				args := append(append([]string{}, command.args...), "--api-url", api.url())
				// "--|foo" stands for the two-token form: an escaped
				// positional that survives the end-of-options marker.
				if extra == "--|foo" {
					args = append(args, "--", "foo")
				} else {
					args = append(args, extra)
				}

				result := runPlatformCLI(t, args...)
				result.mustExit(t, exitUsage, command.name+" with trailing "+extra)

				// Nothing reached the server: the invocation was never valid,
				// so it must not have had an effect to undo.
				if got := api.captured(); len(got) != 0 {
					t.Fatalf("server received %d requests for a rejected invocation", len(got))
				}
				if result.stdout != "" {
					t.Errorf("stdout = %q, want empty", result.stdout)
				}
				if result.stderr == "" {
					t.Error("stderr is empty; a usage error must be diagnosed")
				}
			})
		}
	}
}

// TestBareDoubleDashIsNotAPositionalArgument keeps the check from
// over-rejecting.
//
// A trailing "--" is the standard end-of-options marker. Go's flag package
// consumes it and reports no positional arguments, so `… --api-url X --` is a
// well-formed invocation. Treating it as a stray argument would reject a
// legitimate command, which is the failure mode opposite to the one this
// blocker was about — and the one nobody would think to test for.
func TestBareDoubleDashIsNotAPositionalArgument(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	result := runPlatformCLI(t, "project", "get", "--api-url", api.url(), "--id", "proj-1", "--")
	result.mustExit(t, exitOK, "trailing end-of-options marker")

	if got := api.captured(); len(got) != 1 {
		t.Fatalf("request count = %d, want 1", len(got))
	}

	// But one token after it is a positional argument again.
	rejected := runPlatformCLI(t,
		"project", "get", "--api-url", api.url(), "--id", "proj-1", "--", "foo")
	rejected.mustExit(t, exitUsage, "escaped positional after the marker")
	if got := api.captured(); len(got) != 1 {
		t.Fatalf("request count = %d after a rejected invocation, want still 1", len(got))
	}
}

// TestCompareTrailingArgumentIsNeverAGateResult states the CI-facing half
// separately, because this is the one where silence had a policy meaning.
func TestCompareTrailingArgumentIsNeverAGateResult(t *testing.T) {
	for _, verdict := range []string{gateVerdictPass, gateVerdictFail} {
		t.Run("server would have said "+verdict, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, comparisonWithVerdict(verdict))

			result := runPlatformCLI(t, append(compareArgs(api.url()), "oops")...)
			result.mustExit(t, exitUsage, "compare with a trailing argument")

			if result.code == exitOK || result.code == exitGateFail {
				t.Fatal("a mistyped invocation produced a gate result")
			}
			if got := api.captured(); len(got) != 0 {
				t.Fatalf("server received %d requests", len(got))
			}
		})
	}
}

// TestFamilyLevelUsageIsUnchanged confirms the new check did not disturb
// dispatch-level behavior.
func TestFamilyLevelUsageIsUnchanged(t *testing.T) {
	cases := [][]string{
		{"project"}, {"project", "list"},
		{"agent"}, {"agent", "update"},
		{"candidate"}, {"candidate", "promote"},
		{"eval"}, {"eval", "watch"}, {"eval", "serve"},
		{"env"}, {"env", "promote"},
		{"promotion"}, {"promotion", "approve"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			runPlatformCLI(t, args...).mustExit(t, exitUsage, strings.Join(args, " "))
		})
	}
}
