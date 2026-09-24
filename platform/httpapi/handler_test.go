package httpapi_test

// Task 058: the HTTP adapter.
//
// Two things are under test here and nothing else. That the transport
// translates faithfully — status codes, error codes, canonical numbers, the
// strict envelope around a lenient record — and that it decides nothing: the
// control plane owns every rule, and these tests exist partly to make a
// handler that started computing something fail loudly.
//
// The integration test at the end is the one that matters most: two real
// engines, records crossing the public boundary as JSON, and a gate verdict
// coming back out.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
	"trustvian-platform/httpapi"

	_ "modernc.org/sqlite" // direct SQL for corruption fixtures
)

var testEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	testProfile     = "profile-1"
	testEnvironment = "staging"
)

type api struct {
	t      *testing.T
	server http.Handler
	store  *platform.SQLiteStore
}

func newAPI(t *testing.T) *api {
	t.Helper()
	return openAPI(t, filepath.Join(t.TempDir(), "platform.db"))
}

func openAPI(t *testing.T, path string) *api {
	t.Helper()
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	// A fixed clock: lifecycle timestamps must not make a test flaky, and the
	// clock is not allowed to influence anything else.
	handler, err := httpapi.NewHandler(plane, httpapi.WithClock(func() time.Time { return testEpoch }))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return &api{t: t, server: handler, store: store}
}

// do issues a request with the default JSON content type.
func (a *api) do(method, path string, body any) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.doRaw(method, path, encodeBody(a.t, body), "application/json")
}

func (a *api) doRaw(method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	a.t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	a.server.ServeHTTP(recorder, request)
	return recorder
}

func encodeBody(t *testing.T, body any) []byte {
	t.Helper()
	if body == nil {
		return []byte("{}")
	}
	if raw, ok := body.([]byte); ok {
		return raw
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return encoded
}

func (a *api) mustStatus(r *httptest.ResponseRecorder, want int, context string) {
	a.t.Helper()
	if r.Code != want {
		a.t.Fatalf("%s: status = %d, want %d; body = %s", context, r.Code, want, r.Body.String())
	}
}

// errorCode reads the error envelope's stable code.
func errorCode(t *testing.T, r *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Version string `json:"version"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("error body is not an envelope: %v (%s)", err, r.Body.String())
	}
	if envelope.Version != httpapi.WireVersion {
		t.Errorf("error envelope version = %q, want %q", envelope.Version, httpapi.WireVersion)
	}
	return envelope.Error.Code
}

// seed builds the hierarchy and returns a running run.
func (a *api) seedRunning(runID string) {
	a.t.Helper()
	a.seedPending(runID)
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")
}

func (a *api) seedPending(runID string) {
	a.t.Helper()
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
}

func (a *api) seedHierarchy() {
	a.t.Helper()
	if r := a.do("POST", "/v1/projects", map[string]string{"id": "proj-1", "name": "Checkout"}); r.Code == 201 {
		a.mustStatus(a.do("POST", "/v1/agents", map[string]string{
			"id": "agent-1", "project_id": "proj-1", "name": "Deploy agent",
		}), 201, "create agent")
		a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
			"id": "cand-1", "agent_id": "agent-1", "metadata": map[string]string{"label": "v1"},
		}), 201, "create candidate")
		// Task 065: a run must name an environment its project owns.
		a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
			"project_id": "proj-1", "ref": testEnvironment, "name": "Staging",
		}), 201, "create environment")
	}
}

// envelope builds a valid ingest envelope.
func envelope(sequence uint64, record trustvian.DecisionRecord) map[string]any {
	return map[string]any{
		"version":            httpapi.WireVersion,
		"sequence":           platform.FormatSequence(sequence),
		"behavioral_profile": testProfile,
		"record":             record,
	}
}

func apiRecord(eventID, fingerprintID, operation string) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:     eventID,
		Timestamp:   testEpoch,
		ActorID:     "agent-1",
		ActorType:   event.ActorTypeAIAgent,
		Environment: testEnvironment,
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeAIAgent,
			OperationCategory: event.OperationCategoryTool,
			OperationName:     operation,
			TargetName:        "build-host",
			TargetCategory:    event.TargetCategoryExternal,
			Environment:       testEnvironment,
		},
		FingerprintID:      fingerprintID,
		IdentityConfidence: 0.9,
		AnomalyScore:       0.2,
		AnomalyConfidence:  0.5,
		TrustScore:         0.8,
		ContextRisk:        0.1,
		RiskLevel:          "low",
		Decision:           "allow",
		PolicyReason:       "default allow",
		MatchedDefault:     true,
	}
}

// ---------------------------------------------------------------------
// Routing and entity round-trips
// ---------------------------------------------------------------------

func TestControlEntityRoundTrip(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	r := a.do("GET", "/v1/projects/proj-1", nil)
	a.mustStatus(r, 200, "get project")
	var project struct {
		Version string `json:"version"`
		ID      string `json:"id"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if project.ID != "proj-1" || project.Name != "Checkout" || project.Version != httpapi.WireVersion {
		t.Errorf("project = %+v", project)
	}

	a.mustStatus(a.do("GET", "/v1/agents/agent-1", nil), 200, "get agent")
	a.mustStatus(a.do("GET", "/v1/candidates/cand-1", nil), 200, "get candidate")
}

// No collection GET exists, so list semantics are not frozen by accident.
// Task 065 added exactly one collection route, for one entity. The others
// stay absent: their scope, order, cursor and limit are still undesigned, and
// task 065 neither performs nor pre-empts that design.
func TestListRoutesAreAbsentExceptEnvironments(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	for _, path := range []string{
		"/v1/projects", "/v1/agents", "/v1/candidates", "/v1/evaluation-runs",
		"/v1/environments",
	} {
		r := a.do("GET", path, nil)
		if r.Code == http.StatusOK {
			t.Errorf("GET %s returned 200; listing semantics are not designed yet", path)
		}
	}

	// The one that does exist is project-scoped, because a ref is unique
	// inside a project and nowhere else.
	a.mustStatus(a.do("GET", "/v1/projects/proj-1/environments", nil), 200, "list environments")
}

func TestMissingResourceIsNotFound(t *testing.T) {
	a := newAPI(t)
	for _, path := range []string{
		"/v1/projects/nope", "/v1/agents/nope", "/v1/candidates/nope",
		"/v1/evaluation-runs/nope", "/v1/evaluation-runs/nope/progress",
	} {
		r := a.do("GET", path, nil)
		a.mustStatus(r, 404, "GET "+path)
		if code := errorCode(t, r); code != "not_found" {
			t.Errorf("GET %s code = %q, want not_found", path, code)
		}
	}
}

func TestDuplicateCreateConflicts(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	r := a.do("POST", "/v1/projects", map[string]string{"id": "proj-1", "name": "Different"})
	a.mustStatus(r, 409, "duplicate project")
	if code := errorCode(t, r); code != "already_exists" {
		t.Errorf("code = %q, want already_exists", code)
	}
}

func TestLifecycleConflict(t *testing.T) {
	a := newAPI(t)
	a.seedPending("run-1")

	// Completing a run that never started.
	r := a.do("POST", "/v1/evaluation-runs/run-1/complete", nil)
	a.mustStatus(r, 409, "complete pending")
	if code := errorCode(t, r); code != "conflict" {
		t.Errorf("code = %q, want conflict", code)
	}
}

// ---------------------------------------------------------------------
// Request validation
// ---------------------------------------------------------------------

func TestMalformedAndUnsupportedRequests(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	record := apiRecord("evt-0", "fp-0", "read")

	tests := []struct {
		name        string
		body        []byte
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{
			name: "malformed JSON", body: []byte("{not json"),
			contentType: "application/json", wantStatus: 400, wantCode: "invalid_request",
		},
		{
			name:        "wrong media type",
			body:        encodeBody(t, envelope(1, record)),
			contentType: "text/plain", wantStatus: 415, wantCode: "unsupported_media_type",
		},
		{
			name:        "missing media type",
			body:        encodeBody(t, envelope(1, record)),
			contentType: "", wantStatus: 415, wantCode: "unsupported_media_type",
		},
		{
			name: "unsupported envelope version",
			body: encodeBody(t, map[string]any{
				"version": "2", "sequence": "1",
				"behavioral_profile": testProfile, "record": record,
			}),
			contentType: "application/json", wantStatus: 400, wantCode: "unsupported_version",
		},
		{
			name: "unknown envelope field",
			body: encodeBody(t, map[string]any{
				"version": "1", "sequence": "1", "behavioral_profile": testProfile,
				"record": record, "surprise": true,
			}),
			contentType: "application/json", wantStatus: 400, wantCode: "invalid_request",
		},
		{
			name: "non-canonical sequence",
			body: encodeBody(t, map[string]any{
				"version": "1", "sequence": "01",
				"behavioral_profile": testProfile, "record": record,
			}),
			contentType: "application/json", wantStatus: 400, wantCode: "invalid_request",
		},
		{
			name:        "sequence as a number",
			body:        []byte(`{"version":"1","sequence":1,"behavioral_profile":"profile-1","record":{}}`),
			contentType: "application/json", wantStatus: 400, wantCode: "invalid_request",
		},
		{
			name: "missing record",
			body: encodeBody(t, map[string]any{
				"version": "1", "sequence": "1", "behavioral_profile": testProfile,
			}),
			contentType: "application/json", wantStatus: 400, wantCode: "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := a.doRaw("POST", "/v1/evaluation-runs/run-1/records", tt.body, tt.contentType)
			a.mustStatus(r, tt.wantStatus, tt.name)
			if code := errorCode(t, r); code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
		})
	}
}

// Charset and other parameters are allowed: the media type is what matters.
func TestContentTypeParametersAreAccepted(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	r := a.doRaw("POST", "/v1/evaluation-runs/run-1/records",
		encodeBody(t, envelope(1, apiRecord("evt-0", "fp-0", "read"))),
		"application/json; charset=utf-8")
	a.mustStatus(r, 200, "charset parameter")
}

// DecisionRecord is an additive stable contract: a newer core adding a field
// must not make this server reject records it otherwise understands.
func TestAdditiveUnknownFieldInsideRecordIsTolerated(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	record := apiRecord("evt-0", "fp-0", "read")
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(encoded, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	asMap["field_from_a_future_core"] = "value"

	body := encodeBody(t, map[string]any{
		"version": "1", "sequence": "1",
		"behavioral_profile": testProfile, "record": asMap,
	})
	r := a.doRaw("POST", "/v1/evaluation-runs/run-1/records", body, "application/json")
	a.mustStatus(r, 200, "additive record field")
}

// The body cap is applied before decoding, so it protects the memory it is
// meant to protect.
func TestRequestBodyLimit(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	const limit = 256 << 10

	// Pad a string field until the encoded envelope is exactly `size` bytes.
	// Padding inside the JSON keeps the request structurally valid, so the
	// only thing under test is the size gate.
	build := func(size int) []byte {
		record := apiRecord("evt-0", "fp-0", "read")
		record.PolicyReason = ""
		body := encodeBody(t, envelope(1, record))
		if len(body) > size {
			t.Fatalf("base envelope is %d bytes, larger than %d", len(body), size)
		}
		record.PolicyReason = strings.Repeat("x", size-len(body))
		body = encodeBody(t, envelope(1, record))
		if len(body) != size {
			t.Fatalf("padding produced %d bytes, want %d", len(body), size)
		}
		return body
	}

	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		r := a.doRaw("POST", "/v1/evaluation-runs/run-1/records", build(limit), "application/json")
		if r.Code == http.StatusRequestEntityTooLarge {
			t.Errorf("a body of exactly %d bytes was rejected as too large", limit)
		}
	})

	t.Run("one byte over is refused", func(t *testing.T) {
		r := a.doRaw("POST", "/v1/evaluation-runs/run-1/records", build(limit+1), "application/json")
		a.mustStatus(r, 413, "one byte over the limit")
		if code := errorCode(t, r); code != "payload_too_large" {
			t.Errorf("code = %q, want payload_too_large", code)
		}
	})
}

// ---------------------------------------------------------------------
// Ingest over HTTP
// ---------------------------------------------------------------------

type ingestBody struct {
	Version          string `json:"version"`
	Disposition      string `json:"disposition"`
	NextSequence     string `json:"next_sequence"`
	RecordCount      string `json:"record_count"`
	BehaviorComplete bool   `json:"behavior_complete"`
}

func decodeIngest(t *testing.T, r *httptest.ResponseRecorder) ingestBody {
	t.Helper()
	var body ingestBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ingest response: %v (%s)", err, r.Body.String())
	}
	return body
}

func TestIngestAppliedReplayedAndConflict(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	record := apiRecord("evt-0", "fp-0", "read")

	t.Run("applied", func(t *testing.T) {
		r := a.do("POST", "/v1/evaluation-runs/run-1/records", envelope(1, record))
		a.mustStatus(r, 200, "first ingest")
		body := decodeIngest(t, r)
		if body.Disposition != "applied" || body.RecordCount != "1" || body.NextSequence != "2" {
			t.Errorf("body = %+v", body)
		}
		if !body.BehaviorComplete {
			t.Error("BehaviorComplete = false on a fresh run")
		}
	})

	t.Run("replayed", func(t *testing.T) {
		r := a.do("POST", "/v1/evaluation-runs/run-1/records", envelope(1, record))
		a.mustStatus(r, 200, "retry")
		body := decodeIngest(t, r)
		if body.Disposition != "replayed" {
			t.Errorf("Disposition = %q, want replayed", body.Disposition)
		}
		if body.RecordCount != "1" {
			t.Errorf("RecordCount = %q, want 1: the retry counted twice", body.RecordCount)
		}
	})

	t.Run("same sequence different record conflicts", func(t *testing.T) {
		r := a.do("POST", "/v1/evaluation-runs/run-1/records",
			envelope(1, apiRecord("evt-other", "fp-1", "write")))
		a.mustStatus(r, 409, "divergent retry")
		if code := errorCode(t, r); code != "conflict" {
			t.Errorf("code = %q, want conflict", code)
		}
	})

	t.Run("gap conflicts", func(t *testing.T) {
		r := a.do("POST", "/v1/evaluation-runs/run-1/records",
			envelope(9, apiRecord("evt-gap", "fp-gap", "gap")))
		a.mustStatus(r, 409, "gap")
		if code := errorCode(t, r); code != "conflict" {
			t.Errorf("code = %q, want conflict", code)
		}
	})
}

func TestIngestProfileMismatchIsRejected(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	body := map[string]any{
		"version": "1", "sequence": "1",
		"behavioral_profile": "a-different-profile",
		"record":             apiRecord("evt-0", "fp-0", "read"),
	}
	r := a.do("POST", "/v1/evaluation-runs/run-1/records", body)
	if r.Code == http.StatusOK {
		t.Fatal("a record from another profile was accepted")
	}
}

func TestIngestStateAndProgressEndpoints(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, apiRecord("evt-0", "fp-0", "read"))), 200, "ingest")

	r := a.do("GET", "/v1/evaluation-runs/run-1/ingest-state", nil)
	a.mustStatus(r, 200, "ingest state")
	var state struct {
		NextSequence string `json:"next_sequence"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.NextSequence != "2" {
		t.Errorf("NextSequence = %q, want 2", state.NextSequence)
	}

	r = a.do("GET", "/v1/evaluation-runs/run-1/progress", nil)
	a.mustStatus(r, 200, "progress")
	var progress map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &progress); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if progress["record_count"] != "1" || progress["status"] != "running" {
		t.Errorf("progress = %+v", progress)
	}
	// Progress reports facts, never a verdict.
	for _, forbidden := range []string{"passed", "verdict", "promotable", "safe", "gate"} {
		if _, present := progress[forbidden]; present {
			t.Errorf("progress exposes %q; a running evaluation has progress, not a verdict", forbidden)
		}
	}
}

// ---------------------------------------------------------------------
// Comparison over HTTP
// ---------------------------------------------------------------------

func (a *api) completeRun(runID, candidateID string, operations []string) {
	a.t.Helper()
	a.seedHierarchy()
	if candidateID != "cand-1" {
		a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
			"id": candidateID, "agent_id": "agent-1", "metadata": map[string]string{"label": "v2"},
		}), 201, "create candidate")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": candidateID,
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")

	for i, op := range operations {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records",
			envelope(uint64(i+1), apiRecord(fmt.Sprintf("%s-evt-%d", runID, i), "fp-"+op, op))),
			200, "ingest")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
}

func limitsBody(added, block, critical string) map[string]any {
	return map[string]any{
		"max_added_behaviors":            added,
		"max_block_decisions":            block,
		"max_critical_risk_observations": critical,
	}
}

func TestCompareOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-ref", "cand-1", []string{"read", "list"})
	a.completeRun("run-can", "cand-2", []string{"read", "list", "write"})

	maxUint := platform.FormatSequence(math.MaxUint64)

	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-can",
		"gate_limits":      limitsBody("0", maxUint, maxUint),
	})
	a.mustStatus(r, 200, "compare strict")

	var response struct {
		Version string `json:"version"`
		Diff    struct {
			AddedCount   int `json:"added_count"`
			RemovedCount int `json:"removed_count"`
			SharedCount  int `json:"shared_count"`
		} `json:"behavior_diff"`
		Gate struct {
			Verdict        string `json:"verdict"`
			AddedBehaviors struct {
				Actual string `json:"actual"`
				Passed bool   `json:"passed"`
			} `json:"added_behaviors"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Diff.AddedCount != 1 || response.Diff.SharedCount != 2 {
		t.Errorf("diff = %+v", response.Diff)
	}
	if response.Gate.Verdict != "fail" || response.Gate.AddedBehaviors.Passed {
		t.Errorf("gate = %+v", response.Gate)
	}

	// Same evidence, one different limit.
	r = a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-can",
		"gate_limits":      limitsBody("1", maxUint, maxUint),
	})
	a.mustStatus(r, 200, "compare relaxed")
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Gate.Verdict != "pass" {
		t.Errorf("Verdict = %q, want pass", response.Gate.Verdict)
	}
}

// Zero is a strict limit, so an omitted one cannot silently become zero.
func TestOmittedGateLimitIsNotZero(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-ref", "cand-1", []string{"read"})
	a.completeRun("run-can", "cand-2", []string{"read"})

	maxUint := platform.FormatSequence(math.MaxUint64)

	for _, missing := range []string{
		"max_added_behaviors", "max_block_decisions", "max_critical_risk_observations",
	} {
		t.Run("missing "+missing, func(t *testing.T) {
			limits := limitsBody(maxUint, maxUint, maxUint)
			delete(limits, missing)

			r := a.do("POST", "/v1/evaluations/compare", map[string]any{
				"reference_run_id": "run-ref",
				"candidate_run_id": "run-can",
				"gate_limits":      limits,
			})
			a.mustStatus(r, 400, "missing "+missing)
			if code := errorCode(t, r); code != "invalid_request" {
				t.Errorf("code = %q, want invalid_request", code)
			}
		})
	}

	// An explicit zero is accepted and strict.
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-can",
		"gate_limits":      limitsBody("0", "0", "0"),
	})
	a.mustStatus(r, 200, "explicit zero limits")
}

func TestCompareBeforeCompletionConflicts(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-ref", "cand-1", []string{"read"})
	a.seedPending("run-open")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-open/start", nil), 200, "start")

	maxUint := platform.FormatSequence(math.MaxUint64)
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-open",
		"gate_limits":      limitsBody(maxUint, maxUint, maxUint),
	})
	a.mustStatus(r, 409, "compare running run")
	if code := errorCode(t, r); code != "conflict" {
		t.Errorf("code = %q, want conflict", code)
	}
}

// Incomplete evidence is a measurement that did not finish — not a failed
// policy and not an unsafe candidate.
func TestCompareRefusesIncompleteEvidenceWithItsOwnCode(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-ref", "cand-1", []string{"read"})

	// Saturate the candidate's behavioral evidence.
	a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
		"id": "cand-sat", "agent_id": "agent-1", "metadata": map[string]string{"label": "sat"},
	}), 201, "create candidate")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-sat", "candidate_id": "cand-sat",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-sat/start", nil), 200, "start")

	for i := range 513 {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-sat/records",
			envelope(uint64(i+1), apiRecord(
				fmt.Sprintf("s-evt-%04d", i), fmt.Sprintf("s-fp-%04d", i), fmt.Sprintf("s-op-%04d", i)))),
			200, "ingest")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-sat/complete", nil), 200, "complete")

	maxUint := platform.FormatSequence(math.MaxUint64)
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-sat",
		"gate_limits":      limitsBody(maxUint, maxUint, maxUint),
	})
	a.mustStatus(r, 409, "incomplete evidence")
	if code := errorCode(t, r); code != "incomplete_evidence" {
		t.Errorf("code = %q, want incomplete_evidence", code)
	}
	// It must not be dressed up as a policy failure.
	body := strings.ToLower(r.Body.String())
	for _, forbidden := range []string{"unsafe", "failed policy", "violation"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response calls incomplete evidence %q", forbidden)
		}
	}
}

// ---------------------------------------------------------------------
// Error sanitization
// ---------------------------------------------------------------------

// Storage damage is a server failure, and nothing about the storage layer
// reaches the client.
func TestStorageCorruptionIsSanitized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	a := openAPI(t, path)
	a.completeRun("run-ref", "cand-1", []string{"read"})

	// Corrupt the run row directly.
	if err := corruptRunStatus(t, path, "run-ref"); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	reopened := openAPI(t, path)
	r := reopened.do("GET", "/v1/evaluation-runs/run-ref", nil)
	reopened.mustStatus(r, 500, "corrupt run")
	if code := errorCode(t, r); code != "internal" {
		t.Errorf("code = %q, want internal", code)
	}

	body := strings.ToLower(r.Body.String())
	for _, leak := range []string{
		"select", "insert", "update", "sqlite", "platform_", ".db", "/tmp", "goroutine",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("error body leaks %q: %s", leak, r.Body.String())
		}
	}
}

// corruptRunStatus writes a status no domain transition can produce, so the
// next read fails as corruption rather than as a client error.
func corruptRunStatus(t *testing.T, path, runID string) error {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE platform_evaluation_runs SET status = 'teleported' WHERE id = ?`, runID)
	return err
}

// ---------------------------------------------------------------------
// Real Engine → HTTP → comparison
// ---------------------------------------------------------------------

// The proof the whole chain holds across the public boundary:
//
//	Engine.Analyze → Result.DecisionRecord() → versioned HTTP ingest →
//	control plane → SQLite → diff → scorecard → gate → HTTP response
//
// No hand-built DecisionRecord, and no hard-coded fingerprint: the engine
// decides what a behavior is, and the comparison has to agree with it.
func TestRealEngineThroughHTTPToGateVerdict(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	// drive runs one evaluation end to end using a real engine.
	drive := func(runID, candidateID, profile string, operations []string) {
		t.Helper()
		if candidateID != "cand-1" {
			a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
				"id": candidateID, "agent_id": "agent-1",
				"metadata": map[string]string{"label": candidateID},
			}), 201, "create candidate")
		}
		a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
			"id": runID, "candidate_id": candidateID,
			"environment": testEnvironment, "behavioral_profile": profile,
		}), 201, "create run")
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")

		engine := trustvian.NewEngine(trustvian.WithLearningScope(profile))
		clock := testEpoch
		for i, operation := range operations {
			clock = clock.Add(90 * time.Second)
			result, err := engine.Analyze(t.Context(), event.Event{
				ID:        fmt.Sprintf("%s-evt-%d", runID, i),
				Timestamp: clock,
				Actor: event.Actor{
					ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9,
				},
				Operation: event.Operation{Category: event.OperationCategoryTool, Name: operation},
				Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
				Context:   event.Context{Environment: testEnvironment},
			})
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}

			// The detached public record is what crosses the boundary.
			body := map[string]any{
				"version":            httpapi.WireVersion,
				"sequence":           platform.FormatSequence(uint64(i + 1)),
				"behavioral_profile": profile,
				"record":             result.DecisionRecord(),
			}
			a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records", body), 200,
				fmt.Sprintf("ingest %s/%d", runID, i+1))
		}
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
	}

	drive("run-ref", "cand-1", "profile-ref", []string{"shell.read", "db.query"})
	drive("run-can", "cand-can", "profile-can", []string{"shell.read", "shell.execute"})

	maxUint := platform.FormatSequence(math.MaxUint64)
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-can",
		"gate_limits":      limitsBody("0", maxUint, maxUint),
	})
	a.mustStatus(r, 200, "compare")

	var response struct {
		Diff struct {
			AddedCount   int `json:"added_count"`
			RemovedCount int `json:"removed_count"`
			SharedCount  int `json:"shared_count"`
			Deltas       []struct {
				Change   string `json:"change"`
				Behavior struct {
					OperationName string `json:"operation_name"`
				} `json:"behavior"`
			} `json:"deltas"`
		} `json:"behavior_diff"`
		Scorecard struct {
			ReferenceRunID string `json:"reference_run_id"`
			CandidateRunID string `json:"candidate_run_id"`
			Environment    string `json:"environment"`
		} `json:"scorecard"`
		Gate struct {
			Verdict        string `json:"verdict"`
			AddedBehaviors struct {
				Actual  string `json:"actual"`
				Maximum string `json:"maximum"`
				Passed  bool   `json:"passed"`
			} `json:"added_behaviors"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}

	t.Run("behavioral presence matches what the engines saw", func(t *testing.T) {
		if response.Diff.SharedCount != 1 {
			t.Errorf("SharedCount = %d, want 1 (shell.read)", response.Diff.SharedCount)
		}
		if response.Diff.AddedCount != 1 {
			t.Errorf("AddedCount = %d, want 1 (shell.execute)", response.Diff.AddedCount)
		}
		if response.Diff.RemovedCount != 1 {
			t.Errorf("RemovedCount = %d, want 1 (db.query)", response.Diff.RemovedCount)
		}

		byOperation := map[string]string{}
		for _, delta := range response.Diff.Deltas {
			byOperation[delta.Behavior.OperationName] = delta.Change
		}
		for operation, want := range map[string]string{
			"shell.read":    "shared",
			"db.query":      "removed",
			"shell.execute": "added",
		} {
			if got := byOperation[operation]; got != want {
				t.Errorf("%s = %q, want %q", operation, got, want)
			}
		}
	})

	t.Run("scorecard carries the comparison identity", func(t *testing.T) {
		if response.Scorecard.ReferenceRunID != "run-ref" ||
			response.Scorecard.CandidateRunID != "run-can" ||
			response.Scorecard.Environment != testEnvironment {
			t.Errorf("scorecard identity = %+v", response.Scorecard)
		}
	})

	t.Run("the gate reflects exactly the supplied limits", func(t *testing.T) {
		if response.Gate.AddedBehaviors.Actual != "1" || response.Gate.AddedBehaviors.Maximum != "0" {
			t.Errorf("added-behaviors gate = %+v", response.Gate.AddedBehaviors)
		}
		if response.Gate.AddedBehaviors.Passed {
			t.Error("added-behaviors passed with one added behavior and a limit of zero")
		}
		if response.Gate.Verdict != "fail" {
			t.Errorf("Verdict = %q, want fail", response.Gate.Verdict)
		}
	})

	t.Run("the same evidence passes under a higher limit", func(t *testing.T) {
		relaxed := a.do("POST", "/v1/evaluations/compare", map[string]any{
			"reference_run_id": "run-ref",
			"candidate_run_id": "run-can",
			"gate_limits":      limitsBody("1", maxUint, maxUint),
		})
		a.mustStatus(relaxed, 200, "relaxed compare")
		var body struct {
			Gate struct {
				Verdict string `json:"verdict"`
			} `json:"gate"`
		}
		if err := json.Unmarshal(relaxed.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Gate.Verdict != "pass" {
			t.Errorf("Verdict = %q, want pass", body.Gate.Verdict)
		}
	})
}

// A comparison response must be semantically deterministic: no request id, no
// random value, no server timestamp.
func TestCompareResponseIsDeterministic(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-ref", "cand-1", []string{"read", "list"})
	a.completeRun("run-can", "cand-2", []string{"read", "write"})

	maxUint := platform.FormatSequence(math.MaxUint64)
	request := map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-can",
		"gate_limits":      limitsBody("5", maxUint, maxUint),
	}

	first := a.do("POST", "/v1/evaluations/compare", request).Body.String()
	for range 5 {
		if got := a.do("POST", "/v1/evaluations/compare", request).Body.String(); got != first {
			t.Fatalf("comparison response differed between identical requests:\n first = %s\n got   = %s",
				first, got)
		}
	}
}

// ---------------------------------------------------------------------
// Zero-record evaluations over HTTP
// ---------------------------------------------------------------------

// completeEmptyRun drives a run to Completed without posting any record.
func (a *api) completeEmptyRun(runID, candidateID string) {
	a.t.Helper()
	a.seedHierarchy()
	if candidateID != "cand-1" {
		a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
			"id": candidateID, "agent_id": "agent-1", "metadata": map[string]string{"label": candidateID},
		}), 201, "create candidate")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": candidateID,
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
}

type compareGateBody struct {
	Gate struct {
		Verdict           string `json:"verdict"`
		ReferenceEvidence struct {
			Actual  string `json:"actual"`
			Minimum string `json:"minimum"`
			Passed  bool   `json:"passed"`
		} `json:"reference_evidence"`
		CandidateEvidence struct {
			Actual  string `json:"actual"`
			Minimum string `json:"minimum"`
			Passed  bool   `json:"passed"`
		} `json:"candidate_evidence"`
	} `json:"gate"`
	Diff struct {
		AddedCount   int `json:"added_count"`
		RemovedCount int `json:"removed_count"`
		SharedCount  int `json:"shared_count"`
	} `json:"behavior_diff"`
}

// An evaluation that ran nothing is a valid comparison with a FAIL verdict,
// not a missing resource. Returning 404 would hide the very candidate task
// 056's mandatory evidence gates exist to catch.
func TestCompareTwoEmptyEvaluationsReturnsFailNotNotFound(t *testing.T) {
	a := newAPI(t)
	a.completeEmptyRun("run-ref", "cand-1")
	a.completeEmptyRun("run-can", "cand-2")

	maxUint := platform.FormatSequence(math.MaxUint64)
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-can",
		"gate_limits":      limitsBody(maxUint, maxUint, maxUint),
	})
	a.mustStatus(r, 200, "compare two empty evaluations")

	var body compareGateBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Gate.Verdict != "fail" {
		t.Errorf("Verdict = %q, want fail", body.Gate.Verdict)
	}
	for _, g := range []struct {
		name    string
		actual  string
		minimum string
		passed  bool
	}{
		{"reference_evidence", body.Gate.ReferenceEvidence.Actual,
			body.Gate.ReferenceEvidence.Minimum, body.Gate.ReferenceEvidence.Passed},
		{"candidate_evidence", body.Gate.CandidateEvidence.Actual,
			body.Gate.CandidateEvidence.Minimum, body.Gate.CandidateEvidence.Passed},
	} {
		if g.actual != "0" || g.minimum != "1" || g.passed {
			t.Errorf("%s = actual %q, minimum %q, passed %v; want \"0\"/\"1\"/false",
				g.name, g.actual, g.minimum, g.passed)
		}
	}
	if body.Diff.AddedCount != 0 || body.Diff.RemovedCount != 0 || body.Diff.SharedCount != 0 {
		t.Errorf("diff = %+v, want zeros", body.Diff)
	}

	// It is a result, not an error: no error envelope, no invented code.
	if strings.Contains(r.Body.String(), `"error"`) {
		t.Errorf("empty comparison returned an error envelope: %s", r.Body.String())
	}
}

// Empty evidence participates in the diff over the wire too.
func TestCompareEmptyReferenceAgainstPopulatedCandidate(t *testing.T) {
	a := newAPI(t)
	a.completeEmptyRun("run-ref", "cand-1")
	a.completeRun("run-can", "cand-2", []string{"read"})

	maxUint := platform.FormatSequence(math.MaxUint64)
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-can",
		"gate_limits":      limitsBody(maxUint, maxUint, maxUint),
	})
	a.mustStatus(r, 200, "compare empty reference")

	var body compareGateBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Diff.AddedCount != 1 || body.Diff.RemovedCount != 0 || body.Diff.SharedCount != 0 {
		t.Errorf("diff = %+v, want added 1", body.Diff)
	}
	if body.Gate.ReferenceEvidence.Passed {
		t.Error("reference evidence passed with zero records")
	}
	if !body.Gate.CandidateEvidence.Passed {
		t.Error("candidate evidence failed with one record")
	}
	if body.Gate.Verdict != "fail" {
		t.Errorf("Verdict = %q, want fail", body.Gate.Verdict)
	}
}

// Progress on a zero-record run is the same factual empty state.
func TestProgressOfEmptyCompletedRun(t *testing.T) {
	a := newAPI(t)
	a.completeEmptyRun("run-1", "cand-1")

	r := a.do("GET", "/v1/evaluation-runs/run-1/progress", nil)
	a.mustStatus(r, 200, "progress")

	var progress map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &progress); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for field, want := range map[string]any{
		"record_count":               "0",
		"behavior_observation_count": "0",
		"next_ingest_sequence":       "1",
		"behavior_complete":          true,
		"status":                     "completed",
	} {
		if progress[field] != want {
			t.Errorf("%s = %v, want %v", field, progress[field], want)
		}
	}
	if count, ok := progress["distinct_behavior_count"].(float64); !ok || count != 0 {
		t.Errorf("distinct_behavior_count = %v, want 0", progress["distinct_behavior_count"])
	}
}
