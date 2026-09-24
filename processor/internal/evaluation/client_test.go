package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
)

func TestParseAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "loopback http", raw: "http://127.0.0.1:54321"},
		{name: "https host", raw: "https://control.example"},
		{name: "trailing slash", raw: "http://127.0.0.1:54321/"},
		{name: "empty", raw: "", wantErr: true},
		{name: "whitespace", raw: "   ", wantErr: true},
		{name: "relative", raw: "127.0.0.1:54321", wantErr: true},
		{name: "no host", raw: "http://", wantErr: true},
		{name: "unsupported scheme", raw: "ftp://127.0.0.1", wantErr: true},
		{name: "path", raw: "http://127.0.0.1:54321/api", wantErr: true},
		{name: "query", raw: "http://127.0.0.1:54321?x=1", wantErr: true},
		{name: "fragment", raw: "http://127.0.0.1:54321#f", wantErr: true},
		{name: "credentials", raw: "http://user:hunter2@127.0.0.1:54321", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAPIURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseAPIURL(%q) = %v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAPIURL(%q) error = %v", tt.raw, err)
			}
			if got.Path != "" {
				t.Errorf("ParseAPIURL(%q).Path = %q, want empty", tt.raw, got.Path)
			}
		})
	}
}

// TestParseAPIURLNeverEchoesCredentials is the one URL rule that is a
// security property rather than a usability one: a diagnostic repeating the
// password puts it in Collector logs, which is the exposure the rejection
// exists to prevent.
func TestParseAPIURLNeverEchoesCredentials(t *testing.T) {
	const password = "hunter2"
	_, err := ParseAPIURL("http://user:" + password + "@127.0.0.1:54321")
	if err == nil {
		t.Fatal("ParseAPIURL() error = nil, want rejection")
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("error message %q contains the credential", err.Error())
	}
}

func TestParseSequenceRejectsNonCanonical(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    uint64
		wantErr bool
	}{
		{name: "one", in: "1", want: 1},
		{name: "large", in: "18446744073709551615", want: 18446744073709551615},
		{name: "leading zero", in: "01", wantErr: true},
		{name: "plus", in: "+1", wantErr: true},
		{name: "negative", in: "-1", wantErr: true},
		{name: "float", in: "1.0", wantErr: true},
		{name: "space", in: " 1", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "zero", in: "0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSequence(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSequence(%q) = %d, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSequence(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseSequence(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestIngestStateReadsNextSequence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evaluation-runs/run-1/ingest-state" {
			t.Errorf("path = %q, want the ingest-state route", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "run_id": "run-1", "next_sequence": "7",
		})
	}))
	defer server.Close()

	c, err := newClient(server.URL, requestTimeout)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	got, err := c.ingestState(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("ingestState() error = %v", err)
	}
	if got != 7 {
		t.Errorf("ingestState() = %d, want 7", got)
	}
}

// TestIngestPostsTheWireContract pins every field the server decodes
// strictly. A wrong envelope version or a numeric sequence is rejected by
// the control plane, and only a test that reads the raw body notices.
func TestIngestPostsTheWireContract(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want no credential header", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1", "disposition": "applied",
			"next_sequence": "2", "record_count": "1", "behavior_complete": true,
		})
	}))
	defer server.Close()

	c, err := newClient(server.URL, requestTimeout)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	got, err := c.ingest(context.Background(), "run-1", 1, "support-reference",
		trustvian.DecisionRecord{EventID: "e1", Decision: "observe_only"})
	if err != nil {
		t.Fatalf("ingest() error = %v", err)
	}
	if got.Disposition != "applied" || got.NextSequence != 2 {
		t.Errorf("ingest() = %+v, want {applied 2}", got)
	}

	if string(body["version"]) != `"1"` {
		t.Errorf("version = %s, want \"1\"", body["version"])
	}
	// A string, not a number: uint64 exceeds what a JSON double holds.
	if string(body["sequence"]) != `"1"` {
		t.Errorf("sequence = %s, want the string \"1\"", body["sequence"])
	}
	if string(body["behavioral_profile"]) != `"support-reference"` {
		t.Errorf("behavioral_profile = %s", body["behavioral_profile"])
	}
	var record trustvian.DecisionRecord
	if err := json.Unmarshal(body["record"], &record); err != nil {
		t.Fatalf("record is not a DecisionRecord: %v", err)
	}
	if record.EventID != "e1" {
		t.Errorf("record.EventID = %q, want e1", record.EventID)
	}
}

// TestIngestSurfacesServerErrorEnvelope covers Review Focus item 4: a 409
// sequence conflict is the most likely real failure, and the message is the
// part an operator can act on.
func TestIngestSurfacesServerErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1",
			"error":   map[string]string{"code": "conflict", "message": "sequence 4 leaves a gap"},
		})
	}))
	defer server.Close()

	c, _ := newClient(server.URL, requestTimeout)
	_, err := c.ingest(context.Background(), "run-1", 4, "p", trustvian.DecisionRecord{})
	if err == nil {
		t.Fatal("ingest() error = nil, want a conflict")
	}
	if !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "leaves a gap") {
		t.Errorf("error = %q, want the server's code and message", err.Error())
	}
}

// TestIngestRejectsOversizedBodyBeforeSending covers acceptance criterion 7's
// request-body bound, which had no test: a record whose caller-supplied
// content pushes the encoded envelope over the 256 KiB limit must be refused
// before any request reaches the network, not merely once the server would
// have rejected it.
func TestIngestRejectsOversizedBodyBeforeSending(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := newClient(server.URL, requestTimeout)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	record := trustvian.DecisionRecord{PolicyReason: strings.Repeat("x", maxRequestBody)}
	_, err = c.ingest(context.Background(), "run-1", 1, "support-reference", record)
	if err == nil {
		t.Fatal("ingest() error = nil, want the request body bound enforced")
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("error = %q, want it to name the byte limit", err.Error())
	}
	if called {
		t.Error("the oversized request reached the server; it must be rejected before sending")
	}
}

func TestClientRefusesRedirect(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("redirect was followed; a mutation must not change host")
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v1/evaluation-runs/run-1/records", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	c, _ := newClient(server.URL, requestTimeout)
	if _, err := c.ingest(context.Background(), "run-1", 1, "p", trustvian.DecisionRecord{}); err == nil {
		t.Fatal("ingest() error = nil, want a refused redirect")
	}
}

func TestClientBoundsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1","disposition":"applied","next_sequence":"2","pad":"`))
		chunk := strings.Repeat("a", 4096)
		for range (maxResponseBody / len(chunk)) + 2 {
			_, _ = w.Write([]byte(chunk))
		}
		_, _ = w.Write([]byte(`"}`))
	}))
	defer server.Close()

	c, _ := newClient(server.URL, requestTimeout)
	_, err := c.ingest(context.Background(), "run-1", 1, "p", trustvian.DecisionRecord{})
	if err == nil {
		t.Fatal("ingest() error = nil, want the response bound to be enforced")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %q, want it to name the limit", err.Error())
	}
}

func TestClientTimesOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); server.Close() }()

	c, _ := newClient(server.URL, 50*time.Millisecond)
	if _, err := c.ingestState(context.Background(), "run-1"); err == nil {
		t.Fatal("ingestState() error = nil, want a timeout")
	}
}

// TestIngestClassifiesFailureOutcomes is the table the recovery design rests
// on: which failures prove the record was not applied, and which only prove
// the client did not hear about it.
//
// Getting a row wrong here is not a cosmetic error. A failure wrongly called
// definitive frees a sequence the server may already hold, and the next
// record collides with it; one wrongly called unknown costs a single
// redundant POST the server answers "replayed". The asymmetry is why
// anything unproven belongs in the second column.
func TestIngestClassifiesFailureOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		unknown bool
		reason  string
	}{
		{
			name: "4xx envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1",
					"error":   map[string]string{"code": "ingest_sequence", "message": "stale"}})
			},
			unknown: false,
			reason:  "the control plane declined the request, so it did not apply it",
		},
		{
			name: "5xx envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1",
					"error":   map[string]string{"code": "internal", "message": "boom"}})
			},
			unknown: true,
			reason:  "a server failure may follow a commit it could not then report",
		},
		{
			name: "bare 502 from something in between",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
			},
			unknown: true,
			reason:  "a gateway error says a hop gave up, never whether the record landed",
		},
		{
			name: "connection destroyed after the request was delivered",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				hijackAndClose(w)
			},
			unknown: true,
			reason:  "this is exactly a lost response to a committed record",
		},
		{
			name: "2xx that is not JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("<html>ok</html>"))
			},
			unknown: true,
			reason:  "a 2xx means the record was accepted; the reply only hides which disposition",
		},
		{
			name: "2xx with an unusable next_sequence",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1", "disposition": "applied", "next_sequence": "007"})
			},
			unknown: true,
			reason:  "same: accepted, then unreadable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			c, err := newClient(server.URL, requestTimeout)
			if err != nil {
				t.Fatalf("newClient() error = %v", err)
			}
			_, err = c.ingest(context.Background(), "run-1", 1, "p", trustvian.DecisionRecord{})
			if err == nil {
				t.Fatal("ingest() error = nil, want a failure")
			}
			if got := outcomeUnknown(err); got != tt.unknown {
				t.Errorf("outcomeUnknown(%v) = %t, want %t — %s", err, got, tt.unknown, tt.reason)
			}
		})
	}
}

// TestIngestTreatsAnUnreachableHostAsDefinitive is the one transport failure
// that can be proven: the dial never completed, so no byte of the request
// reached anything and its sequence is free.
func TestIngestTreatsAnUnreachableHostAsDefinitive(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	c, err := newClient(url, requestTimeout)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	_, err = c.ingest(context.Background(), "run-1", 1, "p", trustvian.DecisionRecord{})
	if err == nil {
		t.Fatal("ingest() error = nil, want a refused connection surfaced")
	}
	if outcomeUnknown(err) {
		t.Errorf("error = %v is classified unknown; a failed dial delivered nothing", err)
	}
}

// TestIngestTreatsAnOversizedBodyAsDefinitive covers the checks that run
// before anything is sent. Nothing left this process, so the sequence the
// record was holding is untouched.
func TestIngestTreatsAnOversizedBodyAsDefinitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request must never be sent")
	}))
	defer server.Close()

	c, err := newClient(server.URL, requestTimeout)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	_, err = c.ingest(context.Background(), "run-1", 1, "p",
		trustvian.DecisionRecord{EventID: strings.Repeat("x", maxRequestBody+1)})
	if err == nil {
		t.Fatal("ingest() error = nil, want the body cap enforced")
	}
	if outcomeUnknown(err) {
		t.Errorf("error = %v is classified unknown; the request was never sent", err)
	}
}

// TestRefusedRedirectIsDefinitive: a 3xx is a complete response from an
// endpoint that answered without implementing ingest, and this route never
// redirects. Nothing was applied, so the sequence is free.
func TestRefusedRedirectIsDefinitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/v1", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	c, err := newClient(server.URL, requestTimeout)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	_, err = c.ingest(context.Background(), "run-1", 1, "p", trustvian.DecisionRecord{})
	if err == nil {
		t.Fatal("ingest() error = nil, want the redirect refused")
	}
	if !errors.Is(err, errRefusedRedirect) {
		t.Errorf("error = %v, want it to report the redirect refusal", err)
	}
	if outcomeUnknown(err) {
		t.Errorf("error = %v is classified unknown; a redirect is not an applied record", err)
	}
}
