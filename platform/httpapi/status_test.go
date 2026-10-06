package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

// statusAPI is a handler over a fresh control plane with a clock the test
// moves, because status is entirely about elapsed time.
type statusAPI struct {
	t       *testing.T
	handler http.Handler
	mu      sync.Mutex
	now     time.Time
}

func newStatusAPI(t *testing.T) *statusAPI {
	t.Helper()
	store, err := platform.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	s := &statusAPI{t: t, now: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)}
	handler, err := httpapi.NewHandler(plane, httpapi.WithClock(func() time.Time {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.now
	}))
	if err != nil {
		t.Fatal(err)
	}
	s.handler = handler
	return s
}

func (s *statusAPI) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *statusAPI) post(path, body string) *httptest.ResponseRecorder {
	s.t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, request)
	return recorder
}

func (s *statusAPI) get(path string) *httptest.ResponseRecorder {
	s.t.Helper()
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

const statusReportBody = `{
  "version": "1",
  "collector_id": "dev",
  "instance": "00000000000000aa",
  "sequence": "3",
  "uptime_ms": "90000",
  "receiver_endpoints": ["127.0.0.1:4318"],
  "evaluation_run_id": "run-1",
  "spans": {"received": "412", "evaluated": "398", "invalid": "14", "analyze_errors": "0"},
  "producers": [{
    "service_name": "support-agent",
    "spans": "398",
    "last_seen_age_ms": "1500",
    "scopes": [{"name": "openinference.instrumentation.openai", "version": "0.1.22"}],
    "scopes_truncated": false,
    "sdk": {"name": "opentelemetry", "language": "python", "version": "1.27.0"}
  }],
  "producers_truncated": false
}`

// TestStatusDocumentGolden pins the whole document for one report, byte for
// byte: every interface renders this, so a change to it is a contract change.
func TestStatusDocumentGolden(t *testing.T) {
	s := newStatusAPI(t)

	empty := s.get("/v1/status")
	if empty.Code != http.StatusOK {
		t.Fatalf("GET /v1/status = %d", empty.Code)
	}
	const wantEmpty = `{"version":"1","read_at":"2026-10-06T09:00:00Z","held_since":"2026-10-06T09:00:00Z",` +
		`"collectors":[],"engine":{"state":"unavailable","reason":"the engine exposes no statistics accessor; ` +
		`baseline count, maturity and fingerprint admission against the 512 bound are not available (task 105)"},` +
		`"bounds":{"collectors":"16","producers_per_collector":"64","scopes_per_producer":"16",` +
		`"receiver_endpoints_per_collector":"4","fresh_window_seconds":"30","expiry_window_seconds":"300"}}`
	if got := strings.TrimSpace(empty.Body.String()); got != wantEmpty {
		t.Fatalf("empty document:\n got %s\nwant %s", got, wantEmpty)
	}

	s.advance(10 * time.Second)
	accepted := s.post("/v1/collectors/dev/status", statusReportBody)
	if accepted.Code != http.StatusOK ||
		strings.TrimSpace(accepted.Body.String()) != `{"version":"1","disposition":"accepted"}` {
		t.Fatalf("POST = %d %s", accepted.Code, accepted.Body.String())
	}

	s.advance(5 * time.Second)
	got := strings.TrimSpace(s.get("/v1/status").Body.String())
	const want = `{"version":"1","read_at":"2026-10-06T09:00:15Z","held_since":"2026-10-06T09:00:00Z",` +
		`"collectors":[{"collector_id":"dev","instance":"00000000000000aa","state":"reporting",` +
		`"last_report_at":"2026-10-06T09:00:10Z","started_at":"2026-10-06T08:58:40Z",` +
		`"receiver_endpoints":["127.0.0.1:4318"],"evaluation_run_id":"run-1",` +
		`"spans":{"received":"412","evaluated":"398","invalid":"14","analyze_errors":"0"},` +
		`"producers":[{"service_name":"support-agent","spans":"398","last_seen_at":"2026-10-06T09:00:08.5Z",` +
		`"scopes":[{"name":"openinference.instrumentation.openai","version":"0.1.22"}],"scopes_truncated":false,` +
		`"sdk":{"name":"opentelemetry","language":"python","version":"1.27.0"}}],"producers_truncated":false}],` +
		`"engine":{"state":"unavailable","reason":"the engine exposes no statistics accessor; ` +
		`baseline count, maturity and fingerprint admission against the 512 bound are not available (task 105)"},` +
		`"bounds":{"collectors":"16","producers_per_collector":"64","scopes_per_producer":"16",` +
		`"receiver_endpoints_per_collector":"4","fresh_window_seconds":"30","expiry_window_seconds":"300"}}`
	if got != want {
		t.Fatalf("document:\n got %s\nwant %s", got, want)
	}

	s.advance(time.Minute)
	if !strings.Contains(s.get("/v1/status").Body.String(), `"state":"stale"`) {
		t.Fatal("a collector silent past the fresh window is not stale")
	}
}

func TestStatusReportRefusals(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		ctype  string
		status int
		code   string
	}{
		{"route and body disagree", "/v1/collectors/other/status", statusReportBody, "application/json",
			400, "invalid_request"},
		{"unsupported version", "/v1/collectors/dev/status",
			strings.Replace(statusReportBody, `"version": "1"`, `"version": "2"`, 1), "application/json",
			400, "unsupported_version"},
		{"non-canonical counter", "/v1/collectors/dev/status",
			strings.Replace(statusReportBody, `"received": "412"`, `"received": "0412"`, 1), "application/json",
			400, "invalid_request"},
		{"counter is a JSON number", "/v1/collectors/dev/status",
			strings.Replace(statusReportBody, `"received": "412"`, `"received": 412`, 1), "application/json",
			400, "invalid_request"},
		{"invalid service name", "/v1/collectors/dev/status",
			strings.Replace(statusReportBody, `"support-agent"`, `"bad\u001bname"`, 1), "application/json",
			400, "invalid_request"},
		{"not JSON", "/v1/collectors/dev/status", "{", "application/json", 400, "invalid_request"},
		{"wrong content type", "/v1/collectors/dev/status", statusReportBody, "text/plain",
			415, "unsupported_media_type"},
		{"oversized body", "/v1/collectors/dev/status",
			strings.Replace(statusReportBody, `"evaluation_run_id": "run-1"`,
				`"evaluation_run_id": "run-1", "padding": "`+strings.Repeat("x", 70<<10)+`"`, 1),
			"application/json", 413, "payload_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStatusAPI(t)
			request := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			request.Header.Set("Content-Type", tt.ctype)
			recorder := httptest.NewRecorder()
			s.handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.status {
				t.Fatalf("status %d, want %d: %s", recorder.Code, tt.status, recorder.Body.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != tt.code {
				t.Fatalf("error code %q, want %q: %s", envelope.Error.Code, tt.code, recorder.Body.String())
			}
			if !strings.Contains(s.get("/v1/status").Body.String(), `"collectors":[]`) {
				t.Fatal("a refused report was retained")
			}
		})
	}
}

// TestStatusReportToleratesUnknownSections is the forward-compatibility rule:
// a newer Collector's report is heard on everything this build understands.
func TestStatusReportToleratesUnknownSections(t *testing.T) {
	s := newStatusAPI(t)
	body := strings.Replace(statusReportBody, `"producers_truncated": false`,
		`"producers_truncated": false, "a_future_section": {"anything": ["at", "all"]}`, 1)
	if r := s.post("/v1/collectors/dev/status", body); r.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", r.Code, r.Body.String())
	}
	document := s.get("/v1/status").Body.String()
	if !strings.Contains(document, `"collector_id":"dev"`) || strings.Contains(document, "a_future_section") {
		t.Fatalf("document: %s", document)
	}
}

func TestStatusIgnoresALateOlderReport(t *testing.T) {
	s := newStatusAPI(t)
	if r := s.post("/v1/collectors/dev/status", statusReportBody); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	older := strings.Replace(statusReportBody, `"sequence": "3"`, `"sequence": "2"`, 1)
	older = strings.Replace(older, `"received": "412"`, `"received": "500"`, 1)
	r := s.post("/v1/collectors/dev/status", older)
	if !bytes.Contains(r.Body.Bytes(), []byte(`"disposition":"ignored"`)) {
		t.Fatalf("late report: %s", r.Body.String())
	}
	if !strings.Contains(s.get("/v1/status").Body.String(), `"received":"412"`) {
		t.Fatal("a late report rolled the counters back")
	}
}
